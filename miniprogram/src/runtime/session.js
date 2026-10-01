import { wx } from "./platform";
export const application = {
  globalData: { apiBase: process.env.TARO_APP_API_BASE || '/api/v1', userInfo: null, token: '', refreshToken: '' },
  _refreshPromise: null                        ,
  _generation: 0,
  _loggingOut: false,
  onLaunch() {
    this.globalData.token = wx.getStorageSync('cp_token') || '';
    this.globalData.refreshToken = wx.getStorageSync('cp_refresh_token') || '';
    this.globalData.userInfo = wx.getStorageSync('cp_user') || null;
  },
  onShow(options     ) {
    const raw = options?.query?.q || options?.query?.scene;
    if (typeof raw !== 'string' || !raw) return;
    wx.navigateTo({ url: '/pages/scan-result/scan-result?code=' + encodeURIComponent(raw) });
  },
  saveSession(session     ) {
    this.globalData.token = session.token;
    this.globalData.refreshToken = session.refresh_token;
    wx.setStorageSync('cp_token', session.token);
    wx.setStorageSync('cp_refresh_token', session.refresh_token);
  },
  clearSession() {
    this.globalData.token = ''; this.globalData.refreshToken = ''; this.globalData.userInfo = null;
    ['cp_token','cp_refresh_token','cp_user'].forEach(key => wx.removeStorageSync(key));
  },
  /** @returns {Promise<import('./models').UserLogin>} */
  async login() {
    const generation = ++this._generation;
    const code = await new Promise        ((resolve,reject) => wx.login({success:(r     )=>resolve(r.code),fail:reject}));
    const session = await this.rawRequest('POST','/public/auth/login',{code});
    if (generation !== this._generation || this._loggingOut) {
      await this.rawRequest('POST','/public/auth/logout',undefined,session.refresh_token).catch(()=>{});
      throw new Error('登录已取消');
    }
    if (!session || typeof session.user_id !== 'string' || !/^[1-9]\d{0,19}$/.test(session.user_id)) {
      throw new Error('用户编号响应格式不正确，请重新登录');
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
    catch (e     ) { if (generation === this._generation && (e.status === 401 || e.status === 403)) this.clearSession(); throw e; }
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
  async request   (method        ,path        ,data          ,auth = true)             {
    if (auth && this._loggingOut) throw new Error('正在退出登录');
    const generation = this._generation;
    const token = auth ? this.globalData.token : '';
    try { return await this.rawRequest(method,path,data,token); }
    catch (e     ) {
      if (!auth || e.status !== 401 || generation !== this._generation || this._loggingOut) throw e;
      if (this.globalData.token === token) await this.refreshSession();
      if (generation !== this._generation || this._loggingOut) throw new Error('登录状态已改变');
      return this.rawRequest(method,path,data,this.globalData.token);
    }
  },
  rawRequest(method        ,path        ,data          ,token = '')               {
    return new Promise((resolve,reject) => wx.request({
      url: this.globalData.apiBase + path, method, data,
      header: {'Content-Type':'application/json',...(token ? {Authorization:'Bearer '+token} : {})},
      success: (response     ) => {
        const body = response.data;
        if (response.statusCode >= 200 && response.statusCode < 300 && body && body.code === 0) { resolve(body.data); return; }
        const error      = new Error(body?.message || '请求失败');
        error.status = response.statusCode; error.code = body?.code; reject(error);
      },
      fail: (e     ) => reject(new Error(e.errMsg || '网络连接失败')),
    }));
  },
};
export function getApp() { return application; }
