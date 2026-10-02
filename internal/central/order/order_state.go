package order

import "gorm.io/gorm"

// PaymentStatusSQL 只供采用固定支付表别名 p 的内部查询使用。
// 金额是到账与已成功退款的凭据；原退款状态但退款金额为零不提前宣称已退款。
const PaymentStatusSQL = "CASE WHEN p.paid_cents>0 AND p.refunded_cents>=p.paid_cents THEN 'refunded' WHEN p.refunded_cents>0 THEN 'partial_refunded' WHEN p.paid_cents>0 OR p.status IN ('paid','partial_refunded','refunded') THEN 'paid' ELSE 'pending' END"

// SettledPaymentStatus 依据已经到账和已成功退款的金额计算展示状态。
// 退款申请或渠道处理中不增加 refunded_cents，因此不会提前变成已退款。
func SettledPaymentStatus(paidCents, refundedCents int64, ledgerStatus string) string {
	if paidCents > 0 && refundedCents >= paidCents {
		return "refunded"
	}
	if refundedCents > 0 {
		return "partial_refunded"
	}
	if paidCents > 0 || ledgerStatus == "paid" || ledgerStatus == "partial_refunded" || ledgerStatus == "refunded" {
		return "paid"
	}
	return "pending"
}

// SyncOrderPaymentStatus 与实际支付/退款金额写入共用事务，不使用隐式 ORM hook。
// 一笔支付可能尚未生成充电订单（例如过期支付自动退款），也可能是钱包充值；
// 这两种情况没有订单资料需要修改。业务状态由数据库的 STORED generated 列维护。
func SyncOrderPaymentStatus(tx *gorm.DB, paymentID uint64) error {
	var payment struct {
		ID            uint64
		BizType       string
		UserID        uint64
		PaymentStatus string
	}
	if err := tx.Table("payment_order p").Select("p.id,p.biz_type,p.user_id,"+PaymentStatusSQL+" AS payment_status").Where("p.id=?", paymentID).Take(&payment).Error; err != nil {
		return err
	}
	if payment.BizType != "charge" {
		return nil
	}
	return tx.Table("charge_order").Where("payment_order_id=? AND user_id=?", payment.ID, payment.UserID).Update("payment_status", payment.PaymentStatus).Error
}
