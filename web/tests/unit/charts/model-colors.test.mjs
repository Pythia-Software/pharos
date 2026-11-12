import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createMetricColorResolver } from '@pythia-software/query-table-ui';
import catalog from '../../../../pricing/model_colors.json';
import rules from '../../../../pricing/model_color_rules.json';
import prices from '../../../../pricing/cost_changes.json';
import { generateModelColors, generatedColorLuminance } from '../../../src/model-color-generation';
import { colorForeground, colorLuminance } from '../../../src/model-color-math';
import { modelCatalogDates, modelTimelineDates } from '../../../src/model-color-identity';
import { colorHSL, compareModelColors, customModelColor, effectiveModelColor, installModelColors, knownModels, modelClass, modelColorKey, modelPaletteComparison, modelPaletteGroups, modelProvider, loadModelColors, modelPalette, modelSeriesColor, previewModelColor, providerColorWarnings, recommendedModelColor, saveModelColor } from '../../../src/model-colors';
import { chartMetricTheme, chartSeriesStyle, keyColorTheme, metricSeriesColor } from '../../../src/chart-colors';
const palette = (overrides = {}, models = []) => ({ overrides, models, sources: [] });

test('comparison rows appear only for actual proposed model-color changes', t => {
  t.after(() => installModelColors(palette()));
  installModelColors(palette());
  const names = ['gpt-6-sol', 'gpt-6-luna'], base = catalog.providers.openai.color;
  assert.equal(modelPaletteComparison(names, base).changed, false);
  assert.equal(modelPaletteComparison(names, '#7547b3').changed, true);
  assert.equal(modelPaletteComparison(names, base, {'model:gpt-6-sol':effectiveModelColor('model', 'gpt-6-sol')}).changed, false);
  assert.equal(modelPaletteComparison(names, base, {'model:gpt-6-sol':'#abcdef'}).changed, true);
  installModelColors(palette({'model:gpt-6-sol':'#abcdef'}));
  assert.equal(modelPaletteComparison(['gpt-6-sol'], '#7547b3').changed, false, 'a provider edit cannot alter an explicit model choice');
});

test('palette quality warnings inspect actual version separation and extreme bases', t => {
  t.after(() => installModelColors(palette()));
  installModelColors(palette());
  for (const provider of ['anthropic', 'google', 'openai']) {
    const names = Object.keys(catalog.models).filter(name => catalog.models[name].provider === provider);
    const base = catalog.providers[provider].color;
    assert.deepEqual(providerColorWarnings(provider, base, modelPaletteComparison(names, base).entries), [], `${provider} defaults are usable`);
    assert.ok(providerColorWarnings(provider, '#ffffff', modelPaletteComparison(names, '#ffffff').entries).some(warning => /very light/.test(warning)));
    assert.ok(providerColorWarnings(provider, '#000000', modelPaletteComparison(names, '#000000').entries).some(warning => /very dark/.test(warning)));
  }
  const entries = ['sonnet-4', 'sonnet-4-5'].map(name => ({name, saved:'#112233', preview:'#112233'}));
  assert.ok(providerColorWarnings('anthropic', catalog.providers.anthropic.color, entries).some(warning => /hard to distinguish/.test(warning)));
  const siblings = ['fable-5-2', 'mythos-5-2', 'haiku-undated'].map(name => ({name, saved:'#112233', preview:'#112233'}));
  assert.deepEqual(providerColorWarnings('anthropic', catalog.providers.anthropic.color, siblings), [], 'same-day siblings and undated aliases do not imply failed version separation');
});

test('live palette previews preserve saved colors and explicit model choices until save', t => {
  t.after(() => installModelColors(palette()));
  installModelColors(palette({'model:gpt-6-sol':'#ee1122'}));
  const savedPalette = structuredClone(modelPalette()), saved = effectiveModelColor('model', 'gpt-6-luna');
  const preview = previewModelColor('gpt-6-luna', '#7547b3');
  assert.notEqual(preview, saved);
  assert.equal(preview, recommendedModelColor('gpt-6-luna', '#7547b3'));
  assert.equal(effectiveModelColor('model', 'gpt-6-luna'), saved);
  assert.equal(previewModelColor('claude-opus-5-5', '#7547b3', '#123456'), '#123456');
  assert.equal(previewModelColor('gpt-6-sol', '#7547b3'), '#ee1122');
  assert.equal(previewModelColor('gpt-6-sol', '#7547b3', '#abcdef'), '#abcdef');
  assert.deepEqual(modelPalette(), savedPalette);
});

