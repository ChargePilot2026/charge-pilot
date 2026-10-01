import test from 'node:test';
import assert from 'node:assert/strict';
import {createSessionManager,SessionChanged,SessionExpired} from '../src/api/session.ts';
function setup(){
 const values=new Map();const storage={getItem:k=>values.get(k)??null,setItem:(k,v)=>values.set(k,v),removeItem:k=>values.delete(k)};
 let queue=Promise.resolve(),epoch=0,changes=0;
 const lock=action=>{const next=queue.then(action,action);queue=next.catch(()=>{});return next;};
 return {values,changes:()=>changes,manager:createSessionManager(storage,lock,()=>changes++,()=>String(++epoch))};
}
const login={token:'access1',refresh_token:'refresh1',admin_user_id:1,role:'customer_admin',permissions:['dashboard.read']};
test('concurrent expiry requests rotate only once and retain the logical session',async()=>{
 const {manager,values}=setup();await manager.login(login,'admin');const epoch=manager.epoch();let calls=0;
 const refresh=async()=>{calls++;return {...login,token:'access2',refresh_token:'refresh2'};};
 assert.deepEqual(await Promise.all([manager.refresh('access1',epoch,refresh),manager.refresh('access1',epoch,refresh)]),['access2','access2']);
 assert.equal(calls,1);assert.equal(manager.epoch(),epoch);assert.equal(values.get('cp_refresh'),'refresh2');
});
test('stale request cannot refresh or clear a newer account',async()=>{
 const {manager,values}=setup();await manager.login(login,'admin');const old=manager.epoch();
 await manager.login({...login,token:'other',admin_user_id:2},'other');
 await assert.rejects(manager.refresh('access1',old,async()=>login),SessionChanged);
 assert.equal(await manager.clear(old),false);assert.equal(values.get('cp_token'),'other');
});
test('transient failure retains credentials; expired legacy session requires sign in',async()=>{
 const {manager,values}=setup();await manager.login(login,'admin');const epoch=manager.epoch();
 await assert.rejects(manager.refresh('access1',epoch,async()=>{throw new Error('offline');}),/offline/);
 assert.equal(values.get('cp_refresh'),'refresh1');values.delete('cp_refresh');
 await assert.rejects(manager.refresh('access1',epoch,async()=>login),SessionExpired);
 assert.equal(await manager.clear(epoch),true);assert.equal(values.size,0);
});
test('sign in queued behind an in-flight refresh wins over its old response',async()=>{
 const {manager,values}=setup();await manager.login(login,'admin');let finish;const started=new Promise(r=>finish=r);
 let release;const response=new Promise(r=>release=r);
 const refresh=manager.refresh('access1',manager.epoch(),async()=>{finish();return response;});await started;
 const newer=manager.login({...login,token:'new-login',admin_user_id:2},'other');release({...login,token:'old-rotated'});
 await Promise.all([refresh,newer]);assert.equal(values.get('cp_token'),'new-login');
});
const profile={username:'admin',admin_user_id:1,role:'customer_admin',permissions:['dashboard.read','vendor.read']};
function deferred(){let resolve,reject;const promise=new Promise((yes,no)=>{resolve=yes;reject=no;});return {promise,resolve,reject};}
test('profile synchronization merges requests and updates only cached identity',async()=>{
 const {manager,values,changes}=setup();await manager.login(login,'admin');
 const epoch=manager.epoch(),before=changes(),response=deferred();let calls=0;
 const request=()=>{calls++;return response.promise;};
 const first=manager.syncProfile(request),second=manager.syncProfile(request);
 assert.equal(first,second);response.resolve(profile);assert.equal(await first,true);assert.equal(calls,1);
 assert.deepEqual(JSON.parse(values.get('cp_admin')),profile);assert.equal(changes(),before+1);
 assert.equal(values.get('cp_token'),'access1');assert.equal(values.get('cp_refresh'),'refresh1');assert.equal(manager.epoch(),epoch);
 await manager.syncProfile(async()=>profile);assert.equal(changes(),before+1);
 await manager.syncProfile(async()=>({...profile,permissions:['dashboard.read']}));
 assert.deepEqual(JSON.parse(values.get('cp_admin')).permissions,['dashboard.read']);
});
test('profile synchronization supports legacy sessions without an epoch',async()=>{
 const {manager,values}=setup();await manager.login(login,'admin');values.delete('cp_session_epoch');
 assert.equal(await manager.syncProfile(async()=>profile),true);
 assert.equal(manager.epoch(),null);assert.equal(values.get('cp_token'),'access1');assert.equal(values.get('cp_refresh'),'refresh1');
 assert.ok(JSON.parse(values.get('cp_admin')).permissions.includes('vendor.read'));
});
test('late profile responses cannot restore a logged-out or replaced account',async()=>{
 const {manager,values}=setup();await manager.login(login,'admin');
 const response=deferred(),sync=manager.syncProfile(()=>response.promise);await manager.clear(manager.epoch());response.resolve(profile);
 assert.equal(await sync,false);assert.equal(values.size,0);
 await manager.login(login,'admin');const old=deferred(),stale=manager.syncProfile(()=>old.promise);
 await manager.login({...login,token:'other',admin_user_id:2},'other');old.resolve(profile);
 assert.equal(await stale,false);assert.equal(values.get('cp_token'),'other');assert.equal(JSON.parse(values.get('cp_admin')).admin_user_id,2);
});
test('profile synchronization releases the lock during requests and preserves refreshed permissions',async()=>{
 const {manager,values}=setup();await manager.login(login,'admin');
 const started=deferred(),response=deferred();
 const sync=manager.syncProfile(()=>{started.resolve();return response.promise;});await started.promise;
 await manager.refresh('access1',manager.epoch(),async()=>({...login,token:'access2',refresh_token:'refresh2',permissions:['dashboard.read','vendor.update']}));
 response.resolve(profile);assert.equal(await sync,false);
 assert.equal(values.get('cp_token'),'access2');assert.deepEqual(JSON.parse(values.get('cp_admin')).permissions,['dashboard.read','vendor.update']);
});
test('failed or invalid identity synchronization retains cached credentials and can retry',async()=>{
 const {manager,values}=setup();await manager.login(login,'admin');const before=new Map(values);
 await assert.rejects(manager.syncProfile(async()=>{throw new Error('offline');}),/offline/);assert.deepEqual(values,before);
 await assert.rejects(manager.syncProfile(async()=>({...profile,permissions:null})),/登录身份数据无效/);assert.deepEqual(values,before);
 assert.equal(await manager.syncProfile(async()=>({...profile,admin_user_id:2})),false);assert.deepEqual(values,before);
 assert.equal(await manager.syncProfile(async()=>profile),true);
});

