import { test } from 'node:test';
import assert from 'node:assert/strict';
import { periodStart, periodKey, nextPeriod, periodBuckets, earliestKey, periodIndex, shiftPeriod, validPeriodCount } from '../../../src/chart-period';
import { calendarPresetPeriod, chartPeriodCount, presetPeriod, selectChartRange, customDateBounds, timeBounds, usageTime, withTimeRange } from '../../../src/usage-chart';
import { rangeInputValue, parseRangeInput, rangeHasTime } from '../../../src/chart-range';
import { loadUsageHistory, sharedUsageHistory } from '../../../src/usage-history';

test('range presets reset the time bucket while custom dates preserve it', () => {
  let view = { period: 'month', hours: 12, days: 10 }, bounds, changes = 0;
  const onChange = (from, to) => { bounds = { from, to }; changes++; };
  const onPreset = range => { view = { ...view, ...presetPeriod(range) }; };
  for (const [range, period, count] of [['1y', 'week', 1], ['1d', 'hour', 1], ['7d', 'hours', 4], ['30d', 'day', 1], ['90d', 'day', 1], ['6m', 'week', 1]]) {
    selectChartRange(range, onChange, onPreset);
    assert.equal(view.period, period, range);
    assert.equal(chartPeriodCount(view), count, range);
    assert.equal(bounds.from.getHours(), 0);
    assert.equal(bounds.to, undefined);
  }
  view = { period: 'hours', hours: 8, days: 5 };
  const before = bounds, previousChanges = changes;
  selectChartRange('custom', onChange, onPreset);
  assert.deepEqual(view, { period: 'hours', hours: 8, days: 5 });
  assert.equal(bounds, before);
  assert.equal(changes, previousChanges);
  selectChartRange('7d', onChange, onPreset);
  assert.equal(view.hours, 4, 'a preset overrides a previously chosen custom bucket');
  view.hours = 12;
  selectChartRange('7d', onChange, onPreset);
  assert.equal(view.hours, 4, 'reselecting the same preset resets its bucket');
  selectChartRange('all', onChange, onPreset);
  assert.equal(view.period, 'week');
  assert.deepEqual(bounds, { from: undefined, to: undefined });
});

test('range choices persist relative filters and restore the preset after time advances', () => {
  const provider = { field: 'provider', op: '=', value: 'codex' };
  const now = new Date(2026, 9, 22, 12, 34).getTime();
  let where = withTimeRange([provider], usageTime, new Date(2025, 1, 1), new Date(2025, 1, 3));
  for (const [range, days] of [['1d', 1], ['7d', 7], ['30d', 30], ['90d', 90], ['6m', 180], ['1y', 365]]) {
    selectChartRange(range, (from, to, preset) => { where = withTimeRange(where, usageTime, from, to, preset); });
    for (const clock of [now, now + 86400000]) {
      const bounds = timeBounds(where, usageTime, clock);
      assert.equal(bounds.preset, range);
      assert.equal(bounds.from.getTime(), clock - days * 86400000);
      assert.equal(bounds.to.getTime(), clock);
    }
    assert.deepEqual(where[0], provider);
    assert.equal(timeBounds([...where, { field: 'first_usage_at', op: '<', value: '2026-01-01' }], usageTime, now).preset, undefined);
  }
  for (const mode of ['custom', 'custom-times']) selectChartRange(mode, () => assert.fail('custom modes preserve the current bucket and bounds'));
  selectChartRange('all', (from, to, preset) => { where = withTimeRange(where, usageTime, from, to, preset); });
  assert.deepEqual(where, [provider]);
});

