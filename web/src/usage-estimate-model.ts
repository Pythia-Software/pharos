import type { QueryRequestV3 } from "@pythia-software/query-table-core";

export const estimateExplanation = "* Includes estimated usage for conversations without reported counts: generated output, and processed input where the visible conversation supports it. Hidden system prompts are not counted and the cache division is assumed, so cost is partial and may assume a saved conversation model selection.";
// An estimated row's cache reads and writes are an assumed pricing scenario,
// so cache rates read recorded usage only. Sums of these tokens still include it.
export const recordedCacheReads = "IF([is_estimate],0,[cache_read_input_tokens])";
export const recordedCacheLookups = "IF([is_estimate],0,[cache_read_input_tokens]+[uncached_input_tokens])";
export const recordedInput = "IF([is_estimate],0,[input_tokens])";
export const cacheHitRateExpression = `SUM(${recordedCacheReads})/NULLIF(SUM(${recordedCacheLookups}),0)`;
export const cacheReadShareExpression = `SUM(${recordedCacheReads})/NULLIF(SUM(${recordedInput}),0)`;
export const durationEstimateExplanation = "* Includes estimated tool duration. Parallel calls can overlap.";
export const estimateFlag = (value: unknown) => value === true || value === 1;
export function estimatedField(row: Record<string, unknown>, field: string): boolean {
  return /duration/.test(field) ? estimateFlag(row.duration_is_estimate)
    : /(^token_count$|_tokens$|cost.*usd|^usage_price_delta$)/.test(field) && estimateFlag(row.is_estimate);
}
export const estimateMetricPrefix = "__pharos_estimate_metric:";
// Host metadata travels through ordinary public aggregate expressions. A
// companion maximum uses the same predicates and scope, so measured-only
// selections don't inherit a library-wide estimate marker.
export function withEstimateMetrics(dataset: string, request: QueryRequestV3): QueryRequestV3 {
  const flag = ["tool_calls", "tools"].includes(dataset) ? "duration_is_estimate" : ["usage", "library"].includes(dataset) ? "is_estimate" : null;
  if (!flag || request.distinct || !request.metrics?.length) return request;
  const metrics = [...request.metrics];
  const grouped = request.grouping?.summaries.some(summary => /token|cost|duration/.test(`${summary.field ?? ""} ${summary.expression ?? ""}`));
  for (const metric of request.metrics) {
    if (!grouped && !/token|cost|duration/.test(`${metric.field ?? ""} ${metric.expression ?? ""}`)) continue;
    metrics.push({ id: estimateMetricPrefix + metric.id, op: "max", groupBy: [],
      expression: request.grouping ? "MAX([__pharos_estimate])" : `MAX(IF([${flag}],1,0))`,
      scope: metric.scope, where: metric.where });
  }
  return { ...request, metrics };
}
export function estimateMetricFlags(result: { metrics?: Array<{id: string; buckets?: Array<{value: unknown}>}> }): Set<string> {
  return new Set(result.metrics?.filter(metric => metric.id.startsWith(estimateMetricPrefix) && metric.buckets?.some(bucket => Number(bucket.value) > 0)).map(metric => metric.id.slice(estimateMetricPrefix.length)));
}

export const hasUsageEstimates = (results: readonly unknown[]) => results.some(result => typeof result === "object" && result !== null && estimateFlag((result as Record<string, unknown>).is_estimate));
