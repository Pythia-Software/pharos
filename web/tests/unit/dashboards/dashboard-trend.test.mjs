// Over time as a catalog-driven view: old links and presets, coercions,
// queries and the series builder, over the usage ledger.
import test from "node:test";
import assert from "node:assert/strict";
import { capabilities, core, execution, queryRecipes, schemas } from "../helpers/recipes.mjs";
import { now, usageLedger } from "../helpers/usage-ledger.mjs";
import { analysisInspectQuery, analysisLimits, analysisQuery, selectionWhere, catalogProblems, changeState, selectGroups, normalizeAnalysis, readAnalysisLink, analysisVersion, switchView, trendAdjustments, trendLimits, validAnalysisState } from "../../../src/dashboard-analysis-model";
import { trendNote, trendQuery, trendSeries, trendTitle } from "../../../src/dashboard-trend-model";
import { tokenAnalysisCatalog as catalog } from "../../../src/token-analysis-catalog";
import { tokenTypes } from "../../../src/token-chart-request";
import { usageChartPresets } from "../../../src/usage-presets";
import { runAnalysisQuery } from "../../../src/analysis-request";
import { dashboardQueryLinks } from "../../../src/dashboard-query-links";
import { usageDashboardReturn } from "../../../src/usage-workspace-model";

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
    // Only cost by token type moves, and the note says where.
    const notes = trendAdjustments(catalog, chart);
    if (expected.split !== chart.split) assert.deepEqual(notes, ["Token type splits tokens only; API cost (USD) is shown by provider."], name);
    else assert.deepEqual(notes, [], `${name}: nothing moves`);
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
  for (const bad of [{ period: "fortnight" }, { hours: 0 }, { days: 1.5 }, { rollingPeriods: -1 }, { display: "stacked" }, { scale: "cubic" }, { seriesLimit: 0 }, { selection: { values: [1], excluded: false } }]) {
    assert.equal(validAnalysisState(catalog, { ...base, metric: "tokens", split: "none", ...bad }), false, JSON.stringify(bad));
  }
  assert.deepEqual(catalogProblems({ ...catalog, measures: { ...catalog.measures, mean: { ...catalog.measures.tokens, op: "avg", additive: false } } }), ["mean is a mean; declare it as a ratio of a sum and a count so periods and windows pool."]);
  // A dataset without an hour field: hours are offered disabled and a link to them draws days.
  const daily = { ...catalog, periods: { day: "day", week: "week", month: "month" } };
  assert.equal(normalizeAnalysis(daily, { ...base, metric: "tokens", split: "none", period: "hour" }).period, "day");
  assert.match(trendAdjustments(daily, { ...base, metric: "tokens", split: "none", period: "hours" }).join(" "), /no hour to group by; each bucket is one day/);
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
  const where = range, selection = { values: ["claude"], excluded: false };
  const priced = trendQuery(catalog, { ...base, metric: "priced_cost", split: "provider", rollingPeriods: 7, selection }, where, { now });
  assert.ok(priced.query.where.some(term => term.field === "price_status"), "the population joins the query");
  assert.ok(!priced.range.some(term => term.field === "price_status") && !priced.requested.some(term => term.field === "price_status"), "and never the filters");
  assert.ok(!priced.query.where.some(term => term.field === "provider"), "a legend selection is the view's own, so every series stays in the key");
  assert.ok(priced.supporting.where.some(term => term.field === "provider") && priced.supporting.where.some(term => term.field === "price_status"), "the supporting query covers the selected series");
  const population = trendQuery(catalog, { ...base, metric: "tokens", split: "provider" }, [...range, { field: "provider", op: "=", value: "claude" }], { now });
  assert.ok(population.query.where.some(term => term.field === "provider"), "a filter on the breakdown's field is the population");
  const lower = query => new Date(query.query.where.find(term => term.field === "last_usage_at" && term.op === ">=").value);
  const shifted = lower(priced), start = new Date(now - 30 * 86400000);
  start.setHours(0, 0, 0, 0); start.setDate(start.getDate() - 6);
  assert.equal(shifted.getTime(), start.getTime(), "six days of history before the range's first day");
  const ratio = trendQuery(catalog, { ...base, metric: "cache_read_share", split: "provider_model" }, [...range, { field: "provider", op: "=", value: "claude" }], { now });
  assert.deepEqual(ratio.query.aggregations.map(clause => [clause.op, clause.field, clause.groupBy]), [["sum", "cache_read_input_tokens", ["day", "provider", "model_family"]], ["sum", "input_tokens", ["day", "provider", "model_family"]]]);
  assert.ok(ratio.query.where.some(term => term.field === "provider"), "a filter stays the population");
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
  const { built } = draw({ ...base, metric: "tokens", split: "provider", share: true, rollingPeriods: 3, selection: { values: ["codex"], excluded: false } });
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

