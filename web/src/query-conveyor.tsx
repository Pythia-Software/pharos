import React, { memo, useEffect, useLayoutEffect, useRef, useState } from "react";
import { suggestionWindowLabel, type QueryPreview, type QuerySuggestion } from "./curiosity-data";
import { QueryCard } from "./query-card";
import { cardLoadingPresentation } from "./card-loading-model";
import { Icon } from "./icons";
import { SPACING, beltSlots, growBelt, chooseNext, leanBeside, previewKind } from "./conveyor-deck";

// The timed advance drifts past; a move someone asks for answers in half the time.
const AUTO_SLIDE_MS = 2040, USER_SLIDE_MS = 1020;
const REVEAL_MS = 340;
// Room each side of five cards for a glimpse of the next ones.
const PEEK = 72;
// How many pages a retargeted slide may run ahead of the belt, so repeated
// presses queue a short lead rather than an ever longer belt.
const MAX_LEAD_PAGES = 2;
const wrap = (index: number, count: number) => ((index % count) + count) % count;
const counted = (count: number) => ["one", "two", "three", "four", "five"][count - 1] ?? String(count);
type Movement = { offset: number; center: number; target: string; focus: boolean; items: QuerySuggestion[]; duration: number };

function CarouselCountdown({ held, paused, reset, advance }: { held: boolean; paused: boolean; reset: number; advance: () => void }) {
  const [remaining, setRemaining] = useState(30_000), onAdvance = useRef(advance);
  onAdvance.current = advance;
  useEffect(() => setRemaining(30_000), [reset]);
  useEffect(() => {
    if (held) return;
    let last = performance.now();
    const timer = window.setInterval(() => {
      const now = performance.now(), elapsed = now - last; last = now;
      setRemaining(current => Math.max(0, current - elapsed));
    }, 250);
    return () => clearInterval(timer);
  }, [held]);
  useEffect(() => { if (remaining === 0) { onAdvance.current(); setRemaining(30_000); } }, [remaining]);
  return <span className="home-countdown"><span className="home-countdown-track"><i style={{ transform: `scaleX(${remaining / 30_000})` }} /></span><span>{held ? paused ? "Paused" : "Waiting" : `More in ${Math.ceil(remaining / 1000)}s`}</span></span>;
}

type BeltActions = { open: (url: string) => void; keep: (item: QuerySuggestion) => void; reveal: (index: number) => void;
  dragStart: (event: React.DragEvent, item: QuerySuggestion) => void; dragEnd: () => void };
// Cards cross the reading area mid-slide, so only the ones whose state changes
// re-render; callbacks are read from a ref rather than passed as fresh props.
const BeltCard = memo(function BeltCard({ item, preview, xray, lean, index, revealable, actions }: {
  item: QuerySuggestion; preview?: QueryPreview; xray: boolean; lean: number; index: number; revealable: boolean; actions: React.MutableRefObject<BeltActions | null>;
}) {
  return <QueryCard discovery xray={xray} id={item.instanceId} title={item.title} description={item.description} actionLabel={item.action} url={item.url} windowLabel={suggestionWindowLabel(item)} category={item.category} lean={lean}
    loadingSpec={cardLoadingPresentation(item.query, { recipe: item, cover: item.cover, days: item.window?.days })} preview={preview} onReveal={revealable ? () => actions.current!.reveal(index) : undefined}
    onOpen={() => actions.current!.open(item.url)} onKeep={() => actions.current!.keep(item)} onDragStart={event => actions.current!.dragStart(event, item)} onDragEnd={() => actions.current!.dragEnd()} />;
});

