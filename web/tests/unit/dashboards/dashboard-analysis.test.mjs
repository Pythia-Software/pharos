import test from "node:test";
import assert from "node:assert/strict";
import { core, schemas } from "../helpers/recipes.mjs";
import { now, usageLedger } from "../helpers/usage-ledger.mjs";
import { analysisEmptyLabel, analysisInspectQuery, analysisNote, analysisQuery, availableViews, catalogProblems, cycleResult, normalizeAnalysis, switchView, trendUnavailable, validAnalysisState, viewOptions } from "../../../src/dashboard-analysis-model";
import { tokenAnalysisCatalog as catalog } from "../../../src/token-analysis-catalog";
import { runAnalysisQuery } from "../../../src/analysis-request";
import { answerQuery } from "../../support/query-server.mjs";
import { cycleSamples, hoursOfDay, weekdays } from "../../../src/weekday-cover";

const { rows, hourly } = usageLedger(), schema = schemas.usage;
const trend = { metric: "percent_cost", split: "type", period: "day", hours: 6, days: 3, rollingPeriods: 7, scale: "half", display: "interval" };
const evaluate = query => core.evaluateMetrics(rows, query, schema, { now }).metrics[0];

test("a trend link without a view stays the trend, unchanged", () => {
  assert.ok(validAnalysisState(catalog, trend));
  const state = normalizeAnalysis(catalog, trend);
  assert.equal(state.view, "trend");
  for (const [key, value] of Object.entries(trend)) assert.deepEqual(state[key], value, key);
});

test("field validation rejects unknown measures, units and thresholds but not combinations", () => {
  const compare = { view: "compare", metric: "tokens", split: "model_family" };
  assert.ok(validAnalysisState(catalog, compare));
  assert.ok(validAnalysisState(catalog, { ...compare, view: "scatter", unit: "records" }), "coerced, not rejected");
  for (const bad of [{ view: "pie" }, { metric: "words" }, { unit: "workspace" }, { split: "tool_name" }, { minSamples: 1.5 }, { minSamples: -1 }, { reduce: "sum" }, { compareAs: "pie" }, { cycle: "month" }]) {
    assert.equal(validAnalysisState(catalog, { ...compare, ...bad }), false, JSON.stringify(bad));
  }
});

test("switching views keeps what both views share and coerces the rest", () => {
  const share = switchView(catalog, trend, "compare");
  assert.deepEqual([share.metric, share.compareAs, share.split, share.rollingPeriods], ["cost", "share", "none", 7], "percent cost becomes a cost share; token type is no breakdown");
  assert.deepEqual([switchView(catalog, share, "trend").metric, switchView(catalog, share, "trend").split], ["percent_cost", "model_family"]);
  const ratio = switchView(catalog, { ...share, metric: "cache_read_share", split: "provider" }, "compare");
  assert.equal(ratio.compareAs, "bars", "a ratio cannot be shown as shares");
  const scatter = switchView(catalog, { view: "compare", metric: "cost", split: "model_effort" }, "scatter");
  assert.deepEqual([scatter.unit, scatter.metricY, scatter.split], ["conversation", "tokens", "none"], "scatter pairs two measures of a unit; model + effort is not constant within a conversation");
  const conversation = switchView(catalog, { view: "compare", unit: "conversation", metric: "tokens", split: "model_family", reduce: "median" }, "cycle");
  assert.deepEqual([conversation.unit, conversation.split, conversation.reduce], ["records", "none", "median"], "a cycle profiles records, one box per slot");
  assert.equal(switchView(catalog, { view: "compare", metric: "cache_read_share", split: "none" }, "distribution").unit, "conversation", "a ratio has no per-record value");
  assert.equal(switchView(catalog, { view: "compare", metric: "tokens", split: "none" }, "distribution").unit, "records");
  assert.equal(switchView(catalog, { view: "compare", unit: "agent_session", metric: "tokens", split: "provider", reduce: "count", compareAs: "share" }, "compare").compareAs, "share", "unit counts partition the units");
  assert.equal(switchView(catalog, { view: "compare", unit: "agent_session", metric: "tokens", split: "provider", reduce: "mean", compareAs: "share" }, "compare").compareAs, "bars");
  assert.equal(switchView(catalog, { view: "scatter", unit: "conversation", metric: "tokens", metricY: "tokens", split: "none" }, "scatter").metricY, "cost");
});

