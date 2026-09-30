const { controllerPath, controllerSource, sessionSource } = require('./source.cjs');
const test=require('node:test');
const assert=require('node:assert/strict');
const {formatOrder}=require('../src/utils/order');
test('an unsettled order keeps unknown fees distinct from a confirmed zero charge',()=>{
 const pending=formatOrder({status:'completed',total_cents:null,electric_cents:null,service_cents:null});
 assert.equal(pending.totalText,'待结算');assert.equal(pending.electricText,'待结算');
 const free=formatOrder({status:'completed',total_cents:0,electric_cents:0,service_cents:0});
 assert.equal(free.totalText,'¥0.00');
});
