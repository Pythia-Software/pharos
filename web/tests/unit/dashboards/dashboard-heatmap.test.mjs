// The trend's heatmap display: each period's share of records in each group
// of an ordered breakdown, a rate's value in each group, or a measure's
// distribution over its own band.
import test from "node:test";
import assert from "node:assert/strict";
import { core, schemas } from "../helpers/recipes.mjs";
import { heatmapKind, heatmapSource, normalizeAnalysis, trendAdjustments, validAnalysisState } from "../../../src/dashboard-analysis-model";
import { trendQuery, trendSeries, trendTitle } from "../../../src/dashboard-trend-model";
import { heatmapCellText, heatmapGrid, heatmapShade, trendHeatmap } from "../../../src/dashboard-heatmap-model";
import { sessionShapeAnalysisCatalog } from "../../../src/session-shape-analysis-catalog";

// The engine's rules, without Session Shape's own choice of chart.
const catalog = { ...sessionShapeAnalysisCatalog, present: undefined };

const day = key => ({ key, date: new Date(`${key}T00:00:00`), values: {} });

test("rows follow the breakdown's order, then groups outside it, then the empty group", () => {
  const series = [{ key: "s:2-5", label: "2–5", value: "2-5" }, { key: "other", label: "Other (2)" }, { key: "s:", label: "Not enough evidence", value: "" }, { key: "s:<0.5", label: "Under 0.5", value: "<0.5" }, { key: "s:odd", label: "odd", value: "odd" }];
  const heatmap = trendHeatmap([day("2026-11-01")], series, ["<0.5", "0.5-1", "1-2", "2-5"]);
  assert.deepEqual(heatmap.rows.map(row => row.key), ["s:<0.5", "s:2-5", "s:odd", "other", "s:"]);
});

test("each cell is its period's share; an empty period has no shares, not zeroes", () => {
  const series = [{ key: "a", label: "Low", value: "low" }, { key: "b", label: "High", value: "high" }];
  const buckets = [{ ...day("2026-11-01"), values: { a: 3, b: 1 } }, day("2026-11-02"), { ...day("2026-11-03"), values: { b: 2 } }];
  const heatmap = trendHeatmap(buckets, series, ["low", "high"]);
  assert.deepEqual(heatmap.columns.map(column => [column.total, column.cells]), [[4, [0.75, 0.25]], [0, [null, null]], [2, [0, 1]]]);
  assert.equal(heatmap.max, 1);
  assert.equal(heatmapShade(null, heatmap.max), null);
  assert.equal(heatmapShade(0.25, 0.5), 0.5);
  const text = heatmapCellText(heatmap, 0, 0, { period: "day", noun: "sessions" });
  assert.match(text, /Low: 75%/);
  assert.match(text, /3 of 4 sessions/);
  assert.match(heatmapCellText(heatmap, 1, 1, { period: "day", noun: "sessions" }), /No sessions in this period/);
  assert.match(heatmapCellText(heatmap, 0, 0, { period: "day", noun: "sessions", rollingPeriods: 7 }), /7-period average/);
});

test("a heatmap shades a total's shares or a rate's values by an ordered breakdown, or a banded measure's distribution, and draws no share itself", () => {
  const asked = { view: "trend", metric: "sessions", split: "autonomy_band", display: "heatmap", share: true };
  assert.ok(validAnalysisState(catalog, asked));
  assert.ok(!validAnalysisState(catalog, { ...asked, display: "mosaic" }));
  const state = normalizeAnalysis(catalog, asked);
  assert.deepEqual([state.display, state.share], ["heatmap", false]);
  assert.equal(trendTitle(catalog, asked), "Share of sessions per day by calls per typed message band");
  assert.deepEqual(trendAdjustments(catalog, asked), []);
  const kinds = { sessions: "share", failure_rate: "value" };
  for (const [metric, kind] of Object.entries(kinds)) assert.equal(heatmapKind(catalog, { metric, split: "length_band" }), kind, metric);
  assert.equal(heatmapKind(catalog, { metric: "autonomy", split: "none" }), "distribution");
  assert.equal(trendTitle(catalog, { ...asked, metric: "failure_rate", split: "archetype" }), "Failure rate per day by archetype");
  assert.equal(trendTitle(catalog, { ...asked, metric: "autonomy", split: "none" }), "Tool calls per typed message: sessions per day by band");
  // Harness has no order, and a measure without a band of its own has no
  // distribution to draw without a breakdown.
  for (const change of [{ split: "harness" }, { metric: "failure_rate", split: "none" }]) {
    const moved = normalizeAnalysis(catalog, { ...asked, ...change });
    assert.equal(moved.display, "interval", JSON.stringify(change));
    assert.match(trendAdjustments(catalog, { ...asked, ...change }).join(" "), /heatmap/, JSON.stringify(change));
  }
});

