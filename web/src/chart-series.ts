import { chartSeriesStyle, otherSeriesColor } from "./chart-colors";
import { parseDay, periodBuckets, periodIndex, periodKey, periodStart, type ChartPeriod } from "./chart-period";
import type { MetricResult } from "./metric-request";

export type ChartSeries = { key: string; label: string; color: string; value?: string; hiddenCount?: number; foldedValues?: string[]; group?: string };
export type ChartBucket = { key: string; date: Date; values: Record<string, number>; parts?: Record<string, Array<[string, number]>>; detail?: string; windowSize?: number; history?: boolean };
export type ChartDisplay = "interval" | "cumulative";
export const validChartDisplay = (value: unknown): value is ChartDisplay | undefined => value === undefined || value === "interval" || value === "cumulative";

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

export type CumulativeMetric = { op: "sum" | "max" } | { op: "ratio"; numerator: string; denominator: string };

// Native line charts keep their group identities while accumulating underlying
// amounts. Rates and means divide running totals, never sum interval ratios.
export function cumulativeMetricTrend(metrics: MetricResult[], period: ChartPeriod, start: Date, end: Date, calculation: CumulativeMetric = { op: "sum" }): MetricResult | undefined {
  const result = metrics[0];
  if (!result || result.error || !result.buckets.length) return result;
  const numerator = calculation.op === "ratio" ? metrics.find(metric => metric.id === calculation.numerator) : result;
  const denominator = calculation.op === "ratio" ? metrics.find(metric => metric.id === calculation.denominator) : undefined;
  if (!numerator || calculation.op === "ratio" && !denominator) return { ...result, error: "Cumulative values require the underlying totals and counts." };
  if (numerator.error || denominator?.error) return { ...result, error: numerator.error || denominator?.error };
  const grid = periodBuckets(period, start, end), first = periodKey(start, period), last = periodKey(end, period);
  const groups = new Map<string, unknown[]>(), original = new Map<string, MetricResult["buckets"][number]>();
  for (const bucket of result.buckets) groups.set(JSON.stringify(bucket.keys.slice(1)), bucket.keys.slice(1));
  const values = (source: MetricResult, maximum = false, counts = false) => {
    const byTime = new Map<string, Map<string, number>>();
    for (const bucket of source.buckets) {
      const date = parseDay(String(bucket.keys[0]));
      if (!Number.isFinite(date.getTime())) continue;
      const actual = periodKey(date, period);
      if (actual < first || actual > last) continue;
      const key = plottedBucketKey(date, period, 1, grid, "cumulative"), group = JSON.stringify(bucket.keys.slice(1));
      if (!groups.has(group)) continue;
      if (source === result && !counts) original.set(JSON.stringify([key, group]), bucket);
      const raw = counts ? bucket.count : bucket.value;
      if (raw === null || raw === undefined || !Number.isFinite(Number(raw))) continue;
      const entries = byTime.get(key) ?? new Map<string, number>(), value = Number(raw);
      entries.set(group, maximum ? Math.max(entries.get(group) ?? -Infinity, value) : (entries.get(group) ?? 0) + value);
      byTime.set(key, entries);
    }
    return byTime;
  };
  const amounts = values(numerator, calculation.op === "max"), divisors = denominator && values(denominator), counts = values(result, false, true);
  const totals = new Map<string, number>(), denominatorTotals = new Map<string, number>(), countTotals = new Map<string, number>();
  const buckets = grid.flatMap(bucket => [...groups].map(([group, keys]) => {
    const amount = amounts.get(bucket.key)?.get(group);
    if (calculation.op === "max") {
      if (amount !== undefined) totals.set(group, Math.max(totals.get(group) ?? -Infinity, amount));
    } else totals.set(group, (totals.get(group) ?? 0) + (amount ?? 0));
    denominatorTotals.set(group, (denominatorTotals.get(group) ?? 0) + (divisors?.get(bucket.key)?.get(group) ?? 0));
    countTotals.set(group, (countTotals.get(group) ?? 0) + (counts.get(bucket.key)?.get(group) ?? 0));
    const value = calculation.op === "ratio" ? denominatorTotals.get(group)! > 0 ? totals.get(group)! / denominatorTotals.get(group)! : null
      : calculation.op === "max" ? totals.get(group) ?? null : totals.get(group) ?? 0;
    return { ...original.get(JSON.stringify([bucket.key, group])), keys: [bucket.key, ...keys], value, count: countTotals.get(group)! };
  }));
  return { ...result, buckets, ...(result.groupCount === result.buckets.length ? { groupCount: buckets.length } : {}) };
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
    const size = Math.min(periods, at - indexOf(0) + 1);
    return {
      ...bucket,
      values: Object.fromEntries([...totals].map(([key, entry]) => [key, entry.sum / size])),
      parts: parts.size ? Object.fromEntries([...parts].map(([key, entries]) => [key, [...entries].map(([name, entry]) => [name, entry.sum / size] as [string, number])])) : undefined,
      detail: bucket.detail ? `This period: ${bucket.detail}` : undefined,
      windowSize: size,
    };
  });
}

// Host presentation for native metric charts. Pad missing time intervals for
// each series, keeping exact group keys so a point still opens its raw records.
export function rollingMetricTrend(result: MetricResult | undefined, period: ChartPeriod, periods: number, start: Date, end: Date, historyStart?: Date): MetricResult | undefined {
  if (!result || result.error || !result.buckets.length || !validRollingPeriods(periods) || periods < 2) return result;
  const buckets = chartBucketsWithHistory(period, start, end, 1, result.buckets.map(bucket => parseDay(String(bucket.keys[0]))), historyStart), byKey = new Map(buckets.map(bucket => [bucket.key, bucket]));
  const groups = new Map<string, unknown[]>(), originals = new Map<string, MetricResult["buckets"][number]>();
  for (const bucket of result.buckets) {
    const key = periodKey(parseDay(String(bucket.keys[0])), period), group = JSON.stringify(bucket.keys.slice(1));
    groups.set(group, bucket.keys.slice(1));
    originals.set(JSON.stringify([key, group]), bucket);
    const target = byKey.get(key);
    if (target) target.values[group] = Number(bucket.value) || 0;
  }
  const smoothed = rollingAverageBuckets(buckets, periods, { period }).filter(bucket => !bucket.history).flatMap(bucket => [...groups].map(([group, keys]) => ({
    ...originals.get(JSON.stringify([bucket.key, group])), keys: [bucket.key, ...keys], value: bucket.values[group] ?? 0,
    count: originals.get(JSON.stringify([bucket.key, group]))?.count ?? 0,
  })));
  return { ...result, buckets: smoothed, ...(result.groupCount === result.buckets.length ? { groupCount: smoothed.length } : {}) };
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
