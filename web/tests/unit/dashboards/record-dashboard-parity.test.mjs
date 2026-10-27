// Cards of the Tools, Pharos MCP, Agent Time and Skills dashboards, each
// opened on its destination state. As in dashboard-parity.test.mjs, the
// card's own query and the dashboard's query run over the same ledger with
// Query Table's in-process evaluation and must report the same numbers,
// compared by full group keys.
import test from "node:test";
import assert from "node:assert/strict";
import { core, schemas, instantiateRecipe, recipeById, cardCoverQuery } from "../helpers/recipes.mjs";
import { now, toolCalls, toolsRollup, mcpCalls, skillUsages } from "../helpers/record-ledgers.mjs";
import { analysisQuery, normalizeAnalysis, selectGroups } from "../../../src/dashboard-analysis-model";
import { trendQuery, trendSeries } from "../../../src/dashboard-trend-model";
import { toolsAnalysisCatalog } from "../../../src/tools-analysis-catalog";
import { mcpAnalysisCatalog } from "../../../src/mcp-analysis-catalog";
import { agentTimeAnalysisCatalog, skillsAnalysisCatalog } from "../../../src/record-analysis-catalogs";
import { cardDashboardWhere } from "../../../src/card-exploration";
import { filterBoxSamples } from "../../../src/compact-box-model";
import { loadQueryPreview, clearPreviewCache } from "../../../src/curiosity-data";
import { compactTimelineBuckets } from "../../../src/column-marks";
import { buildCardTrend, cardTrendDays, cardTrendQueries } from "../../../src/card-trend";
import { withTimeRange } from "../../../src/chart-range";
import { answerQuery } from "../../support/query-server.mjs";

