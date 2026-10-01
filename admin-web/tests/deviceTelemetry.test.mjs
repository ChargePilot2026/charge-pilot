import test from 'node:test';
import assert from 'node:assert/strict';
import { portStatusText, signalStrengthText } from '../src/utils/deviceTelemetry.ts';

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
