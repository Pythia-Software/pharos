import { exitGrouping, type QueryState } from "@pythia-software/query-table-core";

export type SearchResultsView = "table" | "conversation";

// Groups have no conversation to read. Keep the preferred presentation so it
// returns when grouping is removed, while showing the actual current output.
export function searchResultsView(query: QueryState, preferred: SearchResultsView): SearchResultsView {
  return query.grouping ? "table" : preferred;
}

export function queryForSearchResultsView(query: QueryState, view: SearchResultsView): QueryState {
  return view === "conversation" && query.grouping ? exitGrouping(query) : query;
}
