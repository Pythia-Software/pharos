import { test } from 'node:test';
import assert from 'node:assert/strict';
import { recommendedModelColor } from '../../../src/model-colors';
import { algorithms, brands, defaults, experimentColor, fromLCH, lch, models, palette, position, providers, roots, specialistHueOffset, specialistModels, workload } from '../../../experiments/model-color-lab/palette';

const model = name => { const value = models.find(item => item.name === name); assert.ok(value, name); return value; };
const chroma = (name, options = defaults, base = brands[model(name).provider].color) => lch(experimentColor(model(name), base, options))[1];

test('color lab baseline uses the production resolver for every model and root', () => {
  for (const base of ['#d97732', '#8951c8', '#777777']) for (const item of models) {
    assert.equal(experimentColor(item, base, { ...defaults, algorithm: 'current' }), recommendedModelColor(item.name, base));
  }
});

test('color lab ranks real dated cohorts without inventing a rank for specialists or aliases', () => {
  assert.equal(position(model('gpt-6-astra')).tier, 1);
  assert.equal(position(model('haiku-5-5')).tier, 0);
  assert.equal(position(model('gpt-image-2.6-flare')).ranked, false);
  assert.equal(position({ ...model('opus-5-5'), date: undefined }).recency, .5);
  assert.equal(position(model('fable-5-2')).recency, position(model('mythos-5-2')).recency);
  assert.notEqual(experimentColor(model('fable-5-2'), brands.anthropic.color, defaults), experimentColor(model('mythos-5-2'), brands.anthropic.color, defaults), 'same-date siblings retain separate colors without a release ordering');
  for (const item of models) {
    assert.ok(position(item).tier >= 0 && position(item).tier <= 1);
    assert.ok(position(item).recency >= 0 && position(item).recency <= 1);
  }
});

test('chroma ladder rewards newer releases within a class and the strongest contemporary tier', () => {
  const ladder = { ...defaults, algorithm: 'chroma' };
  assert.ok(chroma('opus-5-5', ladder) > chroma('opus-4-1', ladder));
  assert.ok(chroma('gpt-6.1-sol', ladder) > chroma('gpt-5.6-sol', ladder));
  assert.ok(chroma('fable-5-2', ladder) > chroma('haiku-5-5', ladder));
  assert.ok(chroma('gpt-6-astra', ladder) > chroma('gpt-6-luna', ladder));

});

test('freshness policy ignores tier emphasis and hue fan responds to spread', () => {
  const item = model('opus-5-5');
  assert.equal(experimentColor(item, '#d97732', { ...defaults, algorithm: 'fresh', tierWeight: 0 }), experimentColor(item, '#d97732', { ...defaults, algorithm: 'fresh', tierWeight: 1 }));
  assert.notEqual(experimentColor(item, '#d97732', { ...defaults, algorithm: 'fan', spread: 0 }), experimentColor(item, '#d97732', { ...defaults, algorithm: 'fan', spread: 80 }));
});

test('root edits propagate only to that provider, and neutral roots stay neutral', () => {
  const original = roots(), edited = { ...original, anthropic: '#a13fc9' };
  const first = palette(original, defaults), second = palette(edited, defaults);
  for (const item of models) assert.equal(first.get(item.name) === second.get(item.name), item.provider !== 'anthropic');
  for (const algorithm of algorithms.filter(item => item.id !== 'current')) for (const base of ['#000000', '#777777', '#ffffff']) {
    for (const item of models) assert.ok(lch(experimentColor(item, base, { ...defaults, algorithm: algorithm.id }))[1] < .002, `${base}: ${item.name}`);
  }
  assert.deepEqual(original, roots(), 'root drafts never mutate the catalog');
});

test('experimental colors remain finite sRGB colors across roots and control extremes', () => {
  for (const algorithm of algorithms) for (const base of ['#000000', '#ffffff', '#ff0000', '#00ff00', '#0000ff', '#ffff00']) for (const vividness of [0, 1.2]) for (const lightness of [.5, .78]) {
    for (const item of models) {
      const color = experimentColor(item, base, { ...defaults, algorithm: algorithm.id, vividness, lightness, spread: 100, specialistSpread: 300 });
      assert.match(color, /^#[0-9a-f]{6}$/);
      assert.ok(lch(color).every(Number.isFinite));
    }
  }
  for (const base of Object.values(roots())) {
    const [L, C, H] = lch(base);
    assert.equal(fromLCH(L, C, H), base);
  }
});

test('synthetic workloads use catalog identities, positive weights, and deterministic color-independent data', () => {
  assert.deepEqual(new Set(workload('balanced').map(entry => entry.model.provider)), new Set(['anthropic', 'google', 'openai']));
  for (const kind of ['frontier', 'balanced', 'history', 'specialists']) for (const provider of [undefined, ...providers]) {
    const entries = workload(kind, provider);
    assert.ok(entries.length <= 8);
    assert.equal(new Set(entries.map(entry => entry.model.name)).size, entries.length);
    for (const { model: item, weight } of entries) {
      assert.ok(models.includes(item));
      assert.ok(Number.isFinite(weight) && weight > 0);
      if (provider) assert.equal(item.provider, provider);
      if (kind === 'specialists') assert.ok(specialistModels(provider).includes(item));
    }
    assert.deepEqual(entries, workload(kind, provider));
  }
});

test('specialist spread changes specialist categories without changing core tiers or standalone developers', () => {
  const narrow = { ...defaults, specialistSpread: 100 }, wide = { ...defaults, specialistSpread: 240 };
  const first = palette(roots(), narrow), second = palette(roots(), wide);
  const specialists = new Set(specialistModels());
  for (const item of models) assert.equal(first.get(item.name) === second.get(item.name), !specialists.has(item), item.name);
  assert.equal(specialistModels('reflection').length, 0, 'a standalone developer is not a specialist of another provider');
  assert.equal(specialistModels('anthropic').length, 0);
  assert.ok(specialistModels('google').some(item => item.class === 'Nano Banana'));
  for (const item of specialistModels()) {
    assert.equal(specialistHueOffset(item, { ...wide, specialistHues: false }), 0);
    assert.equal(specialistHueOffset(item, { ...wide, specialistSpread: 0 }), 0);
    assert.equal(specialistHueOffset(item, { ...wide, specialistMode: 'catalog' }), specialistHueOffset(item, { ...narrow, specialistMode: 'catalog' }));
    assert.equal(specialistHueOffset(item, wide), specialistHueOffset(item, { ...wide, spread: 90 }), 'tier spread does not change specialist offsets');
  }
  const hues = [...new Set(specialistModels('openai').map(item => specialistHueOffset(item, wide)))];
  assert.ok(Math.abs(Math.max(...hues) - Math.min(...hues) - wide.specialistSpread) < 1e-9, 'the control describes the actual span of the authored specialist offsets');
  const entries = workload('specialists', 'openai');
  assert.equal(new Set(entries.map(entry => entry.model.class)).size, entries.length);
  assert.deepEqual(new Set(entries.map(entry => entry.model.class)), new Set(specialistModels('openai').map(item => item.class)));
});


test('production generation and custom roots match the chosen Hue fan preset across the complete catalog', () => {
  for (const item of models) for (const base of [brands[item.provider].color, '#8951c8', '#777777', '#000000', '#ffffff']) {
    assert.equal(recommendedModelColor(item.name, base), experimentColor(item, base, defaults), `${item.name}: ${base}`);
  }
});
