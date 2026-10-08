const { test } = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const { buildSync } = require('../web/node_modules/esbuild');
const { outputFiles } = buildSync({ entryPoints: [path.join(__dirname, '../web/src/chart-period.ts')], bundle: true, platform: 'node', format: 'cjs', write: false });
const loaded = { exports: {} };
new Function('module', 'exports', outputFiles[0].text)(loaded, loaded.exports);
const { periodStart, periodKey, nextPeriod, periodBuckets, earliestKey, validPeriodCount } = loaded.exports;

test('hour buckets align actual timestamps and combine fixed N-hour intervals', () => {
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
