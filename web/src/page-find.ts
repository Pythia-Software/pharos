import { readViewState } from "./view-state";
import { FIND_LIMIT, FIND_MINIMUM, findFromAttention, findMatches, findPattern, findTest, stepIndex, writeFindParameters, type FindPattern, type FindState } from "./page-find-model";
import "./page-find.css";

// Page Find (Cmd/Ctrl+F) searches the current page's content, including the
// contents of closed disclosures and lazy content that is not rendered yet.
// Visible matches use CSS Custom Highlights. A closed disclosure holding
// matches is marked with their count; stepping to such a match opens the
// disclosures on its path (through their own URL-backed state) and marks
// that path as a trail. Find never moves focus out of its input.

type Disclosure = HTMLDetailsElement | HTMLButtonElement;
/**
 * Lazy content marks itself with data-pharos-find-pending. pharosFindPending
 * returns the text it would render (null once rendered); pharosFindMaterialize
 * renders the parts whose text passes the test.
 */
type Pending = HTMLElement & { pharosFindPending?: () => string[] | null; pharosFindMaterialize?: (test: (text: string) => boolean) => void };
/** Clamped content, such as truncated prose, expands before a match is shown. */
type Expandable = HTMLElement & { pharosFindExpand?: () => void };
type Match = { range?: Range; chain: Disclosure[]; pending?: Pending; text?: string; offset: number };
export type FindNote = { text: string; action?: { label: string; run: () => void } };
type NoteProvider = (pattern: RegExp) => FindNote | null;
type Entry = { node: Text; start: number; chain: Disclosure[] };
type Index = { content: string; entries: Entry[]; pending: { element: Pending; chain: Disclosure[]; offset: number }[] };
declare global { interface Window { pharosFind: { open: (text?: string) => void; next: () => void; previous: () => void; close: () => void; registerNote: (provider: NoteProvider) => () => void } } }

const EXCLUDED = "script,style,template,noscript,textarea,input,select,svg,[inert],[aria-hidden='true'],.pharos-find,.pharos-attention-status";
const notes = new Set<NoteProvider>();
let bar: HTMLElement | undefined, input: HTMLInputElement, caseButton: HTMLButtonElement, regexButton: HTMLButtonElement;
let previousButton: HTMLButtonElement, nextButton: HTMLButtonElement, expandButton: HTMLButtonElement, statusText: HTMLElement, noteList: HTMLElement;
/** Where the user dragged Find, kept for this page session; null uses the default place. */
let placed: { left: number; top: number } | null = null;
let matches: Match[] = [], current = -1, currentKey: { node: Node; offset: number } | { pending: Pending; nth: number } | null = null;
let compiled: FindPattern = { status: "empty" };
let typed = "", drafting = false, searchedKey = "", landedKey = "", landOn: string | null = null, generation = 0, timer = 0, dueAt = 0, writeTimer = 0, running = 0, rerun = false;
let marked = new Map<HTMLElement, Match>(), trail = new Set<HTMLElement>(), returnFocus: HTMLElement | null = null;

function readFind(): FindState | null {
  if (!new URLSearchParams(location.search).has("find")) return null;
  // A malformed copied flag falls back to its default rather than closing Find.
  const flag = (read: () => boolean) => { try { return read(); } catch { return false; } };
  return { text: readViewState<string>("find", ""), caseSensitive: flag(() => readViewState("find_case", false)), regex: flag(() => readViewState("find_regex", false)) };
}
function writeFind(state: FindState | null) {
  const url = new URL(location.href);
  writeFindParameters(url.searchParams, state);
  if (url.href === location.href) return;
  history.replaceState(history.state, "", url);
  window.dispatchEvent(new Event("pharos:uri-changed"));
}
const stateKey = (state: FindState | null) => JSON.stringify([state, state?.text === typed, location.pathname, new URLSearchParams(location.search).get("conversation")]);
// The topmost open modal, native or ARIA, owns the page while it is shown.
const modal = () => [...document.querySelectorAll<HTMLElement>("dialog[open], [role='dialog'][aria-modal='true']")].filter(element => element.checkVisibility()).at(-1) ?? null;
const root = () => modal() ?? document.querySelector<HTMLElement>(".drawer.open") ?? document.querySelector<HTMLElement>(".view.active") ?? document.querySelector<HTMLElement>("main");
const summaryOf = (disclosure: Element) => disclosure instanceof HTMLDetailsElement ? disclosure.querySelector<HTMLElement>(":scope > summary") ?? disclosure : disclosure as HTMLElement;

