const detailApp = getApp();
const { formatOrder } = require('../../utils/order');
const { paymentParams } = require('../../utils/payment');
Page({
  data: { order: null, loading: false, error: '', needsLogin: false, paying:false,paymentNotice:'' },
  onLoad(query) { this._orderId = query.order_id || query.order_no; this._gone = false; this._generation = 0; this.load(); },
  onShow(){this._gone=false;if(this._openAfterShow){this._openAfterShow=false;this.charging();}else if(this._wasHidden && !this._paying)this.load();this._wasHidden=false;},
  onHide(){this._gone=true;this._wasHidden=true;this._generation++;},
  onUnload() { this._unloaded=true;this.onHide(); },
  onPullDownRefresh() { this.load().finally(() => wx.stopPullDownRefresh()); },
  async login() { try { await detailApp.login(); await this.load(); } catch (e) { this.setData({ error: e.message || '登录失败' }); } },
  async load() {
    if(this._paying)return;
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
  charging(){if(this.data.order)wx.navigateTo({url:'/pages/charge/charging?order_no='+encodeURIComponent(this.data.order.order_no)});},
  async pay(){
    if(this._paying || this._gone || this.data.loading || this.data.order?.status!=='pending_payment')return;
    this._paying=true;this.setData({paying:true,paymentNotice:''});
    const session=detailApp._generation,orderNo=this.data.order.order_no;
    try{
      const checkout=await detailApp.request('POST','/user/charge/'+encodeURIComponent(orderNo)+'/prepay',{});
      if(this._gone || this._unloaded || session!==detailApp._generation)return;
      if(checkout.order_no!==orderNo || !Number.isSafeInteger(checkout.amount_cents) || checkout.amount_cents<=0)throw new Error('支付信息不匹配，请刷新订单');
      const params=paymentParams(checkout.payment_params);
      const confirmed=await new Promise(resolve=>wx.showModal({title:'继续支付原订单',content:'预付 ¥'+(checkout.amount_cents/100).toFixed(2)+'，最终按实际充电结算。',success:r=>resolve(r.confirm),fail:()=>resolve(false)}));
      if(!confirmed || this._gone || this._unloaded || session!==detailApp._generation)return;
      if(!Number.isFinite(Date.parse(checkout.hold_expires_at)) || Date.parse(checkout.hold_expires_at)<=Date.now())throw new Error('付款期限已过，请刷新订单状态');
      await new Promise((resolve,reject)=>wx.requestPayment({...params,success:resolve,fail:reject}));
      if(this._unloaded || session!==detailApp._generation)return;
      this.setData({paymentNotice:'付款操作已完成，等待服务端确认。'});
      if(this._gone)this._openAfterShow=true;else this.charging();
    }catch(e){if(!this._unloaded)this.setData({paymentNotice:/cancel/i.test(e.errMsg || '')?'已取消付款，订单状态以服务端为准。':e.message || '付款结果不确定，请刷新订单状态。'});}
    finally{this._paying=false;if(!this._unloaded)this.setData({paying:false});}
  },
});
