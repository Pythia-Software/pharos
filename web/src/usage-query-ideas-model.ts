import { fitQueryToScreen } from "./query-fit";
import { EMPTY_QUERY, ITEM_DETAILS_COLUMN, applyPatch, groupKeyFields, matchPatch, type AggregationClause, type QueryState, type WhereClause, type WhereTerm, type SummaryColumn } from "@pythia-software/query-table-core";
import { usageGroupPresets, usageComparisonPresets, usageTablePresetPatch, type UsageTablePreset } from "./usage-queries";
import type { UsageDataset } from "./usage-workspace-model";

export type GroupedQueryIdea = UsageTablePreset & { id: string; section: string; metrics?: AggregationClause[]; where?: WhereTerm[] };
export type RecordQueryIdea = { id: string; section: string; label: string; title: string; query: QueryState };
export type QueryIdea = GroupedQueryIdea | RecordQueryIdea;
const sum = (field: string, label: string): SummaryColumn => ({ id: `idea_${field}`, op: "sum", field, label });
const count = (id: string, label: string, field?: string): SummaryColumn => ({ id, op: "count", ...(field ? { field } : {}), label });
const distinct = (field: string, label: string): SummaryColumn => ({ id: `idea_${field}_count`, op: "count_distinct", field, label });
const ratio = (id: string, expression: string, label: string, percent = false): SummaryColumn => ({ id, op: "formula", expression, label, display: { kind: percent ? "percent" : "number", decimals: 1 } });
const duration = (field: string, label: string): SummaryColumn => ({ ...sum(field, label), display: { kind: "duration", sourceUnit: "milliseconds" } });
const conversation: SummaryColumn = { id: "idea_conversation", op: "link", label: "Conversation", link: { label: "Open conversation", to: { kind: "template", template: "/conversation/{field:conversation_id}" } } };
const work: SummaryColumn = { id: "idea_work", op: "link", label: "Workspace", link: { label: "Open workspace", to: { kind: "template", template: "/work/{field:id}" } } };

// Grouped ideas use native Query Table drilldown. Records links inherit each
// idea's source WHERE and exact group-key predicates, including null keys.
function finish(dataset: UsageDataset, idea: GroupedQueryIdea): GroupedQueryIdea {
  const summaries = idea.grouping.summaries;
  const links = summaries.filter(column => column.op === "link");
  const records: SummaryColumn = { id: "idea_records", op: "link", label: "Drill In", link: { label: "Matching records", to: { kind: "query", target: dataset } } };
  const ordered = [...summaries.filter(column => column.op !== "link"), ...links, records];
  const columns = [...(idea.columns ?? [...groupKeyFields(idea.grouping.keys), ...summaries.map(column => column.id)]).filter(field => !links.some(link => link.id === field)), ...links.map(link => link.id), records.id];
  return { ...idea, grouping: { ...idea.grouping, summaries: ordered }, columns };
}
function idea(dataset: UsageDataset, id: string, section: string, label: string, title: string, keys: string[], summaries: SummaryColumn[], sort = summaries.find(column => column.op !== "link")?.id, chronological = false): GroupedQueryIdea {
  const primary = summaries.find(column => column.id === sort && column.op !== "link");
  return finish(dataset, { id: `${dataset}:${id}`, section, label, title,
    grouping: { keys, summaries, representative: null },
    orderBy: sort ? [{ field: chronological ? keys[0] : sort, dir: "desc", nulls: "last" }] : [],
    metrics: primary ? [{ id: `preview_${id}`, op: "sum", field: primary.id, groupBy: keys, label: primary.label, display: { kind: chronological ? "bar-vertical" : "list", maxGroups: 12, ...(primary.display?.kind === "percent" ? { format: { kind: "percent", decimals: 1 } } : primary.display?.kind === "duration" ? { format: { kind: "duration", sourceUnit: "milliseconds" } } : {}) } }] : [],
  });
}
const written = [sum("typed_words", "Your words"), sum("pasted_words", "Likely pasted words"), sum("user_words", "All user-turn words"), sum("typed_turns", "Turns with your writing"), count("idea_works", "Workspaces"), ratio("idea_words_per_turn", "SUM([typed_words])/NULLIF(SUM([typed_turns]),0)", "Words per written turn"), ratio("idea_authored_share", "SUM([typed_words])/NULLIF(SUM([user_words]),0)", "Human-written word share", true)];
const messages = [sum("typed_words", "Your words"), sum("pasted_words", "Likely pasted words"), sum("words", "All words"), count("idea_messages", "Messages"), ratio("idea_written_messages", "SUM(IF([typed_words]>0,1,0))", "Messages with your writing"), distinct("conversation_id", "Conversations"), ratio("idea_words_per_message", "SUM([typed_words])/NULLIF(SUM(IF([typed_words]>0,1,0)),0)", "Words per written message"), ratio("idea_authored_share", "SUM([typed_words])/NULLIF(SUM([words]),0)", "Human-written word share", true)];
const calls = [count("idea_calls", "Calls"), sum("error_count", "Failed calls"), ratio("idea_failure_rate", "SUM([error_count])/NULLIF(COUNT(),0)", "Failure rate", true), duration("duration_ms", "Recorded tool time"), count("idea_timed", "Timed calls", "duration_ms"), { id: "idea_mean_duration", op: "avg" as const, field: "duration_ms", label: "Time per timed run", display: { kind: "duration" as const, sourceUnit: "milliseconds" as const } }];
const toolTotals = [sum("call_count", "Calls"), sum("error_count", "Failed calls"), ratio("idea_failure_rate", "SUM([error_count])/NULLIF(SUM([call_count]),0)", "Failure rate", true), duration("total_duration_ms", "Recorded tool time"), sum("timed_count", "Timed calls"), { ...ratio("idea_mean_duration", "SUM([total_duration_ms])/NULLIF(SUM([timed_count]),0)", "Time per timed run"), display: { kind: "duration" as const, sourceUnit: "milliseconds" as const } }];
const context = [sum("result_tokens", "Context added (tokens)"), sum("carried_tokens", "Context carried (tokens)")];
const activity = [sum("human_messages", "Human messages"), sum("output_tokens", "Recorded output tokens"), distinct("conversation_id", "Conversations")];
const skills = [count("idea_evidence", "Evidence records"), distinct("conversation_id", "Conversations"), ratio("idea_body_bytes", "SUM(IF([content_bytes]>0,[content_bytes],0))", "Retained body bytes")];
const mcp = [sum("call_count", "Calls"), sum("error_count", "Failed calls"), ratio("idea_failure_rate", "SUM([error_count])/NULLIF(SUM([call_count]),0)", "Failure rate", true), sum("estimated_output_tokens", "Estimated output tokens"), sum("truncated_count", "Truncated responses"), duration("duration_ms", "Server time")];
// Counting method and turn-gap assumptions must stay separate. Unit hours are
// additive; elapsed occupancy is repeated across units and must not be summed.
// Rhythm: wall hours add up within one actor; messages and turns per actor.
const rhythm = [sum("elapsed_hours", "Wall hours"), sum("active_hours", "Active hours"), sum("messages", "Typed messages"), sum("turns", "Turns started"), distinct("unit_id", "Conversations")];
const occupancy = [sum("unit_hours", "Summed activity hours"), distinct("unit_id", "Units"), { id: "idea_peak", op: "max" as const, field: "level", label: "Peak recorded concurrency" }];