/** `text:` attention targets open Find instead; a revealed one lands on the first match. */
export function adoptTextTargets(): boolean {
  const params = new URLSearchParams(location.search), reveal = params.get("reveal") ?? "";
  const state = findFromAttention(params.getAll("highlight"), reveal);
  if (!state) return false;
  const url = new URL(location.href), highlights = url.searchParams.getAll("highlight").filter(target => !target.startsWith("text:"));
  url.searchParams.delete("highlight");
  for (const target of highlights) url.searchParams.append("highlight", target);
  if (reveal.startsWith("text:")) { url.searchParams.delete("reveal"); landOn = reveal.slice(5).toLocaleLowerCase(); }
  writeFindParameters(url.searchParams, state);
  history.replaceState(history.state, "", url);
  window.dispatchEvent(new Event("pharos:uri-changed"));
  return true;
}

// A collapsed button's panel is hidden by its disclosure, not removed from Find.
function panelOpener(element: Element): HTMLButtonElement | null {
  if (!element.id) return null;
  const button = document.querySelector<HTMLButtonElement>(`button[aria-controls~="${CSS.escape(element.id)}"]`);
  return button && button.getAttribute("aria-expanded") !== "true" && !button.disabled ? button : null;
}
function* walk(start: HTMLElement, index: Index): Generator<void, void> {
  let boundary = false, steps = 0;
  function* visit(element: Element, chain: Disclosure[]): Generator<void, void> {
    if (++steps % 200 === 0) yield;
    if (element.matches(EXCLUDED)) return;
    const style = getComputedStyle(element);
    if (style.display === "none") {
      const opener = panelOpener(element);
      if (!opener) return;
      chain = [...chain, opener];
    }
    const block = style.display !== "inline" && style.display !== "contents", closed = element instanceof HTMLDetailsElement && !element.open ? element : null;
    if (block) boundary = true;
    if (element.hasAttribute("data-pharos-find-pending")) index.pending.push({ element: element as Pending, chain: closed ? [...chain, closed] : chain, offset: index.content.length });
    const summary = closed?.querySelector(":scope > summary");
    for (const child of element.childNodes) {
      if (child instanceof Text) {
        if (!child.data || style.visibility !== "visible") continue;
        // Block boundaries separate words, as they read on the page.
        if (boundary && index.content) index.content += "\n";
        boundary = false;
        index.entries.push({ node: child, start: index.content.length, chain });
        index.content += child.data;
      } else if (child instanceof Element) yield* visit(child, closed && child !== summary ? [...chain, closed] : chain);
    }
    if (block) boundary = true;
  }
  yield* visit(start, []);
}