test('date-only custom ranges cover full local dates and retain an exclusive upper boundary', () => {
  for (const [from, to, expectedFrom, expectedTo] of [
    [new Date(2026, 9, 1, 12, 34), new Date(2026, 9, 7, 15, 10), new Date(2026, 9, 1), new Date(2026, 9, 8)],
    [new Date(2026, 9, 1), new Date(2026, 9, 8), new Date(2026, 9, 1), new Date(2026, 9, 8)],
    [undefined, new Date(2026, 2, 8, 15), undefined, new Date(2026, 2, 9)],
    [new Date(2026, 10, 1, 15), undefined, new Date(2026, 10, 1), undefined],
    [undefined, undefined, undefined, undefined],
  ]) {
    const original = { from, to }, before = structuredClone(original);
    assert.deepEqual(customDateBounds(original), { from: expectedFrom, to: expectedTo });
    assert.deepEqual(original, before);
  }
});

test('custom dates and times use the pinned zone and preserve DST day lengths', () => {
  for (const [zone, day, start, end] of [
    ['Asia/Tokyo', '2026-10-20', '2026-10-19T15:00:00.000Z', '2026-10-20T15:00:00.000Z'],
    ['America/Denver', '2026-03-08', '2026-03-08T07:00:00.000Z', '2026-03-09T06:00:00.000Z'],
    ['America/Denver', '2026-11-01', '2026-11-01T06:00:00.000Z', '2026-11-02T07:00:00.000Z'],
    ['Asia/Kathmandu', '2026-10-20', '2026-10-19T18:15:00.000Z', '2026-10-20T18:15:00.000Z'],
  ]) {
    const from = parseRangeInput(day, zone), to = parseRangeInput(day, zone, true);
    assert.equal(from.toISOString(), start); assert.equal(to.toISOString(), end);
    assert.equal(rangeInputValue(from, false, zone), day);
    assert.equal(rangeInputValue(new Date(to.getTime()-1), false, zone), day);
    assert.equal(rangeHasTime(from, zone), false); assert.equal(rangeHasTime(to, zone), false);
    assert.deepEqual(customDateBounds({from, to}, zone), {from, to});
    const midday = parseRangeInput(day+'T12:34', zone);
    assert.equal(rangeInputValue(midday, true, zone), day+'T12:34');
    assert.equal(rangeHasTime(midday, zone), true);
    assert.deepEqual(customDateBounds({from: midday, to: midday}, zone), {from, to});
  }
  assert.equal(parseRangeInput('2026-03-08T02:30','America/Denver').toISOString(), '2026-03-08T09:30:00.000Z');
  assert.equal(parseRangeInput('2026-11-01T01:30','America/Denver').toISOString(), '2026-11-01T07:30:00.000Z');
});

test('bounded range presets use readable intervals with daily detail for 90 days', () => {
  for (const [range, days] of [['1d', 1], ['7d', 7], ['30d', 30], ['90d', 90], ['6m', 183], ['1y', 365]]) {
    const view = { hours: 6, days: 3, ...presetPeriod(range) };
    const end = new Date(2026, 9, 22), start = new Date(end);
    start.setDate(start.getDate() - days);
    const count = periodBuckets(view.period, start, new Date(end.getTime() - 1), chartPeriodCount(view)).length;
    if (range === '90d') assert.equal(count, 90);
    else assert.ok(count >= 20 && count <= 54, `${range}: ${count} intervals`);
  }
});

test('calendar-only dashboards choose supported preset intervals', () => {
  for (const range of ['1d', '7d', '30d', '90d']) assert.equal(calendarPresetPeriod(range), 'day');
  for (const range of ['6m', '1y']) assert.equal(calendarPresetPeriod(range), 'week');
  assert.equal(calendarPresetPeriod('all'), 'week');
});

test('All Time uses months only for conversations strictly older than 20 calendar months', () => {
  const now = new Date(2026, 9, 22, 12, 34, 56, 789), cutoff = new Date(2025, 1, 22, 12, 34, 56, 789);
  for (const [oldest, expected] of [
    [new Date(cutoff.getTime() - 1), 'month'], [cutoff, 'week'], [new Date(cutoff.getTime() + 1), 'week'],
    [now, 'week'], [undefined, 'week'], [new Date(NaN), 'week'],
  ]) {
    assert.equal(presetPeriod('all', oldest, now).period, expected);
    assert.equal(calendarPresetPeriod('all', oldest, now), expected);
  }
});

