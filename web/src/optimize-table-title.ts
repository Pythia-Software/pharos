import type { FieldSchema, QueryState, WhereClause } from "@pythia-software/query-table-core";
import { dashboardQueryDetails } from "./dashboard-query-links";
import type { QueryTableApi } from "@pythia-software/query-table-react";

export type OptimizeTableSchemas = { source: Pick<FieldSchema, "fields">; result: Pick<FieldSchema, "fields"> };

// Query Table projects its result catalogue when grouping; source WHERE still
// needs the source catalogue (including computed fields and enum labels).
export function optimizeTableSchemas(api: Pick<QueryTableApi<Record<string, any>>, "query" | "grouping" | "computed">): OptimizeTableSchemas {
  const result = { fields: api.computed.catalogue };
  return { source: api.query.grouping ? api.grouping!.source.schema : result, result };
}

// Name the records (or groups) actually in the query, including source and
// group filters. Sort and column choices do not change the population.
export function optimizeTableTitle(noun: string, schema: Pick<FieldSchema, "fields">, query: QueryState, resultSchema: Pick<FieldSchema, "fields"> = schema): string {
  const details = dashboardQueryDetails(schema, query, resultSchema);
  const values = (label: string) => details.find(detail => detail.label === label)?.values ?? [];
  const filters = values("Filters"), groups = values("Group by"), groupFilters = values("Group filters");
  return `${noun}${filters.length ? ` where ${filters.join(" and ")}` : ""}${query.grouping ? groups.length ? ` grouped by ${groups.join(" and ")}` : " grouped" : ""}${groupFilters.length ? `, keeping groups where ${groupFilters.join(" and ")}` : ""}`;
}

// Omit only predicates already named by a focused heading. OR groups and
// additional conditions stay visible, including grouped-result filters.
export function optimizeTableRemainder(schema: Pick<FieldSchema, "fields">, query: QueryState, named: Pick<WhereClause, "field" | "op" | "value">[], resultSchema: Pick<FieldSchema, "fields"> = schema): string {
  const source = query.grouping?.source ?? query;
  const where = source.where.filter(term => !("field" in term && !term.negated && named.some(item => item.field === term.field && item.op === term.op && item.value === term.value)));
  const remainder = query.grouping ? { ...query, grouping: { ...query.grouping, source: { ...source, where } } } : { ...query, where };
  return optimizeTableTitle("", schema, remainder, resultSchema).trim();
}
