// Cards of the Human Words, Carbon and Parallelism dashboards (and Pharos
// MCP's empty-search rate, in record-dashboard-parity.test.mjs), each opened
// on its card-destinations.ts state. As in record-dashboard-parity, the
// card's own query and the dashboard's run over the same ledger with Query
// Table's in-process evaluation and must report the same numbers, compared
// by full group keys.
import test from "node:test";
import assert from "node:assert/strict";
import factors from "../../../../carbon/co2_factors.json";
import { core, schemas, instantiateRecipe, recipeById, cardCoverQuery } from "../helpers/recipes.mjs";
import { now, writingMessages, parallelismRecords, carbonUsage } from "../helpers/bespoke-ledgers.mjs";
import { loadClassicScript } from "../helpers/classic-script.mjs";
import { analysisQuery, catalogProblems, cycleResult, normalizeAnalysis, selectGroups, trendAdjustments, validAnalysisState, readAnalysisLink, analysisVersion } from "../../../src/dashboard-analysis-model";
import { trendQuery, trendSeries } from "../../../src/dashboard-trend-model";
import { humanAnalysisCatalog as human, humanWhere } from "../../../src/human-analysis-catalog";
import { carbonAnalysisCatalog, carbonCatalogShape, deriveCarbon } from "../../../src/carbon-analysis-catalog";
import { parallelismAnalysisCatalog } from "../../../src/parallelism-analysis-catalog";
import { cardDashboardWhere } from "../../../src/card-exploration";
import { loadQueryPreview, clearPreviewCache } from "../../../src/curiosity-data";
import { compactTimelineBuckets } from "../../../src/column-marks";
import { buildCardTrend, cardTrendDays, cardTrendQueries } from "../../../src/card-trend";
import { withTimeRange } from "../../../src/chart-range";
import { weekdayMetric } from "../../../src/weekday-cover";
import { answerQuery } from "../../support/query-server.mjs";

const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
// The calculator under some non-default assumptions, and the usage records
// the service estimates under the same ones (carbon_settings).
const settings = { scenario: "high", pueScenario: "low", grid: "world", inputSensitivity: "third" };
const calculator = loadClassicScript("carbon.js", { pharosPrefs: { get: (key, fallback) => key === "pharos-carbon-v1" ? settings : fallback, set() {} } }).window.pharosCarbon;
const assumptions = { settings: calculator.settingsSnapshot(factors), factors, estimate: calculator.estimate };
const carbon = carbonAnalysisCatalog(assumptions), usage = carbonUsage(calculator.estimate, factors);
const saved = { counting: "workspace", gap: 60 }, parallelism = parallelismAnalysisCatalog(saved);
// The service serves the records of the saved settings; the dashboard's
// ledger also holds records of other settings, which it must leave out.
const records = parallelismRecords(), served = records.filter(row => row.counting === saved.counting && row.gap_minutes === saved.gap);
const catalogs = { human, carbon, parallelism };
const cardRows = { writing_messages: writingMessages(), usage: usage.rows, parallelism: served };
const dashboardRows = { writing_messages: cardRows.writing_messages, usage: usage.rows, parallelism: records };
const round = value => typeof value === "number" ? Number(value.toPrecision(12)) : value;
const byKeys = (buckets, scale = 1) => Object.fromEntries(buckets.map(bucket => [JSON.stringify(bucket.keys), round(bucket.value * scale)]));
// Carbon cards report kilograms; the dashboard measures grams.
const scales = { carbon: 1 / 1000 };

