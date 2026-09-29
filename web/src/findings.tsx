import React, { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import "./findings.css";
import { Icon } from "./icons";

// The Findings tab: recurring patterns in agents' work that a change to a
// repository, its instructions, or a harness setting can fix, the prompt cart
// that hands them to an agent, and the before-and-after measurement that
// follows. Words come from the service (each detector writes its own); this
// file only arranges them. Internal terms (IDs, detector names) never appear
// here: IDs live only inside the prompts.

type Row = Record<string, any>;
type Rank = "usd" | "tokens" | "minutes" | "failures";
type Handoff = "pr" | "diff";
type Impact = { usd: number; tokens: number; minutes: number; failures: number; window_days?: number };
type Card = {
  id: string; state: string; title: string; explanation: string; impact: Impact; impact_note?: string; rate?: string;
  change: string; change_label: string; attempt?: number; target: string; target_label: string; scope_label: string;
  repository_id?: string | null; first_seen_at?: string; last_seen?: string; active?: boolean; new?: boolean;
  cart?: { target: string; target_label: string; ticked: boolean }; dismissed?: Row; snoozed?: Row; watching?: Row;
  regressed?: Row; reopened?: Row; inconclusive?: Row; undo?: Row; result?: Row;
};
type CartItem = { id: string; title: string; ticked: boolean; change_label: string; suggested_target: string; undo?: boolean };
type CartGroup = { target: string; label: string; handoff: Handoff; handoff_reason: string; ticked: number; items: CartItem[] };
type Target = { target: string; label: string };
type Checkpoint = { threshold: number; shown: number; typical_wait_days: number; clear_share: number };
type Repository = { id: string; name: string; work: number; pr_share: number; handoff: Handoff; override: Handoff | null };
type Win = { id: string; title: string; where: string; change: string; before: string; now: string; saved: Row; days_left: number; regressed_at: string | null; decided_at?: string; copied_at?: string };
type Status = { built_at: string | null; took_ms: unknown; measured_at: string | null; conversations_28d: unknown; weekly_conversations: unknown; running: boolean; phase: string | null; error: string | null };
type Overview = {
  status: Status; settings: { threshold: number; rank: Rank; handoff: "auto" | Handoff; repository_handoff: Record<string, Handoff>; seen_at: string };
  threshold: number; recommended: number; checkpoints: Checkpoint[];
  summary: { saved: Row; open: number; watching: number; won: number; dismissed: number; snoozed: number; regressed: number; at_stake_usd: number; new: number; next_result_days: number | null };
  findings: Card[]; cart: CartGroup[]; targets: Target[]; repositories: Repository[]; near: Array<{ id: string; title: string; affected: number }>; wins: Win[];
};
type Detail = Card & {
  chart?: { title: string; kind: "rate" | "mean" | "share"; weeks: Week[]; anchor: string; copies: string[]; baseline: number; baseline_phrase: string };
  evidence?: Row[]; facts?: Row; attempts?: Row[]; prompt?: string; steps?: Array<{ change: string; label: string }>; metric?: Row; baseline?: Row;
};
type Week = { start: string; events: number; exposure: number; rate?: number | null; hollow: boolean; after: boolean; cost_usd: number };
type List = "open" | "watching" | "wins" | "dismissed";
type Route = { finding: string; tab: "overview" | "evidence" | "prompt"; view: "" | "settings"; list: List; repo: string };
type Copy = (text: string) => Promise<void>;
type Act = (id: string, body: Row, done?: string) => Promise<boolean>;

declare global {
  interface Window {
    pharosNavigate?: (url: string) => void;
    pharosNameVisit?: (title: string) => void;
  }
}

// ---------------------------------------------------------------- service

async function request<T>(url: string, body?: unknown): Promise<T> {
  const response = await fetch(url, body === undefined ? undefined : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const parsed = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(parsed.error || `Request failed with HTTP ${response.status}`);
  return parsed as T;
}
const findingURL = (id: string) => `/api/findings/${encodeURIComponent(id)}`;

// One overview is shared by the page and the header (tab badge, cart button).
let latest: Overview | null = null;
const subscribers = new Set<(overview: Overview) => void>();
function publish(overview: Overview) {
  latest = overview;
  subscribers.forEach(listener => listener(overview));
  updateChrome(overview.summary?.new ?? 0, cartCount(overview.cart));
}
async function loadOverview(): Promise<Overview> {
  const overview = await request<Overview>("/api/findings");
  publish(overview);
  return overview;
}
const cartCount = (cart: CartGroup[] = []) => cart.reduce((sum, group) => sum + group.items.length, 0);

// ---------------------------------------------------------------- header

let pageOpen = false;
function updateChrome(fresh: number, inCart: number) {
  const badge = document.getElementById("findingsBadge");
  const tab = document.getElementById("findingsTab");
  const shownNew = pageOpen ? 0 : fresh;
  if (badge) { badge.hidden = shownNew <= 0; badge.textContent = shownNew > 99 ? "99+" : String(shownNew); }
  tab?.setAttribute("aria-label", shownNew > 0 ? `Findings, ${shownNew} new since your last visit` : "Findings");
  const button = document.getElementById("findingsCartButton");
  const countNode = document.getElementById("findingsCartCount");
  if (button) {
    button.hidden = inCart <= 0;
    button.title = `Prompts to copy: ${inCart} ${inCart === 1 ? "finding is" : "findings are"} waiting to be handed to an agent`;
    button.setAttribute("aria-label", `Prompts to copy, ${inCart} ${inCart === 1 ? "finding" : "findings"}`);
  }
  if (countNode) countNode.textContent = String(inCart);
}

/** Keeps the tab badge and the header's cart button current: once at load, every few minutes while the app is visible, and after actions. */
export function startFindingsChrome() {
  // A filtered overview carries the same summary and cart with fewer cards.
  const poll = () => { if (!document.hidden) request<{ summary?: { new?: number }; cart_count?: number }>("/api/findings?state=summary").then(body => updateChrome(body.summary?.new ?? 0, body.cart_count ?? 0)).catch(() => { /* The badge waits for the next poll. */ }); };
  poll();
  window.setInterval(poll, 4 * 60_000);
  document.addEventListener("visibilitychange", () => { if (!document.hidden && !pageOpen) poll(); });
  document.getElementById("findingsCartButton")?.addEventListener("click", () => {
    navigate("/findings");
    window.setTimeout(() => window.dispatchEvent(new Event("pharos:findings-cart")), 60);
  });
}

// ---------------------------------------------------------------- routing

function navigate(url: string) {
  if (window.pharosNavigate) window.pharosNavigate(url);
  else { history.pushState(null, "", url); window.dispatchEvent(new Event("pharos:route")); }
}
function readRoute(): Route {
  const params = new URLSearchParams(location.search);
  const tab = params.get("tab");
  const list = params.get("list");
  return {
    finding: params.get("finding") ?? "",
    tab: tab === "evidence" || tab === "prompt" ? tab : "overview",
    view: params.get("view") === "settings" ? "settings" : "",
    list: list === "watching" || list === "wins" || list === "dismissed" ? list : "open",
    repo: params.get("repo") ?? "",
  };
}
/** Changes the address in place (no new history entry), like the other pages' view toggles. */
function replaceParams(values: Record<string, string>) {
  const url = new URL(location.href);
  for (const [key, value] of Object.entries(values)) value ? url.searchParams.set(key, value) : url.searchParams.delete(key);
  if (url.href !== location.href) { history.replaceState(null, "", url); window.dispatchEvent(new Event("pharos:uri-changed")); }
}
const detailURL = (id: string, tab = "") => `/findings?${new URLSearchParams({ finding: id, ...(tab ? { tab } : {}) })}`;
const listForState = (state: string): List => state === "watching" ? "watching" : state === "won" ? "wins" : state === "dismissed" || state === "snoozed" ? "dismissed" : "open";
const mainScroller = () => document.querySelector("main");

// ---------------------------------------------------------------- words and numbers

const numberWords = ["No", "One", "Two", "Three", "Four", "Five", "Six", "Seven", "Eight", "Nine", "Ten"];
const counted = (n: number, one: string, many = `${one}s`) => `${n.toLocaleString()} ${n === 1 ? one : many}`;
const spelled = (n: number, one: string, many = `${one}s`) => `${numberWords[n] ?? n.toLocaleString()} ${n === 1 ? one : many}`;
// Lowercases a label's first word to use it mid-sentence, keeping names as they are.
const properNouns = /^(Claude|Codex|Conductor|Gemini|Antigravity|TL1|Pharos|GitHub|I)\b/;
const lowerFirst = (text: string) => /^[A-Z]([a-z]|\s)/.test(text) && !properNouns.test(text) ? text[0].toLowerCase() + text.slice(1) : text;
const num = (value: unknown) => { const n = Number(value); return Number.isFinite(n) ? n : 0; };

function money(value: unknown): string {
  const amount = num(value);
  const size = Math.abs(amount);
  if (size >= 1e6) return `$${amount.toLocaleString(undefined, { notation: "compact", maximumFractionDigits: 1 })}`;
  if (size >= 10) return `$${Math.round(amount).toLocaleString()}`;
  if (size >= 0.995) return `$${amount.toFixed(2)}`;
  if (size >= 0.005) return `$${amount.toFixed(2)}`;
  return "$0";
}
/** Rounds to two significant figures for estimates that say "about". */
function aboutMoney(value: unknown): string {
  const amount = num(value);
  if (amount < 10) return money(amount);
  const step = 10 ** (Math.floor(Math.log10(amount)) - 1);
  return `$${(Math.round(amount / step) * step).toLocaleString()}`;
}
const compact = (value: unknown) => num(value).toLocaleString(undefined, { notation: "compact", maximumFractionDigits: 1 });
function minutes(value: unknown): string {
  const m = num(value);
  if (m === 0) return "0 min";
  if (m < 1) return "under 1 min";
  if (m < 60) return `${Math.round(m)} min`;
  const hours = m / 60;
  return `${hours < 10 ? hours.toFixed(1) : Math.round(hours).toLocaleString()} h`;
}
const unitValue: Record<Rank, (value: unknown) => string> = {
  usd: money,
  tokens: value => `${compact(value)} tokens`,
  minutes: value => `${minutes(value)} of agent time`,
  failures: value => counted(Math.round(num(value)), "failure"),
};
const rankLabels: Record<Rank, string> = { usd: "Dollars", tokens: "Tokens", minutes: "Time", failures: "Failures" };

function parseDay(value: unknown): Date | null {
  if (!value) return null;
  const text = String(value);
  const date = /^\d{4}-\d{2}-\d{2}$/.test(text) ? new Date(Number(text.slice(0, 4)), Number(text.slice(5, 7)) - 1, Number(text.slice(8, 10))) : new Date(text);
  return Number.isNaN(date.getTime()) ? null : date;
}
const day = (value: unknown) => parseDay(value)?.toLocaleDateString(undefined, { month: "short", day: "numeric" }) ?? "—";
const fullDay = (value: unknown) => parseDay(value)?.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" }) ?? "—";
function whenText(value: unknown): string {
  const date = parseDay(value);
  if (!date) return "—";
  const time = date.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
  const today = new Date();
  const yesterday = new Date(); yesterday.setDate(today.getDate() - 1);
  if (date.toDateString() === today.toDateString()) return `today at ${time}`;
  if (date.toDateString() === yesterday.toDateString()) return `yesterday at ${time}`;
  return `${day(date.toISOString())} at ${time}`;
}
function seconds(value: unknown): string {
  const ms = num(value);
  if (!ms) return "";
  return ms < 60_000 ? `${Math.max(1, Math.round(ms / 1000))} s` : `${(ms / 60_000).toFixed(1)} min`;
}

/** "1 in 3" style phrases for a share, as the service words its own rates. */
function share(rate: number | null | undefined): string {
  if (rate === null || rate === undefined || !Number.isFinite(rate)) return "—";
  if (rate <= 0) return "none";
  if (rate >= 0.97) return "nearly all";
  if (rate <= 0.5) { const n = Math.round(1 / rate); return n > 100 ? "fewer than 1 in 100" : `1 in ${n}`; }
  let best = { a: 1, b: 2, error: Infinity };
  for (let b = 3; b <= 10; b++) for (let a = 1; a < b; a++) { const error = Math.abs(a / b - rate); if (error < best.error - 1e-9) best = { a, b, error }; }
  return `${best.a} in ${best.b}`;
}
const percent = (value: number) => `${Math.round(value * 100)}%`;

/** Renders `code` spans the detectors write with backticks. */
function Text({ text }: { text: unknown }) {
  const parts = String(text ?? "").split(/`([^`]+)`/);
  return <>{parts.map((part, index) => index % 2 ? <code key={index}>{part}</code> : <React.Fragment key={index}>{part}</React.Fragment>)}</>;
}
const plain = (text: unknown) => String(text ?? "").replace(/`/g, "");

// ---------------------------------------------------------------- small parts

function useDismiss(open: boolean, close: () => void, refs: Array<React.RefObject<HTMLElement>>) {
  useEffect(() => {
    if (!open) return;
    const onPointer = (event: PointerEvent) => { if (!refs.some(ref => ref.current?.contains(event.target as Node))) close(); };
    const onKey = (event: KeyboardEvent) => { if (event.key === "Escape") { event.stopPropagation(); close(); } };
    document.addEventListener("pointerdown", onPointer, true);
    document.addEventListener("keydown", onKey, true);
    return () => { document.removeEventListener("pointerdown", onPointer, true); document.removeEventListener("keydown", onKey, true); };
  }, [open, close]);
}

type MenuItem = { label: React.ReactNode; hint?: string; onSelect: () => void; key: string; danger?: boolean };
/** A button that opens a menu: arrow keys move, Escape and clicking away close it and return focus. */
function Menu({ label, items, className = "", heading, align = "start", ariaLabel, filter, disabled }: { label: React.ReactNode; items: MenuItem[]; className?: string; heading?: string; align?: "start" | "end"; ariaLabel?: string; filter?: boolean; disabled?: boolean }) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const button = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const close = useCallback(() => { setOpen(false); setQuery(""); button.current?.focus(); }, []);
  useDismiss(open, close, [button, panel]);
  const shown = filter && query ? items.filter(item => plain(typeof item.label === "string" ? item.label : item.key).toLowerCase().includes(query.toLowerCase())) : items;
  useEffect(() => {
    if (!open) return;
    const first = panel.current?.querySelector<HTMLElement>(filter ? "input" : "[role=menuitem]");
    first?.focus();
  }, [open]);
  const onKey = (event: React.KeyboardEvent) => {
    const entries = [...(panel.current?.querySelectorAll<HTMLElement>("[role=menuitem]") ?? [])];
    const index = entries.indexOf(document.activeElement as HTMLElement);
    if (event.key === "ArrowDown") { entries[(index + 1) % entries.length]?.focus(); event.preventDefault(); }
    if (event.key === "ArrowUp") { entries[(index - 1 + entries.length) % entries.length]?.focus(); event.preventDefault(); }
    if (event.key === "Tab") setOpen(false);
  };
  return <span className={`findings-menu ${className}`}>
    <button ref={button} type="button" className="findings-button" aria-haspopup="menu" aria-expanded={open} aria-label={ariaLabel} disabled={disabled} onClick={() => setOpen(value => !value)}>{label}</button>
    {open ? <div ref={panel} className={`findings-menu-panel ${align}`} role="menu" aria-label={ariaLabel ?? heading} onKeyDown={onKey}>
      {heading ? <div className="findings-menu-heading">{heading}</div> : null}
      {filter ? <input className="findings-menu-filter" type="search" placeholder="Filter…" aria-label="Filter the list" value={query} onChange={event => setQuery(event.target.value)} onKeyDown={event => { if (event.key === "ArrowDown") { panel.current?.querySelector<HTMLElement>("[role=menuitem]")?.focus(); event.preventDefault(); } }} /> : null}
      <div className="findings-menu-items">
        {shown.map(item => <button key={item.key} type="button" role="menuitem" className={item.danger ? "danger" : ""} onClick={() => { setOpen(false); setQuery(""); item.onSelect(); }}>
          <span>{item.label}</span>{item.hint ? <small>{item.hint}</small> : null}
        </button>)}
        {!shown.length ? <p className="findings-menu-empty">Nothing matches.</p> : null}
      </div>
    </div> : null}
  </span>;
}

