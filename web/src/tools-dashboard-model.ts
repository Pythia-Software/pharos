import { EMPTY_QUERY, type AggregationClause, type QueryState, type WhereTerm } from "@pythia-software/query-table-core";
import { withSlice, type SliceValue } from "./chart-selection";
import { cumulativeMetricTrend, rollingMetricTrend, rollingRatioMetricTrend, validChartDisplay, validRollingPeriods, type ChartDisplay, type CumulativeMetric } from "./chart-series";
import type { MetricResult } from "./metric-request";
import { isOriginDimension, originLabel } from "./origin-dimensions";

// MetricCard displays bucket keys directly. Translate only its presentation
// copy and retain an exact lookup for record drilldowns, including duplicate
// device names and the original null group.
export function toolMetricPresentation(metric: MetricResult | undefined, groupBy: string[]) {
  const source = new Map<string, MetricResult["buckets"][number]>();
  if (!metric) return { metric, sourceBucket: (bucket: MetricResult["buckets"][number]) => bucket };
  const names = groupBy.map(field => {
    const labels = new Map<unknown, string>();
    if (!isOriginDimension(field)) return labels;
    const values = [...new Set(metric.buckets.map(bucket => bucket.keys[groupBy.indexOf(field)]))];
    for (const value of values) {
      const label = originLabel(field, value);
      const repeated = values.some(other => other !== value && originLabel(field, other) === label);
      labels.set(value, repeated ? `${label} (${String(value ?? "unknown").slice(-8)})` : label);
    }
    return labels;
  });
  const buckets = metric.buckets.map(bucket => {
    const presented = { ...bucket, keys: bucket.keys.map((key, index) => names[index]?.get(key) ?? key) };
    source.set(JSON.stringify(presented.keys), bucket);
    return presented;
  });
  return { metric: { ...metric, buckets }, sourceBucket: (bucket: MetricResult["buckets"][number]) => source.get(JSON.stringify(bucket.keys)) ?? bucket };
}

export const toolSummaryDimensions = ["day", "tool_name", "tool_category", "mcp_server", "program", "subcommand", "command_category", "channel", "machine_id", "execution_machine_id", "provider", "model", "effort", "repository_name", "session_kind", "source_kind"];
export function toolSummaryCallFilters(row: Record<string, unknown>): WhereTerm[] {
  return toolSummaryDimensions.map(field => {
    const value = row[field];
    return value === null || value === undefined || value === "" ? { field, op: "is_null", value: "" } : { field, op: "=", value: String(value) };
  });
}

export const toolDashboardMeasures = {
  calls: { label: "Calls", field: "call_count", note: "Call counts show the mix of work agents delegate to tools." },
  failures: { label: "Failed calls", field: "error_count", note: "Calls flagged as errors. A shell command’s non-zero exit can be expected, such as a search with no matches." },
  failure_rate: { label: "Failure rate", expression: "SUM([error_count])/NULLIF(SUM([call_count]),0)", note: "Failed calls divided by all calls in each group. Open the grouped query to compare the rate with its call count." },
  time: { label: "Recorded tool time", field: "total_duration_ms", note: "Summed recorded durations can overlap and include approval waits. Calls without timing contribute no recorded duration." },
  mean_time: { label: "Mean timed call duration", expression: "SUM([total_duration_ms])/NULLIF(SUM([timed_count]),0)", note: "Total recorded duration divided by calls with timing. Untimed calls are excluded; grouped queries include both timed and total call counts." },
  context_added: { label: "Context added", field: "result_tokens", note: "Tokens added by tool results. These are estimates from retained result text." },
  context_carried: { label: "Context carried", field: "carried_tokens", note: "Result tokens counted again as later model requests re-read them, until compaction. This measures repeated context consumption." },
} as const;
export const toolDashboardBreakdowns = {
  tool_category: { label: "Tool category", note: "All tool categories", where: [] },
  tool_name: { label: "Tool", note: "All tools", where: [] },
  command_category: { label: "Command category", note: "Shell commands only", where: [{ field: "tool_category", op: "=", value: "command" }] },
  program: { label: "Shell program", note: "Shell commands only", where: [{ field: "tool_category", op: "=", value: "command" }] },
  command_name: { label: "Command", note: "Shell commands only", where: [{ field: "tool_category", op: "=", value: "command" }] },
  mcp_server: { label: "MCP server", note: "Calls with an identified MCP server", where: [{ field: "mcp_server", op: "is_not_null", value: "" }] },
  repository_name: { label: "Repository", note: "All repositories", where: [] },
  source_kind: { label: "App", note: "All recorded source apps", where: [] },
  channel: { label: "Channel", note: "Where tasks started", where: [] },
  machine_id: { label: "Machine", note: "Initiating machines", where: [] },
  execution_machine_id: { label: "Execution machine", note: "Devices running each session", where: [] },
  provider: { label: "Provider", note: "All providers", where: [] },
  model_family: { label: "Model", note: "All model families", where: [] },
  session_kind: { label: "Agent kind", note: "Root agents and subagents", where: [] },
} satisfies Record<string, { label: string; note: string; where: WhereTerm[] }>;

