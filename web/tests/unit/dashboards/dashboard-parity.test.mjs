// Exemplar cards expressed as Tokens and Cost dashboard states. Each pair runs
// the card's own chart query and the dashboard's analysis query over the same
// usage ledger with Query Table's in-process evaluation, grouping stages
// included, and must report the same numbers.
import test from "node:test";
import assert from "node:assert/strict";
import { core, schemas, instantiateRecipe, recipeById, cardCoverQuery } from "../helpers/recipes.mjs";
import { now, usageRows } from "../helpers/usage-ledger.mjs";
import { analysisQuery, normalizeAnalysis } from "../../../src/dashboard-analysis-model";
import { tokenAnalysisCatalog } from "../../../src/token-analysis-catalog";
import { cardDashboardWhere } from "../../../src/card-exploration";
import { filterBoxSamples } from "../../../src/compact-box-model";
import { trendQuery, trendSeries } from "../../../src/dashboard-trend-model";
import { loadQueryPreview, clearPreviewCache } from "../../../src/curiosity-data";
import { compactTimelineBuckets } from "../../../src/column-marks";
import { buildCardTrend, cardTrendDays, cardTrendQueries } from "../../../src/card-trend";
import { usageTime, withTimeRange } from "../../../src/chart-range";
import { answerQuery } from "../../support/query-server.mjs";

const tokenAnalysisSource = tokenAnalysisCatalog;

const rows = usageRows(), schema = schemas.usage;
const evaluate = query => core.evaluateMetrics(rows, query, schema, { now }).metrics[0];
const round = value => typeof value === "number" ? Number(value.toPrecision(12)) : value;

// The card's chart, and the dashboard opened from it: the card's population
// becomes the dashboard's filters, as cardDestination writes them.
function pair(id, state) {
  const recipe = recipeById(id), item = instantiateRecipe(recipe, 30, new Date(now));
  const card = cardCoverQuery(item.query, recipe);
  const where = cardDashboardWhere("usage", "usage", (item.query.grouping?.source ?? item.query).where);
  const dashboard = analysisQuery(tokenAnalysisCatalog, state, where);
  const cardResult = evaluate(card.query), dashboardResult = evaluate(dashboard.query);
  assert.equal(cardResult.error, undefined, `${id}: card`);
  assert.equal(dashboardResult.error, undefined, `${id}: dashboard`);
  return { recipe, card: cardResult, dashboard: dashboardResult, clause: dashboard.clause };
}
// Groups by their full keys: one model family has two providers here.
const byKeys = (buckets, value = bucket => bucket.value) => Object.fromEntries(buckets.map(bucket => [JSON.stringify(bucket.keys), round(value(bucket))]));

test("model-cost-share: compare, shown as shares, by model over fully priced usage", () => {
  const { card, dashboard, clause } = pair("model-cost-share", { view: "compare", metric: "cost", split: "provider_model", compareAs: "share" });
  assert.equal(clause.display.kind, "pie");
  assert.ok(byKeys(card.buckets)['["claude","opus"]'] && byKeys(card.buckets)['["antigravity","opus"]'], "one model family, two providers");
  assert.deepEqual(byKeys(dashboard.buckets), byKeys(card.buckets));
  // Shares divide by the same total.
  const total = buckets => buckets.reduce((sum, bucket) => sum + bucket.value, 0);
  assert.equal(round(total(dashboard.buckets)), round(total(card.buckets)));
});

test("cache-efficiency: a token-weighted ratio measure by model", () => {
  const { card, dashboard, clause } = pair("cache-efficiency", { view: "compare", metric: "cache_read_share", split: "provider_model" });
  assert.equal(clause.display.format.kind, "percent");
  assert.deepEqual(byKeys(dashboard.buckets), byKeys(card.buckets));
  assert.ok(Object.values(byKeys(card.buckets)).every(value => value > 0 && value < 1));
  // The denominator is all input, cache writes included: the older
  // reads / (reads + uncached) definition would give a different share.
  const opus = rows.filter(row => row.provider === "claude" && row.model_family === "opus" && row.input_tokens > 0);
  const sum = field => opus.reduce((n, row) => n + row[field], 0);
  assert.equal(byKeys(dashboard.buckets)['["claude","opus"]'], round(sum("cache_read_input_tokens") / sum("input_tokens")));
  assert.notEqual(byKeys(dashboard.buckets)['["claude","opus"]'], round(sum("cache_read_input_tokens") / (sum("cache_read_input_tokens") + sum("uncached_input_tokens"))));
});

