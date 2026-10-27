import { EMPTY_QUERY, type FieldDef, type FieldSchema, type WhereTerm } from "@pythia-software/query-table-core";
import { usageDatasets, usageWorkspaceURL, type UsageDataset } from "./usage-workspace-model";
import { queryURL } from "./query-links";
import { orderedUsageQuery } from "./usage-query-order";
import type { UsageDrilldown } from "./usage-query-columns";
import { toolSummaryCallFilters } from "./tools-dashboard-model";

type Row = Record<string, any>;
export type UsageRowLink = { kind?: UsageDrilldown; label: string; href: string };
const identifier = (value: unknown) => typeof value === "string" && value.trim() ? value : undefined;

// Aggregate records have time anchors, individual events have exact messages.
// A conversation-only URL is resolved by the server, including modified clicks.
export function conversationRowURL(row: Row, message?: unknown, at?: unknown, call?: unknown): string | undefined {
  const conversation = identifier(row.conversation_id), workspace = identifier(row.workspace_id);
  if (!conversation && !workspace) return;
  const params = new URLSearchParams();
  if (conversation) params.set("conversation", conversation);
  if (conversation && identifier(message)) { params.set("message", String(message)); if (identifier(call)) params.set("call", String(call)); }
  else if (conversation && identifier(at) && Number.isFinite(Date.parse(String(at)))) params.set("at", String(at));
  const path = workspace ? `/work/${encodeURIComponent(workspace)}` : `/conversation/${encodeURIComponent(conversation!)}`;
  return path + (params.size ? `?${params}` : "");
}
export function primaryUsageSource(dataset: UsageDataset, row: Row) {
  if (dataset === "parallelism" && !identifier(row.conversation_id) && identifier(row.location)) return queryURL("library", { ...EMPTY_QUERY, orderBy: [{ field: "activity_at", dir: "desc" }], where: [{ field: "location", op: "=", value: row.location }] });
  if (dataset === "writing") return conversationRowURL({ workspace_id: row.id });
  const message = dataset === "writing_messages" ? row.id : dataset === "tool_calls" ? row.call_message_id : dataset === "skill_usages" ? row.evidence_message_id : row.message_id;
  const at = dataset === "usage" ? row.first_usage_at : dataset === "conversation_activity" ? row.event_at : dataset === "skill_usages" ? row.created_at : row.started_at;
  return conversationRowURL(row, message, at, dataset === "tool_calls" ? row.call_id : undefined);
}
const filtered = (dataset: UsageDataset, where: WhereTerm[], current: URLSearchParams) => usageWorkspaceURL({ mode: "query", dataset, query: orderedUsageQuery(dataset, { ...EMPTY_QUERY, where }) }, current);
export function usageRowLinks(dataset: UsageDataset, row: Row, current = new URLSearchParams()): UsageRowLink[] {
  const links: UsageRowLink[] = [];
  const source = primaryUsageSource(dataset, row);
  if (source) links.push({ kind: "source", label: source.startsWith("/library?") ? "Works at this checkout" : new URL(source, "https://pharos.invalid").searchParams.has("message") ? dataset === "tool_calls" ? "Call message" : dataset === "skill_usages" ? "Evidence message" : "Source message" : new URL(source, "https://pharos.invalid").searchParams.has("at") ? "Conversation near this time" : "Open work", href: source });
  if (dataset === "mcp_calls") {
    // These are request targets, never inferred caller identity or response data.
    try {
      const args = typeof row.arguments_json === "string" ? JSON.parse(row.arguments_json) : row.arguments_json;
      if (args && typeof args === "object" && !Array.isArray(args)) {
        const message = identifier(args.message_id) ?? identifier(args.around_message_id);
        const href = conversationRowURL(args, message);
        if (href) links.push({ kind: "requested", label: identifier(args.conversation_id) ? message ? "Requested message" : "Requested conversation" : "Requested work", href });
      }
    } catch { /* Malformed retained arguments remain readable as text. */ }
  }
  if (dataset === "tool_calls" && identifier(row.result_message_id)) {
    const href = conversationRowURL(row, row.result_message_id, undefined, row.call_id);
    if (href) links.push({ kind: "result", label: "Result message", href });
  }
  if (dataset === "skill_usages") {
    if (identifier(row.body_message_id) && row.body_message_id !== row.evidence_message_id) {
      const href = conversationRowURL(row, row.body_message_id);
      if (href) links.push({ kind: "body", label: "Retained skill body", href });
    }
    if (identifier(row.tool_call_id)) links.push({ kind: "tool", label: "Tool call details", href: usageWorkspaceURL({ mode: "query", dataset: "tool_calls", query: orderedUsageQuery("tool_calls", { ...EMPTY_QUERY, where: [{ field: "id", op: "=", value: row.tool_call_id }], itemDetails: { modes: ["modal", "expand"], active: { id: row.tool_call_id, mode: "modal" } } }) }, current) });
  }
  if (dataset === "tools") links.push({ kind: "calls", label: "Matching calls", href: filtered("tool_calls", toolSummaryCallFilters(row), current) });
  if (dataset === "writing" && identifier(row.id)) links.push({ kind: "messages", label: "Messages in this work", href: filtered("writing_messages", [{ field: "work_id", op: "=", value: row.id }], current) });
  if (dataset === "usage" && identifier(row.conversation_id)) links.push({ kind: "calls", label: "Tool calls in this conversation", href: filtered("tool_calls", [{ field: "conversation_id", op: "=", value: row.conversation_id }], current) });
  if (["usage", "conversation_activity"].includes(dataset) && identifier(row.root_conversation_id) && row.root_conversation_id !== row.conversation_id) links.push({ kind: "root", label: "Root conversation", href: `/conversation/${encodeURIComponent(row.root_conversation_id)}` });
  return links;
}

