import { groupKeyFields, type OrderByClause, type QueryState } from "@pythia-software/query-table-core";
import type { UsageDataset } from "./usage-workspace-model";

const chronology: Record<UsageDataset, string> = {
  tl1_attempts: "started_at", usage: "first_usage_at", conversation_activity: "event_at", writing: "last_message_at",
  writing_messages: "sent_at", tool_calls: "started_at", tools: "day",
  mcp_calls: "called_at", skill_usages: "created_at", parallelism: "started_at",
};

// Read a single conversation in sequence; browse populations newest first.
// OR, negated, and multi-value filters do not establish a single conversation.
export function orderedUsageQuery(dataset: UsageDataset, query: QueryState): QueryState {
  if (query.orderBy.length) return query;
  if (query.grouping) {
    const field = groupKeyFields(query.grouping.keys)[0] ?? query.grouping.summaries.find(column => column.op !== "link")?.id;
    return field ? { ...query, orderBy: [{ field, dir: query.grouping.keys.length ? "asc" : "desc" }] } : query;
  }
  const single = query.where.some(term => "field" in term && !term.negated && term.op === "=" && term.value &&
    ["conversation_id", "work_id", "workspace_id", "unit_id"].includes(term.field));
  const orderBy: OrderByClause[] = [{ field: chronology[dataset], dir: single ? "asc" : "desc", nulls: "last" }, { field: "id", dir: "asc" }];
  return { ...query, orderBy };
}
