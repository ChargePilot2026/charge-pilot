// Local development fixtures use public admin APIs and preserve existing edits.
// Run from the repository root after scripts/dev/start.ps1, with Node.js 24 or later.
const base='http://127.0.0.1:8080/api/v1';let token;
async function call(method,path,body){const r=await fetch(base+path,{method,headers:{'Content-Type':'application/json',...(token?{Authorization:'Bearer '+token}:{})},...(body?{body:JSON.stringify(body)}:{})});const j=await r.json();if(!r.ok||j.code!==0)throw Error(method+' '+path+' '+JSON.stringify(j));return j.data;}
const login=await call('POST','/admin/auth/login',{username:process.env.ADMIN_BOOTSTRAP_USER||'admin',password:process.env.ADMIN_BOOTSTRAP_PASSWORD||'ChangeMe!Admin2026'});token=login.token||login.access_token;
const vs=await call('GET','/admin/vendors');let vendor=vs.items.find(v=>v.vendor_code==='LOCAL_DC589');
if(!vendor)vendor=await call('POST','/admin/vendors',{vendor_code:'LOCAL_DC589',vendor_name:'本地 DC589 模拟设备',adapter_class:'dc589',protocol:'tcp',status:'enabled'});
const stations=await call('GET','/admin/stations');let station=stations.items.find(s=>s.name==='双模拟器联调站');if(!station)station=await call('POST','/admin/stations',{name:'双模拟器联调站',address:'本地开发环境',longitude:116.397,latitude:39.908,status:'active'});
console.log('station',station,'vendor',vendor.id);
const devices=(await call('GET','/admin/devices')).items;
const ids=['5348240514082652','5348240514082653'];const missing=ids.filter(id=>!devices.find(d=>d.device_id===id));
if(missing.length)console.log('import',await call('POST','/admin/device-imports',{import_id:crypto.randomUUID(),devices:missing.map(device_id=>({device_id,vendor_id:vendor.id,station_id:station.id,port_count:2,model:'DC589 模拟器'}))}));
let schemes=(await call('GET','/admin/settings/charging-schemes')).items;let tpl=schemes.find(t=>t.scheme.name==='双模拟器测试方案');
const scheme={name:'双模拟器测试方案',amount:{algorithm:'server_max_power',periods:[{end_minute:1440,tiers:[{max_watts:200,electric_cents:50,service_cents:10},{max_watts:1000,electric_cents:100,service_cents:20},{max_watts:3500,electric_cents:200,service_cents:30}]}]},energy:{electric_cents:100,service_cents:10},packages:[{id:1,name:'1元预算',mode:'amount',price_cents:100},{id:2,name:'1度电',mode:'energy',kwh:1,price_cents:110},{id:3,name:'1分钟联调',mode:'duration',minutes:1,price_cents:1},{id:4,name:'60分钟',mode:'duration',minutes:60,price_cents:100}],policy:{free_minutes:0,min_electric_cents:0,max_minutes:600,channel_bp:10000,loss_rate_bp:0},stop:{},card:{package_id:4,max_minutes:600},display:{show_energy:true,show_power:true,show_tariff:true,show_fee_split:true,fee_split_inline:true,show_fee_on_end:true,show_method:true,show_rule:true}};
if(!tpl){tpl=await call('POST','/admin/settings/charging-schemes',{scheme});tpl.version=1;}
const effective=await call('GET',`/admin/stations/${station.id}/charging-scheme`);
if(!effective?.scheme)console.log('apply',await call('POST','/admin/settings/charging-schemes/apply',{request_id:crypto.randomUUID(),station_id:station.id,template_id:tpl.id,template_version:tpl.version,expected_version:0}));
console.log('ready',ids);