/** A modal dialog: focus moves in, Tab stays inside, Escape closes, and focus returns to what opened it. */
function Dialog({ title, onClose, children, footer, wide }: { title: string; onClose: () => void; children: React.ReactNode; footer?: React.ReactNode; wide?: boolean }) {
  const box = useRef<HTMLDivElement>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null;
    box.current?.querySelector<HTMLElement>("h2")?.focus();
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") { event.preventDefault(); closeRef.current(); return; }
      if (event.key !== "Tab" || !box.current) return;
      const focusable = [...box.current.querySelectorAll<HTMLElement>("button:not(:disabled), input:not(:disabled), textarea, select, [tabindex='0'], h2[tabindex]")];
      if (!focusable.length) return;
      const first = focusable[0], last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) { last.focus(); event.preventDefault(); }
      else if (!event.shiftKey && document.activeElement === last) { first.focus(); event.preventDefault(); }
    };
    document.addEventListener("keydown", onKey);
    return () => { document.removeEventListener("keydown", onKey); opener?.focus?.(); };
  }, []);
  return <div className="findings-backdrop" onPointerDown={event => { if (event.target === event.currentTarget) onClose(); }}>
    <div ref={box} className={`findings-dialog ${wide ? "wide" : ""}`} role="dialog" aria-modal="true" aria-labelledby="findingsDialogTitle">
      <div className="findings-dialog-body">
        <h2 id="findingsDialogTitle" tabIndex={-1}>{title}</h2>
        {children}
      </div>
      {footer ? <div className="findings-dialog-footer">{footer}</div> : null}
    </div>
  </div>;
}

function Tile({ label, value, detail, onClick, pressed }: { label: string; value: React.ReactNode; detail?: React.ReactNode; onClick?: () => void; pressed?: boolean }) {
  const body = <><span className="findings-tile-label">{label}</span><strong>{value}</strong>{detail ? <small>{detail}</small> : null}</>;
  return onClick ? <button type="button" className="findings-tile" aria-pressed={pressed} onClick={onClick}>{body}</button> : <div className="findings-tile">{body}</div>;
}

// ---------------------------------------------------------------- cart actions on a card

function AddToPrompt({ card, targets, act, compact: small }: { card: Card; targets: Target[]; act: Act; compact?: boolean }) {
  if (card.cart) return <span className="findings-in-cart">
    <span className="findings-in-cart-label"><Icon name="check" />In prompt for {card.cart.target_label}</span>
    <button type="button" className="findings-button quiet" onClick={() => void act(card.id, { action: "cart_remove" }, `Removed from the prompt for ${card.cart?.target_label}.`)}>Remove from prompt</button>
  </span>;
  const suggested = targets.find(item => item.target === card.target) ?? { target: card.target, label: card.target_label };
  const others = targets.filter(item => item.target !== suggested.target);
  const items: MenuItem[] = [suggested, ...others].map((item, index) => ({
    key: item.target, label: item.label, hint: index === 0 ? "suggested" : undefined,
    onSelect: () => void act(card.id, { action: "cart_add", target: item.target }, `Added to the prompt for ${item.label}.`),
  }));
  return <span className="findings-split">
    <button type="button" className="findings-button primary" onClick={() => void act(card.id, { action: "cart_add" }, `Added to the prompt for ${card.target_label}.`)}>
      {small ? "Add to prompt" : <>Add to prompt for <span className="findings-split-target">{card.target_label}</span></>}
    </button>
    <Menu className="findings-split-more" label={<Icon name="chevron-down" />} ariaLabel="Choose where this change goes" heading="Add to prompt for" items={items} filter={items.length > 10} />
  </span>;
}

function SnoozeDismiss({ card, act }: { card: Card; act: Act }) {
  return <>
    <Menu label={<>Snooze <Icon name="chevron-down" className="findings-caret" /></>} ariaLabel="Snooze" heading="Hide this for" items={[
      { key: "7d", label: "7 days", onSelect: () => void act(card.id, { action: "snooze", until: "7d" }, "Snoozed for 7 days.") },
      { key: "30d", label: "30 days", onSelect: () => void act(card.id, { action: "snooze", until: "30d" }, "Snoozed for 30 days.") },
      { key: "worse", label: "Until it gets worse", hint: "comes back if it happens twice as often", onSelect: () => void act(card.id, { action: "snooze", until: "until_worse" }, "Snoozed until it gets worse.") },
    ]} />
    <Menu label={<>Dismiss <Icon name="chevron-down" className="findings-caret" /></>} ariaLabel="Dismiss" heading="Why dismiss it?" items={[
      { key: "not_real", label: "Not real", hint: "Pharos misread what happened", onSelect: () => void act(card.id, { action: "dismiss", reason: "not_real" }, "Dismissed as not real.") },
      { key: "not_worth_it", label: "Not worth it", onSelect: () => void act(card.id, { action: "dismiss", reason: "not_worth_it" }, "Dismissed as not worth it.") },
      { key: "wont_fix", label: "Won't fix", onSelect: () => void act(card.id, { action: "dismiss", reason: "wont_fix" }, "Dismissed: won't fix.") },
    ]} />
  </>;
}

