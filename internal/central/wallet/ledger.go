// 钱包账本原语：行锁读取与余额变动流水。
// 只触及 wallet_account / wallet_txn 两表，供刷卡、结算、启动失败退款等
// 跨家族事务复用；错误语义以本包哨兵表达，调用方按业务映射。
package wallet

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrInactive 表示对非 active 状态钱包出账。
var ErrInactive = errors.New("wallet account is not active for debit")

// ErrInsufficient 表示钱包可用余额（balance - frozen）不足。
var ErrInsufficient = errors.New("wallet spendable balance insufficient")

// Row 是 wallet_account 的行锁投影。
type Row struct {
	ID           uint64
	BalanceCents int64
	FrozenCents  int64
	Status       string
}

// Lock 以 UPDATE 锁读取用户钱包账户，供同事务内先锁钱包再锁支付行。
func Lock(tx *gorm.DB, user uint64) (Row, error) {
	var w Row
	err := tx.Table("wallet_account").Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND deleted_at IS NULL", user).Take(&w).Error
	return w, err
}

// Move 变动钱包余额并记录 wallet_txn 流水：cents 为负出账、正入账，零值不动。
// 出账要求 active 状态且可用余额充足，违约返回 ErrInactive / ErrInsufficient。
func Move(tx *gorm.DB, user uint64, w *Row, cents int64, ref, note string) error {
	if cents < 0 && w.Status != "active" {
		return ErrInactive
	}
	if cents < 0 && w.BalanceCents-w.FrozenCents < -cents {
		return ErrInsufficient
	}
	if cents == 0 {
		return nil
	}
	w.BalanceCents += cents
	direction, biz := "in", "refund"
	amount := cents
	if cents < 0 {
		direction, biz, amount = "out", "pay", -cents
	}
	if err := tx.Table("wallet_account").Where("id=?", w.ID).Updates(map[string]any{"balance_cents": w.BalanceCents, "version": gorm.Expr("version+1")}).Error; err != nil {
		return err
	}
	now := time.Now().UTC()
	return tx.Table("wallet_txn").Create(map[string]any{"txn_no": ref, "user_id": user, "wallet_account_id": w.ID, "direction": direction, "amount_cents": amount, "balance_after_cents": w.BalanceCents, "biz_type": biz, "biz_ref": ref, "note": note, "created_month": time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)}).Error
}
