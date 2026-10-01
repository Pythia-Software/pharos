import { isOrGroup, type WhereClause, type WhereTerm } from "@pythia-software/query-table-core";

export type LegendAction = "only" | "toggle" | "exclude" | "clear";
export type SliceValue = string | null;
export type SliceSelection<Value> = { values: Value[]; excluded: boolean };

function clauseSelection(clause: WhereClause, field: string): SliceSelection<SliceValue> | null {
  if (clause.field !== field) return null;
  if (!clause.negated && (clause.op === "=" || clause.op === "is_null")) return { values: [clause.op === "is_null" ? null : clause.value], excluded: false };
  if ((!clause.negated && clause.op === "is_not_null") || (clause.negated && clause.op === "is_null")) return { values: [null], excluded: true };
  if ((!clause.negated && clause.op === "!=") || (clause.negated && clause.op === "=")) return { values: [clause.value, null], excluded: true };
  return null;
}

function termSelection(term: WhereTerm, field: string): SliceSelection<SliceValue> | null {
  if (!isOrGroup(term)) return clauseSelection(term, field);
  const parts = term.any.map(clause => clauseSelection(clause, field));
  if (!parts.length || parts.some(part => !part)) return null;
  if (parts.every(part => !part!.excluded)) return { values: parts.flatMap(part => part!.values), excluded: false };
  if (parts.length === 2) {
    const excluded = parts.find(part => part!.excluded), included = parts.find(part => !part!.excluded);
    if (excluded && included?.values.length === 1 && included.values[0] === null && excluded.values.some(value => value !== null)) return { values: excluded.values.filter(value => value !== null), excluded: true };
  }
  return null;
}

export function isSliceTerm(term: WhereTerm, field: string): boolean {
  return termSelection(term, field) !== null;
}

export function readSlice(where: WhereTerm[], field: string): SliceSelection<SliceValue> {
  const parts = where.map(term => termSelection(term, field)).filter((part): part is SliceSelection<SliceValue> => part !== null);
  const excluded = parts[0]?.excluded ?? false;
  return { values: [...new Set(parts.filter(part => part.excluded === excluded).flatMap(part => part.values))], excluded };
}

export function withSlice(where: WhereTerm[], field: string, selection: SliceSelection<SliceValue>): WhereTerm[] {
  const kept = where.filter(term => !isSliceTerm(term, field));
  if (selection.excluded) {
    for (const value of selection.values) kept.push(value === null ? { field, op: "is_not_null", value: "" } : { any: [{ field, op: "!=", value }, { field, op: "is_null", value: "" }] });
  } else {
    const clauses: WhereClause[] = selection.values.map(value => value === null ? { field, op: "is_null", value: "" } : { field, op: "=", value });
    if (clauses.length === 1) kept.push(clauses[0]);
    else if (clauses.length) kept.push({ any: clauses });
  }
  return kept;
}

export function updateSlice<Value>(selection: SliceSelection<Value>, value: Value, action: LegendAction): SliceSelection<Value> {
  if (action === "clear") return { values: [], excluded: false };
  if (action === "only") return { values: [value], excluded: false };
  if (action === "exclude") return { values: [value], excluded: true };
  const values = selection.values.includes(value) ? selection.values.filter(item => item !== value) : [...selection.values, value];
  return { values, excluded: values.length > 0 && selection.excluded };
}
