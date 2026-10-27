import { EMPTY_QUERY, type AggregationClause, type QueryState, type WhereTerm } from "@pythia-software/query-table-core";
import { withSlice, type SliceValue } from "./chart-selection";
import { cumulativeMetricTrend, rollingMetricTrend, rollingRatioMetricTrend, validChartDisplay, validRollingPeriods, type ChartDisplay, type CumulativeMetric } from "./chart-series";
import type { MetricResult } from "./metric-request";

// These are requests served by Pharos, distinct from external MCP tools
// observed in archived agent conversations on the Tools dashboard.
export const mcpDashboardMeasures = {
  calls: { label: "Calls", op: "sum", field: "call_count", note: "Requests served by Pharos's MCP server." },
  failures: { label: "Failed calls", op: "sum", field: "error_count", note: "Requests recorded as errors, disabled, or oversized." },
  failure_rate: { label: "Failure rate", expression: "SUM([error_count])/NULLIF(SUM([call_count]),0)", note: "Failed calls divided by all calls in each group." },
  output: { label: "Estimated output tokens", op: "sum", field: "estimated_output_tokens", note: "Estimated from response size. Response text is not stored." },
  mean_output: { label: "Mean output tokens", op: "avg", field: "estimated_output_tokens", note: "Mean estimated output per request, including failed calls." },
  largest_output: { label: "Largest output tokens", op: "max", field: "estimated_output_tokens", note: "Largest estimated response in each group." },
  bytes: { label: "Response bytes", op: "sum", field: "response_bytes", note: "Total recorded response size in bytes." },
  truncated: { label: "Truncated calls", op: "sum", field: "truncated_count", note: "Requests whose responses were marked truncated." },
  time: { label: "Total request time", op: "sum", field: "duration_ms", note: "Summed server durations. Concurrent requests can overlap." },
  mean_time: { label: "Mean request duration", op: "avg", field: "duration_ms", note: "Mean recorded server duration per request." },
} as const;
export const mcpDashboardBreakdowns = {
  tool_name: "Pharos tool",
  status: "Call status",
  query_source: "Requested archive source",
} as const;
export type MCPDashboardView = {
  metric: keyof typeof mcpDashboardMeasures;
  split: keyof typeof mcpDashboardBreakdowns;
  view: "compare" | "trend";
  period: "day" | "week" | "month";
  rollingPeriods?: number;
  display?: ChartDisplay;
};
export const defaultMCPDashboard: MCPDashboardView = { metric: "calls", split: "tool_name", view: "compare", period: "day", rollingPeriods: 0, display: "interval" };
export const mcpTrendGroups = 6;
export const validMCPDashboard = (view: MCPDashboardView) => Object.hasOwn(mcpDashboardMeasures, view.metric) && Object.hasOwn(mcpDashboardBreakdowns, view.split) && ["compare", "trend"].includes(view.view) && ["day", "week", "month"].includes(view.period) && validRollingPeriods(view.rollingPeriods ?? 0) && validChartDisplay(view.display);

export function mcpDashboardQuery(view: MCPDashboardView, where: WhereTerm[]): QueryState {
  const measure = mcpDashboardMeasures[view.metric];
  const groupBy = [...(view.view === "trend" ? [view.period] : []), view.split];
  const duration = view.metric === "time" || view.metric === "mean_time";
  const metric: AggregationClause = {
    id: `mcp_${view.metric}`, ...("field" in measure ? { op: measure.op, field: measure.field } : { op: "sum", expression: measure.expression }), groupBy, label: measure.label,
    sort: [{ key: view.view === "trend" ? "group0" : "value", dir: view.view === "trend" ? "asc" : "desc" }],
    display: { kind: view.view === "trend" ? "line" : "list", list: { showBars: true, showValues: true, useGroupColors: true, barPosition: "below", maxLabelLines: 2 }, maxGroups: view.view === "trend" ? mcpTrendGroups : 12, legendPosition: view.view === "trend" ? "bottom" : "none", xLabel: view.view === "trend" ? view.period : measure.label, ...(view.view === "trend" ? { yLabel: measure.label } : {}), ...(duration ? { format: { kind: "duration", sourceUnit: "milliseconds" } } : view.metric === "failure_rate" ? { format: { kind: "percent", decimals: 1 } } : {}) },
  };
  const supporting: AggregationClause[] = view.metric === "calls" ? [] : [{ id: "mcp_calls", op: "sum", field: "call_count", groupBy, label: "Calls" }];
  if (view.metric === "failure_rate") supporting.push({ id: "mcp_failures", op: "sum", field: "error_count", groupBy, label: "Failed calls" });
  if (view.view === "trend" && (view.display === "cumulative" || (view.rollingPeriods ?? 0) > 1) && (view.metric === "mean_time" || view.metric === "mean_output")) {
    const field = view.metric === "mean_time" ? "duration_ms" : "estimated_output_tokens";
    supporting.push({ id: "mcp_total", op: "sum", field, groupBy, label: view.metric === "mean_time" ? "Total request time" : "Estimated output tokens" });
    supporting.push({ id: "mcp_samples", op: "count", field, groupBy, label: "Requests with a recorded value" });
  }
  return { ...EMPTY_QUERY, where, aggregations: [metric, ...supporting] };
}

export function mcpTrendRanking(split: MCPDashboardView["split"]): AggregationClause {
  return { id: "mcp_trend_groups", op: "sum", field: "call_count", groupBy: [split], groupLimit: mcpTrendGroups, sort: [{ key: "value", dir: "desc" }, { key: "group0", dir: "asc" }] };
}

export function withMCPTrendGroups(query: QueryState, split: MCPDashboardView["split"], values: SliceValue[]): QueryState {
  const selected: WhereTerm[] = values.length ? withSlice([], split, { values, excluded: false }) : [{ field: split, op: "is_null", value: "" }, { field: split, op: "is_not_null", value: "" }];
  return { ...query, where: [...query.where, ...selected] };
}

export function cumulativeMCPTrend(view: MCPDashboardView, metrics: MetricResult[], start: Date, end: Date): MetricResult | undefined {
  const calculation: CumulativeMetric = view.metric === "failure_rate" ? { op: "ratio", numerator: "mcp_failures", denominator: "mcp_calls" }
    : view.metric === "mean_time" || view.metric === "mean_output" ? { op: "ratio", numerator: "mcp_total", denominator: "mcp_samples" }
    : { op: view.metric === "largest_output" ? "max" : "sum" };
  return cumulativeMetricTrend(metrics, view.period, start, end, calculation);
}

export function smoothedMCPTrend(view: MCPDashboardView, metrics: MetricResult[], start: Date, end: Date, historyStart?: Date): MetricResult | undefined {
  const ratio = view.metric === "failure_rate" ? { numerator: "mcp_failures", denominator: "mcp_calls" }
    : view.metric === "mean_time" || view.metric === "mean_output" ? { numerator: "mcp_total", denominator: "mcp_samples" } : undefined;
  return ratio ? rollingRatioMetricTrend(metrics, ratio, view.period, view.rollingPeriods ?? 0, start, end, historyStart)
    : rollingMetricTrend(metrics[0], view.period, view.rollingPeriods ?? 0, start, end, historyStart);
}
