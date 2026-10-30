import { applyPatch, groupKeyFields, matchPatch, type AggregationClause, type QueryState, type SummaryColumn } from "@pythia-software/query-table-core";
import { usageGroupPresets, usageComparisonPresets, usageTablePresetPatch, type UsageTablePreset } from "./usage-queries";
import type { UsageDataset } from "./usage-workspace-model";

export type QueryIdea = UsageTablePreset & { id: string; section: string; metrics?: AggregationClause[] };
const sum = (field: string, label: string): SummaryColumn => ({ id: `idea_${field}`, op: "sum", field, label });
const count = (id: string, label: string, field?: string): SummaryColumn => ({ id, op: "count", ...(field ? { field } : {}), label });
const distinct = (field: string, label: string): SummaryColumn => ({ id: `idea_${field}_count`, op: "count_distinct", field, label });
const ratio = (id: string, expression: string, label: string, percent = false): SummaryColumn => ({ id, op: "formula", expression, label, display: { kind: percent ? "percent" : "number", decimals: 1 } });
const duration = (field: string, label: string): SummaryColumn => ({ ...sum(field, label), display: { kind: "duration", sourceUnit: "milliseconds" } });
const conversation: SummaryColumn = { id: "idea_conversation", op: "link", label: "Conversation", link: { label: "Open conversation", to: { kind: "template", template: "/conversation/{field:conversation_id}" } } };
const work: SummaryColumn = { id: "idea_work", op: "link", label: "Work", link: { label: "Open work", to: { kind: "template", template: "/work/{field:id}" } } };

// Every idea is a native grouped query. Link summaries are calculated by Query
// Table from the returned group row and never sent to the server. Records links
// inherit source WHERE plus exact group-key predicates (including null keys).
function finish(dataset: UsageDataset, idea: QueryIdea): QueryIdea {
  const summaries = idea.grouping.summaries;
  const links = summaries.filter(column => column.op === "link");
  const records: SummaryColumn = { id: "idea_records", op: "link", label: "Drill In", link: { label: "Matching records", to: { kind: "query", target: dataset } } };
  const ordered = [...summaries.filter(column => column.op !== "link"), ...links, records];
  const columns = [...(idea.columns ?? [...groupKeyFields(idea.grouping.keys), ...summaries.map(column => column.id)]).filter(field => !links.some(link => link.id === field)), ...links.map(link => link.id), records.id];
  return { ...idea, grouping: { ...idea.grouping, summaries: ordered }, columns };
}
function idea(dataset: UsageDataset, id: string, section: string, label: string, title: string, keys: string[], summaries: SummaryColumn[], sort = summaries.find(column => column.op !== "link")?.id, chronological = false): QueryIdea {
  const primary = summaries.find(column => column.id === sort && column.op !== "link");
  return finish(dataset, { id: `${dataset}:${id}`, section, label, title,
    grouping: { keys, summaries, representative: null },
    orderBy: sort ? [{ field: chronological ? keys[0] : sort, dir: "desc", nulls: "last" }] : [],
    metrics: primary ? [{ id: `preview_${id}`, op: "sum", field: primary.id, groupBy: keys, label: primary.label, display: { kind: chronological ? "bar-vertical" : "list", maxGroups: 12, ...(primary.display?.kind === "percent" ? { format: { kind: "percent", decimals: 1 } } : primary.display?.kind === "duration" ? { format: { kind: "duration", sourceUnit: "milliseconds" } } : {}) } }] : [],
  });
}
const written = [sum("typed_words", "Your words"), sum("pasted_words", "Likely pasted words"), sum("user_words", "All user-turn words"), sum("typed_turns", "Turns with your writing"), count("idea_works", "Works"), ratio("idea_words_per_turn", "SUM([typed_words])/NULLIF(SUM([typed_turns]),0)", "Words per written turn"), ratio("idea_authored_share", "SUM([typed_words])/NULLIF(SUM([user_words]),0)", "Human-written word share", true)];
const messages = [sum("typed_words", "Your words"), sum("pasted_words", "Likely pasted words"), sum("words", "All words"), count("idea_messages", "Messages"), ratio("idea_written_messages", "SUM(IF([typed_words]>0,1,0))", "Messages with your writing"), distinct("conversation_id", "Conversations"), ratio("idea_words_per_message", "SUM([typed_words])/NULLIF(SUM(IF([typed_words]>0,1,0)),0)", "Words per written message"), ratio("idea_authored_share", "SUM([typed_words])/NULLIF(SUM([words]),0)", "Human-written word share", true)];
const calls = [count("idea_calls", "Calls"), sum("error_count", "Failed calls"), ratio("idea_failure_rate", "SUM([error_count])/NULLIF(COUNT(),0)", "Failure rate", true), duration("duration_ms", "Recorded tool time"), count("idea_timed", "Timed calls", "duration_ms"), { id: "idea_mean_duration", op: "avg" as const, field: "duration_ms", label: "Time per timed run", display: { kind: "duration" as const, sourceUnit: "milliseconds" as const } }];
const toolTotals = [sum("call_count", "Calls"), sum("error_count", "Failed calls"), ratio("idea_failure_rate", "SUM([error_count])/NULLIF(SUM([call_count]),0)", "Failure rate", true), duration("total_duration_ms", "Recorded tool time"), sum("timed_count", "Timed calls"), { ...ratio("idea_mean_duration", "SUM([total_duration_ms])/NULLIF(SUM([timed_count]),0)", "Time per timed run"), display: { kind: "duration" as const, sourceUnit: "milliseconds" as const } }];
const context = [sum("result_tokens", "Context added (tokens)"), sum("carried_tokens", "Context carried (tokens)")];
const activity = [sum("human_messages", "Human messages"), sum("human_input_tokens_estimate", "Estimated human input tokens"), sum("output_tokens", "Recorded output tokens"), distinct("conversation_id", "Conversations")];
const skills = [count("idea_evidence", "Evidence records"), distinct("conversation_id", "Conversations"), ratio("idea_body_bytes", "SUM(IF([content_bytes]>0,[content_bytes],0))", "Retained body bytes")];
const mcp = [sum("call_count", "Calls"), sum("error_count", "Failed calls"), ratio("idea_failure_rate", "SUM([error_count])/NULLIF(SUM([call_count]),0)", "Failure rate", true), sum("estimated_output_tokens", "Estimated output tokens"), sum("truncated_count", "Truncated responses"), duration("duration_ms", "Server time")];
// Counting method and turn-gap assumptions must stay separate. Unit hours are
// additive; elapsed occupancy is repeated across units and must not be summed.
const occupancy = [sum("unit_hours", "Agent/workspace hours"), distinct("unit_id", "Units"), { id: "idea_peak", op: "max" as const, field: "level", label: "Peak recorded concurrency" }];

