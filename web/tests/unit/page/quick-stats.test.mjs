import { test } from 'node:test';
import assert from 'node:assert/strict';
import { loadClassicScript, plain } from '../helpers/classic-script.mjs';
import { encodeViewValue, parseViewValue, viewFields } from '../../../src/view-contract';

test('Quick Stats configuration uses the shared disclosure and reveal URL conventions', () => {
  const stats = loadClassicScript('quick-stats.js');
  const url = new URL(stats.quickStatsSettingsURL(), 'http://pharos.test');
  assert.equal(url.pathname, '/settings/layout');
  const entries = parseViewValue(viewFields.disclosures, url.searchParams.get('disclosures'));
  assert.deepEqual(entries, {quickStatsSettings: true});
  assert.equal(encodeViewValue(viewFields.disclosures, entries), url.searchParams.get('disclosures'));
  assert.equal(url.searchParams.get('reveal'), 'section:quickStatsSettings');
});

test('Quick Stats defaults preserve the existing card and gate parallelism', () => {
  const stats = loadClassicScript('quick-stats.js');
  const defaults = plain(stats.normalizeQuickStatsLayout(null));
  assert.deepEqual(defaults.metrics, ['human_words', 'tokens', 'co2e_g', 'cost_usd']);
  assert.deepEqual(defaults.windows, ['1h', '6h', 'today', '24h', '7d', '30d', 'all']);
  assert.ok(stats.normalizeQuickStatsLayout(null, stats.quickStatsMetrics(true)).metrics.includes('parallelism'));
  assert.ok(!defaults.metrics.includes('parallelism'));
});

test('saved Quick Stats retain order and omissions while filtering obsolete and repeated entries', () => {
  const stats = loadClassicScript('quick-stats.js');
  assert.deepEqual(plain(stats.normalizeQuickStatsLayout({metrics: ['cost_usd', 'unknown', 'tokens', 'tokens', null], windows: ['all', 'today', 'all', {}]})), {
    metrics: ['cost_usd', 'tokens'], windows: ['all', 'today'],
  });
  for (const value of [null, false, 'tokens', {}, {metrics: [], windows: []}, {metrics: ['retired'], windows: ['retired']}]) {
    assert.deepEqual(plain(stats.normalizeQuickStatsLayout(value)), plain(stats.normalizeQuickStatsLayout(null)));
  }
  assert.deepEqual(plain(stats.normalizeQuickStatsLayout({metrics: ['parallelism', 'tokens']}).metrics), ['tokens']);
  assert.deepEqual(plain(stats.normalizeQuickStatsLayout({metrics: ['parallelism', 'tokens']}, stats.quickStatsMetrics(true)).metrics), ['parallelism', 'tokens']);
});

test('Quick Stats can reorder, add and remove metrics and periods without changing the other axis', () => {
  const {editQuickStatsLayout: edit} = loadClassicScript('quick-stats.js');
  const saved = {metrics: ['human_words', 'tokens', 'cost_usd'], windows: ['today', 'all']};
  assert.deepEqual(plain(edit(saved, 'metrics', 'cost_usd', 0)), {...saved, metrics: ['cost_usd', 'human_words', 'tokens']});
  assert.deepEqual(plain(edit(saved, 'metrics', 'human_words', 3)), {...saved, metrics: ['tokens', 'cost_usd', 'human_words']});
  assert.deepEqual(plain(edit(saved, 'metrics', 'tokens', 1)), saved);
  assert.deepEqual(plain(edit(saved, 'metrics', 'tokens', 2)), saved);
  assert.deepEqual(plain(edit(saved, 'metrics', 'co2e_g', 1)), {...saved, metrics: ['human_words', 'co2e_g', 'tokens', 'cost_usd']});
  assert.deepEqual(plain(edit(saved, 'metrics', 'tokens', null)), {...saved, metrics: ['human_words', 'cost_usd']});
  assert.deepEqual(plain(edit(saved, 'windows', 'all', 0)), {...saved, windows: ['all', 'today']});
  assert.deepEqual(plain(edit(saved, 'windows', '1h', 99)), {...saved, windows: ['today', 'all', '1h']});
  assert.deepEqual(plain(edit(saved, 'windows', 'all', null)), {...saved, windows: ['today']});
  assert.deepEqual(plain(edit(saved, 'metrics', 'today', 0)), saved, 'items cannot cross axes');
  assert.deepEqual(plain(edit(saved, 'unknown', 'tokens', 0)), saved);
  assert.deepEqual(saved, {metrics: ['human_words', 'tokens', 'cost_usd'], windows: ['today', 'all']});
});

