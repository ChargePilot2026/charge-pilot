package admin

import (
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// registerCasework 挂载客服工单的两条线：用户反馈（feedback）和设备故障报修（device_fault_report）。
// 反馈可回复可关闭；报修多出"处理历史"和"指派/解决"两个动作，状态流转 open → dispatched → fixed → closed。
// 读与写的权限分开：看工单用 feedback.read / fault.read，回复、指派、解决各自独立。
func (a ResourceAPI) registerCasework(r *gin.Engine) {
	r.GET("/api/v1/admin/feedback", a.Auth.Require("feedback.read"), func(c *gin.Context) { a.caseList(c, "feedback", "pending processed closed") })
	r.POST("/api/v1/admin/feedback/:id/reply", a.Auth.Require("feedback.reply"), a.replyFeedback)
	r.GET("/api/v1/admin/device-fault-reports", a.Auth.Require("fault.read"), func(c *gin.Context) { a.caseList(c, "device_fault_report", "open dispatched fixed closed") })
	r.GET("/api/v1/admin/device-fault-reports/:id/history", a.Auth.Require("fault.read"), a.faultHistory)
	r.POST("/api/v1/admin/device-fault-reports/:id/dispatch", a.Auth.Require("fault.dispatch"), a.dispatchFault)
	r.POST("/api/v1/admin/device-fault-reports/:id/resolve", a.Auth.Require("fault.resolve"), a.resolveFault)
}

// stringIDs 把若干列的值统一转成字符串，避免 MySQL 驱动在 []map[string]any 里把 BIGINT 变成科学计数法，
// 前端拿到的 id 因此是字符串形式。nil 值原样保留，表示该列在本行为空。
func stringIDs(rows []map[string]any, keys ...string) {
	for _, row := range rows {
		for _, key := range keys {
			if v, ok := row[key]; ok && v != nil {
				row[key] = fmt.Sprint(v)
			}
		}
	}
}

// caseList 是反馈与报修两类工单共用的列表实现，表名和允许的状态集合由调用方传入。
// 返回前把图片列从 images_json 改名成 images 并保证是数组（没有图为空数组，不是 null），
// 免得前端每处都要判空。
func (a ResourceAPI) caseList(c *gin.Context, table, statuses string) {
	q, ok := parsePage(c, statuses)
	if !ok {
		return
	}
	db := a.Store.UserDB.WithContext(c.Request.Context()).Table(table).Where("deleted_at IS NULL")
	if q.Status != "" {
		db = db.Where("status=?", q.Status)
	}
	out := Page[map[string]any]{Items: []map[string]any{}, Page: q.Page, PageSize: q.PageSize}
	if err := db.Count(&out.Total).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if err := db.Order("id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out.Items).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	normalizeRows(out.Items)
	stringIDs(out.Items, "id", "user_id", "order_id", "assigned_to")
	for _, row := range out.Items {
		row["images"] = row["images_json"]
		if row["images"] == nil {
			row["images"] = []string{}
		}
		delete(row, "images_json")
	}
	httpapi.OK(c, out)
}

// replyFeedback 处理用户反馈：action 为 reply 时写入回复并把状态从 pending 推到 processed，
// 为 close 时直接置为 closed。对已关闭的工单重复关闭是幂等的，重复回复或对已关闭再回复则判冲突。
func (a ResourceAPI) replyFeedback(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		Action       string `json:"action"`        // reply 回复 / close 关闭，二选一
		ReplyContent string `json:"reply_content"` // 回复正文，action 为 reply 时必填，1–2000 字
	}
	if !decodeResource(c, &in) {
		return
	}
	if in.Action != "close" && (in.Action != "reply" || !validText(in.ReplyContent, 2000)) {
		httpapi.BadRequest(c, "请填写 1–2000 字回复或关闭反馈")
		return
	}
	p := c.MustGet("admin_profile").(Profile)
	var auditPending []auditEntry
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var row struct{ Status string } // 反馈当前状态：pending 待处理 / processed 已回复 / closed 已关闭
		if err := tx.Table("feedback").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
			return err
		}
		if in.Action == "close" && row.Status == "closed" {
			return nil
		}
		if row.Status == "closed" || (in.Action == "reply" && row.Status != "pending") {
			return errConflict
		}
		v := map[string]any{"status": "closed"}
		if in.Action == "reply" {
			v = map[string]any{"status": "processed", "reply_content": in.ReplyContent, "replied_by": p.ID, "replied_at": time.Now().UTC()}
		}
		if err := tx.Table("feedback").Where("id=?", id).Updates(v).Error; err != nil {
			return err
		}
		auditPending = []auditEntry{{in.Action, "feedback", id, row, v, ""}}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	a.flushAudit(c, auditPending)
	httpapi.OK(c, gin.H{"saved": true})
}

