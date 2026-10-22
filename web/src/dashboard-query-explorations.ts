import { EMPTY_QUERY, type QueryState, type SummaryColumn, type WhereTerm } from "@pythia-software/query-table-core";
import type { UsageDataset } from "./usage-workspace-model";

type Destination = { title: string; query: QueryState };
const sum = (field: string, label: string, display?: SummaryColumn["display"]): SummaryColumn => ({ id: `explore_${field}`, op: "sum", field, label, ...(display ? { display } : {}) });
const count: SummaryColumn = { id: "explore_count", op: "count", label: "Evidence records" };
const formula = (id: string, expression: string, label: string, percent = false): SummaryColumn => ({ id, op: "formula", expression, label, ...(percent ? { display: { kind: "percent", decimals: 1 } as const } : {}) });
const duration: SummaryColumn["display"] = { kind: "duration", sourceUnit: "milliseconds" };

// These are native queries over the same source population as the dashboard.
// IDs retain entity identity; row summaries supply readable names alongside it.
export function dashboardExplorations(dataset: UsageDataset, source: QueryState, carbon = false): Destination[] {
  const records = (title: string, fields: string[], sort: string, where: WhereTerm[] = []): Destination => ({ title, query: {
    ...source, select: fields.map(field => ({ field })), where: [...source.where, ...where], orderBy: [{ field: sort, dir: "desc" }],
  } });
  const grouped = (title: string, keys: string[], summaries: SummaryColumn[], options: { where?: WhereTerm[]; having?: WhereTerm[]; sort?: string; name?: string; time?: string } = {}): Destination => {
    const columns: SummaryColumn[] = [...summaries, ...(options.name ? [{ id: "explore_name", op: "row" as const, field: options.name, label: "Conversation" }] : [])];
    return { title, query: { ...EMPTY_QUERY, limit: source.limit,
      grouping: { source: { ...source, where: [...source.where, ...(options.where ?? [])] }, keys, summaries: columns,
        representative: options.name && options.time ? { orderBy: [{ field: options.time, dir: "desc", nulls: "last" }] } : null },
      select: [...keys, ...columns.map(column => column.id)].map(field => ({ field })), where: options.having ?? [],
      orderBy: [{ field: options.sort ?? summaries[0].id, dir: "desc" }],
    } };
  };
  switch (dataset) {
    case "usage": return carbon ? [
      records("Usage records with highest emissions", ["day", "title", "model_family", "energy_wh", "co2e_g", "total_tokens"], "co2e_g"),
      grouped("Model size vs energy & emissions", ["model_family", "carbon_tier"], [sum("energy_wh", "Estimated Wh"), sum("co2e_g", "Estimated CO₂e (g)")]),
      grouped("Repository emissions per million tokens", ["repository_name"], [formula("explore_intensity", "SUM([co2e_g])*1000000/NULLIF(SUM([total_tokens]),0)", "g CO₂e / 1M tokens"), sum("total_tokens", "Tokens")]),
    ] : [
      records("Highest-cost token records", ["day", "title", "repository_name", "model_family", "cost_usd", "total_tokens", "cache_read_share"], "cost_usd"),
      grouped("API cost by repository & model", ["repository_name", "model_family"], [sum("cost_usd", "API cost (USD)"), sum("total_tokens", "Tokens")]),
      grouped("Conversation cost & cache use", ["root_conversation_id"], [sum("cost_usd", "API cost (USD)"), formula("explore_cache_share", "SUM([cache_read_input_tokens])/NULLIF(SUM([input_tokens]),0)", "Cache-read share", true)], { name: "root_title", time: "last_usage_at" }),
    ];
    case "writing": return [
      records("Conversations with most typed words", ["title", "repository_name", "providers", "typed_words", "user_words", "pasted_words", "cost_usd"], "typed_words"),
      grouped("Writing mix by repository & provider", ["repository_name", "providers"], [sum("typed_words", "Typed words"), sum("pasted_words", "Likely pasted words"), sum("machine_words", "Harness & automation words")]),
      grouped("Repository input vs token use", ["repository_name"], [formula("explore_words_per_mtok", "SUM([typed_words])*1000000/NULLIF(SUM([total_tokens]),0)", "Typed words / 1M tokens"), sum("typed_words", "Typed words"), sum("total_tokens", "Tokens")]),
    ];
    case "skill_usages": return [
      records("Latest skill evidence", ["created_at", "skill_name", "evidence_type", "status", "content_bytes", "title"], "created_at"),
      grouped("Skills by evidence type", ["skill_name", "evidence_type"], [count, { id: "explore_conversations", op: "count_distinct", field: "conversation_id", label: "Conversations" }]),
      grouped("Repeated file-load attempts per conversation", ["conversation_id", "skill_name"], [count, formula("explore_body_bytes", "SUM(IF([content_bytes]>0,[content_bytes],0))", "Known body bytes")], { name: "title", time: "created_at", where: [{ field: "evidence_type", op: "=", value: "file_load" }], having: [{ field: count.id, op: ">", value: "1" }] }),
    ];
    case "tool_calls": return [
      records("Longest recorded tool calls", ["started_at", "operation_name", "duration_ms", "status", "result_tokens", "title"], "duration_ms"),
      grouped("Command-family time by provider", ["operation_name", "provider"], [sum("duration_ms", "Recorded tool time", duration), sum("error_count", "Errors")]),
      grouped("Conversation tool time vs context added", ["conversation_id"], [sum("duration_ms", "Recorded tool time", duration), sum("result_tokens", "Context added (tokens)")], { name: "title", time: "started_at" }),
    ];
    case "tools": return [
      records("Daily tool summaries with most calls", ["day", "tool_name", "provider", "call_count", "error_count", "result_tokens", "carried_tokens"], "call_count"),
      grouped("Tool failure rates by provider", ["tool_name", "provider"], [formula("explore_error_rate", "SUM([error_count])/NULLIF(SUM([call_count]),0)", "Failure rate", true), sum("error_count", "Errors"), sum("call_count", "Calls")]),
      grouped("Context added vs carried by tool & program", ["tool_name", "program"], [sum("result_tokens", "Context added (tokens)"), sum("carried_tokens", "Context carried (tokens)")]),
    ];
    case "parallelism": return [
      records("Busiest hourly occupancy records", ["started_at", "unit_label", "repository_name", "level", "unit_hours"], "level"),
      grouped("Concurrency by hour of day", ["hour_of_day", "level_band"], [sum("elapsed_hours", "Elapsed hours"), sum("parallel_elapsed_hours", "Parallel elapsed hours")]),
      grouped("Most active units in the selected range", ["unit_id", "unit_label"], [sum("unit_hours", "Active unit hours"), sum("parallel_elapsed_hours", "Parallel elapsed hours")]),
    ];
    case "mcp_calls": return [
      records("Largest estimated MCP responses", ["called_at", "tool_name", "status", "estimated_output_tokens", "duration_ms", "arguments_json"], "estimated_output_tokens"),
      grouped("MCP tools by requested archive source", ["tool_name", "query_source"], [sum("call_count", "Calls"), sum("estimated_output_tokens", "Est. output tokens")]),
      records("Failed or blocked MCP requests", ["called_at", "tool_name", "status", "error_text", "duration_ms"], "called_at", [{ field: "error_count", op: ">", value: "0" }]),
    ];
    default: return [];
  }
}
