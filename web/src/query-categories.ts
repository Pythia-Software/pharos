import type { QueryState } from "@pythia-software/query-table-core";
import type { QueryDataset } from "./query-links";

// One palette for a card's subject, regardless of whether it is collected or
// suggested. Named model/repository series retain the shared Usage palette.
export const queryCategories = {
  human: { label: "Human Words", tone: "var(--gold)" },
  steering: { label: "Steering", tone: "var(--warn)" },
  errors: { label: "Error Patterns", tone: "var(--bad)" },
  context: { label: "Context", tone: "var(--heat-2)" },
  models: { label: "Model Choice", tone: "var(--heat-1)" },
  usage: { label: "Token Usage", tone: "var(--heat-0)" },
  cost: { label: "API Cost", tone: "color-mix(in srgb,var(--gold) 65%,var(--warn))" },
  research: { label: "Research", tone: "color-mix(in srgb,var(--heat-0) 70%,var(--sea))" },
  skills: { label: "Skills", tone: "var(--sea)" },
  tools: { label: "Tool Flow", tone: "color-mix(in srgb,var(--warn) 65%,var(--gold))" },
  delegation: { label: "Delegation", tone: "color-mix(in srgb,var(--heat-1) 70%,var(--sea))" },
  views: { label: "Work Views", tone: "var(--muted)" },
} as const;
export type QueryCategory = keyof typeof queryCategories;

// Older and manually saved QT queries have no recipe ID. Infer their subject
// from query fields, never from the user-editable title or description.
export function categoryForQuery(dataset?: QueryDataset, query?: QueryState): QueryCategory {
  const predicates = query?.where.flatMap(term => "any" in term ? term.any : [term]) ?? [];
  const measures = query?.aggregations ?? [];
  const fields = new Set([...predicates.map(term => term.field), ...measures.flatMap(metric => [metric.field, ...metric.groupBy])]);
  const has = (...names: string[]) => names.some(name => fields.has(name));
  if (has("cost_usd", "cost_today_usd")) return "cost";
  if (has("error_count", "error_signature", "tool_error_count", "error_types", "test_failure") || predicates.some(term => term.field === "status" && term.op === "=" && term.value === "error")) return "errors";
  if (has("carried_tokens", "result_tokens", "truncated", "compaction_count", "cache_read_input_tokens") || predicates.some(term => term.field === "tool_category" && term.op === "=" && term.value === "read")) return "context";
  if (has("subagent_count", "subagent_depth") || predicates.some(term => term.field === "session_kind" && term.op === "=" && term.value === "subagent")) return "delegation";
  if (has("model_family", "models", "model", "flavor")) return "models";
  if (dataset === "writing" || dataset === "writing_messages" || has("typed_words")) {
    return predicates.some(term => term.field === "text" && /you misunderstand|you misunderstood|that's not what|that is not what|i meant/i.test(term.value)) ? "steering" : "human";
  }
  if (has("search_query", "host")) return "research";
  if (dataset === "skill_usages" || has("skill_name")) return "skills";
  if (has("token_count", "total_tokens") || dataset === "usage") return "usage";
  if (dataset === "tool_calls" || dataset === "tools" || dataset === "mcp_calls") return "tools";
  if (dataset === "tl1_attempts") return "delegation";
  return "views";
}