test("model-session-costs: per-conversation cost distributions by root model, five-conversation minimum", () => {
  const { recipe, card, dashboard, clause } = pair("model-session-costs", { view: "distribution", unit: "conversation", metric: "cost", split: "provider_model", minSamples: 5 });
  assert.equal(clause.distribution.kind, "box");
  const cardBoxes = filterBoxSamples(card, recipe.cover.minSamples);
  const summary = bucket => bucket.distribution.summary;
  assert.deepEqual(Object.keys(byKeys(cardBoxes.buckets)), ['["claude","opus"]'], "only Claude Opus has five conversations");
  assert.deepEqual(byKeys(dashboard.buckets, bucket => JSON.stringify(summary(bucket), (key, value) => round(value))), byKeys(cardBoxes.buckets, bucket => JSON.stringify(summary(bucket), (key, value) => round(value))));
  assert.equal(dashboard.hiddenGroups, 3, "Sonnet, GPT-5 and Antigravity Opus are reported as hidden");
});

test("session-token-cost: one point per conversation, tokens against cost", () => {
  const { card, dashboard, clause } = pair("session-token-cost", { view: "scatter", unit: "conversation", metric: "tokens", metricY: "cost", split: "provider_model" });
  assert.equal(clause.display.kind, "scatter");
  const points = buckets => byKeys(buckets, bucket => [round(bucket.value), round(bucket.y)]);
  assert.ok(Object.keys(points(card.buckets)).length >= 10);
  assert.deepEqual(points(dashboard.buckets), points(card.buckets), "same keys: top-level provider, model, conversation");
});

// Trend cards. Each opens its destination chart on Over time; the card's own
// preview (its query, history and timeline transforms) and the dashboard's
// trend query and series must draw the same points, day by day.
const serving = new WeakSet();
function previewServer(t) {
  if (serving.has(t)) return;
  serving.add(t);
  const prior = { fetch: global.fetch, location: global.location };
  global.location = { origin: "http://parity.test" };
  t.mock.timers.enable({ apis: ["Date"], now });
  global.fetch = async (url, init = {}) => {
    const path = String(url), request = init.body ? JSON.parse(init.body) : undefined;
    if (path.includes("computed-columns")) return Response.json([]);
    assert.ok(path.startsWith("/api/query/usage/"), path);
    const reply = answerQuery(path.split("/").at(-1), request, rows, schema, { now });
    assert.ok(reply, path);
    return Response.json(reply);
  };
  t.after(() => { Object.assign(global, prior); clearPreviewCache(); });
}
const close = (left, right, label) => {
  assert.deepEqual(Object.keys(left).sort(), Object.keys(right).sort(), `${label}: series`);
  for (const key of Object.keys(left)) assert.ok(Math.abs(left[key] - right[key]) <= 1e-9 * Math.max(1, Math.abs(right[key])), `${label} ${key}: ${left[key]} != ${right[key]}`);
};
async function trendPair(t, id, days, edit = chart => chart) {
  previewServer(t);
  const recipe = recipeById(id), item = instantiateRecipe(recipe, days, new Date(now));
  const preview = await loadQueryPreview("usage", item.query, schema, { recipe: item, cover: item.cover, window: item.window, url: item.url });
  const card = compactTimelineBuckets(preview.buckets, preview.timeline ?? {});
  const where = cardDashboardWhere("usage", "usage", (item.query.grouping?.source ?? item.query).where);
  const state = normalizeAnalysis(tokenAnalysisSource, edit(item.destination.chart)), query = trendQuery(tokenAnalysisSource, state, where, { now });
  const results = core.evaluateMetrics(rows, query.query, schema, { now }).metrics;
  const dashboard = trendSeries(tokenAnalysisSource, state, query, results, { where, now });
  assert.equal(dashboard.error, undefined);
  assert.deepEqual(dashboard.buckets.map(bucket => bucket.key), card.map(bucket => bucket.key), `${id}: the same days`);
  // Series match by their group value; Other folds the same groups.
  assert.deepEqual(dashboard.series.map(item => item.value ?? item.key).sort(), preview.series.map(item => item.value ?? item.key).sort(), `${id}: the same series`);
  card.forEach((bucket, index) => close(dashboard.buckets[index].values, bucket.values, `${id} ${bucket.key}`));
  return { preview, dashboard, card, state };
}

