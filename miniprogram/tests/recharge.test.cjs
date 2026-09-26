const {test}=require('node:test');const assert=require('node:assert/strict');const vm=require('node:vm');const fs=require('node:fs');
function fixture(){
 const storage=new Map(),posts=[];let page;
 const app={_generation:1,globalData:{token:'token'},request:async(method,path,body)=>{if(method==='GET')return {user_id:'7',items:[]};posts.push({...body});if(app.failure)throw Error('offline');return {request_id:body.request_id,amount_cents:body.amount_cents,can_pay:true,payment_params:{timeStamp:'123',nonceStr:'n',paySign:'s',signType:'RSA',package:'prepay_id=test'}};}};
 const wx={getStorageSync:key=>storage.get(key),setStorageSync:(key,v)=>storage.set(key,{...v}),removeStorageSync:key=>storage.delete(key),requestPayment:opts=>opts.fail({errMsg:'requestPayment:fail cancel'})};
 function create(){vm.runInNewContext(fs.readFileSync(require.resolve('../pages/wallet/recharge.js'),'utf8'),{getApp:()=>app,Page:p=>page=p,wx,require:()=>require('../utils/payment')});page.setData=v=>Object.assign(page.data,v);page._seq=0;return page;}
 return {app,wx,posts,storage,create};
}
test('recharge cancellation and network failure retain the same request across page recreation',async()=>{
 const f=fixture();let p=f.create();await p.onShow();p.data.amount='10.01';await p.submit();assert.match(p.data.error,/已取消/);assert.equal(f.posts[0].amount_cents,1001);
 p=f.create();await p.onShow();f.app.failure=true;await p.submit();assert.equal(p.data.error,'offline');assert.equal(f.posts[0].request_id,f.posts[1].request_id);assert.equal(p.data.pending,true);
});
test('recharge storage failure prevents posting and invalid amount is rejected',async()=>{
 const f=fixture(),p=f.create();await p.onShow();p.data.amount='0.99';await p.submit();assert.equal(f.posts.length,0);
 p.data.amount='10';f.wx.setStorageSync=()=>{throw Error('storage full');};await p.submit();assert.equal(f.posts.length,0);assert.equal(p.data.error,'storage full');
});
test('recharge ignores late payment response after account switch',async()=>{
 const f=fixture(),p=f.create();await p.onShow();p.data.amount='10';let resolve,calls=0;f.wx.requestPayment=()=>calls++;
 f.app.request=()=>new Promise(r=>resolve=r);const pending=p.submit();f.app._generation++;resolve({can_pay:true});await pending;assert.equal(calls,0);
});
test('server paid response clears pending request without opening payment',async()=>{
 const f=fixture(),p=f.create();await p.onShow();p.data.amount='10';const original=f.app.request;let calls=0;f.wx.requestPayment=()=>calls++;
 f.app.request=async(m,path,body)=>m==='GET'?original(m,path,body):{...body,can_pay:false,status:'paid'};
 await p.submit();assert.equal(calls,0);assert.equal(f.storage.size,0);assert.match(p.data.notice,/已到账/);
});
test('recharge submit is single flight and account change clears private pending state',async()=>{
 const f=fixture(),p=f.create();await p.onShow();p.data.amount='12.34';const original=f.app.request;let release,posts=0;
 f.app.request=(m,path,body)=>m==='POST'?(posts++,new Promise(r=>release=()=>r({...body,can_pay:false,status:'paid'}))):original(m,path,body);
 const first=p.submit();await p.submit();assert.equal(posts,1);
 f.app._generation++;f.app.request=async()=>({user_id:'8',items:[]});await p.onShow();assert.equal(p.data.amount,'');assert.equal(p.data.pending,false);assert.equal(p.data.paying,false);
 release();await first;assert.equal(p.data.notice,'');assert.equal(f.storage.size,1);
});
