package admin

import (
	"errors"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// registerInvoices 挂载开票审核的三个接口。列表只读，要求 finance.read；审批和驳回
// 走同一个 handler，由路由路径区分动作，两者都要求 invoice.review。
func (a ResourceAPI) registerInvoices(r *gin.Engine) {
	r.GET("/api/v1/admin/billing/invoices", a.Auth.Require("finance.read"), a.invoices)
	r.POST("/api/v1/admin/billing/invoices/:id/approve", a.Auth.Require("invoice.review"), a.reviewInvoice)
	r.POST("/api/v1/admin/billing/invoices/:id/reject", a.Auth.Require("invoice.review"), a.reviewInvoice)
}

// invoices 返回开票申请列表，左连 invoice_admin_review 汇总双审进度。带 queue_ 前缀
// 的三个字段是汇总后的当前值（优先取审核表，没有再回退到申请单本身），因为审核进度
// 可能只记在审核表里。审核人 ID 统一转成字符串输出，避免 JS 大整数精度丢失。
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

// financeActor 取出当前管理员，校验其既是 customer_finance 角色又确实持有指定审核
// 权限，不满足直接 403。发票审核和计量核实共用这道闸门。
func (a ResourceAPI) financeActor(c *gin.Context, permission string) (Profile, bool) {
	p := c.MustGet("admin_profile").(Profile)
	if p.Role != "customer_finance" || !hasPermission(p, permission) {
		httpapi.Write(c, 403, 1003, "须由拥有审核权限的客户财务人员执行", nil)
		return p, false
	}
	return p, true
}

// reviewInvoice 处理发票审批与驳回，动作由路由路径决定。审批走双人双审：首审只登记
// 发票链接并转入 awaiting_second，二审必须与首审不是同一个人、状态确实在
// awaiting_second、且提交的链接与首审完全一致才真正开票，同时首审账号和权限必须
// 仍然有效。驳回则单人事毕即可。事务内对申请单加行锁并要求它仍处于 pending，
// 否则按并发冲突回 409。
func (a ResourceAPI) reviewInvoice(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	p, ok := a.financeActor(c, "invoice.review")
	if !ok {
		return
	}
	// 请求体随动作而变：审批只读 invoice_url，驳回只读 reason，两者的必填项由下方校验区分。
	var in struct {
		InvoiceURL string `json:"invoice_url"` // 审批时必填的 HTTPS 发票链接。
		Reason     string `json:"reason"`      // 驳回原因，最长 255 字符；审批时可留空。
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
	// 审核表在 user_db，审计在 admin_db，所以审计记录先攒在事务外，事务提交后再落。
	var auditPending []auditEntry
	err := a.Store.UserDB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// 申请单本体：只用到审核状态和已有的发票链接。
		var invoice struct {
			ReviewStatus string  // 申请单当前审核状态，必须是 pending 才允许本次操作。
			InvoiceURL   *string // 申请单上已有的发票链接，可空。
		}
		if err := tx.Table("invoice_request").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&invoice).Error; err != nil {
			return err
		}
		// 双审进度表：记录是否已有人审、审的是谁、发票链接是什么。
		var review struct {
			ReviewStatus     string  // awaiting_second 表示已首审待复核；空表示还没有审核记录。
			FirstReviewerID  *uint64 // 首审人；nil 表示还没人首审。
			SecondReviewerID *uint64 // 二审人；nil 表示尚未复核。
			InvoiceURL       *string // 首审人登记的发票链接，二审必须原样提交。
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