test("record views group or distribute records directly", () => {
  const where = [{ field: "first_usage_at", op: ">=", value: "-30d" }];
  const compare = analysisQuery(catalog, { view: "compare", metric: "priced_cost", split: "provider", minSamples: 3 }, where);
  assert.equal(compare.grouped, false);
  assert.deepEqual(compare.query.where, [...where, { field: "price_status", op: "=", value: "priced" }], "a measure's population joins the filters");
  assert.deepEqual([compare.clause.groupBy, compare.clause.field, compare.clause.minCount], [["provider"], "cost_usd", 3]);
  assert.deepEqual(compare.supporting.where, [{ field: "analysis_records", op: ">=", value: "3" }], "the supporting query keeps the threshold as a group filter");
  const histogram = analysisQuery(catalog, { view: "distribution", metric: "output", split: "none", minSamples: 5 }, []);
  assert.deepEqual([histogram.clause.distribution, histogram.clause.minCount, histogram.clause.groupBy], [{ kind: "histogram", input: "[output_tokens]" }, undefined, []], "a single histogram has no groups to threshold");
  const boxes = evaluate(analysisQuery(catalog, { view: "distribution", metric: "tokens", split: "provider" }, []).query);
  assert.equal(boxes.buckets.reduce((n, bucket) => n + bucket.distribution.summary.n, 0), rows.length, "one observation per usage record");
});

test("unit views group records into one row per unit before comparing", () => {
  const median = analysisQuery(catalog, { view: "compare", unit: "conversation", metric: "cost", split: "model_family", reduce: "median" }, []);
  assert.deepEqual(median.query.grouping.keys, ["root_model_family", "root_conversation_id"]);
  assert.equal(median.supporting, median.query, "the supporting query is the executed query");
  const conversations = new Set(rows.map(row => row.root_conversation_id));
  const count = evaluate(analysisQuery(catalog, { view: "compare", unit: "conversation", metric: "tokens", split: "model_family", reduce: "count" }, []).query);
  assert.deepEqual(Object.fromEntries(count.buckets.map(bucket => [bucket.keys[0], bucket.value])), { opus: 8, sonnet: 4, "gpt-5": 2 });
  const runs = evaluate(analysisQuery(catalog, { view: "compare", unit: "delegated_run", metric: "tokens", split: "none", reduce: "count" }, []).query);
  assert.equal(runs.buckets[0].value, new Set(rows.filter(row => row.delegated_run_depth > 0).map(row => row.delegated_session_id)).size);
  const total = evaluate(analysisQuery(catalog, { view: "compare", unit: "conversation", metric: "tokens", split: "none" }, []).query);
  assert.equal(total.buckets[0].value, rows.reduce((sum, row) => sum + row.total_tokens, 0));
  const per = Object.fromEntries([...conversations].map(id => [id, rows.filter(row => row.root_conversation_id === id && row.cost_usd !== null).reduce((sum, row) => sum + row.cost_usd, 0)]));
  const largest = evaluate(analysisQuery(catalog, { view: "compare", unit: "conversation", metric: "cost", split: "none", reduce: "max" }, []).query);
  assert.equal(largest.buckets[0].value, Math.max(...Object.values(per)));
  const middle = evaluate(median.query).buckets.find(bucket => bucket.keys[0] === "sonnet").value;
  const sonnet = [...conversations].filter(id => rows.find(row => row.root_conversation_id === id).root_model_family === "sonnet").map(id => per[id]).sort((a, b) => a - b);
  assert.equal(middle, (sonnet[1] + sonnet[2]) / 2);
});

