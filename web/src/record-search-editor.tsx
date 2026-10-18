import React, { useEffect, useState } from "react";
import { type WhereTerm } from "@pythia-software/query-table-core";
import type { QueryTableApi } from "@pythia-software/query-table-react";
import { AddFieldCondition, describeWhere, filterIssue, SearchFilters } from "./search-filters";
import { SearchReading } from "./text-search-editor";
import { SearchOrderControl } from "./search-order";
import { SearchHelp } from "./search-help";
import { Icon } from "./icons";
export const recordSearchTargets: Record<string, { label: string; field: string }> = {
  writing_messages: { label: "User messages", field: "text" },
  tool_calls: { label: "Tool calls", field: "command" },
  mcp_calls: { label: "MCP calls", field: "arguments_json" },
  tools: { label: "Tool summaries", field: "tool_name" },
  skill_usages: { label: "Skill uses", field: "skill_name" },
};
export function RecordSearchEditor<Row>({ api, dataset, layout }: { api: QueryTableApi<Row>; dataset: string; layout: React.ReactNode }) {
  const target = recordSearchTargets[dataset], fields = api.computed.catalogue;
  const [where, setWhere] = useState(api.query.where), [order, setOrder] = useState(api.query.orderBy);
  useEffect(() => { setWhere(api.query.where); setOrder(api.query.orderBy); }, [api.query.where, api.query.orderBy]);
  const dirty = JSON.stringify([where, order]) !== JSON.stringify([api.query.where, api.query.orderBy]);
  const issue = filterIssue(fields, where);
  const valid = !issue;
  const change = (next: WhereTerm[]) => setWhere(next);
  return <section className="text-search-editor record-search-editor" aria-label={`Search ${target.label.toLowerCase()}`}>
    <form onSubmit={event => { event.preventDefault(); if (valid) api.setQuery(previous => ({ ...previous, where, orderBy: order, offset: 0 })); }}>
      <SearchReading parts={["Find ", target.label.toLowerCase(), ...(where.length ? [" where ", ...describeWhere(fields, where).flatMap((clause, i) => [...(i ? [" and "] : []), ...clause])] : [])]} empty={!where.length} />
      <div className="text-query-frame">
        <div className="text-query-conditions">
          <div className="text-group-heading text-query-heading"><span className="text-join">Find</span><strong className="record-search-target">{target.label}</strong><SearchHelp label="Record search">Conditions match the records in this table. Rows, counts, and charts use the same applied filters. Available operators depend on the selected field. Message proximity and message-type search belong in Library.</SearchHelp></div>
          {where.length ? <SearchFilters fields={fields} where={where} onChange={change} lead="with" /> : <div className="text-condition"><span className="text-join" aria-hidden="true" /><p className="record-search-empty">All {target.label.toLowerCase()}. Add a condition to narrow them.</p></div>}
        </div>
        <div className="text-query-tools"><AddFieldCondition fields={fields} first={target.field} onAdd={clause => setWhere([...where, clause])} /></div>
      </div>
      <div className="text-editor-messages">{issue ? <p role="alert" className="text-editor-error">{issue}</p> : null}</div>
      <div className="text-editor-footer">
        <SearchOrderControl catalogue={fields} subject={target.label.toLowerCase()} order={order.length ? order : api.defaults.orderBy} onChange={setOrder} />
        <div className="text-submit-actions">
          {dirty ? <p className="text-draft-status" role="status">Changes haven't been searched yet.</p> : null}
          {where.length ? <button type="button" onClick={() => setWhere([])}>Clear</button> : null}
          <button type="submit" className="text-search-submit" disabled={!valid}><Icon name="search" />Search {target.label.toLowerCase()}</button>
        </div>
        <details><summary>Columns, metrics & saved queries</summary>{layout}</details>
      </div>
    </form>
  </section>;
}
