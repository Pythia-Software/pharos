import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createMetricColorResolver } from '@pythia-software/query-table-ui';
import { customModelColor, effectiveModelColor, installModelColors, knownModels, modelColorKey, loadModelColors, modelPalette, modelSeriesColor, recommendedModelColor, saveModelColor, shadeChoices } from '../../../src/model-colors';
import { chartMetricTheme, chartSeriesStyle, keyColorTheme, metricSeriesColor } from '../../../src/chart-colors';
const palette = (overrides = {}, models = []) => ({ overrides, models, sources: [] });

test('provider choices reach models, recorded providers, root scopes and compound charts while preserving explicit choices', t => {
  t.after(() => installModelColors(palette()));
  installModelColors(palette({ 'provider:openai': '#7547b3', 'model:gpt-6-sol': '#ee1122', 'source:chatgpt': '#ab3456' }));
  assert.equal(chartSeriesStyle('provider', 'codex').color, '#7547b3');
  assert.equal(chartSeriesStyle('root_provider', 'codex').color, '#7547b3');
  assert.equal(chartSeriesStyle('provider', 'chatgpt').color, '#ab3456');
  assert.equal(chartSeriesStyle('model_family', 'gpt-6-sol').color, '#ee1122');
  assert.equal(chartSeriesStyle('root_model_family', 'gpt-6-sol').color, '#ee1122');
  const inherited = chartSeriesStyle('model_family', 'gpt-5.5').color;
  assert.equal(inherited, recommendedModelColor('gpt-5.5', '#7547b3'));
  assert.equal(metricSeriesColor(['repository_name', 'root_model_family'], ['repo', 'gpt-6-sol']), '#ee1122');
  assert.equal(chartMetricTheme().dimensions.root_model_family.overrides.find(item => item.value === 'gpt-6-sol').color, '#ee1122');
  assert.equal(keyColorTheme(['model_family'], [{ keys: ['claude-opus-5-5[1m]'] }]).dimensions.model_family.overrides[0].color, chartSeriesStyle('model_family', 'opus-5-5').color);
  const actual = chartMetricTheme([{ id: 'm', groupBy: ['provider', 'model_family'] }], { metrics: [{ id: 'm', buckets: [{ keys: ['local-provider', 'claude-opus-5-5[1m]'], value: 1 }, { keys: ['codex', 'gpt-local'], value: 2 }] }] });
  assert.equal(actual.dimensions.model_family.overrides.find(item => item.value === 'claude-opus-5-5[1m]').color, chartSeriesStyle('model_family', 'opus-5-5').color);
  assert.equal(actual.dimensions.model_family.overrides.find(item => item.value === 'gpt-local').color, chartSeriesStyle('model_family', 'gpt-local').color);
  const mixed = chartSeriesStyle('model_effort', 'gpt-6-sol · high').color;
  assert.ok(mixed.includes('#ee1122'));
  installModelColors(palette({ 'provider:openai': '#226688', 'model:gpt-6-sol': '#ee1122', 'source:chatgpt': '#ab3456' }));
  assert.notEqual(chartSeriesStyle('model_family', 'gpt-5.5').color, inherited);
  assert.equal(chartSeriesStyle('model_family', 'gpt-6-sol').color, '#ee1122');
  assert.equal(chartSeriesStyle('provider', 'chatgpt').color, '#ab3456');
  assert.equal(customModelColor('model', 'gpt-6-sol'), '#ee1122');
  assert.equal(chartSeriesStyle('provider', 'openai').color, '#226688');
  installModelColors(palette({ 'provider:openai': '#226688', 'source:openai': '#aabbcc' }, []));
  assert.equal(chartSeriesStyle('provider', 'openai').color, '#aabbcc', 'a recorded provider named like its developer can still have its own choice');
  assert.equal(effectiveModelColor('model', 'gpt-5.5'), recommendedModelColor('gpt-5.5', '#226688'));

});