test("a bucket opens its records, or its units' grouped rows", () => {
  const records = analysisQuery(catalog, { view: "compare", metric: "tokens", split: "provider" }, [{ field: "total_tokens", op: ">", value: "0" }]);
  const opened = analysisInspectQuery(records, { keys: ["codex"], value: 1, count: 1 }, records.clause, schema, "compare");
  assert.equal(opened.grouping, undefined);
  assert.deepEqual(opened.where, [{ field: "total_tokens", op: ">", value: "0" }, { field: "provider", op: "=", value: "codex" }]);
  const units = analysisQuery(catalog, { view: "distribution", unit: "conversation", metric: "cost", split: "model_family" }, []);
  const group = analysisInspectQuery(units, { keys: ["opus"], value: 1, count: 6 }, units.clause, schema, "distribution");
  assert.deepEqual(group.grouping.keys, units.query.grouping.keys);
  assert.deepEqual(group.where, [{ field: "analysis_value", op: "is_not_null", value: "" }, { field: "root_model_family", op: "=", value: "opus" }], "the plotted conversations of that group");
  assert.equal(group.aggregations?.length ?? 0, 0);
  assert.equal(core.applyQuery(rows, group, schema, { now }).total, 8, "eight Opus conversations, from both providers");
  assert.equal(analysisInspectQuery(units, { keys: ["Monday"], value: 1, count: 1 }, units.clause, schema, "cycle"), null);
});

test("a cycle folds hourly totals into local hours, idle hours included", () => {
  const from = Date.parse("2026-10-12T00:00:00Z"), to = Date.parse("2026-10-14T00:00:00Z");
  const result = { id: "analysis", groupCount: 2, buckets: [{ keys: ["2026-10-12T09:00:00.000Z"], value: 30, count: 2 }, { keys: ["2026-10-13T09:00:00.000Z"], value: 10, count: 1 }] };
  const box = cycleResult(catalog, { view: "cycle", cycle: "hour", metric: "tokens", split: "none", reduce: "median" }, result, { from, to }, "America/New_York");
  assert.deepEqual(box.result.buckets.map(bucket => bucket.keys[0]), hoursOfDay);
  assert.equal(box.slots, 48);
  assert.equal(box.observations, 3);
  const five = box.result.buckets[5].distribution.summary;
  assert.deepEqual([five.n, five.median, five.max], [2, 20, 30], "09:00 UTC is 05:00 in New York in October");
  assert.equal(box.result.buckets[6].distribution.summary.max, 0);
  const totals = cycleResult(catalog, { view: "cycle", cycle: "hour", metric: "tokens", split: "none", reduce: "total" }, result, { from, to }, "UTC");
  assert.equal(totals.clause.display.kind, "bar-vertical");
  assert.deepEqual([totals.result.buckets[9].value, totals.result.buckets[10].value], [40, 0]);
  const ratio = cycleResult(catalog, { view: "cycle", cycle: "hour", metric: "cache_read_share", split: "none", reduce: "mean" }, result, { from, to }, "UTC");
  assert.equal(ratio.result.buckets[10].value, null, "idle hours have no ratio");
  assert.throws(() => cycleResult(catalog, { view: "cycle", metric: "tokens", split: "none" }, { ...result, coverage: "partial" }, { from, to }, "UTC"), /Complete totals/);
});

test("a weekday cycle folds the ledger's local days, as the weekday cards do", () => {
  const result = { id: "analysis", buckets: [{ keys: ["2026-10-05"], value: 7, count: 1 }, { keys: ["2026-10-12"], value: 3, count: 1 }] };
  const zone = "America/Los_Angeles", from = core.metricDateTime("2026-10-05", zone), to = core.metricDateTime("2026-10-19", zone);
  const box = cycleResult(catalog, { view: "cycle", cycle: "weekday", metric: "tokens", split: "none" }, result, { from, to }, zone);
  assert.deepEqual(box.result.buckets.map(bucket => bucket.keys[0]), weekdays);
  assert.deepEqual([box.result.buckets[0].distribution.summary.n, box.result.buckets[0].distribution.summary.median], [2, 5]);
  assert.equal(box.slots, 14);
  // The shared fold also serves weekday covers; hours step on UTC boundaries.
  assert.deepEqual(cycleSamples("hour", Date.parse("2026-10-12T09:30:00Z"), Date.parse("2026-10-12T12:00:00Z"), "UTC", () => 1).flat().length, 2, "partial first hour left out");
});

