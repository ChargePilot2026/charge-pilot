const app=getApp();
const {paymentParams}=require('../../utils/payment');
const labels={initiated:'待支付',paid:'已到账',partially_refunded:'已部分退款',refunded:'已退款',closed:'已关闭',failed:'支付失败'};
function requestId(){return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g,c=>{const r=Math.floor(Math.random()*16);return(c==='x'?r:(r&3)|8).toString(16);});}
Page({
 data:{amount:'',items:[],page:0,hasMore:false,loading:false,paying:false,pending:false,error:'',notice:'',needsLogin:false},
 onShow(){this._gone=false;this._seq=(this._seq||0)+1;if(!this._busy||this._session!==app._generation)return this.load();},
 onHide(){this._gone=true;this._seq++;},
 onUnload(){this._unloaded=true;this.onHide();},
 async login(){try{await app.login();await this.load();}catch(e){this.setData({error:e.message||'登录失败'});}},
 async load(more=false){
  const seq=++this._seq,session=app._generation;
  if(this._session!==session){this._pending=null;this._key=null;this.setData({items:[],amount:'',pending:false,paying:false,notice:'',error:''});}
  this.setData({needsLogin:!app.globalData.token});if(!app.globalData.token)return;
  const page=more?this.data.page+1:1;this.setData({loading:true,error:''});
  try{
   const result=await app.request('GET','/user/wallet/recharges',{page});
   if(this._gone || seq!==this._seq || session!==app._generation)return;
   if(!/^\d+$/.test(String(result.user_id)) || !Array.isArray(result.items))throw Error('充值记录响应异常');
   this._key='cp_wallet_recharge_'+result.user_id;this._session=session;
   const saved=wx.getStorageSync(this._key);
   this._pending=saved && typeof saved.request_id==='string' && Number.isSafeInteger(saved.amount_cents)?saved:null;
   const items=result.items.map(item=>({...item,amountText:(item.amount_cents/100).toFixed(2),statusText:item.status==='initiated'&&!item.can_pay?'支付窗口已结束，等待核实':labels[item.status]||'状态待核实'}));
   this.setData({items:more?this.data.items.concat(items):items,page,hasMore:items.length===20,pending:!!this._pending,...(this._pending?{amount:(this._pending.amount_cents/100).toFixed(2)}:{})});
  }catch(e){if(!this._gone && seq===this._seq && session===app._generation)this.setData({error:e.message||'充值记录读取失败'});}
  finally{if(!this._gone && seq===this._seq && session===app._generation)this.setData({loading:false});}
 },
 refresh(){if(!this._busy||this._session!==app._generation)return this.load();},
 more(){if(!this.data.loading&&!this._busy&&this.data.hasMore)return this.load(true);},
 inputAmount(e){if(!this._pending&&!this._busy)this.setData({amount:e.detail.value});},
 resume(e){if(this._busy||this._pending)return;const item=this.data.items.find(v=>v.request_id===e.currentTarget.dataset.id);if(item&&item.can_pay)return this.submit({request_id:item.request_id,amount_cents:item.amount_cents});},
 async submit(existing){
  if(this._busy||!this._key||this._session!==app._generation||!app.globalData.token)return;
  let req=this._pending;
  if(!req){
   const match=/^(\d{1,7})(?:\.(\d{1,2}))?$/.exec(this.data.amount.trim());
   const amount=match?Number(match[1])*100+Number((match[2]||'').padEnd(2,'0')):0;
   req=existing&&typeof existing.request_id==='string'?existing:{request_id:requestId(),amount_cents:amount};
   if(req.amount_cents<100||req.amount_cents>100000000){this.setData({error:'请输入 1 元至 100 万元的金额，最多两位小数'});return;}
  }
  this._busy=true;const session=app._generation;
  const current=()=>!this._unloaded&&session===app._generation;
  this.setData({paying:true,error:'',notice:''});
  try{
   wx.setStorageSync(this._key,req);this._pending=req;this.setData({pending:true,amount:(req.amount_cents/100).toFixed(2)});
   const result=await app.request('POST','/user/wallet/recharge',req);
   if(!current())return;
   if(result.request_id!==req.request_id||result.amount_cents!==req.amount_cents)throw Error('充值响应不一致，请刷新核实');
   if(!result.can_pay){
    wx.removeStorageSync(this._key);this._pending=null;this.setData({pending:false,amount:'',notice:['paid','partially_refunded','refunded'].includes(result.status)?'充值已到账，最新余额请返回钱包查看':'该支付窗口已结束，请核实充值记录及资金流水'});return;
   }
   await new Promise((resolve,reject)=>wx.requestPayment({...paymentParams(result.payment_params),success:resolve,fail:reject}));
   if(current())this.setData({notice:'支付操作已完成，到账以服务端记录为准。请刷新记录核实。'});
  }catch(e){if(current())this.setData({error:String(e.errMsg||'').includes('cancel')?'已取消支付，可继续原充值单':e.message||e.errMsg||'支付未完成，可重试原充值单'});}
  finally{this._busy=false;if(current()){const error=this.data.error;this.setData({paying:false});await this.load();if(current()&&error)this.setData({error});}}
 },
});