/** Rebuilds matches; returns false when a newer search or term replaced this one. */
async function search(): Promise<boolean> {
  const token = ++generation, state = readFind(), key = stateKey(state);
  if (!state) { teardown(); return true; }
  running++;
  try { return await rebuild(token, state, key); }
  finally { if (!--running && rerun) { rerun = false; schedule(250); } }
}
async function rebuild(token: number, state: FindState, key: string): Promise<boolean> {
  showBar(state);
  compiled = findPattern(state, state.text === typed);
  const scope = root();
  let next: Match[] = [];
  if (compiled.status === "ready" && scope) {
    const index: Index = { content: "", entries: [], pending: [] }, walker = walk(scope, index);
    // Yield to input and rendering on slow machines; a newer search wins.
    let slice = performance.now();
    for (let step = walker.next(); !step.done; step = walker.next()) {
      if (performance.now() - slice < 8) continue;
      await new Promise(resolve => setTimeout(resolve, 0));
      if (token !== generation) return false;
      if (stateKey(readFind()) !== key) { schedule(); return false; }
      slice = performance.now();
    }
    const pattern = compiled.pattern;
    let first = 0;
    for (const [start, end] of findMatches(index.content, pattern)) {
      while (first + 1 < index.entries.length && index.entries[first + 1].start <= start) first++;
      let last = first;
      while (last + 1 < index.entries.length && index.entries[last + 1].start < end) last++;
      const head = index.entries[first], tail = index.entries[last];
      if (!head || !head.node.isConnected || !tail.node.isConnected) continue;
      const range = new Range();
      range.setStart(head.node, Math.min(start - head.start, head.node.length));
      range.setEnd(tail.node, Math.min(end - tail.start, tail.node.length));
      next.push({ range, chain: head.chain, offset: start });
    }
    for (const { element, chain, offset } of index.pending) {
      for (const text of element.pharosFindPending?.() ?? []) {
        if (next.length >= FIND_LIMIT) break;
        for (const [start, end] of findMatches(text, pattern, FIND_LIMIT - next.length)) next.push({ chain, pending: element, text: text.slice(start, end), offset });
      }
    }
    next = next.sort((a, b) => a.offset - b.offset || Number(!a.pending) - Number(!b.pending)).slice(0, FIND_LIMIT);
  }
  if (token !== generation) return false;
  matches = next; searchedKey = key;
  current = restoreCurrent();
  const literal = landOn, landing = literal === null ? -1 : matches.findIndex(match => (match.range?.toString() ?? match.text)?.toLocaleLowerCase() === literal);
  if (landing >= 0) {
    // A revealed literal lands on its own first match, opening its path. It
    // waits until that literal is on the page, even inside lazy content.
    landOn = null; landedKey = key; current = -1;
    void step(1, landing);
  }
  else if (current < 0 && landedKey !== key) {
    landedKey = key;
    // A new term selects the first shown match from the viewport down; it
    // never opens a disclosure until the user steps to a collapsed match.
    current = firstOnScreen(true);
    if (current >= 0) { remember(matches[current]); if (!onScreen(matches[current].range!)) scrollRange(matches[current].range!); }
  }
  paint();
  return true;
}
async function settle() { for (let attempt = 0; attempt < 5; attempt++) if (await search()) return; }
// Page changes queue behind a running search; only a new term or URL cancels
// one. A pending search only moves earlier, so a page that keeps changing
// (rails, polling) cannot postpone it forever.
function schedule(delay = 0) {
  const due = performance.now() + delay;
  if (timer && due >= dueAt) return;
  clearTimeout(timer); dueAt = due;
  timer = window.setTimeout(() => { timer = 0; if (running) rerun = true; else void search(); }, delay);
}

const shown = (match: Match) => !!match.range && !match.chain.length && !match.pending;
function onScreen(range: Range) { const rect = range.getBoundingClientRect(); return rect.bottom > 0 && rect.top < innerHeight; }
function firstOnScreen(shownOnly: boolean): number {
  let fallback = -1;
  for (let index = 0; index < matches.length; index++) {
    const match = matches[index];
    if (shownOnly && !shown(match)) continue;
    if (fallback < 0) fallback = index;
    const box = shown(match) ? match.range!.getBoundingClientRect() : summaryOf(match.chain[0] ?? match.pending!).getBoundingClientRect();
    if (box.bottom > 0) return index;
  }
  return fallback;
}
function remember(match: Match) {
  currentKey = match.range && shown(match) ? { node: match.range.startContainer, offset: match.range.startOffset } : match.pending ? { pending: match.pending, nth: matches.filter(other => other.pending === match.pending).indexOf(match) } : null;
}
function restoreCurrent(): number {
  const key = currentKey;
  if (!key) return -1;
  if ("node" in key) return matches.findIndex(match => shown(match) && match.range!.startContainer === key.node && match.range!.startOffset === key.offset);
  const same = matches.filter(match => match.pending === key.pending);
  return same.length ? matches.indexOf(same[Math.min(key.nth, same.length - 1)]) : -1;
}

