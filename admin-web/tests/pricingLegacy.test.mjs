import test from 'node:test';
import assert from 'node:assert/strict';
import {
  convertLegacyPowerForm, defaultSpecForm, effectiveTierElectricCents, formToSpec,
  isLegacyPowerPricing, specToForm, validateSpecForm,
} from '../src/pages/pricing/model.ts';

const legacySpec = (basis = 'per_hour_at_ceiling') => ({
  mode: 'server_realtime_power', ...(basis ? { tier_price_basis: basis } : {}),
  electric: { basis: 'realtime_power', periods: [
    { end_minute: 480, tiers: [{ max_watts: 0, electric_cents: 50, service_cents: 12 }, { max_watts: 200, electric_cents: 83, service_cents: 30 }, { max_watts: 9990, electric_cents: 120, service_cents: 40 }] },
    { end_minute: 1440, tiers: [{ max_watts: 200, electric_cents: 100, service_cents: 10 }, { max_watts: 9990, electric_cents: 100, service_cents: 20 }] },
  ] },
  service: { basis: 'minute_power' }, loss_rate_bp: 250, multiplier: { temp_bp: 11000, card_bp: 8500 },
  free_minutes: 3, min_electric_cents: 57, spend_cap_cents: 2350, stop_grace_seconds: 42,
});

test('new real-time power templates have fixed per-kWh prices and no conversion choice', () => {
  const form = defaultSpecForm('server_realtime_power');
  assert.equal(form.tier_price_basis, 'per_kwh');
  assert.equal(form.periods[0].tiers[0].max_watts, 9990);
  assert.equal(formToSpec(form).tier_price_basis, 'per_kwh');
  assert.equal(isLegacyPowerPricing(formToSpec(form)), false);
});

test('legacy explicit and omitted bases cannot silently be saved as hourly or relabeled as per-kWh', () => {
  for (const basis of ['per_hour_at_ceiling', '']) {
    const spec = legacySpec(basis);
    assert.equal(isLegacyPowerPricing(spec), true);
    const form = specToForm(spec);
    assert.equal(form.tier_price_basis, 'per_hour_at_ceiling');
    assert.ok(validateSpecForm(form).some(error => error.includes('请先将旧版电价转换')));
    assert.throws(() => formToSpec(form), RangeError);
    assert.deepEqual(form.periods[0].tiers.map(t => t.electric_yuan), [0.5, 0.83, 1.2]);
  }
});

test('conversion preserves legacy integer-cent truncation, all boundaries, service prices and policies', () => {
  for (const basis of ['per_hour_at_ceiling', '']) {
    const spec = legacySpec(basis);
    const form = specToForm(spec);
    const before = structuredClone(form);
    const converted = convertLegacyPowerForm(form);
    assert.deepEqual(form, before);
    assert.deepEqual(validateSpecForm(converted), []);
    const saved = formToSpec(converted);
    const expected = structuredClone(spec);
    expected.tier_price_basis = 'per_kwh';
    for (const period of expected.electric.periods) {
      for (const tier of period.tiers) tier.electric_cents = Math.trunc(tier.electric_cents * tier.max_watts / 1000);
    }
    assert.deepEqual(saved, expected);
    assert.deepEqual(saved.electric.periods[0].tiers.map(t => t.electric_cents), [0, 16, 1198]);
    assert.deepEqual(saved.electric.periods[0].tiers.map(t => t.electric_cents), spec.electric.periods[0].tiers.map(t => effectiveTierElectricCents(spec, t)));
    converted.periods[0].tiers[1].electric_yuan = 9;
    assert.equal(form.periods[0].tiers[1].electric_yuan, 0.83);
  }
});

test('conversion blocks impossible rates and never partially converts the source', () => {
  const form = specToForm(legacySpec());
  for (const [patch, message] of [
    [{ max_watts: 9990, electric_yuan: 10000 }, /超过 10000/],
    [{ max_watts: 1.5 }, /无效/], [{ max_watts: -1 }, /无效/],
    [{ electric_yuan: NaN }, /无效/], [{ electric_yuan: null }, /无效/],
  ]) {
    const draft = structuredClone(form);
    Object.assign(draft.periods[1].tiers[1], patch);
    const before = structuredClone(draft);
    assert.throws(() => convertLegacyPowerForm(draft), message);
    assert.deepEqual(draft, before);
  }
  assert.throws(() => convertLegacyPowerForm({ ...form, tier_price_basis: 'unknown' }), /无法识别/);
});

test('conversion does not reinterpret current energy or hourly pricing', () => {
  for (const mode of ['server_realtime_power', 'server_energy', 'server_max_power']) {
    const form = defaultSpecForm(mode);
    assert.equal(convertLegacyPowerForm(form), form);
  }
});
