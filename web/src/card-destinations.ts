import { EMPTY_QUERY, encodeQuery, type QueryState } from "@pythia-software/query-table-core";
import type { QueryRecipe } from "./curiosity-data";
import { queryURL, type RollingWindow } from "./query-links";
import type { TokenChartView } from "./usage-presets";
import { usageDatasetTopics, usageTopicDatasets, usageTopics, type UsageDataset } from "./usage-workspace-model";
import { cardDashboardWhere } from "./card-exploration";
import { analysisVersion, type AnalysisState } from "./dashboard-analysis-model";
import { concurrencyWindowRange, defaultConcurrencySettings } from "./concurrency-settings";
import { defaultCardCover } from "./card-cover";

type DailyChart = { period: "day"; rollingPeriods: number; display: "interval"; scale: "linear"; seriesLimit: number };
export type CardDashboard =
  | { topic: "tokens"; chart: TokenChartView & { v?: number } }
  // Engine dashboards take an analysis state, versioned so the card's own
  // filters stay its population.
  | { topic: "mcp" | "tools" | "time" | "skills" | "human" | "carbon" | "parallelism"; chart: AnalysisState & { v: number } };
export type CardDestination = CardDashboard & { dataset: UsageDataset; detailAction?: string };

const daily: DailyChart = { period: "day", rollingPeriods: 0, display: "interval", scale: "linear", seriesLimit: 15 };
const tokens = (metric: TokenChartView["metric"], split: TokenChartView["split"], extra: Partial<TokenChartView> = {}): CardDashboard => ({
  topic: "tokens", chart: { ...daily, v: analysisVersion, metric, split, hours: 6, days: 3, selection: { values: [], excluded: false }, ...extra },
});
const engine = (topic: "mcp" | "tools" | "time" | "skills" | "human" | "carbon" | "parallelism", view: AnalysisState["view"]) => (metric: string, split: string, extra: Partial<AnalysisState> = {}): CardDashboard => ({
  topic, chart: { v: analysisVersion, view, metric, split, period: "day", ...extra },
});
const mcp = engine("mcp", "compare"), time = engine("time", "trend"), skills = engine("skills", "trend"), human = engine("human", "trend"), carbon = engine("carbon", "compare"), parallel = engine("parallelism", "trend");
// A card's trailing window with whole days and a norm (buildCardTrend).
const trailing = { rollingPeriods: 7, completePeriods: true, norm: true };
const tools = (metric: string, split = "tool_name", extra: Partial<AnalysisState> = {}) => engine("tools", "compare")(metric, split, extra);
const dashboards: Record<string, CardDashboard> = {
  ...Object.fromEntries((["channel", "machine_id", "execution_machine_id"] as const).flatMap(field => [
    [`origin-tokens-${field}`, tokens("tokens", field)],
    [`origin-cost-${field}`, tokens("cost", field)],
  ])),
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
  "mcp-response-size": mcp("bytes", "tool_name", { view: "distribution", minSamples: 3, scale: "logarithmic" }),
  "pharos-mcp-query-sources": mcp("calls", "query_source"),
  "pharos-mcp-durations": mcp("time", "tool_name", { view: "distribution", minSamples: 3, scale: "logarithmic" }),
  "archive-empty-search-rate": mcp("empty_rate", "tool_name", { minSamples: 10 }),
  "tool-failures": tools("failures", "tool_name", { compareAs: "share" }),
  "tool-call-share": tools("calls", "tool_name", { compareAs: "share" }),
  "tool-failure-rates": tools("failure_rate", "tool_name", { minSamples: 10 }),
  "repository-failure-rates": tools("failure_rate", "repository_name"),
  "edit-failures": tools("failures", "repository_name"),
  "test-failures": tools("failures", "command_name"),
  "large-outputs": time("result_tokens", "tool_source", { view: "compare", compareAs: "share" }),
  "output-distribution": time("result_tokens", "tool_source", { view: "distribution", minSamples: 3, scale: "logarithmic" }),
  "duration-distribution": time("time", "command_source", { view: "distribution", minSamples: 3, scale: "logarithmic" }),
  "machine-time": time("time", "activity", { seriesLimit: 6 }),
  "machine-activities": time("time", "activity", { view: "compare", compareAs: "share" }),
  "machine-operations": time("time", "operation_name", { view: "compare" }),
  "conversation-error-counts": time("calls", "tool_name", { view: "distribution", unit: "tool_conversation", minSamples: 3, scale: "logarithmic" }),
  "tool-calls-per-conversation": time("calls", "repository_name", { view: "distribution", unit: "conversation", minSamples: 3, scale: "logarithmic" }),
  "research-conversation-searches": time("calls", "repository_name", { view: "distribution", unit: "conversation", minSamples: 3, scale: "logarithmic" }),
  "skill-evidence": skills("conversations", "skill_name", { view: "compare" }),
  "skill-body-sizes": skills("body", "skill_evidence", { view: "distribution", minSamples: 3, scale: "logarithmic" }),
  "skill-load-distribution": skills("loads", "skill_name", { view: "distribution", unit: "skill_path", minSamples: 3, scale: "logarithmic" }),
  "mcp-methods": tools("calls", "mcp_server_method"),
  "truncated-outputs": tools("truncation_rate", "tool_name", { minSamples: 10 }),
  "pharos-mcp-sources": tools("calls", "source_kind"),
  "machine-call-trend": tools("calls", "none", { view: "trend", rollingPeriods: 7, completePeriods: true, norm: true }),
  // Its card reads the tools rollup, so its dashboard does too.
  "machine-latency-trend": tools("mean_time", "none", { view: "trend", rollingPeriods: 7, completePeriods: true, norm: true }),
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
  // Human Words: messages (writing_messages).
  "human-words": human("typed", "none"),
  "human-word-total": human("typed", "none", { rollingPeriods: 7, rollingOp: "sum", norm: true }),
  "explaining-repos": human("typed", "repository_name", { view: "compare", compareAs: "share" }),
  "human-word-mix": human("typed_share", "repository_name", { view: "compare" }),
  "human-repository-words-trend": human("typed", "repository_name", { rollingPeriods: 7 }),
  "human-messages-per-day": human("typed_messages", "none", trailing),
  "human-conversations-per-day": human("conversations", "none", trailing),
  "writing-hours": human("typed", "none", { view: "cycle", cycle: "hour", reduce: "total" }),
  "human-words-weekday": human("typed", "none", { view: "cycle", cycle: "weekday" }),
  "human-messages-weekday": human("typed_messages", "none", { view: "cycle", cycle: "weekday" }),
  "human-words-per-message": human("words_per_message", "none", { view: "compare" }),
  "human-words-per-conversation": human("typed", "none", { view: "compare", unit: "conversation", reduce: "mean" }),
  "human-repo-conversation-depth": human("typed", "repository_name", { view: "compare", unit: "conversation", reduce: "mean" }),
  "human-message-lengths": human("typed", "none", { view: "distribution", unit: "conversation" }),
  "human-words-per-work": human("typed", "none", { view: "compare", unit: "work", reduce: "mean" }),
  "human-example-phrases": human("words", "kind"),
  "human-specific-feedback": human("correction_share", "none", { rollingPeriods: 7 }),
  "session-human-output": human("words", "provider"),
  // Carbon: usage, under the user's assumptions.
  "carbon-trend": carbon("co2", "none", { view: "trend", rollingPeriods: 7, rollingOp: "sum", norm: true }),
  "carbon-tiers": carbon("co2", "carbon_tier", { compareAs: "share" }),
  "carbon-repositories": carbon("co2", "repository_name"),
  "carbon-models": carbon("co2", "model_family"),
  "carbon-providers": carbon("co2", "provider", { compareAs: "share" }),
  "carbon-effort": carbon("co2", "effort", { compareAs: "share" }),
  // Parallelism: hour-by-agent records under the user's counting and gap.
  "parallel-days": parallel("active_hours", "level_band"),
  "parallel-trend": parallel("at_once", "none", trailing),
  "parallel-hours": parallel("agent_hours", "none", { view: "cycle", cycle: "hour", reduce: "total" }),
  "parallel-repositories": parallel("agent_hours", "repository_name", { view: "compare" }),
  "parallel-active-weekday": parallel("active_hours", "none", { view: "cycle", cycle: "weekday" }),
  "parallel-weekday": parallel("at_once", "none", { view: "cycle", cycle: "weekday" }),
};

