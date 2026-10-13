import React from "react";
import { coerceValue, isFilterable, NULLARY_OPS, opsForField, negateClause, type FieldDef, type WhereClause, type WhereTerm } from "@pythia-software/query-table-core";
import { SearchSelect } from "./search-select";
import { SearchInput } from "./search-input";
import { SearchPopover } from "./text-search-editor";
import { Icon } from "./icons";

const labels: Record<string, string> = { "=": "is", "!=": "is not", ">=": "at least", "<=": "at most", ">": "after / greater than", "<": "before / less than", contains: "contains text", starts_with: "starts with", ends_with: "ends with", has_word: "contains word", has_words: "contains all words", matches_regex: "matches pattern", not_matches_regex: "does not match pattern", is_null: "is empty", is_not_null: "is not empty", includes: "includes" };
export function filterIssue(fields: FieldDef[], where: WhereTerm[]): string {
  for (const term of where) for (const clause of "any" in term ? term.any : [term]) {
    const field = fields.find(f => f.name === clause.field);
    if (NULLARY_OPS.has(clause.op)) continue;
    if (!clause.value.trim()) return `Enter a value for ${field?.label ?? clause.field}, or remove its filter.`;
    if (field) { try { coerceValue(field.type, clause.value); } catch { return `Enter a valid ${field.type} value for ${field.label}.`; } }
  }
  return "";
}
export function initialClause(field: FieldDef): WhereClause {
  const ops = opsForField(field);
  return { field: field.name, op: ops.includes("contains") ? "contains" : ops[0], value: "" };
}
export function SearchFilterFields({ fields, onAdd, label = "Add filter" }: { fields: FieldDef[]; onAdd: (clause: WhereClause) => void; label?: string }) {
  return <SearchSelect aria-label={label} value="" onChange={event => { const field = fields.find(f => f.name === event.target.value); if (field) onAdd(initialClause(field)); }}><option value="">+ {label}</option>{fields.filter(isFilterable).map(f => <option key={f.name} value={f.name}>{f.label}</option>)}</SearchSelect>;
}
// Keep saved OR groups and negations intact. Only expose operators declared by
// this dataset; message-tree operators are not equivalent to row predicates.
export function SearchFilters({ fields, where, onChange, subject = "" }: { fields: FieldDef[]; where: WhereTerm[]; onChange: (where: WhereTerm[]) => void; subject?: string }) {
  const edit = (index: number, term: WhereTerm) => onChange(where.map((v, i) => i === index ? term : v));
  const remove = (index: number) => onChange(where.filter((_, i) => i !== index));
  const clause = (term: WhereClause, path: string, change: (term: WhereClause) => void, drop: () => void) => {
    const field = fields.find(f => f.name === term.field);
    const values = field?.filter?.values;
    const options = values?.source === "static" ? values.options.map(v => typeof v === "string" ? { value: v, label: v } : v) : field?.type === "bool" ? [{ value: "true", label: "Yes" }, { value: "false", label: "No" }] : undefined;
    const operators = field ? opsForField(field) : [term.op];
    return <div className="text-filter-clause" key={path}>
      {term.negated ? <span className="text-join">NOT</span> : null}
      {subject ? <span className="text-filter-subject">{subject}</span> : null}
      <SearchSelect aria-label={`Filter field ${path}`} value={term.field} onChange={event => { const next = fields.find(f => f.name === event.target.value); if (next) change(initialClause(next)); }}>
        {!field ? <option value={term.field}>{term.field}</option> : null}{fields.filter(isFilterable).map(f => <option value={f.name} key={f.name}>{f.label}</option>)}
      </SearchSelect>
      <SearchSelect aria-label={`Filter operator ${path}`} value={term.op} onChange={event => change({ ...term, setFilter: undefined, op: event.target.value as WhereClause["op"] })}>
        {[...new Set([...operators, term.op])].map(op => <option value={op} key={op}>{labels[op] ?? op.replaceAll("_", " ")}</option>)}
      </SearchSelect>
      {!NULLARY_OPS.has(term.op) ? options ? <SearchSelect className="text-value-select" aria-label={`Filter value ${path}`} value={term.value} onChange={event => change({ ...term, setFilter: undefined, value: event.target.value })}><option value="">Choose value…</option>{term.value && !options.some(o => o.value === term.value) ? <option value={term.value}>{term.value}</option> : null}{options.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}</SearchSelect> : <SearchInput aria-label={`Filter value ${path}`} value={term.value} placeholder={field?.type === "datetime" ? "Date or timestamp" : "Value…"} onChange={event => change({ ...term, setFilter: undefined, value: event.target.value })} /> : null}
      <button type="button" className="text-remove" aria-label={`Remove filter ${path}`} onClick={drop}><Icon name="close" /></button>
      <SearchPopover label={`Options for filter ${path}`} className="text-condition-options" triggerContent={<Icon name="settings" />}>{close => <button type="button" onClick={() => { close(); change({ ...negateClause(term, operators), setFilter: undefined }); }}>Negate this condition</button>}</SearchPopover>
    </div>;
  };
  return <>{where.map((term, index) => <div className="text-filter-condition" key={index}>
    <span className="text-join">AND</span>
    {"any" in term ? <div className="search-filter-alternatives" role="group" aria-label={`Any filter in group ${index + 1}`}>
      {term.any.map((item, j) => <React.Fragment key={j}>{j ? <span className="text-join">OR</span> : null}{clause(item, `${index + 1}.${j + 1}`, next => edit(index, { any: term.any.map((v, k) => k === j ? next : v) }), () => term.any.length === 1 ? remove(index) : edit(index, { any: term.any.filter((_, k) => k !== j) }))}</React.Fragment>)}
    </div> : clause(term, `${index + 1}`, next => edit(index, next), () => remove(index))}
    <SearchPopover label={`Options for filter group ${index + 1}`} className="text-condition-options" triggerContent={<Icon name="plus" />}>{close => <button type="button" onClick={() => { close(); const first = "any" in term ? term.any[0] : term; edit(index, { any: [...("any" in term ? term.any : [term]), { ...first, value: "", setFilter: undefined }] }); }}>Add OR alternative</button>}</SearchPopover>
  </div>)}</>;
}
