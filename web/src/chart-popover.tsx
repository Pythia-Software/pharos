import React, { forwardRef } from "react";
import { PageLink } from "./nested-pages";

// Shared detail surface for dashboard timelines and standalone charts.
export const ChartPopover = forwardRef<HTMLDivElement, {
  heading: string; pinned: boolean; className?: string; style?: React.CSSProperties; children: React.ReactNode;
}>(function ChartPopover({ heading, pinned, className = "", style, children }, ref) {
  return <div className={`usage-chart-tip${pinned ? " pinned" : ""} ${className}`} ref={ref} style={style}
    role={pinned ? "group" : "status"} aria-label={pinned ? `Details for ${heading}` : undefined}>
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
