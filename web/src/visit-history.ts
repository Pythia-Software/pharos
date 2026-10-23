import { decodeQueryStrict, type WhereClause } from "@pythia-software/query-table-core";

// A visit's description must preserve the meaning of compressed Query Table
// links. It is also the text Ask Pharos searches when recalling a filtered view.
export function visitQuerySummary(token: string): string {
  try {
    const query = decodeQueryStrict(token), source = query.grouping?.source ?? query;
    const name = (field: string) => field.replaceAll("_", " ");
    const clause = (term: WhereClause) => `${term.negated ? "not " : ""}${name(term.field)} ${term.op} ${term.value ?? ""}`.trim();
    const filters = source.where.map(term => "any" in term ? `(${term.any.map(clause).join(" or ")})` : clause(term));
    const groups = [...new Set([...(query.grouping?.keys ?? []), ...(source.aggregations ?? []).flatMap(metric => metric.groupBy ?? [])])];
    const order = query.orderBy.map(term => `${name(term.field)} ${term.dir}`);
    return [...filters, ...(groups.length ? [`by ${groups.map(name).join(", ")}`] : []), ...(order.length ? [`sorted by ${order.join(", ")}`] : [])].join("; ");
  } catch { return ""; }
}

declare global { interface Window { pharosDescribeVisitQuery?: (token: string) => string; } }
if (typeof window !== "undefined") window.pharosDescribeVisitQuery = visitQuerySummary;
