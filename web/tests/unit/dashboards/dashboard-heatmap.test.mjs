// The trend's heatmap: a value on a continuous axis over time, from fine
// bins the service stores with each record (value-bins.ts), shaded by the
// records or the additive measure in each bin each period.
import test from "node:test";
import assert from "node:assert/strict";
import { core, schemas } from "../helpers/recipes.mjs";
import { analysisQuery, distributionBins, heatmapAxis, heatmapBins, heatmapSource, normalizeAnalysis, percentileLimit, trendAdjustments, validAnalysisState } from "../../../src/dashboard-analysis-model";
import { levelOf, trendQuery, trendSeries, trendTitle } from "../../../src/dashboard-trend-model";
import { heatmapBinLabel, heatmapCellText, heatmapColumnText, heatmapGrid, heatmapShade, trendHeatmap } from "../../../src/dashboard-heatmap-model";
import { binOf, binsSpanning, nextEdge } from "../../../src/value-bins";
import { periodKey } from "../../../src/chart-period";
import { sessionShapeAnalysisCatalog } from "../../../src/session-shape-analysis-catalog";
import { humanAnalysisCatalog as human } from "../../../src/human-analysis-catalog";
import { histogramBarText, histogramOf } from "../../../src/dashboard-histogram";
import fixture from "../../../../testdata/value-bins.json";

// The engine's rules, without Session Shape's own choice of chart.
const catalog = { ...sessionShapeAnalysisCatalog, present: undefined };

test("a value's bin and the next edge match the service's, case for case", () => {
  for (const { scheme, pairs } of fixture.cases) for (const [value, bin] of pairs) assert.equal(binOf(value, scheme), bin, `${scheme.scale} ${value}`);
  for (const { scheme, pairs } of fixture.next) for (const [low, high] of pairs) assert.equal(nextEdge(low, scheme), high, `${scheme.scale} after ${low}`);
  // Bins between the observed ones take their places; a log zero is apart.
  assert.deepEqual(binsSpanning([0, 1, 2], { scale: "log" }), [0, 1, 1.2, 1.5, 2]);
  assert.deepEqual(binsSpanning([3, 12], { scale: "count" }), [3, 4, 5, 6, 7, 8, 9, 10, 12]);
  assert.deepEqual(binsSpanning([0.05, 0.1], { scale: "linear", per: 40 }), [0.05, 0.075, 0.1]);
});

const day = key => ({ key, date: new Date(`${key}T00:00:00`), values: {} });
const series = edges => edges.map(edge => ({ key: `s:${edge}`, label: String(edge), value: String(edge), color: "" }));

test("rows sit at their place on a log axis, the zero bin apart below, and quantiles read from the bins", () => {
  const buckets = [{ ...day("2026-11-01"), values: { "s:0": 1, "s:1": 1, "s:8": 2 } }, day("2026-11-02")];
  const heatmap = trendHeatmap(buckets, series([0, 1, 8]), { scale: "count" });
  // Every bin from 1 to 8 is a row, 0 apart and first.
  assert.deepEqual(heatmap.rows.map(row => row.low), [0, 1, 2, 3, 4, 5, 6, 7, 8]);
  assert.ok(heatmap.rows[0].zero && heatmap.rows[0].y1 < heatmap.rows[1].y0, "zero sits below with a gap");
  // On a log axis 1–2 is taller than 8–9.
  const tall = heatmap.rows[1].y1 - heatmap.rows[1].y0, short = heatmap.rows[8].y1 - heatmap.rows[8].y0;
  assert.ok(tall > 4 * short);
  assert.equal(heatmap.rows.at(-1).y1, 1);
  const [full, empty] = heatmap.columns;
  assert.equal(full.total, 4);
  // The median and 90th percentile are traced unless asked otherwise.
  assert.deepEqual(heatmap.levels, ["p50", "p90"]);
  assert.deepEqual([empty.total, ...empty.levels], [0, null, null]);
  // Half the sessions are at or below 1: the median ends the 1 bin.
  const [median, p90] = full.levels;
  assert.ok(Math.abs(median.y - heatmap.rows[1].y1) < 1e-9);
  assert.ok(Math.abs(median.value - 2) < 1e-9);
  assert.ok(p90.y > heatmap.rows[8].y0 && p90.y <= 1 && p90.value >= 8);
  // Other levels on request; a mean has no place in the bins.
  assert.deepEqual(trendHeatmap(buckets, series([0, 1, 8]), { scale: "count" }, ["mean", "p75"]).levels, ["p75"]);
  assert.deepEqual(heatmap.ticks.map(tick => tick.value), [0, 1, 2, 5]);
  assert.equal(heatmap.place(-1), heatmap.place(0));
  assert.equal(heatmap.place(1000), null);
  assert.equal(heatmapShade(1, 4), Math.log(2) / Math.log(5));
  assert.equal(heatmapShade(4, 4), 1);
  assert.equal(heatmapShade(0, 4), null);
});

