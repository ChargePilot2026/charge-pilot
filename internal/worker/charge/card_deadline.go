package charge

import (
	"context"
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
		if err := tx.Table("charge_billing_cutoff").Clauses(clause.OnConflict{DoUpdates: clause.Assignments(map[string]any{"charge_order_id": gorm.Expr("charge_order_id")})}).Create(map[string]any{"charge_order_id": id, "cutoff_at": deadline, "reason": "card_duration_exhausted"}).Error; err != nil {
			return err
		}
		stop = true
		return nil
	})
	return stop, err
}