test('Quick Stats keep at least one metric and period when removing the final item', () => {
  const stats = loadClassicScript('quick-stats.js');
  const saved = {metrics: ['tokens'], windows: ['today']};
  assert.deepEqual(plain(stats.editQuickStatsLayout(saved, 'metrics', 'tokens', null)), saved);
  assert.deepEqual(plain(stats.editQuickStatsLayout(saved, 'windows', 'today', null)), saved);
});


test('new Quick Stats metrics and calendar columns are opt-in and survive saved layouts', () => {
  const stats = loadClassicScript('quick-stats.js');
  const selected = {metrics:['active_conversations','message_count','user_messages','tool_calls','agent_time_ms','active_time_ms','delegated_runs','failed_tool_calls','tool_failure_rate','active_repositories','tool_time_ms','assistant_messages'],windows:['5m','15m','yesterday','week','month']};
  assert.deepEqual(plain(stats.normalizeQuickStatsLayout(selected)), selected);
  const defaults=plain(stats.normalizeQuickStatsLayout(null));
  assert.deepEqual(defaults.metrics,['human_words','tokens','co2e_g','cost_usd']);
  assert.deepEqual(defaults.windows,['1h','6h','today','24h','7d','30d','all']);
  for(const [value,format,want] of [[null,'duration','—'],[undefined,'percent','—'],[0,'duration','0s'],[500,'duration','500ms'],[60000,'duration','1m'],[3590000,'duration','1h 0m'],[7200000,'duration','2h 0m'],[0,'percent','0%'],[33.3333,'percent','33.3%']]) assert.equal(stats.formatQuickStatsValue(value,format),want);
});

test('Quick Stats mark message and tool-call counts approximate when copied history is unresolved', () => {
  const stats = loadClassicScript('quick-stats.js');
  const partial = {occurrence_coverage: 'partial', unresolved_occurrences: 3, message_observations: 12, tool_call_observations: 5};
  for (const field of ['message_count', 'user_messages', 'assistant_messages', 'tool_calls', 'failed_tool_calls', 'tool_failure_rate']) {
    const coverage = stats.quickStatsOccurrenceCoverage(field, partial);
    assert.equal(coverage?.mark, '≈', field);
    assert.match(coverage.note, /3 retained observations/);
  }
  assert.match(stats.quickStatsOccurrenceCoverage('tool_calls', partial).note, /5 tool calls are retained/);
  // Complete coverage, and metrics copies cannot affect, stay exact.
  assert.equal(stats.quickStatsOccurrenceCoverage('message_count', {...partial, occurrence_coverage: 'complete'}), null);
  assert.equal(stats.quickStatsOccurrenceCoverage('tokens', partial), null);
  assert.equal(stats.quickStatsOccurrenceCoverage('message_count', {}), null);
  // A transcript-level partial without unresolved observations still explains itself.
  assert.match(stats.quickStatsOccurrenceCoverage('message_count', {occurrence_coverage: 'partial', unresolved_occurrences: 0, occurrence_partial_conversations: 2}).note, /2 transcripts may include copied history/);
});

test('Quick Stats never show conversation or run counts as exact when ancestry or identity is unresolved', () => {
  const stats = loadClassicScript('quick-stats.js');
  const partial = {ancestry_coverage: 'partial', unrooted_conversations: 1, identity_uncertain_conversations: 2, ancestry_qualified_conversations: 0};
  for (const field of ['active_conversations', 'delegated_runs']) {
    const coverage = stats.quickStatsAncestryCoverage(field, partial);
    assert.equal(coverage?.mark, '≈', field);
    assert.match(coverage.note, /1 active transcript has no known top-level conversation/);
    assert.match(coverage.note, /2 transcripts have unresolved execution identity/);
  }
  const qualified = stats.quickStatsAncestryCoverage('delegated_runs', {ancestry_coverage: 'qualified', ancestry_qualified_conversations: 3});
  assert.equal(qualified?.mark, '?');
  assert.match(qualified.note, /inferred or unverified link/);
  assert.equal(stats.quickStatsAncestryCoverage('active_conversations', {ancestry_coverage: 'complete'}), null);
  assert.equal(stats.quickStatsAncestryCoverage('tokens', partial), null, 'usage totals do not depend on ancestry');
});

const summary = value => ({generated_at: '2026-11-07T18:00:00Z', windows: [{key: 'today', tokens: value}]});
function cacheStorage() {
  const saved = new Map();
  return {getItem: key => saved.get(key), setItem: (key, value) => saved.set(key, value)};
}

