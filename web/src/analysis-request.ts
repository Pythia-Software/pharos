import { hasUsageEstimates } from "./usage-estimate-model";
import { evaluateMetrics, planMetricSeries, type AggregationResultEntry, type FieldSchema, type QueryState } from "@pythia-software/query-table-core";
import { MetricRequestError, postQueryStateMetrics, type RequestParams } from "./metric-request";
import { applyUsageAggregations, usageRowsForWhere } from "./usage-aggregations";

type Row = Record<string, any>;
// A shared snapshot carries its rows; usage also carries its hourly ledger.
export type LocalRows = { rows: Row[]; hourly?: Row[] };

// Dashboard metrics, grouped views and calendars all use V3. Shared snapshots
// evaluate their own rows, including Usage's distinct hourly population.
// params are the catalog's request parameters (AnalysisRequest), sent with
// every request; a snapshot's rows already hold what they describe.
export async function runAnalysisQuery(dataset: string, schema: FieldSchema<any>, query: QueryState, { signal, local, params, now = Date.now() }: { signal?: AbortSignal; local?: LocalRows; params?: RequestParams; now?: number } = {}): Promise<AggregationResultEntry[]> {
  const clauses = query.aggregations ?? [];
  if (!clauses.length) return [];
  if (local) {
    const evaluate = (current: QueryState) => !current.grouping && dataset === "usage" ? applyUsageAggregations(local.rows, local.hourly ?? [], { where: current.where, aggregations: current.aggregations ?? [] }, schema)
      : current.grouping && dataset === "usage" ? groupedUsage(local, current, schema) : evaluateMetrics(local.rows, current, schema, { now });
    const result = evaluate(query);
    const failed = result.metrics.find(metric => metric.error);
    if (failed) throw new MetricRequestError(failed.error!);
    const flag = ["tool_calls", "tools"].includes(dataset) ? "duration_is_estimate" : ["usage", "library"].includes(dataset) ? "is_estimate" : "";
    let estimated = false;
    if (flag && [...clauses,...query.grouping?.summaries ?? []].some(metric => /token|cost|duration/.test(`${metric.field ?? ""} ${metric.expression ?? ""}`))) {
      const grouping = query.grouping ? { ...query.grouping, summaries: [...query.grouping.summaries, {id:"__pharos_estimate",op:"formula" as const,expression:`MAX(IF([${flag}],1,0))`}] } : undefined;
      const quality = evaluate({ ...query, grouping, aggregations: [{id:"__pharos_estimate",op:"max",expression:grouping?"MAX([__pharos_estimate])":`MAX(IF([${flag}],1,0))`,groupBy:[]}] });
      const failure = quality.metrics.find(metric => metric.error);
      if (failure) throw new MetricRequestError(failure.error!);
      estimated = quality.metrics.some(metric => metric.buckets.some(bucket => Number(bucket.value)>0));
    }
    return result.metrics.map(metric => ({...metric,is_estimate:estimated}));
  }
  if (query.grouping) return await postQueryStateMetrics(dataset, schema, query, signal, params) as AggregationResultEntry[];
  // Per-occurrence statistics (a timestamp cycle's boxes and means) and whole
  // intervals are computed from plain calendar metrics, which the client
  // plans and combines: servers never receive them. The plan's queries carry
  // absolute bounds, so they run as asked whenever they arrive.
  const plan = planMetricSeries(query, schema, { now });
  if (plan) {
    const parts = (await Promise.all(plan.queries.map(part => runPlain(dataset, schema, part, signal, params)))).flat();
    return plan.combine(parts).map(metric => ({ ...metric, is_estimate: hasUsageEstimates(parts) }));
  }
  return await runPlain(dataset, schema, query, signal, params);
}

async function runPlain(dataset: string, schema: FieldSchema<any>, query: QueryState, signal?: AbortSignal, params?: RequestParams): Promise<AggregationResultEntry[]> {
  return await postQueryStateMetrics(dataset, schema, query, signal, params) as AggregationResultEntry[];
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
