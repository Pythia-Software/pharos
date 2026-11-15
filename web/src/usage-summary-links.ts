import { todayBounds } from "./day-window";
import { EMPTY_QUERY, encodeQuery, type WhereTerm } from "@pythia-software/query-table-core";
import { defaultConcurrencySettings, type ConcurrencySettings } from "./concurrency-settings";
import { usageWorkspaceURL, type UsageTopic } from "./usage-workspace-model";

const durations: Record<string, number> = { "5m": 300000, "15m": 900000, "1h": 3600000, "6h": 6 * 3600000, "24h": 24 * 3600000, "7d": 7 * 86400000, "30d": 30 * 86400000 };
export type UsageSummaryMetric = "human_words" | "tokens" | "co2e_g" | "cost_usd" | "parallelism" | "active_conversations" | "message_count" | "user_messages" | "assistant_messages" | "tool_calls" | "failed_tool_calls" | "tool_failure_rate" | "tool_time_ms" | "delegated_runs" | "active_repositories" | "agent_time_ms" | "active_time_ms";

// Capture the summary's trailing interval in the link so navigation and reloads
// inspect the same snapshot. An explicit empty filter selects all history.
export function usageSummaryURL(metric: UsageSummaryMetric, window: string, generatedAt: string, concurrency: ConcurrencySettings = defaultConcurrencySettings, from?: string, includeEstimates = true): string | undefined {
  const end = Date.parse(generatedAt);
  const calendar = ["today", "yesterday", "week", "month"].includes(window);
  const start = calendar && Number.isFinite(end) ? Date.parse(from ?? (window === "today" ? todayBounds(new Date(end))[0] : "")) : undefined;
  const duration = calendar && start !== undefined ? end - start : Object.hasOwn(durations, window) ? durations[window] : 0;
  if (window !== "all" && (!Number.isFinite(end) || !Number.isFinite(duration) || duration < 0 || (!duration && !calendar))) return;
  const messages = ["message_count", "user_messages", "assistant_messages"].includes(metric);
  const activity = ["active_conversations", "active_repositories"].includes(metric);
  if (messages || activity || metric === "delegated_runs") {
    const dataset = messages ? "messages" : activity ? "conversation_activity" : "usage";
    const field = messages ? "sent_at" : activity ? "event_at" : "last_usage_at";
    const where: WhereTerm[] = window === "all" ? [] : [
      {field, op: ">=", value: new Date(end - duration).toISOString()},
      {field: metric === "delegated_runs" ? "first_usage_at" : field, op: "<", value: new Date(end).toISOString()},
    ];
    if (messages) {
      where.push({field: "selected", op: "=", value: "true"}, {field: "kind", op: "=", value: "message"});
      if (metric === "message_count") where.push({any: ["user", "assistant"].map(value => ({field: "role", op: "=" as const, value}))});
      else where.push({field: "role", op: "=", value: metric === "user_messages" ? "user" : "assistant"});
      if (metric === "user_messages") where.push({field: "subagent", op: "=", value: "false"});
    } else if (metric === "delegated_runs") {
      where.push({field: "delegated_run_depth", op: ">", value: "0"});
    } else if (metric === "active_repositories") {
      where.push({field: "repository_name", op: "!=", value: ""});
    }
    const source = {...EMPTY_QUERY, where};
    const key = metric === "active_conversations" ? "root_conversation_id" : metric === "active_repositories" ? "repository_name" : metric === "delegated_runs" ? "delegated_session_id" : undefined;
    const query = key ? {...EMPTY_QUERY, grouping: {source, keys: [key], summaries: [{id: "records", op: "count" as const, label: "Records"}], representative: null}} : source;
    return usageWorkspaceURL({mode: "query", dataset, query});
  }
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
    if (!includeEstimates && (topic === "tokens" || topic === "carbon")) where.push({field: "is_estimate", op: "=", value: "false"});
    params.set("analyze_filters", encodeQuery({ ...EMPTY_QUERY, where }));
    params.set("analyze_chart", JSON.stringify({ period: duration && duration <= durations["24h"] ? "hour" : "day", ...(topic === "tokens" ? { metric: metric === "cost_usd" ? "cost" : "tokens" } : {}) }));
  }
  return usageWorkspaceURL({ mode: "dashboards", topic, dataset }, params);
}
