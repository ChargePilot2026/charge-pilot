const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { createRequire } = require('node:module');

function page(name, app, wxOverride={}) {
  let definition;
  const filename = path.resolve(__dirname, '../pages/charge', name + '.js');
  const context = { getApp: () => app, Page: value => { definition = value; }, require: createRequire(filename), wx: { stopPullDownRefresh() {}, navigateTo() {},...wxOverride } };
  vm.runInNewContext(fs.readFileSync(filename,'utf8'), context, { filename });
  const instance = { ...definition, data: JSON.parse(JSON.stringify(definition.data)), _generation: 0, _gone: false };
  instance.setData = data => Object.assign(instance.data, data);
  return instance;
}
const item = id => ({ order_id: id, order_no: 'ORDER_'+id, status: 'completed', total_fee_cents: 55, refund_status: 'none' });
test('detail resumes original checkout and displays server amount before payment',async()=>{
 const calls=[],urls=[];let confirmation='',paid=0;
 const app={globalData:{token:'test'},request:async(method,url)=>{calls.push({method,url});return {order_no:'ORDER_1',amount_cents:123,hold_expires_at:new Date(Date.now()+60000).toISOString(),payment_params:{timeStamp:'123',nonceStr:'nonce',package:'prepay_id=saved',signType:'RSA',paySign:'signature'}};}};
 const p=page('detail',app,{showModal:o=>{confirmation=o.content;o.success({confirm:true});},requestPayment:o=>{paid++;o.success({});},navigateTo:o=>urls.push(o.url)});
 p.data.order={...item(1),status:'pending_payment'};await p.pay();assert.match(confirmation,/1.23/);assert.equal(calls[0].url,'/user/charge/ORDER_1/prepay');assert.equal(paid,1);assert.equal(urls[0],'/pages/charge/charging?order_no=ORDER_1');
});

test('history paginates and retains previous page on a recoverable load error', async () => {
  let fail = false;
  const app = { globalData: { token: 'test' }, request: async (_,__,query) => {
    if (fail) throw new Error('连接失败');
    return { items: [item(query.page)], total: 2 };
  }};
  const p = page('history',app);
  await p.load(true); fail = true; await p.load(false);
  assert.equal(p.data.page,1); assert.equal(p.data.items.length,1); assert.equal(p.data.error,'连接失败');
  fail=false; await p.load(false);
  assert.equal(p.data.page,2); assert.equal(p.data.items.length,2); assert.equal(p.data.items[0].totalText,'¥0.55');
});
test('a stale response cannot replace a newer status filter', async () => {
  const pending=[];
  const p=page('history',{globalData:{token:'test'},request:()=>new Promise(resolve=>pending.push(resolve))});
  const first=p.load(true); p.data.statusIndex=2; const second=p.load(true);
  pending[1]({items:[item(2)],total:1}); await second;
  pending[0]({items:[item(1)],total:1}); await first;
  assert.equal(p.data.items[0].order_id,2);
});
test('logged out history does not request protected orders', async () => {
  const p=page('history',{globalData:{token:''},request:()=>{throw new Error('unexpected request');}});
  await p.load(true); assert.equal(p.data.needsLogin,true); assert.equal(p.data.items.length,0);
});
test('detail clears old data on errors and ignores results after unload', async () => {
  const pending=[];
  const p=page('detail',{globalData:{token:'test'},request:()=>new Promise((resolve,reject)=>pending.push({resolve,reject}))});
  p._orderId='123'; const first=p.load(); pending[0].resolve(item(123)); await first;
  assert.equal(p.data.order.order_id,123);
  const second=p.load(); pending[1].reject(new Error('订单不存在')); await second;
  assert.equal(p.data.order,null); assert.equal(p.data.error,'订单不存在');
  const third=p.load(); p.onUnload(); pending[2].resolve(item(123)); await third;
  assert.equal(p.data.order,null);
});
