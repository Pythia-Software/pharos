import React, { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { isOrGroup, type WhereTerm } from "@pythia-software/query-table-core";
import type { QueryTableApi } from "@pythia-software/query-table-react";
import type { CellContext } from "@pythia-software/query-table-ui";

// The Human Words message view: one row per user message, with its text
// highlighted by what the authorship classification counted each span as (see
// docs/human-authorship.md), and the rule that labelled it on hover.

type Row = Record<string, any>;
type SpanSource = { workspace_id: string; conversation_id: string; message_id?: string | null; title?: string | null };
export type Segment = { text: string; category?: string; rule?: string; reason?: string | null; words?: number; source?: SpanSource };

// Categories in the order the page lists them. The second category of a group
// shares its color, striped, so the chart's five groups still read.
export const authorshipCategories = [
  { key: "typed", label: "Typed", color: "var(--sea)", description: "Written or dictated by you: no other rule claimed it." },
  { key: "pasted", label: "Likely pasted", color: "var(--heat-1)", description: "Looks like code, logs, tables, or agent formatting, or arrived faster than anyone types. Inferred from how the text looks." },
  { key: "quoted", label: "Copied", color: "var(--gold)", description: "Matches agent output from the 48 hours before, or is quoted with >." },
  { key: "resent", label: "Re-sent", color: "var(--gold)", striped: true, description: "Matches something you sent before." },
  { key: "template", label: "Template", color: "var(--heat-2)", description: "A one-click prompt or saved prompt sent again and again, or a slash command." },
  { key: "attachment", label: "Attachment", color: "var(--heat-2)", striped: true, description: "An attachment reference or image placeholder." },
  { key: "harness", label: "Harness", color: "var(--muted)", description: "Instructions the harness put at the start of the prompt." },
  { key: "automated", label: "Automated", color: "var(--muted)", striped: true, description: "The whole message came from a script or another agent." },
];
const categoryByKey = new Map(authorshipCategories.map(category => [category.key, category]));
const categoryLabel = (key: unknown) => categoryByKey.get(String(key))?.label ?? String(key ?? "");

// What each rule looks for, from docs/human-authorship.md.
const ruleHelp: Record<string, string> = {
  "No other rule matched": "No other rule claimed this text, so it counts as typed.",
  "Code block": "Text inside ``` fences.",
  "Log, trace, code, or data": "At least three lines of program output or code (logs, stack traces, diffs, JSON, file:line, shell prompts, source) with at most two prose lines between them.",
  "Table": "Two or more markdown table rows.",
  "Formatted like agent output": "A region with two or more headings or three or more bolded bullets, the way agents format replies.",
  "Mostly identifiers, paths, or code": "A line whose words are mostly identifiers: paths, IDs, selectors, or code.",
  "Path, URL, or ID": "A run of identifiers 20 or more characters long inside a typed sentence.",
  "Sent faster than typing": "The rest of the message arrived faster than 20 characters a second after your previous message in this conversation.",
  "Generated page feedback": "A page-feedback report a tool generated.",
  "Quoted with >": "Lines starting with >.",
  "Matches recent agent output": "A run of 12 or more words matching agent output from the 48 hours before, in any conversation.",
  "Already sent earlier": "A run of 12 or more words matching a message you sent before, however long ago.",
  "Line already sent in other conversations": "A line of 8 or more words already sent in 2 other conversations.",
  "Repeated prompt": "The same text was sent in 5 or more sessions on 3 or more days: a button or saved prompt.",
  "Slash command": "A leading slash command. Its arguments stay typed.",
  "Attachment reference": "A Conductor attachment marker, an attachment path, or an image placeholder.",
  "Launched by a command": "A headless run linked to the shell command, in another conversation, that started it.",
  "Sub-agent conversation": "A prompt a parent agent sent a sub-agent.",
  "Sent by TL1 orchestration": "A prompt TL1 sent.",
  "Sent by another agent session": "Conductor recorded another agent session as its sender.",
  "Sent by automation": "Conductor recorded an API key as its sender.",
};
const harnessRules = new Set(["Conductor system instruction", "System reminder", "Environment context", "Project instructions", "Plugin recommendations", "Harness context"]);
function explainRule(rule: string | undefined, category: string | undefined): string {
  if (rule && ruleHelp[rule]) return ruleHelp[rule];
  if (rule && harnessRules.has(rule)) return "A block the harness adds at the start of the message. It stays part of the prompt; only this count sets it apart.";
  if (rule?.startsWith("Sent by a headless") || rule === "Sent by a codex exec run") return "The run's transcript or logs show it was started headlessly, by a script or another agent.";
  return categoryByKey.get(category ?? "")?.description ?? "";
}

// Opens a message in its conversation, as the Library's keyword matches do.
export function openMessage(workspace: string, conversation?: string | null, message?: string | null) {
  const params = new URLSearchParams();
  if (conversation) params.set("conversation", conversation);
  if (message) params.set("message", message);
  history.pushState(null, "", `/work/${encodeURIComponent(workspace)}${params.size ? `?${params}` : ""}`);
  (window as unknown as { routeFromLocation?: () => void }).routeFromLocation?.();
}

function segmentsOf(row: Row): Segment[] {
  return Array.isArray(row.segments) ? row.segments : [{ text: String(row.text ?? "") }];
}

// A span's explanation, shown on hover or focus and kept open by a click so
// its source link can be followed.
type Tip = { segment: Segment; anchor: DOMRect; pinned: boolean };

function SpanTip({ tip, onEnter, onLeave, onClose }: { tip: Tip; onEnter: () => void; onLeave: () => void; onClose: () => void }) {
  const box = useRef<HTMLDivElement>(null);
  const { segment, anchor } = tip;
  const category = categoryByKey.get(segment.category ?? "");
  useLayoutEffect(() => {
    const element = box.current;
    if (!element) return;
    const width = element.offsetWidth, height = element.offsetHeight;
    const left = Math.max(8, Math.min(anchor.left, window.innerWidth - width - 8));
    const below = anchor.bottom + 6, above = anchor.top - height - 6;
    element.style.left = `${left}px`;
    element.style.top = `${below + height > window.innerHeight - 8 && above > 8 ? above : below}px`;
  }, [tip]);
  useEffect(() => {
    const close = (event: KeyboardEvent) => { if (event.key === "Escape") onClose(); };
    window.addEventListener("keydown", close);
    window.addEventListener("scroll", onClose, true);
    return () => { window.removeEventListener("keydown", close); window.removeEventListener("scroll", onClose, true); };
  }, []);
  const words = Number(segment.words) || 0;
  const source = segment.source;
  return createPortal(<div ref={box} className={`authored-tip${tip.pinned ? " pinned" : ""}`} data-category={segment.category} role="tooltip" onMouseEnter={onEnter} onMouseLeave={onLeave}>
    <div className="authored-tip-head">
      <i className="authored-swatch" data-category={segment.category} />
      <strong>{category?.label ?? "Unclassified"}</strong>
      <span className="muted">{words.toLocaleString()} word{words === 1 ? "" : "s"}</span>
    </div>
    <div className="authored-tip-reason">{segment.reason || segment.rule || "No other rule matched"}</div>
    <p className="authored-tip-help">{explainRule(segment.rule, segment.category)}</p>
    {source?.workspace_id ? <button type="button" className="authored-tip-source" onClick={() => { onClose(); openMessage(source.workspace_id, source.conversation_id, source.message_id); }}>
      Open the matching text in {source.title || "its conversation"}
    </button> : null}
    {tip.pinned ? null : <div className="authored-tip-hint muted">Click to keep this open</div>}
  </div>, document.body);
}

// Long spans that are not typed fold to their start, so the words you typed
// around them stay in view: harness blocks almost entirely.
const foldAt = (segment: Segment) => segment.category === "harness" ? 160 : segment.category && segment.category !== "typed" ? 1200 : Infinity;
const foldedChars = (segment: Segment) => segment.category === "harness" ? 90 : 400;

// AuthoredText draws a message with each classified span highlighted.
export function AuthoredText({ segments }: { segments: Segment[] }) {
  const [tip, setTip] = useState<Tip | null>(null);
  const [unfolded, setUnfolded] = useState<Set<number>>(() => new Set());
  const hide = useRef(0);
  const tipRef = useRef(tip);
  tipRef.current = tip;
  useEffect(() => () => window.clearTimeout(hide.current), []);
  // A click outside a pinned tip closes it.
  useEffect(() => {
    if (!tip?.pinned) return;
    const close = (event: MouseEvent) => {
      const target = event.target as Element | null;
      if (!target?.closest(".authored-tip, .authored-span")) setTip(null);
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [tip?.pinned]);
  const anchorAt = (element: HTMLElement, x?: number, y?: number) => {
    const rects = [...element.getClientRects()];
    if (x === undefined || y === undefined || rects.length < 2) return rects[0] ?? element.getBoundingClientRect();
    // A span wrapping over lines anchors to the line under the pointer.
    return rects.reduce((best, rect) => Math.abs((rect.top + rect.bottom) / 2 - y) < Math.abs((best.top + best.bottom) / 2 - y) ? rect : best);
  };
  const show = (segment: Segment, element: HTMLElement, x?: number, y?: number) => {
    window.clearTimeout(hide.current);
    if (tipRef.current?.pinned && tipRef.current.segment !== segment) return;
    if (tipRef.current?.segment === segment) return;
    setTip({ segment, anchor: anchorAt(element, x, y), pinned: false });
  };
  const leave = () => {
    window.clearTimeout(hide.current);
    hide.current = window.setTimeout(() => setTip(current => current?.pinned ? current : null), 150);
  };
  return <div className="authored-text">
    {segments.map((segment, index) => segment.category
      ? <mark key={index} className={`authored-span${tip?.segment === segment ? " active" : ""}`} data-category={segment.category} tabIndex={0}
        aria-label={`${categoryLabel(segment.category)}: ${segment.reason || segment.rule || ""}`}
        onMouseEnter={event => show(segment, event.currentTarget, event.clientX, event.clientY)} onMouseLeave={leave}
        onFocus={event => show(segment, event.currentTarget)} onBlur={leave}
        onClick={event => {
          const element = event.currentTarget;
          setTip(current => current?.segment === segment && current.pinned ? null : { segment, anchor: anchorAt(element, event.clientX, event.clientY), pinned: true });
        }}>{segment.text.length > foldAt(segment) && !unfolded.has(index) ? <>
          {segment.text.slice(0, foldedChars(segment)).trimEnd()}…{" "}
          <button type="button" className="authored-unfold" onClick={event => { event.stopPropagation(); setUnfolded(current => new Set(current).add(index)); }}>
            show all {(Number(segment.words) || 0).toLocaleString()} words
          </button>
        </> : segment.text}</mark>
      : <React.Fragment key={index}>{segment.text}</React.Fragment>)}
    {tip ? <SpanTip tip={tip} onEnter={() => window.clearTimeout(hide.current)} onLeave={leave} onClose={() => setTip(null)} /> : null}
  </div>;
}

// A row's words per category, as a thin stacked bar.
function CategoryBar({ row }: { row: Row }) {
  const parts = authorshipCategories.map(category => [category, Number(row[`${category.key}_words`]) || 0] as const).filter(([, words]) => words > 0);
  const total = parts.reduce((sum, [, words]) => sum + words, 0);
  if (!total) return null;
  return <div className="authored-bar" role="img" aria-label={parts.map(([category, words]) => `${category.label} ${words} words`).join(", ")}>
    {parts.map(([category, words]) => <i key={category.key} data-category={category.key} style={{ flex: words }} title={`${category.label}: ${words.toLocaleString()} words (${Math.round(100 * words / total)}%)`} />)}
  </div>;
}

// Messages longer than this start collapsed.
const collapsedChars = 1800;

function AuthoredMessageCard({ row }: { row: Row }) {
  const segments = segmentsOf(row);
  const length = segments.reduce((sum, segment) => sum + (segment.text.length > foldAt(segment) ? foldedChars(segment) : segment.text.length), 0);
  const [expanded, setExpanded] = useState(false);
  const collapsible = length > collapsedChars;
  const typed = Number(row.typed_words) || 0, other = Number(row.other_words) || 0;
  const sent = row.sent_at ? new Date(row.sent_at) : null;
  const meta = [sent ? sent.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }) : "", row.repository_name, row.source_kind, row.provider, row.subagent ? "Sub-agent" : ""].filter(Boolean);
  return <article className="authored-card">
    <div className="authored-head">
      <div className="authored-summary">
        <button type="button" className="authored-title" onClick={() => window.pharosOpenDetail?.(String(row.work_id ?? row.workspace_id))}>{String(row.title ?? "Untitled work")}</button>
        <div className="authored-meta">{meta.join(" · ")}</div>
      </div>
      <button type="button" className="authored-open" onClick={() => openMessage(String(row.workspace_id), row.conversation_id, row.id)}>Open in conversation</button>
    </div>
    <div className="authored-facts">
      <span className="authored-fact" data-category="typed"><i className="authored-swatch" data-category="typed" />{typed.toLocaleString()} word{typed === 1 ? "" : "s"} typed</span>
      {other ? <span className="authored-fact">{other.toLocaleString()} from elsewhere</span> : null}
      {row.main_category && row.main_category !== "typed" ? <span className="authored-fact"><i className="authored-swatch" data-category={row.main_category} />Mostly {categoryLabel(row.main_category).toLowerCase()}</span> : null}
      {row.typed_share !== null && row.typed_share !== undefined ? <span className="authored-fact muted">{Number(row.typed_share).toLocaleString()}% typed</span> : null}
    </div>
    <CategoryBar row={row} />
    <div className={`authored-body${collapsible && !expanded ? " collapsed" : ""}`}>
      <AuthoredText segments={segments} />
    </div>
    {collapsible || row.text_truncated ? <div className="authored-more">
      {collapsible ? <button type="button" onClick={() => setExpanded(value => !value)} aria-expanded={expanded}>{expanded ? "Collapse" : `Show the whole message (${(Number(row.words) || 0).toLocaleString()} words)`}</button> : null}
      {row.text_truncated && (expanded || !collapsible) ? <span className="muted">Only the start of this message is shown. <button type="button" className="authored-link" onClick={() => openMessage(String(row.workspace_id), row.conversation_id, row.id)}>Read the rest in its conversation</button></span> : null}
    </div> : null}
  </article>;
}

export function AuthoredMessages({ api, emptyMessage }: { api: QueryTableApi<Row>; emptyMessage: string }) {
  if (!api.loading && !api.rows.length) return <div className="conversation-results-empty no-results">{emptyMessage}</div>;
  return <div className={`conversation-results authored-results ${api.loading ? "loading" : ""}`} aria-busy={api.loading}>
    {api.loading && !api.rows.length ? <div className="conversation-results-empty">Loading messages…</div> : null}
    {api.rows.map(row => <AuthoredMessageCard key={String(api.rowId(row) ?? row.id)} row={row} />)}
    <AuthoredPager api={api} />
  </div>;
}

// The table's footer pages it; the message list pages with this.
function AuthoredPager({ api }: { api: QueryTableApi<Row> }) {
  const { limit, offset } = api.query, total = api.total ?? 0;
  if (!total || total <= limit) return null;
  const go = (next: number) => { api.setQuery(previous => ({ ...previous, offset: Math.max(0, next) })); window.scrollTo({ top: 0 }); };
  return <nav className="authored-pager" aria-label="Message pages">
    <button type="button" disabled={offset <= 0} onClick={() => go(offset - limit)}>Previous</button>
    <span>{(offset + 1).toLocaleString()}–{Math.min(offset + limit, total).toLocaleString()} of {total.toLocaleString()}</span>
    <button type="button" disabled={offset + limit >= total} onClick={() => go(offset + limit)}>Next</button>
  </nav>;
}

const inlineChars = 400;

// Table cells: the start of a message on one line, highlighted, with each
// span's category and reason as its tooltip.
export const authoredRenderers = {
  authored_text({ row }: CellContext<Row>) {
    const shown: React.ReactNode[] = [];
    let used = 0;
    for (const [index, segment] of segmentsOf(row).entries()) {
      if (used >= inlineChars) break;
      // Harness blocks shrink to a marker, so the text after them shows.
      const text = segment.category === "harness" ? "harness " : segment.text.slice(0, inlineChars - used);
      used += text.length;
      shown.push(segment.category
        ? <mark key={index} className="authored-span" data-category={segment.category} title={`${categoryLabel(segment.category)} · ${segment.reason || segment.rule || ""}${segment.words ? ` · ${segment.words} words` : ""}`}>{text}</mark>
        : <React.Fragment key={index}>{text}</React.Fragment>);
    }
    return <span className="authored-inline">{shown}</span>;
  },
  authorship_category({ value }: CellContext<Row>) {
    return value ? <CategoryPill category={String(value)} /> : "";
  },
  authorship_categories({ value }: CellContext<Row>) {
    return Array.isArray(value) ? <span className="authored-pills">{value.map(category => <CategoryPill key={category} category={category} />)}</span> : "";
  },
  authorship_rules({ value }: CellContext<Row>) {
    return Array.isArray(value) ? <span title={value.join("\n")}>{value.join(", ")}</span> : "";
  },
};

export function CategoryPill({ category, label }: { category: string; label?: string }) {
  return <span className="authored-pill"><i className="authored-swatch" data-category={category} />{label ?? categoryLabel(category)}</span>;
}

// includesCategory reports whether where keeps only messages holding category.
function includesCategory(where: WhereTerm[], category: string): boolean {
  return where.some(term => !isOrGroup(term) && term.field === "categories" && term.op === "includes" && !term.negated && term.value === category);
}

type MixResult = { messages: number; words: Record<string, number> };

// The words per category of every message the filters match, as a stacked
// bar and a key that filters to messages holding a category.
export function MessageMix({ where, reload, onFilter }: { where: WhereTerm[]; reload: number; onFilter: (change: (where: WhereTerm[]) => WhereTerm[]) => void }) {
  const key = JSON.stringify(where);
  const [state, setState] = useState<{ result: MixResult | null; error: string; loading: boolean }>({ result: null, error: "", loading: true });
  useEffect(() => {
    const controller = new AbortController();
    setState(previous => ({ ...previous, loading: true }));
    const aggregations = [{ id: "messages", op: "count", groupBy: [] }, ...authorshipCategories.map(category => ({ id: category.key, op: "sum", field: `${category.key}_words`, groupBy: [] }))];
    fetch("/api/query/writing_messages/aggregations", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ where, aggregations }), signal: controller.signal })
      .then(async response => {
        const body = await response.json().catch(() => ({}));
        if (!response.ok) throw new Error(body.error || `Request failed with HTTP ${response.status}`);
        const value = (id: string) => Number(body.metrics?.find((metric: Row) => metric.id === id)?.buckets?.[0]?.value) || 0;
        setState({ result: { messages: value("messages"), words: Object.fromEntries(authorshipCategories.map(category => [category.key, value(category.key)])) }, error: "", loading: false });
      })
      .catch(failure => { if (!controller.signal.aborted) setState({ result: null, error: failure instanceof Error ? failure.message : String(failure), loading: false }); });
    return () => controller.abort();
  }, [key, reload]);
  const words = state.result?.words ?? {};
  const total = Object.values(words).reduce((sum, value) => sum + value, 0);
  function toggle(category: string) {
    onFilter(current => includesCategory(current, category)
      ? current.filter(term => isOrGroup(term) || !(term.field === "categories" && term.op === "includes" && !term.negated && term.value === category))
      : [...current, { field: "categories", op: "includes", value: category }]);
  }
  return <section className="writing-mix authored-mix" aria-label="Where these messages' text came from">
    <h3>{state.result ? `${state.result.messages.toLocaleString()} message${state.result.messages === 1 ? "" : "s"} · ${total.toLocaleString()} words` : "Messages"}</h3>
    {state.error ? <p className="query-table-error">{state.error}</p> : null}
    {total ? <div className="writing-stack" role="img" aria-label={authorshipCategories.filter(category => words[category.key]).map(category => `${category.label} ${Math.round(100 * words[category.key] / total)}%`).join(", ")}>
      {authorshipCategories.map(category => words[category.key] ? <i key={category.key} data-category={category.key} style={{ flex: words[category.key] }} title={`${category.label}: ${words[category.key].toLocaleString()} words (${Math.round(100 * words[category.key] / total)}%)`} /> : null)}
    </div> : <p className="muted">{state.loading ? "Loading…" : "No classified user messages match these filters."}</p>}
    <div className="authored-legend" role="group" aria-label="Show messages holding a category">
      {authorshipCategories.map(category => {
        const active = includesCategory(where, category.key);
        return <button key={category.key} type="button" data-category={category.key} aria-pressed={active} title={`${category.description}\n\nClick to ${active ? "stop showing only" : "show only"} messages holding ${category.label.toLowerCase()} text.`} onClick={() => toggle(category.key)}>
          <i className="authored-swatch" data-category={category.key} />{category.label}<strong>{(words[category.key] ?? 0).toLocaleString()}</strong>
        </button>;
      })}
    </div>
    <p className="muted authored-mix-note">Each message's text is highlighted by what it counted as. Hover over or focus a highlighted span to see the rule that labelled it and why; click it to keep the explanation open.</p>
  </section>;
}
