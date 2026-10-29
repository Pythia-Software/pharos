import { todayBounds, localTimezone, type WindowChoice } from "./day-window";
import { EMPTY_QUERY, decodeQueryStrict, predicatesOf, localQuerySnapshotCache, encodeQuery, type QueryState } from "@pythia-software/query-table-core";
import { isUsageWorkspace, readUsageWorkspace, usageDatasetTopics, type UsageDataset } from "./usage-workspace-model";
import { cardDashboardWhere, cardSourceDataset } from "./card-exploration";
import { concurrencyWindowRange } from "./concurrency-settings";
import { windowedChart } from "./card-windows";

export const queryDatasets = ["library", "usage", "conversation_activity", "writing", "writing_messages", "mcp_calls", "tools", "tool_calls", "skill_usages", "tl1_attempts", "parallelism", "findings"] as const;
export type QueryDataset = typeof queryDatasets[number];
export type RollingWindow = { days: number; field: string; mode?: "relative" | "calendar" | "today"; untilField?: string; timezone?: string };
const timeFields: Partial<Record<QueryDataset, string[]>> = {
  writing: ["first_message_at", "last_message_at"], conversation_activity: ["event_at"], mcp_calls: ["called_at"], library: ["activity_at"], usage: ["first_usage_at", "last_usage_at"], tool_calls: ["started_at"],
  skill_usages: ["created_at"], writing_messages: ["sent_at"], tl1_attempts: ["started_at"], parallelism: ["started_at"], findings: ["first_seen_at"],
};
export const rollingWindowDays = [1, 7, 30, 90, 180, 365] as const;
export function rollingWindowField(dataset: QueryDataset): string | undefined { return timeFields[dataset]?.[0]; }

export function queryHasTimeFilter(query: QueryState, field: string): boolean {
  const source = query.grouping?.source ?? query;
  return source.where.some(term => predicatesOf(term).some(clause => clause.field === field));
}

// New cards use native elapsed offsets in their saved query and URL. Legacy
// calendar-window links keep their original interpretation until edited.
export function rollingBounds(days: number, clock = new Date()): [string, string] {
  const start = new Date(clock), end = new Date(clock);
  start.setHours(0, 0, 0, 0); start.setDate(start.getDate() - days + 1);
  end.setHours(0, 0, 0, 0); end.setDate(end.getDate() + 1);
  return [start.toISOString(), end.toISOString()];
}

export function readRollingWindow(params: URLSearchParams, dataset: QueryDataset): RollingWindow | undefined {
  const days = Number(params.get("window_days")), field = params.get("window_field") ?? "";
  if (params.get("window_dataset") !== dataset || !rollingWindowDays.some(value => value === days) || !timeFields[dataset]?.includes(field)) return;
  const mode = params.get("window_mode");
  if (mode === "today" && (days !== 1 || dataset === "tl1_attempts" || dataset === "findings")) return;
  const untilField = params.get("window_until_field");
  if (untilField && !timeFields[dataset]?.includes(untilField)) return;
  const timezone = params.get("window_timezone");
  if (timezone) { try { new Intl.DateTimeFormat("en", { timeZone: timezone }); } catch { return; } }
  return { ...(timezone ? { timezone } : {}), days, field, mode: mode === "today" ? "today" : mode === "relative" ? "relative" : "calendar", ...(untilField ? { untilField } : {}) };
}

