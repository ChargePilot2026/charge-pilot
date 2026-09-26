const chargingApp=getApp();
const {formatOrder}=require('../../utils/order');
const display=value=>value==null ? '暂无数据' : String(value);
Page({
 data:{snapshot:null,loading:false,error:'',needsLogin:false,stopping:false,stopNotice:''},
 onLoad(query){this._orderId=query.order_id || query.order_no;this._generation=0;this._viewVersion=0;this._gone=false;},
 onShow(){this._gone=false;this.setData({stopping:!!this._stopping});return this.load();},
 onHide(){this.pause();},
 onUnload(){this.pause();},
 pause(){this._gone=true;this._generation++;this._viewVersion++;clearTimeout(this._timer);this._timer=null;},
 async login(){try{await chargingApp.login();if(!this._gone)await this.load();}catch(e){if(!this._gone)this.setData({error:e.message || '登录失败'});}},
 async load(){
  clearTimeout(this._timer);this._timer=null;
  if(this._gone)return;
  const generation=++this._generation;
  if(!this._orderId){this.setData({error:'缺少订单标识',snapshot:null});return;}
  if(!chargingApp.globalData.token){this.setData({needsLogin:true,snapshot:null,loading:false});return;}
  this.setData({loading:true,error:'',needsLogin:false});
  try{
   const s=await chargingApp.request('GET','/user/charge/ongoing/snapshot',{order_id:this._orderId});
   if(this._gone || generation!==this._generation)return;
   this.setData({snapshot:{...s,statusLabel:formatOrder(s).statusLabel,powerText:display(s.current_power_w ?? s.power_w),voltageText:display(s.voltage_v),temperatureText:display(s.temperature_c),energyText:display(s.charged_kwh),durationText:s.elapsed_seconds==null ? '暂无数据' : Math.floor(s.elapsed_seconds/60)+' 分 '+s.elapsed_seconds%60+' 秒',feeText:s.current_fee_cents==null ? '待结算' : '¥'+(s.current_fee_cents/100).toFixed(2)}});
   if(s.poll_continue){const delay=Math.min(30000,Math.max(3000,Number(s.next_poll_after_ms)||5000));this._timer=setTimeout(()=>this.load(),delay);}
  }catch(e){if(!this._gone && generation===this._generation)this.setData({error:e.message || '充电状态读取失败',snapshot:null,needsLogin:!chargingApp.globalData.token});}
  finally{if(!this._gone && generation===this._generation)this.setData({loading:false});}
 },
 onPullDownRefresh(){return this.load().finally(()=>wx.stopPullDownRefresh());},
 async stopCharge(){
  if(this._stopping || this.data.snapshot?.status!=='charging')return;
  this._stopping=true;
  const viewVersion=this._viewVersion;
  const orderNo=this.data.snapshot.order_no;
  this.setData({stopping:true,stopNotice:''});
  try{
   const confirmed=await new Promise(resolve=>wx.showModal({title:'确认停止充电？',content:'将向设备提交停止请求，请等待设备确认。',success:r=>resolve(r.confirm),fail:()=>resolve(false)}));
   if(!confirmed || this._gone || viewVersion!==this._viewVersion)return;
   await chargingApp.request('POST','/user/charge/stop',{order_no:orderNo});
   if(!this._gone && viewVersion===this._viewVersion){this.setData({stopNotice:'停止请求已提交，等待设备确认。'});await this.load();}
  }catch(e){if(!this._gone && viewVersion===this._viewVersion)this.setData({stopNotice:e.message || '停止请求失败，请重试'});}
  finally{this._stopping=false;if(!this._gone)this.setData({stopping:false});}
 },
 detail(){if(this._orderId)wx.navigateTo({url:'/pages/charge/detail?order_id='+encodeURIComponent(this._orderId)});},
});
