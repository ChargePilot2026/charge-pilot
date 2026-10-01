import { defineController, getApp, wx } from "../../runtime";
const stationDetailApp=getApp();

// 在线状态由服务端按最后心跳时间判定（1 小时内为在线）。
// 客户端只负责展示，不重复推导这条业务规则。
function deviceView(device) {
  const online = device?.online === true;
  const known = device?.runtime_available !== false;
  return {
    device_id: device?.device_id || '',
    online,
    // 运行态取不到时不谎称离线，明确表达为状态未知。
    statusText: online ? '在线' : (known ? '离线' : '状态未知'),
    lastHeartbeatText: device?.last_heartbeat_at ? formatTime(device.last_heartbeat_at) : '从未上报',
    clickable: online,
  };
}

function formatTime(value) {
  const at = new Date(value);
  if (Number.isNaN(at.getTime())) return '时间未知';
  const pad = (n) => String(n).padStart(2,'0');
  return `${at.getFullYear()}-${pad(at.getMonth()+1)}-${pad(at.getDate())} ${pad(at.getHours())}:${pad(at.getMinutes())}`;
}

export default defineController({
  data:{station:null,announcements:[],devices:[],loading:false,error:'',needsLogin:false},
  onLoad(query) { this._id=query.id; this._gone=false; this._generation=0; this.load(); },
  onUnload() { this._gone=true; this._generation++; },
  async load() {
    if (!this._id) { this.setData({error:'缺少站点标识'}); return; }
    const generation=++this._generation;
    this.setData({loading:true,error:'',station:null,needsLogin:false});
    try {
      const station=await stationDetailApp.request('GET','/user/station/'+encodeURIComponent(this._id),undefined,false);
      if (this._gone || generation!==this._generation) return;
      this.setData({
        station,
        announcements: station?.announcements || [],
        devices: (station?.devices || []).map(deviceView),
      });
    }
    catch(e) { if (!this._gone && generation===this._generation) this.setData({error:e.message || '站点查询失败'}); }
    finally { if (!this._gone && generation===this._generation) this.setData({loading:false}); }
  },
  onPullDownRefresh() { this.load().finally(()=>wx.stopPullDownRefresh()); },

  // 仅在线设备可点；离线卡片不绑定该事件。
  openDevice(event) {
    const id = event.currentTarget.dataset.id;
    const device = this.data.devices.find((item) => item.device_id === id);
    if (!device || !device.clickable) return;
    wx.showToast({ title:'设备 ' + id + ' 即将支持快捷充电', icon:'none' });
  },
});
