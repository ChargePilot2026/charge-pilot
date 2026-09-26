const scanResultApp=getApp();
const {normalizeCode,portView}=require('../../utils/scan');
Page({
 data:{deviceId:'',ports:[],selected:null,loading:false,error:'',needsLogin:false,quote:null,estimatedKwh:'0.500',estimatedMinutes:'120'},
 onLoad(query){try{this._code=decodeURIComponent(query.code || '');}catch(_){this._code='';}this._generation=0;this._gone=false;},
 onShow(){this._gone=false;return this.load();},
 onHide(){this._gone=true;this._generation++;},
 onUnload(){this.onHide();},
 async login(){try{await scanResultApp.login();if(!this._gone)await this.load();}catch(e){if(!this._gone)this.setData({error:e.message || '登录失败'});}},
 async load(){
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
  if(!chosen || !chosen.selectable || this.data.loading)return;
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
 inputKwh(event){this._generation++;this.setData({estimatedKwh:event.detail.value,quote:null,loading:false});},
 inputMinutes(event){this._generation++;this.setData({estimatedMinutes:event.detail.value,quote:null,loading:false});},
 async estimate(){
  if(this.data.loading || !this.data.selected?.selectable)return;
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
 rescan(){wx.navigateTo({url:'/pages/scan/scan'});},
});