function paint() {
  const visible = matches.filter(shown), selected = matches[current];
  if (typeof Highlight !== "undefined" && CSS.highlights) {
    CSS.highlights.set("pharos-matches", new Highlight(...visible.map(match => match.range!)));
    CSS.highlights.set("pharos-landing", new Highlight(...(selected && shown(selected) ? [selected.range!] : [])));
  }
  // Each closed disclosure on the page shows how many matches it holds.
  const counts = new Map<HTMLElement, number>(), firsts = new Map<HTMLElement, Match>();
  for (const match of matches) {
    if (shown(match)) continue;
    const badge = summaryOf(match.chain[0] ?? match.pending!);
    counts.set(badge, (counts.get(badge) ?? 0) + 1);
    if (!firsts.has(badge)) firsts.set(badge, match);
  }
  for (const element of marked.keys()) if (!counts.has(element)) { element.removeAttribute("data-pharos-find"); element.removeAttribute("data-pharos-find-count"); }
  for (const [element, count] of counts) {
    if (element.dataset.pharosFind !== "container") element.dataset.pharosFind = "container";
    const text = count >= FIND_LIMIT ? `${count}+` : String(count);
    if (element.dataset.pharosFindCount !== text) element.dataset.pharosFindCount = text;
  }
  marked = firsts;
  if (!bar) return;
  const collapsed = matches.length - visible.length, more = matches.length >= FIND_LIMIT ? "+" : "";
  const status = compiled.status === "short" ? `Type ${FIND_MINIMUM}+ characters or press Enter`
    : compiled.status === "invalid" ? compiled.error
    : compiled.status === "empty" ? ""
    : !matches.length ? "No matches"
    : `${current >= 0 ? `${current + 1} of ` : ""}${matches.length}${more}${current >= 0 ? "" : ` match${matches.length === 1 ? "" : "es"}`}${collapsed ? ` · ${collapsed}${more} collapsed` : ""}`;
  if (statusText.textContent !== status) statusText.textContent = status;
  bar.classList.toggle("invalid", compiled.status === "invalid");
  previousButton.disabled = nextButton.disabled = !matches.length;
  expandButton.hidden = !collapsed;
  noteList.replaceChildren();
  if (compiled.status !== "ready") return;
  for (const provider of notes) {
    let note: FindNote | null = null;
    try { note = provider(new RegExp(compiled.pattern.source, compiled.pattern.flags.replace("g", ""))); } catch { /* A failing provider must not break Find. */ }
    if (!note) continue;
    const line = document.createElement("p");
    line.textContent = note.text;
    if (note.action) { const action = note.action; line.append(" ", button(action.label, "", () => action.run())); }
    noteList.append(line);
  }
}
function clearTrail() { for (const element of trail) element.removeAttribute("data-pharos-find-trail"); trail.clear(); }
function markTrail(disclosure: Element) { const element = summaryOf(disclosure); element.dataset.pharosFindTrail = ""; trail.add(element); }
function teardown() {
  clearTimeout(writeTimer); bar?.remove(); bar = undefined;
  matches = []; current = -1; currentKey = null; typed = ""; drafting = false; landOn = null; landedKey = ""; searchedKey = ""; compiled = { status: "empty" };
  clearTrail(); paint();
}

