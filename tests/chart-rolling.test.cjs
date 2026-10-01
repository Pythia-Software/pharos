const { test } = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const { buildSync } = require('../web/node_modules/esbuild');
const { outputFiles } = buildSync({ entryPoints: [path.join(__dirname, '../web/src/chart-series.ts')], bundle: true, platform: 'node', format: 'cjs', write: false });
const loaded = { exports: {} };
new Function('module', 'exports', outputFiles[0].text)(loaded, loaded.exports);
const { rollingAverageBuckets, validRollingPeriods } = loaded.exports;
const bucketsOf = values => values.map((values, index) => ({ key: `2026-10-${String(index + 1).padStart(2, '0')}`, date: new Date(2026, 9, index + 1), values }));

test('0 and 1 periods preserve the original bucket data without smoothing', () => {
  const buckets = bucketsOf([{ tokens: 0 }, { tokens: 900 }]);
  for (const periods of [0, 1]) assert.equal(rollingAverageBuckets(buckets, periods), buckets);
});

test('rolling windows are trailing and include zero-usage periods', () => {
  const buckets = bucketsOf([{ tokens: 0 }, { tokens: 0 }, { tokens: 900 }, {}, {}]);
  assert.deepEqual(rollingAverageBuckets(buckets, 3).map(bucket => bucket.values.tokens ?? 0), [0, 0, 300, 300, 300]);
  assert.deepEqual(rollingAverageBuckets(buckets, 2).map(bucket => bucket.values.tokens ?? 0), [0, 0, 450, 450, 0]);
  assert.deepEqual(buckets[2].values, { tokens: 900 });
});

test('the range start uses only available periods, even for a ten-period window', () => {
  const buckets = bucketsOf([{ tokens: 100 }, { tokens: 200 }, { tokens: 600 }]);
  const averaged = rollingAverageBuckets(buckets, 10);
  assert.deepEqual(averaged.map(bucket => bucket.values.tokens), [100, 150, 300]);
  assert.deepEqual(averaged.map(bucket => bucket.windowSize), [1, 2, 3]);
});

test('every series is averaged independently with a common denominator', () => {
  const buckets = bucketsOf([{ orange: 90, green: 10 }, { green: 30, blue: 10 }, {}]);
  const averaged = rollingAverageBuckets(buckets, 2);
  assert.deepEqual(averaged[1].values, { orange: 45, green: 20, blue: 5 });
  assert.deepEqual(averaged[2].values, { green: 15, blue: 5 });
  assert.equal(Object.values(averaged[1].values).reduce((sum, value) => sum + value, 0), 70);
});

test('Other breakdowns use the same window and match their averaged series total', () => {
  const buckets = bucketsOf([{ other: 90 }, {}, { other: 60 }]);
  buckets[0].parts = { other: [['small-a', 60], ['small-b', 30]] };
  buckets[2].parts = { other: [['small-a', 20], ['small-c', 40]] };
  const averaged = rollingAverageBuckets(buckets, 3);
  assert.deepEqual(averaged[2].parts.other, [['small-a', 80 / 3], ['small-b', 10], ['small-c', 40 / 3]]);
  assert.equal(averaged[2].values.other, 50);
  assert.ok(Math.abs(averaged[2].parts.other.reduce((sum, [, value]) => sum + value, 0) - 50) < 1e-10);
});

test('keys, dates, and raw detail counts stay attached to the original period', () => {
  const buckets = bucketsOf([{ tokens: 3 }, { tokens: 12 }]);
  buckets[1].detail = '3 messages with typed text';
  const before = structuredClone(buckets);
  const averaged = rollingAverageBuckets(buckets, 2);
  assert.equal(averaged[1].key, buckets[1].key);
  assert.equal(averaged[1].date, buckets[1].date);
  assert.equal(averaged[1].values.tokens, 7.5);
  assert.equal(averaged[1].detail, 'This period: 3 messages with typed text');
  assert.deepEqual(buckets, before);
});

test('empty data and invalid stored window values remain safe', () => {
  assert.deepEqual(rollingAverageBuckets([], 10), []);
  const buckets = bucketsOf([{ tokens: 3 }]);
  for (const value of [-1, 11, 1.5, NaN, Infinity, undefined, null, '3']) {
    assert.equal(validRollingPeriods(value), false);
    assert.equal(rollingAverageBuckets(buckets, value), buckets);
  }
  for (let periods = 0; periods <= 10; periods++) assert.equal(validRollingPeriods(periods), true);
});
