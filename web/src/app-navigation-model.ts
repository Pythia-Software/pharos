import { nestedPageTrail, type NestedPage } from "./nested-pages-model";
import { safetyURL } from "./safety-route";
import { logsRoute, logSourceFromParams, logSourceNavigationID } from "./logs-model";
import { agentEventDatasets, agentEventTopics, agentEventsEnabled } from "./agent-events-flag";
import { canonicalAnalyzeURL, readUsageWorkspace, usageDatasets, usageDatasetTopics, usageTopics, usageWorkspaceURL, type UsageMode } from "./usage-workspace-model";

export const findingsPages: readonly NestedPage[] = [
  { id: "findings:dashboard", label: "Dashboard", href: "/findings" },
  { id: "findings:table", label: "Table", href: "/findings?view=table" },
  { id: "findings:settings", label: "Settings", href: "/findings?view=settings" },
];
export const safetyPages: readonly NestedPage[] = [
  { id: "safety:dashboard", label: "Dashboard", href: safetyURL() },
  { id: "safety:table", label: "Table", href: safetyURL("table") },
  { id: "safety:settings", label: "Settings", href: safetyURL("settings") },
];

export function optimizePages(): NestedPage[] {
  return [
    { id: "findings", label: "Efficiency", children: findingsPages },
    { id: "safety", label: "Risky actions", children: safetyPages },
  ];
}

// Preview pages (agent-events-flag.ts) are listed only when turned on.
export function analyzePages(params: URLSearchParams, previews = agentEventsEnabled()): NestedPage[] {
  const pages: NestedPage[] = (["dashboards", "query"] as UsageMode[]).map(mode => ({
    id: mode, label: mode === "dashboards" ? "Dashboards" : "Query", children: mode === "dashboards"
      ? Object.entries(usageTopics).filter(([topic]) => previews || !agentEventTopics.has(topic)).map(([topic, item]) => ({ id: `dashboards:${topic}`, label: item.label, href: usageWorkspaceURL({ mode, topic: topic as keyof typeof usageTopics, report: "" }, params) }))
      : Object.entries(usageDatasets).filter(([dataset]) => previews || !agentEventDatasets.has(dataset)).map(([dataset, item]) => ({ id: `query:${dataset}`, label: item.label, href: usageWorkspaceURL({ mode, dataset: dataset as keyof typeof usageDatasets, topic: usageDatasetTopics[dataset as keyof typeof usageDatasets], report: "" }, params) })),
  }));
  if (typeof document !== "undefined" && document.documentElement.hasAttribute("data-label-trajectories")) pages.push({id:"label-trajectories",label:"Conversation shapes · lab",href:"/analyze?label_lab=fingerprints"});
  return pages;
}

export type AppSection = { id: string; label: string; href: string; pages: readonly NestedPage[] };
export type NavigationExpansion = Readonly<Record<string, Readonly<Record<string, boolean>>>>;

/** Groups start open until the user toggles them, including groups loaded later. */
export function isNavigationSectionExpanded(expansion: NavigationExpansion[string], page: string): boolean {
  return expansion[page] ?? true;
}

export function toggleNavigationSection(expansion: NavigationExpansion, section: string, page: string): NavigationExpansion {
  const expanded = expansion[section] ?? {};
  return { ...expansion, [section]: { ...expanded, [page]: !isNavigationSectionExpanded(expanded, page) } };
}

export function appSections(settings: readonly NestedPage[], params: URLSearchParams): AppSection[] {
  return [
    { id: "home", label: "Home", href: "/", pages: [] },
    { id: "library", label: "Search", href: "/library", pages: [] },
    { id: "analyze", label: "Analyze", href: "/analyze", pages: analyzePages(params) },
    { id: "findings", label: "Optimize", href: "/findings", pages: optimizePages() },
    { id: "settings", label: "Settings", href: "/settings", pages: settings },
  ];
}

/** Resolve old analysis links too: opening navigation never rewrites a query. */
export function appLocation(path: string, params: URLSearchParams) {
  if (path === "/cards") return { section: "home", current: "/cards" };
  if (path === "/settings/optimization") return { section: "findings", current: "findings:settings" };
  if (path === logsRoute) return { section: "settings", current: logSourceNavigationID(logSourceFromParams(params)) };
  if (path.startsWith("/findings")) return { section: "findings", current: `${params.get("optimize_area") === "safety" ? "safety" : "findings"}:${params.get("view") === "settings" ? "settings" : params.get("view") === "table" ? "table" : "dashboard"}` };
  if (path.startsWith("/settings")) return { section: "settings", current: path };
  if (path === "/mcp") return { section: "analyze", current: "dashboards:mcp" };
  if (path === "/analyze" || path === "/tools") {
    if (params.has("label_lab")) return {section:"analyze",current:params.get("label_lab")==="distribution"?(params.get("label_audience")==="human"?"label-human":params.get("label_audience")==="thinking"?"label-thinking":"label-distribution"):"label-trajectories"};
    const state = readUsageWorkspace(new URL(canonicalAnalyzeURL(path, params), "http://localhost").searchParams);
    return { section: "analyze", current: `${state.mode}:${state.mode === "dashboards" ? state.topic : state.dataset}` };
  }
  return { section: path === "/" || path === "/home" || path === "/ask" ? "home" : "library", current: "" };
}

export function appLocationLabel(sections: readonly AppSection[], selected: ReturnType<typeof appLocation>) {
  const section = sections.find(item => item.id === selected.section) ?? sections[0];
  return { section, page: selected.current === "/cards" ? { id: "/cards", label: "Card Library", href: "/cards" } : nestedPageTrail(section.pages, selected.current).at(-1) };
}