// Agent events: each row is one event; agent_event_messages: one row per message read.
const agentEvents: SummaryColumn[] = [count("idea_events", "Events"), distinct("conversation_id", "Conversations"), distinct("message_id", "Messages")];
// agent_event_rates: one row per message read and kind, so its populations
// are distinct messages, turns and conversations.
const agentEventRates: SummaryColumn[] = [distinct("message_id", "Messages read"), sum("events", "Events"),
  ratio("idea_events_per_thousand", "1000*SUM([events])/NULLIF(COUNT_DISTINCT([message_id]),0)", "Events per 1,000 messages read"),
  ratio("idea_message_share", "COUNT_DISTINCT(IF([events]>0,[message_id],NULL))/NULLIF(COUNT_DISTINCT([message_id]),0)", "Share of messages with an event", true)];
// Session shape: one row per agent session; rates pool their parts.
const sessionShape: SummaryColumn[] = [count("idea_sessions", "Sessions"), sum("forensic_events", "Forensic events"), ratio("idea_failure_rate", "SUM([failed_calls])/NULLIF(SUM([tool_calls]),0)", "Failure rate", true), ratio("idea_steering", "SUM([corrections])/NULLIF(SUM([typed_human_messages]),0)", "Steering load", true)];
const agentEventMessages: SummaryColumn[] = [sum("message_count", "Messages read"), sum("candidates", "Keyword hits"), sum("events", "Events"), ratio("idea_events_per_message", "SUM([events])/NULLIF(SUM([message_count]),0)", "Events per message")];

// Outcomes: one row per Library workspace; rates pooled over the group's lines
// and workspaces. Kept share and cost per kept change read judged workspaces
// only, and cost per kept change only priced ones (outcomes_table.go).
const outcomes: SummaryColumn[] = [count("idea_sessions", "Workspaces"), sum("cost_usd", "API equivalent"), sum("kept_change", "Kept changes"),
  ratio("idea_kept_share", "SUM([kept_change])/NULLIF(SUM([judged]),0)", "Kept share", true),
  ratio("idea_cost_per_kept", "SUM([judged_cost_usd])/NULLIF(SUM([kept_priced]),0)", "Cost per kept change"), ratio("idea_rework_rate", "SUM([rework_removed_lines])/NULLIF(SUM([kept_lines]),0)", "Rework rate", true)];

// Spend by branch: rows are slices of a session's usage, so sessions are distinct.
const branchSpend: SummaryColumn[] = [sum("cost_usd", "API equivalent"), sum("tokens", "Tokens"), distinct("workspace_id", "Sessions")];

