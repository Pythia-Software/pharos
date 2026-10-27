import type { WhereTerm } from "@pythia-software/query-table-core";
import type { AnalysisCatalog, AnalysisMeasure } from "./dashboard-analysis-model";
import { timeBounds, type TimeFields } from "./chart-range";
import { toolDashboardWhere } from "./usage-dashboard-model";

// Tools over the tools rollup: one record is one recorded day's calls of one
// tool, command, model, repository and origin. A rollup row is no observation
// a person cares about, so this catalog has no units and no per-record values:
// Compare and Over time only. Record-level analysis of individual calls
// (tool_calls, with error signatures, paths, hosts and URLs) is phase 3; it
// would be another catalog over that dataset, beside this one.

const count = { kind: "number", decimals: 0 } as const, percent = { kind: "percent", decimals: 1 } as const, duration = { kind: "duration", sourceUnit: "milliseconds" } as const;
const measures: Record<string, AnalysisMeasure> = {
  calls: { label: "Calls", unit: "calls", format: count, op: "sum", field: "call_count", additive: true, note: "Call counts show the mix of work agents delegate to tools." },
  failures: { label: "Failed calls", unit: "calls", format: count, op: "sum", field: "error_count", additive: true, note: "Calls flagged as errors. A shell command’s non-zero exit can be expected, such as a search with no matches." },
  failure_rate: { label: "Failure rate", unit: "percent", format: percent, op: "sum", ratio: ["error_count", "call_count"], additive: false, note: "Failed calls divided by all calls, pooled over each group's calls rather than averaging daily rates." },
  truncation_rate: { label: "Truncation rate", unit: "percent", format: percent, op: "sum", ratio: ["truncated_count", "call_count"], additive: false, note: "Calls whose recorded result was truncated, divided by all calls, pooled over each group's calls." },
  time: { label: "Recorded tool time", unit: "ms", format: duration, op: "sum", field: "total_duration_ms", additive: true, note: "Summed recorded durations can overlap and include approval waits. Calls without timing contribute no recorded duration." },
  mean_time: { label: "Mean timed call duration", unit: "ms", format: duration, op: "sum", ratio: ["total_duration_ms", "timed_count"], additive: false, note: "Total recorded duration divided by calls with timing, pooled over each group's calls. Untimed calls are excluded." },
  context_added: { label: "Context added", unit: "tokens", format: count, op: "sum", field: "result_tokens", additive: true, note: "Tokens added by tool results. These are estimates from retained result text." },
  context_carried: { label: "Context carried", unit: "tokens", format: count, op: "sum", field: "carried_tokens", additive: true, note: "Result tokens counted again as later model requests re-read them, until compaction. This measures repeated context consumption." },
};

const commands: WhereTerm[] = [{ field: "tool_category", op: "=", value: "command" }];
const breakdowns: AnalysisCatalog["breakdowns"] = {
  tool_category: { label: "Tool category", fields: ["tool_category"] },
  tool_name: { label: "Tool", fields: ["tool_name"] },
  command_category: { label: "Command category", fields: ["command_category"], where: commands, note: "Command categories count shell commands only." },
  program: { label: "Shell program", fields: ["program"], where: commands, note: "Shell programs count shell commands only." },
  command_name: { label: "Command", fields: ["command_name"], where: commands, note: "Commands count shell commands only." },
  mcp_server: { label: "MCP server", fields: ["mcp_server"], where: [{ field: "mcp_server", op: "is_not_null", value: "" }], note: "MCP servers count calls with an identified MCP server only." },
  mcp_method: { label: "MCP method", fields: ["mcp_method"], where: [{ field: "mcp_method", op: "is_not_null", value: "" }, { field: "mcp_method", op: "!=", value: "" }], note: "MCP methods count calls with a recorded MCP method only." },
  repository_name: { label: "Repository", fields: ["repository_name"], empty: "No repository" },
  source_kind: { label: "App", fields: ["source_kind"] },
  channel: { label: "Channel", fields: ["channel"] },
  machine_id: { label: "Machine", fields: ["machine_id"] },
  execution_machine_id: { label: "Execution machine", fields: ["execution_machine_id"] },
  provider: { label: "Provider", fields: ["provider"], order: ["claude", "antigravity", "codex", "chatgpt", "tl1", "canonical"] },
  model_family: { label: "Model", fields: ["model_family"] },
  session_kind: { label: "Agent kind", fields: ["session_kind"], order: ["root", "subagent"], values: { root: "Root agent", subagent: "Sub-agent" } },
};

// The rollup's day is text, so time bounds become day predicates.
const time: TimeFields = { first: "day", last: "day" };
export const toolsAnalysisCatalog: AnalysisCatalog = {
  dataset: "tools", time, measures, breakdowns, units: {},
  records: { label: "Daily tool summary", plural: "Daily tool summaries", note: "A daily tool summary is one recorded day's calls of one tool, command, model, repository and origin: a rollup, not one call. Follow a summary's Calls link to its individual calls." },
  periods: { day: "day", week: "week", month: "month" },
  trend: { metric: "calls", split: "tool_category" },
  // A minimum counts calls: a daily summary holds many.
  samples: "calls",
  executable: (where, now) => toolDashboardWhere(where, timeBounds(where, time, now), new Date(now)),
  scatter: ["calls", "failures"],
};
