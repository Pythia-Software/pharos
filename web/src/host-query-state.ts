import { MAX_HOST_QUERY_VALUE_LENGTH, parseHostQueryState, type FieldDef, type HostQueryState } from "@pythia-software/query-table-core";
import type { HostQueryOptions } from "@pythia-software/query-table-react";
import { readRecordKeywordSearch, recordKeywordParameter, recordKeywordRequest, type RecordKeywordSearch } from "./record-keyword-model";
import { emptyScope, refreshSearchToday, readTextSearch, textFindParameters, type TextCondition, type TextSearchDraft } from "./text-search-model";
import type { SearchResultsView } from "./search-results-view-model";

// Clauses Pharos runs itself, registered with Query Table as host query state
// (useQueryTable's hostQuery), so Save, Saved, Share, Reset and Undo/Redo
// carry them with the table query. Each table owns URL parameters that mirror
// its state. normalize validates and canonicalizes, and throws to reject: a
// rejected state fails the whole restore instead of running a broader query.
// Canonical drafts omit the editor's rule and group IDs; reading a draft
// rebuilds them, so read once per state (useMemo) to keep them stable.
// Message cursors, result pages, dialogs and editor drafts are never part of
// host state.

export const HOST_STATE_VERSION = 1;
export const emptyHostState = (): HostQueryState => ({ version: HOST_STATE_VERSION, params: {} });
// Compares canonical forms: Query Table sorts parameter names.
const canonicalState = (state: HostQueryState | undefined) => { try { return JSON.stringify(state ? parseHostQueryState(state) : null); } catch { return JSON.stringify(state); } };
export const sameHostState = (a: HostQueryState | undefined, b: HostQueryState | undefined) => canonicalState(a) === canonicalState(b);

function editorFree(condition: TextCondition): object {
  return condition.kind === "group"
    ? { kind: "group", join: condition.join, exclude: condition.exclude, children: condition.children.map(editorFree) }
    : { kind: condition.kind, text: condition.text, other: condition.other, distance: condition.distance, exclude: condition.exclude };
}
/** A draft's definition without editor IDs. */
export function canonicalDraft(draft: TextSearchDraft): string {
  return JSON.stringify({ ...draft, root: editorFree(draft.root) });
}

function bounded(value: string, what: string): string {
  if (value.length > MAX_HOST_QUERY_VALUE_LENGTH) throw new Error(`This ${what} is too large to keep with the query. Remove some conditions.`);
  return value;
}
// Exactly one value per parameter, and only parameters this table owns.
function readParams(state: HostQueryState, owned: readonly string[]): Map<string, string> {
  if (state.version !== HOST_STATE_VERSION) throw new Error(`This query's search state has an unsupported version (${state.version}).`);
  const params = new Map<string, string>();
  for (const [name, values] of Object.entries(state.params)) {
    if (!owned.includes(name)) throw new Error(`This query's search state has an unknown setting "${name}".`);
    if (values.length !== 1) throw new Error(`This query's search state repeats "${name}".`);
    params.set(name, values[0]);
  }
  return params;
}
// Parameter names sorted, as Query Table keeps them.
const stateOf = (params: Record<string, string>): HostQueryState => ({ version: HOST_STATE_VERSION, params: Object.fromEntries(Object.entries(params).sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0).map(([name, value]) => [name, [value]])) });

// ---- Analyze: one record keyword clause per dataset (record_<dataset>). ----

export function recordKeywordHostQuery(dataset: string, fields: FieldDef[]): HostQueryOptions {
  const parameter = recordKeywordParameter(dataset);
  return {
    ownedParameters: [parameter],
    defaults: emptyHostState(),
    normalize(state) {
      const token = readParams(state, [parameter]).get(parameter);
      if (token === undefined) return emptyHostState();
      const { value, error } = readRecordKeywordSearch(token, fields);
      if (!value) throw new Error(error);
      recordKeywordRequest(value); // Rejects a clause the service could not run.
      return stateOf({ [parameter]: bounded(canonicalRecordKeyword(value), "keyword filter") });
    },
  };
}
export function canonicalRecordKeyword(search: RecordKeywordSearch): string {
  return `{"field":${JSON.stringify(search.field)},"draft":${canonicalDraft(search.draft)}}`;
}
/** The keyword clause of a normalized state, with fresh editor IDs. */
export function readRecordKeyword(state: HostQueryState | undefined, dataset: string, fields: FieldDef[]): RecordKeywordSearch | null {
  const token = state?.params[recordKeywordParameter(dataset)]?.[0];
  return token ? readRecordKeywordSearch(token, fields).value : null;
}
export function withRecordKeyword(dataset: string, search: RecordKeywordSearch | null): HostQueryState {
  return search ? stateOf({ [recordKeywordParameter(dataset)]: bounded(canonicalRecordKeyword(search), "keyword filter") }) : emptyHostState();
}
/** The compiled request parameters the service runs (record_search). */
export function recordKeywordFind(state: HostQueryState | undefined, dataset: string, fields: FieldDef[]): string {
  const search = readRecordKeyword(state, dataset, fields);
  return search ? new URLSearchParams({ record_search: recordKeywordRequest(search) }).toString() : "";
}