test("whole periods drop the current day and a partial first period, also from running totals", () => {
  const today = localDay(new Date(now));
  const all = draw({ ...base, metric: "tokens", split: "provider" }).built;
  const whole = draw({ ...base, metric: "tokens", split: "provider", completePeriods: true }).built;
  assert.equal(all.buckets.at(-1).key, today);
  assert.notEqual(whole.buckets.at(-1).key, today);
  assert.equal(whole.buckets.length, all.buckets.length - 2, "the partial first day and today are left out");
  assert.match(trendNote(catalog, { ...base, metric: "tokens", split: "none", completePeriods: true }), /whole local days.*current day is left out/);
  // Three-day buckets from a mid-day start. Independently of the chart: the
  // range starts at the next local midnight; three-day buckets are aligned to
  // calendar day numbers; the first whole one starts on or after that
  // midnight, and the last whole one ends by now.
  const from = new Date(now - 5 * 86400000), where = [{ field: "last_usage_at", op: ">=", value: from.toISOString() }];
  const midnight = new Date(from); midnight.setHours(24, 0, 0, 0);
  const dayNumber = date => Date.UTC(date.getFullYear(), date.getMonth(), date.getDate()) / 86400000;
  const first = new Date(midnight); while (dayNumber(first) % 3) first.setDate(first.getDate() + 1);
  const expected = [];
  for (let start = new Date(first); ; start.setDate(start.getDate() + 3)) { const end = new Date(start); end.setDate(end.getDate() + 3); if (end.getTime() > now) break; expected.push(localDay(start)); }
  const end = new Date(first); end.setDate(end.getDate() + 3 * expected.length);
  const { built } = draw({ ...base, metric: "tokens", split: "none", period: "days", days: 3, display: "cumulative", completePeriods: true }, { where });
  assert.deepEqual(built.buckets.map(bucket => bucket.key), expected);
  const local = key => { const [y, m, d] = key.split("-").map(Number); return new Date(y, m - 1, d); };
  assert.ok(rows.some(row => local(row.day) >= midnight && local(row.day) < first), "the fixture has records in the partial first period");
  assert.equal(built.buckets.at(-1).values.total, rows.filter(row => local(row.day) >= first && local(row.day) < end).reduce((sum, row) => sum + row.total_tokens, 0));
  // What "View supporting query" opens is what is drawn.
  const supporting = trendQuery(catalog, { ...base, metric: "tokens", split: "none", period: "days", days: 3, completePeriods: true }, where, { now }).supporting.where;
  assert.equal(new Date(supporting.find(term => term.op === ">=").value).getTime(), first.getTime(), "it starts at the first drawn bucket, not the rounded midnight");
  const current = new Date(now); current.setHours(0, 0, 0, 0); while (dayNumber(current) % 3) current.setDate(current.getDate() - 1);
  assert.equal(new Date(supporting.find(term => term.op === "<").value).getTime(), current.getTime(), "it ends where the current partial bucket begins");
});

test("results that may be incomplete are an error, never idle periods", () => {
  const state = { ...base, metric: "cache_read_share", split: "provider" };
  const query = trendQuery(catalog, state, range, { now });
  const exact = core.evaluateMetrics(rows, query.query, schema, { now }).metrics;
  assert.equal(trendSeries(catalog, state, query, exact, { where: range, now }).error, undefined);
  for (const [name, broken] of [
    ["partial coverage", results => results.map(result => ({ ...result, coverage: "partial" }))],
    ["a truncated group count", results => results.map((result, index) => index ? { ...result, groupCount: result.buckets.length + 1 } : result)],
    ["a bucket error", results => results.map((result, index) => index ? { ...result, buckets: [...result.buckets.slice(1), { ...result.buckets[0], error: "overflow" }] } : result)],
    ["a missing denominator", results => results.slice(0, 1)],
  ]) assert.ok(trendSeries(catalog, state, query, broken(exact), { where: range, now }).error, name);
});

test("a ratio may pool a sum over a count: a mean per timed record", () => {
  const meanCost = { label: "Mean cost per priced record", unit: "USD", format: { kind: "number", decimals: 2 }, op: "sum", ratio: ["cost_usd", { op: "count", field: "cost_usd" }], additive: false, note: "" };
  const means = { ...catalog, measures: { ...catalog.measures, mean_cost: meanCost } };
  const day = offset => { const at = new Date(now); at.setHours(6, 0, 0, 0); at.setDate(at.getDate() - offset); return at; };
  const row = (id, at, cost) => ({ ...rows[0], id, day: localDay(at), first_usage_at: at.toISOString(), last_usage_at: at.toISOString(), cost_usd: cost });
  // One 10-dollar record yesterday, 99 at 100 today, and one unpriced.
  const data = [row("one", day(1), 10), ...Array.from({ length: 99 }, (_, index) => row(`many-${index}`, day(0), 100)), row("unpriced", day(0), null)];
  const state = { ...base, metric: "mean_cost", split: "none", rollingPeriods: 2 };
  const query = trendQuery(means, state, range, { now });
  assert.deepEqual(query.query.aggregations.map(clause => [clause.op, clause.field]), [["sum", "cost_usd"], ["count", "cost_usd"]]);
  const built = trendSeries(means, state, query, core.evaluateMetrics(data, query.query, schema, { now }).metrics, { where: range, now });
  assert.ok(Math.abs(built.buckets.at(-1).values.total - 9910 / 100) < 1e-12, `pooled ${built.buckets.at(-1).values.total}, not the 55 of averaged means`);
  assert.match(trendNote(means, state), /summed cost usd divided by the count of cost usd/);
  const units = analysisQuery(means, { view: "compare", unit: "conversation", metric: "mean_cost", split: "none", reduce: "total" }, []);
  assert.deepEqual(units.query.grouping.summaries.slice(1).map(column => [column.op, column.field]), [["sum", "cost_usd"], ["count", "cost_usd"]], "units pool the same parts");
});

