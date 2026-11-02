import { todayBounds } from "./day-window";
import { EMPTY_QUERY, encodeQuery, type WhereTerm } from "@pythia-software/query-table-core";
import { defaultConcurrencySettings, type ConcurrencySettings } from "./concurrency-settings";
import { usageWorkspaceURL, type UsageTopic } from "./usage-workspace-model";

const durations: Record<string, number> = { "5m": 300000, "15m": 900000, "1h": 3600000, "6h": 6 * 3600000, "24h": 24 * 3600000, "7d": 7 * 86400000, "30d": 30 * 86400000 };
export type UsageSummaryMetric = "human_words" | "tokens" | "co2e_g" | "cost_usd" | "parallelism" | "active_conversations" | "message_count" | "user_messages" | "assistant_messages" | "tool_calls" | "failed_tool_calls" | "tool_failure_rate" | "tool_time_ms" | "delegated_runs" | "active_repositories" | "agent_time_ms" | "active_time_ms";

// Capture the summary's trailing interval in the link so navigation and reloads
// inspect the same snapshot. An explicit empty filter selects all history.
export function usageSummaryURL(metric: UsageSummaryMetric, window: string, generatedAt: string, concurrency: ConcurrencySettings = defaultConcurrencySettings, from?: string): string | undefined {
  const end = Date.parse(generatedAt);
  const calendar = ["today", "yesterday", "week", "month"].includes(window);
  const start = calendar && Number.isFinite(end) ? Date.parse(from ?? (window === "today" ? todayBounds(new Date(end))[0] : "")) : undefined;
  const duration = calendar && start !== undefined ? end - start : Object.hasOwn(durations, window) ? durations[window] : 0;
  if (window !== "all" && (!Number.isFinite(end) || !Number.isFinite(duration) || duration < 0 || (!duration && !calendar))) return;
  // No existing query population is equivalent to all message/root counts;
  // leave those values plain rather than linking to a misleading subset.
  if (["active_conversations", "message_count", "user_messages", "assistant_messages", "delegated_runs", "active_repositories"].includes(metric)) return;
  const tool = ["tool_calls", "failed_tool_calls", "tool_failure_rate", "tool_time_ms"].includes(metric);
  if (tool) {
    const where: WhereTerm[] = window === "all" ? [] : [
      {field: "started_at", op: ">=", value: new Date(end - duration).toISOString()},
      {field: "started_at", op: "<", value: new Date(end).toISOString()},
    ];
    if (metric === "failed_tool_calls") where.push({field: "status", op: "=", value: "error"});
    return usageWorkspaceURL({mode: "query", topic: "time", dataset: "tool_calls", query: {...EMPTY_QUERY, where}});
  }
  const timing = ["parallelism", "agent_time_ms", "active_time_ms"].includes(metric);
  const topic: UsageTopic = metric === "human_words" ? "human" : metric === "co2e_g" ? "carbon" : timing ? "parallelism" : "tokens";
  const dataset = topic === "human" ? "writing_messages" : topic === "parallelism" ? "parallelism" : "usage";
  const params = new URLSearchParams({ analyze_topic: topic });
  if (topic === "parallelism") {
    params.set("analyze_parallelism", JSON.stringify({ ...concurrency, ...(metric !== "parallelism" ? {counting: "agent", repository: ""} : {}), mode: "user", range: "all", day: "", startDay: "", endDay: "", workspace: "", agent: "", zoom: window !== "all" ? [end - duration, end] : null }));
  } else {
    const where: WhereTerm[] = window !== "all" ? [
      { field: topic === "human" ? "sent_at" : "last_usage_at", op: ">=", value: new Date(end - duration).toISOString() },
      { field: topic === "human" ? "sent_at" : "first_usage_at", op: "<", value: new Date(end).toISOString() },
    ] : [];
    params.set("analyze_filters", encodeQuery({ ...EMPTY_QUERY, where }));
    params.set("analyze_chart", JSON.stringify({ period: duration && duration <= durations["24h"] ? "hour" : "day", ...(topic === "tokens" ? { metric: metric === "cost_usd" ? "cost" : "tokens" } : {}) }));
  }
  return usageWorkspaceURL({ mode: "dashboards", topic, dataset }, params);
}