test('All Time clamps its 20-month cutoff to the end of February', () => {
  for (const [year, lastDay] of [[2026, 28], [2025, 29]]) {
    const now = new Date(year, 9, 31, 12), cutoff = new Date(year - 1, 1, lastDay, 12);
    assert.equal(presetPeriod('all', cutoff, now).period, 'week');
    assert.equal(presetPeriod('all', new Date(cutoff.getTime() - 1), now).period, 'month');
  }
});

test('shared conversation history ignores missing/invalid dates and compares actual instants', () => {
  assert.deepEqual(sharedUsageHistory([]), { oldest_conversation_at: null });
  assert.deepEqual(sharedUsageHistory([
    {}, { conversations: [{}, { started_at: '' }, { started_at: 'unknown' }] },
    { conversations: [{ started_at: '2025-02-21T23:45:00Z' }, { started_at: '2025-02-22T01:30:00+02:00' }] },
  ]), { oldest_conversation_at: '2025-02-22T01:30:00+02:00' });
});

test('charts share history reads, refresh imports, retry failures and support offline exports', async t => {
  const previousWindow = globalThis.window;
  globalThis.window = {};
  t.after(() => { if (previousWindow === undefined) delete globalThis.window; else globalThis.window = previousWindow; });
  let reads = 0, oldest = '2026-01-01T00:00:00Z', fail = false;
  t.mock.method(globalThis, 'fetch', async url => {
    assert.equal(url, '/api/usage/history');
    reads++;
    return new Response(JSON.stringify(fail ? { error: 'unavailable' } : { oldest_conversation_at: oldest }), { status: fail ? 500 : 200 });
  });
  const first = loadUsageHistory(true);
  assert.equal(loadUsageHistory(true), first);
  assert.deepEqual(await first, { oldest_conversation_at: oldest });
  await loadUsageHistory();
  assert.equal(reads, 1);
  oldest = '2023-01-01T00:00:00Z';
  assert.deepEqual(await loadUsageHistory(true), { oldest_conversation_at: oldest });
  fail = true;
  await assert.rejects(loadUsageHistory(true), /unavailable/);
  fail = false;
  await loadUsageHistory(true);
  assert.equal(reads, 4);
  window.pharosShare = { works: [{ conversations: [{ started_at: '2024-01-01T00:00:00Z' }] }] };
  assert.deepEqual(await loadUsageHistory(true), { oldest_conversation_at: '2024-01-01T00:00:00Z' });
  assert.equal(reads, 4, 'shared exports use their own history without contacting the server');
});

test('smoothing history shifts and counts calendar/custom intervals across DST', () => {
  const previous = process.env.TZ; process.env.TZ = 'America/Denver';
  try {
    const from = new Date(2026,10,2), earlier = new Date(2026,10,1);
    assert.equal(shiftPeriod(from,'day',-1).getTime(),earlier.getTime());
    assert.equal(periodIndex(from,'day')-periodIndex(earlier,'day'),1);
    assert.equal(periodIndex(from,'hour')-periodIndex(earlier,'hour'),25);
    assert.equal(periodIndex(from,'hours',6)-periodIndex(earlier,'hours',6),4);
    assert.equal(shiftPeriod(from,'hours',-4,6).getTime(),earlier.getTime());
    assert.equal(shiftPeriod(new Date(2026,2,15),'month',-2).getTime(),new Date(2026,0,1).getTime());
    assert.equal(shiftPeriod(from,'days',-2,3).getTime(),new Date(periodStart(from,'days',3).getFullYear(),periodStart(from,'days',3).getMonth(),periodStart(from,'days',3).getDate()-6).getTime());
    assert.equal(periodIndex(from,'week')-periodIndex(shiftPeriod(from,'week',-19),'week'),19);
  } finally { if(previous===undefined) delete process.env.TZ; else process.env.TZ=previous; }
});

