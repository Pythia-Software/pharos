import { fitQueryToScreen } from "./query-fit";
import { retainPageView } from "./view-contract";
import { EMPTY_QUERY, decodeQueryStrict, encodeQuery, predicatesOf, type QueryState, type WhereTerm } from "@pythia-software/query-table-core";
import { dashboardDefaults } from "./dashboard-defaults";
import { peopleMessageLabelScope } from "./people-message-labels";
import { generatedMessageField, generatedQueryTarget, toolWhereCanUseCalls, writingMessageSourceField } from "./generated-query-target";
import { schemaDocuments } from "./schema-documents";
import { mapTermPredicates } from "./where-terms";

// Analyze owns guided dashboards and the full query surfaces. Explicit older
// analysis URLs remain available while their specialized controls migrate.
export const usageDatasets = {
  scatter: { label: "Scatter plot points", description: "Measurements matched by conversation, workspace, agent session or message identity.", field: "title", searchLabel: "Point titles" },
  messages: { label: "Messages", description: "Every retained prompt, reply, thinking block and tool message, with authorship evidence where available.", field: "text", searchLabel: "Message text" },
  people_message_labels:{label:"People message labels",description:"Message classifications within People dashboard populations.",field:"label",searchLabel:"Message label"},
  message_labels:{label:"Message labels",description:"What messages do, labeling coverage and matching conversations.",field:"text",searchLabel:"Labeled message text"},
  compactions: { label: "Compactions", description: "Recorded context compactions and what triggered them.", field: "model", searchLabel: "Compaction Model Names" },
  model_requests: { label: "Model requests", description: "Recorded model responses, token usage and timing after tool results.", field: "model", searchLabel: "Model request names" },
  usage: { label: "Token usage", description: "Tokens, API costs, models, cache, and pricing coverage.", field: "model", searchLabel: "Token Usage Model Names" },
  conversation_activity: { label: "Conversation activity", description: "Human messages and recorded output across conversations.", field: "root_model_family", searchLabel: "Top-level Conversation Model Names" },
  writing: { label: "Human words by workspace", description: "Word totals across each workspace’s conversations, including linked sub-agent activity.", field: "title", searchLabel: "Workspace Titles" },
  writing_messages: { label: "User messages", description: "Messages in the user role, with text classified by where it came from.", field: "text", searchLabel: "Conversation Message Text" },
  tool_calls: { label: "Tool calls", description: "Commands, results, timing, status, and context added or carried.", field: "command", searchLabel: "Workspace Tool Command Text" },
  tools: { label: "Tool summary", description: "Daily tool-call totals by tool, command, repository and agent settings.", field: "tool_name", searchLabel: "Workspace Tool Names" },
  mcp_calls: { label: "Pharos MCP requests", description: "Calls to Pharos's MCP server, sanitized arguments, output estimates, and timing.", field: "arguments_json", searchLabel: "MCP Call Arguments" },
  skill_usages: { label: "Skill evidence", description: "Recorded skill invocations, file loads and saved instruction text.", field: "skill_name", searchLabel: "Skill Names" },
  tl1_attempts: { label: "TL1 attempts", description: "Recorded task attempts, configurations, outcomes, and costs.", field: "title", searchLabel: "TL1 Task Titles" },
  parallelism: { label: "Concurrent activity hours", description: "Hourly activity counted by workspace or top-level conversation, using your Parallelism settings.", field: "model", searchLabel: "Parallel Activity Model Names" },
  rhythm: { label: "Activity hours", description: "Hourly activity for you and agents working with you, unattended or automatically.", field: "unit_label", searchLabel: "Rhythm Conversation Titles" },
  agent_events: { label: "Agent events", description: "Sentences where agents hit obstacles, were surprised, discovered something, worked around a problem, and more.", field: "sentence", searchLabel: "Agent Event Sentences" },
  agent_event_rates: { label: "Agent event observations", description: "Every agent message checked for events, once per kind of event, with who prompted its turn, its model and effort: the rows the Agent Events rates divide.", field: "title", searchLabel: "Agent Event Rate Conversation Titles" },
  agent_event_messages: { label: "Messages checked for agent events", description: "Agent replies and thinking blocks checked for events, with matching keywords and classifications.", field: "title", searchLabel: "Agent Event Conversation Titles" },
  session_shape: { label: "Agent sessions", description: "One row per agent session: its length, active time, tool calls and autonomy, and the forensic signals that fired.", field: "title", searchLabel: "Session Titles" },
  outcomes: { label: "Workspace outcomes", description: "One row per Library workspace: its outcome, the evidence that decided it, its cost and what it changed.", field: "title", searchLabel: "Workspace Titles" },
} as const;
export type UsageDataset = keyof typeof usageDatasets;

