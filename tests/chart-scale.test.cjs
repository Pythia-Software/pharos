const { test } = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const { buildSync } = require('../web/node_modules/esbuild');
const { outputFiles } = buildSync({ entryPoints: [path.join(__dirname, '../web/src/chart-scale.ts')], bundle: true, platform: 'node', format: 'cjs', write: false });
const loaded = { exports: {} };
new Function('module', 'exports', outputFiles[0].text)(loaded, loaded.exports);
const { columnScale, validChartScale } = loaded.exports;
const modes = ['linear', 'logarithmic', 'half'];
const close = (actual, expected) => assert.ok(Math.abs(actual - expected) < 1e-9, `${actual} should equal ${expected}`);

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