test("a distinct count's drill-down opens only the records it counted", () => {
  const query = trendQuery(catalog, { ...base, metric: "delegated_runs", split: "none" }, range, { now });
  const opened = core.applyQuery(rows, { ...core.EMPTY_QUERY, where: query.records, limit: 1000 }, schema, { now }).rows;
  assert.ok(opened.length > 0 && opened.length < rows.length);
  assert.ok(opened.every(row => row.delegated_session_id), "root records hold no delegated run");
  assert.deepEqual(trendQuery(catalog, { ...base, metric: "priced_cost", split: "none" }, range, { now }).records, [{ field: "price_status", op: "=", value: "priced" }]);
});

test("a change applies to the state as asked for: old names never override it, and coerced settings return", () => {
  for (const alias of ["percent", "percent_cost"]) {
    const link = { ...base, metric: alias, split: "provider" };
    const values = normalizeAnalysis(catalog, changeState(catalog, link, { share: false }));
    assert.deepEqual([values.metric, values.share], [alias === "percent" ? "tokens" : "cost", false], `${alias}: Values takes effect at once`);
    assert.equal(normalizeAnalysis(catalog, changeState(catalog, link, { split: "model_family" })).share, true, `${alias}: other changes keep the shares`);
  }
  const asked = { ...base, metric: "tokens", split: "provider", share: true, rollingPeriods: 7, rollingOp: "sum", norm: true };
  const ratio = changeState(catalog, asked, { metric: "cache_read_share" });
  assert.deepEqual(["share", "rollingOp", "norm"].map(key => normalizeAnalysis(catalog, ratio)[key]), [false, "mean", false], "drawn without what the ratio cannot use");
  assert.equal(trendAdjustments(catalog, ratio).length, 3, "and each change is explained");
  const back = changeState(catalog, ratio, { metric: "tokens" });
  assert.deepEqual(["share", "rollingOp", "norm"].map(key => back[key]), [true, "sum", true], "all three are still saved");
  assert.deepEqual(normalizeAnalysis(catalog, back), normalizeAnalysis(catalog, asked), "and tokens draws exactly as before");
  // Shares have no norm, so without them the saved norm draws again.
  assert.equal(normalizeAnalysis(catalog, changeState(catalog, back, { share: false })).norm, true);
  // A visit to Compare keeps the trend's own settings.
  const returned = switchView(catalog, switchView(catalog, asked, "compare"), "trend");
  assert.deepEqual(["share", "rollingOp", "norm", "rollingPeriods"].map(key => returned[key]), [true, "sum", true, 7]);
});

