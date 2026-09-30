package charge

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strconv"
	"time"
)

// WalletRefundReservation freezes spendable funds and splits a refund across
// the original paid recharge transactions in the caller's transaction.
type WalletRefundReservation struct {
	RequestID   string
	UserID      uint64
	AmountCents int64
	Reason      string
}
type refundWalletReservation struct {
	ID, UserID, Version       uint64
	BalanceCents, FrozenCents int64
	Status                    string
}

func ReserveWalletRefund(tx *gorm.DB, req WalletRefundReservation) error {
	var w refundWalletReservation
	if err := tx.Table("wallet_account").Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND deleted_at IS NULL", req.UserID).Take(&w).Error; err != nil {
		return err
	}
	if req.AmountCents <= 0 || w.BalanceCents < w.FrozenCents || req.AmountCents > w.BalanceCents-w.FrozenCents {
		return ErrRefundConflict
	}
	var existing int64
	if err := tx.Table("wallet_refund_part").Where("request_id=?", req.RequestID).Count(&existing).Error; err != nil {
		return err
	}
	if existing != 0 {
		return ErrRefundConflict
	}
	var payments []PaymentOrderRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND biz_type='wallet_recharge' AND pay_method='wechat' AND status IN ('paid','partial_refunded') AND deleted_at IS NULL", req.UserID).Order("id").Find(&payments).Error; err != nil {
		return err
	}
	left := req.AmountCents
	now := time.Now().UTC()
	for _, pay := range payments {
		if !pay.WechatTransactionID.Valid || pay.PaidCents != pay.TotalCents {
			continue
		}
		var reserved int64
		if err := tx.Table("refund_record").Select("COALESCE(SUM(refund_cents),0)").Where("payment_order_id=? AND status IN ('pending','processing') AND deleted_at IS NULL", pay.ID).Scan(&reserved).Error; err != nil {
			return err
		}
		amount := min(left, pay.PaidCents-pay.RefundedCents-reserved)
		if amount <= 0 {
			continue
		}
		sum := sha256.Sum256([]byte(req.RequestID + ":" + strconv.FormatUint(pay.ID, 10)))
		r := RefundRecord{RefundNo: "WR" + hex.EncodeToString(sum[:16]), PaymentOrderID: pay.ID, UserID: req.UserID, BizType: "wallet_recharge", BizID: pay.BizID, RefundCents: amount, Reason: sql.NullString{String: req.Reason, Valid: true}, Status: "pending", ExecutionPolicy: "automatic", NextAttemptAt: now, CreatedMonth: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)}
		if err := tx.Create(&r).Error; err != nil {
			return err
		}
		if err := tx.Table("wallet_refund_part").Create(map[string]any{"refund_record_id": r.ID, "request_id": req.RequestID, "wallet_account_id": w.ID, "amount_cents": amount, "settled": false}).Error; err != nil {
			return err
		}
		left -= amount
		if left == 0 {
			break
		}
	}
	if left != 0 {
		return ErrRefundConflict
	}
	if err := tx.Table("wallet_account").Where("id=?", w.ID).Updates(map[string]any{"frozen_cents": gorm.Expr("frozen_cents+?", req.AmountCents), "version": gorm.Expr("version+1")}).Error; err != nil {
		return err
	}
	return nil
}
