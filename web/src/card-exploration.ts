import type { WhereClause, WhereTerm } from "@pythia-software/query-table-core";
import { usageDatasets, usageWorkspaceURL, type UsageDataset } from "./usage-workspace-model";
import writing from "../../schemas/writing.schema.json";
import tools from "../../schemas/tools.schema.json";

const cohorts = new Set(["repository_name", "source_kind", "channel", "machine_id", "execution_machine_id", "provider", "model", "model_family", "model_effort", "effort", "session_kind", "tool_name", "tool_category", "program", "subcommand", "command_name", "command_category", "mcp_server", "skill_name", "evidence_type", "status"]);
const targetFields = { writing: new Set(writing.fields.map(field => field.name)), tools: new Set(tools.fields.map(field => field.name)) };
const sourceTimes: Partial<Record<UsageDataset, string[]>> = { tool_calls: ["started_at", "ended_at"], writing_messages: ["sent_at"], conversation_activity: ["event_at"] };

// Carry shared context across grains. The details retain every original
// predicate. Carry an OR as a whole or omit it as a whole, so removing an
// unsupported branch cannot accidentally narrow the dashboard population.
export function cardDashboardWhere(source: UsageDataset, target: UsageDataset, where: WhereTerm[]): WhereTerm[] {
  if (source === target) return where;
  if (target !== "writing" && target !== "tools") return [];
  const project = (term: WhereClause): WhereClause | undefined => {
    if (sourceTimes[source]?.includes(term.field) && !term.negated && [">", ">=", "<", "<="].includes(term.op)) {
      return { ...term, field: target === "tools" ? "day" : [">", ">="].includes(term.op) ? "last_message_at" : "first_message_at" };
    }
    return cohorts.has(term.field) && targetFields[target].has(term.field) ? term : undefined;
  };
  return where.flatMap<WhereTerm>(term => {
    if ("any" in term) {
      const projected = term.any.map(project);
      return projected.every((clause): clause is WhereClause => Boolean(clause)) ? [{ ...term, any: projected }] : [];
    }
    const projected = project(term);
    return projected ? [projected] : [];
  });
}

export function cardSourceDataset(params: URLSearchParams): UsageDataset | undefined {
  const dataset = params.get("card_query");
  return dataset && Object.hasOwn(usageDatasets, dataset) && params.getAll(`q_${dataset}`).length === 1 ? dataset as UsageDataset : undefined;
}

// Preserve durable @snapshot tokens without needing to fetch them first.
export function cardDetailsURL(params: URLSearchParams): string | undefined {
  const dataset = cardSourceDataset(params);
  if (!dataset || !params.get("card_id")) return;
  return usageWorkspaceURL({ mode: "query", dataset, search: false }, params);
}