function openDisclosure(disclosure: Disclosure) {
  // Opening goes through each disclosure's own handlers, so its URL state
  // (disclosures, reader_expanded) records the opening like a click would.
  if (disclosure instanceof HTMLDetailsElement) { if (!disclosure.open) disclosure.open = true; }
  else if (disclosure.getAttribute("aria-expanded") !== "true") disclosure.click();
}
const inside = (scope: Element, match: Match) => match.range ? scope.contains(match.range.startContainer) : !!match.pending && scope.contains(match.pending);
const frame = () => new Promise(resolve => requestAnimationFrame(resolve));
/** Opens the path to a match, rendering lazy content, then shows the exact text. */
async function reveal(target: Match | undefined) {
  clearTrail();
  for (let attempt = 0; target && !shown(target) && attempt < 8; attempt++) {
    if (compiled.status !== "ready") return;
    const container = target.pending ?? target.chain.at(-1)!;
    // A lazy control can replace itself; its parent keeps the position.
    const scope = target.pending && !(target.pending instanceof HTMLDetailsElement) ? target.pending.parentElement ?? container : container;
    const nth = matches.filter(match => inside(scope, match)).indexOf(target);
    for (const disclosure of target.chain) { markTrail(disclosure); openDisclosure(disclosure); }
    const pending: Pending | undefined = target.pending;
    if (pending) { if (pending.tagName === "DETAILS") markTrail(pending); pending.pharosFindMaterialize?.(findTest(compiled.pattern)); }
    await frame(); await frame(); await settle();
    const candidates = matches.filter(match => inside(scope, match));
    target = candidates[Math.max(0, Math.min(nth, candidates.length - 1))];
  }
  if (!target || !shown(target)) { paint(); return; }
  current = matches.indexOf(target); remember(target);
  for (let element = target.range!.startContainer.parentElement as Expandable | null; element; element = element.parentElement as Expandable | null) element.pharosFindExpand?.();
  scrollRange(target.range!);
  paint();
}
function scrollParent(element: HTMLElement): HTMLElement | null {
  for (let parent = element.parentElement; parent; parent = parent.parentElement) {
    const overflow = getComputedStyle(parent).overflowY;
    if ((overflow === "auto" || overflow === "scroll") && parent.scrollHeight > parent.clientHeight) return parent;
  }
  return null;
}
/** Scrolls a range's text to the middle of its scroller, without focus. */
export function scrollRange(range: Range) {
  const element = range.startContainer.parentElement;
  if (!element) return;
  element.scrollIntoView({ block: "nearest", inline: "nearest" });
  // Center the matched text itself; its paragraph can be taller than the view.
  const rect = range.getBoundingClientRect(), scroller = scrollParent(element);
  const box = scroller?.getBoundingClientRect() ?? { top: 0, height: innerHeight };
  const delta = rect.top + rect.height / 2 - (box.top + box.height / 2);
  if (Math.abs(delta) > 4) (scroller ?? document.scrollingElement ?? document.documentElement).scrollBy({ top: delta });
}
/** Opens every disclosure holding a match and renders lazy content holding one. */
async function expandAll() {
  clearTrail();
  for (let pass = 0; pass < 8 && compiled.status === "ready"; pass++) {
    const hidden = matches.filter(match => !shown(match));
    if (!hidden.length) break;
    const test = findTest(compiled.pattern);
    for (const match of hidden) {
      for (const disclosure of match.chain) openDisclosure(disclosure);
      match.pending?.pharosFindMaterialize?.(test);
    }
    await frame(); await frame(); await settle();
  }
  paint();
}
async function step(delta: number, from?: number) {
  const state = readFind();
  if (!state) return;
  if (typed) { typed = ""; drafting = false; clearTimeout(writeTimer); writeFind({ ...state, text: input.value }); }
  if (searchedKey !== stateKey(readFind())) await settle();
  if (!matches.length) { paint(); return; }
  current = stepIndex(current, delta, matches.length, from ?? firstOnScreen(false));
  await reveal(matches[current]);
}