test("the three catalogs are well formed and read their older links", () => {
  for (const catalog of [human, carbon, carbonCatalogShape, parallelism]) assert.deepEqual(catalogProblems(catalog), [], catalog.dataset);
  // Human Words: the series endpoint's scope and splits.
  const read = state => normalizeAnalysis(human, state);
  assert.deepEqual([read({ scope: "typed", split: "kind" }).metric, read({ scope: "typed", split: "kind" }).split], ["typed", "none"]);
  assert.deepEqual([read({ scope: "all", split: "kind" }).metric, read({ scope: "all", split: "kind" }).split], ["words", "kind"]);
  assert.deepEqual([read({ scope: "all", split: "providers" }).metric, read({ scope: "all", split: "providers" }).split], ["words", "provider"]);
  assert.equal(read({ scope: "typed", split: "channels" }).split, "channel");
  assert.ok(validAnalysisState(human, { period: "day", rollingPeriods: 0, display: "interval", scale: "linear", seriesLimit: 15, scope: "typed", split: "repository_name", hours: 6, days: 3 }));
  // An older link's work filters read as message filters, and its legend
  // filter on the breakdown's field becomes the selection.
  const where = [{ field: "last_message_at", op: ">=", value: "-7d" }, { field: "first_message_at", op: "<", value: "+0s" }, { field: "providers", op: "=", value: "codex" }];
  const link = readAnalysisLink(human, { scope: "all", split: "providers" }, humanWhere(where));
  assert.deepEqual(link.where, [{ field: "sent_at", op: ">=", value: "-7d" }, { field: "sent_at", op: "<", value: "+0s" }]);
  assert.deepEqual(link.state.selection, { values: ["codex"], excluded: false });
  const current = [{ field: "sent_at", op: ">=", value: "-7d" }];
  assert.equal(humanWhere(current), current, "a current link is not rewritten");
  // Carbon: the model tier split, and filters that were always populations.
  assert.equal(normalizeAnalysis(carbonCatalogShape, { metric: "co2", split: "tier" }).split, "carbon_tier");
  assert.deepEqual(readAnalysisLink(carbonCatalogShape, { metric: "co2", split: "provider" }, [{ field: "provider", op: "=", value: "codex" }]).state.selection, undefined);
  // The typed scope's text source was one series; all words split by source.
  assert.deepEqual(trendAdjustments(human, { metric: "typed", split: "kind" }), ["Text source splits all user words only; Typed words is shown as one total."]);
});

// The card's cover metric and the dashboard its destination opens, with the
// card's population as the dashboard's filters (cardDestination).
function pair(id, { days = 7, at = now } = {}) {
  const recipe = recipeById(id), item = instantiateRecipe(recipe, days, new Date(at));
  const { topic, chart, dataset } = recipe.destination, catalog = catalogs[topic];
  assert.equal(catalog.dataset, dataset, id);
  const card = cardCoverQuery(item.query, recipe);
  const where = cardDashboardWhere(recipe.dataset, dataset, (item.query.grouping?.source ?? item.query).where);
  const analysis = analysisQuery(catalog, chart, where, { now: at, zone });
  const cardResult = core.evaluateMetrics(cardRows[recipe.dataset], card.query, schemas[recipe.dataset], { now: at }).metrics.find(metric => metric.id === card.metricId);
  const dashboard = core.evaluateMetrics(dashboardRows[dataset], analysis.query, schemas[dataset], { now: at }).metrics[0];
  assert.equal(cardResult.error, undefined, `${id}: card`);
  assert.equal(dashboard.error, undefined, `${id}: dashboard`);
  return { recipe, chart, card: cardResult, dashboard: selectGroups(dashboard, analysis.selection), analysis, where, state: normalizeAnalysis(catalog, chart), scale: scales[topic] ?? 1 };
}
const sameGroups = ({ recipe, card, dashboard, scale }) => {
  assert.ok(card.buckets.length > 1, `${recipe.id}: the fixture has groups to compare`);
  assert.deepEqual(byKeys(dashboard.buckets.filter(bucket => bucket.value), scale), byKeys(card.buckets.filter(bucket => bucket.value)), recipe.id);
};

// ----- Human Words: one record per message -----------------------------------

