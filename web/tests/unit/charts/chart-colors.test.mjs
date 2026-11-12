import { test } from 'node:test';
import assert from 'node:assert/strict';
import catalog from '../../../../pricing/model_colors.json';
import prices from '../../../../pricing/cost_changes.json';
import rules from '../../../../pricing/model_color_rules.json';
import { generateModelColors } from '../../../src/model-color-generation';
import { colorHSL, modelCatalogDate, modelClass, recommendedModelColor } from '../../../src/model-colors';
import { chartSeriesStyle, colorFields, hourOfDayColors, keyColorTheme, metricSeriesColor, modelColorKey, otherSeriesColor } from '../../../src/chart-colors';
import { orderedValues } from '../../../src/session-shape-bands';
import { foldSeries } from '../../../src/chart-series';
const bucketKey = '2026-10-01';
const fold = (names, limit = 5, selected = [], scope = 'model_family') => {
  const buckets = new Map([[bucketKey, { key: bucketKey, date: new Date(2026, 9, 1), values: {} }]]);
  const series = foldSeries(names.map((name, index) => [bucketKey, name, names.length - index]), buckets, name => name, undefined, selected, limit, scope);
  return { series, bucket: buckets.get(bucketKey) };
};
// Measure visible separation independently of the palette's HSL construction.
const perceptualColor = color => {
  const [r, g, b] = color.slice(1).match(/../g).map(channel => parseInt(channel, 16) / 255)
    .map(value => value <= .04045 ? value / 12.92 : ((value + .055) / 1.055) ** 2.4);
  const l = Math.cbrt(.4122214708 * r + .5363325363 * g + .0514459929 * b);
  const m = Math.cbrt(.2119034982 * r + .6806995451 * g + .1073969566 * b);
  const s = Math.cbrt(.0883024619 * r + .2817188376 * g + .6299787005 * b);
  return [.2104542553 * l + .793617785 * m - .0040720468 * s, 1.9779984951 * l - 2.428592205 * m + .4505937099 * s, .0259040371 * l + .7827717662 * m - .808675766 * s];
};
const colorDistance = (a, b) => { const first = perceptualColor(a), second = perceptualColor(b); return Math.hypot(...first.map((value, index) => value - second[index])); };

