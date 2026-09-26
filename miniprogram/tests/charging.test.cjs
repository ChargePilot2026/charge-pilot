const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');const path=require('node:path');const vm=require('node:vm');const {createRequire}=require('node:module');
function setup(app){
 let definition;const timers=new Map();let id=0;const navigations=[];
 const filename=path.resolve(__dirname,'../pages/charge/charging.ts');
 vm.runInNewContext(fs.readFileSync(filename,'utf8'),{getApp:()=>app,Page:v=>definition=v,require:createRequire(filename),setTimeout:(fn,ms)=>{timers.set(++id,{fn,ms});return id;},clearTimeout:key=>timers.delete(key),wx:{stopPullDownRefresh(){},showModal:opts=>opts.success({confirm:true}),navigateTo:v=>navigations.push(v.url)}},{filename});
 const p={...definition,data:structuredClone(definition.data)};p.setData=v=>Object.assign(p.data,v);p.onLoad({order_no:'ORD:1'});return {p,timers,navigations};
}
const snap=status=>({order_id:1,order_no:'ORD:1',status,poll_continue:['paid','charging','pending_payment'].includes(status),next_poll_after_ms:5000,current_power_w:null,current_fee_cents:null,elapsed_seconds:null});
test('charging page displays pending startup without fabricated telemetry and stops polling at completion',async()=>{
 let state='paid';const {p,timers,navigations}=setup({globalData:{token:'t'},request:async()=>snap(state)});
 await p.onShow();assert.equal(p.data.snapshot.statusLabel,'等待设备启动');assert.equal(p.data.snapshot.powerText,'暂无数据');assert.equal(timers.size,1);
 state='completed';await p.load();assert.equal(timers.size,0);assert.equal(p.data.snapshot.statusLabel,'已完成');p.detail();assert.equal(navigations[0],'/pages/charge/detail?order_id=ORD%3A1');
});
test('hidden charging page rejects late responses and clears polling timers',async()=>{
 const pending=[];const {p,timers}=setup({globalData:{token:'t'},request:()=>new Promise(resolve=>pending.push(resolve))});
 const first=p.onShow();p.onHide();pending[0](snap('charging'));await first;assert.equal(p.data.snapshot,null);assert.equal(timers.size,0);
 const second=p.onShow();pending[1](snap('charging'));await second;assert.equal(timers.size,1);p.onUnload();assert.equal(timers.size,0);
});
test('snapshot errors pause polling and allow retry; logout clears private state',async()=>{
 let fail=false;const app={globalData:{token:'t'},request:async()=>{if(fail)throw new Error('连接失败');return snap('charging');}};
 const {p,timers}=setup(app);await p.onShow();fail=true;await p.load();assert.equal(p.data.snapshot,null);assert.equal(p.data.error,'连接失败');assert.equal(timers.size,0);
 fail=false;await p.load();assert.equal(timers.size,1);app.globalData.token='';await p.load();assert.equal(p.data.needsLogin,true);assert.equal(p.data.snapshot,null);assert.equal(timers.size,0);
});
test('stop acceptance does not claim physical completion and repeated clicks submit once',async()=>{
 let resolveStop,calls=0;const {p}=setup({globalData:{token:'t'},request:async method=>{
  if(method==='POST'){calls++;return new Promise(resolve=>resolveStop=resolve);}return snap('charging');
 }});
 await p.onShow();const first=p.stopCharge();await Promise.resolve();await p.stopCharge();assert.equal(calls,1);resolveStop({stopped:true});await first;
 assert.equal(p.data.snapshot.status,'charging');assert.equal(p.data.stopNotice,'停止请求已提交，等待设备确认。');assert.equal(p.data.stopping,false);
});
