const { controllerPath, controllerSource, sessionSource } = require('./source.cjs');
const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const {stripTypeScriptTypes}=require('node:module');
function setup(handler) {
  let app; const storage=new Map();
  const wx={getStorageSync:k=>storage.get(k),setStorageSync:(k,v)=>storage.set(k,v),removeStorageSync:k=>storage.delete(k),request:handler,login:({success})=>success({code:'test-code'})};
  vm.runInNewContext(sessionSource(),{wx,App:value=>{app=value;}});
  app.saveSession({token:'old-access',refresh_token:'old-refresh'});
  return {app,storage};
}
const ok=(request,data)=>request.success({statusCode:200,data:{code:0,data}});
const reject=(request,status)=>request.success({statusCode:status,data:{code:1001,message:'expired'}});

test('concurrent unauthorized requests share one refresh and retry with new access token',async()=>{
  let refreshes=0; let release;
  const {app}=setup(r=>{
    if(r.url.endsWith('/auth/refresh')) { refreshes++; release=()=>ok(r,{token:'new-access',refresh_token:'new-refresh'}); }
    else if(r.header.Authorization==='Bearer old-access') reject(r,401);
    else ok(r,{value:42});
  });
  const a=app.request('GET','/user/a'); const b=app.request('GET','/user/b');
  await new Promise(setImmediate); assert.equal(refreshes,1); release();
  assert.equal((await a).value,42); assert.equal((await b).value,42);
  assert.equal(app.globalData.refreshToken,'new-refresh');
});
test('transient refresh failure keeps session for retry; rejected refresh clears it',async()=>{
  let permanent=false;
  const {app,storage}=setup(r=>{
    if(r.url.endsWith('/auth/refresh')) permanent ? reject(r,401) : r.fail({errMsg:'offline'});
    else reject(r,401);
  });
  await assert.rejects(app.request('GET','/user/a'),/offline/);
  assert.equal(storage.get('cp_refresh_token'),'old-refresh');
  permanent=true; await assert.rejects(app.request('GET','/user/a'),/expired/);
  assert.equal(app.globalData.token,''); assert.equal(storage.has('cp_refresh_token'),false);
});
test('business errors do not refresh and logout waits for refresh before revoking',async()=>{
  let release; let revoked; let refreshes=0;
  const {app}=setup(r=>{
    if(r.url.endsWith('/auth/refresh')) { refreshes++; release=()=>ok(r,{token:'new-access',refresh_token:'new-refresh'}); }
    else if(r.url.endsWith('/auth/logout')) { revoked=r.header.Authorization; ok(r,{logged_out:true}); }
    else reject(r,403);
  });
  await assert.rejects(app.request('GET','/user/a')); assert.equal(refreshes,0);
  const refresh=app.refreshSession(); const logout=app.logout();
  release(); await refresh; await logout;
  assert.equal(revoked,'Bearer new-refresh'); assert.equal(app.globalData.token,'');
});

test('login preserves legacy and full Snowflake user IDs as decimal strings',async()=>{
  for(const userID of ['7','9223372036854775807']){
    const {app,storage}=setup(r=>ok(r,{token:'new-access',refresh_token:'new-refresh',user_id:userID,is_new_user:false}));
    const result=await app.login();
    assert.equal(result.user_id,userID);
    assert.equal(typeof result.user_id,'string');
    assert.equal(storage.get('cp_token'),'new-access');
  }
});

test('login rejects numeric user IDs before replacing the existing session',async()=>{
  for(const userID of [7,9223372036854775807]){
    const {app,storage}=setup(r=>ok(r,{token:'new-access',refresh_token:'new-refresh',user_id:userID}));
    await assert.rejects(app.login(),/用户编号响应格式不正确/);
    assert.equal(app.globalData.token,'old-access');
    assert.equal(storage.get('cp_refresh_token'),'old-refresh');
  }
});
