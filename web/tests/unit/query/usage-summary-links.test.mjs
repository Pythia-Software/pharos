import test from "node:test";
import assert from "node:assert/strict";
import { EMPTY_QUERY, decodeQueryStrict, loadSchema, toQueryRequestV3 } from "@pythia-software/query-table-core";
import { usageSummaryURL } from "../../../src/usage-summary-links";
import { readUsageWorkspace } from "../../../src/usage-workspace-model";
import { readDashboardQuery } from "../../../src/usage-dashboard-model";
import { timeBounds, usageTime, writingTime } from "../../../src/usage-chart";
import { defaultConcurrencySettings, normalizeConcurrencySettings } from "../../../src/concurrency-settings";
import usage from "../../../../schemas/usage.schema.json";
import writingMessages from "../../../../schemas/writing_messages.schema.json";
import messages from "../../../../schemas/messages.schema.json";
import conversationActivity from "../../../../schemas/conversation_activity.schema.json";
import { loadClassicScript } from "../helpers/classic-script.mjs";

const generatedAt = "2026-11-01T08:30:12.345Z"; // Crosses the US daylight saving change.
const windows = [["1h", 1], ["6h", 6], ["24h", 24], ["7d", 168], ["30d", 720], ["all", 0]];
const urlFor = (...args) => new URL(usageSummaryURL(...args), "http://pharos.test");

test("summary values open their dashboard with the snapshot's exact elapsed duration", () => {
  for (const [metric, topic] of [["human_words", "human"], ["tokens", "tokens"], ["cost_usd", "tokens"], ["co2e_g", "carbon"]]) {
    for (const [window, hours] of windows) {
      const url = urlFor(metric, window, generatedAt), params = url.searchParams;
      const state = readUsageWorkspace(params), human = metric === "human_words";
      assert.equal(url.pathname, "/analyze");
      assert.equal(state.topic, topic);
      assert.equal(state.mode, "dashboards");
      assert.equal(state.dataset, human ? "writing_messages" : "usage");
      const restored = readDashboardQuery(params, { ...EMPTY_QUERY, where: [{ field: human ? "sent_at" : "last_usage_at", op: ">=", value: "-30d" }] });
      assert.equal(restored.error, "");
      assert.deepEqual(restored.query, decodeQueryStrict(params.get("analyze_filters")));
      const bounds = timeBounds(restored.query.where, human ? { first: "sent_at", last: "sent_at" } : usageTime);
      if (hours) {
        assert.equal(bounds.to.toISOString(), generatedAt);
        assert.equal(bounds.from.getTime(), Date.parse(generatedAt) - hours * 3600000);
      } else {
        assert.deepEqual(restored.query.where, [], "All time explicitly clears saved/default dates");
        assert.equal(bounds.from, undefined);
        assert.equal(bounds.to, undefined);
      }
      const chart = JSON.parse(params.get("analyze_chart"));
      assert.equal(chart.period, hours && hours <= 24 ? "hour" : "day");
      if (metric === "cost_usd") assert.equal(chart.metric, "cost");
      if (metric === "tokens") assert.equal(chart.metric, "tokens");
      const schema = loadSchema(human ? writingMessages : usage);
      assert.doesNotThrow(() => toQueryRequestV3(restored.query, schema, { version: 3, profile: "qt-sqlite-v1" }));
    }
  }
});

test("parallelism summary links retain counting assumptions and replace saved selections and dates", () => {
  const saved = { ...defaultConcurrencySettings, counting: "agent", gap: 15, repository: "pharos", mode: "model", range: "custom", startDay: "2026-01-01", endDay: "2026-01-03", workspace: "selected", agent: "selected", zoom: [1, 2] };
  const before = structuredClone(saved);
  for (const [window, hours] of windows) {
    const params = urlFor("parallelism", window, generatedAt, saved).searchParams;
    assert.equal(readUsageWorkspace(params).topic, "parallelism");
    const settings = normalizeConcurrencySettings(JSON.parse(params.get("analyze_parallelism")));
    assert.equal(settings.counting, "agent");
    assert.equal(settings.gap, 15);
    assert.equal(settings.repository, "pharos");
    assert.equal(settings.mode, "user", "Header summary measures threaded activity");
    assert.equal(settings.range, "all");
    assert.equal(settings.workspace, "");
    assert.equal(settings.agent, "");
    assert.equal(settings.startDay, "");
    assert.equal(settings.endDay, "");
    assert.deepEqual(settings.zoom, hours ? [Date.parse(generatedAt) - hours * 3600000, Date.parse(generatedAt)] : null);
  }
  assert.deepEqual(saved, before);
});

test("summary links reject unknown durations and invalid snapshot times", () => {
  for (const window of ["", "2h", "constructor"]) assert.equal(usageSummaryURL("tokens", window, generatedAt), undefined);
  assert.equal(usageSummaryURL("tokens", "1h", "invalid"), undefined);
  assert.ok(usageSummaryURL("tokens", "all", "invalid"));
});


test("Today summary links use the service's midnight and freeze the displayed snapshot", () => {
  const from = "2026-11-01T06:00:00.000Z";
  for (const metric of ["human_words", "tokens", "cost_usd", "co2e_g", "parallelism"]) {
    const params = urlFor(metric, "today", generatedAt, defaultConcurrencySettings, from).searchParams;
    assert.equal(params.has("analyze_window"), false);
    if (metric === "parallelism") assert.deepEqual(JSON.parse(params.get("analyze_parallelism")).zoom, [Date.parse(from), Date.parse(generatedAt)]);
    else {
      const where = decodeQueryStrict(params.get("analyze_filters")).where;
      assert.equal(where.find(term => term.op === ">=").value, from);
      assert.equal(where.find(term => term.op === "<").value, generatedAt);
    }
  }
  assert.equal(usageSummaryURL("tokens", "today", "invalid"), undefined);
});


