import type { WhereTerm } from "@pythia-software/query-table-core";
import type { AnalysisCatalog, AnalysisMeasure, AnalysisState } from "./dashboard-analysis-model";
import { activityPresentation } from "./machine-activity-data";

// Agent Time and Skills read individual records (tool calls, skill evidence),
// so each record is one observation, and records of one agent conversation
// group into conversation units.

const percent = { kind: "percent", decimals: 1 } as const;
const count = { kind: "number", decimals: 0 } as const, duration = { kind: "duration", sourceUnit: "milliseconds" } as const;
const present = (field: string): WhereTerm[] => [{ field, op: "is_not_null", value: "" }, { field, op: "!=", value: "" }];
const origin = { channel: ["channel"], machine_id: ["machine_id"], execution_machine_id: ["execution_machine_id"] };
const single = (entries: Array<[string, string]>): AnalysisCatalog["breakdowns"] => Object.fromEntries(entries.map(([field, label]) => [field, { label, fields: [field] }]));
const providerOrder = ["claude", "antigravity", "codex", "chatgpt", "tl1", "canonical"];
// The old charts' metrics were list positions; links that still carry them
// read as the measure at that position.
const numbered = (ids: string[]) => (state: AnalysisState): AnalysisState => {
  const position: unknown = state.metric;
  return typeof position === "number" && ids[position] ? { ...state, metric: ids[position] } : state;
};

// ----- Agent Time: tool_calls, one record per call ----------------------------

const timeMeasures: Record<string, AnalysisMeasure> = {
  // Durations are milliseconds; the dashboard shows this total in hours.
  time: { label: "Recorded tool time", unit: "hours", format: duration, op: "sum", field: "duration_ms", value: "duration_ms", additive: true, note: "Recorded durations can overlap and may include approval waits. Their sum is tool time, not elapsed wall time." },
  calls: { label: "Tool calls", unit: "calls", format: count, op: "count", additive: true, note: "Every recorded call counts once, including errors and calls without a duration." },
  mean_time: { label: "Mean call duration", unit: "ms", format: duration, op: "sum", ratio: ["duration_ms", { op: "count", field: "duration_ms" }], additive: false, note: "Recorded duration divided by calls with a duration, pooled over each group's calls; calls without timing are left out." },
  failures: { label: "Failed calls", unit: "calls", format: count, op: "sum", field: "error_count", additive: true, note: "Calls recorded as errors. A shell command’s non-zero exit can be expected, such as a search with no matches." },
  failure_rate: { label: "Failure rate", unit: "percent", format: percent, op: "sum", ratio: ["error_count", { op: "count", field: "id" }], additive: false, note: "Failed calls divided by all matching calls, pooled over each group." },
  truncation_rate: { label: "Truncation rate", unit: "percent", format: percent, op: "sum", ratio: [{ op: "sum", expression: "IF([truncated],1,0)", label: "truncated calls" }, { op: "count", field: "id" }], additive: false, note: "Calls with truncated results divided by all matching calls." },
  conversations: { label: "Conversations reached", unit: "conversations", format: count, op: "count_distinct", field: "conversation_id", additive: false, note: "Distinct agent conversations with matching calls; a conversation can appear in several groups." },
  repositories: { label: "Repositories reached", unit: "repositories", format: count, op: "count_distinct", field: "repository_name", additive: false, note: "Distinct repositories with matching calls; a repository can appear in several groups." },
  carried_tokens: { label: "Context carried", unit: "tokens", format: count, op: "sum", field: "carried_tokens", value: "carried_tokens", additive: true, note: "Attributed tool-result tokens carried into later requests." },
  result_tokens: { label: "Result tokens", unit: "tokens", format: count, op: "sum", field: "result_tokens", value: "result_tokens", additive: true, note: "Tokens a call's result added to the context, measured or estimated from retained result text." },
};
const conversationSplits = { repository_name: ["repository_name"], provider: ["provider"], ...origin };
export const agentTimeAnalysisCatalog: AnalysisCatalog = {
  dataset: "tool_calls", time: { first: "started_at", last: "started_at" }, measures: timeMeasures,
  breakdowns: {
    activity: { label: "Activity", fields: ["activity"] },
    ...single([["tool_category", "Tool category"], ["tool_name", "Tool"], ["operation_name", "Operation"], ["command_name", "Command"], ["provider", "Provider"], ["channel", "Channel"], ["machine_id", "Machine"], ["execution_machine_id", "Execution machine"], ["repository_name", "Repository"]]),
    ...single([["error_signature", "Error signature"], ["error_type", "Failure kind"], ["repo_path", "Repository path"], ["host", "Research host"], ["url", "Source URL"], ["search_query", "Search query"], ["mcp_server", "MCP server"], ["mcp_method", "MCP method"], ["source_kind", "App"]]),
    repository_path: { label: "Repository + path", fields: ["repository_name", "repo_path"] },
    repository_error: { label: "Repository + error", fields: ["repository_name", "error_signature"] },
    repository_command: { label: "Repository + command", fields: ["repository_name", "command_name"] },
    mcp_server_method: { label: "MCP server + method", fields: ["mcp_server", "mcp_method"] },
    app_provider: { label: "App + provider", fields: ["source_kind", "provider"] },
    // Cards keep evidence sources apart: a reported duration and one derived
    // from timestamps, or a measured and an estimated result size.
    command_source: { label: "Command + timing source", fields: ["command_name", "duration_source"] },
    tool_source: { label: "Tool + result-size source", fields: ["tool_name", "result_tokens_source"] },
  },
  labels: { activity: value => activityPresentation(value).label },
  records: { label: "Tool call", plural: "Tool calls", note: "Calls are placed by their start time and keep their full recorded duration." },
  units: {
    conversation: { label: "Agent conversation", plural: "Agent conversations", key: "conversation_id", where: present("conversation_id"), breakdowns: conversationSplits,
      note: "An agent conversation is one recorded agent transcript; delegated subagents are conversations of their own. Calls without a conversation are left out." },
    path_conversation: { label: "Path in a conversation", plural: "Conversation/path pairs", key: ["conversation_id", "repository_name", "repo_path"], where: [...present("conversation_id"), ...present("repo_path")],
      breakdowns: { repository_path: ["repository_name", "repo_path"], ...conversationSplits }, note: "Calls to one repository path in one agent conversation form one observation." },
    tool_conversation: { label: "Tool in a conversation", plural: "Tool/conversation pairs", key: ["conversation_id", "tool_name"], where: present("conversation_id"),
      breakdowns: { tool_name: ["tool_name"], tool_category: ["tool_category"], ...conversationSplits },
      note: "A tool in a conversation is one tool's calls in one recorded agent transcript, so a conversation using several tools is one observation under each." },
  },
  cycle: { hour: { kind: "timestamp", field: "started_at" }, weekday: { kind: "timestamp", field: "started_at" } },
  periods: { day: "day", week: "week", month: "month" }, timestamp: "started_at",
  trend: { metric: "time", split: "activity" },
  legacy: numbered(["time", "calls"]),
  legacySlices: true,
  scatter: ["calls", "time"],
};
agentTimeAnalysisCatalog.breakdowns.provider.order = providerOrder;
agentTimeAnalysisCatalog.breakdowns.repository_name.empty = "No repository";

