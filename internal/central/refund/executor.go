package refund

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ChargePilot2026/charge-pilot/internal/central/eventoutbox"
	orderpkg "github.com/ChargePilot2026/charge-pilot/internal/central/order"
	"strings"
	"time"

	"github.com/ChargePilot2026/charge-pilot/internal/central/channel"
	"github.com/ChargePilot2026/charge-pilot/internal/central/wallet"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrRefundConflict = errors.New("refund identity or amount conflict")

// walletSettlement 把退款行映射为钱包结算的最小事实集合。
func walletSettlement(r RefundRecord) wallet.Settlement {
	return wallet.Settlement{ID: r.ID, UserID: r.UserID, RefundNo: r.RefundNo, BizType: r.BizType, RefundCents: r.RefundCents}
}

// paymentOrderRow 是 claim/apply 在 payment_order 行锁内需要的最小投影。
// 跨家族类型不得穿透：payment→refund 依赖已单向占用。
type paymentOrderRow struct {
	ID                  uint64
	OrderNo             string
	UserID              uint64
	BizID               uint64
	BizType             string
	PayMethod           string
	WechatTransactionID sql.NullString
	TotalCents          int64
	PaidCents           int64
	RefundedCents       int64
	Status              string
}

// RefundExecutor 只派发明确属于自动充电退款的请求。
// 结果未知的退款保持 processing 状态，每次幂等重试之前都先查询一次。
type RefundExecutor struct {
	DB           *gorm.DB
	Provider     channel.RefundProvider
	ProviderName string
}

