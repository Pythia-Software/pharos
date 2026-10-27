import type { AnalysisCatalog, AnalysisMeasure } from "./dashboard-analysis-model";

// Pharos MCP over mcp_calls: one record is one request Pharos's own MCP
// server answered, distinct from external MCP tools observed in archived agent
// conversations on the Tools dashboard. A request is one observation, so
// record distributions of its duration and response size are honest; there
// is no larger unit of analysis in this dataset.

const count = { kind: "number", decimals: 0 } as const, percent = { kind: "percent", decimals: 1 } as const, duration = { kind: "duration", sourceUnit: "milliseconds" } as const;
const measures: Record<string, AnalysisMeasure> = {
  calls: { label: "Calls", unit: "calls", format: count, op: "sum", field: "call_count", additive: true, note: "Requests served by Pharos's MCP server." },
  failures: { label: "Failed calls", unit: "calls", format: count, op: "sum", field: "error_count", additive: true, note: "Requests recorded as errors, disabled, or oversized." },
  failure_rate: { label: "Failure rate", unit: "percent", format: percent, op: "sum", ratio: ["error_count", "call_count"], additive: false, note: "Failed calls divided by all calls, pooled over each group's requests." },
  output: { label: "Estimated output tokens", unit: "tokens", format: count, op: "sum", field: "estimated_output_tokens", value: "estimated_output_tokens", additive: true, note: "Estimated from response size. Response text is not stored." },
  mean_output: { label: "Mean output tokens", unit: "tokens", format: count, op: "sum", ratio: ["estimated_output_tokens", { op: "count", field: "estimated_output_tokens" }], additive: false, note: "Estimated output divided by requests with an estimate, failed calls included, pooled over each group's requests." },
  largest_output: { label: "Largest output tokens", unit: "tokens", format: count, op: "max", field: "estimated_output_tokens", additive: false, note: "The largest estimated response in each group." },
  bytes: { label: "Response bytes", unit: "bytes", format: count, op: "sum", field: "response_bytes", value: "response_bytes", additive: true, note: "Recorded response size in bytes." },
  truncated: { label: "Truncated calls", unit: "calls", format: count, op: "sum", field: "truncated_count", additive: true, note: "Requests whose responses were marked truncated." },
  time: { label: "Request time", unit: "ms", format: duration, op: "sum", field: "duration_ms", value: "duration_ms", additive: true, note: "Recorded server durations. Concurrent requests can overlap, so totals are not elapsed time." },
  mean_time: { label: "Mean request duration", unit: "ms", format: duration, op: "sum", ratio: ["duration_ms", { op: "count", field: "duration_ms" }], additive: false, note: "Recorded server duration divided by requests with a duration, pooled over each group's requests." },
};

export const mcpAnalysisCatalog: AnalysisCatalog = {
  dataset: "mcp_calls", time: { first: "called_at", last: "called_at" }, measures,
  breakdowns: {
    tool_name: { label: "Pharos tool", fields: ["tool_name"] },
    status: { label: "Call status", fields: ["status"] },
    query_source: { label: "Requested archive source", fields: ["query_source"], empty: "No source requested", note: "The archive source is the caller's explicit source filter; unscoped requests have no recorded source." },
  },
  units: {},
  records: { label: "Pharos request", plural: "Pharos requests", note: "The latest 5,000 requests are retained, so a long range can hold fewer than were made." },
  // Requests are timestamps: hours and local days come from called_at.
  cycle: { hour: { kind: "timestamp", field: "called_at" }, weekday: { kind: "timestamp", field: "called_at" } },
  periods: { day: "day", week: "week", month: "month" },
  trend: { metric: "calls", split: "tool_name" },
  scatter: ["calls", "output"],
};