// ----- Skills: skill_usages, one record per piece of evidence ----------------

const skillMeasures: Record<string, AnalysisMeasure> = {
  records: { label: "Evidence records", unit: "records", format: count, op: "count", additive: true, note: "Observed invocations, file loads and harness-reported loads. A load does not prove a skill was followed." },
  body: { label: "Retained body volume", unit: "bytes", format: count, op: "sum", field: "content_bytes", value: "content_bytes", additive: true, where: [{ field: "content_bytes", op: ">", value: "0" }],
    note: "Body volume sums known positive recorded sizes, including repeated loads; evidence without a recorded size is left out." },
  conversations: { label: "Conversations reached", unit: "conversations", format: count, op: "count_distinct", field: "conversation_id", additive: false, note: "Distinct conversations with skill evidence. A conversation with several skills counts once in each, so groups do not add up to the total." },
  repositories: { label: "Repositories reached", unit: "repositories", format: count, op: "count_distinct", field: "repository_name", additive: false, note: "Distinct repositories holding matching skill evidence; a repository can appear under several skills." },
  loads: { label: "Distinct file-load calls", unit: "loads", format: count, op: "count_distinct", field: "tool_call_id", additive: false, note: "Distinct tool calls that loaded a skill file; duplicate evidence for one call counts once." },
};
export const skillsAnalysisCatalog: AnalysisCatalog = {
  dataset: "skill_usages", time: { first: "created_at", last: "created_at" }, measures: skillMeasures,
  breakdowns: {
    ...single([["skill_name", "Skill"], ["evidence_type", "Evidence type"]]),
    skill_evidence: { label: "Skill + evidence type", fields: ["skill_name", "evidence_type"] },
    ...single([["provider", "Provider"], ["channel", "Channel"], ["machine_id", "Machine"], ["execution_machine_id", "Execution machine"], ["repository_name", "Repository"], ["status", "Status"]]),
  },
  records: { label: "Evidence record", plural: "Evidence records", note: "One record is one observed invocation, file load or harness-reported load of a skill." },
  units: {
    conversation: { label: "Conversation", plural: "Conversations", key: "conversation_id", where: present("conversation_id"), breakdowns: { provider: ["provider"], repository_name: ["repository_name"], ...origin },
      note: "A conversation is one recorded agent conversation with skill evidence; evidence without a conversation is left out." },
    skill_path: { label: "Skill path in a conversation", plural: "Conversation/path pairs", key: ["conversation_id", "skill_path"], where: [...present("conversation_id"), ...present("skill_path")],
      breakdowns: { skill_name: ["skill_name"], provider: ["provider"], repository_name: ["repository_name"], ...origin },
      note: "A skill path in a conversation is the evidence for one skill file in one recorded conversation, so a conversation loading several skills is one observation under each." },
  },
  cycle: { hour: { kind: "timestamp", field: "created_at" }, weekday: { kind: "timestamp", field: "created_at" } },
  periods: { day: "day", week: "week", month: "month" }, timestamp: "created_at",
  trend: { metric: "records", split: "skill_name" },
  legacy: numbered(["records", "body"]),
  legacySlices: true,
  scatter: ["records", "body"],
};
skillsAnalysisCatalog.breakdowns.provider.order = providerOrder;
skillsAnalysisCatalog.breakdowns.repository_name.empty = "No repository";
