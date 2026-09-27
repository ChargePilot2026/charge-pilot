const invoiceApp = getApp();
const labels = { pending: '待审核', approved: '已通过', rejected: '已拒绝', issued: '已开具', failed: '处理失败' };
function view(item) { return { ...item, amountText: (item.total_cents / 100).toFixed(2), statusText: labels[item.review_status] || item.review_status, createdText: item.created_at ? new Date(item.created_at).toLocaleString() : '—' }; }
Page({
  data: { items: [], page: 0, pageSize: 50, loading: false, error: '', needsLogin: false },
  onShow() { this._gone = false; this._generation = (this._generation || 0) + 1; this.load(true); },
  onHide() { this._gone = true; this._generation++; },
  onUnload() { this.onHide(); },
  onPullDownRefresh() { this.load(true).finally(() => wx.stopPullDownRefresh()); },
  onReachBottom() { if (!this.data.loading && this.data.items.length === this.data.page * this.data.pageSize) this.load(false); },
  async login() { try { await invoiceApp.login(); if (!this._gone) await this.load(true); } catch (error) { if (!this._gone) this.setData({ error: error.message || '登录失败' }); } },
  async load(reset) {
    const generation = ++this._generation;
    if (!invoiceApp.globalData.token) { this.setData({ items: [], page: 0, loading: false, needsLogin: true, error: '' }); return; }
    const page = reset ? 1 : this.data.page + 1;
    this.setData({ loading: true, error: '', needsLogin: false, ...(reset ? { items: [], page: 0 } : {}) });
    try {
      const result = await invoiceApp.request('GET', '/user/invoice/my', { page, page_size: this.data.pageSize });
      if (this._gone || generation !== this._generation) return;
      if (!result || !Array.isArray(result.items)) throw new Error('发票列表响应异常');
      this.setData({ items: (reset ? [] : this.data.items).concat(result.items.map(view)), page });
    } catch (error) { if (!this._gone && generation === this._generation) this.setData({ error: error.message || '发票列表读取失败', needsLogin: !invoiceApp.globalData.token }); }
    finally { if (!this._gone && generation === this._generation) this.setData({ loading: false }); }
  },
  apply() { wx.navigateTo({ url: '/pages/invoice/apply' }); },
  openInvoice(event) {
    const url = event.currentTarget.dataset.url;
    if (!url || !url.startsWith('https://')) { this.setData({ error: '发票文件链接无效' }); return; }
    wx.setClipboardData({ data: url, success: () => wx.showToast({ title: '发票链接已复制', icon: 'none' }) });
  },
});