test('Quick Stats reuse recent results, restore snapshots as stale, and respect expiry and calendar boundaries', () => {
  const {createQuickStatsCache} = loadClassicScript('quick-stats.js');
  const storage = cacheStorage();
  let now = Date.parse('2026-11-07T18:00:00Z'), context = 'Denver:2026-11-07';
  const options = {scope: 'library-a', storage, now: () => now, context: () => context};
  const cache = createQuickStatsCache(options);
  assert.equal(cache.get(false), null);
  cache.put(false, summary(42));
  assert.equal(cache.get(false).fresh, true);
  const restored = createQuickStatsCache(options);
  assert.equal(restored.get(false).data.windows[0].tokens, 42);
  assert.equal(restored.get(false).fresh, false, 'a new page must revalidate its saved result');
  now += 60000;
  assert.equal(cache.get(false).fresh, false, 'an older result stays visible while refreshing');
  context = 'Denver:2026-11-08';
  assert.equal(cache.get(false), null, 'yesterday’s Today total must not appear as today’s');
  context = 'London:2026-11-07';
  assert.equal(cache.get(false), null, 'calendar totals belong to their timezone');
  context = 'Denver:2026-11-07'; now += 7 * 86400000;
  assert.equal(cache.get(false), null, 'expired snapshots cannot be restored');
});

test('Quick Stats separate libraries and detail requests, and do not persist an unscoped response', () => {
  const {createQuickStatsCache} = loadClassicScript('quick-stats.js');
  const storage = cacheStorage();
  const cache = createQuickStatsCache({scope: 'library-a', storage});
  cache.put(false, summary(10));
  assert.equal(cache.get(true), null, 'base totals cannot satisfy the opt-in detail request');
  cache.put(true, {...summary(20), windows: [{key: 'today', tokens: 20, message_count: 3}]});
  assert.equal(cache.get(false).data.windows[0].tokens, 10);
  assert.equal(cache.get(true).data.windows[0].message_count, 3);
  assert.equal(createQuickStatsCache({scope: 'library-b', storage}).get(false), null);
  const unscoped = createQuickStatsCache({storage});
  unscoped.put(false, summary(30));
  assert.equal(unscoped.get(false).fresh, true);
  assert.equal(createQuickStatsCache({storage}).get(false), null);
});

test('Quick Stats invalidation keeps the last value while rejecting requests started before the update', () => {
  const {createQuickStatsCache} = loadClassicScript('quick-stats.js');
  const storage = cacheStorage(), cache = createQuickStatsCache({scope: 'library-a', storage});
  cache.put(false, summary(10));
  const before = cache.generation();
  cache.invalidate();
  assert.equal(cache.get(false).fresh, false);
  assert.equal(cache.get(false).data.windows[0].tokens, 10);
  cache.put(false, summary(20));
  cache.put(false, summary(5), before);
  assert.equal(cache.get(false).data.windows[0].tokens, 20);
  assert.equal(createQuickStatsCache({scope: 'library-a', storage}).get(false).data.windows[0].tokens, 20);
});

test('Quick Stats tolerate unavailable storage and discard malformed or future snapshots', () => {
  const {createQuickStatsCache} = loadClassicScript('quick-stats.js');
  const blocked = createQuickStatsCache({scope: 'library-a', storage: {getItem() {throw Error('blocked');}, setItem() {throw Error('full');}}});
  blocked.put(false, summary(10));
  assert.equal(blocked.get(false).data.windows[0].tokens, 10, 'live results work without browser storage');
  const now = Date.now();
  for (const saved of ['invalid JSON', '{}', '[null]', JSON.stringify([
    {details: false, at: now + 1, context: 'today', data: summary(10)},
    {details: true, at: now, context: 'today', data: {generated_at: 'invalid', windows: []}},
  ])]) {
    const cache = createQuickStatsCache({scope: 'library-a', storage: {getItem: () => saved}, now: () => now, context: () => 'today'});
    assert.equal(cache.get(false), null); assert.equal(cache.get(true), null);
  }
});

