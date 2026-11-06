import React from "react";
import { createPortal } from "react-dom";
import { ChartTooltip } from "./chart-tooltip";
import type { ChartPeriod } from "./chart-period";
import { heatmapCellText, heatmapPeriodLabel, heatmapShade, type Heatmap } from "./dashboard-heatmap-model";
import "./dashboard-heatmap.css";

// The trend's heatmap (dashboard-heatmap-model.ts): time across, the value
// up its own continuous axis, each cell shaded by how much falls in that bin
// that period. A line traces each period's median, a dashed one its 90th
// percentile. Guides mark a band's edges, so the band a cell falls in still
// reads. Hover a cell for its bin and amount.
export type HeatmapGuide = { value: number; label: string };
const height = 1000;
export function TrendHeatmap({ title, controls, action, titleSlot, heatmap, period, noun, label, format = String, amount, rollingPeriods = 0, guides = [], loading, error }: {
  title: string; controls: React.ReactNode; action?: React.ReactNode; titleSlot?: HTMLElement | null; heatmap: Heatmap; period: ChartPeriod; noun: string;
  label: string; format?: (value: number) => string; amount?: (value: number) => string; rollingPeriods?: number; guides?: HeatmapGuide[]; loading?: boolean; error?: string;
}) {
  const head = <div className="usage-chart-title usage-chart-headline"><h2>{title}</h2>{action}</div>;
  const { columns, rows } = heatmap, middle = columns[Math.floor(columns.length / 2)];
  const options = { period, noun, label, format, amount, rollingPeriods };
  const placed = guides.flatMap(guide => { const y = heatmap.place(guide.value); return y === null || y <= 0.001 || y >= 0.999 ? [] : [{ ...guide, y }]; });
  // A trace breaks at an empty period rather than bridging it.
  const trace = (pick: (column: Heatmap["columns"][number]) => number | null) => {
    let path = "", open = false;
    columns.forEach((column, at) => {
      const y = pick(column);
      if (y === null) { open = false; return; }
      path += `${open ? "L" : "M"}${at + 0.5},${(1 - y) * height} `;
      open = true;
    });
    return path.trim();
  };
  const smoothed = rollingPeriods > 1 ? ` Each column averages ${rollingPeriods} periods.` : "";
  const peak = amount ? amount(heatmap.max) : heatmap.max.toLocaleString(undefined, { maximumFractionDigits: rollingPeriods > 1 ? 1 : 0 });
  return <div className="usage-chart dashboard-heatmap" data-pharos-target={`section:chart:${title}`}>
    <div className="usage-chart-head">{titleSlot ? createPortal(head, titleSlot) : <div className="usage-chart-title"><h3>{title}</h3>{action}</div>}<div className="usage-chart-controls">{controls}</div></div>
    {error ? <p className="query-table-error" role="alert">{error}</p>
      : !columns.length || !rows.length ? <p className="usage-chart-empty" role="status">{loading ? "Loading…" : `No ${noun} in this range`}</p>
      : <ChartTooltip>
        <div className="dashboard-heatmap-plot" role="img" aria-label={`${title}: ${label.toLowerCase()} up the side, time across; darker cells hold more ${noun}.`}>
          <span className="dashboard-heatmap-name">{label}</span>
          <div className="dashboard-heatmap-yaxis" aria-hidden="true">
            {heatmap.ticks.map(tick => <span key={tick.value} style={{ bottom: `${100 * tick.y}%` }}>{format(tick.value)}</span>)}
          </div>
          <div className="dashboard-heatmap-area">
            <svg viewBox={`0 0 ${columns.length} ${height}`} preserveAspectRatio="none" aria-hidden="true">
              <defs><pattern id="dashboard-heatmap-hatch" width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><line x1="0" y1="0" x2="0" y2="6" className="dashboard-heatmap-hatch" /></pattern></defs>
              {heatmap.ticks.map(tick => <line key={tick.value} x1={0} x2={columns.length} y1={(1 - tick.y) * height} y2={(1 - tick.y) * height} className="dashboard-heatmap-gridline" vectorEffect="non-scaling-stroke" />)}
              {columns.map((column, at) => column.total <= 0
                ? <rect key={column.key} x={at} y={0} width={1} height={height} fill="url(#dashboard-heatmap-hatch)" data-chart-tooltip={`${heatmapPeriodLabel(column.date, period)}\nNo ${noun} in this period`} />
                : <g key={column.key}>{rows.map((row, index) => {
                  const shade = heatmapShade(column.cells[index], heatmap.max);
                  return shade === null ? null : <rect key={index} x={at + 0.06} width={0.88} y={(1 - row.y1) * height} height={(row.y1 - row.y0) * height} className="dashboard-heatmap-cell"
                    fillOpacity={0.06 + 0.94 * shade} data-chart-tooltip={heatmapCellText(heatmap, index, at, options)} />;
                })}</g>)}
              {placed.map(guide => <line key={guide.value} x1={0} x2={columns.length} y1={(1 - guide.y) * height} y2={(1 - guide.y) * height} className="dashboard-heatmap-guide" vectorEffect="non-scaling-stroke" />)}
              <path d={trace(column => column.p90)} className="dashboard-heatmap-trace is-p90" vectorEffect="non-scaling-stroke" />
              <path d={trace(column => column.median)} className="dashboard-heatmap-trace" vectorEffect="non-scaling-stroke" />
            </svg>
            {placed.map(guide => <span key={guide.value} className="dashboard-heatmap-guide-label" style={{ bottom: `${100 * guide.y}%` }}>{guide.label}</span>)}
          </div>
          <div className="usage-chart-axis dashboard-heatmap-axis"><span>{heatmapPeriodLabel(columns[0].date, period)}</span><span>{middle && columns.length > 4 ? heatmapPeriodLabel(middle.date, period) : ""}</span><span>{heatmapPeriodLabel(columns[columns.length - 1].date, period)}</span></div>
        </div>
      </ChartTooltip>}
    <p className="usage-dashboard-note dashboard-heatmap-key"><span className="dashboard-heatmap-ramp" aria-hidden="true" />
      <span>Each column is one period; each cell, the {noun} whose {label.toLowerCase()} falls in that bin, darkest at {peak}, shaded on a log scale so sparse bins still show. The solid line is each period's median, the dashed line its 90th percentile, both read from the bins.{placed.length ? " Dotted lines mark where each band starts." : ""} A hatched column had none.{smoothed}</span></p>
  </div>;
}
