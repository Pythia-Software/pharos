import { evaluateMetrics, planMetricSeries, type AggregationResultEntry, type FieldSchema, type QueryState } from "@pythia-software/query-table-core";
import { MetricRequestError, postMetrics, postQueryStateMetrics, postQueryV3Metrics, type RequestParams } from "./metric-request";
import { applyUsageAggregations, usageRowsForWhere } from "./usage-aggregations";

type Row = Record<string, any>;
// A shared snapshot carries its rows; usage also carries its hourly ledger.
export type LocalRows = { rows: Row[]; hourly?: Row[] };

// runAnalysisQuery answers a dashboard analysis's metrics. Record metrics use
// /metrics, falling back to /query-v3 when /metrics refuses their budget; a
// grouping stage and calendar grouping need /query-v3. A shared snapshot evaluates its own rows,
// keeping usage's hourly metrics on its hourly rows as the service does.
// params are the catalog's request parameters (AnalysisRequest), sent with
// every request; a snapshot's rows already hold what they describe.
export async function runAnalysisQuery(dataset: string, schema: FieldSchema<any>, query: QueryState, { signal, local, params, now = Date.now() }: { signal?: AbortSignal; local?: LocalRows; params?: RequestParams; now?: number } = {}): Promise<AggregationResultEntry[]> {
  const clauses = query.aggregations ?? [];
  if (!clauses.length) return [];
  if (local) {
    const result = !query.grouping && dataset === "usage" ? applyUsageAggregations(local.rows, local.hourly ?? [], { where: query.where, aggregations: clauses }, schema)
      : query.grouping && dataset === "usage" ? groupedUsage(local, query, schema) : evaluateMetrics(local.rows, query, schema, { now });
    const failed = result.metrics.find(metric => metric.error);
    if (failed) throw new MetricRequestError(failed.error!);
    return result.metrics;
  }
  if (query.grouping) return await postQueryStateMetrics(dataset, schema, query, signal, params) as AggregationResultEntry[];
  // Per-occurrence statistics (a timestamp cycle's boxes and means) and whole
  // intervals are computed from plain calendar metrics, which the client
  // plans and combines: servers never receive them. The plan's queries carry
  // absolute bounds, so they run as asked whenever they arrive.
  const plan = planMetricSeries(query, schema, { now });
  if (plan) return plan.combine((await Promise.all(plan.queries.map(part => runPlain(dataset, schema, part, signal, params)))).flat());
  return await runPlain(dataset, schema, query, signal, params);
}

async function runPlain(dataset: string, schema: FieldSchema<any>, query: QueryState, signal?: AbortSignal, params?: RequestParams): Promise<AggregationResultEntry[]> {
  const clauses = query.aggregations ?? [];
  // Calendar grouping of a timestamp (a cycle's slots or occurrences, a
  // trend's hours) runs on /query-v3.
  if (clauses.some(clause => clause.dateGrouping)) return await postQueryV3Metrics(dataset, schema, query.where, clauses, signal, params) as AggregationResultEntry[];
  try { return await postMetrics(dataset, schema, query.where, clauses, signal, params) as AggregationResultEntry[]; }
  catch (failure) {
    if (failure instanceof MetricRequestError && failure.resourceLimit) return await postQueryV3Metrics(dataset, schema, query.where, clauses, signal, params) as AggregationResultEntry[];
    throw failure;
  }
}

// A grouping stage reads daily usage rows, which carry no hour. As the service
// does, a source filter on hour first selects the days holding a matching
// hour, and the stage then reads those days unfiltered.
function groupedUsage(local: LocalRows, query: QueryState, schema: FieldSchema<any>) {
  const source = query.grouping!.source;
  const selected = usageRowsForWhere(local.rows, local.hourly ?? [], source.where, schema);
  if (!selected.filtersHours) return evaluateMetrics(local.rows, query, schema);
  return evaluateMetrics(selected.days, { ...query, grouping: { ...query.grouping!, source: { ...source, where: [] } } }, schema);
}