// Dataset pages describe the source row. Specialized dashboard populations and
// older rollups remain internal sources; their analyses live in query ideas.
export const analyzeQueryDatasets = [
  "messages", "tool_calls", "model_requests", "usage", "compactions",
  "skill_usages", "message_labels", "session_shape", "tl1_attempts",
  "conversation_activity", "rhythm", "parallelism", "mcp_calls",
  "agent_events", "agent_event_messages", "agent_event_rates",
] as const satisfies readonly UsageDataset[];
export type UsageMode = "dashboards" | "query";
export const engineUsageTopics = {
  scatter: { label: "Scatter Plots", description: "Compare measures across dashboards, with one point per conversation, workspace, agent session or message." },
  compaction: { label: "Compaction", description: "Count context compactions and compare human and agent-app triggers across providers, models and repositories." },
  labels:{label:"Message Labels",description:"Explore conversation functions over time, compare labels and follow their matching conversations."},
  activity: { label: "Conversations", description: "How many conversations are you having, and how much do they produce? Explore conversations, your messages and model output across models and repositories." },
  human: { label: "Human Words", description: "How much are you contributing? Explore your writing over time." },
  tokens: { label: "Tokens and Cost", description: "How much are agents consuming? Choose a measure, a breakdown, and a time range." },
  skills: { label: "Skills", description: "Where does skill evidence appear? Compare observed invocations and loads." },
  calls: { label: "Tool Calls", description: "Explore individual calls, failures, research, file paths, and context. Compare groups, distributions, and changes over time." },
  tools: { label: "Tool Summary", description: "Understand tool mix, reliability, and context overhead. Compare groups or follow changes over time." },
  time: { label: "Tool Time", description: "Where is recorded tool time going? Compare activities over time." },
  parallelism: { label: "Parallelism", description: "Explore overlapping conversations and estimated model activity. Select a ribbon or drag to zoom." },
  work_hours: { label: "Work Hours", description: "Explore wall hours for you and your agents over time, by who is active, conversation, model or repository." },
  mcp: { label: "Pharos MCP", description: "How are agents using Pharos? Explore calls, reliability, response sizes, and time spent serving requests." },
  carbon: { label: "Carbon Impact", description: "Estimated electricity and emissions, with explicit assumptions." },
  agent_event_rates: { label: "Agent Events", description: "How often do agents run into each kind of event? Rates per message, turn or conversation the event pass read, by kind, model and effort, who prompted, prompt length, repository and more." },
  outcomes: { label: "Outcomes", description: "What became of agents' work, and what did it cost? Workspaces by outcome, rework, cost per kept change and evidence coverage, with or without Git." },
  agent_experience: { label: "Agent Experience", description: "What are agents running into? Obstacles, surprises, discoveries, workarounds and unchecked claims, found in their replies and thinking." },
  session_shape: { label: "Session Shape", description: "What does a typical session look like, and which went badly? Session length, active time, tool calls, autonomy and turn cycles, and forensic signals against your own history." },
} as const;
export const usageTopics = { ...engineUsageTopics,
  habits: { label: "Habits", description: "Explore your activity calendar, weekday and hour patterns, or daily work schedule, with streaks, work and rest patterns, and automatic agent runs." },
  "thinking-overview": {label:"Waiting",description:"Understand observed model waits, context associations and timing evidence."},
  "thinking-detail": {label:"Thinking",description:"Compare recorded model-request distributions across models, effort, workloads and dates."},
} as const;
export type UsageTopic = keyof typeof usageTopics;
export const usageDatasetTopics: Record<UsageDataset, UsageTopic> = {
  scatter: "scatter",
  messages: "human",
  people_message_labels:"labels", message_labels:"labels",
  compactions: "compaction", model_requests: "thinking-detail", usage: "tokens", conversation_activity: "activity", writing: "human", writing_messages: "human",
  tools: "tools", tool_calls: "calls", mcp_calls: "mcp", skill_usages: "skills", parallelism: "parallelism", rhythm: "work_hours", tl1_attempts: "tokens", agent_events: "agent_experience", agent_event_messages: "agent_experience", agent_event_rates: "agent_event_rates", session_shape: "session_shape", outcomes: "outcomes",
};
export const usageTopicDatasets: Record<UsageTopic, UsageDataset> = {
  scatter: "scatter",
  compaction:"compactions", labels:"message_labels",
  "thinking-overview":"model_requests", "thinking-detail":"model_requests",
  calls: "tool_calls", activity: "conversation_activity", tokens: "usage", human: "writing_messages", time: "tool_calls", parallelism: "parallelism", habits: "rhythm", work_hours: "rhythm", tools: "tools", mcp: "mcp_calls", skills: "skill_usages", carbon: "usage", agent_experience: "agent_events", agent_event_rates: "agent_event_rates", session_shape: "session_shape", outcomes: "outcomes",
};

