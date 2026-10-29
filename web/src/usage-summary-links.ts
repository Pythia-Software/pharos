import { EMPTY_QUERY, encodeQuery, type WhereTerm } from "@pythia-software/query-table-core";
import { defaultConcurrencySettings, type ConcurrencySettings } from "./concurrency-settings";
import { usageWorkspaceURL, type UsageTopic } from "./usage-workspace-model";

const durations: Record<string, number> = { "1h": 3600000, "6h": 6 * 3600000, "24h": 24 * 3600000, "7d": 7 * 86400000, "30d": 30 * 86400000 };
export type UsageSummaryMetric = "human_words" | "tokens" | "co2e_g" | "cost_usd" | "parallelism";

// Capture the summary's trailing interval in the link so navigation and reloads
// inspect the same snapshot. An explicit empty filter selects all history.
export function usageSummaryURL(metric: UsageSummaryMetric, window: string, generatedAt: string, concurrency: ConcurrencySettings = defaultConcurrencySettings): string | undefined {
  const duration = Object.hasOwn(durations, window) ? durations[window] : 0, end = Date.parse(generatedAt);
  if (window !== "all" && (!duration || !Number.isFinite(end))) return;
  const topic: UsageTopic = metric === "human_words" ? "human" : metric === "co2e_g" ? "carbon" : metric === "parallelism" ? "parallelism" : "tokens";
  const dataset = topic === "human" ? "writing_messages" : topic === "parallelism" ? "parallelism" : "usage";
  const params = new URLSearchParams({ analyze_topic: topic });
  if (topic === "parallelism") {
    params.set("analyze_parallelism", JSON.stringify({ ...concurrency, mode: "user", range: "all", day: "", startDay: "", endDay: "", workspace: "", agent: "", zoom: duration ? [end - duration, end] : null }));
  } else {
    const where: WhereTerm[] = duration ? [
      { field: topic === "human" ? "sent_at" : "last_usage_at", op: ">=", value: new Date(end - duration).toISOString() },
      { field: topic === "human" ? "sent_at" : "first_usage_at", op: "<", value: new Date(end).toISOString() },
    ] : [];
    params.set("analyze_filters", encodeQuery({ ...EMPTY_QUERY, where }));
    params.set("analyze_chart", JSON.stringify({ period: duration && duration <= durations["24h"] ? "hour" : "day", ...(topic === "tokens" ? { metric: metric === "cost_usd" ? "cost" : "tokens" } : {}) }));
  }
  return usageWorkspaceURL({ mode: "dashboards", topic, dataset }, params);
}
