const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
function page(name,app,wxOverrides={}){
  let p;vm.runInNewContext(fs.readFileSync(require.resolve('../pages/wallet/'+name+'.js'),'utf8'),{
    getApp:()=>app,Page:value=>p=value,wx:{stopPullDownRefresh(){},navigateTo(){},...wxOverrides},
  });
  p._generation=0;p._gone=false;p.setData=value=>Object.assign(p.data,value);return p;
}
const txn=(id,amount)=>({txn_no:String(id),txn_type:'consume',amount_cents:amount,balance_after_cents:4123,created_at:'2026-09-26T00:00:00Z'});
test('wallet failures are visible rather than a zero balance',async()=>{
  let fail=false;
  const p=page('wallet',{globalData:{token:'access'},request:async()=>{if(fail)throw Error('offline');return {available_cents:4123,frozen_cents:100,status:'active'};}});
  await p.load();assert.equal(p.data.wallet.availableText,'41.23');
  fail=true;await p.load();assert.equal(p.data.wallet,null);assert.equal(p.data.error,'offline');
});
test('ledger load-more retry retains successful page and sends the chosen filter',async()=>{
  let fail=false;let query;
  const p=page('txns',{globalData:{token:'access'},request:async(_,__,q)=>{query=q;if(fail)throw Error('offline');return {total:2,items:[txn(q.page,-877)]};}});
  p.data.statusIndex=2;await p.load(true);assert.equal(query.type,'consume');assert.equal(p.data.items[0].amountText,'-8.77');
  fail=true;await p.load(false);assert.equal(p.data.page,1);assert.equal(p.data.items.length,1);
  fail=false;await p.load(false);assert.equal(p.data.page,2);assert.equal(p.data.items.length,2);
});
test('logout invalidates a pending ledger load',async()=>{
  let resolve;const app={globalData:{token:'access'},request:()=>new Promise(r=>resolve=r)};
  const p=page('txns',app);const pending=p.load(true);app.globalData.token='';await p.load(true);
  resolve({items:[txn(1,5000)],total:1});await pending;
  assert.equal(p.data.needsLogin,true);assert.equal(p.data.items.length,0);
});

function refundFixture(){
  const storage=new Map(),posts=[];
  const app={_generation:1,globalData:{token:'access'},request:async(method,path,body)=>{
    if(method==='GET')return {user_id:'7',items:[],total:0};
    posts.push(JSON.parse(JSON.stringify(body)));
    if(app.failure)throw app.failure;
    return {request_id:body.request_id,status:'accepted'};
  }};
  const wx={getStorageSync:key=>storage.get(key),setStorageSync:(key,value)=>storage.set(key,JSON.parse(JSON.stringify(value))),removeStorageSync:key=>storage.delete(key),showModal:options=>options.success({confirm:true})};
  return {app,wx,storage,posts,open:()=>page('refund',app,wx)};
}
test('refund retry survives page recreation and reuses the exact original request',async()=>{
  const f=refundFixture();let p=f.open();await p.load();p.data.amount='12.34';p.data.reason='余额退款';
  f.app.failure=Error('offline');await p.submit();assert.equal(p.data.pendingRetry,true);assert.equal(f.posts.length,1);
  p.onUnload();p=f.open();await p.onShow();assert.equal(p.data.amount,'12.34');
  p.inputAmount({detail:{value:'99'}});assert.equal(p.data.amount,'12.34');
  f.app.failure=null;await p.submit();assert.deepEqual(f.posts[1],f.posts[0]);
  assert.equal(f.posts[0].amount_cents,1234);assert.equal(f.storage.size,0);assert.equal(p.data.pendingRetry,false);assert.match(p.data.notice,/已受理/);
});
test('refund business rejection remains visible and storage failure cannot send an unrecorded request',async()=>{
  const f=refundFixture();const p=f.open();await p.load();p.data.amount='10';
  f.app.failure=Object.assign(Error('余额不足'),{status:400});await p.submit();
  assert.equal(p.data.error,'余额不足');assert.equal(p.data.pendingRetry,false);assert.equal(f.storage.size,0);
  f.wx.setStorageSync=()=>{throw Error('storage full');};const q=f.open();await q.load();q.data.amount='5';
  await q.submit();assert.equal(f.posts.length,1);assert.equal(q._pending,null);assert.equal(q.data.error,'storage full');
});
test('refund validation and cancelled confirmation never send a request',async()=>{
  const f=refundFixture();f.wx.showModal=options=>options.success({confirm:false});const p=f.open();await p.load();
  for(const amount of ['0','-1','1.001','Infinity','1000000.01']){p.data.amount=amount;await p.submit();assert.match(p.data.error,/有效退款金额/);}
  p.data.amount='1';await p.submit();assert.equal(f.posts.length,0);assert.equal(f.storage.size,0);
});
test('refund account changes clear private form and ignore a late list response',async()=>{
  const f=refundFixture();const p=f.open();await p.load();p.data.amount='23';p.data.notice='private';
  let reject;f.app.request=()=>new Promise((_,r)=>reject=r);const loading=p.load();
  f.app._generation++;f.app.globalData.token='';await p.load();reject(Error('old account error'));await loading;
  assert.equal(p.data.amount,'');assert.equal(p.data.notice,'');assert.equal(p.data.error,'');assert.equal(p.data.needsLogin,true);assert.equal(p.data.loading,false);
});
test('refund submission is single flight while confirmation is pending',async()=>{
  const f=refundFixture();let confirm;f.wx.showModal=options=>confirm=options.success;
  const p=f.open();await p.load();p.data.amount='1';const submitting=p.submit();await p.submit();
  confirm({confirm:true});await submitting;assert.equal(f.posts.length,1);
});

test('wallet refund list exposes risk rejection and its review reason',async()=>{
 const p=page('refund',{_generation:1,globalData:{token:'access'},request:async()=>({user_id:'7',total:1,items:[{request_id:'r',amount_cents:100,refunded_cents:0,status:'rejected',review:{comment:'核实后拒绝'},refund_orders:[]}]})},{getStorageSync(){}});
 await p.onShow();assert.equal(p.data.items[0].statusText,'审核已拒绝');assert.equal(p.data.items[0].review.comment,'核实后拒绝');
});
