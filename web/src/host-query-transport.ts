import { EMPTY_QUERY, sourceExecutionSchema, toQueryRequestV3, type ComputedExecution, type FieldSchema, type HostQueryState, type Transport } from "@pythia-software/query-table-core";

// Record keyword searches require the service's V3 source stage even when
// the table has no grouping or computed filters. Advertise only that row
// protocol, and run ordinary metric-panel requests through the same stage.
export function hostQueryV3Transport<Row>(transport: Transport<Row>, schema: FieldSchema<Row>, execution?: ComputedExecution, requiresV3: (state?: HostQueryState) => boolean = () => true): Transport<Row> {
  return {
    ...transport,
    // Read at request selection, after the current definition is restored.
    // Tables without a keyword retain the indexed V2 execution path.
    get fetchRows() { return requiresV3() ? undefined : transport.fetchRows; },
    get fetchRowsV2() { return requiresV3() ? undefined : transport.fetchRowsV2; },
    async fetchMetrics(query, signal) {
      if (!requiresV3(query.hostState) && transport.fetchMetrics) return transport.fetchMetrics(query, signal);
      const capabilities = transport.queryCapabilities;
      if (!transport.fetchQueryV3 || capabilities?.version !== 3) throw new Error("This server cannot run record keyword searches. Reload the page to retry.");
      if (query.diagnostics.length) throw new Error(query.diagnostics.map(issue => issue.message).join(" "));
      const request = toQueryRequestV3({
        ...EMPTY_QUERY, where: query.where, orderBy: query.orderBy,
        limit: query.limit, offset: query.offset, aggregations: query.metrics, hostState: query.hostState,
      }, sourceExecutionSchema(schema, execution), capabilities, execution, { rows: false });
      if (request.diagnostics.length) throw new Error(request.diagnostics.join(" "));
      request.hostState = query.hostState;
      request.expectedRevisions = { ...request.expectedRevisions, ...query.expectedRevisions };
      if (query.planToken !== undefined) request.planToken = query.planToken;
      if (query.snapshot !== undefined) request.snapshot = query.snapshot;
      const result = await transport.fetchQueryV3(request, signal);
      return { metrics: result.metrics };
    },
  };
}
