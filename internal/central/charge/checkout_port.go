package charge

import "gorm.io/gorm"

// checkoutPortAvailable 确认端口上没有未过期支付意图或进行中的订单。
// 并发抢占由 charge_payment_intent.uk_active_port 与 card_charge.active_port
// 唯一键兜底：冲突事务在插入时被拒绝，不会形成双占。
func checkoutPortAvailable(tx *gorm.DB, port string) error {
	var count int64
	err := tx.Table("charge_payment_intent i").Joins("LEFT JOIN charge_order c ON c.id=i.charge_order_id").
		Where("i.port_code=? AND ((i.status='initiated' AND i.expires_at>UTC_TIMESTAMP(3)) OR (i.status='paid' AND c.status IN ('paid','charging')))", port).Count(&count).Error
	if err != nil {
		return err
	}
	if count != 0 {
		return ErrPaymentIntentConflict
	}
	return nil
}
