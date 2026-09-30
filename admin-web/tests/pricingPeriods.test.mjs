import test from 'node:test';
import assert from 'node:assert/strict';
import {
  clockToMinute, defaultSpecForm, formToSpec, minuteToClock, removePeriod,
  specToForm, splitPeriod, suggestedSplitMinute, validatePeriodEndMinute, validateSpecForm,
} from '../src/pages/pricing/model.ts';

const rate = (end_minute, electric_yuan = 0.83) => ({
  end_minute,
  electric_yuan,
  tiers: [
    { max_watts: 0, electric_yuan: 0.27, service_yuan: 0.09 },
    { max_watts: 9990, electric_yuan: 0.91, service_yuan: 0.31 },
  ],
});

test('parses only complete HH:mm times, including the distinct end of day', () => {
  for (const [clock, minute] of [['00:00', 0], ['00:01', 1], ['08:00', 480], ['18:37', 1117], ['23:59', 1439], ['24:00', 1440]]) {
    assert.equal(clockToMinute(clock), minute);
    assert.equal(minuteToClock(minute), clock);
  }
  for (const invalid of ['', '8:00', '08:0', '08', ' 08:00 ', '24:01', '25:00', '12:60', '-1:00', '12:1.5', '08:00:00', 'NaN:00', null, undefined, 480]) {
    assert.equal(clockToMinute(invalid), undefined, String(invalid));
  }
});

test('validates boundaries against both neighbors and keeps the last period at 24:00', () => {
  const periods = [rate(480), rate(1080), rate(1440)];
  assert.equal(validatePeriodEndMinute(periods, 0, 540), undefined);
  assert.equal(validatePeriodEndMinute(periods, 1, 1117), undefined);
  assert.equal(validatePeriodEndMinute(periods, 2, 1440), undefined);
  assert.equal(validatePeriodEndMinute([rate(1440)], 0, 1440), undefined);
  for (const invalid of [480, 1440, 0, -1, 1500, 600.5, undefined, NaN]) {
    assert.ok(validatePeriodEndMinute(periods, 1, invalid));
  }
  assert.match(validatePeriodEndMinute(periods, 1, 480), /08:00.*24:00/);
  assert.match(validatePeriodEndMinute(periods, 2, 1200), /固定为 24:00/);
  assert.ok(validatePeriodEndMinute([rate(1440), rate(1440)], 1, 1440));
  for (const index of [-1, 0.5, 3]) assert.ok(validatePeriodEndMinute(periods, index, 600));
});

test('suggests a midpoint aligned to 15 minutes when possible and handles narrow periods', () => {
  assert.equal(suggestedSplitMinute([rate(1440)], 0), 720);
  assert.equal(suggestedSplitMinute([rate(483), rate(1440)], 0), 240);
  assert.equal(suggestedSplitMinute([rate(7), rate(10), rate(1440)], 1), 8);
  assert.equal(suggestedSplitMinute([rate(1), rate(3), rate(1440)], 1), 2);
  assert.equal(suggestedSplitMinute([rate(1438), rate(1440)], 1), 1439);
  assert.equal(suggestedSplitMinute([rate(1), rate(1440)], 0), undefined);
  assert.equal(suggestedSplitMinute([rate(10), rate(10), rate(1440)], 1), undefined);
  assert.equal(suggestedSplitMinute([], 0), undefined);
  assert.equal(suggestedSplitMinute([rate(1440)], 0.5), undefined);
});

test('split preserves all rates in two independent copies and leaves unrelated periods unchanged', () => {
  const periods = [rate(480, 0.42), rate(1080), rate(1440, 1.04)];
  const original = structuredClone(periods);
  const result = splitPeriod(periods, 1, 701);
  assert.deepEqual(result, [periods[0], { ...periods[1], end_minute: 701 }, periods[1], periods[2]]);
  assert.deepEqual(periods, original);
  assert.notEqual(result[1].tiers, result[2].tiers);
  assert.notEqual(result[1].tiers[0], result[2].tiers[0]);
  result[1].electric_yuan = 7;
  result[1].tiers[0].electric_yuan = 8;
  result[1].tiers[1].service_yuan = 9;
  result[0].tiers[0].max_watts = 12;
  assert.equal(result[2].electric_yuan, original[1].electric_yuan);
  assert.deepEqual(result[2].tiers, original[1].tiers);
  assert.deepEqual(periods, original);
});

