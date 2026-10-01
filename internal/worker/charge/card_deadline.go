package charge

import (
	"context"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// Lock the purchased duration before freezing its deadline, in the same order
// as CardStore.Swipe. A stale poll cannot stop a just-extended card order.
func (s AutoStopper) freezeCardDeadline(ctx context.Context, id uint64, now time.Time) (bool, error) {
	stop := false
	err := s.UserDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var card struct{ PurchasedMinutes uint16 }
		if err := tx.Table("card_charge").Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=?", id).Take(&card).Error; err != nil {
			return err
		}
		var order struct {
			StartedAt time.Time
			Status    string
		}
		if err := tx.Table("charge_order").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL", id).Take(&order).Error; err != nil {
			return err
		}
		if order.Status != "charging" {
			return nil
		}
		deadline := order.StartedAt.Add(time.Duration(card.PurchasedMinutes) * time.Minute)
		if now.Before(deadline) {
			return nil
		}
		// 计费截止点按订单主键首写冻结：已有记录表明此前已冻结，
		// 视为冲突返回错误并回滚本事务，交由外层定时循环重试，不做静默空更新。
		var frozen int64
		if err := tx.Table("charge_billing_cutoff").Where("charge_order_id=?", id).Count(&frozen).Error; err != nil {
			return err
		}
		if frozen > 0 {
			return fmt.Errorf("charge billing cutoff already frozen for order %d", id)
		}
		if err := tx.Table("charge_billing_cutoff").Create(map[string]any{"charge_order_id": id, "cutoff_at": deadline, "reason": "card_duration_exhausted"}).Error; err != nil {
			return err
		}
		stop = true
		return nil
	})
	return stop, err
}
