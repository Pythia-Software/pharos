import { test } from 'node:test';
import assert from 'node:assert/strict';
import { columnScale, validChartScale } from '../../../src/chart-scale';
import { timelineGeometry, timelinePath } from '../../../src/chart-series';
const modes = ['linear', 'logarithmic', 'half'];
const close = (actual, expected, message) => assert.ok(Math.abs(actual - expected) < 1e-9, message ?? `${actual} should equal ${expected}`);

test('each scale starts at zero, reaches the peak, and preserves stacked totals', () => {
  for (const mode of modes) {
    for (const peak of [0.05, 100, 1e12]) {
      const { y, ticks } = columnScale([0, peak / 100, peak], mode);
      close(y(0), 0);
      close(y(peak), 100);
      const values = [0, peak / 100, peak / 10, peak / 2, peak];
      const heights = values.slice(1).map((value, index) => y(value) - y(values[index]));
      assert.ok(heights.every(height => Number.isFinite(height) && height > 0));
      close(heights.reduce((sum, height) => sum + height, 0), 100);
      assert.ok(ticks.length > 0);
      assert.ok(ticks.every(tick => tick > 0 && tick <= peak && Number.isFinite(y(tick))));
    }
  }
});

test('linear heights are proportional and logarithmic heights expand small columns', () => {
  const linear = columnScale([10, 100, 1000], 'linear');
  const log = columnScale([10, 100, 1000], 'logarithmic');
  close(linear.y(100), 10);
  assert.ok(log.y(10) > linear.y(10));
  assert.ok(log.y(100) > linear.y(100));
  assert.equal(linear.split, undefined);
  assert.equal(log.split, undefined);
});

test('half-and-half uses a continuous midpoint break even without large outliers', () => {
  for (const totals of [[100], [10, 10, 10, 10, 10, 1000]]) {
    const { y, split } = columnScale(totals, 'half');
    assert.ok(split > 0 && split < Math.max(...totals));
    close(y(split), 50);
    close(y(split / 2), 25);
    assert.ok(y(split + split * 1e-10) - y(split) < 1e-6);
  }
  assert.equal(columnScale([10, 10, 10, 10, 10, 1000], 'half').split, 10);
});

test('empty charts have finite coordinates and no ticks or break', () => {
  for (const mode of modes) {
    for (const totals of [[], [0, 0]]) {
      const { y, ticks, split } = columnScale(totals, mode);
      assert.equal(y(0), 0);
      assert.deepEqual(ticks, []);
      assert.equal(split, undefined);
    }
  }
});

test('saved scales accept only the three supported modes', () => {
  for (const mode of modes) assert.equal(validChartScale(mode), true);
  for (const value of [null, undefined, '', 'auto', 1, {}]) assert.equal(validChartScale(value), false);
});

test('log timelines compare independent series on the same axis, regardless of stacking order', () => {
  const buckets = [
    { key: 'one', values: { a: 1000, b: 1000, c: 10 } },
    { key: 'idle', values: {} },
    { key: 'two', values: { a: 10, b: 100, c: 10 } },
  ], series = ['a', 'b', 'c'].map(key => ({ key })), before = structuredClone(buckets);
  const plot = timelineGeometry(buckets, series, { scale: 'logarithmic' });
  assert.equal(plot.lines, true);
  close(plot.y(1000), 100, 'axis uses the largest series value, not the stacked total');
  assert.equal(timelinePath(buckets, 'a', plot.y), timelinePath(buckets, 'a', timelineGeometry(buckets, [...series].reverse(), { scale: 'logarithmic' }).y));
  assert.equal(timelinePath(buckets, 'a', plot.y).split(' L ')[1].split(' ')[1], '100', 'idle days remain on the zero baseline');
  assert.deepEqual(buckets, before);
  const linear = timelineGeometry(buckets, series, { scale: 'linear' });
  assert.equal(linear.lines, false);
  close(linear.y(2010), 100);
  assert.equal(timelineGeometry(buckets, series, { scale: 'logarithmic', percent: true }).lines, false, 'shares keep their linear stack');
  close(timelineGeometry(buckets, series, { scale: 'logarithmic', reference: 10000 }).y(10000), 100, 'reference values remain inside the axis');
});

test('timeline lines preserve gaps in non-additive measures and remain visible for a single period', () => {
  const y = value => value;
  assert.equal(timelinePath([], 'a', y), '');
  const buckets = [{ values: { a: 10 } }, { values: {} }, { values: { a: 20 } }];
  assert.equal(timelinePath(buckets, 'a', y, false).match(/M /g).length, 2, 'missing ratios break the line');
  assert.equal(timelinePath(buckets, 'a', y).match(/M /g).length, 1, 'idle amounts stay connected');
  const lone = timelinePath(buckets.slice(0, 1), 'a', y);
  assert.ok(lone.includes(' L '), 'one period draws a segment');
  assert.ok(!/NaN|Infinity/.test(lone));
});
