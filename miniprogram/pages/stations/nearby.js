const stationApp = getApp();
Page({
  data: { items: [], markers: [], latitude: null, longitude: null, loading: false, error: '', radiusIndex: 1, radii: [1,5,10,20,50], needsLogin: false },
  onLoad() { this._generation=0; this._gone=false; },
  onUnload() { this._gone=true; this._generation++; },
  async login() { try { await stationApp.login(); this.setData({needsLogin:false}); this.locate(); } catch(e) { this.setData({error:e.message || '登录失败'}); } },
  locate() {
    if (!stationApp.globalData.token) { this.setData({needsLogin:true}); return; }
    this.setData({error:'',loading:true});
    wx.getLocation({ type:'gcj02', success:location=>{
      if (this._gone) return;
      this.setData({latitude:location.latitude,longitude:location.longitude,loading:false}); this.load();
    }, fail:()=>{ if (!this._gone) this.setData({loading:false,error:'未能获取位置，请检查定位授权后重试'}); } });
  },
  onRadius(event) { this.setData({radiusIndex:Number(event.detail.value)}); if (this.data.latitude != null) this.load(); },
  async load() {
    if (this.data.latitude == null) { this.locate(); return; }
    const generation=++this._generation;
    this.setData({loading:true,error:'',items:[],markers:[]});
    try {
      const result=await stationApp.request('GET','/user/station/nearby',{lat:this.data.latitude,lng:this.data.longitude,radius_km:this.data.radii[this.data.radiusIndex]});
      if (this._gone || generation!==this._generation) return;
      this.setData({items:result.items.map(s=>({...s,distanceText:s.distance_km.toFixed(2)+' 公里'})),markers:result.items.map(s=>({id:s.id,latitude:s.latitude,longitude:s.longitude,title:s.name}))});
    } catch(e) { if (!this._gone && generation===this._generation) this.setData({error:e.message || '站点查询失败',needsLogin:!stationApp.globalData.token}); }
    finally { if (!this._gone && generation===this._generation) this.setData({loading:false}); }
  },
  onPullDownRefresh() { this.load().finally(()=>wx.stopPullDownRefresh()); },
  open(event) { const id=event.currentTarget.dataset.id || event.detail.markerId; wx.navigateTo({url:'/pages/stations/detail?id='+encodeURIComponent(id)}); },
});