export const usageQueryIdeas: Record<UsageDataset, QueryIdea[]> = {
  scatter: [],
  messages: [], people_message_labels:[], message_labels:[],
  compactions: [idea("compactions", "triggers", "Compare", "Compactions by trigger and service", "Recorded boundary events by trigger and service", ["trigger", "provider"], [count("idea_compactions", "Compactions")])],
  model_requests: [idea("model_requests","models","Compare","Timing coverage by model","Retained requests and usable post-tool gaps by model",["model"],[count("idea_requests","Retained requests"),count("idea_timed","Usable gaps","gap_seconds")])],
  tl1_attempts: [],
  usage: [...usageGroupPresets.map(preset => ({ preset, section: "Group by" })), ...usageComparisonPresets.map(preset => ({ preset, section: "Compare" }))].map(({ preset, section }) => finish("usage", { ...preset, id: `usage:${preset.label}`, section })),
  writing: [
    idea("writing", "repos", "Compare", "By project", "Compare workspace writing totals and weighted words per written turn by project", ["project_name"], written),
    idea("writing", "apps", "Compare", "By source", "Compare writing and supplied text across sources", ["source_kind"], written),
    idea("writing", "providers", "Compare", "By provider set", "Keep recorded provider combinations separate when comparing workspace totals", ["providers"], written),
    idea("writing", "works", "Explore", "Workspaces with the most writing", "Rank workspaces by your writing and open their conversation history", ["id"], [{ id: "idea_title", op: "max", field: "title", label: "Workspace" }, ...written, work], "idea_typed_words"),
    idea("writing", "coverage", "Compare", "Price coverage", "Compare authored text, tokens and API-equivalent cost across pricing statuses", ["price_status"], [...written.slice(0, 4), sum("total_tokens", "Tokens"), sum("cost_usd", "API equivalent")]),
  ],
  writing_messages: [
    idea("writing_messages", "repos", "Compare", "Writing by project", "Compare message-date writing, message counts and weighted authorship shares", ["project_name"], messages),
    idea("writing_messages", "weeks", "Over time", "Weekly writing", "Typed, pasted and all supplied words by message week", ["week"], messages, "idea_typed_words", true),
    idea("writing_messages", "categories", "Compare", "Text sources", "Compare main authorship categories with their word totals and message counts", ["main_category"], messages),
    idea("writing_messages", "threads", "Explore", "Conversation writing", "Compare your words per conversation and open the exact matching messages", ["conversation_id"], [...messages, conversation]),
    idea("writing_messages", "apps", "Compare", "Source and provider", "Compare human writing by source and provider", ["source_kind", "provider"], messages),
  ],
  conversation_activity: [
    idea("conversation_activity", "repos", "Compare", "Messages and output by project", "Compare human messages and recorded output without treating the ratio as quality", ["project_name"], activity),
    idea("conversation_activity", "models", "Compare", "Top-level model and effort", "Compare activity attributed to each top-level model and effort", ["root_provider", "root_model_family", "root_effort"], activity),
    idea("conversation_activity", "events", "Compare", "Event kinds", "Inspect where human-message and output observations enter the activity records", ["event_kind"], activity),
    idea("conversation_activity", "threads", "Explore", "By conversation", "Open conversations contributing human messages and output in the selected dates", ["conversation_id"], [...activity, conversation]),
  ],
  tool_calls: [
    idea("tool_calls", "commands", "Time and reliability", "Command families", "Compare parsed command families using total time, timed-run means and failure rates", ["operation_key", "operation_name"], calls, "idea_duration_ms"),
    idea("tool_calls", "tools", "Time and reliability", "Tools and failures", "Compare call counts and weighted failure rates by named tool", ["tool_name"], calls),
    idea("tool_calls", "errors", "Time and reliability", "Failure kinds", "Compare statuses and retained error signatures; inspect the underlying calls", ["status", "error_type"], calls, "idea_error_count"),
    idea("tool_calls", "context", "Context", "Context by command", "Compare context added and carried by command family; token values may be estimated", ["operation_key", "operation_name"], [...context, ...calls]),
    idea("tool_calls", "weeks", "Over time", "Weekly tool time", "Compare recorded tool duration and timing coverage per week", ["week"], calls, "idea_duration_ms", true),
    idea("tool_calls", "threads", "Explore", "Calls by conversation", "Compare conversation call totals, then inspect matching invocations or the conversation", ["conversation_id"], [...calls, conversation]),
  ],
  tools: [
    idea("tools", "tools", "Compare", "Tools and failures", "Compare rollups using calls as the failure-rate denominator", ["tool_name"], toolTotals),
    idea("tools", "commands", "Compare", "Shell commands", "Compare programs and subcommands using weighted timed-run averages", ["program", "subcommand"], toolTotals, "idea_total_duration_ms"),
    idea("tools", "mcp", "Compare", "External MCP methods", "Compare captured external MCP servers and methods, including unknown values", ["mcp_server", "mcp_method"], toolTotals),
    idea("tools", "context", "Context", "Context by project", "Compare context volume added and carried alongside call counts", ["project_name"], [...context, ...toolTotals]),
    idea("tools", "interruptions", "Compare", "Interruptions and truncation", "Compare timeouts, rejections, interruptions and truncated results by tool", ["tool_name"], [sum("timeout_count", "Timeouts"), sum("rejected_count", "Rejected calls"), sum("interrupted_count", "Interrupted calls"), sum("truncated_count", "Truncated results"), ...toolTotals]),
    idea("tools", "weeks", "Over time", "Weekly summaries", "Calls, failures and recorded time per week across retained rollups", ["week"], toolTotals, "idea_call_count", true),
  ],
  skill_usages: [
    idea("skill_usages", "skills", "Compare", "Skills and evidence", "Compare observed evidence volume, conversations and retained body sizes by skill", ["skill_name"], skills),
    idea("skill_usages", "types", "Compare", "Evidence types", "Distinguish invocations, file loads and app-reported loads for each skill", ["skill_name", "evidence_type"], skills),
    idea("skill_usages", "repos", "Compare", "Skills by project", "Compare skill evidence across projects without treating a load as compliance", ["project_name", "skill_name"], skills),
    idea("skill_usages", "status", "Compare", "Evidence status", "Compare retained skill observations by status and evidence type", ["status", "evidence_type"], skills),
    idea("skill_usages", "threads", "Explore", "Evidence by conversation", "Open matching evidence and its source messages or conversation", ["conversation_id"], [...skills, conversation]),
  ],
  mcp_calls: [
    idea("mcp_calls", "tools", "Compare", "Pharos tools", "Compare requests served by Pharos, failures, output estimates and truncation", ["tool_name"], mcp),
    idea("mcp_calls", "days", "Over time", "Daily requests", "Calls and server overhead per day within the latest 5,000 retained requests", ["day"], mcp, "idea_call_count", true),
    idea("mcp_calls", "failures", "Compare", "Failures by tool", "Compare Pharos request status by tool; source records retain sanitized arguments", ["tool_name", "status"], mcp, "idea_error_count"),
    idea("mcp_calls", "output", "Compare", "Response size and truncation", "Compare estimated output, bytes and truncated responses for each Pharos tool", ["tool_name"], [sum("estimated_output_tokens", "Estimated output tokens"), sum("response_bytes", "Response bytes"), ...mcp.filter(column => column.field !== "estimated_output_tokens")]),
    idea("mcp_calls", "sources", "Compare", "Requested archive sources", "Compare explicit source filters from retained Pharos request arguments", ["query_source"], mcp),
  ],
  rhythm: [
    idea("rhythm", "actors", "Compare", "Time by who is active", "Compare your engaged hours with your agents' time with you, unattended and automated", ["actor"], rhythm),
    idea("rhythm", "weeks", "Over time", "Weekly rhythm", "Wall hours per week for you and each kind of agent time", ["week", "actor"], rhythm, "idea_elapsed_hours", true),
    idea("rhythm", "automations", "Compare", "Automations", "Turns and agent hours each schedule, script or orchestration started", ["initiator", "automation"], rhythm, "idea_turns"),
  ],
  parallelism: [
    idea("parallelism", "repos", "Compare", "Activity by project", "Compare additive unit hours, keeping counting and gap assumptions separate", ["counting", "gap_minutes", "project_name"], occupancy),
    idea("parallelism", "weeks", "Over time", "Weekly activity", "Summed activity hours per week with each counting method and gap separate", ["week", "counting", "gap_minutes"], occupancy, "idea_unit_hours", true),
    idea("parallelism", "levels", "Compare", "Concurrency bands", "Compare unit-hours by concurrency band without summing repeated elapsed time", ["counting", "gap_minutes", "level_band"], occupancy),
    idea("parallelism", "threads", "Explore", "Conversation activity", "Inspect hourly occupancy records and open their source conversation when recorded", ["counting", "gap_minutes", "conversation_id"], [...occupancy, conversation]),
  ],
  agent_events: [
    idea("agent_events", "kinds", "Compare", "Events by kind", "Compare how often each kind of event occurs, and in how many conversations", ["family"], agentEvents),
    idea("agent_events", "weeks", "Over time", "Weekly events", "Events per week by kind group", ["week", "family_group"], agentEvents, "idea_events", true),
    idea("agent_events", "repos", "Compare", "Events by project", "Compare events and affected conversations across projects", ["project_name"], agentEvents),
    idea("agent_events", "threads", "Explore", "Conversations with the most events", "Rank conversations by their events and open them in the reader", ["conversation_id"], [...agentEvents, conversation]),
  ],
  agent_event_rates: [
    idea("agent_event_rates", "kinds", "Compare", "Rates by kind", "Events per 1,000 messages read and the share of messages with each kind", ["family"], agentEventRates),
    idea("agent_event_rates", "prompted", "Compare", "Rates by who prompted", "Compare event rates in turns a person, an automation or a delegating agent prompted", ["prompted_by"], agentEventRates),
    idea("agent_event_rates", "models", "Compare", "Rates by model and source", "Compare event rates by model, keeping replies and thinking apart", ["model", "source"], agentEventRates),
  ],
  session_shape: [
    idea("session_shape", "archetypes", "Compare", "Sessions by archetype", "Compare sessions, forensic events, failure rate and steering load across archetypes", ["archetype"], sessionShape),
    idea("session_shape", "weeks", "Over time", "Weekly session length", "Sessions per week in each length band", ["week", "length_band"], sessionShape, "idea_sessions", true),
    idea("session_shape", "threads", "Explore", "Sessions with the most forensic events", "Rank sessions by their forensic events and open them in the reader", ["conversation_id"], [...sessionShape, conversation], "idea_forensic_events"),
  ],
  agent_event_messages: [
    idea("agent_event_messages", "repos", "Compare", "Messages read by project", "Compare messages read, keyword hits and events across projects", ["project_name"], agentEventMessages),
    idea("agent_event_messages", "sources", "Compare", "Replies and thinking", "Compare events per message in visible replies and thinking", ["source"], agentEventMessages),
  ],
  branch_usage: [
    idea("branch_usage", "branches", "Compare", "By branch", "Compare spend and sessions across branches, each within its project", ["project_branch"], branchSpend),
    idea("branch_usage", "coverage", "Compare", "Coverage", "How much spend has a recorded branch, an unknown one, or no branch data", ["coverage"], branchSpend),
    idea("branch_usage", "repos", "Compare", "By project and branch", "Compare spend across each project's branches", ["project_name", "branch"], branchSpend),
  ],
  outcomes: [
    idea("outcomes", "labels", "Compare", "By outcome", "Compare workspaces and cost by what became of their changes", ["outcome_label"], outcomes),
    idea("outcomes", "repos", "Compare", "By project", "Compare cost per kept change and rework rate across projects", ["project_name"], outcomes),
    idea("outcomes", "models", "Compare", "By main model", "Compare cost per kept change and rework rate across workspaces' main models", ["model_family"], outcomes),
    idea("outcomes", "tiers", "Compare", "By evidence tier", "Count workspaces by the strongest evidence their outcome rests on", ["evidence_tier"], outcomes),
  ],
};

