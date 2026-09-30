import test from 'node:test';
import assert from 'node:assert/strict';
import { defaultSpecForm, formToSpec, specToForm, tierRateUnits, validateSpecForm } from '../src/pages/pricing/model.ts';

test('switching service basis submits only the selected rate and retains tier drafts', () => {
  const form = defaultSpecForm('server_realtime_power');
  form.periods[0].tiers = [
    { max_watts: 200, electric_yuan: 0.83, service_yuan: 0.12 },
    { max_watts: 9990, electric_yuan: 1.2, service_yuan: 0.3 },
  ];
  form.service_kwh_yuan = 0.19;
  form.service_minute_yuan = 0.04;
  form.service_session_yuan = 1.5;
  const original = structuredClone(form.periods);
  for (const [basis, service] of [
    ['minute_power', { basis: 'minute_power' }],
    ['energy', { basis: 'energy', cents_per_kwh: 19 }],
    ['minute', { basis: 'minute', cents_per_minute: 4 }],
    ['session', { basis: 'session', cents_per_session: 150 }],
    ['none', undefined],
    ['minute_power', { basis: 'minute_power' }],
  ]) {
    form.service_basis = basis;
    assert.deepEqual(validateSpecForm(form), []);
    const spec = formToSpec(form);
    assert.deepEqual(spec.service, service);
    assert.deepEqual(spec.electric.periods[0].tiers, [
      { max_watts: 200, electric_cents: 83, ...(basis === 'minute_power' ? { service_cents: 12 } : {}) },
      { max_watts: 9990, electric_cents: 120, ...(basis === 'minute_power' ? { service_cents: 30 } : {}) },
    ]);
    assert.deepEqual(form.periods, original);
  }
});

test('power rate units follow billing mode without converting stored money', () => {
  for (const [mode, basis, units] of [
    ['server_realtime_power', 'per_kwh', { electric: '元/度', service: '元/度' }],
    ['server_max_power', undefined, { electric: '元/小时', service: '元/小时' }],
  ]) {
    const spec = {
      mode, ...(basis ? { tier_price_basis: basis } : {}), service: { basis: 'minute_power' },
      electric: { basis: mode === 'server_max_power' ? 'max_power' : 'realtime_power', periods: [
        { end_minute: 1440, tiers: [{ max_watts: 200, electric_cents: 83, service_cents: 12 }] },
      ] },
    };
    assert.deepEqual(tierRateUnits(mode), units);
    const saved = formToSpec(specToForm(spec));
    assert.deepEqual(saved.electric, spec.electric);
    assert.deepEqual(saved.service, spec.service);
    assert.equal(saved.tier_price_basis, basis);
  }
});

test('energy and device billing do not submit hidden power tier service rates', () => {
  const energy = defaultSpecForm('server_energy');
  energy.service_basis = 'minute_power';
  assert.ok(validateSpecForm(energy).some(error => error.includes('电量计费没有档位')));
  energy.service_basis = 'energy';
  energy.service_kwh_yuan = 0.35;
  assert.deepEqual(formToSpec(energy).service, { basis: 'energy', cents_per_kwh: 35 });
  assert.equal(formToSpec(energy).electric.periods[0].tiers, undefined);
  for (const mode of ['device_duration', 'device_energy', 'device_power']) {
    const form = { ...energy, mode, service_basis: 'minute_power' };
    const spec = formToSpec(form);
    assert.equal(spec.service, undefined);
    assert.equal(spec.electric, undefined);
  }
});