const dismissReasons: Record<string, string> = { not_real: "not real", not_worth_it: "not worth it", wont_fix: "won't fix" };

function stateChip(card: Card): { text: string; tone: string } {
  switch (card.state) {
    case "watching": {
      const watching = card.watching ?? {};
      return { text: watching.day && watching.of_days ? `Measuring · day ${watching.day} of about ${watching.of_days}` : "Measuring", tone: "watching" };
    }
    case "won": return { text: "Improved", tone: "good" };
    case "dismissed": return { text: `Dismissed${card.dismissed?.reason ? ` · ${dismissReasons[card.dismissed.reason] ?? ""}` : ""}`, tone: "muted" };
    case "snoozed": return { text: card.snoozed?.until_worse ? "Snoozed until it gets worse" : `Snoozed until ${day(card.snoozed?.until)}`, tone: "muted" };
    default:
      if (card.undo) return { text: "Got worse · undo", tone: "bad" };
      if (card.regressed) return { text: "Came back", tone: "warn" };
      if (card.reopened) return { text: "Needs a stronger change", tone: "warn" };
      if (card.inconclusive) return { text: "No clear result", tone: "muted" };
      return { text: "Open", tone: "open" };
  }
}

/** The one note a card needs about its history, as a sentence. */
function StateNote({ card }: { card: Card }) {
  if (card.undo) return <p className="findings-note bad"><Icon name="severity-high" /><span>This got worse after {lowerFirst(card.undo.change ?? "the last change")}{card.undo.copied_at ? ` (copied ${day(card.undo.copied_at)})` : ""}. {card.undo.sentence ? <Text text={card.undo.sentence} /> : null} The prompt asks the agent to undo it.</span></p>;
  if (card.regressed) return <p className="findings-note warn"><Icon name="refresh" /><span>Came back after {lowerFirst(card.regressed.change ?? "the change")}{card.regressed.at || card.regressed.regressed_at ? ` on ${day(card.regressed.at ?? card.regressed.regressed_at)}` : ""}. {card.regressed.sentence ? <Text text={card.regressed.sentence} /> : null} Its savings so far still count. Next: {lowerFirst(card.change_label)}.</span></p>;
  if (card.reopened) return <p className="findings-note warn"><Icon name="refresh" /><span>The last change ({lowerFirst(card.reopened.change ?? "")}) didn't make this happen less often. {card.reopened.sentence ? <Text text={card.reopened.sentence} /> : null} Next: {lowerFirst(card.change_label)}.</span></p>;
  if (card.inconclusive) return <p className="findings-note"><Icon name="info" /><span>{card.inconclusive.sentence ? <Text text={card.inconclusive.sentence} /> : `There wasn't enough of this work after ${lowerFirst(card.inconclusive.change ?? "the change")} to tell whether it helped.`}</span></p>;
  if (card.state === "watching" && card.watching) {
    const watching = card.watching;
    return <p className="findings-note watching"><span className="findings-pulse" aria-hidden="true" /><span>Copied {day(watching.copied_at)} in the prompt for {watching.target_label ?? card.target_label}. Before: {watching.before}.{watching.result_on ? ` First result on ${day(watching.result_on)}.` : ""}{num(watching.saved_usd) > 0 ? ` Saved so far: ${money(watching.saved_usd)}.` : ""}</span></p>;
  }
  if (card.state === "won" && card.result) return <p className="findings-note good"><Icon name="check" /><span><Text text={card.result.sentence} /></span></p>;
  if (card.state === "dismissed") return <p className="findings-note"><span>Dismissed {day(card.dismissed?.at)}. It stays hidden until you restore it.</span></p>;
  if (card.state === "snoozed") return <p className="findings-note"><span>{card.snoozed?.until_worse ? "It comes back if it happens twice as often as when you snoozed it." : `It comes back on ${fullDay(card.snoozed?.until)}, or sooner if you restore it.`}</span></p>;
  return null;
}

function ImpactLine({ card, rank }: { card: Card; rank: Rank }) {
  const impact = card.impact ?? { usd: 0, tokens: 0, minutes: 0, failures: 0 };
  const note = card.impact_note && !(rank === "tokens" && /token/i.test(card.impact_note)) ? card.impact_note : "";
  // Units a pattern has no measure of (no agent time, no failures) are left out.
  const others = (["usd", "tokens", "minutes", "failures"] as Rank[]).filter(unit => unit !== rank && num(impact[unit]) > 0);
  const brief: Record<Rank, (value: unknown) => string> = { usd: money, tokens: value => `${compact(value)} tokens`, minutes: value => `${minutes(value)}`, failures: value => counted(Math.round(num(value)), "failure") };
  return <div className="findings-impact">
    <span className="findings-impact-lead"><strong>{unitValue[rank](impact[rank])}</strong> a month{note ? <> · <Text text={note} /></> : null}</span>
    <span className="findings-impact-rest" title="Every finding's impact in all four units, per month">{others.map(unit => brief[unit](impact[unit])).join(" · ")}</span>
  </div>;
}

function FindingCard({ card, rank, targets, act, isNew }: { card: Card; rank: Rank; targets: Target[]; act: Act; isNew: boolean }) {
  const chip = stateChip(card);
  const open = card.state === "open";
  return <article className={`findings-card ${card.cart ? "in-cart" : ""} ${card.state}`} aria-labelledby={`finding-${card.id}`}>
    <div className="findings-card-meta">
      {isNew ? <span className="findings-chip new">New</span> : null}
      <span className="findings-where">{card.scope_label}</span>
      {chip.tone !== "open" ? <span className={`findings-chip ${chip.tone}`}>{chip.text}</span> : null}
    </div>
    <h3 id={`finding-${card.id}`}><a href={detailURL(card.id)} onClick={event => { if (event.metaKey || event.ctrlKey || event.shiftKey) return; event.preventDefault(); navigate(detailURL(card.id)); }}><Text text={card.title} /></a></h3>
    <p className="findings-explanation"><Text text={card.explanation} /></p>
    <ImpactLine card={card} rank={rank} />
    <p className="findings-change"><span className="findings-change-label">{card.state === "watching" || card.state === "won" ? "Change made" : "Change"}</span> <Text text={card.change} /></p>
    <StateNote card={card} />
    <div className="findings-actions">
      {open ? <AddToPrompt card={card} targets={targets} act={act} /> : null}
      {card.state === "watching" || card.state === "won" ? <button type="button" className="findings-button" onClick={() => navigate(detailURL(card.id))}>See the measurement</button> : null}
      <button type="button" className="findings-button" onClick={() => navigate(detailURL(card.id, "evidence"))}>Evidence</button>
      {open ? <SnoozeDismiss card={card} act={act} /> : null}
      {card.state === "dismissed" || card.state === "snoozed" ? <button type="button" className="findings-button" onClick={() => void act(card.id, { action: "restore" }, "Restored to open findings.")}>Restore</button> : null}
    </div>
  </article>;
}

// ---------------------------------------------------------------- cart

function CartPanel({ cart, targets, act, onReview, highlight }: { cart: CartGroup[]; targets: Target[]; act: Act; onReview: (group: CartGroup) => void; highlight: boolean }) {
  // Tick boxes answer at once; the saved cart replaces these when it reloads.
  const [ticks, setTicks] = useState<Record<string, boolean>>({});
  useEffect(() => setTicks({}), [cart]);
  const ticked = (item: CartItem) => ticks[item.id] ?? item.ticked;
  const tick = (item: CartItem, value: boolean) => {
    setTicks(current => ({ ...current, [item.id]: value }));
    void act(item.id, { action: "cart_tick", ticked: value }).then(ok => { if (!ok) setTicks(current => { const next = { ...current }; delete next[item.id]; return next; }); });
  };
  return <aside className={`findings-cart ${highlight ? "highlight" : ""}`} id="findingsCart" aria-labelledby="findingsCartTitle" tabIndex={-1}>
    <h2 id="findingsCartTitle">Prompts to copy</h2>
    {!cart.length ? <p className="findings-cart-empty">Add findings to a prompt for the repository or Mac where the change goes. Each prompt collects its findings, and copying it hands them to an agent at once.</p> : null}
    {cart.map(group => { const count = group.items.filter(ticked).length; return <section key={group.target} className="findings-cart-group" aria-label={`Prompt for ${group.label}`}>
      <div className="findings-cart-head"><h3>{group.label}</h3><span>{count} of {group.items.length} ticked</span></div>
      <ul>{group.items.map(item => <li key={item.id} className={ticked(item) ? "" : "unticked"}>
        <label>
          <input type="checkbox" checked={ticked(item)} onChange={event => tick(item, event.target.checked)} />
          <span><span className="findings-cart-title"><Text text={item.title} /></span><small>{item.undo ? "Undo the last change" : item.change_label}</small></span>
        </label>
        <Menu className="findings-cart-move" align="end" label={<span aria-hidden="true">⋯</span>} ariaLabel={`Move or remove “${plain(item.title)}”`} heading="Move to the prompt for" filter={targets.length > 10}
          items={[
            ...targets.filter(target => target.target !== group.target).map(target => ({ key: target.target, label: target.label, hint: target.target === item.suggested_target ? "suggested" : undefined, onSelect: () => void act(item.id, { action: "cart_move", target: target.target }, `Moved to the prompt for ${target.label}.`) }))
              .sort((a, b) => Number(Boolean(b.hint)) - Number(Boolean(a.hint))),
            { key: "__remove", label: "Remove from prompt", danger: true, onSelect: () => void act(item.id, { action: "cart_remove" }, "Removed from the prompt.") },
          ]} />
      </li>)}</ul>
      <button type="button" className="findings-button primary" disabled={!count || count !== group.ticked} onClick={() => onReview(group)}>Review prompt ({count} ticked)</button>
    </section>; })}
    <p className="findings-cart-note">Saved in the library, so it's here on any Mac. Only ticked items are copied; the rest wait here.</p>
  </aside>;
}

