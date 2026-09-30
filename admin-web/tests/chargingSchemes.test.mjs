import test from 'node:test';
import assert from 'node:assert/strict';
import {blankScheme,priceEnergy,missingInputs} from '../src/pages/schemes/model.ts';

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
