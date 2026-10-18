import React from "react";

export type LegendItem = { key: string; label: string; color: string; detail?: string };

// The same markup, swatches, typography and responsive columns serve the
// stacked timeline and composition charts.
export function ChartLegend({ items, columns = 2 }: { items: LegendItem[]; columns?: 1 | 2 | 3 }) {
  return <span className={`query-timeline-legend query-chart-legend${columns === 1 ? " is-single-column" : columns === 3 ? " is-three-column" : ""}`}>
    {items.map(item => <span key={item.key} data-chart-tooltip={item.detail ?? item.label} data-chart-colors={JSON.stringify([item.color])}>
      <i style={{ background: item.color }} /><span>{item.label}</span>
    </span>)}
  </span>;
}
