const { test } = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const { buildSync } = require('../web/node_modules/esbuild');
const { outputFiles } = buildSync({ entryPoints: [path.join(__dirname, '../web/src/usage-chart-colors.tsx')], bundle: true, platform: 'node', format: 'cjs', write: false });
const loaded = { exports: {} };
new Function('module', 'exports', outputFiles[0].text)(loaded, loaded.exports);
const { chartCellColor, chartFacetPalette, usageTableRenderers } = loaded.exports;
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
