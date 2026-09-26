const test=require('node:test');const assert=require('node:assert/strict');const fs=require('node:fs');const path=require('node:path');const vm=require('node:vm');const {createRequire}=require('node:module');
function page(name,app,wx={}){
 const filename=path.resolve(__dirname,`../pages/${name}/${name}.js`);let definition;
 vm.runInNewContext(fs.readFileSync(filename,'utf8'),{getApp:()=>app,Page:v=>definition=v,require:createRequire(filename),wx:{stopPullDownRefresh(){},...wx}},{filename});
 const p={...definition,data:structuredClone(definition.data)};p.setData=v=>Object.assign(p.data,v);p.onLoad({code:'DEV00001%3A1'});return p;
}
const port=(n,status='idle')=>({port_id:'DEV00001:'+n,port_code:'DEV00001:'+n,device_id:'DEV00001',port_no:n,status});
test('scanner login and navigation preserve canonical port code without creating orders',async()=>{
 const urls=[];let logins=0;const app={globalData:{token:''},login:async()=>{logins++;app.globalData.token='t';}};
 const p=page('scan',app,{scanCode:opts=>opts.success({result:'DEV00001:1'}),navigateTo:opts=>{urls.push(opts.url);opts.success();}});
 await p.scan();assert.equal(logins,1);assert.equal(urls[0],'/pages/scan-result/scan-result?code=DEV00001%3A1');assert.equal(p.data.busy,false);
});
test('scan cancellation is silent; invalid manual input never logs in or navigates',async()=>{
 const p=page('scan',{globalData:{token:''},login:()=>assert.fail('unexpected login')},{scanCode:opts=>opts.fail({errMsg:'scanCode:fail cancel'})});
 await p.scan();assert.equal(p.data.error,'');p.data.manualCode='not a code';await p.manual();assert.match(p.data.error,/二维码内容无效/);
});
test('device selection refreshes occupied state and forwards printed port code',async()=>{
 const calls=[];const p=page('scan-result',{globalData:{token:'t'},request:async(_,url,body)=>{calls.push({url,body});return url.endsWith('resolve') ? {kind:'device',device_id:'DEV00001',ports:[port(1),port(2,'fault')]} : port(1,'charging');}});
 await p.onShow();assert.equal(calls[0].body.code,'DEV00001:1');assert.equal(p.data.ports[1].selectable,false);
 await p.selectPort({currentTarget:{dataset:{id:'DEV00001:2'}}});assert.equal(calls.length,1);
 await p.selectPort({currentTarget:{dataset:{id:'DEV00001:1'}}});assert.equal(calls[1].body.port_id,'DEV00001:1');assert.equal(p.data.selected.status,'charging');assert.equal(p.data.ports[0].selectable,false);
 assert.ok(calls.every(c=>['/user/scan/resolve','/user/scan/port'].includes(c.url)));
});
test('port scan selects canonical port and errors clear earlier results',async()=>{
 let fail=false;const p=page('scan-result',{globalData:{token:'t'},request:async()=>{if(fail)throw new Error('设备已停用');return {kind:'port',...port(7)};}});
 await p.onShow();assert.equal(p.data.selected.port_no,7);fail=true;await p.load();assert.equal(p.data.selected,null);assert.equal(p.data.ports.length,0);assert.equal(p.data.error,'设备已停用');
});
test('reserved port stays visible but cannot be selected or quoted',async()=>{
 const calls=[];const p=page('scan-result',{globalData:{token:'t'},request:async(_,url)=>{calls.push(url);return {kind:'device',device_id:'DEV00001',ports:[port(1,'reserved'),port(2)]};}});
 await p.onShow();assert.equal(p.data.error,'');assert.equal(p.data.ports[0].statusLabel,'启动处理中');assert.equal(p.data.ports[0].selectable,false);
 await p.selectPort({currentTarget:{dataset:{id:'DEV00001:1'}}});assert.equal(calls.length,1);assert.equal(p.data.selected,null);
});
test('hidden scan page ignores late responses and anonymous page offers login',async()=>{
 let resolve;const app={globalData:{token:'t'},request:()=>new Promise(r=>resolve=r)};const p=page('scan-result',app);const pending=p.onShow();p.onHide();resolve({kind:'port',...port(1)});await pending;assert.equal(p.data.selected,null);
 app.globalData.token='';await p.onShow();assert.equal(p.data.needsLogin,true);assert.equal(p.data.loading,false);
});
test('fee preview sends explicit estimates, shows cents and invalidates on edits',async()=>{
 const calls=[];const p=page('scan-result',{globalData:{token:'t'},request:async(_,url,body)=>{calls.push({url,body});return url.endsWith('resolve') ? {kind:'port',...port(1)} : {electric_cents:50,service_cents:20,total_cents:70,pricing:{name:'Station tariff'}};}});
 await p.onShow();await p.estimate();assert.equal(p.data.quote.totalText,'¥0.70');assert.equal(calls[1].body.estimated_kwh,'0.500');assert.equal(calls[1].body.estimated_minutes,120);
 p.inputKwh({detail:{value:'1.000'}});assert.equal(p.data.quote,null);p.inputMinutes({detail:{value:'0'}});await p.estimate();assert.equal(calls.length,2);assert.match(p.data.error,/1–1440/);
});
test('quote response cannot replace a changed estimate',async()=>{
 let resolve;const p=page('scan-result',{globalData:{token:'t'},request:async(_,url)=>url.endsWith('resolve') ? {kind:'port',...port(1)} : new Promise(r=>resolve=r)});
 await p.onShow();const pending=p.estimate();p.inputKwh({detail:{value:'2.000'}});resolve({electric_cents:50,service_cents:20,total_cents:70,pricing:{name:'old'}});await pending;assert.equal(p.data.quote,null);
});
