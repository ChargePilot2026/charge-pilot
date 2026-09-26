import test from 'node:test';
import assert from 'node:assert/strict';
import { apiErrorMessage } from '../src/api/errorMessage.ts';
test('permission and business conflicts explain the server rejection',()=>{
  assert.equal(apiErrorMessage(403,{message:'缺少 finance.refund.read 权限'}),'缺少 finance.refund.read 权限');
  assert.equal(apiErrorMessage(409,{message:'退款尚未完成双签审核'}),'退款尚未完成双签审核');
});
test('transport and malformed errors remain readable without exposing server internals',()=>{
  assert.match(apiErrorMessage(undefined,undefined,'ECONNABORTED'),/结果请刷新核实/);
  assert.match(apiErrorMessage(undefined,undefined,'ERR_NETWORK'),/检查网络/);
  assert.equal(apiErrorMessage(500,{message:'SQL password internal error'}),'服务暂时不可用，请稍后重试');
  assert.match(apiErrorMessage(403,'<html>forbidden</html>'),/没有操作权限/);
  assert.match(apiErrorMessage(409,{message:42}),/状态已变化/);
  assert.match(apiErrorMessage(401,{message:'irrelevant'}),/重新登录/);
});
