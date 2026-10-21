import { EMPTY_QUERY, encodeQuery, type QueryState } from "@pythia-software/query-table-core";
import type { QueryRecipe } from "./curiosity-data";
import { queryURL, type RollingWindow } from "./query-links";
import type { TokenChartView } from "./usage-presets";
import { usageDatasetTopics, usageTopicDatasets, usageTopics, type UsageDataset } from "./usage-workspace-model";
import type { MCPDashboardView } from "./mcp-dashboard-model";
import type { ToolDashboardView } from "./tools-dashboard-model";
import { cardDashboardWhere } from "./card-exploration";
import { concurrencyWindowRange, defaultConcurrencySettings } from "./concurrency-settings";

type DailyChart = { period: "day"; rollingPeriods: number; display: "interval"; scale: "linear"; seriesLimit: number };
export type CardDashboard =
  | { topic: "tokens"; chart: TokenChartView }
  | { topic: "mcp"; chart: MCPDashboardView }
  | { topic: "tools"; chart: ToolDashboardView }
  | { topic: "time" | "skills"; chart: DailyChart & { metric: number; split: string } }
  | { topic: "human"; chart: DailyChart & { scope: "typed" | "all"; split: "kind" | "repository_name" | "source_kind" | "providers"; hours: number; days: number } }
  | { topic: "carbon"; chart: { metric: "co2"; split: "type" | "provider" | "repository_name" | "tier"; period: "day"; hours: number; days: number; rollingPeriods: number; display: "interval"; seriesLimit: number } }
  | { topic: "parallelism"; chart: null };
export type CardDestination = CardDashboard & { dataset: UsageDataset; detailAction?: string };

const daily: DailyChart = { period: "day", rollingPeriods: 0, display: "interval", scale: "linear", seriesLimit: 15 };
const tokens = (metric: TokenChartView["metric"], split: TokenChartView["split"], extra: Partial<TokenChartView> = {}): CardDashboard => ({
  topic: "tokens", chart: { ...daily, metric, split, hours: 6, days: 3, selection: { values: [], excluded: false }, ...extra },
});
const mcp = (metric: MCPDashboardView["metric"], split: MCPDashboardView["split"], extra: Partial<MCPDashboardView> = {}): CardDashboard => ({
  topic: "mcp", chart: { metric, split, view: "compare", period: "day", rollingPeriods: 0, display: "interval", ...extra },
});
const tools = (metric: ToolDashboardView["metric"], split: ToolDashboardView["split"] = "tool_name", extra: Partial<ToolDashboardView> = {}): CardDashboard => ({
  topic: "tools", chart: { metric, split, view: "compare", period: "day", rollingPeriods: 0, display: "interval", ...extra },
});
const dashboards: Record<string, CardDashboard> = {
  "usage-models": tokens("tokens", "model_family"),
  "usage-repos": tokens("tokens", "repository_name"),
  "cost-repos": tokens("cost", "repository_name"),
  "model-token-share-trend": tokens("percent", "model_family", { rollingPeriods: 7 }),
  "model-cost-share-trend": tokens("percent_cost", "model_family", { rollingPeriods: 7 }),
  "provider-cost-share": tokens("percent_cost", "provider", { rollingPeriods: 7 }),
  "token-volume-trend": tokens("tokens", "provider", { rollingPeriods: 7 }),
  "token-volume-log": tokens("tokens", "repository_name", { scale: "logarithmic" }),
  "cumulative-api-cost": tokens("cost", "provider", { display: "cumulative" }),
  "mcp-call-trend": mcp("calls", "tool_name", { view: "trend", rollingPeriods: 7 }),
  "mcp-response-size": mcp("bytes", "tool_name"),
  "pharos-mcp-query-sources": mcp("calls", "query_source"),
  "pharos-mcp-durations": mcp("mean_time", "tool_name"),
  "archive-empty-search-rate": mcp("calls", "status"),
  "tool-failure-rates": tools("failure_rate"),
  "repository-failure-rates": tools("failure_rate", "repository_name"),
  "edit-failures": tools("failures", "repository_name"),
  "test-failures": tools("failures", "command_name"),
  "large-outputs": tools("context_added"),
  "output-distribution": tools("context_added"),
  "duration-distribution": tools("mean_time", "command_name"),
  "mcp-methods": tools("calls", "mcp_server"),
  "pharos-mcp-sources": tools("calls", "source_kind"),
  "machine-call-trend": tools("calls", "tool_category", { view: "trend", rollingPeriods: 7 }),
  "machine-latency-trend": tools("mean_time", "tool_category", { view: "trend", rollingPeriods: 7 }),
  "cache-efficiency": tokens("tokens", "type"),
  "cache-repositories": tokens("tokens", "type"),
  "price-coverage": tokens("tokens", "provider"),
  "models-needing-prices": tokens("tokens", "model_family"),
  "repository-output-share": tokens("tokens", "type"),
  "run-effort-token-ranges": tokens("tokens", "effort"),
  "task-effort-token-ranges": tokens("tokens", "effort"),
  "task-model-effort-token-ranges": tokens("tokens", "model_effort"),
  "effort-cost-share": tokens("cost", "effort"),
  "effort-cost": tokens("cost", "model_effort"),
  "repository-token-share": tokens("percent", "repository_name"),
  "model-token-share": tokens("percent", "model_family"),
  "model-cost-share": tokens("percent_cost", "model_family"),
  "repository-cost-share": tokens("percent_cost", "repository_name"),
  "human-example-phrases": { topic: "human", chart: { ...daily, scope: "all", split: "kind", hours: 6, days: 3 } },
  "session-human-output": { topic: "human", chart: { ...daily, scope: "all", split: "providers", hours: 6, days: 3 } },
};