test("a share is of every group in the filters; a legend selection only chooses what is drawn, in the trend and in Compare", () => {
  // The smallest provider, selected in the legend.
  const totals = new Map();
  for (const row of rows) totals.set(row.provider, (totals.get(row.provider) ?? 0) + row.total_tokens);
  const smallest = [...totals].sort((a, b) => a[1] - b[1])[0][0], all = [...totals.values()].reduce((a, b) => a + b, 0);
  const selection = { values: [smallest], excluded: false }, where = range;
  const trend = draw({ ...base, metric: "tokens", split: "provider", share: true, display: "cumulative", seriesLimit: 1, selection }).built;
  assert.ok(trend.series.some(item => item.value === smallest), "a selected group is never folded into Other");
  assert.deepEqual(trend.selected, [`s:${smallest}`]);
  assert.ok(Math.abs(trend.buckets.at(-1).values[`s:${smallest}`] - totals.get(smallest) / all) < 1e-12);
  const compare = analysisQuery(catalog, { view: "compare", metric: "tokens", split: "provider", compareAs: "share", selection }, where, { now });
  assert.deepEqual(compare.selection, { values: [smallest], excluded: false, fields: 1, share: true });
  assert.ok(!compare.query.where.some(term => term.field === "provider"), "the denominator is every provider");
  assert.ok(compare.supporting.grouping.source.where.some(term => term.field === "provider"), "the supporting query keeps the selection");
  const pie = selectGroups(core.evaluateMetrics(rows, compare.query, schema, { now }).metrics[0], compare.selection);
  assert.deepEqual(pie.buckets.map(bucket => bucket.keys[0]), [smallest], "the selected group is drawn");
  assert.equal(pie.other.value, all - totals.get(smallest), "every other provider is Other");
  assert.ok(Math.abs(pie.buckets[0].value / (pie.buckets[0].value + pie.other.value) - trend.buckets.at(-1).values[`s:${smallest}`]) < 1e-12, "the trend and Compare agree");
  // An excluded group: every other group is drawn, the excluded one is Other.
  const excluded = analysisQuery(catalog, { view: "compare", metric: "tokens", split: "provider", compareAs: "share", selection: { values: [smallest], excluded: true } }, range, { now });
  assert.deepEqual(excluded.selection, { values: [smallest], excluded: true, fields: 1, share: true });
  assert.equal(selectGroups(core.evaluateMetrics(rows, excluded.query, schema, { now }).metrics[0], excluded.selection).other.value, totals.get(smallest));
  // Combined groups keep the trend's own selection; so do unit counts.
  const pair = JSON.stringify(["claude", "opus"]);
  const multi = analysisQuery(catalog, { view: "compare", metric: "tokens", split: "provider_model", compareAs: "share", selection: { values: [pair], excluded: false } }, range, { now });
  assert.deepEqual(selectGroups(core.evaluateMetrics(rows, multi.query, schema, { now }).metrics[0], multi.selection).buckets.map(bucket => JSON.stringify(bucket.keys)), [pair]);
  const sessions = analysisQuery(catalog, { view: "compare", unit: "agent_session", reduce: "count", metric: "tokens", split: "provider", compareAs: "share", selection }, where, { now });
  assert.deepEqual(sessions.selection, { values: [smallest], excluded: false, fields: 1, share: true });
  assert.ok(!sessions.query.grouping.source.where.some(term => term.field === "provider") && sessions.supporting.where.some(term => term.field === "provider"), "the supporting query keeps the selected group");
  const counted = selectGroups(core.evaluateMetrics(rows, sessions.query, schema, { now }).metrics[0], sessions.selection);
  assert.equal(counted.buckets[0].value + counted.other.value, new Set(rows.map(row => row.agent_session_id)).size, "unit shares are of every session");
  // Outside shares there is no denominator to protect: the selection
  // filters the executed query, so no group limit can crowd it out.
  const bars = analysisQuery(catalog, { view: "compare", metric: "tokens", split: "provider", selection }, where, { now });
  assert.ok(bars.query.where.some(term => term.field === "provider"));
  assert.deepEqual(selectGroups(core.evaluateMetrics(rows, bars.query, schema, { now }).metrics[0], bars.selection).buckets.map(bucket => [bucket.keys[0], bucket.value]), [[smallest, totals.get(smallest)]]);
  // A population filter on the breakdown's own field is a population: its
  // shares are of the filtered groups only.
  const filtered = analysisQuery(catalog, { view: "compare", metric: "tokens", split: "provider", compareAs: "share" }, [...range, { field: "provider", op: "=", value: smallest }], { now });
  assert.equal(filtered.selection, undefined);
  assert.deepEqual(core.evaluateMetrics(rows, filtered.query, schema, { now }).metrics[0].buckets.map(bucket => bucket.keys[0]), [smallest]);
});

test("links: an unversioned legend filter becomes the selection; a versioned link keeps its filters", () => {
  const filter = { field: "provider", op: "=", value: "codex" };
  // An old Tokens link: the legend wrote codex as a filter on the breakdown's field.
  const old = readAnalysisLink(catalog, { metric: "tokens", split: "provider" }, [...range, filter]);
  assert.deepEqual(old.state, { v: analysisVersion, metric: "tokens", split: "provider", selection: { values: ["codex"], excluded: false } });
  assert.deepEqual(old.where, range);
  const excluded = readAnalysisLink(catalog, { metric: "tokens", split: "provider" }, [...range, { any: [{ field: "provider", op: "!=", value: "codex" }, { field: "provider", op: "is_null", value: "" }] }]);
  assert.deepEqual([excluded.state.selection, excluded.where], [{ values: ["codex"], excluded: true }, range]);
  // A filter on another field, or under a breakdown of several fields, stays.
  assert.deepEqual(readAnalysisLink(catalog, { metric: "tokens", split: "model_family" }, [...range, filter]).where, [...range, filter]);
  assert.deepEqual(readAnalysisLink(catalog, { metric: "percent", split: "provider_model" }, [...range, filter]).where, [...range, filter]);
  // A versioned link, as cards write them: the card's filter is its population.
  const card = queryRecipes.find(recipe => recipe.destination?.topic === "tokens").destination.chart;
  assert.equal(card.v, analysisVersion);
  const current = readAnalysisLink(catalog, { ...card, split: "provider" }, [...range, filter]);
  assert.deepEqual([current.where, current.state.selection], [[...range, filter], card.selection]);
  // A round trip: the migrated link reads the same again, and draws what the
  // old link drew, the codex series among every provider.
  assert.deepEqual(readAnalysisLink(catalog, old.state, old.where), old);
  const drawn = draw(old.state, { where: old.where }).built;
  assert.deepEqual(drawn.selected, ["s:codex"]);
  assert.ok(drawn.series.length > 2, "the other providers stay in the key");
  // A dashboard whose old chart had no legend selection keeps every filter.
  assert.deepEqual(readAnalysisLink({ ...catalog, legacySlices: false }, { metric: "tokens", split: "provider" }, [...range, filter]).where, [...range, filter]);
});

