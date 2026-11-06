import type { ChartPeriod } from "./chart-period";
import type { ChartBucket, ChartSeries } from "./chart-series";
import { gridTicks, type GridCover } from "./grid-cover";
import { binsSpanning, nextEdge, type BinScheme } from "./value-bins";
import { heatmapPercentiles, percentileFraction, percentileLabel, type PercentileLevel } from "./dashboard-analysis-model";

// The trend's heatmap: a value on a continuous axis over time. Each column
// is a period, each row one fine bin of the value (value-bins.ts), and each
// cell is shaded by the records, or the additive measure, in that bin that
// period. Rows sit at their true place on the value's axis, a log axis for a
// log or count scheme, so the picture is the distribution, not a few
// predefined groups. It reads the trend's prepared buckets, so filters,
// smoothing and Refresh apply as they do to the columns
// (docs/dashboard-analysis.md).

// A row is one bin, [low, high). y0 and y1 place it on the axis, 0 at the
// bottom and 1 at the top. A log axis has no place for zero, so the bin of
// zero (and less) sits apart below the rest.
export type HeatmapRow = { low: number; high: number; zero: boolean; y0: number; y1: number };
// A column's cells are the amounts in each row, 0 where none. An empty
// period has a total of 0: no evidence, which the chart hatches.
// Each traced level (Heatmap.levels) is its place on the axis and its
// value, read from the bins; null in an empty period.
export type HeatmapLevel = { y: number; value: number };
export type HeatmapColumn = { key: string; date: Date; total: number; windowSize?: number; cells: number[]; levels: Array<HeatmapLevel | null> };
export type HeatmapTick = { value: number; y: number };
export type Heatmap = { scheme: BinScheme; levels: PercentileLevel[]; rows: HeatmapRow[]; columns: HeatmapColumn[]; max: number; ticks: HeatmapTick[]; place: (value: number) => number | null };

const logScale = (scheme: BinScheme) => scheme.scale !== "linear";

// Bins from the series' values (each a bin's lower edge), every bin between
// the lowest and highest included, laid out on the value's axis.
export function trendHeatmap(buckets: ChartBucket[], series: ChartSeries[], scheme: BinScheme, asked: PercentileLevel[] = heatmapPercentiles): Heatmap {
  const levels = asked.filter(level => percentileFraction(level) !== undefined);
  const keyed = new Map<number, string>();
  for (const item of series) {
    const value = item.value === undefined || item.value === "" ? NaN : Number(item.value);
    if (Number.isFinite(value)) keyed.set(value, item.key);
  }
  const bins = binsSpanning([...keyed.keys()], scheme);
  const log = logScale(scheme), apart = (low: number) => log && low <= 0;
  const position = (value: number) => log ? Math.log10(value) : value;
  const placed = bins.filter(low => !apart(low));
  const bottom = placed.length ? position(placed[0]) : 0, top = placed.length ? position(nextEdge(placed.at(-1)!, scheme)) : 1;
  const span = Math.max(top - bottom, Number.EPSILON);
  // The zero bin is as tall as an average bin, with a gap of half that.
  const zero = bins.some(apart), height = placed.length ? span / placed.length : 1, below = zero ? height * 1.5 : 0;
  const total = span + below, at = (value: number) => (below + position(value) - bottom) / total;
  const rows: HeatmapRow[] = bins.map(low => apart(low)
    ? { low, high: scheme.scale === "count" ? 1 : 0, zero: true, y0: 0, y1: height / total }
    : { low, high: nextEdge(low, scheme), zero: false, y0: at(low), y1: at(nextEdge(low, scheme)) });
  let max = 0;
  const columns = buckets.map(bucket => {
    const cells = bins.map(low => Math.max(0, bucket.values[keyed.get(low) ?? ""] ?? 0));
    for (const cell of cells) max = Math.max(max, cell);
    const sum = cells.reduce((all, cell) => all + cell, 0);
    return { key: bucket.key, date: bucket.date, total: sum, ...bucket.windowSize ? { windowSize: bucket.windowSize } : {}, cells, levels: levels.map(level => quantile(rows, cells, sum, percentileFraction(level)!, log)) };
  });
  const place = (value: number) => {
    if (!rows.length) return null;
    if (log && value <= 0) return zero ? height / total / 2 : null;
    const y = at(value);
    return y < (zero ? below / total : 0) - 1e-9 || y > 1 + 1e-9 ? null : y;
  };
  return { scheme, levels, rows, columns, max, ticks: axisTicks(bins, scheme, place), place };
}