test("explaining-repos and human-word-mix: shares of typed words, and typed shares, by repository", () => {
  const shares = pair("explaining-repos");
  assert.deepEqual([shares.state.view, shares.state.compareAs, shares.state.metric, shares.state.split], ["compare", "share", "typed", "repository_name"]);
  sameGroups(shares);
  const mix = pair("human-word-mix");
  assert.deepEqual([mix.state.metric, mix.state.split], ["typed_share", "repository_name"]);
  sameGroups(mix);
  assert.ok(mix.dashboard.buckets.every(bucket => bucket.value > 0 && bucket.value < 1));
});

test("human-words-per-message, human-words-per-conversation and human-repo-conversation-depth: pooled and per-conversation means", () => {
  const perMessage = pair("human-words-per-message");
  assert.equal(perMessage.dashboard.buckets[0].value, perMessage.card.buckets[0].value);
  const perConversation = pair("human-words-per-conversation");
  assert.deepEqual([perConversation.state.unit, perConversation.state.reduce], ["conversation", "mean"]);
  assert.ok(Math.abs(perConversation.dashboard.buckets[0].value - perConversation.card.buckets[0].value) < 1e-9);
  const depth = pair("human-repo-conversation-depth", { days: 30 });
  for (const [key, value] of Object.entries(byKeys(depth.card.buckets))) assert.ok(Math.abs(byKeys(depth.dashboard.buckets)[key] - value) < 1e-9, key);
  assert.equal(depth.dashboard.buckets.length, depth.card.buckets.length);
});

test("human-message-lengths: the distribution of typed words per conversation", () => {
  const { recipe, card, analysis, where } = pair("human-message-lengths", { days: 30 });
  assert.equal(analysis.clause.distribution.kind, "histogram");
  // The card's histogram and the dashboard's bin the same conversation totals.
  const item = instantiateRecipe(recipe, 30, new Date(now));
  const conversations = core.applyQuery(cardRows.writing_messages, { ...core.EMPTY_QUERY, ...item.query, where: (item.query.grouping?.source ?? item.query).where, grouping: { source: { ...core.EMPTY_QUERY, where: (item.query.grouping?.source ?? item.query).where }, keys: ["conversation_id"], summaries: [{ id: "conversation_words", op: "sum", field: "typed_words" }], representative: null }, select: [{ field: "conversation_id" }, { field: "conversation_words" }], limit: 1000 }, schemas.writing_messages, { now }).rows;
  const { aggregations: _, ...units } = analysis.query;
  const drawn = core.applyQuery(cardRows.writing_messages, { ...units, limit: 1000 }, schemas.writing_messages, { now }).rows;
  assert.deepEqual(Object.fromEntries(drawn.map(row => [row.conversation_id, row.analysis_value])), Object.fromEntries(conversations.map(row => [row.conversation_id, row.conversation_words])));
  assert.equal(card.buckets[0].distribution.n ?? card.buckets.reduce((sum, bucket) => sum + bucket.count, 0), conversations.length);
  assert.ok(where.some(term => term.field === "typed_words"), "the card's population is the dashboard's");
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
    const reply = answerQuery(path.split("?")[0].split("/").at(-1), request, cardRows[dataset], schemas[dataset], { now });
    assert.ok(reply, path);
    return Response.json(reply);
  };
  t.after(() => { Object.assign(global, prior); clearPreviewCache(); });
}
test("human-words and human-repository-words-trend: typed words a day, and a 7-day mean by repository", async t => {
  previewServer(t);
  for (const [id, days] of [["human-words", 7], ["human-repository-words-trend", 30]]) {
    const recipe = recipeById(id), item = instantiateRecipe(recipe, days, new Date(now));
    const preview = await loadQueryPreview(recipe.dataset, item.query, schemas[recipe.dataset], { recipe: item, cover: item.cover, window: item.window, url: item.url });
    const card = compactTimelineBuckets(preview.buckets, preview.timeline ?? {});
    const where = cardDashboardWhere(recipe.dataset, human.dataset, (item.query.grouping?.source ?? item.query).where);
    const state = normalizeAnalysis(human, { ...recipe.destination.chart, seriesLimit: 1000 }), query = trendQuery(human, state, where, { now, zone });
    const built = trendSeries(human, state, query, core.evaluateMetrics(cardRows.writing_messages, query.query, schemas.writing_messages, { now }).metrics, { now });
    assert.equal(built.error, undefined);
    assert.deepEqual(built.buckets.map(bucket => bucket.key), card.map(bucket => bucket.key), `${id}: the same days`);
    // Series match by their raw values; the card's preview keys a missing
    // repository "Unspecified", the dashboard "".
    const labels = series => Object.fromEntries(series.map(item => [item.key, item.value === "Unspecified" ? "" : item.value ?? ""]));
    const named = (values, names) => Object.fromEntries(Object.entries(values).filter(([, value]) => value).map(([key, value]) => [names[key], round(value)]));
    // A single series is the total, whatever each names it.
    const single = series => series.length === 1 ? { [series[0].key]: "total" } : labels(series);
    card.forEach((bucket, index) => assert.deepEqual(named(built.buckets[index].values, single(built.series)), named(bucket.values, single(preview.series)), `${id} ${bucket.key}`));
    assert.ok(card.some(bucket => Object.values(bucket.values).some(value => value > 0)));
  }
});

