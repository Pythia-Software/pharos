import { localTimezone } from "./day-window";
import { EMPTY_QUERY, decodeQueryStrict, encodeQuery, predicatesOf, type QueryState, type WhereTerm } from "@pythia-software/query-table-core";
import { analysisVersion } from "./dashboard-analysis-model";

// Analyze owns guided dashboards and the full query surfaces. Explicit older
// analysis URLs remain available while their specialized controls migrate.
export const usageDatasets = {
  usage: { label: "Token usage", description: "Tokens, API costs, models, cache, and pricing coverage.", field: "model", searchLabel: "Token Usage Model Names" },
  conversation_activity: { label: "Conversation activity", description: "Human input and recorded output across conversations.", field: "root_model_family", searchLabel: "Top-level Conversation Model Names" },
  writing: { label: "Human words by workspace", description: "Authorship totals across each workspace’s conversations and folded delegated work.", field: "title", searchLabel: "Workspace Titles" },
  writing_messages: { label: "Human messages", description: "Individual messages and their authorship evidence.", field: "text", searchLabel: "Conversation Message Text" },
  tool_calls: { label: "Tool calls", description: "Commands, results, timing, status, and context added or carried.", field: "command", searchLabel: "Workspace Tool Command Text" },
  tools: { label: "Tool summary", description: "Tool use grouped by work, tool, and command.", field: "tool_name", searchLabel: "Workspace Tool Names" },
  mcp_calls: { label: "MCP calls", description: "Calls to Pharos's MCP server, sanitized arguments, output estimates, and timing.", field: "arguments_json", searchLabel: "MCP Call Arguments" },
  skill_usages: { label: "Skill evidence", description: "Invocations, file loads, retained bodies, and source evidence.", field: "skill_name", searchLabel: "Skill Names" },
  parallelism: { label: "Parallel activity", description: "Hour-by-agent records with the existing counting assumptions.", field: "model", searchLabel: "Parallel Activity Model Names" },
} as const;
export type UsageDataset = keyof typeof usageDatasets;
export type UsageMode = "dashboards" | "query";
export const usageTopics = {
  human: { label: "Human Words", description: "How much are you contributing? Explore your writing over time." },
  tokens: { label: "Tokens and Cost", description: "How much are agents consuming? Choose a measure, a breakdown, and a time range." },
  skills: { label: "Skills", description: "Where does skill evidence appear? Compare observed invocations and loads." },
  tools: { label: "Tools", description: "Understand tool mix, reliability, and context overhead. Compare groups or follow changes over time." },
  time: { label: "Agent Time", description: "Where is recorded tool time going? Compare activities over time." },
  parallelism: { label: "Parallelism", description: "Explore overlapping conversations and estimated model activity. Select a ribbon or drag to zoom." },
  mcp: { label: "Pharos MCP", description: "How are agents using Pharos? Explore calls, reliability, response sizes, and time spent serving requests." },
  carbon: { label: "Carbon Impact", description: "Estimated electricity and emissions, with explicit assumptions." },
} as const;
export type UsageTopic = keyof typeof usageTopics;
export const usageDatasetTopics: Record<UsageDataset, UsageTopic> = {
  usage: "tokens", conversation_activity: "human", writing: "human", writing_messages: "human",
  tools: "tools", tool_calls: "tools", mcp_calls: "mcp", skill_usages: "skills", parallelism: "parallelism",
};
export const usageTopicDatasets: Record<UsageTopic, UsageDataset> = {
  tokens: "usage", human: "writing_messages", time: "tool_calls", parallelism: "parallelism", tools: "tools", mcp: "mcp_calls", skills: "skill_usages", carbon: "usage",
};

// Fields that hold a dashboard's dates, by dataset: its range, and the day
// predicates a link may translate it into (the tools rollup's text day).
const timeFields: Record<UsageDataset, string[]> = {
  usage: ["first_usage_at", "last_usage_at", "day", "hour", "week", "month"], conversation_activity: ["event_at"],
  writing: ["first_message_at", "last_message_at"], writing_messages: ["sent_at", "day", "week", "month"], tools: ["day", "week", "month"], tool_calls: ["started_at", "ended_at", "day", "week", "month"],
  mcp_calls: ["called_at", "day", "week", "month"], skill_usages: ["created_at", "day", "week", "month"], parallelism: ["started_at", "day", "hour_of_day", "week", "month"],
};
const builtWith: Partial<Record<UsageDataset, string[]>> = { parallelism: ["counting", "gap_minutes"] };
const termFields = (term: WhereTerm): string[] => predicatesOf(term).map(clause => clause.field);

