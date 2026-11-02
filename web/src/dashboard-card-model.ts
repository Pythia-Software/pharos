import { EMPTY_QUERY, encodeQuery, type WhereTerm } from "@pythia-software/query-table-core";
import { analysisCatalogs } from "./analysis-catalogs";
import { canonicalState, normalizeAnalysis, validAnalysisState, type AnalysisCatalog, type AnalysisState } from "./dashboard-analysis-model";
import { dashboardDefaults } from "./dashboard-defaults";
import { readDashboardLink } from "./usage-dashboard-model";
import { readUsageWorkspace, usageTopicDatasets, usageTopics, usageWorkspaceURL } from "./usage-workspace-model";
import { readBookmarks, writeBookmarks, type Bookmark } from "./bookmarks";
import type { QueryCategory } from "./query-categories";
import type { CardLoadingSpec } from "./card-loading-model";
import { readConcurrencySettings } from "./concurrency-settings";

// The dashboard URL is the card definition. Keep requested filters relative;
// execution (including Today) resolves them anew whenever the card refreshes.
export function dashboardCardURL(address: string, catalog: AnalysisCatalog, state: AnalysisState, where: WhereTerm[]): string {
  const url = new URL(address, location.origin), { topic } = readUsageWorkspace(url.searchParams);
  url.searchParams.set("analyze_chart", JSON.stringify(canonicalState(catalog, state)));
  url.searchParams.set("analyze_filters", encodeQuery({ ...EMPTY_QUERY, where }));
  if (catalog.request?.params?.carbon_settings) url.searchParams.set("analyze_carbon", catalog.request.params.carbon_settings);
  if (topic === "parallelism" && !url.searchParams.has("analyze_parallelism")) url.searchParams.set("analyze_parallelism", JSON.stringify(readConcurrencySettings()));
  for (const key of [...url.searchParams.keys()]) if (key.startsWith("card_") || ["overlay", "history_search", "history_collapsed", "highlight", "reveal", "chart_pins"].includes(key)) url.searchParams.delete(key);
  return usageWorkspaceURL({ mode: "dashboards", topic, dataset: usageTopicDatasets[topic] }, url.searchParams);
}

export function readDashboardCard(address: string, now = new Date()) {
  const url = new URL(address, location.origin), { topic, mode } = readUsageWorkspace(url.searchParams), catalog = analysisCatalogs[topic];
  if (url.origin !== location.origin || url.pathname !== "/analyze" || mode !== "dashboards" || !catalog || !Object.hasOwn(usageTopics, url.searchParams.get("analyze_topic") ?? "")) throw new Error("This dashboard card is unavailable.");
  const opened = readDashboardLink(url.searchParams, dashboardDefaults[topic], state => validAnalysisState(catalog, state), catalog.time, undefined, now);
  if (opened.error) throw new Error(opened.error);
  return { topic, dataset: usageTopicDatasets[topic], state: normalizeAnalysis(catalog, opened.view), where: opened.where, params: url.searchParams };
}

export function dashboardCardTitle(catalog: AnalysisCatalog, state: AnalysisState): string {
  const view = normalizeAnalysis(catalog, state), measure = catalog.measures[view.metric].label;
  const split = catalog.breakdowns[view.split]?.label;
  return `${measure}${view.view === "trend" ? " over time" : view.view === "cycle" ? ` by ${view.cycle === "hour" ? "hour of day" : "weekday"}` : split ? ` by ${split.toLowerCase()}` : ""}`;
}

export function collectDashboardCard(url: string, title: string, description: string): Bookmark {
  readDashboardCard(url);
  if (!title.trim() || title.trim().length > 200 || description.length > 500) throw new Error("Use a title up to 200 characters and a description up to 500.");
  const items = readBookmarks();
  const item: Bookmark = { id: crypto.randomUUID(), dashboard: true, url, title: title.trim(), description: description.trim(), savedAt: Date.now() };
  writeBookmarks([...items, item]);
  return item;
}

export function dashboardCardDescription(address: string): string {
  return usageTopics[readDashboardCard(address).topic].description;
}

export function dashboardCardWindow(address: string): string {
  const { params, where, topic } = readDashboardCard(address), time = analysisCatalogs[topic]!.time;
  if (params.get("analyze_window") === "today") return "Today";
  const dates = where.filter(term => "field" in term && [time.first, time.last].includes(term.field));
  const relative = dates.find(term => "field" in term && term.op === ">=" && /^-\d+d$/.test(term.value));
  return relative && "field" in relative ? `Past ${relative.value.slice(1, -1)} days` : dates.length ? "Saved range" : "All time";
}

export function dashboardCardPresentation(address: string): { category: QueryCategory; loading: CardLoadingSpec } {
  const { topic, state } = readDashboardCard(address);
  const category: QueryCategory = topic === "tokens" ? state.metric.includes("cost") ? "cost" : "usage"
    : topic === "carbon" ? "carbon" : topic === "human" ? "human" : topic === "skills" ? "skills"
    : topic === "activity" ? "models" : topic === "time" || topic === "parallelism" ? "machines" : "tools";
  const kind = state.view === "trend" ? state.display === "cumulative" ? "line" : "columns" : state.view === "distribution" || state.view === "cycle" && state.reduce === "median" ? "box" : state.view === "scatter" ? "scatter" : state.view === "cycle" ? "columns" : state.compareAs === "share" ? "pie" : "list";
  return { category, loading: { kind, count: state.view === "cycle" ? state.cycle === "hour" ? 24 : 7 : kind === "columns" ? 12 : 4 } };
}
