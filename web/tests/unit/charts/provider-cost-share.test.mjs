import assert from 'node:assert/strict';
import test from 'node:test';
import { core, schemas } from '../helpers/recipes.mjs';
import { queryRecipes, instantiateRecipe, loadQueryPreview, clearPreviewCache } from '../../../src/curiosity-data';
import { rollingAverageBuckets, shareBuckets, foldSeries } from '../../../src/chart-series';
const schema = schemas.usage;
const recipe = queryRecipes.find(recipe => recipe.id === 'provider-cost-share');
const execution = { profile: 'qt-sqlite-v1', planToken: '', fields: {}, resolvedRevisions: {} };

function fixture(t, mutate = result => result) {
  const originalFetch = global.fetch, originalLocation = global.location, requests = [];
  global.location = { origin: 'http://preview.test' };
  clearPreviewCache();
  global.fetch = async (url, init = {}) => {
    const request = init.body && JSON.parse(init.body);
    const json = body => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
    if (url.includes('computed-columns')) return json([]);
    if (url.endsWith('capabilities')) return json({ computedExecution: execution, metricCapabilities: { version: 2 }, queryCapabilities: { version: 3, profile: execution.profile, groupedRows: true, calendarGrouping: true, maxRows: 10000, maxGroups: 10000, maxPopulation: 250000 } });
    requests.push(request);
    const now = request.now;
    const fact = (id, daysAgo, provider, cost_usd, price_status = 'priced') => {
      const at = new Date(now - daysAgo * 86400000 - 1000);
      return { id, first_usage_at: at.getTime(), day: `${at.getFullYear()}-${String(at.getMonth() + 1).padStart(2, '0')}-${String(at.getDate()).padStart(2, '0')}`, provider, model_family: provider === 'claude' ? 'opus-4-6' : 'gpt-5.3-codex', cost_usd, price_status, total_tokens: 100, cache_read_input_tokens: 90 };
    };
    const rows = [fact('a', 1, 'claude', 90), fact('b', 1, 'codex', 10, 'assumed'), fact('c', 0, 'codex', 900, 'partial'), fact('unknown', 0, 'codex', null, 'unpriced'), fact('negative', 0, 'codex', -100), fact('old', 91, 'claude', 99999), fact('future', -1, 'claude', 99999)];
    const query = { ...core.EMPTY_QUERY, ...request.result, select: request.result.select.map(field => ({ field })), aggregations: request.metrics,
      grouping: { ...request.grouping, source: { ...core.EMPTY_QUERY, ...request.source, select: request.source.select.map(field => ({ field })) } } };
    const prepared = core.prepareGrouping(rows, schema, query.grouping, { now });
    const result = core.applyQuery(rows, query, schema, { now });
    return json(mutate({ version: 3, now, sourceTotal: prepared.inputs.length, rows: request.rows ? result.rows : [], total: result.total,
      metrics: core.evaluateMetrics(rows, query, schema, { now, shownRows: result.rows }).metrics,
      schema: { name: prepared.schema.name, idField: prepared.schema.idField, fields: prepared.schema.fields.map(({ name, type }) => ({ name, type })) }, execution }));
  };
  t.after(() => { global.fetch = originalFetch; global.location = originalLocation; clearPreviewCache(); });
  return requests;
}

