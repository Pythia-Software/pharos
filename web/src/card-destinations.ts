import { EMPTY_QUERY, encodeQuery, type QueryState } from "@pythia-software/query-table-core";
import type { QueryRecipe } from "./curiosity-data";
import { queryURL, type RollingWindow } from "./query-links";
import { usageDatasetTopics, usageTopicDatasets, usageTopics, type UsageDataset, type UsageTopic } from "./usage-workspace-model";
import { cardDashboardWhere } from "./card-exploration";
import { analysisVersion, type AnalysisState } from "./dashboard-analysis-model";
import { concurrencyWindowRange, defaultConcurrencySettings } from "./concurrency-settings";
import { defaultCardCover } from "./card-cover";
import { windowedChart } from "./card-windows";

type DailyChart = { period: "day"; rollingPeriods: number; display: "interval"; scale: "linear"; seriesLimit: number };
// Versioned chart states keep source filters as the dashboard population.
export type CardDashboard = { topic: UsageTopic; chart: AnalysisState & { v: number } };
export type CardDestination = CardDashboard & { dataset: UsageDataset; detailAction?: string };

const daily: DailyChart = { period: "day", rollingPeriods: 0, display: "interval", scale: "linear", seriesLimit: 15 };
const tokens = (metric: string, split: string, extra: Partial<AnalysisState> = {}): CardDashboard => ({
  topic: "tokens", chart: { ...daily, v: analysisVersion, metric, split, hours: 6, days: 3, selection: { values: [], excluded: false }, ...extra },
});
const engine = (topic: UsageTopic, view: AnalysisState["view"]) => (metric: string, split: string, extra: Partial<AnalysisState> = {}): CardDashboard => ({
  // Card charts were linear before the engine; their states say so, since
  // a trend without a scale draws the linear + log split.
  topic, chart: { v: analysisVersion, view, metric, split, period: "day", scale: "linear", ...extra },
});
const activity = engine("activity", "scatter");
const mcp = engine("mcp", "compare"), time = engine("time", "trend"), skills = engine("skills", "trend"), human = engine("human", "trend"), carbon = engine("carbon", "compare"), parallel = engine("parallelism", "trend");
// A card's trailing window with whole days and a norm (buildCardTrend).
const trailing = { rollingPeriods: 7, completePeriods: true, norm: true };
const calls = (metric: string, split = "tool_name", extra: Partial<AnalysisState> = {}) => engine("calls", "compare")(metric, split, extra);
const dashboards: Record<string, CardDashboard> = {
  "error-signatures": calls("calls", "error_signature", { minSamples: 2 }),
  "error-reach": calls("repositories", "error_signature"),
  "cross-repo-errors": calls("calls", "repository_error"),
  "read-paths": calls("calls", "repository_path"),
  "repeated-reads": calls("calls", "repository_path", { unit: "path_conversation", reduce: "max" }),
  "primary-domains": calls("calls", "host"),
  "primary-urls": calls("conversations", "url"),
  "skill-reach": skills("repositories", "skill_name", { view: "compare" }),
  "usage-work": tokens("tokens", "repository_name", { view: "distribution", unit: "conversation", scale: "logarithmic" }),
  "delegation-depth": tokens("tokens", "delegated_run_depth", { view: "compare" }),
  "model-session-costs": tokens("cost", "provider_model", { view: "distribution", unit: "conversation", scale: "logarithmic", minSamples: 3 }),
  "largest-subagent-weight": tokens("delegated_runs", "provider_model_effort", { view: "compare", unit: "conversation", reduce: "max" }),
  "session-delegation-count": tokens("delegated_runs", "provider_model_effort", { view: "distribution", unit: "conversation", scale: "logarithmic" }),
  "model-agent-roles": tokens("agent_sessions", "provider_model_role", { view: "compare", compareAs: "bars" }),
  "session-token-cost": tokens("tokens", "provider_model", { view: "scatter", unit: "conversation", metricY: "cost", scale: "logarithmic", scaleY: "logarithmic" }),
  "session-input-output": tokens("input", "provider_model", { view: "scatter", unit: "conversation", metricY: "output", scale: "logarithmic", scaleY: "logarithmic" }),
  "model-conversation-reach": tokens("conversations", "provider_model", { view: "compare" }),
  "repository-model-use": tokens("tokens", "repository_provider_model", { view: "compare" }),
  "api-cost-total": tokens("cost", "repository_name", { view: "compare" }),
  "window-work-cost": tokens("cost", "repository_name", { view: "compare", unit: "conversation", reduce: "median" }),
  "work-cost-distribution": tokens("cost", "repository_name", { view: "distribution", unit: "conversation", scale: "logarithmic" }),
  "work-volume-cost": tokens("root_conversations", "root_repository_name", { view: "scatter", unit: "repository", metricY: "cost", scale: "logarithmic", scaleY: "logarithmic" }),
  "task-token-ranges": tokens("tokens", "repository_name", { view: "distribution", unit: "conversation", scale: "logarithmic", minSamples: 3 }),
  "task-output-ranges": tokens("output", "repository_name", { view: "distribution", unit: "conversation", scale: "logarithmic", minSamples: 3 }),
  "task-model-token-ranges": tokens("tokens", "provider_model", { view: "distribution", unit: "conversation", scale: "logarithmic", minSamples: 3 }),
  "repository-delegated-share": tokens("delegated_share", "root_repository_name", { view: "compare" }),
  "delegating-task-share": tokens("delegating_share", "root_repository_name", { view: "compare" }),
  "delegated-run-token-ranges": tokens("tokens", "repository_name", { view: "distribution", unit: "delegated_run", scale: "logarithmic", minSamples: 3 }),

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
  "tool-failures": calls("failures", "tool_name", { compareAs: "share" }),
  "tool-call-share": calls("calls", "tool_name", { compareAs: "share" }),
  "tool-failure-rates": calls("failure_rate", "tool_name", { minSamples: 10 }),
  "repository-failure-rates": calls("failure_rate", "repository_name"),
  "edit-failures": calls("calls", "repository_path"),
  "test-failures": calls("calls", "repository_command"),
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
  "mcp-methods": calls("calls", "mcp_server_method"),
  "truncated-outputs": calls("truncation_rate", "tool_name", { minSamples: 10 }),
  "pharos-mcp-sources": calls("calls", "app_provider"),
  "machine-call-trend": calls("calls", "none", { view: "trend", rollingPeriods: 7, completePeriods: true, norm: true }),
  // Record-level trends retain the card's precise timestamp bounds.
  "machine-latency-trend": calls("mean_time", "none", { view: "trend", rollingPeriods: 7, completePeriods: true, norm: true }),
  "cache-efficiency": tokens("cache_read_share", "provider_model", { view: "compare" }),
  "cache-repositories": tokens("cache_read_share", "repository_name", { view: "compare" }),
  "price-coverage": tokens("tokens", "price_status", { view: "compare", compareAs: "share" }),
  "models-needing-prices": tokens("tokens", "model", { view: "compare" }),
  "repository-output-share": tokens("output_share", "repository_name", { view: "compare" }),
  "run-effort-token-ranges": tokens("tokens", "effort", { view: "distribution", unit: "run_effort", scale: "logarithmic", minSamples: 3 }),
  "task-effort-token-ranges": tokens("tokens", "effort", { view: "distribution", unit: "conversation", scale: "logarithmic", minSamples: 3 }),
  "task-model-effort-token-ranges": tokens("tokens", "provider_model_effort", { view: "distribution", unit: "conversation", scale: "logarithmic", minSamples: 3 }),
  "effort-cost-share": tokens("cost", "effort", { view: "compare", compareAs: "share" }),
  "effort-cost": tokens("cost", "provider_model_effort", { view: "compare", unit: "conversation", reduce: "mean" }),
  "repository-token-share": tokens("tokens", "repository_name", { view: "compare", compareAs: "share" }),
  "model-token-share": tokens("tokens", "provider_model", { view: "compare", compareAs: "share" }),
  "model-cost-share": tokens("cost", "provider_model", { view: "compare", compareAs: "share" }),
  "repository-cost-share": tokens("cost", "repository_name", { view: "compare", compareAs: "share" }),
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
  "human-message-lengths": human("typed", "none", { view: "distribution", unit: "conversation", scale: "logarithmic" }),
  "human-words-per-work": human("typed", "none", { view: "compare", unit: "work", reduce: "mean" }),
  "human-example-phrases": human("words", "kind"),
  "human-specific-feedback": human("correction_share", "none", { rollingPeriods: 7 }),
  "session-human-output": activity("human_input", "provider_model", { unit: "conversation", metricY: "output", scale: "logarithmic", scaleY: "logarithmic" }),
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
  throw new Error(`Card ${recipe.id} needs an explicit dashboard destination.`);
}