function button(label: string, title: string, action: () => void, symbol = "") {
  const element = document.createElement("button");
  element.type = "button";
  if (title) { element.title = title; element.setAttribute("aria-label", title); }
  if (symbol) element.append(icon(symbol));
  else element.textContent = label;
  element.onclick = action;
  return element;
}
function icon(name: string) {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg"), use = document.createElementNS("http://www.w3.org/2000/svg", "use");
  svg.classList.add("app-icon"); svg.setAttribute("viewBox", "0 0 24 24"); svg.setAttribute("aria-hidden", "true");
  use.setAttribute("href", `#ph-icon-${name}`); svg.append(use);
  return svg;
}
// Find opens below the header unless the user dragged it elsewhere.
function place() {
  if (!bar) return;
  const header = document.querySelector("body > header")?.getBoundingClientRect().bottom ?? 0;
  bar.style.setProperty("--pharos-find-top", `${Math.max(16, header + 24)}px`);
  bar.classList.toggle("placed", !!placed);
  if (!placed) { bar.style.left = bar.style.top = bar.style.right = ""; return; }
  const box = bar.getBoundingClientRect();
  placed = { left: Math.min(Math.max(0, placed.left), Math.max(0, innerWidth - box.width)), top: Math.min(Math.max(0, placed.top), Math.max(0, innerHeight - box.height)) };
  bar.style.left = `${placed.left}px`; bar.style.top = `${placed.top}px`; bar.style.right = "auto";
}
function moveBy(dx: number, dy: number) {
  const box = bar!.getBoundingClientRect();
  placed = { left: box.left + dx, top: box.top + dy };
  place();
}
function draggable(grip: HTMLButtonElement) {
  grip.addEventListener("pointerdown", event => {
    if (event.button !== 0 || !bar) return;
    event.preventDefault();
    const box = bar.getBoundingClientRect(), start = { x: event.clientX - box.left, y: event.clientY - box.top };
    grip.setPointerCapture(event.pointerId); bar.classList.add("dragging");
    const move = (next: PointerEvent) => { placed = { left: next.clientX - start.x, top: next.clientY - start.y }; place(); };
    const end = () => { bar?.classList.remove("dragging"); grip.removeEventListener("pointermove", move); grip.removeEventListener("pointerup", end); grip.removeEventListener("pointercancel", end); };
    grip.addEventListener("pointermove", move); grip.addEventListener("pointerup", end); grip.addEventListener("pointercancel", end);
  });
  // Keyboard users move it with the arrow keys; double-click or Home resets it.
  grip.addEventListener("keydown", event => {
    const delta = event.shiftKey ? 64 : 16, moves: Record<string, [number, number]> = { ArrowLeft: [-delta, 0], ArrowRight: [delta, 0], ArrowUp: [0, -delta], ArrowDown: [0, delta] };
    if (moves[event.key]) { event.preventDefault(); moveBy(...moves[event.key]); }
    else if (event.key === "Home") { event.preventDefault(); placed = null; place(); }
  });
  grip.addEventListener("dblclick", () => { placed = null; place(); });
}
function showBar(state: FindState) {
  if (!bar) {
    bar = document.createElement("aside"); bar.className = "pharos-find"; bar.setAttribute("role", "search"); bar.setAttribute("aria-label", "Find on this page");
    const grip = button("", "Move Find (drag, or arrow keys; Home resets)", () => {}, "grip"); grip.classList.add("pharos-find-grip");
    draggable(grip);
    input = document.createElement("input"); input.type = "search"; input.placeholder = "Find on page"; input.setAttribute("aria-label", "Find on this page"); input.spellcheck = false;
    const toggle = (name: "caseSensitive" | "regex") => () => { const now = readFind(); if (now) writeFind({ ...now, text: input.value, [name]: !now[name] }); input.focus(); };
    caseButton = button("Aa", "Match case", toggle("caseSensitive")); caseButton.dataset.viewField = "find_case";
    regexButton = button(".*", "Regular expression", toggle("regex")); regexButton.dataset.viewField = "find_regex";
    const field = document.createElement("div"); field.className = "pharos-find-field";
    field.append(icon("search"), input, caseButton, regexButton);
    statusText = document.createElement("span"); statusText.className = "pharos-find-status"; statusText.setAttribute("role", "status");
    previousButton = button("", "Previous match (Shift+Enter)", () => void step(-1), "arrow-up");
    nextButton = button("", "Next match (Enter)", () => void step(1), "arrow-down");
    expandButton = button("Expand all", "Open every collapsed section holding a match", () => void expandAll()); expandButton.classList.add("pharos-find-expand"); expandButton.hidden = true;
    noteList = document.createElement("div"); noteList.className = "pharos-find-notes";
    const row = document.createElement("div"); row.className = "pharos-find-row";
    row.append(grip, field, previousButton, nextButton, button("", "Close Find (Escape)", close, "close"));
    const meta = document.createElement("div"); meta.className = "pharos-find-meta";
    meta.append(statusText, expandButton);
    bar.append(row, meta, noteList);
    input.dataset.viewField = "find";
    input.addEventListener("input", () => {
      typed = input.value; drafting = true; clearTimeout(writeTimer);
      // The URL follows typing after a pause, so each keystroke is not a search.
      writeTimer = window.setTimeout(() => { drafting = false; const now = readFind(); if (now) writeFind({ ...now, text: input.value }); }, 150);
    });
    input.addEventListener("keydown", event => {
      if (event.key === "Enter") { event.preventDefault(); void step(event.shiftKey ? -1 : 1); }
    });
    // Escape closes Find from any of its controls, before an enclosing modal sees it.
    bar.addEventListener("keydown", event => {
      if (event.key !== "Escape") return;
      event.preventDefault(); event.stopPropagation(); close();
    });
  }
  // A modal dialog makes the rest of the page inert; Find lives inside it.
  const host = modal() ?? document.body;
  if (bar.parentElement !== host) host.append(bar);
  place();
  // Back/Forward and links replace the term; only an unwritten draft is kept.
  if (input.value !== state.text && !drafting) input.value = state.text;
  caseButton.setAttribute("aria-pressed", String(state.caseSensitive));
  regexButton.setAttribute("aria-pressed", String(state.regex));
}

