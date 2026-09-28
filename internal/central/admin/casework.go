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

func (a ResourceAPI) registerCasework(r *gin.Engine) {
	r.GET("/api/v1/admin/feedback", a.Auth.Require("feedback.read"), func(c *gin.Context) { a.caseList(c, "feedback", "pending processed closed") })
	r.POST("/api/v1/admin/feedback/:id/reply", a.Auth.Require("feedback.reply"), a.replyFeedback)
	r.GET("/api/v1/admin/device-fault-reports", a.Auth.Require("fault.read"), func(c *gin.Context) { a.caseList(c, "device_fault_report", "open dispatched fixed closed") })
	r.GET("/api/v1/admin/device-fault-reports/:id/history", a.Auth.Require("fault.read"), a.faultHistory)
	r.POST("/api/v1/admin/device-fault-reports/:id/dispatch", a.Auth.Require("fault.dispatch"), a.dispatchFault)
	r.POST("/api/v1/admin/device-fault-reports/:id/resolve", a.Auth.Require("fault.resolve"), a.resolveFault)
}
func stringIDs(rows []map[string]any, keys ...string) {
	for _, row := range rows {
		for _, key := range keys {
			if v, ok := row[key]; ok && v != nil {
				row[key] = fmt.Sprint(v)
			}
		}
	}
}
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
func (a ResourceAPI) replyFeedback(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		Action       string `json:"action"`
		ReplyContent string `json:"reply_content"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if in.Action != "close" && (in.Action != "reply" || !validText(in.ReplyContent, 2000)) {
		httpapi.BadRequest(c, "请填写 1–2000 字回复或关闭反馈")
		return
	}
	p := c.MustGet("admin_profile").(Profile)
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var row struct{ Status string }
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
		return resourceAudit(tx, p, in.Action, "feedback", id, row, v, c.ClientIP(), "")
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"saved": true})
}
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

type faultRow struct {
	ID         uint64
	Status     string
	AssignedTo *uint64
}

func (a ResourceAPI) dispatchFault(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		AssignedTo uint64  `json:"assigned_to"`
		Note       *string `json:"note"`
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
		return resourceAudit(tx, p, event, "device_fault_report", id, row, in, c.ClientIP(), "")
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"assigned_to": in.AssignedTo, "status": "dispatched"})
}
func (a ResourceAPI) resolveFault(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		Status string  `json:"status"`
		Note   *string `json:"note"`
	}
	if !decodeResource(c, &in) {
		return
	}
	if (in.Status != "fixed" && in.Status != "closed") || (in.Note != nil && utf8.RuneCountInString(*in.Note) > 2000) || (in.Status == "fixed" && (in.Note == nil || !validText(*in.Note, 2000))) {
		httpapi.BadRequest(c, "状态无效或缺少修复说明")
		return
	}
	p := c.MustGet("admin_profile").(Profile)
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
		return resourceAudit(tx, p, in.Status, "device_fault_report", id, row, in, c.ClientIP(), "")
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"status": in.Status})
}
