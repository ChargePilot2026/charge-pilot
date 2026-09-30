export interface SessionProfile {
 username?: string;
 admin_user_id: number;
 role: string;
 permissions: string[];
}
export interface SessionTokens extends SessionProfile {
 token: string;
 refresh_token: string;
}
export class SessionChanged extends Error {}
export class SessionExpired extends Error {}
type StoragePort = Pick<Storage,'getItem'|'setItem'|'removeItem'>;
type LockPort = <T>(action:()=>Promise<T>)=>Promise<T>;

// 刷新、登录、登出共用同一把锁。一个迟到的响应绝不能覆盖掉属于更晚一次登录的凭据，
// 无论是本标签页还是别的标签页。
export function createSessionManager(storage:StoragePort, lock:LockPort, changed:()=>void, newEpoch:()=>string) {
 const epoch=()=>storage.getItem('cp_session_epoch');
 const accountId=()=>{
  try { return JSON.parse(storage.getItem('cp_admin')||'null')?.admin_user_id as number|undefined; } catch { return undefined; }
 };
 let pendingProfile:{epoch:string|null;token:string;accountId:number;promise:Promise<boolean>}|undefined;
 const write=(data:SessionTokens,username?:string)=>{
  storage.setItem('cp_token',data.token);
  storage.setItem('cp_refresh',data.refresh_token);
  storage.setItem('cp_admin',JSON.stringify({username:data.username||username,role:data.role,admin_user_id:data.admin_user_id,permissions:data.permissions}));
  changed();
 };
 return {
  epoch,
  syncProfile:(request:()=>Promise<SessionProfile>):Promise<boolean>=>{
   const expected=epoch(),token=storage.getItem('cp_token'),id=accountId();
   if(!token || !Number.isSafeInteger(id) || !id || id<0)return Promise.resolve(false);
   if(pendingProfile?.epoch===expected && pendingProfile.token===token && pendingProfile.accountId===id)return pendingProfile.promise;
   // 请求放在会话锁外，避免 /me 的 401 重试等待同一把刷新锁。
   const promise=Promise.resolve().then(request).then(profile=>lock(async()=>{
    if(epoch()!==expected || storage.getItem('cp_token')!==token || accountId()!==id)return false;
    if(!profile || !Number.isSafeInteger(profile.admin_user_id) || typeof profile.username!=='string' || !profile.username || typeof profile.role!=='string' || !profile.role || !Array.isArray(profile.permissions) || !profile.permissions.every(permission=>typeof permission==='string' && !!permission))throw new Error('登录身份数据无效，请稍后重试');
    if(profile.admin_user_id!==id)return false;
    const value=JSON.stringify({username:profile.username,role:profile.role,admin_user_id:profile.admin_user_id,permissions:profile.permissions});
    if(storage.getItem('cp_admin')!==value){storage.setItem('cp_admin',value);changed();}
    return true;
   })).finally(()=>{if(pendingProfile?.promise===promise)pendingProfile=undefined;});
   pendingProfile={epoch:expected,token,accountId:id,promise};
   return promise;
  },
  login:(data:SessionTokens,username:string)=>lock(async()=>{storage.setItem('cp_session_epoch',newEpoch());write(data,username);}),
  clear:(expected:string|null)=>lock(async()=>{
   if(epoch()!==expected)return false;
   for(const key of ['cp_token','cp_refresh','cp_admin','cp_session_epoch'])storage.removeItem(key);
   changed();return true;
  }),
  refresh:(failedToken:string,expected:string|null,request:(refresh:string)=>Promise<SessionTokens>)=>lock(async()=>{
   if(epoch()!==expected)throw new SessionChanged('登录账号已变化，请重新打开页面');
   const current=storage.getItem('cp_token');
   if(current && current!==failedToken)return current;
   const refresh=storage.getItem('cp_refresh');
   if(!expected || !refresh)throw new SessionExpired('登录已过期，请重新登录');
   const result=await request(refresh);
   if(epoch()!==expected)throw new SessionChanged('登录账号已变化，请重新打开页面');
   write(result);
   return result.token;
  })
 };
}
