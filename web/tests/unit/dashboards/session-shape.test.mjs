// Session Shape: the catalog's rules, its bands, the dashboard's own panels
// (the signal table's trailing range), and the cards that open it.
import test from "node:test";
import assert from "node:assert/strict";
import document from "../../../../schemas/session_shape.schema.json";
import { core, schemas, queryRecipes, recipeById, instantiateRecipe, cardCoverQuery } from "../helpers/recipes.mjs";
import { analysisQuery, catalogProblems, changeState, heatmapAxis, heatmapSource, nativeCycleResult, normalizeAnalysis, selectGroups, trendDrawing } from "../../../src/dashboard-analysis-model";
import { heatmapGrid, trendHeatmap } from "../../../src/dashboard-heatmap-model";
import { binOf } from "../../../src/value-bins";
import { trendQuery, trendSeries } from "../../../src/dashboard-trend-model";
import { cardDashboardWhere } from "../../../src/card-exploration";
import { filterBoxSamples } from "../../../src/compact-box-model";
import { loadQueryPreview, clearPreviewCache } from "../../../src/curiosity-data";
import { buildCardTrend, cardTrendDays, cardTrendQueries } from "../../../src/card-trend";
import { withTimeRange } from "../../../src/chart-range";
import { weekdayMetric, weekdays } from "../../../src/weekday-cover";
import { answerQuery } from "../../support/query-server.mjs";
import { analysisCatalogs } from "../../../src/analysis-catalogs";
import { dashboardDefaults } from "../../../src/dashboard-defaults";
import { archetypes, bandBreakdowns, sessionShapeAnalysisCatalog as catalog } from "../../../src/session-shape-analysis-catalog";
import { dayTotals, formatSeconds, formatSignal, signalDefinitions, signalParts, signalRows, signalTablePopulation, signalTableStart, signalVerdict, withoutDates } from "../../../src/session-shape-model";
import { viewFields } from "../../../src/view-contract";

const field = name => document.fields.find(item => item.name === name);
const now = Date.parse("2026-11-03T12:00:00Z");
const session = (id, values) => ({ id, conversation_id: id, root_conversation_id: id, started_at: "2026-11-02T10:00:00.000Z", day: "2026-11-02", week: "2026-11-02", month: "2026-11", ...values });

test("the catalog passes catalogProblems, opens on sessions by length as a daily heatmap over trailing weeks, and is the session_shape topic's", () => {
  assert.deepEqual(catalogProblems(catalog), []);
  assert.equal(analysisCatalogs.session_shape, catalog);
  const opened = normalizeAnalysis(catalog, dashboardDefaults.session_shape);
  assert.deepEqual([opened.metric, opened.split, opened.display, opened.period, opened.rollingPeriods], ["sessions", "length_band", "heatmap", "day", 7]);
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
  // The trend's sessions by length are a heatmap of their messages' bins.
  const source = heatmapSource(catalog, normalizeAnalysis(catalog, dashboardDefaults.session_shape));
  const trend = trendQuery(source.catalog, source.state, [], { now });
  assert.ok(trend.query.aggregations.some(clause => clause.groupBy.includes("messages_bin")));
});

