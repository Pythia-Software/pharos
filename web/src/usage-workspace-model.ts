import { EMPTY_QUERY, decodeQueryStrict, encodeQuery, type QueryState } from "@pythia-software/query-table-core";

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
  tokens: "usage", human: "writing", time: "tool_calls", parallelism: "parallelism", tools: "tools", mcp: "mcp_calls", skills: "skill_usages", carbon: "usage",
};

export function usageDashboardReturn(current: URLSearchParams) {
  const state = readUsageWorkspace(current), dataset = usageTopicDatasets[state.topic];
  const keywords = current.has(`record_${state.dataset}`);
  if (!keywords && current.get("card_id") && current.get("card_query") === state.dataset && current.has(`q_${state.dataset}`)) return {
    href: usageWorkspaceURL({ mode: "dashboards", dataset, search: false }, current), label: "Back to dashboard", resetsFilters: false,
  };
  let query: QueryState | undefined;
  try { query = decodeQueryStrict(current.get(`q_${state.dataset}`) ?? "") ?? undefined; } catch { /* Explicitly reset an unavailable query. */ }
  const compatible = dataset === state.dataset && state.topic !== "parallelism" && Boolean(query);
  const params = new URLSearchParams(current);
  params.set("analyze_filters", encodeQuery({ ...EMPTY_QUERY, where: compatible ? (query!.grouping?.source ?? query!).where : [] }));
  return {
    href: usageWorkspaceURL({ mode: "dashboards", dataset, search: false }, params),
    label: !compatible ? "Back to dashboard (resets query filters)" : keywords ? "Back to dashboard (resets keyword filters)" : query!.grouping && query!.where.length ? "Back to dashboard (resets group filters)" : "Back to dashboard",
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
    for (const key of ["analyze_chart", "analyze_carbon"]) if (current.has(key)) url.searchParams.set(key, current.get(key)!);
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
  if (next.topic !== previous.topic) { params.delete("analyze_filters"); params.delete("analyze_chart"); params.delete("analyze_parallelism"); }
  for (const key of [...params.keys()]) {
    if (key.startsWith("record_") && (key !== `record_${next.dataset}` || next.dataset !== previous.dataset || mode === "dashboards")) params.delete(key);
    // A different dataset must never inherit another table's query/window.
    if (key.startsWith("q_") || key.startsWith("window_") || ["card_query", "analyze_workspace", "analyze_view", "writing", "machine_view", "activity_days", "activity_repo", "activity_provider", "activity_model", "activity_channel", "activity_machine_id", "activity_execution_machine_id", "machine_demo", "tools_view"].includes(key)) params.delete(key);
  }
  params.set("analyze_mode", mode);
  if (next.mode === "search" || next.search) params.set("analyze_search", "1"); else params.delete("analyze_search");
  params.set("analyze_data", next.dataset); params.set("analyze_topic", next.topic); params.set("analyze_days", String(next.days));
  if (next.report) params.set("analyze_report", next.report); else params.delete("analyze_report");
  if (mode !== "dashboards") {
    const query = change.query;
    if (query) params.set(`q_${next.dataset}`, encodeQuery({ ...query, fitToScreen: true }));
    else if (next.dataset === previous.dataset && current.has(`q_${next.dataset}`)) params.set(`q_${next.dataset}`, current.get(`q_${next.dataset}`)!);
    if (current.get("window_dataset") === next.dataset) for (const [key, value] of current) if (key.startsWith("window_")) params.set(key, value);
  }
  const cardDataset = current.get("card_query");
  if (!change.query && current.get("card_id") && cardDataset && Object.hasOwn(usageDatasets, cardDataset) && current.has(`q_${cardDataset}`) && next.topic === previous.topic && (mode === "dashboards" || next.dataset === cardDataset)) {
    params.set("card_query", cardDataset); params.set(`q_${cardDataset}`, current.get(`q_${cardDataset}`)!);
    if (current.get("window_dataset") === cardDataset) for (const [key, value] of current) if (key.startsWith("window_")) params.set(key, value);
  } else params.delete("card_id");
  return `/analyze?${params}`;
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
