// Package wallet 实现钱包退款的预占、结算与释放。
// 钱包表（wallet_account / wallet_txn / wallet_refund_part）的写入
// 集中在退款事务内完成；本包不依赖 charge 的记录类型，
// 跨表事实一律经局部行投影或裸列名传递。
package wallet

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrConflict 表示钱包退款的身份或金额冲突（预占重复、余额不足等）。
var ErrConflict = errors.New("wallet refund identity or amount conflict")

// Settlement 是退款结算事务传给钱包侧的最小事实集合。
type Settlement struct {
	ID          uint64
	UserID      uint64
	RefundNo    string
	BizType     string
	RefundCents int64
}

// 钱包退款的预占与渠道回单原子地一起结算。
// 所有地方都是先锁钱包行再锁支付行，以避免加锁顺序反转。
func LockRefundWallet(tx *gorm.DB, s Settlement) error {
	if s.BizType != "wallet_recharge" {
		return nil
	}
	var part struct {
		WalletAccountID uint64
		AmountCents     int64
	}
	if err := tx.Table("wallet_refund_part").Where("refund_record_id=?", s.ID).Take(&part).Error; err != nil {
		return err
	}
	if part.AmountCents != s.RefundCents {
		return ErrConflict
	}
	var account struct{ ID uint64 }
	return tx.Table("wallet_account").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=? AND deleted_at IS NULL", part.WalletAccountID, s.UserID).Take(&account).Error
}

func SettleRefundWallet(tx *gorm.DB, s Settlement) error {
	if s.BizType != "wallet_recharge" {
		return nil
	}
	var part struct {
		WalletAccountID uint64
		AmountCents     int64
		Settled         bool
	}
	if err := tx.Table("wallet_refund_part").Where("refund_record_id=?", s.ID).Take(&part).Error; err != nil {
		return err
	}
	if part.Settled || part.AmountCents != s.RefundCents {
		return ErrConflict
	}
	var w struct{ BalanceCents, FrozenCents int64 }
	if err := tx.Table("wallet_account").Where("id=? AND user_id=?", part.WalletAccountID, s.UserID).Take(&w).Error; err != nil {
		return err
	}
	if w.BalanceCents < s.RefundCents || w.FrozenCents < s.RefundCents {
		return ErrConflict
	}
	if err := tx.Table("wallet_account").Where("id=?", part.WalletAccountID).Updates(map[string]any{"balance_cents": gorm.Expr("balance_cents-?", s.RefundCents), "frozen_cents": gorm.Expr("frozen_cents-?", s.RefundCents), "version": gorm.Expr("version+1")}).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := tx.Table("wallet_txn").Create(map[string]any{"txn_no": "RF" + s.RefundNo, "user_id": s.UserID, "wallet_account_id": part.WalletAccountID, "direction": "out", "amount_cents": s.RefundCents, "balance_after_cents": w.BalanceCents - s.RefundCents, "biz_type": "refund", "biz_ref": s.RefundNo, "note": "provider refund succeeded", "created_month": time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)}).Error; err != nil {
		return err
	}
	return tx.Table("wallet_refund_part").Where("refund_record_id=?", s.ID).Update("settled", true).Error
}

// ReleaseFailedRefund 在渠道确认终态失败时释放退款预占；结果未知时保留预占，等待对账。
func ReleaseFailedRefund(tx *gorm.DB, s Settlement) error {
	if s.BizType != "wallet_recharge" {
		return nil
	}
	var part struct {
		WalletAccountID uint64
		AmountCents     int64
		Settled         bool
	}
	if err := tx.Table("wallet_refund_part").Where("refund_record_id=?", s.ID).Take(&part).Error; err != nil {
		return err
	}
	if part.Settled || part.AmountCents != s.RefundCents {
		return ErrConflict
	}
	result := tx.Table("wallet_account").Where("id=? AND user_id=? AND frozen_cents>=?", part.WalletAccountID, s.UserID, s.RefundCents).Updates(map[string]any{"frozen_cents": gorm.Expr("frozen_cents-?", s.RefundCents), "version": gorm.Expr("version+1")})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrConflict
	}
	return tx.Table("wallet_refund_part").Where("refund_record_id=?", s.ID).Update("settled", true).Error
}
