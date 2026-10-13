import { decodeQuery, EMPTY_QUERY, encodeQuery, type FieldSchema, type QueryState } from "@pythia-software/query-table-core";
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
    // decodeQuery deliberately falls back to an empty query for bad tokens.
    // A search composer must report a corrupt filter envelope before that can
    // silently broaden the search being handed to Library.
    const envelope = JSON.parse(new TextDecoder().decode(Uint8Array.from(atob(token.replaceAll("-", "+").replaceAll("_", "/")), c => c.charCodeAt(0))));
    if (!envelope || typeof envelope !== "object" || Array.isArray(envelope)) throw new Error("Invalid query");
    state.window = readRollingWindow(params, "library");
    state.query = resolveRollingQuery(decodeQuery(token), state.window);
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
  }
  if (state.kind === "text") params.set("text_builder", JSON.stringify(state.text));
  else { params.set("kind", state.kind); params.set("search", state.file.trim()); if (state.fileCase) params.set("case", "1"); }
  return `/library?${params}`;
}