test("calendar and short summary links freeze the service's exact interval", () => {
  const from="2026-11-01T06:00:00.000Z",until="2026-11-02T07:00:00.000Z";
  for(const key of ['yesterday','week','month']) {
    const where=decodeQueryStrict(urlFor('tokens',key,until,defaultConcurrencySettings,from).searchParams.get('analyze_filters')).where;
    assert.equal(where.find(term=>term.op==='>=').value,from);
    assert.equal(where.find(term=>term.op==='<').value,until);
    assert.equal(usageSummaryURL('tokens',key,until),undefined);
  }
  for(const [key,minutes] of [['5m',5],['15m',15]]) {
    const where=decodeQueryStrict(urlFor('tokens',key,until).searchParams.get('analyze_filters')).where;
    assert.equal(Date.parse(where.find(term=>term.op==='>=').value),Date.parse(until)-minutes*60000);
  }
  for(const metric of ['tool_calls','failed_tool_calls','tool_failure_rate','tool_time_ms']) {
    const url=urlFor(metric,'yesterday',until,defaultConcurrencySettings,from);
    assert.equal(readUsageWorkspace(url.searchParams).dataset,'tool_calls');
    const where=decodeQueryStrict(url.searchParams.get('q_tool_calls')).where;
    assert.equal(where.find(term=>term.op==='>=').value,from);
    assert.equal(where.some(term=>term.field==='status'),metric==='failed_tool_calls');
  }
});

test('count links open the closest searchable records for every snapshot window', () => {
  const specs = [
    ['message_count', 'messages', messages, 'sent_at', null],
    ['user_messages', 'messages', messages, 'sent_at', null],
    ['assistant_messages', 'messages', messages, 'sent_at', null],
    ['active_conversations', 'conversation_activity', conversationActivity, 'event_at', 'root_conversation_id'],
    ['active_repositories', 'conversation_activity', conversationActivity, 'event_at', 'repository_name'],
    ['delegated_runs', 'usage', usage, 'last_usage_at', 'delegated_session_id'],
  ];
  const from = '2026-10-31T06:00:00.000Z';
  for (const [metric, dataset, schemaDoc, timeField, group] of specs) {
    for (const [window, expected] of [['1h', '2026-11-01T07:30:12.345Z'], ['yesterday', from], ['all', null]]) {
      const url = urlFor(metric, window, generatedAt, defaultConcurrencySettings, from);
      const state = readUsageWorkspace(url.searchParams);
      assert.equal(state.mode, 'query');
      assert.equal(state.dataset, dataset);
      const query = decodeQueryStrict(url.searchParams.get(`q_${dataset}`));
      assert.deepEqual(query.grouping?.keys ?? null, group ? [group] : null);
      const where = (query.grouping?.source ?? query).where;
      assert.equal(where.find(term => term.field === timeField && term.op === '>=')?.value ?? null, expected);
      assert.equal(where.find(term => term.op === '<')?.value ?? null, expected && generatedAt);
      if (metric === 'delegated_runs' && expected) assert.ok(where.some(term => term.field === 'first_usage_at' && term.op === '<'));
      if (dataset === 'messages') {
        assert.ok(where.some(term => term.field === 'selected' && term.value === 'true'));
        assert.ok(where.some(term => term.field === 'kind' && term.value === 'message'));
        if (metric === 'message_count') assert.deepEqual(where.find(term => term.any)?.any.map(term => term.value), ['user', 'assistant']);
        else assert.ok(where.some(term => term.field === 'role' && term.value === (metric === 'user_messages' ? 'user' : 'assistant')));
        if (metric === 'user_messages') assert.ok(where.some(term => term.field === 'subagent' && term.value === 'false'));
      }
      if (metric === 'active_repositories') assert.ok(where.some(term => term.field === 'repository_name' && term.op === '!='));
      if (metric === 'delegated_runs') assert.ok(where.some(term => term.field === 'delegated_run_depth' && term.op === '>'));
      assert.doesNotThrow(() => toQueryRequestV3(query, loadSchema(schemaDoc), {version: 3, profile: 'qt-sqlite-v1', groupedRows: true}));
    }
  }
});

test('every available Quick Stats metric has a destination for every period', () => {
  const stats = loadClassicScript('quick-stats.js');
  const layout = stats.normalizeQuickStatsLayout({windows: ['1h', '6h', 'today', '24h', '7d', '30d', 'all', '5m', '15m', 'yesterday', 'week', 'month']}, stats.quickStatsMetrics(true));
  for (const [, metric] of stats.quickStatsMetrics(true)) for (const window of layout.windows) {
    assert.ok(usageSummaryURL(metric, window, generatedAt, defaultConcurrencySettings, '2026-10-01T06:00:00.000Z'), `${metric}/${window}`);
  }
});


test('reported-only Quick Stats links preserve the estimate filter', () => {
  for (const metric of ['tokens', 'cost_usd', 'co2e_g']) {
    const url = urlFor(metric, '1h', generatedAt, defaultConcurrencySettings, undefined, false);
    const query = decodeQueryStrict(url.searchParams.get('analyze_filters'));
    assert.ok(query.where.some(term => term.field === 'is_estimate' && term.op === '=' && term.value === 'false'));
    const included = urlFor(metric, '1h', generatedAt);
    assert.ok(!decodeQueryStrict(included.searchParams.get('analyze_filters')).where.some(term => term.field === 'is_estimate'));
  }
  const human = urlFor('human_words', 'all', generatedAt, defaultConcurrencySettings, undefined, false);
  assert.ok(!decodeQueryStrict(human.searchParams.get('analyze_filters')).where.some(term => term.field === 'is_estimate'));
});
