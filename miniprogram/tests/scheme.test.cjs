const test=require('node:test'),assert=require('node:assert/strict');
const {offerView,confirmationLabel}=require('../utils/scheme');
test('all three configured modes explain the actual rights and refunds',()=>{
 const common={id:1,name:'套餐',price_cents:300};
 assert.match(offerView({...common,mode:'amount',max_minutes:600}).ruleText,/10 小时/);
 assert.match(offerView({...common,mode:'duration',duration_minutes:120}).ruleText,/完整分钟/);
 assert.match(offerView({...common,mode:'energy',energy_wh:3000}).ruleText,/购买 3 度/);
 assert.throws(()=>offerView({...common,mode:'package'}));
});
test('unknown starts, additions and missing meter evidence remain explicit',()=>{
 assert.equal(confirmationLabel({status:'paid'}),'启动确认中');
 assert.equal(confirmationLabel({status:'charging',card_operations:[{kind:'extend',status:'confirming'}]}),'充电中 · 加时确认中');
 assert.equal(confirmationLabel({status:'completed',billing_status:'manual_review'}),'待核对');
});