function dashboardFor(recipe: QueryRecipe): CardDashboard {
  const configured = dashboards[recipe.id];
  if (configured) return configured;
  if (recipe.dataset === "writing_messages" || recipe.dataset === "writing" || recipe.dataset === "conversation_activity") return { topic: "human", chart: { ...daily, scope: "typed", split: recipe.id.includes("repo") || recipe.id === "explaining-repos" ? "repository_name" : "kind", hours: 6, days: 3 } };
  if (recipe.dataset === "parallelism") return { topic: "parallelism", chart: null };
  if (recipe.category === "carbon") return { topic: "carbon", chart: { metric: "co2", split: recipe.id.includes("tier") ? "tier" : recipe.id.includes("repo") ? "repository_name" : recipe.id.includes("provider") ? "provider" : "type", period: "day", hours: 6, days: 3, rollingPeriods: 0, display: "interval", seriesLimit: 15 } };
  if (recipe.dataset === "skill_usages") return { topic: "skills", chart: { ...daily, metric: recipe.id === "skill-body-sizes" ? 1 : 0, split: "skill_name" } };
  if (recipe.category === "machines") return { topic: "time", chart: { ...daily, metric: 0, split: "activity" } };
  if (recipe.dataset === "mcp_calls") return mcp("calls", "tool_name");
  if (recipe.dataset === "tool_calls" || recipe.dataset === "tools") return tools(recipe.category === "errors" ? "failures" : "calls", recipe.id.includes("repo") || recipe.id === "cross-repo-errors" ? "repository_name" : "tool_name");
  return tokens(recipe.unit === "USD" || recipe.category === "cost" ? "cost" : "tokens", recipe.category === "delegation" ? "session_kind" : recipe.category === "models" ? "model_family" : "repository_name");
}

export function refineCardDestination(recipe: QueryRecipe): QueryRecipe {
  if (recipe.retired || !Object.hasOwn(usageDatasetTopics, recipe.dataset)) return recipe;
  const dashboard = dashboardFor(recipe);
  return { ...recipe, destination: { ...dashboard, dataset: usageTopicDatasets[dashboard.topic], detailAction: recipe.action }, action: `Explore ${usageTopics[dashboard.topic].label}` };
}

export function cardDestination(recipe: QueryRecipe, query: QueryState, window?: RollingWindow): string {
  const url = queryURL(recipe.dataset, query, window), target = recipe.destination;
  if (!target) return url;
  const parsed = new URL(url, "http://localhost");
  parsed.searchParams.set("analyze_mode", "dashboards");
  parsed.searchParams.set("analyze_topic", target.topic);
  parsed.searchParams.set("analyze_data", target.dataset);
  parsed.searchParams.set("analyze_days", String(window?.days ?? 30));
  if (target.chart) parsed.searchParams.set("analyze_chart", JSON.stringify(target.chart));
  parsed.searchParams.set("analyze_filters", encodeQuery({ ...EMPTY_QUERY, where: cardDashboardWhere(recipe.dataset as UsageDataset, target.dataset, (query.grouping?.source ?? query).where) }));
  if (target.topic === "parallelism") {
    // The record dataset uses saved counting/gap preferences. Inherit those
    // on entry, while resetting selections and setting the card's time range.
    const { counting: _counting, gap: _gap, ...view } = defaultConcurrencySettings;
    parsed.searchParams.set("analyze_parallelism", JSON.stringify({ ...view, range: concurrencyWindowRange(window?.days) }));
  }
  parsed.searchParams.set("card_query", recipe.dataset);
  parsed.searchParams.set("card_id", recipe.id);
  return parsed.pathname + parsed.search;
}
