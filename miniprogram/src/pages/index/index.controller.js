import { defineController, getApp, wx } from "../../runtime";
// 首页： 扫码、公告、附近站点与充电中入口。
const app = getApp();

export default defineController({
  _locationAttempted: false,
  data: {
    ongoing: null                                               ,
    announcements: []         ,
    latitude: null                 ,
    longitude: null                 ,
    stations: []         ,
    markers: []         ,
    mapError: '',
  },

  onShow() {
    this.refresh();
    if (!this._locationAttempted) this.locate();
  },

  async refresh() {
    try {
      const ann = await app.request                  ('GET', '/user/announcement/list', undefined, false);
      this.setData({ announcements: ann.items || [] });
    } catch (_error) { this.setData({ announcements: [] }); }
    if (!app.globalData.token) { this.setData({ ongoing: null }); return; }
    try {
      const ong = await app.request     ('GET', '/user/charge/ongoing');
      this.setData({ ongoing: ong?.order_no ? ong : null });
    } catch (_error) { this.setData({ ongoing: null }); }
  },
  locate() {
    this._locationAttempted = true;
    wx.getLocation({ type: 'gcj02', success: (location     ) => {
      this.setData({ latitude: location.latitude, longitude: location.longitude, mapError: '' });
      this.loadStations();
    }, fail: () => this.setData({ mapError: '定位未开启，可在地图页重试。' }) });
  },
  async loadStations() {
    if (this.data.latitude === null || this.data.longitude === null) return;
    try {
      const result = await app.request                  ('GET', '/user/station/nearby', { latitude: this.data.latitude, longitude: this.data.longitude, radius_km: 5 }, false);
      const stations = (result.items || []).map(station => ({ ...station, latitude: Number(station.latitude), longitude: Number(station.longitude) }));
      this.setData({ stations, markers: stations.map(station => ({ id: station.id, latitude: station.latitude, longitude: station.longitude, title: station.name })) });
    } catch (error     ) { this.setData({ mapError: error.message || '附近站点读取失败' }); }
  },

  onScanTap() {
    wx.navigateTo({ url: '/pages/scan/scan' });
  },

  goMap() { wx.navigateTo({ url: '/pages/stations/nearby' }); },
  goAnnouncements() { wx.navigateTo({ url: '/pages/announcement/list' }); },
  goProfile() { wx.switchTab({ url: '/pages/profile/profile' }); },
  goStation(event     ) { const id = event.currentTarget.dataset.id || event.detail.markerId; wx.navigateTo({ url: '/pages/stations/detail?id=' + encodeURIComponent(id) }); },
  goOngoing() {
    if (this.data.ongoing) {
      wx.navigateTo({ url: `/pages/charge/charging?order_no=${encodeURIComponent(this.data.ongoing.order_no)}` });
    }
  },
});
