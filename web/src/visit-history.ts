import { branchesOf, decodeQueryStrict, isCalendarGroupKey, type GroupKey, type WhereClause } from "@pythia-software/query-table-core";

// A visit's description must preserve the meaning of compressed Query Table
// links. It is also the text Ask Pharos searches when recalling a filtered view.
export function visitQuerySummary(token: string): string {
  try {
    const query = decodeQueryStrict(token), source = query.grouping?.source ?? query;
    const name = (field: string) => field.replaceAll("_", " ");
    const clause = (term: WhereClause) => `${term.negated ? "not " : ""}${name(term.field)} ${term.op} ${term.value ?? ""}`.trim();
    const branch = (clauses: WhereClause[]) => clauses.length > 1 ? `(${clauses.map(clause).join(" and ")})` : clause(clauses[0]);
    const filters = source.where.map(term => "any" in term ? `(${branchesOf(term).map(branch).join(" or ")})` : clause(term));
    const key = (key: GroupKey) => isCalendarGroupKey(key) ? `${name(key.field)} by ${key.interval}` : name(key);
    const groups = [...new Set([...(query.grouping?.keys ?? []).map(key), ...(source.aggregations ?? []).flatMap(metric => metric.groupBy ?? []).map(name)])];
    const order = query.orderBy.map(term => `${name(term.field)} ${term.dir}`);
    return [...filters, ...(groups.length ? [`by ${groups.join(", ")}`] : []), ...(order.length ? [`sorted by ${order.join(", ")}`] : [])].join("; ");
  } catch { return ""; }
}

declare global { interface Window { pharosDescribeVisitQuery?: (token: string) => string; } }
if (typeof window !== "undefined") window.pharosDescribeVisitQuery = visitQuerySummary;