// null selects all time; undefined at the bookmark layer preserves a saved range.
export function updateQueryWindowURL(url: string, days: WindowChoice | null, fallback?: QueryState): string {
  const spec = queryFromURL(url, fallback), field = spec && (spec.window?.field ?? rollingWindowField(spec.dataset));
  if (!spec || !field || days === "today" && ["tl1_attempts", "findings"].includes(spec.dataset) || (days !== null && days !== "today" && !rollingWindowDays.some(value => value === days))) throw new Error("This view does not support that time window.");
  const window: RollingWindow | undefined = days === null ? undefined : { days: days === "today" ? 1 : days, field, mode: days === "today" ? "today" : "relative", ...(spec.window?.untilField ? { untilField: spec.window.untilField } : {}) };
  // Replace direct date selections, including older fixed ranges. Leave other
  // predicates (and compound date expressions) intact rather than rewriting
  // the user's logical query. Relative bounds are renewed on every evaluation.
  const source = spec.query.grouping?.source ?? spec.query;
  const adjusted = { ...source, where: source.where.filter(term => !("field" in term && (timeFields[spec.dataset]?.includes(term.field) ?? term.field === field) && !term.negated && ["=", ">", ">=", "<", "<="].includes(term.op))) };
  if (days === null && adjusted.where.length === source.where.length && !spec.window) return url;
  const query = resolveRollingQuery(spec.query.grouping ? { ...spec.query, offset: 0, grouping: { ...spec.query.grouping, source: { ...adjusted, offset: 0 } } } : { ...adjusted, offset: 0 }, window);
  const parsed = new URL(url, location.origin);
  parsed.searchParams.set(`q_${spec.dataset}`, encodeQuery(query));
  if (parsed.searchParams.get("card_query") === spec.dataset && parsed.searchParams.get("analyze_mode") === "dashboards") {
    const target = readUsageWorkspace(parsed.searchParams).dataset;
    parsed.searchParams.set("analyze_filters", encodeQuery({ ...EMPTY_QUERY, where: cardDashboardWhere(spec.dataset as UsageDataset, target, (query.grouping?.source ?? query).where) }));
    if (days !== null) parsed.searchParams.set("analyze_days", String(days === "today" ? 1 : days));
    parsed.searchParams.delete("analyze_window");
    const chart = parsed.searchParams.get("analyze_chart");
    if (chart) { try { parsed.searchParams.set("analyze_chart", JSON.stringify(windowedChart(parsed.searchParams.get("card_id"), JSON.parse(chart), days === "today" ? 1 : days))); } catch { /* An unreadable chart is reported by the dashboard. */ } }
    if (parsed.searchParams.has("analyze_parallelism")) {
      const settings = JSON.parse(parsed.searchParams.get("analyze_parallelism")!);
      parsed.searchParams.set("analyze_parallelism", JSON.stringify({ ...settings, range: days === "today" ? "today" : concurrencyWindowRange(days) }));
    }
  }
  if (window?.mode === "today") parsed.searchParams.set("window_timezone", localTimezone()); else parsed.searchParams.delete("window_timezone");
  if (window?.untilField) parsed.searchParams.set("window_until_field", window.untilField); else parsed.searchParams.delete("window_until_field");
  if (window) {
    parsed.searchParams.set("window_mode", window.mode!); parsed.searchParams.set("window_days", String(window.days)); parsed.searchParams.set("window_field", field); parsed.searchParams.set("window_dataset", spec.dataset);
  } else {
    for (const key of ["window_mode", "window_days", "window_field", "window_dataset", "window_until_field", "window_timezone"]) parsed.searchParams.delete(key);
  }
  // Search text, chart options and all other URL context stay with the view.
  return `${parsed.pathname}${parsed.search}${parsed.hash}`;
}

export function resolveRollingQuery(query: QueryState, window?: RollingWindow, clock = new Date()): QueryState {
  if (!window) return query;
  if (window.mode === "relative" && rollingQueryMatches(query, window)) return query;
  const source = query.grouping?.source ?? query;
  if (window.mode === "today" && rollingQueryMatches(query, window) && source.where.some(term => "field" in term && term.field === window.field && term.op === ">=" && Date.parse(term.value) === Date.parse(todayBounds(clock)[0]))) return query;
  if (query.grouping) return { ...query, offset: 0, grouping: { ...query.grouping, source: resolveRollingQuery(query.grouping.source, window, clock) } };
  const [from, to] = window.mode === "today" ? [todayBounds(clock)[0], "+0s"] : window.mode === "relative" ? [`-${window.days}d`, "+0s"] : rollingBounds(window.days, clock);
  return { ...query, offset: 0, where: [
    ...query.where.filter(term => !("field" in term && (term.field === window.field || term.field === window.untilField) && !term.negated && [">=", "<"].includes(term.op))),
    { field: window.field, op: ">=", value: from }, { field: window.untilField ?? window.field, op: "<", value: to },
  ] };
}

