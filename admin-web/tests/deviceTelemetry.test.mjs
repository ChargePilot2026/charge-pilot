import test from 'node:test';
import assert from 'node:assert/strict';
import { lastOnlineText, portStatusText, signalStrengthText } from '../src/utils/deviceTelemetry.ts';

test('device signal keeps valid zero and the raw unknown CSQ without replacing missing data', () => {
  assert.equal(signalStrengthText(0), 'CSQ 0');
  assert.equal(signalStrengthText(31), 'CSQ 31');
  assert.equal(signalStrengthText(99), 'CSQ 99（未知）');
  assert.equal(signalStrengthText(null), '—');
  assert.equal(signalStrengthText(undefined), '—');
});

test('port status treats only an actual zero report as idle and retains unknown codes', () => {
  assert.equal(portStatusText(0), '空闲');
  assert.equal(portStatusText(1), '充电');
  assert.equal(portStatusText(3), '输出故障');
  assert.equal(portStatusText(4), '粘连');
  assert.equal(portStatusText(2), '未知（2）');
  assert.equal(portStatusText(255), '未知（255）');
  assert.equal(portStatusText(null), '—');
  assert.equal(portStatusText(undefined), '—');
});

test('last online labels switch precisely at five and sixty minutes', () => {
  const now = Date.parse('2026-10-01T12:00:00.000Z');
  const cases = [
    [0, '刚刚'],
    [299_999, '刚刚'],
    [300_000, '1小时内'],
    [3_599_999, '1小时内'],
    [3_600_000, '离线'],
    [86_400_000, '离线'],
  ];
  for (const [elapsed, label] of cases) {
    assert.equal(lastOnlineText(new Date(now - elapsed).toISOString(), now), label, `elapsed ${elapsed} ms`);
  }
});

test('last online compares actual instants across timezone offsets and tolerates a small future clock drift', () => {
  const now = Date.parse('2026-10-01T12:00:00.000Z');
  for (const value of ['2026-10-01T11:55:00.000Z', '2026-10-01T19:55:00.000+08:00', '2026-10-01T07:55:00.000-04:00']) {
    assert.equal(lastOnlineText(value, now), '1小时内');
  }
  assert.equal(lastOnlineText('2026-10-01T12:00:01.000Z', now), '刚刚');
  assert.equal(lastOnlineText(new Date().toISOString()), '刚刚');
});

test('last online distinguishes absent reports from invalid timestamps', () => {
  const now = Date.parse('2026-10-01T12:00:00.000Z');
  for (const value of [null, undefined, '', '   ']) assert.equal(lastOnlineText(value, now), '从未上线');
  for (const value of ['invalid', '2026-13-01T12:00:00Z', '2026-10-01T25:00:00Z']) assert.equal(lastOnlineText(value, now), '—');
  assert.equal(lastOnlineText('2026-10-01T12:00:00Z', Number.NaN), '—');
});