export function queryIdeaPatch(idea: GroupedQueryIdea) {
  const patch = usageTablePresetPatch(idea);
  return { ...patch, id: idea.id, owns: [...patch.owns, "filters" as const, "metrics" as const], query: { ...patch.query, grouping: { ...patch.query.grouping!, source: { ...EMPTY_QUERY, where: idea.where ?? [] } }, aggregations: idea.metrics ?? [] } };
}
// Ideas are independently reproducible views. Previous WHERE, HAVING, keyword
// clauses, metrics and pagination must not leak into the next starting point.
export function applyQueryIdea(_query: QueryState, idea: QueryIdea): QueryState {
  const query = "query" in idea ? idea.query : applyPatch(EMPTY_QUERY, queryIdeaPatch(idea));
  return fitQueryToScreen({ ...query, offset: 0, hostState: query.hostState ?? { version: 1, params: idea.id.startsWith("message_labels:") ? { label_results: ["table"] } : {} } });
}
export function queryIdeaMatches(query: QueryState, idea: QueryIdea): boolean {
  const expected = applyQueryIdea(EMPTY_QUERY, idea);
  const dataFields = (value: QueryState) => value.select.map(column => column.field).filter(field => field !== ITEM_DETAILS_COLUMN && !field.startsWith("usage_drill_"));
  if ("query" in idea) {
    return !query.grouping && JSON.stringify(query.where) === JSON.stringify(expected.where)
      && JSON.stringify(dataFields(query)) === JSON.stringify(dataFields(expected))
      && JSON.stringify(query.orderBy) === JSON.stringify(expected.orderBy)
      && JSON.stringify(query.aggregations) === JSON.stringify(expected.aggregations)
      && JSON.stringify(query.hostState?.params ?? {}) === JSON.stringify(expected.hostState?.params ?? {});
  }
  return Boolean(query.grouping && matchPatch({ ...query, where: [] }, { ...queryIdeaPatch(idea), owns: ["grouping"] }).status === "active"
    && JSON.stringify(query.grouping.source.where) === JSON.stringify(idea.where ?? [])
    && JSON.stringify(query.hostState?.params ?? {}) === JSON.stringify(expected.hostState?.params ?? {}));
}
export function queryIdeasPreferenceKey(dataset: UsageDataset): string { return `pharos-query-ideas:${dataset}`; }
export function queryIdeasOpen(saved: unknown): boolean { return typeof saved === "boolean" ? saved : true; }

// Retain useful diagnostics from the older report shortcuts, now as editable
// grouped result tables with metrics and explicit source drill-downs.
usageQueryIdeas.tool_calls.push(
  idea("tool_calls", "signatures", "Time and reliability", "Recurring error signatures", "Compare retained error signatures and the conversations where they recur; unknown signatures stay visible", ["error_signature", "error_type"], [...calls, distinct("conversation_id", "Conversations")], "idea_error_count"),
  idea("tool_calls", "slow-calls", "Explore", "Slowest calls", "Rank individual calls by recorded duration and inspect their source invocation", ["conversation_id", "id"], [duration("duration_ms", "Recorded duration"), { id: "idea_command", op: "max", field: "command", label: "Command" }, conversation]),
  idea("tool_calls", "large-results", "Context", "Largest results", "Rank individual results by the context they added; tokens may be estimated", ["conversation_id", "id"], [sum("result_tokens", "Context added (tokens)"), sum("carried_tokens", "Context carried (tokens)"), conversation]),
  idea("tool_calls", "carried-results", "Context", "Most carried results", "Find results counted again in later request contexts", ["conversation_id", "id"], [sum("carried_tokens", "Context carried (tokens)"), sum("result_tokens", "Context added (tokens)"), conversation]),
  idea("tool_calls", "sites", "Explore", "Sites and tools", "Compare reached sites by calling tool, with links to retained invocations", ["host", "tool_name"], calls),
);
usageQueryIdeas.tools.push(
  idea("tools", "models", "Compare", "Reliability by model", "Compare failures and weighted failure rates with call counts by model", ["provider", "model_family"], toolTotals, "idea_error_count"),
  idea("tools", "categories", "Compare", "Command categories", "Compare test, build, search and other parsed command categories", ["command_category"], toolTotals, "idea_total_duration_ms"),
  idea("tools", "costs", "Context", "Cost by tool", "Compare API-equivalent context and tool costs alongside pricing coverage", ["tool_name", "price_status"], [sum("tool_cost_usd", "Tool API equivalent"), sum("context_cost_usd", "Context API equivalent"), ...context, ...toolTotals]),
  idea("tools", "agents", "Compare", "Main agents and sub-agents", "Compare call mix, failure rates and recorded durations by agent kind", ["session_kind", "tool_category"], toolTotals),
);

function withExample(dataset: UsageDataset, current: GroupedQueryIdea, sort: string, message: string, label: string, call?: string): QueryIdea {
  const helper = (field: string): SummaryColumn => ({ id: `example_${field}`, op: "row", field, label: field });
  const fields = ["conversation_id", message, ...(call ? [call] : [])];
  const link: SummaryColumn = { id: "idea_example", op: "link", label: "Source message", link: { label, to: { kind: "template", template: `/conversation/{field:example_conversation_id}?message={field:example_${message}}${call ? `&call={field:example_${call}}` : ""}` } } };
  return finish(dataset, { ...current,
    grouping: { ...current.grouping, summaries: [...current.grouping.summaries.filter(column => column.id !== "idea_records"), ...fields.map(helper), link], representative: { orderBy: [{ field: sort, dir: "desc", nulls: "last" }, { field: "id", dir: "asc", nulls: "last" }] } },
    columns: [...current.columns!.filter(field => field !== "idea_records"), link.id],
  });
}
usageQueryIdeas.writing_messages = usageQueryIdeas.writing_messages.map(current => "grouping" in current && current.id === "writing_messages:threads" ? withExample("writing_messages", current, "sent_at", "id", "Latest message") : current);
usageQueryIdeas.tool_calls = usageQueryIdeas.tool_calls.map(current => "grouping" in current && current.id === "tool_calls:commands" ? withExample("tool_calls", current, "duration_ms", "call_message_id", "Inspect call", "call_id") : current);
usageQueryIdeas.skill_usages = usageQueryIdeas.skill_usages.map(current => "grouping" in current && current.id === "skill_usages:types" ? withExample("skill_usages", current, "created_at", "evidence_message_id", "Latest evidence") : current);

const tokenTotals = usageGroupPresets[0].grouping.summaries;
usageQueryIdeas.usage.push(
  idea("usage", "cache-churn", "Compare", "Cache churn", "Compare weighted cache reuse and cache writes per read, with the underlying token totals", ["provider", "model_family"], [
    ...tokenTotals, sum("cache_creation_input_tokens", "Cache writes"), sum("cache_read_input_tokens", "Cache reads"),
    ratio("idea_write_per_read", "SUM([cache_creation_input_tokens])/NULLIF(SUM([cache_read_input_tokens]),0)", "Writes per cached read"),
  ], "idea_write_per_read"),
  idea("usage", "delegation", "Compare", "Delegation by top-level model", "Compare root and delegated run roles under their top-level provider and model", ["root_provider", "root_model_family", "run_role"], tokenTotals, "usage_cost_usd"),
  { ...idea("usage", "root-tasks", "Explore", "Top-level conversations", "Compare total usage across each top-level conversation and its delegated work, then inspect contributing facts or the top-level conversation", ["root_conversation_id"], [
    { id: "idea_root_title", op: "max", field: "root_title", label: "Top-level conversation" }, ...tokenTotals, distinct("delegated_session_id", "Delegated agent sessions"),
    { id: "idea_root_thread", op: "link", label: "Top-level conversation", link: { label: "Open top-level conversation", to: { kind: "template", template: "/conversation/{field:root_conversation_id}" } } },
  ], "usage_cost_usd"), metrics: [] },
);

