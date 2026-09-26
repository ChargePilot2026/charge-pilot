const refundApp=getApp();
const labels={rejected:'审核已拒绝',pending:'待处理',processing:'退款处理中',success:'已退款',manual_review:'等待人工审核',needs_review:'退款异常，待核实',failed:'退款异常，待核实'};
const money=n=>'¥'+(n/100).toFixed(2);
function cents(value){const match=/^(\d{1,7})(?:\.(\d{1,2}))?$/.exec(value.trim());if(!match)return null;const result=Number(match[1])*100+Number((match[2] || '').padEnd(2,'0'));return result>0 && result<=100000000 ? result:null;}
function requestId(){return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g,c=>{const r=Math.floor(Math.random()*16);return(c==='x'?r:(r&3)|8).toString(16);});}
function view(item){return {...item,amountText:money(item.amount_cents),refundedText:money(item.refunded_cents),statusText:labels[item.status] || '状态待核实',refund_orders:(item.refund_orders || []).map(p=>({...p,amountText:money(p.refund_cents),statusText:labels[p.status] || '状态待核实'}))};}
Page({
 data:{items:[],total:0,page:0,amount:'',reason:'',loading:false,refunding:false,pendingRetry:false,error:'',notice:'',needsLogin:false},
 onShow(){this._gone=false;this._generation=(this._generation || 0)+1;return this.load(true);},
 onHide(){this._gone=true;this._generation++;},
 onUnload(){this._unloaded=true;this.onHide();},
 onPullDownRefresh(){return this.load(true).finally(()=>wx.stopPullDownRefresh());},
 onReachBottom(){if(!this.data.loading && this.data.items.length<this.data.total)return this.load(false);},
 async login(){try{await refundApp.login();if(!this._gone)await this.load(true);}catch(e){if(!this._gone)this.setData({error:e.message || '登录失败'});}},
 async load(reset=true){
  if(this._refunding)return;
  const generation=++this._generation,session=refundApp._generation;
  if(this._ownerSession!==session){this._pending=null;this._storageKey=null;this.setData({items:[],total:0,page:0,pendingRetry:false,amount:'',reason:'',notice:'',error:''});}
  if(!refundApp.globalData.token){this._pending=null;this._storageKey=null;this.setData({needsLogin:true,items:[],total:0,loading:false,pendingRetry:false,amount:'',reason:'',notice:'',error:''});return;}
  const page=reset?1:this.data.page+1;
  this.setData({loading:true,error:'',needsLogin:false,...(reset?{items:[],total:0,page:0}:{})});
  try{
   const result=await refundApp.request('GET','/user/wallet/refunds',{page,page_size:20});
   if(this._gone || generation!==this._generation || session!==refundApp._generation)return;
   if(!result || !/^\d+$/.test(String(result.user_id)) || !Array.isArray(result.items))throw new Error('退款列表响应异常');
   this._storageKey='cp_wallet_refund_'+result.user_id;this._ownerSession=session;
   const saved=wx.getStorageSync(this._storageKey);
   this._pending=saved && typeof saved.request_id==='string' && Number.isSafeInteger(saved.amount_cents) ? saved:null;
   this.setData({items:(reset?[]:this.data.items).concat(result.items.map(view)),total:result.total,page,pendingRetry:!!this._pending,...(this._pending?{amount:(this._pending.amount_cents/100).toFixed(2),reason:this._pending.reason || ''}:{})});
  }catch(e){if(!this._gone && generation===this._generation && session===refundApp._generation)this.setData({error:e.message || '退款记录读取失败'});}
  finally{if(!this._gone && generation===this._generation && session===refundApp._generation)this.setData({loading:false});}
 },
 inputAmount(e){if(!this._pending && !this._refunding)this.setData({amount:e.detail.value});},
 inputReason(e){if(!this._pending && !this._refunding)this.setData({reason:e.detail.value});},
 async submit(){
  if(this._refunding || this._gone || this.data.loading || !this._storageKey)return;
  if(this._ownerSession!==refundApp._generation){this.setData({error:'登录账号已变化，请刷新页面'});return;}
  const amount=this._pending?.amount_cents || cents(this.data.amount);
  if(!amount){this.setData({error:'请输入有效退款金额，最多两位小数'});return;}
  this._refunding=true;this.setData({refunding:true,error:'',notice:''});
  const session=refundApp._generation,key=this._storageKey;let accepted=false;
  try{
   const confirmed=await new Promise(resolve=>wx.showModal({title:'确认钱包退款',content:'申请原路退回 '+money(amount)+'，对应余额将预留，到账状态以退款进度为准。',success:r=>resolve(r.confirm),fail:()=>resolve(false)}));
   if(!confirmed || this._gone || session!==refundApp._generation)return;
   if(!this._pending){const pending={request_id:requestId(),amount_cents:amount,reason:this.data.reason.trim() || null};wx.setStorageSync(key,pending);this._pending=pending;this.setData({pendingRetry:true});}
   const result=await refundApp.request('POST','/user/wallet/refund',this._pending);
   if(!result || result.request_id!==this._pending.request_id || !['accepted','manual_review'].includes(result.status))throw new Error('申请结果暂不确定，请重试核实同一申请');
   wx.removeStorageSync(key);this._pending=null;accepted=true;
   if(!this._gone && session===refundApp._generation)this.setData({pendingRetry:false,amount:'',reason:'',notice:result.status==='manual_review'?'申请已进入人工审核，请查看退款进度。':'申请已受理，退款将按原充值支付单处理。'});
  }catch(e){
   if(e.status>=200 && e.status<500 && ![401,403].includes(e.status)){wx.removeStorageSync(key);this._pending=null;}
   if(!this._gone && session===refundApp._generation)this.setData({pendingRetry:!!this._pending,error:e.message || '结果暂不确定，请重试同一申请'});
  }finally{this._refunding=false;if(!this._unloaded)this.setData({refunding:false});if(!this._gone && session===refundApp._generation && accepted)await this.load(true);}
 },
});
