import type { ChartPeriod } from "./chart-period";
import type { ChartBucket, ChartSeries } from "./chart-series";
import { gridTicks, type GridCover } from "./grid-cover";

// The trend's heatmap display: one column per period, one row per group of
// an ordered breakdown (a band). A share heatmap's cell is the period's share
// of the measure in that group; a value heatmap's is the group's own value,
// such as a pooled rate (heatmapKind). It reads the trend's prepared
// buckets, so filters, smoothing and Refresh apply as they do to the columns
// (docs/dashboard-analysis.md).

export type HeatmapRow = { key: string; label: string };
// A cell is the group's share of its period, or its value; null when the
// period has no records (or the group no value): an empty period is no
// evidence, not a zero.
export type HeatmapColumn = { key: string; date: Date; total: number; windowSize?: number; cells: Array<number | null> };
export type Heatmap = { kind: "share" | "value"; rows: HeatmapRow[]; columns: HeatmapColumn[]; max: number };

// Rows follow the breakdown's order. Groups outside it (Other, an unknown
// value) come after, and a group without a value last.
export function trendHeatmap(buckets: ChartBucket[], series: ChartSeries[], order: readonly string[], kind: Heatmap["kind"] = "share"): Heatmap {
  const rank = (item: ChartSeries) => {
    if (item.value === undefined) return order.length + 1;
    if (item.value === "" || item.value === "null") return order.length + 2;
    const index = order.indexOf(item.value);
    return index < 0 ? order.length : index;
  };
  const rows = series.map((item, index) => ({ item, index })).sort((a, b) => rank(a.item) - rank(b.item) || a.index - b.index).map(({ item }) => item);
  let max = 0;
  const columns = buckets.map(bucket => {
    if (kind === "value") {
      const cells = rows.map(item => { const value = bucket.values[item.key]; return value === undefined || !Number.isFinite(value) ? null : Math.max(0, value); });
      for (const cell of cells) if (cell !== null) max = Math.max(max, cell);
      return { key: bucket.key, date: bucket.date, total: 0, ...bucket.windowSize ? { windowSize: bucket.windowSize } : {}, cells };
    }
    const values = rows.map(item => Math.max(0, bucket.values[item.key] ?? 0));
    const total = values.reduce((sum, value) => sum + value, 0);
    const cells = values.map(value => total > 0 ? value / total : null);
    for (const cell of cells) if (cell !== null) max = Math.max(max, cell);
    return { key: bucket.key, date: bucket.date, total, ...bucket.windowSize ? { windowSize: bucket.windowSize } : {}, cells };
  });
  return { kind, rows: rows.map(item => ({ key: item.key, label: item.label })), columns, max };
}

// A cell's shade, 0 to 1, relative to the largest share or value drawn, so a
// grid of small shares still reads. Empty cells have none.
export const heatmapShade = (share: number | null, max: number) => share === null || max <= 0 ? null : share / max;

// A column's name, as the trend names its periods.
export function heatmapPeriodLabel(date: Date, period: ChartPeriod): string {
  if (period === "hour" || period === "hours") return date.toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric" });
  if (period === "month") return date.toLocaleDateString(undefined, { month: "short", year: "numeric" });
  return `${period === "week" ? "Week of " : ""}${date.toLocaleDateString(undefined, { month: "short", day: "numeric" })}`;
}

// A cell's tooltip: the period, the group, its share and the records behind
// it. A smoothed column's counts are averages over its window.
// A value cell says its value as the dashboard formats it.
export function heatmapCellText(heatmap: Heatmap, row: number, column: number, { period, noun, rollingPeriods = 0, format = String }: { period: ChartPeriod; noun: string; rollingPeriods?: number; format?: (value: number) => string }): string {
  const item = heatmap.columns[column], share = item.cells[row], name = heatmap.rows[row].label, when = heatmapPeriodLabel(item.date, period);
  if (heatmap.kind === "value") return share === null ? `${when}\n${name}\nNo ${noun} in this period` : `${when}\n${name}: ${format(share)}${rollingPeriods > 1 ? `\nPooled over ${rollingPeriods} periods` : ""}`;
  const count = (value: number) => value.toLocaleString(undefined, { maximumFractionDigits: rollingPeriods > 1 ? 1 : 0 });
  const smoothed = rollingPeriods > 1 ? `, a ${rollingPeriods}-period average` : "";
  if (share === null) return `${when}\n${name}\nNo ${noun} in this period`;
  const amount = share * item.total;
  return `${when}\n${name}: ${(100 * share).toLocaleString(undefined, { maximumFractionDigits: 1 })}%\n${count(amount)} of ${count(item.total)} ${noun}${smoothed}`;
}

// The heatmap as a card's grid cover (grid-cover.ts): the same rows, highest
// band at the top, and columns, with the cells' text as their tooltips.
export function heatmapGrid(heatmap: Heatmap, { period, noun, rollingPeriods = 0, format = String, caption }: { period: ChartPeriod; noun: string; rollingPeriods?: number; format?: (value: number) => string; caption?: string }): GridCover {
  const order = heatmap.rows.map((_, index) => index).reverse();
  const top = heatmap.kind === "share" ? `${(100 * heatmap.max).toLocaleString(undefined, { maximumFractionDigits: 0 })}%` : format(heatmap.max);
  return {
    rows: order.map(index => heatmap.rows[index].label), columns: heatmap.columns.map(column => heatmapPeriodLabel(column.date, period)),
    cells: order.map(index => heatmap.columns.map(column => heatmapShade(column.cells[index], heatmap.max))),
    // The tooltip names the row and column itself, so a value is the rest.
    values: order.map(index => heatmap.columns.map((column, at) => {
      if (column.cells[index] === null) return null;
      const [, first, ...rest] = heatmapCellText(heatmap, index, at, { period, noun, rollingPeriods, format }).split("\n");
      return [first.slice(heatmap.rows[index].label.length + 2), ...rest].join(" · ");
    })),
    tone: "sequential", legend: [heatmap.kind === "share" ? "0%" : format(0), top], rowTicks: gridTicks(heatmap.rows.length, 6), columnTicks: gridTicks(heatmap.columns.length, 3),
    ...caption ? { caption } : {},
  };
}