test("token-volume-trend: tokens by provider, 7-day rolling mean", async t => {
  const { dashboard, state } = await trendPair(t, "token-volume-trend", 30);
  assert.deepEqual([state.metric, state.split, state.rollingPeriods], ["tokens", "provider", 7]);
  assert.ok(dashboard.buckets.every(bucket => bucket.windowSize === 7), "history before the range fills every window");
  assert.ok(dashboard.buckets.some(bucket => Object.values(bucket.values).some(value => value > 0)));
});

test("token-volume-log: tokens by repository on a log scale", async t => {
  const { state } = await trendPair(t, "token-volume-log", 30);
  assert.deepEqual([state.split, state.scale], ["repository_name", "logarithmic"]);
});

test("cumulative-api-cost: priced cost by provider, cumulative", async t => {
  const { dashboard } = await trendPair(t, "cumulative-api-cost", 30);
  // The fully priced measure carries the same population without the filter.
  const priced = await trendPair(t, "cumulative-api-cost", 30, chart => ({ ...chart, metric: "priced_cost" }));
  assert.deepEqual(priced.dashboard.buckets.at(-1).values, dashboard.buckets.at(-1).values);
  const unfiltered = cardDashboardWhere("usage", "usage", []);
  assert.ok(trendQuery(tokenAnalysisSource, { ...priced.state, metric: "priced_cost" }, unfiltered, { now }).query.where.some(term => term.field === "price_status"), "the population joins the query");
  const total = rows.filter(row => row.price_status === "priced" && row.cost_usd >= 0 && row.total_tokens > 0).reduce((sum, row) => sum + row.cost_usd, 0);
  assert.ok(Math.abs(Object.values(dashboard.buckets.at(-1).values).reduce((sum, value) => sum + value, 0) - total) < 1e-9, "the last point is the range's priced total");
});

test("model-token-share-trend: token shares by model, smoothed before the share", async t => {
  const { dashboard, state } = await trendPair(t, "model-token-share-trend", 90);
  assert.deepEqual([state.metric, state.share, state.rollingPeriods], ["tokens", true, 7], "the old percent metric is tokens shown as shares");
  for (const bucket of dashboard.buckets) {
    const sum = Object.values(bucket.values).reduce((total, value) => total + value, 0);
    assert.ok(sum === 0 || Math.abs(sum - 1) < 1e-12, bucket.key);
  }
});

test("a pooled ratio trend: cache-read share by model over 7 days, never an average of ratios", () => {
  const where = [{ field: "last_usage_at", op: ">=", value: "-30d" }, { field: "first_usage_at", op: "<", value: "+0s" }];
  const state = { metric: "cache_read_share", split: "provider_model", period: "day", hours: 6, days: 3, rollingPeriods: 7 };
  // Cache reads vary by day here, so pooling and averaging daily ratios differ.
  const varied = rows.map((row, index) => { const reads = row.cache_read_input_tokens * (1 + index % 4); return { ...row, cache_read_input_tokens: reads, input_tokens: row.input_tokens - row.cache_read_input_tokens + reads }; });
  const query = trendQuery(tokenAnalysisSource, state, where, { now });
  const results = core.evaluateMetrics(varied, query.query, schema, { now }).metrics;
  const built = trendSeries(tokenAnalysisSource, state, query, results, { where, now });
  assert.equal(built.lines, true, "ratios of several groups are lines, not stacks");
  // Independently: each day's window pools the reads and inputs of its own
  // and the six earlier recorded days.
  const local = day => { const [y, m, d] = day.split("-").map(Number); return new Date(y, m - 1, d); };
  let checked = 0;
  for (const bucket of built.buckets) for (const item of built.series) {
    const end = local(bucket.key), start = new Date(end); start.setDate(start.getDate() - 6);
    const [provider, model] = JSON.parse(item.value);
    const window = varied.filter(row => row.provider === provider && row.model_family === model && local(row.day) >= start && local(row.day) <= end);
    const input = window.reduce((sum, row) => sum + row.input_tokens, 0), reads = window.reduce((sum, row) => sum + row.cache_read_input_tokens, 0);
    if (!input) { assert.equal(bucket.values[item.key], undefined, "a window without input has no value"); continue; }
    assert.ok(Math.abs(bucket.values[item.key] - reads / input) < 1e-12, `${bucket.key} ${item.value}`);
    const days = [...new Set(window.map(row => row.day))];
    if (days.length > 1) {
      const mean = days.map(day => window.filter(row => row.day === day)).map(part => part.reduce((sum, row) => sum + row.cache_read_input_tokens, 0) / part.reduce((sum, row) => sum + row.input_tokens, 0)).reduce((a, b) => a + b, 0) / days.length;
      if (Math.abs(mean - reads / input) > 1e-9) checked++;
    }
  }
  assert.ok(checked > 0, "some windows differ between pooling and averaging daily ratios");
});

