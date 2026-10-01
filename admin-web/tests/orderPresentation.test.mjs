import test from 'node:test';
import assert from 'node:assert/strict';
import { businessStatusInfo, paymentStatusInfo, chargingDuration, formatDuration, refundedAmount } from '../src/pages/orders/presentation.ts';

const order = (values = {}) => ({
  business_status: 'completed', payment_status: 'paid', status: 'completed', refund_status: 'none',
  started_at: '2026-10-01T08:00:00+08:00', ended_at: '2026-10-01T01:00:00Z',
  duration_seconds: null, ...values,
});

test('business and payment labels stay independent of old lifecycle and refund progress', () => {
  const row = order({ business_status: 'charging', payment_status: 'partial_refunded', status: 'refunding', refund_status: 'processing' });
  assert.equal(businessStatusInfo(row).label, '充电中');
  assert.equal(paymentStatusInfo(row).label, '已部分退款');
  assert.match(paymentStatusInfo(row).hint, /退款处理中/);
  assert.equal(paymentStatusInfo(order({ status: 'refunding', refund_status: 'processing' })).label, '已支付');
});

test('terminal cancellation and failure explain why the business label is completed', () => {
  const cancelled = businessStatusInfo(order({ status: 'cancelled', failure_reason: '用户取消' }));
  assert.equal(cancelled.label, '已完成');
  assert.match(cancelled.hint, /取消/);
  assert.match(cancelled.hint, /用户取消/);
  const failed = businessStatusInfo(order({ status: 'failed', failure_reason: '设备拒绝启动' }));
  assert.equal(failed.label, '已完成');
  assert.match(failed.hint, /设备拒绝启动/);
  const refunding = businessStatusInfo(order({ status: 'refunding', failure_reason: '启动失败，退回预付金额' }));
  assert.equal(refunding.label, '已完成');
  assert.match(refunding.hint, /启动失败/);
});

test('unconfirmed, failed and closed payment orders remain pending with an explanation', () => {
  for (const status of ['paying', 'failed', 'closed']) {
    const info = paymentStatusInfo({ payment_status: 'pending', payment_order_status: status });
    assert.equal(info.label, '待支付');
    assert.match(info.hint, /尚未确认到账/);
  }
  assert.equal(paymentStatusInfo({ payment_status: 'paid', payment_order_status: 'paid' }).hint, undefined);
});

test('charging duration uses the latest device sample and retains stale provenance', () => {
  const sampled = chargingDuration(order({ business_status: 'charging', duration_seconds: 10,
    live: { at: '2026-10-01T01:01:01Z', stale: true, seconds: 3661 } }));
  assert.equal(sampled.text, '1小时1分1秒（旧）');
  assert.equal(sampled.source, 'live');
  assert.equal(sampled.sampledAt, '2026-10-01T01:01:01Z');
  assert.match(sampled.hint, /已过期/);
  assert.equal(chargingDuration(order({ business_status: 'charging', duration_seconds: 10 })).text, '待上报');
  assert.equal(chargingDuration(order({ business_status: 'pending_start', live: { seconds: 999, stale: false, at: '' } })).text, '—');
});

test('final measured duration wins and missing measurement uses only valid actual start and end', () => {
  assert.deepEqual(chargingDuration(order({ duration_seconds: 0 })), { text: '0秒', source: 'measured' });
  assert.equal(chargingDuration(order({ duration_seconds: 61 })).text, '1分1秒');
  const estimated = chargingDuration(order());
  assert.equal(estimated.text, '≈1小时0分0秒');
  assert.equal(estimated.source, 'estimated');
  assert.match(estimated.hint, /估算/);
  for (const values of [
    { started_at: null }, { ended_at: null }, { started_at: 'invalid' },
    { ended_at: '2026-09-30T23:59:59Z' }, { started_at: null, duration_seconds: NaN },
  ]) assert.equal(chargingDuration(order(values)).text, '—');
});

test('duration and money preserve real zero while invalid or unknown readings are not fabricated', () => {
  assert.equal(formatDuration(0), '0秒');
  assert.equal(formatDuration(3600), '1小时0分0秒');
  assert.equal(formatDuration(86401), '24小时0分1秒');
  assert.equal(formatDuration(61.9), '1分1秒');
  for (const invalid of [-1, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1]) assert.equal(formatDuration(invalid), '—');
  assert.equal(refundedAmount(0), '¥0.00');
  assert.equal(refundedAmount(123), '¥1.23');
  assert.equal(refundedAmount(null), '—');
  assert.equal(refundedAmount(undefined), '—');
});
