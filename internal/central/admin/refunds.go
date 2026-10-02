package admin

import (
	"context"
	"errors"
	"strconv"
	"strings"

	refundpkg "github.com/ChargePilot2026/charge-pilot/internal/central/refund"
	settlementpkg "github.com/ChargePilot2026/charge-pilot/internal/central/settlement"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerRefunds 注册人工退款、列表、复核和重试路由。
// 分别要求 order.refund.create、finance.refund.read、order.refund.review、finance.refund.retry 权限。
// 写入语义（建单幂等重放、双人复核、执行重投）归属 refund 家族包，本文件只做 HTTP 编排与审计。
func (a ResourceAPI) registerRefunds(r *gin.Engine) {
	r.POST("/api/v1/admin/orders/:id/refunds", a.Auth.Require("order.refund.create"), a.manualRefund)
	r.GET("/api/v1/admin/billing/refunds", a.Auth.Require("finance.refund.read"), a.refunds)
	r.POST("/api/v1/admin/billing/refunds/:refund_no/approve", a.Auth.Require("order.refund.review"), a.reviewRefund)
	r.POST("/api/v1/admin/billing/refunds/:refund_no/reject", a.Auth.Require("order.refund.review"), a.reviewRefund)
	r.POST("/api/v1/admin/billing/refunds/:refund_no/retry", a.Auth.Require("finance.refund.retry"), a.retryRefund)
}

// refundReviewStore 装配 refund 家族的人工退款工作流存储；
// 第二签的实时身份核对回读 admin 授权域的账号档案与权限。
func (a ResourceAPI) refundReviewStore() refundpkg.ReviewStore {
	return refundpkg.ReviewStore{
		DB: a.Store.UserDB,
		LookupSigner: func(ctx context.Context, signerID uint64) (refundpkg.SignerStatus, error) {
			p, err := a.Auth.Store.Profile(ctx, signerID)
			if err != nil {
				return refundpkg.SignerStatus{}, err
			}
			return refundpkg.SignerStatus{Role: p.Role, Permitted: hasPermission(p, "order.refund.review")}, nil
		},
	}
}

// asConflict 将家族包的冲突哨兵映射为后台统一的 409 语义。
func asConflict(err error) error {
	if errors.Is(err, refundpkg.ErrRefundConflict) || errors.Is(err, settlementpkg.ErrInvoiceConflict) {
		return errConflict
	}
	return err
}

// manualRefund 创建待双人复核的人工退款。同一 request_id、相同内容返回原退款单；同号不同内容返回冲突。
func (a ResourceAPI) manualRefund(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var in struct {
		RequestID   string `json:"request_id"`   // 客户端生成的幂等请求号，格式为 UUID；重放同一请求号不会重复建单
		AmountCents int64  `json:"amount_cents"` // 本次退款金额，单位分，必须为正整数
		Reason      string `json:"reason"`       // 退款原因，必填，最多 255 字，用于工单与审计追溯
	}
	if !decodeResource(c, &in) {
		return
	}
	if !requestIDPattern.MatchString(in.RequestID) || in.AmountCents <= 0 || !validText(in.Reason, 255) {
		httpapi.BadRequest(c, "请填写有效请求编号、正整数退款分值和退款原因")
		return
	}
	p := c.MustGet("admin_profile").(Profile)
	created := false
	refundNo := ""
	var recordID uint64
	var auditPending []auditEntry
	err := a.auditedTransaction(c, a.Store.UserDB, &auditPending, func(tx *gorm.DB) error {
		var err error
		created, refundNo, recordID, err = a.refundReviewStore().ManualCreate(c.Request.Context(), tx, refundpkg.ManualRefundInput{
			ActorID: p.ID, OrderID: id, RequestID: in.RequestID, AmountCents: in.AmountCents, Reason: in.Reason,
		})
		if err != nil {
			return err
		}
		if created {
			auditPending = []auditEntry{{"request", "refund", recordID, nil, in, in.RequestID}}
		}
		return nil
	})
	if err != nil {
		resourceFailure(c, asConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"created": created, "refund_no": refundNo, "request_id": in.RequestID})
}

// refunds 分页返回退款记录及当前操作人的 can_approve、can_reject、can_retry。
// 仅 pending、manual_review 记录可复核，首签人不能再次签署；自动退款的执行进度放入 task。
func (a ResourceAPI) refunds(c *gin.Context) {
	q, ok := parsePage(c, "pending processing success failed rejected")
	if !ok {
		return
	}
	if len(c.Query("refund_no")) > 64 {
		httpapi.BadRequest(c, "退款单号过长")
		return
	}
	db := a.Store.UserDB.WithContext(c.Request.Context()).Table("refund_record r").Joins("LEFT JOIN refund_review v ON v.refund_record_id=r.id").Where("r.deleted_at IS NULL")
	if q.Status != "" {
		db = db.Where("r.status=?", q.Status)
	}
	if c.Query("refund_no") != "" {
		db = db.Where("r.refund_no=?", c.Query("refund_no"))
	}
	out := Page[map[string]any]{Items: []map[string]any{}, Page: q.Page, PageSize: q.PageSize}
	if err := db.Count(&out.Total).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	if err := db.Select("r.id,r.refund_no,r.user_id,r.payment_order_id,r.refund_cents,r.status,r.reason,r.failure_reason,r.created_at,r.completed_at,r.execution_policy,r.retry_count,r.next_attempt_at,v.first_signer,v.second_signer,v.first_comment,v.second_comment").Order("r.id DESC").Offset((q.Page - 1) * q.PageSize).Limit(q.PageSize).Find(&out.Items).Error; err != nil {
		resourceFailure(c, err)
		return
	}
	normalizeRows(out.Items)
	stringIDs(out.Items, "id", "user_id", "payment_order_id", "first_signer", "second_signer")
	p := c.MustGet("admin_profile").(Profile)
	for _, row := range out.Items {
		status := row["status"]
		manual := row["execution_policy"] == "manual_review"
		reviewable := status == "pending" && manual && p.Role == "customer_finance" && hasPermission(p, "order.refund.review")
		row["can_approve"] = reviewable && row["first_signer"] != strconv.FormatUint(p.ID, 10)
		row["can_reject"] = reviewable
		row["can_retry"] = row["execution_policy"] == "automatic" && (status == "pending" || status == "processing") && hasPermission(p, "finance.refund.retry")
		row["task"] = nil
		if !manual {
			row["task"] = gin.H{"stage": status, "attempts": row["retry_count"], "last_error": row["failure_reason"], "scheduled_at": row["next_attempt_at"]}
		}
		row["review"] = nil
		if status == "rejected" {
			row["review"] = gin.H{"status": "rejected", "first_signer": "", "second_signer": nil, "first_comment": "", "second_comment": nil}
		}
		if row["first_signer"] != nil {
			state := "awaiting_second"
			if row["second_signer"] != nil {
				state = "approved"
			}
			if status == "rejected" {
				state = "rejected"
			}
			row["review"] = gin.H{"status": state, "first_signer": row["first_signer"], "second_signer": row["second_signer"], "first_comment": row["first_comment"], "second_comment": row["second_comment"]}
		}
		for _, key := range []string{"first_signer", "second_signer", "first_comment", "second_comment", "execution_policy", "retry_count", "next_attempt_at"} {
			delete(row, key)
		}
	}
	httpapi.OK(c, out)
}

// reviewRefund 按路由后缀执行通过或驳回。首签进入 awaiting_second；不同审核人
// 第二签（实时身份由 refund 家族经 LookupSigner 回读授权域核对）后 approved 并安排执行；
// 驳回写入 refund_rejection 并置 rejected，无需第二签。
func (a ResourceAPI) reviewRefund(c *gin.Context) {
	p, ok := a.financeActor(c, "order.refund.review")
	if !ok {
		return
	}
	var in struct {
		ApproveComment string `json:"approve_comment"` // 通过时必填的审核意见，1–255 字
		Reason         string `json:"reason"`          // 驳回时必填的驳回理由，1–255 字
	}
	if !decodeResource(c, &in) {
		return
	}
	approve := strings.HasSuffix(c.FullPath(), "/approve")
	if approve && !validText(in.ApproveComment, 255) || !approve && !validText(in.Reason, 255) {
		httpapi.BadRequest(c, "请填写 1–255 字审核意见")
		return
	}
	status := ""
	var recordID uint64
	var auditPending []auditEntry
	err := a.auditedTransaction(c, a.Store.UserDB, &auditPending, func(tx *gorm.DB) error {
		var err error
		status, recordID, err = a.refundReviewStore().Review(c.Request.Context(), tx, c.Param("refund_no"), refundpkg.ReviewDecision{
			Approve: approve, ApproveComment: in.ApproveComment, Reason: in.Reason, ActorID: p.ID,
		})
		if err != nil {
			return err
		}
		auditPending = []auditEntry{{status, "refund", recordID, nil, in, ""}}
		return nil
	})
	if err != nil {
		resourceFailure(c, asConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"review_status": status})
}

// retryRefund 将 automatic、pending/processing 退款的执行安排到当前时间，不修改金额或状态。
// 人工复核退款不可通过此入口执行。
func (a ResourceAPI) retryRefund(c *gin.Context) {
	var in struct {
		Reason string `json:"reason"` // 重投原因，必填，1–255 字，落审计用
	}
	if !decodeResource(c, &in) {
		return
	}
	if !validText(in.Reason, 255) {
		httpapi.BadRequest(c, "请填写重试原因")
		return
	}
	var recordID uint64
	var auditPending []auditEntry
	err := a.auditedTransaction(c, a.Store.UserDB, &auditPending, func(tx *gorm.DB) error {
		var err error
		recordID, err = a.refundReviewStore().Retry(c.Request.Context(), tx, c.Param("refund_no"))
		if err != nil {
			return err
		}
		auditPending = []auditEntry{{"retry", "refund", recordID, nil, in, ""}}
		return nil
	})
	if err != nil {
		resourceFailure(c, asConflict(err))
		return
	}
	httpapi.OK(c, gin.H{"scheduled": true})
}
