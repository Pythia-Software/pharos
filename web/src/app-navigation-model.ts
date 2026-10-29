import { nestedPageTrail, type NestedPage } from "./nested-pages-model";
import { isUsageWorkspace, readUsageWorkspace, usageDatasets, usageDatasetTopics, usageTopics, usageWorkspaceURL, type UsageMode } from "./usage-workspace-model";

export const findingsPages: readonly NestedPage[] = [
  { id: "findings:dashboard", label: "Dashboard", href: "/findings" },
  { id: "findings:table", label: "Table", href: "/findings?view=table" },
  { id: "findings:settings", label: "Settings", href: "/findings?view=settings" },
];

export function optimizePages(): NestedPage[] {
  return [
    { id: "findings", label: "Findings", children: findingsPages },
  ];
}

export function analyzePages(params: URLSearchParams): NestedPage[] {
  return (["dashboards", "query"] as UsageMode[]).map(mode => ({
    id: mode, label: mode === "dashboards" ? "Dashboards" : "Query", children: mode === "dashboards"
      ? Object.entries(usageTopics).map(([topic, item]) => ({ id: `dashboards:${topic}`, label: item.label, href: usageWorkspaceURL({ mode, topic: topic as keyof typeof usageTopics, report: "" }, params) }))
      : Object.entries(usageDatasets).map(([dataset, item]) => ({ id: `query:${dataset}`, label: item.label, href: usageWorkspaceURL({ mode, dataset: dataset as keyof typeof usageDatasets, topic: usageDatasetTopics[dataset as keyof typeof usageDatasets], report: "" }, params) })),
  }));
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
  if (path.startsWith("/findings")) return { section: "findings", current: `findings:${params.get("view") === "settings" ? "settings" : params.get("view") === "table" ? "table" : "dashboard"}` };
  if (path.startsWith("/settings")) return { section: "settings", current: path };
  if (path === "/tools") return { section: "analyze", current: `query:${params.get("tools_view") === "summary" ? "tools" : params.get("tools_view") === "skills" ? "skill_usages" : "tool_calls"}` };
  if (path === "/analyze" || path === "/mcp") {
    if (path === "/mcp") return { section: "analyze", current: "dashboards:mcp" };
    if (isUsageWorkspace(params)) {
      const state = readUsageWorkspace(params);
      return { section: "analyze", current: `${state.mode}:${state.mode === "dashboards" ? state.topic : state.dataset}` };
    }
    const view = params.get("analyze_view"), machine = params.get("machine_view");
    const current = view === "conversations" ? "query:conversation_activity" : view === "writing"
      ? params.get("writing") === "messages" ? "query:writing_messages" : "dashboards:human"
      : `dashboards:${view === "carbon" ? "carbon" : machine === "time" ? "time" : machine === "parallelism" ? "parallelism" : "tokens"}`;
    return { section: "analyze", current };
  }
  return { section: path === "/" || path === "/home" ? "home" : "library", current: "" };
}

export function appLocationLabel(sections: readonly AppSection[], selected: ReturnType<typeof appLocation>) {
  const section = sections.find(item => item.id === selected.section) ?? sections[0];
  return { section, page: selected.current === "/cards" ? { id: "/cards", label: "Card Library", href: "/cards" } : nestedPageTrail(section.pages, selected.current).at(-1) };
}