// A trailing-window card through buildCardTrend, opened on "Last 30 days" at
// 14:00 (dashboard-parity.test.mjs): the dashboard's points and norm are the
// card's.
function trailingCard(id, scale = 1) {
  const recipe = recipeById(id), trend = recipe.trend, catalog = catalogs[recipe.destination.topic], lookback = trend.lookback ?? 7;
  const afternoon = new Date(now); afternoon.setHours(14, 0, 0, 0);
  const at = afternoon.getTime() < now ? afternoon.getTime() + 86400000 : afternoon.getTime();
  const { days, ends } = cardTrendDays(lookback, Boolean(trend.completeDays), at);
  const card = buildCardTrend(trend, lookback, cardTrendQueries(recipe.where ?? [], trend, days).map(query => core.evaluateMetrics(cardRows[trend.dataset ?? recipe.dataset], query, schemas[recipe.dataset], { now: at }).metrics), at).study;
  const where = [...cardDashboardWhere(recipe.dataset, catalog.dataset, recipe.where ?? []), ...withTimeRange([], catalog.time, undefined, undefined, "30d")];
  const state = { ...recipe.destination.chart, view: "trend" };
  assert.deepEqual([state.rollingPeriods, state.completePeriods === true, state.norm, state.split], [lookback, Boolean(trend.completeDays), true, "none"], id);
  const query = trendQuery(catalog, state, where, { now: at, zone });
  const built = trendSeries(catalog, state, query, core.evaluateMetrics(dashboardRows[catalog.dataset], query.query, schemas[catalog.dataset], { now: at }).metrics, { now: at });
  assert.equal(built.error, undefined);
  const skip = ends.length - built.buckets.length;
  assert.deepEqual(built.buckets.map(bucket => bucket.key), ends.slice(skip), `${id}: the card's days`);
  built.buckets.forEach((bucket, index) => {
    // An idle window of an additive measure draws nothing, which is zero.
    const expected = card.points[index + skip], drawn = bucket.values.total ?? (expected === 0 ? 0 : undefined);
    if (expected === null) assert.equal(drawn, undefined, bucket.key);
    else assert.ok(Math.abs(drawn * scale - expected) <= 1e-9 * Math.max(1, Math.abs(expected)), `${id} ${bucket.key}: ${drawn * scale} != ${expected}`);
  });
  assert.ok(card.points.some(point => point > 0), `${id}: the fixture has values`);
  assert.equal(built.normPoints, card.points.slice(0, -1).filter(point => point !== null).length);
  assert.ok(Math.abs(built.norm * scale - card.baseline) < 1e-9 * Math.max(1, card.baseline), `${id}: norm ${built.norm * scale} != baseline ${card.baseline}`);
}
test("human-messages-per-day and human-conversations-per-day: 7-day means over complete days, with their norms", () => {
  trailingCard("human-messages-per-day");
  trailingCard("human-conversations-per-day");
});

