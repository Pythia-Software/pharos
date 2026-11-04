import { EMPTY_QUERY, encodeQuery, type QueryState, type WhereClause } from "@pythia-software/query-table-core";
export const safetyViews = ["", "table", "settings"] as const;
export type SafetyView = typeof safetyViews[number];
export const safetyStateLabels = { open: "Open", handed_off: "Handed off", dismissed: "Closed" };
export function safetyURL(view: SafetyView = ""): string {
  return `/findings?${new URLSearchParams({ optimize_area: "safety", ...(view ? { view } : {}) })}`;
}
export function safetyDetailURL(id: string, current?: URL): string {
  const url = current ? new URL(current.href) : new URL(safetyURL(), "http://localhost");
  url.searchParams.set("optimize_area", "safety");
  url.searchParams.set("safety_case", id);
  return url.pathname + url.search;
}

// One check's notices as a table of the resources it selected, sorted by
// path, so a check with many notices can be read at once. State and location
// narrow it the same way the dashboard's tabs and place filter do. The
// columns lead with the path and the command that selected it; the query
// builder starts collapsed, since the page states what the table holds.
export function safetyCheckTableURL(rule: string, { state, target }: { state?: string; target?: string } = {}): string {
  const where: WhereClause[] = [{ field: "rule", op: "=", value: rule }];
  if (state) where.push({ field: "state", op: "=", value: state });
  if (target) where.push({ field: "target", op: "=", value: target });
  const select = [{ field: "subject", width: 340 }, { field: "command", width: 400 }, ...target ? [] : [{ field: "target_label", width: 130 }], { field: "last_seen_at", width: 110 }, ...state ? [] : [{ field: "state", width: 110 }], { field: "cwd", width: 240 }];
  const url = new URL(safetyURL("table"), "http://localhost");
  url.searchParams.set("q_safety", encodeQuery({ ...EMPTY_QUERY, where, orderBy: [{ field: "subject", dir: "asc" }], select, fitToScreen: true }));
  url.searchParams.set("query_layout", JSON.stringify({ safety: true }));
  return url.pathname + url.search;
}

export type SafetyTableFocus = { rule: string; state?: string; target?: string; place?: string };
// The check a table query is limited to, with its state and location, when
// each is one plain equality. Any other query is a general table.
export function safetyTableFocus(query: QueryState): SafetyTableFocus | null {
  const source = query.grouping?.source ?? query;
  const equal = (field: string) => source.where.flatMap(term => "field" in term && term.field === field && term.op === "=" && !term.negated ? [term.value] : []);
  const [rule, ...others] = equal("rule"), [state] = equal("state"), [target] = equal("target"), [place] = equal("target_label");
  if (!rule || others.length) return null;
  // A filter from a Location cell names the place rather than its ID.
  return { rule, ...equal("state").length === 1 ? { state } : {}, ...equal("target").length === 1 ? { target } : {}, ...equal("target_label").length === 1 ? { place } : {} };
}
