import { ViewDisclosure } from "./view-disclosure";
import React, { useEffect, useState } from "react";
import type { QueryTableApi } from "@pythia-software/query-table-react";
import { TextSearchEditor } from "./text-search-editor";
import { SearchOrderControl } from "./search-order";
import { describeWhere, filterIssue, SearchFilters, SearchFilterFields } from "./search-filters";
import type { QueryState } from "@pythia-software/query-table-core";
import { emptyTextSearch, scopes, type TextSearchDraft } from "./text-search-model";
export const newMessageSearch = () => { const draft = emptyTextSearch(); draft.scope.surface = "user"; return draft; };
// onApply receives the search with the record filters and order drafted beside
// it, so the host can commit them as one definition.
export function MessageTableSearch<Row>({ api, value, active, initialError, onApply, onClear, layout }: { api: QueryTableApi<Row>; value: TextSearchDraft; active: boolean; initialError?: string; onApply: (next: TextSearchDraft, table: Pick<QueryState, "where" | "orderBy">) => void; onClear: () => void; layout: React.ReactNode }) {
  const [where, setWhere] = useState(api.query.where), [order, setOrder] = useState(api.query.orderBy);
  useEffect(() => { setWhere(api.query.where); setOrder(api.query.orderBy); }, [api.query.where, api.query.orderBy]);
  return <>
    <TextSearchEditor filterError={filterIssue(api.computed.catalogue, where)} value={value} active={active} initialError={initialError} textOnly allowedScopes={["user", ...scopes.slice(8).map(s => s.id)]} kind="text" onKind={() => {}} onClear={onClear}
      onApply={next => onApply(next, { where, orderBy: order })}
      file={{ text: "", active: false, dirty: false, caseSensitive: false, onText() {}, onCase() {}, onApply() {}, onClear() {} }}
      workspaceScope={{ filters: [], reading: describeWhere(api.computed.catalogue, where), caption: "Message record", sentenceLead: "where ", inline: <SearchFilters fields={api.computed.catalogue} where={where} onChange={setWhere} subject="Message record" />, add: <SearchFilterFields fields={api.computed.catalogue.filter(f => f.name !== "text")} label="Record property" onAdd={clause => setWhere([...where, clause])} /> }}
      orderControl={<SearchOrderControl catalogue={api.computed.catalogue} subject="messages in this batch" order={order.length ? order : api.defaults.orderBy} onChange={setOrder} batched={active} />} />
    <ViewDisclosure viewKey={`query:${api.schema?.name ?? "messages"}:layout`} className="message-search-layout" summary="Table layout & saved queries">{layout}</ViewDisclosure>
  </>;
}
