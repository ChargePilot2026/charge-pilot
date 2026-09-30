import test from 'node:test';
import assert from 'node:assert/strict';
import {blankScheme,priceEnergy,missingInputs,validateScheme,switchAmountAlgorithm} from '../src/pages/schemes/model.ts';

const durationScheme=()=>({...blankScheme(),name:'完整方案',packages:[{id:1,name:'120分钟',mode:'duration',minutes:120,price_cents:200}],card:{package_id:1,max_minutes:600}});
test('whole scheme validation requires enabled modes to have matching packages',()=>{
 const s=durationScheme();assert.deepEqual(missingInputs(s),[]);
 s.energy={electric_cents:80,service_cents:20};assert.ok(missingInputs(s).includes('电量套餐'));
});
test('energy price uses fixed electric and service rates without altering duration package',()=>{
 const s=durationScheme();s.energy={electric_cents:83,service_cents:17};s.packages.push({id:2,name:'3度',mode:'energy',kwh:3,price_cents:1});
 const result=priceEnergy(s);assert.equal(result.packages[1].price_cents,300);assert.equal(result.packages[0].price_cents,200);assert.equal(s.packages[1].price_cents,1);assert.deepEqual(missingInputs(result),[]);
});
test('missing rates and reversed power ceilings are actionable before preview or save',()=>{
 const s=durationScheme();s.amount={algorithm:'server_max_power',periods:[{end_minute:1440,tiers:[{max_watts:200,electric_cents:80,service_cents:NaN},{max_watts:100,electric_cents:160,service_cents:80}]}]};s.packages.push({id:2,name:'3元',mode:'amount',price_cents:300});
 const errors=missingInputs(s);assert.ok(errors.includes('时段1档位1服务费'));assert.ok(errors.includes('时段1档位2功率上限'));
});
test('card duration cannot exceed its whole-purchase cumulative limit',()=>{
 const s=durationScheme();s.card.max_minutes=60;assert.ok(missingInputs(s).includes('刷卡套餐超过累计上限'));
});

test('validation identifies only invalid package fields with stable package IDs',()=>{
 const s=durationScheme();s.packages[0].name='';s.packages.push({id:7,name:'预算',mode:'amount',price_cents:NaN});
 const issues=validateScheme(s);
 assert.deepEqual(issues.find(i=>i.message==='套餐1名称与价格'),{message:'套餐1名称与价格',step:3,fields:['packages.1.name']});
 assert.deepEqual(issues.find(i=>i.message==='套餐7名称与价格').fields,['packages.7.price_cents']);
 s.packages[0].name='120分钟';s.packages[1].price_cents=300;
 assert.ok(!validateScheme(s).some(i=>i.fields.includes('packages.1.name')||i.fields.includes('packages.7.price_cents')));
});

test('validation locates rate, power, and card relationship failures across steps',()=>{
 const s=durationScheme();s.card.max_minutes=60;
 s.amount={algorithm:'server_max_power',periods:[{end_minute:1440,tiers:[{max_watts:200,electric_cents:NaN,service_cents:0},{max_watts:100,electric_cents:0,service_cents:0}]}]};
 const issues=validateScheme(s);
 assert.deepEqual(issues.find(i=>i.message==='时段1档位1电费').fields,['amount.periods.0.tiers.0.electric_cents']);
 assert.deepEqual(issues.find(i=>i.message==='时段1档位2功率上限').fields,['amount.periods.0.tiers.1.max_watts']);
 assert.deepEqual(issues.find(i=>i.message==='刷卡套餐超过累计上限').fields,['card.package_id','card.max_minutes','packages.1.minutes']);
 assert.ok(!issues.some(i=>i.fields.includes('amount.periods.0.tiers.0.service_cents')));
});

test('algorithm switches restore independent periods and rates without unit conversion',()=>{
 const original={algorithm:'server_max_power',periods:[{end_minute:720,tiers:[{max_watts:200,electric_cents:80,service_cents:40}]},{end_minute:1440,tiers:[{max_watts:400,electric_cents:160,service_cents:80}]}]};
 let state=switchAmountAlgorithm(original,{},'server_realtime_power');
 assert.deepEqual(state.amount.periods,[{end_minute:1440,tiers:[{max_watts:200,electric_cents:0,service_cents:0}]}]);
 state.amount.periods[0].tiers[0].electric_cents=123;
 state=switchAmountAlgorithm(state.amount,state.drafts,'server_energy');
 state.amount.periods=[{end_minute:1440,electric_cents:85,service_cents:15}];
 state=switchAmountAlgorithm(state.amount,state.drafts,'server_max_power');
 assert.deepEqual(state.amount,original);
 state.amount.periods[0].tiers[0].max_watts=300;
 assert.equal(state.drafts.server_max_power.periods[0].tiers[0].max_watts,200);
 state=switchAmountAlgorithm(state.amount,state.drafts,'server_realtime_power');
 assert.equal(state.amount.periods[0].tiers[0].electric_cents,123);
 state=switchAmountAlgorithm(state.amount,state.drafts,'server_energy');
 assert.deepEqual(state.amount.periods,[{end_minute:1440,electric_cents:85,service_cents:15}]);
});

test('inactive invalid drafts do not enter validation or the submitted scheme',()=>{
 const power={algorithm:'server_max_power',periods:[{end_minute:1440,tiers:[{max_watts:200,electric_cents:NaN,service_cents:0}]}]};
 let state=switchAmountAlgorithm(power,{},'server_energy');
 state.amount.periods[0].electric_cents=80;
 const s={...durationScheme(),amount:state.amount,packages:[{id:1,name:'预算',mode:'amount',price_cents:300}],card:{package_id:0,max_minutes:600}};
 assert.deepEqual(missingInputs(s),[]);
 assert.deepEqual(JSON.parse(JSON.stringify(s)).amount,{algorithm:'server_energy',periods:[{end_minute:1440,electric_cents:80,service_cents:0}]});
 assert.ok(Number.isNaN(state.drafts.server_max_power.periods[0].tiers[0].electric_cents));
 state=switchAmountAlgorithm(state.amount,state.drafts,'server_max_power');
 assert.ok(missingInputs({...s,amount:state.amount}).includes('时段1档位1电费'));
});
