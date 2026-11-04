import React, { useEffect, useId, useRef, useState } from "react";
import { ITEM_DETAILS_COLUMN, readFieldValue, type FieldDef, type QueryState } from "@pythia-software/query-table-core";
import { resolveRenderer, type RenderRegistry } from "@pythia-software/query-table-ui";
import { Icon } from "./icons";
import { useViewEntry, useViewState } from "./view-state";
import { ViewDisclosure } from "./view-disclosure";
import { formatConcurrencyDuration } from "./concurrency-data";
import { findMatches, findTest } from "./page-find-model";
import { scrollRange } from "./page-find";

type Row = Record<string, any>;
export type TranscriptMessage = { id?: string; native_id?: string; role: string; text: unknown; raw_text?: unknown; authorship?: unknown; text_clipped?: boolean };
export type OutlineEntry = { id: string; kind: "turn" | "agent"; label: string; prompt: string; human: TranscriptMessage | null; response: TranscriptMessage | null; messages: TranscriptMessage[]; tools: number; children: OutlineEntry[] };
/** A message to open in Browse, with Search's terms to mark in it. */
export type ResultTarget = { messageID: string; conversationID?: string; pattern: RegExp | null };
declare global {
  interface Window {
    pharosMessageCard?: (message: TranscriptMessage, workspace?: string) => HTMLElement;
    pharosLoadOriginal?: (message: TranscriptMessage) => Promise<TranscriptMessage>;
    pharosTranscriptHighlights?: (panel: HTMLElement, conversation: Row) => (HTMLElement & { dispose: () => void }) | null;
    pharosMessageSummary?: (message: TranscriptMessage) => string;
    pharosConversationOutline?: (conversation: Row) => OutlineEntry[];
  }
}

// Use the selected columns in order, including computed fields and display
// overrides. The work title remains the card's navigation heading.
export function ConversationFacts({ row, fields, query, renderers }: { row: Row; fields: FieldDef<Row>[]; query: QueryState; renderers: RenderRegistry<Row> }) {
  return <dl className="conversation-result-facts">
    {fields.filter(field => field.name !== "title" && field.name !== ITEM_DETAILS_COLUMN).map(field => {
      const column = query.select.find(column => column.field === field.name);
      const value = readFieldValue(field, row);
      if (value == null || typeof value === "string" && !value.trim() || Array.isArray(value) && !value.length) return null;
      const rendered = resolveRenderer(field, renderers, column?.display)({ value, row, field, query, presentation: "property" });
      if (rendered == null || rendered === "" || rendered === false) return null;
      return <div key={field.name} className="conversation-result-fact">
        <dt className="conversation-fact-label">{field.label}</dt>
        <dd className="conversation-fact-value">{rendered}</dd>
      </div>;
    })}
  </dl>;
}

export function conversationActivitySummary(row: Row): string {
  const parts: string[] = [];
  for (const [field, label] of [["turn_count", "turn"], ["subagent_count", "sub-agent"]]) {
    const count = Number(row[field]);
    if (row[field] != null && Number.isFinite(count) && count >= 0) parts.push(`${Math.floor(count).toLocaleString()} ${label}${count === 1 ? "" : "s"}`);
  }
  const elapsed = Number(row.preview_elapsed_ms);
  if (row.preview_elapsed_ms != null && Number.isFinite(elapsed) && elapsed >= 0) parts.push(`${formatConcurrencyDuration(elapsed)} elapsed`);
  return parts.join(" · ") || "Conversation details";
}

// A preview message that opens Browse at its turn when clicked anywhere
// except on a link it contains.
export function openableProps(onOpen?: () => void): React.HTMLAttributes<HTMLDivElement> {
  return onOpen ? { role: "button", tabIndex: 0, title: "Browse to this message", onClick: event => { if (!(event.target as Element).closest("a")) onOpen(); },
    onKeyDown: event => { if (event.target === event.currentTarget && (event.key === "Enter" || event.key === " ")) { event.preventDefault(); onOpen(); } } } : {};
}

function textRanges(root: Node, pattern: RegExp): Range[] {
  const ranges: Range[] = [], walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  for (let node = walker.nextNode() as Text | null; node; node = walker.nextNode() as Text | null) {
    for (const [start, end] of findMatches(node.data, pattern)) {
      const range = document.createRange(); range.setStart(node, start); range.setEnd(node, end); ranges.push(range);
    }
  }
  return ranges;
}