// Fields that hold a dashboard's dates, by dataset: its range, and the day
// predicates a link may translate it into (the tools rollup's text day).
const timeFields: Record<UsageDataset, string[]> = {
  scatter: ["first_at", "last_at"],
  messages: ["sent_at", "day", "week", "month"],
  people_message_labels:["sent_at"], message_labels:["occurred_at"],
  compactions: ["occurred_at"], model_requests: ["requested_at"],
  tl1_attempts: ["started_at"],
  usage: ["first_usage_at", "last_usage_at", "day", "hour", "week", "month"], conversation_activity: ["event_at"],
  writing: ["first_message_at", "last_message_at"], writing_messages: ["sent_at", "day", "week", "month"], tools: ["day", "week", "month"], tool_calls: ["started_at", "ended_at", "day", "week", "month"],
  mcp_calls: ["called_at", "day", "week", "month"], skill_usages: ["created_at", "day", "week", "month"], parallelism: ["started_at", "day", "hour_of_day", "week", "month"], rhythm: ["started_at", "day", "hour_of_day", "week", "month"],
  agent_events: ["occurred_at", "day", "week", "month"], agent_event_messages: ["occurred_at", "day", "week", "month"], agent_event_rates: ["occurred_at", "day", "week", "month"], session_shape: ["started_at", "day", "week", "month"], outcomes: ["started_at"],
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
  const state = readUsageWorkspace(current), originalDataset = usageTopicDatasets[state.topic];
  let chart = { ...dashboardDefaults[state.topic] };
  try { chart = { ...chart, ...JSON.parse(current.get("analyze_chart") ?? "{}") }; } catch { /* An invalid chart retains the topic's ordinary source. */ }
  const dataset = peopleMessageLabelScope(originalDataset, chart) ? "people_message_labels" : originalDataset;
  const keywords = current.has(`record_${state.dataset}`);
  let query: QueryState | undefined;
  try { query = decodeQueryStrict(current.get(`q_${state.dataset}`) ?? "") ?? undefined; } catch { /* Explicitly reset an unavailable query. */ }
  const sourceWhere = (query?.grouping?.source ?? query)?.where ?? [];
  const authored = (field: string, value: string) => sourceWhere.some(term => "field" in term && !term.negated && term.field === field && term.op === "=" && term.value === value);
  const oldFields = new Set(schemaDocuments.writing_messages.fields.map(field => field.name));
  const sourceField = (field: string) => writingMessageSourceField(field);
  const oldWhere = (terms: WhereTerm[]) => terms.map(term => mapTermPredicates(term, clause => ({ ...clause, field: sourceField(clause.field) }))!);
  const population = (term: WhereTerm) => "field" in term && !term.negated && term.op === "=" && (term.field === "role" && term.value === "user" || term.field === "has_authorship" && term.value === "true");
  const humanMessages = dataset === "writing_messages" && state.dataset === "messages" && authored("role", "user") && authored("has_authorship", "true")
    && sourceWhere.every(term => population(term) || predicatesOf(term).every(clause => oldFields.has(sourceField(clause.field)) && generatedMessageField(sourceField(clause.field)) === clause.field));
  // Chart drilldowns add contribution predicates that only describe the
  // opened calls. Check the filters returning to the dashboard after those
  // unchanged predicates have been removed; user-added call filters still
  // need an explicit reset when the daily Tools source cannot represent them.
  const callReturn = dataset === "tools" && state.dataset === "tool_calls"
    ? returnedWhere(current, sourceWhere, originalDataset) : undefined;
  const callRecords = Boolean(callReturn && toolWhereCanUseCalls(callReturn.where));
  const compatible = Boolean(query) && (dataset === state.dataset || humanMessages || callRecords);
  const params = new URLSearchParams(current);
  const compared = humanMessages ? new URLSearchParams(current) : current;
  if (humanMessages) {
    try {
      const opened = decodeQueryStrict(current.get("analyze_opened") ?? "");
      if (opened) compared.set("analyze_opened", encodeQuery({ ...opened, where: oldWhere(opened.where) }));
    } catch { /* An unreadable opened query falls back to the edited filters. */ }
  }
  const edited = humanMessages ? oldWhere(sourceWhere) : sourceWhere;
  const returned = compatible ? callReturn ?? returnedWhere(compared, edited, originalDataset) : { where: [], dates: false };
  if (humanMessages) returned.where = returned.where.filter(term => !population(term));
  // Settings a dataset's records are built with are not the dashboard's
  // filters: Parallelism's counting and gap follow the saved settings.
  const settings = builtWith[state.dataset] ?? [];
  if (settings.length) returned.where = returned.where.filter(term => !termFields(term).some(field => settings.includes(field)));
  if (returned.dates || !compatible) {
    params.delete("analyze_window");
    for (const key of [...params.keys()]) if (key.startsWith("window_")) params.delete(key);
  }
  const openedThinking = compatible && ["thinking-overview", "thinking-detail"].includes(state.topic) ? decodeQueryStrict(current.get("analyze_opened") ?? "") : null;
  if (compatible && ["thinking-overview", "thinking-detail"].includes(state.topic) && (!openedThinking || JSON.stringify(openedThinking.where) !== JSON.stringify((query!.grouping?.source ?? query!).where))) {
    // The edited evidence query now owns the entire population. Keep presentation,
    // but rederive range/model controls so removed predicates cannot be reapplied.
    returned.where = (query!.grouping?.source ?? query!).where;
    const thinking = JSON.parse(params.get("thinking") || "{}");
    for (const key of ["range", "from", "to"]) delete thinking[key];
    params.set("thinking", JSON.stringify({ ...thinking, model: "all", effort: "all" }));
  }
  params.set("analyze_filters", encodeQuery({ ...EMPTY_QUERY, where: returned.where }));
  return {
    href: usageWorkspaceURL({ mode: "dashboards", dataset, search: false }, params),
    label: !compatible ? "Back to dashboard (resets query filters)" : keywords ? "Back to dashboard (resets keyword filters)" : query!.grouping && query!.where.length ? "Back to dashboard (resets group filters)" : returned.dates ? "Back to dashboard (dates from this query)" : "Back to dashboard",
    resetsFilters: !compatible || keywords || Boolean(query?.grouping && query.where.length),
  };
}

