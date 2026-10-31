import { chartSeriesStyle, otherSeriesColor } from "./chart-colors";
import { periodBuckets, periodIndex, periodKey, periodStart, type ChartPeriod } from "./chart-period";
import { columnScale, type ChartScale } from "./chart-scale";

export type ChartSeries = { key: string; label: string; color: string; value?: string; hiddenCount?: number; foldedValues?: string[]; group?: string };
export type ChartBucket = { key: string; date: Date; values: Record<string, number>; parts?: Record<string, Array<[string, number]>>; detail?: string; windowSize?: number; history?: boolean };
export const chartDisplays = ["interval", "cumulative"] as const;
export type ChartDisplay = typeof chartDisplays[number];
export const validChartDisplay = (value: unknown): value is ChartDisplay | undefined => value === undefined || chartDisplays.includes(value as ChartDisplay);

// Logarithmic timelines compare each series independently: a stack's segment
// heights would depend on the amounts below it, rather than its own value.
export function timelineGeometry(buckets: ChartBucket[], series: ChartSeries[], { scale, lines = false, percent = false, reference = 0 }: { scale: ChartScale; lines?: boolean; percent?: boolean; reference?: number }) {
  lines ||= scale === "logarithmic" && !percent;
  const peaks = buckets.map(bucket => {
    const values = series.map(item => bucket.values[item.key] ?? 0);
    return lines ? Math.max(0, ...values) : values.reduce((sum, value) => sum + value, 0);
  });
  return { ...columnScale([...peaks, reference], percent ? "linear" : scale), lines };
}

// Missing additive amounts are idle periods; missing ratios break the line.
// A single period gets a short horizontal segment so it remains visible.
export function timelinePath(buckets: ChartBucket[], key: string, y: (value: number) => number, additive = true): string {
  let pen = "M";
  const steps = buckets.flatMap((bucket, index) => {
    const value = bucket.values[key] ?? (additive ? 0 : undefined);
    if (value === undefined) { pen = "M"; return []; }
    const step = `${pen} ${1000 * (index + .5) / buckets.length} ${100 - y(value)}`;
    pen = "L";
    return [step];
  });
  return steps.join(" ") + (buckets.length === 1 && steps.length ? ` L 1000 ${100 - y(buckets[0].values[key] ?? 0)}` : "");
}

// Accumulate original amounts within the visible range, including folded
// series. Normalize shares afterwards; adding interval percentages is wrong.
export function cumulativeBuckets(buckets: ChartBucket[]): ChartBucket[] {
  const totals: Record<string, number> = {}, parts = new Map<string, Map<string, number>>();
  return buckets.filter(bucket => !bucket.history).map(bucket => {
    for (const [key, value] of Object.entries(bucket.values)) totals[key] = (totals[key] ?? 0) + value;
    for (const [key, entries] of Object.entries(bucket.parts ?? {})) {
      const items = parts.get(key) ?? new Map<string, number>();
      for (const [name, value] of entries) items.set(name, (items.get(name) ?? 0) + value);
      parts.set(key, items);
    }
    return { ...bucket, values: { ...totals },
      parts: parts.size ? Object.fromEntries([...parts].map(([key, entries]) => [key, [...entries]])) : undefined,
      detail: undefined, windowSize: undefined };
  });
}

// Retain the full selected range when the plot is capped at 5,000 intervals.
// Earlier amounts become the opening balance of the first visible point.
export function plottedBucketKey(date: Date, period: ChartPeriod, count: number, buckets: ChartBucket[], display?: ChartDisplay): string {
  const key = periodKey(date, period, count);
  return display === "cumulative" && buckets.length && key < buckets[0].key ? buckets[0].key : key;
}

