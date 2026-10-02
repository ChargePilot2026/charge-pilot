package settlement

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrInvoiceConflict 表示开票审核的状态、签名或内容冲突。
var ErrInvoiceConflict = errors.New("invoice review conflict")

// ReviewerStatus 是审核人的实时身份快照，由调用方（admin 授权域）注入查询。
// 第二签必须核对前一审核人的实时角色与权限，不沿用首签时的会话结论。
type ReviewerStatus struct {
	Role      string
	Permitted bool
}

// InvoiceReviewStore 承载人工开票的双人复核写入（invoice_request / invoice_admin_review）。
// 方法不自行开事务：调用方须传入已挂载审计与幂等语义的 *gorm.DB 事务。
type InvoiceReviewStore struct {
	DB *gorm.DB
	// LookupReviewer 查询审核人的实时角色与权限；nil 时第二签一律冲突。
	LookupReviewer func(ctx context.Context, reviewerID uint64) (ReviewerStatus, error)
}

// InvoiceDecision 是一笔开票审核决定；Approve=true 走双人通过，false 为单人驳回。
type InvoiceDecision struct {
	Approve    bool
	InvoiceURL string
	Reason     string
	ActorID    uint64
}

// Review 在调用方事务内锁定申请并校验当前审核状态。
// 审批要求两个不同且仍有权限的审核人，第二签必须提交与首签一致的发票链接；
// 驳回由单人完成。不满足状态或审核条件时返回 ErrInvoiceConflict。
// before 返回审核前的申请单要素快照，供调用方落审计。
func (s InvoiceReviewStore) Review(ctx context.Context, tx *gorm.DB, id uint64, in InvoiceDecision) (string, map[string]any, error) {
	status := "rejected"
	// 申请单本体：只用到审核状态和已有的发票链接。
	var invoice struct {
		ReviewStatus string  // 申请单当前审核状态，必须是 pending 才允许本次操作。
		InvoiceURL   *string // 申请单上已有的发票链接，可空。
	}
	if err := tx.WithContext(ctx).Table("invoice_request").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&invoice).Error; err != nil {
		return status, nil, err
	}
	before := map[string]any{"review_status": invoice.ReviewStatus, "invoice_url": invoice.InvoiceURL}
	// 双审进度表：记录是否已有人审、审的是谁、发票链接是什么。
	var review struct {
		ReviewStatus     string  // awaiting_second 表示已首审待复核；空表示还没有审核记录。
		FirstReviewerID  *uint64 // 首审人；nil 表示还没人首审。
		SecondReviewerID *uint64 // 二审人；nil 表示尚未复核。
		InvoiceURL       *string // 首审人登记的发票链接，二审必须原样提交。
	}
	err := tx.Table("invoice_admin_review").Where("invoice_request_id=?", id).Take(&review).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return status, before, err
	}
	if invoice.ReviewStatus != "pending" {
		return status, before, ErrInvoiceConflict
	}
	if !in.Approve {
		if review.ReviewStatus != "" && review.ReviewStatus != "awaiting_second" {
			return status, before, ErrInvoiceConflict
		}
		if err := tx.Exec("INSERT INTO invoice_admin_review(invoice_request_id,review_status,reject_reason) VALUES (?,'rejected',?) ON DUPLICATE KEY UPDATE review_status='rejected',reject_reason=VALUES(reject_reason),invoice_url=NULL", id, in.Reason).Error; err != nil {
			return status, before, err
		}
		return status, before, tx.Table("invoice_request").Where("id=?", id).Updates(map[string]any{"review_status": "rejected", "reviewed_by": in.ActorID, "reviewed_at": time.Now().UTC(), "reject_reason": in.Reason}).Error
	}
	if review.FirstReviewerID == nil {
		status = "awaiting_second"
		return status, before, tx.Table("invoice_admin_review").Create(map[string]any{"invoice_request_id": id, "review_status": status, "first_reviewer_id": in.ActorID, "invoice_url": in.InvoiceURL}).Error
	}
	if *review.FirstReviewerID == in.ActorID || review.ReviewStatus != "awaiting_second" || review.InvoiceURL == nil || *review.InvoiceURL != in.InvoiceURL {
		return status, before, ErrInvoiceConflict
	}
	if s.LookupReviewer == nil {
		return status, before, ErrInvoiceConflict
	}
	first, err := s.LookupReviewer(ctx, *review.FirstReviewerID)
	if err != nil || first.Role != "customer_finance" || !first.Permitted {
		return status, before, ErrInvoiceConflict
	}
	status = "approved"
	if err := tx.Table("invoice_admin_review").Where("invoice_request_id=?", id).Updates(map[string]any{"review_status": status, "second_reviewer_id": in.ActorID}).Error; err != nil {
		return status, before, err
	}
	return status, before, tx.Table("invoice_request").Where("id=?", id).Updates(map[string]any{"review_status": "issued", "invoice_url": in.InvoiceURL, "reviewed_by": in.ActorID, "reviewed_at": time.Now().UTC()}).Error
}