// Order is intentional: content/evidence first, then measures and provenance.
// All other fields remain available in the native property picker.
export const usageDetailFields: Record<UsageDataset, string[]> = {
  usage: ["source_links", "title", "first_usage_at", "last_usage_at", "repository_name", "model_effort", "session_kind", "total_tokens", "cost_usd", "price_status", "uncached_input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "output_tokens", "reasoning_output_tokens", "cache_read_share", "attribution", "root_title", "energy_wh", "co2e_g", "carbon_tier"],
  conversation_activity: ["channel", "machine_id", "execution_machine_id", "source_links", "title", "event_at", "event_kind", "human_input_tokens_estimate", "human_messages", "output_tokens", "repository_name", "root_title", "root_provider", "root_model_family", "root_effort"],
  writing: ["channel", "machine_id", "execution_machine_id", "source_links", "title", "repository_name", "first_message_at", "last_message_at", "typed_words", "typed_turns", "typed_share", "user_words", "pasted_words", "copied_words", "quoted_words", "resent_words", "template_words", "attachment_words", "harness_words", "automated_words", "total_tokens", "cost_usd", "price_status", "typed_per_mtok"],
  writing_messages: ["channel", "machine_id", "execution_machine_id", "source_links", "text", "text_truncated", "main_category", "categories", "rules", "classification_sources", "segments", "typed_words", "other_words", "words", "typed_share", "title", "sent_at", "provider", "repository_name"],
  tool_calls: ["channel", "machine_id", "execution_machine_id", "source_links", "tool_name", "status", "duration_ms", "result_tokens", "carried_tokens", "error_type", "input_text", "input_text_truncated", "result_text", "result_text_truncated", "result_details", "commands", "urls", "started_at", "ended_at", "title", "repository_name", "model_effort", "tool_cost_usd", "request", "next_request"],
  tools: ["channel", "machine_id", "execution_machine_id", "source_links", "tool_name", "command_name", "day", "repository_name", "model_effort", "session_kind", "source_kind", "provider", "tool_category", "command_category", "program", "subcommand", "mcp_server", "call_count", "error_count", "error_rate", "no_result_count", "rejected_count", "interrupted_count", "timeout_count", "truncated_count", "timed_count", "avg_duration_ms", "max_duration_ms", "result_tokens", "carried_tokens", "tool_cost_usd", "price_status"],
  mcp_calls: ["tool_name", "source_links", "called_at", "status", "arguments_json", "error_text", "query_source", "duration_ms", "result_count", "response_bytes", "estimated_output_tokens", "truncated"],
  skill_usages: ["channel", "machine_id", "execution_machine_id", "source_links", "skill_name", "skill_path", "evidence_type", "status", "content_bytes", "created_at", "tool_name", "title", "repository_name", "model_effort", "provider", "session_kind", "evidence_message_id", "body_message_id"],
  parallelism: ["channel", "machine_id", "execution_machine_id", "source_links", "unit_label", "started_at", "repository_name", "counting", "gap_minutes", "level", "repo_level", "unit_hours", "elapsed_hours", "repo_elapsed_hours", "parallel_elapsed_hours", "location", "provider", "model", "unit_id"],
};
const extraProperties: Partial<Record<UsageDataset, [string, string, "code" | "json" | "boolean" | "number"][]>> = {
  tool_calls: [["input_text", "Input excerpt", "code"], ["input_text_bytes", "Input bytes", "number"], ["input_text_truncated", "Input excerpt truncated", "boolean"], ["result_text", "Result excerpt", "code"], ["result_text_bytes", "Result bytes", "number"], ["result_text_truncated", "Result excerpt truncated", "boolean"], ["result_details", "Result metadata", "json"], ["commands", "Parsed commands", "json"], ["urls", "URLs and provenance", "json"], ["request", "Request context", "json"], ["next_request", "Following request", "json"]],
  writing_messages: [["text_truncated", "Retained preview truncated", "boolean"], ["segments", "Classified spans and sources", "json"]],
};
export function usageDetailsSchema(schema: FieldSchema, dataset: string): FieldSchema | undefined {
  if (!Object.hasOwn(usageDatasets, dataset)) return;
  const name = dataset as UsageDataset, defaults = usageDetailFields[name];
  const fields: FieldDef[] = schema.fields.map(field => ({ ...field, property: { ...field.property, default: defaults.includes(field.name), ...(field.name === "arguments_json" ? { render: "usage_json" } : field.name === "text" ? { render: "authored_text" } : ["command", "target", "skill_path", "file_path", "error_text", "error_signature", "search_query"].includes(field.name) ? { format: { kind: "code" as const } } : field.type === "datetime" ? { render: "usage_timestamp" } : {}) } }));
  fields.push({ name: "source_links", label: "Explore / source", type: "text", source: { kind: "derived", dependencies: [] }, render: "usage_sources", group: "Evidence", property: { default: defaults.includes("source_links") } });
  for (const [field, label, format] of extraProperties[name] ?? []) fields.push({ name: field, label, type: format === "boolean" ? "bool" : format === "number" ? "number" : "text", source: { kind: "backend" }, group: "Content", property: { default: defaults.includes(field), ...(["urls", "input_text", "result_text"].includes(field) ? {} : { format: { kind: format } }), ...(field === "urls" ? { render: "usage_tool_urls" } : ["input_text", "result_text"].includes(field) ? { render: "usage_payload" } : {}) } });
  if (name === "writing_messages") fields.push({ name: "classification_sources", label: "Matched source passages", type: "text", source: { kind: "derived", dependencies: ["segments"] }, render: "usage_authorship_sources", group: "Evidence", property: { default: true } });
  const rank = (field: FieldDef) => { const i = defaults.indexOf(field.name); return i < 0 ? defaults.length : i; };
  return { ...schema, fields: fields.sort((a, b) => rank(a) - rank(b)) };
}
