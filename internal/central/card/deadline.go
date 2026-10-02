// 刷卡时长截止冻结：归属 card 家族（读写 card_charge / charge_billing_cutoff）。
// charge 侧 AutoStopper 以薄方法委托本函数，保持方法接收者与类型的同包约束。
package card

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FreezeDeadline 锁定已购时长对应的计费截止点：仍在充电且截止点已到则写入
// charge_billing_cutoff（按订单主键首写冻结，重复冻结返回冲突错误并回滚，
// 交由外层定时循环重试），并返回是否应触发停机。
// 加锁顺序与 Swipe 一致：先 card_charge 会话行，后 charge_order 行。
func FreezeDeadline(ctx context.Context, db *gorm.DB, id uint64, now time.Time) (bool, error) {
	stop := false
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var session struct{ PurchasedMinutes uint16 }
		if err := tx.Table("card_charge").Clauses(clause.Locking{Strength: "UPDATE"}).Where("charge_order_id=?", id).Take(&session).Error; err != nil {
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
		deadline := order.StartedAt.Add(time.Duration(session.PurchasedMinutes) * time.Minute)
		if now.Before(deadline) {
			return nil
		}
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