// Record explorations favor a compact table over another chart of opaque IDs.
function recordIdea(dataset: UsageDataset, id: string, label: string, title: string, keys: string[], summaries: SummaryColumn[], sort: string): GroupedQueryIdea {
  return { ...idea(dataset, id, "Explore", label, title, keys, summaries, sort), metrics: [] };
}
const workTitle: SummaryColumn = { id: "idea_title", op: "max", field: "title", label: "Workspace" };
const message: SummaryColumn = { id: "idea_message", op: "link", label: "Message", link: { label: "Read message", to: { kind: "template", template: "/conversation/{field:conversation_id}?message={field:id}" } } };
const rootThread: SummaryColumn = { id: "idea_root_thread", op: "link", label: "Top-level conversation", link: { label: "Open top-level conversation", to: { kind: "template", template: "/conversation/{field:root_conversation_id}" } } };
usageQueryIdeas.usage.push(
  recordIdea("usage", "sessions", "Agent sessions", "Inspect each agent session's usage within its conversation, including unpriced token facts", ["agent_session_id", "conversation_id"], [workTitle, ...tokenTotals, conversation], "usage_cost_usd"),
  recordIdea("usage", "conversation-cost", "Costliest conversations", "Rank individual conversations by recorded API price equivalent; unpriced usage remains visible in token totals", ["conversation_id"], [workTitle, ...tokenTotals, conversation], "usage_cost_usd"),
);
usageQueryIdeas.writing.push(
  recordIdea("writing", "hands-on", "Most hands-on", "Rank workspaces by turns containing your writing and open their conversation history; totals cover the whole matching workspace", ["id"], [workTitle, ...written, work], "idea_typed_turns"),
  recordIdea("writing", "pasted-context", "Most pasted context", "Inspect workspaces containing the most likely-pasted words, alongside your writing and all supplied text", ["id"], [workTitle, ...written, work], "idea_pasted_words"),
  recordIdea("writing", "copied-text", "Most copied text", "Inspect work with the most copied or re-sent words, keeping those counts separate from typed and pasted text", ["id"], [workTitle, sum("copied_words", "Copied or re-sent words"), ...written, work], "idea_copied_words"),
);
usageQueryIdeas.writing_messages.push(
  recordIdea("writing_messages", "long-messages", "Longest messages", "Read individual messages ranked by typed/dictated words, with pasted words and authorship shares alongside", ["conversation_id", "id"], [workTitle, ...messages, message], "idea_typed_words"),
  recordIdea("writing_messages", "pasted-messages", "Most pasted messages", "Read the individual messages with the most likely-pasted words and inspect their classified source text", ["conversation_id", "id"], [workTitle, ...messages, message], "idea_pasted_words"),
);
usageQueryIdeas.conversation_activity.push(
  recordIdea("conversation_activity", "root-threads", "Top-level conversations", "Explore human messages and recorded output across top-level conversations and their delegated activity", ["root_conversation_id"], [{ id: "idea_root_title", op: "max", field: "root_title", label: "Top-level conversation" }, ...activity, rootThread], "idea_output_tokens"),
  withExample("conversation_activity", recordIdea("conversation_activity", "recent-events", "Latest activity events", "Inspect recent human-message and usage observations; message links are available only when the event records a message ID", ["event_kind", "conversation_id", "id"], [{ id: "idea_event_at", op: "max", field: "event_at", label: "Observed at" }, ...activity, conversation], "idea_event_at"), "event_at", "message_id", "Source message"),
);
usageQueryIdeas.tool_calls.push(
  idea("tool_calls", "repeated-operations", "Explore", "Repeated operations", "Find command families repeated within the same conversation, then inspect their exact invocations and outcomes", ["conversation_id", "operation_key", "operation_name"], [...calls, conversation], "idea_calls"),
);
usageQueryIdeas.tools.push(
  idea("tools", "longest-runs", "Explore", "Longest recorded runs", "Find tools and commands containing the longest single recorded run, with timed counts and weighted means for context", ["tool_name", "command_name"], [{ id: "idea_longest_run", op: "max", field: "max_duration_ms", label: "Longest recorded run", display: { kind: "duration", sourceUnit: "milliseconds" } }, ...toolTotals], "idea_longest_run"),
  idea("tools", "context-commands", "Explore", "Context-heavy commands", "Inspect commands whose results add or carry the most context, keeping these token estimates separate", ["tool_name", "command_name"], [...context, ...toolTotals], "idea_carried_tokens"),
);
usageQueryIdeas.skill_usages.push(
  withExample("skill_usages", recordIdea("skill_usages", "latest-evidence", "Latest evidence", "Open the newest retained skill observations and their exact evidence messages; an observation does not prove a skill was followed", ["id"], [{ id: "idea_created_at", op: "max", field: "created_at", label: "Observed at" }, { id: "idea_skill", op: "max", field: "skill_name", label: "Skill" }, ...skills], "idea_created_at"), "created_at", "evidence_message_id", "Read evidence"),
  withExample("skill_usages", recordIdea("skill_usages", "large-loads", "Large skill loads", "Inspect the largest retained skill bodies and open the recorded body message when available; missing or nonpositive sizes do not add bytes", ["id"], [{ id: "idea_skill", op: "max", field: "skill_name", label: "Skill" }, ...skills], "idea_body_bytes"), "content_bytes", "body_message_id", "Read retained body"),
);
usageQueryIdeas.mcp_calls.push(
  recordIdea("mcp_calls", "slow-requests", "Slowest requests", "Inspect individual Pharos server requests ranked by recorded duration; the log retains at most 5,000 requests", ["id", "tool_name"], mcp, "idea_duration_ms"),
  recordIdea("mcp_calls", "large-responses", "Largest responses", "Inspect individual Pharos requests ranked by estimated response tokens, with response bytes and truncation counts; response text is not retained", ["id", "tool_name"], [...mcp, sum("response_bytes", "Response bytes")], "idea_estimated_output_tokens"),
);
usageQueryIdeas.parallelism.push(
  idea("parallelism", "busy-units", "Explore", "Busiest units", "Explore units ranked by additive activity hours, keeping workspace/conversation counting and turn-gap assumptions separate", ["counting", "gap_minutes", "unit_id"], [{ id: "idea_unit_label", op: "max", field: "unit_label", label: "Unit" }, ...occupancy], "idea_unit_hours"),
  idea("parallelism", "checkouts", "Explore", "Active checkouts", "Inspect activity at each project and checkout directory; unit-hours count overlapping units separately", ["counting", "gap_minutes", "project_name", "location"], occupancy, "idea_unit_hours"),
);

