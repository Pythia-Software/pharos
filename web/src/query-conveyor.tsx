import React, { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { QueryPreview, QuerySuggestion } from "./curiosity-data";
import { QueryCard } from "./query-card";
import { Icon } from "./icons";

const CARD_WIDTH = 172, GAP = 18, STRIDE = CARD_WIDTH + GAP, SLIDE_MS = 2040;
const REVEAL_MS = 340;
const leans = [-1.8, 1.3, -1, 1.9, -.9, 1.2];
const leanFor = (id: string) => leans[Array.from(id).reduce((hash, character) => (hash * 31 + character.charCodeAt(0)) >>> 0, 0) % leans.length];
const wrap = (index: number, count: number) => ((index % count) + count) % count;
type Movement = { offset: number; center: number; target: string; focus: boolean; items: QuerySuggestion[] };

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

export function QueryConveyor({ items, previews, loading, hasErrors, active, suspended, onRefresh, onOpen, onKeep, onDragStart, onDragEnd }: {
  items: QuerySuggestion[]; previews: Record<string, QueryPreview>; loading: boolean; hasErrors: boolean;
  active: boolean; suspended: boolean; onRefresh: () => void; onOpen: (url: string) => void; onKeep: (item: QuerySuggestion) => void;
  onDragStart: (event: React.DragEvent, item: QuerySuggestion) => void; onDragEnd: () => void;
}) {
  const section = useRef<HTMLElement>(null), heading = useRef<HTMLDivElement>(null), row = useRef<HTMLDivElement>(null);
  const [layout, setLayout] = useState({ width: innerWidth, frame: Math.min(1124, innerWidth - 56), overhang: 28, edge: 0 });
  const [published, setPublished] = useState<QuerySuggestion[]>([]), [center, setCenter] = useState<string>();
  const [entering, setEntering] = useState<string[]>([]), lastReveal = useRef(0), pendingFocus = useRef<string>();
  const [clockReset, setClockReset] = useState(0);
  const [reducedMotion, setReducedMotion] = useState(() => matchMedia("(prefers-reduced-motion: reduce)").matches);
  const [paused, setPaused] = useState(reducedMotion), [interacting, setInteracting] = useState(false);
  const [movement, setMovement] = useState<Movement | null>(null), running = useRef<Movement | null>(null), latestItems = useRef(items);
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
      setLayout({ width: scroller.clientWidth, frame: frame.width, overhang: frame.left - main.left, edge: main.left - host.left });
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
    const timer = setTimeout(finishMovement, SLIDE_MS + 50);
    return () => clearTimeout(timer);
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
    if (!published.length) {
      const first = pending[0];
      setPublished([first]); setCenter(first.instanceId); setEntering([first.instanceId]); lastReveal.current = performance.now();
      return;
    }
    if (interacting || movement || pending.length < 2 && loading) return;
    const timer = setTimeout(() => {
      const pair = pending.slice(0, 2);
      // The middle stays fixed: each ready pair extends the real belt by one
      // card to the left and one to the right. A final unpaired card waits for
      // loading to finish, then joins at the outer end without moving its peers.
      setPublished(current => pair.length === 2 ? [pair[0], ...current, pair[1]] : [...current, pair[0]]);
      setEntering(pair.map(item => item.instanceId)); lastReveal.current = performance.now();
    }, Math.max(0, lastReveal.current + REVEAL_MS - performance.now()));
    return () => clearTimeout(timer);
  }, [items, published, center, loading, active, suspended, interacting, movement]);
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

  function move(offset: number, focus = false) {
    if (running.current || published.length < 2) return;
    setClockReset(value => value + 1);
    const index = Math.max(0, published.findIndex(item => item.instanceId === center));
    const target = published[wrap(index + offset, published.length)].instanceId;
    if (reducedMotion) { if (focus) pendingFocus.current = target; setCenter(target); return; }
    const moving = { offset, center: index, target, focus, items: published };
    running.current = moving; setMovement(moving);
  }
  const available = new Set(items.map(item => item.instanceId));
  const shown = movement?.items ?? published.filter(item => available.has(item.instanceId));
  const populating = loading || items.some(item => !published.some(known => known.instanceId === item.instanceId));
  const start = movement?.center ?? Math.max(0, shown.findIndex(item => item.instanceId === center));
  // An odd reading area leaves a permanent middle slot as pairs arrive.
  const capacity = Math.max(1, Math.floor((layout.frame + GAP) / STRIDE));
  const count = Math.min(shown.length, capacity % 2 ? capacity : capacity - 1);
  const first = -Math.floor((count - 1) / 2), last = Math.ceil((count - 1) / 2);
  const cycling = Boolean(movement) || !populating && shown.length > count;
  const overscan = Math.ceil(Math.max(0, layout.width - (count * STRIDE - GAP)) / (2 * STRIDE)) + Math.max(3, Math.abs(movement?.offset ?? 0));
  const positions = !shown.length ? [] : cycling ? Array.from({ length: count + 2 * overscan }, (_, index) => first - overscan + index) : shown.map((_, index) => index - start);
  const slots = positions.map(position => {
    const index = start + position, itemIndex = wrap(index, shown.length), left = (layout.width - CARD_WIDTH) / 2 + position * STRIDE;
    const item = shown[itemIndex];
    return { position, item, lean: leanFor(item.instanceId), cycle: Math.floor(index / shown.length), onscreen: left < layout.width && left + CARD_WIDTH > 0 };
  });
  const canNavigate = published.length > count && !movement;
  const held = paused || interacting || suspended || Boolean(movement) || populating || !canNavigate;
  return <section ref={section} className="home-conveyor" aria-label="Different angles on your work" aria-busy={populating} tabIndex={-1}
    style={{ "--belt-viewport-width": `${layout.width}px`, "--conveyor-edge": `${layout.edge}px` } as React.CSSProperties}
    onKeyDown={event => {
      if (!event.altKey && !event.metaKey && !event.ctrlKey && !event.shiftKey && ["ArrowLeft", "ArrowRight"].includes(event.key)) {
        event.preventDefault(); if (canNavigate) { section.current?.focus({ preventScroll: true }); move(event.key === "ArrowLeft" ? -3 : 3); }
      }
    }}>
    <div ref={heading} className="home-conveyor-head"><h2><Icon name="compass" />Different angles on your work</h2><div className="home-conveyor-actions">
      <CarouselCountdown held={held} paused={paused} reset={clockReset} advance={() => move(3)} /><div className="home-conveyor-controls">
      <button type="button" className="home-play" aria-label={paused ? "Play suggestions" : "Pause suggestions"} onClick={() => setPaused(value => !value)}>{paused ? "▶ Play" : "Ⅱ Pause"}</button>
      <button type="button" aria-label="Previous three suggestions" disabled={!canNavigate} onClick={() => move(-3)}><Icon name="chevron-left" /></button>
      <button type="button" aria-label="Next three suggestions" disabled={!canNavigate} onClick={() => move(3)}><Icon name="chevron-right" /></button>
      <button type="button" aria-label="Refresh suggestions" onClick={() => { running.current = null; pendingFocus.current = undefined; setMovement(null); setPublished([]); setCenter(undefined); setEntering([]); lastReveal.current = 0; setClockReset(value => value + 1); onRefresh(); }}><Icon name="refresh" /></button>
    </div></div></div>
    <div className="home-belt-viewport" style={{ width: layout.width, marginLeft: -layout.overhang }}
      onMouseEnter={() => setInteracting(true)} onMouseLeave={() => setInteracting(false)} onFocus={() => setInteracting(true)} onBlur={event => { if (!event.currentTarget.contains(event.relatedTarget as Node)) setInteracting(false); }}>
      <div ref={row} className={`home-belt-row${movement ? " sliding" : ""}`} aria-busy={Boolean(movement)}
        style={{ left: (layout.width - CARD_WIDTH) / 2 + (positions[0] ?? 0) * STRIDE, "--belt-start": first, "--belt-end": last, "--belt-shift": movement ? -movement.offset : 0 } as React.CSSProperties}
        onTransitionEnd={event => { if (event.target === row.current && event.propertyName === "--belt-shift") finishMovement(); }}>
        {slots.map(({ position, item, lean, cycle, onscreen }) => <div className={`home-belt-slot${entering.includes(item.instanceId) ? " arriving" : ""}`} key={`${item.instanceId}:${cycle}`} data-belt-position={position} style={{ "--belt-index": position } as React.CSSProperties}>
          <QueryCard discovery xray={position < first || position > last} id={item.instanceId} title={item.title} description={item.description} url={item.url} windowLabel={`${item.window.days}d`} category={item.category} lean={lean}
            preview={previews[item.instanceId]} onReveal={onscreen && !movement ? () => move(position, true) : undefined} onOpen={() => onOpen(item.url)} onKeep={() => onKeep(item)} onDragStart={event => onDragStart(event, item)} onDragEnd={onDragEnd} />
        </div>)}
      </div>
      {!items.length ? <p className="home-discovery-empty">{loading ? "Looking for interesting angles in your library…" : hasErrors ? "Some previews could not load. Refresh to try again." : "No matching angles in these recent windows yet. As your library grows, new views will appear here."}<a href="/library" onClick={event => { event.preventDefault(); onOpen("/library"); }}>Explore the library <Icon name="arrow-right" /></a></p> : null}
    </div>
  </section>;
}
