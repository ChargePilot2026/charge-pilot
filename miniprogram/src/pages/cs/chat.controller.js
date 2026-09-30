import { defineController, getApp, wx } from "../../runtime";
const customerServiceApp = getApp();
export default defineController({
  data: { scene: 'general', entry: null, loading: false, error: '', needsLogin: false, opening: false },
  onLoad(options) { this._scene = ['refund', 'complaint'].includes(options.scene) ? options.scene : 'general'; this.setData({ scene: this._scene }); },
  onShow() { this._gone = false; this._generation = (this._generation || 0) + 1; this.load(); },
  onHide() { this._gone = true; this._generation++; },
  onUnload() { this.onHide(); },
  async login() { try { await customerServiceApp.login(); if (!this._gone) await this.load(); } catch (error) { if (!this._gone) this.setData({ error: error.message || '登录失败' }); } },
  async load() {
    const generation = ++this._generation;
    if (!customerServiceApp.globalData.token) { this.setData({ needsLogin: true, entry: null, loading: false, error: '' }); return; }
    this.setData({ loading: true, needsLogin: false, error: '', entry: null });
    try {
      const entry = await customerServiceApp.request('POST', '/user/customer-service/entry', { scene: this._scene });
      if (this._gone || generation !== this._generation) return;
      this.setData({ entry });
    } catch (error) { if (!this._gone && generation === this._generation) this.setData({ error: error.message || '客服信息读取失败', needsLogin: !customerServiceApp.globalData.token }); }
    finally { if (!this._gone && generation === this._generation) this.setData({ loading: false }); }
  },
  openChat() {
    const entry = this.data.entry;
    if (!entry || !entry.available || !entry.corp_id || !entry.entry_url || this.data.opening) return;
    this.setData({ opening: true });
    wx.openCustomerServiceChat({
      corpId: entry.corp_id,
      extInfo: { url: entry.entry_url },
      success: () => {},
      fail: error => this.setData({ error: error.errMsg || '微信客服暂不可用，请稍后重试' }),
      complete: () => this.setData({ opening: false }),
    });
  },
  copyWechat() {
    const account = this.data.entry && this.data.entry.agent_wechat;
    if (!account) return;
    wx.setClipboardData({ data: account, success: () => wx.showToast({ title: '客服微信号已复制', icon: 'none' }) });
  },
});
