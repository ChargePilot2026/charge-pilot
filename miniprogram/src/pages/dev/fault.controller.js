import { defineController, getApp, wx } from "../../runtime";
const faultApp = getApp();
const types = ['机械损坏', '电气异常', '通讯异常', '显示异常', '其他'];
const values = ['mechanical', 'electrical', 'communication', 'display', 'other'];
const statuses = { open: '待派单', dispatched: '处理中', fixed: '已修复', closed: '已关闭' };
const eventLabels = { reported: '已提交报修', dispatched: '已派单', reassigned: '已重新指派', fixed: '已标记修复', closed: '已关闭' };
export default defineController({
  data: { deviceId: '', faultTypeIndex: 0, faultTypes: types, description: '', submitting: false, needsLogin: false, error: '', historyError: '', notice: '', reports: [], reportsPage: 1, reportsHasMore: false, loadingReports: false, expandedReport: '', reportHistory: [], reportHistoryLoading: false, reportHistoryError: '' },
  onLoad(options) { this._gone = false; this._deviceId = (options.device_id || '').trim(); this.setData({ deviceId: this._deviceId }); },
  onShow() { this._gone = false; if (faultApp.globalData.token) void this.loadReports(true); },
  onHide() { this._gone = true; this._reportsGeneration = (this._reportsGeneration || 0) + 1; this._historyGeneration = (this._historyGeneration || 0) + 1; this.setData({ loadingReports: false, reportHistoryLoading: false }); },
  onUnload() { this.onHide(); },
  async login() { try { await faultApp.login(); if (!this._gone) { this.setData({ needsLogin: false }); await this.loadReports(true); } } catch (error) { if (!this._gone) this.setData({ error: error.message || '登录失败' }); } },
  onType(event) { this.setData({ faultTypeIndex: Number(event.detail.value) }); },
  onDescription(event) { this.setData({ description: event.detail.value }); },
  retryReports() { return this.loadReports(true); },
  loadMoreReports() { return this.loadReports(false); },
  async toggleHistory(event) {
    const reportId = String(event.currentTarget.dataset.reportId || '');
    if (!reportId || this._gone) return;
    if (this.data.expandedReport === reportId) {
      this._historyGeneration = (this._historyGeneration || 0) + 1;
      this.setData({ expandedReport: '', reportHistory: [], reportHistoryLoading: false, reportHistoryError: '' });
      return;
    }
    const generation = (this._historyGeneration || 0) + 1;
    this._historyGeneration = generation;
    this.setData({ expandedReport: reportId, reportHistory: [], reportHistoryLoading: true, reportHistoryError: '' });
    try {
      const result = await faultApp.request('GET', `/user/device/fault-reports/${encodeURIComponent(reportId)}/history`, { page: 1, page_size: 100 });
      if (this._gone || generation !== this._historyGeneration) return;
      const items = Array.isArray(result?.items) ? result.items.map((item) => ({
        ...item,
        eventText: eventLabels[item.event_type] || item.event_type,
        fromStatusText: statuses[item.from_status] || item.from_status || '',
        toStatusText: statuses[item.to_status] || item.to_status || '',
        createdText: String(item.created_at || '').replace('T', ' ').slice(0, 16),
      })) : [];
      this.setData({ reportHistory: items });
    } catch (error) {
      if (!this._gone && generation === this._historyGeneration) this.setData({ reportHistoryError: error.message || '处理记录读取失败' });
    } finally { if (!this._gone && generation === this._historyGeneration) this.setData({ reportHistoryLoading: false }); }
  },
  retryReportHistory(event) {
    const reportId = String(event.currentTarget.dataset.reportId || this.data.expandedReport);
    this.setData({ expandedReport: '', reportHistory: [], reportHistoryError: '' }, () => this.toggleHistory({ currentTarget: { dataset: { reportId } } }));
  },
  async loadReports(reset = false) {
    if (!faultApp.globalData.token || this.data.loadingReports || this._gone) return;
    const page = reset ? 1 : this.data.reportsPage + 1;
    if (!reset && !this.data.reportsHasMore) return;
    const generation = (this._reportsGeneration || 0) + 1;
    this._reportsGeneration = generation;
    this.setData({ loadingReports: true, historyError: '' });
    try {
      const result = await faultApp.request('GET', '/user/device/fault-reports', { page, page_size: 20 });
      if (this._gone || generation !== this._reportsGeneration) return;
      const items = Array.isArray(result?.items) ? result.items.map((item) => ({ ...item, statusText: statuses[item.status] || item.status, createdText: String(item.created_at || '').replace('T', ' ').slice(0, 16) })) : [];
      this.setData({ reports: reset ? items : this.data.reports.concat(items), reportsPage: page, reportsHasMore: reset ? items.length < (result?.total || 0) : (page * 20) < (result?.total || 0) });
    } catch (error) {
      if (!this._gone && generation === this._reportsGeneration) this.setData({ historyError: error.message || '报修进度读取失败' });
    } finally { if (!this._gone && generation === this._reportsGeneration) this.setData({ loadingReports: false }); }
  },
  async submit() {
    if (this.data.submitting || this._gone) return;
    if (!faultApp.globalData.token) { this.setData({ needsLogin: true, error: '请先登录后提交报修' }); return; }
    if (!this._deviceId || !/^[A-Za-z0-9_-]{1,64}$/.test(this._deviceId)) { this.setData({ error: '设备编号无效，请从订单或设备页面进入报修' }); return; }
    if (this.data.description.length > 2000) { this.setData({ error: '故障说明最多 2000 字' }); return; }
    const generation = faultApp._generation;
    this._requesting = true;
    this.setData({ submitting: true, error: '', notice: '' });
    try {
      const result = await faultApp.request('POST', '/user/device/report-fault', { device_id: this._deviceId, fault_type: values[this.data.faultTypeIndex], description: this.data.description.trim() || null });
      if (this._gone || generation !== faultApp._generation) return;
      if (!result || result.submitted !== true) throw new Error('报修结果未确认，请重试');
      this.setData({ notice: `报修已收到（${result.report_id || '已提交'}），可在下方查看处理进度。`, description: '' });
      await this.loadReports(true);
    } catch (error) {
      if (!this._gone && generation === faultApp._generation) this.setData({ error: error.message || '提交失败，请重试', needsLogin: !faultApp.globalData.token });
    } finally { this._requesting = false; if (!this._gone) this.setData({ submitting: false }); }
  },
});
