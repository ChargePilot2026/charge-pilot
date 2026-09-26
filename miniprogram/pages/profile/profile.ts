// 个人中心
const app = getApp();

Page({
  data: { profile: null as any, loading:false, error:'', needsLogin:false, loggingIn:false },
  _generation:0,
  _gone:false,
  onShow() { this._gone=false; this.setData({loggingIn:false}); this.refresh(); },
  onHide() { this._gone=true; this._generation++; },
  onUnload() { this.onHide(); },
  onPullDownRefresh() { this.refresh().finally(()=>wx.stopPullDownRefresh()); },
  async login() {
    if (this.data.loggingIn) return;
    this.setData({loggingIn:true,error:''});
    try { await app.login(); if (!this._gone) await this.refresh(); }
    catch(e: any) { if(!this._gone) this.setData({error:e.message || '登录失败'}); }
    finally { if(!this._gone) this.setData({loggingIn:false}); }
  },
  async refresh() {
    const generation=++this._generation;
    this.setData({profile:null,error:'',needsLogin:!app.globalData.token,loading:false});
    if (!app.globalData.token) return;
    this.setData({loading:true});
    try {
      const p = await app.request<any>('GET', '/user/profile');
      if (!this._gone && generation===this._generation) this.setData({profile:{...p,
        balanceText:(p.wallet.available_cents/100).toFixed(2),frozenText:(p.wallet.frozen_cents/100).toFixed(2),
        registeredText:p.registered_at.slice(0,10),
        membershipText:p.membership_card ? ({month:'月卡',quarter:'季卡',year:'年卡'} as any)[p.membership_card.card_type] || '会员卡' : '暂无有效会员卡',
      }});
    } catch (e: any) {
      if(!this._gone && generation===this._generation) this.setData({error:e.message || '资料读取失败',needsLogin:!app.globalData.token});
    } finally { if(!this._gone && generation===this._generation) this.setData({loading:false}); }
  },
  onLogout() {
    wx.showModal({title:'退出登录?',success:async(r: any)=>{
      if(!r.confirm) return;
      ++this._generation;
      this.setData({loading:false});
      try { await app.logout(); if(!this._gone) this.setData({profile:null,needsLogin:true,error:'',loading:false}); }
      catch(e: any) { if(!this._gone) this.setData({error:e.message || '退出失败，请重试'}); }
    }});
  },

  goPhoneBind() { wx.navigateTo({ url: '/pages/profile/phone' }); },
  goOrders() { wx.navigateTo({ url: '/pages/charge/history' }); },
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
