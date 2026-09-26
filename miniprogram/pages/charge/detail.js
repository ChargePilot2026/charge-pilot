const detailApp = getApp();
const { formatOrder } = require('../../utils/order');
Page({
  data: { order: null, loading: false, error: '', needsLogin: false },
  onLoad(query) { this._orderId = query.order_id || query.order_no; this._gone = false; this._generation = 0; this.load(); },
  onUnload() { this._gone = true; this._generation++; },
  onPullDownRefresh() { this.load().finally(() => wx.stopPullDownRefresh()); },
  async login() { try { await detailApp.login(); await this.load(); } catch (e) { this.setData({ error: e.message || '登录失败' }); } },
  async load() {
    if (!this._orderId) { this.setData({ error: '缺少订单标识' }); return; }
    if (!detailApp.globalData.token) { this.setData({ needsLogin: true, order: null }); return; }
    const generation = ++this._generation;
    this.setData({ loading: true, error: '', order: null, needsLogin: false });
    try {
      const order = await detailApp.request('GET', `/user/charge/${encodeURIComponent(this._orderId)}`);
      if (!this._gone && generation === this._generation) this.setData({ order: formatOrder(order) });
    } catch (e) { if (!this._gone && generation === this._generation) this.setData({ error: e.message || '订单详情加载失败', needsLogin: !detailApp.globalData.token }); }
    finally { if (!this._gone && generation === this._generation) this.setData({ loading: false }); }
  },
});
