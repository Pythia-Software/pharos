import test from "node:test";
import assert from "node:assert/strict";
import { core, schemas, recipeById, instantiateRecipe, cardCoverQuery } from "../helpers/recipes.mjs";
import { now, usageLedger } from "../helpers/usage-ledger.mjs";
import { viewKeys, chosenView, scatterUnitChange, viewChosen, changeState, aggregate, analysisResult, analysisLabels, analysisFields, analysisEmptyLabel, analysisInspectQuery, analysisNote, analysisQuery, availableViews, catalogProblems, cycleResult, normalizeAnalysis, switchView, trendUnavailable, validAnalysisState, viewOptions } from "../../../src/dashboard-analysis-model";
import { humanAnalysisCatalog as human } from "../../../src/human-analysis-catalog";
import { tokenAnalysisCatalog as catalog } from "../../../src/token-analysis-catalog";
import { runAnalysisQuery } from "../../../src/analysis-request";
import { answerQuery } from "../../support/query-server.mjs";
import { cycleSamples, hoursOfDay, weekdayMetric, weekdays } from "../../../src/weekday-cover";
import { analysisLoadingPresentation } from "../../../src/dashboard-loading-model";

const { rows, hourly } = usageLedger(), schema = schemas.usage;
const trend = { metric: "percent_cost", split: "type", period: "day", hours: 6, days: 3, rollingPeriods: 7, scale: "half", display: "interval" };
const evaluate = query => core.evaluateMetrics(rows, query, schema, { now }).metrics[0];

test("analysis loading follows the selected chart, including folded cycles", () => {
  for (const [input, kind] of [
    [{ view: "compare", metric: "tokens", split: "provider" }, "list"],
    [{ view: "compare", metric: "tokens", split: "provider", compareAs: "share" }, "pie"],
    [{ view: "distribution", metric: "tokens", split: "provider" }, "box"],
    [{ view: "distribution", metric: "output", split: "none" }, "columns"],
    [{ view: "scatter", unit: "conversation", metric: "cost", metricY: "tokens" }, "scatter"],
    [{ view: "cycle", cycle: "hour", metric: "tokens", reduce: "median" }, "box"],
    [{ view: "cycle", cycle: "weekday", metric: "tokens", reduce: "mean" }, "columns"],
  ]) {
    const view = normalizeAnalysis(catalog, input), { clause } = analysisQuery(catalog, view, []);
    assert.equal(analysisLoadingPresentation(clause, view, false).kind, kind, JSON.stringify(input));
  }
  const view = normalizeAnalysis(catalog, { view: "distribution", metric: "tokens" });
  assert.equal(analysisLoadingPresentation(analysisQuery(catalog, view, []).clause, view, true).kind, "columns");
});

test("a trend link without a view stays the trend; an old share metric reads as its measure shown as shares", () => {
  assert.ok(validAnalysisState(catalog, trend));
  const state = normalizeAnalysis(catalog, trend);
  assert.equal(state.view, "trend");
  for (const [key, value] of Object.entries(trend)) if (key !== "metric" && key !== "split") assert.deepEqual(state[key], value, key);
  assert.deepEqual([state.metric, state.share], ["cost", true]);
  // Cost is priced per request, not per token type: as before, it splits by provider.
  assert.equal(state.split, "provider");
});

test("field validation rejects unknown measures, units and thresholds but not combinations", () => {
  const compare = { view: "compare", metric: "tokens", split: "model_family" };
  assert.ok(validAnalysisState(catalog, compare));
  assert.ok(validAnalysisState(catalog, { ...compare, view: "scatter", unit: "records" }), "coerced, not rejected");
  for (const bad of [{ view: "pie" }, { metric: "words" }, { unit: "workspace" }, { split: "tool_name" }, { minSamples: 1.5 }, { minSamples: -1 }, { reduce: "sum" }, { compareAs: "pie" }, { cycle: "month" }]) {
    assert.equal(validAnalysisState(catalog, { ...compare, ...bad }), false, JSON.stringify(bad));
  }
});

