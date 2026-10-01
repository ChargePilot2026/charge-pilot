export interface SessionProfile {
 username?: string;
 display_name?: string | null;
 role_name?: string | null;
 mfa_enabled?: boolean;
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
type RefreshRequest = (refresh:string)=>Promise<SessionTokens>;

// exp 仅用于提前续期；令牌有效性和会话授权仍由服务端校验。
function tokenExpiresAt(token:string):number|null {
 try {
  const payload=token.split('.')[1];
  if(!payload)return null;
  const {exp}=JSON.parse(atob(payload.replace(/-/g,'+').replace(/_/g,'/')));
  return typeof exp==='number' && exp>0 && Number.isFinite(exp*1000) ? exp*1000 : null;
 } catch { return null; }
}

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
  storage.setItem('cp_admin',JSON.stringify({username:data.username||username,display_name:data.display_name,role:data.role,role_name:data.role_name,mfa_enabled:data.mfa_enabled,admin_user_id:data.admin_user_id,permissions:data.permissions}));
  changed();
 };
 const rotate=async(expected:string|null,request:RefreshRequest)=>{
  const refresh=storage.getItem('cp_refresh');
  if(!expected || !refresh)throw new SessionExpired('登录已过期，请重新登录');
  const result=await request(refresh);
  if(epoch()!==expected)throw new SessionChanged('登录账号已变化，请重新打开页面');
  write(result);
  return result.token;
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
    if(!profile || !Number.isSafeInteger(profile.admin_user_id) || typeof profile.username!=='string' || !profile.username || typeof profile.role!=='string' || !profile.role || !Array.isArray(profile.permissions) || !profile.permissions.every(permission=>typeof permission==='string' && !!permission)
      || (profile.display_name!=null && typeof profile.display_name!=='string') || (profile.role_name!=null && typeof profile.role_name!=='string')
      || (profile.mfa_enabled!==undefined && typeof profile.mfa_enabled!=='boolean'))throw new Error('登录身份数据无效，请稍后重试');
    if(profile.admin_user_id!==id)return false;
    const value=JSON.stringify({username:profile.username,display_name:profile.display_name,role:profile.role,role_name:profile.role_name,mfa_enabled:profile.mfa_enabled,admin_user_id:profile.admin_user_id,permissions:profile.permissions});
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
  ensureFresh:(expected:string|null,request:RefreshRequest)=>lock(async()=>{
   if(epoch()!==expected)throw new SessionChanged('登录账号已变化，请重新打开页面');
   const current=storage.getItem('cp_token');
   if(!current)return '';
   const expiresAt=tokenExpiresAt(current);
   // 锁内复核到期时间，使并发请求复用已续期的令牌；30 秒余量覆盖请求传输时间。
   if(expiresAt===null || expiresAt>Date.now()+30_000)return current;
   return rotate(expected,request);
  }),
  refresh:(failedToken:string,expected:string|null,request:RefreshRequest)=>lock(async()=>{
   if(epoch()!==expected)throw new SessionChanged('登录账号已变化，请重新打开页面');
   const current=storage.getItem('cp_token');
   if(current && current!==failedToken)return current;
   return rotate(expected,request);
  })
 };
}