// A trailing-window card through buildCardTrend: daily tokens, a 7-day mean
// over complete days, and its norm, the mean of the 30 endpoints before the
// latest. The dashboard opens on the "Last 30 days" preset mid-afternoon, so
// it draws fewer whole days than the card's 31 endpoints; the points it draws
// and its norm must still be the card's.
const codex = [{ field: "provider", op: "=", value: "codex" }];
for (const [name, filter, selected] of [["all usage", [], false], ["one provider, selected in the legend", codex, true], ["one provider as the population", codex, false]]) test(`a trailing-window card: daily tokens, 7-day mean over complete days, with its norm (${name})`, () => {
  const afternoon = new Date(now); afternoon.setHours(14, 0, 0, 0);
  const at = afternoon.getTime() < now ? afternoon.getTime() + 86400000 : afternoon.getTime();
  const trend = { value: { op: "sum", field: "total_tokens" }, perDay: true, lookback: 7, completeDays: true, valueLabel: "tokens a day", pointValueLabel: "tokens a day", sample: "records", detail: "" };
  const { days, ends } = cardTrendDays(7, true, at);
  const card = buildCardTrend(trend, 7, cardTrendQueries(filter, trend, days).map(query => core.evaluateMetrics(rows, query, schema, { now: at }).metrics), at).study;
  const where = [...withTimeRange([], usageTime, undefined, undefined, "30d"), ...selected ? [] : filter];
  const state = { metric: "tokens", split: filter.length ? "provider" : "none", period: "day", hours: 6, days: 3, rollingPeriods: 7, rollingOp: "mean", completePeriods: true, norm: true, ...selected ? { selection: { values: ["codex"], excluded: false } } : {} };
  const query = trendQuery(tokenAnalysisSource, state, where, { now: at });
  const built = trendSeries(tokenAnalysisSource, state, query, core.evaluateMetrics(rows, query.query, schema, { now: at }).metrics, { where, now: at });
  assert.deepEqual(built.buckets.map(bucket => bucket.key), ends.slice(2), "29 whole days through yesterday: the partial first day is left out");
  const drawn = bucket => filter.length ? bucket.values["s:codex"] ?? 0 : bucket.values.total ?? 0;
  close(Object.fromEntries(built.buckets.map(bucket => [bucket.key, drawn(bucket)])), Object.fromEntries(ends.slice(2).map((end, index) => [end, card.points[index + 2]])), "points");
  assert.ok(card.points.some(point => point > 0));
  assert.equal(built.normPoints, 30);
  assert.ok(Math.abs(built.norm - card.baseline) < 1e-9, `norm ${built.norm} != baseline ${card.baseline}`);
});


test("delegation shares count top-level conversations separately from delegated token volume", () => {
  const records = [
    { id: "r1", root_conversation_id: "one", delegated_run_depth: 0, total_tokens: 10 },
    { id: "d1", root_conversation_id: "one", delegated_run_depth: 1, total_tokens: 30 },
    { id: "d2", root_conversation_id: "one", delegated_run_depth: 2, total_tokens: 20 },
    { id: "r2", root_conversation_id: "two", delegated_run_depth: 0, total_tokens: 40 },
  ];
  for (const [metric, expected] of [["delegating_share", 0.5], ["delegated_share", 0.5]]) {
    const query = analysisQuery(tokenAnalysisCatalog, { view: "compare", metric, split: "none" }, []).query;
    const result = core.evaluateMetrics(records, query, schema, { now }).metrics[0];
    assert.equal(result.error, undefined);
    assert.equal(result.buckets[0].value, expected);
  }
});
