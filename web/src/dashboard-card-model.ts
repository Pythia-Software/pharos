import { EMPTY_QUERY, decodeQueryStrict, encodeQuery, type FieldSchema, type WhereTerm } from "@pythia-software/query-table-core";
import { analysisCatalogFor, analysisCatalogs } from "./analysis-catalogs";
import { isTrendView, barsShown, canonicalState, normalizeAnalysis, slicesShown, validAnalysisState, type AnalysisCatalog, type AnalysisState } from "./dashboard-analysis-model";

import { dashboardDefaults } from "./dashboard-defaults";
import { readDashboardLink } from "./usage-dashboard-model";
import { readUsageWorkspace, usageTopicDatasets, usageTopics, usageWorkspaceURL, type UsageDataset } from "./usage-workspace-model";
import { cardLabels, collectDurableQuery, readBookmarks, writeBookmarks, type Bookmark, type CardLabels } from "./bookmarks";
import { cardGroupsMax, defaultCardCover, pluralNoun, validCardGroups, type CardGroups } from "./card-cover";
import type { QuerySuggestion } from "./curiosity-data";
import type { QueryCategory } from "./query-categories";
import { fittedCoverHeight, type CardLoadingSpec } from "./card-loading-model";
import { normalizeConcurrencySettings, readConcurrencySettings } from "./concurrency-settings";
import { ribbonCardDescription, ribbonCardTitle, ribbonCardWindow } from "./parallelism-ribbon-card";
import { habitsCardDescription, habitsCardPresentation, habitsCardTargets, habitsCardTitle, habitsCardWindow, isHabitsCardTarget, readHabitsCardView, type HabitsCardTarget } from "./habits-card";
import { isThinkingCardAddress, isThinkingCardTarget, readThinkingCard, thinkingCardDescription, thinkingCardPresentation, thinkingCardTitle, thinkingCardWindow, thinkingTopicTargets, type ThinkingCardTarget } from "./thinking-card";
import { analysisCardCopy, analysisCardTitle, dashboardCardCopy, type DashboardCardCopy } from "./dashboard-card-copy";

// The dashboard URL is the card definition. Keep requested filters relative;
// execution (including Today) resolves them anew whenever the card refreshes.
export function dashboardCardURL(address: string, catalog: AnalysisCatalog, state: AnalysisState, where: WhereTerm[]): string {
  const url = new URL(address, location.origin), { topic } = readUsageWorkspace(url.searchParams);
  url.searchParams.set("analyze_chart", JSON.stringify(canonicalState(catalog, state)));
  url.searchParams.set("analyze_filters", encodeQuery({ ...EMPTY_QUERY, where }));
  if (catalog.request?.params?.carbon_settings) url.searchParams.set("analyze_carbon", catalog.request.params.carbon_settings);
  if (topic === "parallelism" && !url.searchParams.has("analyze_parallelism")) url.searchParams.set("analyze_parallelism", JSON.stringify(readConcurrencySettings()));
  return pageCardURL(url.href);
}

// A page without a catalog chart (Waiting, Thinking) is its
// own definition: its address, less what only this visit chose.
export function pageCardURL(address: string): string {
  const url = new URL(address, location.origin), { topic } = readUsageWorkspace(url.searchParams);
  for (const key of [...url.searchParams.keys()]) if (key.startsWith("card_") || ["overlay", "history_search", "history_collapsed", "highlight", "reveal", "find", "find_case", "find_regex", "chart_pins", "habits_day"].includes(key)) url.searchParams.delete(key);
  return usageWorkspaceURL({ mode: "dashboards", topic, dataset: usageTopicDatasets[topic] }, url.searchParams);
}

