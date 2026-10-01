import test from 'node:test';
import assert from 'node:assert/strict';
import {registerHooks} from 'node:module';
import axios, {AxiosError} from 'axios';

// Node 直接载入生产模块，补齐 Vite 的扩展名解析及浏览器会话存储。
const clientURL=new URL('../src/api/client.ts',import.meta.url).href;
registerHooks({resolve(specifier,context,nextResolve){
 if(context.parentURL===clientURL && ['./errorMessage','./session'].includes(specifier))return nextResolve(`${specifier}.ts`,context);
 return nextResolve(specifier,context);
}});
const values=new Map();
const storage={getItem:key=>values.get(key)??null,setItem:(key,value)=>values.set(key,value),removeItem:key=>values.delete(key)};
Object.defineProperty(globalThis,'localStorage',{value:storage,configurable:true});
Object.defineProperty(globalThis,'navigator',{value:{},configurable:true});
globalThis.window={dispatchEvent(){},location:{href:'/'}};
const {http,adminSession,apiGet,apiPost}=await import(clientURL);

const NOW=1_800_000_000_000;
const jwt=expiresAt=>`${Buffer.from('{"alg":"HS256"}').toString('base64url')}.${Buffer.from(JSON.stringify({exp:expiresAt/1000})).toString('base64url')}.test-signature`;
const account={username:'admin',admin_user_id:1,role:'customer_admin',permissions:['dashboard.read']};
const session=(expiresAt=NOW+8*60*60*1000)=>({...account,token:jwt(expiresAt),refresh_token:'test-refresh'});
function response(config,data,status=200){return {config,status,statusText:String(status),headers:{},data:{code:0,message:'',data,request_id:'test'}};}
function rejectHTTP(config,status){const result=response(config,null,status);throw new AxiosError('HTTP failure',AxiosError.ERR_BAD_RESPONSE,config,undefined,result);}
function deferred(){let resolve;const promise=new Promise(done=>resolve=done);return {promise,resolve};}
async function setup(t,expiresAt){
 t.mock.method(Date,'now',()=>NOW);
 values.clear();window.location.href='/';
 await adminSession.login(session(expiresAt),'admin');
 const calls=[];
 return {calls,useAdapter(handler){
  const adapter=async config=>{calls.push(config.url);return handler(config);};
  http.defaults.adapter=adapter;axios.defaults.adapter=adapter;
 }};
}
const refreshPath='/api/v1/admin/auth/refresh',mePath='/api/v1/admin/auth/me';

test('expired access is refreshed before the adapter receives a protected request',async t=>{
 const {calls,useAdapter}=await setup(t,NOW-1000);let unauthorized=0;
 useAdapter(config=>{
  if(config.url===refreshPath)return response(config,session());
  const token=config.headers.get('Authorization').slice(7);
  const {exp}=JSON.parse(Buffer.from(token.split('.')[1],'base64url').toString());
  if(exp*1000<=Date.now()){unauthorized++;return rejectHTTP(config,401);}
  return response(config,account);
 });
 assert.deepEqual(await apiGet(mePath),account);
 assert.deepEqual(calls,[refreshPath,mePath]);assert.equal(unauthorized,0);
});

test('parallel requests within the expiry margin share one rotation and retain their epoch',async t=>{
 const {calls,useAdapter}=await setup(t,NOW+30_000);const epoch=adminSession.epoch();
 useAdapter(config=>response(config,config.url===refreshPath?session():account));
 await Promise.all([apiGet(mePath),apiGet('/api/v1/admin/dashboard'),apiGet(mePath)]);
 assert.equal(calls.filter(path=>path===refreshPath).length,1);
 assert.equal(calls.length,4);assert.equal(adminSession.epoch(),epoch);
});

test('fresh eight-hour tokens do not cause identity or refresh requests before business requests',async t=>{
 const {calls,useAdapter}=await setup(t);
 useAdapter(config=>response(config,account));
 const paths=['/api/v1/admin/dashboard','/api/v1/admin/orders','/api/v1/admin/devices'];
 await Promise.all(paths.map(path=>apiGet(path)));
 assert.deepEqual(calls,paths);assert.ok(!calls.includes(mePath));assert.ok(!calls.includes(refreshPath));
});

for(const failure of ['network',503,429])test(`preflight ${failure} failure retains credentials and permits a later retry`,async t=>{
 const {calls,useAdapter}=await setup(t,NOW);const before=new Map(values);
 useAdapter(config=>failure==='network'?Promise.reject(new AxiosError('offline',AxiosError.ERR_NETWORK,config)):rejectHTTP(config,failure));
 await assert.rejects(apiGet(mePath));
 assert.deepEqual(values,before);assert.equal(window.location.href,'/');assert.deepEqual(calls,[refreshPath]);
 useAdapter(config=>response(config,config.url===refreshPath?session():account));
 await apiGet(mePath);assert.deepEqual(calls,[refreshPath,refreshPath,mePath]);
});

