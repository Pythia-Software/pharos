import { loadSearchPage } from "./search-pagination";
import { ViewDisclosure } from "./view-disclosure";
import { readViewState, writeViewState, useViewState } from "./view-state";
import React, { useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { TextSearchEditor } from "./text-search-editor";
import { emptyTextSearch, refreshSearchToday, searchRequest, withheldBatch, type TextSearchDraft } from "./text-search-model";
import { SearchSelect } from "./search-select";
import { Icon } from "./icons";

type Hit = { message_id: string; conversation_id: string; workspace_id: string; snippet: string; start_byte: number; end_byte: number; range_kind: string };
// cursor is the one this batch was requested with, so a withheld batch can be retried.
type Batch = { items: Hit[]; complete: boolean; next_cursor?: string; skipped_oversize: number; elapsed_ms: number; stop_reason: string; stop_message?: string; coverage: string; cursor: string };
type Options = { conversationID: string; workspaceID: string; title: string; reveal: (hit: Hit | null) => void; position: (id: string) => number; prepare?: (ids: string[]) => void; contains?: (id: string, text: string) => boolean };
type Handle = { open: (term?: string, options?: { regex?: boolean; caseSensitive?: boolean; output?: boolean }) => void; close: () => void; destroy: () => void };
declare global { interface Window { pharosMountConversationSearch?: (host: HTMLElement, options: Options) => Handle; } }
function ConversationSearch({ options, host, register }: { options: Options; host: HTMLElement; register: (handle: Omit<Handle, "destroy">) => void }) {
  const [open, setOpen] = useState(false), [draft, setDraft] = useState(emptyTextSearch), [applied, setApplied] = useState<TextSearchDraft>();
  const [result, setResult] = useState<Batch>(), [busy, setBusy] = useState(false), [error, setError] = useState("");
  const [active, setActive] = useState(-1), [direction, setDirection] = useViewState("reader_order", "asc", location.pathname.startsWith("/work/"));
  const [pageIndex, setPageIndex] = useViewState("reader_page", "0", location.pathname.startsWith("/work/"));
  const pending = useRef<AbortController>();
  const attentionSearch=useRef(false);
  const invoker = useRef<HTMLElement | null>(null);
  const hits = [...(result?.items ?? [])].sort((a, b) => (options.position(a.message_id) - options.position(b.message_id)) * (direction === "asc" ? 1 : -1));
  const reset = () => {
    pending.current?.abort(); pending.current=undefined; attentionSearch.current=false;
    setBusy(false); setOpen(false); setDraft(emptyTextSearch()); setApplied(undefined);
    setResult(undefined); setError(""); setActive(-1); options.reveal(null);
  };
  const ownPage = () => location.pathname === `/work/${encodeURIComponent(options.workspaceID)}` || location.pathname.startsWith(`/work/${encodeURIComponent(options.workspaceID)}/`);
  const close = () => { reset(); if(!ownPage())return;writeViewState("reader_search", {}); const url=new URL(location.href);if(url.searchParams.get("reveal")?.startsWith("text:"))url.searchParams.delete("reveal");const highlights=url.searchParams.getAll("highlight").filter(target=>!target.startsWith("text:"));url.searchParams.delete("highlight");for(const target of highlights)url.searchParams.append("highlight",target);history.replaceState(history.state,"",url);window.dispatchEvent(new Event("pharos:uri-changed")); invoker.current?.focus(); };
  const reveal = (index: number) => { if (!hits.length) return; const next = (index + hits.length) % hits.length; setActive(next); options.reveal(hits[next]); const url=new URL(location.href);url.searchParams.set("reveal",`message:${hits[next].message_id}`);history.replaceState(history.state,"",url);window.dispatchEvent(new Event("pharos:uri-changed")); };
  const run = async (value: TextSearchDraft, cursor = "", landing = false, landingText = "", requestedPage = 0) => {
    attentionSearch.current=Boolean(landingText);
    if (!cursor) value=refreshSearchToday(value);
    value={...value,scope:{...value.scope,conversation_id:options.conversationID,workspace_id:options.workspaceID}};
    const request = searchRequest(value).request; if (!request) return;
    if (!cursor) {writeViewState("reader_search", value);setPageIndex(String(requestedPage));}
    const controller = new AbortController(); pending.current?.abort(); pending.current = controller; setBusy(true); setError("");
    try {
      const { batch, cursor: loadedCursor, page } = await loadSearchPage(async batchCursor => {
        const response = await fetch("/api/search/text", { method: "POST", headers: { "Content-Type": "application/json" }, signal: controller.signal,
          body: JSON.stringify({ ...request, workspace_id: options.workspaceID, conversation_id: options.conversationID, cursor: batchCursor, limit: 50, timeout_ms: 700 }) });
        const batch = await response.json(); if (!response.ok) throw new Error(batch.error ?? "Search failed");
        return batch as Batch;
      }, requestedPage, cursor);
      if (!controller.signal.aborted) { setPageIndex(String(page)); setResult({ ...batch, cursor:loadedCursor }); setApplied(value); setActive(-1); options.reveal(null); if (landingText) options.prepare?.((batch.items ?? []).map((hit: Hit) => hit.message_id));
        if (landing && batch.items?.length) {
          const ordered = [...batch.items].sort((a: Hit,b: Hit) => options.position(a.message_id)-options.position(b.message_id));
          const first = ordered.find((hit: Hit) => !options.contains || options.contains(hit.message_id,landingText));
          if(first){setActive(ordered.indexOf(first));options.reveal(first);}
        } }
    } catch (cause) { if (!controller.signal.aborted) setError(String(cause instanceof Error ? cause.message : cause)); }
    finally { if (!controller.signal.aborted) setBusy(false); }
  };
  useEffect(() => {
    register({ open: (term, legacy = {}) => {
      invoker.current = document.activeElement as HTMLElement;
      setOpen(true);
      if (term) { const next = emptyTextSearch(); next.match = legacy.regex ? "regex" : "literal"; next.text = term; next.caseSensitive = !!legacy.caseSensitive; next.scope.surface = legacy.output ? "all" : "prose"; next.scope.conversation_id = options.conversationID; next.scope.workspace_id = options.workspaceID; setDraft(next); void run(next); }
      requestAnimationFrame(() => { host.scrollIntoView({ block: "nearest" }); host.querySelector<HTMLInputElement>('input:not([type="checkbox"])')?.focus(); });
    }, close });
    return () => pending.current?.abort();
  }, []);
  useEffect(() => {
    const restore = () => {
      if(!ownPage()){reset();return;}
      const saved = readViewState<TextSearchDraft | null>("reader_search", null);
      if (saved?.scope) { setDraft(saved); setOpen(true); void run(saved,"",false,"",Number(readViewState("reader_page","0"))); }
      else reset();
    };
    const find = (event: Event) => {
      const request = (event as CustomEvent<{ text: string; texts: string[]; landing: boolean; handled: boolean }>).detail;
      if (!host.isConnected || !host.closest("#detail.active") || host.closest("[hidden],[inert]")) return;
      request.handled = true;
      const next = emptyTextSearch(); next.match=request.texts.length>1?"regex":"literal"; next.text=request.texts.length>1?request.texts.map(text=>text.replace(/[.*+?^${}()|[\]\\]/g,"\\$&")).join("|"):request.text; next.scope.surface="all";
      const saved=readViewState<TextSearchDraft|null>("reader_search",null);
      const page=saved?.text===next.text&&saved.match===next.match?Number(readViewState("reader_page","0")):0;
      setDraft(next); setOpen(true); void run(next,"",request.landing,request.text,page);
    };
    const clearAttention=()=>{if(attentionSearch.current){pending.current?.abort();setBusy(false);}setActive(-1);options.reveal(null);};
    restore();
    window.addEventListener("pharos:attention-cleared",clearAttention);
    window.addEventListener("pharos:find-text",find);
    window.addEventListener("pharos:route",restore); window.addEventListener("pharos:view",restore);
    window.pharosView?.refresh();
    return () => {window.removeEventListener("pharos:attention-cleared",clearAttention);window.removeEventListener("pharos:find-text",find);window.removeEventListener("pharos:route",restore);window.removeEventListener("pharos:view",restore);};
  }, []);
  useEffect(() => {
    const key = (event: KeyboardEvent) => {
      if (!open || !host.closest("#detail.active") || document.querySelector("dialog[open]")) return;
      if (event.key === "Escape" && !event.defaultPrevented && !(event.target as HTMLElement)?.closest(".text-condition-popup, .search-help[aria-describedby]")) { event.preventDefault(); close(); }
      if (event.key === "Enter" && (event.target === host || (event.target as HTMLElement)?.closest(".conversation-search-results"))) { event.preventDefault(); reveal(active + (event.shiftKey ? -1 : 1)); }
    };
    document.addEventListener("keydown", key); return () => document.removeEventListener("keydown", key);
  }, [open, hits, active]);
  // Scope is visible and fixed for this surface; it is never inherited from
  // transcript display filters, which otherwise hide valid message matches.
  const scopedDraft = useMemo(() => ({ ...draft, scope: { ...draft.scope, conversation_id: options.conversationID, workspace_id: options.workspaceID } }), [draft, options.conversationID, options.workspaceID]);
  if (!open) return null;
  return <div className="conversation-search-composer" data-view-field="reader_search">
    <div className="search-context"><strong>Find in this conversation</strong><span>{options.title}</span><button type="button" aria-label="Close conversation search" onClick={close}><Icon name="close" /></button></div>
    <TextSearchEditor value={scopedDraft} active={!!result} textOnly fixedContext kind="text" onKind={() => {}} onDraft={next => { pending.current?.abort(); setBusy(false); setDraft(next); }}
      onApply={next => void run(next)} onClear={() => { pending.current?.abort(); setBusy(false); setDraft(emptyTextSearch()); setResult(undefined); setApplied(undefined); writeViewState("reader_search", {}); options.reveal(null); }}
      file={{ text: "", active: false, dirty: false, caseSensitive: false, onText() {}, onCase() {}, onApply() {}, onClear() {} }} workspaceScope={{ filters: [] }}
      orderControl={<label className="text-search-order">Message order <SearchSelect data-view-field="reader_order" aria-label="Message order" value={direction} onChange={e => { setDirection(e.target.value); setActive(-1); options.reveal(null); }}><option value="asc">First to last in this batch</option><option value="desc">Last to first in this batch</option></SearchSelect></label>} />
    <div className="conversation-search-results" tabIndex={0} aria-label="Conversation search results" aria-busy={busy}>
      <div className="conversation-search-navigation" data-view-field="reader_page"><button type="button" disabled={!hits.length} aria-label="Previous matching message" onClick={() => reveal(active - 1)}><Icon name="arrow-up" /></button><button type="button" disabled={!hits.length} aria-label="Next matching message" onClick={() => reveal(active + 1)}><Icon name="arrow-down" /></button><span role="status">{busy ? "Searching…" : result ? `Page ${Number(pageIndex)+1} · ${hits.length} matching message${hits.length === 1 ? "" : "s"}${active >= 0 ? ` · ${active + 1} selected` : ""}${result.complete ? " · Complete" : " · Partial batch"}` : "Search to find matching messages, including collapsed and hidden content."}</span></div>
      {error ? <p role="alert" className="text-editor-error">{error}</p> : null}
      {result ? <><div className="conversation-search-hits">{hits.map((hit, index) => <button type="button" key={`${hit.message_id}:${hit.start_byte}`} aria-current={index === active ? "true" : undefined} onClick={() => reveal(index)}>{hit.snippet}</button>)}</div>
        <p className="text-editor-scope-note">{result.elapsed_ms} ms · {result.complete ? "All eligible messages checked." : `Stopped: ${result.stop_reason.replaceAll("_", " ")}. More messages may match.`}{result.skipped_oversize ? ` ${result.skipped_oversize} oversized messages were skipped.` : ""} Navigation reveals matching messages in the transcript.</p>
        {result.stop_message ? <p role="status">{result.stop_message}</p> : null}
        {result.next_cursor && Number(pageIndex)<250 ? <button type="button" disabled={busy || !applied} onClick={() => applied && void run(applied, result.next_cursor,false,"",Number(pageIndex)+1)}>Next search batch</button> : null}
        {withheldBatch(result) ? <button type="button" disabled={busy || !applied} onClick={() => applied && void run(applied, result.cursor,false,"",Number(pageIndex))}>Retry this batch</button> : null}
        <ViewDisclosure viewKey="reader:search-coverage"><summary>Search coverage</summary>{result.coverage}</ViewDisclosure>
      </> : null}
    </div>
  </div>;
}
window.pharosMountConversationSearch = (host, options) => {
  const root = createRoot(host); let current: Omit<Handle, "destroy"> | undefined; let queued: Parameters<Handle["open"]> | undefined;
  root.render(<ConversationSearch host={host} options={options} register={handle => { current = handle; if (queued) { handle.open(...queued); queued = undefined; } }} />);
  return { open: (...args) => { if (current) current.open(...args); else queued = args; }, close: () => current?.close(), destroy: () => { options.reveal(null); root.unmount(); } };
};