function open(text?: string) {
  const state = readFind();
  if (!bar || !bar.contains(document.activeElement)) returnFocus = document.activeElement instanceof HTMLElement && document.activeElement !== document.body ? document.activeElement : null;
  if (text !== undefined) { typed = ""; writeFind({ text, caseSensitive: state?.caseSensitive ?? false, regex: state?.regex ?? false }); }
  else if (!state) writeFind({ text: "", caseSensitive: false, regex: false });
  void search().then(() => { if (!bar) return; input.focus(); input.select(); });
}
function close() {
  const focus = returnFocus; returnFocus = null;
  writeFind(null);
  if (focus?.isConnected) focus.focus({ preventScroll: true });
}

export function installPageFind() {
  window.pharosFind = { open, next: () => void step(1), previous: () => void step(-1), close, registerNote: provider => { notes.add(provider); schedule(); return () => { notes.delete(provider); schedule(); }; } };
  document.addEventListener("keydown", event => {
    if (!(event.metaKey || event.ctrlKey) || event.altKey) return;
    const key = event.key.toLowerCase();
    if (key === "f" && !event.shiftKey) {
      // A second Cmd+F in the browser opens its own find; the Mac app has none.
      if (bar && document.activeElement === input && !document.documentElement.classList.contains("pharos-window")) return;
      event.preventDefault(); open();
    } else if (key === "g" && readFind()) { event.preventDefault(); void step(event.shiftKey ? -1 : 1); }
  }, true);
  // Clicking a marked disclosure opens it and goes to its first match.
  document.addEventListener("click", event => {
    const badge = event.target instanceof Element ? event.target.closest<HTMLElement>("[data-pharos-find='container']") : null;
    const target = badge && marked.get(badge);
    if (target) setTimeout(() => void reveal(target), 0);
  }, true);
  for (const name of ["popstate", "pharos:route", "pharos:view", "pharos:uri-changed"]) window.addEventListener(name, () => { if (stateKey(readFind()) !== searchedKey) schedule(); });
  const owned = (node: Node) => !!(node instanceof Element ? node : node.parentElement)?.closest(".pharos-find");
  new MutationObserver(records => {
    if (!bar) return;
    // Only the searched content matters, plus switches of the content root.
    const scope = root(), relevant = (node: Node) => !owned(node) && (!scope || scope.contains(node) || node.contains(scope) || node instanceof Element && node.matches("dialog,.drawer,.view"));
    const changed = records.filter(record => relevant(record.target));
    // Class and style changes (hover, rails) rarely change text; wait longer.
    if (changed.length) schedule(changed.some(record => record.type !== "attributes" || !["class", "style"].includes(record.attributeName!)) ? 250 : 750);
  }).observe(document.body, { subtree: true, childList: true, characterData: true, attributes: true, attributeFilter: ["open", "hidden", "class", "aria-expanded", "style", "inert"] });
  window.addEventListener("resize", place);
  window.dispatchEvent(new Event("pharos:find-ready"));
  schedule();
}
