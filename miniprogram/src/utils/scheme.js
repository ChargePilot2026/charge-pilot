const money = cents => '¥' + (cents / 100).toFixed(2);
function offerView(o) {
 const modeLabel = {amount:'金额',duration:'时长',energy:'电量'}[o.mode];
 if (!modeLabel || !Number.isSafeInteger(o.price_cents) || o.price_cents <= 0) throw new Error('支付套餐无效，请刷新');
 const ruleText = o.mode === 'amount'
  ? `预付金额作为预算，余额用尽或最长 ${o.max_minutes / 60} 小时停止；提前结束退还未消费金额。`
  : o.mode === 'duration' ? `购买 ${o.duration_minutes} 分钟；提前结束按设备确认的完整分钟消费，向下取分，剩余退款。`
  : `购买 ${o.energy_wh / 1000} 度；提前结束按实际电量和冻结单价消费，向下取分，剩余退款。`;
 return {...o,modeLabel,priceText:money(o.price_cents),ruleText};
}
function confirmationLabel(o){
 if(o.billing_status === 'manual_review')return '待核对';
 if(o.card_operations?.some(p=>p.kind==='extend'&&p.status==='confirming'))return '充电中 · 加时确认中';
 if(o.start_confirmation_pending || o.status==='paid')return '启动确认中';
 return '';
}
export {offerView,confirmationLabel};
