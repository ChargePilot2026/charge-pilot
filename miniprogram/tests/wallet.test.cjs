const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
function page(name,app){
  let p;vm.runInNewContext(fs.readFileSync(require.resolve('../pages/wallet/'+name+'.js'),'utf8'),{
    getApp:()=>app,Page:value=>p=value,wx:{stopPullDownRefresh(){},navigateTo(){}},
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
