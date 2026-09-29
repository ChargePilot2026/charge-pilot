const announcementApp = getApp();
Page({
  data: { items: [], loading: false, error: '', needsLogin: false },
  onShow() { this._gone = false; this._generation = (this._generation || 0) + 1; this.load(); },
  onHide() { this._gone = true; this._generation++; },
  onUnload() { this.onHide(); },
  onPullDownRefresh() { this.load().finally(() => wx.stopPullDownRefresh()); },
  async load() {
    const generation = ++this._generation;
    this.setData({ loading: true, error: '' });
    try {
      const result = await announcementApp.request('GET', '/user/announcement/list', undefined, false);
      if (this._gone || generation !== this._generation) return;
      if (!result || !Array.isArray(result.items)) throw new Error('公告列表响应异常');
      this.setData({ items: result.items.map(item => ({ ...item, timeText: item.start_at ? new Date(item.start_at).toLocaleString() : '' })) });
    } catch (error) { if (!this._gone && generation === this._generation) this.setData({ error: error.message || '公告读取失败' }); }
    finally { if (!this._gone && generation === this._generation) this.setData({ loading: false }); }
  },
});
