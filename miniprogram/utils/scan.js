function normalizeCode(value){
 if(typeof value!=='string' || !value || value.length>64 || /[\s\u0000-\u001f\u007f]/.test(value))throw new Error('二维码内容无效，请扫描充电设备或端口上的二维码');
 return value;
}
const labels={idle:'空闲',charging:'使用中',reserved:'启动处理中',full:'已充满',fault:'故障',disabled:'已停用'};
function portView(port){
 if(!port || typeof port.device_id!=='string' || typeof port.port_id!=='string' || !Number.isInteger(port.port_no) || port.port_no<1 || port.port_no>255 || !labels[port.status])throw new Error('端口信息异常，请刷新重试');
 return {...port,statusLabel:labels[port.status],selectable:port.status==='idle'};
}
module.exports={normalizeCode,portView};