// Weekday and hour cards fold the same daily or hourly totals.
function weekdayPair(id) {
  const recipe = recipeById(id), catalog = catalogs[recipe.destination.topic], item = instantiateRecipe(recipe, 90, new Date(now));
  const source = cardCoverQuery(item.query, recipe).query, clause = { ...source.aggregations[0], dateGrouping: { ...source.aggregations[0].dateGrouping, timeZone: zone } };
  const daily = core.evaluateMetrics(cardRows[recipe.dataset], { ...source, aggregations: [clause] }, schemas[recipe.dataset], { now }).metrics[0];
  const card = weekdayMetric(clause, daily, { ...source, aggregations: [clause] }, now, recipe.weekday.unit);
  const where = cardDashboardWhere(recipe.dataset, catalog.dataset, (item.query.grouping?.source ?? item.query).where);
  const state = normalizeAnalysis(catalog, recipe.destination.chart), analysis = analysisQuery(catalog, state, where, { now, zone });
  const result = core.evaluateMetrics(dashboardRows[catalog.dataset], analysis.query, schemas[catalog.dataset], { now }).metrics[0];
  const start = where.find(term => term.field === catalog.time.first && term.op === ">=");
  const bounds = { from: Number(core.coerceValue("datetime", start.value, now)), to: now };
  const folded = cycleResult(catalog, state, result, bounds, zone);
  assert.deepEqual([state.view, state.cycle, state.reduce], ["cycle", "weekday", "median"], id);
  const summaries = entry => entry.result.buckets.map(bucket => [bucket.keys[0], JSON.stringify(bucket.distribution.summary, (_, value) => round(value))]);
  assert.deepEqual(summaries(folded), summaries(card), id);
  assert.equal(folded.slots, card.days, `${id}: the same complete days`);
  assert.ok(card.result.buckets.some(bucket => bucket.distribution.summary?.max > 0));
}
test("human-words-weekday and human-messages-weekday: daily totals by local weekday", () => {
  weekdayPair("human-words-weekday");
  weekdayPair("human-messages-weekday");
});

function hourPair(id, cardHours) {
  const recipe = recipeById(id), catalog = catalogs[recipe.destination.topic];
  // Whole local hours: the card's window starts at a local hour boundary.
  const at = new Date(now); at.setMinutes(0, 0, 0);
  const item = instantiateRecipe(recipe, 7, at), where = cardDashboardWhere(recipe.dataset, catalog.dataset, (item.query.grouping?.source ?? item.query).where);
  const state = normalizeAnalysis(catalog, recipe.destination.chart), analysis = analysisQuery(catalog, state, where, { now: at.getTime(), zone });
  assert.deepEqual([state.view, state.cycle, state.reduce], ["cycle", "hour", "total"], id);
  const result = core.evaluateMetrics(dashboardRows[catalog.dataset], analysis.query, schemas[catalog.dataset], { now: at.getTime() }).metrics[0];
  const from = where.find(term => term.field === catalog.time.first && (term.op === ">=" || term.op === ">"));
  const folded = cycleResult(catalog, state, result, { from: from ? Number(core.coerceValue("datetime", from.value, at.getTime())) : undefined, to: at.getTime() }, zone);
  const expected = cardHours(item, at.getTime());
  assert.deepEqual(Object.fromEntries(folded.result.buckets.filter(bucket => bucket.value).map(bucket => [bucket.keys[0], round(bucket.value)])), expected, id);
  assert.ok(Object.keys(expected).length > 3);
}
test("writing-hours: typed words by local hour", () => {
  hourPair("writing-hours", (item, at) => {
    // The card groups UTC hours; each lands at its local hour.
    const card = core.evaluateMetrics(cardRows.writing_messages, cardCoverQuery(item.query, recipeById("writing-hours")).query, schemas.writing_messages, { now: at }).metrics[0], hours = {};
    for (const bucket of card.buckets) { const hour = `${String(new Date(String(bucket.keys[0])).getHours()).padStart(2, "0")}:00`; hours[hour] = round((hours[hour] ?? 0) + bucket.value); }
    return hours;
  });
});