export const usageQueryIdeas: Record<UsageDataset, QueryIdea[]> = {
  tl1_attempts: [],
  usage: [...usageGroupPresets.map(preset => ({ preset, section: "Group by" })), ...usageComparisonPresets.map(preset => ({ preset, section: "Compare" }))].map(({ preset, section }) => finish("usage", { ...preset, id: `usage:${preset.label}`, section })),
  writing: [
    idea("writing", "repos", "Compare", "By repository", "Compare whole-work writing totals and weighted words per written turn by repository", ["repository_name"], written),
    idea("writing", "apps", "Compare", "By source app", "Compare writing and supplied text across source apps", ["source_kind"], written),
    idea("writing", "providers", "Compare", "By provider set", "Keep recorded provider combinations separate when comparing work totals", ["providers"], written),
    idea("writing", "works", "Explore", "Deepest conversations", "Rank works by your writing and open their conversation history", ["id"], [{ id: "idea_title", op: "max", field: "title", label: "Work" }, ...written, work], "idea_typed_words"),
    idea("writing", "coverage", "Compare", "Price coverage", "Compare authored text, tokens and API-equivalent cost across pricing statuses", ["price_status"], [...written.slice(0, 4), sum("total_tokens", "Tokens"), sum("cost_usd", "API equivalent")]),
  ],
  writing_messages: [
    idea("writing_messages", "repos", "Compare", "Writing by repository", "Compare message-date writing, message counts and weighted authorship shares", ["repository_name"], messages),
    idea("writing_messages", "weeks", "Over time", "Weekly writing", "Typed, pasted and all supplied words by message week", ["week"], messages, "idea_typed_words", true),
    idea("writing_messages", "categories", "Compare", "Text sources", "Compare main authorship categories with their word totals and message counts", ["main_category"], messages),
    idea("writing_messages", "threads", "Explore", "Conversation writing", "Compare your words per conversation and open the exact matching messages", ["conversation_id"], [...messages, conversation]),
    idea("writing_messages", "apps", "Compare", "App and provider", "Compare human writing by source app and provider", ["source_kind", "provider"], messages),
  ],
  conversation_activity: [
    idea("conversation_activity", "repos", "Compare", "Input and output by repo", "Compare human input estimates and recorded output without treating the ratio as quality", ["repository_name"], activity),
    idea("conversation_activity", "models", "Compare", "Root model and effort", "Compare activity attributed to each root model and effort", ["root_provider", "root_model_family", "root_effort"], activity),
    idea("conversation_activity", "events", "Compare", "Event kinds", "Inspect where human-message and output observations enter the activity ledger", ["event_kind"], activity),
    idea("conversation_activity", "threads", "Explore", "By conversation", "Open conversations contributing human input and output in the selected dates", ["conversation_id"], [...activity, conversation]),
  ],
  tool_calls: [
    idea("tool_calls", "commands", "Time and reliability", "Command families", "Compare parsed command families using total time, timed-run means and failure rates", ["operation_key", "operation_name"], calls, "idea_duration_ms"),
    idea("tool_calls", "tools", "Time and reliability", "Tools and failures", "Compare call counts and weighted failure rates by named tool", ["tool_name"], calls),
    idea("tool_calls", "errors", "Time and reliability", "Failure kinds", "Compare statuses and retained error signatures; inspect the underlying calls", ["status", "error_type"], calls, "idea_error_count"),
    idea("tool_calls", "context", "Context", "Context by command", "Compare context added and carried by command family; token values may be estimated", ["operation_key", "operation_name"], [...context, ...calls]),
    idea("tool_calls", "weeks", "Over time", "Weekly tool time", "Compare recorded tool duration and timing coverage per week", ["week"], calls, "idea_duration_ms", true),
    idea("tool_calls", "threads", "Explore", "Calls by conversation", "Compare conversation call totals, then inspect matching invocations or the thread", ["conversation_id"], [...calls, conversation]),
  ],
  tools: [
    idea("tools", "tools", "Compare", "Tools and failures", "Compare rollups using calls as the failure-rate denominator", ["tool_name"], toolTotals),
    idea("tools", "commands", "Compare", "Shell commands", "Compare programs and subcommands using weighted timed-run averages", ["program", "subcommand"], toolTotals, "idea_total_duration_ms"),
    idea("tools", "mcp", "Compare", "External MCP methods", "Compare captured external MCP servers and methods, including unknown values", ["mcp_server", "mcp_method"], toolTotals),
    idea("tools", "context", "Context", "Context by repository", "Compare context volume added and carried alongside call counts", ["repository_name"], [...context, ...toolTotals]),
    idea("tools", "interruptions", "Compare", "Interruptions and truncation", "Compare timeouts, rejections, interruptions and truncated results by tool", ["tool_name"], [sum("timeout_count", "Timeouts"), sum("rejected_count", "Rejected calls"), sum("interrupted_count", "Interrupted calls"), sum("truncated_count", "Truncated results"), ...toolTotals]),
    idea("tools", "weeks", "Over time", "Weekly summaries", "Calls, failures and recorded time per week across retained rollups", ["week"], toolTotals, "idea_call_count", true),
  ],
  skill_usages: [
    idea("skill_usages", "skills", "Compare", "Skills and evidence", "Compare observed evidence volume, conversations and retained body sizes by skill", ["skill_name"], skills),
    idea("skill_usages", "types", "Compare", "Evidence types", "Distinguish invocations, file loads and harness-reported loads for each skill", ["skill_name", "evidence_type"], skills),
    idea("skill_usages", "repos", "Compare", "Skills by repository", "Compare skill evidence across repositories without treating a load as compliance", ["repository_name", "skill_name"], skills),
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
  parallelism: [
    idea("parallelism", "repos", "Compare", "Activity by repository", "Compare additive unit hours, keeping counting and gap assumptions separate", ["counting", "gap_minutes", "repository_name"], occupancy),
    idea("parallelism", "weeks", "Over time", "Weekly activity", "Agent/workspace hours per week with each counting method and gap separate", ["week", "counting", "gap_minutes"], occupancy, "idea_unit_hours", true),
    idea("parallelism", "levels", "Compare", "Concurrency bands", "Compare unit-hours by concurrency band without summing repeated elapsed time", ["counting", "gap_minutes", "level_band"], occupancy),
    idea("parallelism", "threads", "Explore", "Conversation activity", "Inspect hourly occupancy records and open their source conversation when recorded", ["counting", "gap_minutes", "conversation_id"], [...occupancy, conversation]),
  ],
};

export function queryIdeaPatch(idea: QueryIdea) {
  const patch = usageTablePresetPatch(idea);
  return { ...patch, id: idea.id, owns: [...patch.owns, "metrics" as const], query: { ...patch.query, aggregations: idea.metrics ?? [] } };
}
export function applyQueryIdea(query: QueryState, idea: QueryIdea): QueryState { return { ...applyPatch(query, queryIdeaPatch(idea)), fitToScreen: true }; }
export function queryIdeaMatches(query: QueryState, idea: QueryIdea): boolean {
  return Boolean(query.grouping && matchPatch({ ...query, where: [] }, { ...queryIdeaPatch(idea), owns: ["grouping"] }).status === "active");
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
  idea("tools", "agents", "Compare", "Root agents and subagents", "Compare call mix, failure rates and recorded durations by agent kind", ["session_kind", "tool_category"], toolTotals),
);

function withExample(dataset: UsageDataset, current: QueryIdea, sort: string, message: string, label: string, call?: string): QueryIdea {
  const helper = (field: string): SummaryColumn => ({ id: `example_${field}`, op: "row", field, label: field });
  const fields = ["conversation_id", message, ...(call ? [call] : [])];
  const link: SummaryColumn = { id: "idea_example", op: "link", label: "Source message", link: { label, to: { kind: "template", template: `/conversation/{field:example_conversation_id}?message={field:example_${message}}${call ? `&call={field:example_${call}}` : ""}` } } };
  return finish(dataset, { ...current,
    grouping: { ...current.grouping, summaries: [...current.grouping.summaries.filter(column => column.id !== "idea_records"), ...fields.map(helper), link], representative: { orderBy: [{ field: sort, dir: "desc", nulls: "last" }, { field: "id", dir: "asc", nulls: "last" }] } },
    columns: [...current.columns!.filter(field => field !== "idea_records"), link.id],
  });
}
usageQueryIdeas.writing_messages = usageQueryIdeas.writing_messages.map(current => current.id === "writing_messages:threads" ? withExample("writing_messages", current, "sent_at", "id", "Latest message") : current);
usageQueryIdeas.tool_calls = usageQueryIdeas.tool_calls.map(current => current.id === "tool_calls:commands" ? withExample("tool_calls", current, "duration_ms", "call_message_id", "Inspect call", "call_id") : current);
usageQueryIdeas.skill_usages = usageQueryIdeas.skill_usages.map(current => current.id === "skill_usages:types" ? withExample("skill_usages", current, "created_at", "evidence_message_id", "Latest evidence") : current);

const tokenTotals = usageGroupPresets[0].grouping.summaries;
usageQueryIdeas.usage.push(
  idea("usage", "cache-churn", "Compare", "Cache churn", "Compare weighted cache reuse and cache writes per read, with the underlying token totals", ["provider", "model_family"], [
    ...tokenTotals, sum("cache_creation_input_tokens", "Cache writes"), sum("cache_read_input_tokens", "Cache reads"),
    ratio("idea_write_per_read", "SUM([cache_creation_input_tokens])/NULLIF(SUM([cache_read_input_tokens]),0)", "Writes per cached read"),
  ], "idea_write_per_read"),
  idea("usage", "delegation", "Compare", "Delegation by root model", "Compare root and delegated run roles under their root provider/model; keep all matching source filters", ["root_provider", "root_model_family", "run_role"], tokenTotals, "usage_cost_usd"),
  { ...idea("usage", "root-tasks", "Explore", "Top-level conversations", "Compare total usage across each root conversation and its delegated work, then inspect contributing facts or the root thread", ["root_conversation_id"], [
    { id: "idea_root_title", op: "max", field: "root_title", label: "Root conversation" }, ...tokenTotals, distinct("delegated_session_id", "Delegated sessions"),
    { id: "idea_root_thread", op: "link", label: "Root thread", link: { label: "Open root conversation", to: { kind: "template", template: "/conversation/{field:root_conversation_id}" } } },
  ], "usage_cost_usd"), metrics: [] },
);

// Record explorations favor a compact table over another chart of opaque IDs.
function recordIdea(dataset: UsageDataset, id: string, label: string, title: string, keys: string[], summaries: SummaryColumn[], sort: string): QueryIdea {
  return { ...idea(dataset, id, "Explore", label, title, keys, summaries, sort), metrics: [] };
}
const workTitle: SummaryColumn = { id: "idea_title", op: "max", field: "title", label: "Work" };
const message: SummaryColumn = { id: "idea_message", op: "link", label: "Message", link: { label: "Read message", to: { kind: "template", template: "/conversation/{field:conversation_id}?message={field:id}" } } };
const rootThread: SummaryColumn = { id: "idea_root_thread", op: "link", label: "Root thread", link: { label: "Open root conversation", to: { kind: "template", template: "/conversation/{field:root_conversation_id}" } } };
usageQueryIdeas.usage.push(
  recordIdea("usage", "sessions", "Agent sessions", "Inspect each agent session's usage within its conversation, preserving source filters and unpriced facts", ["agent_session_id", "conversation_id"], [workTitle, ...tokenTotals, conversation], "usage_cost_usd"),
  recordIdea("usage", "conversation-cost", "Costliest conversations", "Rank individual conversations by recorded API price equivalent; unpriced usage remains visible in token totals", ["conversation_id"], [workTitle, ...tokenTotals, conversation], "usage_cost_usd"),
);
usageQueryIdeas.writing.push(
  recordIdea("writing", "hands-on", "Most hands-on", "Rank works by turns containing your writing and open their conversation history; totals cover the whole matching work", ["id"], [workTitle, ...written, work], "idea_typed_turns"),
  recordIdea("writing", "pasted-context", "Most pasted context", "Inspect works containing the most likely-pasted words, alongside your writing and all supplied text", ["id"], [workTitle, ...written, work], "idea_pasted_words"),
  recordIdea("writing", "copied-text", "Most copied text", "Inspect work with the most copied or re-sent words, keeping those counts separate from typed and pasted text", ["id"], [workTitle, sum("copied_words", "Copied or re-sent words"), ...written, work], "idea_copied_words"),
);
usageQueryIdeas.writing_messages.push(
  recordIdea("writing_messages", "long-messages", "Longest messages", "Read individual messages ranked by typed/dictated words, with pasted words and authorship shares alongside", ["conversation_id", "id"], [workTitle, ...messages, message], "idea_typed_words"),
  recordIdea("writing_messages", "pasted-messages", "Most pasted messages", "Read the individual messages with the most likely-pasted words and inspect their classified source text", ["conversation_id", "id"], [workTitle, ...messages, message], "idea_pasted_words"),
);
usageQueryIdeas.conversation_activity.push(
  recordIdea("conversation_activity", "root-threads", "Top-level threads", "Explore human input and recorded output across root conversations and their delegated activity", ["root_conversation_id"], [{ id: "idea_root_title", op: "max", field: "root_title", label: "Root conversation" }, ...activity, rootThread], "idea_output_tokens"),
  withExample("conversation_activity", recordIdea("conversation_activity", "recent-events", "Latest activity events", "Inspect recent human-input and usage observations; message links are available only when the event records a message ID", ["event_kind", "conversation_id", "id"], [{ id: "idea_event_at", op: "max", field: "event_at", label: "Observed at" }, ...activity, conversation], "idea_event_at"), "event_at", "message_id", "Source message"),
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
  idea("parallelism", "busy-units", "Explore", "Busiest units", "Explore units ranked by additive activity hours, keeping workspace/session counting and turn-gap assumptions separate", ["counting", "gap_minutes", "unit_id"], [{ id: "idea_unit_label", op: "max", field: "unit_label", label: "Unit" }, ...occupancy], "idea_unit_hours"),
  idea("parallelism", "checkouts", "Explore", "Active checkouts", "Inspect activity at each repository and checkout directory; unit-hours count overlapping units separately", ["counting", "gap_minutes", "repository_name", "location"], occupancy, "idea_unit_hours"),
);