test("switching views keeps the data both views share, coerces the rest and returns the view to its defaults", () => {
  const share = switchView(catalog, { ...trend, split: "provider" }, "compare");
  assert.deepEqual([share.metric, share.split], ["cost", "provider"], "percent cost becomes cost");
  assert.deepEqual(viewKeys.filter(key => share[key] !== undefined), [], "the trend's shares, smoothing and scale are no choice about Compare");
  assert.equal(normalizeAnalysis(catalog, share).compareAs, "bars");
  assert.deepEqual(["metric", "share", "split"].map(key => normalizeAnalysis(catalog, switchView(catalog, { ...share, compareAs: "share" }, "trend"))[key]), ["cost", false, "provider"], "nor are Compare's shares about the trend");
  const typed = switchView(catalog, { ...trend, metric: "percent" }, "compare");
  assert.deepEqual([typed.metric, typed.split], ["tokens", "type"], "token type remains a component breakdown");
  assert.deepEqual(["metric", "split"].map(key => switchView(catalog, typed, "trend")[key]), ["tokens", "type"], "and returns to the trend");
  const ratio = normalizeAnalysis(catalog, switchView(catalog, { ...share, metric: "cache_read_share", split: "provider", compareAs: "share" }, "compare"));
  assert.equal(ratio.compareAs, "bars", "a ratio cannot be shown as shares");
  const scatter = switchView(catalog, { view: "compare", metric: "cost", split: "model_effort" }, "scatter");
  assert.deepEqual([scatter.unit, scatter.metricY, scatter.split], ["conversation", "tokens", "none"], "scatter pairs two measures of a unit; model + effort is not constant within a conversation");
  const conversation = switchView(catalog, { view: "compare", unit: "conversation", metric: "tokens", split: "model_family", reduce: "median" }, "cycle");
  assert.deepEqual([conversation.unit, conversation.split, conversation.reduce], ["records", "none", "median"], "a cycle profiles records, one box per slot");
  assert.equal(switchView(catalog, { view: "compare", metric: "cache_read_share", split: "none" }, "distribution").unit, "conversation", "a ratio has no per-record value");
  assert.equal(switchView(catalog, { view: "compare", metric: "tokens", split: "none" }, "distribution").unit, "records");
  assert.equal(switchView(catalog, { view: "compare", unit: "agent_session", metric: "tokens", split: "provider", reduce: "count", compareAs: "share" }, "compare").compareAs, "share", "unit counts partition the units");
  assert.equal(normalizeAnalysis(catalog, switchView(catalog, { view: "compare", unit: "agent_session", metric: "tokens", split: "provider", reduce: "mean", compareAs: "share" }, "compare")).compareAs, "bars");
  assert.equal(switchView(catalog, { view: "scatter", unit: "conversation", metric: "tokens", metricY: "tokens", split: "none" }, "scatter").metricY, "cost");
});

