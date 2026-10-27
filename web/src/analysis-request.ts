import { evaluateMetrics, type AggregationResultEntry, type FieldSchema, type QueryState } from "@pythia-software/query-table-core";
import { MetricRequestError, postMetrics, postQueryStateMetrics, postQueryV3Metrics } from "./metric-request";
import { applyUsageAggregations } from "./usage-aggregations";

type Row = Record<string, any>;
// A shared snapshot carries its rows; usage also carries its hourly ledger.
export type LocalRows = { rows: Row[]; hourly?: Row[] };

// runAnalysisQuery answers a dashboard analysis's metrics. Record metrics use
// /metrics, falling back to /query-v3 when /metrics refuses their budget; a
// grouping stage needs /query-v3. A shared snapshot evaluates its own rows,
// keeping usage's hourly metrics on its hourly rows as the service does.
export async function runAnalysisQuery(dataset: string, schema: FieldSchema<any>, query: QueryState, { signal, local }: { signal?: AbortSignal; local?: LocalRows } = {}): Promise<AggregationResultEntry[]> {
  const clauses = query.aggregations ?? [];
  if (!clauses.length) return [];
  if (local) {
    const result = !query.grouping && dataset === "usage" ? applyUsageAggregations(local.rows, local.hourly ?? [], { where: query.where, aggregations: clauses }, schema) : evaluateMetrics(local.rows, query, schema);
    const failed = result.metrics.find(metric => metric.error);
    if (failed) throw new MetricRequestError(failed.error!);
    return result.metrics;
  }
  if (query.grouping) return await postQueryStateMetrics(dataset, schema, query, signal) as AggregationResultEntry[];
  try { return await postMetrics(dataset, schema, query.where, clauses, signal) as AggregationResultEntry[]; }
  catch (failure) {
    if (failure instanceof MetricRequestError && failure.resourceLimit) return await postQueryV3Metrics(dataset, schema, query.where, clauses, signal) as AggregationResultEntry[];
    throw failure;
  }
}
