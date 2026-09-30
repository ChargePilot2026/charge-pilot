const {confirmationLabel}=require('./scheme');
const statuses = { pending_payment: '待支付', paid: '等待设备启动', charging: '充电中', completed: '已完成', finished: '已完成', cancelled: '已取消', failed: '失败', refunding: '退款中', refunded: '已退款' };
const refunds = { none: '无退款', processing: '退款处理中', partial_refunded: '部分退款', refunded: '已全额退款' };
function money(value) { return value == null ? '待结算' : `¥${(value / 100).toFixed(2)}`; }
function formatOrder(order) {
  return { ...order, display:order.display||{}, meter_kwh:order.meter_kwh??order.charged_kwh, payment_order_no:order.payment_order_no??order.payment?.order_no, statusLabel: confirmationLabel(order)||statuses[order.status] || order.status,
    refundLabel: refunds[order.refund_status||order.payment?.status] || order.refund_status || '无退款',
    totalText: money(order.total_fee_cents??order.total_cents), electricText: money(order.electric_fee_cents??order.electric_cents), serviceText: money(order.service_fee_cents??order.service_cents),
    paidText: money(order.paid_fee_cents??order.payment?.paid_cents), refundedText: money(order.refunded_cents??order.payment?.refunded_cents),
    startedText: order.started_at ? order.started_at.replace('T', ' ').replace('.000Z', ' UTC') : '尚未开始',
    endedText: order.ended_at ? order.ended_at.replace('T', ' ').replace('.000Z', ' UTC') : '—',
  };
}
module.exports = { formatOrder };
