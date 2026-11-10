import { aggregate, type AnalysisCatalog, type AnalysisMeasure } from "./dashboard-analysis-model";
import { humanAnalysisCatalog } from "./human-analysis-catalog";
import { tokenAnalysisCatalog } from "./token-analysis-catalog";
import { conversationAnalysisCatalog } from "./conversation-analysis-catalog";
import { toolTimeAnalysisCatalog, skillsAnalysisCatalog } from "./record-analysis-catalogs";
import { compactionAnalysisCatalog } from "./compaction-analysis-catalog";
import { sessionShapeAnalysisCatalog } from "./session-shape-analysis-catalog";
import { agentEventsAnalysisCatalog } from "./agent-events-analysis-catalog";
import { agentEventRatesCatalog } from "./agent-event-rates-catalog";
import { messageLabelsCatalog } from "./message-labels-catalog";
import { carbonCatalogShape } from "./carbon-analysis-catalog";
import { habitsAnalysisCatalog } from "./habits-analysis-catalog";
import { parallelismCatalogShape } from "./parallelism-analysis-catalog";
import sessions from "../../schemas/session_shape.schema.json";

export type ScatterPoint = "conversation" | "work" | "agent_session" | "message";
export type ScatterMetric = { id: string; group: string; dataset: string; measure: AnalysisMeasure; keys: Partial<Record<ScatterPoint, string>>; time: AnalysisCatalog["time"]; params?: Record<string, string> };
const own = { conversation: "conversation_id", work: "workspace_id" };
const sessionKeys = { ...own, agent_session: "agent_session_id" };
const number = { kind: "number", decimals: 0 } as const;
const metric = (label: string, field: string, note: string, op: AnalysisMeasure["op"] = "sum"): AnalysisMeasure => ({ label, field, op, unit: "", format: number, additive: op === "sum", note });
const catalogs: Array<[string, string, AnalysisCatalog, Partial<Record<ScatterPoint, string>>, Record<string, string>?]> = [
  ["human", "Human Words", humanAnalysisCatalog, { ...sessionKeys, message: "id" }],
  ["tokens", "Tokens and Cost", tokenAnalysisCatalog, sessionKeys],
  ["activity", "Conversations", conversationAnalysisCatalog, own],
  ["calls", "Tool Calls and Time", toolTimeAnalysisCatalog, { ...sessionKeys, message: "call_message_id" }],
  ["skills", "Skills", skillsAnalysisCatalog, { ...sessionKeys, message: "evidence_message_id" }],
  ["compaction", "Compaction", compactionAnalysisCatalog, { ...sessionKeys, message: "message_id" }],
  ["session_shape", "Session Shape", sessionShapeAnalysisCatalog, sessionKeys],
  ["agent_experience", "Agent Experience", agentEventsAnalysisCatalog, { ...sessionKeys, message: "message_id" }],
  ["agent_event_rates", "Agent Events", agentEventRatesCatalog, { ...sessionKeys, message: "message_id" }],
  ["labels", "Message Labels", messageLabelsCatalog, { ...sessionKeys, message: "message_id" }],
  ["carbon", "Carbon Impact", carbonCatalogShape, sessionKeys],
  // Hourly sources identify top-level conversations. Keep that distinction
  // in the catalog notes instead of assigning hourly time to messages.
  ["work_hours", "Work Hours", habitsAnalysisCatalog, { conversation: "conversation_id" }],
  ["parallelism", "Parallelism", parallelismCatalogShape, { conversation: "conversation_id" }],
];
// Identical measures have one home. Tool Summary uses the call ledger's
// equivalent measures; it has no conversation identity of its own. MCP server
// requests cannot be attributed to a message, conversation or workspace.
const duplicates: Record<string, string[]> = { carbon: ["tokens"] };
export const scatterMetrics: ScatterMetric[] = catalogs.flatMap(([topic, group, catalog, keys, params]) => Object.entries(catalog.measures)
  .filter(([id]) => !duplicates[topic]?.includes(id))
  .map(([id, measure]) => ({ id: `${topic}_${id}`, group, dataset: catalog.dataset, measure, keys, time: topic === "session_shape" ? { first: "started_at", last: "last_message_at" } : catalog.time, params })));

