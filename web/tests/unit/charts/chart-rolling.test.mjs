import { test } from 'node:test';
import assert from 'node:assert/strict';
import { chartBucketsWithHistory, cumulativeBuckets, plottedBucketKey, rollingAverageBuckets, shareBuckets, validChartDisplay, validRollingPeriods } from '../../../src/chart-series';
import { shiftPeriod } from '../../../src/chart-period';
import { smoothingWhere, timeBounds, usageTime, withTimeRange } from '../../../src/usage-chart';
const bucketsOf = values => values.map((values, index) => ({ key: `2026-10-${String(index + 1).padStart(2, '0')}`, date: new Date(2026, 9, index + 1), values }));

test('relative card ranges stay elapsed across DST and smoothing preserves fact-time selection and stable requests', () => {
  const now = new Date(2026, 2, 10, 12).getTime();
  const where = [{field:'provider',op:'=',value:'codex'}, {field:'first_usage_at',op:'>=',value:'-7d'}, {field:'first_usage_at',op:'<',value:'+0s'}];
  const before = structuredClone(where), bounds = timeBounds(where, usageTime, now);
  assert.equal(bounds.from.getTime(), now - 7 * 86400000);
  assert.equal(bounds.to.getTime(), now);
  const history = smoothingWhere(where, usageTime, 'day', 7, 1, now);
  const start = new Date(now - 7 * 86400000); start.setHours(0,0,0,0); start.setDate(start.getDate()-6);
  assert.equal(timeBounds(history, usageTime, now).from.getTime(), start.getTime());
  assert.ok(history.some(term=>term.field==='first_usage_at' && term.op==='>='));
  assert.ok(!history.some(term=>term.field==='last_usage_at'), 'a source-fact window must not turn into an overlapping-session window');
  assert.ok(history.some(term=>term.op==='<' && term.value==='+0s'));
  assert.deepEqual(smoothingWhere(where, usageTime, 'day', 7, 1, now+1000), history, 'rerenders within a calendar bucket reuse the same request');
  assert.deepEqual(where, before);
});

test('a 20-day average starts with its full history before the selected 30-day range', () => {
  const start = new Date(2026, 9, 1), end = new Date(2026, 9, 31);
  const where = withTimeRange([{ field: 'provider', op: '=', value: 'codex' }], usageTime, start, end);
  const before = structuredClone(where), requested = smoothingWhere(where, usageTime, 'day', 20), bounds = timeBounds(requested, usageTime);
  assert.equal(bounds.from.getTime(), new Date(2026, 8, 12).getTime());
  assert.equal(bounds.to.getTime(), end.getTime());
  assert.deepEqual(requested[0], where[0]);
  assert.deepEqual(where, before, 'visible dates and other filters remain unchanged');
  for (const periods of [0, 1]) assert.equal(smoothingWhere(where, usageTime, 'day', periods), where);
  assert.deepEqual(smoothingWhere(where, usageTime, 'day', Number.MAX_SAFE_INTEGER), withTimeRange(where, usageTime, undefined, end));
  const dates = Array.from({length:20}, (_, index) => new Date(2026, 8, 12 + index));
  const buckets = chartBucketsWithHistory('day', start, new Date(end.getTime()-1), 1, dates, bounds.from);
  for (const bucket of buckets) bucket.values.cost = bucket.date < start ? 100 : bucket.date.getTime() === start.getTime() ? 2000 : 0;
  const visible = rollingAverageBuckets(buckets, 20, {period:'day'}).filter(bucket=>!bucket.history);
  assert.equal(visible.length, 30);
  assert.equal(visible[0].date.getTime(), start.getTime());
  assert.equal(visible[0].windowSize, 20);
  assert.equal(visible[0].values.cost, 195, '19 earlier days plus the current day');
  assert.equal(visible[1].values.cost, 190, 'the oldest history day leaves the next window');
});

test('sparse historical buckets count idle intervals and keep very large windows bounded', () => {
  const start = new Date(2026,9,1), historyStart = new Date(2026,8,12);
  const buckets = chartBucketsWithHistory('day', start, start, 1, [new Date(2026,8,30)], historyStart);
  assert.equal(buckets.length,3, 'only the history anchor, occupied history and visible day are materialized');
  buckets[1].values.cost = 100;
  const first = rollingAverageBuckets(buckets,20,{period:'day'}).at(-1);
  assert.equal(first.values.cost,5);
  assert.equal(first.windowSize,20);
  const huge = chartBucketsWithHistory('day',start,start,1,[new Date(2000,0,2)],new Date(2000,0,1));
  huge[1].values.cost = 100;
  assert.equal(huge.length,3);
  assert.ok(rollingAverageBuckets(huge,Number.MAX_SAFE_INTEGER,{period:'day'}).at(-1).values.cost > 0);
  assert.equal(shiftPeriod(start,'day',1-Number.MAX_SAFE_INTEGER),undefined);
});

