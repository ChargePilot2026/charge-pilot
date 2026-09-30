import { defineController, getApp, wx } from "../../runtime";
const app=getApp();
import {offerView} from '../../utils/scheme';
import {normalizeCode,portView,deviceView} from '../../utils/scan';
import {paymentParams,newRequestId} from '../../utils/payment';

export default defineController({
 data:{deviceId:'',deviceStatus:'',deviceStatusLabel:'',deviceNotice:'',ports:[],selected:null,offers:[],selectedOffer:null,loading:false,error:'',paying:false,paymentNotice:'',paymentNo:'',startAttempted:false,canRetryPayment:false,feeText:'',display:{},stopWhenFull:false},
 onLoad(query){try{this._code=decodeURIComponent(query.code || '');}catch(_){this._code='';}this._generation=0;this._gone=false;this._unloaded=false;},
 onShow(){this._gone=false;if(this._openHistoryOnShow){this._openHistoryOnShow=false;this.history();return;}if(this._paying || this.data.startAttempted)return;return this.load();},
 onHide(){this._gone=true;this._generation++;},
 onUnload(){this._unloaded=true;this.onHide();},
 async load(){
  if(this._paying || this.data.startAttempted)return;
  const generation=++this._generation;
  this.setData({deviceId:'',deviceStatus:'',deviceStatusLabel:'',deviceNotice:'',ports:[],selected:null,offers:[],selectedOffer:null,error:'',loading:true});
  try{
   const code=normalizeCode(this._code);
   const result=await app.request('POST','/user/scan/resolve',{code},false);
   if(this._gone || generation!==this._generation)return;
   if(!result || !['port','device'].includes(result.kind) || typeof result.device_id!=='string' || (result.kind==='device' && !Array.isArray(result.ports)))throw new Error('二维码解析结果异常，请重试');
   const operation=deviceView(result.device_status);
   const ports=(result.kind==='port' ? [result.port] : result.ports).map(portView);
   if(ports.some(p=>p.device_id!==result.device_id||p.device_status!==operation.deviceStatus) || new Set(ports.map(p=>p.port_id)).size!==ports.length)throw new Error('端口信息异常，请重试');
   this.setData({deviceId:result.device_id,...operation,ports,selected:result.kind==='port' ? ports[0] : null});
   if(result.kind==='port' && ports[0].selectable)await this.loadOffers(ports[0].port_id,generation);
  }catch(e){if(!this._gone && generation===this._generation)this.setData({error:e.message || '二维码解析失败'});}
  finally{if(!this._gone && generation===this._generation)this.setData({loading:false});}
 },
 async selectPort(event){
  const id=event.currentTarget.dataset.id;
  const chosen=this.data.ports.find(p=>p.port_id===id);
  if(!chosen || !chosen.selectable || this.data.loading || this._paying || this.data.startAttempted)return;
  const generation=++this._generation;
  this.setData({loading:true,error:'',selected:null,offers:[],selectedOffer:null});
  try{
   const port=portView(await app.request('POST','/user/scan/port',{port_id:id},false));
   if(this._gone || generation!==this._generation)return;
   if(port.port_id!==id || port.device_id!==this.data.deviceId)throw new Error('端口信息不匹配，请刷新重试');
   const operation=deviceView(port.device_status);
   this.setData({...operation,selected:port,ports:this.data.ports.map(p=>portView({...p,...(p.port_id===id?port:{}),device_status:operation.deviceStatus}))});
   if(port.selectable)await this.loadOffers(id,generation);
  }catch(e){if(!this._gone && generation===this._generation)this.setData({error:e.message || '端口读取失败'});}
  finally{if(!this._gone && generation===this._generation)this.setData({loading:false});}
 },
 async loadOffers(portID,generation){
  const result=await app.request('POST','/user/scan/offers',{port_id:portID},false);
  if(this._gone || generation!==this._generation)return;
  if(!result || result.port_id!==portID || !Array.isArray(result.items))throw new Error('充电方案响应异常');
  this.setData({offers:result.items.map(offerView),display:result.display||{},stopWhenFull:!!result.stop_when_full});
 },
 selectOffer(event){
  if(this.data.startAttempted || this._paying)return;
  const id=Number(event.currentTarget.dataset.id);
  const selected=this.data.offers.find(item=>item.id===id);
  if(selected){this._requestID=null;this.setData({selectedOffer:selected,error:''});}
 },
 onPullDownRefresh(){return this.load().finally(()=>wx.stopPullDownRefresh());},
 async pay(){
  if(this._paying || this.data.loading || this._gone || this._unloaded)return;
  if(this.data.startAttempted && this._checkout && !this.data.canRetryPayment)return;
  const port=this.data.selected;
  if(!port?.selectable && !this._checkout){this.setData({error:'请先选择空闲端口'});return;}
  const offer=this.data.selectedOffer;
  if(!offer && !this._checkout){this.setData({error:'请先选择充电方案'});return;}
  this._paying=true;this.setData({paying:true,paymentNotice:'',error:''});
  try{
   if(!app.globalData.token){
    const proceed=await new Promise(resolve=>wx.showModal({title:'登录后继续支付',content:'查看设备无需登录，发起支付前需微信登录。',success:r=>resolve(r.confirm),fail:()=>resolve(false)}));
    if(!proceed || this._gone || this._unloaded)return;
    await app.login();
   }
   if(this._gone || this._unloaded)return;
   const session=app._generation;
   const confirmed=await new Promise(resolve=>wx.showModal({title:'确认充电方案',content:'端口 '+port.port_no+'，'+offer.name+'，支付 '+offer.priceText+'。'+offer.ruleText,success:r=>resolve(r.confirm),fail:()=>resolve(false)}));
   if(!confirmed || this._gone || this._unloaded)return;
   if(session!==app._generation || (this._checkout && this._checkoutGeneration!==session))throw new Error('登录账号已变化，请重新核实');
   if(!this._checkout){
    if(!this._requestID)this._requestID=newRequestId();
    this.setData({startAttempted:true});
    const checkout=await app.request('POST','/user/scan/start',{client_request_id:this._requestID,port_id:port.port_id,offer_id:offer.id});
    if(session!==app._generation)throw new Error('登录账号已变化，请重新核实');
    if(!checkout || !checkout.merchant_order_no || !checkout.payment_params || !Number.isSafeInteger(checkout.payable_cents))throw new Error('支付意图响应异常，请核实支付状态');
    this._checkout=checkout;this._checkoutGeneration=session;
    if(!this._unloaded)this.setData({paymentNo:checkout.merchant_order_no,feeText:'¥'+(checkout.payable_cents/100).toFixed(2)});
   }
   if(Date.parse(this._checkout.expires_at)<=Date.now()){this.setData({canRetryPayment:false});throw new Error('支付意图已过期，请查看订单记录');}
   const params=paymentParams(this._checkout.payment_params);
   this.setData({canRetryPayment:true});
   await new Promise((resolve,reject)=>wx.requestPayment({...params,success:resolve,fail:reject}));
   if(this._unloaded)return;
   if(session!==app._generation){this.setData({canRetryPayment:false,paymentNotice:'登录账号已变化，请登录原账号核实支付。'});return;}
   this.setData({canRetryPayment:false,paymentNotice:'付款操作已完成，正在等待服务端确认支付和设备启动。'});
   if(this._gone)this._openHistoryOnShow=true;else this.history();
  }catch(e){
   if(!this._unloaded)this.setData({paymentNotice:!this.data.startAttempted && (!app.globalData.token || e.status===401) ? '登录已失效，请重新登录后支付。' : /cancel/i.test(e.errMsg || '') ? '已取消付款，可继续支付同一支付单，或查看订单记录。' : (e.message || '支付结果暂不确定，请查看订单记录，避免重复付款。')});
  }finally{this._paying=false;if(!this._unloaded)this.setData({paying:false});}
 },
 history(){wx.navigateTo({url:'/pages/charge/history'});},
 rescan(){wx.navigateTo({url:'/pages/scan/scan'});},
});
