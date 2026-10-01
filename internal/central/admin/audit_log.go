package admin

import (
	"log"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 网关变更由远端提交，收到响应后记录运营审计。
// central 内的业务变更与审计通过 auditedTransaction 在同一事务提交。
func (a ResourceAPI) auditToAdmin(c *gin.Context, action, target string, id uint64, before, after any, requestID string) {
	if err := resourceAudit(a.Store.AdminDB.WithContext(c.Request.Context()), c.MustGet("admin_profile").(Profile), action, target, id, before, after, c.ClientIP(), firstNonEmpty(requestID, httpapi.RequestID(c))); err != nil {
		log.Printf("gateway audit failed action=%s target=%s id=%d: %v", action, target, id, err)
	}
}

// firstNonEmpty 依次取参数中第一个非空字符串，全空则返回空串。
// 用来让业务自己带的请求号优先于框架生成的。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// auditRow 映射 central_db.audit_log；可空字段使用指针保留 NULL 语义。
type auditRow struct {
	ID         uint64  `json:"id"`          // 审计记录主键
	ActorID    uint64  `json:"actor_id"`    // 操作的后台账号 ID
	ActorName  string  `json:"actor_name"`  // 操作人登录名（当时的快照）
	Module     string  `json:"module"`      // 所属模块，等于目标类型，如 auth / coupon
	Action     string  `json:"action"`      // 动作，如 login / update / grant
	TargetType *string `json:"target_type"` // 目标类型；指针，未指定对象时为 null
	TargetID   *string `json:"target_id"`   // 目标主键（字符串形式）；指针，未指定时为 null
	RequestID  *string `json:"request_id"`  // 关联请求号；指针，取不到时为 null
	ClientIP   *string `json:"client_ip"`   // 客户端 IP；指针，取不到时为 null
	// 快照使用 *string 返回 JSON 文本，避免 []byte 被 encoding/json 编码为 base64。
	BeforeJSON *string   `json:"before_json"` // 变更前快照；指针，新建记录时为 null
	AfterJSON  *string   `json:"after_json"`  // 变更后快照；指针，新建记录时为 null
	CreatedAt  time.Time `json:"created_at"`  // 记录时间
}

// registerAuditLogs 挂载审计日志查询接口，权限为 audit.read。
func (a ResourceAPI) registerAuditLogs(r *gin.Engine) {
	r.GET("/api/v1/admin/audit-logs", a.Auth.Require("audit.read"), a.listAuditLogs)
}

// listAuditLogs 按模块、动作、操作人、目标类型及时间分页查询审计记录，按 ID 倒序。
// 时间参数必须符合 RFC3339；分页或时间参数无效时返回 400。
func (a ResourceAPI) listAuditLogs(c *gin.Context) {
	page, ok := parsePage(c, "")
	if !ok {
		return
	}
	out := Page[auditRow]{Items: []auditRow{}, Page: page.Page, PageSize: page.PageSize}

	// Count 与列表查询分别构造 GORM 语句，避免计数操作修改后续查询的投影。
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

	// 复用筛选闭包，确保 Count 与列表使用相同过滤条件。
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
	// 将 JSON 列 CAST 为 CHAR 后扫描到字符串，统一快照的文本表示。
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

// auditEntry is committed in the same central_db transaction as its business change.
type auditEntry struct {
	action    string // 动作，如 create / update / grant
	target    string // 目标类型，同时也是所属模块，如 coupon
	id        uint64 // 目标主键
	before    any    // 变更前快照，新建时为 nil
	after     any    // 变更后快照
	requestID string // 关联请求号，可为空
}

// auditedTransaction rolls back the business change if its audit cannot be stored.
func (a ResourceAPI) auditedTransaction(c *gin.Context, db *gorm.DB, entries *[]auditEntry, apply func(*gorm.DB) error) error {
	return db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := apply(tx); err != nil {
			return err
		}
		p := c.MustGet("admin_profile").(Profile)
		for _, entry := range *entries {
			if err := resourceAudit(tx, p, entry.action, entry.target, entry.id, entry.before, entry.after, c.ClientIP(), firstNonEmpty(entry.requestID, httpapi.RequestID(c))); err != nil {
				return err
			}
		}
		return nil
	})
}
