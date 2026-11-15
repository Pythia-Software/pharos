import React from "react";
import { gridGaps, type GridCover } from "./grid-cover";

// Sequential cells shade toward the sea; diverging ones toward the sea below
// their reference and toward warning above it, as the dashboards do.
const shade = (value: number, tone: GridCover["tone"]) => tone === "diverging"
  ? `color-mix(in srgb,${value >= 0 ? "var(--warn)" : "var(--sea)"} ${Math.round(Math.min(1, Math.abs(value)) * 90)}%,var(--panel))`
  : `color-mix(in srgb,var(--sea) ${Math.round(Math.max(0, Math.min(1, value)) * 88)}%,var(--panel))`;
// Long labels keep both ends: a model and its effort, a harness and its client.
const clip = (label: string) => label.length > 18 ? label.slice(0, 9) + "…" + label.slice(-8) : label;

// The cells stretch to fill the cover, whatever its size; labels keep their
// type size and take no more room than they need.
export function CompactGrid({ cover }: { cover: GridCover }) {
  const rows = new Set(cover.rowTicks), columns = new Set(cover.columnTicks), gaps = gridGaps(cover);
  return <span className="compact-grid">
    <span className="compact-grid-plot" role="img" aria-label={`${cover.rows.length} by ${cover.columns.length} grid`}
      style={{ gridTemplateColumns: `max-content repeat(${cover.columns.length},minmax(0,1fr))`, gridTemplateRows: `repeat(${cover.rows.length},minmax(0,1fr)) auto`, columnGap: gaps.column, rowGap: gaps.row }}>
      {cover.cells.flatMap((row, r) => [
        <span key={`r${r}`} className="compact-grid-label">{rows.has(r) ? clip(cover.rows[r]) : null}</span>,
        ...row.map((value, c) => <span key={`${r}|${c}`} className="compact-grid-cell" style={value === null ? undefined : { background: shade(value, cover.tone) }}
          data-chart-tooltip={value === null ? undefined : `${cover.rows[r]} · ${cover.columns[c]}\n${cover.values[r]?.[c] ?? ""}`} />),
      ])}
      <span />
      {cover.columns.map((label, c) => <span key={`c${c}`} className={`compact-grid-tick${c === 0 ? " is-first" : c === cover.columns.length - 1 ? " is-last" : ""}`}>{columns.has(c) ? label : null}</span>)}
    </span>
    <span className="compact-grid-key">
      <span className="compact-grid-legend"><span>{cover.legend[0]}</span><i className={`compact-grid-ramp compact-grid-${cover.tone}`} /><span>{cover.legend[1]}</span></span>
      {cover.caption ? <span className="compact-grid-caption" data-overflow-title="">{cover.caption}</span> : null}
    </span>
  </span>;
}
