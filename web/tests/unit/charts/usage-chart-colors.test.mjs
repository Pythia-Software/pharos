import { test } from 'node:test';
import assert from 'node:assert/strict';
import { chartSeriesStyle } from '../../../src/chart-colors';
import { lch } from '../../../src/model-color-math';
import { chartCellColor, chartFacetPalette, usageTableRenderers } from '../../../src/usage-chart-colors';
const series = [
  { key: 's:opus-5-5', label: 'opus-5-5', value: 'opus-5-5', color: '#f4a664' },
  { key: 'other', label: 'Other (2)', hiddenCount: 2, foldedValues: ['sonnet-5-5', 'gpt-6-sol'], color: '#85909b' },
];

test('only the current facet has table colors, with folded values matching Other', () => {
  const palette = chartFacetPalette('model_family', series);
  assert.equal(chartCellColor(palette, 'model_family', 'opus-5-5'), '#f4a664');
  assert.equal(chartCellColor(palette, 'model_family', 'sonnet-5-5'), '#85909b');
  assert.equal(chartCellColor(palette, 'repository_name', 'opus-5-5'), undefined);
  assert.equal(chartCellColor(palette, 'provider', 'opus-5-5'), undefined);
  assert.equal(chartCellColor(palette, 'model_family', 'unpriced-and-not-in-the-graph'), undefined);
});

test('reported model variants match their graph family without changing their value', () => {
  const palette = chartFacetPalette('model_family', series);
  for (const value of ['claude-opus-5-5', 'claude-opus-5-5-20260928[1m]', 'OPUS-5-5-1m']) assert.equal(chartCellColor(palette, 'model', value), '#f4a664');
  assert.equal(chartCellColor(palette, 'model', 'reported-alias', 'opus-5-5'), '#f4a664');
});

test('combined model-effort facet shades the model color by effort step, in chart and table alike', () => {
  const efforts = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'ultra', 'max', 'Unspecified'];
  const pairs = efforts.map(effort => {
    const value = `claude-opus-5-5 · ${effort}`;
    return { key: effort, value, color: chartSeriesStyle('model_effort', value).color };
  });
  const color = effort => pairs.find(pair => pair.key === effort).color;
  // Medium is the model's own color; providers offer one of each shared pair.
  assert.equal(color('medium'), chartSeriesStyle('model_family', 'opus-5-5').color);
  for (const [a, b] of [['none', 'medium'], ['minimal', 'low'], ['Unspecified', 'low'], ['ultra', 'max']]) assert.equal(color(a), color(b), `${a} shares ${b}'s shade`);
  const steps = ['low', 'medium', 'high', 'xhigh', 'max'].map(color);
  assert.equal(new Set(steps).size, steps.length);
  const lightness = steps.map(step => lch(step)[0]);
  for (let i = 1; i < lightness.length; i++) assert.ok(lightness[i] < lightness[i - 1], 'higher effort is darker');
  for (const step of steps) assert.ok(Math.abs(lch(step)[2] - lch(color('medium'))[2]) < 3, 'every step keeps the model hue');
  const palette = chartFacetPalette('model_effort', pairs);
  for (const pair of pairs) {
    assert.equal(chartCellColor(palette, 'model_effort', pair.value), pair.color);
    assert.equal(chartSeriesStyle('root_model_effort', pair.value).color, pair.color);
  }
  assert.equal(chartSeriesStyle('model_effort', 'claude-opus-5-5 · HIGH').color, color('high'));
  assert.equal(chartCellColor(palette, 'model', 'claude-opus-5-5'), undefined);
});

test('empty repository values and combined writing providers keep their exact graph keys', () => {
  const repository = chartFacetPalette('repository_name', [{ key: 's:', label: 'No repository', value: '', color: '#abcdef' }]);
  assert.equal(chartCellColor(repository, 'repository_name', null), '#abcdef');
  assert.equal(chartCellColor(repository, 'repository_name', ''), '#abcdef');
  const providers = chartFacetPalette('providers', [{ key: 's:claude,codex', label: 'Claude + Codex', value: 'claude,codex', color: '#123456' }]);
  assert.equal(chartCellColor(providers, 'providers', 'claude,codex'), '#123456');
  assert.equal(chartCellColor(providers, 'provider', 'claude'), undefined);
});

test('expanding Other changes its members to the newly visible series colors', () => {
  const expanded = chartFacetPalette('model_family', [...series.slice(0, 1), { key: 's:sonnet-5-5', label: 'sonnet-5-5', value: 'sonnet-5-5', color: '#efb65c' }]);
  assert.equal(chartCellColor(expanded, 'model_family', 'sonnet-5-5'), '#efb65c');
});

test('unsupported facets leave existing renderers untouched', () => {
  const registry = { text: context => String(context.value) };
  for (const facet of [null, 'type', 'kind', 'session_kind', 'source_kind']) {
    const palette = chartFacetPalette(facet, series);
    assert.equal(palette.facet, null);
    assert.equal(usageTableRenderers(registry, palette), registry);
  }
});

test('renderer decoration preserves the raw context and original renderer content', () => {
  const context = { value: 'claude-opus-5-5-20260928[1m]', row: { model_family: 'opus-5-5' }, field: { name: 'model' }, query: { where: [], orderBy: [] } };
  const before = structuredClone(context);
  const content = { original: true };
  const registry = { text: received => { assert.equal(received, context); return content; } };
  const decorated = usageTableRenderers(registry, chartFacetPalette('model_family', series));
  const rendered = decorated.text(context);
  assert.equal(rendered.props.children[0].props.style.backgroundColor, '#f4a664');
  assert.equal(rendered.props.children[0].props['aria-hidden'], 'true');
  assert.equal(rendered.props.children[1].props.children, content);
  assert.deepEqual(context, before);
  assert.equal(registry.text(context), content);
});
