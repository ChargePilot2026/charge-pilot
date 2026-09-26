const stationDetailApp=getApp();
Page({
  data:{station:null,loading:false,error:'',needsLogin:false},
  onLoad(query) { this._id=query.id; this._gone=false; this._generation=0; this.load(); },
  onUnload() { this._gone=true; this._generation++; },
  async login() { try { await stationDetailApp.login(); await this.load(); } catch(e) { this.setData({error:e.message || '登录失败'}); } },
  async load() {
    if (!this._id) { this.setData({error:'缺少站点标识'}); return; }
    if (!stationDetailApp.globalData.token) { this.setData({needsLogin:true}); return; }
    const generation=++this._generation;
    this.setData({loading:true,error:'',station:null,needsLogin:false});
    try { const station=await stationDetailApp.request('GET','/user/station/'+encodeURIComponent(this._id)); if (!this._gone && generation===this._generation) this.setData({station}); }
    catch(e) { if (!this._gone && generation===this._generation) this.setData({error:e.message || '站点查询失败'}); }
    finally { if (!this._gone && generation===this._generation) this.setData({loading:false}); }
  },
  onPullDownRefresh() { this.load().finally(()=>wx.stopPullDownRefresh()); },
  navigate() { const s=this.data.station; if(s) wx.openLocation({latitude:s.latitude,longitude:s.longitude,name:s.name,address:s.address || '',fail:()=>wx.showToast({title:'地图导航打开失败',icon:'none'})}); },
  call() { const s=this.data.station; if(s && s.contact_phone) wx.makePhoneCall({phoneNumber:s.contact_phone}); },
});
