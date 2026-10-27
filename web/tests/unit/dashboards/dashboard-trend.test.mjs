// Over time as a catalog-driven view: old links and presets, coercions,
// queries and the series builder, over the usage ledger.
import test from "node:test";
import assert from "node:assert/strict";
import { core, queryRecipes, schemas } from "../helpers/recipes.mjs";
import { now, usageLedger } from "../helpers/usage-ledger.mjs";
import { analysisQuery, catalogProblems, changeState, selectedShares, normalizeAnalysis, switchView, trendAdjustments, trendLimits, validAnalysisState } from "../../../src/dashboard-analysis-model";
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
  assert.equal(new Date(supporting.find(term => term.op === ">=").value).getTime(), midnight.getTime());
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
  const where = [...range, { field: "provider", op: "=", value: smallest }];
  const trend = draw({ ...base, metric: "tokens", split: "provider", share: true, display: "cumulative", seriesLimit: 1 }, { where }).built;
  assert.ok(trend.series.some(item => item.value === smallest), "a selected group is never folded into Other");
  assert.deepEqual(trend.selected, [`s:${smallest}`]);
  assert.ok(Math.abs(trend.buckets.at(-1).values[`s:${smallest}`] - totals.get(smallest) / all) < 1e-12);
  const compare = analysisQuery(catalog, { view: "compare", metric: "tokens", split: "provider", compareAs: "share" }, where, { now });
  assert.deepEqual(compare.selection, { values: [smallest], excluded: false });
  assert.ok(!compare.query.where.some(term => term.field === "provider"), "the denominator is every provider");
  assert.ok(compare.supporting.grouping.source.where.some(term => term.field === "provider"), "the supporting query keeps the user's filter");
  const pie = selectedShares(core.evaluateMetrics(rows, compare.query, schema, { now }).metrics[0], compare.selection);
  assert.deepEqual(pie.buckets.map(bucket => bucket.keys[0]), [smallest], "the selected group is drawn");
  assert.equal(pie.other.value, all - totals.get(smallest), "every other provider is Other");
  assert.ok(Math.abs(pie.buckets[0].value / (pie.buckets[0].value + pie.other.value) - trend.buckets.at(-1).values[`s:${smallest}`]) < 1e-12, "the trend and Compare agree");
  // An excluded group: every other group is drawn, the excluded one is Other.
  const excluded = analysisQuery(catalog, { view: "compare", metric: "tokens", split: "provider", compareAs: "share" }, [...range, { field: "provider", op: "!=", value: smallest }], { now });
  assert.deepEqual(excluded.selection, { values: [smallest], excluded: true });
  assert.equal(selectedShares(core.evaluateMetrics(rows, excluded.query, schema, { now }).metrics[0], excluded.selection).other.value, totals.get(smallest));
  // Combined groups keep the trend's own selection; so do unit counts.
  const pair = JSON.stringify(["claude", "opus"]);
  const multi = analysisQuery(catalog, { view: "compare", metric: "tokens", split: "provider_model", compareAs: "share", selection: { values: [pair], excluded: false } }, range, { now });
  assert.deepEqual(selectedShares(core.evaluateMetrics(rows, multi.query, schema, { now }).metrics[0], multi.selection).buckets.map(bucket => JSON.stringify(bucket.keys)), [pair]);
  const sessions = analysisQuery(catalog, { view: "compare", unit: "agent_session", reduce: "count", metric: "tokens", split: "provider", compareAs: "share" }, where, { now });
  assert.deepEqual(sessions.selection, { values: [smallest], excluded: false });
  assert.ok(!sessions.query.grouping.source.where.some(term => term.field === "provider") && sessions.supporting.grouping.source.where.some(term => term.field === "provider"));
  const counted = selectedShares(core.evaluateMetrics(rows, sessions.query, schema, { now }).metrics[0], sessions.selection);
  assert.equal(counted.buckets[0].value + counted.other.value, new Set(rows.map(row => row.agent_session_id)).size, "unit shares are of every session");
  // Bars keep the filter: there a selection chooses the groups.
  assert.ok(analysisQuery(catalog, { view: "compare", metric: "tokens", split: "provider" }, where, { now }).query.where.some(term => term.field === "provider"));
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