function ReviewDialog({ group, copy, onClose, onCopied }: { group: CartGroup; copy: Copy; onClose: () => void; onCopied: () => void }) {
  const idsKey = group.items.filter(item => item.ticked).map(item => item.id).join("\n");
  const ids = useMemo(() => idsKey ? idsKey.split("\n") : [], [idsKey]);
  const repository = group.target.startsWith("repo:");
  const [handoff, setHandoff] = useState<Handoff>(group.handoff);
  const [prompt, setPrompt] = useState<Row | null>(null);
  const [error, setError] = useState("");
  const [state, setState] = useState<"" | "copying" | "done">("");
  useEffect(() => {
    let cancelled = false;
    setError("");
    request<Row>("/api/findings/cart/prompt", { target: group.target, ids, ...(repository ? { handoff } : {}) })
      .then(body => { if (!cancelled) setPrompt(body); })
      .catch(failure => { if (!cancelled) setError(failure.message); });
    return () => { cancelled = true; };
  }, [group.target, ids, handoff, repository]);
  const count = ids.length;
  const global = group.label.match(/· (\S+) on (.+)$/);
  const where = repository ? `an agent working in ${group.label}`
    : group.target.startsWith("global:") ? (global ? `an agent on ${global[2]}; it changes the instructions every ${global[1]} session there reads` : "an agent on that Mac; it changes the instructions every session there reads")
      : `an agent working in the repository that defines ${group.label}`;
  async function start() {
    if (!prompt?.prompt) return;
    setState("copying"); setError("");
    try { await copy(prompt.prompt); } catch {
      setState(""); setError("Pharos couldn't write to the clipboard, so nothing started. Select the text, copy it, and try again."); return;
    }
    try {
      await request("/api/findings/cart/copy", { target: group.target, ids, handoff: repository ? handoff : prompt.handoff });
      setState("done");
      window.setTimeout(onCopied, 1400);
    } catch (failure) {
      setState(""); setError(`The prompt is on your clipboard, but Pharos couldn't start measuring: ${(failure as Error).message}. Try again before pasting it.`);
    }
  }
  return <Dialog title={`Prompt for ${group.label}`} onClose={onClose} wide footer={<>
    <span className="findings-dialog-fine">Measured separately: each finding on its own rate.</span>
    <span className="findings-dialog-buttons">
      <button type="button" className="findings-button" onClick={onClose}>Keep in cart</button>
      <button type="button" className="findings-button primary" disabled={!prompt?.prompt || state !== ""} onClick={() => void start()}>
        {state === "done" ? <><Icon name="check" />Copied · measuring</> : state === "copying" ? "Copying…" : "Copy prompt and start measuring"}
      </button>
    </span>
  </>}>
    <p className="findings-dialog-lede">{spelled(count, "finding")}. Paste this into {where}. Copying it starts the before-and-after measurement for {count === 1 ? "it" : count === 2 ? "both" : `all ${count}`}.</p>
    {repository ? <fieldset className="findings-handoff">
      <legend>Ask the agent to</legend>
      <div>
        <label><input type="radio" name="findingsHandoff" checked={handoff === "pr"} onChange={() => setHandoff("pr")} /> Open a pull request</label>
        <label><input type="radio" name="findingsHandoff" checked={handoff === "diff"} onChange={() => setHandoff("diff")} /> Show me the diff</label>
      </div>
      <p>{group.handoff_reason} <a href="/findings?view=settings" onClick={event => { event.preventDefault(); onClose(); navigate("/findings?view=settings"); }}>Change the default</a></p>
    </fieldset> : <p className="findings-handoff-note">{group.handoff_reason}</p>}
    {error ? <p className="findings-error" role="alert">{error}</p> : null}
    {state === "done" ? <p className="findings-success" role="status">Copied. Paste it into the agent; Pharos is now measuring {count === 1 ? "this finding" : `these ${count} findings`}.</p> : null}
    <pre className="findings-prompt" tabIndex={0} aria-label="Prompt text">{prompt?.prompt ?? (error ? "" : "Writing the prompt…")}</pre>
  </Dialog>;
}

// ---------------------------------------------------------------- chart

const DAY = 86_400_000;
function niceMax(value: number, kind: string): number {
  if (value <= 0) return kind === "mean" ? 10 : 0.1;
  if (kind !== "mean") {
    for (const step of [0.02, 0.05, 0.1, 0.2, 0.25, 0.4, 0.5, 0.6, 0.8, 1]) if (value <= step) return step;
    return Math.ceil(value);
  }
  const power = 10 ** Math.floor(Math.log10(value));
  for (const factor of [1, 2, 2.5, 4, 5, 10]) if (value <= factor * power) return factor * power;
  return 10 * power;
}

function WeeklyChart({ chart, unit, copiedLabel }: { chart: NonNullable<Detail["chart"]>; unit: string; copiedLabel: string }) {
  const wrap = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(680);
  const [active, setActive] = useState<number | null>(null);
  useLayoutEffect(() => {
    if (!wrap.current) return;
    setWidth(wrap.current.clientWidth || 680);
    const observer = new ResizeObserver(entries => setWidth(Math.max(320, entries[0].contentRect.width)));
    observer.observe(wrap.current);
    return () => observer.disconnect();
  }, []);
  const weeks = chart.weeks ?? [];
  if (!weeks.length) return <p className="findings-muted">No weekly numbers yet.</p>;
  const kind = chart.kind;
  const format = (value: number) => kind === "mean" ? compact(value) : percent(value);
  const height = 236, margin = { top: 30, right: 18, bottom: 30, left: kind === "mean" ? 46 : 42 };
  const start = parseDay(weeks[0].start)!.getTime();
  const end = parseDay(weeks[weeks.length - 1].start)!.getTime() + 7 * DAY;
  const plotWidth = width - margin.left - margin.right, plotHeight = height - margin.top - margin.bottom;
  const x = (time: number) => margin.left + ((time - start) / (end - start)) * plotWidth;
  const values = weeks.map(week => week.rate).filter((value): value is number => typeof value === "number" && Number.isFinite(value));
  const top = niceMax(Math.max(...values, chart.baseline || 0) * 1.08, kind);
  const y = (value: number) => margin.top + (1 - value / top) * plotHeight;
  const ticks = [0, 0.25, 0.5, 0.75, 1].map(part => part * top);
  const points = weeks.map((week, index) => ({ index, week, cx: x(parseDay(week.start)!.getTime() + 3.5 * DAY), cy: typeof week.rate === "number" && Number.isFinite(week.rate) ? y(week.rate) : null }));
  let path = "";
  let pen = false;
  // The line joins weeks with enough work to trust; hollow weeks stand alone so a noisy point doesn't read as a trend.
  for (const point of points) {
    if (point.cy === null) { pen = false; continue; }
    if (point.week.hollow) continue;
    path += `${pen ? "L" : "M"}${point.cx.toFixed(1)},${point.cy.toFixed(1)}`;
    pen = true;
  }
  const copies = (chart.copies ?? []).map(copy => ({ day: copy, at: x(Math.min(end, Math.max(start, parseDay(copy)!.getTime() + DAY / 2))) }));
  const firstCopy = copies.length ? Math.min(...copies.map(copy => copy.at)) : null;
  const labelEvery = Math.max(1, Math.ceil(weeks.length / Math.max(2, Math.floor(plotWidth / 78))));
  const hasBaseline = Number.isFinite(chart.baseline) && chart.baseline > 0;
  const baselineLabel = `${copies.length ? "Before" : "Last 28 days"}: ${chart.baseline_phrase}`;
  const hollow = weeks.some(week => week.hollow);
  const last = [...points].reverse().find(point => point.cy !== null);
  const summary = `${chart.title}, by week, ${fullDay(weeks[0].start)} to ${fullDay(weeks[weeks.length - 1].start)}.${copies.length ? ` Prompt copied ${copies.map(copy => day(copy.day)).join(" and ")}.` : ""}${hasBaseline ? ` ${baselineLabel}.` : ""}${last ? ` Latest week: ${format(last.week.rate as number)}.` : ""}`;
  const pick = (clientX: number) => {
    const box = wrap.current?.getBoundingClientRect();
    if (!box) return;
    const at = clientX - box.left;
    let best = 0;
    points.forEach((point, index) => { if (Math.abs(point.cx - at) < Math.abs(points[best].cx - at)) best = index; });
    setActive(best);
  };
  const shown = active === null ? null : points[active];
  const tip = shown ? weekText(shown.week, kind, unit) : "";
  return <div className="findings-chart" ref={wrap}>
    <svg width={width} height={height} role="img" aria-label={summary} tabIndex={0}
      onPointerMove={event => pick(event.clientX)} onPointerLeave={() => setActive(null)} onBlur={() => setActive(null)}
      onKeyDown={event => {
        if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
        event.preventDefault();
        setActive(current => Math.max(0, Math.min(points.length - 1, (current ?? points.length) + (event.key === "ArrowLeft" ? -1 : 1))));
      }}>
      {firstCopy !== null ? <rect className="findings-chart-after" x={firstCopy} y={margin.top} width={Math.max(0, width - margin.right - firstCopy)} height={plotHeight} /> : null}
      {ticks.map(tick => <g key={tick}>
        <line className="findings-chart-grid" x1={margin.left} x2={width - margin.right} y1={y(tick)} y2={y(tick)} />
        <text className="findings-chart-axis" x={margin.left - 8} y={y(tick)} dy="0.32em" textAnchor="end">{format(tick)}</text>
      </g>)}
      {points.map((point, index) => index % labelEvery === 0 ? <text key={point.week.start} className="findings-chart-axis" x={x(parseDay(point.week.start)!.getTime())} y={height - 10} textAnchor={index === 0 ? "start" : "middle"}>{day(point.week.start)}</text> : null)}
      {hasBaseline ? <line className="findings-chart-baseline" x1={margin.left} x2={firstCopy ?? width - margin.right} y1={y(chart.baseline)} y2={y(chart.baseline)} /> : null}
      {copies.map(copy => {
        const right = copy.at > width - 150;
        return <g key={copy.day}>
          <line className="findings-chart-copy" x1={copy.at} x2={copy.at} y1={margin.top - 14} y2={height - margin.bottom} />
          <text className="findings-chart-label" x={copy.at + (right ? -6 : 6)} y={margin.top - 16} dy="0.9em" textAnchor={right ? "end" : "start"}>{copiedLabel} {day(copy.day)}</text>
        </g>;
      })}
      <path className="findings-chart-line" d={path} />
      {shown ? <line className="findings-chart-crosshair" x1={shown.cx} x2={shown.cx} y1={margin.top} y2={height - margin.bottom} /> : null}
      {points.map(point => point.cy === null ? null : <circle key={point.week.start} className={`findings-chart-point ${point.week.hollow ? "hollow" : ""} ${point.index === active ? "active" : ""}`} cx={point.cx} cy={point.cy} r={point.index === active ? 5.5 : 4.5} />)}
    </svg>
    {shown ? <div className={`findings-chart-tip ${shown.cx > width - 190 ? "left" : ""}`} style={{ left: `${shown.cx}px`, top: `${Math.max(8, (shown.cy ?? margin.top + plotHeight / 2) - 12)}px` }} role="status">
      <strong>{shown.week.rate === null || shown.week.rate === undefined ? "No work" : format(shown.week.rate)}</strong>
      <span>Week of {day(shown.week.start)}</span><span>{tip}</span>
      {shown.week.hollow ? <span className="findings-muted">Little of this work that week</span> : null}
    </div> : null}
    {hasBaseline || hollow ? <p className="findings-chart-legend">
      {hasBaseline ? <span><svg width="22" height="10" aria-hidden="true"><line x1="1" x2="21" y1="5" y2="5" className="findings-chart-baseline" /></svg>{baselineLabel}</span> : null}
      {hollow ? <span><svg width="12" height="12" aria-hidden="true"><circle cx="6" cy="6" r="4" className="findings-chart-point hollow" /></svg>Hollow points: weeks with little of this work</span> : null}
    </p> : null}
  </div>;
}