test("a cycle query reads the ledger's hourly or daily totals", () => {
  const hours = analysisQuery(catalog, { view: "cycle", cycle: "hour", metric: "tokens", split: "model_family" }, []);
  assert.deepEqual([hours.clause.groupBy, hours.clause.groupLimit], [["hour"], 10000]);
  assert.equal(hours.supporting.grouping, undefined, "grouped usage queries read daily records, which have no hour");
  const days = analysisQuery(catalog, { view: "cycle", cycle: "weekday", metric: "tokens", split: "none" }, []);
  assert.deepEqual([days.clause.groupBy, days.supporting.grouping.keys], [["day"], ["day"]]);
});

test("notes say what one observation is", () => {
  assert.match(analysisNote(catalog, { view: "compare", unit: "conversation", metric: "cost", split: "model_family", reduce: "mean", minSamples: 5 }), /One observation is one conversation.*mean conversation.*fewer than 5 conversations are hidden/);
  assert.match(analysisNote(catalog, { view: "distribution", metric: "tokens", split: "provider" }), /One observation is one usage record\./);
  assert.match(analysisNote(catalog, { view: "compare", metric: "tokens", split: "provider", compareAs: "share" }), /Shares divide/);
  assert.equal(analysisEmptyLabel(catalog, { view: "distribution", unit: "conversation", metric: "cost", split: "model_family", minSamples: 5 }), "No groups with at least 5 conversations");
  assert.equal(analysisEmptyLabel(catalog, { view: "compare", metric: "cost", split: "none" }), "No matching usage records");
});

test("analysis queries run on /metrics, grouped ones on /query-v3, shared snapshots locally", async t => {
  const prior = global.fetch, requests = [];
  global.fetch = async (url, init = {}) => {
    const path = String(url), body = init.body ? JSON.parse(init.body) : undefined;
    requests.push(path);
    return new Response(JSON.stringify(answerQuery(path.split("/").at(-1), body, rows, schema, { now })), { headers: { "Content-Type": "application/json" } });
  };
  t.after(() => { global.fetch = prior; });
  const records = analysisQuery(catalog, { view: "compare", metric: "tokens", split: "provider", minSamples: 2 }, []);
  const units = analysisQuery(catalog, { view: "distribution", unit: "conversation", metric: "cost", split: "model_family", minSamples: 5 }, []);
  const [served] = await runAnalysisQuery("usage", schema, records.query);
  assert.deepEqual(requests, ["/api/query/usage/metrics"]);
  const [grouped] = await runAnalysisQuery("usage", schema, units.query);
  assert.deepEqual(requests.slice(1), ["/api/query/usage/capabilities", "/api/query/usage/query-v3"]);
  assert.deepEqual(grouped.buckets.map(bucket => bucket.keys[0]), ["opus"]);
  assert.equal(grouped.hiddenGroups, 2);
  const [local] = await runAnalysisQuery("usage", schema, units.query, { local: { rows } });
  assert.deepEqual(local.buckets, grouped.buckets);
  assert.deepEqual((await runAnalysisQuery("usage", schema, records.query, { local: { rows, hourly: [] } }))[0].buckets, served.buckets);
  assert.equal(requests.length, 3, "shared snapshots make no requests");
});

test("the catalog reserves records and none, and keeps implied filters off unit breakdowns", () => {
  assert.deepEqual(catalogProblems(catalog), []);
  const broken = { ...catalog, units: { ...catalog.units, records: catalog.units.conversation }, breakdowns: { ...catalog.breakdowns, none: { label: "None", fields: ["provider"] }, provider: { ...catalog.breakdowns.provider, where: [{ field: "provider", op: "is_not_null", value: "" }] } } };
  const problems = catalogProblems(broken).join("\n");
  assert.match(problems, /may not be named records/);
  assert.match(problems, /may not be named none/);
  assert.match(problems, /conversation offers provider, whose implied filter/);
  const noCycle = { ...catalog, cycle: undefined };
  assert.ok(!availableViews(noCycle).some(([view]) => view === "cycle"));
  assert.equal(validAnalysisState(noCycle, { view: "cycle", metric: "tokens", split: "none" }), false);
  assert.equal(normalizeAnalysis(noCycle, { view: "cycle", metric: "tokens", split: "none" }).view, "compare");
});