test("a group only smoothing or norm history holds is listed in Other, so series add up to the total", () => {
  // tl1 is used 3 and 12 days ago, never in the drawn week.
  const at = days => { const date = new Date(now - days * 86400000); return { ...rows[0], id: `tl1-${days}`, provider: "tl1", day: localDay(date), first_usage_at: date.toISOString(), last_usage_at: date.toISOString(), total_tokens: 5e6 }; };
  const data = [...rows, at(9), at(12)], week = [{ field: "last_usage_at", op: ">=", value: "-7d" }, { field: "first_usage_at", op: "<", value: "+0s" }];
  const state = { ...base, metric: "tokens", rollingPeriods: 7, norm: true };
  const byProvider = draw({ ...state, split: "provider" }, { where: week, data }).built, total = draw({ ...state, split: "none" }, { where: week, data }).built;
  assert.deepEqual(byProvider.series.find(item => item.key === "other")?.foldedValues, ["tl1"], "Other lists the history-only group");
  assert.ok(Math.abs(byProvider.norm - total.norm) < 1e-9, `norm by provider ${byProvider.norm} != ${total.norm}`);
  assert.ok(total.buckets.some(bucket => bucket.values.total > 0));
  byProvider.buckets.forEach((bucket, index) => {
    const stacked = byProvider.series.reduce((sum, item) => sum + (bucket.values[item.key] ?? 0), 0);
    assert.ok(Math.abs(stacked - (total.buckets[index].values.total ?? 0)) < 1e-6, bucket.key);
  });
  const shares = draw({ ...state, split: "provider", share: true, norm: false }, { where: week, data }).built;
  for (const bucket of shares.buckets) {
    const sum = shares.series.reduce((total, item) => total + (bucket.values[item.key] ?? 0), 0);
    assert.ok(sum === 0 || Math.abs(sum - 1) < 1e-12, `${bucket.key}: drawn shares add up to ${sum}`);
  }
});

test("with whole periods, the partial last period does not rank the series", () => {
  const today = new Date(now - 3600000), burst = { ...rows[0], id: "burst", provider: "chatgpt", day: localDay(today), first_usage_at: today.toISOString(), last_usage_at: today.toISOString(), total_tokens: 1e12 };
  const built = draw({ ...base, metric: "tokens", split: "provider", seriesLimit: 1, completePeriods: true }, { data: [...rows, burst] }).built;
  assert.notEqual(built.series[0].value, "chatgpt");
  assert.ok(!built.series.some(item => item.foldedValues?.includes("chatgpt")), "it adds nothing that is drawn");
});

test("Compare maps a selection through the unit: provider selects top-level provider groups of conversations", () => {
  const where = range;
  const conversations = analysisQuery(catalog, { view: "compare", unit: "conversation", reduce: "count", metric: "tokens", split: "provider", compareAs: "share", selection: { values: ["codex"], excluded: false } }, where, { now });
  assert.deepEqual(conversations.selection, { values: ["codex"], excluded: false, mapped: "root_provider", fields: 1, share: true });
  assert.ok(!conversations.query.grouping.source.where.some(term => term.field === "provider"), "never a population filter on record provider");
  const pie = selectGroups(core.evaluateMetrics(rows, conversations.query, schema, { now }).metrics[0], conversations.selection);
  const roots = new Map(rows.map(row => [row.root_conversation_id, row.root_provider]));
  assert.deepEqual(pie.buckets.map(bucket => [bucket.keys[0], bucket.value]), [["codex", [...roots.values()].filter(provider => provider === "codex").length]]);
  assert.equal(pie.buckets[0].value + pie.other.value, roots.size, "every conversation is in the denominator");
  assert.deepEqual(conversations.supporting.where.filter(term => term.field === "root_provider"), [{ field: "root_provider", op: "=", value: "codex" }], "the supporting query lists the selected group's conversations");
});

test("a selected group beyond the returned groups is reported, not silently dropped", () => {
  const entry = { id: "analysis", groupCount: 1001, buckets: [{ keys: ["repo-0"], value: 10, count: 1 }], other: { keys: [], value: 5, count: 3 } };
  const shown = selectGroups(entry, { values: ["repo-0", "repo-1001"], excluded: false, fields: 1, share: true });
  assert.deepEqual([shown.buckets.length, shown.other.value, shown.missing], [1, 5, 1]);
  assert.equal(selectGroups({ ...entry, groupCount: 1, other: undefined }, { values: ["repo-0", "unused"], excluded: false, fields: 1, share: true }).missing, undefined, "a complete result has nothing beyond it");
  const bars = selectGroups(entry, { values: ["repo-0", "repo-1001"], excluded: false, fields: 1, share: false });
  assert.deepEqual([bars.buckets.length, bars.other, bars.missing], [1, undefined, 1], "bars hide the rest");
});

test("while results load there is no error", () => {
  const state = { ...base, metric: "cache_read_share", split: "provider" }, query = trendQuery(catalog, state, range, { now });
  const loading = trendSeries(catalog, state, query, [], { where: range, now });
  assert.deepEqual([loading.error, loading.series.length, loading.buckets.length], [undefined, 0, 0]);
});