// ----- Carbon: usage under the user's assumptions ----------------------------

test("carbon-tiers, carbon-repositories, carbon-models, carbon-providers and carbon-effort: estimated CO₂e by group", () => {
  for (const [id, split, share] of [["carbon-tiers", "carbon_tier", true], ["carbon-repositories", "repository_name", false], ["carbon-models", "model_family", false], ["carbon-providers", "provider", true], ["carbon-effort", "effort", true]]) {
    const compared = pair(id);
    assert.deepEqual([compared.state.view, compared.state.metric, compared.state.split, compared.state.compareAs], ["compare", "co2", split, share ? "share" : "bars"], id);
    sameGroups(compared);
  }
});

test("carbon-trend: estimated CO₂e over each trailing 7 days, including today, with its norm", () => {
  trailingCard("carbon-trend", 1 / 1000);
});

test("Carbon sends the user's assumptions with every request, and splits token types with the page's own calculator", () => {
  assert.deepEqual(JSON.parse(carbon.request.params.carbon_settings), structuredClone(calculator.settingsSnapshot(factors)));
  assert.equal(carbonCatalogShape.request, undefined, "the shape for links sends nothing");
  const state = { metric: "co2", split: "type", period: "day" }, where = withTimeRange([], carbon.time, undefined, undefined, "7d");
  const query = trendQuery(carbon, state, where, { now, zone });
  assert.ok(query.query.aggregations.every(clause => JSON.stringify(clause.groupBy) === JSON.stringify(["day", "carbon_tier"])));
  const built = trendSeries(carbon, state, query, core.evaluateMetrics(usage.rows, query.query, schemas.usage, { now }).metrics, { now });
  assert.deepEqual(built.series.map(item => item.key), ["uncached_input", "cache_read", "cache_write", "output"]);
  // Each day's parts are the calculator's categories for that day's tokens
  // by tier, and add up to the service's estimate of the day's records.
  const days = {};
  for (const row of usage.rows) if (Date.parse(row.last_usage_at) >= Date.parse(core.coerceValue("datetime", where[0].value, now)) && Date.parse(row.first_usage_at) < now) (days[row.day] ??= []).push(row);
  for (const [day, rows] of Object.entries(days)) {
    const bucket = built.buckets.find(item => item.key === day), tokens = {};
    for (const row of rows) { const tier = (tokens[row.carbon_tier] ??= { uncached_input: 0, cache_read: 0, cache_write: 0, output: 0 }); tier.uncached_input += row.uncached_input_tokens; tier.cache_read += row.cache_read_input_tokens; tier.cache_write += row.cache_creation_input_tokens; tier.output += row.output_tokens; }
    const estimate = calculator.estimate(factors, { tokens });
    for (const category of ["uncached_input", "cache_read", "cache_write", "output"]) assert.ok(Math.abs(bucket.values[category] - estimate.grams(estimate.byCategory[category].wh)) < 1e-9, `${day} ${category}`);
    const stored = rows.reduce((sum, row) => sum + row.co2e_g, 0), parts = Object.values(bucket.values).reduce((sum, value) => sum + value, 0);
    assert.ok(Math.abs(parts - stored) < 1e-9 * Math.max(1, stored), `${day}: ${parts} != ${stored}`);
  }
  // Electricity is facility energy: IT energy times PUE.
  const energy = deriveCarbon(assumptions, [{ keys: ["large"], values: { output_tokens: 1e6 } }], "energy"), expected = calculator.estimate(factors, { tokens: { large: { uncached_input: 0, cache_read: 0, cache_write: 0, output: 1e6 } } });
  assert.ok(Math.abs(energy.output - expected.facilityWh) < 1e-9);
});

// ----- Parallelism: hour-by-agent records under the saved settings -------------