test("record views group or distribute records directly", () => {
  const where = [{ field: "first_usage_at", op: ">=", value: "-30d" }];
  const compare = analysisQuery(catalog, { view: "compare", metric: "priced_cost", split: "provider", minSamples: 3 }, where);
  assert.equal(compare.grouped, false);
  assert.deepEqual(compare.query.where, [...where, { field: "price_status", op: "=", value: "priced" }], "a measure's population joins the filters");
  // A usage record is a slice of a session's day: the minimum counts the
  // distinct top-level conversations a group's records belong to, as the
  // Conversation unit does, so delegated runs do not count again.
  assert.deepEqual([compare.clause.groupBy, compare.clause.field, compare.clause.minCount], [["provider"], "cost_usd", { min: 3, distinct: "root_conversation_id" }]);
  assert.deepEqual(compare.supporting.where, [{ field: "analysis_samples", op: ">=", value: "3" }], "the supporting query keeps the threshold as a group filter");
  assert.ok(compare.supporting.grouping.summaries.some(column => column.id === "analysis_samples" && column.op === "count_distinct" && column.field === "root_conversation_id"));
  const reach = (min, split) => {
    const conversations = {};
    for (const row of rows) if (row.root_conversation_id) (conversations[row[split]] ??= new Set()).add(row.root_conversation_id);
    return Object.entries(conversations).filter(([, set]) => set.size >= min).map(([key]) => key).sort();
  };
  let hid = 0;
  // Antigravity has 6 records in 4 conversations; Sonnet 12 records in 8;
  // Claude 21 records in 11 conversations, 15 counting delegated runs.
  for (const [min, split] of [[5, "provider"], [9, "model_family"], [12, "provider"]]) {
    const result = evaluate(analysisQuery(catalog, { view: "compare", metric: "tokens", split, minSamples: min }, []).query);
    hid += result.hiddenGroups ?? 0;
    assert.deepEqual(result.buckets.map(bucket => bucket.keys[0]).sort(), reach(min, split), `${split}: groups reaching ${min} conversations`);
    assert.equal(result.hiddenGroups, new Set(rows.map(row => row[split])).size - reach(min, split).length);
  }
  assert.equal(hid, 6, "groups with enough records, or runs, but too few conversations");
  assert.match(analysisNote(catalog, { view: "compare", metric: "tokens", split: "provider", minSamples: 3 }), /fewer than 3 conversations are hidden/);
  // A distribution's minimum counts its plotted records.
  assert.equal(analysisQuery(catalog, { view: "distribution", metric: "tokens", split: "provider", minSamples: 3 }, []).clause.minCount, 3);
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
  // Cost per conversation is long-tailed, so its default axis is log, which
  // plots only conversations with a cost.
  assert.deepEqual(group.where, [{ field: "analysis_value", op: ">", value: "0" }, { field: "root_model_family", op: "=", value: "opus" }], "the plotted conversations of that group");
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

test("a weekday cycle folds days exactly as the weekday cards' weekdayMetric does", () => {
  const recipe = recipeById("human-words-weekday"), zone = "America/Los_Angeles";
  // A partial first day (from noon), quiet days, and two days with words.
  const from = Date.parse("2026-10-05T19:00:00Z"), to = core.metricDateTime("2026-10-19", zone), at = to;
  const messages = [["a", "2026-10-05T20:00:00Z", 11], ["b", "2026-10-06T17:00:00Z", 4], ["c", "2026-10-13T17:00:00Z", 6], ["d", "2026-10-14T03:00:00Z", 5]].map(([id, sent_at, typed_words]) => ({ id, sent_at, typed_words }));
  const source = cardCoverQuery(instantiateRecipe(recipe, 30, new Date(at)).query, recipe).query;
  const query = { ...source, where: [{ field: "typed_words", op: ">", value: "0" }, { field: "sent_at", op: ">=", value: new Date(from).toISOString() }, { field: "sent_at", op: "<", value: new Date(to).toISOString() }],
    aggregations: source.aggregations.map(metric => ({ ...metric, dateGrouping: { ...metric.dateGrouping, timeZone: zone } })) };
  const daily = core.evaluateMetrics(messages, query, schemas.writing_messages, { now: at }).metrics[0];
  const card = weekdayMetric(query.aggregations[0], daily, query, at, "words");
  // The dashboard folds the same daily totals, keyed by date.
  const dated = { ...daily, buckets: daily.buckets.map(bucket => ({ ...bucket, keys: [new Intl.DateTimeFormat("en-CA", { timeZone: zone }).format(core.metricDateTime(String(bucket.keys[0]), zone))] })) };
  const dashboard = cycleResult(catalog, { view: "cycle", cycle: "weekday", metric: "tokens", split: "none" }, dated, { from, to }, zone);
  assert.deepEqual(dashboard.result.buckets.map(bucket => [bucket.keys[0], bucket.distribution.summary]), card.result.buckets.map(bucket => [bucket.keys[0], bucket.distribution.summary]));
  assert.equal(dashboard.slots, card.days, "the same set of complete dates: the partial first Monday is out of both");
  assert.equal(dashboard.earlier, 1);
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
  assert.deepEqual(requests, ["/api/query/usage/capabilities", "/api/query/usage/query-v3"]);
  const [grouped] = await runAnalysisQuery("usage", schema, units.query);
  assert.deepEqual(requests.slice(2), ["/api/query/usage/capabilities", "/api/query/usage/query-v3"]);
  assert.deepEqual(grouped.buckets.map(bucket => bucket.keys[0]), ["opus"]);
  assert.equal(grouped.hiddenGroups, 2);
  const [local] = await runAnalysisQuery("usage", schema, units.query, { local: { rows } });
  assert.deepEqual(local.buckets, grouped.buckets);
  assert.deepEqual((await runAnalysisQuery("usage", schema, records.query, { local: { rows, hourly: [] } }))[0].buckets, served.buckets);
  assert.equal(requests.length, 4, "shared snapshots make no requests");
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

test("Over time draws every measure and population; only unit reductions stay unavailable", () => {
  for (const metric of Object.keys(catalog.measures)) for (const split of ["provider", "provider_model", "none"]) {
    const state = { ...trend, view: "compare", metric, split, compareAs: "bars" };
    assert.equal(trendUnavailable(catalog, state), undefined, metric);
    for (const drawing of ["trend_line", "trend_bar"]) assert.equal(viewOptions(catalog, state).find(([view]) => view === drawing)[2], undefined, `${metric}: ${drawing} is enabled`);
    const there = switchView(catalog, state, "trend");
    assert.deepEqual([there.view, there.metric, there.split, normalizeAnalysis(catalog, there).share], ["trend", metric, split, false], `${metric} by ${split}: nothing is substituted`);
    assert.deepEqual(["metric", "split", "compareAs"].map(key => normalizeAnalysis(catalog, switchView(catalog, there, "compare"))[key]), [metric, split, "bars"], `${metric} by ${split}: and back`);
  }
  const share = { ...trend, view: "compare", metric: "cost", split: "provider", compareAs: "share" };
  assert.equal(trendUnavailable(catalog, share), undefined);
  const there = switchView(catalog, share, "trend"), back = switchView(catalog, there, "compare");
  assert.deepEqual([there.metric, there.share, there.split, back.metric, back.compareAs, back.split], ["cost", undefined, "provider", "cost", undefined, "provider"], "the data moves; the shares return to the default");
  // Unit counts, means, medians and maxima are not record totals.
  for (const reduce of ["count", "mean", "median", "max"]) {
    const units = { ...share, unit: "conversation", reduce };
    assert.match(trendUnavailable(catalog, units), /over records/, reduce);
    assert.deepEqual(switchView(catalog, units, "trend").view, "compare", reduce);
  }
  assert.deepEqual(["metric", "view"].map(key => switchView(catalog, { ...share, unit: "conversation", reduce: "total" }, "trend")[key]), ["cost", "trend"], "a unit total adds up the same records");
  // A share needs groups to divide.
  assert.equal(normalizeAnalysis(catalog, { ...share, split: "none" }).compareAs, "bars");
  assert.equal(normalizeAnalysis(catalog, { ...switchView(catalog, { ...trend, metric: "percent", split: "type" }, "compare"), compareAs: "share" }).compareAs, "share", "token components partition the total");
  // Values return as bars, whatever display came before.
  assert.equal(normalizeAnalysis(catalog, switchView(catalog, { ...there, share: true }, "compare")).compareAs, "bars");
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

test("a weekday cycle counts complete days: the query starts at a local midnight", () => {
  const zone = "America/Denver", at = Date.parse("2026-10-16T20:00:00Z"); // 14:00 in Denver
  const preset = [{ field: "last_usage_at", op: ">=", value: "-30d" }, { field: "first_usage_at", op: "<", value: "+0s" }];
  const days = analysisQuery(catalog, { view: "cycle", cycle: "weekday", metric: "tokens", split: "none" }, preset, { zone, now: at });
  assert.deepEqual(days.query.where[0], { field: "last_usage_at", op: ">=", value: new Date(core.metricDateTime("2026-09-17", zone)).toISOString() }, "the date of now − 30d holds part of a day; start the next midnight");
  assert.deepEqual(days.query.where[1], preset[1]);
  assert.deepEqual(analysisQuery(catalog, { view: "cycle", cycle: "hour", metric: "tokens", split: "none" }, preset, { zone, now: at }).query.where, preset, "hours keep the exact start");
  assert.deepEqual(analysisQuery(catalog, { view: "compare", metric: "tokens", split: "none" }, preset, { zone, now: at }).query.where, preset);
  const midnight = [{ field: "last_usage_at", op: ">=", value: new Date(core.metricDateTime("2026-10-01", zone)).toISOString() }];
  assert.deepEqual(analysisQuery(catalog, { view: "cycle", cycle: "weekday", metric: "tokens", split: "none" }, midnight, { zone, now: at }).query.where, midnight, "a midnight start is already whole");
});

test("a date from another time zone before the first date is reported, never counted as whole", () => {
  // Recorded in Denver late on Sunday 4 October; selected by a Tokyo viewer from Monday 5 October.
  const result = { id: "analysis", buckets: [{ keys: ["2026-10-04"], value: 9, count: 1 }, { keys: ["2026-10-05"], value: 7, count: 1 }] };
  const zone = "Asia/Tokyo", from = core.metricDateTime("2026-10-05", zone), to = core.metricDateTime("2026-10-12", zone);
  const box = cycleResult(catalog, { view: "cycle", cycle: "weekday", metric: "tokens", split: "none", reduce: "total" }, result, { from, to }, zone);
  assert.deepEqual([box.result.buckets[6].value, box.result.buckets[0].value, box.slots, box.earlier], [0, 7, 7, 1]);
  assert.match(analysisNote(catalog, { view: "cycle", cycle: "weekday", metric: "tokens", split: "none" }), /left out and reported/);
});
test("a shared snapshot filtered to an hour groups the days holding that hour, as the service does", async () => {
  const hour = hourly[0].hour, where = [{ field: "hour", op: "=", value: hour }];
  const units = analysisQuery(catalog, { view: "compare", unit: "conversation", metric: "tokens", split: "model_family" }, where);
  const [grouped] = await runAnalysisQuery("usage", schema, units.query, { local: { rows, hourly } });
  const ids = new Set(hourly.filter(row => row.hour === hour).map(row => row.id.split("|").slice(0, 4).join("|")));
  const days = rows.filter(row => ids.has(row.id));
  assert.ok(days.length > 0 && days.length < rows.length);
  const expected = {};
  for (const row of days) expected[row.root_model_family] = (expected[row.root_model_family] ?? 0) + row.total_tokens;
  assert.deepEqual(Object.fromEntries(grouped.buckets.map(bucket => [bucket.keys[0], bucket.value])), expected);
  // The record path selects the same days.
  const records = analysisQuery(catalog, { view: "compare", metric: "tokens", split: "none" }, where);
  const [total] = await runAnalysisQuery("usage", schema, records.query, { local: { rows, hourly } });
  assert.equal(total.buckets[0].value, Object.values(expected).reduce((n, value) => n + value, 0));
});


test("component comparisons partition every matching word or input token, with full shares and record drill-downs", async () => {
  const wordRows = [
    { id: "one", provider: "codex", words: 100, typed_words: 20, pasted_words: 30, quoted_words: 10, resent_words: 10, template_words: 10, attachment_words: 5, harness_words: 10, automated_words: 5 },
    { id: "two", provider: "claude", words: 900, typed_words: 900 },
  ];
  const tokenRows = [{ provider: "codex", input_tokens: 100, uncached_input_tokens: 20, cache_read_input_tokens: 70, cache_creation_input_tokens: 10, output_tokens: 500 }];
  for (const [source, metric, split, records, total] of [[human, "words", "category", wordRows, 100], [catalog, "input", "input_type", tokenRows, 100]]) {
    const sourceSchema = schemas[source.dataset], where = [{ field: "provider", op: "=", value: "codex" }];
    const state = { view: "compare", metric, split, compareAs: "share", minSamples: 50 };
    assert.ok(validAnalysisState(source, state));
    assert.doesNotMatch(analysisNote(source, state), /hidden/, "a threshold cannot omit components from 100%");
    const analysis = analysisQuery(source, state, where);
    assert.equal(analysis.clause.display.kind, "pie");
    assert.deepEqual(analysis.query.where, where);
    const results = await runAnalysisQuery(source.dataset, sourceSchema, analysis.query, { local: { rows: records }, now });
    const result = analysisResult(analysis, results);
    assert.equal(result.error, undefined);
    assert.equal(result.buckets.reduce((sum, bucket) => sum + bucket.value, 0), total, "parts sum to the full selected population");
    assert.deepEqual(analysis.supporting.where, where);
    assert.equal(analysisFields(source, state, sourceSchema).at(-1).label, source.components[split].label);
    assert.equal(analysisLabels(source, state)[split](result.buckets[0].keys[0]), source.components[split].parts[0].label);
    const drilled = analysisInspectQuery(analysis, result.buckets[0], analysis.clause, sourceSchema, "compare");
    assert.deepEqual(core.evaluateMetrics(records, { ...drilled, aggregations: [{ id: "count", op: "count", groupBy: [] }] }, sourceSchema).metrics[0].buckets[0].value, 1);
    const selected = analysisQuery(source, { ...state, selection: { values: [result.buckets[0].keys[0]], excluded: false } }, where);
    const drawn = analysisResult(selected, results);
    assert.equal(drawn.buckets.length, 1);
    assert.equal(drawn.buckets[0].value + drawn.other.value, total, "a selected part keeps the rest in Other");
    assert.deepEqual(analysisResult(analysis, results.slice(1)).buckets, []);
    assert.match(analysisResult(analysis, results.map((entry, index) => index ? entry : { ...entry, coverage: "partial" })).error, /incomplete/);
    const empty = analysisResult(analysis, await runAnalysisQuery(source.dataset, sourceSchema, analysis.query, { local: { rows: [] }, now }));
    assert.ok(empty.buckets.every(bucket => bucket.value === 0));
    assert.equal(normalizeAnalysis(source, { ...state, metric: metric === "words" ? "typed" : "cost" }).split, "none", "incompatible measures leave the component split");
    assert.equal(switchView(source, state, "distribution").split, "none");
    assert.equal(switchView(source, state, "trend").split, split);
  }
});

test('standalone scatter plots pair dashboard metrics and keep point-type changes meaningful', async () => {
  const { scatterAnalysisCatalog: scatter } = await import('../../../src/scatter-analysis-catalog');
  const { scatterMetrics, scatterMetricOptions } = await import('../../../src/scatter-metrics');
  assert.deepEqual(catalogProblems(scatter), []);
  assert.equal(scatterMetricOptions(scatter,"conversation","tokens_cost","cache write")[0][0],"usage_cache_creation_input_tokens");
  assert.ok(scatterMetricOptions(scatter,"message","human_typed","session shape").length === 0);
  assert.deepEqual(viewOptions(scatter, {view:'scatter',metric:'human_typed',split:'none'}).map(([view])=>view), ['scatter']);
  for (const source of [catalog, human]) assert.ok(!viewOptions(source, source.trend).some(([view]) => view === 'scatter'));
  const state = normalizeAnalysis(scatter, {v:2,view:'scatter',unit:'conversation',metric:'human_typed',metricY:'tokens_cost',split:'provider'});
  assert.equal(state.metricY,'tokens_cost');
  const query = analysisQuery(scatter, state, []);
  assert.ok(query.query.grouping.source.where.some(term=>term.field==='point_kind' && term.value==='conversation'));
  const message = normalizeAnalysis(scatter, {...state,unit:'message'});
  assert.equal(message.metric,'human_typed');
  assert.notEqual(message.metricY,message.metric);
  assert.ok(scatter.measures[message.metricY].units.includes('message'));
  assert.equal(normalizeAnalysis(scatter,{...state,unit:'work'}).split,'none','a workspace may span providers');
  // Count each changes the data the chart holds and no view setting: the
  // reader has still chosen none, so Reset view stays hidden.
  const asked = {v:3,view:'scatter',unit:'conversation',metric:'human_typed',metricY:'tokens_cost',split:'provider'};
  const counted = changeState(scatter, asked, scatterUnitChange(scatter, asked, 'work'));
  assert.deepEqual(JSON.parse(JSON.stringify(counted)), {...asked, unit:'work', split:'none'});
  assert.equal(viewChosen(counted), false);
  // Workspaces force the provider colour off; that is no breakdown the
  // reader chose, so the scales they chose stay.
  const scaled = {...asked, scale:'logarithmic', scaleY:'linear'};
  const kept = changeState(scatter, scaled, scatterUnitChange(scatter, scaled, 'work'));
  assert.deepEqual(JSON.parse(JSON.stringify(kept)), {...scaled, unit:'work', split:'none'});
  assert.deepEqual(chosenView(kept), {scale:'logarithmic', scaleY:'linear'});
  // Choosing a breakdown still returns the view to its defaults.
  assert.equal(viewChosen(changeState(scatter, kept, {split:'provider'})), false);
  const session = normalizeAnalysis(scatter,{...state,unit:'agent_session',metricY:'session_shape_autonomy'});
  assert.equal(session.metric,'human_typed');
  assert.equal(session.metricY,'session_shape_autonomy');
  assert.ok(analysisQuery(scatter,session,[]).query.grouping.source.where.some(term=>term.field==='point_kind'&&term.value==='agent_session'));
  for (const metric of ['tokens_cost','session_shape_wall_seconds','session_shape_context_peak_tokens','calls_calls']) assert.ok(scatter.measures[metric].units.includes('agent_session'),metric);
  assert.equal(scatterMetricOptions(scatter,'agent_session','tokens_cost','work hours').length,0,'conversation activity is not assigned to a session');
  assert.equal(scatterMetrics.length,new Set(scatterMetrics.map(item=>item.id)).size,'source and extra metrics never duplicate');
  for (const item of scatterMetrics) assert.ok(item.group && item.measure.note && Object.keys(item.keys).length, item.id);
  const rows = [
    {id:'conversation:a',entity_id:'a',point_kind:'conversation',human_typed:8,tokens_cost:2},
    {id:'conversation:b',entity_id:'b',point_kind:'conversation',human_typed:0,tokens_cost:0},
    {id:'conversation:c',entity_id:'c',point_kind:'conversation',human_typed:5,tokens_cost:null},
  ];
  const chart = analysisQuery(scatter,{...state,split:'none'},[]);
  const result = core.evaluateMetrics(rows,chart.query,schemas.scatter,{now}).metrics[0];
  assert.deepEqual(result.buckets.filter(bucket=>bucket.value!==null&&bucket.y!==null).map(bucket=>[bucket.value,bucket.y]),[[8,2],[0,0]]);

  // Human and agent wall time overlap. Pairing either with another metric
  // must keep the two populations separate, including the concurrency ratio.
  const hours = [
    {id:'you',actor:'you',elapsed_hours:2,active_ms:2,elapsed_ms:2},
    {id:'agent-a',actor:'with_you',elapsed_hours:1,active_ms:2,elapsed_ms:1},
    {id:'agent-b',actor:'unattended',elapsed_hours:1,active_ms:4,elapsed_ms:1},
  ];
  for (const [id,expected] of [['work_hours_wall_hours',2],['work_hours_engaged_hours',2],['work_hours_at_once',3]]) {
    const {measure} = scatterMetrics.find(item=>item.id===id);
    const query = {...core.EMPTY_QUERY,where:measure.where,aggregations:[{id:'hours',...aggregate(measure),groupBy:[]}]};
    assert.equal(core.evaluateMetrics(hours,query,schemas.rhythm,{now}).metrics[0].buckets[0].value,expected,id);
  }
});


test("shared usage snapshots retain estimate provenance after filters and grouping", async () => {
  const source=rows.map((row,index)=>({...row,is_estimate:index===0}));
  const query=analysisQuery(catalog,{view:"compare",metric:"tokens",split:"provider",minSamples:1},[]).query;
  const mixed=await runAnalysisQuery("usage",schema,query,{local:{rows:source}});
  assert.equal(mixed.some(metric=>metric.is_estimate),true);
  const groupedQuery=analysisQuery(catalog,{view:"distribution",unit:"conversation",metric:"cost",split:"model_family",minSamples:1},[]).query;
  const grouped=await runAnalysisQuery("usage",schema,groupedQuery,{local:{rows:source}});
  assert.equal(grouped.some(metric=>metric.is_estimate),true);
  const measured=await runAnalysisQuery("usage",schema,{...query,where:[{field:"is_estimate",op:"=",value:false}]},{local:{rows:source}});
  assert.equal(measured.some(metric=>metric.is_estimate),false);
});
