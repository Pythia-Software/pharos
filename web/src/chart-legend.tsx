import React from "react";

export type LegendItem = { key: string; label: string; color: string; detail?: string };

// The same markup, swatches, typography and responsive columns serve the
// stacked timeline and composition charts. Unless one or three columns are
// asked for, the key uses three columns when the longest label fits a third
// of the width, and two otherwise.
export function ChartLegend({ items, columns }: { items: LegendItem[]; columns?: 1 | 2 | 3 }) {
  const longest = Math.max(1, ...items.map(item => item.label.length));
  return <span className={`query-timeline-legend query-chart-legend${columns === 1 ? " is-single-column" : columns === 3 ? " is-three-column" : " is-auto-columns"}`} style={columns === 1 || columns === 3 ? undefined : { "--legend-label": `${longest}ch` } as React.CSSProperties}>
    {items.map(item => <span key={item.key} data-chart-tooltip={item.detail ?? item.label} data-chart-colors={JSON.stringify([item.color])}>
      <i style={{ background: item.color }} /><span>{item.label}</span>
    </span>)}
  </span>;
}