test('palette banners group classes and dates, separating Fable and Mythos presentation', () => {
  const names = ['mythos-5-2', 'sonnet-5-5', 'opus-5-5', 'haiku', 'sonnet-4', 'fable-5-2', 'haiku-4-5', 'fable-5', '3-5-haiku', 'unclassified-model'];
  const groups = modelPaletteGroups(names);
  assert.deepEqual(groups.map(group => group.name), ['Haiku', 'Sonnet', 'Opus', 'Fable', 'Mythos', 'Other models']);
  assert.deepEqual(groups[0].models, ['3-5-haiku', 'haiku-4-5', 'haiku']);
  assert.deepEqual(groups[1].models, ['sonnet-4', 'sonnet-5-5']);
  assert.deepEqual(groups[3].models, ['fable-5', 'fable-5-2']);
  assert.equal(modelClass('fable-5-2'), modelClass('mythos-5-2'), 'shared color policy stays intact');
  assert.deepEqual(new Set(groups.flatMap(group => group.models)), new Set(names), 'every model has one labeled swatch');
  assert.deepEqual(modelPaletteGroups([...names].reverse()), groups);
});

test('palette generation is reproducible, ignores previous colors and leaves inputs untouched', () => {
  const before = structuredClone(catalog), priceBefore = structuredClone(prices), ruleBefore = structuredClone(rules);
  const generated = generateModelColors(catalog, prices.changes, rules);
  assert.deepEqual(generated.catalog, catalog, 'the committed palette matches its rules and inventory');
  assert.deepEqual(generated.warnings, []);
  const recolored = structuredClone(catalog);
  for (const model of Object.values(recolored.models)) model.color = '#ff00ff';
  assert.deepEqual(generateModelColors(recolored, [...prices.changes].reverse(), rules), generated);
  assert.deepEqual(generateModelColors(generated.catalog, prices.changes, rules), generated, 'regeneration is idempotent');
  assert.deepEqual(catalog, before);
  assert.deepEqual(prices, priceBefore);
  assert.deepEqual(rules, ruleBefore);
});

const release = (model, date, provider = 'anthropic') => ({model, provider, effective_from: date, status: 'proposed'});
function assertGeneratedOrder(generated, changes) {
  const dates = modelTimelineDates(changes, rules.observed_dates), entries = Object.entries(generated.models);
  for (const [provider, rule] of Object.entries(rules.providers)) {
    const names = entries.filter(([name, model]) => model.provider === provider && dates.has(name));
    for (const family of Object.keys(rule.classes)) {
      const versions = names.filter(([, model]) => model.class === family).sort(([a], [b]) => dates.get(a).localeCompare(dates.get(b)));
      for (let i = 1; i < versions.length; i++) if (dates.get(versions[i - 1][0]) !== dates.get(versions[i][0])) {
        assert.ok(generatedColorLuminance(versions[i - 1][1].color) > generatedColorLuminance(versions[i][1].color), `${family} darkens over time`);
      }
    }
    for (const date of [...new Set(names.map(([name]) => dates.get(name)))]) {
      const latest = rule.ranked_classes.map(family => names.filter(([name, model]) => model.class === family && dates.get(name) <= date)
        .sort(([a], [b]) => dates.get(a).localeCompare(dates.get(b))).at(-1)).filter(Boolean);
      for (let i = 1; i < latest.length; i++) assert.ok(generatedColorLuminance(latest[i - 1][1].color) > generatedColorLuminance(latest[i][1].color), `${date}: contemporary core tiers stay ordered`);
    }
  }
  assert.equal(new Set(entries.map(([, model]) => model.color)).size, entries.length);
}