// A quantile's place on the axis and its value, interpolated within its
// bin along the axis: the bins hold amounts, not the values, so it is as
// precise as a bin is narrow. In the zero bin it is 0.
function quantile(rows: HeatmapRow[], cells: number[], total: number, q: number, log: boolean): HeatmapLevel | null {
  if (total <= 0) return null;
  let below = 0;
  for (let index = 0; index < rows.length; index++) {
    const cell = cells[index];
    if (cell > 0 && below + cell >= q * total) {
      const row = rows[index], within = (q * total - below) / cell, y = row.y0 + within * (row.y1 - row.y0);
      if (row.zero) return { y, value: 0 };
      return { y, value: log ? 10 ** (Math.log10(row.low) + within * (Math.log10(row.high) - Math.log10(row.low))) : row.low + within * (row.high - row.low) };
    }
    below += cell;
  }
  const last = rows.at(-1)!;
  return { y: last.y1, value: last.high };
}

// Labels on the value axis: 1, 2 and 5 times powers of ten on a log axis
// (only powers of ten when those crowd), round steps on a linear one, and 0
// beside the zero bin.
function axisTicks(bins: number[], scheme: BinScheme, place: (value: number) => number | null): HeatmapTick[] {
  if (!bins.length) return [];
  const low = bins[0], high = nextEdge(bins.at(-1)!, scheme), values: number[] = [];
  if (logScale(scheme)) {
    if (low <= 0) values.push(0);
    const first = bins.find(bin => bin > 0);
    if (first !== undefined) {
      const from = Math.floor(Math.log10(first)), to = Math.ceil(Math.log10(high));
      const steps = to - from <= 3 ? [1, 2, 5] : [1];
      for (let decade = from; decade <= to; decade++) for (const step of steps) {
        const value = Number(`${step}e${decade}`);
        if (value >= first && value <= high) values.push(value);
      }
    }
  } else {
    const range = high - low, rough = range / 5, power = 10 ** Math.floor(Math.log10(rough || 1));
    const step = [1, 2, 2.5, 5, 10].map(factor => factor * power).find(candidate => candidate >= rough) ?? power * 10;
    for (let value = Math.ceil(low / step) * step; value <= high + step / 1e6; value += step) values.push(Number(value.toPrecision(12)));
  }
  return values.flatMap(value => { const y = place(value); return y === null ? [] : [{ value, y }]; });
}

// A cell's shade, 0 to 1, on a log scale of the largest cell: a burst of
// one kind of session would otherwise wash out every other bin. Empty
// cells have none.
export const heatmapShade = (amount: number, max: number) => amount <= 0 || max <= 0 ? null : Math.log1p(amount) / Math.log1p(max);

// A bin in words: "3", "10–11" or "1.2–1.5" (from the low edge, up to the
// high one), as the value's format writes them.
export function heatmapBinLabel(row: Pick<HeatmapRow, "low" | "high" | "zero">, scheme: BinScheme, format: (value: number) => string = String): string {
  if (row.zero) return scheme.scale === "count" ? format(0) : `${format(0)} or less`;
  if (scheme.scale === "count") return row.high - row.low === 1 ? format(row.low) : `${format(row.low)}–${format(row.high - 1)}`;
  return `${format(row.low)}–${format(row.high)}`;
}

