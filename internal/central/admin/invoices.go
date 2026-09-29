package admin

import (
	"errors"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (a ResourceAPI) registerInvoices(r *gin.Engine) {
	r.GET("/api/v1/admin/billing/invoices", a.Auth.Require("finance.read"), a.invoices)
	r.POST("/api/v1/admin/billing/invoices/:id/approve", a.Auth.Require("invoice.review"), a.reviewInvoice)
	r.POST("/api/v1/admin/billing/invoices/:id/reject", a.Auth.Require("invoice.review"), a.reviewInvoice)
}
func (a ResourceAPI) invoices(c *gin.Context) {
	rows := []map[string]any{}
	err := a.Store.UserDB.WithContext(c.Request.Context()).Table("invoice_request i").Select("i.id AS invoice_request_id,i.invoice_no,i.user_id,i.biz_type,i.biz_id,i.total_cents,i.invoice_type,i.title,i.tax_no,i.email,i.review_status,COALESCE(r.review_status,i.review_status) AS queue_review_status,r.reject_reason AS queue_reject_reason,r.first_reviewer_id,r.second_reviewer_id,i.reject_reason,COALESCE(r.invoice_url,i.invoice_url) AS invoice_url,i.created_at").Joins("LEFT JOIN invoice_admin_review r ON r.invoice_request_id=i.id").Where("i.deleted_at IS NULL").Order("i.id DESC").Find(&rows).Error
	if err != nil {
		resourceFailure(c, err)
		return
	}
	normalizeRows(rows)
	stringIDs(rows, "first_reviewer_id", "second_reviewer_id")
	httpapi.OK(c, gin.H{"items": rows})
}
func (a ResourceAPI) financeActor(c *gin.Context, permission string) (Profile, bool) {
	p := c.MustGet("admin_profile").(Profile)
	if p.Role != "customer_finance" || !hasPermission(p, permission) {
		httpapi.Write(c, 403, 1003, "须由拥有审核权限的客户财务人员执行", nil)
		return p, false
	}
	return p, true
}
func (a ResourceAPI) reviewInvoice(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	p, ok := a.financeActor(c, "invoice.review")
	if !ok {
		return
	}
	var in struct {
		InvoiceURL string `json:"invoice_url"`
		Reason     string `json:"reason"`
	}
	if !decodeResource(c, &in) {
		return
	}
	approve := c.FullPath() == "/api/v1/admin/billing/invoices/:id/approve"
	if (approve && !httpsURL(in.InvoiceURL)) || (!approve && !validText(in.Reason, 255)) {
		httpapi.BadRequest(c, "请填写 HTTPS 发票链接或拒绝原因")
		return
	}
	status := "rejected"
	var auditPending []auditEntry
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var invoice struct {
			ReviewStatus string
			InvoiceURL   *string
		}
		if err := tx.Table("invoice_request").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&invoice).Error; err != nil {
			return err
		}
		var review struct {
			ReviewStatus     string
			FirstReviewerID  *uint64
			SecondReviewerID *uint64
			InvoiceURL       *string
		}
		err := tx.Table("invoice_admin_review").Where("invoice_request_id=?", id).Take(&review).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if invoice.ReviewStatus != "pending" {
			return errConflict
		}
		if !approve {
			if review.ReviewStatus != "" && review.ReviewStatus != "awaiting_second" {
				return errConflict
			}
			if err := tx.Exec("INSERT INTO invoice_admin_review(invoice_request_id,review_status,reject_reason) VALUES (?,'rejected',?) ON DUPLICATE KEY UPDATE review_status='rejected',reject_reason=VALUES(reject_reason),invoice_url=NULL", id, in.Reason).Error; err != nil {
				return err
			}
			if err := tx.Table("invoice_request").Where("id=?", id).Updates(map[string]any{"review_status": "rejected", "reviewed_by": p.ID, "reviewed_at": time.Now().UTC(), "reject_reason": in.Reason}).Error; err != nil {
				return err
			}
		} else if review.FirstReviewerID == nil {
			status = "awaiting_second"
			if err := tx.Table("invoice_admin_review").Create(map[string]any{"invoice_request_id": id, "review_status": status, "first_reviewer_id": p.ID, "invoice_url": in.InvoiceURL}).Error; err != nil {
				return err
			}
		} else {
			if *review.FirstReviewerID == p.ID || review.ReviewStatus != "awaiting_second" || review.InvoiceURL == nil || *review.InvoiceURL != in.InvoiceURL {
				return errConflict
			}
			first, err := a.Auth.Store.Profile(c.Request.Context(), *review.FirstReviewerID)
			if err != nil || first.Role != "customer_finance" || !hasPermission(first, "invoice.review") {
				return errConflict
			}
			status = "approved"
			if err := tx.Table("invoice_admin_review").Where("invoice_request_id=?", id).Updates(map[string]any{"review_status": status, "second_reviewer_id": p.ID}).Error; err != nil {
				return err
			}
			if err := tx.Table("invoice_request").Where("id=?", id).Updates(map[string]any{"review_status": "issued", "invoice_url": in.InvoiceURL, "reviewed_by": p.ID, "reviewed_at": time.Now().UTC()}).Error; err != nil {
				return err
			}
		}
		auditPending = []auditEntry{{status, "invoice", id, invoice, in, ""}}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	a.flushAudit(c, auditPending)
	httpapi.OK(c, gin.H{"review_status": status})
}