export function refineCardDestination(recipe: QueryRecipe): QueryRecipe {
  if (recipe.retired || recipe.dataset === "tl1_attempts" || !Object.hasOwn(usageDatasetTopics, recipe.dataset)) return recipe;
  const mapped = dashboardFor(recipe);
  const dashboard = mapped.chart.view === "distribution" && recipe.cover?.minSamples !== undefined ? { ...mapped, chart: { ...mapped.chart, minSamples: recipe.cover.minSamples } } : mapped;
  return { ...recipe, destination: { ...dashboard, dataset: usageTopicDatasets[dashboard.topic], detailAction: recipe.action }, action: `Explore ${usageTopics[dashboard.topic].label}` };
}

// The Parallelism ribbon's view on entry from a link: the defaults, less the
// counting and gap the saved settings own, over the link's range.
export function parallelismRibbon(days?: number | null) {
  const { counting: _counting, gap: _gap, ...view } = defaultConcurrencySettings;
  return { ...view, range: concurrencyWindowRange(days) };
}

export function cardDestination(recipe: QueryRecipe, query: QueryState, window?: RollingWindow): string {
  const url = queryURL(recipe.dataset, query, window), target = recipe.destination;
  if (!target) return url;
  const parsed = new URL(url, "http://localhost");
  parsed.searchParams.set("analyze_mode", "dashboards");
  parsed.searchParams.set("analyze_topic", target.topic);
  parsed.searchParams.set("analyze_data", target.dataset);
  parsed.searchParams.set("analyze_days", String(window?.days ?? 30));
  parsed.searchParams.set("analyze_chart", JSON.stringify(windowedChart(recipe.id, target.chart, window?.days)));
  parsed.searchParams.set("analyze_filters", encodeQuery({ ...EMPTY_QUERY, where: cardDashboardWhere(recipe.dataset as UsageDataset, target.dataset, (query.grouping?.source ?? query).where) }));
  if (target.topic === "parallelism") {
    // The record dataset uses saved counting/gap preferences. Inherit those
    // on entry, while resetting selections and setting the card's time range.
    parsed.searchParams.set("analyze_parallelism", JSON.stringify(window?.mode === "today" ? { ...parallelismRibbon(1), range: "today" } : parallelismRibbon(window?.days)));
  }
  parsed.searchParams.set("card_query", recipe.dataset);
  parsed.searchParams.set("card_id", recipe.id);
  const cover = defaultCardCover(recipe);
  if (cover) parsed.searchParams.set("card_cover", JSON.stringify(cover));
  parsed.searchParams.set("card_title", recipe.title);
  parsed.searchParams.set("card_description", recipe.description);
  return parsed.pathname + parsed.search;
}