// Opens the collapsed prose and disclosures holding a range.
function expandTo(range: Range, root: Node) {
  for (let element = range.startContainer.parentElement; element && element !== root; element = element.parentElement) {
    (element as HTMLElement & { pharosFindExpand?: () => void }).pharosFindExpand?.();
    if (element instanceof HTMLDetailsElement) element.open = true;
  }
}

// `reveal` outlines the message and brings it into view, marking the
// pattern's matches (none for null) and opening the text holding the first;
// it never moves focus. `full` shows the original of a shortened output.
export function ResultMessage({ label, message: given, kind, compact = false, workspaceID = "", onOpen, reveal, full = false }: { label: string; message: TranscriptMessage; kind: "human" | "assistant"; compact?: boolean; workspaceID?: string; onOpen?: () => void; reveal?: RegExp | null; full?: boolean }) {
  const root = useRef<HTMLDivElement>(null);
  const body = useRef<HTMLDivElement>(null);
  const [original, setOriginal] = useState<{ of: TranscriptMessage; message: TranscriptMessage } | null>(null);
  const message = original?.of === given ? original.message : given;
  useEffect(() => {
    if (!full || !given.text_clipped || !window.pharosLoadOriginal) return;
    let live = true;
    window.pharosLoadOriginal(given).then(loaded => { if (live) setOriginal({ of: given, message: { ...loaded } }); }, () => {});
    return () => { live = false; };
  }, [given, full]);
  useEffect(() => {
    if (compact) {
      if (body.current) body.current.textContent = window.pharosMessageSummary?.(message) ?? String(message.text ?? "").slice(0, 640);
      return;
    }
    const card = window.pharosMessageCard?.(message, workspaceID);
    if (card) body.current?.replaceChildren(card);
    else if (body.current) body.current.textContent = String(message.text ?? "");
  }, [message.role, message.text, message.raw_text, message.authorship, compact, workspaceID]);
  useEffect(() => {
    if (reveal === undefined || !body.current) return;
    const ranges = reveal ? textRanges(body.current, reveal) : [];
    if (ranges.length) { expandTo(ranges[0], body.current); scrollRange(ranges[0]); }
    else root.current?.scrollIntoView({ block: "nearest" });
    if (!ranges.length || typeof Highlight === "undefined" || !CSS.highlights) return;
    CSS.highlights.set("pharos-result-match", new Highlight(...ranges));
    return () => { CSS.highlights.delete("pharos-result-match"); };
  }, [reveal, message]);
  return <div ref={root} className={`result-message ${kind}${compact ? " compact" : ""}${reveal !== undefined ? " pharos-highlight" : ""}`} {...openableProps(onOpen)}>
    <div className="result-message-label">{label}</div>
    <div className="result-message-body" ref={body} />
  </div>;
}

// The reader's Highlight row (transcriptHighlightControl in ui.py) for one
// session. Its boxes toggle show-<kind> classes on the panel holding the
// session's messages, which must be keyed by session so its pointer listeners
// go with it; the control's own window listeners go with this effect.
function TranscriptHighlights({ panel, conversation }: { panel: React.RefObject<HTMLDivElement | null>; conversation: Row }) {
  const host = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const control = panel.current ? window.pharosTranscriptHighlights?.(panel.current, conversation) : null;
    host.current?.replaceChildren(...(control ? [control] : []));
    return () => { control?.dispose(); control?.remove(); };
  }, [conversation]);
  return <div className="result-highlights" ref={host} />;
}

export type ConversationBranch = { conversation: Row; children: ConversationBranch[] };
export function conversationForest(conversations: Row[]): ConversationBranch[] {
  const nodes = new Map(conversations.map(conversation => [String(conversation.id), { conversation, children: [] } as ConversationBranch]));
  const roots: ConversationBranch[] = [];
  for (const node of nodes.values()) {
    const seen = new Set([String(node.conversation.id)]);
    let parent = nodes.get(String(node.conversation.parent_id)), ancestor = parent;
    while (ancestor) {
      const id = String(ancestor.conversation.id);
      if (seen.has(id)) { parent = undefined; break; }
      seen.add(id); ancestor = nodes.get(String(ancestor.conversation.parent_id));
    }
    if (parent) parent.children.push(node); else roots.push(node);
  }
  return roots;
}

