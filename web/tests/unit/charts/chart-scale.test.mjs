import { test } from 'node:test';
import assert from 'node:assert/strict';
import { columnScale, defaultChartScale, validChartScale } from '../../../src/chart-scale';
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

test('the default scale preserves proportional heights unless peaks exceed ten times the 80th percentile', () => {
  for (const totals of [
    [100], [1, 1000], [1, 1, 1, 1000],
    [10, 10, 10, 10, 10], [10, 20, 30, 40, 50],
    [10, 10, 10, 10, 10, 99], [10, 10, 10, 10, 10, 100],
    [11, 11, 11, 11, 11, 110],
    [0, 0, 0, 0, 0, 1, 1000],
    [0.001, 100, 100, 100, 100, 100],
  ]) {
    const original = [...totals], expected = columnScale(totals, 'linear');
    const actual = columnScale(totals, defaultChartScale);
    assert.equal(actual.split, undefined, JSON.stringify(totals));
    assert.deepEqual(actual.ticks, expected.ticks);
    for (const value of totals) close(actual.y(value), expected.y(value));
    assert.deepEqual(totals, original);
  }
});

test('half-and-half compresses extreme peaks with a continuous midpoint break in any unit', () => {
  for (const unit of [0.0001, 1, 1e9]) {
    for (const values of [[10, 10, 10, 10, 10, 101], [0, 0, 10, 10, 10, 10, 10, 10000], [11, 11, 11, 11, 11, 111]]) {
      const totals = values.map(value => value * unit);
      const { y, split } = columnScale(totals, 'half');
      assert.ok(split > 0 && split < Math.max(...totals));
      close(y(split), 50);
      close(y(split / 2), 25);
      assert.ok(y(split + split * 1e-10) - y(split) < 1e-6);
      close(y(Math.max(...totals)), 100);
    }
  }
  assert.equal(columnScale([10, 10, 10, 10, 10, 101], 'half').split, 10);
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
  for (const scale of modes) {
    const bars = timelineGeometry(buckets, series, { scale, bars: true, grouped: true, lines: true });
    assert.equal(bars.lines, false, 'explicit bars keep their shape, including on a log scale');
    close(bars.y(1000), 100, 'overlapping categories use an independent baseline, not a sum');
    close(timelineGeometry(buckets, [...series].reverse(), { scale, bars: true, grouped: true }).y(1000), bars.y(1000));
  }
});

test('timeline lines preserve gaps in non-additive measures and remain visible for a single period', () => {
  const y = value => value;
  assert.equal(timelinePath([], 'a', y), '');
  const buckets = [{ values: { a: 10 } }, { values: {} }, { values: { a: 20 } }];
  assert.equal(timelinePath(buckets, 'a', y, false).match(/M /g).length, 2, 'missing ratios break the line');
  assert.equal(timelinePath(buckets, 'a', y).match(/M /g).length, 1, 'idle amounts stay connected');
  const passed = timelinePath(buckets, 'a', y, true, true);
  assert.equal(passed.match(/M /g).length, 1, 'without zeroes, a line passes over a missing value');
  assert.equal(passed.split(' ').filter(part => part === 'L').length, 1, 'and draws nothing at it');
  // A series left with one value among several periods still draws.
  const single = timelinePath([{ values: { a: 10 } }, { values: { b: 20 } }], 'a', y, true, true);
  assert.equal(single, 'M 0 90 L 500 90', 'across its own period');
  assert.equal(timelinePath([{ values: { a: 10 } }, {values: {} }, { values: { a: 20 } }], 'a', y, false).match(/ L /g).length, 2, 'as does each lone point of a broken line');
  const lone = timelinePath(buckets.slice(0, 1), 'a', y);
  assert.ok(lone.includes(' L '), 'one period draws a segment');
  assert.ok(!/NaN|Infinity/.test(lone));
});