export type ToolDashboardView = {
  metric: keyof typeof toolDashboardMeasures;
  split: keyof typeof toolDashboardBreakdowns;
  view: "compare" | "trend";
  period: "day" | "week" | "month";
  rollingPeriods?: number;
  display?: ChartDisplay;
};
export const defaultToolDashboard: ToolDashboardView = { metric: "calls", split: "tool_category", view: "compare", period: "day", rollingPeriods: 0, display: "interval" };
export const toolTrendGroups = 6;
export const validToolDashboard = (view: ToolDashboardView) => Object.hasOwn(toolDashboardMeasures, view.metric) && Object.hasOwn(toolDashboardBreakdowns, view.split) && ["compare", "trend"].includes(view.view) && ["day", "week", "month"].includes(view.period) && validRollingPeriods(view.rollingPeriods ?? 0) && validChartDisplay(view.display);

export function toolDashboardQuery(view: ToolDashboardView, where: WhereTerm[]): QueryState {
  const measure = toolDashboardMeasures[view.metric];
  const groupBy = [...(view.view === "trend" ? [view.period] : []), view.split];
  const duration = view.metric === "time" || view.metric === "mean_time";
  const metric: AggregationClause = {
    id: `tools_${view.metric}`, op: "sum", ...("field" in measure ? { field: measure.field } : { expression: measure.expression }), groupBy, label: measure.label,
    sort: [{ key: view.view === "trend" ? "group0" : "value", dir: view.view === "trend" ? "asc" : "desc" }],
    display: { kind: view.view === "trend" ? "line" : "bar-horizontal", maxGroups: view.view === "trend" ? toolTrendGroups : 12, legendPosition: view.view === "trend" ? "bottom" : "none", xLabel: view.view === "trend" ? view.period[0].toUpperCase() + view.period.slice(1) : measure.label, ...(view.view === "trend" ? { yLabel: measure.label } : {}), ...(duration ? { format: { kind: "duration", sourceUnit: "milliseconds" } } : view.metric === "failure_rate" ? { format: { kind: "percent", decimals: 1 } } : {}) },
  };
  // Keep denominators beside rates and averages when opening grouped rows.
  const supporting: AggregationClause[] = view.metric === "calls" ? [] : [{ id: "tools_calls", op: "sum", field: "call_count", groupBy, label: "Calls" }];
  if (view.metric === "failure_rate") supporting.push({ id: "tools_failures", op: "sum", field: "error_count", groupBy, label: "Failed calls" });
  if (duration) supporting.push({ id: "tools_timed", op: "sum", field: "timed_count", groupBy, label: "Calls with timing" });
  if (view.metric === "mean_time") supporting.push({ id: "tools_time", op: "sum", field: "total_duration_ms", groupBy, label: "Recorded duration" });
  return { ...EMPTY_QUERY, where: [...where, ...toolDashboardBreakdowns[view.split].where], aggregations: [metric, ...supporting] };
}

export function toolTrendRanking(split: ToolDashboardView["split"]): AggregationClause {
  return { id: "tools_trend_groups", op: "sum", field: "call_count", groupBy: [split], groupLimit: toolTrendGroups, sort: [{ key: "value", dir: "desc" }, { key: "group0", dir: "asc" }] };
}

// Append the exact chosen population so every drilldown answers the displayed
// view. An empty ranking must remain empty instead of opening all records.
export function withToolTrendGroups(query: QueryState, split: ToolDashboardView["split"], values: SliceValue[]): QueryState {
  const selected: WhereTerm[] = values.length ? withSlice([], split, { values, excluded: false }) : [{ field: split, op: "is_null", value: "" }, { field: split, op: "is_not_null", value: "" }];
  return { ...query, where: [...query.where, ...selected] };
}

export function cumulativeToolTrend(view: ToolDashboardView, metrics: MetricResult[], start: Date, end: Date): MetricResult | undefined {
  const calculation: CumulativeMetric = view.metric === "failure_rate" ? { op: "ratio", numerator: "tools_failures", denominator: "tools_calls" }
    : view.metric === "mean_time" ? { op: "ratio", numerator: "tools_time", denominator: "tools_timed" } : { op: "sum" };
  return cumulativeMetricTrend(metrics, view.period, start, end, calculation);
}

export function smoothedToolTrend(view: ToolDashboardView, metrics: MetricResult[], start: Date, end: Date, historyStart?: Date): MetricResult | undefined {
  const ratio = view.metric === "failure_rate" ? { numerator: "tools_failures", denominator: "tools_calls" }
    : view.metric === "mean_time" ? { numerator: "tools_time", denominator: "tools_timed" } : undefined;
  return ratio ? rollingRatioMetricTrend(metrics, ratio, view.period, view.rollingPeriods ?? 0, start, end, historyStart)
    : rollingMetricTrend(metrics[0], view.period, view.rollingPeriods ?? 0, start, end, historyStart);
}
