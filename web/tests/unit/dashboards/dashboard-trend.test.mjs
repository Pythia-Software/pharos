// Over time as a catalog-driven view: old links and presets, coercions,
// queries and the series builder, over the usage ledger.
import test from "node:test";
import assert from "node:assert/strict";
import { core, queryRecipes, schemas } from "../helpers/recipes.mjs";
import { now, usageLedger } from "../helpers/usage-ledger.mjs";
import { catalogProblems, normalizeAnalysis, trendAdjustments, trendLimits, validAnalysisState } from "../../../src/dashboard-analysis-model";
import { trendNote, trendQuery, trendSeries, trendTitle } from "../../../src/dashboard-trend-model";
import { tokenAnalysisCatalog as catalog } from "../../../src/token-analysis-catalog";
import { tokenTypes } from "../../../src/token-chart-request";
import { usageChartPresets } from "../../../src/usage-presets";
import { runAnalysisQuery } from "../../../src/analysis-request";

const { rows, hourly } = usageLedger(), schema = schemas.usage;
const base = { period: "day", hours: 6, days: 3, rollingPeriods: 0, scale: "linear", display: "interval" };
const range = [{ field: "last_usage_at", op: ">=", value: "-30d" }, { field: "first_usage_at", op: "<", value: "+0s" }];
function draw(state, { where = range, data = rows } = {}) {
  const query = trendQuery(catalog, state, where, { now });
  const results = core.evaluateMetrics(data, query.query, schema, { now }).metrics;
  return { query, built: trendSeries(catalog, state, query, results, { where, now }) };
}
const localDay = date => `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;

test("every Tokens card destination and Usage chart preset opens the chart it always did", () => {
  // The old Token chart: tokens or cost, as amounts or percentages, summed
  // by the period's field and the split; cost by token type is by provider.
  const legacy = chart => {
    const cost = chart.metric === "cost" || chart.metric === "percent_cost", split = cost && chart.split === "type" ? "provider" : chart.split;
    const field = ["hour", "hours"].includes(chart.period) ? "hour" : chart.period === "days" ? "day" : chart.period;
    return { metric: cost ? "cost" : "tokens", share: chart.metric.startsWith("percent"), split,
      clauses: split === "type" ? tokenTypes.map(type => [type.key, [field]]) : [[cost ? "cost_usd" : "total_tokens", [field, split]]] };
  };
  const charts = [...queryRecipes.filter(recipe => recipe.destination?.topic === "tokens").map(recipe => [recipe.id, recipe.destination.chart]),
    ...usageChartPresets.map(preset => [preset.label, { ...base, ...preset.chart }]),
    ...["tokens", "cost", "percent", "percent_cost"].map(metric => [`type ${metric}`, { ...base, metric, split: "type" }])];
  assert.ok(charts.length > 30);
  for (const [name, chart] of charts) {
    assert.ok(validAnalysisState(catalog, chart), name);
    const state = normalizeAnalysis(catalog, chart), expected = legacy(chart);
    assert.deepEqual([state.view, state.metric, state.share, state.split], ["trend", expected.metric, expected.share, expected.split], name);
    for (const key of ["period", "rollingPeriods", "display", "scale", "seriesLimit"]) if (chart[key] !== undefined) assert.equal(state[key], chart[key], `${name}: ${key}`);
    assert.deepEqual(trendQuery(catalog, chart, range, { now }).query.aggregations.map(clause => [clause.field, clause.groupBy]), expected.clauses, name);
    assert.deepEqual(trendAdjustments(catalog, chart).filter(note => !note.startsWith("Token type")), [], `${name}: nothing else moves`);
  }
});

test("components, aliases and defaults are checked with the catalog", () => {
  assert.deepEqual(catalogProblems(catalog), []);
  assert.deepEqual(catalogProblems({ ...catalog, components: { provider: { ...catalog.components.type, measures: ["cache_read_share"], fallback: "nowhere" } } }), [
    "Component breakdown provider shares its id with a breakdown.", "provider splits cache_read_share, which is not an additive measure.", "provider falls back to unknown breakdown nowhere."]);
  assert.equal(validAnalysisState(catalog, { ...base, metric: "words", split: "none" }), false);
  assert.equal(validAnalysisState(catalog, { ...base, metric: "tokens", split: "tool_name" }), false);
  assert.equal(validAnalysisState(catalog, { ...base, metric: "tokens", split: "type", rollingOp: "median" }), false);
  assert.equal(validAnalysisState(catalog, { ...base, metric: "tokens", split: "type", norm: "yes" }), false);
});

test("options a measure cannot use move to the nearest one, and say why", () => {
  const cases = [
    [{ metric: "cache_read_share", split: "provider", share: true }, { share: false }, /does not add up across groups/],
    [{ metric: "tokens", split: "none", share: true }, { share: false }, /Shares need a breakdown/],
    [{ metric: "conversations", split: "none", display: "cumulative" }, { display: "interval" }, /running count of distinct conversations/],
    [{ metric: "output_share", split: "none", rollingPeriods: 7, rollingOp: "sum" }, { rollingOp: "mean" }, /pools its window/],
    [{ metric: "agent_sessions", split: "none", period: "days", days: 3 }, { period: "day" }, /counted per hour, day, week or month/],
    [{ metric: "cost", split: "type" }, { split: "provider" }, /Token type splits tokens only/],
    [{ metric: "tokens", split: "provider", share: true, norm: true }, { norm: false }, /Shares have no norm/],
    [{ metric: "tokens", split: "none", display: "cumulative", norm: true }, { norm: false }, /running totals/],
    [{ metric: "cache_read_share", split: "provider", norm: true }, { norm: false }, /no total across groups/],
  ];
  for (const [asked, moved, reason] of cases) {
    const state = normalizeAnalysis(catalog, { ...base, ...asked });
    for (const [key, value] of Object.entries(moved)) assert.equal(state[key], value, `${JSON.stringify(asked)}: ${key}`);
    assert.match(trendAdjustments(catalog, { ...base, ...asked }).join(" "), reason, JSON.stringify(asked));
  }
  // What each measure allows.
  assert.equal(normalizeAnalysis(catalog, { ...base, metric: "conversations", split: "none", rollingPeriods: 7, rollingOp: "sum" }).rollingOp, "sum", "conversation-days are allowed");
  assert.equal(normalizeAnalysis(catalog, { ...base, metric: "cache_read_share", split: "none", display: "cumulative", norm: true }).display, "cumulative", "a running pooled ratio is allowed");
  assert.equal(normalizeAnalysis(catalog, { ...base, metric: "cache_read_share", split: "none", norm: true }).norm, true, "one ratio series has a norm");
  assert.equal(trendLimits(catalog, normalizeAnalysis(catalog, { ...base, metric: "tokens", split: "provider" })).share, undefined);
  // Coercion is pure: the same input normalizes the same way, twice.
  const once = normalizeAnalysis(catalog, { ...base, metric: "percent_cost", split: "type", display: "cumulative", norm: true });
  assert.deepEqual(normalizeAnalysis(catalog, once), once);
});

test("the query keeps the measure's population and smoothing history off the user's filters", () => {
  const where = [...range, { field: "provider", op: "=", value: "claude" }];
  const priced = trendQuery(catalog, { ...base, metric: "priced_cost", split: "provider", rollingPeriods: 7 }, where, { now });
  assert.ok(priced.query.where.some(term => term.field === "price_status"), "the population joins the query");
  assert.ok(!priced.range.some(term => term.field === "price_status") && !priced.requested.some(term => term.field === "price_status"), "and never the filters");
  assert.ok(!priced.query.where.some(term => term.field === "provider"), "a legend selection is left out so its alternatives stay in the key");
  assert.ok(priced.supporting.where.some(term => term.field === "provider") && priced.supporting.where.some(term => term.field === "price_status"));
  const lower = query => new Date(query.query.where.find(term => term.field === "last_usage_at" && term.op === ">=").value);
  const shifted = lower(priced), start = new Date(now - 30 * 86400000);
  start.setHours(0, 0, 0, 0); start.setDate(start.getDate() - 6);
  assert.equal(shifted.getTime(), start.getTime(), "six days of history before the range's first day");
  const ratio = trendQuery(catalog, { ...base, metric: "cache_read_share", split: "provider_model" }, where, { now });
  assert.deepEqual(ratio.query.aggregations.map(clause => [clause.op, clause.field, clause.groupBy]), [["sum", "cache_read_input_tokens", ["day", "provider", "model_family"]], ["sum", "input_tokens", ["day", "provider", "model_family"]]]);
  assert.ok(ratio.query.where.some(term => term.field === "provider"), "a two-field breakdown's selection is the chart's own, so a filter stays");
  assert.match(ratio.supporting.aggregations[0].expression, /SUM\(\[cache_read_input_tokens\]\)\/NULLIF/);
  const whole = trendQuery(catalog, { ...base, metric: "tokens", split: "none", completePeriods: true }, range, { now });
  const first = new Date(whole.range.find(term => term.op === ">=").value), midnight = new Date(now - 30 * 86400000);
  midnight.setHours(24, 0, 0, 0);
  assert.equal(first.getTime(), midnight.getTime(), "whole periods start at the next local midnight");
  const week = trendQuery(catalog, { ...base, period: "week", metric: "tokens", split: "none", completePeriods: true }, range, { now });
  assert.equal(new Date(week.range.find(term => term.op === ">=").value).getDay(), 1, "and whole weeks on a Monday");
});

test("distinct counts are per period: a rolling mean of daily counts, or a rolling sum of conversation-days", () => {
  const counts = new Map();
  for (const row of rows) { const day = row.day; counts.set(day, (counts.get(day) ?? new Set()).add(row.conversation_id)); }
  const daily = day => counts.get(day)?.size ?? 0;
  const mean = draw({ ...base, metric: "conversations", split: "none", rollingPeriods: 3 }).built;
  const sum = draw({ ...base, metric: "conversations", split: "none", rollingPeriods: 3, rollingOp: "sum" }).built;
  const local = key => { const [y, m, d] = key.split("-").map(Number); return new Date(y, m - 1, d); };
  for (const [index, bucket] of sum.buckets.entries()) {
    const end = local(bucket.key), window = [0, 1, 2].map(back => { const day = new Date(end); day.setDate(day.getDate() - back); return daily(localDay(day)); });
    const total = window.reduce((a, b) => a + b, 0);
    assert.equal(bucket.values.total ?? 0, total, bucket.key);
    assert.ok(Math.abs((mean.buckets[index].values.total ?? 0) - total / 3) < 1e-12, bucket.key);
  }
  assert.ok(sum.buckets.some(bucket => bucket.values.total > 0));
  assert.match(trendNote(catalog, { ...base, metric: "conversations", split: "none", rollingPeriods: 3, rollingOp: "sum" }), /conversation-days/);
  assert.match(trendNote(catalog, { ...base, metric: "conversations", split: "none", rollingPeriods: 7 }), /divides by 7/);
  // Groups of distinct counts do not add up, so they draw as lines and Other draws nothing.
  const folded = draw({ ...base, metric: "conversations", split: "repository_name", seriesLimit: 1 }).built;
  assert.equal(folded.lines, true);
  const other = folded.series.find(item => item.hiddenCount);
  assert.ok(other && /not drawn/.test(other.label));
  assert.ok(folded.buckets.every(bucket => bucket.values.other === undefined));
});

test("shares divide by every series, after smoothing; a selection hides series without changing the total", () => {
  const where = [...range, { field: "provider", op: "=", value: "codex" }];
  const { built } = draw({ ...base, metric: "tokens", split: "provider", share: true, rollingPeriods: 3 }, { where });
  assert.deepEqual(built.selected, ["s:codex"]);
  const busy = built.buckets.filter(bucket => Object.keys(bucket.values).length > 1);
  assert.ok(busy.length);
  for (const bucket of busy) assert.ok(Math.abs(Object.values(bucket.values).reduce((a, b) => a + b, 0) - 1) < 1e-12);
  assert.ok(busy.some(bucket => bucket.values["s:codex"] < 1), "codex's share is of every provider, not of itself");
  // Token type shares divide the parts of each period's tokens.
  const types = draw({ ...base, metric: "percent", split: "type" }).built;
  assert.deepEqual(types.series.map(item => item.key), tokenTypes.filter(type => type.key !== "unclassified_tokens").map(type => type.key), "an empty optional part is left out");
  const day = types.buckets.find(bucket => Object.keys(bucket.values).length);
  const parts = rows.filter(row => row.day === day.key);
  assert.ok(Math.abs(day.values.output_tokens - parts.reduce((n, row) => n + row.output_tokens, 0) / parts.reduce((n, row) => n + row.total_tokens, 0)) < 1e-12);
});

test("whole periods drop the current day; the norm averages every earlier drawn point", () => {
  const today = localDay(new Date(now));
  const all = draw({ ...base, metric: "tokens", split: "provider" }).built;
  const whole = draw({ ...base, metric: "tokens", split: "provider", completePeriods: true, norm: true, selection: { values: [], excluded: false } }).built;
  assert.equal(all.buckets.at(-1).key, today);
  assert.notEqual(whole.buckets.at(-1).key, today);
  assert.equal(whole.buckets.length, all.buckets.length - 2, "the partial first day and today are left out");
  const totals = whole.buckets.map(bucket => Object.values(bucket.values).reduce((a, b) => a + b, 0));
  assert.ok(Math.abs(whole.norm - totals.slice(0, -1).reduce((a, b) => a + b, 0) / (totals.length - 1)) < 1e-9);
  // Over the drawn series only.
  const where = [...range, { field: "provider", op: "=", value: "codex" }];
  const codex = draw({ ...base, metric: "tokens", split: "provider", completePeriods: true, norm: true }, { where }).built;
  const drawn = codex.buckets.slice(0, -1).map(bucket => bucket.values["s:codex"] ?? 0);
  assert.ok(Math.abs(codex.norm - drawn.reduce((a, b) => a + b, 0) / drawn.length) < 1e-9);
  assert.match(trendNote(catalog, { ...base, metric: "tokens", split: "none", completePeriods: true, norm: true }), /whole local days.*current day is left out.*norm is the mean/);
});

test("hourly trends read the hourly ledger; several hours add theirs", async () => {
  const where = [{ field: "last_usage_at", op: ">=", value: new Date(now - 7 * 86400000).toISOString() }];
  for (const [period, hours] of [["hour", 1], ["hours", 6]]) {
    const state = { ...base, period, hours, metric: "tokens", split: "none" }, query = trendQuery(catalog, state, where, { now });
    assert.deepEqual(query.query.aggregations[0].groupBy, ["hour"]);
    const results = await runAnalysisQuery("usage", schema, query.query, { local: { rows, hourly } });
    assert.ok(results[0].buckets.every(bucket => /T\d\d:00:00Z$/.test(bucket.keys[0])));
    const built = trendSeries(catalog, state, query, results, { where, now });
    const total = built.buckets.reduce((sum, bucket) => sum + (bucket.values.total ?? 0), 0);
    assert.equal(total, hourly.reduce((sum, row) => sum + row.total_tokens, 0), period);
  }
});

test("titles name the measure, period and breakdown", () => {
  assert.equal(trendTitle(catalog, { ...base, metric: "percent_cost", split: "model_family" }), "Share of API cost per day by model");
  assert.equal(trendTitle(catalog, { ...base, metric: "tokens", split: "type", display: "cumulative" }), "Cumulative tokens by token type");
  assert.equal(trendTitle(catalog, { ...base, metric: "conversations", split: "none" }), "Conversations reached per day");
  assert.equal(trendTitle(catalog, { ...base, metric: "percent", split: "type", display: "cumulative" }), "Cumulative share of tokens by token type");
});
