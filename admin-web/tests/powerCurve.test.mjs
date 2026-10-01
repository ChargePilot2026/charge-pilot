import test from 'node:test';
import assert from 'node:assert/strict';
import { nearestPowerPoint, powerAxisStep, powerPoints, powerSegments, readProcessPages } from '../src/pages/orders/powerCurve.ts';

const sample = (id, ts = '2026-10-01T00:00:00Z', power_w = 0) => ({
  id, ts, power_w, charged_kwh: 0, remaining_kwh: 0, charged_seconds: 0, remaining_seconds: 0, signal_strength: 0,
});

test('reads every cursor page before chronological sorting, including long orders and out-of-order timestamps', async () => {
  const stored = Array.from({ length: 2501 }, (_, index) => sample(index + 1, new Date(Date.UTC(2026, 9, 1) + (2501 - index) * 1000).toISOString(), index));
  const cursors = [];
  const loaded = await readProcessPages(async afterID => {
    cursors.push(afterID);
    const items = stored.slice(afterID ?? 0, (afterID ?? 0) + 1000);
    return { items, next_after_id: items.length === 1000 ? items[items.length - 1].id : null };
  }, new AbortController().signal);
  assert.deepEqual(cursors, [undefined, 1000, 2000]);
  assert.equal(loaded.length, 2501);
  const points = powerPoints(loaded);
  assert.equal(points[0].sample.id, 2501);
  assert.equal(points[points.length - 1].sample.id, 1);
  assert.equal(loaded[0].id, 1);
});

test('incremental reads begin after the last stored id and preserve late timestamps when merged', async () => {
  const loaded = await readProcessPages(async afterID => {
    assert.equal(afterID, 2);
    return { items: [sample(3, '2026-10-01T00:00:10Z')], next_after_id: null };
  }, new AbortController().signal, 2);
  const merged = powerPoints([sample(1), sample(2, '2026-10-01T00:00:20Z'), ...loaded]);
  assert.deepEqual(merged.map(point => point.sample.id), [1, 3, 2]);
});

test('invalid pagination cannot loop indefinitely and cancellation stops later pages', async () => {
  let calls = 0;
  await assert.rejects(readProcessPages(async afterID => {
    calls++;
    return { items: [sample((afterID ?? 0) + 1)], next_after_id: 1 };
  }, new AbortController().signal), /游标未前进/);
  assert.equal(calls, 2);
  await assert.rejects(readProcessPages(async () => ({ items: [sample(1)] }), new AbortController().signal), /响应不完整/);
  for (const items of [[sample(2), sample(1)], [sample(1), sample(1)]]) {
    await assert.rejects(readProcessPages(async () => ({ items, next_after_id: null }), new AbortController().signal), /游标无效/);
  }
  const controller = new AbortController();
  let cancelledCalls = 0;
  await assert.rejects(readProcessPages(async () => {
    cancelledCalls++; controller.abort();
    return { items: [sample(1)], next_after_id: 1 };
  }, controller.signal), { name: 'AbortError' });
  assert.equal(cancelledCalls, 1);
});

test('zero power and isolated points remain real samples, while long gaps are not connected', () => {
  const points = powerPoints([sample(4, '2026-10-01T00:02:01Z', 0), sample(1),
    sample(2, '2026-10-01T00:00:30Z', 75), sample(3, '2026-10-01T00:01:00Z', 0)]);
  const segments = powerSegments(points);
  assert.deepEqual(segments.map(segment => segment.map(point => point.sample.id)), [[1, 2, 3], [4]]);
  assert.equal(segments[1][0].sample.power_w, 0);
  assert.equal(powerSegments(powerPoints([sample(1)]))[0].length, 1);
  assert.equal(powerSegments(powerPoints([sample(1), sample(2, '2026-10-01T00:01:00Z')])).length, 1);
  assert.equal(powerAxisStep(0), 1);
  assert.ok(powerAxisStep(301) * 4 >= 301);
  assert.ok(powerAxisStep(0.1) * 4 >= 0.1);
});

test('hover lookup reaches every sample and chooses a real point at the ends and in gaps', () => {
  const points = powerPoints([sample(1), sample(2, '2026-10-01T00:00:30Z'), sample(3, '2026-10-01T00:03:00Z')]);
  assert.equal(nearestPowerPoint([], 0), -1);
  assert.equal(nearestPowerPoint(points, points[0].at - 5000), 0);
  assert.equal(nearestPowerPoint(points, points[2].at + 5000), 2);
  assert.equal(nearestPowerPoint(points, points[1].at + 1000), 1);
  assert.equal(nearestPowerPoint(points, points[2].at - 1000), 2);
});

test('invalid readings fail explicitly instead of plotting fabricated watts', () => {
  for (const point of [sample(1, 'invalid'), sample(1, undefined, -1), sample(1, undefined, NaN)]) {
    assert.throws(() => powerPoints([point]), /无效时间或功率/);
  }
});
