import { decodeQueryStrict, localQuerySnapshotCache, EMPTY_QUERY, encodeQuery, type FieldSchema, type QueryState } from "@pythia-software/query-table-core";
import { emptyTextSearch, readTextSearch, type TextSearchDraft } from "./text-search-model";
import { readRollingWindow, resolveRollingQuery, rollingQueryMatches, type RollingWindow } from "./query-links";
export type SearchComposerState = { text: TextSearchDraft; kind: "text" | "file" | "url"; file: string; fileCase: boolean; query: QueryState; window?: RollingWindow; error?: string };
export function freshSearch(schema: FieldSchema): SearchComposerState {
  return { text: emptyTextSearch(), kind: "text", file: "", fileCase: false, query: { ...EMPTY_QUERY, where: [], orderBy: schema.defaultSort ?? [], select: schema.defaultSelect ?? [], limit: schema.defaultLimit ?? 50 } };
}
export function restoreSearch(params: URLSearchParams, schema: FieldSchema): SearchComposerState {
  const state = freshSearch(schema), text = readTextSearch(params);
  const token = params.get("q_library");
  try { if (token) {
    if (params.getAll("q_library").length !== 1) throw new Error("Duplicate query state");
    const snapshot = token.startsWith("@") ? localQuerySnapshotCache().get(schema.name, token.slice(1)) : null;
    if (token.startsWith("@") && !snapshot) throw new Error("This query link needs resolution");
    state.window = readRollingWindow(params, "library");
    state.query = resolveRollingQuery(decodeQueryStrict(snapshot?.token ?? token), state.window);
  } }
  catch { state.error = "The saved workspace filters could not be read. Reset the search to start again."; }
  state.kind = params.get("kind") === "file" ? "file" : params.get("kind") === "url" ? "url" : "text";
  state.text = text.draft; state.file = params.get("search") ?? ""; state.fileCase = params.get("case") === "1";
  state.error ||= text.error;
  return state;
}
export function searchURL(state: SearchComposerState): string {
  // Start a fresh results page, retaining the complete table query but never a
  // cursor or unrelated page state. A cursor belongs to the old search scope.
  const params = new URLSearchParams({ q_library: encodeQuery({ ...state.query, offset: 0 }) });
  if (state.window && rollingQueryMatches(state.query, state.window)) {
    params.set("window_dataset", "library"); params.set("window_field", state.window.field); params.set("window_days", String(state.window.days));
    if (state.window.mode === "relative") params.set("window_mode", "relative");
  }
  if (state.kind === "text") params.set("text_builder", JSON.stringify(state.text));
  else { params.set("kind", state.kind); params.set("search", state.file.trim()); if (state.fileCase) params.set("case", "1"); }
  return `/library?${params}`;
}
