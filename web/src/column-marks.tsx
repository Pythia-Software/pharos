import React from "react";
import { columnScale } from "./chart-scale";
import type { ChartBucket, ChartSeries } from "./chart-series";

const tooltipValue = (value: number, unit: string) => unit === "USD"
  ? value.toLocaleString(undefined, { style: "currency", currency: "USD", minimumFractionDigits: 2, maximumFractionDigits: 4 })
  : unit === "percent" ? value.toLocaleString(undefined, { style: "percent", maximumFractionDigits: 2 })
  : `${value.toLocaleString(undefined, { maximumFractionDigits: 3 })}${unit ? ` ${unit}` : ""}`;

// Both Usage and query-card covers use these marks. Keep stack geometry and
// visibility decisions here so compact previews inherit chart changes.
export function ColumnMarks({ bucket, series, scale, percent = false }: {
  bucket: ChartBucket; series: ChartSeries[]; scale: (value: number) => number; percent?: boolean;
}) {
  let below = 0;
  const stacked = series.length > 1;
  const parts = series.flatMap(item => {
    const value = bucket.values[item.key] ?? 0, bottom = scale(below);
    below += value;
    const height = scale(below) - bottom;
    return value > 0 && (!stacked || percent || height >= 0.5) ? [{ item, height }] : [];
  });
  return <>{parts.map(({ item, height }, index) => <i key={item.key} className={!percent && index === parts.length - 1 ? "top" : ""}
    style={{ height: `${stacked ? height : Math.max(1.5, height)}%`, background: item.color }} />)}</>;
}

export function CompactColumns({ buckets, series, unit = "", showLegend = series.length > 1, xLabel = "Day", yLabel = unit || "Value" }: { buckets: ChartBucket[]; series: ChartSeries[]; unit?: string; showLegend?: boolean; xLabel?: string; yLabel?: string }) {
  const { y, ticks } = columnScale(buckets.map(bucket => series.reduce((sum, item) => sum + (bucket.values[item.key] ?? 0), 0)), "linear");
  const axis: number[] = [0];
  for (const tick of ticks) if (y(tick) - y(axis.at(-1)!) >= 20) axis.push(tick);
  const dates = [...new Set([0, Math.floor((buckets.length - 1) / 2), buckets.length - 1])].filter(index => buckets[index]);
  return <span className="query-cover-timeline" aria-hidden="true">
    <span className="query-timeline-plot">
      <span className="query-timeline-ticks">{axis.map(value => <span key={value} style={{ bottom: `${y(value)}%` }}>{`${unit === "USD" ? "$" : ""}${value.toLocaleString(undefined, { notation: "compact", maximumSignificantDigits: 3 })}`}</span>)}</span>
      <span className="query-cover-columns" style={{ gap: buckets.length > 60 ? ".5px" : buckets.length > 30 ? "1px" : "2px" }}>{buckets.map(bucket => <span key={bucket.key} data-chart-tooltip={[bucket.date.toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" }), ...series.map(item => `${item.label}: ${tooltipValue(bucket.values[item.key] ?? 0, unit)}`), bucket.detail].filter(Boolean).join("\n")} title="">
        <ColumnMarks bucket={bucket} series={series} scale={y} />
      </span>)}</span>
    </span>
    <span className="query-timeline-dates">{dates.map(index => <span key={index}>{buckets[index].date.toLocaleDateString(undefined, { month: "short", day: "numeric" })}</span>)}</span>
    <span className="query-chart-axis-labels"><span title={yLabel}>↑ {yLabel}</span><span title={xLabel}>→ {xLabel}</span></span>
    {showLegend ? <span className="query-timeline-legend">{series.map(item => <span key={item.key} title={item.label}><i style={{ background: item.color }} /><span>{item.label}</span></span>)}</span> : null}
  </span>;
}
