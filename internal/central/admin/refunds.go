package admin

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/charge"
	"github.com/ChargePilot2026/charge-pilot/internal/platform/httpapi"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// registerRefunds 注册人工退款、列表、复核和重试路由。
// 分别要求 order.refund.create、finance.refund.read、order.refund.review、finance.refund.retry 权限。
func (a ResourceAPI) registerRefunds(r *gin.Engine) {
	r.POST("/api/v1/admin/orders/:id/refunds", a.Auth.Require("order.refund.create"), a.manualRefund)
	r.GET("/api/v1/admin/billing/refunds", a.Auth.Require("finance.refund.read"), a.refunds)
	r.POST("/api/v1/admin/billing/refunds/:refund_no/approve", a.Auth.Require("order.refund.review"), a.reviewRefund)
	r.POST("/api/v1/admin/billing/refunds/:refund_no/reject", a.Auth.Require("order.refund.review"), a.reviewRefund)
	r.POST("/api/v1/admin/billing/refunds/:refund_no/retry", a.Auth.Require("finance.refund.retry"), a.retryRefund)
}

// manualRefund 在事务中锁定支付单，校验订单可退及累计退款不超额，再创建待双人复核的退款记录。
// 同一 request_id、相同内容返回原退款单；同号不同内容返回冲突。
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
	payload, _ := json.Marshal(gin.H{"actor_id": p.ID, "order_id": id, "amount_cents": in.AmountCents, "reason": in.Reason})
	sum := sha256.Sum256([]byte(in.RequestID))
	refundNo := "MR" + hex.EncodeToString(sum[:16])
	created := false
	var auditPending []auditEntry
	err := a.auditedTransaction(c, a.Store.UserDB, &auditPending, func(tx *gorm.DB) error {
		var order charge.ChargeOrderRecord
		if err := tx.Where("id=? AND deleted_at IS NULL", id).Take(&order).Error; err != nil {
			return err
		}
		var pay charge.PaymentOrderRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", order.PaymentOrderID).Take(&pay).Error; err != nil {
			return err
		}
		var receipt struct {
			PayloadJSON string // 同一 request_id 首次提交时的请求体快照，重放时用于逐字段比对内容是否一致
			RefundNo    string // 首次生成的退款单号，重放时直接返回它，保证同号始终对应同一笔退款
		}
		e := tx.Table("manual_refund_request").Where("request_id=?", in.RequestID).Take(&receipt).Error
		if e == nil {
			var x, y any
			_ = json.Unmarshal([]byte(receipt.PayloadJSON), &x)
			_ = json.Unmarshal(payload, &y)
			xb, _ := json.Marshal(x)
			yb, _ := json.Marshal(y)
			if string(xb) != string(yb) {
				return errConflict
			}
			refundNo = receipt.RefundNo
			return nil
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&order).Error; err != nil {
			return err
		}
		if !oneOf(order.Status, "completed cancelled failed refunded") || order.Status == "refunded" || pay.UserID != order.UserID || pay.PayMethod != "wechat" || !oneOf(pay.Status, "paid partial_refunded") || in.AmountCents > pay.PaidCents-pay.RefundedCents {
			return errConflict
		}
		var pending int64
		if err := tx.Table("refund_record").Select("COALESCE(SUM(refund_cents),0)").Where("payment_order_id=? AND status IN ('pending','processing') AND deleted_at IS NULL", pay.ID).Scan(&pending).Error; err != nil {
			return err
		}
		if pending > pay.PaidCents-pay.RefundedCents-in.AmountCents {
			return errConflict
		}
		now := time.Now().UTC()
		record := charge.RefundRecord{RefundNo: refundNo, PaymentOrderID: pay.ID, UserID: pay.UserID, BizType: "charge", BizID: id, RefundCents: in.AmountCents, Reason: sql.NullString{String: in.Reason, Valid: true}, Status: "pending", ExecutionPolicy: "manual_review", NextAttemptAt: now, CreatedMonth: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		if err := tx.Table("manual_refund_request").Create(map[string]any{"request_id": in.RequestID, "payload_json": string(payload), "refund_no": refundNo}).Error; err != nil {
			return err
		}
		created = true
		auditPending = []auditEntry{{"request", "refund", record.ID, nil, in, in.RequestID}}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"created": created, "refund_no": refundNo, "request_id": in.RequestID})
}

