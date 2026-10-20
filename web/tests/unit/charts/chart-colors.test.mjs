import { test } from 'node:test';
import assert from 'node:assert/strict';
import catalog from '../../../../pricing/model_colors.json';
import prices from '../../../../pricing/cost_changes.json';
import { chartSeriesStyle, metricSeriesColor, modelColorKey, otherSeriesColor } from '../../../src/chart-colors';
import { foldSeries } from '../../../src/chart-series';
const bucketKey = '2026-10-01';
const fold = (names, limit = 5, selected = [], scope = 'model_family') => {
  const buckets = new Map([[bucketKey, { key: bucketKey, date: new Date(2026, 9, 1), values: {} }]]);
  const series = foldSeries(names.map((name, index) => [bucketKey, name, names.length - index]), buckets, name => name, undefined, selected, limit, scope);
  return { series, bucket: buckets.get(bucketKey) };
};

test('static catalog covers priced models, with unique colors for model versions and sources', () => {
  for (const price of prices.changes) {
    const key = modelColorKey(price.model);
    assert.equal(catalog.models[key]?.provider, price.provider, price.model);
    assert.equal(chartSeriesStyle('model_family', key).color, catalog.models[key].color);
  }
  for (const entries of [Object.values(catalog.models), Object.values(catalog.sources)]) {
    assert.equal(new Set(entries.map(entry => entry.color)).size, entries.length);
    assert.ok(entries.every(entry => /^#[0-9a-f]{6}$/.test(entry.color)));
    assert.ok(entries.every(entry => entry.color !== otherSeriesColor));
  }
});

test('newer model versions use darker shades within each class', () => {
  const luminance = color => color.slice(1).match(/../g)
    .map(channel => parseInt(channel, 16) / 255)
    .map(channel => channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4)
    .reduce((total, channel, index) => total + channel * [0.2126, 0.7152, 0.0722][index], 0);
  const families = [
    ['opus-4-1', 'opus-4-5', 'opus-4-6', 'opus-4-7', 'opus-4-8', 'opus-5', 'opus-5-5'],
    ['sonnet-4', 'sonnet-4-5', 'sonnet-4-6', 'sonnet-5', 'sonnet-5-5'],
    ['haiku', 'haiku-4-5', 'haiku-5-5'],
    ['fable-5', 'fable-5-1'],
    ['gpt-5.5', 'gpt-5.6-luna', 'gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-6-astra', 'gpt-6-luna', 'gpt-6-sol'],
  ];
  for (const versions of families) {
    for (let index = 1; index < versions.length; index++) {
      const older = chartSeriesStyle('model_family', versions[index - 1]);
      const newer = chartSeriesStyle('model_family', versions[index]);
      assert.ok(luminance(newer.color) < luminance(older.color), `${versions[index]} should be darker than ${versions[index - 1]}`);
      assert.equal(newer.group, older.group);
    }
  }
});

test('model normalization keeps snapshots and context variants on the same static color', () => {
  for (const name of ['claude-opus-5-5', 'OPUS-5-5', 'opus-5-5-1m', 'claude-opus-5-5[1m]', 'claude-opus-5-5-20260928', 'claude-opus-5-5-20260928[1m]']) {
    assert.equal(chartSeriesStyle('model_family', name).color, catalog.models['opus-5-5'].color);
  }
});

test('expansion and ranking changes never recycle or change existing colors', () => {
  const names = [...Object.keys(catalog.models), ...Array.from({ length: 50 }, (_, index) => `opus-future-${index}`), ...Array.from({ length: 50 }, (_, index) => `unknown-${index}`)];
  const initial = fold(names).series;
  const expanded = fold(names, names.length).series;
  assert.equal(new Set(expanded.map(series => series.color)).size, expanded.length);
  for (const series of initial.filter(series => series.value !== undefined)) assert.equal(expanded.find(item => item.key === series.key).color, series.color);
  const reversed = fold([...names].reverse(), names.length).series;
  for (const series of expanded) assert.equal(reversed.find(item => item.key === series.key).color, series.color);
  const filtered = fold(['opus-5-5']).series;
  assert.equal(filtered[0].color, expanded.find(item => item.value === 'opus-5-5').color);
  assert.ok(!expanded.some(series => series.color === otherSeriesColor));
});

test('stacks group by provider, then class and version rather than token rank', () => {
  const names = ['gpt-6-sol', 'sonnet-5-5', 'gemini-3.8-flash', 'opus-5-5', 'opus-4-8', 'gpt-5.3-codex'];
  const { series, bucket } = fold(names, names.length);
  assert.deepEqual(series.map(item => item.value), ['opus-4-8', 'opus-5-5', 'sonnet-5-5', 'gemini-3.8-flash', 'gpt-5.3-codex', 'gpt-6-sol']);
  assert.deepEqual(series.map(item => item.group), ['Anthropic', 'Anthropic', 'Anthropic', 'Google', 'OpenAI', 'OpenAI']);
  assert.equal(Object.values(bucket.values).reduce((total, value) => total + value, 0), 21);
});

test('Other retains totals and selected models stay visible, even without usage', () => {
  const names = ['opus-5', 'gpt-5.5', 'gemini-3.8-flash', 'sonnet-5', 'haiku-4-5', 'gpt-6-sol', 'fable-5'];
  const { series, bucket } = fold(names, 5, ['gpt-6-sol', 'opus-no-usage']);
  assert.equal(series.find(item => item.key === 'other').hiddenCount, 1);
  assert.equal(bucket.values.other, 1);
  assert.deepEqual(bucket.parts.other, [['fable-5', 1]]);
  assert.ok(series.some(item => item.value === 'opus-no-usage'));
  assert.equal(Object.values(bucket.values).reduce((total, value) => total + value, 0), 28);
  assert.equal(new Set(series.map(item => item.color)).size, series.length);
});

test('sources share provider families without sharing colors', () => {
  const { series } = fold(['codex', 'tl1', 'chatgpt', 'claude', 'antigravity', 'canonical', 'new-source'], 10, [], 'provider');
  assert.deepEqual(series.map(item => item.group), ['Anthropic', 'Google', 'OpenAI', 'OpenAI', 'Other providers', 'Other providers', 'Other providers']);
  assert.equal(series.find(item => item.value === 'antigravity').color, '#4285f4');
  assert.equal(series.find(item => item.value === 'codex').color, '#10a37f');
  assert.equal(new Set(series.map(item => item.color)).size, series.length);
});

test('repository and other splits have stable unique colors beyond five series', () => {
  const names = Array.from({ length: 500 }, (_, index) => `repository-${index}`);
  const expanded = fold(names, names.length, [], 'repository_name').series;
  assert.equal(new Set(expanded.map(series => series.color)).size, names.length);
  const reversed = fold([...names].reverse(), names.length, [], 'repository_name').series;
  for (const series of expanded) assert.equal(reversed.find(item => item.key === series.key).color, series.color);
});

test('unrecognized models and object-property names safely get fallback colors', () => {
  for (const name of ['', '<synthetic>', 'constructor', '__proto__', 'toString']) {
    assert.match(chartSeriesStyle('model_family', name).color, /^#[0-9a-f]{6}$/);
    assert.match(chartSeriesStyle('provider', name).color, /^#[0-9a-f]{6}$/);
  }
});

test('compound model groups keep catalog model colors regardless of label or key order', () => {
  const expected = chartSeriesStyle('model_family', 'opus-5-5').color;
  assert.equal(metricSeriesColor(['provider', 'model_family'], ['claude', 'opus-5-5']), expected);
  assert.equal(metricSeriesColor(['repository_name', 'model_family', 'provider', 'effort'], ['repo', 'opus-5-5', 'claude', 'high']), expected);
  assert.equal(metricSeriesColor(['provider', 'model_family', 'agent_session_id'], ['claude', 'opus-5-5', 'session']), expected);
  assert.equal(metricSeriesColor(['root_provider', 'root_model_family', 'root_effort'], ['claude', 'opus-5-5', 'high']), expected);
  assert.equal(metricSeriesColor(['effort'], ['high']), chartSeriesStyle('effort', 'high').color);
});

test('effort levels use a sequential palette that darkens with effort and keeps unknown neutral', () => {
  const levels = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'ultra', 'max'];
  const luminance = color => { const channels = color.slice(1).match(/../g).map(c => parseInt(c, 16)); return .2126 * channels[0] + .7152 * channels[1] + .0722 * channels[2]; };
  const lightness = levels.map(level => luminance(chartSeriesStyle('effort', level).color));
  assert.ok(lightness.every((value, i) => !i || value < lightness[i - 1]));
  assert.equal(chartSeriesStyle('effort', 'Unspecified').color, otherSeriesColor);
});

test('concurrency bands stack with the busiest at the bottom and warm toward red', () => {
  const labels = ['1 active', '2 at once', '3 at once', '4+ at once'];
  const { series } = fold(labels, 4, [], 'level_band');
  assert.deepEqual(series.map(item => item.label), ['4+ at once', '3 at once', '2 at once', '1 active']);
  assert.equal(new Set(series.map(item => item.color)).size, 4);
  assert.equal(chartSeriesStyle('level_band', '4+ at once').color, chartSeriesStyle('level_band', '4+').color);
  const red = color => parseInt(color.slice(1, 3), 16) - parseInt(color.slice(3, 5), 16);
  assert.ok(red(series[0].color) > red(series[3].color));
});
