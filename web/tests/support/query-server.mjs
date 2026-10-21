// The fake Query Table server shared by the node tests and the browser
// harness: the production transport (capabilities, query-v3, metrics and
// rows-v2) answered by @pythia-software/query-table-core over in-memory rows,
// plus `population`, a small synthetic row set for any schema.
import * as core from "@pythia-software/query-table-core";

export const execution = { profile: "qt-sqlite-v1", planToken: "", fields: {}, resolvedRevisions: {} };
export const queryCapabilities = { version: 3, profile: execution.profile, groupedRows: true, calendarGrouping: true, computedSourceFilters: true, regex: false, maxRows: 10000, maxGroups: 10000, maxPopulation: 250000 };
export const metricCapabilities = { version: 2, expressions: true, distributions: true };
export const capabilities = () => ({ metricCapabilities, computedExecution: execution, queryCapabilities });

const day = 86400000;

// `count` rows dated over the 40 days before `now`, with every schema field
// filled and the identity and category fields the cards group by cycling
// through a few values.
export function population(schema, { now = Date.now(), count = 96 } = {}) {
  return Array.from({ length: count }, (_, index) => {
    const at = new Date(now - (index % 40 + .25) * day).toISOString();
    const row = Object.fromEntries(schema.fields.map(field => [field.name, field.type === "datetime" ? at : field.type === "number" ? (index + 1) * 100 : field.type === "bool" ? index % 2 === 0 : `${field.name}-${index % 3}`]));
    // Calendar bucket keys are text dimensions. Keep their synthetic values
    // on the same date/ISO-hour contract as the service.
    if (Object.hasOwn(row, "hour")) row.hour = `${at.slice(0, 13)}:00:00.000Z`;
    if (Object.hasOwn(row, "month")) row.month = at.slice(0, 7);
    if (Object.hasOwn(row, "week")) {
      const monday = new Date(at); monday.setUTCDate(monday.getUTCDate() - (monday.getUTCDay() + 6) % 7);
      row.week = monday.toISOString().slice(0, 10);
    }
    return { ...row, id: `row-${index}`, day: at.slice(0, 10), title: `Work ${index % 8}`, conversation_id: `conversation-${index % 8}`, root_conversation_id: `conversation-${index % 8}`,
      work_id: `row-${index % 8}`, workspace_id: `row-${index % 8}`, location: `/repo/dir-${index % 4}`, repository_name: `repo-${index % 3}`, root_repository_name: `repo-${index % 3}`,
      provider: index % 2 ? "codex" : "claude", root_provider: index % 2 ? "codex" : "claude", session_kind: index % 3 ? "root" : "subagent",
      session_role: index % 3 ? "root" : "subagent", price_status: "priced", tool_name: ["Read", "Bash", "Edit"][index % 3], tool_category: ["read", "command", "edit", "web"][index % 4],
      status: index % 2 ? "ok" : "error", error_count: index % 2 ? 0 : 1, call_count: 1, result_tokens_source: "measured", evidence_type: "body-load",
      text: "Replace 3 fields because they repeat.", text_truncated: false, segments: [{ category: "typed", text: "Replace 3 fields because they repeat." }], pasted_words: 0, attachment_words: 0 };
  });
}

// Answers a query-v3 request the way the server does: the result page, the
// metrics over the source rows, and the (possibly grouped) result schema.
export function serverQuery(request, rows, schema) {
  const state = { ...core.EMPTY_QUERY, ...request.result, select: request.result.select.map(field => ({ field })), aggregations: request.metrics,
    ...(request.grouping ? { grouping: { ...request.grouping, source: { ...core.EMPTY_QUERY, ...request.source, select: request.source.select.map(field => ({ field })) } } } : {}) };
  const prepared = state.grouping ? core.prepareGrouping(rows, schema, state.grouping, { now: request.now }) : undefined;
  const projected = prepared?.schema ?? schema, page = core.applyQuery(rows, state, schema, { now: request.now });
  const metrics = core.evaluateMetrics(rows, state, schema, { now: request.now, shownRows: page.rows });
  return { version: 3, now: request.now, sourceTotal: prepared?.inputs.length ?? rows.length, rows: request.rows ? page.rows : [], total: page.total, metrics: metrics.metrics, execution,
    schema: { name: projected.name, idField: projected.idField, fields: projected.fields.map(({ name, type }) => ({ name, type })) } };
}

// Answers any Query Table operation on one dataset ("rows" is the legacy
// POST /api/query/<dataset>), or returns undefined for one this fake does not
// serve.
export function answerQuery(operation, request, rows, schema, { now = Date.now(), id } = {}) {
  switch (operation) {
    case "rows": return core.applyQuery(rows, { ...core.EMPTY_QUERY, ...request }, schema, { now });
    case "item": return { row: rows.find(row => String(row[schema.idField ?? "id"]) === String(id)) ?? null };
    case "capabilities": return capabilities();
    case "query-v3": return serverQuery(request, rows, schema);
    case "metrics": return { version: 2, execution, ...core.evaluateMetrics(rows, { ...core.EMPTY_QUERY, ...request, aggregations: request.metrics }, schema, { now }) };
    case "rows-v2": return { version: 2, ...core.applyQuery(rows, { ...core.EMPTY_QUERY, ...request }, schema, { now }), computed: [], execution };
    case "distinct": return { values: [], hasMore: false };
    case "field-stats": return {};
  }
}