// One dashboard URL can hold several charts: Parallelism draws its ribbon
// above the catalog chart, and Habits its sections. A card names the one it
// shows; "chart", the catalog chart, is the default and is never stored.
export type DashboardCardTarget = "chart" | "ribbon" | HabitsCardTarget | ThinkingCardTarget;
export function dashboardCardTargets(address: string, schema?: FieldSchema): Array<{ id: DashboardCardTarget; label: string; title: string }> {
  // Each is labelled with the heading it has on the page.
  if (isThinkingCardAddress(address)) {
    const { topic, state } = readThinkingCard(address);
    return thinkingTopicTargets(topic).map(id => ({ id, label: thinkingCardTitle(id, state), title: dashboardCardMetadata(address, id, schema).title }));
  }
  const { topic } = readDashboardCard(address), title = dashboardCardMetadata(address, "chart", schema).title;
  const charts: Array<{ id: DashboardCardTarget; label: string; title: string }> = [{ id: "chart", label: title, title }];
  if (topic === "habits") {
    const view = readHabitsCardView(new URL(address, location.origin).searchParams);
    return [...habitsCardTargets.map(id => ({ id, label: habitsCardTitle(id, view), title: dashboardCardMetadata(address, id, schema).title })), ...charts];
  }
  if (topic !== "parallelism") return charts;
  const ribbon = ribbonCardTitle(dashboardCardRibbon(address));
  return [{ id: "ribbon", label: ribbon, title: dashboardCardMetadata(address, "ribbon", schema).title }, ...charts];
}

// The ribbon's settings travel in the URL; without them it opens on the
// saved settings, as the page does.
export function dashboardCardRibbon(address: string) {
  const encoded = new URL(address, location.origin).searchParams.get("analyze_parallelism");
  if (encoded === null) return readConcurrencySettings();
  try { return normalizeConcurrencySettings(JSON.parse(encoded)); } catch { throw new Error("This dashboard card's ribbon settings are invalid."); }
}

// The records a dashboard card reads, for any dashboard page.
export function dashboardCardSource(address: string): { dataset: UsageDataset; where: WhereTerm[] } {
  if (isThinkingCardAddress(address)) return { dataset: "model_requests", where: readThinkingCard(address).where };
  const { dataset, where } = readDashboardCard(address);
  return { dataset, where };
}

export function readDashboardCard(address: string, now = new Date()) {
  const url = new URL(address, location.origin), { topic, mode } = readUsageWorkspace(url.searchParams), catalog = analysisCatalogs[topic];
  if (url.origin !== location.origin || url.pathname !== "/analyze" || mode !== "dashboards" || !catalog || !Object.hasOwn(usageTopics, url.searchParams.get("analyze_topic") ?? "")) throw new Error("This dashboard card is unavailable.");
  const opened = readDashboardLink(url.searchParams, dashboardDefaults[topic], state => validAnalysisState(catalog, state), catalog.time, undefined, now);
  if (opened.error) throw new Error(opened.error);
  return { topic, dataset: usageTopicDatasets[topic], state: normalizeAnalysis(catalog, opened.view), where: opened.where, params: url.searchParams };
}

export function dashboardCardTitle(catalog: AnalysisCatalog, state: AnalysisState): string {
  return analysisCardTitle(catalog, state);
}

export function collectDashboardCard(url: string, title: string, description: string, target: DashboardCardTarget = "chart", labels: CardLabels = {}, templateId?: string, groups?: number): Bookmark {
  if (!dashboardCardTargets(url).some(item => item.id === target)) throw new Error("This chart cannot be a card.");
  if (!title.trim() || title.trim().length > 200 || description.length > 500) throw new Error("Use a title up to 200 characters and a description up to 500.");
  if (groups !== undefined && (!dashboardCardGroups(url, target) || !validCardGroups(groups))) throw new Error("Choose a whole number of groups to show.");
  const items = readBookmarks();
  const item: Bookmark = { id: crypto.randomUUID(), dashboard: true, url, ...target === "chart" ? {} : { dashboardTarget: target }, title: title.trim(), description: description.trim(), ...templateId ? { templateId } : {}, ...dashboardCardLabels(url, target, labels), ...groups !== undefined ? { maxGroups: groups } : {}, savedAt: Date.now() };
  writeBookmarks([...items, item]);
  return item;
}

// Every dashboard card's call to action unless its author names another.
export const DASHBOARD_CARD_ACTION = "Explore this dashboard";