// refundReview 保存人工退款的双人复核记录，每笔退款至多一条。
// 首签进入 awaiting_second；不同审核人完成第二签后进入 approved。
// snapshot_json 固定首签时的退款要素，第二签必须匹配。
type refundReview struct {
	RefundRecordID uint64  // 关联的退款记录主键 refund_record.id，一对一
	SnapshotJSON   string  // 第一签时冻结的退款要素 JSON，第二签据此比对，内容不一致即冲突
	FirstSigner    uint64  // 第一审核人 ID
	FirstComment   string  // 第一审核人的审核意见
	SecondSigner   *uint64 // 第二审核人 ID，nil 表示第二签还没做
	SecondComment  *string // 第二审核人的审核意见，nil 表示第二签还没做
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

// refundSnapshot 将退款的业务要素编码为规范 JSON，作为双人复核快照。
// 状态、重试次数和时间戳在审核期间可能变化，不参与一致性比较。
func refundSnapshot(r charge.RefundRecord) string {
	b, _ := json.Marshal(gin.H{"refund_no": r.RefundNo, "payment_order_id": r.PaymentOrderID, "user_id": r.UserID, "biz_type": r.BizType, "biz_id": r.BizID, "refund_cents": r.RefundCents})
	return string(b)
}

// reviewRefund 按路由后缀执行通过或驳回，仅接受 pending、manual_review 退款。
// 首签进入 awaiting_second；不同审核人第二签后改为 approved、automatic，并立即安排执行。
// 驳回写入 refund_rejection 并将退款置为 rejected，无需第二签。
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
	status := "rejected"
	var auditPending []auditEntry
	err := a.auditedTransaction(c, a.Store.UserDB, &auditPending, func(tx *gorm.DB) error {
		var r charge.RefundRecord
		if err := tx.Where("refund_no=? AND deleted_at IS NULL", c.Param("refund_no")).Take(&r).Error; err != nil {
			return err
		}
		var pay charge.PaymentOrderRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", r.PaymentOrderID).Take(&pay).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", r.ID).Take(&r).Error; err != nil {
			return err
		}
		if r.Status != "pending" || r.ExecutionPolicy != "manual_review" {
			return errConflict
		}
		var v refundReview
		e := tx.Table("refund_review").Where("refund_record_id=?", r.ID).Take(&v).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		if !approve {
			if err := tx.Table("refund_rejection").Create(map[string]any{"refund_record_id": r.ID, "actor_id": p.ID, "reason": in.Reason}).Error; err != nil {
				return err
			}
			if err := tx.Table("refund_record").Where("id=?", r.ID).Updates(map[string]any{"status": "rejected", "failure_reason": in.Reason}).Error; err != nil {
				return err
			}
		} else {
			if pay.PayMethod != "wechat" || pay.UserID != r.UserID || pay.BizID != r.BizID || pay.BizType != r.BizType || r.RefundCents <= 0 || r.RefundCents > pay.PaidCents-pay.RefundedCents {
				return errConflict
			}
			snapshot := refundSnapshot(r)
			if errors.Is(e, gorm.ErrRecordNotFound) {
				status = "awaiting_second"
				if err := tx.Table("refund_review").Create(map[string]any{"refund_record_id": r.ID, "snapshot_json": snapshot, "first_signer": p.ID, "first_comment": in.ApproveComment}).Error; err != nil {
					return err
				}
			} else {
				var stored any
				_ = json.Unmarshal([]byte(v.SnapshotJSON), &stored)
				b, _ := json.Marshal(stored)
				if v.FirstSigner == p.ID || v.SecondSigner != nil || string(b) != snapshot {
					return errConflict
				}
				first, err := a.Auth.Store.Profile(c.Request.Context(), v.FirstSigner)
				if err != nil || first.Role != "customer_finance" || !hasPermission(first, "order.refund.review") {
					return errConflict
				}
				status = "approved"
				if err := tx.Table("refund_review").Where("refund_record_id=?", r.ID).Updates(map[string]any{"second_signer": p.ID, "second_comment": in.ApproveComment, "approved_at": time.Now().UTC()}).Error; err != nil {
					return err
				}
				if err := tx.Table("refund_record").Where("id=?", r.ID).Updates(map[string]any{"execution_policy": "automatic", "next_attempt_at": time.Now().UTC()}).Error; err != nil {
					return err
				}
			}
		}
		auditPending = []auditEntry{{status, "refund", r.ID, nil, in, ""}}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"review_status": status})
}

// retryRefund 将 automatic、pending/processing 退款的 next_attempt_at 设为当前时间，不修改金额或状态。
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
	var auditPending []auditEntry
	err := a.auditedTransaction(c, a.Store.UserDB, &auditPending, func(tx *gorm.DB) error {
		var r charge.RefundRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("refund_no=? AND deleted_at IS NULL", c.Param("refund_no")).Take(&r).Error; err != nil {
			return err
		}
		if r.ExecutionPolicy != "automatic" || (r.Status != "pending" && r.Status != "processing") {
			return errConflict
		}
		if err := tx.Table("refund_record").Where("id=?", r.ID).Update("next_attempt_at", time.Now().UTC()).Error; err != nil {
			return err
		}
		auditPending = []auditEntry{{"retry", "refund", r.ID, nil, in, ""}}
		return nil
	})
	if err != nil {
		resourceFailure(c, err)
		return
	}
	httpapi.OK(c, gin.H{"scheduled": true})
}
