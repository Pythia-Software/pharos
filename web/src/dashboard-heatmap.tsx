import React from "react";
import { createPortal } from "react-dom";
import { ChartTooltip } from "./chart-tooltip";
import type { ChartPeriod } from "./chart-period";
import { heatmapCellText, heatmapPeriodLabel, heatmapShade, type Heatmap, type HeatmapRow } from "./dashboard-heatmap-model";
import "./dashboard-heatmap.css";

// The trend's heatmap display (dashboard-heatmap-model.ts): rows in the
// breakdown's order, the highest at the top, one shade of one hue. Hover a
// cell for its share and the records behind it, or its value.
// A row's label may be a legend chip (rowLabel), so a row selects and
// filters as a legend value does.
export function TrendHeatmap({ title, controls, action, titleSlot, heatmap, period, noun, rollingPeriods = 0, format, rowLabel, loading, error }: {
  title: string; controls: React.ReactNode; action?: React.ReactNode; titleSlot?: HTMLElement | null; heatmap: Heatmap; period: ChartPeriod; noun: string;
  rollingPeriods?: number; format?: (value: number) => string; rowLabel?: (row: HeatmapRow) => React.ReactNode; loading?: boolean; error?: string;
}) {
  const head = <div className="usage-chart-title usage-chart-headline"><h2>{title}</h2>{action}</div>;
  const rows = heatmap.rows.map((row, index) => ({ row, index })).reverse();
  const { columns } = heatmap, middle = columns[Math.floor(columns.length / 2)];
  const smoothed = rollingPeriods > 1 ? ` Each column ${heatmap.kind === "value" ? "pools" : "averages"} ${rollingPeriods} periods.` : "";
  const share = heatmap.kind === "share";
  return <div className="usage-chart dashboard-heatmap" data-pharos-target={`section:chart:${title}`}>
    <div className="usage-chart-head">{titleSlot ? createPortal(head, titleSlot) : <div className="usage-chart-title"><h3>{title}</h3>{action}</div>}<div className="usage-chart-controls">{controls}</div></div>
    {error ? <p className="query-table-error" role="alert">{error}</p>
      : !columns.length || !rows.length ? <p className="usage-chart-empty" role="status">{loading ? "Loading…" : `No ${noun} in this range`}</p>
      : <ChartTooltip>
        <div className="dashboard-heatmap-grid" role="img" aria-label={share ? `${title}: the share of each period's ${noun} in each group, darker for a larger share.` : `${title}: each group's value in each period, darker for a larger value.`}
          style={{ gridTemplateColumns: `minmax(6rem, max-content) repeat(${columns.length}, minmax(0, 1fr))` }}>
          {rows.map(({ row, index }) => <React.Fragment key={row.key}>
            <span className="dashboard-heatmap-label" title={row.label}>{rowLabel?.(row) ?? row.label}</span>
            {columns.map((column, at) => {
              const shade = heatmapShade(column.cells[index], heatmap.max);
              return <span key={column.key} className={`dashboard-heatmap-cell${shade === null ? " is-empty" : ""}`}
                style={shade === null ? undefined : { background: `color-mix(in srgb, var(--heat-1) ${Math.round(6 + 94 * shade)}%, transparent)` }}
                data-chart-tooltip={heatmapCellText(heatmap, index, at, { period, noun, rollingPeriods, format })} />;
            })}
          </React.Fragment>)}
          <span aria-hidden="true" />
          <div className="usage-chart-axis dashboard-heatmap-axis"><span>{heatmapPeriodLabel(columns[0].date, period)}</span><span>{middle && columns.length > 4 ? heatmapPeriodLabel(middle.date, period) : ""}</span><span>{heatmapPeriodLabel(columns[columns.length - 1].date, period)}</span></div>
        </div>
      </ChartTooltip>}
    <p className="usage-dashboard-note dashboard-heatmap-key"><span className="dashboard-heatmap-ramp" aria-hidden="true" />{share
      ? <>Each column is one period; a cell is the share of that period's {noun} in its group, darkest at {(100 * heatmap.max).toLocaleString(undefined, { maximumFractionDigits: 0 })}%. A hatched column had none.{smoothed} Hover a cell for its count.</>
      : <>Each column is one period; a cell is its group's value in that period, darkest at {(format ?? String)(heatmap.max)}. A hatched cell had no {noun}.{smoothed} Hover a cell for its value.</>}</p>
  </div>;
}