// The editors show each label's effective value; one equal to its default is
// not stored, so the card keeps following the chart it shows.
export function dashboardCardLabels(url: string, target: DashboardCardTarget = "chart", labels: CardLabels): CardLabels {
  const chosen = cardLabels(labels);
  if (chosen.action === DASHBOARD_CARD_ACTION) delete chosen.action;
  try { if (chosen.category === dashboardCardPresentation(url, target).category) delete chosen.category; } catch { /* An invalid definition keeps the chosen category. */ }
  return chosen;
}

// A template becomes a saved query, or for a Habits section the dashboard
// card its page saves, either way remembering the template it came from.
export async function collectCardTemplate(item: QuerySuggestion): Promise<Bookmark> {
  if (item.habits) return collectDashboardCard(item.url, item.title, item.description, item.habits.target, { action: item.action }, item.instanceId);
  return collectDurableQuery(item.dataset, item.query, item.title, { description: item.description, templateId: item.instanceId, window: item.window, cover: item.cover ?? defaultCardCover(item), url: item.url });
}

// Ask offers a chart's own address as a card; the same address is collected once.
export function dashboardCardCollected(url: string, target: DashboardCardTarget = "chart"): boolean {
  return readBookmarks().some(item => item.dashboard && item.url === url && (item.dashboardTarget ?? "chart") === target);
}

// All creation entry points use this contract: title and context come from the
// selected target's saved controls and population. Bespoke targets must describe
// their own inputs; catalog additions inherit the shared implementation.
export function dashboardCardMetadata(address: string, target: DashboardCardTarget = "chart", schema?: FieldSchema, groups?: number): DashboardCardCopy {
  if (isThinkingCardTarget(target)) {
    const { state, filters } = readThinkingCard(address), where = filters ? decodeQueryStrict(filters)!.where : [];
    return dashboardCardCopy(thinkingCardTitle(target, state), [thinkingCardWindow(state), thinkingCardDescription(target, state)], where, schema, ["requested_at"]);
  }
  if (target === "ribbon") {
    const settings = dashboardCardRibbon(address);
    return dashboardCardCopy(ribbonCardTitle(settings), [ribbonCardWindow(settings), ribbonCardDescription(settings)]);
  }
  const { topic, state, params, where } = readDashboardCard(address);
  const catalog = topic === "parallelism" ? analysisCatalogFor(topic, { parallelism: dashboardCardRibbon(address).counting })! : analysisCatalogs[topic]!;
  if (isHabitsCardTarget(target)) {
    const view = readHabitsCardView(params);
    // These sections read /api/habits, which does not use analyze_filters.
    return dashboardCardCopy(habitsCardTitle(target, view), [habitsCardDescription(target, view)]);
  }
  const window = dashboardCardWindow(address);
  const population = window === "Today" ? where.filter(term => !("field" in term && [catalog.time.first, catalog.time.last].includes(term.field) && !term.negated && [">=", ">", "<", "<="].includes(term.op))) : where;
  return analysisCardCopy(catalog, state, population, schema, groups ?? dashboardCardGroups(address)?.fallback,
    [window, ...dashboardSettingsContext(topic, params)]);
}

function dashboardSettingsContext(topic: string, params: URLSearchParams): string[] {
  if (topic === "parallelism") {
    const settings = dashboardCardRibbon(`/analyze?${params}`);
    return [`Count ${settings.counting === "agent" ? "top-level sessions" : "workspaces"}; join turns across gaps up to ${settings.gap} minutes`];
  }
  if (topic === "carbon" && params.has("analyze_carbon")) {
    const settings = JSON.parse(params.get("analyze_carbon")!);
    if (!settings || typeof settings !== "object" || Array.isArray(settings)) throw new Error("This card's Carbon assumptions are invalid.");
    return [
      typeof settings.scenario === "string" ? `Energy estimate: ${settings.scenario}` : "",
      typeof settings.pueScenario === "string" ? `Facility overhead: ${settings.pueScenario}` : "",
      typeof settings.grid === "string" ? `Grid: ${settings.grid === "custom" ? `${Math.max(0, Number(settings.customGrid) || 0)} g CO₂e/kWh` : settings.grid.replaceAll("_", " ")}` : "",
      settings.inputSensitivity === "third" ? "Watershed input sensitivity" : settings.inputSensitivity === "current" ? "Labs input sensitivity" : "",
    ].filter(Boolean);
  }
  return [];
}