test("the drawn periods alone choose the series; history only follows them", () => {
  // Repository repo-9 is busy three weeks ago and idle in the last week.
  const old = new Date(now - 20 * 86400000), busy = { ...rows[0], id: "busy", repository_name: "repo-9", day: localDay(old), first_usage_at: old.toISOString(), last_usage_at: old.toISOString(), total_tokens: 1e9 };
  const week = [{ field: "last_usage_at", op: ">=", value: "-7d" }, { field: "first_usage_at", op: "<", value: "+0s" }];
  const series = norm => draw({ ...base, metric: "tokens", split: "repository_name", seriesLimit: 1, rollingPeriods: 3, norm }, { where: week, data: [...rows, busy] }).built.series.map(item => item.value ?? item.key);
  assert.deepEqual(series(true), series(false));
  assert.ok(!series(true).includes("repo-9"));
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

test("a selection outside shares is in the executed query: a scatter's selected group is drawn whatever the point limit", t => {
  const prior = analysisLimits.points;
  analysisLimits.points = 3;
  t.after(() => { analysisLimits.points = prior; });
  // Codex's two conversations are far from the three largest by tokens.
  const state = { view: "scatter", unit: "conversation", metric: "tokens", metricY: "cost", split: "provider", selection: { values: ["codex"], excluded: false } };
  const all = core.evaluateMetrics(rows, analysisQuery(catalog, { ...state, selection: undefined }, range, { now }).query, schema, { now }).metrics[0];
  assert.ok(!all.buckets.some(bucket => bucket.keys[0] === "codex"), "the overall top three hold none of them");
  const scatter = analysisQuery(catalog, state, range, { now });
  assert.deepEqual(scatter.query.where.filter(term => term.field === "root_provider"), [{ field: "root_provider", op: "=", value: "codex" }], "mapped through the unit's field, as a group filter");
  const drawn = selectGroups(core.evaluateMetrics(rows, scatter.query, schema, { now }).metrics[0], scatter.selection);
  assert.deepEqual(drawn.buckets.map(bucket => bucket.keys[0]), ["codex", "codex"]);
  // Records filter on the breakdown's own field.
  const box = analysisQuery(catalog, { view: "distribution", metric: "tokens", split: "provider", selection: { values: ["codex"], excluded: true } }, range, { now });
  assert.ok(box.query.where.some(term => term.any?.some(clause => clause.field === "provider" && clause.op === "!=")));
});

test("View supporting query, then Back to dashboard, returns the dashboard's own filters and selection", () => {
  // Over time by provider, as shares, with codex selected.
  const chart = { v: analysisVersion, metric: "tokens", split: "provider", share: true, period: "day", selection: { values: ["codex"], excluded: false } };
  const dashboard = new URLSearchParams({ analyze_mode: "dashboards", analyze_topic: "tokens", analyze_data: "usage", analyze_chart: JSON.stringify(chart), analyze_filters: core.encodeQuery({ ...core.EMPTY_QUERY, where: range }) });
  const supporting = trendQuery(catalog, chart, range, { now }).supporting;
  assert.ok(supporting.where.some(term => term.field === "provider"), "the supporting query lists the selected series");
  for (const link of dashboardQueryLinks("usage", supporting, schema, dashboard)) {
    const opened = new URL(link.href, "http://localhost").searchParams;
    const back = new URL(usageDashboardReturn(opened).href, "http://localhost").searchParams;
    assert.equal(back.get("analyze_filters"), dashboard.get("analyze_filters"), link.title);
    const read = readAnalysisLink(catalog, JSON.parse(back.get("analyze_chart")), core.decodeQueryStrict(back.get("analyze_filters")).where);
    assert.deepEqual([read.state.selection, read.where], [chart.selection, range], `${link.title}: codex is still a selection among every provider`);
    // An edit in the table is kept; what the link added is not.
    const edited = core.decodeQueryStrict(opened.get("q_usage")), extra = { field: "effort", op: "=", value: "high" };
    const source = edited.grouping ? { ...edited, grouping: { ...edited.grouping, source: { ...edited.grouping.source, where: [...edited.grouping.source.where, extra] } } } : { ...edited, where: [...edited.where, extra] };
    opened.set("q_usage", core.encodeQuery(source));
    assert.deepEqual(core.decodeQueryStrict(new URL(usageDashboardReturn(opened).href, "http://localhost").searchParams.get("analyze_filters")).where, [...range, extra], link.title);
  }
});


test("a combined selection filters the executed query where a filter can state it", t => {
  const prior = analysisLimits.points;
  analysisLimits.points = 3;
  t.after(() => { analysisLimits.points = prior; });
  const pairs = [["codex", "gpt-5"], ["claude", "sonnet"]].map(pair => JSON.stringify(pair));
  const state = { view: "scatter", unit: "conversation", metric: "tokens", metricY: "cost", split: "provider_model", selection: { values: pairs, excluded: false } };
  const scatter = analysisQuery(catalog, state, range, { now });
  assert.deepEqual(core.toQueryRequestV3(scatter.query, schema, capabilities, execution, { now, rows: false }).diagnostics, [], "an OR across two fields is a valid filter");
  const drawn = selectGroups(core.evaluateMetrics(rows, scatter.query, schema, { now }).metrics[0], scatter.selection);
  const roots = rows.filter(row => pairs.includes(JSON.stringify([row.root_provider, row.root_model_family])));
  assert.deepEqual(new Set(drawn.buckets.map(bucket => bucket.keys.at(-1))).size, Math.min(3, new Set(roots.map(row => row.root_conversation_id)).size));
  assert.ok(drawn.buckets.every(bucket => pairs.includes(JSON.stringify(bucket.keys.slice(0, 2)))), "only the selected pairs, never codex sonnet or claude gpt-5");
  // Excluding pairs is one OR per pair, missing values included.
  const kept = analysisQuery(catalog, { view: "compare", metric: "tokens", split: "provider_model", selection: { values: pairs, excluded: true } }, range, { now });
  const totals = core.evaluateMetrics(rows, kept.query, schema, { now }).metrics[0].buckets.map(bucket => JSON.stringify(bucket.keys));
  assert.ok(totals.length > 0 && totals.every(key => !pairs.includes(key)));
  // Included pairs are one OR of AND branches, however many: the executed
  // query, the supporting query and a bucket's records all carry it.
  const many = Array.from({ length: 30 }, (_, index) => JSON.stringify(index ? ["claude", `model-${index}`] : ["codex", "gpt-5"]));
  const term = { any: many.map(pair => ({ all: [{ field: "provider", op: "=", value: JSON.parse(pair)[0] }, { field: "model_family", op: "=", value: JSON.parse(pair)[1] }] })) };
  assert.deepEqual(selectionWhere(["provider", "model_family"], { values: many, excluded: false }), [term]);
  const compare = analysisQuery(catalog, { view: "compare", metric: "tokens", split: "provider_model", selection: { values: many, excluded: false } }, range, { now });
  assert.ok(compare.query.where.some(item => JSON.stringify(item) === JSON.stringify(term)) && !compare.selection.late);
  assert.deepEqual(core.toQueryRequestV3(compare.query, schema, capabilities, execution, { now, rows: false }).diagnostics, []);
  assert.deepEqual(core.evaluateMetrics(rows, compare.query, schema, { now }).metrics[0].buckets.map(bucket => bucket.keys), [["codex", "gpt-5"]], "only the selected pairs that hold usage");
  assert.ok(compare.supporting.grouping.source.where.some(item => JSON.stringify(item) === JSON.stringify(term)));
  const opened = analysisInspectQuery(compare, { keys: ["codex", "gpt-5"], value: 1, count: 1 }, compare.clause, schema, "compare");
  assert.ok(opened.where.some(item => JSON.stringify(item) === JSON.stringify(term)));
  // Links carry the term as it is.
  for (const query of [compare.supporting, opened]) assert.deepEqual(core.decodeQueryStrict(core.encodeQuery(query)), core.decodeQueryStrict(core.encodeQuery(core.decodeQueryStrict(core.encodeQuery(query)))));
  assert.deepEqual(core.decodeQueryStrict(core.encodeQuery(opened)).where, opened.where);
  // The trend draws the selection itself; its supporting query lists it too.
  const trend = trendQuery(catalog, { metric: "tokens", split: "provider_model", period: "day", selection: { values: many, excluded: false } }, range, { now });
  assert.ok(trend.supporting.where.some(item => JSON.stringify(item) === JSON.stringify(term)));
  assert.ok(!trend.query.where.some(item => item.any), "never the executed query: the key keeps every series");
  // Every predicate counts toward the query's 100, and a bar's drill-down
  // adds one per key: a selection is a filter only while the drill-down
  // still fits, so a link never drops the bar's own keys.
  const pairs$ = count => Array.from({ length: count }, (_, index) => JSON.stringify(["claude", `m${index}`]));
  const inspect = analysis => analysisInspectQuery(analysis, { keys: ["claude", "m3"], value: 1, count: 1 }, analysis.clause, schema, "compare");
  const fits = analysisQuery(catalog, { view: "compare", metric: "tokens", split: "provider_model", selection: { values: pairs$(48), excluded: false } }, range, { now });
  assert.ok(!fits.selection.late);
  const bar = inspect(fits), linked = core.decodeQueryStrict(core.encodeQuery(bar));
  assert.equal(bar.where.reduce((n, item) => n + core.predicatesOf(item).length, 0), 100);
  assert.deepEqual(linked.where, bar.where, "the drill-down survives its link");
  assert.ok(linked.where.some(item => item.field === "model_family" && item.value === "m3"));
  // 49 pairs and two bounds are 100 predicates: no room for the bar's keys.
  const late = analysisQuery(catalog, { view: "compare", metric: "tokens", split: "provider_model", selection: { values: pairs$(49), excluded: false } }, range, { now });
  assert.ok(late.selection.late && !late.query.where.some(item => item.any), "drawn from the returned groups, and the note says so");
  assert.equal(late.supporting.grouping.source.where.length, 2, "nor can the supporting query state it");
  const lateBar = inspect(late);
  assert.deepEqual(core.decodeQueryStrict(core.encodeQuery(lateBar)).where, [...range, { field: "provider", op: "=", value: "claude" }, { field: "model_family", op: "=", value: "m3" }]);
  // Units: the group filter leaves room for the bar's keys beside the value filter.
  const units = analysisQuery(catalog, { view: "compare", unit: "conversation", metric: "tokens", split: "provider_model", reduce: "mean", selection: { values: pairs$(49), excluded: false } }, range, { now });
  assert.ok(units.selection.late, "48 pairs, one value filter and two keys would be 99; 49 pairs are 101");
  const unitFits = analysisQuery(catalog, { view: "compare", unit: "conversation", metric: "tokens", split: "provider_model", reduce: "mean", selection: { values: pairs$(48), excluded: false } }, range, { now });
  const group = inspect(unitFits);
  assert.deepEqual(core.decodeQueryStrict(core.encodeQuery(group)).where, group.where);
  assert.ok(group.where.some(item => item.field === "root_model_family" && item.value === "m3"));
  // A drill-down that would exceed the budget anyway has none.
  const over = { ...fits, query: { ...fits.query, where: [...fits.query.where, { field: "effort", op: "=", value: "high" }] } };
  assert.equal(inspect(over), null);
});

test("a derived component computes each period's parts from its input totals, before smoothing and shares", () => {
  // Parts derived from per-provider token totals and a parameter, as Carbon
  // derives token types from per-tier totals and the user's factors.
  const factor = 2.5, derive = (totals, metric) => {
    assert.equal(metric, "tokens");
    const sum = (field, keep = () => true) => totals.filter(entry => keep(entry.keys[0])).reduce((total, entry) => total + (entry.values[field] ?? 0), 0);
    return { codex: factor * sum("input_tokens", provider => provider === "codex"), rest: sum("input_tokens", provider => provider !== "codex") + sum("output_tokens") };
  };
  const derived = { ...catalog, components: { ...catalog.components, weighted: { label: "Weighted", measures: ["tokens"], fallback: "provider",
    parts: [{ key: "codex", label: "Codex input", color: "red", fields: ["input_tokens"] }, { key: "rest", label: "Everything else", color: "blue", fields: ["input_tokens", "output_tokens"] }],
    derived: { inputs: { fields: ["input_tokens", "output_tokens"], groupBy: ["provider"] }, derive } } } };
  assert.deepEqual(catalogProblems(derived), []);
  const state = { ...base, metric: "tokens", split: "weighted" };
  const query = trendQuery(derived, state, range, { now });
  assert.deepEqual(query.query.aggregations.map(clause => [clause.id, clause.groupBy]), [["trend:input:input_tokens", ["day", "provider"]], ["trend:input:output_tokens", ["day", "provider"]]]);
  const built = trendSeries(derived, state, query, core.evaluateMetrics(rows, query.query, schema, { now }).metrics, { now });
  const daily = {};
  for (const row of rows) {
    const day = (daily[localDay(new Date(`${row.day}T00:00:00`))] ??= { codex: 0, rest: 0 });
    if (row.provider === "codex") day.codex += factor * row.input_tokens; else day.rest += row.input_tokens;
    day.rest += row.output_tokens;
  }
  assert.deepEqual(built.series.map(item => item.key), ["codex", "rest"]);
  assert.deepEqual(Object.fromEntries(built.buckets.filter(bucket => bucket.values.rest).map(bucket => [bucket.key, bucket.values])), daily);
  // Smoothing and shares follow the derived values.
  const shares = { ...state, rollingPeriods: 3, share: true }, sharesQuery = trendQuery(derived, shares, range, { now });
  const window = trendSeries(derived, shares, sharesQuery, core.evaluateMetrics(rows, sharesQuery.query, schema, { now }).metrics, { now });
  for (const point of window.buckets) {
    const days = [0, 1, 2].map(offset => { const date = new Date(point.date); date.setDate(date.getDate() - offset); return daily[localDay(date)] ?? { codex: 0, rest: 0 }; });
    const sums = days.reduce((total, day) => ({ codex: total.codex + day.codex, rest: total.rest + day.rest }), { codex: 0, rest: 0 });
    if (sums.codex + sums.rest) assert.ok(Math.abs(point.values.codex - sums.codex / (sums.codex + sums.rest)) < 1e-9, `${point.key}: the share is of the smoothed derived parts`);
  }
  assert.ok(window.buckets.some(bucket => bucket.values.codex > 0 && bucket.values.codex < 1));
  assert.match(trendNote(derived, state), /Weighted splits each day's tokens into its parts/);
});

test("a trend without a scale resolves it once, for the Scale control and the chart alike", async () => {
  const { defaultChartScale } = await import("../../../src/chart-scale");
  const { humanAnalysisCatalog } = await import("../../../src/human-analysis-catalog");
  const { carbonCatalogShape } = await import("../../../src/carbon-analysis-catalog");
  for (const target of [catalog, humanAnalysisCatalog, carbonCatalogShape]) {
    // AnalysisControls shows, and AnalysisTrend draws, the normalized state's scale.
    const drawn = normalizeAnalysis(target, { metric: target.trend.metric, split: target.trend.split });
    assert.equal(drawn.scale, defaultChartScale, `${target.dataset}: an absent scale is the old trend charts' linear + log split`);
    assert.equal(normalizeAnalysis(target, { metric: target.trend.metric, split: "none", scale: "linear" }).scale, "linear", "a chosen scale is kept");
  }
  // An older link without a scale keeps the split axis it always drew.
  assert.equal(normalizeAnalysis(catalog, { metric: "percent", split: "provider", period: "day" }).scale, defaultChartScale);
  // Other views keep their own axis default (linear).
  assert.equal(normalizeAnalysis(catalog, { view: "compare", metric: "tokens", split: "provider" }).scale, undefined);
});
