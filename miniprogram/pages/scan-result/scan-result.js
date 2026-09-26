const scanResultApp=getApp();
const {normalizeCode,portView}=require('../../utils/scan');
const {paymentParams,validQuote}=require('../../utils/payment');
Page({
 data:{deviceId:'',ports:[],selected:null,loading:false,error:'',needsLogin:false,quote:null,estimatedKwh:'0.500',estimatedMinutes:'120',paying:false,paymentNotice:'',orderNo:'',startAttempted:false,canRetryPayment:false},
 onLoad(query){try{this._code=decodeURIComponent(query.code || '');}catch(_){this._code='';}this._generation=0;this._gone=false;},
 onShow(){this._gone=false;if(this._openOrderOnShow){this._openOrderOnShow=false;this.viewOrder();return;}if(this._paying || this.data.startAttempted)return;return this.load();},
 onHide(){this._gone=true;this._generation++;},
 onUnload(){this._unloaded=true;this.onHide();},
 async login(){try{await scanResultApp.login();if(!this._gone)await this.load();}catch(e){if(!this._gone)this.setData({error:e.message || '登录失败'});}},
 async load(){
  if(this._paying || this.data.startAttempted)return;
  const generation=++this._generation;
  this.setData({deviceId:'',ports:[],selected:null,quote:null,error:'',loading:false,needsLogin:!scanResultApp.globalData.token});
  if(this._gone)return;
  try{
   const code=normalizeCode(this._code);
   if(!scanResultApp.globalData.token)return;
   this.setData({loading:true});
   const result=await scanResultApp.request('POST','/user/scan/resolve',{code});
   if(this._gone || generation!==this._generation)return;
   if(!result || !['port','device'].includes(result.kind) || typeof result.device_id!=='string' || (result.kind==='device' && !Array.isArray(result.ports)))throw new Error('二维码解析结果异常，请重试');
   const ports=(result.kind==='port' ? [result] : result.ports).map(portView);
   if(ports.some(p=>p.device_id!==result.device_id) || new Set(ports.map(p=>p.port_id)).size!==ports.length)throw new Error('端口信息异常，请重试');
   this.setData({deviceId:result.device_id,ports,selected:result.kind==='port' ? ports[0] : null});
  }catch(e){if(!this._gone && generation===this._generation)this.setData({error:e.message || '二维码解析失败',needsLogin:!scanResultApp.globalData.token});}
  finally{if(!this._gone && generation===this._generation)this.setData({loading:false});}
 },
 async selectPort(event){
  const id=event.currentTarget.dataset.id;
  const chosen=this.data.ports.find(p=>p.port_id===id);
  if(!chosen || !chosen.selectable || this.data.loading || this._paying || this.data.startAttempted)return;
  const generation=++this._generation;
  this.setData({loading:true,error:'',selected:null,quote:null});
  try{
   const port=portView(await scanResultApp.request('POST','/user/scan/port',{port_id:id}));
   if(this._gone || generation!==this._generation)return;
   if(port.port_id!==id || port.device_id!==this.data.deviceId)throw new Error('端口信息不匹配，请刷新重试');
   this.setData({selected:port,ports:this.data.ports.map(p=>p.port_id===id ? port : p)});
  }catch(e){if(!this._gone && generation===this._generation)this.setData({error:e.message || '端口读取失败',needsLogin:!scanResultApp.globalData.token});}
  finally{if(!this._gone && generation===this._generation)this.setData({loading:false});}
 },
 onPullDownRefresh(){return this.load().finally(()=>wx.stopPullDownRefresh());},
 inputKwh(event){if(this.data.startAttempted || this._paying)return;this._generation++;this.setData({estimatedKwh:event.detail.value,quote:null,loading:false});},
 inputMinutes(event){if(this.data.startAttempted || this._paying)return;this._generation++;this.setData({estimatedMinutes:event.detail.value,quote:null,loading:false});},
 async estimate(){
  if(this.data.loading || this._paying || this.data.startAttempted || !this.data.selected?.selectable)return;
  const kwh=this.data.estimatedKwh.trim(),minutes=this.data.estimatedMinutes.trim();
  if(!/^\d{1,3}(\.\d{1,3})?$/.test(kwh) || Number(kwh)<=0 || Number(kwh)>100 || !/^\d{1,4}$/.test(minutes) || Number(minutes)<1 || Number(minutes)>1440){this.setData({quote:null,error:'请输入 0.001–100 度电和 1–1440 分钟'});return;}
  const generation=++this._generation;
  this.setData({loading:true,error:'',quote:null});
  try{
   const quote=await scanResultApp.request('POST','/user/scan/quote',{port_id:this.data.selected.port_id,estimated_kwh:kwh,estimated_minutes:Number(minutes)});
   if(this._gone || generation!==this._generation)return;
   if(!quote || !Number.isSafeInteger(quote.total_cents) || quote.total_cents<=0 || !Number.isSafeInteger(quote.electric_cents) || !Number.isSafeInteger(quote.service_cents) || quote.electric_cents<0 || quote.service_cents<0 || quote.electric_cents+quote.service_cents!==quote.total_cents || !quote.pricing)throw new Error('报价信息异常，请重试');
   this.setData({quote:{...quote,totalText:'¥'+(quote.total_cents/100).toFixed(2),electricText:'¥'+(quote.electric_cents/100).toFixed(2),serviceText:'¥'+(quote.service_cents/100).toFixed(2)}});
  }catch(e){if(!this._gone && generation===this._generation)this.setData({error:e.message || '计费信息读取失败，请重试'});}
  finally{if(!this._gone && generation===this._generation)this.setData({loading:false});}
 },
 async pay(){
  if(this._paying || this.data.loading || this._gone || this._unloaded)return;
  if(this.data.startAttempted && (!this._checkout || !this.data.canRetryPayment))return;
  if(!this._checkout && (!this.data.selected?.selectable || !validQuote(this.data.quote))){this.setData({quote:null,error:'报价已过期，请重新预估费用'});return;}
  this._paying=true;this.setData({paying:true,paymentNotice:'',error:''});
  const sessionGeneration=scanResultApp._generation;
  const quote=this.data.quote,port=this.data.selected;
  try{
   const confirmed=await new Promise(resolve=>wx.showModal({title:'确认付款充电',content:'确认端口 '+port.port_no+'，预付 '+quote.totalText+'。最终按实际充电结算，多余金额原路退回。',success:r=>resolve(r.confirm),fail:()=>resolve(false)}));
   if(!confirmed || this._gone || this._unloaded)return;
   if(sessionGeneration!==scanResultApp._generation || (this._checkout && this._checkoutGeneration!==sessionGeneration))throw new Error('登录账号已变化，请从订单列表重新核实');
   if(!this._checkout){
    if(!validQuote(quote)){this.setData({quote:null,error:'报价已过期，请重新预估费用'});return;}
    this.setData({startAttempted:true});
    const order=await scanResultApp.request('POST','/user/scan/start',{port_id:port.port_id,quote_id:quote.quote_id,estimated_kwh:quote.estimated_kwh,estimated_minutes:quote.estimated_minutes});
    if(sessionGeneration!==scanResultApp._generation)throw new Error('登录账号已变化，请从订单列表重新核实');
    if(!order || typeof order.order_no!=='string' || !order.order_no)throw new Error('订单响应异常，请从订单列表核实');
    this._checkout=order;
    this._checkoutGeneration=sessionGeneration;
    if(!this._unloaded)this.setData({orderNo:order.order_no});
   }
   if(this._gone || this._unloaded)return;
   if(!Number.isFinite(Date.parse(this._checkout.hold_expires_at)) || Date.parse(this._checkout.hold_expires_at)<=Date.now()){this.setData({canRetryPayment:false});throw new Error('付款时限已过，请查看订单状态');}
   const params=paymentParams(this._checkout.payment_params);
   this.setData({canRetryPayment:true});
   await new Promise((resolve,reject)=>wx.requestPayment({...params,success:resolve,fail:reject}));
   if(this._unloaded)return;
   if(sessionGeneration!==scanResultApp._generation){this.setData({canRetryPayment:false,orderNo:'',paymentNotice:'登录账号已变化，请登录原账号查看订单。'});return;}
   this.setData({canRetryPayment:false,paymentNotice:'付款操作已完成，正在核实支付和设备启动状态。'});
   if(this._gone)this._openOrderOnShow=true;else this.viewOrder();
  }catch(e){
   if(!this._unloaded)this.setData({paymentNotice:/cancel/i.test(e.errMsg || '') ? '已取消付款，可继续支付同一订单，或查看订单状态。' : (e.message || '支付结果暂不确定，请查看订单状态，避免重复下单。')});
  }finally{this._paying=false;if(!this._unloaded)this.setData({paying:false});}
 },
 viewOrder(){if(this.data.orderNo)wx.navigateTo({url:'/pages/charge/charging?order_no='+encodeURIComponent(this.data.orderNo)});else this.history();},
 history(){wx.navigateTo({url:'/pages/charge/history'});},
 rescan(){wx.navigateTo({url:'/pages/scan/scan'});},
});
