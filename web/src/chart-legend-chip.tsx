import React, { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import type { ChartSeries } from "./chart-series";
import type { LegendAction } from "./chart-selection";

export function ChartLegendChip({ item, active, dimmed, selectionActive, soleSelection, onAction }: { item: ChartSeries; active: boolean; dimmed: boolean; selectionActive: boolean; soleSelection: boolean; onAction: (action: LegendAction) => void }) {
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
    const entries = panel.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)');
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
  function choose(action: LegendAction) {
    close(true);
    onAction(action);
  }
  const remove = selectionActive && active;
  return <span className="usage-legend-chip">
    <button ref={trigger} type="button" className={`usage-legend-item${dimmed ? " dimmed" : ""}`} aria-label={item.label} aria-pressed={active} aria-haspopup="menu" aria-expanded={open} aria-controls={open ? menuId : undefined}
      title={`Filter ${item.label}`} onClick={() => { firstFocus.current = 0; setOpen(current => !current); }} onKeyDown={event => {
        if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
        event.preventDefault();
        firstFocus.current = event.key === "ArrowUp" ? -1 : 0;
        setOpen(true);
      }}>
      <span className="usage-swatch" style={{ background: item.color }} />{item.label}<span className="usage-legend-caret" aria-hidden="true">▾</span>
    </button>
    {open ? <div ref={menu} id={menuId} className="usage-legend-menu" role="menu" aria-label={`Filter ${item.label}`} onKeyDown={event => {
      const entries = [...(menu.current?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)') ?? [])];
      const index = entries.indexOf(document.activeElement as HTMLButtonElement);
      if (event.key === "Tab") { close(true); return; }
      if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      const next = event.key === "Home" ? 0 : event.key === "End" ? entries.length - 1 : (index + (event.key === "ArrowDown" ? 1 : -1) + entries.length) % entries.length;
      entries[next]?.focus();
    }}>
      <div className="usage-legend-menu-heading" title={item.label}><span className="usage-swatch" style={{ background: item.color }} />{item.label}</div>
      <button type="button" role="menuitem" tabIndex={-1} onClick={() => choose("only")}><span>Show only this value</span><small>Replace the current selection</small></button>
      <button type="button" role="menuitem" tabIndex={-1} onClick={() => choose("toggle")}><span>{remove ? "Remove from selection" : "Add to selection"}</span><small>{soleSelection ? "Show all values again" : selectionActive ? "Keep the rest of your selection" : "Start a multi-value selection"}</small></button>
      <button type="button" role="menuitem" tabIndex={-1} onClick={() => choose("exclude")}><span>Show all except this value</span><small>Include every other value, even in Other</small></button>
      <button type="button" role="menuitem" tabIndex={-1} className="usage-legend-menu-reset" disabled={!selectionActive} onClick={() => choose("clear")}>Show all values</button>
    </div> : null}
  </span>;
}
