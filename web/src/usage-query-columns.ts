import { ITEM_DETAILS_COLUMN, groupKeyFields, type FieldDef, type FieldSchema, type QueryState } from "@pythia-software/query-table-core";
import { toolSummaryDimensions } from "./tools-dashboard-model";
import type { UsageDataset } from "./usage-workspace-model";

export type UsageDrilldown = "source" | "requested" | "result" | "body" | "tool" | "calls" | "messages" | "root";
type DrilldownColumn = { kind: UsageDrilldown; label: string; dependencies: string[] };
const source = (dependencies: string[]): DrilldownColumn => ({ kind: "source", label: "Source", dependencies });
const conversation = ["workspace_id", "conversation_id"];
export const usageDrilldownColumns: Record<UsageDataset, DrilldownColumn[]> = {
  model_requests: [source([...conversation, "requested_at"])],
  tl1_attempts: [source(["workspace_id"])],
  usage: [source([...conversation, "first_usage_at"]), { kind: "calls", label: "Calls", dependencies: ["conversation_id"] }, { kind: "root", label: "Root", dependencies: ["conversation_id", "root_conversation_id"] }],
  conversation_activity: [source([...conversation, "message_id", "event_at"]), { kind: "root", label: "Root", dependencies: ["conversation_id", "root_conversation_id"] }],
  writing: [source(["id"]), { kind: "messages", label: "Messages", dependencies: ["id"] }],
  writing_messages: [source([...conversation, "id"])],
  tool_calls: [source([...conversation, "call_message_id", "call_id", "started_at"]), { kind: "result", label: "Result", dependencies: [...conversation, "result_message_id", "call_id"] }],
  tools: [{ kind: "calls", label: "Calls", dependencies: toolSummaryDimensions }],
  mcp_calls: [{ kind: "requested", label: "Requested target", dependencies: ["arguments_json"] }],
  skill_usages: [source([...conversation, "evidence_message_id", "created_at"]), { kind: "body", label: "Skill body", dependencies: [...conversation, "evidence_message_id", "body_message_id"] }, { kind: "tool", label: "Tool call", dependencies: ["tool_call_id"] }],
  parallelism: [source(["conversation_id", "location", "started_at"])],
};
export const usageDrilldownField = (kind: UsageDrilldown) => `usage_drill_${kind}`;

// Ordinary host-owned derived fields use Query Table's dependency projection
// and render registry. They never become backend SELECT fields.
export function usageQuerySchema(schema: FieldSchema, dataset: UsageDataset): FieldSchema {
  const fields: FieldDef[] = usageDrilldownColumns[dataset].map(column => ({
    name: usageDrilldownField(column.kind), label: `Drill in · ${column.label}`, type: "text",
    source: { kind: "derived", dependencies: column.dependencies },
    filter: { enabled: false }, sort: { enabled: false }, aggregate: { measure: false, groupable: false },
    render: usageDrilldownField(column.kind), select: { width: 170 }, property: { default: false },
  }));
  return { ...schema, fields: [...schema.fields, ...fields] };
}

// A grouped result ends with its link summaries, then native group Detail
// (Query Table 0.9.7), as record tables end with their drill-ins and Detail.
// An empty select means Query Table's grouped default, the keys, so older
// saves and links show their keys, then Detail. Columns the select leaves
// out, link summaries included, stay out.
export function withGroupDetail(query: QueryState): QueryState {
  if (!query.grouping) return query;
  const links = new Set(query.grouping.summaries.filter(column => column.op === "link").map(column => column.id));
  const select = query.select.length ? query.select : groupKeyFields(query.grouping.keys).map(field => ({ field }));
  const columns = [
    ...select.filter(column => column.field !== ITEM_DETAILS_COLUMN && !links.has(column.field)),
    ...select.filter(column => links.has(column.field)),
    select.find(column => column.field === ITEM_DETAILS_COLUMN) ?? { field: ITEM_DETAILS_COLUMN },
  ];
  return JSON.stringify(columns) === JSON.stringify(query.select) ? query : { ...query, select: columns };
}

export function usageActionQuery(query: QueryState, schema: FieldSchema, dataset: UsageDataset): QueryState {
  if (query.grouping) return withGroupDetail(query);
  const actionFields = usageDrilldownColumns[dataset].map(column => usageDrilldownField(column.kind));
  const select = query.select.length ? query.select : schema.defaultSelect ?? [];
  const data = select.filter(column => column.field !== ITEM_DETAILS_COLUMN && !actionFields.includes(column.field));
  const columns = [...data, ...actionFields.map(field => select.find(column => column.field === field) ?? { field }), select.find(column => column.field === ITEM_DETAILS_COLUMN) ?? { field: ITEM_DETAILS_COLUMN }];
  const modes = query.itemDetails?.modes.length ? query.itemDetails.modes : ["modal", "expand"] as const;
  if (JSON.stringify(columns) === JSON.stringify(query.select) && query.itemDetails?.modes.length) return query;
  return { ...query, select: columns, itemDetails: { ...query.itemDetails, modes: [...modes] } };
}
