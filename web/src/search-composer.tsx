import React, { useEffect, useRef, useSyncExternalStore } from "react";
import type { FieldSchema } from "@pythia-software/query-table-core";
import { TextSearchEditor } from "./text-search-editor";
import { SearchOrderControl } from "./search-order";
import { filterIssue, SearchFilters, SearchFilterFields } from "./search-filters";
import { freshSearch, searchURL, type SearchComposerState } from "./search-composer-state";

// Home and the menu dialog share an unfinished draft. Editing is entirely
// local: validation runs on submit and only Library fetches workspace results.
let saved: SearchComposerState | undefined;
const listeners = new Set<() => void>();
export function replaceSearchDraft(next: SearchComposerState) { saved = next; listeners.forEach(fn => fn()); }
export function useSearchDraft(schema: FieldSchema) {
  if (!saved) saved = freshSearch(schema);
  return useSyncExternalStore(fn => { listeners.add(fn); return () => { listeners.delete(fn); }; }, () => saved!);
}
export function SearchComposer({ schema, state, onChange, onSubmit = next => window.pharosNavigate?.(searchURL(next)) }: { schema: FieldSchema; state: SearchComposerState; onChange: (next: SearchComposerState) => void; onSubmit?: (next: SearchComposerState) => void }) {
  const latest = useRef(state); latest.current = state;
  const patch = (change: Partial<SearchComposerState>) => { latest.current = { ...latest.current, ...change }; onChange(latest.current); };
  const query = state.query;
  const where = (terms: typeof query.where) => patch({ query: { ...query, where: terms, offset: 0 } });
  const reset = () => { const next = freshSearch(schema); onChange(next); };
  return <div className="shared-search-composer">
    {state.error ? <p role="alert" className="text-editor-error">{state.error}</p> : null}
    <TextSearchEditor compact filterError={filterIssue(schema.fields, query.where)} value={state.text} active kind={state.kind} onKind={kind => patch({ kind })} onDraft={text => patch({ text })}
      onApply={text => { if (!state.error) onSubmit({ ...state, text }); }} onClear={reset}
      file={{ text: state.file, caseSensitive: state.fileCase, active: true, dirty: false, onText: file => patch({ file }), onCase: fileCase => patch({ fileCase }), onApply: () => { if (!state.error) onSubmit(state); }, onClear: reset }}
      workspaceScope={{ filters: [], inline: <SearchFilters subject="Workspace" fields={schema.fields} where={query.where} onChange={where} />, add: <SearchFilterFields fields={schema.fields} label="Workspace filter" onAdd={clause => where([...query.where, clause])} /> }}
      orderControl={<SearchOrderControl catalogue={schema.fields} order={query.orderBy} onChange={orderBy => patch({ query: { ...query, orderBy, offset: 0 } })} />} />
  </div>;
}
export function HomeSearch({ schema }: { schema: FieldSchema }) {
  const state = useSearchDraft(schema);
  const host = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const focus = () => { if (["/", "/home"].includes(location.pathname) && !document.querySelector("dialog[open]")) requestAnimationFrame(() => host.current?.querySelector<HTMLInputElement>('input:not([type="checkbox"])')?.focus({ preventScroll: true })); };
    focus(); window.addEventListener("pharos:view", focus);
    return () => window.removeEventListener("pharos:view", focus);
  }, []);
  return <div ref={host}><SearchComposer key={state.text.root.id} schema={schema} state={state} onChange={replaceSearchDraft} /></div>;
}
