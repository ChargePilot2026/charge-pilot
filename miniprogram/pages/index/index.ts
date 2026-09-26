// 首页:扫码入口 + 公告 + 钱包余额
const app = getApp();

Page({
  data: {
    balance_cents: 0,
    ongoing: null as null | { order_no: string; status: string },
    announcements: [] as any[],
  },

  onShow() {
    this.refresh();
  },

  async refresh() {
    if (!app.globalData.token) return;
    try {
      const me = await app.request<any>('GET', '/user/profile');
      this.setData({ balance_cents: me.balance_cents || 0 });
      const ong = await app.request<any>('GET', '/user/charge/ongoing');
      this.setData({ ongoing: ong });
      const ann = await app.request<{ items: any[] }>('GET', '/user/announcement/list');
      this.setData({ announcements: ann.items || [] });
    } catch (e: any) { /* 静默 */ }
  },

  onScanTap() {
    wx.navigateTo({ url: '/pages/scan/scan' });
  },

  goWallet() { wx.switchTab({ url: '/pages/wallet/wallet' }); },
  goOngoing() {
    if (this.data.ongoing) {
      wx.navigateTo({ url: `/pages/charge/charging?order_no=${encodeURIComponent(this.data.ongoing.order_no)}` });
    }
  },
});