// faultHistory 分页返回一条报修工单的流转历史（device_fault_report_event），含每次状态变化的操作人、备注和对用户是否可见。
// 先确认报修单存在再查事件，避免对不存在的工单返回一个空列表（看起来像"没有历史"）。
func (a ResourceAPI) faultHistory(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	q, ok := parsePage(c, "")
	if !ok {
		return
	}
	var n int64
	db := a.Store.UserDB.WithContext(c.Request.Context())
	if err := db.Table("device_fault_report").Where("id=? AND deleted_at IS NULL", id).Count(&n).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if n == 0 {
		resourceFailure(c, gorm.ErrRecordNotFound)
		return
	}
	query := db.Table("device_fault_report_event").Where("report_id=?", id)
	out := Page[map[string]any]{Items: []map[string]any{}, Page: q.Page, PageSize: q.PageSize}
	if err := query.Count(&out.Total).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if err := query.Select("id AS event_id,event_type,actor_id,assigned_to,from_status,to_status,note,created_at,user_visible").Order("id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out.Items).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	normalizeRows(out.Items)
	stringIDs(out.Items, "event_id", "actor_id", "assigned_to")
	httpapi.OK(c, out)
}

// faultRow 是报修工单在流转处理中真正用到的三列最小投影。
type faultRow struct {
	ID         uint64  // 报修单主键
	Status     string  // 当前状态：open 待受理 / dispatched 已指派 / fixed 已修复 / closed 已关闭
	AssignedTo *uint64 // 当前处理人 ID，nil 表示还没人接手
}

// dispatchFault 把报修单指派给某个处理人，状态推进到 dispatched。
// 处理人必须存在且拥有 fault.resolve 权限，否则指派出去也没人能处理完。
// 已有处理人时记 reassigned 事件而非 dispatched，并写一条对用户可见的处理记录。
// 重复指派给同一人是幂等的。
func (a ResourceAPI) dispatchFault(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		AssignedTo uint64  `json:"assigned_to"` // 处理人管理员 ID，必填且非 0
		Note       *string `json:"note"`        // 备注，可空；非空时最多 2000 字
	}
	if !decodeResource(c, &in) {
		return
	}
	if in.AssignedTo == 0 || (in.Note != nil && utf8.RuneCountInString(*in.Note) > 2000) {
		httpapi.BadRequest(c, "请指定有效人员，备注最多 2000 字")
		return
	}
	assignee, err := a.Auth.Store.Profile(c.Request.Context(), in.AssignedTo)
	if err != nil || !hasPermission(assignee, "fault.resolve") {
		httpapi.BadRequest(c, "指派账号不存在、未启用或无故障处理权限")
		return
	}
	p := c.MustGet("admin_profile").(Profile)
	var auditPending []auditEntry
	err = a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var row faultRow
		if err := tx.Table("device_fault_report").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
			return err
		}
		if row.Status != "open" && row.Status != "dispatched" {
			return errConflict
		}
		if row.AssignedTo != nil && *row.AssignedTo == in.AssignedTo {
			return nil
		}
		event := "dispatched"
		if row.AssignedTo != nil {
			event = "reassigned"
		}
		if err := tx.Table("device_fault_report").Where("id=?", id).Updates(map[string]any{"status": "dispatched", "assigned_to": in.AssignedTo}).Error; err != nil {
			return err
		}
		if err := tx.Table("device_fault_report_event").Create(map[string]any{"report_id": id, "actor_id": p.ID, "event_type": event, "from_status": row.Status, "to_status": "dispatched", "assigned_to": in.AssignedTo, "note": in.Note, "user_visible": true}).Error; err != nil {
			return err
		}
		auditPending = []auditEntry{{event, "device_fault_report", id, row, in, ""}}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	a.flushAudit(c, auditPending)
	httpapi.OK(c, gin.H{"assigned_to": in.AssignedTo, "status": "dispatched"})
}

// resolveFault 由当前处理人把报修单标记为 fixed 或 closed。
// 只有被指派的本人能操作；fixed 必须从 dispatched 来、closed 必须从 fixed 来，保证修复和关闭两步都留痕。
// 标记 fixed 时必须写修复说明。重复提交同一目标状态是幂等的。
func (a ResourceAPI) resolveFault(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		Status string  `json:"status"` // 目标状态：fixed 已修复 或 closed 已关闭
		Note   *string `json:"note"`   // 备注；status 为 fixed 时必填，1–2000 字
	}
	if !decodeResource(c, &in) {
		return
	}
	if (in.Status != "fixed" && in.Status != "closed") || (in.Note != nil && utf8.RuneCountInString(*in.Note) > 2000) || (in.Status == "fixed" && (in.Note == nil || !validText(*in.Note, 2000))) {
		httpapi.BadRequest(c, "状态无效或缺少修复说明")
		return
	}
	p := c.MustGet("admin_profile").(Profile)
	var auditPending []auditEntry
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var row faultRow
		if err := tx.Table("device_fault_report").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&row).Error; err != nil {
			return err
		}
		if row.AssignedTo == nil || *row.AssignedTo != p.ID {
			return errConflict
		}
		if row.Status == in.Status {
			return nil
		}
		if (in.Status == "fixed" && row.Status != "dispatched") || (in.Status == "closed" && row.Status != "fixed") {
			return errConflict
		}
		if err := tx.Table("device_fault_report").Where("id=?", id).Updates(map[string]any{"status": in.Status, "resolved_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		if err := tx.Table("device_fault_report_event").Create(map[string]any{"report_id": id, "actor_id": p.ID, "event_type": in.Status, "from_status": row.Status, "to_status": in.Status, "assigned_to": p.ID, "note": in.Note, "user_visible": true}).Error; err != nil {
			return err
		}
		auditPending = []auditEntry{{in.Status, "device_fault_report", id, row, in, ""}}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	a.flushAudit(c, auditPending)
	httpapi.OK(c, gin.H{"status": in.Status})
}