// Each chart's default view follows its data; the View controls change any
// of it, and a choice holds until the breakdown or analysis changes.
test("Session Shape's default views: heatmaps of a band's value, lines for rates, columns for totals", () => {
  const drawn = change => { const state = normalizeAnalysis(catalog, { view: "trend", ...change }); return [state.display, state.share, trendDrawing(catalog, state)]; };
  assert.deepEqual(drawn({ metric: "sessions", split: "length_band" }), ["heatmap", false, "trend"]);
  assert.deepEqual(drawn({ metric: "active_hours", split: "archetype" }), ["heatmap", false, "trend"]);
  // A rate by band has no total to spread over the band's value: a line per band.
  assert.deepEqual(drawn({ metric: "failure_rate", split: "archetype" }), ["interval", false, "trend_line"]);
  assert.equal(heatmapAxis(catalog, { metric: "failure_rate", split: "archetype" }), undefined);
  for (const metric of ["autonomy", "active_time", "turn_cycle", "context_peak"]) assert.deepEqual(drawn({ metric, split: "none" }), ["heatmap", false, "trend"], metric);
  assert.deepEqual(drawn({ metric: "failure_rate", split: "none" }), ["interval", false, "trend_line"]);
  // A single smoothed line draws its mean; columns draw each day as counted.
  assert.equal(normalizeAnalysis(catalog, { view: "trend", metric: "failure_rate", split: "none" }).norm, true);
  assert.equal(normalizeAnalysis(catalog, { view: "trend", metric: "sessions", split: "none" }).norm, false);
  assert.deepEqual(drawn({ metric: "active_hours", split: "repository_name" }), ["interval", false, "trend"]);
  assert.deepEqual(drawn({ metric: "failure_rate", split: "model_family" }), ["interval", false, "trend_line"]);
  // A day's rates are smoothed over a week, and a band's heatmap; a week's
  // are not, nor are a day's columns.
  const rolling = (metric, split, period) => normalizeAnalysis(catalog, { view: "trend", metric, split, period }).rollingPeriods;
  assert.deepEqual([rolling("failure_rate", "model_family", "day"), rolling("failure_rate", "model_family", "week"), rolling("sessions", "length_band", "day"), rolling("sessions", "repository_name", "day")], [7, 0, 7, 0]);
  // Outside Over time, long-tailed times and counts are on a log axis; a
  // count split into a few groups is not.
  const scale = (view, metric, split = "provider") => normalizeAnalysis(catalog, { view, metric, split }).scale;
  assert.deepEqual([scale("distribution", "turn_cycle"), scale("distribution", "context_peak"), scale("compare", "sessions"), scale("compare", "sessions", "agent_kind"), scale("compare", "failure_rate")], ["logarithmic", "linear", "logarithmic", "linear", "linear"]);
  // What the reader chooses is drawn instead, where the chart can draw it.
  const chosen = normalizeAnalysis(catalog, { view: "trend", metric: "sessions", split: "repository_name", display: "cumulative", share: true, scale: "logarithmic", rollingPeriods: 0 });
  assert.deepEqual([chosen.display, chosen.share, chosen.scale, chosen.rollingPeriods], ["cumulative", true, "logarithmic", 0]);
  assert.equal(normalizeAnalysis(catalog, { view: "trend", metric: "sessions", split: "length_band", display: "interval" }).display, "interval", "a heatmap's band may draw as columns");
  assert.equal(normalizeAnalysis(catalog, { view: "distribution", metric: "turn_cycle", split: "provider", scale: "linear" }).scale, "linear");
  assert.equal(normalizeAnalysis(catalog, { view: "compare", metric: "sessions", split: "harness", compareAs: "share" }).compareAs, "share");
  // A new breakdown returns the view to its defaults.
  const regrouped = normalizeAnalysis(catalog, changeState(catalog, { view: "trend", metric: "sessions", split: "repository_name", display: "cumulative" }, { split: "length_band" }));
  assert.equal(regrouped.display, "heatmap");
});

test("durations read as people say them", () => {
  assert.equal(formatSeconds(42), "42 s");
  assert.equal(formatSeconds(720), "12 min");
  assert.equal(formatSeconds(4800), "1 h 20 min");
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
  // Its window bounds started_at, with operators the field's schema enables:
  // the service rejects a range on the text field day.
  const population = signalTablePopulation(where, "2026-10-31");
  assert.deepEqual(population.slice(0, 1), [{ field: "repository_name", op: "=", value: "pharos" }]);
  assert.deepEqual(population.slice(1).map(term => [term.field, term.op, new Date(term.value).getTime()]),
    [["started_at", ">=", new Date("2026-08-30T00:00:00").getTime()], ["started_at", "<", new Date("2026-11-01T00:00:00").getTime()]]);
  for (const term of population) assert.ok(core.opsForField(document.fields.find(field => field.name === term.field)).includes(term.op), `${term.field} ${term.op}`);
});