test('hour buckets align actual timestamps and combine N-hour intervals in UTC', () => {
  const previous = process.env.TZ;
  process.env.TZ = 'UTC';
  try {
    assert.equal(periodKey(new Date('2026-10-08T13:42:19Z'), 'hour'), '2026-10-08T13:00:00.000Z');
    assert.equal(periodKey(new Date('2026-10-08T13:42:19Z'), 'hours', 6), '2026-10-08T12:00:00.000Z');
    const buckets = periodBuckets('hours', new Date('2026-10-08T01:30:00Z'), new Date('2026-10-09T02:00:00Z'), 6);
    assert.equal(buckets.length, 5);
    assert.equal(buckets[0].key, '2026-10-08T00:00:00.000Z');
    for (const bucket of buckets) {
      const end = new Date(bucket.date);
      nextPeriod(end, 'hours', 6);
      assert.equal(end - bucket.date, 6 * 3600000);
    }
  } finally { if (previous === undefined) delete process.env.TZ; else process.env.TZ = previous; }
});

test('custom day groups have stable boundaries across adjacent queries', () => {
  const dates = Array.from({ length: 11 }, (_, i) => new Date(2026, 9, 1 + i, 12));
  const keys = dates.map(date => periodKey(date, 'days', 3));
  assert.equal(new Set(keys.slice(3, 6)).size <= 2, true);
  for (const date of dates) {
    const start = periodStart(date, 'days', 3), end = new Date(start);
    nextPeriod(end, 'days', 3);
    assert.ok(start <= date && end > date);
    assert.equal(periodKey(start, 'days', 3), periodKey(date, 'days', 3));
  }
  const full = periodBuckets('days', dates[0], dates[10], 3);
  const narrow = periodBuckets('days', dates[3], dates[7], 3);
  assert.ok(narrow.every(bucket => full.some(entry => entry.key === bucket.key)));
});

test('hourly DST transitions have unique keys, and days keep local midnight', () => {
  const previous = process.env.TZ;
  process.env.TZ = 'America/Denver';
  try {
    const hours = periodBuckets('hour', new Date('2026-11-01T06:00:00Z'), new Date('2026-11-01T11:00:00Z'));
    assert.equal(hours.length, 6);
    assert.equal(new Set(hours.map(bucket => bucket.key)).size, 6);
    const days = periodBuckets('day', new Date(2026, 2, 7), new Date(2026, 2, 10));
    assert.equal(days.length, 4);
    assert.ok(days.every(bucket => bucket.date.getHours() === 0));
    assert.equal(days[2].date - days[1].date, 23 * 3600000);
    const custom = periodBuckets('days', new Date(2026, 2, 1), new Date(2026, 2, 20), 3);
    assert.ok(custom.every(bucket => bucket.date.getHours() === 0));
  } finally { if (previous === undefined) delete process.env.TZ; else process.env.TZ = previous; }
});

test('large ranges retain recent activity, and timestamp keys preserve their hour', () => {
  const end = new Date('2026-10-08T13:00:00Z');
  const buckets = periodBuckets('hour', new Date('2020-01-01T00:00:00Z'), end);
  assert.equal(buckets.length, 5000);
  assert.equal(buckets.at(-1).date.getTime(), end.getTime());
  assert.equal(earliestKey(['2026-10-08T13:00:00Z', '2026-10-08T02:00:00Z']).toISOString(), '2026-10-08T02:00:00.000Z');
});

test('custom counts accept only positive whole numbers', () => {
  for (const count of [1, 6, 24, 10000]) assert.equal(validPeriodCount(count), true);
  for (const count of [0, -1, 1.5, Infinity, NaN, '', '6', 10001]) assert.equal(validPeriodCount(count), false);
});