export const initialOutlineEntry = (entries: OutlineEntry[]): OutlineEntry | undefined => entries.find(entry => entry.kind === "turn" && entry.human) ?? entries[0];
type ConversationState = { loading: boolean; error: string; entries: OutlineEntry[]; conversation?: Row };

// The innermost entry owning a message: a sub-agent before its turn.
export function outlineEntryForMessage(entries: OutlineEntry[], messageID: string): OutlineEntry | undefined {
  for (const item of entries) {
    const child = outlineEntryForMessage(item.children, messageID);
    if (child) return child;
    if ((item.messages ?? []).some(message => String(message.id) === messageID)) return item;
  }
}

// The block of a stored message a target names. Blocks split from one stored
// message share its ID; the first whose text matches the search is the one,
// else the entry's own prompt or reply, else the first.
export function targetBlock(entry: OutlineEntry, messageID: string, pattern: RegExp | null): TranscriptMessage | undefined {
  const blocks = entry.messages.filter(message => String(message.id) === messageID), test = pattern ? findTest(pattern) : null;
  return (test && blocks.find(block => test(String(block.text ?? "")))) || blocks.find(block => block === entry.human || block === entry.response) || blocks[0];
}
export const messageKey = (message: TranscriptMessage) => String(message.native_id ?? message.id ?? "");

export function findOutlineEntry(entries: OutlineEntry[], id: string): OutlineEntry | undefined {
  for (const item of entries) {
    if (item.id === id) return item;
    const child = findOutlineEntry(item.children, id);
    if (child) return child;
  }
}

// Older links identify a turn without its session. Search the root sessions
// before displaying a preview, so a later session's turn never opens the first.
export function conversationSelection(conversations: Row[], loaded: Record<string, ConversationState>, conversationID: string, entryID: string) {
  const roots = conversationForest(conversations);
  if (conversationID || !entryID) {
    const id = conversationID || String(roots[0]?.conversation.id ?? "");
    return { id, loadID: id, pending: false, error: "" };
  }
  for (const { conversation } of roots) {
    const id = String(conversation.id), state = loaded[id];
    if (!state || state.loading) return { id: "", loadID: id, pending: true, error: "" };
    if (findOutlineEntry(state.entries, entryID)) return { id, loadID: id, pending: false, error: "" };
  }
  const failed = roots.some(({ conversation }) => loaded[String(conversation.id)]?.error);
  return { id: "", loadID: "", pending: false, error: failed ? "Could not load the session for this saved turn." : "This saved turn is no longer available." };
}

