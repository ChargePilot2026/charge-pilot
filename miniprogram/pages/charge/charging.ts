// 充电中页面:5s 轮询 snapshot(技术规格 § 3.2.3)
const app = getApp();

Page({
  data: {
    order_no: '',
    snapshot: null as any,
    timer: 0 as any,
  },

  onLoad(q: any) {
    this.setData({ order_no: q.order_no });
    this.poll();
    const t = setInterval(() => this.poll(), 5000);
    this.setData({ timer: t });
  },

  onUnload() {
    if (this.data.timer) clearInterval(this.data.timer as any);
  },

  async poll() {
    try {
      const snap = await app.request<any>('GET', `/user/charge/ongoing/snapshot?order_id=${this.data.order_no}`);
      this.setData({ snapshot: snap });
      if (!snap.poll_continue) {
        // 充电结束 → 跳详情
        if (this.data.timer) clearInterval(this.data.timer as any);
        wx.redirectTo({ url: `/pages/charge/detail?order_no=${this.data.order_no}` });
      }
    } catch (e: any) {
      wx.showToast({ title: e.message, icon: 'none' });
    }
  },

  async stopCharge() {
    wx.showModal({
      title: '确认停止充电?',
      content: '停止后将按实际充电量结算',
      success: async (r) => {
        if (!r.confirm) return;
        try {
          await app.request('POST', '/user/charge/stop', { order_no: this.data.order_no });
          wx.showToast({ title: '已停止', icon: 'success' });
        } catch (e: any) {
          wx.showToast({ title: e.message, icon: 'none' });
        }
      },
    });
  },
});