import type { ChartPeriod } from "./chart-period";
import type { ChartBucket, ChartSeries } from "./chart-series";

// The trend's heatmap display: one column per period, one row per group of
// an ordered breakdown (a band), each cell the period's share of records in
// that group. It reads the trend's prepared buckets, so filters, smoothing
// and Refresh apply as they do to the columns (docs/dashboard-analysis.md).

export type HeatmapRow = { key: string; label: string };
// A cell is the group's share of its period, or null when the period has no
// records: an empty period is no evidence, not a zero share.
export type HeatmapColumn = { key: string; date: Date; total: number; windowSize?: number; cells: Array<number | null> };
export type Heatmap = { rows: HeatmapRow[]; columns: HeatmapColumn[]; max: number };

// Rows follow the breakdown's order. Groups outside it (Other, an unknown
// value) come after, and a group without a value last.
export function trendHeatmap(buckets: ChartBucket[], series: ChartSeries[], order: readonly string[]): Heatmap {
  const rank = (item: ChartSeries) => {
    if (item.value === undefined) return order.length + 1;
    if (item.value === "" || item.value === "null") return order.length + 2;
    const index = order.indexOf(item.value);
    return index < 0 ? order.length : index;
  };
  const rows = series.map((item, index) => ({ item, index })).sort((a, b) => rank(a.item) - rank(b.item) || a.index - b.index).map(({ item }) => item);
  let max = 0;
  const columns = buckets.map(bucket => {
    const values = rows.map(item => Math.max(0, bucket.values[item.key] ?? 0));
    const total = values.reduce((sum, value) => sum + value, 0);
    const cells = values.map(value => total > 0 ? value / total : null);
    for (const cell of cells) if (cell !== null) max = Math.max(max, cell);
    return { key: bucket.key, date: bucket.date, total, ...bucket.windowSize ? { windowSize: bucket.windowSize } : {}, cells };
  });
  return { rows: rows.map(item => ({ key: item.key, label: item.label })), columns, max };
}

// A cell's shade, 0 to 1, relative to the largest share drawn, so a grid of
// small shares still reads. Empty periods have none.
export const heatmapShade = (share: number | null, max: number) => share === null || max <= 0 ? null : share / max;

// A column's name, as the trend names its periods.
export function heatmapPeriodLabel(date: Date, period: ChartPeriod): string {
  if (period === "hour" || period === "hours") return date.toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric" });
  if (period === "month") return date.toLocaleDateString(undefined, { month: "short", year: "numeric" });
  return `${period === "week" ? "Week of " : ""}${date.toLocaleDateString(undefined, { month: "short", day: "numeric" })}`;
}

// A cell's tooltip: the period, the group, its share and the records behind
// it. A smoothed column's counts are averages over its window.
export function heatmapCellText(heatmap: Heatmap, row: number, column: number, { period, noun, rollingPeriods = 0 }: { period: ChartPeriod; noun: string; rollingPeriods?: number }): string {
  const item = heatmap.columns[column], share = item.cells[row], name = heatmap.rows[row].label, when = heatmapPeriodLabel(item.date, period);
  const count = (value: number) => value.toLocaleString(undefined, { maximumFractionDigits: rollingPeriods > 1 ? 1 : 0 });
  const smoothed = rollingPeriods > 1 ? `, a ${rollingPeriods}-period average` : "";
  if (share === null) return `${when}\n${name}\nNo ${noun} in this period`;
  const amount = share * item.total;
  return `${when}\n${name}: ${(100 * share).toLocaleString(undefined, { maximumFractionDigits: 1 })}%\n${count(amount)} of ${count(item.total)} ${noun}${smoothed}`;
}
