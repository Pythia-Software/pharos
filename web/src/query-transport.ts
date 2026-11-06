import { fetchRowV3, type ComputedExecution, type FieldSchema, type QueryCapabilitiesV3, type QueryRequestV3, type QueryResultV3, type RowId, type Transport } from "@pythia-software/query-table-core";
import { queryRequest } from "./query-request";

// Identity lookups and evidence reads have no collection search or saved view.
// Linked Carbon and concurrency assumptions still follow queryRequest's URL policy.
export async function datasetTransport<Row>(dataset: string, signal?: AbortSignal) {
  const { queryCapabilities, computedExecution } = await queryRequest<{ queryCapabilities: QueryCapabilitiesV3; computedExecution: ComputedExecution }>(`/api/query/${dataset}/capabilities`, { signal });
  const transport: Transport<Row> = {
    queryCapabilities,
    fetchQueryV3: (request, signal) => queryRequest<QueryResultV3>(`/api/query/${dataset}/query-v3`, {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(request), signal,
    }),
  };
  return { transport, execution: computedExecution };
}

export async function postQueryRows<Row>(dataset: string, result: QueryRequestV3["result"], signal?: AbortSignal, now = Date.now()): Promise<{ rows: Row[]; total: number }> {
  const { transport } = await datasetTransport<Row>(dataset, signal);
  const out = await transport.fetchQueryV3({ version: 3, profile: transport.queryCapabilities.profile, now, source: {select: [], where: [], orderBy: [], limit: 0, offset: 0}, result, rows: true, metrics: [], expectedRevisions: {} }, signal);
  return { rows: out.rows as Row[], total: out.total };
}

export async function queryRow<Row>(dataset: string, schema: FieldSchema<Row>, id: RowId, signal?: AbortSignal): Promise<Row | null> {
  const { transport, execution } = await datasetTransport<Row>(dataset, signal);
  return fetchRowV3<Row>(transport, schema, id, { execution }, signal);
}