// ---- Search (Library): a message, file or URL search and the result view. ----

export type LibrarySearchKind = "text" | "file" | "url";
export type LibraryHost = { kind: LibrarySearchKind; text: TextSearchDraft | null; file: string; fileCase: boolean; view: SearchResultsView };
// The older keyword-search parameters are owned too, so their links convert
// to text_builder and the address bar drops them.
const legacyTextParameters = ["fuzzy", "separators", "text_mode", "text_plain", ...Object.keys(emptyScope()).map(key => `text_${key}`)];
export const libraryHostParameters = ["text_builder", "kind", "search", "case", "view", ...legacyTextParameters];

export function libraryHostState(host: LibraryHost): HostQueryState {
  const params: Record<string, string> = {};
  if (host.kind === "text") { if (host.text) params.text_builder = bounded(canonicalDraft(host.text), "search"); }
  else if (host.file.trim()) {
    params.kind = host.kind; params.search = host.file.trim();
    if (host.fileCase) params.case = "1";
  }
  if (host.view === "conversation") params.view = "conversation";
  return stateOf(params);
}
export function readLibraryHost(state: HostQueryState | undefined): LibraryHost {
  const params = new URLSearchParams(Object.entries(state?.params ?? {}).flatMap(([name, values]) => values.map(value => [name, value])));
  const kind = params.get("kind") === "file" || params.get("kind") === "url" ? params.get("kind") as LibrarySearchKind : "text";
  const view: SearchResultsView = params.get("view") === "conversation" ? "conversation" : "table";
  if (kind !== "text") return { kind, text: null, file: params.get("search") ?? "", fileCase: params.get("case") === "1", view };
  const text = params.has("text_builder") || params.has("search") ? readTextSearch(params) : null;
  return { kind, text: text?.active && !text.error ? text.draft : null, file: "", fileCase: false, view };
}
export const libraryHostQuery: HostQueryOptions = {
  ownedParameters: libraryHostParameters,
  defaults: emptyHostState(),
  normalize(state) {
    const params = readParams(state, libraryHostParameters);
    const kind = params.get("kind") ?? "text", view = params.get("view") ?? "table";
    if (!["text", "file", "url"].includes(kind)) throw new Error("This search has an unknown kind.");
    if (!["table", "conversation"].includes(view)) throw new Error("This search has an unknown result view.");
    if (params.has("case") && !["0", "1"].includes(params.get("case")!)) throw new Error("This search's case setting could not be read.");
    if (kind === "text" && (params.has("text_builder") || params.has("search"))) {
      const text = readTextSearch(new URLSearchParams([...params]));
      if (text.error) throw new Error(text.error);
    }
    return libraryHostState(readLibraryHost(state));
  },
};
/** The service's find parameters for a normalized Search state. */
export function libraryFind(state: HostQueryState | undefined): string {
  const host = readLibraryHost(state);
  if (host.kind === "text") return host.text ? textFindParameters(host.text) : "";
  if (!host.file) return "";
  const params = new URLSearchParams({ find: host.file, find_kind: host.kind });
  if (host.fileCase) params.set("find_case", "1");
  return params.toString();
}
export const withoutLibrarySearch = (state: HostQueryState | undefined): HostQueryState =>
  libraryHostState({ ...readLibraryHost(state), kind: "text", text: null, file: "", fileCase: false });

// ---- Older Human messages table: its message search (message_query). ----

export const messageHostQuery: HostQueryOptions = {
  ownedParameters: ["message_query"],
  defaults: emptyHostState(),
  normalize(state) {
    const token = readParams(state, ["message_query"]).get("message_query");
    if (token === undefined) return emptyHostState();
    const text = readTextSearch(new URLSearchParams({ text_builder: token }));
    if (text.error) throw new Error(text.error);
    return withMessageSearch(text.active ? text.draft : null);
  },
};
export function readMessageSearchHost(state: HostQueryState | undefined): TextSearchDraft | null {
  const token = state?.params.message_query?.[0];
  const text = token ? readTextSearch(new URLSearchParams({ text_builder: token })) : null;
  return text?.active && !text.error ? text.draft : null;
}
export const withMessageSearch = (draft: TextSearchDraft | null): HostQueryState =>
  draft ? stateOf({ message_query: bounded(canonicalDraft(draft), "search") }) : emptyHostState();
export const messageFind = (state: HostQueryState | undefined): string => {
  const draft = readMessageSearchHost(state);
  return draft ? textFindParameters(draft) : "";
};

// Renew a calendar search at execution boundaries, never while paging a batch.
export function refreshTodayHost(state: HostQueryState | undefined, clock = new Date(), onlyNewDay = false): HostQueryState | undefined {
  if (state?.params.text_builder) {
    const host = readLibraryHost(state);
    const text = host.text && refreshSearchToday(host.text, clock, onlyNewDay);
    if (text && text !== host.text) return libraryHostState({ ...host, text });
  } else if (state?.params.message_query) {
    const draft = readMessageSearchHost(state);
    const next = draft && refreshSearchToday(draft, clock, onlyNewDay);
    if (next && next !== draft) return withMessageSearch(next);
  }
  return state;
}
