function normalizeCode(value){
 if(typeof value!=='string' || !value || value.length>2048 || /[\u0000-\u001f\u007f]/.test(value))throw new Error('二维码内容无效，请扫描充电设备或端口上的二维码');
 return value;
}
const labels={idle:'空闲',charging:'使用中',reserved:'启动处理中',full:'已充满',fault:'故障',disabled:'已停用'};
const deviceLabels={enabled:'已启用',disabled:'已禁用',fault:'设备故障',retired:'已退役'};
function deviceView(status){
 if(!deviceLabels[status])throw new Error('设备运营状态异常，请刷新重试');
 return {deviceStatus:status,deviceStatusLabel:deviceLabels[status],deviceNotice:status==='enabled'?'':'该设备已暂停服务，请选择其他设备。'};
}
function portView(port){
 const status=port?.port_status;
 if(!port || typeof port.device_id!=='string' || typeof port.port_id!=='string' || !Number.isInteger(port.port_no) || port.port_no<1 || port.port_no>255 || !labels[status])throw new Error('端口信息异常，请刷新重试');
 const operation=port.device_status;
 if(!deviceLabels[operation])throw new Error('设备运营状态异常，请刷新重试');
 const active=operation==='enabled';
 return {...port,status,statusLabel:!active ? deviceLabels[operation] : port.online===false ? '离线' : labels[status],selectable:active && port.online!==false && status==='idle' && port.available===true};
}
export {normalizeCode,portView,deviceView};