const catalogs = { tools: toolsAnalysisCatalog, mcp: mcpAnalysisCatalog, time: agentTimeAnalysisCatalog, skills: skillsAnalysisCatalog };
const pad = value => String(value).padStart(2, "0"), localDay = time => { const date = new Date(time); return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`; };
// The rollup's days are whole, so a card's range starting mid-day can only
// match it when that first, partial day holds no calls.
const calls = toolCalls().filter(call => call.day !== localDay(now - 7 * 86400000));
const rows = { tool_calls: calls, tools: toolsRollup(calls), mcp_calls: mcpCalls(), skill_usages: skillUsages() };
const round = value => typeof value === "number" ? Number(value.toPrecision(12)) : value;
const byKeys = (buckets, value = bucket => bucket.value) => Object.fromEntries(buckets.map(bucket => [JSON.stringify(bucket.keys), round(value(bucket))]));
const summary = bucket => JSON.stringify(bucket.distribution.summary, (_, value) => round(value));

// The card's cover metric and the dashboard its destination opens, with the
// card's population as the dashboard's filters (cardDestination).
function pair(id, { days = 7 } = {}) {
  const recipe = recipeById(id), item = instantiateRecipe(recipe, days, new Date(now));
  const { topic, chart, dataset } = recipe.destination, catalog = catalogs[topic];
  assert.equal(catalog.dataset, dataset, id);
  const card = cardCoverQuery(item.query, recipe);
  const where = cardDashboardWhere(recipe.dataset, dataset, (item.query.grouping?.source ?? item.query).where);
  const analysis = analysisQuery(catalog, chart, where, { now });
  const cardResult = core.evaluateMetrics(rows[recipe.dataset], card.query, schemas[recipe.dataset], { now }).metrics.find(metric => metric.id === card.metricId);
  const dashboard = core.evaluateMetrics(rows[dataset], analysis.query, schemas[dataset], { now }).metrics[0];
  assert.equal(cardResult.error, undefined, `${id}: card`);
  assert.equal(dashboard.error, undefined, `${id}: dashboard`);
  assert.ok(cardResult.buckets.length > 1, `${id}: the fixture has groups to compare`);
  return { recipe, chart, card: cardResult, dashboard: selectGroups(dashboard, analysis.selection), analysis, state: normalizeAnalysis(catalog, chart) };
}
// A share's zero group has no slice: the card's population leaves out
// records the rollup's cannot (calls without a failure), whose share is zero.
const sameGroups = (id, card, dashboard, share = false) => assert.deepEqual(byKeys(dashboard.buckets.filter(bucket => !share || bucket.value)), byKeys(card.buckets), id);
const sameBoxes = (id, recipe, card, dashboard) => {
  const shown = filterBoxSamples(card, recipe.cover.minSamples);
  assert.ok(shown.buckets.length > 1 && shown.buckets.length < card.buckets.length, `${id}: the minimum hides some groups`);
  assert.deepEqual(byKeys(dashboard.buckets, summary), byKeys(shown.buckets, summary), id);
  assert.equal(dashboard.hiddenGroups, card.buckets.length - shown.buckets.length, `${id}: hidden groups are reported`);
};

// ----- Tools: the daily rollup -----------------------------------------------

test("tool-failures and tool-call-share: shares of failures and calls by tool, from the rollup", () => {
  for (const [id, metric] of [["tool-failures", "failures"], ["tool-call-share", "calls"]]) {
    const { card, dashboard, state, analysis } = pair(id);
    assert.deepEqual([state.view, state.metric, state.split, state.compareAs], ["compare", metric, "tool_name", "share"]);
    assert.equal(analysis.clause.display.kind, "pie");
    sameGroups(id, card, dashboard, true);
  }
});

test("tool-failure-rates: pooled failure rates by tool, with the card's ten-call minimum counted in calls", () => {
  const { card, dashboard, state, analysis } = pair("tool-failure-rates");
  assert.deepEqual([state.metric, state.minSamples], ["failure_rate", 10]);
  assert.match(analysis.query.where.map(term => `${term.field} ${term.op} ${term.value}`).join(), /analysis_samples >= 10/, "the minimum counts calls, not daily summaries");
  // Every tool the card shows has the card's rate.
  const dashboardRates = byKeys(dashboard.buckets);
  for (const [key, rate] of Object.entries(byKeys(card.buckets))) assert.equal(dashboardRates[key], rate, key);
  // Known gap: the card also requires one failure, a second minimum the
  // dashboard cannot state; it shows tools with ten calls and no failures at
  // 0% (docs/dashboard-analysis.md).
  const extra = Object.entries(dashboardRates).filter(([key]) => !(key in byKeys(card.buckets)));
  assert.ok(extra.length > 0 && extra.every(([, rate]) => rate === 0), "the only difference is zero-failure tools");
});

test("machine-call-trend: calls a day, a 7-day mean over complete days, with its norm, from the rollup", () => {
  trailingCard("machine-call-trend", 1);
});

// ----- Pharos MCP: one record per request -----------------------------------

test("mcp-response-size and pharos-mcp-durations: request distributions by tool, three-request minimum", () => {
  for (const [id, metric] of [["mcp-response-size", "bytes"], ["pharos-mcp-durations", "time"]]) {
    const { recipe, card, dashboard, state, analysis } = pair(id, { days: 30 });
    assert.deepEqual([state.view, state.metric, state.split, state.minSamples], ["distribution", metric, "tool_name", 3]);
    assert.equal(analysis.clause.distribution.kind, "box");
    // Every tool has three requests here; a tighter minimum hides some.
    assert.deepEqual(byKeys(dashboard.buckets, summary), byKeys(filterBoxSamples(card, recipe.cover.minSamples).buckets, summary), id);
    const strict = analysisQuery(mcpAnalysisCatalog, { ...state, minSamples: 30 }, cardDashboardWhere("mcp_calls", "mcp_calls", (instantiateRecipe(recipe, 30, new Date(now)).query.grouping?.source ?? instantiateRecipe(recipe, 30, new Date(now)).query).where), { now });
    const hidden = core.evaluateMetrics(rows.mcp_calls, strict.query, schemas.mcp_calls, { now }).metrics[0];
    assert.deepEqual(byKeys(hidden.buckets, summary), byKeys(filterBoxSamples(card, 30).buckets, summary), `${id} at 30`);
  }
});

test("pharos-mcp-query-sources: calls by requested source, unscoped requests included", () => {
  const { card, dashboard } = pair("pharos-mcp-query-sources");
  sameGroups("pharos-mcp-query-sources", card, dashboard);
  assert.ok(Object.keys(byKeys(dashboard.buckets)).includes("[null]"));
});

// ----- Agent Time: one record per call ----------------------------------------

test("machine-activities: shares of recorded tool time by activity", () => {
  const { card, dashboard, analysis } = pair("machine-activities");
  assert.equal(analysis.clause.display.kind, "pie");
  sameGroups("machine-activities", card, dashboard);
});

test("large-outputs: shares of result tokens by tool and result-size source", () => {
  const { card, dashboard } = pair("large-outputs");
  assert.ok(Object.keys(byKeys(card.buckets)).some(key => key.includes("estimated")) && Object.keys(byKeys(card.buckets)).some(key => key.includes("measured")));
  sameGroups("large-outputs", card, dashboard);
});

test("duration-distribution: call durations by command and timing source, three-call minimum", () => {
  const { recipe, card, dashboard, state } = pair("duration-distribution");
  assert.deepEqual([state.view, state.unit, state.split], ["distribution", "records", "command_source"]);
  sameBoxes("duration-distribution", recipe, card, dashboard);
});

test("conversation-error-counts: failed calls per tool in each affected conversation, three-conversation minimum", () => {
  const { recipe, card, dashboard, state, analysis } = pair("conversation-error-counts", { days: 30 });
  assert.deepEqual([state.view, state.unit, state.metric, state.split], ["distribution", "tool_conversation", "calls", "tool_name"]);
  assert.deepEqual(analysis.query.grouping.keys, ["tool_name", "conversation_id"], "one observation is one tool in one conversation, as the card's");
  sameBoxes("conversation-error-counts", recipe, card, dashboard);
});

// Trend cards: the card's own preview (its query, history and timeline
// transforms) and the dashboard's trend draw the same points, day by day.
function previewServer(t) {
  const prior = { fetch: global.fetch, location: global.location };
  global.location = { origin: "http://parity.test" };
  t.mock.timers.enable({ apis: ["Date"], now });
  global.fetch = async (url, init = {}) => {
    const path = String(url), request = init.body ? JSON.parse(init.body) : undefined;
    if (path.includes("computed-columns")) return Response.json([]);
    const dataset = path.split("/")[3];
    const reply = answerQuery(path.split("/").at(-1), request, rows[dataset], schemas[dataset], { now });
    assert.ok(reply, path);
    return Response.json(reply);
  };
  t.after(() => { Object.assign(global, prior); clearPreviewCache(); });
}
async function trendPair(t, id, days, scale = 1) {
  previewServer(t);
  const recipe = recipeById(id), item = instantiateRecipe(recipe, days, new Date(now)), catalog = catalogs[recipe.destination.topic];
  const preview = await loadQueryPreview(recipe.dataset, item.query, schemas[recipe.dataset], { recipe: item, cover: item.cover, window: item.window, url: item.url });
  const card = compactTimelineBuckets(preview.buckets, preview.timeline ?? {});
  const where = cardDashboardWhere(recipe.dataset, catalog.dataset, (item.query.grouping?.source ?? item.query).where);
  const state = normalizeAnalysis(catalog, recipe.destination.chart), query = trendQuery(catalog, state, where, { now });
  const built = trendSeries(catalog, state, query, core.evaluateMetrics(rows[catalog.dataset], query.query, schemas[catalog.dataset], { now }).metrics, { now, label: (field, value) => String(value) });
  assert.equal(built.error, undefined);
  assert.deepEqual(built.buckets.map(bucket => bucket.key), card.map(bucket => bucket.key), `${id}: the same days`);
  // Series match by their labels: the card names activities as Agent Time
  // labels them.
  const labels = series => Object.fromEntries(series.map(item => [item.key, item.label]));
  const named = (values, names) => Object.fromEntries(Object.entries(values).map(([key, value]) => [names[key], value]));
  assert.deepEqual(Object.values(labels(built.series)).sort(), Object.values(labels(preview.series)).sort(), `${id}: the same series`);
  card.forEach((bucket, index) => {
    const left = Object.fromEntries(Object.entries(named(built.buckets[index].values, labels(built.series))).map(([key, value]) => [key, value * scale])), right = named(bucket.values, labels(preview.series));
    assert.deepEqual(Object.keys(left).sort(), Object.keys(right).sort(), `${id} ${bucket.key}`);
    for (const key of Object.keys(right)) assert.ok(Math.abs(left[key] - right[key]) <= 1e-9 * Math.max(1, Math.abs(right[key])), `${id} ${bucket.key} ${key}: ${left[key]} != ${right[key]}`);
  });
  assert.ok(card.some(bucket => Object.values(bucket.values).some(value => value > 0)));
  return { state };
}

test("machine-time: recorded tool time by activity, day by day", async t => {
  // The card draws hours; the dashboard's measure is milliseconds, shown in hours.
  const { state } = await trendPair(t, "machine-time", 30, 1 / 3_600_000);
  assert.deepEqual([state.view, state.metric, state.split], ["trend", "time", "activity"]);
});

test("mcp-call-trend: Pharos calls by tool, 7-day rolling mean", async t => {
  const { state } = await trendPair(t, "mcp-call-trend", 30);
  assert.deepEqual([state.metric, state.split, state.rollingPeriods], ["calls", "tool_name", 7]);
});

// A trailing-window card through buildCardTrend, opened on "Last 30 days" at
// 14:00 with whole periods and a norm: the points the dashboard draws, and
// its norm, are the card's (dashboard-parity.test.mjs, for tokens).
function trailingCard(id, scale) {
  const recipe = recipeById(id), trend = recipe.trend, catalog = catalogs[recipe.destination.topic];
  const afternoon = new Date(now); afternoon.setHours(14, 0, 0, 0);
  const at = afternoon.getTime() < now ? afternoon.getTime() + 86400000 : afternoon.getTime();
  const { days, ends } = cardTrendDays(trend.lookback, true, at);
  const card = buildCardTrend(trend, trend.lookback, cardTrendQueries([], trend, days).map(query => core.evaluateMetrics(rows[trend.dataset], query, schemas[trend.dataset], { now: at }).metrics), at).study;
  const where = withTimeRange([], catalog.time, undefined, undefined, "30d"), state = recipe.destination.chart;
  assert.deepEqual([state.rollingPeriods, state.completePeriods, state.norm, state.split], [trend.lookback, true, true, "none"]);
  const query = trendQuery(catalog, state, where, { now: at });
  const built = trendSeries(catalog, state, query, core.evaluateMetrics(rows[catalog.dataset], query.query, schemas[catalog.dataset], { now: at }).metrics, { now: at });
  assert.equal(built.error, undefined);
  assert.deepEqual(built.buckets.map(bucket => bucket.key), ends.slice(2), "29 whole days through yesterday");
  built.buckets.forEach((bucket, index) => {
    const expected = card.points[index + 2], drawn = bucket.values.total;
    if (expected === null) assert.equal(drawn, undefined, bucket.key);
    else assert.ok(Math.abs(drawn * scale - expected) <= 1e-9 * Math.max(1, Math.abs(expected)), `${id} ${bucket.key}: ${drawn * scale} != ${expected}`);
  });
  assert.ok(card.points.some(point => point > 0));
  assert.equal(built.normPoints, card.points.slice(0, -1).filter(point => point !== null).length);
  assert.ok(Math.abs(built.norm * scale - card.baseline) < 1e-9 * Math.max(1, card.baseline), `norm ${built.norm * scale} != baseline ${card.baseline}`);
}

test("machine-latency-trend: a pooled trailing mean call time over complete days, with its norm, from individual calls", () => {
  // The card reads the rollup's total duration over timed calls; Agent Time
  // pools the calls' own durations over calls with one.
  trailingCard("machine-latency-trend", 0.001);
});

// ----- Skills: one record per piece of evidence -----------------------------

test("skill-evidence: distinct conversations reached by each skill", () => {
  const { card, dashboard, state } = pair("skill-evidence");
  assert.deepEqual([state.view, state.metric, state.split], ["compare", "conversations", "skill_name"]);
  sameGroups("skill-evidence", card, dashboard);
});

test("skill-body-sizes: retained body sizes by skill and evidence type, three-body minimum", () => {
  const { recipe, card, dashboard, analysis } = pair("skill-body-sizes", { days: 30 });
  assert.ok(analysis.query.where.some(term => term.field === "content_bytes" && term.op === ">"), "the measure's population: known positive sizes");
  sameBoxes("skill-body-sizes", recipe, card, dashboard);
});

// Further cards whose destinations these catalogs now state.
for (const [id, days] of [["output-distribution", 30], ["tool-calls-per-conversation", 30], ["research-conversation-searches", 30], ["skill-load-distribution", 30]]) test(`${id}: the destination's distribution is the card's`, () => {
  const { recipe, card, dashboard } = pair(id, { days });
  const shown = filterBoxSamples(card, recipe.cover.minSamples);
  assert.ok(shown.buckets.length > 0, `${id}: groups to compare`);
  assert.deepEqual(byKeys(dashboard.buckets, summary), byKeys(shown.buckets, summary), id);
});