test('Quick Stats share in-flight requests and bypass a recent result only for refresh or invalidation', async () => {
  const {createQuickStatsCache} = loadClassicScript('quick-stats.js');
  const cache = createQuickStatsCache({storage: null});
  let requests = 0, finish;
  const pending = new Promise(resolve => {finish = resolve;});
  const request = () => {requests++; return pending;};
  const first = [], second = [];
  const a = cache.load(false, request, value => first.push(value));
  const b = cache.load(false, request, value => second.push(value));
  finish(summary(10)); await Promise.all([a, b]);
  assert.equal(requests, 1);
  assert.equal(first[0].data.windows[0].tokens, 10);
  assert.equal(second[0].data.windows[0].tokens, 10);
  const live = async () => {requests++; return summary(requests * 10);};
  await cache.load(false, live, () => {});
  assert.equal(requests, 1, 'reopening a recent snapshot does not refetch');
  await cache.load(false, live, () => {}, true);
  assert.equal(requests, 2, 'Refresh and Auto bypass the one-minute reuse');
  cache.invalidate();
  await cache.load(false, live, () => {});
  assert.equal(requests, 3, 'a library update fetches new totals');
  await cache.load(true, live, () => {});
  assert.equal(requests, 4, 'details require their own response');
});

test('Quick Stats show a restored result before the network completes and retain it after failure', async () => {
  const {createQuickStatsCache} = loadClassicScript('quick-stats.js');
  const storage = cacheStorage(), options = {scope: 'library-a', storage};
  createQuickStatsCache(options).put(false, summary(10));
  const cache = createQuickStatsCache(options), updates = [];
  let fail;
  const offline = new Promise((_resolve, reject) => {fail = reject;});
  const loading = cache.load(false, () => offline, entry => updates.push(entry));
  assert.equal(updates[0].data.windows[0].tokens, 10, 'the old result is published synchronously');
  assert.equal(updates[0].fresh, false);
  fail(Error('Offline'));
  await assert.rejects(loading, /Offline/);
  assert.equal(cache.get(false).data.windows[0].tokens, 10);
  await cache.load(false, async () => summary(20), entry => updates.push(entry));
  assert.equal(updates.at(-1).data.windows[0].tokens, 20, 'failed requests are retried');
});

test('Quick Stats discard late responses after a data update or calendar change', async () => {
  const {createQuickStatsCache} = loadClassicScript('quick-stats.js');
  for (const change of ['data', 'calendar']) {
    let context = 'today', finish;
    const cache = createQuickStatsCache({storage: null, context: () => context});
    const pending = new Promise(resolve => {finish = resolve;}), updates = [];
    const loading = cache.load(false, () => pending, entry => updates.push(entry));
    if (change === 'data') cache.invalidate(); else context = 'tomorrow';
    await cache.load(false, async () => summary(20), entry => updates.push(entry));
    finish(summary(10)); await loading;
    assert.deepEqual(updates.map(entry => entry.data.windows[0].tokens), [20]);
    assert.equal(cache.get(false).data.windows[0].tokens, 20);
  }
});

test('Quick Stats coalesce calendar and usage notifications before loading the invalidated cache', async () => {
  const {createQuickStatsCache, createQuickStatsRefreshQueue} = loadClassicScript('quick-stats.js');
  const cache = createQuickStatsCache({storage: null});
  cache.put(false, summary(10));
  let requests = 0, loading;
  const updates = [], refresh = createQuickStatsRefreshQueue(() => {
    loading = cache.load(false, async () => {requests++; return summary(20);}, entry => updates.push(entry));
  });
  cache.invalidate(); refresh(); // Calendar change.
  cache.invalidate(); refresh(); // Its accompanying usage refresh.
  await Promise.resolve(); await loading;
  assert.equal(requests, 1, 'both notifications share one aggregation');
  assert.equal(updates.at(-1).fresh, true, 'the combined refresh must not invalidate its own response');
  assert.equal(cache.get(false).data.windows[0].tokens, 20);
  cache.invalidate(); refresh();
  await Promise.resolve(); await loading;
  assert.equal(requests, 2, 'a later update still fetches new totals');
});


test('Quick Stats provenance follows the metric and selected window',()=>{
 const stats=loadClassicScript('quick-stats.js');
 const mixed={is_estimate:true,cost_is_estimate:true,price_status:'partial',tool_time_is_estimate:true};
 for(const field of ['tokens','cost_usd','co2e_g','tool_time_ms']) assert.equal(stats.quickStatsMetricCaveat(field,mixed).suffix,'*');
 assert.equal(stats.quickStatsMetricCaveat('human_words',mixed),null);
 assert.equal(stats.quickStatsMetricCaveat('tokens',{is_estimate:false}),null);
 assert.equal(stats.quickStatsMetricCaveat('cost_usd',{is_estimate:true,cost_is_estimate:false,price_status:'unpriced'}).suffix,'+');
 assert.match(stats.quickStatsMetricCaveat('cost_usd',mixed).note,/estimated usage.*partial/);
});
