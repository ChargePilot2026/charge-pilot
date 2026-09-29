function normalizeCode(value){
 if(typeof value!=='string' || !value || value.length>2048 || /[\u0000-\u001f\u007f]/.test(value))throw new Error('二维码内容无效，请扫描充电设备或端口上的二维码');
 return value;
}
const labels={idle:'空闲',charging:'使用中',reserved:'启动处理中',full:'已充满',fault:'故障',disabled:'已停用'};
function portView(port){
 const status=port?.port_status;
 if(!port || typeof port.device_id!=='string' || typeof port.port_id!=='string' || !Number.isInteger(port.port_no) || port.port_no<1 || port.port_no>255 || !labels[status])throw new Error('端口信息异常，请刷新重试');
 return {...port,status,statusLabel:labels[status],selectable:status==='idle' && port.available===true};
}
module.exports={normalizeCode,portView};