export function dashboardCardDescription(address: string, target: DashboardCardTarget = "chart", schema?: FieldSchema, groups?: number): string {
  return dashboardCardMetadata(address, target, schema, groups).description;
}

export function dashboardCardWindow(address: string, target: DashboardCardTarget = "chart"): string {
  if (target === "ribbon") return ribbonCardWindow(dashboardCardRibbon(address));
  if (isHabitsCardTarget(target)) return habitsCardWindow(target, readHabitsCardView(readDashboardCard(address).params));
  if (isThinkingCardTarget(target)) return thinkingCardWindow(readThinkingCard(address).state);
  const { params, where, topic } = readDashboardCard(address), time = analysisCatalogs[topic]!.time;
  if (params.get("analyze_window") === "today") return "Today";
  const dates = where.filter(term => "field" in term && [time.first, time.last].includes(term.field));
  const relative = dates.find(term => "field" in term && term.op === ">=" && /^-\d+d$/.test(term.value));
  return relative && "field" in relative ? `Past ${relative.value.slice(1, -1)} days` : dates.length ? "Saved range" : "All time";
}

// A card can show more or fewer of the chart's groups than its dashboard,
// which keeps its own count and its full list. Only the catalog chart's
// breakdowns, over time or compared, have groups to count.
export function dashboardCardGroups(address: string, target: DashboardCardTarget = "chart"): CardGroups | undefined {
  if (target !== "chart") return;
  const opened = readDashboardCard(address), { topic } = opened;
  const catalog = topic === "parallelism" ? analysisCatalogFor(topic, { parallelism: dashboardCardRibbon(address).counting })! : analysisCatalogs[topic]!;
  const state = normalizeAnalysis(catalog, opened.state), split = catalog.breakdowns[state.split];
  if (!split || !isTrendView(state.view) && !["compare", "distribution"].includes(state.view)) return;
  if (isTrendView(state.view) && analysisCatalogs[topic]!.trend.allSeries) return;
  const fallback = isTrendView(state.view) ? state.seriesLimit! : state.view === "compare" && state.compareAs === "share" ? Math.max(slicesShown, (state.selection?.values.length ?? 0) + 1) : barsShown;
  return { noun: pluralNoun(split.label.toLowerCase()), max: Math.max(cardGroupsMax, fallback), fallback };
}

export function dashboardCardPresentation(address: string, target: DashboardCardTarget = "chart", groups?: number): { category: QueryCategory; loading: CardLoadingSpec } {
  if (target === "ribbon") return { category: "machines", loading: { kind: "columns", count: 24, height: fittedCoverHeight("columns", 24) } };
  if (isHabitsCardTarget(target)) return habitsCardPresentation(target, readHabitsCardView(readDashboardCard(address).params));
  if (isThinkingCardTarget(target)) return thinkingCardPresentation(target, readThinkingCard(address).state);
  const { topic, state } = readDashboardCard(address);
  const category: QueryCategory = topic === "tokens" ? state.metric.includes("cost") ? "cost" : "usage"
    : topic === "compaction" ? "thinking" : topic === "carbon" ? "carbon" : topic === "human" ? "human" : topic === "skills" ? "skills"
    : topic === "activity" ? "models" : topic === "time" || topic === "parallelism" || topic === "habits" ? "machines" : "tools";
  const kind = isTrendView(state.view) ? state.view === "trend_line" || state.view !== "trend_bar" && state.display === "cumulative" ? "line" : "columns" : state.view === "distribution" || state.view === "cycle" && state.reduce === "median" ? "box" : state.view === "scatter" ? "scatter" : state.view === "cycle" ? "columns" : state.compareAs === "share" ? "pie" : "list";
  const count = state.view === "cycle" ? state.cycle === "hour" ? 24 : 7 : kind === "columns" ? 12 : Math.min(groups ?? 4, cardGroupsMax);

  return { category, loading: { kind, count, height: fittedCoverHeight(kind, count) } };
}