test('percentage shares normalize averaged amounts including pre-range history', () => {
  const start = new Date(2026,9,2), history = new Date(2026,9,1);
  const buckets = chartBucketsWithHistory('day',start,start,1,[history],history);
  buckets[0].values = {a:100,b:0}; buckets[1].values = {a:0,b:10};
  const last = shareBuckets(rollingAverageBuckets(buckets,2,{period:'day'})).at(-1);
  assert.equal(last.values.a,100/110);
  assert.equal(last.values.b,10/110);
});

test('cumulative totals retain each series through empty intervals without changing source data', () => {
  const buckets = bucketsOf([{ orange: 90, green: 10 }, {}, { green: 30, blue: 10 }]);
  const before = structuredClone(buckets), totals = cumulativeBuckets(buckets);
  assert.deepEqual(totals.map(bucket => bucket.values), [
    { orange: 90, green: 10 }, { orange: 90, green: 10 }, { orange: 90, green: 40, blue: 10 },
  ]);
  assert.deepEqual(totals.map(bucket => [bucket.key, bucket.date]), buckets.map(bucket => [bucket.key, bucket.date]));
  assert.deepEqual(buckets, before);
  assert.deepEqual(cumulativeBuckets(buckets.slice(2))[0].values, { green: 30, blue: 10 }, 'a new range restarts the balance');
  assert.deepEqual(cumulativeBuckets([]), []);
});

test('cumulative Other details and percentage shares use accumulated raw amounts', () => {
  const buckets = bucketsOf([{ model: 90, other: 10 }, {}, { model: 10, other: 30 }]);
  buckets[0].parts = { other: [['small-a', 10]] };
  buckets[2].parts = { other: [['small-a', 20], ['small-b', 10]] };
  const cumulative = cumulativeBuckets(buckets);
  assert.deepEqual(cumulative[0].parts.other, [['small-a', 10]]);
  assert.deepEqual(cumulative[2].parts.other, [['small-a', 30], ['small-b', 10]]);
  assert.equal(cumulative[2].values.other, cumulative[2].parts.other.reduce((sum, [, value]) => sum + value, 0));
  const shares = shareBuckets(cumulative);
  assert.equal(shares[2].values.model, 100 / 140);
  assert.equal(shares[2].values.other, 40 / 140);
  assert.equal(Object.values(shares[2].values).reduce((sum, value) => sum + value, 0), 1);
  assert.equal(shareBuckets(cumulativeBuckets(bucketsOf([{}])))[0].values.model, undefined);
});

test('display settings accept existing links and reject invalid modes', () => {
  for (const display of [undefined, 'interval', 'cumulative']) assert.equal(validChartDisplay(display), true);
  for (const display of [null, '', 'total', {}, 0, true]) assert.equal(validChartDisplay(display), false);
});

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

test('the range start uses only available periods, including arbitrarily large windows', () => {
  const buckets = bucketsOf([{ tokens: 100 }, { tokens: 200 }, { tokens: 600 }]);
  for (const periods of [10, 37, 1000000, Number.MAX_SAFE_INTEGER]) {
    const averaged = rollingAverageBuckets(buckets, periods);
    assert.deepEqual(averaged.map(bucket => bucket.values.tokens), [100, 150, 300]);
    assert.deepEqual(averaged.map(bucket => bucket.windowSize), [1, 2, 3]);
  }
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
  for (const value of [-1, 1.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1, undefined, null, '3']) {
    assert.equal(validRollingPeriods(value), false);
    assert.equal(rollingAverageBuckets(buckets, value), buckets);
  }
  for (const periods of [0, 1, 2, 10, 11, 30, 365, 10000]) assert.equal(validRollingPeriods(periods), true);
});

test('cumulative charts exclude smoothing history and retain the selected opening balance at the plot limit', () => {
  const source = bucketsOf([{ calls: 100 }, { calls: 2 }, {}, { calls: 3 }]);
  source[0].history = true;
  assert.deepEqual(cumulativeBuckets(source).map(bucket => bucket.values.calls), [2, 2, 5]);
  const visible = bucketsOf([{}, {}]).map((bucket, i) => ({ ...bucket, key: `2026-10-0${i+3}`, date: new Date(2026,9,i+3) }));
  assert.equal(plottedBucketKey(new Date(2026,9,1), 'day', 1, visible, 'cumulative'), '2026-10-03');
  assert.equal(plottedBucketKey(new Date(2026,9,1), 'day', 1, visible, 'interval'), '2026-10-01');
});

