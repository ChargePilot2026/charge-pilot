import { defineController, getApp, wx } from "../../runtime";
// 首页：功能入口、快捷服务、附近电站列表与底部导航。
const app = getApp();

// 距离按设计稿展示为“距你 60m”，不足 1 公里时换算为米。
function distanceText(km) {
  if (typeof km !== "number" || !Number.isFinite(km)) return "距离未知";
  if (km < 1) return "距你" + Math.round(km * 1000) + "m";
  return "距你" + km.toFixed(1) + "km";
}

export default defineController({
  _locationAttempted: false,
  data: {
    ongoing: null,
    announcements: [],
    latitude: null,
    longitude: null,
    stations: [],
    markers: [],
    mapError: '',
    locationNotice: '',
    locating: false,
  },

  onShow() {
    this.refresh();
    if (!this._locationAttempted) this.locate();
  },

  async refresh() {
    try {
      const ann = await app.request('GET', '/user/announcement/list', undefined, false);
      this.setData({ announcements: ann.items || [] });
    } catch (_error) { this.setData({ announcements: [] }); }
    if (!app.globalData.token) { this.setData({ ongoing: null }); return; }
    try {
      const ong = await app.request('GET', '/user/charge/ongoing');
      this.setData({ ongoing: ong?.order_no ? ong : null });
    } catch (_error) { this.setData({ ongoing: null }); }
  },

  locate() {
    this._locationAttempted = true;
    this.setData({ locating: true, mapError: '', locationNotice: '' });
    wx.getLocation({ type: 'gcj02', success: (location) => {
      this.setData({ latitude: location.latitude, longitude: location.longitude, mapError: '', locating: false });
      this.loadStations();
    }, fail: () => {
      // 未开启定位时接口按创建时间倒序返回最新站点，距离字段为 null。
      this.setData({ locating: false, locationNotice: '未开启定位，以下按创建时间展示最新电站。' });
      this.loadStations();
    } });
  },

  // 重新定位：忽略首次定位标记，强制取一次新坐标并重查附近电站。
  relocate() { this._locationAttempted = false; this.locate(); },

  async loadStations() {
    // 没有坐标时不传参，后端返回最新站点而非按距离排序的结果。
    const located = this.data.latitude !== null && this.data.longitude !== null;
    const query = located ? { latitude: this.data.latitude, longitude: this.data.longitude, radius_km: 5 } : {};
    try {
      const result = await app.request('GET', '/user/station/nearby', query, false);
      const stations = (result.items || []).map(station => ({
        ...station,
        latitude: Number(station.latitude),
        longitude: Number(station.longitude),
        // distance_km 在未开启定位时为 null，不能经 Number() 转换，
        // 否则 null 会变成 0 并被显示成“距你0m”。
        distanceText: distanceText(station.distance_km),
      }));
      this.setData({
        stations,
        markers: stations.map(station => ({ id: station.id, latitude: station.latitude, longitude: station.longitude, title: station.name })),
        mapError: '',
      });
    } catch (error) { this.setData({ mapError: error.message || '附近电站读取失败' }); }
  },

  onScanTap() { wx.navigateTo({ url: '/pages/scan/scan' }); },

  goMap() { wx.navigateTo({ url: '/pages/stations/nearby' }); },
  goAnnouncements() { wx.navigateTo({ url: '/pages/announcement/list' }); },
  goOrders() { wx.navigateTo({ url: '/pages/charge/history' }); },
  goProfile() { wx.reLaunch({ url: '/pages/profile/profile' }); },
  goOngoing() {
    if (this.data.ongoing) {
      wx.navigateTo({ url: `/pages/charge/charging?order_no=${encodeURIComponent(this.data.ongoing.order_no)}` });
    }
  },

  // 常用电站尚无对应页面，明确提示而不跳转到相近但无关的页面。
  comingSoon(title) { wx.showToast({ title, icon: 'none' }); },
  goFrequent() { this.comingSoon('常用电站即将上线'); },
  goDeviceMap() { wx.navigateTo({ url: '/pages/stations/nearby' }); },

  goStation(event) {
    const id = event.currentTarget.dataset.id || event.detail?.markerId;
    wx.navigateTo({ url: '/pages/stations/detail?id=' + encodeURIComponent(id) });
  },
});
