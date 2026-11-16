import { withEstimateMetrics, estimateMetricFlags } from "./usage-estimate-model";
import { EMPTY_QUERY, MAX_AGGREGATIONS, toQueryRequestV3, type AggregationClause, type ComputedExecution, type FieldSchema, type QueryCapabilitiesV3, sourceExecutionSchema, type ErrorCode, type QueryRequestV3, type QueryState, type WhereTerm } from "@pythia-software/query-table-core";
import { queryRequest, QueryRequestError } from "./query-request";

// Standalone chart and card metrics use the same V3 protocol as native tables.

export type MetricBucket = { keys: unknown[]; value: unknown; count?: number; error?: string };
export type MetricResult = { is_estimate?: boolean; id: string; buckets: MetricBucket[]; error?: string; errorCode?: ErrorCode | (string & {}); groupCount?: number; processedRows?: number; hiddenGroups?: number; coverage?: "exact" | "partial"; other?: MetricBucket };
export type MetricResponse = { metrics?: MetricResult[] };

// Keep structured resource-limit failures available to callers.
export class MetricRequestError extends Error {
  constructor(message: string, readonly resourceLimit = false) { super(message); }
}

// Compile a complete V3 metric request; unsupported predicates throw.
export function metricRequest(schema: FieldSchema<any>, where: WhereTerm[], clauses: AggregationClause[], capabilities: QueryCapabilitiesV3, execution?: ComputedExecution): QueryRequestV3 {
  return toQueryRequestV3({ ...EMPTY_QUERY, where, aggregations: clauses }, sourceExecutionSchema(schema, execution), capabilities, execution, { rows: false });
}

// metricResults reads a metrics response: one result per clause, in order. A
// metric that failed has no buckets, so reading it as zero would misreport;
// it fails the whole read instead.
export function metricResults(response: MetricResponse, clauses: AggregationClause[]): MetricResult[] {
  const byId = new Map((response.metrics ?? []).map(metric => [metric.id, metric]));
  return clauses.map(clause => {
    const metric = byId.get(clause.id);
    if (!metric) throw new MetricRequestError(`Pharos did not return every result this chart needs (metric ${clause.id}).`);
    if (metric.error) throw new MetricRequestError(metric.error, metric.errorCode === "resource_limit");
    return metric;
  });
}

async function post<T>(url: string, body: unknown, signal?: AbortSignal): Promise<T> {
  try {
    return await queryRequest<T>(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), signal });
  } catch (error) {
    if (error instanceof QueryRequestError) throw new MetricRequestError(error.message, error.code === "resource_limit");
    throw error;
  }
}

// The service accepts at most MAX_AGGREGATIONS metrics per request, counting
// the estimate companions withEstimateMetrics adds, so a larger read goes out
// as several requests over the same population.
export function metricRequestBatches(dataset: string, request: QueryRequestV3, limit = MAX_AGGREGATIONS): QueryRequestV3[] {
  const whole = withEstimateMetrics(dataset, request);
  if ((whole.metrics?.length ?? 0) <= limit) return [whole];
  const batches: QueryRequestV3[] = [];
  let current: NonNullable<QueryRequestV3["metrics"]> = [];
  for (const metric of request.metrics ?? []) {
    const next = [...current, metric];
    if (current.length && (withEstimateMetrics(dataset, { ...request, metrics: next }).metrics?.length ?? 0) > limit) {
      batches.push(withEstimateMetrics(dataset, { ...request, metrics: current }));
      current = [metric];
    } else current = next;
  }
  if (current.length) batches.push(withEstimateMetrics(dataset, { ...request, metrics: current }));
  return batches;
}

// A request's query-string parameters, such as Carbon's assumptions.
export type RequestParams = Record<string, string>;
const withParams = (path: string, params?: RequestParams) => params && Object.keys(params).length ? `${path}?${new URLSearchParams(params)}` : path;

// postMetrics runs clauses over dataset's rows matching where.
export async function postMetrics(dataset: string, schema: FieldSchema<any>, where: WhereTerm[], clauses: AggregationClause[], signal?: AbortSignal, params?: RequestParams, now?: number): Promise<MetricResult[]> {
  return postQueryStateMetrics(dataset, schema, { ...EMPTY_QUERY, where, aggregations: clauses }, signal, params, now);
}

// Metrics can reduce source rows or the result of a grouping stage. now, when
// given, is the request's clock (relative bounds such as "-7d" end there).
export async function postQueryStateMetrics(dataset: string, schema: FieldSchema<any>, query: QueryState, signal?: AbortSignal, params?: RequestParams, now?: number): Promise<MetricResult[]> {
  let handshake: { queryCapabilities?: QueryCapabilitiesV3; computedExecution?: ComputedExecution };
  try { handshake = await queryRequest<{ queryCapabilities?: QueryCapabilitiesV3; computedExecution?: ComputedExecution }>(`/api/query/${dataset}/capabilities`, { signal });
  } catch (error) { if (error instanceof QueryRequestError) throw new MetricRequestError(error.message, error.code === "resource_limit"); throw error; }
  if (handshake.queryCapabilities?.version !== 3) throw new MetricRequestError("This version of Pharos cannot answer this request. Quit and reopen Pharos, then reload the page.");
  const request = toQueryRequestV3(query, sourceExecutionSchema(schema, handshake.computedExecution), handshake.queryCapabilities, handshake.computedExecution, { rows: false, ...now === undefined ? {} : { now } });
  request.hostState = query.hostState;
  const responses = await Promise.all(metricRequestBatches(dataset, request).map(body => post<MetricResponse>(withParams(`/api/query/${dataset}/query-v3`, params), body, signal)));
  const response: MetricResponse = { metrics: responses.flatMap(part => part.metrics ?? []) };
  const flags = estimateMetricFlags(response);
  return metricResults(response, query.aggregations ?? []).map(metric => ({ ...metric, is_estimate: flags.has(metric.id) }));
}