for (const zone of ['America/Phoenix', 'America/Denver']) {
  test(`multi-hour buckets align local midnight and traverse DST in ${zone}`, () => {
    const previous = process.env.TZ;
    process.env.TZ = zone;
    try {
      for (const count of [2, 3, 4, 6, 8, 12, 24]) {
        for (const month of [2, 10]) {
          const start = new Date(2026, month, month === 2 ? 7 : 31, 0);
          const end = new Date(start);
          end.setDate(end.getDate() + 4);
          const buckets = periodBuckets('hours', start, end, count);
          assert.equal(new Set(buckets.map(bucket => bucket.key)).size, buckets.length);
          assert.equal(buckets[0].date.getTime(), start.getTime());
          assert.equal(buckets.at(-1).date.getTime(), end.getTime());
          for (let i = 0; i < buckets.length - 1; i++) {
            const bucket = buckets[i], next = buckets[i + 1];
            const stepped = new Date(bucket.date);
            nextPeriod(stepped, 'hours', count);
            assert.equal(stepped.getTime(), next.date.getTime());
            assert.ok(next.date > bucket.date);
            for (let at = bucket.date.getTime(); at < next.date.getTime(); at += 15 * 60000) {
              assert.equal(periodKey(new Date(at), 'hours', count), bucket.key);
            }
            if (count === 24) assert.equal(bucket.date.getHours(), 0);
          }
        }
      }
      const at = new Date(2026, 9, 8, 13, 42);
      assert.equal(periodStart(at, 'hours', 6).getHours(), 12);
      assert.equal(periodStart(at, 'hours', 24).getHours(), 0);
      const sixHours = periodBuckets('hours', new Date(2026, 9, 8), new Date(2026, 9, 9), 6);
      assert.deepEqual(sixHours.map(bucket => bucket.date.getHours()), [0, 6, 12, 18, 0]);
    } finally { if (previous === undefined) delete process.env.TZ; else process.env.TZ = previous; }
  });
}


test('Today follows local midnight across DST and fractional offsets while 24 hours stays elapsed', async () => {
  const { todayBounds } = await import('../../../src/day-window');
  for (const [zone, clock, midnight] of [
    ['America/Denver', '2026-03-08T10:00:00Z', '2026-03-08T07:00:00.000Z'],
    ['America/Denver', '2026-11-01T10:00:00Z', '2026-11-01T06:00:00.000Z'],
    ['Asia/Kathmandu', '2026-10-29T05:00:00Z', '2026-10-28T18:15:00.000Z'],
  ]) assert.deepEqual(todayBounds(new Date(clock), zone), [midnight, new Date(clock).toISOString()]);
  const today = withTimeRange([], usageTime, undefined, undefined, 'today');
  const now = Date.now();
  assert.equal(timeBounds(today, usageTime, now).preset, 'today');
  assert.equal(timeBounds(today, usageTime, now).from.toISOString(), todayBounds(new Date(now))[0]);
  const elapsed = withTimeRange([], usageTime, undefined, undefined, '1d');
  assert.equal(now - timeBounds(elapsed, usageTime, now).from.getTime(), 86400000);
});


test('the calendar clock rolls at midnight and rechecks after waking', async t => {
  const { startDayClock } = await import('../../../src/day-window');
  const now = new Date(2026, 9, 29, 23, 59, 59);
  t.mock.timers.enable({ apis: ['Date', 'setTimeout'], now });
  const target = new EventTarget(); target.document = new EventTarget(); target.document.visibilityState = 'visible';
  let days = 0, refreshes = 0;
  target.addEventListener('pharos:day-change', () => days++);
  target.addEventListener('pharos:usage-refresh', () => refreshes++);
  const stop = startDayClock(target);
  t.after(stop);
  t.mock.timers.tick(1000);
  assert.equal(days, 1); assert.equal(refreshes, 1);
  target.dispatchEvent(new Event('focus'));
  assert.equal(days, 1); assert.equal(refreshes, 2);
  stop();
  t.mock.timers.tick(86400000);
  assert.equal(days, 1);
});