// A column's name, as the trend names its periods.
export function heatmapPeriodLabel(date: Date, period: ChartPeriod): string {
  if (period === "hour" || period === "hours") return date.toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric" });
  if (period === "month") return date.toLocaleDateString(undefined, { month: "short", year: "numeric" });
  return `${period === "week" ? "Week of " : ""}${date.toLocaleDateString(undefined, { month: "short", day: "numeric" })}`;
}

type CellOptions = { period: ChartPeriod; noun: string; label: string; format?: (value: number) => string; amount?: (value: number) => string; rollingPeriods?: number };
const counted = (value: number, rollingPeriods: number) => value.toLocaleString(undefined, { maximumFractionDigits: rollingPeriods > 1 ? 1 : 0 });

// A cell's tooltip: the period, the bin, the amount in it and its share of
// the period. A smoothed column's amounts are averages over its window.
export function heatmapCellText(heatmap: Heatmap, row: number, column: number, { period, noun, label, format = String, amount, rollingPeriods = 0 }: CellOptions): string {
  const item = heatmap.columns[column], value = item.cells[row], when = heatmapPeriodLabel(item.date, period);
  const bin = `${label} ${heatmapBinLabel(heatmap.rows[row], heatmap.scheme, format)}`;
  if (item.total <= 0) return `${when}\n${bin}\nNo ${noun} in this period`;
  const smoothed = rollingPeriods > 1 ? `, a ${rollingPeriods}-period average` : "";
  const share = (100 * value / item.total).toLocaleString(undefined, { maximumFractionDigits: 1 });
  return [`${when}\n${bin}\n${amount ? amount(value) : counted(value, rollingPeriods)} ${noun} (${share}% of ${amount ? amount(item.total) : counted(item.total, rollingPeriods)})${smoothed}`, ...heatmapLevelLines(heatmap, column, format)].join("\n");
}

// What the traced lines are at a column: "Median: 35", "90th %ile: 280".
export function heatmapLevelLines(heatmap: Heatmap, column: number, format: (value: number) => string = String): string[] {
  return heatmap.levels.flatMap((level, index) => { const at = heatmap.columns[column].levels[index]; return at ? [`${percentileLabel(level)} line: ${format(at.value)}`] : []; });
}

// A column's own tooltip, for a place in it with no cell: the period, its
// total and the traced levels.
export function heatmapColumnText(heatmap: Heatmap, column: number, { period, noun, format = String, amount, rollingPeriods = 0 }: CellOptions): string {
  const item = heatmap.columns[column], when = heatmapPeriodLabel(item.date, period);
  if (item.total <= 0) return `${when}\nNo ${noun} in this period`;
  return [`${when}\n${amount ? amount(item.total) : counted(item.total, rollingPeriods)} ${noun}${rollingPeriods > 1 ? `, a ${rollingPeriods}-period average` : ""}`, ...heatmapLevelLines(heatmap, column, format)].join("\n");
}

// The heatmap as a card's grid cover (grid-cover.ts): the highest bin at the
// top, each cell's text as its tooltip.
export function heatmapGrid(heatmap: Heatmap, { caption, ...options }: CellOptions & { caption?: string }): GridCover {
  const order = heatmap.rows.map((_, index) => index).reverse(), rollingPeriods = options.rollingPeriods ?? 0;
  return {
    rows: order.map(index => heatmapBinLabel(heatmap.rows[index], heatmap.scheme, options.format)), columns: heatmap.columns.map(column => heatmapPeriodLabel(column.date, options.period)),
    cells: order.map(index => heatmap.columns.map(column => heatmapShade(column.cells[index], heatmap.max))),
    values: order.map(index => heatmap.columns.map((column, at) => column.cells[index] > 0 ? heatmapCellText(heatmap, index, at, options).split("\n")[2] : null)),
    tone: "sequential", legend: ["0", options.amount ? options.amount(heatmap.max) : counted(heatmap.max, rollingPeriods)], rowTicks: gridTicks(heatmap.rows.length, 5), columnTicks: gridTicks(heatmap.columns.length, 3),
    ...caption ? { caption } : {},
  };
}
