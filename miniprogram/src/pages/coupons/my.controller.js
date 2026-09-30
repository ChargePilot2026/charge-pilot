import { defineController, getApp, wx } from "../../runtime";
const couponApp = getApp();
const statusValues = ['unused', 'used', 'expired'];
const statusLabels = ['可使用', '已使用', '已过期'];
const typeLabels = { amount: '立减券', percentage: '折扣券', time_free: '免费时长券' };
function formatCoupon(item) {
  const discount = item.discount_type === 'percentage'
    ? `${item.discount_percent || 0}% 折扣`
    : item.discount_type === 'time_free'
      ? '充电费用抵扣'
      : `立减 ¥${((item.discount_value_cents || 0) / 100).toFixed(2)}`;
  return { ...item, statusText: statusLabels[statusValues.indexOf(item.status)] || item.status, typeText: typeLabels[item.discount_type] || item.discount_type, discountText: discount, minimumText: (item.min_charge_cents / 100).toFixed(2), expiresText: item.expired_at ? new Date(item.expired_at).toLocaleString() : '—' };
}
export default defineController({
  data: { items: [], total: 0, page: 0, pageSize: 20, statusIndex: 0, statusNames: statusLabels, loading: false, error: '', needsLogin: false },
  onShow() { this._gone = false; this._generation = (this._generation || 0) + 1; this.load(); },
  onHide() { this._gone = true; this._generation++; },
  onUnload() { this.onHide(); },
  onPullDownRefresh() { this.load().finally(() => wx.stopPullDownRefresh()); },
  onReachBottom() { if (!this.data.loading && this.data.items.length < this.data.total) this.load(false); },
  async login() { try { await couponApp.login(); if (!this._gone) await this.load(); } catch (error) { if (!this._gone) this.setData({ error: error.message || '登录失败' }); } },
  onStatus(event) { this.setData({ statusIndex: Number(event.detail.value) }); this.load(); },
  async load(reset = true) {
    const generation = ++this._generation;
    if (!couponApp.globalData.token) { this.setData({ items: [], total: 0, page: 0, needsLogin: true, loading: false, error: '' }); return; }
    const page = reset ? 1 : this.data.page + 1;
    this.setData({ loading: true, error: '', needsLogin: false, ...(reset ? { items: [], total: 0, page: 0 } : {}) });
    try {
      const status = statusValues[this.data.statusIndex];
      const result = await couponApp.request('GET', '/user/coupon/my', { status, page, page_size: this.data.pageSize });
      if (this._gone || generation !== this._generation) return;
      if (!result || !Array.isArray(result.items)) throw new Error('优惠券列表响应异常');
      this.setData({ items: (reset ? [] : this.data.items).concat(result.items.map(formatCoupon)), total: result.total, page });
    } catch (error) { if (!this._gone && generation === this._generation) this.setData({ error: error.message || '优惠券读取失败', needsLogin: !couponApp.globalData.token }); }
    finally { if (!this._gone && generation === this._generation) this.setData({ loading: false }); }
  },
  goScan() { wx.switchTab({ url: '/pages/index/index' }); },
});