test('new releases are classified and rebalance older defaults while retaining temporal class order', () => {
  const changes = [...prices.changes, release('claude-sonnet-5-6', '2026-12-01'), release('claude-opus-5-6', '2026-12-05'), release('claude-fable-5-3', '2026-12-09'), release('gpt-6.2-sol', '2026-12-10', 'openai'), release('gemini-omni-1.3-flash', '2026-12-11', 'google')];
  const {catalog: next} = generateModelColors(catalog, changes, rules);
  assert.equal(next.models['sonnet-5-6'].class, 'Sonnet');
  assert.equal(next.models['fable-5-3'].class, 'Fable / Mythos');
  assert.equal(next.models['gpt-6.2-sol'].class, 'Sol');
  assert.equal(next.models['gemini-omni-1.3-flash'].class, 'Flash');
  assert.notEqual(next.models['sonnet-5-5'].color, catalog.models['sonnet-5-5'].color);
  assert.notEqual(next.models['fable-5-2'].color, catalog.models['fable-5-2'].color);
  assertGeneratedOrder(next, changes);
});

test('backfilled pricing, rejected releases, same-day siblings and undated aliases have deliberate behavior', () => {
  const changes = [...prices.changes, release('claude-3-5-sonnet', '2024-10-22'), release('claude-opus-5-5-new', '2026-09-22'), {...release('unclassified-rejected', 'bad-date'), status: 'rejected'}];
  const {catalog: next} = generateModelColors(catalog, changes, rules);
  assert.ok(next.models['3-5-sonnet']);
  assert.ok(!Object.hasOwn(next.models, 'unclassified-rejected'));
  assert.notEqual(next.models['opus-5-5-new'].color, next.models['opus-5-5'].color);
  assert.ok(!modelCatalogDates(changes).has('sonnet'), 'an undated alias does not invent a release date');
  assertGeneratedOrder(next, changes);
});

test('observed short labels follow their evidenced dates without becoming pricing aliases', () => {
  const dates = modelTimelineDates(prices.changes, rules.observed_dates);
  const {catalog: next} = generateModelColors(catalog, prices.changes, rules);
  for (const [label, before, after] of [['haiku', 'haiku-4-5', 'haiku-5-5'], ['sonnet', 'sonnet-4', 'sonnet-4-5'], ['opus', 'opus-4-1', 'opus-4-5']]) {
    assert.ok(dates.get(before) < dates.get(label) && dates.get(label) < dates.get(after));
    assert.ok(generatedColorLuminance(next.models[before].color) > generatedColorLuminance(next.models[label].color));
    assert.ok(generatedColorLuminance(next.models[label].color) > generatedColorLuminance(next.models[after].color));
    assert.ok(compareModelColors(before, label) < 0 && compareModelColors(label, after) < 0);
    assert.equal(modelCatalogDates(prices.changes).has(label), false);
  }
  assert.throws(() => modelTimelineDates([], {sonnet: '2026-02-30'}), /Invalid observed model date/);
  assert.equal(modelTimelineDates([release('sonnet', '2025-01-01')], rules.observed_dates).get('sonnet'), '2025-01-01', 'reviewed pricing dates take precedence');
});

test('specialist OpenAI families use distinct hues outside the mainline green spectrum', t => {
  t.after(() => installModelColors(palette())); installModelColors(palette());
  const hueGap = (a, b) => Math.min(Math.abs(a - b), 360 - Math.abs(a - b));
  const core = Object.entries(catalog.models).filter(([,model]) => model.provider === 'openai' && rules.providers.openai.ranked_classes.includes(model.class));
  for (const [name, model] of Object.entries(catalog.models).filter(([,model]) => model.provider === 'openai' && !rules.providers.openai.ranked_classes.includes(model.class))) {
    for (const [,main] of core) assert.ok(hueGap(colorHSL(model.color)[0], colorHSL(main.color)[0]) > 20, `${name} has a distinct specialist hue`);
  }
  for (const name of ['gpt-image-2.6-flare', 'gpt-6-cyber', 'gpt-rosalind-research']) {
    const provider = '#7547b3';
    assert.notEqual(previewModelColor(name, provider), effectiveModelColor('model', name), 'provider customization still shifts specialist colors');
  }
});

