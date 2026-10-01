import test from 'node:test';
import assert from 'node:assert/strict';
import { isChargeUserID } from '../src/utils/chargeUserID.ts';

test('charging user IDs keep legacy and Snowflake decimal values without numeric coercion', () => {
  for (const value of ['1', '9007199254740993', '9007199254740994', '9223372036854775807', '18446744073709551615']) {
    assert.equal(isChargeUserID(value), true);
  }
});

test('charging user IDs reject already numeric responses, malformed input and uint64 overflow', () => {
  for (const value of [1, 9007199254740993, null, undefined, '', '0', '01', '-1', '+1', '1.0', '1e19', ' 1 ', '１８', '18446744073709551616', '100000000000000000000']) {
    assert.equal(isChargeUserID(value), false, `value ${String(value)}`);
  }
});
