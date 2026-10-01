import { chartSeriesStyle, otherSeriesColor } from "./chart-colors";

export type ChartSeries = { key: string; label: string; color: string; value?: string; hiddenCount?: number; foldedValues?: string[]; group?: string };
export type ChartBucket = { key: string; date: Date; values: Record<string, number>; parts?: Record<string, Array<[string, number]>>; detail?: string; windowSize?: number };

export function validRollingPeriods(value: unknown): value is number {
  return typeof value === "number" && Number.isInteger(value) && value >= 0 && value <= 10;
}

export function rollingAverageBuckets(buckets: ChartBucket[], periods: number): ChartBucket[] {
  if (!validRollingPeriods(periods) || periods < 2) return buckets;
  return buckets.map((bucket, index) => {
    const window = buckets.slice(Math.max(0, index - periods + 1), index + 1);
    const values: Record<string, number> = {};
    const parts = new Map<string, Map<string, number>>();
    for (const entry of window) {
      for (const [key, value] of Object.entries(entry.values)) values[key] = (values[key] ?? 0) + value;
      for (const [key, entries] of Object.entries(entry.parts ?? {})) {
        const totals = parts.get(key) ?? new Map<string, number>();
        for (const [name, value] of entries) totals.set(name, (totals.get(name) ?? 0) + value);
        parts.set(key, totals);
      }
    }
    return {
      ...bucket,
      values: Object.fromEntries(Object.entries(values).map(([key, value]) => [key, value / window.length])),
      parts: parts.size ? Object.fromEntries([...parts].map(([key, totals]) => [key, [...totals].map(([name, value]) => [name, value / window.length] as [string, number])])) : undefined,
      detail: bucket.detail ? `This period: ${bucket.detail}` : undefined,
      windowSize: window.length,
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
  if (scope === "model_family" || scope === "provider") top.sort((left, right) => styles.get(left)!.order!.localeCompare(styles.get(right)!.order!, undefined, { numeric: true }));
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