test('splitting retains the serialized prices for energy and power modes', () => {
  const specs = [
    { mode: 'server_energy', electric: { basis: 'energy', periods: [{ end_minute: 1440, electric_cents: 83 }] } },
    ...['server_realtime_power', 'server_max_power'].map(mode => ({
      mode,
      electric: {
        basis: mode === 'server_realtime_power' ? 'realtime_power' : 'max_power',
        periods: [{ end_minute: 1440, tiers: [{ max_watts: 0, electric_cents: 27, service_cents: 9 }, { max_watts: 9990, electric_cents: 91, service_cents: 31 }] }],
      },
      service: { basis: 'minute_power' },
      ...(mode === 'server_realtime_power' ? { tier_price_basis: 'per_kwh' } : {}),
    })),
  ];
  for (const spec of specs) {
    const form = specToForm(spec);
    form.periods = splitPeriod(form.periods, 0, clockToMinute('08:00'));
    assert.deepEqual(validateSpecForm(form), []);
    const saved = formToSpec(form);
    assert.deepEqual(saved.electric.periods, [
      { ...spec.electric.periods[0], end_minute: 480 },
      spec.electric.periods[0],
    ]);
    assert.equal(saved.mode, spec.mode);
    assert.deepEqual(saved.service, spec.service);
    assert.equal(saved.tier_price_basis, spec.tier_price_basis);
    assert.deepEqual(specToForm(saved).periods, form.periods);
  }
});

test('split rejects zero-length halves, invalid indices or times, and enforces the 48-period limit', () => {
  const periods = [rate(480), rate(1080), rate(1440)];
  for (const minute of [480, 1080, 479, 1081, 600.5, NaN, Infinity]) {
    assert.throws(() => splitPeriod(periods, 1, minute), RangeError);
  }
  for (const index of [-1, 0.5, 3]) assert.throws(() => splitPeriod(periods, index, 600), RangeError);
  assert.throws(() => splitPeriod([rate(1), rate(1440)], 0, 1), /不足两分钟/);
  assert.deepEqual(splitPeriod([rate(2), rate(1440)], 0, 1).map(p => p.end_minute), [1, 2, 1440]);
  const full = Array.from({ length: 48 }, (_, i) => rate((i + 1) * 30));
  assert.throws(() => splitPeriod(full, 0, 15), /最多 48/);
  const form = defaultSpecForm('server_energy');
  form.periods = Array.from({ length: 47 }, (_, i) => rate(i === 46 ? 1440 : (i + 1) * 30));
  form.periods = splitPeriod(form.periods, 0, 15);
  assert.equal(form.periods.length, 48);
  assert.deepEqual(validateSpecForm(form), []);
});

test('delete assigns the removed range to the next period, or extends the previous final period', () => {
  const periods = [rate(480, 0.42), rate(1080, 0.83), rate(1440, 1.04)];
  const original = structuredClone(periods);
  assert.deepEqual(removePeriod(periods, 0).map(p => [p.end_minute, p.electric_yuan]), [[1080, 0.83], [1440, 1.04]]);
  assert.deepEqual(removePeriod(periods, 1).map(p => [p.end_minute, p.electric_yuan]), [[480, 0.42], [1440, 1.04]]);
  assert.deepEqual(removePeriod(periods, 2).map(p => [p.end_minute, p.electric_yuan]), [[480, 0.42], [1440, 0.83]]);
  assert.deepEqual(periods, original);
  const remaining = removePeriod(periods, 1);
  remaining[1].tiers[0].service_yuan = 99;
  assert.deepEqual(periods, original);
  assert.deepEqual(removePeriod([periods[2]], 0), [periods[2]]);
  for (const index of [-1, 0.5, 3]) assert.deepEqual(removePeriod(periods, index), periods);
});