export function ResultConversationBrowser({ workspaceID, readJSON, target }: { workspaceID: string; readJSON: (path: string, signal?: AbortSignal) => Promise<Row>; target?: ResultTarget | null }) {
  const [overview, setOverview] = useState<{ conversations: Row[]; loading: boolean; error: string }>({ conversations: [], loading: true, error: "" });
  const [reload, setReload] = useState(0);
  const [loaded, setLoaded] = useState<Record<string, ConversationState>>({});
  const [conversationID, setConversationID] = useViewEntry("search_cards", `${workspaceID}/conversation`, "");
  const [entryID, setEntryID] = useViewEntry("search_cards", `${workspaceID}/turn`, "");
  const [selectedKey, setSelectedKey] = useViewEntry("search_cards", `${workspaceID}/message`, "");
  const [disclosures] = useViewState<Record<string, boolean>>("disclosures", {});
  const requests = useRef(new Map<string, AbortController>());
  const loadedRef = useRef(loaded); loadedRef.current = loaded;
  const base = `/api/work/${encodeURIComponent(workspaceID)}`;
  const regionID = useId();
  const highlightPanel = useRef<HTMLDivElement>(null);
  async function load(id: string) {
    const cached = loadedRef.current[id];
    if ((cached && !cached.loading && !cached.error) || requests.current.has(id)) return;
    const controller = new AbortController(); requests.current.set(id, controller);
    setLoaded(previous => ({ ...previous, [id]: { loading: true, error: "", entries: [] } }));
    try {
      const conversation = await readJSON(`${base}/conversations/${encodeURIComponent(id)}`, controller.signal);
      const outline = window.pharosConversationOutline;
      if (!outline) throw new Error("The conversation reader did not load. Reload this page to try again.");
      if (!controller.signal.aborted) setLoaded(previous => ({ ...previous, [id]: { loading: false, error: "", entries: outline(conversation), conversation } }));
    } catch (error) {
      if (!controller.signal.aborted) setLoaded(previous => ({ ...previous, [id]: { loading: false, error: error instanceof Error ? error.message : "Conversation unavailable", entries: [] } }));
    } finally { if (requests.current.get(id) === controller) requests.current.delete(id); }
  }
  useEffect(() => {
    const controller = new AbortController();
    setOverview({ conversations: [], loading: true, error: "" }); setLoaded({}); loadedRef.current = {};
    void readJSON(base, controller.signal).then(work => {
      if (controller.signal.aborted) return;
      const conversations: Row[] = work.conversations ?? [];
      setOverview({ conversations, loading: false, error: "" });
    }).catch(error => { if (!controller.signal.aborted) setOverview({ conversations: [], loading: false, error: error instanceof Error ? error.message : "Conversation unavailable" }); });
    return () => { controller.abort(); for (const request of requests.current.values()) request.abort(); requests.current.clear(); };
  }, [workspaceID, reload]);
  const forest = conversationForest(overview.conversations);
  const selection = conversationSelection(overview.conversations, loaded, conversationID, entryID);
  const currentID = selection.id;
  useEffect(() => {
    if (overview.loading) return;
    if (selection.loadID) void load(selection.loadID);
    // Materialize restored sessions as well as deliberate expansions. The
    // standard accordion's onOpenChange callback is only for user changes.
    for (const conversation of overview.conversations) {
      const id = String(conversation.id), key = `search:${workspaceID}:conversation:${id}`;
      const open = disclosures[key] ?? (id === String(forest[0]?.conversation.id) || id === currentID);
      if (open) void load(id);
    }
  }, [selection.loadID, overview.loading, overview.conversations, currentID, workspaceID, disclosures]);
  const state = loaded[currentID];
  const selectedEntry = entryID || initialOutlineEntry(state?.entries ?? [])?.id;
  const entry = findOutlineEntry(state?.entries ?? [], selectedEntry ?? "") ?? initialOutlineEntry(state?.entries ?? []);
  const current = overview.conversations.find(conversation => String(conversation.id) === currentID);
  const choose = (conversation: string, entry = "", message = "") => { setConversationID(conversation); setEntryID(entry); setSelectedKey(message); setRevealed(null); void load(conversation); };
  // A target opens the entry holding its message: in its own session, or in
  // each session in turn, roots first. One no session holds changes nothing.
  const [pending, setPending] = useState<ResultTarget | null>(null);
  const [revealed, setRevealed] = useState<{ key: string; pattern: RegExp | null } | null>(null);
  useEffect(() => { if (target) setPending(target); }, [target]);
  useEffect(() => {
    if (!pending || overview.loading) return;
    const order = pending.conversationID ? [pending.conversationID] : [...new Set([...forest.map(branch => String(branch.conversation.id)), ...overview.conversations.map(conversation => String(conversation.id))])];
    for (const id of order) {
      const state = loaded[id];
      if (!state || state.loading) { void load(id); return; }
      const found = outlineEntryForMessage(state.entries, pending.messageID);
      const block = found && targetBlock(found, pending.messageID, pending.pattern);
      if (found && block) { choose(id, found.id, messageKey(block)); setRevealed({ key: messageKey(block), pattern: pending.pattern }); break; }
    }
    setPending(null);
  }, [pending, loaded, overview.loading]);
  // The selected message (from a Search match) is in the URL; revealing it is
  // attention, only when it was just opened.
  const selected = selectedKey ? entry?.messages?.find(message => messageKey(message) === selectedKey) : undefined;
  const revealOf = (message: TranscriptMessage | null | undefined) => selected && message === selected && revealed?.key === selectedKey ? revealed.pattern : undefined;
  // A match in a tool call, result or intermediate reply shows between them.
  const matched = selected !== entry?.human && selected !== entry?.response ? selected : undefined;
  function entryList(id: string, entries: OutlineEntry[]): React.ReactNode {
    return <ul>{entries.map(item => <li key={item.id} className={`conversation-outline-entry${item.children.length ? " has-subagents" : ""}`}>
      <button type="button" data-view-field="search_cards" className={item.kind === "turn" && !item.human ? "conversation-outline-setup" : undefined} title={item.prompt} aria-current={id === currentID && item.id === entry?.id ? "true" : undefined} onClick={() => choose(id, item.id)}>
        <span>{item.kind === "agent" ? "Sub-agent · " : ""}{item.label}{item.tools ? ` · ${item.tools} tool${item.tools === 1 ? "" : "s"}` : ""}</span>{item.prompt !== item.label ? <small>{item.prompt}</small> : null}
      </button>
      {item.children.length ? <ViewDisclosure viewKey={`search:${workspaceID}:conversation:${id}:entry:${item.id}:children`} className="conversation-outline-children" defaultOpen summary={<>{item.children.length} sub-agent{item.children.length === 1 ? "" : "s"}<small>in {item.kind === "turn" ? item.label.toLowerCase() : item.label}</small></>}>{entryList(id, item.children)}</ViewDisclosure> : null}
    </li>)}</ul>;
  }
  function branchList(branches: ConversationBranch[]): React.ReactNode {
    return <ul>{branches.map(({ conversation, children }) => {
      const id = String(conversation.id), data = loaded[id];
      const turnCount = data?.entries.filter(item => item.human).length ?? 0;
      return <li key={id}><ViewDisclosure viewKey={`search:${workspaceID}:conversation:${id}`} defaultOpen={id === String(forest[0]?.conversation.id) || id === currentID}
        summary={<span title="A session contains a sequence of prompts and replies. Select a turn below to preview it.">{conversation.parent_id ? "Sub-agent session" : "Session"} · {conversation.provider || "Agent"}{data && !data.loading && !data.error ? ` · ${turnCount} turn${turnCount === 1 ? "" : "s"}` : ""}</span>}>
        <p className="conversation-outline-session" title={conversation.title || conversation.first_prompt || id}>{conversation.title || conversation.first_prompt || id}</p>
        {data?.loading ? <p role="status">Loading turns…</p> : data?.error ? <p role="alert">{data.error} <button type="button" onClick={() => void load(id)}>Retry</button></p> : data ? data.entries.length ? entryList(id, data.entries) : <p>No retained turns.</p> : null}
        {children.length ? branchList(children) : null}
      </ViewDisclosure></li>;
    })}</ul>;
  }
  return <div className="result-turn-browser with-outline">
    <nav className="conversation-outline" aria-label="Conversation outline">
      <div className="conversation-outline-heading"><Icon name="tree" /> Conversation outline</div>
      {overview.loading ? <p role="status">Loading conversations…</p> : overview.error ? <p role="alert">{overview.error}</p> : forest.length ? branchList(forest) : <p>No retained conversations.</p>}
    </nav>
    <section className="result-turn-content" id={regionID} aria-label="Selected turn preview" aria-busy={overview.loading || selection.pending || state?.loading}>
      {overview.loading || selection.pending || state?.loading ? <p role="status">Loading turns…</p> : overview.error || selection.error || state?.error ? <p role="alert">{overview.error || selection.error || state?.error} <button type="button" onClick={() => overview.error || selection.error ? setReload(value => value + 1) : void load(currentID)}>Retry</button></p> : entry ? <div key={currentID} ref={highlightPanel} className="result-exchange">
        {state?.conversation ? <TranscriptHighlights panel={highlightPanel} conversation={state.conversation} /> : null}
        {entry.human ? <ResultMessage workspaceID={workspaceID} label={entry.kind === "agent" || current?.parent_id ? "Assignment" : "You"} message={entry.human} kind="human" reveal={revealOf(entry.human)} full={entry.human === selected} /> : null}
        {matched ? <ResultMessage workspaceID={workspaceID} label="Match" message={matched} kind={matched.role === "user" ? "human" : "assistant"} reveal={revealOf(matched)} full /> : null}
        <ResultMessage workspaceID={workspaceID} label={entry.kind === "agent" ? entry.label : current?.provider || "Agent"} message={entry.response ?? { role: "assistant", text: "No retained response for this turn." }} kind="assistant" reveal={revealOf(entry.response)} full={entry.response === selected} />
      </div> : <p className="muted">No retained turns are available.</p>}
    </section>
  </div>;
}
