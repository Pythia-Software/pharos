// Session Shape: the catalog's rules, its bands, and the dashboard's own
// panels (percentile ranks, the active-time bins, the archetype mix and the
// signal table's trailing range).
import test from "node:test";
import assert from "node:assert/strict";
import document from "../../../../schemas/session_shape.schema.json";
import { core, schemas } from "../helpers/recipes.mjs";
import { analysisQuery, catalogProblems, normalizeAnalysis } from "../../../src/dashboard-analysis-model";
import { trendQuery } from "../../../src/dashboard-trend-model";
import { analysisCatalogs } from "../../../src/analysis-catalogs";
import { dashboardDefaults } from "../../../src/dashboard-defaults";
import { archetypes, bandBreakdowns, sessionShapeAnalysisCatalog as catalog } from "../../../src/session-shape-analysis-catalog";
import { archetypeMix, mixFirstClauses, mixOther, mixRepositoryClauses, mixShownRepositories, readPercentiles, binClauses, binEdgeLabel, binLabel, binPosition, dayTotals, durationEdges, formatSeconds, formatSignal, nearestRankOffset, signalDefinitions, signalParts, signalRows, signalTableStart, signalVerdict, withoutDates } from "../../../src/session-shape-model";
import { viewFields } from "../../../src/view-contract";

const field = name => document.fields.find(item => item.name === name);
const now = Date.parse("2026-11-03T12:00:00Z");
const session = (id, values) => ({ id, conversation_id: id, root_conversation_id: id, started_at: "2026-11-02T10:00:00.000Z", day: "2026-11-02", week: "2026-11-02", month: "2026-11", ...values });

test("the catalog passes catalogProblems, opens on sessions by length as weekly shares, and is the session_shape topic's", () => {
  assert.deepEqual(catalogProblems(catalog), []);
  assert.equal(analysisCatalogs.session_shape, catalog);
  assert.deepEqual(normalizeAnalysis(catalog, dashboardDefaults.session_shape), normalizeAnalysis(catalog, { metric: "sessions", split: "length_band", period: "week", share: true }));
  // Root conversations hold sessions that differ in every breakdown.
  assert.deepEqual(catalog.units.conversation.breakdowns, {});
  assert.deepEqual(Object.keys(catalog.units.session.breakdowns), Object.keys(catalog.breakdowns));
  assert.deepEqual(catalog.cycle, { hour: { kind: "timestamp", field: "started_at" }, weekday: { kind: "timestamp", field: "started_at" } });
  assert.deepEqual(viewFields.session_signal.values.slice(0, 2), ["all", "failed_calls"]);
});

test("bands and archetypes keep the schema's order and labels", () => {
  for (const [id, band] of Object.entries(bandBreakdowns)) {
    const options = field(id).filter.values.options;
    assert.deepEqual(band.order, options.map(option => option.value), id);
    assert.deepEqual(band.values, Object.fromEntries(options.map(option => [option.value, option.label])), id);
    assert.deepEqual(catalog.breakdowns[id].order, band.order, id);
    assert.ok(field(band.field), `${id} summarizes ${band.field}`);
  }
  const options = field("archetype").filter.values.options;
  assert.deepEqual(archetypes.order, options.map(option => option.value));
  assert.deepEqual(catalog.breakdowns.archetype.values, Object.fromEntries(options.map(option => [option.value, option.label])));
});

test("rates pool their parts over the sessions with the evidence they need, never averaging", () => {
  const ratio = id => catalog.measures[id].ratio;
  assert.deepEqual(ratio("one_shot_rate"), ["one_shot_edit_turns", "edit_turns"]);
  assert.deepEqual(ratio("steering_load"), ["corrections", "typed_human_messages"]);
  assert.deepEqual(ratio("verification"), ["verified_handbacks", "edit_handbacks"]);
  assert.deepEqual(ratio("failure_rate"), ["failed_calls", "tool_calls"]);
  assert.deepEqual(ratio("autonomy"), ["tool_calls", "autonomy_prompts"]);
  assert.deepEqual(ratio("active_share"), ["active_seconds", "wall_seconds"]);
  for (const [id, measure] of Object.entries(catalog.measures)) {
    assert.notEqual(measure.op, "avg", id);
    if (measure.ratio) assert.equal(measure.additive, false, id);
    for (const name of [measure.field, measure.value, ...(measure.ratio ?? []).map(part => typeof part === "string" ? part : part.field), ...(measure.where ?? []).map(term => term.field)].filter(Boolean)) assert.ok(field(name), `${id} reads ${name}`);
  }
  assert.deepEqual(catalog.measures.steering_load.where, [{ field: "has_authorship", op: "=", value: "true" }]);
  assert.deepEqual(catalog.measures.context_peak.where, [{ field: "has_context_window", op: "=", value: "true" }]);
  // A pooled rate over two sessions is their summed parts' ratio, not the mean
  // of their rates: a session without authorship adds nothing.
  const rows = [session("a", { corrections: 1, typed_human_messages: 10, has_authorship: true }), session("b", { corrections: 1, typed_human_messages: 2, has_authorship: true }),
    session("c", { corrections: null, typed_human_messages: null, has_authorship: false })];
  const query = analysisQuery(catalog, { view: "compare", metric: "steering_load", split: "none" }, [], { now });
  const [result] = core.evaluateMetrics(rows, query.query, schemas.session_shape, { now }).metrics;
  assert.equal(result.buckets[0].value, 2 / 12);
  // The trend's sessions by band are weekly shares.
  const trend = trendQuery(catalog, normalizeAnalysis(catalog, dashboardDefaults.session_shape), [], { now });
  assert.ok(trend.query.aggregations.some(clause => clause.groupBy.includes("length_band")));
});

