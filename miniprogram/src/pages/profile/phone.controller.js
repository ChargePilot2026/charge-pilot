import { defineController, getApp, wx } from "../../runtime";
const phoneApp = getApp();

export default defineController({
  data: { loading: false, busy: false, bound: false, needsLogin: false, error: '', notice: '', phone: '' },
  inputPhone(event) { this.setData({ phone: event.detail.value }); },
  async bindDevelopment() {
    if(this.data.busy || this._gone)return;
    if(!/^1[3-9]\d{9}$/.test(this.data.phone)){this.setData({error:'请输入有效的测试手机号'});return;}
    const generation=phoneApp._generation;this.setData({busy:true,error:'',notice:''});
    try{const result=await phoneApp.request('POST','/user/phone/bind',{phone:this.data.phone});if(!this._gone && generation===phoneApp._generation){if(result.bound!==true)throw Error('绑定结果未确认');this.setData({bound:true,notice:'测试手机号已绑定。'});}}
    catch(error){if(!this._gone && generation===phoneApp._generation)this.setData({error:error.message || '绑定失败'});}
    finally{if(!this._gone)this.setData({busy:false});}
  },
  onShow() { this._gone = false; this._generation = (this._generation || 0) + 1; this.load(); },
  onHide() { this._gone = true; this._generation++; },
  onUnload() { this.onHide(); },
  async login() {
    try { await phoneApp.login(); if (!this._gone) await this.load(); }
    catch (error) { if (!this._gone) this.setData({ error: error.message || '登录失败' }); }
  },
  async load() {
    const generation = ++this._generation;
    if (!phoneApp.globalData.token) { this.setData({ bound: false, needsLogin: true, loading: false, error: '', notice: '' }); return; }
    this.setData({ loading: true, needsLogin: false, error: '' });
    try {
      const profile = await phoneApp.request('GET', '/user/profile');
      if (!this._gone && generation === this._generation) this.setData({ bound: !!profile.phone_bound, error: '' });
    } catch (error) {
      if (!this._gone && generation === this._generation) this.setData({ error: error.message || '账号状态读取失败', needsLogin: !phoneApp.globalData.token });
    } finally { if (!this._gone && generation === this._generation) this.setData({ loading: false }); }
  },
  async onGetPhoneNumber(event) {
    if (this.data.busy || this._gone) return;
    const code = event.detail && event.detail.code;
    if (!code) {
      const cancelled = event.detail && event.detail.errMsg && event.detail.errMsg.includes('deny');
      this.setData({ error: cancelled ? '你取消了手机号授权。' : '未取得微信手机号凭证，请重新点击授权。' });
      return;
    }
    if (!phoneApp.globalData.token) { this.setData({ needsLogin: true, error: '请先登录后再绑定手机号' }); return; }
    const generation = phoneApp._generation;
    this.setData({ busy: true, error: '', notice: '' });
    try {
      const result = await phoneApp.request('POST', '/user/phone/bind', { code });
      if (this._gone || generation !== phoneApp._generation) return;
      if (!result || result.bound !== true) throw new Error('绑定结果未确认，请刷新账号状态');
      this.setData({ bound: true, notice: '手机号已通过微信验证并绑定。' });
    } catch (error) {
      if (!this._gone && generation === phoneApp._generation) this.setData({ error: error.message || '绑定失败，请重新授权后重试' });
    } finally { if (!this._gone) this.setData({ busy: false }); }
  },
  unbind() {
    if (!this.data.bound || this.data.busy) return;
    wx.showModal({ title: '解除手机号绑定', content: '解除后，部分账号服务可能无法使用。', success: async result => {
      if (!result.confirm || this._gone) return;
      const generation = phoneApp._generation;
      this.setData({ busy: true, error: '', notice: '' });
      try {
        const response = await phoneApp.request('POST', '/user/phone/unbind');
        if (this._gone || generation !== phoneApp._generation) return;
        if (!response || response.unbound !== true) throw new Error('解除结果未确认，请刷新账号状态');
        this.setData({ bound: false, notice: '手机号已解除绑定。' });
      } catch (error) {
        if (!this._gone && generation === phoneApp._generation) this.setData({ error: error.message || '解除失败，请重试' });
      } finally { if (!this._gone) this.setData({ busy: false }); }
    }});
  },
});