function weekText(week: Week, kind: string, unit: string): string {
  if (kind === "rate") return `${num(week.events).toLocaleString()} of ${counted(num(week.exposure), unit.replace(/s$/, ""), unit)}`;
  if (kind === "mean") return `${compact(week.events)} tokens over ${counted(num(week.exposure), unit.replace(/s$/, "") || "call", unit || "calls")}`;
  return `${compact(week.events)} of ${compact(week.exposure)} tokens`;
}

function WeeksTable({ chart, unit }: { chart: NonNullable<Detail["chart"]>; unit: string }) {
  const format = (value: number | null | undefined) => value === null || value === undefined || !Number.isFinite(value) ? "—" : chart.kind === "mean" ? compact(value) : percent(value);
  return <details className="findings-weeks">
    <summary>Show the weekly numbers</summary>
    <table><thead><tr><th>Week of</th><th className="num">{chart.kind === "mean" ? "Average" : "Share"}</th><th className="num">{chart.kind === "rate" ? "Hit it" : chart.kind === "mean" ? "Tokens" : "Tokens"}</th><th className="num">{chart.kind === "rate" ? `All ${unit}` : chart.kind === "mean" ? unit || "Calls" : "All tokens"}</th><th /></tr></thead>
      <tbody>{chart.weeks.map(week => <tr key={week.start} className={week.hollow ? "hollow" : ""}>
        <td>{fullDay(week.start)}</td><td className="num">{format(week.rate)}</td>
        <td className="num">{chart.kind === "rate" ? num(week.events).toLocaleString() : compact(week.events)}</td>
        <td className="num">{chart.kind === "share" ? compact(week.exposure) : num(week.exposure).toLocaleString()}</td>
        <td className="findings-muted">{week.after ? "after the copy" : ""}{week.hollow ? `${week.after ? " · " : ""}little work` : ""}</td>
      </tr>)}</tbody>
    </table>
  </details>;
}

// ---------------------------------------------------------------- detail

function sinceSoFar(detail: Detail): string {
  const weeks = (detail.chart?.weeks ?? []).filter(week => week.after);
  const events = weeks.reduce((sum, week) => sum + num(week.events), 0);
  const exposure = weeks.reduce((sum, week) => sum + num(week.exposure), 0);
  if (!exposure) return "";
  const rate = events / exposure;
  return detail.chart?.kind === "mean" ? `${compact(rate)} tokens` : share(rate);
}

function Facts({ rows }: { rows: Array<[string, React.ReactNode] | null | false> }) {
  const shown = rows.filter(Boolean) as Array<[string, React.ReactNode]>;
  if (!shown.length) return null;
  return <dl className="findings-facts">{shown.map(([label, value]) => <React.Fragment key={label}><dt>{label}</dt><dd>{value}</dd></React.Fragment>)}</dl>;
}

function savedText(savings: Row | undefined): React.ReactNode {
  if (!savings) return "—";
  const added = num(savings.added_usd);
  return <>{money(savings.usd)}{added > 0 ? <span className="findings-muted"> (after {money(added)} of added context)</span> : null}</>;
}

const attemptStatus: Record<string, string> = { watching: "measuring", improved: "improved", unchanged: "no change", worse: "got worse", inconclusive: "no clear result", withdrawn: "not applied", not_applied: "not applied" };

