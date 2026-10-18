import React from "react";
import { isSortable, MAX_ORDER_BY_TERMS, type FieldDef, type OrderByClause } from "@pythia-software/query-table-core";
import type { QueryTableApi } from "@pythia-software/query-table-react";
import { Icon } from "./icons";
import { SearchHelp } from "./search-help";
import { SearchSelect } from "./search-select";

// Share the table controller so headers, saved views, and the query editor
// always show the same ordering, including advanced saved sort clauses.
export function SearchOrder<Row>({ api, batched }: { api: QueryTableApi<Row>; batched: boolean }) {
  return <SearchOrderControl catalogue={api.computed.catalogue} order={api.query.orderBy.length ? api.query.orderBy : api.defaults.orderBy} onChange={api.setSort} batched={batched} />;
}
export function SearchOrderControl({ catalogue, order, onChange, batched = false, subject = "workspaces" }: { catalogue: FieldDef[]; order: OrderByClause[]; onChange: (order: OrderByClause[]) => void; batched?: boolean; subject?: string }) {
  const fields = [...new Map(catalogue.filter(isSortable).map(field => [field.sort?.field ?? field.name, field])).entries()];
  const available = fields.filter(([name]) => !order.some(term => term.field === name));
  const update = (index: number, patch: Partial<OrderByClause>) => onChange(order.map((term, i) => i === index ? { ...term, ...patch } : term));
  const labels = (field: string) => {
    const type = fields.find(([name]) => name === field)?.[1].type;
    return type === "datetime" ? ["Oldest first", "Newest first"] : type === "number" ? ["Low to high", "High to low"] : type === "text" ? ["A → Z", "Z → A"] : ["Ascending", "Descending"];
  };
  return <div className="text-search-order" aria-label="Result ordering">
    <span className="text-order-label">Sort by</span>
    {order.map((term, index) => <React.Fragment key={index}>
      {index ? <span className="text-order-then">then</span> : null}
      <div className="text-sort-clause">
        <SearchSelect aria-label={`Sort field ${index + 1}`} value={term.field} onChange={event => update(index, { field: event.target.value })}>
          {!fields.some(([name]) => name === term.field) ? <option value={term.field}>{term.field}</option> : null}
          {fields.filter(([name]) => name === term.field || !order.some(other => other.field === name)).map(([name, field]) => <option value={name} key={name}>{field.label ?? name}</option>)}
        </SearchSelect>
        <SearchSelect aria-label={`Sort direction ${index + 1}`} value={term.dir} onChange={event => update(index, { dir: event.target.value as OrderByClause["dir"] })}><option value="asc">{labels(term.field)[0]}</option><option value="desc">{labels(term.field)[1]}</option></SearchSelect>
        {term.extract ? <span className="text-sort-detail" title={`Sort uses regex extraction: ${term.extract.regex}`}>Extract: <code>{term.extract.regex}</code></span> : null}
        {term.nulls === "first" ? <span className="text-sort-detail">Empty first</span> : null}
        {order.length > 1 ? <button type="button" className="text-remove" aria-label={`Remove sort ${index + 1}`} title="Remove sort" onClick={() => onChange(order.filter((_, i) => i !== index))}><Icon name="close" /></button> : null}
      </div>
    </React.Fragment>)}
    {available.length && order.length < MAX_ORDER_BY_TERMS ? <SearchSelect className="text-add-sort" aria-label="Add sort field" value="" onChange={event => { if (event.target.value) onChange([...order, { field: event.target.value, dir: "asc" }]); }}><option value="">{order.length ? "+ then by" : "Choose field"}</option>{available.map(([name, field]) => <option key={name} value={name}>{field.label ?? name}</option>)}</SearchSelect> : null}
    {batched ? <span className="text-sort-detail">This batch</span> : null}
    {batched || order.some(term => term.extract || term.nulls === "first") ? <SearchHelp label="Result ordering">{batched ? `Sorting orders the matching ${subject} in the current search batch only; it does not change which messages the next batch checks. ` : ""}{order.some(term => term.extract || term.nulls === "first") ? "Regex extraction and empty-value ordering are edited in the table layout controls." : ""}</SearchHelp> : null}
  </div>;
}