// Parameters only the removed Analyze and Tools pages read.
const legacyKeys = ["analyze_workspace", "analyze_view", "writing", "messages", "message_query", "message_cursor", "machine_view", "machine_demo", "tools_view", "usage", "parallelism", "carbon_window"];
export function isUsageWorkspace(params = new URLSearchParams(location.search)) {
  if (params.get("analyze_workspace") === "1" || ["analyze_mode", "analyze_data", "analyze_topic"].some(key => params.has(key))) return true;
  return !["analyze_view", "writing", "machine_view", "tools_view", "q_usage", "q_writing", "q_writing_messages", "q_conversation_activity", "q_parallelism"].some(key => params.has(key));
}

// An older Analyze or Tools link (analyze_view, machine_view, writing,
// tools_view) opens what replaced its page: a link holding its table's query
// opens that query table, and otherwise the page's dashboard, with the chart
// a dashboard reads through its catalog's legacy mapping, and Carbon's
// population. A workspace link is returned as it is.
export function canonicalAnalyzeURL(path: string, params: URLSearchParams): string {
  if (path === "/analyze" && isUsageWorkspace(params)) return `/analyze?${params}`;
  const table = (dataset: UsageDataset) => ({ mode: "query" as const, dataset, topic: usageDatasetTopics[dataset] });
  const dashboard = (topic: UsageTopic) => ({ mode: "dashboards" as const, dataset: usageTopicDatasets[topic], topic });
  const view = params.get("analyze_view"), machine = params.get("machine_view");
  const target = path === "/tools" || params.has("tools_view") ? table(params.get("tools_view") === "summary" ? "tools" : params.get("tools_view") === "skills" ? "skill_usages" : "tool_calls")
    : view === "conversations" ? table("conversation_activity")
    : view === "writing" ? params.get("writing") === "messages" ? table("writing_messages") : params.has("q_writing") ? table("writing") : dashboard("human")
    : view === "carbon" ? dashboard("carbon")
    : machine === "time" ? dashboard("time")
    : machine === "parallelism" || view === "concurrency" ? params.has("q_parallelism") ? table("parallelism") : dashboard("parallelism")
    : params.has("q_usage") ? table("usage") : dashboard("tokens");
  const next = new URLSearchParams();
  for (const [key, value] of params) {
    if (legacyKeys.includes(key) || key.startsWith("activity_") || key.startsWith("q_") && key !== `q_${target.dataset}`) continue;
    if (key.startsWith("window_") && params.get("window_dataset") !== target.dataset) continue;
    if (key === "analyze_chart" && target.mode === "query") continue;
    next.append(key, value);
  }
  // The legacy Carbon page kept its population in q_carbon and its Today
  // window in carbon_window; the dashboard keeps them in analyze_filters and
  // analyze_window.
  if (target.topic === "carbon" && !next.has("analyze_filters")) {
    try { const where = decodeQueryStrict(params.get("q_carbon") ?? "")?.where; if (where?.length) next.set("analyze_filters", encodeQuery({ ...EMPTY_QUERY, where })); } catch { /* An unreadable query leaves the dashboard's own population. */ }
    if (params.get("carbon_window") === "today") next.set("analyze_window", "today");
  }
  next.set("analyze_mode", target.mode); next.set("analyze_data", target.dataset); next.set("analyze_topic", target.topic);
  return `/analyze?${next}`;
}
export function readUsageWorkspace(params: URLSearchParams) {
  const mode = params.get("analyze_mode"), dataset = params.get("analyze_data"), topic = params.get("analyze_topic");
  return {
    mode: (mode === "query" || mode === "search" ? "query" : "dashboards") as UsageMode,
    search: mode === "search" || params.get("analyze_search") === "1",
    dataset: (dataset && Object.hasOwn(usageDatasets, dataset) ? dataset : "usage") as UsageDataset,
    topic: (topic && Object.hasOwn(usageTopics, topic) ? topic : "tokens") as UsageTopic,
    report: params.get("analyze_report") ?? "",
    days: [1, 7, 30, 90, 180, 365].includes(Number(params.get("analyze_days"))) ? Number(params.get("analyze_days")) : 30,
  };
}