test('independent developer palettes classify existing and future models without rewriting pricing identities', t => {
  t.after(() => installModelColors(palette())); installModelColors(palette());
  for (const [name, provider] of [['reflection/beam-free','reflection'], ['deepseek-v4.1-flash-free','deepseek'], ['big-pickle','opencode'], ['mai-code-1.1-flash','microsoft'], ['grok-4.7','xai'], ['composer-2.5','cursor']]) {
    assert.equal(modelProvider(name), provider);
    const before = effectiveModelColor('model', name);
    installModelColors(palette({[`provider:${provider}`]:'#225599'}));
    assert.notEqual(effectiveModelColor('model', name), before);
    assert.equal(effectiveModelColor('model', 'sonnet-4-5'), catalog.models['sonnet-4-5'].color);
    installModelColors(palette());
  }
  const inventory = structuredClone(catalog); delete inventory.models['reflection/beam-free'];
  const next = generateModelColors(inventory, [...prices.changes, release('deepseek-v5-flash', '2027-01-01', 'other')], rules).catalog;
  assert.equal(next.models['reflection/beam-free'].provider, 'reflection');
  assert.equal(next.models['deepseek-v5-flash'].provider, 'deepseek');
  assert.equal(prices.changes.find(row => row.model === 'reflection/beam-free').provider, 'other');
  assert.equal(modelProvider('unidentified-local-model'), 'other');
});