test('login, refresh and profile synchronization carry account names and MFA without changing session identity',async()=>{
 const {manager,values}=setup();
 await manager.login({...login,display_name:'运营管理员',role_name:'客户管理员',mfa_enabled:false},'admin');
 const epoch=manager.epoch();
 assert.equal(JSON.parse(values.get('cp_admin')).display_name,'运营管理员');
 assert.equal(JSON.parse(values.get('cp_admin')).role_name,'客户管理员');
 await manager.refresh('access1',epoch,async()=>({...login,token:'access2',refresh_token:'refresh2',display_name:'值班管理员',role_name:'自定义运维角色',mfa_enabled:true}));
 const refreshed=JSON.parse(values.get('cp_admin'));
 assert.equal(refreshed.display_name,'值班管理员');assert.equal(refreshed.role_name,'自定义运维角色');
 assert.equal(refreshed.mfa_enabled,true);assert.equal(refreshed.role,'customer_admin');assert.equal(manager.epoch(),epoch);
 await manager.syncProfile(async()=>({...profile,display_name:null,role_name:'新角色名称',mfa_enabled:true}));
 assert.equal(JSON.parse(values.get('cp_admin')).display_name,null);
 assert.equal(JSON.parse(values.get('cp_admin')).role_name,'新角色名称');
 assert.equal(values.get('cp_token'),'access2');assert.equal(values.get('cp_refresh'),'refresh2');assert.equal(manager.epoch(),epoch);
 const before=new Map(values);
 await assert.rejects(manager.syncProfile(async()=>({...profile,role_name:23})),/登录身份数据无效/);
 await assert.rejects(manager.syncProfile(async()=>({...profile,mfa_enabled:'true'})),/登录身份数据无效/);
 assert.deepEqual(values,before);
});