test('a preflight refresh 401 expires the original session without recursively refreshing',async t=>{
 const {calls,useAdapter}=await setup(t,NOW);
 useAdapter(config=>rejectHTTP(config,401));
 await assert.rejects(apiGet(mePath));
 assert.deepEqual(calls,[refreshPath]);assert.equal(values.size,0);assert.equal(window.location.href,'/admin/login');
});

test('an expired access without refresh credentials is cleared before sending a request',async t=>{
 const {calls,useAdapter}=await setup(t,NOW);values.delete('cp_refresh');
 useAdapter(config=>response(config,account));
 await assert.rejects(apiGet(mePath),/登录已过期/);
 assert.equal(calls.length,0);assert.equal(values.size,0);assert.equal(window.location.href,'/admin/login');
});

test('server rejection of an unexpired access retains one refresh and one retry',async t=>{
 const {calls,useAdapter}=await setup(t);let attempts=0;
 useAdapter(config=>{
  if(config.url===refreshPath)return response(config,session(NOW+9*60*60*1000));
  if(++attempts===1)return rejectHTTP(config,401);
  return response(config,account);
 });
 await apiGet(mePath);assert.deepEqual(calls,[mePath,refreshPath,mePath]);assert.equal(values.size,4);
});

test('an access token without a readable expiry retains the server 401 fallback',async t=>{
 const {calls,useAdapter}=await setup(t);values.set('cp_token','test-legacy-access');let attempts=0;
 useAdapter(config=>{
  if(config.url===refreshPath)return response(config,session());
  if(++attempts===1)return rejectHTTP(config,401);
  return response(config,account);
 });
 await apiGet(mePath);assert.deepEqual(calls,[mePath,refreshPath,mePath]);assert.equal(values.size,4);
});

test('a revoked session is cleared when its refresh is rejected',async t=>{
 const {calls,useAdapter}=await setup(t);
 useAdapter(config=>rejectHTTP(config,401));
 await assert.rejects(apiGet(mePath));
 assert.deepEqual(calls,[mePath,refreshPath]);assert.equal(values.size,0);assert.equal(window.location.href,'/admin/login');
});

test('a retried protected request that remains unauthorized clears the session without a second refresh',async t=>{
 const {calls,useAdapter}=await setup(t);
 useAdapter(config=>config.url===refreshPath?response(config,session(NOW+9*60*60*1000)):rejectHTTP(config,401));
 await assert.rejects(apiGet(mePath));
 assert.deepEqual(calls,[mePath,refreshPath,mePath]);assert.equal(values.size,0);
});

test('login and MFA bypass preflight and invalid MFA cannot clear an existing session',async t=>{
 const {calls,useAdapter}=await setup(t,NOW);const before=new Map(values);
 useAdapter(config=>config.url.endsWith('/mfa')?rejectHTTP(config,401):response(config,session()));
 await apiPost('/api/v1/admin/auth/login',{username:'admin',password:'test-password'});
 await assert.rejects(apiPost('/api/v1/admin/auth/mfa',{mfa_challenge:'test-challenge',code:'000000'}));
 assert.deepEqual(calls,['/api/v1/admin/auth/login','/api/v1/admin/auth/mfa']);assert.deepEqual(values,before);assert.equal(window.location.href,'/');
});

test('a preflight response cannot overwrite credentials replaced by another account',async t=>{
 const {calls,useAdapter}=await setup(t,NOW);const started=deferred(),finish=deferred();
 useAdapter(async config=>{started.resolve();await finish.promise;return response(config,session());});
 const request=apiGet(mePath);await started.promise;
 const replacement={...session(NOW+9*60*60*1000),admin_user_id:2,username:'other',refresh_token:'other-refresh'};
 values.set('cp_session_epoch','other-epoch');values.set('cp_token',replacement.token);values.set('cp_refresh',replacement.refresh_token);values.set('cp_admin',JSON.stringify(replacement));
 const before=new Map(values);finish.resolve();
 await assert.rejects(request,/登录账号已变化/);
 assert.deepEqual(values,before);assert.deepEqual(calls,[refreshPath]);assert.equal(window.location.href,'/');
});

test('a late unauthorized response cannot refresh or clear a newer login',async t=>{
 const {calls,useAdapter}=await setup(t);const started=deferred(),finish=deferred();
 useAdapter(async config=>{started.resolve();await finish.promise;return rejectHTTP(config,401);});
 const request=apiGet(mePath);await started.promise;
 await adminSession.login({...session(NOW+9*60*60*1000),admin_user_id:2},'other');
 const before=new Map(values);finish.resolve();
 await assert.rejects(request,/登录账号已变化/);
 assert.deepEqual(values,before);assert.deepEqual(calls,[mePath]);assert.equal(window.location.href,'/');
});