// Record ideas show the source row directly. The source-action columns are
// supplied by the Analyze table, so these lists contain only schema fields.
function records(dataset: UsageDataset, id: string, section: string, label: string, title: string, columns: string[], sort: string, where: WhereTerm[] = []): RecordQueryIdea {
  return { id: `${dataset}:${id}`, section, label, title, query: {
    ...EMPTY_QUERY, where, select: columns.map(field => ({ field })),
    orderBy: [{ field: sort, dir: "desc", nulls: "last" }],
  } };
}
function scoped(group: GroupedQueryIdea, where: WhereTerm[]): GroupedQueryIdea { return { ...group, where }; }
const eq = (field: string, value: string): WhereClause => ({ field, op: "=", value });
const positive = (field: string): WhereTerm => ({ field, op: ">", value: "0" });
const known = (field: string): WhereTerm => ({ field, op: "is_not_null", value: "" });
const messagePopulation = [count("idea_messages", "Messages"), distinct("conversation_id", "Conversations")];
const authoredMessagePopulation = [count("idea_messages", "Classified user messages"), sum("typed_words", "Your words"), sum("pasted_words", "Likely pasted words"), sum("words", "All classified words"), ratio("idea_authored_share", "SUM([typed_words])/NULLIF(SUM([words]),0)", "Human-written word share", true)];

usageQueryIdeas.messages.push(
  records("messages", "own-writing", "Read messages", "Your writing", "Read user messages with classified human-written words; unclassified messages have no authorship measure", ["sent_at", "text", "typed_words", "pasted_words", "words", "main_category", "work_id", "conversation_id"], "sent_at", [eq("role", "user"), eq("has_authorship", "true"), positive("typed_words")]),
  records("messages", "user-unclassified", "Read messages", "Unclassified user messages", "Inspect user-role messages without authorship evidence; missing word counts are unknown, not zero", ["sent_at", "text", "role", "kind", "sender", "has_authorship", "conversation_id"], "sent_at", [eq("role", "user"), eq("has_authorship", "false")]),
  records("messages", "assistant-replies", "Read messages", "Assistant replies", "Read retained assistant reply text, with model and effort where recorded", ["sent_at", "text", "model", "effort", "sender", "conversation_id"], "sent_at", [eq("role", "assistant"), eq("kind", "message")]),
  records("messages", "thinking", "Read messages", "Thinking blocks", "Read retained reasoning and thinking blocks separately from visible replies", ["sent_at", "text", "kind", "model", "effort", "conversation_id"], "sent_at", [eq("role", "assistant"), { any: [eq("kind", "reasoning"), eq("kind", "thinking")] }]),
  records("messages", "tool-calls", "Read messages", "Tool call messages", "Inspect tool invocations and their call IDs as distinct message records", ["sent_at", "text", "kind", "call_id", "model", "conversation_id"], "sent_at", [eq("kind", "tool_call")]),
  records("messages", "tool-results", "Read messages", "Tool result messages", "Inspect tool output messages, including retained errors and call IDs", ["sent_at", "text", "kind", "call_id", "conversation_id"], "sent_at", [eq("kind", "tool_result")]),
  scoped(idea("messages", "workspace-writing", "Compare", "Writing by workspace", "Classified user messages grouped by authorship work ID; open the exact messages contributing to each workspace", ["work_id"], [{ id: "idea_workspace_title", op: "max", field: "authorship_title", label: "Workspace" }, ...authoredMessagePopulation, { id: "idea_work", op: "link", label: "Workspace", link: { label: { column: "idea_workspace_title" }, to: { kind: "template", template: "/work/{field:work_id}" } } }], "idea_typed_words"), [eq("role", "user"), eq("has_authorship", "true"), known("work_id")]),
  scoped(idea("messages", "writing-weeks", "Over time", "Weekly human writing", "Compare classified user messages and human-written words by message week", ["authorship_week"], authoredMessagePopulation, "idea_typed_words", true), [eq("role", "user"), eq("has_authorship", "true")]),
  idea("messages", "roles-kinds", "Compare", "Roles and message kinds", "Count retained prompts, replies, reasoning, tool calls and tool results without dropping unclassified messages", ["role", "kind"], messagePopulation),
  scoped(idea("messages", "models", "Compare", "Assistant messages by model", "Compare retained assistant message kinds by recorded model; missing model remains a separate group", ["model", "kind"], messagePopulation), [eq("role", "assistant")]),
  idea("messages", "conversation-volume", "Explore", "Messages by conversation", "Rank conversations by retained message count, then drill into exactly those messages", ["conversation_id"], [...messagePopulation, conversation]),
);

const attempts = [sum("attempt_count", "Attempts"), sum("advanced_count", "Advanced attempts"), sum("escalated_count", "Escalated attempts"), sum("error_count", "Error attempts"), sum("cost_usd", "Recorded cost"), count("idea_priced", "Priced attempts", "cost_usd")];
usageQueryIdeas.tl1_attempts.push(
  records("tl1_attempts", "attempts", "Inspect attempts", "Attempt details", "Inspect each attempt's task, configuration, disposition, attribution and transcript availability", ["started_at", "project", "task_id", "attempt_id", "flavor", "shape_version", "configuration", "model_effort", "disposition", "error_attribution", "cost_source", "has_transcript"], "started_at"),
  records("tl1_attempts", "revisits", "Inspect attempts", "Revisited tasks", "Inspect iteration within each task; revisit does not establish that an earlier attempt failed", ["started_at", "project", "task_id", "attempt_id", "revisit", "configuration", "disposition", "error_class", "cost_usd"], "started_at", [eq("revisit", "true")]),
  records("tl1_attempts", "infrastructure", "Inspect attempts", "Infrastructure errors", "Inspect error attribution, signature and disposition for attempts affected by infrastructure", ["started_at", "project", "task_id", "attempt_id", "execution_class", "error_attribution", "error_signature", "disposition", "has_transcript"], "started_at", [eq("error_attribution", "infrastructure")]),
  scoped(idea("tl1_attempts", "llm-configurations", "Compare", "LLM configurations", "Compare advancement, escalation, errors and known cost within the LLM execution class", ["project", "flavor", "shape_version", "configuration"], attempts, "idea_advanced_count"), [eq("execution_class", "llm")]),
  idea("tl1_attempts", "dispositions", "Compare", "Disposition and execution class", "Keep LLM and non-LLM attempts apart while comparing disposition and error attribution", ["execution_class", "disposition", "error_attribution"], attempts),
  idea("tl1_attempts", "task-iterations", "Explore", "Attempts by task", "Follow attempts within a task; revisit marks iteration, not proven failure", ["project", "task_id"], attempts),
  idea("tl1_attempts", "pricing", "Compare", "Cost coverage", "Compare known cost and priced attempt counts by source; unpriced attempts remain in the attempt total", ["cost_source", "execution_class"], attempts),
);

