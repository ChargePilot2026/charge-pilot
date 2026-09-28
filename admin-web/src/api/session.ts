export interface SessionTokens {
 token: string;
 refresh_token: string;
 username?: string;
 admin_user_id: number;
 role: string;
 permissions: string[];
}
export class SessionChanged extends Error {}
export class SessionExpired extends Error {}
type StoragePort = Pick<Storage,'getItem'|'setItem'|'removeItem'>;
type LockPort = <T>(action:()=>Promise<T>)=>Promise<T>;

// A single lock is shared by refresh, sign-in and sign-out. A late response may
// never replace credentials belonging to a later login in this or another tab.
export function createSessionManager(storage:StoragePort, lock:LockPort, changed:()=>void, newEpoch:()=>string) {
 const epoch=()=>storage.getItem('cp_session_epoch');
 const write=(data:SessionTokens,username?:string)=>{
  storage.setItem('cp_token',data.token);
  storage.setItem('cp_refresh',data.refresh_token);
  storage.setItem('cp_admin',JSON.stringify({username:data.username||username,role:data.role,admin_user_id:data.admin_user_id,permissions:data.permissions}));
  changed();
 };
 return {
  epoch,
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