test("a bin, a cell and a card's grid say what they hold", () => {
  assert.equal(heatmapBinLabel({ low: 3, high: 4, zero: false }, { scale: "count" }), "3");
  assert.equal(heatmapBinLabel({ low: 10, high: 12, zero: false }, { scale: "count" }), "10–11");
  assert.equal(heatmapBinLabel({ low: 0, high: 0, zero: true }, { scale: "log" }), "0 or less");
  assert.equal(heatmapBinLabel({ low: 1.2, high: 1.5, zero: false }, { scale: "log" }), "1.2–1.5");
  const heatmap = trendHeatmap([{ ...day("2026-11-01"), values: { "s:10": 3, "s:12": 1 } }, day("2026-11-02")], series([10, 12]), { scale: "count" });
  const options = { period: "day", noun: "sessions", label: "Messages" };
  assert.match(heatmapCellText(heatmap, 0, 0, options), /Messages 10–11\n3 sessions \(75% of 4\)/);
  assert.match(heatmapCellText(heatmap, 0, 1, options), /No sessions in this period/);
  // Hovering says what the lines are and where they stand.
  assert.match(heatmapCellText(heatmap, 0, 0, options), /Median line: [\d.]+\n90th %ile line: [\d.]+$/);
  assert.match(heatmapColumnText(heatmap, 0, options), /4 sessions\nMedian line/);
  assert.match(heatmapCellText(heatmap, 0, 0, { ...options, rollingPeriods: 7 }), /7-period average/);
  const grid = heatmapGrid(heatmap, options);
  assert.deepEqual(grid.rows, ["12–14", "10–11"]);
  assert.deepEqual(grid.cells, [[Math.log(2) / Math.log(4), null], [1, null]]);
  assert.equal(grid.values[1][0], "3 sessions (75% of 4)");
  assert.deepEqual(grid.legend, ["0", "3"]);
});

test("a heatmap draws a binned value: sessions by it without a breakdown, or a total by its band", () => {
  const asked = { view: "trend", metric: "sessions", split: "length_band", display: "heatmap", share: true };
  assert.ok(validAnalysisState(catalog, asked));
  const state = normalizeAnalysis(catalog, asked);
  assert.deepEqual([state.display, state.share], ["heatmap", false]);
  assert.equal(trendTitle(catalog, asked), "Sessions per day by messages per session");
  assert.deepEqual(trendAdjustments(catalog, asked), []);
  assert.deepEqual(heatmapAxis(catalog, { metric: "active_hours", split: "length_band" }), { breakdown: "length_band", bins: catalog.breakdowns.length_band.bins, counted: false });
  assert.equal(heatmapAxis(catalog, { metric: "autonomy", split: "none" })?.counted, true);
  assert.equal(trendTitle(catalog, { ...asked, metric: "autonomy", split: "none" }), "Sessions per day by tool calls per typed message");
  // Harness has no value to bin, a rate does not add up across sessions,
  // and a measure without a binned band has no distribution of its own.
  for (const change of [{ split: "harness" }, { metric: "failure_rate", split: "length_band" }, { metric: "failure_rate", split: "none" }]) {
    const moved = normalizeAnalysis(catalog, { ...asked, ...change });
    assert.equal(moved.display, "interval", JSON.stringify(change));
    assert.match(trendAdjustments(catalog, { ...asked, ...change }).join(" "), /heatmap/, JSON.stringify(change));
  }
});