const requests = [count("idea_requests", "Retained responses"), count("idea_usable", "Usable post-tool gaps", "gap_seconds"), sum("input_tokens", "Input tokens"), sum("output_tokens", "Output tokens")];
usageQueryIdeas.model_requests.push(
  records("model_requests", "request-details", "Inspect requests", "Request timing details", "Inspect response time, preceding tool, timing quality, speed source and token counts", ["requested_at", "model_effort_speed", "speed_source", "preceding_tool", "timing_quality", "gap_seconds", "compacted_before", "input_tokens", "output_tokens", "reasoning_output_tokens", "conversation_id"], "requested_at"),
  records("model_requests", "post-tool-waits", "Inspect requests", "Usable post-tool waits", "Inspect responses with a checked post-tool gap; gap seconds measure observed wait, not all model latency", ["requested_at", "preceding_tool", "gap_seconds", "timing_quality", "speed_source", "model_effort_speed", "conversation_id"], "gap_seconds", [known("gap_seconds")]),
  idea("model_requests", "timing-quality", "Compare", "Timing quality by preceding tool", "Expose coverage before comparing post-tool waits; missing gaps remain in the response count", ["timing_quality", "preceding_tool"], requests),
  scoped(idea("model_requests", "usable-speed", "Compare", "Usable wait by model and speed", "Compare observed post-tool waits per usable gap; assumed standard speed is a fallback classification", ["model_effort_speed", "speed_source"], [...requests, ratio("idea_mean_gap", "SUM([gap_seconds])/NULLIF(COUNT([gap_seconds]),0)", "Seconds per usable gap")], "idea_mean_gap"), [known("gap_seconds")]),
  idea("model_requests", "compaction-boundary", "Compare", "Requests after compaction", "Compare retained response counts and timing coverage with and without a preceding compaction", ["compacted_before", "timing_quality"], requests),
  idea("model_requests", "weekly-coverage", "Over time", "Weekly request coverage", "Track retained responses, usable gaps and tokens by request week", ["week"], requests, "idea_requests", true),
);

const labelCounts = [count("idea_labels", "Label observations"), distinct("message_id", "Messages"), distinct("conversation_id", "Conversations")];
usageQueryIdeas.message_labels.push(
  records("message_labels", "observations", "Inspect labels", "Label observations", "Read each classification with the message's role, kind, source and both generating and labeling models", ["occurred_at", "text", "source", "role", "kind", "label_status", "function_role", "family", "label", "model", "label_model", "message_id"], "occurred_at"),
  records("message_labels", "matched", "Inspect labels", "Matched labels", "Inspect matched labels on individual messages; one message may carry several labels", ["occurred_at", "message_id", "label", "family", "function_role", "source", "model", "label_model"], "occurred_at", [eq("label_status", "matched")]),
  idea("message_labels", "functions", "Compare", "Functions by source", "Count label observations and distinct messages by source, function role, family and status; shares can overlap", ["source", "function_role", "family", "label_status"], labelCounts),
  idea("message_labels", "coverage", "Compare", "Label status by role", "Compare status across role and message kind; reading this table does not start labeling", ["role", "kind", "label_status"], labelCounts),
  idea("message_labels", "classifiers", "Compare", "Generating and labeling models", "Keep the model that generated a message separate from the model that classified it", ["model", "label_model", "label_status"], labelCounts),
  idea("message_labels", "weekly", "Over time", "Weekly label observations", "Follow accepted and other label observations by message week", ["week", "label_status"], labelCounts, "idea_labels", true),
);

const boundaryCounts = [count("idea_compactions", "Compactions"), distinct("conversation_id", "Conversations")];
usageQueryIdeas.compactions.push(
  records("compactions", "boundaries", "Inspect boundaries", "Boundary evidence", "Inspect the recorded or inferred trigger, evidence, model and source conversation for each boundary", ["occurred_at", "trigger", "trigger_evidence", "model", "agent_depth", "conversation_id", "title"], "occurred_at"),
  idea("compactions", "weekly-evidence", "Over time", "Weekly triggers and evidence", "Count boundary events and affected conversations by week, trigger and evidence; consecutive boundaries still count", ["week", "trigger", "trigger_evidence"], boundaryCounts, "idea_compactions", true),
  idea("compactions", "models", "Compare", "Service and model", "Compare boundary counts by provider and model, including unknown triggers", ["provider", "model"], boundaryCounts),
  idea("compactions", "depth", "Compare", "Agent depth and trigger", "Compare compactions in main and delegated agent sessions without assuming a cause", ["agent_depth", "trigger"], boundaryCounts),
);

usageQueryIdeas.skill_usages.push(
  records("skill_usages", "evidence-records", "Inspect evidence", "Skill evidence records", "Inspect the path, evidence type, status, call and message IDs for each observation", ["created_at", "skill_name", "skill_path", "evidence_type", "status", "tool_call_id", "evidence_message_id", "body_message_id", "content_bytes", "conversation_id"], "created_at"),
  records("skill_usages", "retained-bodies", "Inspect evidence", "Retained skill bodies", "Inspect positive retained body sizes; repeated loads repeat bytes and a load does not prove instructions were followed", ["created_at", "skill_name", "skill_path", "content_bytes", "tool_call_id", "body_message_id", "status"], "content_bytes", [positive("content_bytes")]),
  idea("skill_usages", "paths", "Compare", "Skill paths and evidence", "Distinguish paths, evidence types and status; paths may share one tool call", ["skill_name", "skill_path", "evidence_type", "status"], [...skills, distinct("tool_call_id", "Tool calls")]),
  idea("skill_usages", "calls", "Compare", "Evidence by tool", "Compare invocations and loads by tool; count distinct calls separately from evidence rows", ["tool_name", "evidence_type", "status"], [...skills, distinct("tool_call_id", "Tool calls")]),
  idea("skill_usages", "weekly", "Over time", "Weekly skill evidence", "Track observed loads and invocations by week and evidence type", ["week", "evidence_type"], skills, "idea_evidence", true),
);

const sessionEvidence = [count("idea_sessions", "Agent sessions"), sum("failed_calls", "Failed calls"), sum("identical_retries", "Identical retries"), sum("thrash_retries", "Thrash retries"), sum("mid_task_compactions", "Mid-task compactions"), sum("unverified_handbacks", "Unverified handbacks"), ratio("idea_failure_rate", "SUM([failed_calls])/NULLIF(SUM([tool_calls]),0)", "Failure rate", true)];
usageQueryIdeas.session_shape.push(
  records("session_shape", "evidence", "Inspect sessions", "Session evidence", "Inspect coverage flags beside retries, compactions, context pressure and handbacks; absent evidence is unknown", ["started_at", "title", "agent_kind", "archetype", "has_tool_data", "has_authorship", "has_timestamps", "has_context_window", "failed_calls", "identical_retries", "thrash_retries", "mid_task_compactions", "context_peak_ratio", "unverified_handbacks", "conversation_id"], "started_at"),
  records("session_shape", "context-pressure", "Inspect sessions", "Context pressure", "Inspect known peak context ratios with context-window evidence and compactions", ["started_at", "title", "context_peak_ratio", "context_peak_tokens", "context_window", "has_context_window", "mid_task_compactions", "conversation_id"], "context_peak_ratio", [eq("has_context_window", "true"), known("context_peak_ratio")]),
  records("session_shape", "retries", "Inspect sessions", "Repeated work", "Inspect identical and thrash retries alongside failed calls and evidence coverage", ["started_at", "title", "identical_retries", "thrash_retries", "failed_calls", "tool_calls", "has_tool_data", "conversation_id"], "identical_retries", [positive("identical_retries")]),
  idea("session_shape", "kind-archetype", "Compare", "Agent kind and archetype", "Pool retries, compactions and failure counts across agent sessions by kind and archetype", ["agent_kind", "archetype"], sessionEvidence),
  idea("session_shape", "coverage", "Compare", "Evidence coverage", "Compare agent sessions by availability of tool, authorship, timing and context-window evidence", ["has_tool_data", "has_authorship", "has_timestamps", "has_context_window"], [count("idea_sessions", "Agent sessions"), sum("forensic_events", "Forensic events")]),
  idea("session_shape", "length", "Over time", "Weekly length bands", "Compare session counts and pooled failure parts by week and length band", ["week", "length_band"], sessionEvidence, "idea_sessions", true),
);

