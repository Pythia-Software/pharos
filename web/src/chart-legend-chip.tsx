import React, { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import type { ChartSeries } from "./chart-series";
import type { LegendAction } from "./chart-selection";
import type { PopulationAction } from "./dashboard-population";
import { Icon } from "./icons";

// Shared positioning, dismissal and keyboard navigation for value and preset menus.
export function LegendMenu({ name, label, menuLabel, title, heading, marker, suffix, active, dimmed = false, mixed = false, category = false, children }: {
  name: string; label: React.ReactNode; menuLabel: string; title: string; heading: React.ReactNode;
  marker?: React.ReactNode; suffix?: React.ReactNode; active?: boolean; dimmed?: boolean; mixed?: boolean; category?: boolean;
  children: (choose: (action: () => void) => void) => React.ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null), menu = useRef<HTMLDivElement>(null);
  const firstFocus = useRef(0);
  const menuId = useId();
  function close(restoreFocus: boolean) {
    setOpen(false);
    if (restoreFocus) trigger.current?.focus();
  }
  useLayoutEffect(() => {
    if (!open || !menu.current || !trigger.current) return;
    const panel = menu.current, anchor = trigger.current.getBoundingClientRect();
    const left = Math.max(8, Math.min(anchor.left, window.innerWidth - panel.offsetWidth - 8));
    const below = anchor.bottom + 6;
    const preferredTop = below + panel.offsetHeight <= window.innerHeight - 8 ? below : anchor.top - panel.offsetHeight - 6;
    const top = Math.max(8, Math.min(preferredTop, window.innerHeight - panel.offsetHeight - 8));
    panel.style.left = `${left - anchor.left}px`;
    panel.style.top = `${top - anchor.top}px`;
    const entries = panel.querySelectorAll<HTMLButtonElement>('[role^="menuitem"]:not(:disabled)');
    entries[firstFocus.current === -1 ? entries.length - 1 : 0]?.focus();
  }, [open]);
  useEffect(() => {
    if (!open) return;
    const outside = (event: Event) => {
      const target = event.target as Node;
      if (!trigger.current?.contains(target) && !menu.current?.contains(target)) close(false);
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      close(true);
    };
    const dismiss = (event: Event) => {
      if (event.target instanceof Node && menu.current?.contains(event.target)) return;
      close(false);
    };
    document.addEventListener("pointerdown", outside);
    document.addEventListener("focusin", outside);
    document.addEventListener("keydown", escape);
    window.addEventListener("resize", dismiss);
    window.addEventListener("scroll", dismiss, true);
    return () => {
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("focusin", outside);
      document.removeEventListener("keydown", escape);
      window.removeEventListener("resize", dismiss);
      window.removeEventListener("scroll", dismiss, true);
    };
  }, [open]);
  function choose(action: () => void) { close(true); action(); }
  return <span className={`usage-legend-chip${category ? " is-category" : ""}`}>
    <button ref={trigger} type="button" className={`usage-legend-item${category ? " usage-legend-category" : ""}${dimmed ? " dimmed" : ""}`} aria-label={name} aria-pressed={mixed ? "mixed" : active} aria-haspopup="menu" aria-expanded={open} aria-controls={open ? menuId : undefined}
      title={title} onClick={() => { firstFocus.current = 0; setOpen(current => !current); }} onKeyDown={event => {
        if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
        event.preventDefault();
        firstFocus.current = event.key === "ArrowUp" ? -1 : 0;
        setOpen(true);
      }}>
      {marker}{label}{suffix}<Icon name="chevron-down" className="usage-legend-caret" />
    </button>
    {open ? <div ref={menu} id={menuId} className="usage-legend-menu" role="menu" aria-label={menuLabel} onKeyDown={event => {
      const entries = [...(menu.current?.querySelectorAll<HTMLButtonElement>('[role^="menuitem"]:not(:disabled)') ?? [])];
      const index = entries.indexOf(document.activeElement as HTMLButtonElement);
      if (event.key === "Tab") { close(true); return; }
      if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      const next = event.key === "Home" ? 0 : event.key === "End" ? entries.length - 1 : (index + (event.key === "ArrowDown" ? 1 : -1) + entries.length) % entries.length;
      entries[next]?.focus();
    }}>
      <div className="usage-legend-menu-heading" data-overflow-title="">{heading}</div>
      {children(choose)}
    </div> : null}
  </span>;
}

// Value actions change the drawn series; population actions filter every view.
export function ChartLegendChip({ item, active, dimmed, selectionActive, soleSelection, onAction, groupSize, mixed = false, swatch, suffix, onPopulation }: { item: ChartSeries; active: boolean; dimmed: boolean; selectionActive: boolean; soleSelection: boolean; onAction: (action: LegendAction) => void; groupSize?: number; mixed?: boolean; swatch?: React.ReactNode; suffix?: React.ReactNode; onPopulation?: (action: PopulationAction) => void }) {
  const remove = selectionActive && active;
  const category = groupSize !== undefined, name = category ? `${item.label} category` : item.label;
  const marker = swatch === undefined ? !category ? <span className="usage-swatch" style={{ background: item.color }} /> : null : swatch;
  return <LegendMenu name={name} label={item.label} menuLabel={`Filter ${name}`} title="Filter this series"
    heading={<>{marker}{item.label}{category ? ` · ${groupSize} ${groupSize === 1 ? 'label' : 'labels'}` : ''}</>}
    marker={marker} suffix={suffix} active={active} dimmed={dimmed} mixed={mixed} category={category}>
    {choose => <>
      {/* With nothing selected, adding to the selection is the same as showing only this value. */}
      <button type="button" role="menuitem" tabIndex={-1} onClick={() => choose(() => onAction("only"))}><span>{category ? "Show only this category" : "Show only this value"}</span><small>{selectionActive ? "Replace the current selection" : "Then add others from their menus"}</small></button>
      {selectionActive ? <button type="button" role="menuitem" tabIndex={-1} onClick={() => choose(() => onAction("toggle"))}><span>{category ? remove ? "Remove all from selection" : "Add all to selection" : remove ? "Remove from selection" : "Add to selection"}</span><small>{soleSelection ? "Show all values again" : "Keep the rest of your selection"}</small></button> : null}
      <button type="button" role="menuitem" tabIndex={-1} onClick={() => choose(() => onAction("exclude"))}><span>{category ? "Show all except this category" : "Show all except this value"}</span><small>{category ? "Include every label outside this category" : "Include every other value, even in Other"}</small></button>
      <button type="button" role="menuitem" tabIndex={-1} className="usage-legend-menu-reset" disabled={!selectionActive} onClick={() => choose(() => onAction("clear"))}>Show all values</button>
      {onPopulation ? <>
        <button type="button" role="menuitem" tabIndex={-1} className="usage-legend-menu-population" onClick={() => choose(() => onPopulation!("keep"))}><span>Count only this value</span><small>Population filter: every view and breakdown</small></button>
        <button type="button" role="menuitem" tabIndex={-1} onClick={() => choose(() => onPopulation!("drop"))}><span>Leave this value out</span><small>Population filter: every view and breakdown</small></button>
      </> : null}
    </>}
  </LegendMenu>;
}