// Rhythm apportions human and agent wall time separately. They overlap, so
// combining both actors would count the same clock time twice and pull the
// concurrency ratio toward one. Keep the two populations explicit here.
for (const item of scatterMetrics) if (["work_hours_wall_hours", "work_hours_active_hours", "work_hours_parallel_hours", "work_hours_at_once"].includes(item.id)) {
  item.measure = { ...item.measure, where: [...item.measure.where ?? [], { field: "actor", op: "!=", value: "you" }] };
}
scatterMetrics.push({ id: "work_hours_engaged_hours", group: "Work Hours", dataset: "rhythm", keys: { conversation: "conversation_id" }, time: habitsAnalysisCatalog.time,
  measure: { ...habitsAnalysisCatalog.measures.wall_hours, label: "Your engaged hours", where: [{ field: "actor", op: "=", value: "you" }],
    note: "Estimated engaged human time, shared among top-level conversations active at once. Human and agent hours can overlap and are measured separately." },
});

function extra(group: string, dataset: string, time: string, keys: ScatterMetric["keys"], fields: Array<[string, string, string, AnalysisMeasure["op"]?]>) {
  for (const [id, label, note, op] of fields) if (!scatterMetrics.some(item => item.id === `${dataset}_${id}`)) scatterMetrics.push({ id: `${dataset}_${id}`, group, dataset, time: { first: time, last: dataset === "session_shape" ? "last_message_at" : dataset === "usage" ? "last_usage_at" : time }, keys, measure: metric(label, id, note, op) });
}
extra("Human Words", "writing_messages", "sent_at", { ...sessionKeys, message: "id" }, [
  ["chars", "User message characters", "Characters in classified user messages; other message roles and unclassified text have no measurement."],
  ["pasted_words", "Pasted words", "Words classified as likely pasted text."],
  ["copied_words", "Copied or re-sent words", "Words matched to previous agent output or earlier user text."],
  ["prompt_words", "Template and attachment words", "Words in templates and attachment references."],
  ["machine_words", "App and automation words", "Words supplied by the app or automation."],
]);
extra("Tokens and Cost", "usage", "first_usage_at", sessionKeys, [
  ["uncached_input_tokens", "Uncached input tokens", "Input tokens excluding cache reads and writes."],
  ["cache_read_input_tokens", "Cache read tokens", "Recorded cache reads."],
  ["cache_creation_input_tokens", "Cache write tokens", "Recorded cache writes."],
  ["reasoning_output_tokens", "Reasoning tokens", "Reasoning output reported by the source; unavailable measurements remain missing."],
]);
extra("Waiting and Thinking", "model_requests", "requested_at", sessionKeys, [
  ["gap_seconds", "Response wait", "Mean recorded wait after tool results, in seconds; requests without timing are excluded.", "avg"],
  ["input_tokens", "Mean request input", "Mean input tokens per recorded model request.", "avg"],
  ["output_tokens", "Mean request output", "Mean output tokens per recorded model request.", "avg"],
]);
extra("Tool Calls and Time", "tool_calls", "started_at", { ...sessionKeys, message: "call_message_id" }, [
  ["lines_added", "Lines added", "Recorded lines added by tool calls."], ["lines_removed", "Lines removed", "Recorded lines removed by tool calls."],
  ["result_bytes", "Tool result bytes", "Recorded result size in bytes."], ["input_bytes", "Tool input bytes", "Recorded tool input size in bytes."],
]);
// Numeric session diagnostics are already maintained in the session schema.
// Offer the ones the guided dashboard has not yet put in its metric menu.
const sessionExtras: Array<[string, string, AnalysisMeasure["op"]?]> = [
  ["wall_seconds", "Wall time"], ["idle_seconds", "Idle time"], ["files_edited", "Files edited"], ["subagents", "Delegated agents"],
  ["turn_cycles", "Turn cycles"], ["first_response_seconds", "First response wait", "avg"], ["turn_cycle_p90_seconds", "Turn cycle p90", "avg"],
  ["max_unprompted_tool_calls", "Longest unprompted tool run", "max"], ["rejections", "Rejected calls"], ["interrupts", "Interrupts"],
  ["hook_blocks", "Hook blocks"], ["failure_streak_max", "Longest failure streak", "max"], ["identical_retries", "Identical retries"],
  ["thrash_retries", "Alternating retries"], ["churned_files", "Churned files"], ["mid_task_compactions", "Mid-task compactions"],
  ["context_peak_tokens", "Peak context tokens", "max"], ["corrections", "Human corrections"], ["verified_handbacks", "Verified handbacks"],
  ["unverified_handbacks", "Unverified handbacks"], ["ended_on_failure", "Sessions ending on failure"],
];
extra("Session Shape", "session_shape", "started_at", sessionKeys, sessionExtras.map(([field, label, op]) => [field, label, sessions.fields.find(item => item.name === field)?.description ?? `Recorded ${label.toLowerCase()} across agent sessions.`, op]));