test("a heatmap queries the counted records or the measure by the value's bins, within its population and with a value", () => {
  const counted = heatmapSource(catalog, normalizeAnalysis(catalog, { view: "trend", metric: "autonomy", split: "none", display: "heatmap" }));
  assert.equal(counted.state.split, heatmapBins);
  assert.deepEqual(counted.catalog.breakdowns[heatmapBins].fields, ["autonomy_bin"]);
  assert.deepEqual([counted.catalog.measures.autonomy.op, counted.catalog.measures.autonomy.additive], ["count", true]);
  assert.deepEqual(counted.catalog.measures.autonomy.where, [...catalog.measures.autonomy.where, { field: "autonomy_bin", op: "is_not_null", value: "" }]);
  const totalled = heatmapSource(catalog, normalizeAnalysis(catalog, { view: "trend", metric: "active_hours", split: "archetype", display: "heatmap" }));
  assert.deepEqual([totalled.catalog.measures.active_hours.op, totalled.catalog.measures.active_hours.field], ["sum", "active_hours"]);
  assert.deepEqual(totalled.catalog.breakdowns[heatmapBins].fields, ["typed_messages_bin"]);
  // Any other trend queries the state as it is.
  const lines = normalizeAnalysis(catalog, { view: "trend", metric: "sessions", split: "harness" });
  assert.equal(heatmapSource(catalog, lines).state, lines);
});

test("over session rows, every bin is a series and each period's sessions add up", () => {
  const now = Date.parse("2026-11-04T12:00:00Z");
  const rows = Array.from({ length: 60 }, (_, index) => {
    const at = new Date(now - (index % 6 + 1) * 86400000), key = at.toISOString().slice(0, 10), messages = 1 + index * 7;
    return { id: `s${index}`, conversation_id: `s${index}`, started_at: at.toISOString(), day: key, week: key, month: key.slice(0, 7), messages, messages_bin: binOf(messages, { scale: "count" }) };
  });
  const state = normalizeAnalysis(catalog, { view: "trend", metric: "sessions", split: "length_band", display: "heatmap", period: "day", rollingPeriods: 0 });
  const source = heatmapSource(catalog, state), where = [{ field: "started_at", op: ">=", value: "-7d" }];
  const trend = trendQuery(source.catalog, source.state, where, { now });
  const built = trendSeries(source.catalog, source.state, trend, core.evaluateMetrics(rows, trend.query, schemas.session_shape, { now }).metrics, { now });
  assert.equal(built.error, undefined);
  assert.equal(built.series.length, new Set(rows.map(row => row.messages_bin)).size, "no bin folds into Other");
  const heatmap = trendHeatmap(built.buckets, built.series, catalog.breakdowns.length_band.bins.scheme);
  assert.equal(heatmap.columns.reduce((sum, column) => sum + column.total, 0), 60);
  assert.equal(heatmap.rows[0].low, 1);
  assert.ok(heatmap.rows.every((row, index) => index === 0 || row.y0 >= heatmap.rows[index - 1].y1 - 1e-9), "rows ascend without overlap");
});

