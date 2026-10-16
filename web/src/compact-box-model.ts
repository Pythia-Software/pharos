import type { AggregationBucket, AggregationResultEntry, MetricBoxSummary } from "@pythia-software/query-table-core";

export function filterBoxSamples(result: AggregationResultEntry, minimum?: number): AggregationResultEntry {
  if (!minimum) return result;
  const buckets = result.buckets.filter(bucket => bucket.error || bucket.distribution?.kind === "box" && (bucket.distribution.summary?.n ?? 0) >= minimum);
  return { ...result, buckets, groupCount: result.groupCount === undefined || result.groupCount === result.buckets.length ? buckets.length : result.groupCount,
    processedRows: buckets.reduce((n, bucket) => n + (bucket.distribution?.kind === "box" ? bucket.distribution.summary?.n ?? 0 : 0), 0) };
}

// Compact covers use the visible population instead of reserving a fixed tall
// chart. The same geometry sizes loading placeholders and populated cards.
export function boxCoverHeight(groups: number, compact = false): number {
  return Math.max(170, 105 + groups * (compact ? 28 : 52));
}

export function sharedBoxScale(buckets: AggregationBucket[], budget: number, requested: "linear" | "log" = "linear", integer = false) {
  const summaries = buckets.slice(0, budget).flatMap(bucket => !bucket.error && bucket.distribution?.kind === "box" && bucket.distribution.summary ? [bucket.distribution.summary] : []);
  const minimum = Math.min(...summaries.map(summary => summary.min)), maximum = Math.max(0, ...summaries.map(summary => summary.max));
  // Keep zero-cost samples visible; never replace them with a positive epsilon.
  const log = requested === "log" && minimum > 0;
  let min = log ? 10 ** Math.floor(Math.log10(minimum)) : Math.min(0, Number.isFinite(minimum) ? minimum : 0);
  let max = log ? 10 ** Math.max(Math.ceil(Math.log10(maximum)), Math.log10(min) + 1) : maximum > min ? maximum : min + 1;
  const fraction = (value: number) => log ? (Math.log10(value) - Math.log10(min)) / (Math.log10(max) - Math.log10(min)) : (value - min) / (max - min);
  const ticks: number[] = [];
  if (log) {
    const start = Math.round(Math.log10(min)), end = Math.round(Math.log10(max)), step = Math.max(1, Math.ceil((end - start) / 4));
    for (let power = start; power <= end; power += step) ticks.push(10 ** power);
    if (ticks.at(-1) !== max) ticks.push(max);
  } else if (integer) {
    const rough = (max - min) / 4, power = 10 ** Math.floor(Math.log10(rough));
    const step = Math.max(1, [1, 2, 5, 10].find(value => value * power >= rough)! * power);
    min = Math.floor(min / step) * step;
    max = Math.ceil(max / step) * step;
    for (let value = min; value <= max; value += step) ticks.push(value);
  } else for (let i = 0; i <= 4; i++) ticks.push(min + (max - min) * i / 4);
  return { min, max, log, fraction, ticks, summaries };
}

export function boxCost(value: number): string {
  return value.toLocaleString(undefined, { style: "currency", currency: "USD", maximumFractionDigits: value > 0 && value < .01 ? Math.min(8, 1 - Math.floor(Math.log10(value))) : 2 });
}
export function boxNumber(value: number): string {
  return value.toLocaleString(undefined, { maximumFractionDigits: value !== 0 && Math.abs(value) < .01 ? Math.min(6, 1 - Math.floor(Math.log10(Math.abs(value)))) : 2 });
}
export function boxDetails(label: string, summary: MetricBoxSummary, unit = "USD"): string {
  const format = unit === "USD" ? boxCost : boxNumber;
  return [label, ...(unit && unit !== "USD" ? [unit] : []), `Median: ${format(summary.median)} · Mean: ${format(summary.mean)}`, `Q1: ${format(summary.q1)} · Q3: ${format(summary.q3)}`, `Min: ${format(summary.min)} · Max: ${format(summary.max)}`, `${summary.n.toLocaleString()} observations · ${summary.outlierCount.toLocaleString()} outliers (${boxOutliers(summary).length.toLocaleString()} retained markers)`].join("\n");
}

export function boxAxisCost(value: number): string {
  return value.toLocaleString(undefined, { style: "currency", currency: "USD", notation: value >= 1000 ? "compact" : "standard", maximumFractionDigits: value > 0 && value < .01 ? Math.min(8, -Math.floor(Math.log10(value))) : value < 1 && value > 0 ? 2 : 0 });
}
export function boxOutliers(summary: MetricBoxSummary): number[] {
  return [...new Set([...summary.outliers, ...(summary.max > summary.high ? [summary.max] : []), ...(summary.min < summary.low ? [summary.min] : [])])];
}