test("units pool a ratio's parts for a total, and never add distinct counts", () => {
  const state = { view: "compare", unit: "conversation", metric: "cache_read_share", split: "model_family" };
  assert.equal(normalizeAnalysis(catalog, state).reduce, "total");
  const pooled = evaluate(analysisQuery(catalog, state, []).query);
  const records = evaluate(analysisQuery(catalog, { view: "compare", metric: "cache_read_share", split: "none" }, [{ field: "root_model_family", op: "=", value: "opus" }]).query);
  assert.equal(pooled.buckets.find(bucket => bucket.keys[0] === "opus").value, records.buckets[0].value, "the ratio of summed parts over the group's records");
  assert.ok(pooled.buckets.every(bucket => bucket.value > 0 && bucket.value < 1));
  assert.match(analysisNote(catalog, state), /pool the conversations/);
  const mean = normalizeAnalysis(catalog, { ...state, reduce: "mean" });
  assert.match(analysisNote(catalog, mean), /weighted equally/);
  assert.equal(normalizeAnalysis(catalog, { ...state, metric: "agent_sessions" }).reduce, "mean", "distinct counts have no unit total");
  assert.equal(normalizeAnalysis(catalog, { ...state, metric: "tokens" }).reduce, "total");
});

test("Over time is offered only for what the trend draws, and the mapping is reversible", () => {
  for (const metric of ["priced_cost", "cache_read_share", "input", "output", "conversations"]) {
    const state = { ...trend, view: "compare", metric, split: "provider", compareAs: "bars" };
    assert.ok(trendUnavailable(catalog, state), metric);
    assert.ok(viewOptions(catalog, state).find(([view]) => view === "trend")[2], `${metric}: the option says why`);
    assert.deepEqual(switchView(catalog, state, "trend"), normalizeAnalysis(catalog, state), `${metric}: nothing is substituted`);
  }
  assert.match(trendUnavailable(catalog, { view: "compare", metric: "priced_cost", split: "none" }), /population/);
  const share = { ...trend, view: "compare", metric: "cost", split: "provider", compareAs: "share" };
  assert.equal(trendUnavailable(catalog, share), undefined);
  const there = switchView(catalog, share, "trend"), back = switchView(catalog, there, "compare");
  assert.deepEqual([there.metric, there.split, back.metric, back.compareAs, back.split], ["percent_cost", "provider", "cost", "share", "provider"]);
  // The trend's absolute metric returns as bars, whatever display came before.
  assert.equal(switchView(catalog, { ...there, metric: "cost" }, "compare").compareAs, "bars");
});

test("minimums count plotted observations, and shares ignore them", () => {
  const sparse = [10, null, null, 5, 6, 7].map((cost, index) => ({ ...rows[0], id: `sparse-${index}`, provider: index < 3 ? "claude" : "codex", cost_usd: cost }));
  const run = state => core.evaluateMetrics(sparse, analysisQuery(catalog, state, []).query, schema, { now }).metrics[0];
  const box = run({ view: "distribution", metric: "cost", split: "provider", minSamples: 3 });
  assert.deepEqual([box.buckets.map(bucket => bucket.keys[0]), box.hiddenGroups], [["codex"], 1], "two missing costs do not count toward three");
  const units = sparse.map((row, index) => ({ ...row, root_conversation_id: `c-${index}`, root_provider: row.provider }));
  const perUnit = core.evaluateMetrics(units, analysisQuery(catalog, { view: "distribution", unit: "conversation", metric: "cost", split: "provider", minSamples: 3 }, []).query, schema, { now }).metrics[0];
  assert.deepEqual([perUnit.buckets.map(bucket => bucket.keys[0]), perUnit.hiddenGroups], [["codex"], 1], "conversations without cost are not plotted or counted");
  const share = analysisQuery(catalog, { view: "compare", metric: "tokens", split: "provider", compareAs: "share", minSamples: 5 }, []);
  assert.equal(share.clause.minCount, undefined, "a share's denominator keeps every group");
  assert.doesNotMatch(analysisNote(catalog, { view: "compare", metric: "tokens", split: "provider", compareAs: "share", minSamples: 5 }), /hidden/);
});

