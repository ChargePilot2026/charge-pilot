const walletApp=getApp();
Page({
  data:{wallet:null,error:'',loading:false,needsLogin:false},
  onShow(){this._gone=false;this._generation=(this._generation || 0)+1;this.load();},
  onHide(){this._gone=true;this._generation++;},
  onUnload(){this.onHide();},
  onPullDownRefresh(){this.load().finally(()=>wx.stopPullDownRefresh());},
  async login(){try{await walletApp.login();if(!this._gone)await this.load();}catch(e){if(!this._gone)this.setData({error:e.message || '登录失败'});}},
  async load(){
    const generation=++this._generation;
    this.setData({wallet:null,error:'',loading:false,needsLogin:!walletApp.globalData.token});
    if(!walletApp.globalData.token)return;
    this.setData({loading:true});
    try{const w=await walletApp.request('GET','/user/wallet/balance');if(!this._gone && generation===this._generation)this.setData({wallet:{...w,availableText:(w.available_cents/100).toFixed(2),frozenText:(w.frozen_cents/100).toFixed(2)}});}
    catch(e){if(!this._gone && generation===this._generation)this.setData({error:e.message || '钱包查询失败',needsLogin:!walletApp.globalData.token});}
    finally{if(!this._gone && generation===this._generation)this.setData({loading:false});}
  },
  goRecharge(){wx.navigateTo({url:'/pages/wallet/recharge'});},
  goTxns(){wx.navigateTo({url:'/pages/wallet/txns'});},
  goRefund(){wx.navigateTo({url:'/pages/wallet/refund'});},
});
