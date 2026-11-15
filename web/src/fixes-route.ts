import { EMPTY_QUERY, encodeQuery, type QueryState, type WhereClause } from "@pythia-software/query-table-core";
// Optimize → Fixes: recurring agent problems a change to guidance, the
// environment, a tool, setup or docs could prevent.
export const fixesViews = ["", "table", "settings"] as const;
export type FixesView = typeof fixesViews[number];
export const fixesStateLabels = { open: "Open", handed_off: "Handed off", dismissed: "Closed" };
export function fixesURL(view: FixesView = ""): string {
  return `/findings?${new URLSearchParams({ optimize_area: "fixes", ...(view ? { view } : {}) })}`;
}
// One fix's detail. From the table, the table's query stays for the way back.
export function fixDetailURL(id: string, current?: URL): string {
  const url = current ? new URL(current.href) : new URL(fixesURL(), "http://localhost");
  url.searchParams.set("optimize_area", "fixes");
  url.searchParams.set("fixes_item", id);
  return url.pathname + url.search;
}
// The table of every fix the bounds hide from Open, by why.
export function fixesHiddenTableURL(): string {
  const where: WhereClause[] = [{ field: "state", op: "=", value: "open" }, { field: "shown", op: "!=", value: "shown" }];
  const url = new URL(fixesURL("table"), "http://localhost");
  url.searchParams.set("q_fixes", encodeQuery({ ...EMPTY_QUERY, where, orderBy: [{ field: "priority", dir: "desc" }] }));
  return url.pathname + url.search;
}
export type FixesTableFocus = { shown?: string; state?: string };
export function fixesTableFocus(query: QueryState): FixesTableFocus {
  const source = query.grouping?.source ?? query;
  const value = (field: string, op: string) => source.where.flatMap(term => "field" in term && term.field === field && term.op === op && !term.negated ? [String(term.value)] : [])[0];
  return { shown: value("shown", "!=") === "shown" ? "hidden" : value("shown", "="), state: value("state", "=") };
}