// The dashboard's own filters, with the user's edits to the query a dashboard
// link opened. A link adds its own terms (a legend selection, a measure's
// population, the dataset's day predicates); those are not the dashboard's
// filters. So the way back keeps the dashboard's filters, less the opened
// terms the user removed, plus the terms the user added. Dates are compared
// as a whole: the opened query may state the range differently (whole days,
// rounded periods), so if the user changed any date predicate, the dashboard's
// own range is dropped and the edited query's dates become the range
// (dates: true), never intersected with the old one. Without a record of the
// opened query (older links), the query's own filters are the filters.
function returnedWhere(current: URLSearchParams, edited: WhereTerm[], dataset: UsageDataset): { where: WhereTerm[]; dates: boolean } {
  let opened: QueryState | null = null, dashboard: QueryState | null = null;
  try { opened = decodeQueryStrict(current.get("analyze_opened") ?? ""); dashboard = decodeQueryStrict(current.get("analyze_filters") ?? ""); } catch { return { where: edited, dates: false }; }
  if (!opened || !dashboard) return { where: edited, dates: false };
  const key = (term: WhereTerm) => JSON.stringify(term), dated = (term: WhereTerm) => termFields(term).some(field => timeFields[dataset].includes(field));
  const before = new Set(opened.where.map(key)), after = new Set(edited.map(key));
  const dates = JSON.stringify(opened.where.filter(dated).map(key).sort()) !== JSON.stringify(edited.filter(dated).map(key).sort());
  const kept = dashboard.where.filter(term => (!before.has(key(term)) || after.has(key(term))) && !(dates && dated(term)));
  const added = edited.filter(term => !before.has(key(term)) || dates && dated(term));
  return { where: [...kept, ...added.filter(term => !kept.some(other => key(other) === key(term)))], dates };
}

export function usageDashboardReturn(current: URLSearchParams) {
  const state = readUsageWorkspace(current), dataset = usageTopicDatasets[state.topic];
  const keywords = current.has(`record_${state.dataset}`);
  if (!keywords && current.get("card_id") && current.get("card_query") === state.dataset && current.has(`q_${state.dataset}`)) return {
    href: usageWorkspaceURL({ mode: "dashboards", dataset, search: false }, current), label: "Back to dashboard", resetsFilters: false,
  };
  let query: QueryState | undefined;
  try { query = decodeQueryStrict(current.get(`q_${state.dataset}`) ?? "") ?? undefined; } catch { /* Explicitly reset an unavailable query. */ }
  const compatible = dataset === state.dataset && Boolean(query);
  const params = new URLSearchParams(current);
  const returned = compatible ? returnedWhere(current, (query!.grouping?.source ?? query!).where, state.dataset) : { where: [], dates: false };
  // Settings a dataset's records are built with are not the dashboard's
  // filters: Parallelism's counting and gap follow the saved settings.
  const settings = builtWith[state.dataset] ?? [];
  if (settings.length) returned.where = returned.where.filter(term => !termFields(term).some(field => settings.includes(field)));
  if (returned.dates || !compatible) {
    params.delete("analyze_window");
    for (const key of [...params.keys()]) if (key.startsWith("window_")) params.delete(key);
  }
  params.set("analyze_filters", encodeQuery({ ...EMPTY_QUERY, where: returned.where }));
  return {
    href: usageWorkspaceURL({ mode: "dashboards", dataset, search: false }, params),
    label: !compatible ? "Back to dashboard (resets query filters)" : keywords ? "Back to dashboard (resets keyword filters)" : query!.grouping && query!.where.length ? "Back to dashboard (resets group filters)" : returned.dates ? "Back to dashboard (dates from this query)" : "Back to dashboard",
    resetsFilters: !compatible || keywords || Boolean(query?.grouping && query.where.length),
  };
}