export function validRollingPeriods(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

// Keep the visible plot bounded, with sparse prior buckets used only for
// smoothing. A zero anchor counts idle history even when no rows were returned.
export function chartBucketsWithHistory(period: ChartPeriod, start: Date, end: Date, count: number, historyDates: Date[], historyStart?: Date): ChartBucket[] {
  const visible = periodBuckets(period, start, end, count);
  if (!visible.length || !historyStart) return visible;
  const from = periodStart(historyStart, period, count), first = visible[0].date;
  if (from >= first) return visible;
  const history = new Map<string, ChartBucket>();
  for (const date of [from, ...historyDates]) {
    if (!Number.isFinite(date.getTime())) continue;
    const at = periodStart(date, period, count);
    if (at < from || at >= first) continue;
    const key = periodKey(at, period, count);
    history.set(key, { key, date: at, values: {}, history: true });
  }
  return [...history.values()].sort((a, b) => a.date.getTime() - b.date.getTime()).concat(visible);
}

export function rollingAverageBuckets(buckets: ChartBucket[], periods: number, grid?: { period: ChartPeriod; count?: number }): ChartBucket[] {
  return rollingBuckets(buckets, periods, grid, "mean");
}

// A trailing window over the grid: its mean, or its total. Idle periods in
// the window count as zero; a window reaching before the first bucket is
// shorter, and windowSize says how long it is.
export function rollingBuckets(buckets: ChartBucket[], periods: number, grid?: { period: ChartPeriod; count?: number }, op: "mean" | "sum" = "mean"): ChartBucket[] {
  if (!validRollingPeriods(periods) || periods < 2) return buckets;
  const totals = new Map<string, { sum: number; count: number }>();
  const parts = new Map<string, Map<string, { sum: number; count: number }>>();
  function accumulate(entries: Map<string, { sum: number; count: number }>, key: string, value: number, sign: number) {
    const entry = entries.get(key) ?? { sum: 0, count: 0 };
    entry.sum += sign * value; entry.count += sign;
    if (entry.count) entries.set(key, entry); else entries.delete(key);
  }
  function add(bucket: ChartBucket, sign: number) {
    for (const [key, value] of Object.entries(bucket.values)) accumulate(totals, key, value, sign);
    for (const [key, entries] of Object.entries(bucket.parts ?? {})) {
      const items = parts.get(key) ?? new Map();
      for (const [name, value] of entries) accumulate(items, name, value, sign);
      if (items.size) parts.set(key, items); else parts.delete(key);
    }
  }
  const indexOf = (index: number) => grid ? periodIndex(buckets[index].date, grid.period, grid.count) : index;
  let first = 0;
  return buckets.map((bucket, index) => {
    // A sliding sum keeps very large user-entered windows linear in the data.
    const at = indexOf(index);
    while (first < index && at - indexOf(first) >= periods) add(buckets[first++], -1);
    add(bucket, 1);
    const size = Math.min(periods, at - indexOf(0) + 1), divisor = op === "sum" ? 1 : size;
    return {
      ...bucket,
      values: Object.fromEntries([...totals].map(([key, entry]) => [key, entry.sum / divisor])),
      parts: parts.size ? Object.fromEntries([...parts].map(([key, entries]) => [key, [...entries].map(([name, entry]) => [name, entry.sum / divisor] as [string, number])])) : undefined,
      detail: bucket.detail ? `This period: ${bucket.detail}` : undefined,
      windowSize: size,
    };
  });
}

// Normalize after smoothing the amounts, so high-volume days carry their
// actual weight. Include folded/hidden values in the denominator as well.
export function shareBuckets(buckets: ChartBucket[]): ChartBucket[] {
  return buckets.map(bucket => {
    const total = Object.values(bucket.values).reduce((sum, value) => sum + value, 0);
    return { ...bucket,
      values: Object.fromEntries(Object.entries(bucket.values).map(([key, value]) => [key, total ? value / total : 0])),
      parts: bucket.parts && Object.fromEntries(Object.entries(bucket.parts).map(([key, entries]) => [key, entries.map(([name, value]) => [name, total ? value / total : 0] as [string, number])])),
    };
  });
}

export function foldSeries(rows: Array<[string, string, number]>, byKey: Map<string, ChartBucket>, label: (name: string) => string, fixed: string[] | undefined, selected: string[], limit = 5, scope = "generic"): ChartSeries[] {
  const inView = rows.filter(([key]) => byKey.has(key)), totals = new Map<string, number>();
  for (const [, name, value] of inView) totals.set(name, (totals.get(name) ?? 0) + value);
  const shown = fixed ? [...fixed.filter(name => totals.get(name)), ...[...totals.keys()].filter(name => !fixed.includes(name) && totals.get(name))]
    : [...totals.entries()].filter(([, value]) => value > 0).sort((left, right) => right[1] - left[1] || left[0].localeCompare(right[0])).map(([name]) => name);
  const top = shown.slice(0, limit);
  for (const name of selected) if (!top.includes(name)) top.push(name);
  const rest = shown.filter(name => !top.includes(name));
  const styles = new Map([...new Set([...shown, ...selected])].sort().map(name => [name, chartSeriesStyle(scope, name)]));
  if (scope === "level_band" || scope === "model_family" || scope === "provider" || scope === "effort" || scope === "model_effort") top.sort((left, right) => styles.get(left)!.order!.localeCompare(styles.get(right)!.order!, undefined, { numeric: true }));
  const series: ChartSeries[] = top.map(name => ({ key: `s:${name}`, label: label(name), color: styles.get(name)!.color, group: styles.get(name)!.group, value: name }));
  if (rest.length) series.push({ key: "other", label: `Other (${rest.length})`, color: otherSeriesColor, hiddenCount: rest.length, foldedValues: rest });
  for (const [bucketKey, name, value] of inView) {
    const target = byKey.get(bucketKey)!, key = top.includes(name) ? `s:${name}` : "other";
    target.values[key] = (target.values[key] ?? 0) + value;
    if (key !== "other" || value <= 0) continue;
    const parts = ((target.parts ??= {}).other ??= []), part = parts.find(([item]) => item === label(name));
    if (part) part[1] += value;
    else parts.push([label(name), value]);
  }
  return series;
}