test('recommendations remain stable when models arrive and undated local models remain customizable', t => {
  t.after(() => installModelColors(palette()));
  const color = effectiveModelColor('model', 'gpt-5.5');
  installModelColors(palette({}, ['gpt-local', 'opus-future', 'constructor']));
  assert.equal(effectiveModelColor('model', 'gpt-5.5'), color);
  assert.ok(knownModels().includes('gpt-local'));
  installModelColors({ overrides: { 'provider:openai': '#7547b3' }, models: null, sources: null });
  assert.ok(knownModels().includes('gpt-local'), 'an override refresh retains the discovered inventory');
  for (const name of ['gpt-local', '__proto__', 'constructor', 'opus-future']) assert.match(effectiveModelColor('model', name), /^#[0-9a-f]{6}$/);
  installModelColors(palette({ 'model:opus-future': '#2255aa' }));
  assert.ok(knownModels().includes('opus-future'), 'temporarily absent saved models stay editable');
  assert.equal(effectiveModelColor('model', 'claude-opus-future-20261110[1m]'), '#2255aa');
  assert.equal(new Set(shadeChoices('#2255aa')).size, 5);
  for (const base of ['#000000', '#ffffff', '#ff0000', '#00ff00', '#0000ff', '#2255aa']) {
    for (const shade of shadeChoices(base)) assert.match(shade, /^#[0-9a-f]{6}$/);
    for (const model of ['gpt-local', 'opus-5-5']) assert.match(recommendedModelColor(model, base), /^#[0-9a-f]{6}$/);
  }
});

test('already mounted marks and cached SVG colors change through their stable CSS identity', t => {
  const original = globalThis.document, properties = new Map();
  globalThis.document = { documentElement: { style: { setProperty: (key, value) => properties.set(key, value) } } };
  t.after(() => { installModelColors(palette()); if (original === undefined) delete globalThis.document; else globalThis.document = original; });
  const mark = modelSeriesColor('model', 'claude-opus-5-5[1m]'), token = mark.match(/^var\(([^,]+)/)[1];
  installModelColors(palette({ 'provider:anthropic': '#1234ab' }));
  assert.equal(properties.get(token), recommendedModelColor('opus-5-5', '#1234ab'));
  installModelColors(palette({ 'model:opus-5-5': '#ab1234' }));
  assert.equal(properties.get(token), '#ab1234');
  assert.ok(modelSeriesColor('model', 'opus-5-5').startsWith(`var(${token},`));
  installModelColors(palette());
  assert.equal(properties.get(token), effectiveModelColor('model', 'opus-5-5'));

  const names = [
    'anthropic.claude-3-5-sonnet-20241022-v2:0',
    `${'namespace/'.repeat(24)}model-a`, `${'namespace/'.repeat(24)}model-b`,
    'local-🚀', 'local-💡',
  ];
  installModelColors(palette(Object.fromEntries(names.map(name => [`model:${modelColorKey(name)}`, '#ab1234']))));
  const references = names.map(name => {
    const mark = chartSeriesStyle('model_family', name).color, token = mark.match(/^var\(([^,]+)/)[1];
    assert.ok(mark.length <= 256, name);
    assert.equal(properties.get(token), '#ab1234');
    for (const field of ['model_family', 'root_model_family']) {
      const theme = keyColorTheme([field], [{ keys: [name] }]);
      assert.equal(createMetricColorResolver(theme).color(field, name), mark, 'the published resolver accepts the saved color');
    }
    const pair = `${name} · ultra`, mix = chartSeriesStyle('model_effort', pair).color;
    assert.ok(mix.length <= 256, pair);
    const theme = { dimensions: { model_effort: { overrides: [{ value: pair, color: mix }] } } };
    assert.equal(createMetricColorResolver(theme).color('model_effort', pair), mix);
    return { name, token };
  });
  assert.equal(new Set(references.map(reference => reference.token)).size, names.length, 'common prefixes and Unicode remain separate identities');
  installModelColors(palette(Object.fromEntries(names.map(name => [`model:${modelColorKey(name)}`, '#2255aa']))));
  for (const { name, token } of references) {
    assert.equal(properties.get(token), '#2255aa');
    assert.ok(modelSeriesColor('model', name).startsWith(`var(${token},`));
  }

});

test('failed reads and saves retain the palette; an old read cannot undo a successful edit', async t => {
  t.after(() => installModelColors(palette()));
  installModelColors(palette({ 'provider:openai': '#123456' }));
  t.mock.method(globalThis, 'fetch', async () => ({ ok: false }));
  await assert.rejects(saveModelColor('provider', 'openai', '#abcdef'), /could not be saved/);
  await assert.rejects(loadModelColors(), /could not be loaded/);
  assert.equal(effectiveModelColor('provider', 'openai'), '#123456');
  const oldRead = Promise.withResolvers();
  t.mock.method(globalThis, 'fetch', async (_url, options) => {
    if (!options) return oldRead.promise;
    assert.deepEqual(JSON.parse(options.body), { id: 'provider:openai', color: '#abcdef' });
    return { ok: true, json: async () => palette({ 'provider:openai': '#abcdef' }) };
  });
  const pending = loadModelColors();
  assert.equal(loadModelColors(), pending);
  await saveModelColor('provider', 'openai', '#abcdef');
  oldRead.resolve({ ok: true, json: async () => palette() });
  await pending;
  assert.equal(modelPalette().overrides['provider:openai'], '#abcdef');
  const inventory = Promise.withResolvers();
  t.mock.method(globalThis, 'fetch', async (_url, options) => options
    ? { ok: true, json: async () => ({ overrides: { 'provider:openai': '#112233' } }) }
    : inventory.promise);
  const discovery = loadModelColors(true);
  await saveModelColor('provider', 'openai', '#112233');
  inventory.resolve({ ok: true, json: async () => palette({}, ['gpt-local']) });
  await discovery;
  assert.equal(modelPalette().overrides['provider:openai'], '#112233');
  assert.ok(knownModels().includes('gpt-local'), 'a save during discovery keeps the newly discovered models');

});
