import React, { useEffect, useState } from "react";
import type { QueryTableApi } from "@pythia-software/query-table-react";
import { TextSearchEditor } from "./text-search-editor";
import { SearchOrderControl } from "./search-order";
import { filterIssue, SearchFilters, SearchFilterFields } from "./search-filters";
import { emptyTextSearch, readTextSearch, scopes, type TextSearchDraft } from "./text-search-model";
export const newMessageSearch = () => { const draft = emptyTextSearch(); draft.scope.surface = "user"; return draft; };
export function readMessageSearch() {
  const token = new URLSearchParams(location.search).get("message_query");
  return token ? readTextSearch(new URLSearchParams({ text_builder: token })) : { draft: newMessageSearch(), active: false, error: "" };
}
export function MessageTableSearch<Row>({ api, value, active, initialError, onApply, onClear, layout }: { api: QueryTableApi<Row>; value: TextSearchDraft; active: boolean; initialError?: string; onApply: (next: TextSearchDraft) => void; onClear: () => void; layout: React.ReactNode }) {
  const [where, setWhere] = useState(api.query.where), [order, setOrder] = useState(api.query.orderBy);
  useEffect(() => { setWhere(api.query.where); setOrder(api.query.orderBy); }, [api.query.where, api.query.orderBy]);
  return <>
    <TextSearchEditor filterError={filterIssue(api.computed.catalogue, where)} value={value} active={active} initialError={initialError} textOnly allowedScopes={["user", ...scopes.slice(8).map(s => s.id)]} kind="text" onKind={() => {}} onClear={onClear}
      onApply={next => { api.setQuery(previous => ({ ...previous, where, orderBy: order, offset: 0 })); onApply(next); }}
      file={{ text: "", active: false, dirty: false, caseSensitive: false, onText() {}, onCase() {}, onApply() {}, onClear() {} }}
      workspaceScope={{ filters: [], caption: "Matching message record", inline: <SearchFilters fields={api.computed.catalogue} where={where} onChange={setWhere} subject="Message record" />, add: <SearchFilterFields fields={api.computed.catalogue.filter(f => f.name !== "text")} label="Record filter" onAdd={clause => setWhere([...where, clause])} /> }}
      orderControl={<SearchOrderControl catalogue={api.computed.catalogue} subject="messages in this batch" order={order.length ? order : api.defaults.orderBy} onChange={setOrder} batched={active} />} />
    <details className="message-search-layout"><summary>Table layout & saved queries</summary>{layout}</details>
  </>;
}