test("a distribution heatmap counts the records with a value in the measure's own band, within its population", () => {
  const asked = normalizeAnalysis(catalog, { view: "trend", metric: "autonomy", split: "none", display: "heatmap" });
  const { catalog: drawn, state } = heatmapSource(catalog, asked);
  assert.equal(state.split, "autonomy_band");
  assert.deepEqual([drawn.measures.autonomy.op, drawn.measures.autonomy.additive], ["count", true]);
  assert.deepEqual(drawn.measures.autonomy.where, [...catalog.measures.autonomy.where, { field: "autonomy_band", op: "is_not_null", value: "" }]);
  // Any other heatmap queries the state as it is.
  const shares = normalizeAnalysis(catalog, { view: "trend", metric: "sessions", split: "length_band", display: "heatmap" });
  assert.equal(heatmapSource(catalog, shares).state, shares);
});

test("a value heatmap shades each group's own value, and an empty cell is no value", () => {
  const series = [{ key: "a", label: "Low", value: "low" }, { key: "b", label: "High", value: "high" }];
  const buckets = [{ ...day("2026-11-01"), values: { a: 0.1, b: 0.4 } }, { ...day("2026-11-02"), values: { b: 0.2 } }];
  const heatmap = trendHeatmap(buckets, series, ["low", "high"], "value");
  assert.deepEqual(heatmap.columns.map(column => column.cells), [[0.1, 0.4], [null, 0.2]]);
  assert.equal(heatmap.max, 0.4);
  const format = value => `${Math.round(100 * value)}%`;
  assert.match(heatmapCellText(heatmap, 1, 0, { period: "day", noun: "sessions", format }), /High: 40%/);
  assert.match(heatmapCellText(heatmap, 0, 1, { period: "day", noun: "sessions", format }), /No sessions in this period/);
  // As a card's grid: the highest row first, each cell's value without its row.
  const grid = heatmapGrid(heatmap, { period: "day", noun: "sessions", format });
  assert.deepEqual(grid.rows, ["High", "Low"]);
  assert.deepEqual(grid.cells, [[1, 0.5], [0.25, null]]);
  assert.deepEqual(grid.values, [["40%", "20%"], ["10%", null]]);
  assert.deepEqual(grid.legend, ["0%", "40%"]);
});

test("over session rows, every column with sessions sums to one, in band order", () => {
  const now = Date.parse("2026-11-04T12:00:00Z");
  const bands = ["<0.5", "0.5-1", "1-2", "2-5", "5-10", "10+"];
  const rows = Array.from({ length: 60 }, (_, index) => {
    const at = new Date(now - (index % 6 + 1) * 86400000), key = at.toISOString().slice(0, 10);
    return { id: `s${index}`, conversation_id: `s${index}`, started_at: at.toISOString(), day: key, week: key, month: key.slice(0, 7), autonomy_band: index % 5 ? bands[index % 6] : null };
  });
  const state = { view: "trend", metric: "sessions", split: "autonomy_band", display: "heatmap", period: "day" };
  const where = [{ field: "started_at", op: ">=", value: "-7d" }];
  const trend = trendQuery(catalog, state, where, { now });
  const built = trendSeries(catalog, state, trend, core.evaluateMetrics(rows, trend.query, schemas.session_shape, { now }).metrics, { now });
  assert.equal(built.error, undefined);
  const heatmap = trendHeatmap(built.buckets, built.series, catalog.breakdowns.autonomy_band.order);
  const known = heatmap.rows.map(row => built.series.find(item => item.key === row.key)?.value).filter(value => bands.includes(value));
  assert.deepEqual(known, [...known].sort((a, b) => bands.indexOf(a) - bands.indexOf(b)));
  assert.equal(heatmap.rows.at(-1).label, catalog.breakdowns.autonomy_band.empty);
  const filled = heatmap.columns.filter(column => column.total > 0);
  assert.ok(filled.length >= 5);
  for (const column of filled) assert.ok(Math.abs(column.cells.reduce((sum, cell) => sum + cell, 0) - 1) < 1e-9);
  assert.ok(heatmap.columns.some(column => column.total === 0 && column.cells.every(cell => cell === null)), "today has no sessions");
});