func (e RefundExecutor) Batch(ctx context.Context) (int, error) {
	if e.Provider == nil {
		return 0, nil
	}
	var rows []RefundRecord
	if err := e.DB.WithContext(ctx).Where("execution_policy='automatic' AND biz_type IN ('charge','wallet_recharge') AND status IN ('pending','processing') AND next_attempt_at <= UTC_TIMESTAMP(3) AND deleted_at IS NULL").Order("next_attempt_at,id").Limit(20).Find(&rows).Error; err != nil {
		return 0, err
	}
	count := 0
	var first error
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		if err := e.Execute(ctx, row.ID); err != nil {
			if first == nil {
				first = err
			}
		} else {
			count++
		}
	}
	return count, first
}
func (e RefundExecutor) Execute(ctx context.Context, id uint64) error {
	if e.Provider == nil || e.ProviderName == "" {
		return errors.New("refund provider unavailable")
	}
	r, request, claimed, err := e.claim(ctx, id)
	if err != nil || !claimed {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := e.Provider.QueryRefund(callCtx, request)
	if errors.Is(err, channel.ErrRefundNotFound) {
		result, err = e.Provider.CreateRefund(callCtx, request)
	}
	if err != nil {
		// 只落库脱敏后的诊断信息，绝不落 SDK 响应体或凭据。
		_ = e.DB.WithContext(ctx).Model(&RefundRecord{}).Where("id = ? AND status='processing'", id).Update("failure_reason", "provider result unknown; query scheduled").Error
		return fmt.Errorf("refund %s: provider result unknown", r.RefundNo)
	}
	return e.apply(ctx, r, request, result)
}
func (e RefundExecutor) claim(ctx context.Context, id uint64) (RefundRecord, channel.RefundRequest, bool, error) {
	var r RefundRecord
	var request channel.RefundRequest
	claimed := false
	err := e.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先在支付行上串行化金额，再去锁单条请求。
		if err := tx.Where("id = ? AND deleted_at IS NULL", id).Take(&r).Error; err != nil {
			return err
		}
		if err := wallet.LockRefundWallet(tx, walletSettlement(r)); err != nil {
			return err
		}
		// 跨家族读：payment_order 归属 payment 家族，行投影 + 行锁，refund 不得反向依赖 payment。
		var p paymentOrderRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("payment_order").Where("id = ? AND deleted_at IS NULL", r.PaymentOrderID).Take(&p).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&r).Error; err != nil {
			return err
		}
		if r.Status == "success" {
			return nil
		}
		if r.ExecutionPolicy != "automatic" || (r.BizType != "charge" && r.BizType != "wallet_recharge") || (r.Status != "pending" && r.Status != "processing") {
			return ErrRefundConflict
		}
		if r.NextAttemptAt.After(time.Now().UTC()) {
			return nil
		}
		if p.UserID != r.UserID || p.BizID != r.BizID || p.BizType != r.BizType || p.PayMethod != "wechat" || !p.WechatTransactionID.Valid || p.WechatTransactionID.String == "" || p.PaidCents != p.TotalCents || p.RefundedCents < 0 || r.RefundCents <= 0 || r.RefundCents > p.PaidCents-p.RefundedCents || (p.Status != "paid" && p.Status != "partial_refunded") {
			return ErrRefundConflict
		}
		if r.BizType == "charge" {
			var prepay struct {
				ParamsJSON []byte
			}
			if err := tx.Table("charge_prepay").Where("payment_order_id = ?", p.ID).Take(&prepay).Error; err != nil {
				return err
			}
			var params channel.PrepayParams
			if json.Unmarshal(prepay.ParamsJSON, &params) != nil || params.Provider != e.ProviderName {
				return ErrRefundConflict
			}
		} else {
			// 模拟器的交易绝不能发到真实商户，
			// 真实渠道的交易也绝不能让模拟器去结算。
			simulated := strings.HasPrefix(p.WechatTransactionID.String, "SIM")
			if (e.ProviderName == "simulation") != simulated {
				return ErrRefundConflict
			}
		}

		var reserved int64
		if err := tx.Model(&RefundRecord{}).Select("COALESCE(SUM(refund_cents),0)").Where("payment_order_id = ? AND id <> ? AND status IN ('pending','processing') AND deleted_at IS NULL", p.ID, r.ID).Scan(&reserved).Error; err != nil {
			return err
		}
		if reserved > p.PaidCents-p.RefundedCents-r.RefundCents {
			return ErrRefundConflict
		}
		request = channel.RefundRequest{RefundNo: r.RefundNo, MerchantOrderNo: p.OrderNo, TransactionID: p.WechatTransactionID.String, TotalCents: p.TotalCents, RefundCents: r.RefundCents}
		// 这把持久化的租约同时提供了崩溃之后的有界指数退避重试。
		delay := time.Duration(30*(1<<min(r.RetryCount, uint32(7)))) * time.Second
		update := tx.Model(&RefundRecord{}).Where("id = ?", r.ID).Updates(map[string]any{"status": "processing", "retry_count": gorm.Expr("retry_count+1"), "next_attempt_at": time.Now().UTC().Add(delay)})
		if update.Error != nil {
			return update.Error
		}
		claimed = true
		return nil
	})
	return r, request, claimed, err
}
func (e RefundExecutor) apply(ctx context.Context, r RefundRecord, request channel.RefundRequest, result channel.RefundResult) error {
	if result.RefundNo != request.RefundNo || result.MerchantOrderNo != request.MerchantOrderNo || result.TransactionID != request.TransactionID || result.TotalCents != request.TotalCents || result.RefundCents != request.RefundCents || result.RefundID == "" || len(result.RefundID) > 64 {
		return ErrRefundConflict
	}
	switch result.Status {
	case "PROCESSING":
		return nil
	case "CLOSED", "ABNORMAL":
		return e.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := wallet.LockRefundWallet(tx, walletSettlement(r)); err != nil {
				return err
			}
			var current RefundRecord
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", r.ID).Take(&current).Error; err != nil {
				return err
			}
			if current.Status == "failed" {
				return nil
			}
			if current.Status != "processing" {
				return ErrRefundConflict
			}
			if err := wallet.ReleaseFailedRefund(tx, walletSettlement(r)); err != nil {
				return err
			}
			return tx.Model(&RefundRecord{}).Where("id=?", r.ID).Updates(map[string]any{"status": "failed", "wechat_refund_id": result.RefundID, "failure_reason": "provider refund " + result.Status}).Error
		})
	case "SUCCESS":
		if result.SuccessAt.IsZero() {
			return ErrRefundConflict
		}
	default:
		return ErrRefundConflict
	}
	return e.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := wallet.LockRefundWallet(tx, walletSettlement(r)); err != nil {
			return err
		}
		var p paymentOrderRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("payment_order").Where("id = ?", r.PaymentOrderID).Take(&p).Error; err != nil {
			return err
		}
		var current RefundRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", r.ID).Take(&current).Error; err != nil {
			return err
		}
		var receipt struct {
			RefundRecordID uint64
			WechatRefundID string
		}
		err := tx.Table("refund_success_receipt").Where("refund_record_id = ?", r.ID).Take(&receipt).Error
		if err == nil {
			if current.Status != "success" || receipt.WechatRefundID != result.RefundID {
				return ErrRefundConflict
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if current.Status != "processing" || current.RefundCents != result.RefundCents || p.TotalCents != result.TotalCents || p.WechatTransactionID.String != result.TransactionID || p.RefundedCents < 0 || result.RefundCents > p.PaidCents-p.RefundedCents {
			return ErrRefundConflict
		}
		if err := tx.Table("refund_success_receipt").Create(map[string]any{"refund_record_id": r.ID, "wechat_refund_id": result.RefundID}).Error; err != nil {
			return err
		}
		total := p.RefundedCents + result.RefundCents
		status := "partial_refunded"
		if total == p.PaidCents {
			status = "refunded"
		}
		if err := tx.Table("payment_order").Where("id = ?", p.ID).Updates(map[string]any{"refunded_cents": total, "status": status}).Error; err != nil {
			return err
		}
		if err := orderpkg.SyncOrderPaymentStatus(tx, p.ID); err != nil {
			return err
		}
		if err := tx.Model(&RefundRecord{}).Where("id = ?", r.ID).Updates(map[string]any{"status": "success", "wechat_refund_id": result.RefundID, "completed_at": result.SuccessAt.UTC(), "failure_reason": nil}).Error; err != nil {
			return err
		}
		if err := wallet.SettleRefundWallet(tx, walletSettlement(r)); err != nil {
			return err
		}
		if r.BizType == "charge" && r.BizID != 0 {
			if status == "refunded" {
				if err := tx.Model(&orderpkg.ChargeOrderRecord{}).Where("id = ? AND payment_order_id = ? AND status='refunding'", r.BizID, p.ID).Update("status", "refunded").Error; err != nil {
					return err
				}
			} else {
				var pending int64
				if err := tx.Model(&RefundRecord{}).Where("payment_order_id=? AND status IN ('pending','processing') AND deleted_at IS NULL", p.ID).Count(&pending).Error; err != nil {
					return err
				}
				if pending == 0 {
					if err := tx.Model(&orderpkg.ChargeOrderRecord{}).Where("id=? AND payment_order_id=? AND status='refunding' AND ended_at IS NOT NULL", r.BizID, p.ID).Update("status", "completed").Error; err != nil {
						return err
					}
				}
			}
		}
		digest := sha256.Sum256([]byte("refund-success:" + r.RefundNo))
		eventID := "RS" + hex.EncodeToString(digest[:16])
		body, err := json.Marshal(map[string]any{"event_id": eventID, "refund_no": r.RefundNo, "payment_order_id": p.ID, "charge_order_id": r.BizID, "user_id": r.UserID, "refund_cents": result.RefundCents, "provider_refund_id": result.RefundID})
		if err != nil {
			return err
		}
		return tx.Create(&eventoutbox.EventOutboxRecord{EventID: eventID, Stream: "refund_succeeded_stream", EnvelopeJSON: body}).Error
	})
}