test('catalog covers priced models, with coordinated default colors for model versions and sources', () => {
  for (const price of prices.changes) {
    if (price.status === 'rejected') continue;
    const key = modelColorKey(price.model);
    assert.equal(catalog.models[key]?.provider, price.provider, price.model);
    assert.equal(chartSeriesStyle('model_family', key).color, recommendedModelColor(key, catalog.providers[price.provider].color));
  }
  for (const entries of [Object.values(catalog.models), Object.values(catalog.sources)]) {
    assert.equal(new Set(entries.map(entry => entry.color)).size, entries.length);
    assert.ok(entries.every(entry => /^#[0-9a-f]{6}$/.test(entry.color)));
    assert.ok(entries.every(entry => entry.color !== otherSeriesColor));
  }
  for (const model of Object.values(catalog.models)) assert.ok(catalog.class_order[model.provider].includes(model.class), `${model.provider}: ${model.class} has a reviewed position`);
});

test('catalog versions progress distinctly and contemporary core tiers stay ordered', () => {
  const {versionSteps} = generateModelColors(catalog, prices.changes, rules);
  const luminance = color => color.slice(1).match(/../g)
    .map(channel => parseInt(channel, 16) / 255)
    .map(channel => channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4)
    .reduce((total, channel, index) => total + channel * [0.2126, 0.7152, 0.0722][index], 0);
  const families = Map.groupBy(Object.keys(catalog.models), name => `${catalog.models[name].provider}:${catalog.models[name].class}`);
  for (const versions of families.values()) {
    versions.sort((a, b) => (modelCatalogDate(a) ?? '').localeCompare(modelCatalogDate(b) ?? ''));
    // Brightness alone misses alternating muted/saturated shades. A
    // chronological ramp should also take a direct path through color space.
    const dated = versions.filter((name, index) => modelCatalogDate(name)
      && modelCatalogDate(name) !== modelCatalogDate(versions[index - 1] ?? ''));
    if (dated.length > 2) {
      const colors = dated.map(name => chartSeriesStyle('model_family', name).color);
      const distance = colorDistance(colors[0], colors.at(-1));
      const travel = colors.slice(1).reduce((total, color, index) => total + colorDistance(colors[index], color), 0);
      assert.ok(travel <= distance * 1.05, `${dated.join(' → ')} should progress smoothly without hue or saturation zigzags`);
    }
    for (let index = 1; index < versions.length; index++) {
      if (!modelCatalogDate(versions[index - 1]) || modelCatalogDate(versions[index]) === modelCatalogDate(versions[index - 1])) continue;
      const older = chartSeriesStyle('model_family', versions[index - 1]);
      const newer = chartSeriesStyle('model_family', versions[index]);
      assert.ok(luminance(newer.color) < luminance(older.color), `${versions[index]} should be darker than ${versions[index - 1]}`);
      // Permit the reported finite-gamut reduction for very dense histories,
      // allowing a small tolerance for RGB rounding and the lightness proxy.
      const provider = catalog.models[versions[index]].provider;
      const minimumDistance = Math.min(provider === 'anthropic' ? .055 : .05, versionSteps[provider] * .95);
      assert.ok(colorDistance(older.color, newer.color) >= minimumDistance, `${versions[index - 1]} and ${versions[index]} need visibly different version colors`);
      assert.equal(newer.group, older.group);
    }
  }
  const coreTiers = Object.fromEntries(Object.entries(rules.providers).filter(([, rule]) => rule.ranked_classes.length).map(([provider, rule]) => [provider, rule.ranked_classes]));
  for (const [provider, tiers] of Object.entries(coreTiers)) {
    for (const tier of tiers) {
      const names = Object.entries(catalog.models).filter(([, model]) => model.provider === provider && model.class === tier).map(([name]) => name);
      if (!names.length) continue;
      // Unlisted versions stay inside their own class's overall shade range.
      const fallback = `${names.at(-1)}-future`;
      const hasFallback = modelClass(fallback) === tier;
      if (hasFallback) names.push(fallback);
      const colors = names.map(name => chartSeriesStyle('model_family', name).color);
      assert.ok(colors.every(color => colorHSL(color)[1] < (provider === 'anthropic' ? .6 : .5)), `${provider} ${tier} stays in a restrained ramp`);
      if (hasFallback) {
        const catalogLevels = names.slice(0, -1).map(name => luminance(chartSeriesStyle('model_family', name).color));
        const fallbackLevel = luminance(colors.at(-1));
        assert.ok(fallbackLevel >= Math.min(...catalogLevels) - .001 && fallbackLevel <= Math.max(...catalogLevels) + .001, `${provider} ${tier} fallback stays in its reviewed range`);
      }
    }
    // Compare the latest releases available at each catalog date. Historical
    // class ranges may overlap so versions can use a wider, clearer ramp.
    const names = Object.keys(catalog.models).filter(name => catalog.models[name].provider === provider
      && tiers.includes(catalog.models[name].class) && modelCatalogDate(name));
    for (const date of [...new Set(names.map(modelCatalogDate))].sort()) {
      const contemporaries = tiers.map(tier => {
        const available = names.filter(name => catalog.models[name].class === tier && modelCatalogDate(name) <= date);
        const latestDate = available.map(modelCatalogDate).sort().at(-1);
        return available.filter(name => modelCatalogDate(name) === latestDate);
      }).filter(versions => versions.length);
      for (let index = 1; index < contemporaries.length; index++) {
        for (const weaker of contemporaries[index - 1]) for (const stronger of contemporaries[index]) {
          const weakColor = chartSeriesStyle('model_family', weaker).color, strongColor = chartSeriesStyle('model_family', stronger).color;
          assert.ok(luminance(weakColor) > luminance(strongColor), `${date}: contemporary ${stronger} is deeper than ${weaker}`);
          assert.ok(colorDistance(weakColor, strongColor) >= .035, `${date}: contemporary ${stronger} and ${weaker} remain distinguishable`);
        }
      }
    }
  }
});

test('model normalization keeps snapshots and context variants on the same static color', () => {
  for (const name of ['claude-opus-5-5', 'OPUS-5-5', 'opus-5-5-1m', 'claude-opus-5-5[1m]', 'claude-opus-5-5-20260928', 'claude-opus-5-5-20260928[1m]']) {
    assert.equal(chartSeriesStyle('model_family', name).color, chartSeriesStyle('model_family', 'opus-5-5').color);
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

test('stacks group by provider, then catalog tier and version rather than token rank', () => {
  const names = ['gpt-6-sol', 'sonnet-5-5', 'gemini-3.8-flash', 'opus-5-5', 'opus-4-8', 'gpt-5.3-codex', 'haiku-4-5', 'fable-5-2', 'mythos-5-2', 'gpt-6-luna', 'gpt-6.1-terra', 'gpt-6-astra'];
  const { series, bucket } = fold(names, names.length);
  assert.deepEqual(series.map(item => item.value), ['haiku-4-5', 'sonnet-5-5', 'opus-4-8', 'opus-5-5', 'fable-5-2', 'mythos-5-2', 'gemini-3.8-flash', 'gpt-6-luna', 'gpt-6.1-terra', 'gpt-6-sol', 'gpt-6-astra', 'gpt-5.3-codex']);
  assert.deepEqual(series.map(item => item.group), [...Array(6).fill('Anthropic'), 'Google', ...Array(5).fill('OpenAI')]);
  assert.equal(Object.values(bucket.values).reduce((total, value) => total + value, 0), 78);
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

test('a grouping by several keys colours each bar by its palette key, an event kind before a repository', () => {
  const buckets = [{ keys: ['alpha', 'env_friction'] }, { keys: ['beta', 'env_friction'] }, { keys: ['beta', 'stale_guidance'] }];
  const theme = keyColorTheme(['repository_name', 'family'], buckets);
  assert.equal(theme.colorBy, 'family');
  assert.deepEqual(theme.dimensions.family.overrides, ['env_friction', 'stale_guidance'].map(value => ({ value, color: chartSeriesStyle('family', value).color })));
  // The compact covers pick the same key.
  assert.equal(metricSeriesColor(['repository_name', 'family'], ['beta', 'env_friction']), chartSeriesStyle('family', 'env_friction').color);
  assert.equal(keyColorTheme(['repository_name', 'model_family'], [{ keys: ['repo', 'opus-5-5'] }]).colorBy, 'model_family');
  assert.equal(keyColorTheme(['family'], buckets), undefined, 'event kinds keep their own colours');
  assert.equal(keyColorTheme(['tool_name', 'agent_session_id'], buckets), undefined, 'no palette key keeps whole-group colours');
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

test('hours of the day take a distinct sky colour, whatever form their key takes', () => {
  assert.equal(new Set(hourOfDayColors).size, 24);
  for (const [hour, key] of [[7, '07:00'], [7, '7'], [21, '21']]) assert.equal(chartSeriesStyle('hour_of_day', key).color, hourOfDayColors[hour]);
  assert.equal(chartSeriesStyle('hour_of_day', 'Unspecified').color, otherSeriesColor);
  // An hour-of-day date grouping colors its timestamp field by hour.
  const fields = colorFields({ groupBy: ['started_at'], dateGrouping: { interval: 'hourOfDay' } });
  assert.equal(metricSeriesColor(fields, [5]), hourOfDayColors[5]);
  assert.deepEqual(colorFields({ groupBy: ['started_at'], dateGrouping: { interval: 'day' } }), ['started_at']);
});

test('session bands and archetypes take ordered steps of one ramp, by value or label', () => {
  for (const [field, ordered] of Object.entries(orderedValues)) {
    const colors = ordered.order.map(value => chartSeriesStyle(field, value).color);
    assert.equal(new Set(colors).size, colors.length, `${field}: a colour per step`);
    for (const value of ordered.order) assert.deepEqual(chartSeriesStyle(field, ordered.labels[value]), chartSeriesStyle(field, value), `${field} ${value}`);
    assert.deepEqual(ordered.order.map(value => Number(chartSeriesStyle(field, value).order)), ordered.order.map((_, index) => index), field);
    assert.ok(!colors.includes(otherSeriesColor), field);
  }
  // Each ramp darkens as its values rise.
  const lightness = color => { const [r, g, b] = [1, 3, 5].map(at => Number.parseInt(color.slice(at, at + 2), 16)); return 0.2126 * r + 0.7152 * g + 0.0722 * b; };
  const bands = orderedValues.length_band.order.map(value => lightness(chartSeriesStyle('length_band', value).color));
  assert.ok(bands.every((value, index) => !index || value < bands[index - 1]));
  assert.equal(chartSeriesStyle('archetype', 'Something else').color, otherSeriesColor);
  // Legends and stacks follow the order, not the totals: the biggest band
  // stays in its place.
  assert.deepEqual(fold(['121+', '1-5', '31-60'], 5, [], 'length_band').series.map(item => item.value), ['1-5', '31-60', '121+']);
  assert.deepEqual(fold(['Marathon', 'Automated', 'Quick'], 5, [], 'archetype').series.map(item => item.value), ['Automated', 'Quick', 'Marathon']);
});