export function usageAnalysisLink(dataset: UsageDataset, query: QueryState, target: string, current: URLSearchParams) {
  const url = new URL(target, "http://localhost");
  // The older activity/ribbon analyses use different populations and controls.
  // Their entry point is useful, but cannot claim to preserve an arbitrary query.
  const resetsFilters = ["time", "parallelism"].includes(url.searchParams.get("machine_view") ?? "");
  if (!resetsFilters) {
    url.searchParams.set(dataset === "usage" && url.searchParams.get("analyze_view") === "carbon" ? "q_carbon" : `q_${dataset}`, encodeQuery(query));
    for (const key of ["analyze_chart", "analyze_carbon", "window_mode", "window_days", "window_field", "window_dataset", "window_until_field", "window_timezone"]) if (current.has(key)) url.searchParams.set(key, current.get(key)!);
  }
  return { href: url.pathname + url.search, resetsFilters };
}
export function isUsageWorkspace(params = new URLSearchParams(location.search)) {
  if (params.get("analyze_workspace") === "1" || ["analyze_mode", "analyze_data", "analyze_topic"].some(key => params.has(key))) return true;
  return !["analyze_view", "writing", "machine_view", "q_usage", "q_writing", "q_writing_messages", "q_conversation_activity", "q_parallelism"].some(key => params.has(key));
}
export function readUsageWorkspace(params: URLSearchParams) {
  const mode = params.get("analyze_mode"), dataset = params.get("analyze_data"), topic = params.get("analyze_topic");
  return {
    mode: (mode === "query" || mode === "search" ? "query" : "dashboards") as UsageMode,
    search: mode === "search" || params.get("analyze_search") === "1",
    dataset: (dataset && Object.hasOwn(usageDatasets, dataset) ? dataset : "usage") as UsageDataset,
    topic: (topic && Object.hasOwn(usageTopics, topic) ? topic : "tokens") as UsageTopic,
    report: params.get("analyze_report") ?? "",
    days: [7, 30, 90].includes(Number(params.get("analyze_days"))) ? Number(params.get("analyze_days")) : 30,
  };
}

export function usageWorkspaceURL(change: { mode?: UsageMode | "search"; search?: boolean; dataset?: UsageDataset; topic?: UsageTopic; report?: string; days?: number; query?: QueryState }, current = new URLSearchParams()) {
  const previous = readUsageWorkspace(current), next = { ...previous, ...change };
  const mode = next.mode === "search" ? "query" : next.mode;
  const params = new URLSearchParams(current);
  if (next.topic !== previous.topic) { params.delete("analyze_filters"); params.delete("analyze_window"); params.delete("analyze_chart"); params.delete("analyze_parallelism"); }
  for (const key of [...params.keys()]) {
    if (key.startsWith("record_") && (key !== `record_${next.dataset}` || next.dataset !== previous.dataset || mode === "dashboards")) params.delete(key);
    // A different dataset must never inherit another table's query/window.
    if (key.startsWith("q_") || key.startsWith("window_") || ["card_query", "analyze_workspace", "analyze_view", "writing", "machine_view", "activity_days", "activity_repo", "activity_provider", "activity_model", "activity_channel", "activity_machine_id", "activity_execution_machine_id", "machine_demo", "tools_view"].includes(key)) params.delete(key);
  }
  params.set("analyze_mode", mode);
  if (next.mode === "search" || next.search) params.set("analyze_search", "1"); else params.delete("analyze_search");
  params.set("analyze_data", next.dataset); params.set("analyze_topic", next.topic); params.set("analyze_days", String(next.days));
  if (next.report) params.set("analyze_report", next.report); else params.delete("analyze_report");
  // The query a dashboard link opened, so the way back can tell the user's
  // edits from what the link added (usageDashboardReturn).
  if (mode === "dashboards" || next.dataset !== previous.dataset) params.delete("analyze_opened");
  if (mode !== "dashboards") {
    const query = change.query;
    if (query) {
      params.set(`q_${next.dataset}`, encodeQuery({ ...query, fitToScreen: true }));
      if (current.has("analyze_filters")) params.set("analyze_opened", encodeQuery({ ...EMPTY_QUERY, where: (query.grouping?.source ?? query).where }));
    } else if (next.dataset === previous.dataset && current.has(`q_${next.dataset}`)) params.set(`q_${next.dataset}`, current.get(`q_${next.dataset}`)!);
    if (current.get("window_dataset") === next.dataset) for (const [key, value] of current) if (key.startsWith("window_")) params.set(key, value);
  }
  const cardDataset = current.get("card_query");
  if (!change.query && current.get("card_id") && cardDataset && Object.hasOwn(usageDatasets, cardDataset) && current.has(`q_${cardDataset}`) && next.topic === previous.topic && (mode === "dashboards" || next.dataset === cardDataset)) {
    params.set("card_query", cardDataset); params.set(`q_${cardDataset}`, current.get(`q_${cardDataset}`)!);
    if (current.get("window_dataset") === cardDataset) for (const [key, value] of current) if (key.startsWith("window_")) params.set(key, value);
  } else {
    if (params.has("card_query")) stampCardChart(params);
    for (const key of [...params.keys()]) if (key.startsWith("card_")) params.delete(key);
  }
  // Related overviews shed the card/query metadata, but keep its calendar
  // intent in the dashboard's own range setting.
  if (mode === "dashboards" && next.topic === previous.topic && current.get("window_mode") === "today" && !params.has("card_query") && params.has("analyze_filters")) {
    params.set("analyze_window", "today");
  }
  if (mode === "query" && change.query && current.get("analyze_window") === "today") {
    const where = (change.query.grouping?.source ?? change.query).where;
    const lower = where.filter(term => "field" in term && term.op === ">=" && /T/.test(term.value));
    const upper = where.filter(term => "field" in term && term.op === "<");
    if (lower.length === 1 && upper.length === 1 && "field" in lower[0] && "field" in upper[0] && upper[0].value === "+0s") {
      params.set("window_mode", "today"); params.set("window_days", "1"); params.set("window_field", lower[0].field); params.set("window_until_field", upper[0].field); params.set("window_dataset", next.dataset); params.set("window_timezone", localTimezone());
    }
  }
  return `/analyze?${params}`;
}