function DetailOverview({ detail, onNotApplied }: { detail: Detail; onNotApplied: () => void }) {
  const attempts = detail.attempts ?? [];
  const current = attempts[attempts.length - 1];
  const chart = detail.chart;
  const unit = String(detail.metric?.unit || "conversations");
  const since = sinceSoFar(detail);
  const [confirming, setConfirming] = useState(false);
  let caption: React.ReactNode = null;
  if (detail.state === "watching") {
    const before = detail.watching?.before ?? chart?.baseline_phrase;
    caption = <>Before the change, about {before}. {since ? <>Since then, {since} so far. </> : "Nothing to compare yet. "}The result comes once there has been about as much of this work as before{detail.watching?.result_on ? `, around ${day(detail.watching.result_on)}` : ""}.</>;
  } else if (current?.result?.sentence) caption = <Text text={current.result.sentence} />;
  else if (detail.result?.sentence) caption = <Text text={detail.result.sentence} />;
  else if (chart?.baseline_phrase && chart.baseline_phrase !== "none") caption = <>Over the last 28 days: {chart.baseline_phrase}. After you copy a prompt with this finding, its line continues here so you can see whether the change worked.</>;
  const harness = attempts.map(attempt => attempt.result?.harness_note).filter(Boolean);
  const alongside = attempts.flatMap(attempt => attempt.result?.alongside ?? attempt.result?.others ?? []).filter(Boolean);
  const guards: Row[] = current?.result?.guards ?? [];
  const nextIndex = detail.state === "watching" || detail.state === "won" ? num(detail.attempt || attempts.length) : num(detail.attempt || 1) - 1;
  const next = detail.steps?.[nextIndex + (detail.state === "watching" || detail.state === "won" ? 0 : 1)];
  const facts: Array<[string, React.ReactNode] | null | false> = detail.state === "watching" || detail.state === "won" || current ? [
    current ? ["Copied", <>{fullDay(current.copied_at)}, in the prompt for {current.target_label ?? detail.target_label}</>] : null,
    ["Before", <>{detail.watching?.before ?? (current?.result?.before?.rate !== undefined ? share(current.result.before.rate) : current?.plan?.before_rate !== undefined ? share(current.plan.before_rate) : chart?.baseline_phrase)} {chart?.kind === "rate" ? unit : ""}</>],
    detail.state === "watching" ? ["Since", since ? `${since} so far` : "nothing yet"] : current?.result?.after?.rate !== undefined ? ["After", share(current.result.after.rate)] : null,
    current?.savings ? ["Saved so far", savedText(current.savings)] : detail.watching?.saved_usd !== undefined ? ["Saved so far", money(detail.watching.saved_usd)] : null,
    detail.state === "watching" && detail.watching?.result_on ? ["Result", `around ${fullDay(detail.watching.result_on)}`] : current?.result && detail.result?.decided_at ? ["Result", fullDay(detail.result.decided_at)] : null,
    ...guards.map((guard): [string, React.ReactNode] => [`Also checked`, <>{guard.name}: {compact(guard.before)} → {compact(guard.after)}{guard.ok === false ? " (got worse)" : ""}</>]),
    ...harness.map((note, index): [string, React.ReactNode] => [index ? "Note " : "Note", <Text text={note} />]),
    alongside.length ? ["Copied alongside", alongside.map((item: Row | string) => typeof item === "string" ? item : item.title).join("; ")] : null,
  ] : [
    ["How often", <>{detail.rate ?? chart?.baseline_phrase}</>],
    ["Impact", <>{[money(detail.impact?.usd), `${compact(detail.impact?.tokens)} tokens`, num(detail.impact?.minutes) > 0 ? minutes(detail.impact?.minutes) : "", num(detail.impact?.failures) > 0 ? counted(Math.round(num(detail.impact?.failures)), "failure") : ""].filter(Boolean).join(" · ")} a month</>],
    detail.first_seen_at ? ["Found", fullDay(detail.first_seen_at)] : null,
    detail.last_seen ? ["Last seen", fullDay(detail.last_seen)] : null,
    ["Change", detail.change_label],
  ];
  return <div className="findings-detail-grid">
    <section className="findings-panel findings-chart-panel" aria-labelledby="findingsChartTitle">
      {chart ? <>
        <div className="findings-panel-head"><h2 id="findingsChartTitle"><Text text={chart.title} />, by week</h2></div>
        <WeeklyChart chart={chart} unit={unit} copiedLabel="Prompt copied" />
        {caption ? <p className="findings-caption">{caption}</p> : null}
        <WeeksTable chart={chart} unit={unit} />
      </> : <p className="findings-muted">No weekly numbers yet.</p>}
    </section>
    <div className="findings-side">
      <section className="findings-panel" aria-label="Facts"><Facts rows={facts} />
        {detail.state === "watching" ? <div className="findings-not-applied">
          {confirming ? <>
            <p>This stops the measurement and moves the finding back to open, as if the prompt was never copied.</p>
            <div className="findings-actions"><button type="button" className="findings-button" onClick={() => { setConfirming(false); onNotApplied(); }}>Yes, I didn't apply it</button><button type="button" className="findings-button quiet" onClick={() => setConfirming(false)}>Cancel</button></div>
          </> : <button type="button" className="findings-button quiet" onClick={() => setConfirming(true)}>I didn't apply this</button>}
        </div> : null}
      </section>
      <section className="findings-panel" aria-labelledby="findingsAttemptsTitle">
        <h2 id="findingsAttemptsTitle">Attempts</h2>
        {attempts.length ? <ol className="findings-attempts">{attempts.map(attempt => <li key={attempt.attempt} className={attempt.status}>
          <span className="findings-attempt-mark" aria-hidden="true" />
          <div><strong><Text text={attempt.change} /></strong>
            <span className="findings-muted">{fullDay(attempt.copied_at)} · {attemptStatus[attempt.status] ?? attempt.status}{attempt.regressed_at ? ` · came back ${day(attempt.regressed_at)}` : ""}</span>
            {attempt.result?.sentence ? <p><Text text={attempt.result.sentence} /></p> : null}
            {attempt.savings && num(attempt.savings.usd) > 0 ? <p className="findings-muted">Saved {money(attempt.savings.usd)}{num(attempt.savings.added_usd) > 0 ? ` after ${money(attempt.savings.added_usd)} of added context` : ""}.</p> : null}
          </div>
        </li>)}</ol> : <p className="findings-muted">Nothing tried yet. Copying a prompt with this finding starts the first attempt.</p>}
        {next && detail.state !== "dismissed" && detail.state !== "snoozed" ? <p className="findings-next">{detail.state === "watching" ? "If nothing changes, Pharos will suggest" : detail.state === "won" ? "If it comes back, Pharos will suggest" : "After this, Pharos would suggest"}: <Text text={lowerFirst(next.label)} />.</p> : null}
      </section>
    </div>
  </div>;
}

function EvidenceList({ rows }: { rows: Row[] }) {
  if (!rows.length) return <p className="findings-muted">No examples are kept for this finding.</p>;
  const open = (row: Row) => {
    if (!row.workspace_id) return;
    const params = new URLSearchParams();
    if (row.conversation_id) params.set("conversation", row.conversation_id);
    if (row.message_id) params.set("message", row.message_id);
    navigate(`/work/${encodeURIComponent(row.workspace_id)}${params.size ? `?${params}` : ""}`);
  };
  return <>
    <p className="findings-muted findings-evidence-lede">The newest examples Pharos found. Select one to open its conversation.</p>
    <ol className="findings-evidence">{rows.map((row, index) => {
      const body = <><time>{day(row.at)}</time><span className="findings-evidence-where">{row.where}</span><span className="findings-evidence-did"><Text text={row.did} /></span>{row.happened ? <span className="findings-evidence-happened"><Text text={row.happened} /></span> : null}</>;
      return <li key={index}>{row.workspace_id ? <button type="button" onClick={() => open(row)} title="Open this conversation">{body}<Icon name="arrow-right" className="findings-evidence-go" /></button> : <div>{body}</div>}</li>;
    })}</ol>
  </>;
}

function FindingDetail({ id, tab, overview, act, rank, onReview, newIds }: { id: string; tab: Route["tab"]; overview: Overview | null; act: Act; rank: Rank; onReview: (group: CartGroup) => void; newIds: Set<string> }) {
  const [detail, setDetail] = useState<Detail | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(() => request<Detail>(findingURL(id)).then(body => { setDetail(body); setError(""); window.pharosNameVisit?.(plain(body.title)); }).catch(failure => setError(failure.message)), [id]);
  useEffect(() => { setDetail(null); void load(); }, [load]);
  // Actions reload the overview; the detail follows it.
  useEffect(() => { if (overview && detail) void load(); }, [overview]);
  const back = () => navigate(`/findings${detail ? (listForState(detail.state) === "open" ? "" : `?list=${listForState(detail.state)}`) : ""}`);
  if (error) return <div className="findings-page"><button type="button" className="findings-crumb" onClick={() => navigate("/findings")}>Findings</button><p className="findings-error" role="alert">{error}</p></div>;
  if (!detail) return <div className="findings-page"><p className="findings-muted">Loading the finding…</p></div>;
  const chip = stateChip(detail);
  const stateName = { open: "Open", watching: "Watching", won: "Wins", dismissed: "Dismissed", snoozed: "Snoozed" }[detail.state] ?? "Open";
  const evidence = detail.evidence ?? [];
  const group = detail.cart ? overview?.cart.find(item => item.target === detail.cart?.target) : undefined;
  const tabs: Array<[Route["tab"], string]> = [["overview", "Overview"], ["evidence", `Evidence · ${evidence.length}`], ["prompt", "Prompt"]];
  const onTab = (next: Route["tab"]) => replaceParams({ tab: next === "overview" ? "" : next });
  return <div className="findings-page findings-detail">
    <nav className="findings-breadcrumb" aria-label="Breadcrumb"><button type="button" className="findings-crumb" onClick={() => navigate("/findings")}>Findings</button><span aria-hidden="true">›</span><button type="button" className="findings-crumb" onClick={back}>{stateName}</button></nav>
    <h1><Text text={detail.title} /></h1>
    <div className="findings-detail-chips">
      <span className="findings-chip">{detail.scope_label}</span>
      <span className={`findings-chip ${chip.tone}`}>{chip.tone === "watching" ? <span className="findings-pulse" aria-hidden="true" /> : null}{chip.text}</span>
      {newIds.has(detail.id) ? <span className="findings-chip new">New</span> : null}
    </div>
    <p className="findings-detail-explanation"><Text text={detail.explanation} /></p>
    <ImpactLine card={detail} rank={rank} />
    <p className="findings-change"><span className="findings-change-label">{detail.state === "watching" || detail.state === "won" ? "Change made" : "Change"}</span> <Text text={detail.change} /></p>
    {detail.state !== "watching" ? <StateNote card={detail} /> : null}
    <div className="findings-actions">
      {detail.state === "open" ? <><AddToPrompt card={detail} targets={overview?.targets ?? []} act={act} />{group ? <button type="button" className="findings-button" onClick={() => onReview(group)} disabled={!group.ticked}>Review that prompt</button> : null}<SnoozeDismiss card={detail} act={act} /></> : null}
      {detail.state === "dismissed" || detail.state === "snoozed" ? <button type="button" className="findings-button" onClick={() => void act(detail.id, { action: "restore" }, "Restored to open findings.")}>Restore</button> : null}
    </div>
    <div className="findings-tabs detail" role="tablist" aria-label="Finding sections">
      {tabs.map(([key, label]) => <button key={key} type="button" role="tab" id={`findings-tab-${key}`} aria-controls="findingsTabPanel" aria-selected={tab === key} tabIndex={tab === key ? 0 : -1} onClick={() => onTab(key)}
        onKeyDown={event => { if (event.key !== "ArrowRight" && event.key !== "ArrowLeft") return; const index = tabs.findIndex(([name]) => name === tab); const next = tabs[(index + (event.key === "ArrowRight" ? 1 : tabs.length - 1)) % tabs.length][0]; onTab(next); window.setTimeout(() => document.getElementById(`findings-tab-${next}`)?.focus()); }}>{label}</button>)}
    </div>
    <div id="findingsTabPanel" role="tabpanel" aria-labelledby={`findings-tab-${tab}`}>
      {tab === "overview" ? <DetailOverview detail={detail} onNotApplied={() => void act(detail.id, { action: "not_applied" }, "Measurement withdrawn; the finding is open again.")} /> : null}
      {tab === "evidence" ? <EvidenceList rows={evidence} /> : null}
      {tab === "prompt" ? <div className="findings-prompt-tab">
        <p className="findings-muted">What the prompt says about this finding. Copying happens from the prompt for {detail.cart?.target_label ?? detail.target_label}, where it can go together with other findings for the same place.</p>
        <pre className="findings-prompt" tabIndex={0} aria-label="Prompt text">{detail.prompt ?? ""}</pre>
        {detail.state === "open" ? <div className="findings-actions"><AddToPrompt card={detail} targets={overview?.targets ?? []} act={act} />{group ? <button type="button" className="findings-button" onClick={() => onReview(group)} disabled={!group.ticked}>Review that prompt</button> : null}</div> : null}
      </div> : null}
    </div>
  </div>;
}

// ---------------------------------------------------------------- wins

/** On the Wins tab, the savings take the place of the summary tiles. */
function WinsHero({ overview }: { overview: Overview }) {
  const saved = overview.summary.saved ?? {};
  return <div className="findings-wins-hero">
    <div className="findings-hero"><span>Saved so far (estimate)</span><strong>{money(saved.usd)}</strong>{num(saved.added_usd) > 0 ? <small>after {money(saved.added_usd)} of added context</small> : null}</div>
    <Tile label="Tokens" value={compact(saved.tokens)} detail={num(saved.added_tokens) > 0 ? `after ${compact(saved.added_tokens)} of added context` : "saved"} />
    <Tile label="Agent time" value={minutes(saved.minutes)} detail="saved" />
    <Tile label="Failures avoided" value={Math.round(num(saved.failures)).toLocaleString()} detail={`across ${counted(overview.wins?.length ?? 0, "win")}`} />
  </div>;
}

function WinsView({ overview }: { overview: Overview }) {
  const wins = overview.wins ?? [];
  return <div className="findings-wins">
    <p className="findings-wins-method">Each fix is credited with the decline beyond the half an untouched pattern fades to anyway, times the work done since, for 90 days, less the context the change added to every request.</p>
    {wins.length ? <div className="findings-table-wrap"><table className="findings-table">
      <thead><tr><th>What was fixed</th><th>Where</th><th>Change</th><th className="num">How often, before → now</th><th className="num">Saved</th><th>Status</th></tr></thead>
      <tbody>{wins.map(win => {
        const status = win.regressed_at ? { text: "Came back · reopened", tone: "warn" } : num(win.days_left) > 0 ? { text: `Counting savings for ${counted(num(win.days_left), "more day")}`, tone: "good" } : { text: "Done", tone: "good" };
        return <tr key={win.id}>
          <td><a href={detailURL(win.id)} onClick={event => { if (event.metaKey || event.ctrlKey) return; event.preventDefault(); navigate(detailURL(win.id)); }}><Text text={win.title} /></a></td>
          <td>{win.where}</td><td>{win.change}</td>
          <td className="num">{win.before} → {win.now}</td>
          <td className="num">{money(win.saved?.usd)}{num(win.saved?.added_usd) > 0 ? <small className="findings-muted"> after {money(win.saved.added_usd)} of added context</small> : null}</td>
          <td><span className={`findings-chip ${status.tone}`}>{status.tone === "warn" ? <Icon name="refresh" /> : <Icon name="check" />}{status.text}</span></td>
        </tr>;
      })}</tbody>
    </table></div> : <div className="findings-empty"><strong>No wins yet.</strong><p>When a copied change makes its pattern happen less often, it lands here with an estimate of what it saved.</p></div>}
  </div>;
}

// ---------------------------------------------------------------- settings

function FindingsSettings({ overview, reload }: { overview: Overview | null; reload: () => Promise<unknown> }) {
  const [error, setError] = useState("");
  const [pending, setPending] = useState<number | null>(null);
  const [showAll, setShowAll] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const timer = useRef<number>();
  useEffect(() => () => window.clearTimeout(timer.current), []);
  const running = Boolean(overview?.status?.running) || refreshing;
  useEffect(() => {
    if (!overview?.status?.running) return;
    const poll = window.setInterval(() => void reload(), 2000);
    return () => window.clearInterval(poll);
  }, [overview?.status?.running, reload]);
  if (!overview) return <div className="findings-page"><p className="findings-muted">Loading…</p></div>;
  const save = async (body: Row) => {
    try { await request("/api/findings/settings", body); setError(""); await reload(); } catch (failure) { setError((failure as Error).message); }
  };
  const checkpoints = [...(overview.checkpoints ?? [])].sort((a, b) => a.threshold - b.threshold);
  const following = !overview.settings?.threshold;
  const effective = pending ?? overview.threshold;
  const nearest = checkpoints.reduce((best, point, index) => Math.abs(point.threshold - effective) < Math.abs(checkpoints[best].threshold - effective) ? index : best, 0);
  const selected = checkpoints[nearest];
  const recommendedIndex = checkpoints.findIndex(point => point.threshold === overview.recommended);
  const choose = (index: number) => {
    const threshold = checkpoints[index]?.threshold;
    if (!threshold) return;
    setPending(threshold);
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => void save({ threshold }).then(() => setPending(null)), 350);
  };
  const handoff = overview.settings?.handoff ?? "auto";
  const repositories = [...(overview.repositories ?? [])].sort((a, b) => b.work - a.work);
  const shownRepositories = showAll ? repositories : repositories.slice(0, 8);
  const status = overview.status ?? ({} as Status);
  async function refresh() {
    setRefreshing(true);
    try { await request("/api/findings/refresh", {}); await reload(); } catch (failure) { setError((failure as Error).message); }
    finally { setRefreshing(false); }
  }
  const position = (index: number) => checkpoints.length > 1 ? `${(index / (checkpoints.length - 1)) * 100}%` : "0%";
  return <div className="findings-page findings-settings">
    <nav className="findings-breadcrumb" aria-label="Breadcrumb"><button type="button" className="findings-crumb" onClick={() => navigate("/findings")}>Findings</button><span aria-hidden="true">›</span><span>Settings</span></nav>
    <div className="view-heading"><div><h1>Findings settings</h1><p className="muted">How much evidence a pattern needs before it's shown, how findings are ranked, and what prompts ask agents to do. Saved in the library, so they apply on every Mac.</p></div></div>
    {error ? <p className="findings-error" role="alert">{error}</p> : null}

    <section className="findings-panel findings-setting" aria-labelledby="findingsThresholdTitle">
      <h2 id="findingsThresholdTitle">When a pattern becomes a finding</h2>
      <p>A pattern is shown once it affects this many conversations in 28 days, with at least one in the last 7. Your library does about {Math.round(num(overview.status?.weekly_conversations)).toLocaleString()} conversations a week.</p>
      {checkpoints.length ? <>
        <div className="findings-slider">
          {recommendedIndex >= 0 ? <span className="findings-slider-recommended" style={{ left: position(recommendedIndex) }}>Recommended: {overview.recommended}</span> : null}
          <input type="range" min={0} max={checkpoints.length - 1} step={1} value={nearest} aria-label="Conversations a pattern must affect" aria-valuetext={`${selected.threshold} conversations${selected.threshold === overview.recommended ? " (recommended)" : ""}`} onChange={event => choose(Number(event.target.value))} />
          <div className="findings-slider-ticks" aria-hidden="true">{checkpoints.map((point, index) => <span key={point.threshold} className={index === nearest ? "selected" : ""} style={{ left: position(index) }}>{point.threshold}</span>)}</div>
        </div>
        <label className="findings-check"><input type="checkbox" checked={following && pending === null} onChange={event => { if (event.target.checked) { setPending(null); void save({ threshold: 0 }); } else choose(nearest); }} /> Use the recommendation{following ? "" : ` (${overview.recommended})`}, and follow it as the library grows</label>
        <div className="findings-tiles three">
          <Tile label={`At ${selected.threshold} conversations`} value={selected.shown.toLocaleString()} detail="findings would show today" />
          <Tile label="Typical wait for a result" value={`about ${selected.typical_wait_days} days`} detail="after you copy a prompt" />
          <Tile label="Results expected to be clear" value={share(selected.clear_share) === "nearly all" ? "nearly all" : share(selected.clear_share)} detail="the rest end with no clear answer" />
        </div>
        <p className="findings-muted">A lower number shows more findings, sooner, but more of their results will be inconclusive: with little of the work before and after a change, a real improvement can't be told apart from chance. The recommendation is the lowest number at which a typical finding can still reach a clear result within 30 days at your library's pace.</p>
      </> : <p className="findings-muted">The preview appears after findings are first computed.</p>}
    </section>

    <section className="findings-panel findings-setting" aria-labelledby="findingsRankTitle">
      <h2 id="findingsRankTitle">Rank findings by</h2>
      <p>Every finding shows all four; this decides the order and which number leads. Dollars are what the same tokens would cost at API prices, which on a subscription shows where your quota goes.</p>
      <div className="findings-radios" role="radiogroup" aria-labelledby="findingsRankTitle">
        {(["usd", "tokens", "minutes", "failures"] as Rank[]).map(unit => <label key={unit}><input type="radio" name="findingsRank" checked={(overview.settings?.rank ?? "usd") === unit} onChange={() => void save({ rank: unit })} /> {unit === "minutes" ? "Agent time" : rankLabels[unit]}</label>)}
      </div>
    </section>

    <section className="findings-panel findings-setting" aria-labelledby="findingsHandoffTitle">
      <h2 id="findingsHandoffTitle">What prompts ask the agent to do</h2>
      <p>By default, Pharos follows each repository's habit: a pull request where its work usually ends in one, otherwise the diff for you to review. Changes to instruction files outside a repository always show the diff first.</p>
      <div className="findings-radios" role="radiogroup" aria-labelledby="findingsHandoffTitle">
        {([["auto", "Follow each repository"], ["pr", "Always open a pull request"], ["diff", "Always show the diff"]] as const).map(([value, label]) => <label key={value}><input type="radio" name="findingsHandoff" checked={handoff === value} onChange={() => void save({ handoff: value })} /> {label}</label>)}
      </div>
      {repositories.length ? <div className={`findings-table-wrap ${handoff !== "auto" ? "dimmed" : ""}`}><table className="findings-table">
        <thead><tr><th>Repository</th><th className="num">Work ending in a PR</th><th>Prompts ask for</th></tr></thead>
        <tbody>{shownRepositories.map(repository => <tr key={repository.id}>
          <td>{repository.name}</td>
          <td className="num" title={`${repository.work.toLocaleString()} pieces of work in the last 90 days`}>{percent(num(repository.pr_share))}</td>
          <td><select aria-label={`What prompts for ${repository.name} ask for`} value={repository.override ?? "auto"} onChange={event => void save({ repository_handoff: { [repository.id]: event.target.value } })}>
            <option value="auto">{repository.override ? "Automatic" : `Automatic: ${repository.handoff === "pr" ? "pull request" : "diff"}`}</option>
            <option value="pr">Pull request</option>
            <option value="diff">Diff</option>
          </select></td>
        </tr>)}</tbody>
      </table>{handoff !== "auto" ? <p className="findings-table-note">These apply when prompts follow each repository.</p> : null}</div> : null}
      {repositories.length > 8 ? <button type="button" className="findings-button quiet" onClick={() => setShowAll(value => !value)}>{showAll ? "Show fewer" : `Show all ${repositories.length} repositories`}</button> : null}
    </section>

    <section className="findings-panel findings-setting" aria-labelledby="findingsUpdateTitle">
      <h2 id="findingsUpdateTitle">Updating findings</h2>
      <p>Findings are recomputed once a day after the first index, or when you ask. Findings you're watching update after every index.</p>
      <div className="findings-actions">
        <button type="button" className="findings-button" disabled={running} onClick={() => void refresh()}>{running ? "Refreshing…" : "Refresh findings now"}</button>
        <span className="findings-muted" role="status">{running ? `${status.phase ? `${status.phase}…` : "Working…"}` : status.error ? `Last refresh failed: ${status.error}` : status.built_at ? `Last refreshed ${whenText(status.built_at)}${seconds(status.took_ms) ? ` · took ${seconds(status.took_ms)}` : ""}` : "Not computed yet"}</span>
      </div>
      {status.measured_at ? <p className="findings-muted">Findings you're watching were last measured {whenText(status.measured_at)}.</p> : null}
    </section>
  </div>;
}

// ---------------------------------------------------------------- list page

function EmptyOpen({ overview }: { overview: Overview }) {
  const near = overview.near ?? [];
  const threshold = overview.threshold;
  const settings = <a href="/findings?view=settings" onClick={event => { event.preventDefault(); navigate("/findings?view=settings"); }}>Change the threshold</a>;
  return <div className="findings-empty">
    <strong>Nothing qualifies yet.</strong>
    <p>Pharos looked at {counted(Math.round(num(overview.status?.conversations_28d)), "conversation")} from the last 28 days. A pattern becomes a finding once it shows up in {threshold} conversations ({threshold === overview.recommended ? "the recommended threshold for a library this size" : `you set this; Pharos recommends ${overview.recommended} for a library this size`}).{" "}
      {near.length ? <>{spelled(near.length, "pattern")} {near.length === 1 ? "is" : "are"} close: {near.slice(0, 4).map((item, index) => <React.Fragment key={item.id}>{index ? index === Math.min(near.length, 4) - 1 ? " and " : ", " : ""}<Text text={item.title} /> ({counted(item.affected, "conversation")})</React.Fragment>)}. </> : null}
      {settings}</p>
  </div>;
}

function Building({ status }: { status: Status }) {
  return <div className="findings-empty building" role="status">
    <strong>{status.running ? "Pharos is looking for findings…" : "Findings haven't been computed yet."}</strong>
    <p>{status.running ? `${status.phase ? `${status.phase}. ` : ""}This reads the catalog and takes about a minute on a large library. You can keep using Pharos.` : "They're computed once a day after the first index. You can also start now from Findings settings."}</p>
    {status.running ? <div className="findings-progress" aria-hidden="true"><span /></div> : null}
  </div>;
}

function FindingsList({ overview, route, act, rank, setRank, onReview, newIds, highlightCart }: { overview: Overview; route: Route; act: Act; rank: Rank; setRank: (rank: Rank) => void; onReview: (group: CartGroup) => void; newIds: Set<string>; highlightCart: boolean }) {
  const summary = overview.summary;
  const status = overview.status ?? ({} as Status);
  const findings = overview.findings ?? [];
  const setList = (list: List) => replaceParams({ list: list === "open" ? "" : list });
  const places = useMemo(() => {
    const map = new Map<string, { key: string; label: string; count: number }>();
    for (const card of findings) {
      const key = card.repository_id || card.target;
      const label = card.repository_id ? card.scope_label.split(" · ")[0] : card.target_label;
      const entry = map.get(key) ?? { key, label, count: 0 };
      entry.count++;
      map.set(key, entry);
    }
    return [...map.values()].sort((a, b) => b.count - a.count || a.label.localeCompare(b.label));
  }, [findings]);
  const inList = (card: Card) => listForState(card.state) === route.list && (!route.repo || (card.repository_id || card.target) === route.repo);
  const shown = findings.filter(inList).sort((a, b) => num(b.impact?.[rank]) - num(a.impact?.[rank]));
  const counts: Record<List, number> = { open: summary.open ?? 0, watching: summary.watching ?? 0, wins: overview.wins?.length ?? summary.won ?? 0, dismissed: (summary.dismissed ?? 0) + (summary.snoozed ?? 0) };
  const saved = summary.saved ?? {};
  const built = Boolean(status.built_at);
  const tabs: Array<[List, string]> = [["open", "Open"], ["watching", "Watching"], ["wins", "Wins"], ["dismissed", "Dismissed"]];
  const showCart = route.list !== "wins";
  return <div className="findings-page">
    <div className="view-heading findings-heading">
      <div><h1>Findings</h1>
        <p className="muted">Recurring patterns in your agents' work that a change to a repository, its instructions, or a harness setting can fix. Each is measured before and after you hand it to an agent.</p>
        {built ? <p className="findings-updated">{status.running ? <>Updating{status.phase ? ` · ${status.phase}` : ""}… </> : null}Updated {whenText(status.built_at)} from {counted(Math.round(num(status.conversations_28d)), "conversation")}.</p> : null}
      </div>
      <button type="button" className="findings-button" onClick={() => navigate("/findings?view=settings")}><Icon name="settings" />Settings</button>
    </div>
    {status.error ? <p className="findings-error" role="alert">The last refresh failed: {status.error}</p> : null}
    {!built ? <Building status={status} /> : !findings.length && !overview.wins?.length && !overview.cart?.length ? <EmptyOpen overview={overview} /> : <>
      {route.list === "wins" ? <WinsHero overview={overview} /> : <div className="findings-tiles">
        <Tile label="Saved so far (estimate)" value={money(saved.usd)} detail={`${compact(saved.tokens)} tokens · ${minutes(saved.minutes)} · ${Math.round(num(saved.failures)).toLocaleString()} failures avoided`} onClick={() => setList("wins")} />
        <Tile label="Open" value={counts.open.toLocaleString()} detail={counts.open ? `about ${aboutMoney(summary.at_stake_usd)} a month at stake` : "nothing to fix right now"} onClick={() => setList("open")} pressed={route.list === "open"} />
        <Tile label="Watching" value={counts.watching.toLocaleString()} detail={counts.watching ? summary.next_result_days === null || summary.next_result_days === undefined ? "measuring" : summary.next_result_days <= 0 ? "first result due today" : `first result in ${counted(summary.next_result_days, "day")}` : "copy a prompt to start"} onClick={() => setList("watching")} pressed={route.list === "watching"} />
        <Tile label="Wins" value={(summary.won ?? 0).toLocaleString()} detail={summary.regressed ? `${summary.regressed} came back` : summary.won ? "none came back" : "none yet"} onClick={() => setList("wins")} />
      </div>}
      <div className="findings-toolbar">
        <div className="findings-tabs" role="tablist" aria-label="Findings by state">
          {tabs.map(([key, label]) => <button key={key} type="button" role="tab" aria-selected={route.list === key} onClick={() => setList(key)}>{label} <span className="findings-count">{counts[key]}</span></button>)}
        </div>
        {route.list !== "wins" && places.length > 1 ? <label className="findings-filter"><span className="visually-hidden">Where</span>
          <select value={route.repo} onChange={event => replaceParams({ repo: event.target.value })} aria-label="Show findings for">
            <option value="">Everywhere</option>
            {places.map(place => <option key={place.key} value={place.key}>{place.label} ({place.count})</option>)}
          </select><Icon name="chevron-down" />
        </label> : null}
        {route.list !== "wins" ? <div className="findings-rank"><span id="findingsRankLabel">Rank by</span>
          <div className="findings-segmented" role="group" aria-labelledby="findingsRankLabel">
            {(Object.keys(rankLabels) as Rank[]).map(unit => <button key={unit} type="button" aria-pressed={rank === unit} onClick={() => setRank(unit)}>{rankLabels[unit]}</button>)}
          </div>
        </div> : null}
      </div>
      {route.list === "wins" ? <WinsView overview={overview} /> : <div className={`findings-layout ${showCart ? "with-cart" : ""}`}>
        <div className="findings-list">
          {shown.map(card => <FindingCard key={card.id} card={card} rank={rank} targets={overview.targets ?? []} act={act} isNew={newIds.has(card.id)} />)}
          {!shown.length ? route.list === "open" && !route.repo ? <EmptyOpen overview={overview} />
            : <div className="findings-empty"><strong>{route.repo ? "Nothing here for this filter." : route.list === "watching" ? "Nothing is being measured." : "Nothing dismissed or snoozed."}</strong>
              <p>{route.repo ? "Choose Everywhere to see every finding in this list." : route.list === "watching" ? "Copy a prompt from Prompts to copy; its findings are measured here until each has a result." : "Findings you dismiss or snooze wait here, and you can restore any of them."}</p></div> : null}
        </div>
        {showCart ? <CartPanel cart={overview.cart ?? []} targets={overview.targets ?? []} act={act} onReview={onReview} highlight={highlightCart} /> : null}
      </div>}
    </>}
  </div>;
}

// ---------------------------------------------------------------- page

export function FindingsPage({ copy }: { copy: Copy }) {
  const [route, setRoute] = useState<Route>(readRoute);
  const [overview, setOverview] = useState<Overview | null>(latest);
  const [error, setError] = useState("");
  const [newIds, setNewIds] = useState<Set<string>>(new Set());
  const [review, setReview] = useState<string>("");
  const [toast, setToast] = useState("");
  const [highlightCart, setHighlightCart] = useState(false);
  const [rankOverride, setRankOverride] = useState<Rank | null>(null);
  const toastTimer = useRef<number>();
  const listScroll = useRef(0);

  useEffect(() => { subscribers.add(setOverview); return () => { subscribers.delete(setOverview); }; }, []);
  const reload = useCallback(() => loadOverview().then(body => { setError(""); return body; }).catch(failure => { setError(failure.message); return null; }), []);

  useEffect(() => {
    let wasOpen = false;
    const onRoute = () => {
      const here = location.pathname === "/findings";
      pageOpen = here;
      if (!here) { wasOpen = false; return; }
      const next = readRoute();
      setRoute(previous => {
        const scroller = mainScroller();
        if (!previous.finding && next.finding && scroller) listScroll.current = scroller.scrollTop;
        if (previous.finding !== next.finding || previous.view !== next.view) window.requestAnimationFrame(() => mainScroller()?.scrollTo(0, next.finding || next.view ? 0 : listScroll.current));
        return next;
      });
      if (!wasOpen) {
        wasOpen = true;
        // A visit: remember which findings were new, then mark them seen.
        void reload().then(body => {
          if (!body) return;
          setNewIds(new Set(body.findings.filter(card => card.new).map(card => card.id)));
          if (body.summary?.new) request("/api/findings/seen", {}).catch(() => { /* The badge clears on the next visit. */ });
          updateChrome(0, cartCount(body.cart));
        });
      }
    };
    const onURI = () => { if (location.pathname === "/findings") setRoute(readRoute()); };
    onRoute();
    // pharos:route follows address changes; pharos:view follows tab clicks, which change the address without one.
    window.addEventListener("pharos:route", onRoute);
    window.addEventListener("pharos:view", onRoute);
    window.addEventListener("pharos:uri-changed", onURI);
    return () => { window.removeEventListener("pharos:route", onRoute); window.removeEventListener("pharos:view", onRoute); window.removeEventListener("pharos:uri-changed", onURI); };
  }, [reload]);

  // Refresh while a pass is running so the page fills in when it's done.
  useEffect(() => {
    if (!overview?.status?.running || route.view === "settings") return;
    const poll = window.setInterval(() => { if (location.pathname === "/findings") void reload(); }, 3000);
    return () => window.clearInterval(poll);
  }, [overview?.status?.running, route.view, reload]);

  useEffect(() => {
    const show = () => {
      setHighlightCart(true);
      // The list may still be loading; wait for the cart to appear.
      let tries = 0;
      const reveal = () => {
        const cart = document.getElementById("findingsCart");
        if (!cart) { if (++tries < 40) window.setTimeout(reveal, 100); return; }
        cart.scrollIntoView({ behavior: "smooth", block: "nearest" });
        cart.focus({ preventScroll: true });
        window.setTimeout(() => setHighlightCart(false), 1800);
      };
      window.setTimeout(reveal, 60);
    };
    window.addEventListener("pharos:findings-cart", show);
    return () => window.removeEventListener("pharos:findings-cart", show);
  }, []);

  const say = useCallback((message: string) => {
    setToast(message);
    window.clearTimeout(toastTimer.current);
    toastTimer.current = window.setTimeout(() => setToast(""), 3200);
  }, []);

  const act: Act = useCallback(async (id, body, done) => {
    try {
      await request(`${findingURL(id)}/action`, body);
      await reload();
      if (done) say(done);
      return true;
    } catch (failure) { say(`Couldn't do that: ${(failure as Error).message}`); return false; }
  }, [reload, say]);

  const rank: Rank = rankOverride ?? overview?.settings?.rank ?? "usd";
  const setRank = (next: Rank) => {
    setRankOverride(next);
    request("/api/findings/settings", { rank: next }).then(reload).then(() => setRankOverride(null)).catch(failure => say(`Couldn't save the ranking: ${failure.message}`));
  };
  const group = review ? overview?.cart?.find(item => item.target === review) : undefined;

  let body: React.ReactNode;
  if (route.view === "settings") body = <FindingsSettings overview={overview} reload={reload} />;
  else if (route.finding) body = <FindingDetail id={route.finding} tab={route.tab} overview={overview} act={act} rank={rank} onReview={next => setReview(next.target)} newIds={newIds} />;
  else if (!overview) body = <div className="findings-page">{error ? <p className="findings-error" role="alert">{error}</p> : <p className="findings-muted">Loading findings…</p>}</div>;
  else body = <FindingsList overview={overview} route={route} act={act} rank={rank} setRank={setRank} onReview={next => setReview(next.target)} newIds={newIds} highlightCart={highlightCart} />;

  return <>
    {body}
    {group ? <ReviewDialog group={group} copy={copy} onClose={() => setReview("")} onCopied={() => { setReview(""); void reload(); say("Copied. Measuring has started."); }} /> : null}
    <div className={`findings-toast ${toast ? "shown" : ""}`} role="status" aria-live="polite">{toast}</div>
  </>;
}
