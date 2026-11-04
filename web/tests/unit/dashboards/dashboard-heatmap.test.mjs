// The trend's heatmap display: a distribution over time, as each period's
// share of records in each group of an ordered breakdown.
import test from "node:test";
import assert from "node:assert/strict";
import { core, schemas } from "../helpers/recipes.mjs";
import { normalizeAnalysis, trendAdjustments, validAnalysisState } from "../../../src/dashboard-analysis-model";
import { trendQuery, trendSeries, trendTitle } from "../../../src/dashboard-trend-model";
import { heatmapCellText, heatmapShade, trendHeatmap } from "../../../src/dashboard-heatmap-model";
import { sessionShapeAnalysisCatalog as catalog } from "../../../src/session-shape-analysis-catalog";

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

test("a heatmap needs an additive measure and an ordered breakdown, and draws no share itself", () => {
  const asked = { view: "trend", metric: "sessions", split: "autonomy_band", display: "heatmap", share: true };
  assert.ok(validAnalysisState(catalog, asked));
  assert.ok(!validAnalysisState(catalog, { ...asked, display: "mosaic" }));
  const state = normalizeAnalysis(catalog, asked);
  assert.deepEqual([state.display, state.share], ["heatmap", false]);
  assert.equal(trendTitle(catalog, asked), "Share of sessions per day by calls per typed message band");
  assert.deepEqual(trendAdjustments(catalog, asked), []);
  // Harness has no order; a rate does not add up across groups.
  for (const change of [{ split: "harness" }, { metric: "failure_rate" }, { split: "none" }]) {
    const moved = normalizeAnalysis(catalog, { ...asked, ...change });
    assert.equal(moved.display, "interval", JSON.stringify(change));
    assert.match(trendAdjustments(catalog, { ...asked, ...change }).join(" "), /heatmap/, JSON.stringify(change));
  }
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
