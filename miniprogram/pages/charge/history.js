const orderApp = getApp();
const { formatOrder } = require('../../utils/order');
Page({
  data: { items: [], total: 0, page: 0, loading: false, error: '', needsLogin: false,
    statusIndex: 0, statusNames: ['全部', '充电中', '已完成', '已取消', '失败'], statusValues: ['', 'charging', 'finished', 'cancelled', 'failed'] },
  onLoad() { this._generation = 0; this._gone = false; this.load(true); },
  onUnload() { this._gone = true; this._generation++; },
  onPullDownRefresh() { this.load(true).finally(() => wx.stopPullDownRefresh()); },
  onReachBottom() { if (!this.data.loading && this.data.items.length < this.data.total) this.load(false); },
  onStatus(event) { this.setData({ statusIndex: Number(event.detail.value) }); this.load(true); },
  async login() {
    try { await orderApp.login(); await this.load(true); }
    catch (e) { this.setData({ error: e.message || '登录失败，请重试' }); }
  },
  retry() { this.load(this.data.page === 0); },
  async load(reset) {
    if (!orderApp.globalData.token) { this.setData({ needsLogin: true, loading: false, items: [], total: 0 }); return; }
    const generation = ++this._generation;
    const page = reset ? 1 : this.data.page + 1;
    this.setData({ loading: true, error: '', needsLogin: false, ...(reset ? { items: [], page: 0, total: 0 } : {}) });
    try {
      const status = this.data.statusValues[this.data.statusIndex];
      const result = await orderApp.request('GET', '/user/charge/history', { page, page_size: 20, ...(status ? { status } : {}) });
      if (this._gone || generation !== this._generation) return;
      this.setData({ items: (reset ? [] : this.data.items).concat(result.items.map(formatOrder)), total: result.total, page });
    } catch (e) { if (!this._gone && generation === this._generation) this.setData({ error: e.message || '订单加载失败', needsLogin: !orderApp.globalData.token }); }
    finally { if (!this._gone && generation === this._generation) this.setData({ loading: false }); }
  },
  openDetail(event) { wx.navigateTo({ url: `/pages/charge/detail?order_id=${encodeURIComponent(event.currentTarget.dataset.id)}` }); },
});