test("Human Words heatmaps bin each conversation's period total, including zero, and preserve the message filters", () => {
  const now = Date.parse("2026-11-04T18:00:00Z"), zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const rows = [
    { id: "a1", conversation_id: "a", sent_at: "2026-11-01T10:00:00Z", typed_words: 4, words: 5, provider: "codex" },
    { id: "a2", conversation_id: "a", sent_at: "2026-11-01T11:00:00Z", typed_words: 6, words: 7, provider: "codex" },
    { id: "a3", conversation_id: "a", sent_at: "2026-11-02T10:00:00Z", typed_words: 3, words: 4, provider: "codex" },
    { id: "b", conversation_id: "b", sent_at: "2026-11-01T12:00:00Z", typed_words: 10, words: 11, provider: "codex" },
    { id: "c", conversation_id: "c", sent_at: "2026-11-01T13:00:00Z", typed_words: 0, words: 6, provider: "codex" },
    { id: "d", conversation_id: "d", sent_at: "2026-11-01T14:00:00Z", typed_words: 100, words: 100, provider: "claude" },
    { id: "missing", conversation_id: "", sent_at: "2026-11-01T14:00:00Z", typed_words: 100, words: 100, provider: "codex" },
  ];
  const where = [{ field: "sent_at", op: ">=", value: new Date(2026, 10, 1).toISOString() }, { field: "sent_at", op: "<", value: new Date(2026, 10, 4).toISOString() }, { field: "provider", op: "=", value: "codex" }];
  const state = normalizeAnalysis(human, { view: "trend", metric: "typed", split: "none", display: "heatmap", period: "day", nonzero: false });
  assert.equal(state.display, "heatmap");
  assert.equal(trendTitle(human, state), "Conversations per day by typed words per conversation");
  const axis = heatmapAxis(human, state), source = heatmapSource(human, state);
  const query = trendQuery(source.catalog, source.state, where, { now, zone });
  assert.equal(query.query.aggregations[0].groupLimit, 10000);
  assert.deepEqual(query.query.aggregations[0].dateGrouping, { field: "sent_at", interval: "day", timeZone: zone, includeEmpty: false, range: "query" });
  const results = core.evaluateMetrics(rows, query.query, schemas.writing_messages, { now }).metrics;
  assert.equal(results[0].error, undefined);
  const built = trendSeries(source.catalog, source.state, query, results, { now });
  assert.equal(built.error, undefined);
  const grid = trendHeatmap(built.buckets, built.series, axis.bins.scheme);
  const column = key => grid.columns.find(item => item.key === key);
  const countAt = (key, bin) => column(key).cells[grid.rows.findIndex(row => row.low === bin)];
  assert.equal(countAt("2026-11-01", 10), 2, "two conversations each wrote ten words, even though one wrote two messages");
  assert.equal(countAt("2026-11-01", 0), 1, "a conversation with only supplied text still has zero typed words");
  assert.equal(countAt("2026-11-02", 3), 1, "the same conversation counts again on another day");
  assert.equal(column("2026-11-03").total, 0);
  assert.match(heatmapCellText(grid, grid.rows.findIndex(row => row.low === 10), grid.columns.findIndex(item => item.key === "2026-11-01"), { period: "day", noun: "conversations", label: axis.bins.label }), /2 conversations/);
  for (const partial of [{ ...results[0], coverage: "partial" }, { ...results[0], groupCount: results[0].buckets.length + 1 }]) {
    assert.match(trendSeries(source.catalog, source.state, query, [partial], { now }).error, /complete totals/i);
  }
  const missingValue = { ...results[0], buckets: results[0].buckets.map((bucket, index) => index ? bucket : { ...bucket, value: null }) };
  assert.match(trendSeries(source.catalog, source.state, query, [missingValue], { now }).error, /needs a word total/i);
  const all = normalizeAnalysis(human, { ...state, metric: "words" });
  assert.equal(heatmapAxis(human, all).bins.label, "User words per conversation");
});

test("multi-day Human Words heatmaps combine a conversation before binning and average rolling windows", () => {
  const first = new Date(2026, 10, 1, 12), second = new Date(2026, 10, 2, 12);
  if (periodKey(first, "days", 2) !== periodKey(second, "days", 2)) { first.setDate(first.getDate() + 1); second.setDate(second.getDate() + 1); }
  const now = new Date(2026, 10, 6, 12).getTime();
  const state = normalizeAnalysis(human, { view: "trend", metric: "typed", split: "none", display: "heatmap", period: "days", days: 2, rollingPeriods: 2, rollingOp: "sum" });
  const source = heatmapSource(human, state), where = [{ field: "sent_at", op: ">=", value: first.toISOString() }];
  assert.equal(source.state.rollingOp, "mean");
  const trend = trendQuery(source.catalog, source.state, where, { now });
  const results = [{ id: "trend", coverage: "exact", buckets: [
    { keys: [first.toISOString(), "a"], count: 1, value: 4 },
    { keys: [second.toISOString(), "a"], count: 1, value: 6 },
  ] }];
  const built = trendSeries(source.catalog, source.state, trend, results, { now });
  const grid = trendHeatmap(built.buckets, built.series, heatmapAxis(human, state).bins.scheme);
  const column = grid.columns.find(item => item.key === periodKey(first, "days", 2));
  assert.equal(column.cells[grid.rows.findIndex(row => row.low === 10)], 0.5, "one conversation at ten words, averaged over two periods");
});

