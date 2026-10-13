import React, { useEffect, useState } from "react";
import { type WhereTerm } from "@pythia-software/query-table-core";
import type { QueryTableApi } from "@pythia-software/query-table-react";
import { filterIssue, SearchFilters, SearchFilterFields, initialClause } from "./search-filters";
import { SearchOrderControl } from "./search-order";
import { SearchHelp } from "./search-help";
import { Icon } from "./icons";
export const recordSearchTargets: Record<string, { label: string; field: string }> = {
  writing_messages: { label: "Messages from people", field: "text" },
  tool_calls: { label: "Tool calls", field: "command" },
  mcp_calls: { label: "MCP calls", field: "arguments_json" },
  tools: { label: "Tool summaries", field: "tool_name" },
  skill_usages: { label: "Skill uses", field: "skill_name" },
};
export function RecordSearchEditor<Row>({ api, dataset, layout }: { api: QueryTableApi<Row>; dataset: string; layout: React.ReactNode }) {
  const target = recordSearchTargets[dataset], fields = api.computed.catalogue;
  const [where, setWhere] = useState(api.query.where), [order, setOrder] = useState(api.query.orderBy);
  useEffect(() => { setWhere(api.query.where); setOrder(api.query.orderBy); }, [api.query.where, api.query.orderBy]);
  const add = () => { const field = fields.find(f => f.name === target.field); if (field) setWhere([...where, initialClause(field)]); };
  const dirty = JSON.stringify([where, order]) !== JSON.stringify([api.query.where, api.query.orderBy]);
  const issue = filterIssue(fields, where);
  const valid = !issue;
  const change = (next: WhereTerm[]) => setWhere(next);
  return <section className="text-search-editor record-search-editor" aria-label={`Search ${target.label.toLowerCase()}`}>
    <div className="text-editor-toolbar"><span className="text-editor-caption">Query</span><strong>{target.label}</strong><SearchHelp label="Record search">Conditions match the records in this table. Rows, counts, and charts use the same applied filters. Filter suggestions describe available field values. Available operators depend on the selected field. Message proximity and message-type search belong in Library.</SearchHelp></div>
    <form onSubmit={event => { event.preventDefault(); if (valid) api.setQuery(previous => ({ ...previous, where, orderBy: order, offset: 0 })); }}>
      <div className="text-query-frame"><div className="text-query-conditions"><span className="record-match-label">Match all filters</span><SearchFilters fields={fields} where={where} onChange={change} />{!where.length ? <span className="text-editor-scope-note">All {target.label.toLowerCase()}</span> : null}</div>
        <div className="text-query-tools"><div className="text-query-actions"><button type="button" onClick={add}><Icon name="plus" />Add text clause</button><SearchFilterFields fields={fields} onAdd={clause => setWhere([...where, clause])} /></div><SearchOrderControl catalogue={fields} subject={target.label.toLowerCase()} order={order.length ? order : api.defaults.orderBy} onChange={setOrder} /></div>
      </div>
      <div className="text-editor-footer"><details><summary>Columns, metrics & saved queries</summary>{layout}</details><div className="text-submit-actions">{where.length ? <button type="button" onClick={() => setWhere([])}>Clear filters</button> : null}<button type="submit" className="text-search-submit" disabled={!valid}><Icon name="search" />Search {target.label.toLowerCase()}</button></div></div>
      {issue ? <p role="alert" className="text-editor-error">{issue}</p> : null}
      {dirty ? <p className="text-draft-status" role="status">Changes haven't been searched yet.</p> : null}
    </form>
  </section>;
}
