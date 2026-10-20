import { EMPTY_QUERY, toMetricQuery, toQueryRequestV3, type AggregationClause, type ComputedExecution, type FieldSchema, type MetricQuery, type QueryCapabilitiesV3, type WhereTerm } from "@pythia-software/query-table-core";
import { queryRequest, QueryRequestError } from "./query-request";

// Charts and cards outside a table ask the server for metrics directly: a
// V2 /metrics request over the rows matching where, as a table's metric panel
// sends. The server returns every group of a metric or that metric's error.

export type MetricBucket = { keys: unknown[]; value: unknown; count?: number; error?: string };
export type MetricResult = { id: string; buckets: MetricBucket[]; error?: string; groupCount?: number; processedRows?: number };
export type MetricResponse = { metrics?: MetricResult[] };

// The adapter's per-metric message when a metric has more groups than its
// generic budget allows. Machine Time's own cube budget is worded differently.
const groupBudget = "group population exceeds host budget";

// resourceLimit marks a failure that a larger execution budget can answer:
// more matching rows, or more groups, than /metrics allows.
export class MetricRequestError extends Error {
  constructor(message: string, readonly resourceLimit = false) { super(message); }
}

// metricRequest is the /metrics body for clauses over the rows matching where.
// Labels and display settings are dropped; a filter the server cannot run
// fails here rather than being left out.
export function metricRequest(schema: FieldSchema<any>, where: WhereTerm[], clauses: AggregationClause[]): MetricQuery {
  const request = toMetricQuery({ ...EMPTY_QUERY, where, aggregations: clauses }, schema);
  if (request.diagnostics.length) throw new MetricRequestError(request.diagnostics.map(issue => issue.message).join(" "));
  return request;
}

// metricResults reads a metrics response: one result per clause, in order. A
// metric that failed has no buckets, so reading it as zero would misreport;
// it fails the whole read instead.
export function metricResults(response: MetricResponse, clauses: AggregationClause[]): MetricResult[] {
  const byId = new Map((response.metrics ?? []).map(metric => [metric.id, metric]));
  return clauses.map(clause => {
    const metric = byId.get(clause.id);
    if (!metric) throw new MetricRequestError(`The server omitted the metric ${clause.id}.`);
    if (metric.error) throw new MetricRequestError(metric.error, metric.error === groupBudget);
    return metric;
  });
}

async function post<T>(url: string, body: unknown, signal?: AbortSignal): Promise<T> {
  try {
    return await queryRequest<T>(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), signal });
  } catch (error) {
    if (error instanceof QueryRequestError) throw new MetricRequestError(error.message, /resource_limit/.test(error.message));
    throw error;
  }
}

// postMetrics runs clauses over dataset's rows matching where.
export async function postMetrics(dataset: string, schema: FieldSchema<any>, where: WhereTerm[], clauses: AggregationClause[], signal?: AbortSignal): Promise<MetricResult[]> {
  return metricResults(await post<MetricResponse>(`/api/query/${dataset}/metrics`, metricRequest(schema, where, clauses), signal), clauses);
}

// postQueryV3Metrics runs the same clauses through /query-v3, which stages up
// to ten million matching rows and allows more groups than /metrics: slower,
// but it answers what /metrics refuses for its budgets.
export async function postQueryV3Metrics(dataset: string, schema: FieldSchema<any>, where: WhereTerm[], clauses: AggregationClause[], signal?: AbortSignal): Promise<MetricResult[]> {
  const handshake = await queryRequest<{ queryCapabilities?: QueryCapabilitiesV3; computedExecution?: ComputedExecution }>(`/api/query/${dataset}/capabilities`, { signal });
  if (handshake.queryCapabilities?.version !== 3) throw new MetricRequestError("This server cannot run the query with a larger budget.");
  const request = toQueryRequestV3({ ...EMPTY_QUERY, where, aggregations: clauses }, schema, handshake.queryCapabilities, handshake.computedExecution, { rows: false });
  if (request.diagnostics?.length) throw new MetricRequestError(request.diagnostics.join(" "));
  return metricResults(await post<MetricResponse>(`/api/query/${dataset}/query-v3`, request, signal), clauses);
}
