import { EMPTY_QUERY, normalizeQueryState, type AggregationResultEntry, type QueryState } from "@pythia-software/query-table-core";
import type { PreviewJob } from "./query-previews";
import type { HumanStudyPreview } from "./human-study";
import { rollingQueryMatches } from "./query-links";

const day = 86400000;
export type CostTimeBucket = { at: number; cost: number; count: number };
// Edited populations, HAVING clauses, alternative metrics, and source covers
// keep native execution. Only the unchanged period-total cover gets history.
export function usesApiCostTrend(job: PreviewJob): boolean {
  const { query, options } = job, recipe = options?.recipe, view = recipe?.exploration;
  const cover = options?.cover ?? recipe?.cover;
  return job.dataset === "usage" && recipe?.id === "api-cost-total" && Boolean(view && query.grouping
    && options?.window?.mode === "relative" && options.window.field === "first_usage_at" && rollingQueryMatches(query, options.window)
    && !query.where.length && (!query.aggregations?.length || JSON.stringify(normalizeQueryState({ ...EMPTY_QUERY, aggregations: query.aggregations }).aggregations) === JSON.stringify(normalizeQueryState({ ...EMPTY_QUERY, aggregations: [view.coverMetric!] }).aggregations))
    && JSON.stringify(query.grouping.keys) === JSON.stringify(view.keys)
    && JSON.stringify(query.grouping.summaries) === JSON.stringify(view.summaries)
    && !query.grouping.representative && !query.grouping.source.grouping && !query.grouping.source.aggregations?.length
    && JSON.stringify(query.grouping.source.where.filter(term => !("field" in term && term.field === "first_usage_at"))) === JSON.stringify(recipe.where)
    && cover?.mode === "result" && cover.metricId === "ac_cost" && (cover.kind ?? "auto") === "auto" && cover.showHeadline !== false
    && (!options.coverMetricId || options.coverMetricId === "ac_cost"));
}

// Reduce equal timestamps at the same daily-fact grain as the destination.
// Hourly usage would move portions of those facts to different time windows.
export function apiCostTrendQuery(source: QueryState, from: number, to: number): QueryState {
  return { ...EMPTY_QUERY, where: [
    ...source.where.filter(term => !("field" in term && term.field === "first_usage_at")),
    { field: "first_usage_at", op: ">=", value: new Date(from).toISOString() },
    { field: "first_usage_at", op: "<", value: new Date(to).toISOString() },
  ], aggregations: [{ id: "cost-history", op: "sum", field: "cost_usd", groupBy: ["first_usage_at"], groupLimit: 10000 }] };
}

// A truncated group set must be split and fetched again, never summed as if
// complete. Coverage errors are different and cannot be repaired by splitting.
export function apiCostTrendFacts(result?: AggregationResultEntry): CostTimeBucket[] | undefined {
  if (!result || result.error || result.coverage !== "exact" || result.buckets.some(bucket => bucket.error)) throw new Error("Complete API cost history is unavailable.");
  if (result.groupCount !== undefined && result.groupCount > result.buckets.length) return;
  const facts = result.buckets.map(bucket => {
    const key = bucket.keys[0], at = typeof key === "number" ? key : Date.parse(String(key));
    if (bucket.keys.length !== 1 || !Number.isFinite(at) || typeof bucket.value !== "number" || !Number.isFinite(bucket.value) || bucket.value < 0 || !Number.isInteger(bucket.count) || bucket.count <= 0) throw new Error("Invalid API cost history value.");
    return { at, cost: bucket.value, count: bucket.count };
  });
  if (result.processedRows !== undefined && facts.reduce((n, fact) => n + fact.count, 0) !== result.processedRows) throw new Error("Incomplete API cost history counts.");
  return facts;
}

export function buildApiCostTrend(facts: CostTimeBucket[], days: number, now: number): HumanStudyPreview {
  const window = (end: number, length: number) => facts.filter(fact => fact.at >= end - length * day && fact.at < end);
  const sum = (rows: CostTimeBucket[]) => rows.reduce((n, row) => n + row.cost, 0);
  const current = window(now, days), count = current.reduce((n, fact) => n + fact.count, 0);
  const pointTimes = Array.from({ length: 31 }, (_, i) => now - (30 - i) * day), points = pointTimes.map(end => sum(window(end, 7)) / 7);
  return { kind: "trend", format: "USD", value: count ? sum(current) : undefined, valueLabel: "API equivalent", pointValueLabel: "API equivalent / day", trendLabel: "7d avg / day", sampleLabel: `${count.toLocaleString()} priced usage facts · last ${days} days`,
    points, pointTimes, lookbackDays: 7, baseline: points.slice(0, -1).reduce((total, value) => total + value, 0) / 30,
    detail: "Headline: selected-period API equivalent. Each sparkline point: summed fully priced cost in its trailing seven elapsed days divided by seven, including zero-activity days. History spans the last 30 days; the dashed norm is the mean of the prior 30 daily endpoints. All values use the same retained usage-fact time placement as the period total." };
}
