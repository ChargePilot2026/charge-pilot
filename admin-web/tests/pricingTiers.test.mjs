import test from 'node:test';
import assert from 'node:assert/strict';
import { canInsertTier, defaultSpecForm, formToSpec, insertTier, validateSpecForm } from '../src/pages/pricing/model.ts';

const tier = (max_watts, electric_yuan = 0.83, service_yuan = 0.12) => ({ max_watts, electric_yuan, service_yuan });

test('appending after a 200 W tier preserves its ceiling and prices', () => {
  const tiers = [tier(200)];
  const before = structuredClone(tiers);
  const result = insertTier(tiers, 1);
  assert.deepEqual(tiers, before);
  assert.deepEqual(result, [tier(200), tier(9990, 0, 0)]);
  result[0].max_watts = 300;
  result[0].electric_yuan = 9;
  assert.deepEqual(tiers, before);
});

test('inserting in the middle preserves both neighbors and unrelated tiers', () => {
  const tiers = [tier(200), tier(400, 1.2, 0.3), tier(9990, 2, 0.4)];
  const before = structuredClone(tiers);
  assert.deepEqual(insertTier(tiers, 1), [tier(200), tier(300, 0, 0), tiers[1], tiers[2]]);
  assert.deepEqual(tiers, before);
  assert.deepEqual(insertTier([tier(200), tier(202)], 1), [tier(200), tier(201, 0, 0), tier(202)]);
});

test('full ranges, adjacent ceilings, tier count and invalid indices cannot create invalid tiers', () => {
  for (const [tiers, at] of [
    [[tier(9990)], 1], [[tier(200), tier(201)], 1],
    [Array.from({ length: 8 }, (_, i) => tier(i * 100)), 1],
    [[tier(200)], -1], [[tier(200)], 0.5], [[tier(200)], 2],
    [[tier(null)], 1], [[tier(NaN)], 1], [[tier(-1)], 1],
    [[tier(9991)], 0], [[tier(400), tier(200)], 2],
  ]) {
    const before = structuredClone(tiers);
    assert.equal(canInsertTier(tiers, at), false);
    assert.throws(() => insertTier(tiers, at), RangeError);
    assert.deepEqual(tiers, before);
  }
  assert.deepEqual(insertTier([], 0), [tier(9990, 0, 0)]);
  assert.deepEqual(insertTier([tier(9989)], 1), [tier(9989), tier(9990, 0, 0)]);
});

test('200 W first ceiling validates and round-trips in both power billing modes', () => {
  for (const mode of ['server_realtime_power', 'server_max_power']) {
    const form = defaultSpecForm(mode);
    form.service_basis = 'minute_power';
    form.periods[0].tiers = insertTier([tier(200)], 1);
    assert.deepEqual(validateSpecForm(form), []);
    assert.deepEqual(formToSpec(form).electric.periods[0].tiers, [
      { max_watts: 200, electric_cents: 83, service_cents: 12 },
      { max_watts: 9990, electric_cents: 0, service_cents: 0 },
    ]);
    form.periods[0].tiers[1].max_watts = 200;
    assert.ok(validateSpecForm(form).some(error => error.includes('必须大于上一档')));
  }
});
