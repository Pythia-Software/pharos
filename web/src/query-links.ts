import { decodeQuery, encodeQuery, type QueryState } from "@pythia-software/query-table-core";

export const queryDatasets = ["library", "usage", "writing", "writing_messages", "mcp_calls", "tools", "tool_calls", "skill_usages", "tl1_attempts"] as const;
export type QueryDataset = typeof queryDatasets[number];
export type RollingWindow = { days: number; field: string };
const timeFields: Partial<Record<QueryDataset, string[]>> = {
  library: ["activity_at"], usage: ["first_usage_at"], tool_calls: ["started_at"],
  skill_usages: ["created_at"], writing_messages: ["sent_at"],
};
export const rollingWindowDays = [1, 7, 30, 90] as const;
export function rollingWindowField(dataset: QueryDataset): string | undefined { return timeFields[dataset]?.[0]; }

// Until QT's relative bounds land, URLs carry a host-owned rolling window.
// Resolve it for both previews and navigation. These are local calendar days,
// including today; daily Usage facts are never cut into imaginary partial rows.
export function rollingBounds(days: number, clock = new Date()): [string, string] {
  const start = new Date(clock), end = new Date(clock);
  start.setHours(0, 0, 0, 0); start.setDate(start.getDate() - days + 1);
  end.setHours(0, 0, 0, 0); end.setDate(end.getDate() + 1);
  return [start.toISOString(), end.toISOString()];
}

export function readRollingWindow(params: URLSearchParams, dataset: QueryDataset): RollingWindow | undefined {
  const days = Number(params.get("window_days")), field = params.get("window_field") ?? "";
  if (params.get("window_dataset") !== dataset || !rollingWindowDays.some(value => value === days) || !timeFields[dataset]?.includes(field)) return;
  return { days, field };
}

export function updateQueryWindowURL(url: string, days: number): string {
  const spec = queryFromURL(url), field = spec && (spec.window?.field ?? rollingWindowField(spec.dataset));
  if (!spec || !field || !rollingWindowDays.some(value => value === days)) throw new Error("This view does not support that time window.");
  const window = { days, field };
  // Replace direct date selections, including older fixed ranges. Leave other
  // predicates (and compound date expressions) intact rather than rewriting
  // the user's logical query. Relative bounds are renewed on every evaluation.
  const query = resolveRollingQuery({ ...spec.query, where: spec.query.where.filter(term => !("field" in term && term.field === field && !term.negated && ["=", ">", ">=", "<", "<="].includes(term.op))) }, window);
  const parsed = new URL(url, location.origin);
  parsed.searchParams.set(`q_${spec.dataset}`, encodeQuery(query));
  parsed.searchParams.set("window_days", String(days)); parsed.searchParams.set("window_field", field); parsed.searchParams.set("window_dataset", spec.dataset);
  // Search text, chart options and all other URL context stay with the view.
  return `${parsed.pathname}${parsed.search}${parsed.hash}`;
}

export function resolveRollingQuery(query: QueryState, window?: RollingWindow, clock = new Date()): QueryState {
  if (!window) return query;
  const [from, to] = rollingBounds(window.days, clock);
  return { ...query, offset: 0, where: [
    ...query.where.filter(term => !("field" in term && term.field === window.field && !term.negated && [">=", "<"].includes(term.op))),
    { field: window.field, op: ">=", value: from }, { field: window.field, op: "<", value: to },
  ] };
}

export function rollingQueryMatches(query: QueryState, window: RollingWindow): boolean {
  // An open table may still carry yesterday's resolved dates. Sorting or
  // paging does not turn its calendar window into a fixed range. Count local
  // days, rather than hours, so spans crossing daylight saving still match.
  const bounds = query.where.filter(term => "field" in term && term.field === window.field && !term.negated && ["=", ">", ">=", "<", "<="].includes(term.op));
  if (bounds.length !== 2) return false;
  const from = bounds.find(term => "field" in term && term.op === ">="), to = bounds.find(term => "field" in term && term.op === "<");
  if (!from || !to || !("field" in from) || !("field" in to)) return false;
  const start = new Date(from.value), end = new Date(to.value);
  if (![start, end].every(date => Number.isFinite(date.getTime()) && date.getHours() === 0 && date.getMinutes() === 0 && date.getSeconds() === 0 && date.getMilliseconds() === 0)) return false;
  start.setDate(start.getDate() + window.days);
  return start.getTime() === end.getTime();
}

export function queryURL(dataset: QueryDataset, query: QueryState, window?: RollingWindow): string {
  const params = new URLSearchParams({ [`q_${dataset}`]: encodeQuery(query) });
  let path = "/library";
  if (["tools", "tool_calls", "skill_usages"].includes(dataset)) {
    path = "/tools";
    if (dataset !== "tool_calls") params.set("tools_view", dataset === "tools" ? "summary" : "skills");
  } else if (["usage", "writing", "writing_messages"].includes(dataset)) {
    path = "/usage";
    params.set("usage", dataset === "usage" ? "tokens" : "writing");
    if (dataset === "writing_messages") params.set("writing", "messages");
  } else if (dataset === "mcp_calls") path = "/mcp";
  else if (dataset === "tl1_attempts") path = "/tl1/runs";
  if (window) {
    params.set("window_days", String(window.days)); params.set("window_field", window.field); params.set("window_dataset", dataset);
  }
  return `${path}?${params}`;
}

export function queryFromURL(url: string): { dataset: QueryDataset; query: QueryState; window?: RollingWindow } | undefined {
  try {
    const parsed = new URL(url, location.origin);
    if (parsed.origin !== location.origin) return;
    const params = parsed.searchParams, path = parsed.pathname;
    // Older URLs can carry offscreen tables' state as well. The route and
    // selected tab decide the dataset, rather than the first q_* parameter.
    const dataset: QueryDataset | undefined = path === "/library" || path === "/" ? "library"
      : path === "/usage" ? params.get("usage") === "writing" ? params.get("writing") === "messages" ? "writing_messages" : "writing" : "usage"
      : path === "/tools" ? params.get("tools_view") === "summary" ? "tools" : params.get("tools_view") === "skills" ? "skill_usages" : "tool_calls"
      : path === "/mcp" ? "mcp_calls" : path.startsWith("/tl1") ? "tl1_attempts" : undefined;
    if (dataset) {
      const token = params.get(`q_${dataset}`);
      if (token !== null) return { dataset, query: decodeQuery(token), window: readRollingWindow(params, dataset) };
    }
  } catch { /* Old non-query bookmarks are still valid collection entries. */ }
}
