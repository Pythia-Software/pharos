import React, { useId } from "react";
import { CompactColumns } from "./column-marks";
import { ChartLegend } from "./chart-legend";
import { pieWedgePath } from "./compact-pie-model";
import type { ChartBucket, ChartSeries } from "./chart-series";
import { HumanStudyCover } from "./human-study-cover";
import type { HumanStudyPreview } from "./human-study";

// Draft covers render calculations that are not Query Table fields yet (carbon,
// machine timing, findings, library health). Each mark carries its exact value
// and population in the shared tooltip, like the native covers.
export type DraftRow = { label: string; value: number; display: string; detail?: string; color?: string };
export type DraftStat = { label: string; value: string; detail?: string; tone?: "good" | "warn" | "bad" };
export type DraftCoverData =
  | { kind: "trend"; study: HumanStudyPreview }
  | { kind: "rows"; rows: DraftRow[]; footnote?: string }
  | { kind: "pie"; rows: DraftRow[]; legendColumns?: 1 | 2 | 3; footnote?: string }
  | { kind: "columns"; buckets: ChartBucket[]; series: ChartSeries[]; unit: string; yLabel?: string; legendColumns?: 1 | 2 | 3; footnote?: string }
  | { kind: "hours"; values: number[]; format: (value: number) => string; detail: string; timeZone: string; footnote?: string }
  | { kind: "stats"; stats: DraftStat[]; footnote?: string }
  | { kind: "value"; value: string; label: string; detail?: string; footnote?: string };

export function DraftCover({ cover }: { cover: DraftCoverData }) {
  const id = useId().replace(/:/g, "");
  if (cover.kind === "trend") return <HumanStudyCover study={cover.study} />;
  const footnote = cover.footnote ? <span className="draft-cover-footnote">{cover.footnote}</span> : null;
  if (cover.kind === "columns") return <span className="draft-cover is-columns"><CompactColumns buckets={cover.buckets} series={cover.series} unit={cover.unit} yLabel={cover.yLabel} showLegend={cover.series.length > 1} legendColumns={cover.legendColumns} />{footnote}</span>;
  if (cover.kind === "value") return <span className="draft-cover is-value"><span className="human-study-headline query-stat-line" data-chart-tooltip={cover.detail} title=""><strong>{cover.value}</strong><span>{cover.label}</span></span>{footnote}</span>;
  if (cover.kind === "stats") return <span className="draft-cover is-stats"><span className="draft-stats">{cover.stats.map(stat => <span key={stat.label} className={`draft-stat${stat.tone ? ` is-${stat.tone}` : ""}`} data-chart-tooltip={stat.detail ? `${stat.label}\n${stat.value}\n${stat.detail}` : undefined} title=""><small>{stat.label}</small><strong>{stat.value}</strong></span>)}</span>{footnote}</span>;
  if (cover.kind === "hours") {
    const max = Math.max(...cover.values, 0) || 1;
    return <span className="draft-cover is-hours"><span className="human-study-hour-bars">{cover.values.map((value, hour) => <span key={hour} data-chart-tooltip={`${String(hour).padStart(2, "0")}:00–${String((hour + 1) % 24).padStart(2, "0")}:00\n${cover.format(value)} ${cover.detail}\n${cover.timeZone}`} title=""><i style={{ height: `${value / max * 100}%` }} /></span>)}</span><span className="human-study-axis"><span>00:00</span><span>12:00</span><span>23:00</span></span>{footnote}</span>;
  }
  const total = cover.rows.reduce((sum, row) => sum + Math.max(0, row.value), 0);
  if (cover.kind === "pie") {
    let start = -Math.PI / 2;
    const slices = cover.rows.filter(row => row.value > 0).map((row, index) => {
      const end = start + row.value / total * 2 * Math.PI, slice = { ...row, key: String(index), start, end, color: row.color ?? "var(--query-tone)", detail: `${row.label}\n${row.display} · ${(row.value / total).toLocaleString(undefined, { style: "percent", maximumFractionDigits: 1 })}${row.detail ? `\n${row.detail}` : ""}` };
      start = end; return slice;
    });
    return <span className="draft-cover is-pie"><span className="query-pie-chart"><span className="query-pie-plot"><svg viewBox="0 0 100 100" preserveAspectRatio="xMidYMid meet" aria-hidden="true">
      <defs><clipPath id={id}><circle cx="50" cy="50" r="48" /></clipPath></defs>
      <g clipPath={`url(#${id})`}>{slices.map(slice => <g key={slice.key} className="qt-metric-mark" data-chart-tooltip={slice.detail} data-chart-colors={JSON.stringify([slice.color])}>
        {slices.length === 1 ? <circle cx="50" cy="50" r="48" fill={slice.color} /> : <path fill={slice.color} d={pieWedgePath(slice.start, slice.end)} />}
      </g>)}</g>
    </svg></span><ChartLegend items={slices} columns={cover.legendColumns} /></span>{footnote}</span>;
  }
  const max = Math.max(...cover.rows.map(row => row.value), 0) || 1;
  return <span className="draft-cover is-rows"><span className="human-study-rows">{cover.rows.map(row => <span className="human-study-row" key={row.label} data-chart-tooltip={`${row.label}\n${row.display}${row.detail ? `\n${row.detail}` : ""}`} title=""><span>{row.label}</span><strong>{row.display}</strong><i><span style={{ width: `${Math.max(1.5, row.value / max * 100)}%`, background: row.color }} /></i></span>)}</span>{footnote}</span>;
}
