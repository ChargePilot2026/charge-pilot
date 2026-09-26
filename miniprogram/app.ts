/// <reference path="./types/global.d.ts" />
App({
  globalData: { apiBase: 'https://your-domain.com/api/v1', userInfo: null, token: '', refreshToken: '' },
  _refreshPromise: null as Promise<void> | null,
  _generation: 0,
  _loggingOut: false,
  onLaunch() {
    this.globalData.token = wx.getStorageSync('cp_token') || '';
    this.globalData.refreshToken = wx.getStorageSync('cp_refresh_token') || '';
    this.globalData.userInfo = wx.getStorageSync('cp_user') || null;
  },
  saveSession(session: any) {
    this.globalData.token = session.token;
    this.globalData.refreshToken = session.refresh_token;
    wx.setStorageSync('cp_token', session.token);
    wx.setStorageSync('cp_refresh_token', session.refresh_token);
  },
  clearSession() {
    this.globalData.token = ''; this.globalData.refreshToken = ''; this.globalData.userInfo = null;
    ['cp_token','cp_refresh_token','cp_user'].forEach(key => wx.removeStorageSync(key));
  },
  async login() {
    const generation = ++this._generation;
    const code = await new Promise<string>((resolve,reject) => wx.login({success:(r: any)=>resolve(r.code),fail:reject}));
    const session = await this.rawRequest('POST','/public/auth/login',{code});
    if (generation !== this._generation || this._loggingOut) {
      await this.rawRequest('POST','/public/auth/logout',undefined,session.refresh_token).catch(()=>{});
      throw new Error('登录已取消');
    }
    this.saveSession(session);
    return session;
  },
  async refreshSession() {
    if (this._refreshPromise) return this._refreshPromise;
    if (!this.globalData.refreshToken) throw new Error('请重新登录');
    const generation = this._generation;
    this._refreshPromise = (async () => {
      const session = await this.rawRequest('POST','/public/auth/refresh',undefined,this.globalData.refreshToken);
      if (generation !== this._generation) {
        await this.rawRequest('POST','/public/auth/logout',undefined,session.refresh_token).catch(()=>{});
        throw new Error('登录状态已改变');
      }
      this.saveSession(session);
    })();
    try { await this._refreshPromise; }
    catch (e: any) { if (generation === this._generation && (e.status === 401 || e.status === 403)) this.clearSession(); throw e; }
    finally { this._refreshPromise = null; }
  },
  async logout() {
    this._loggingOut = true;
    try {
      if (this._refreshPromise) await this._refreshPromise.catch(()=>{});
      if (this.globalData.refreshToken) await this.rawRequest('POST','/public/auth/logout',undefined,this.globalData.refreshToken);
      this._generation++; this.clearSession();
    } finally { this._loggingOut = false; }
  },
  async request<T>(method: string,path: string,data?: unknown,auth = true): Promise<T> {
    if (auth && this._loggingOut) throw new Error('正在退出登录');
    const generation = this._generation;
    const token = auth ? this.globalData.token : '';
    try { return await this.rawRequest(method,path,data,token); }
    catch (e: any) {
      if (!auth || e.status !== 401 || generation !== this._generation || this._loggingOut) throw e;
      if (this.globalData.token === token) await this.refreshSession();
      if (generation !== this._generation || this._loggingOut) throw new Error('登录状态已改变');
      return this.rawRequest(method,path,data,this.globalData.token);
    }
  },
  rawRequest(method: string,path: string,data?: unknown,token = ''): Promise<any> {
    return new Promise((resolve,reject) => wx.request({
      url: this.globalData.apiBase + path, method, data,
      header: {'Content-Type':'application/json',...(token ? {Authorization:'Bearer '+token} : {})},
      success: (response: any) => {
        const body = response.data;
        if (response.statusCode >= 200 && response.statusCode < 300 && body && body.code === 0) { resolve(body.data); return; }
        const error: any = new Error(body?.message || '请求失败');
        error.status = response.statusCode; error.code = body?.code; reject(error);
      },
      fail: (e: any) => reject(new Error(e.errMsg || '网络连接失败')),
    }));
  },
});