export function rollingQueryMatches(query: QueryState, window: RollingWindow): boolean {
  if (query.grouping) return rollingQueryMatches(query.grouping.source, window);
  // An open table may still carry yesterday's resolved dates. Sorting or
  // paging does not turn its calendar window into a fixed range. Count local
  // days, rather than hours, so spans crossing daylight saving still match.
  const bounds = query.where.filter(term => "field" in term && (term.field === window.field || term.field === window.untilField) && !term.negated && ["=", ">", ">=", "<", "<="].includes(term.op));
  if (bounds.length !== 2) return false;
  const from = bounds.find(term => "field" in term && term.op === ">="), to = bounds.find(term => "field" in term && term.op === "<");
  if (!from || !to || !("field" in from) || !("field" in to) || from.field !== window.field || to.field !== (window.untilField ?? window.field)) return false;
  if (window.mode === "relative") return from.value === `-${window.days}d` && to.value === "+0s";
  if (window.mode === "today") {
    const start = new Date(from.value);
    return to.value === "+0s" && Number.isFinite(start.getTime()) && (start.getTime() === Date.parse(todayBounds(start)[0]) || Boolean(window.timezone && start.getTime() === Date.parse(todayBounds(start, window.timezone)[0])));
  }
  const start = new Date(from.value), end = new Date(to.value);
  if (![start, end].every(date => Number.isFinite(date.getTime()) && date.getHours() === 0 && date.getMinutes() === 0 && date.getSeconds() === 0 && date.getMilliseconds() === 0)) return false;
  start.setDate(start.getDate() + window.days);
  return start.getTime() === end.getTime();
}

export function queryURL(dataset: QueryDataset, query: QueryState, window?: RollingWindow): string {
  const params = new URLSearchParams({ [`q_${dataset}`]: encodeQuery(window?.mode === "relative" || window?.mode === "today" ? resolveRollingQuery(query, window) : query) });
  let path = "/library";
  if (Object.hasOwn(usageDatasetTopics, dataset)) {
    path = "/analyze";
    params.set("analyze_mode", "query");
    params.set("analyze_topic", usageDatasetTopics[dataset as UsageDataset]);
    params.set("analyze_data", dataset);
  } else if (dataset === "findings") {
    path = "/findings"; params.set("view", "table");
  }
  if (window) {
    if (window.mode === "relative" || window.mode === "today") params.set("window_mode", window.mode);
    if (window.mode === "today") params.set("window_timezone", localTimezone());
    if (window.untilField) params.set("window_until_field", window.untilField);
    params.set("window_days", String(window.days)); params.set("window_field", window.field); params.set("window_dataset", dataset);
  }
  // Host query state also travels in its own parameters, as the table mirrors
  // it, so previews and older readers of the address see the same search.
  for (const [name, values] of Object.entries(query.hostState?.params ?? {})) for (const value of values) params.append(name, value);
  return `${path}?${params}`;
}

export function queryFromURL(url: string, fallback?: QueryState): { dataset: QueryDataset; query: QueryState; window?: RollingWindow } | undefined {
  try {
    const parsed = new URL(url, location.origin);
    if (parsed.origin !== location.origin) return;
    const params = parsed.searchParams, path = parsed.pathname;
    const workspace = path === "/analyze" && isUsageWorkspace(params) ? readUsageWorkspace(params) : null;
    // Older URLs can carry offscreen tables' state as well. The route and
    // selected tab decide the dataset, rather than the first q_* parameter.
    const dataset: QueryDataset | undefined = workspace ? workspace.mode === "dashboards" ? cardSourceDataset(params) : workspace.dataset
      : path === "/library" || path === "/" ? "library"
      : path === "/analyze" && params.get("machine_view") === "parallelism" && params.has("q_parallelism") ? "parallelism"
      : path === "/findings" ? params.get("view") === "table" ? "findings" : undefined
      : path === "/analyze" ? params.get("analyze_view") === "conversations" ? "conversation_activity" : params.get("analyze_view") === "writing" ? params.get("writing") === "messages" ? "writing_messages" : "writing" : "usage"
      : path === "/tools" ? params.get("tools_view") === "summary" ? "tools" : params.get("tools_view") === "skills" ? "skill_usages" : "tool_calls"
      : path === "/mcp" ? "mcp_calls" : undefined;
    if (dataset) {
      const token = params.get(`q_${dataset}`);
      if (token !== null) {
        if (params.getAll(`q_${dataset}`).length !== 1) return;
        const query = token.startsWith("@") ? fallback ?? (() => {
          const snapshot = localQuerySnapshotCache().get(`pharos_${dataset}`, token.slice(1));
          return snapshot ? decodeQueryStrict(snapshot.token) : undefined;
        })() : decodeQueryStrict(token);
        if (query) return { dataset, query, window: readRollingWindow(params, dataset) };
      }
    }
  } catch { /* Old non-query bookmarks are still valid collection entries. */ }
}