export function usageWorkspaceURL(change: { mode?: UsageMode | "search"; search?: boolean; dataset?: UsageDataset; topic?: UsageTopic; report?: string; days?: number; query?: QueryState }, current = new URLSearchParams()) {
  const previous = readUsageWorkspace(current);
  // Only newly built queries change source. A saved source token or a link
  // without an explicit query continues to open its existing dataset.
  const requested = change.dataset ?? previous.dataset;
  const target = change.query && !current.has(`record_${requested}`)
    ? generatedQueryTarget(requested, change.query) : undefined;
  const effective = target && target.dataset !== requested
    ? { ...change, dataset: target.dataset as UsageDataset, query: target.query } : change;
  const next = { ...previous, ...effective };
  const mode = next.mode === "search" ? "query" : next.mode;
  const params = new URLSearchParams(current);
  // Leaving the opted-in label lab must actually open the requested dashboard
  // or table instead of carrying its route switch into that destination.
  for (const key of [...params.keys()]) if (key.startsWith("label_")) params.delete(key);
  if (next.topic !== previous.topic && !(["thinking-overview","thinking-detail"].includes(next.topic)&&["thinking-overview","thinking-detail"].includes(previous.topic))) { params.delete("analyze_filters"); params.delete("analyze_window"); params.delete("analyze_timezone"); params.delete("analyze_chart"); params.delete("analyze_parallelism"); }
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
    const query = effective.query;
    if (query) {
      params.set(`q_${next.dataset}`, encodeQuery(fitQueryToScreen(query)));
      if (current.has("analyze_filters")) params.set("analyze_opened", encodeQuery({ ...EMPTY_QUERY, where: (query.grouping?.source ?? query).where }));
    } else if (next.dataset === previous.dataset && current.has(`q_${next.dataset}`)) params.set(`q_${next.dataset}`, current.get(`q_${next.dataset}`)!);
    if (current.get("window_dataset") === next.dataset || target && target.dataset !== requested && current.get("window_dataset") === requested) {
      for (const [key, value] of current) if (key.startsWith("window_")) params.set(key,
        key === "window_dataset" ? next.dataset : target?.dataset === "messages" && ["window_field", "window_until_field"].includes(key) ? generatedMessageField(value) : value);
    }
  }
  // A full dashboard owns its chart and filters. Source queries and covers
  // remain in saved cards only, never in dashboard or drilldown navigation.
  if (current.has("card_query")) stampCardChart(params);
  for (const key of [...params.keys()]) if (key.startsWith("card_")) params.delete(key);
  // Dashboard navigation sheds source metadata and keeps calendar intent
  // in the dashboard's own range setting.
  if (mode === "dashboards" && next.topic === previous.topic && current.get("window_mode") === "today" && !params.has("card_query") && params.has("analyze_filters")) {
    params.set("analyze_window", "today");
    if (current.has("window_timezone")) params.set("analyze_timezone", current.get("window_timezone")!);
  }
  if (mode === "query" && effective.query && current.get("analyze_window") === "today") {
    const where = (effective.query.grouping?.source ?? effective.query).where;
    const lower = where.filter(term => "field" in term && term.op === ">=" && /T/.test(term.value));
    const upper = where.filter(term => "field" in term && term.op === "<");
    if (lower.length === 1 && upper.length === 1 && "field" in lower[0] && "field" in upper[0] && upper[0].value === "+0s") {
      params.set("window_mode", "today"); params.set("window_days", "1"); params.set("window_field", lower[0].field); params.set("window_until_field", upper[0].field); params.set("window_dataset", next.dataset);
      if (current.has("analyze_timezone")) params.set("window_timezone", current.get("analyze_timezone")!);
    }
  }
  const page=mode==="query"?`analyze/query/${next.dataset}`:`analyze/${next.topic}`;
  const sourcePage=previous.mode==="query"?`analyze/query/${previous.dataset}`:`analyze/${previous.topic}`;
  retainPageView(params,page,[...(mode==="dashboards"?["analyze_chart","analyze_filters","analyze_window","analyze_days"]:[])],sourcePage);
  if(mode!==previous.mode||next.topic!==previous.topic||next.dataset!==previous.dataset){params.delete("highlight");params.delete("reveal");}
  return `/analyze?${params}`;
}