test("percentiles take the nearest rank, as the dataset's own do", () => {
  assert.equal(nearestRankOffset(0, 0.5), null);
  assert.equal(nearestRankOffset(1, 0.9), 0);
  assert.equal(nearestRankOffset(10, 0.5), 4);
  assert.equal(nearestRankOffset(10, 0.9), 8);
  assert.equal(nearestRankOffset(11, 0.9), 9);
  assert.equal(formatSeconds(42), "42 s");
  assert.equal(formatSeconds(720), "12 min");
  assert.equal(formatSeconds(4800), "1 h 20 min");
});

test("active-time bins count each session once and place the median and p90 within them", () => {
  const values = [0, 29, 30, 299, 300, 3600, 40000, null];
  const rows = values.map((active_seconds, index) => session(`s${index}`, { active_seconds }));
  const metrics = core.evaluateMetrics(rows, { ...core.EMPTY_QUERY, aggregations: binClauses("active_seconds", durationEdges) }, schemas.session_shape, { now }).metrics;
  const counts = metrics.map(metric => metric.buckets[0].value);
  assert.equal(counts.reduce((sum, value) => sum + value, 0), 7, "a session without timestamps is in no bin");
  assert.deepEqual([counts[0], counts[1], counts[3], counts[4], counts[8], counts[11]], [2, 1, 1, 1, 1, 1]);
  assert.equal(binLabel(durationEdges, 3), "2 min–5 min");
  assert.deepEqual([binEdgeLabel(durationEdges, 0), binEdgeLabel(durationEdges, 3), binEdgeLabel(durationEdges, 11)], ["0 s", "2 min", "8 h+"]);
  assert.equal(binPosition(durationEdges, 0), 0);
  assert.equal(binPosition(durationEdges, 15), 0.5);
  assert.equal(binPosition(durationEdges, 60), 2);
  assert.ok(Math.abs(binPosition(durationEdges, 3600 * Math.SQRT2) - 8.5) < 1e-9, "log-interpolated within a bin");
  assert.equal(binPosition(durationEdges, 1e9), durationEdges.length);
});

test("the archetype mix reads every session exactly however many repositories there are, folding the rest into Other", () => {
  // Twelve repositories: more than are shown, and their archetype pairs more
  // than one grouped request returns whole.
  const rows = [];
  for (let repository = 0; repository < 12; repository++) for (let index = 0; index <= repository; index++)
    rows.push(session(`r${repository}-${index}`, { repository_name: repository === 11 ? null : `repo-${repository}`, archetype: index % 2 ? "quick" : "deep" }));
  const run = clauses => core.evaluateMetrics(rows, { ...core.EMPTY_QUERY, aggregations: clauses }, schemas.session_shape, { now }).metrics;
  const [totals, repositories] = run(mixFirstClauses());
  const shown = mixShownRepositories(repositories);
  assert.equal(shown.folded, true);
  assert.deepEqual(shown.repositories, ["", "repo-10", "repo-9", "repo-8", "repo-7", "repo-6", "repo-5", "repo-4", "repo-3"], "the largest first, one fewer so the last row is Other");
  const mix = archetypeMix(totals.buckets, shown.repositories, run(mixRepositoryClauses(shown.repositories)).map(metric => metric.buckets), shown.folded);
  assert.deepEqual(mix.map(row => [row.key, row.total]), [["", 12], ["repo-10", 11], ["repo-9", 10], ["repo-8", 9], ["repo-7", 8], ["repo-6", 7], ["repo-5", 6], ["repo-4", 5], ["repo-3", 4], [mixOther, 6]]);
  assert.deepEqual(mix.at(-1).counts, { deep: 4, quick: 2 }, "Other is repo-0 to repo-2: every session counted once");
  assert.equal(mix.reduce((sum, row) => sum + row.total, 0), rows.length);
  // Ten or fewer repositories are all shown, with no Other.
  const few = mixShownRepositories({ buckets: [{ keys: ["a"], value: 2 }, { keys: [null], value: 1 }], groupCount: 2 });
  assert.deepEqual([few.repositories, few.folded], [["a", ""], false]);
  assert.deepEqual(archetypeMix([{ keys: ["quick"], value: 3 }], few.repositories, [[{ keys: ["quick"], value: 2 }], [{ keys: ["quick"], value: 1 }]], false).map(row => row.key), ["a", ""]);
});

