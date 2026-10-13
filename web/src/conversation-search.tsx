import React, { useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { TextSearchEditor } from "./text-search-editor";
import { emptyTextSearch, searchRequest, type TextSearchDraft } from "./text-search-model";
import { SearchSelect } from "./search-select";
import { Icon } from "./icons";

type Hit = { message_id: string; conversation_id: string; workspace_id: string; snippet: string; start_byte: number; end_byte: number; range_kind: string };
type Batch = { items: Hit[]; complete: boolean; next_cursor?: string; skipped_oversize: number; elapsed_ms: number; stop_reason: string; coverage: string };
type Options = { conversationID: string; workspaceID: string; title: string; reveal: (hit: Hit | null) => void; position: (id: string) => number };
type Handle = { open: (term?: string, options?: { regex?: boolean; caseSensitive?: boolean; output?: boolean }) => void; close: () => void; destroy: () => void };
declare global { interface Window { pharosMountConversationSearch?: (host: HTMLElement, options: Options) => Handle; } }
function ConversationSearch({ options, host, register }: { options: Options; host: HTMLElement; register: (handle: Omit<Handle, "destroy">) => void }) {
  const [open, setOpen] = useState(false), [draft, setDraft] = useState(emptyTextSearch), [applied, setApplied] = useState<TextSearchDraft>();
  const [result, setResult] = useState<Batch>(), [busy, setBusy] = useState(false), [error, setError] = useState("");
  const [active, setActive] = useState(-1), [direction, setDirection] = useState("asc");
  const pending = useRef<AbortController>();
  const invoker = useRef<HTMLElement | null>(null);
  const hits = [...(result?.items ?? [])].sort((a, b) => (options.position(a.message_id) - options.position(b.message_id)) * (direction === "asc" ? 1 : -1));
  const close = () => { pending.current?.abort(); setBusy(false); setOpen(false); options.reveal(null); invoker.current?.focus(); };
  const reveal = (index: number) => { if (!hits.length) return; const next = (index + hits.length) % hits.length; setActive(next); options.reveal(hits[next]); };
  const run = async (value: TextSearchDraft, cursor = "") => {
    const request = searchRequest(value).request; if (!request) return;
    const controller = new AbortController(); pending.current?.abort(); pending.current = controller; setBusy(true); setError("");
    try {
      const response = await fetch("/api/search/text", { method: "POST", headers: { "Content-Type": "application/json" }, signal: controller.signal,
        body: JSON.stringify({ ...request, workspace_id: options.workspaceID, conversation_id: options.conversationID, cursor, limit: 50, timeout_ms: 1500 }) });
      const batch = await response.json(); if (!response.ok) throw new Error(batch.error ?? "Search failed");
      if (!controller.signal.aborted) { setResult(batch); setApplied(value); setActive(-1); options.reveal(null); }
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
  return <div className="conversation-search-composer">
    <div className="search-context"><strong>Find in this conversation</strong><span>{options.title}</span><button type="button" aria-label="Close conversation search" onClick={close}><Icon name="close" /></button></div>
    <TextSearchEditor value={scopedDraft} active={!!result} textOnly fixedContext kind="text" onKind={() => {}} onDraft={next => { pending.current?.abort(); setBusy(false); setDraft(next); }}
      onApply={next => void run(next)} onClear={() => { pending.current?.abort(); setBusy(false); setDraft(emptyTextSearch()); setResult(undefined); setApplied(undefined); options.reveal(null); }}
      file={{ text: "", active: false, dirty: false, caseSensitive: false, onText() {}, onCase() {}, onApply() {}, onClear() {} }} workspaceScope={{ filters: [] }}
      orderControl={<label className="text-search-order">Message order <SearchSelect aria-label="Message order" value={direction} onChange={e => { setDirection(e.target.value); setActive(-1); options.reveal(null); }}><option value="asc">First to last in this batch</option><option value="desc">Last to first in this batch</option></SearchSelect></label>} />
    <div className="conversation-search-results" tabIndex={0} aria-label="Conversation search results" aria-busy={busy}>
      <div className="conversation-search-navigation"><button type="button" disabled={!hits.length} aria-label="Previous matching message" onClick={() => reveal(active - 1)}><Icon name="arrow-up" /></button><button type="button" disabled={!hits.length} aria-label="Next matching message" onClick={() => reveal(active + 1)}><Icon name="arrow-down" /></button><span role="status">{busy ? "Searching…" : result ? `${hits.length} matching message${hits.length === 1 ? "" : "s"}${active >= 0 ? ` · ${active + 1} selected` : ""}${result.complete ? " · Complete" : " · Partial batch"}` : "Search to find matching messages, including collapsed and hidden content."}</span></div>
      {error ? <p role="alert" className="text-editor-error">{error}</p> : null}
      {result ? <><div className="conversation-search-hits">{hits.map((hit, index) => <button type="button" key={`${hit.message_id}:${hit.start_byte}`} aria-current={index === active ? "true" : undefined} onClick={() => reveal(index)}>{hit.snippet}</button>)}</div>
        <p className="text-editor-scope-note">{result.elapsed_ms} ms · {result.complete ? "All eligible messages checked." : `Stopped: ${result.stop_reason.replaceAll("_", " ")}. More messages may match.`}{result.skipped_oversize ? ` ${result.skipped_oversize} oversized messages were skipped.` : ""} Navigation reveals matching messages in the transcript.</p>
        {result.next_cursor ? <button type="button" disabled={busy || !applied} onClick={() => applied && void run(applied, result.next_cursor)}>Next search batch</button> : null}
        <details><summary>Search coverage</summary>{result.coverage}</details>
      </> : null}
    </div>
  </div>;
}
window.pharosMountConversationSearch = (host, options) => {
  const root = createRoot(host); let current: Omit<Handle, "destroy"> | undefined; let queued: Parameters<Handle["open"]> | undefined;
  root.render(<ConversationSearch host={host} options={options} register={handle => { current = handle; if (queued) { handle.open(...queued); queued = undefined; } }} />);
  return { open: (...args) => { if (current) current.open(...args); else queued = args; }, close: () => current?.close(), destroy: () => { options.reveal(null); root.unmount(); } };
};