// Dashboards on the analysis engine read an unversioned analyze_chart as one
// whose legend may have written its selection into the filters
// (readAnalysisLink). A card's filters are always its population, so where a
// link stops being a card link its chart is marked version 2: its filters
// stay the population, and, as with any link from before version 3, view
// settings equal to its page's old defaults are left out (readLegacyView).
const analysisTopics = new Set<string>(["labels", "tokens", "calls", "activity", "tools", "mcp", "time", "skills", "agent_experience", "agent_event_rates"]);
export function stampCardChart(params: URLSearchParams) {
  if (!analysisTopics.has(params.get("analyze_topic") ?? "") || !params.has("analyze_chart")) return;
  try {
    const chart = JSON.parse(params.get("analyze_chart")!);
    if (chart && typeof chart === "object" && !Array.isArray(chart) && chart.v === undefined) params.set("analyze_chart", JSON.stringify({ ...chart, v: 2 }));
  } catch { /* An invalid chart stays as it is and reports itself. */ }
}

// Search explicitly opens source records. A grouped result's HAVING predicate
// is not a source predicate and cannot be silently relabelled as one.
export function usageSearchQuery(query: QueryState): QueryState {
  const source = query.grouping?.source ?? query;
  return { ...source, aggregations: [], offset: 0 };
}