usageQueryIdeas.agent_events.push(
  records("agent_events", "sentences", "Read events", "Classified sentences", "Read each classified sentence with subject, response, probability, source and event-model version", ["occurred_at", "sentence", "family", "subject", "response", "probability", "source", "event_model", "message_id", "conversation_id"], "occurred_at"),
  records("agent_events", "high-probability", "Read events", "High-probability events", "Inspect strongly classified sentences in their original message; probabilities are classifier outputs", ["occurred_at", "sentence", "family", "probability", "source", "event_model", "message_id"], "probability", [{ field: "probability", op: ">=", value: "0.8" }]),
  idea("agent_events", "source-version", "Compare", "Event source and model", "Compare classified sentence counts within each event-model version, keeping replies and thinking separate", ["event_model", "source", "family"], agentEvents),
  idea("agent_events", "subjects", "Compare", "Events with subjects", "Compare event families by whether a subject was extracted, then inspect matching sentences", ["family", "has_subject", "source"], agentEvents),
);
usageQueryIdeas.agent_event_rates.push(
  records("agent_event_rates", "message-observations", "Inspect observations", "Message-kind observations", "Inspect one checked message per event kind, including prompt source, model and event count", ["occurred_at", "family", "events", "source", "prompted_by", "model", "effort", "message_id", "conversation_id"], "occurred_at"),
  idea("agent_event_rates", "family-source-prompt", "Compare", "Rates by prompt source", "Use distinct checked messages as the denominator within each event family, reply or thinking source, and prompt origin", ["family", "source", "prompted_by"], agentEventRates),
  idea("agent_event_rates", "weekly-family", "Over time", "Weekly event rates", "Calculate events per 1,000 checked messages by week and event family", ["week", "family"], agentEventRates, "idea_events", true),
  idea("agent_event_rates", "effort-family", "Compare", "Rates by model effort", "Compare rates within each model, effort and event family, keeping replies and thinking apart in the other views", ["model", "effort", "family"], agentEventRates),
);
usageQueryIdeas.agent_event_messages.push(
  records("agent_event_messages", "checked-messages", "Inspect messages", "Checked messages", "Inspect reply and thinking messages read by the event classifier, including keyword hits and matched events", ["occurred_at", "source", "candidates", "events", "has_event", "event_model", "message_id", "conversation_id"], "occurred_at"),
  records("agent_event_messages", "matched-messages", "Inspect messages", "Messages with events", "Read checked messages with at least one classified event", ["occurred_at", "source", "candidates", "events", "event_model", "message_id", "conversation_id"], "occurred_at", [positive("events")]),
  idea("agent_event_messages", "classifier-coverage", "Compare", "Classifier coverage", "Compare checked replies and thinking by event-model version, including keyword hits without events", ["event_model", "source"], agentEventMessages),
  idea("agent_event_messages", "weekly", "Over time", "Weekly checked messages", "Track checked message populations, keyword candidates and events by week and source", ["week", "source"], agentEventMessages, "idea_message_count", true),
);

usageQueryIdeas.tool_calls.push(
  records("tool_calls", "failed-records", "Inspect calls", "Failed calls", "Inspect failed invocations with the command, error signature, status and recorded duration", ["started_at", "tool_name", "command", "status", "error_type", "error_signature", "duration_ms", "conversation_id"], "started_at", [positive("error_count")]),
  records("tool_calls", "research-records", "Inspect calls", "Research calls", "Inspect retained search queries and reached hosts beside result size and context tokens", ["started_at", "tool_name", "search_query", "host", "url", "result_tokens", "carried_tokens", "conversation_id"], "started_at", [known("search_query")]),
  records("tool_calls", "timed-records", "Inspect calls", "Longest timed calls", "Rank calls with recorded duration, including zero-duration results", ["started_at", "tool_name", "operation_name", "command", "duration_ms", "duration_source", "status", "conversation_id"], "duration_ms", [known("duration_ms")]),
);
usageQueryIdeas.usage.push(
  records("usage", "usage-facts", "Inspect usage", "Usage facts", "Inspect retained usage facts with source conversation, model, tokens, cost and price status", ["day", "title", "provider", "model", "effort", "input_tokens", "output_tokens", "total_tokens", "cost_usd", "price_status", "conversation_id"], "day"),
  records("usage", "unpriced", "Inspect usage", "Unpriced usage", "Inspect token facts without a recorded cost; missing price is not zero cost", ["day", "title", "provider", "model", "total_tokens", "cost_usd", "price_status", "conversation_id"], "day", [eq("price_status", "unpriced")]),
);
usageQueryIdeas.conversation_activity.push(
  records("conversation_activity", "observations", "Inspect activity", "Activity observations", "Inspect the event source, top-level conversation and recorded human messages or output", ["event_at", "event_kind", "human_messages", "output_tokens", "root_conversation_id", "conversation_id", "message_id"], "event_at"),
  idea("conversation_activity", "source-provider", "Compare", "Activity source and provider", "Compare human messages and recorded output by observation kind and top-level provider", ["event_kind", "root_provider"], activity),
);
usageQueryIdeas.rhythm.push(
  records("rhythm", "hourly", "Inspect hours", "Hourly activity records", "Inspect the actor, initiator, counted turns and active versus elapsed hours for each observed hour", ["started_at", "actor", "initiator", "automation", "unit_label", "messages", "turns", "active_hours", "elapsed_hours", "conversation_id"], "started_at"),
  idea("rhythm", "initiators", "Compare", "Who started the work", "Compare recorded activity hours and turns by actor and initiator", ["actor", "initiator"], rhythm),
);
usageQueryIdeas.parallelism.push(
  records("parallelism", "hourly", "Inspect hours", "Hourly concurrent activity", "Inspect each unit-hour, counting method, gap assumption and recorded concurrency level", ["started_at", "counting", "gap_minutes", "unit_label", "repository_name", "level", "unit_hours", "elapsed_hours", "conversation_id"], "started_at"),
  idea("parallelism", "methods", "Compare", "Counting assumptions", "Keep workspace and conversation counts and turn-gap settings apart while comparing additive unit-hours", ["counting", "gap_minutes"], occupancy),
);
usageQueryIdeas.mcp_calls.push(
  records("mcp_calls", "requests", "Inspect requests", "Pharos MCP request log", "Inspect sanitized arguments, status, result count, estimated output and duration for retained requests", ["called_at", "tool_name", "status", "query_source", "arguments_json", "result_count", "estimated_output_tokens", "duration_ms", "truncated"], "called_at"),
  records("mcp_calls", "truncated", "Inspect requests", "Truncated responses", "Inspect requests whose response was truncated, with estimated tokens and bytes; response text is not retained", ["called_at", "tool_name", "query_source", "response_bytes", "estimated_output_tokens", "truncated", "status"], "called_at", [eq("truncated", "true")]),
);
