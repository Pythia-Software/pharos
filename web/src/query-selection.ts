import { sourceExecutionSchema, toQueryRequestV3, toServerQueryV2, validateQueryResultV3, type ComputedExecution, type FieldSchema, type QueryState, type RowId, type Transport } from "@pythia-software/query-table-core";

// Share/export needs every matching identity, independent of the visible page
// or selected columns. Use the same revision-aware row transport as the table.
export async function matchingSelectionIDs<Row extends Record<string, unknown>>(
  query: QueryState, schema: FieldSchema<Row>, execution: ComputedExecution,
  transport: Transport<Row>, limit: number,
): Promise<RowId[]> {
  if (query.grouping) throw new Error("Select source records before exporting conversations.");
  const selection = { ...query, select: [{ field: schema.idField }], aggregations: [], offset: 0, limit };
  let result: { rows: Record<string, unknown>[]; total: number };
  if (transport.queryCapabilities?.version === 3 && transport.fetchQueryV3) {
    // The table catalogue exposes computed fields as derived. The negotiated
    // execution schema gives canonical fields their server filter/sort policy.
    const source = sourceExecutionSchema(schema, execution);
    const request = toQueryRequestV3(selection, source, transport.queryCapabilities, execution, { rows: true });
    if (request.diagnostics.length) throw new Error(request.diagnostics.join("; "));
    const response = await transport.fetchQueryV3(request);
    validateQueryResultV3(response, request, source);
    result = response;
  } else if (transport.fetchRowsV2) {
    const request = toServerQueryV2(selection, schema, execution);
    if (request.diagnostics.length) throw new Error(request.diagnostics.map(item => item.message).join("; "));
    const response = await transport.fetchRowsV2(request);
    if (response.version !== 2 || response.execution.profile !== request.profile ||
        request.snapshot !== undefined && response.execution.snapshot !== request.snapshot ||
        Object.entries(request.expectedRevisions).some(([id, revision]) => response.execution.resolvedRevisions[id] !== revision)) {
      throw new Error("The query definitions changed. Refresh the table and try again.");
    }
    result = response;
  } else {
    throw new Error("The server does not support selecting matching works.");
  }
  if (!Number.isSafeInteger(result.total) || result.total < 0 || result.total > limit || result.rows.length !== result.total) {
    throw new Error("The matching works no longer fit in one export. Refresh or narrow the query.");
  }
  const ids = result.rows.map(row => row[schema.idField]);
  if (ids.some(id => typeof id !== "string" && typeof id !== "number") || new Set(ids).size !== ids.length) {
    throw new Error("The matching works have missing or duplicate identities. Refresh the table and try again.");
  }
  return ids as RowId[];
}