test("an hour cycle runs on the hourly ledger and places every hour", async () => {
  const analysis = analysisQuery(catalog, { view: "cycle", cycle: "hour", metric: "tokens", split: "none", reduce: "total" }, []);
  const [result] = await runAnalysisQuery("usage", schema, analysis.query, { local: { rows, hourly } });
  assert.ok(result.buckets.every(bucket => /T\d\d:00:00Z$/.test(bucket.keys[0])), "hourly keys, not daily records");
  const folded = cycleResult(catalog, { view: "cycle", cycle: "hour", metric: "tokens", split: "none", reduce: "total" }, result, { from: now - 10 * 86400000, to: now }, "UTC");
  const total = rows.reduce((n, row) => n + row.total_tokens, 0);
  assert.equal(folded.result.buckets.reduce((n, bucket) => n + bucket.value, 0), total);
  const expected = Array(24).fill(0);
  for (const row of hourly) expected[new Date(row.hour).getUTCHours()] += row.total_tokens;
  assert.deepEqual(folded.result.buckets.map(bucket => bucket.value), expected);
  // Daily records have no hour; a null key must fail rather than read as idle.
  assert.throws(() => cycleResult(catalog, { view: "cycle", cycle: "hour", metric: "tokens", split: "none" }, { id: "analysis", buckets: [{ keys: [null], value: total, count: rows.length }] }, { from: now - 86400000, to: now }, "UTC"), /no hour/);
});

test("idle and unpriced slots count as zero for sums and distinct counts, not ratios", () => {
  const from = Date.parse("2026-10-12T00:00:00Z"), to = Date.parse("2026-10-12T03:00:00Z");
  const result = { id: "analysis", buckets: [{ keys: ["2026-10-12T00:00:00Z"], value: 2, count: 2 }, { keys: ["2026-10-12T01:00:00Z"], value: null, count: 1 }] };
  const fold = metric => cycleResult(catalog, { view: "cycle", cycle: "hour", metric, split: "none", reduce: "mean" }, result, { from, to }, "UTC").result.buckets.slice(0, 3).map(bucket => bucket.value);
  assert.deepEqual(fold("conversations"), [2, 0, 0]);
  assert.deepEqual(fold("cost"), [2, 0, 0], "an hour of only unpriced usage adds no cost");
  assert.deepEqual(fold("cache_read_share"), [2, null, null]);
  assert.match(analysisNote(catalog, { view: "cycle", cycle: "hour", metric: "conversations", split: "none" }), /idle hours included as zero/);
  assert.match(analysisNote(catalog, { view: "cycle", cycle: "hour", metric: "output_share", split: "none" }), /which have no value/);
});

test("weekdays fold the recorded dates, whatever the viewer's time zone", () => {
  const result = { id: "analysis", buckets: [{ keys: ["2026-10-05"], value: 7, count: 1 }] };
  for (const zone of ["Pacific/Kiritimati", "Pacific/Pago_Pago", "UTC"]) {
    const from = core.metricDateTime("2026-10-05", zone), to = core.metricDateTime("2026-10-12", zone);
    const box = cycleResult(catalog, { view: "cycle", cycle: "weekday", metric: "tokens", split: "none" }, result, { from, to }, zone);
    assert.equal(box.result.buckets[0].distribution.summary.max, 7, `${zone}: 2026-10-05 is a Monday`);
    assert.equal(box.slots, 7, zone);
  }
  assert.match(analysisNote(catalog, { view: "cycle", cycle: "weekday", metric: "tokens", split: "none" }), /dates the recording Mac assigned in its own time zone/);
});

