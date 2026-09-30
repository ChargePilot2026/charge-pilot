package charge

import "gorm.io/gorm"

// A persistent mutex serializes wallet and WeChat checkout on the same port.
// Paid/unknown starts hold it until a definitive failure or physical end.
func lockCheckoutPort(tx *gorm.DB, port string) error {
	return tx.Exec("INSERT INTO charge_port_lock(port_code) VALUES (?) ON DUPLICATE KEY UPDATE port_code=VALUES(port_code)", port).Error
}

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