function dashboardFor(recipe: QueryRecipe): CardDashboard {
  const configured = dashboards[recipe.id];
  if (configured) return configured;
  if (recipe.dataset === "writing_messages" || recipe.dataset === "writing" || recipe.dataset === "conversation_activity") return human("typed", recipe.id.includes("repo") ? "repository_name" : "none");
  if (recipe.dataset === "parallelism") return parallel("agent_hours", "none");
  if (recipe.category === "carbon") return carbon("co2", recipe.id.includes("repo") ? "repository_name" : recipe.id.includes("provider") ? "provider" : "type", { view: "trend" });
  if (recipe.dataset === "skill_usages") return skills("records", "skill_name");
  if (recipe.category === "machines") return time("time", "activity");
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
  parsed.searchParams.set("analyze_chart", JSON.stringify(target.chart));
  parsed.searchParams.set("analyze_filters", encodeQuery({ ...EMPTY_QUERY, where: cardDashboardWhere(recipe.dataset as UsageDataset, target.dataset, (query.grouping?.source ?? query).where) }));
  if (target.topic === "parallelism") {
    // The record dataset uses saved counting/gap preferences. Inherit those
    // on entry, while resetting selections and setting the card's time range.
    const { counting: _counting, gap: _gap, ...view } = defaultConcurrencySettings;
    parsed.searchParams.set("analyze_parallelism", JSON.stringify({ ...view, range: concurrencyWindowRange(window?.days) }));
  }
  parsed.searchParams.set("card_query", recipe.dataset);
  parsed.searchParams.set("card_id", recipe.id);
  const cover = defaultCardCover(recipe);
  if (cover) parsed.searchParams.set("card_cover", JSON.stringify(cover));
  parsed.searchParams.set("card_title", recipe.title);
  parsed.searchParams.set("card_description", recipe.description);
  return parsed.pathname + parsed.search;
}
