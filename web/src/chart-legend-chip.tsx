import React, { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import type { ChartSeries } from "./chart-series";
import { legendMenuItems, type LegendAction } from "./chart-selection";
import { Icon } from "./icons";

// Shared positioning, dismissal and keyboard navigation for value and preset menus.
export function LegendMenu({ name, label, menuLabel, title, heading, marker, suffix, active, dimmed = false, mixed = false, category = false, iconOnly = false, description, readOnly = false, onActivate, children }: {
  name: string; label: React.ReactNode; menuLabel: string; title: string; heading: React.ReactNode;
  marker?: React.ReactNode; suffix?: React.ReactNode; active?: boolean; dimmed?: boolean; mixed?: boolean; category?: boolean;
  iconOnly?: boolean; description?: string; readOnly?: boolean; onActivate?: () => void;
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
  return <span className={`usage-legend-chip${category ? " is-category" : ""}${iconOnly ? " is-icon-only" : ""}`}>
    <button ref={trigger} type="button" className={`usage-legend-item${category ? " usage-legend-category" : ""}${dimmed ? " dimmed" : ""}`} aria-label={name} aria-pressed={mixed ? "mixed" : active} aria-haspopup={readOnly ? undefined : "menu"} aria-expanded={readOnly ? undefined : open} aria-controls={!readOnly && open ? menuId : undefined}
      title={iconOnly ? undefined : title} aria-description={description} data-chart-tooltip={iconOnly && !open ? [typeof label === "string" ? label : name,description].filter(Boolean).join("\n") : undefined} onClick={() => { if(readOnly){onActivate?.();return;} firstFocus.current = 0; setOpen(current => !current); }} onKeyDown={event => {
        if(readOnly)return;
        if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
        event.preventDefault();
        firstFocus.current = event.key === "ArrowUp" ? -1 : 0;
        setOpen(true);
      }}>
      {marker}{iconOnly ? null : <>{label}{suffix}<Icon name="chevron-down" className="usage-legend-caret" /></>}
    </button>
    {!readOnly && open ? <div ref={menu} id={menuId} className="usage-legend-menu" role="menu" aria-label={menuLabel} onKeyDown={event => {
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

// Shared visibility actions, with the same counts and read-only icon behavior as Timeline.
export function ChartLegendChip({ item, active, dimmed, selectionActive, onAction, groupSize, mixed = false, swatch, suffix, visibleCount, hiddenCount, iconOnly = false, description, readOnly = false, onActivate }: {
  item: ChartSeries; active: boolean; dimmed: boolean; selectionActive: boolean; onAction: (action: LegendAction) => void;
  groupSize?: number; mixed?: boolean; swatch?: React.ReactNode; suffix?: React.ReactNode; visibleCount?: number; hiddenCount?: number;
  iconOnly?: boolean; description?: string; readOnly?: boolean; onActivate?: () => void;
}) {
  const options = legendMenuItems(visibleCount ?? (dimmed ? 0 : 1), hiddenCount ?? (dimmed ? 1 : 0), selectionActive);
  const category = groupSize !== undefined, name = category ? `${item.label} category` : item.label;
  const marker = swatch === undefined ? !category ? <span className="usage-swatch" style={{ background: item.color }} /> : null : swatch;
  return <LegendMenu name={name} label={item.label} menuLabel={`Filter ${name}`} title="Filter this series"
    heading={<>{marker}{item.label}{category ? ` · ${groupSize} ${groupSize === 1 ? 'label' : 'labels'}` : ''}</>}
    marker={marker} suffix={suffix} active={active} dimmed={dimmed} mixed={mixed} category={category}
    iconOnly={iconOnly} description={description} readOnly={readOnly} onActivate={onActivate}>
    {choose => <>{options.map(option => <React.Fragment key={option.action}>
      {option.separator ? <hr className="usage-legend-menu-separator" role="separator" /> : null}
      <button type="button" role="menuitem" tabIndex={-1} onClick={() => choose(() => onAction(option.action))}>{option.label}</button>
    </React.Fragment>)}</>}
  </LegendMenu>;
}