const names: Record<string, string> = {
  activity_conversations: "Active conversations", activity_output: "Conversation output tokens",
  tokens_conversations: "Conversations with usage", tokens_root_conversations: "Top-level conversations with usage",
  work_hours_typed_words: "Typed words during activity hours", work_hours_messages: "Typed messages during activity hours", work_hours_conversations: "Conversations with activity hours", work_hours_at_once: "Agents at once during work hours", parallelism_agents: "Active conversations",
  work_hours_wall_hours: "Agent wall hours", work_hours_active_hours: "Active agent hours", work_hours_parallel_hours: "Hours with parallel agents",
  human_words: "User words", human_words_per_message: "Typed words per typed message", human_copied_share: "Messages with copied text (%)",
  tokens_cost: "API cost (USD)", tokens_priced_cost: "Fully priced API cost (USD)", tokens_cache_read_share: "Cache read share (%)",
  tokens_output_share: "Output token share (%)", calls_time: "Recorded tool time", calls_mean_time: "Mean tool call duration",
  calls_failure_rate: "Tool failure rate (%)", calls_truncation_rate: "Tool truncation rate (%)",
  agent_experience_events: "Agent events", labels_labels: "Distinct message labels", skills_records: "Skill evidence records",
  session_shape_sessions: "Agent sessions", session_shape_turn_cycle: "Mean session median turn cycle",
};
for (const item of scatterMetrics) {
  item.measure = { ...item.measure, label: names[item.id] ?? item.measure.label };
  if (item.measure.field?.endsWith("_seconds")) item.measure = { ...item.measure, unit: "seconds", format: { kind: "duration", sourceUnit: "seconds" } };
  // avg is an honest per-entity mean here; the outer scatter never averages
  // these values across entities or time periods.
}
export const scatterMetricDefinitions = Object.fromEntries(scatterMetrics.map(item => [item.id, {
  dataset: item.dataset, keys: item.keys, time: item.time, where: item.measure.where ?? [],
  summary: { id: "value", ...aggregate(item.measure), ...(item.measure.expression || item.measure.ratio ? { op: "formula" } : {}) },
  params: item.params,
}]));

// Prefer the metric's name to incidental words in its explanation. Keep each
// dashboard together so visual order and keyboard navigation remain identical.
export function scatterMetricOptions(catalog: AnalysisCatalog, unit: string, other: string, search: string): Array<[string, AnalysisMeasure]> {
  const query = search.trim().toLocaleLowerCase();
  const ranked = Object.entries(catalog.measures).filter(([key, item]) => key !== other && (!item.units || item.units.includes(unit)))
    .map(([key, item]) => {
      const name = item.label.toLocaleLowerCase(), group = (item.group ?? "Metrics").toLocaleLowerCase();
      const rank = !query || name.startsWith(query) ? 0 : name.includes(query) ? 1 : group.includes(query) ? 2 : item.note.toLocaleLowerCase().includes(query) ? 3 : 4;
      return { key, item, rank };
    }).filter(item => item.rank < 4).sort((a, b) => a.rank - b.rank);
  const groups = [...new Set(ranked.map(({ item }) => item.group ?? "Metrics"))];
  return groups.flatMap(group => ranked.filter(({ item }) => (item.group ?? "Metrics") === group).map(({ key, item }): [string, AnalysisMeasure] => [key, item]));
}