test('90-day provider cost shares smooth amounts before normalizing, matching Machine Tokens pricing', async t => {
  const requests = fixture(t), item = instantiateRecipe(recipe, recipe.days[0]);
  const preview = await loadQueryPreview('usage', item.query, schema, { recipe: item, cover: item.cover, window: item.window, url: item.url });
  assert.equal(preview.state, 'ready');
  assert.equal(item.window.days, 90);
  assert.equal(new URL(item.url, location.origin).searchParams.get('window_days'), '90');
  assert.deepEqual(item.query.grouping.keys, ['day', 'provider']);
  assert.deepEqual(preview.timeline, { rollingPeriods: 7, percent: true });
  assert.equal(preview.buckets.length, 91, 'elapsed 90 days touches both partial boundary dates');
  assert.equal(preview.total, 1000, 'include known assumed/partial costs, exclude missing, negative and out-of-window cost');
  assert.equal(preview.headlineMetric, undefined);
  assert.equal(requests.length, 1);
  const averages = rollingAverageBuckets(preview.buckets, preview.timeline.rollingPeriods);
  const shares = shareBuckets(averages);
  assert.ok(Math.abs(shares.at(-1).values['s:claude'] - .09) < 1e-12);
  assert.ok(Math.abs(shares.at(-1).values['s:codex'] - .91) < 1e-12);
  assert.equal(averages.at(-1).windowSize, 7);
  assert.deepEqual(shares[0].values, {}, 'zero-activity windows have no shares');
  assert.equal(preview.series.find(series => series.value === 'claude').color, '#d97732');
});

test('partial daily totals cannot silently become a percentage denominator', async t => {
  fixture(t, result => ({ ...result, metrics: result.metrics.map(metric => ({ ...metric, coverage: 'partial' })) }));
  const item = instantiateRecipe(recipe, 90);
  await assert.rejects(loadQueryPreview('usage', item.query, schema, { recipe: item, cover: item.cover, window: item.window }), /Complete daily totals/);
});

test('model timelines use different token and cost denominators with the same smoothing and model colors', async t => {
  fixture(t);
  for (const [id, total, claudeShare, codexShare] of [['model-token-share-trend', 500, .2, .8], ['model-cost-share-trend', 1000, .09, .91]]) {
    const modelRecipe = queryRecipes.find(recipe => recipe.id === id), item = instantiateRecipe(modelRecipe, modelRecipe.days[0]);
    const preview = await loadQueryPreview('usage', item.query, schema, { recipe: item, cover: item.cover, window: item.window, url: item.url });
    assert.equal(item.window.days, 90);
    assert.deepEqual(item.query.grouping.keys, ['day', 'model_family']);
    assert.deepEqual(preview.timeline, { rollingPeriods: 7, percent: true });
    assert.equal(preview.state, 'ready');
    assert.equal(preview.total, total, id);
    const values = shareBuckets(rollingAverageBuckets(preview.buckets, 7)).at(-1).values;
    assert.ok(Math.abs(values['s:opus-4-6'] - claudeShare) < 1e-12, id);
    assert.ok(Math.abs(values['s:gpt-5.3-codex'] - codexShare) < 1e-12, id);
    assert.equal(preview.series.find(series => series.value === 'opus-4-6').color, '#e67a30');
    assert.equal(preview.series.find(series => series.value === 'gpt-5.3-codex').color, '#087e62');
  }
});

test('calendar gaps, zero windows, and exact Other retain cost-weighted shares', () => {
  const buckets = Array.from({ length: 8 }, (_, i) => ({ key: String(i), date: new Date(2026, 9, i + 1), values: {} }));
  foldSeries([['0', 'a', 80], ['0', 'b', 15], ['0', 'c', 5]], new Map(buckets.map(bucket => [bucket.key, bucket])), name => name, undefined, [], 1, 'provider');
  const smoothed = rollingAverageBuckets(buckets, 7), shares = shareBuckets(smoothed);
  assert.equal(smoothed[6].values['s:a'], 80 / 7);
  assert.ok(Math.abs(shares[6].values['s:a'] - .8) < 1e-12);
  assert.ok(Math.abs(shares[6].values.other - .2) < 1e-12);
  assert.ok(Math.abs(shares[6].parts.other.reduce((sum, [, value]) => sum + value, 0) - .2) < 1e-12);
  assert.deepEqual(shares[7].values, {}, 'cost drops out after seven calendar buckets');
  assert.deepEqual(shareBuckets([{ ...buckets[0], values: { a: 0 } }])[0].values, { a: 0 });
});