// ----- Cards: each card's cover is the dashboard state it opens --------------
//
// The card's own query and the dashboard's run over the same sessions with
// Query Table's in-process evaluation and must report the same numbers,
// compared by full group keys (record-dashboard-parity.test.mjs).
const pad = value => String(value).padStart(2, "0");
const localDay = date => `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
const cardNow = Date.parse("2026-10-16T18:00:00Z");
// Sessions every few hours over 40 days. Some lack tool data, authorship or
// a known context window, so each rate's population is a part of them.
// Rows carry the bands and fine bins the service derives from their values.
const edges = { autonomy_band: [0.5, 1, 2, 5, 10], turn_cycle_band: [10, 30, 60, 180, 600], context_band: [0.1, 0.25, 0.5, 0.75, 0.9] };
const banded = (id, value) => value === null ? null : bandBreakdowns[id].order[edges[id].filter(edge => value >= edge).length];
const binned = (value, scheme) => value === null ? null : binOf(value, scheme);
const withBands = row => ({ ...row, autonomy_band: banded("autonomy_band", row.autonomy), turn_cycle_band: banded("turn_cycle_band", row.turn_cycle_p50_seconds), context_band: banded("context_band", row.context_peak_ratio),
  ...Object.fromEntries(Object.entries({ messages: "messages_bin", typed_human_messages: "typed_messages_bin", tool_calls: "tool_calls_bin", active_seconds: "active_seconds_bin", autonomy: "autonomy_bin", turn_cycle_p50_seconds: "turn_cycle_bin", context_peak_ratio: "context_peak_bin" })
    .map(([value, field]) => [field, binned(row[value], Object.values(catalog.breakdowns).find(breakdown => breakdown.bins?.field === field).bins.scheme)])) });
const ledger = Array.from({ length: 360 }, (_, index) => {
  const at = new Date(cardNow - (index % 40 + 1) * 86400000); at.setHours(index * 5 % 24, index % 60, 0, 0);
  const monday = new Date(at); monday.setDate(monday.getDate() - (monday.getDay() + 6) % 7);
  const tools = index % 9 !== 0, authored = index % 7 !== 0, window = index % 4 !== 0, typed = authored ? index % 5 * 3 : null;
  const calls = tools ? index % 11 * 4 + 1 : null, edits = tools && authored ? index % 6 : null, handbacks = tools && authored ? index % 4 : null;
  return withBands({
    id: `session-${index}`, conversation_id: `session-${index}`, root_conversation_id: `session-${index % 300}`, started_at: at.toISOString(), last_message_at: new Date(at.getTime() + index % 9 * 7_200_000).toISOString(), day: localDay(at), week: localDay(monday), month: localDay(at).slice(0, 7),
    provider: ["claude", "codex", "antigravity"][index % 3], harness: ["claude-code", "codex/codex_exec", "antigravity-cli", "conductor"][index % 4], model_family: ["opus", "gpt-6", "gemini", "sonnet"][index % 4 === 3 ? 3 : index % 3],
    repository_name: ["pharos", "query-table", null][index % 3 === 2 && index % 2 ? 2 : index % 2], length_band: ["1-5", "6-15", "16-30", "31-60", "61-120", "121+"][index % 6],
    archetype: typed === null ? null : typed ? ["quick", "standard", "deep", "marathon"][index % 4] : "automated", messages: index % 6 * 10 + 2,
    has_tool_data: tools, has_authorship: authored, has_context_window: window, has_timestamps: index % 13 !== 0,
    active_seconds: index % 13 === 0 ? null : (index % 17 + 1) * 97, active_hours: index % 13 === 0 ? null : (index % 17 + 1) * 97 / 3600, wall_seconds: (index % 17 + 1) * 210, turn_cycle_p50_seconds: authored && typed ? index % 8 * 15 + 5 : null,
    typed_human_messages: typed, autonomy_prompts: authored ? Math.max(1, typed) : null, autonomy: tools && authored ? calls / Math.max(1, typed) : null,
    tool_calls: calls, failed_calls: tools ? index % 5 : null, corrections: authored ? index % 3 : null, edit_turns: edits, one_shot_edit_turns: edits === null ? null : Math.max(0, edits - index % 2),
    edit_handbacks: handbacks, verified_handbacks: handbacks === null ? null : Math.max(0, handbacks - index % 3), context_peak_ratio: window ? index % 10 / 10 + 0.05 : null,
  });
});
const shapeRound = value => typeof value === "number" ? Number(value.toPrecision(12)) : value;
const shapeKeys = (buckets, value = bucket => bucket.value) => Object.fromEntries(buckets.map(bucket => [JSON.stringify(bucket.keys), shapeRound(value(bucket))]));
const cardItem = (id, days = 30, at = cardNow) => { const recipe = recipeById(id); return { recipe, item: instantiateRecipe(recipe, days, new Date(at)) }; };
const dashboardWhere = item => cardDashboardWhere("session_shape", "session_shape", (item.query.grouping?.source ?? item.query).where);

function cardPair(id) {
  const { recipe, item } = cardItem(id), chart = recipe.destination.chart;
  assert.equal(recipe.destination.topic, "session_shape", id);
  const card = cardCoverQuery(item.query, recipe), analysis = analysisQuery(catalog, chart, dashboardWhere(item), { now: cardNow });
  const cardResult = core.evaluateMetrics(ledger, card.query, schemas.session_shape, { now: cardNow }).metrics.find(metric => metric.id === card.metricId);
  const dashboard = selectGroups(core.evaluateMetrics(ledger, analysis.query, schemas.session_shape, { now: cardNow }).metrics[0], analysis.selection);
  assert.equal(cardResult.error, undefined, `${id}: card`);
  assert.equal(dashboard.error, undefined, `${id}: dashboard`);
  assert.ok(cardResult.buckets.length >= 1, `${id}: the card draws something`);
  return { recipe, card: cardResult, dashboard, analysis, state: normalizeAnalysis(catalog, chart) };
}

test("Session Shape cards: every one opens the Session Shape dashboard with its population as the filters", () => {
  const cards = queryRecipes.filter(recipe => recipe.dataset === "session_shape");
  assert.ok(cards.length >= 12, "a collection of cards");
  assert.equal(new Set(cards.map(recipe => recipe.title)).size, cards.length, "titles are distinct");
  for (const recipe of cards) {
    assert.equal(recipe.category, "sessions", recipe.id);
    assert.equal(recipe.destination.topic, "session_shape", recipe.id);
    const { item } = cardItem(recipe.id), params = new URL(item.url, "http://localhost").searchParams;
    assert.equal(params.get("analyze_topic"), "session_shape", recipe.id);
    const filters = core.decodeQueryStrict(params.get("analyze_filters")).where;
    for (const term of recipe.where ?? []) assert.ok(filters.some(filter => JSON.stringify(filter) === JSON.stringify(term)), `${recipe.id}: ${JSON.stringify(term)} is a dashboard filter`);
  }
  // A partial population is the dashboard's, and its query's.
  for (const [id, term] of [["session-autonomy", { field: "archetype", op: "!=", value: "automated" }], ["session-model-failure-rates", { field: "model_family", op: "!=", value: "<synthetic>" }]]) {
    const { analysis } = cardPair(id);
    assert.ok(analysis.query.where.some(filter => JSON.stringify(filter) === JSON.stringify(term)), id);
  }
});

test("Session Shape rate cards: pooled rates by group over the covered sessions, with the dashboard's session minimum", () => {
  for (const id of ["session-model-failure-rates", "session-kind-failure-rates", "session-repository-verification"]) {
    const { recipe, card, dashboard, state } = cardPair(id);
    assert.deepEqual([state.view, state.minSamples], ["compare", Number(recipe.exploration.having[0].value)], id);
    assert.deepEqual(shapeKeys(dashboard.buckets), shapeKeys(card.buckets), id);
    assert.ok(card.buckets.some(bucket => bucket.value > 0 && bucket.value < 1), `${id}: rates between zero and one`);
  }
  // Steering leaves out sessions whose prompts are not all classified; the
  // failure rate, sessions without tool data.
  const pooled = (rows, top, bottom) => rows.reduce((n, row) => n + row[top], 0) / rows.reduce((n, row) => n + row[bottom], 0);
  const from = cardNow - 30 * 86400000;
  const opus = ledger.filter(row => row.model_family === "opus" && row.has_tool_data && Date.parse(row.started_at) >= from);
  assert.equal(shapeKeys(cardPair("session-model-failure-rates").card.buckets)['["opus"]'], shapeRound(pooled(opus, "failed_calls", "tool_calls")));
});

test("Session Shape count and distribution cards draw the dashboard's bars and boxes", () => {
  // Hours read as whole hours, not three decimals.
  const { recipe: hoursRecipe, item: hoursItem } = cardItem("session-repository-hours");
  const hoursCover = cardCoverQuery(hoursItem.query, hoursRecipe);
  assert.deepEqual(hoursCover.query.aggregations.find(clause => clause.id === hoursCover.metricId).display.format, { kind: "number", decimals: 0 });
  for (const id of ["session-sources", "session-repository-hours"]) {
    const { card, dashboard } = cardPair(id);
    assert.ok(card.buckets.every(bucket => bucket.value > 0), id);
    assert.deepEqual(shapeKeys(dashboard.buckets), shapeKeys(card.buckets), id);
  }
  const summary = bucket => JSON.stringify(bucket.distribution.summary, (_, value) => shapeRound(value));
  for (const id of ["session-active-time", "session-turn-cycle", "session-autonomy", "session-model-context"]) {
    const { recipe, card, dashboard, state } = cardPair(id);
    assert.equal(state.minSamples, recipe.cover.minSamples, id);
    assert.deepEqual(shapeKeys(dashboard.buckets, summary), shapeKeys(filterBoxSamples(card, recipe.cover.minSamples).buckets, summary), id);
  }
});

// Every heatmap card's grid is the dashboard's heatmap: the same days, bins
// and cells, whether its value is a breakdown's band or a measure's own.
test("Session Shape heatmap cards: the card's grid of trailing seven-day sessions by bin is the dashboard's heatmap, day by day", async t => {
  const prior = { fetch: global.fetch, location: global.location };
  global.location = { origin: "http://parity.test" };
  t.mock.timers.enable({ apis: ["Date"], now: cardNow });
  global.fetch = async (url, init = {}) => {
    const path = String(url);
    if (path.includes("computed-columns")) return Response.json([]);
    return Response.json(answerQuery(path.split("/").at(-1), init.body ? JSON.parse(init.body) : undefined, ledger, schemas.session_shape, { now: cardNow }));
  };
  t.after(() => { Object.assign(global, prior); clearPreviewCache(); });
  for (const id of ["session-length-mix", "session-archetype-mix", "session-autonomy-trend", "session-turn-cycle-trend", "session-context-trend", "session-active-time-trend", "session-length-hours", "session-length-tool-calls"]) {
    const { item } = cardItem(id);
    clearPreviewCache();
    const preview = await loadQueryPreview("session_shape", item.query, schemas.session_shape, { recipe: item, cover: item.cover, window: item.window, url: item.url });
    assert.ok(preview.grid, `${id}: a grid`);
    const where = dashboardWhere(item), state = normalizeAnalysis(catalog, { ...dashboardDefaults.session_shape, ...item.destination.chart });
    assert.deepEqual([state.view, state.display, state.period, state.rollingPeriods], ["trend", "heatmap", "day", 7], id);
    const source = heatmapSource(catalog, state), query = trendQuery(source.catalog, source.state, where, { now: cardNow });
    const built = trendSeries(source.catalog, { ...source.state, nonzero: false }, query, core.evaluateMetrics(ledger, query.query, schemas.session_shape, { now: cardNow }).metrics, { where, now: cardNow });
    assert.equal(built.error, undefined, id);
    const axis = heatmapAxis(catalog, state);
    const grid = heatmapGrid(trendHeatmap(built.buckets, built.series, axis.bins.scheme), { period: "day", noun: item.unit ?? "sessions", label: axis.bins.label, rollingPeriods: 7 });
    assert.deepEqual(grid.columns, preview.grid.columns, `${id}: the same days`);
    assert.equal(grid.rows.length, preview.grid.rows.length, `${id}: the same bins`);
    assert.deepEqual(grid.values, preview.grid.values, id);
    assert.ok(grid.cells.flat().some(cell => cell !== null && cell > 0), `${id}: cells`);
  }
});

// The failure rate and the sparklines of your sessions: each card's points
// are the dashboard's trailing seven complete days, and over 30 days its
// norm. Over 90 days the dashboard draws more days; the card's are its last.
test("Session Shape trend cards: each pools its measure over 7 complete days, as the dashboard's trend, with its norm", () => {
  const cards = { "session-failure-trend": "failure_rate", "session-count-trend": "sessions", "session-hours-trend": "active_hours" };
  const afternoon = new Date(cardNow); afternoon.setHours(14, 0, 0, 0);
  const at = afternoon.getTime() < cardNow ? afternoon.getTime() + 86400000 : afternoon.getTime();
  for (const [id, metric] of Object.entries(cards)) {
    const recipe = recipeById(id), trend = recipe.trend;
    const { days, ends } = cardTrendDays(trend.lookback, true, at);
    const card = buildCardTrend(trend, trend.lookback, cardTrendQueries(recipe.where, trend, days).map(query => core.evaluateMetrics(ledger, query, schemas.session_shape, { now: at }).metrics), at).study;
    assert.ok(card.points.some(point => point > 0), id);
    const state = normalizeAnalysis(catalog, recipe.destination.chart);
    assert.deepEqual([state.view, state.metric, state.split, state.period, state.rollingPeriods, state.completePeriods, state.norm], ["trend", metric, "none", "day", 7, true, true], id);
    for (const window of recipe.days) {
      const where = [...recipe.where, ...withTimeRange([], catalog.time, undefined, undefined, `${window}d`)];
      const query = trendQuery(catalog, state, where, { now: at, zone: Intl.DateTimeFormat().resolvedOptions().timeZone });
      const built = trendSeries(catalog, state, query, core.evaluateMetrics(ledger, query.query, schemas.session_shape, { now: at }).metrics, { now: at });
      assert.equal(built.error, undefined, id);
      const drawn = new Map(built.buckets.map(bucket => [bucket.key, bucket.values.total])), shared = ends.filter(end => drawn.has(end));
      assert.ok(shared.length >= Math.min(window, ends.length) - 7, `${id} ${window}d: ${shared.length} shared days`);
      for (const end of shared) {
        const point = card.points[ends.indexOf(end)];
        assert.ok(point === null ? drawn.get(end) === null : Math.abs(drawn.get(end) - point) < 1e-9, `${id} ${window}d ${end}: ${drawn.get(end)} != ${point}`);
      }
      if (window === 30) assert.ok(Math.abs(built.references?.[0]?.value - card.baseline) < 1e-9, `${id}: norm ${built.references?.[0]?.value} != baseline ${card.baseline}`);
    }
  }
});

test("Session Shape weekday cards: daily sessions by the local weekday they start or last interact on are the dashboard's weekday cycle", () => {
  for (const id of ["session-weekday", "session-last-weekday"]) weekdayParity(id);
});

function weekdayParity(id) {
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const { recipe, item } = cardItem(id, 90);
  const source = cardCoverQuery(item.query, recipe).query, clause = { ...source.aggregations[0], dateGrouping: { ...source.aggregations[0].dateGrouping, timeZone: zone } };
  const daily = core.evaluateMetrics(ledger, { ...source, aggregations: [clause] }, schemas.session_shape, { now: cardNow }).metrics[0];
  const card = weekdayMetric(clause, daily, { ...source, aggregations: [clause] }, cardNow, recipe.weekday.unit);
  const state = normalizeAnalysis(catalog, recipe.destination.chart), analysis = analysisQuery(catalog, state, dashboardWhere(item), { now: cardNow, zone });
  assert.deepEqual([state.view, state.cycle, state.reduce, analysis.native], ["cycle", "weekday", "median", true]);
  const native = nativeCycleResult(analysis, core.evaluateMetrics(ledger, analysis.query, schemas.session_shape, { now: cardNow }).metrics[0]);
  const summaries = buckets => Object.fromEntries(buckets.map(bucket => [bucket.keys[0], JSON.stringify(bucket.distribution.summary, (_, value) => shapeRound(value))]));
  assert.deepEqual(summaries(native.result.buckets.map(bucket => ({ ...bucket, keys: [weekdays[bucket.keys[0] - 1]] }))), summaries(card.result.buckets));
  assert.ok(card.result.buckets.some(bucket => bucket.distribution.summary?.max > 0));
}


test("session shape reads disclose older and missing statistics, and clear once current", async () => {
  const { sessionShapeFreshnessNote } = await import("../../../src/session-shape-freshness");
  const { withSessionShapeFreshness } = await import("../../../src/query-previews");
  assert.equal(sessionShapeFreshnessNote(null), "");
  assert.equal(sessionShapeFreshnessNote({ state: "current" }), "");
  const coverage = { state: "pending", pending_sessions: 5, stale_sessions: 2, missing_sessions: 3 };
  const note = sessionShapeFreshnessNote(coverage);
  assert.match(note, /across the library/);
  assert.match(note, /2 agent sessions use older statistics/);
  assert.match(note, /3 agent sessions are not included yet/);
  assert.match(note, /totals may change/);
  assert.match(sessionShapeFreshnessNote({ state: "pending", missing_sessions: 1 }), /1 agent session is not included/);
  const preview = { summary: "4 sessions", note: "Available evidence" };
  const marked = withSessionShapeFreshness(preview, "session_shape", { coverage });
  assert.equal(marked.summary, "≈4 sessions");
  assert.match(marked.note, /older statistics.*Available evidence/);
  assert.equal(withSessionShapeFreshness(preview, "session_shape", { coverage: { state: "current" } }), preview);
  assert.equal(withSessionShapeFreshness(preview, "usage", { coverage }), preview);
});