test('new model classes and invalid metadata require explicit rules rather than guessed rankings', () => {
  assert.throws(() => generateModelColors(catalog, [...prices.changes, release('unclassified-new-model', '2027-01-01')], rules), /Define a model color class\/rule/);
  assert.throws(() => generateModelColors(catalog, [...prices.changes, release('sonnet-6', '2027-02-30')], rules), /Invalid catalog date/);
  assert.throws(() => generateModelColors(catalog, [...prices.changes, release('sonnet-5-5', '2027-01-01', 'google')], rules), /Conflicting provider/);
  assert.throws(() => generateModelColors(catalog, prices.changes, {...rules, minimum_version_step: -1}), /Invalid model color spacing/);
  const extended = structuredClone(rules), inventory = structuredClone(catalog);
  extended.providers.anthropic.classes.Legend = {luminance: [.05, .02], match: '^legend(?:-|$)'};
  extended.providers.anthropic.ranked_classes.push('Legend');
  inventory.models['legend-1'] = {provider: 'anthropic', class: 'Legend', color: ''};
  const next = generateModelColors(inventory, [...prices.changes, release('legend-1', '2027-01-01'), release('legend-2', '2027-02-01')], extended).catalog;
  assert.ok(next.class_order.anthropic.includes('Legend'));
  assert.match(next.models['legend-1'].color, /^#[0-9a-f]{6}$/);
  assert.equal(next.models['legend-2'].class, 'Legend', 'future releases use a data-defined class matcher');
  extended.providers.anthropic.classes.Duplicate = {luminance: [.1, .05], match: '^legend(?:-|$)'};
  assert.throws(() => generateModelColors(inventory, [release('legend-2', '2027-02-01')], extended), /Ambiguous model color class/);
});

test('dense release histories report finite-gamut crowding and still produce an ordered usable palette', () => {
  const inventory = {version: 1, providers: {anthropic: catalog.providers.anthropic}, class_order: {}, models: {}, sources: {}};
  const denseRules = {...rules, providers: {anthropic: {...rules.providers.anthropic, ranked_classes: ['Opus'], classes: {Opus: rules.providers.anthropic.classes.Opus}}}};
  const changes = Array.from({length: 24}, (_, index) => release(`opus-${index + 1}`, `2027-01-${String(index + 1).padStart(2, '0')}`));
  const {catalog: next, warnings, versionSteps} = generateModelColors(inventory, changes, denseRules);
  assert.ok(warnings.some(warning => warning.includes('dense release history')));
  assert.ok(versionSteps.anthropic < denseRules.minimum_version_step);
  const colors = changes.map(row => next.models[row.model].color);
  assert.equal(new Set(colors).size, changes.length);
  assert.ok(colors.every(color => /^#[0-9a-f]{6}$/.test(color)));
  for (let i = 1; i < colors.length; i++) assert.ok(generatedColorLuminance(colors[i - 1]) > generatedColorLuminance(colors[i]));
});

test('provider color families contain distinct class ranges and closely related version hues', t => {
  t.after(() => installModelColors(palette()));
  installModelColors(palette());
  const color = name => effectiveModelColor('model', name);
  const hueDistance = (a, b) => { const delta = Math.abs(colorHSL(a)[0] - colorHSL(b)[0]); return Math.min(delta, 360 - delta); };
  const rgbDistance = (a, b) => Math.hypot(...[1, 3, 5].map(i => parseInt(a.slice(i, i + 2), 16) - parseInt(b.slice(i, i + 2), 16)));
  for (const [older, newer] of [['opus-4-1', 'opus-4-5'], ['sonnet-4', 'sonnet-4-5'], ['gpt-5.3-codex', 'gpt-5.3-codex-spark'], ['gpt-5.6-luna', 'gpt-6-luna'], ['gpt-5.6-terra', 'gpt-6.1-terra'], ['gpt-5.6-sol', 'gpt-6.1-sol']]) {
    assert.ok(hueDistance(color(older), color(newer)) < 8, `${older} and ${newer} remain in the same class range`);
    assert.ok(rgbDistance(color(older), color(newer)) >= 15, `${older} and ${newer} are distinguishable`);
  }
  for (const [name, model] of Object.entries(catalog.models)) {
    if (model.provider === 'openai' && !rules.providers.openai.ranked_classes.includes(model.class)) continue;
    assert.ok(hueDistance(color(name), catalog.providers[model.provider].color) < 40, `${name} stays in its provider color family`);
  }
  for (const name of ['3-5-sonnet', 'opus-future', 'haiku-future', 'gemini-2.5-pro', 'o3-mini']) {
    assert.ok(hueDistance(color(name), catalog.providers[modelProvider(name)].color) < 40, `${name} inherits its provider color family`);
  }
  assert.equal(modelProvider('claude-3-5-sonnet-20241022'), 'anthropic');
  assert.equal(modelClass('claude-3-5-sonnet-20241022'), 'Sonnet');
  assert.equal(modelClass('opus-future'), 'Opus');
  assert.equal(modelClass('gemini-2.5-pro'), 'Pro');
  assert.equal(modelClass('o3-mini'), 'Reasoning');
  assert.equal(modelClass('mythos-future'), modelClass('fable-future'));
  assert.equal(modelClass('mythos-5-2'), modelClass('fable-5-2'));
  assert.ok(hueDistance(color('mythos-5-2'), color('fable-5-2')) < 4, 'Fable and Mythos share the Fable range');
  assert.equal(modelClass('gpt-7-sol'), 'Sol');
  assert.ok(hueDistance(color('gpt-7-sol'), color('gpt-6-sol')) < 4, 'unlisted versions follow their catalog tier');
  const ordered = ['opus-5-5', 'mythos-5-2', 'gpt-6-sol', 'gpt-6-luna', 'sonnet-5-5', 'gpt-6-astra', 'haiku-4-5', 'gpt-6.1-terra'].sort(compareModelColors);
  assert.deepEqual(ordered.map(modelClass), ['Haiku', 'Sonnet', 'Opus', 'Fable / Mythos', 'Luna', 'Terra', 'Sol', 'Astra']);
  const opus = color('opus-4-5'), sonnet = color('sonnet-4-5');
  installModelColors(palette({ 'provider:anthropic': '#339966', 'model:haiku-4-5': '#112233' }));
  assert.notEqual(color('opus-4-5'), opus);
  assert.notEqual(color('opus-4-5'), color('sonnet-4-5'));
  assert.ok(hueDistance(color('opus-4-5'), '#339966') < 20, 'provider customization keeps the whole family together');
  assert.equal(color('haiku-4-5'), '#112233');
  installModelColors(palette());
  assert.equal(color('opus-4-5'), opus);
  assert.equal(color('sonnet-4-5'), sonnet);
});

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
  for (const base of ['#000000', '#ffffff', '#ff0000', '#00ff00', '#0000ff', '#2255aa']) {
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

test('picker text and edit icons retain readable contrast on palette and custom colors', () => {
  for (const color of [...Object.values(catalog.models).map(model => model.color), '#000000', '#ffffff', '#ff0000', '#00ff00', '#0000ff', '#777777']) {
    const levels = [colorLuminance(color), colorLuminance(colorForeground(color))].sort((a, b) => a - b);
    assert.ok((levels[1] + .05) / (levels[0] + .05) >= 4.5, color);
  }
});
