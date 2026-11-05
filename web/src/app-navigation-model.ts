import { loadSchema, type QueryState } from "@pythia-software/query-table-core";
import { schemaDocuments } from "./schema-documents";
import { fittedDefaultQuery } from "./usage-query-columns";
import { nestedPageTrail, type NestedPage } from "./nested-pages-model";
import { safetyURL } from "./safety-route";
import { fixesURL } from "./fixes-route";
import { logsRoute, logSourceFromParams, logSourceNavigationID } from "./logs-model";
import { agentEventDatasets, agentEventTopics, agentEventsEnabled } from "./agent-events-flag";
import { canonicalAnalyzeURL, readUsageWorkspace, analyzeQueryDatasets, usageDatasets, usageDatasetTopics, usageTopicDatasets, usageTopics, usageWorkspaceURL, type UsageDataset, type UsageTopic } from "./usage-workspace-model";

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

export const fixesPages: readonly NestedPage[] = [
  { id: "fixes:dashboard", label: "Dashboard", href: fixesURL() },
  { id: "fixes:table", label: "Table", href: fixesURL("table") },
  { id: "fixes:settings", label: "Settings", href: fixesURL("settings") },
];

export function optimizePages(): NestedPage[] {
  return [
    { id: "findings", label: "Efficiency", children: findingsPages },
    { id: "safety", label: "Risky actions", children: safetyPages },
    { id: "fixes", label: "Fixes", children: fixesPages },
  ];
}

// Every dashboard has one home; adding a topic requires choosing its section.
const dashboardGroups: Record<UsageTopic, "People" | "Agents" | "Outcomes"> = {
  activity: "People", human: "People", work_hours: "People", habits: "People",
  compaction: "Agents", skills: "Agents", calls: "Agents", tools: "Agents", time: "Agents",
  parallelism: "Agents", mcp: "Agents", agent_experience: "Agents", labels: "Agents", session_shape: "Agents", agent_event_rates: "Agents",
  "thinking-overview": "Agents", "thinking-detail": "Agents",
  tokens: "Outcomes", carbon: "Outcomes", scatter: "Outcomes", outcomes: "Outcomes",
};

// Query tables are grouped by the records they contain, not dashboard topic.
const queryGroups = [
  { id: "conversations", label: "Conversations & agent sessions" },
  { id: "tools", label: "Tools & context" },
  { id: "usage", label: "Usage & activity" },
  { id: "events", label: "Agent events" },
] as const;
const queryDatasetGroups: Record<(typeof analyzeQueryDatasets)[number], (typeof queryGroups)[number]["id"]> = {
  messages: "conversations", message_labels: "conversations", session_shape: "conversations",
  tl1_attempts: "conversations", conversation_activity: "conversations",
  tool_calls: "tools", compactions: "tools", skill_usages: "tools", mcp_calls: "tools",
  model_requests: "usage", usage: "usage", rhythm: "usage", parallelism: "usage",
  agent_events: "events", agent_event_messages: "events", agent_event_rates: "events",
};

// Schema defaults are immutable; loading all schemas on every URL edit makes
// navigation needlessly repeat the same work.
const datasetQueries = new Map<UsageDataset, QueryState>(analyzeQueryDatasets.map(dataset => [dataset, fittedDefaultQuery(loadSchema(schemaDocuments[dataset]))]));

// Preview pages (agent-events-flag.ts) are listed only when turned on; Agent
// Events is not one of them.
export function analyzePages(params: URLSearchParams, previews = agentEventsEnabled()): NestedPage[] {
  const datasetContext = new URLSearchParams(params);
  for (const key of [...datasetContext.keys()]) if (/^(?:q_|record_|window_|analyze_|card_|label_)/.test(key)) datasetContext.delete(key);
  return [
    { id: "dashboards", label: "Dashboards", children: (["People", "Agents", "Outcomes"] as const).map(label => ({
      // A group's id differs from its dashboards' (dashboards:<topic>): the
      // Outcomes group holds the Outcomes dashboard.
      id: `dashboard-group:${label.toLowerCase()}`, label,
      children: Object.entries(usageTopics).filter(([topic]) => dashboardGroups[topic as UsageTopic] === label && (previews || !agentEventTopics.has(topic)))
        .map(([topic, item]) => ({ id: `dashboards:${topic}`, label: item.label, href: usageWorkspaceURL({ mode: "dashboards", topic: topic as UsageTopic, dataset: usageTopicDatasets[topic as UsageTopic], report: "" }, params) })),
    })) },
    { id: "query", label: "Query tables", children: queryGroups.map(group => ({
      id: `query-group:${group.id}`, label: group.label,
      children: analyzeQueryDatasets.filter(dataset => queryDatasetGroups[dataset] === group.id && (previews || !agentEventDatasets.has(dataset)))
        .map(dataset => ({ id: `query:${dataset}`, label: usageDatasets[dataset].label, href: usageWorkspaceURL({ mode: "query", dataset, topic: usageDatasetTopics[dataset], report: "", query: datasetQueries.get(dataset)! }, datasetContext) })),
    })).filter(group => group.children.length),
    },
  ];

}

export type AppSection = { id: string; label: string; href: string; pages: readonly NestedPage[] };
export type NavigationExpansion = Readonly<Record<string, Readonly<Record<string, boolean>>>>;

/** Keep explicit choices; deeper groups can default to the current page's path. */
export function isNavigationSectionExpanded(expansion: NavigationExpansion[string], page: string, defaultOpen = true): boolean {
  return expansion[page] ?? defaultOpen;
}

export function toggleNavigationSection(expansion: NavigationExpansion, section: string, page: string, defaultOpen = true): NavigationExpansion {
  const expanded = expansion[section] ?? {};
  return { ...expansion, [section]: { ...expanded, [page]: !isNavigationSectionExpanded(expanded, page, defaultOpen) } };
}

/** Sidebar presentation changes keep the navigation picker open. */
export function navigationPageURL(href: string): string {
  const url = new URL(href);
  url.searchParams.delete("nav_expanded");
  url.searchParams.delete("nav_collapsed");
  return url.href;
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
  if (path.startsWith("/findings")) return { section: "findings", current: `${["safety", "fixes"].includes(params.get("optimize_area") ?? "") ? params.get("optimize_area") : "findings"}:${params.get("view") === "settings" ? "settings" : params.get("view") === "table" ? "table" : "dashboard"}` };
  if (path.startsWith("/settings")) return { section: "settings", current: path };
  if (path === "/mcp") return { section: "analyze", current: "dashboards:mcp" };
  if (path === "/analyze" || path === "/tools") {
    const state = readUsageWorkspace(new URL(canonicalAnalyzeURL(path, params), "http://localhost").searchParams);
    return { section: "analyze", current: `${state.mode}:${state.mode === "dashboards" ? state.topic : state.dataset}` };
  }
  return { section: path === "/" || path === "/home" || path === "/ask" ? "home" : "library", current: "" };
}

export function appLocationLabel(sections: readonly AppSection[], selected: ReturnType<typeof appLocation>) {
  const section = sections.find(item => item.id === selected.section) ?? sections[0];
  return { section, page: selected.current === "/cards" ? { id: "/cards", label: "Card Library", href: "/cards" } : nestedPageTrail(section.pages, selected.current).at(-1) };
}
