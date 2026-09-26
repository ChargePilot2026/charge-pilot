// 个人中心
const app = getApp();

Page({
  data: { profile: null as any },

  onShow() { this.refresh(); },

  async refresh() {
    if (!app.globalData.token) { wx.navigateTo({ url: '/pages/profile/phone' }); return; }
    try {
      const p = await app.request<any>('GET', '/user/profile');
      this.setData({ profile: p });
    } catch (e: any) { /* ignore */ }
  },

  async onLogout() {
    wx.showModal({
      title: '退出登录?', success: (r) => {
        if (r.confirm) {
          app.globalData.token = '';
          wx.removeStorageSync('cp_token');
          wx.removeStorageSync('cp_user');
          this.setData({ profile: null });
          wx.showToast({ title: '已退出' });
        }
      }
    });
  },

  goPhoneBind() { wx.navigateTo({ url: '/pages/profile/phone' }); },
  goAnnouncements() { wx.navigateTo({ url: '/pages/announcement/list' }); },
  goCoupons() { wx.navigateTo({ url: '/pages/coupons/my' }); },
  goInvoices() { wx.navigateTo({ url: '/pages/invoice/my' }); },
  goCustomerService() {
    wx.openCustomerServiceChat({
      extInfo: { url: '' },
      corpId: '', // 从白标配置读取
      success: () => {},
      fail: (e: any) => wx.showToast({ title: '客服暂不可用', icon: 'none' }),
    });
  },
});