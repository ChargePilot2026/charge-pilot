// A successful WeChat payment UI only means the client submitted payment.
// The order and device state must still be read from the server.
function paymentParams(value) {
 if(!value || value.signType!=='RSA' || !/^\d+$/.test(value.timeStamp || '') || typeof value.nonceStr!=='string' || !value.nonceStr || typeof value.paySign!=='string' || !value.paySign || !/^prepay_id=\S+$/.test(value.package || ''))throw new Error('支付参数异常，请在订单列表核实状态');
 return {timeStamp:value.timeStamp,nonceStr:value.nonceStr,package:value.package,signType:'RSA',paySign:value.paySign};
}
function newRequestId(){
 return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g,c=>{const r=Math.floor(Math.random()*16);return(c==='x'?r:(r&3)|8).toString(16);});
}
module.exports={paymentParams,newRequestId};
