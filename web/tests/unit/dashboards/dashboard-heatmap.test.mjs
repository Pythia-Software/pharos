// The trend's heatmap: a value on a continuous axis over time, from fine
// bins the service stores with each record (value-bins.ts), shaded by the
// records or the additive measure in each bin each period.
import test from "node:test";
import assert from "node:assert/strict";
import { core, schemas } from "../helpers/recipes.mjs";
import { heatmapAxis, heatmapBins, heatmapSource, normalizeAnalysis, trendAdjustments, validAnalysisState } from "../../../src/dashboard-analysis-model";
import { trendQuery, trendSeries, trendTitle } from "../../../src/dashboard-trend-model";
import { heatmapBinLabel, heatmapCellText, heatmapGrid, heatmapShade, trendHeatmap } from "../../../src/dashboard-heatmap-model";
import { binOf, binsSpanning, nextEdge } from "../../../src/value-bins";
import { sessionShapeAnalysisCatalog } from "../../../src/session-shape-analysis-catalog";
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
  assert.deepEqual([empty.total, empty.median, empty.p90], [0, null, null]);
  // Half the sessions are at or below 1: the median ends the 1 bin.
  assert.ok(Math.abs(full.median - heatmap.rows[1].y1) < 1e-9);
  assert.ok(full.p90 > heatmap.rows[8].y0 && full.p90 <= 1);
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
