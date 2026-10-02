// 端口占用检查与支付编号分配归属 payment 家族：
// charge_payment_intent 由本家族持有，charge_order 状态经裸表名连接读取，
// 不 import order 包，保持 payment 为可被 order/card 安全依赖的下游包。
package payment

import (
	"strconv"

	"github.com/ChargePilot2026/charge-pilot/internal/platform/snowflake"
	"gorm.io/gorm"
)

// CheckoutPortAvailable 确认端口上没有未过期支付意图或进行中的订单。
// 并发抢占由 charge_payment_intent.uk_active_port 与 card_charge.active_port
// 唯一键兜底：冲突事务在插入时被拒绝，不会形成双占。
func CheckoutPortAvailable(tx *gorm.DB, port string) error {
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

// NewOrderNumber 分配与支付来源和用户注册无关的独立支付单号（雪花序列表）。
func NewOrderNumber(tx *gorm.DB) (string, error) {
	id, err := snowflake.Next(tx)
	if err != nil {
		return "", err
	}
	return "P" + strconv.FormatUint(id, 10), nil
}