test("a cycle totals only additive measures, and says when a mean is unweighted", () => {
  const cycle = { view: "cycle", cycle: "hour", split: "none", reduce: "total" };
  assert.equal(normalizeAnalysis(catalog, { ...cycle, metric: "tokens" }).reduce, "total");
  assert.equal(normalizeAnalysis(catalog, { ...cycle, metric: "cache_read_share" }).reduce, "median", "hourly ratios do not add up");
  assert.equal(normalizeAnalysis(catalog, { ...cycle, metric: "conversations" }).reduce, "median", "a conversation active on two days would count twice");
  const result = { id: "analysis", buckets: [{ keys: ["2026-10-12T00:00:00Z"], value: .6, count: 1 }, { keys: ["2026-10-13T00:00:00Z"], value: .6, count: 1 }] };
  const mean = cycleResult(catalog, { ...cycle, metric: "cache_read_share", reduce: "mean" }, result, { from: Date.parse("2026-10-12T00:00:00Z"), to: Date.parse("2026-10-14T00:00:00Z") }, "UTC");
  assert.equal(mean.result.buckets[0].value, .6);
  assert.match(mean.clause.label, /^Unweighted mean/);
  assert.match(analysisNote(catalog, { ...cycle, metric: "cache_read_share", reduce: "mean" }), /weights every hour equally, not by tokens/);
  assert.throws(() => cycleSamples("hour", 0, 401 * 86400000, "UTC", () => 1), /at most 400 days/);
});

test("a unit compare's minimum counts units with a value", () => {
  const sparse = [10, null, null, null, 5, 6, 7].map((cost, index) => ({ ...rows[0], id: `sparse-${index}`, root_conversation_id: `c-${index}`, root_provider: index < 4 ? "claude" : "codex", cost_usd: cost }));
  const median = core.evaluateMetrics(sparse, analysisQuery(catalog, { view: "compare", unit: "conversation", metric: "cost", split: "provider", reduce: "median", minSamples: 3 }, []).query, schema, { now }).metrics[0];
  assert.deepEqual([median.buckets.map(bucket => bucket.keys[0]), median.hiddenGroups], [["codex"], 1], "one priced conversation of four does not make three");
  const count = core.evaluateMetrics(sparse, analysisQuery(catalog, { view: "compare", unit: "conversation", metric: "cost", split: "provider", reduce: "count", minSamples: 3 }, []).query, schema, { now }).metrics[0];
  assert.deepEqual(count.buckets.map(bucket => [bucket.keys[0], bucket.value]), [["claude", 4], ["codex", 3]], "a count of conversations counts them all");
});

test("a weekday cycle folds every returned date, even one before the viewer's first date", () => {
  // Recorded in Denver late on Sunday 4 October; selected by a Tokyo viewer from Monday 5 October.
  const result = { id: "analysis", buckets: [{ keys: ["2026-10-04"], value: 9, count: 1 }, { keys: ["2026-10-05"], value: 7, count: 1 }] };
  const zone = "Asia/Tokyo", from = core.metricDateTime("2026-10-05", zone), to = core.metricDateTime("2026-10-12", zone);
  const box = cycleResult(catalog, { view: "cycle", cycle: "weekday", metric: "tokens", split: "none", reduce: "total" }, result, { from, to }, zone);
  assert.equal(box.result.buckets[6].value, 9, "Sunday's usage is kept");
  assert.equal(box.result.buckets.reduce((n, bucket) => n + bucket.value, 0), 16);
  // Weekday cards key days in the zone of their query; a date's weekday is the
  // same in every zone, so the cards and the dashboard share one rule.
  for (const zone of ["Pacific/Kiritimati", "Pacific/Pago_Pago"]) {
    const day = core.metricDateTime("2026-10-05", zone);
    assert.equal(cycleSamples("weekday", day, day + 86400000 * 1.5, zone, () => 1)[0].length, 1, zone);
  }
});
