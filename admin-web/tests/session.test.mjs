import test from 'node:test';
import assert from 'node:assert/strict';
import {createSessionManager,SessionChanged,SessionExpired} from '../src/api/session.ts';
function setup(){
 const values=new Map();const storage={getItem:k=>values.get(k)??null,setItem:(k,v)=>values.set(k,v),removeItem:k=>values.delete(k)};
 let queue=Promise.resolve(),epoch=0;
 const lock=action=>{const next=queue.then(action,action);queue=next.catch(()=>{});return next;};
 return {values,manager:createSessionManager(storage,lock,()=>{},()=>String(++epoch))};
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
