package admin

import (
	"log"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// auditToAdmin 通过 AdminDB 写一条审计，供 user_db 等其他库的业务在事务提交
// 后补记；失败只打 error 日志、不影响请求返回，理由见下方三段。
//
// audit_log 表建在 admin_db，而优惠券、退款、发票、分账和工单这些资源的
// 数据在 user_db：一个处理函数先改了 user_db 的数据，再拿自己的事务顺手
// 写审计，审计就会落进 user_db.audit_log 而不是 admin_db.audit_log。
// 两边的行互相看不见，一次资金操作的轨迹就落在没人看的地方。
//
// 这个方法是 user_db 侧业务记录动作的唯一受支持入口。它走 AdminDB 写，
// 并且排在业务事务提交之后：把两件事并进同一次写就是跨 schema 写，
// 而本项目不做跨 schema 写。
//
// 失败语义是刻意选的：审计执行的时候业务变更已经落库，此时让响应判失败，
// 等于告诉运营一件已经发生的事没有发生；而重试一次退款或一次打款，比少
// 一行日志更糟。所以审计写失败时只按 error 级别带着完整上下文记日志，
// 请求照常成功。写失败只打日志、不影响返回这个取舍记在
// docs/migration/go-rebuild.md 里，不是一句没写下来的默认行为。
func (a ResourceAPI) auditToAdmin(c *gin.Context, action, target string, id uint64, before, after any, requestID string) {
	err := resourceAudit(a.Store.AdminDB.WithContext(c.Request.Context()),
		c.MustGet("admin_profile").(Profile), action, target, id, before, after,
		c.ClientIP(), firstNonEmpty(requestID, httpapi.RequestID(c)))
	if err != nil {
		log.Printf("AUDIT WRITE FAILED action=%s target=%s id=%d actor=%v: %v",
			action, target, id, c.MustGet("admin_profile").(Profile).Username, err)
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

// auditRow 是 admin_db.audit_log 的一行，即审计查询的返回结构。
// 多数列可空（未带请求号、未取到客户端 IP 等），所以用指针表达"可能没有"。
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
	// 用 *string 而不是 *[]byte：encoding/json 会把 []byte 渲染成
	// base64，运营拿到手的就是一串读不懂的 blob，而不是他核对
	// 这次改动要用的快照。
	BeforeJSON *string   `json:"before_json"` // 变更前快照；指针，新建记录时为 null
	AfterJSON  *string   `json:"after_json"`  // 变更后快照；指针，新建记录时为 null
	CreatedAt  time.Time `json:"created_at"`  // 记录时间
}

// registerAuditLogs 挂载审计日志查询接口，权限为 audit.read。
func (a ResourceAPI) registerAuditLogs(r *gin.Engine) {
	r.GET("/api/v1/admin/audit-logs", a.Auth.Require("audit.read"), a.listAuditLogs)
}

// listAuditLogs 是 GET /api/v1/admin/audit-logs 的处理函数：按模块、
// 动作、操作人、目标类型和时间区间分页查询审计记录，按 ID 倒序。
// 审计只有写的一侧、没有读的一侧就等于没有审计：运营可以在一个
// 什么都没记下来的界面上动手，事后无从回查这次改动。
//
// 时间参数须为 RFC 3339，不合法直接写 400；分页参数与其它列表
// 接口一致。
func (a ResourceAPI) listAuditLogs(c *gin.Context) {
	page, ok := parsePage(c, "")
	if !ok {
		return
	}
	out := Page[auditRow]{Items: []auditRow{}, Page: page.Page, PageSize: page.PageSize}

	// 筛选条件先收齐，然后同一个查询建两遍：count 与列表各调一次
	// 带 Session 的语句，会让 count 里的 SELECT count(*) 泄漏进
	// 第二条语句，列清单被悄悄抹掉。
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

	// 闭包把筛选条件封装起来，Count 和列表查询各调一次，保证两边口径完全一致。
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
	// CAST(... AS CHAR) 是必需的：MySQL 交回的 JSON 列是它的内部二进制
	// 形式，到运营手里就成了一串 base64 blob，而不是他核对这次改动
	// 需要的快照。
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

// auditEntry 是一条待补写的审计：它替 user_db 的事务把一条审计记录带
// 出去，等那个事务提交之后再写进 admin_db。
//
// user_db 的业务在事务内攒下这些条目，事务提交后由 flushAudit 逐条写进
// admin_db.audit_log。字段含义与 resourceAudit 相同。
type auditEntry struct {
	action    string // 动作，如 create / update / grant
	target    string // 目标类型，同时也是所属模块，如 coupon
	id        uint64 // 目标主键
	before    any    // 变更前快照，新建时为 nil
	after     any    // 变更后快照
	requestID string // 关联请求号，可为空
}

// flushAudit 在业务事务提交后把攒下的审计条目逐条写入 admin_db。
// 单条写失败只记日志、不影响已提交的业务结果。
func (a ResourceAPI) flushAudit(c *gin.Context, entries []auditEntry) {
	for _, entry := range entries {
		a.auditToAdmin(c, entry.action, entry.target, entry.id, entry.before, entry.after, entry.requestID)
	}
}
