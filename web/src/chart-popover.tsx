import React, { forwardRef } from "react";
import { PageLink } from "./nested-pages";
import type { ChartPopoverContent } from "./chart-popover-model";
import "./chart-popover.css";

// Shared detail surface for dashboard timelines, standalone charts and the
// hover bubble on cards (ChartTooltip). A pinned popover is a labelled group.
export const ChartPopover = forwardRef<HTMLDivElement, {
  heading: React.ReactNode; label?: string; pinned?: boolean; role?: string; className?: string; style?: React.CSSProperties; children?: React.ReactNode;
}>(function ChartPopover({ heading, label, pinned = false, role, className = "", style, children }, ref) {
  return <div className={`usage-chart-tip${pinned ? " pinned" : ""} ${className}`} ref={ref} style={style}
    role={role ?? (pinned ? "group" : "status")} aria-label={pinned ? `Details for ${label ?? heading}` : undefined}>
    <div className="usage-chart-tip-head">{heading}</div>
    {children}
  </div>;
});

export function ChartPopoverRow({ label, value, color, href }: { label: string; value: string; color?: string; href?: string }) {
  return <span className="usage-chart-tip-row">
    {color ? <span className="usage-swatch" style={{ background: color }} /> : null}
    {href ? <PageLink className="usage-chart-tip-label" href={href}>{label}</PageLink> : <span className="usage-chart-tip-label">{label}</span>}
    <span className="usage-chart-tip-value">{value}</span>
  </span>;
}

// One period's rows: notes under the heading, then each series with the
// largest parts of a folded series, then totals set off below a rule.
export function ChartPopoverDetails({ notes, rows, totals, detail, empty }: Omit<ChartPopoverContent, "heading">) {
  return <>
    {notes?.map(note => <div key={note} className="usage-chart-tip-more">{note}</div>)}
    {rows.length ? <ul>{rows.map((row, index) => <li key={index}>
      <ChartPopoverRow label={row.label} value={row.value} color={row.color} href={row.href} />
      {row.parts?.length ? <ul className="usage-chart-tip-parts">
        {row.parts.map(([name, value]) => <li key={name} className="usage-chart-tip-row"><span className="usage-chart-tip-label">{name}</span><span className="usage-chart-tip-value">{value}</span></li>)}
        {row.more ? <li className="usage-chart-tip-more">+{row.more} more</li> : null}
      </ul> : null}
    </li>)}</ul> : empty ? <div className="usage-chart-tip-more">{empty}</div> : null}
    {totals?.map(([label, value]) => <div key={label} className="usage-chart-tip-row usage-chart-tip-total"><span className="usage-chart-tip-label">{label}</span><span className="usage-chart-tip-value">{value}</span></div>)}
    {detail ? <div className="usage-chart-tip-more">{detail}</div> : null}
  </>;
}
