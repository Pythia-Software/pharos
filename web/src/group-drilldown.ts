import type { FieldSchema, QueryState, WhereTerm } from "@pythia-software/query-table-core";

export function groupSourceQuery(query: QueryState, row: Record<string, unknown>, schema: FieldSchema): QueryState {
  if (!query.grouping) return query;
  const filters = query.grouping.keys.map((field): WhereTerm => {
    if (!Object.hasOwn(row, field)) throw new Error(`Missing group key: ${field}`);
    const value = row[field];
    if (value === null || value === undefined) return { field, op: "is_null", value: "" };
    const type = schema.fields.find(column => column.name === field)?.type;
    return { field, op: "=", value: type === "datetime" ? new Date(value as string | number).toISOString() : String(value) };
  });
  return { ...query.grouping.source, offset: 0, where: [...query.grouping.source.where, ...filters] };
}
