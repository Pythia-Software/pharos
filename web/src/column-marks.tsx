import React from "react";
import { columnScale, type ChartScale } from "./chart-scale";
import { cumulativeBuckets, rollingAverageBuckets, shareBuckets, type ChartBucket, type ChartSeries, type ChartDisplay } from "./chart-series";
import { ChartLegend } from "./chart-legend";

export function compactTimelineBuckets(source: ChartBucket[], { display, rollingPeriods = 0, percent = false }: { display?: ChartDisplay; rollingPeriods?: number; percent?: boolean } = {}): ChartBucket[] {
  const amounts = display === "cumulative" ? cumulativeBuckets(source) : rollingAverageBuckets(source, rollingPeriods, { period: "day" }).filter(bucket => !bucket.history);
  return percent ? shareBuckets(amounts) : amounts;
}

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
    style={{ height: `${stacked || percent ? height : Math.max(1.5, height)}%`, background: item.color }} />)}</>;
}

export type CompactTimelineOptions = { unit?: string; showLegend?: boolean; legendColumns?: 1 | 2 | 3; xLabel?: string; yLabel?: string; rollingPeriods?: number; percent?: boolean; scale?: ChartScale; display?: ChartDisplay; prepared?: boolean; lines?: boolean; reference?: number };
// percent selects a share-of-total axis. Percentage-valued measures keep
// their requested scale and use unit="percent" only to format their values.
export function compactTimelineScale(buckets: ChartBucket[], series: ChartSeries[], { percent = false, lines = false, scale = "linear", reference }: CompactTimelineOptions = {}) {
  const peaks = buckets.map(bucket => lines ? Math.max(0, ...series.map(item => bucket.values[item.key] ?? 0)) : series.reduce((sum, item) => sum + (bucket.values[item.key] ?? 0), 0));
  return percent ? { y: (value: number) => value * 100, ticks: [.25, .5, .75, 1] } : columnScale([...peaks, reference ?? 0], scale);
}
export function CompactColumns({ buckets: sourceBuckets, series, unit = "", showLegend = series.length > 1, legendColumns, xLabel = "Day", yLabel = unit || "Value", rollingPeriods = 0, percent = false, scale = "linear", display = "interval", prepared = false, lines = false, reference }: { buckets: ChartBucket[]; series: ChartSeries[] } & CompactTimelineOptions) {
  const accumulated = display === "cumulative";
  const buckets = (prepared ? sourceBuckets : compactTimelineBuckets(sourceBuckets, { display, rollingPeriods, percent })).map(bucket => ({ ...bucket, date: new Date(bucket.date) }));
  const { y, ticks } = compactTimelineScale(buckets, series, { percent, lines, scale, reference });
  const percentageValues = percent || unit === "percent";
  const axis: number[] = [0];
  for (const tick of ticks) if (y(tick) - y(axis.at(-1)!) >= 20) axis.push(tick);
  const dates = [...new Set([0, Math.floor((buckets.length - 1) / 2), buckets.length - 1])].filter(index => buckets[index]);
  const dateLabel = (date: Date, detail = false) => date.toLocaleString(undefined, { month: "short", day: "numeric", ...detail ? { year: "numeric" as const } : {}, ...prepared && ["hour", "hours"].includes(xLabel) ? { hour: "numeric" as const, minute: "2-digit" as const } : {} });
  return <span className="query-cover-timeline" aria-hidden="true">
    <span className="query-timeline-plot">
      <span className="query-timeline-ticks">{axis.map(value => <span key={value} style={{ bottom: `${y(value)}%` }}>{percentageValues ? tooltipValue(value, "percent") : `${unit === "USD" ? "$" : ""}${value.toLocaleString(undefined, { notation: "compact", maximumSignificantDigits: 3 })}`}</span>)}</span>
      <span className="query-cover-columns">{buckets.map(bucket => <span key={bucket.key} style={lines ? { position: "relative" } : undefined} data-chart-colors={JSON.stringify([null, ...series.map(item => item.color)])} data-chart-tooltip={[dateLabel(bucket.date, true), accumulated ? "Cumulative from range start" : rollingPeriods > 1 ? `${bucket.windowSize}d rolling average` : undefined, scale === "logarithmic" && !percent ? "Log scale" : undefined, ...series.map(item => `${item.label}: ${tooltipValue(bucket.values[item.key] ?? 0, percent ? "percent" : unit)}`), bucket.detail].filter(Boolean).join("\n")} title="">
        {lines ? series.filter(item => bucket.values[item.key] !== undefined).map(item => <i key={item.key} className="query-timeline-point" style={{ bottom: `${y(bucket.values[item.key])}%`, background: item.color }} />) : <ColumnMarks bucket={bucket} series={series} scale={y} percent={percent} />}
      </span>)}
        {lines ? <svg className="query-timeline-lines" viewBox="0 0 100 100" preserveAspectRatio="none">{series.map(item => <path key={item.key} stroke={item.color} vectorEffect="non-scaling-stroke" d={buckets.map((bucket, index) => bucket.values[item.key] === undefined ? "" : `${!index || buckets[index - 1].values[item.key] === undefined ? "M" : "L"}${100 * (index + .5) / buckets.length},${100 - y(bucket.values[item.key])}`).join(" ")} />)}</svg> : null}
        {reference !== undefined ? <span className="query-timeline-reference" style={{ bottom: `${y(reference)}%` }} title={`Norm: ${tooltipValue(reference, unit)}`} /> : null}
      </span>
    </span>
    <span className="query-timeline-dates">{dates.map(index => <span key={index}>{dateLabel(buckets[index].date)}</span>)}</span>
    <span className="query-chart-axis-labels"><span title={yLabel}>↑ {yLabel}</span><span title={xLabel}>→ {xLabel}</span></span>
    {showLegend ? <ChartLegend items={series} columns={legendColumns} /> : null}
  </span>;
}
