import React, { useEffect, useId, useRef, useState } from "react";
import { ITEM_DETAILS_COLUMN, readFieldValue, type FieldDef, type QueryState } from "@pythia-software/query-table-core";
import { resolveRenderer, type RenderRegistry } from "@pythia-software/query-table-ui";
import { Icon } from "./icons";
import { useViewEntry } from "./view-state";
import { ViewDisclosure } from "./view-disclosure";
import { formatConcurrencyDuration } from "./concurrency-data";

type Row = Record<string, any>;
export type TranscriptMessage = { id?: string; role: string; text: unknown; raw_text?: unknown; authorship?: unknown };
export type OutlineEntry = { id: string; kind: "turn" | "agent"; label: string; prompt: string; human: TranscriptMessage | null; response: TranscriptMessage | null; tools: number; children: OutlineEntry[] };
declare global {
  interface Window {
    pharosMessageCard?: (message: TranscriptMessage, workspace?: string) => HTMLElement;
    pharosHasHumanWriting?: (message: TranscriptMessage) => boolean;
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

export function ResultMessage({ label, message, kind, compact = false, workspaceID = "" }: { label: string; message: TranscriptMessage; kind: "human" | "assistant"; compact?: boolean; workspaceID?: string }) {
  const body = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (compact) {
      if (body.current) body.current.textContent = window.pharosMessageSummary?.(message) ?? String(message.text ?? "").slice(0, 640);
      return;
    }
    const card = window.pharosMessageCard?.(message, workspaceID);
    if (card) body.current?.replaceChildren(card);
    else if (body.current) body.current.textContent = String(message.text ?? "");
  }, [message.role, message.text, message.raw_text, message.authorship, compact, workspaceID]);
  return <div className={`result-message ${kind}${compact ? " compact" : ""}`}>
    <div className="result-message-label">{label}{!compact && kind === "human" && window.pharosHasHumanWriting?.(message) ? <span className="human-writing-key"><i aria-hidden="true" />Human-written text highlighted</span> : null}</div>
    <div className="result-message-body" ref={body} />
  </div>;
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
type ConversationState = { loading: boolean; error: string; entries: OutlineEntry[] };

export function ResultConversationBrowser({ workspaceID, readJSON }: { workspaceID: string; readJSON: (path: string, signal?: AbortSignal) => Promise<Row> }) {
  const [overview, setOverview] = useState<{ conversations: Row[]; loading: boolean; error: string }>({ conversations: [], loading: true, error: "" });
  const [reload, setReload] = useState(0);
  const [loaded, setLoaded] = useState<Record<string, ConversationState>>({});
  const [conversationID, setConversationID] = useViewEntry("search_cards", `${workspaceID}/conversation`, "");
  const [entryID, setEntryID] = useViewEntry("search_cards", `${workspaceID}/turn`, "");
  const requests = useRef(new Map<string, AbortController>());
  const loadedRef = useRef(loaded); loadedRef.current = loaded;
  const base = `/api/work/${encodeURIComponent(workspaceID)}`;
  const regionID = useId();
  async function load(id: string) {
    const cached = loadedRef.current[id];
    if ((cached && !cached.loading && !cached.error) || requests.current.has(id)) return;
    const controller = new AbortController(); requests.current.set(id, controller);
    setLoaded(previous => ({ ...previous, [id]: { loading: true, error: "", entries: [] } }));
    try {
      const conversation = await readJSON(`${base}/conversations/${encodeURIComponent(id)}`, controller.signal);
      const outline = window.pharosConversationOutline;
      if (!outline) throw new Error("The conversation reader did not load. Reload this page to try again.");
      if (!controller.signal.aborted) setLoaded(previous => ({ ...previous, [id]: { loading: false, error: "", entries: outline(conversation) } }));
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
      const first = conversationForest(conversations)[0]?.conversation;
      if (first) void load(conversations.some(item => String(item.id) === conversationID) ? conversationID : String(first.id));
    }).catch(error => { if (!controller.signal.aborted) setOverview({ conversations: [], loading: false, error: error instanceof Error ? error.message : "Conversation unavailable" }); });
    return () => { controller.abort(); for (const request of requests.current.values()) request.abort(); requests.current.clear(); };
  }, [workspaceID, reload]);
  const forest = conversationForest(overview.conversations);
  const currentID = conversationID || String(forest[0]?.conversation.id ?? "");
  useEffect(() => { if (currentID) void load(currentID); }, [currentID]);
  const state = loaded[currentID];
  const selectedEntry = entryID || initialOutlineEntry(state?.entries ?? [])?.id;
  function findEntry(entries: OutlineEntry[]): OutlineEntry | undefined {
    for (const item of entries) {
      if (item.id === selectedEntry) return item;
      const child = findEntry(item.children);
      if (child) return child;
    }
  }
  const entry = findEntry(state?.entries ?? []) ?? initialOutlineEntry(state?.entries ?? []);
  const current = overview.conversations.find(conversation => String(conversation.id) === currentID);
  const choose = (conversation: string, entry = "") => { setConversationID(conversation); setEntryID(entry); void load(conversation); };
  function entryList(id: string, entries: OutlineEntry[]): React.ReactNode {
    return <ul>{entries.map(item => <li key={item.id} className={`conversation-outline-entry${item.children.length ? " has-subagents" : ""}`}>
      <button type="button" data-view-field="search_cards" className={item.kind === "turn" && !item.human ? "conversation-outline-setup" : undefined} title={item.prompt} aria-current={id === currentID && item.id === entry?.id ? "true" : undefined} onClick={() => choose(id, item.id)}>
        <span>{item.kind === "agent" ? "Sub-agent · " : ""}{item.label}{item.tools ? ` · ${item.tools} tool${item.tools === 1 ? "" : "s"}` : ""}</span>{item.prompt !== item.label ? <small>{item.prompt}</small> : null}
      </button>
      {item.children.length ? <ViewDisclosure viewKey={`search:${workspaceID}:conversation:${id}:entry:${item.id}:children`} className="conversation-outline-children" open><summary>{item.children.length} sub-agent{item.children.length === 1 ? "" : "s"}<small>in {item.kind === "turn" ? item.label.toLowerCase() : item.label}</small></summary>{entryList(id, item.children)}</ViewDisclosure> : null}
    </li>)}</ul>;
  }
  function branchList(branches: ConversationBranch[]): React.ReactNode {
    return <ul>{branches.map(({ conversation, children }) => {
      const id = String(conversation.id), data = loaded[id];
      const turnCount = data?.entries.filter(item => item.human).length ?? 0;
      return <li key={id}><ViewDisclosure viewKey={`search:${workspaceID}:conversation:${id}`} open={id === String(forest[0]?.conversation.id) || id === currentID} onToggle={event => {
        const open = event.currentTarget.open;
        if (open && !loadedRef.current[id]) void load(id);
      }}>
        <summary title="A session contains a sequence of prompts and replies. Select a turn below to preview it."><span>{conversation.parent_id ? "Sub-agent session" : "Session"} · {conversation.provider || "Agent"}{data && !data.loading && !data.error ? ` · ${turnCount} turn${turnCount === 1 ? "" : "s"}` : ""}</span></summary>
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
    <section className="result-turn-content" id={regionID} aria-label="Selected turn preview" aria-busy={overview.loading || state?.loading}>
      {overview.loading || state?.loading ? <p role="status">Loading turns…</p> : overview.error || state?.error ? <p role="alert">{overview.error || state?.error} <button type="button" onClick={() => overview.error ? setReload(value => value + 1) : void load(currentID)}>Retry</button></p> : entry ? <div className="result-exchange">
        {entry.human ? <ResultMessage workspaceID={workspaceID} label={entry.kind === "agent" || current?.parent_id ? "Assignment" : "You"} message={entry.human} kind="human" /> : null}
        <ResultMessage workspaceID={workspaceID} label={entry.kind === "agent" ? entry.label : current?.provider || "Agent"} message={entry.response ?? { role: "assistant", text: "No retained response for this turn." }} kind="assistant" />
      </div> : <p className="muted">No retained turns are available.</p>}
    </section>
  </div>;
}