export function QueryConveyor({ items, previews, loading, hasErrors, active, suspended, onAllCards, onRefresh, onOpen, onKeep, onDragStart, onDragEnd }: {
  items: QuerySuggestion[]; previews: Record<string, QueryPreview>; loading: boolean; hasErrors: boolean;
  active: boolean; suspended: boolean; onRefresh: () => void; onOpen: (url: string) => void; onKeep: (item: QuerySuggestion) => void;
  onDragStart: (event: React.DragEvent, item: QuerySuggestion) => void; onDragEnd: () => void;
  onAllCards: () => void;
}) {
  const section = useRef<HTMLElement>(null), heading = useRef<HTMLDivElement>(null), row = useRef<HTMLDivElement>(null);
  const [layout, setLayout] = useState({ width: innerWidth, frame: Math.min(1124, innerWidth - 56), overhang: 28, edge: 0, cardWidth: 240, gap: 18 });
  const CARD_WIDTH = layout.cardWidth, GAP = layout.gap, STRIDE = CARD_WIDTH + GAP;
  const [published, setPublished] = useState<QuerySuggestion[]>([]), [center, setCenter] = useState<string>(), leans = useRef(new Map<string, number>());
  const [entering, setEntering] = useState<string[]>([]), lastReveal = useRef(0), pendingFocus = useRef<string>();
  const [clockReset, setClockReset] = useState(0);
  const [reducedMotion, setReducedMotion] = useState(() => matchMedia("(prefers-reduced-motion: reduce)").matches);
  const [paused, setPaused] = useState(reducedMotion), [interacting, setInteracting] = useState(false);
  const [movement, setMovement] = useState<Movement | null>(null), running = useRef<Movement | null>(null), latestItems = useRef(items);
  // Whole cards the belt has travelled so far in a movement, so cards crossing
  // into the reading area become usable while it is still moving.
  const [travelled, setTravelled] = useState(0), reached = useRef(0), actions = useRef<BeltActions | null>(null);
  latestItems.current = items;

  function finishMovement() {
    const moving = running.current;
    if (!moving) return;
    running.current = null;
    if (latestItems.current.some(item => item.instanceId === moving.target)) {
      setCenter(moving.target);
      if (moving.focus) pendingFocus.current = moving.target;
    }
    setMovement(null);
  }
  useLayoutEffect(() => {
    if (!active || !section.current || !heading.current) return;
    const scroller = section.current.closest("main");
    if (!scroller) return;
    const measure = () => {
      const frame = heading.current!.getBoundingClientRect(), host = section.current!.getBoundingClientRect(), main = scroller.getBoundingClientRect();
      if (!frame.width) return;
      finishMovement();
      const style = getComputedStyle(section.current!);
      setLayout({ width: scroller.clientWidth, frame: frame.width, overhang: frame.left - main.left, edge: main.left - host.left,
        cardWidth: parseFloat(style.getPropertyValue("--query-card-width")) || 240, gap: parseFloat(style.getPropertyValue("--query-card-gap")) || 18 });
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(scroller); observer.observe(heading.current);
    return () => observer.disconnect();
  }, [active]);
  useEffect(() => {
    const preference = matchMedia("(prefers-reduced-motion: reduce)");
    const change = () => { setReducedMotion(preference.matches); if (preference.matches) { setPaused(true); finishMovement(); } };
    preference.addEventListener("change", change);
    return () => preference.removeEventListener("change", change);
  }, []);
  useEffect(() => {
    if (!movement) return;
    // transitionend finishes the move normally. The deadline also handles a
    // tab hidden mid-transition or a browser that cancels the CSS transition.
    const timer = setTimeout(finishMovement, movement.duration + 50);
    let frame = requestAnimationFrame(function follow() {
      if (row.current) { reached.current = -Math.round(parseFloat(getComputedStyle(row.current).getPropertyValue("--belt-shift")) || 0); setTravelled(reached.current); }
      frame = requestAnimationFrame(follow);
    });
    return () => { clearTimeout(timer); cancelAnimationFrame(frame); };
  }, [movement]);
  useEffect(() => {
    const available = new Set(items.map(item => item.instanceId));
    const kept = published.filter(item => available.has(item.instanceId));
    if (kept.length !== published.length) {
      setPublished(kept);
      if (!kept.some(item => item.instanceId === center)) setCenter(kept[Math.max(0, Math.min(published.findIndex(item => item.instanceId === center), kept.length - 1))]?.instanceId);
      if (!kept.length) { running.current = null; setMovement(null); lastReveal.current = 0; setEntering([]); }
      return;
    }
    const known = new Set(published.map(item => item.instanceId)), pending = items.filter(item => !known.has(item.instanceId));
    if (!pending.length || !active || suspended) return;
    const lean = (item: QuerySuggestion, neighbors: QuerySuggestion[]) => {
      if (!leans.current.has(item.instanceId)) leans.current.set(item.instanceId, leanBeside(neighbors.map(neighbor => leans.current.get(neighbor.instanceId) ?? 0)));
    };
    if (!published.length) {
      const first = pending[0];
      lean(first, []);
      setPublished([first]); setCenter(first.instanceId); setEntering([first.instanceId]); lastReveal.current = performance.now();
      return;
    }
    if (interacting || movement) return;
    // The middle stays fixed: each ready pair extends the real belt by one
    // card to the left and one to the right. Each end takes the waiting card
    // least like its neighbors. An unpaired card fills the shorter side
    // without moving its peers or waiting for another preview to finish.
    const tail = published.slice(-SPACING).reverse(), head = published.slice(0, SPACING);
    const kinds = new Map([...pending, ...tail, ...head].map(item => [item, previewKind(item, previews[item.instanceId])]));
    const kindOf = (item: QuerySuggestion) => kinds.get(item)!;
    const right = chooseNext(pending, tail, kindOf)!, left = pending.length > 1 ? chooseNext(pending.filter(item => item !== right), head, kindOf) : undefined;
    const growth = growBelt(published, Math.max(0, published.findIndex(item => item.instanceId === center)), right, left);
    const timer = setTimeout(() => {
      if (growth.right) lean(growth.right, tail); if (growth.left) lean(growth.left, head);
      setPublished(growth.items);
      setEntering([growth.right, growth.left].flatMap(item => item ? [item.instanceId] : [])); lastReveal.current = performance.now();
    }, Math.max(0, lastReveal.current + REVEAL_MS - performance.now()));
    return () => clearTimeout(timer);
  }, [items, published, center, active, suspended, interacting, movement]);
  useEffect(() => {
    if (!entering.length) return;
    const timer = setTimeout(() => setEntering([]), REVEAL_MS - 20);
    return () => clearTimeout(timer);
  }, [entering]);
  useEffect(() => {
    if (movement || !pendingFocus.current) return;
    const id = pendingFocus.current; pendingFocus.current = undefined;
    const link = row.current?.querySelector<HTMLAnchorElement>(`[data-belt-position="0"] [data-card-id="${CSS.escape(id)}"] a`);
    link?.focus({ preventScroll: true });
  }, [center, movement]);

  // `offset` counts from the card the belt is leaving. A move asked for while
  // the belt slides retargets it: the transition continues from wherever the
  // belt has reached to the new destination.
  function slide(requested: number, user: boolean, focus = false) {
    if (published.length < 2) return;
    setClockReset(value => value + 1);
    const moving = running.current, belt = moving?.items ?? published, at = moving ? reached.current : 0, lead = MAX_LEAD_PAGES * count;
    const offset = Math.max(at - lead, Math.min(at + lead, requested));
    const index = moving?.center ?? Math.max(0, belt.findIndex(item => item.instanceId === center));
    const target = belt[wrap(index + offset, belt.length)].instanceId;
    if (reducedMotion) { if (focus) pendingFocus.current = target; setCenter(target); return; }
    if (moving?.offset === offset) { moving.focus ||= focus; return; }
    if (!moving) { if (!offset) return; reached.current = 0; setTravelled(0); }
    const next = { offset, center: index, target, focus, items: belt, duration: user ? USER_SLIDE_MS : AUTO_SLIDE_MS };
    running.current = next; setMovement(next);
  }
  const available = new Set(items.map(item => item.instanceId));
  const shown = movement?.items ?? published.filter(item => available.has(item.instanceId));
  const populating = loading || items.some(item => !published.some(known => known.instanceId === item.instanceId));
  const start = movement?.center ?? Math.max(0, shown.findIndex(item => item.instanceId === center));
  // An odd reading area leaves a permanent middle slot as pairs arrive: five
  // cards when the window also shows some of their neighbors, otherwise three,
  // or one in a narrow window.
  const span = (cards: number) => cards * STRIDE - GAP;
  const count = Math.min(shown.length, layout.width >= span(5) + 2 * PEEK ? 5 : layout.frame >= span(3) ? 3 : 1);
  const navigationStep = Math.floor((count + 1) / 2);
  const step = (direction: -1 | 1, user = true) => slide((running.current?.offset ?? 0) + direction * (user ? navigationStep : count), user);
  // Mid-slide the belt covers every card between where it is now and where it
  // is going, plus enough on each side to fill the viewport.
  const at = movement ? travelled : 0;
  const overscan = Math.ceil(Math.max(0, layout.width - (count * STRIDE - GAP)) / (2 * STRIDE)) + 3;
  const { first, last, positions } = beltSlots(shown.length, start, count, overscan, movement ? { at, offset: movement.offset } : undefined);
  const slots = positions.map(position => {
    const index = start + position, itemIndex = wrap(index, shown.length), seen = position - at, left = (layout.width - CARD_WIDTH) / 2 + seen * STRIDE;
    const item = shown[itemIndex];
    return { position, index, item, lean: leans.current.get(item.instanceId) ?? 0, cycle: Math.floor(index / shown.length), xray: seen < first || seen > last, onscreen: left < layout.width && left + CARD_WIDTH > 0 };
  });
  const canNavigate = published.length > count;
  actions.current = { open: onOpen, keep: onKeep, reveal: (index: number) => slide(index - start, true, true), dragStart: onDragStart, dragEnd: onDragEnd };
  // A slow preview must not hold the cards that are already ready to browse.
  const held = paused || interacting || suspended || Boolean(movement) || !canNavigate;
  return <section ref={section} className="home-conveyor" aria-label="Dashboard Catalog" aria-busy={populating} tabIndex={-1}
    style={{ "--belt-viewport-width": `${layout.width}px`, "--conveyor-edge": `${layout.edge}px`, "--query-card-stride": `${STRIDE}px` } as React.CSSProperties}
    onKeyDown={event => {
      if (!event.altKey && !event.metaKey && !event.ctrlKey && !event.shiftKey && ["ArrowLeft", "ArrowRight"].includes(event.key)) {
        event.preventDefault(); if (canNavigate) { section.current?.focus({ preventScroll: true }); step(event.key === "ArrowLeft" ? -1 : 1); }
      }
    }}>
    <div ref={heading} className="home-conveyor-head"><h2><Icon name="compass" />Dashboard Catalog</h2><div className="home-conveyor-actions">
      <button type="button" className="home-all-cards" onClick={onAllCards}>All Cards<Icon name="arrow-right" /></button>
      <CarouselCountdown held={held} paused={paused} reset={clockReset} advance={() => step(1, false)} /><div className="home-conveyor-controls">
      <button type="button" className="home-play" aria-label={paused ? "Play suggestions" : "Pause suggestions"} onClick={() => setPaused(value => !value)}>{paused ? "▶ Play" : "Ⅱ Pause"}</button>
      <button type="button" aria-label={`Previous ${counted(navigationStep)} suggestions`} disabled={!canNavigate} onClick={() => step(-1)}><Icon name="chevron-left" /></button>
      <button type="button" aria-label={`Next ${counted(navigationStep)} suggestions`} disabled={!canNavigate} onClick={() => step(1)}><Icon name="chevron-right" /></button>
      <button type="button" aria-label="Refresh suggestions" onClick={() => { running.current = null; pendingFocus.current = undefined; leans.current.clear(); setMovement(null); setPublished([]); setCenter(undefined); setEntering([]); lastReveal.current = 0; setClockReset(value => value + 1); onRefresh(); }}><Icon name="refresh" /></button>
    </div></div></div>
    <div className="home-belt-viewport" style={{ width: layout.width, marginLeft: -layout.overhang }}
      onMouseEnter={() => setInteracting(true)} onMouseLeave={() => setInteracting(false)} onFocus={() => setInteracting(true)} onBlur={event => { if (!event.currentTarget.contains(event.relatedTarget as Node)) setInteracting(false); }}>
      <div ref={row} className={`home-belt-row${movement ? " sliding" : ""}`} aria-busy={Boolean(movement)}
        style={{ left: (layout.width - CARD_WIDTH) / 2 + (positions[0] ?? 0) * STRIDE, "--belt-start": first, "--belt-end": last, "--belt-shift": movement ? -movement.offset : 0, "--belt-duration": `${movement?.duration ?? AUTO_SLIDE_MS}ms` } as React.CSSProperties}
        onTransitionEnd={event => { if (event.target === row.current && event.propertyName === "--belt-shift") finishMovement(); }}>
        {slots.map(({ position, index, item, lean, cycle, xray, onscreen }) => <div className={`home-belt-slot${entering.includes(item.instanceId) ? " arriving" : ""}`} key={`${item.instanceId}:${cycle}`} data-belt-position={position} style={{ "--belt-index": position } as React.CSSProperties}>
          <BeltCard item={item} preview={previews[item.instanceId]} xray={xray} lean={lean} index={index} revealable={onscreen} actions={actions} />
        </div>)}
      </div>
      {!items.length ? <p className="home-discovery-empty">{loading ? "Looking for interesting angles in your library…" : hasErrors ? "Some previews could not load. Refresh to try again." : "No matching angles in these recent windows yet. As your library grows, new views will appear here."}<a href="/library" onClick={event => { event.preventDefault(); onOpen("/library"); }}>Explore the library <Icon name="arrow-right" /></a></p> : null}
    </div>
  </section>;
}
