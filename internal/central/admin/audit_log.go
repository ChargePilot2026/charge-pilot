package admin

import (
	"log"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// audit_log lives in admin_db. Resource data for coupons, refunds, invoices,
// settlements and casework lives in user_db, so a handler that changes one and
// then reuses its own transaction for the audit writes the audit into
// user_db.audit_log instead. The two rows are invisible to each other and the
// trail for a financial action ends up somewhere nobody looks.
//
// This helper is the only supported way for a user_db handler to record an
// action. It writes through AdminDB, after the business transaction has
// committed, because joining the two would be a cross-schema write and this
// project does not do those.
//
// Failure semantics, chosen deliberately: the business change is already
// durable when the audit runs, so failing the response would tell the operator
// an action did not happen when it did, and a retry of a refund or a payout is
// worse than a missing log line. A failed audit is therefore logged at error
// level with the full context and the request still succeeds. That trade is
// recorded in docs/migration/go-rebuild.md rather than left implicit.
func (a ResourceAPI) auditToAdmin(c *gin.Context, action, target string, id uint64, before, after any, requestID string) {
	err := resourceAudit(a.Store.AdminDB.WithContext(c.Request.Context()),
		c.MustGet("admin_profile").(Profile), action, target, id, before, after,
		c.ClientIP(), firstNonEmpty(requestID, httpapi.RequestID(c)))
	if err != nil {
		log.Printf("AUDIT WRITE FAILED action=%s target=%s id=%d actor=%v: %v",
			action, target, id, c.MustGet("admin_profile").(Profile).Username, err)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type auditRow struct {
	ID         uint64  `json:"id"`
	ActorID    uint64  `json:"actor_id"`
	ActorName  string  `json:"actor_name"`
	Module     string  `json:"module"`
	Action     string  `json:"action"`
	TargetType *string `json:"target_type"`
	TargetID   *string `json:"target_id"`
	RequestID  *string `json:"request_id"`
	ClientIP   *string `json:"client_ip"`
	// *string, not *[]byte: encoding/json renders []byte as base64, which would
	// hand the operator an unreadable blob instead of the change snapshot.
	BeforeJSON *string   `json:"before_json"`
	AfterJSON  *string   `json:"after_json"`
	CreatedAt  time.Time `json:"created_at"`
}

func (a ResourceAPI) registerAuditLogs(r *gin.Engine) {
	r.GET("/api/v1/admin/audit-logs", a.Auth.Require("audit.read"), a.listAuditLogs)
}

// listAuditLogs is the read side of the audit trail. Without it the trail is
// write-only: operators could act on a screen that recorded nothing they could
// later inspect.
func (a ResourceAPI) listAuditLogs(c *gin.Context) {
	page, ok := parsePage(c, "")
	if !ok {
		return
	}
	out := Page[auditRow]{Items: []auditRow{}, Page: page.Page, PageSize: page.PageSize}

	// The filters are collected first and the query is then built twice. Reusing
	// one sessioned statement for both count and list lets the count's
	// SELECT count(*) leak into the second statement, which silently drops the
	// column list.
	var (
		module, action, actor, target string
		from, to                      *time.Time
		badTime                       bool
	)
	module, action, actor, target = c.Query("module"), c.Query("action"), c.Query("actor"), c.Query("target_type")
	for _, spec := range []struct {
		raw  string
		into **time.Time
	}{{c.Query("from"), &from}, {c.Query("to"), &to}} {
		if spec.raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, spec.raw)
		if err != nil {
			badTime = true
			break
		}
		utc := parsed.UTC()
		*spec.into = &utc
	}
	if badTime {
		httpapi.BadRequest(c, "时间须为 RFC 3339 格式")
		return
	}

	filtered := func() *gorm.DB {
		q := a.Store.AdminDB.WithContext(c.Request.Context()).Table("audit_log")
		if module != "" {
			q = q.Where("module = ?", module)
		}
		if action != "" {
			q = q.Where("action = ?", action)
		}
		if actor != "" {
			q = q.Where("actor_name LIKE ?", likePattern(actor))
		}
		if target != "" {
			q = q.Where("target_type = ?", target)
		}
		if from != nil {
			q = q.Where("created_at >= ?", *from)
		}
		if to != nil {
			q = q.Where("created_at <= ?", *to)
		}
		return q
	}
	if err := filtered().Count(&out.Total).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	var rows []auditRow
	// CAST(... AS CHAR) is required: MySQL hands back a JSON column in its
	// internal binary form, which reaches the operator as a base64 blob instead
	// of the snapshot they need in order to check the change.
	if err := filtered().
		Select("id, actor_id, actor_name, module, action, target_type, target_id, request_id, client_ip, " +
			"CAST(before_json AS CHAR) AS before_json, CAST(after_json AS CHAR) AS after_json, created_at").
		Order("id DESC").Offset((page.Page - 1) * page.PageSize).Limit(page.PageSize).Scan(&rows).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	out.Items = rows
	httpapi.OK(c, out)
}

// auditEntry carries an audit record out of a user_db transaction so it can be
// written to admin_db once that transaction has committed.
type auditEntry struct {
	action    string
	target    string
	id        uint64
	before    any
	after     any
	requestID string
}

func (a ResourceAPI) flushAudit(c *gin.Context, entries []auditEntry) {
	for _, entry := range entries {
		a.auditToAdmin(c, entry.action, entry.target, entry.id, entry.before, entry.after, entry.requestID)
	}
}
