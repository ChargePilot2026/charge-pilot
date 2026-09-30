package charge

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 钱包退款的预占与渠道回单原子地一起结算。
// 所有地方都是先锁钱包行再锁支付行，以避免加锁顺序反转。
func lockRefundWallet(tx *gorm.DB, r RefundRecord) error {
	if r.BizType != "wallet_recharge" {
		return nil
	}
	var part struct {
		WalletAccountID uint64
		AmountCents     int64
	}
	if err := tx.Table("wallet_refund_part").Where("refund_record_id=?", r.ID).Take(&part).Error; err != nil {
		return err
	}
	if part.AmountCents != r.RefundCents {
		return ErrRefundConflict
	}
	var wallet struct{ ID uint64 }
	return tx.Table("wallet_account").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=? AND deleted_at IS NULL", part.WalletAccountID, r.UserID).Take(&wallet).Error
}
func settleRefundWallet(tx *gorm.DB, r RefundRecord) error {
	if r.BizType != "wallet_recharge" {
		return nil
	}
	var part struct {
		WalletAccountID uint64
		AmountCents     int64
		Settled         bool
	}
	if err := tx.Table("wallet_refund_part").Where("refund_record_id=?", r.ID).Take(&part).Error; err != nil {
		return err
	}
	if part.Settled || part.AmountCents != r.RefundCents {
		return ErrRefundConflict
	}
	var w struct{ BalanceCents, FrozenCents int64 }
	if err := tx.Table("wallet_account").Where("id=? AND user_id=?", part.WalletAccountID, r.UserID).Take(&w).Error; err != nil {
		return err
	}
	if w.BalanceCents < r.RefundCents || w.FrozenCents < r.RefundCents {
		return ErrRefundConflict
	}
	if err := tx.Table("wallet_account").Where("id=?", part.WalletAccountID).Updates(map[string]any{"balance_cents": gorm.Expr("balance_cents-?", r.RefundCents), "frozen_cents": gorm.Expr("frozen_cents-?", r.RefundCents), "version": gorm.Expr("version+1")}).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := tx.Table("wallet_txn").Create(map[string]any{"txn_no": "RF" + r.RefundNo, "user_id": r.UserID, "wallet_account_id": part.WalletAccountID, "direction": "out", "amount_cents": r.RefundCents, "balance_after_cents": w.BalanceCents - r.RefundCents, "biz_type": "refund", "biz_ref": r.RefundNo, "note": "provider refund succeeded", "created_month": time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)}).Error; err != nil {
		return err
	}
	return tx.Table("wallet_refund_part").Where("refund_record_id=?", r.ID).Update("settled", true).Error
}

// 渠道明确返回终态失败时把预占退回钱包；
// 响应未知时则刻意继续预占，直到对账为止。
func releaseFailedWalletRefund(tx *gorm.DB, r RefundRecord) error {
	if r.BizType != "wallet_recharge" {
		return nil
	}
	var part struct {
		WalletAccountID uint64
		AmountCents     int64
		Settled         bool
	}
	if err := tx.Table("wallet_refund_part").Where("refund_record_id=?", r.ID).Take(&part).Error; err != nil {
		return err
	}
	if part.Settled || part.AmountCents != r.RefundCents {
		return ErrRefundConflict
	}
	result := tx.Table("wallet_account").Where("id=? AND user_id=? AND frozen_cents>=?", part.WalletAccountID, r.UserID, r.RefundCents).Updates(map[string]any{"frozen_cents": gorm.Expr("frozen_cents-?", r.RefundCents), "version": gorm.Expr("version+1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRefundConflict
	}
	return tx.Table("wallet_refund_part").Where("refund_record_id=?", r.ID).Update("settled", true).Error
}
