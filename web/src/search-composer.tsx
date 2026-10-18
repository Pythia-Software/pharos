import React, { useEffect, useId, useRef, useState, useSyncExternalStore } from "react";
import type { FieldSchema } from "@pythia-software/query-table-core";
import { SearchSentence, TextSearchEditor } from "./text-search-editor";
import { SearchOrderControl } from "./search-order";
import { describeWhere, filterIssue, SearchFilters, SearchFilterFields } from "./search-filters";
import { freshSearch, searchURL, type SearchComposerState } from "./search-composer-state";
import { searchSentence } from "./text-search-model";
import { Icon } from "./icons";

// Home and the menu dialog share an unfinished draft. Editing is entirely
// local: validation runs on submit and only Library fetches workspace results.
let saved: SearchComposerState | undefined;
const listeners = new Set<() => void>();
export function replaceSearchDraft(next: SearchComposerState) { saved = next; listeners.forEach(fn => fn()); }
export function useSearchDraft(schema: FieldSchema) {
  if (!saved) saved = freshSearch(schema);
  return useSyncExternalStore(fn => { listeners.add(fn); return () => { listeners.delete(fn); }; }, () => saved!);
}
export function SearchComposer({ schema, state, onChange, onSubmit = next => window.pharosNavigate?.(searchURL(next)), sentence = true }: { schema: FieldSchema; state: SearchComposerState; onChange: (next: SearchComposerState) => void; onSubmit?: (next: SearchComposerState) => void; sentence?: boolean }) {
  const latest = useRef(state); latest.current = state;
  const patch = (change: Partial<SearchComposerState>) => { latest.current = { ...latest.current, ...change }; onChange(latest.current); };
  const query = state.query;
  const where = (terms: typeof query.where) => patch({ query: { ...query, where: terms, offset: 0 } });
  const reset = () => { const next = freshSearch(schema); onChange(next); };
  return <div className="shared-search-composer">
    {state.error ? <p role="alert" className="text-editor-error">{state.error}</p> : null}
    <TextSearchEditor compact sentence={sentence} filterError={filterIssue(schema.fields, query.where)} value={state.text} active kind={state.kind} onKind={kind => patch({ kind })} onDraft={text => patch({ text })}
      onApply={text => { if (!state.error) onSubmit({ ...state, text }); }} onClear={reset}
      file={{ text: state.file, caseSensitive: state.fileCase, active: true, dirty: false, onText: file => patch({ file }), onCase: fileCase => patch({ fileCase }), onApply: () => { if (!state.error) onSubmit(state); }, onClear: reset }}
      workspaceScope={{ filters: [], reading: describeWhere(schema.fields, query.where), inline: <SearchFilters subject="Workspace" fields={schema.fields} where={query.where} onChange={where} />, add: <SearchFilterFields fields={schema.fields} label="Workspace property" onAdd={clause => where([...query.where, clause])} /> }}
      orderControl={<SearchOrderControl catalogue={schema.fields} order={query.orderBy} onChange={orderBy => patch({ query: { ...query, orderBy, offset: 0 } })} />} />
  </div>;
}
export function HomeSearch({ schema }: { schema: FieldSchema }) {
  const state = useSearchDraft(schema);
  const [expanded, setExpanded] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const id = useId();
  useEffect(() => {
    const collapse = () => { if (["/", "/home"].includes(location.pathname)) setExpanded(false); };
    window.addEventListener("pharos:view", collapse);
    return () => window.removeEventListener("pharos:view", collapse);
  }, []);
  useEffect(() => {
    if (content.current) content.current.inert = !expanded;
    if (!expanded) return;
    const frame = requestAnimationFrame(() => content.current?.querySelector<HTMLElement>('textarea, input:not([type="checkbox"])')?.focus({ preventScroll: true }));
    return () => cancelAnimationFrame(frame);
  }, [expanded]);
  // The box reads the search back in English as it is composed.
  const reading = searchSentence(state.text, { kind: state.kind, file: state.file, fileCase: state.fileCase, workspace: describeWhere(schema.fields, state.query.where) });
  return <div className={`home-search-disclosure${expanded ? " is-expanded" : ""}`} onKeyDown={event => {
    if (event.key === "Escape" && expanded && !event.defaultPrevented && !document.querySelector(".text-condition-popup, .search-help-bubble")) {
      event.preventDefault(); setExpanded(false); trigger.current?.focus();
    }
  }}>
    <button ref={trigger} type="button" className="home-search-trigger" aria-label={expanded ? "Collapse search" : "Search your library"} aria-describedby={reading.empty ? undefined : `${id}-reading`} aria-expanded={expanded} aria-controls={id} onClick={() => setExpanded(!expanded)}>
      <Icon name="search" />
      {reading.empty && !expanded ? <span className="home-search-placeholder">Search your library…</span> : <span id={`${id}-reading`} className={reading.empty ? "home-search-placeholder" : "home-search-preview"}><SearchSentence parts={reading.parts} empty={reading.empty} /></span>}
      <Icon name="chevron-down" />
    </button>
    <div className="home-search-expansion" aria-hidden={!expanded}>
      <div ref={content} id={id} className="home-search-content">
        <SearchComposer key={state.text.root.id} schema={schema} state={state} onChange={replaceSearchDraft} sentence={false} />
      </div>
    </div>
  </div>;
}
