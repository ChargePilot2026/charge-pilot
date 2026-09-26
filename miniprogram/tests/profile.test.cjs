const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const {stripTypeScriptTypes}=require('node:module');
function page(app) {
  let p;
  vm.runInNewContext(stripTypeScriptTypes(fs.readFileSync(require.resolve('../pages/profile/profile.ts'),'utf8')),{
    getApp:()=>app,Page:value=>p=value,wx:{stopPullDownRefresh(){},showModal:({success})=>success({confirm:true})},
  });
  p.setData=value=>Object.assign(p.data,value); return p;
}
const profile={nickname:'测试',wallet:{available_cents:4123,frozen_cents:100},registered_at:'2026-09-26T00:00:00Z',membership_card:null};
test('anonymous profile offers login without requesting data or navigating to a missing page',async()=>{
  const p=page({globalData:{token:''},request:()=>{throw Error('unexpected request');}});
  await p.refresh(); assert.equal(p.data.needsLogin,true); assert.equal(p.data.profile,null);
});
test('profile formats money and replaces stale data with a recoverable error',async()=>{
  let fail=false;
  const p=page({globalData:{token:'access'},request:async()=>{if(fail)throw Error('offline');return profile;}});
  await p.refresh(); assert.equal(p.data.profile.balanceText,'41.23'); assert.equal(p.data.profile.frozenText,'1.00');
  fail=true; await p.refresh(); assert.equal(p.data.profile,null); assert.equal(p.data.error,'offline');
});
test('hidden page and successful logout do not accept stale profile responses',async()=>{
  let resolve;
  const app={globalData:{token:'access'},request:()=>new Promise(r=>resolve=r),logout:async()=>{app.globalData.token='';}};
  const p=page(app); const loading=p.refresh(); p.onHide(); resolve(profile); await loading;
  assert.equal(p.data.profile,null);
  p._gone=false; const next=p.refresh(); p.onLogout(); await new Promise(setImmediate); resolve(profile); await next;
  assert.equal(p.data.profile,null); assert.equal(p.data.needsLogin,true);
});
