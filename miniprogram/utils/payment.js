// Payment UI completion never proves that the order is charging; query the server next.
function paymentParams(value) {
 if(!value || value.signType!=='RSA' || !/^\d+$/.test(value.timeStamp || '') || typeof value.nonceStr!=='string' || !value.nonceStr || typeof value.paySign!=='string' || !value.paySign || !/^prepay_id=\S+$/.test(value.package || ''))throw new Error('支付参数异常，请在订单列表核实状态');
 return {timeStamp:value.timeStamp,nonceStr:value.nonceStr,package:value.package,signType:'RSA',paySign:value.paySign};
}
function validQuote(quote,now=Date.now()) {
 return !!quote && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(quote.quote_id || '') && Number.isFinite(Date.parse(quote.quote_expires_at)) && Date.parse(quote.quote_expires_at)>now;
}
module.exports={paymentParams,validQuote};