test("percentiles read again when sessions change between requests, and a missing rank is no value", async () => {
  // The count changes once between the total and the ranks, then holds.
  const totals = [10, 11, 11, 11];
  let call = 0;
  const reads = [];
  const read = async offset => { const total = totals[Math.min(call++, totals.length - 1)]; reads.push([offset, total]); return { value: offset * 10, total }; };
  const first = await readPercentiles(read, [0.5, 0.9]);
  assert.deepEqual(first, { n: 11, values: [50, 90] });
  // A rank past the rows that remain returns no value rather than throwing.
  assert.deepEqual(await readPercentiles(async offset => ({ value: offset ? null : 1, total: 4 }), [0.5]), { n: 4, values: [null] });
  // Sessions that never settle are an error the panel shows.
  let n = 0;
  await assert.rejects(readPercentiles(async () => ({ value: 1, total: ++n }), [0.5]), /changed while they were read/);
});

test("the signal table compares the last seven days with the eight weeks before and says no detectable change inside them", () => {
  assert.deepEqual(signalVerdict(0.2, [0.1, 0.2, 0.3, 0.25]), { low: 0.1, high: 0.3, verdict: "within" });
  assert.equal(signalVerdict(0.3, [0.1, 0.2, 0.3, 0.25]).verdict, "within", "the range's ends are inside it");
  assert.equal(signalVerdict(0.31, [0.1, 0.2, 0.3, 0.25]).verdict, "above");
  assert.equal(signalVerdict(0.05, [0.1, 0.2, 0.3, 0.25]).verdict, "below");
  assert.equal(signalVerdict(0.5, [0.1, 0.2, 0.3]).verdict, "no-history");
  assert.equal(signalVerdict(null, [0.1, 0.2, 0.3, 0.4]).verdict, "no-evidence");

  const end = "2026-11-03";
  assert.equal(signalTableStart(end), "2026-09-02");
  // Failure rate: 10% in each earlier week; 30% in the last seven days.
  const results = [{ id: "tool_calls", buckets: [] }, { id: "failed_calls", buckets: [] }, { id: "tool_sessions", buckets: [] }];
  for (let week = 0; week <= 8; week++) {
    const day = new Date(Date.parse(`${end}T00:00:00Z`) - (week * 7 + 2) * 86_400_000).toISOString().slice(0, 10);
    results[0].buckets.push({ keys: [day], value: 100 });
    results[1].buckets.push({ keys: [day], value: week ? 10 : 30 });
    results[2].buckets.push({ keys: [day], value: 4 });
  }
  const rows = signalRows(dayTotals(results), end);
  const failures = rows.find(row => row.definition.id === "failed_calls");
  assert.deepEqual([failures.value, failures.low, failures.high, failures.history, failures.verdict, failures.sessions, failures.numerator, failures.denominator], [0.3, 0.1, 0.1, 8, "above", 4, 30, 100]);
  assert.equal(formatSignal(failures.definition, failures.value), "30%");
  const rejections = rows.find(row => row.definition.id === "rejections");
  assert.deepEqual([rejections.value, rejections.verdict], [0, "within"]);
  assert.equal(formatSignal(rejections.definition, 2.5), "2.5 per 100 calls");
  // A signal whose evidence never arrived is not enough evidence, not zero.
  assert.equal(rows.find(row => row.definition.id === "context_pressure").verdict, "no-evidence");
});

test("the signal table's parts execute over session rows, and its population drops only the dashboard's dates", () => {
  const rows = [session("a", { has_tool_data: true, has_authorship: true, has_context_window: false, tool_calls: 4, failed_calls: 1 }), session("b", { has_tool_data: false, has_authorship: true, has_context_window: true, tool_calls: null })];
  const clauses = signalParts.map(part => ({ id: part.id, op: "sum", expression: part.expression, groupBy: ["day"] }));
  assert.ok(clauses.length <= 2 * 11, "two requests of at most 11 clauses each");
  // Query Table takes at most 20 clauses a request, so the panel sends two.
  const metrics = [clauses.slice(0, 11), clauses.slice(11)].flatMap(part => core.evaluateMetrics(rows, { ...core.EMPTY_QUERY, aggregations: part }, schemas.session_shape, { now }).metrics);
  const value = id => metrics.find(metric => metric.id === id).buckets[0].value;
  assert.deepEqual([value("tool_sessions"), value("authored_sessions"), value("both_sessions"), value("window_sessions"), value("tool_calls"), value("failed_calls")], [1, 2, 1, 1, 4, 1]);
  for (const definition of signalDefinitions) for (const part of [definition.numerator, definition.denominator, definition.evidence]) assert.ok(signalParts.some(item => item.id === part), `${definition.id} needs ${part}`);
  const where = [{ field: "started_at", op: ">=", value: "2026-10-01" }, { field: "repository_name", op: "=", value: "pharos" }, { field: "day", op: "<", value: "2026-11-01" }];
  assert.deepEqual(withoutDates(where), [{ field: "repository_name", op: "=", value: "pharos" }]);
});
