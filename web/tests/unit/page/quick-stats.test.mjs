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
