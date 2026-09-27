const invoiceApp = getApp();
Page({
  data: { orders: [], orderLabels: [], orderIndex: -1, selected: null, invoiceTypeIndex: 0, invoiceTypes: ['普通发票', '增值税专用发票'], title: '', taxNo: '', email: '', loading: false, loadingDetail: false, submitting: false, needsLogin: false, error: '', notice: '' },
  onLoad() { this._gone = false; this._generation = 0; this.loadOrders(); },
  onShow() { this._gone = false; },
  onHide() { this._gone = true; this._generation++; },
  onUnload() { this.onHide(); },
  async login() { try { await invoiceApp.login(); if (!this._gone) await this.loadOrders(); } catch (error) { if (!this._gone) this.setData({ error: error.message || '登录失败' }); } },
  async loadOrders() {
    const generation = ++this._generation;
    if (!invoiceApp.globalData.token) { this.setData({ needsLogin: true, loading: false, orders: [], orderLabels: [] }); return; }
    this.setData({ loading: true, needsLogin: false, error: '' });
    try {
      const result = await invoiceApp.request('GET', '/user/charge/history', { page: 1, page_size: 100, status: 'completed' });
      if (this._gone || generation !== this._generation) return;
      const orders = (result.items || []).filter(order => order.status === 'completed');
      this.setData({ orders, orderLabels: orders.map(order => `${order.order_no} · ${order.device_id} · ¥${(order.total_fee_cents / 100).toFixed(2)}`) });
      if (orders.length === 100) this.setData({ error: '仅显示最近 100 笔已完成订单；请选择需要开票的订单。' });
    } catch (error) { if (!this._gone && generation === this._generation) this.setData({ error: error.message || '订单读取失败', needsLogin: !invoiceApp.globalData.token }); }
    finally { if (!this._gone && generation === this._generation) this.setData({ loading: false }); }
  },
  onOrderSelect(event) {
    const index = Number(event.detail.value);
    const order = this.data.orders[index];
    this.setData({ orderIndex: index, selected: null, error: '', notice: '' });
    if (!order) return;
    this.loadOrderDetail(order);
  },
  async loadOrderDetail(order) {
    const generation = ++this._generation;
    this.setData({ loadingDetail: true, selected: null, error: '' });
    try {
      const detail = await invoiceApp.request('GET', `/user/charge/${encodeURIComponent(order.order_id)}`);
      if (this._gone || generation !== this._generation) return;
      const paid = Number(detail.paid_fee_cents);
      if (detail.status !== 'completed' || !Number.isSafeInteger(paid) || paid <= 0 || Number(detail.refunded_cents || 0) > 0) {
        throw new Error('该订单未完成支付或发生过退款，暂不能申请发票');
      }
      this.setData({ selected: { order_id: String(order.order_id), order_no: order.order_no, amount_cents: paid, amountText: (paid / 100).toFixed(2) } });
    } catch (error) { if (!this._gone && generation === this._generation) this.setData({ error: error.message || '订单详情读取失败' }); }
    finally { if (!this._gone && generation === this._generation) this.setData({ loadingDetail: false }); }
  },
  onType(event) { this.setData({ invoiceTypeIndex: Number(event.detail.value) }); },
  onTitle(event) { this.setData({ title: event.detail.value }); },
  onTaxNo(event) { this.setData({ taxNo: event.detail.value }); },
  onEmail(event) { this.setData({ email: event.detail.value }); },
  async submit() {
    if (this.data.submitting || this._gone || !this.data.selected) return;
    const title = this.data.title.trim();
    const taxNo = this.data.taxNo.trim();
    const email = this.data.email.trim();
    const invoiceType = this.data.invoiceTypeIndex === 1 ? 'vat_special' : 'normal';
    if (!title || title.length > 255) { this.setData({ error: '请填写有效的发票抬头' }); return; }
    if (invoiceType === 'vat_special' && !taxNo) { this.setData({ error: '增值税专用发票必须填写税号' }); return; }
    if (email && (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email) || email.length > 128)) { this.setData({ error: '邮箱格式无效' }); return; }
    const generation = invoiceApp._generation;
    this.setData({ submitting: true, error: '', notice: '' });
    try {
      const response = await invoiceApp.request('POST', '/user/invoice/apply', {
        biz_type: 'charge', biz_id: Number(this.data.selected.order_id), total_cents: this.data.selected.amount_cents,
        invoice_type: invoiceType, title, tax_no: taxNo || null, email: email || null,
      });
      if (this._gone || generation !== invoiceApp._generation) return;
      if (!response || !response.invoice_no) throw new Error('申请结果未确认，请到发票记录核实');
      this.setData({ notice: `发票申请已提交，编号 ${response.invoice_no}` });
      wx.showModal({ title: '申请已提交', content: `申请编号：${response.invoice_no}`, showCancel: false, success: () => { if (!this._gone) wx.navigateBack(); } });
    } catch (error) { if (!this._gone && generation === invoiceApp._generation) this.setData({ error: error.message || '发票申请失败' }); }
    finally { if (!this._gone && generation === invoiceApp._generation) this.setData({ submitting: false }); }
  },
});
