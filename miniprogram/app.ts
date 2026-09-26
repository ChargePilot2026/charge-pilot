/// <reference path="./types/global.d.ts" />

App({
  globalData: {
    apiBase: 'https://your-domain.com/api/v1', // 部署时修改
    userInfo: null,
    token: '',
  },

  onLaunch() {
    // 从本地缓存恢复 token
    const token = wx.getStorageSync('cp_token');
    if (token) {
      this.globalData.token = token;
      this.globalData.userInfo = wx.getStorageSync('cp_user');
    }
  },

  onShow() {
    // 网络恢复时自动重连(简化)
  },

  // 调 wx.login → 调后端换 JWT
  async login(): Promise<{ token: string; user_id: number; openid: string }> {
    const code = await new Promise<string>((resolve, reject) => {
      wx.login({
        success: r => resolve(r.code),
        fail: reject,
      });
    });
    const env = await this.request<{ token: string; user_id: number; openid: string }>(
      'POST', '/public/auth/login', { code }, false,
    );
    this.globalData.token = env.token;
    wx.setStorageSync('cp_token', env.token);
    return env;
  },

  // 通用请求封装(简化版;生产用 request-promise / 拦截器)
  async request<T>(method: string, path: string, data?: unknown, auth = true): Promise<T> {
    const url = `${this.globalData.apiBase}${path}`;
    return new Promise((resolve, reject) => {
      wx.request({
        url,
        method: method as any,
        data,
        header: {
          'Content-Type': 'application/json',
          ...(auth && this.globalData.token ? { Authorization: `Bearer ${this.globalData.token}` } : {}),
        },
        success: (resp) => {
          const body = resp.data as { code: number; message: string; data: T };
          if (body.code !== 0) {
            if (body.code === 1001 && auth) {
              // token 失效:清空缓存 + 跳登录
              this.globalData.token = '';
              wx.removeStorageSync('cp_token');
              wx.showToast({ title: '请重新登录', icon: 'none' });
            }
            reject(new Error(body.message || 'api error'));
            return;
          }
          resolve(body.data);
        },
        fail: (e) => reject(new Error(e.errMsg || 'network error')),
      });
    });
  },
});