// Dashboards on the analysis engine read an unversioned analyze_chart as one
// whose legend may have written its selection into the filters
// (readAnalysisLink). A card's filters are always its population, so where a
// link stops being a card link its chart is marked current, and its filters
// stay the population.
const analysisTopics = new Set<string>(["tokens", "tools", "mcp", "time", "skills"]);
export function stampCardChart(params: URLSearchParams) {
  if (!analysisTopics.has(params.get("analyze_topic") ?? "") || !params.has("analyze_chart")) return;
  try {
    const chart = JSON.parse(params.get("analyze_chart")!);
    if (chart && typeof chart === "object" && !Array.isArray(chart) && chart.v === undefined) params.set("analyze_chart", JSON.stringify({ ...chart, v: analysisVersion }));
  } catch { /* An invalid chart stays as it is and reports itself. */ }
}

// Search explicitly opens source records. A grouped result's HAVING predicate
// is not a source predicate and cannot be silently relabelled as one.
export function usageSearchQuery(query: QueryState): QueryState {
  const source = query.grouping?.source ?? query;
  return { ...source, aggregations: [], offset: 0 };
}

export const currentUsageAnalyses = [
  { label: "Tokens & cost", topic: "tokens", url: "/analyze?analyze_view=tokens&machine_view=tokens" },
  { label: "Human words", topic: "human", url: "/analyze?analyze_view=writing" },
  { label: "Human messages", topic: "human", url: "/analyze?analyze_view=writing&writing=messages" },
  { label: "Conversation activity", topic: "human", url: "/analyze?analyze_view=conversations" },
  { label: "Agent time", topic: "time", url: "/analyze?analyze_view=tokens&machine_view=time" },
  { label: "Parallelism", topic: "parallelism", url: "/analyze?analyze_view=tokens&machine_view=parallelism" },
  { label: "Carbon impact & assumptions", topic: "carbon", url: "/analyze?analyze_view=carbon" },
  { label: "Tool calls", topic: "tools", url: "/tools?tools_view=calls" },
  { label: "Tool summary", topic: "tools", url: "/tools?tools_view=summary" },
  { label: "Skill evidence", topic: "skills", url: "/tools?tools_view=skills" },
] as const;