test("parallel-days and parallel-repositories: active hours by agents at once, and agent hours by repository", () => {
  const recipe = recipeById("parallel-days"), item = instantiateRecipe(recipe, 30, new Date(now));
  const state = normalizeAnalysis(parallelism, recipe.destination.chart);
  assert.deepEqual([state.view, state.metric, state.split], ["trend", "active_hours", "level_band"]);
  const where = cardDashboardWhere(recipe.dataset, parallelism.dataset, (item.query.grouping?.source ?? item.query).where);
  const query = trendQuery(parallelism, { ...state, seriesLimit: 1000 }, where, { now, zone });
  const built = trendSeries(parallelism, { ...state, seriesLimit: 1000 }, query, core.evaluateMetrics(records, query.query, schemas.parallelism, { now }).metrics, { now });
  const cover = cardCoverQuery(item.query, recipe), card = core.evaluateMetrics(served, cover.query, schemas.parallelism, { now }).metrics.find(metric => metric.id === cover.metricId), expected = {};
  for (const bucket of card.buckets) expected[`${bucket.keys[0]} s:${bucket.keys[1]}`] = round(bucket.value);
  const drawn = {};
  for (const bucket of built.buckets) for (const [key, value] of Object.entries(bucket.values)) if (value) drawn[`${bucket.key} ${key}`] = round(value);
  assert.deepEqual(drawn, expected);
  sameGroups(pair("parallel-repositories", { days: 30 }));
});

test("parallel-trend: agents at once while active, pooled over 7 complete days, with its norm", () => {
  trailingCard("parallel-trend");
});

test("parallel-hours, parallel-active-weekday and parallel-weekday: agent hours by local hour, and daily values by weekday", () => {
  hourPair("parallel-hours", (item, at) => {
    // The card groups the dataset's own local hour (hour_of_day).
    const card = core.evaluateMetrics(served, cardCoverQuery(item.query, recipeById("parallel-hours")).query, schemas.parallelism, { now: at }).metrics[0];
    return Object.fromEntries(card.buckets.filter(bucket => bucket.value).map(bucket => [`${bucket.keys[0]}:00`, round(bucket.value)]));
  });
  weekdayPair("parallel-active-weekday");
  weekdayPair("parallel-weekday");
});

test("Parallelism counts only records built with the saved settings, and says which", () => {
  const where = withTimeRange([], parallelism.time, undefined, undefined, "30d");
  const total = catalog => core.evaluateMetrics(records, analysisQuery(catalog, { view: "compare", metric: "agent_hours", split: "none" }, where, { now }).query, schemas.parallelism, { now }).metrics[0].buckets[0].value;
  const expected = served.filter(row => Date.parse(row.started_at) >= now - 30 * 86400000).reduce((sum, row) => sum + row.unit_hours, 0);
  assert.ok(Math.abs(total(parallelism) - expected) < 1e-9);
  assert.ok(total(parallelismAnalysisCatalog({ counting: "agent", gap: 15 })) < expected, "another setting's records are another population");
  assert.match(parallelismAnalysisCatalog({ counting: "agent", gap: 15 }).request.note, /top-level sessions and join turns across gaps up to 15 minutes/);
});

test("every 2c card destination opens a valid, versioned state", () => {
  for (const id of ["human-words", "human-word-total", "explaining-repos", "human-word-mix", "human-repository-words-trend", "human-messages-per-day", "human-conversations-per-day", "writing-hours", "human-words-weekday", "human-messages-weekday", "human-words-per-message", "human-words-per-conversation", "human-repo-conversation-depth", "human-message-lengths", "human-words-per-work", "human-example-phrases", "human-specific-feedback", "session-human-output",
    "carbon-trend", "carbon-tiers", "carbon-repositories", "carbon-models", "carbon-providers", "carbon-effort", "parallel-days", "parallel-trend", "parallel-hours", "parallel-repositories", "parallel-active-weekday", "parallel-weekday"]) {
    const { topic, chart } = recipeById(id).destination;
    assert.equal(chart.v, analysisVersion, id);
    assert.ok(validAnalysisState(catalogs[topic], chart), id);
  }
});
