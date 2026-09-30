const test=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const vm=require('node:vm');
const {createRequire}=require('node:module');

function page(name,app,wx={}){
 const filename=path.resolve(__dirname,`../pages/${name}/${name}.js`);let definition;
 vm.runInNewContext(fs.readFileSync(filename,'utf8'),{getApp:()=>app,Page:value=>definition=value,require:createRequire(filename),wx:{stopPullDownRefresh(){},...wx}},{filename});
 const p={...definition,data:structuredClone(definition.data)};p.setData=value=>Object.assign(p.data,value);p.onLoad({code:'DEV00001%3A1'});return p;
}
const port=(n,status='idle')=>({port_id:`DEV00001:${n}`,port_code:`DEV00001:${n}`,device_id:'DEV00001',port_no:n,port_status:status,online:true,available:status==='idle'});
const offer={id:7,station_id:1,code:'P60',name:'60 分钟套餐',mode:'duration',price_cents:600,duration_minutes:60};
const checkout=()=>({merchant_order_no:'PAY_TEST',payable_cents:600,expires_at:new Date(Date.now()+60000).toISOString(),payment_params:{timeStamp:'1234567890',nonceStr:'nonce',package:'prepay_id=test',signType:'RSA',paySign:'signed'}});
function app(token='t'){
 const calls=[];const result={globalData:{token},_generation:0,calls,login:async()=>{result.globalData.token='t';result._generation++;},request:async(method,url,body,auth)=>{
  calls.push({method,url,body,auth});
  if(url.endsWith('/resolve'))return {kind:'port',device_id:'DEV00001',port:port(1)};
  if(url.endsWith('/offers'))return {port_id:'DEV00001:1',items:[offer]};
  if(url.endsWith('/port'))return port(1);
  return checkout();
 }};return result;
}
async function selectedPage(application,wx={}){const p=page('scan-result',application,wx);await p.onShow();p.selectOffer({currentTarget:{dataset:{id:7}}});return p;}

test('scanner forwards raw QR and anonymous device discovery does not log in',async()=>{
 const routes=[];const application=app('');let logins=0;application.login=async()=>{logins++;};
 const p=page('scan',application,{scanCode:options=>options.success({result:'DEV00001:1'}),navigateTo:options=>{routes.push(options.url);options.success();}});
 await p.scan();assert.equal(logins,0);assert.equal(routes[0],'/pages/scan-result/scan-result?code=DEV00001%3A1');
});
test('device and port responses use the current nested gateway contract',async()=>{
 const application=app('');application.request=async(_method,url)=>url.endsWith('/resolve')?{kind:'device',device_id:'DEV00001',ports:[port(1),port(2,'fault')]}:url.endsWith('/port')?port(1):{port_id:'DEV00001:1',items:[offer]};
 const p=page('scan-result',application);await p.onShow();assert.equal(p.data.ports[0].statusLabel,'空闲');assert.equal(p.data.ports[1].selectable,false);
 await p.selectPort({currentTarget:{dataset:{id:'DEV00001:1'}}});assert.equal(p.data.selected.port_no,1);assert.equal(p.data.offers[0].priceText,'¥6.00');
});
test('anonymous port browsing requests offers without login',async()=>{
 const application=app('');const p=page('scan-result',application);await p.onShow();assert.equal(p.data.selected.port_no,1);assert.equal(p.data.offers.length,1);assert.equal(application.calls.length,2);assert.ok(application.calls.every(call=>call.auth===false));
});
test('payment checks login after offer selection and submits only the configured offer ID',async()=>{
 const application=app('');let paid=0;const routes=[];
 const p=await selectedPage(application,{showModal:options=>options.success({confirm:true}),requestPayment:options=>{paid++;options.success({});},navigateTo:options=>routes.push(options.url)});
 await p.pay();assert.equal(paid,1);assert.equal(application._generation,1);
 const start=application.calls.find(call=>call.url.endsWith('/start'));
 assert.equal(start.body.offer_id,7);assert.equal(start.body.port_id,'DEV00001:1');assert.match(start.body.client_request_id,/^[0-9a-f-]{36}$/);
 assert.equal(start.body.estimated_kwh,undefined);assert.equal(start.body.estimated_minutes,undefined);
 assert.equal(routes[0],'/pages/charge/history');assert.equal(p.data.paymentNo,'PAY_TEST');
});
test('cancelled WeChat UI retries the same payment intent without a second start',async()=>{
 const application=app();let payments=0;
 const p=await selectedPage(application,{showModal:options=>options.success({confirm:true}),requestPayment:options=>{payments++;if(payments===1)options.fail({errMsg:'requestPayment:fail cancel'});else options.success({});},navigateTo:()=>{}});
 await p.pay();assert.match(p.data.paymentNotice,/取消/);await p.pay();
 assert.equal(application.calls.filter(call=>call.url.endsWith('/start')).length,1);assert.equal(payments,2);
});
test('uncertain start reuses its request ID and cannot silently change the offer',async()=>{
 const application=app();const ids=[];application.request=async(method,url,body,auth)=>{
  if(url.endsWith('/resolve'))return {kind:'port',device_id:'DEV00001',port:port(1)};
  if(url.endsWith('/offers'))return {port_id:'DEV00001:1',items:[offer]};
  ids.push(body.client_request_id);throw new Error('network timeout');
 };
 const p=await selectedPage(application,{showModal:options=>options.success({confirm:true})});await p.pay();await p.pay();
 assert.equal(ids.length,2);assert.equal(ids[0],ids[1]);assert.equal(p.data.startAttempted,true);assert.match(p.data.paymentNotice,/timeout/);
 p.selectOffer({currentTarget:{dataset:{id:7}}});assert.equal(p._requestID,ids[0]);
});
