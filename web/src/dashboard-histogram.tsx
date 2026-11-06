import React from "react";
import { ChartTooltip } from "./chart-tooltip";
import { heatmapBinLabel, trendHeatmap, type Heatmap } from "./dashboard-heatmap-model";
import { percentileLabel, type AxisBins } from "./dashboard-analysis-model";
import "./dashboard-heatmap.css";

// A record distribution over the value's fine bins (distributionBins): each
// bar one bin, placed on the value's own axis as the heatmap places its rows,
// so a log axis gives a long tail its shape. Zero, which a log axis cannot
// place, is a bar apart at the left. The median and 90th percentile are
// marked and named. Hover a bar for its range and share; select it to open
// its records.
export type BinCount = { low: number; count: number };
export function histogramOf(counts: BinCount[], bins: AxisBins): Heatmap {
  const series = counts.map(item => ({ key: `b:${item.low}`, label: String(item.low), value: String(item.low), color: "" }));
  const values = Object.fromEntries(counts.map(item => [`b:${item.low}`, item.count]));
  return trendHeatmap([{ key: "all", date: new Date(0), values }], series, bins.scheme, ["p50", "p90"]);
}

// A bar's tooltip: its range, its records and their share.
export function histogramBarText(histogram: Heatmap, row: number, { label, noun, format = String }: { label: string; noun: string; format?: (value: number) => string }): string {
  const column = histogram.columns[0], count = column.cells[row];
  const share = (100 * count / column.total).toLocaleString(undefined, { maximumFractionDigits: 1 });
  return `${label} ${heatmapBinLabel(histogram.rows[row], histogram.scheme, format)}\n${count.toLocaleString()} ${noun} (${share}%)`;
}

const width = 1000, height = 1000;
export function BinnedHistogram({ histogram, label, noun, format = String, onSelect }: {
  histogram: Heatmap; label: string; noun: string; format?: (value: number) => string; onSelect?: (low: number) => void;
}) {
  const column = histogram.columns[0];
  if (!column || column.total <= 0) return null;
  const tallest = Math.max(...column.cells);
  const marks = histogram.levels.flatMap((level, index) => { const at = column.levels[index]; return at ? [{ level, x: at.y, value: at.value }] : []; });
  return <ChartTooltip>
    <div className="dashboard-histogram" role="img" aria-label={`${label}: ${noun} in each range of the value; ${marks.map(mark => `${percentileLabel(mark.level).toLowerCase()} ${format(mark.value)}`).join(", ")}.`}>
      <div className="dashboard-histogram-area">
        <svg viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" aria-hidden="true">
          {histogram.ticks.map(tick => <line key={tick.value} x1={tick.y * width} x2={tick.y * width} y1={0} y2={height} className="dashboard-heatmap-gridline" vectorEffect="non-scaling-stroke" />)}
          {histogram.rows.map((row, index) => {
            const count = column.cells[index];
            if (count <= 0) return null;
            const tall = Math.max(height * count / tallest, 4);
            return <rect key={row.low} x={row.y0 * width} width={(row.y1 - row.y0) * width} y={height - tall} height={tall} className="dashboard-histogram-bar"
              data-chart-tooltip={histogramBarText(histogram, index, { label, noun, format })} onClick={onSelect ? () => onSelect(row.low) : undefined} />;
          })}
          {marks.map(mark => <line key={mark.level} x1={mark.x * width} x2={mark.x * width} y1={0} y2={height} className="dashboard-heatmap-trace" data-level={mark.level} vectorEffect="non-scaling-stroke" />)}
        </svg>
        {marks.map((mark, index) => <span key={mark.level} className={`dashboard-histogram-mark${mark.x > 0.6 ? " is-left" : ""}`} style={{ left: `${100 * mark.x}%`, top: `${4 + index * 18}px` }}>{percentileLabel(mark.level)} {format(mark.value)}</span>)}
      </div>
      <div className="dashboard-histogram-axis" aria-hidden="true">
        {histogram.ticks.map(tick => <span key={tick.value} style={{ left: `${100 * tick.y}%` }}>{format(tick.value)}</span>)}
      </div>
      <span className="dashboard-histogram-name">{label}{histogram.scheme.scale === "linear" ? "" : " (log axis)"}</span>
    </div>
  </ChartTooltip>;
}