test("the %ile control's levels: a line's reference over earlier points, a heatmap's traces per period", () => {
  const line = { view: "trend", metric: "sessions", split: "none" };
  // The older norm reads as the mean; levels draw in their own order.
  assert.deepEqual(normalizeAnalysis(catalog, { ...line, norm: true }).percentiles, ["mean"]);
  const asked = normalizeAnalysis(catalog, { ...line, percentiles: ["p90", "mean"] });
  assert.deepEqual([asked.percentiles, asked.norm], [["mean", "p90"], true]);
  assert.ok(validAnalysisState(catalog, { ...line, percentiles: ["p50"] }));
  assert.ok(!validAnalysisState(catalog, { ...line, percentiles: ["p42"] }));
  // A heatmap traces its median and 90th percentile, and never a mean.
  const heat = normalizeAnalysis(catalog, { ...line, metric: "autonomy", display: "heatmap" });
  assert.deepEqual([heat.percentiles, heat.norm], [["p50", "p90"], false]);
  assert.match(percentileLimit(catalog, heat, "mean"), /not a mean/);
  assert.equal(percentileLimit(catalog, heat, "p99"), undefined);
  assert.deepEqual(normalizeAnalysis(catalog, { ...line, metric: "autonomy", display: "heatmap", percentiles: [] }).percentiles, []);
  // Shares have no reference level, so none is drawn and the control says why.
  const shares = normalizeAnalysis(catalog, { ...line, split: "harness", share: true, percentiles: ["p50"] });
  assert.deepEqual(shares.percentiles, []);
  assert.match(percentileLimit(catalog, shares, "p50"), /Shares/);
  assert.match(trendAdjustments(catalog, { ...line, split: "harness", share: true, percentiles: ["p50"] }).join(" "), /reference level/);
  // Levels read a mean or interpolated percentiles of the earlier points.
  assert.equal(levelOf([1, 2, 3, 10], "mean"), 4);
  assert.equal(levelOf([1, 2, 3, 10], "p50"), 2.5);
  assert.equal(levelOf([5], "p90"), 5);
});

test("Distribution offers only the combinations that draw something: binned histograms, no spikes at zero, no duplicate units", () => {
  const distribution = { view: "distribution", unit: "records", split: "none" };
  // A long-tailed value without a breakdown is a histogram over its fine bins.
  const state = normalizeAnalysis(catalog, { ...distribution, metric: "active_time", scale: "logarithmic" });
  assert.equal(distributionBins(catalog, state)?.field, "active_seconds_bin");
  const { clause, query } = analysisQuery(catalog, state, []);
  assert.deepEqual(clause.groupBy, ["active_seconds_bin"]);
  // Zero is a bin apart, so it is kept.
  assert.ok(!query.where.some(term => term.op === ">"));
  assert.equal(distributionBins(catalog, normalizeAnalysis(catalog, { ...distribution, metric: "messages" }))?.field, "messages_bin");
  // With a breakdown it is boxes, and a log axis leaves out zero.
  const boxes = analysisQuery(catalog, normalizeAnalysis(catalog, { ...distribution, metric: "active_time", split: "provider", scale: "logarithmic" }), []);
  assert.equal(boxes.clause.distribution.kind, "box");
  assert.ok(boxes.query.where.some(term => term.field === "active_seconds" && term.op === ">" && term.value === "0"));
  // A rare signal's distribution is a spike at zero: another measure is drawn.
  assert.notEqual(normalizeAnalysis(catalog, { ...distribution, metric: "rejections" }).metric, "rejections");
  // A unit of one record each distributes as the records do.
  assert.equal(normalizeAnalysis(catalog, { ...distribution, unit: "session", metric: "messages" }).unit, "records");
  assert.equal(normalizeAnalysis(catalog, { ...distribution, unit: "session", metric: "failure_rate" }).unit, "session");
  // ...and its totals in Compare are the records' own.
  assert.equal(normalizeAnalysis(catalog, { view: "compare", unit: "session", metric: "messages", split: "provider", reduce: "total" }).reduce, "mean");
});

test("a binned histogram places its bars on the value's axis and marks its median and 90th percentile", () => {
  const bins = catalog.breakdowns.length_band.bins;
  const histogram = histogramOf([{ low: 1, count: 6 }, { low: 3, count: 2 }, { low: 10, count: 2 }], bins);
  assert.deepEqual(histogram.levels, ["p50", "p90"]);
  assert.equal(histogram.columns[0].total, 10);
  assert.ok(histogram.rows.every((row, index) => index === 0 || row.y0 >= histogram.rows[index - 1].y1 - 1e-9));
  assert.match(histogramBarText(histogram, 0, { label: "Messages", noun: "sessions" }), /Messages 1\n6 sessions \(60%\)/);
  assert.ok(histogram.columns[0].levels[0].value < 2, "the median is in the first bin");
});
