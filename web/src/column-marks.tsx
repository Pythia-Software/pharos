import { localeDateTime, localeNumber } from "./locale-format";
import { formatUSD } from "./currency";
import React from "react";
import type { ChartScale } from "./chart-scale";
import { cumulativeBuckets, rollingAverageBuckets, shareBuckets, timelineGeometry, timelinePath, withoutZeroes, type ChartBucket, type ChartSeries, type ChartDisplay } from "./chart-series";
import { ChartLegend } from "./chart-legend";
import { percentileLabel, type PercentileLevel } from "./dashboard-analysis-model";

export function compactTimelineBuckets(source: ChartBucket[], { display, rollingPeriods = 0, percent = false, nonzero = false }: { display?: ChartDisplay; rollingPeriods?: number; percent?: boolean; nonzero?: boolean } = {}): ChartBucket[] {
  const amounts = display === "cumulative" ? cumulativeBuckets(source) : rollingAverageBuckets(source, rollingPeriods, { period: "day" }).filter(bucket => !bucket.history);
  return percent ? shareBuckets(amounts) : nonzero ? withoutZeroes(amounts) : amounts;
}

const tooltipValue = (value: number, unit: string) => unit === "USD"
  ? formatUSD(value)
  : unit === "percent" ? localeNumber(value, { style: "percent", maximumFractionDigits: 2 })
  : `${localeNumber(value, { maximumFractionDigits: 3 })}${unit ? ` ${unit}` : ""}`;

// Both Usage and query-card covers use these marks. Keep stack geometry and
// visibility decisions here so compact previews inherit chart changes.
export function ColumnMarks({ bucket, series, scale, percent = false, grouped = false }: {
  bucket: ChartBucket; series: ChartSeries[]; scale: (value: number) => number; percent?: boolean; grouped?: boolean;
}) {
  if (grouped) return <>{series.map((item, index) => <i key={item.key} style={{position:'absolute',bottom:0,left:`${100 * index / series.length}%`,width:`${100 / series.length}%`,height:`${scale(bucket.values[item.key] ?? 0)}%`,background:item.color}} />)}</>;
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

export type CompactTimelineOptions = { unit?: string; showLegend?: boolean; legendColumns?: 1 | 2 | 3; xLabel?: string; yLabel?: string; rollingPeriods?: number; percent?: boolean; scale?: ChartScale; display?: ChartDisplay; prepared?: boolean; lines?: boolean; bars?: boolean; grouped?: boolean; references?: CompactReference[]; nonzero?: boolean };
// A trend card's reference levels, as its dashboard's %ile control draws them.
export type CompactReference = { value: number; level: PercentileLevel };
// percent selects a share-of-total axis. Percentage-valued measures keep
// their requested scale and use unit="percent" only to format their values.
export function compactTimelineScale(buckets: ChartBucket[], series: ChartSeries[], { percent = false, lines = false, bars = false, grouped = false, scale = "linear", references = [] }: CompactTimelineOptions = {}) {
  const geometry = timelineGeometry(buckets, series, { percent, lines, bars, grouped, scale, reference: Math.max(0, ...references.map(item => item.value)) });
  return percent ? { ...geometry, y: (value: number) => value * 100, ticks: [.25, .5, .75, 1] } : geometry;
}
export function CompactColumns({ buckets: sourceBuckets, series, unit = "", showLegend = series.length > 1, legendColumns, xLabel = "Day", yLabel = unit || "Value", rollingPeriods = 0, percent = false, scale = "linear", display = "interval", prepared = false, lines = false, bars = false, grouped = false, references = [], nonzero = false }: { buckets: ChartBucket[]; series: ChartSeries[] } & CompactTimelineOptions) {
  const accumulated = display === "cumulative";
  // Prepared buckets arrive without their zeroes (trendSeries).
  const buckets = (prepared ? sourceBuckets : compactTimelineBuckets(sourceBuckets, { display, rollingPeriods, percent, nonzero })).map(bucket => ({ ...bucket, date: new Date(bucket.date) }));
  const { y, ticks, lines: drawLines } = compactTimelineScale(buckets, series, { percent, lines, bars, grouped, scale, references });
  const percentageValues = percent || unit === "percent";
  const axis: number[] = [0];
  for (const tick of ticks) if (y(tick) - y(axis.at(-1)!) >= 20) axis.push(tick);
  const dates = [...new Set([0, Math.floor((buckets.length - 1) / 2), buckets.length - 1])].filter(index => buckets[index]);
  const dateLabel = (date: Date, detail = false) => localeDateTime(date, { month: "short", day: "numeric", ...detail ? { year: "numeric" as const } : {}, ...prepared && ["hour", "hours"].includes(xLabel) ? { hour: "numeric" as const, minute: "2-digit" as const } : {} });
  // The tooltip keys each line to its colour, so notes get none.
  const tooltipLines = (bucket: typeof buckets[number]) => {
    const notes = [dateLabel(bucket.date, true), accumulated ? "Cumulative from range start" : rollingPeriods > 1 ? `${bucket.windowSize}d rolling average` : undefined, scale === "logarithmic" && !percent ? "Log scale" : undefined].filter(Boolean) as string[];
    const values = series.filter(item => !nonzero || bucket.values[item.key] !== undefined);
    return { "data-chart-colors": JSON.stringify([...notes.map(() => null), ...values.map(item => item.color), ...(bucket.detail ? [null] : [])]),
      "data-chart-tooltip": [...notes, ...values.map(item => `${item.label}: ${tooltipValue(bucket.values[item.key] ?? 0, percent ? "percent" : unit)}`), bucket.detail].filter(Boolean).join("\n") };
  };
  return <span className="query-cover-timeline" aria-hidden="true">
    <span className="query-timeline-plot">
      <span className="query-timeline-ticks">{axis.map(value => <span key={value} style={{ bottom: `${y(value)}%` }}>{percentageValues ? tooltipValue(value, "percent") : unit === "USD" ? formatUSD(value) : value.toLocaleString(undefined, { notation: "compact", maximumSignificantDigits: 3 })}</span>)}</span>
      <span className="query-cover-columns">{buckets.map(bucket => <span key={bucket.key} style={drawLines || grouped ? { position: "relative" } : undefined} {...tooltipLines(bucket)} title="">
        {drawLines ? series.filter(item => bucket.values[item.key] !== undefined).map(item => <i key={item.key} className="query-timeline-point" style={{ bottom: `${y(bucket.values[item.key])}%`, background: item.color }} />) : <ColumnMarks bucket={bucket} series={series} scale={y} percent={percent} grouped={grouped} />}
      </span>)}
        {drawLines && buckets.length ? <svg className="query-timeline-lines" viewBox="0 0 1000 100" preserveAspectRatio="none">{series.map(item => <path key={item.key} fill="none" stroke={item.color} strokeWidth="1.5" vectorEffect="non-scaling-stroke" d={timelinePath(buckets, item.key, y, !percentageValues || percent, nonzero)} />)}</svg> : null}
        {references.map(item => <span key={item.level} className="query-timeline-reference" data-level={item.level} style={{ bottom: `${y(item.value)}%` }} title={`${percentileLabel(item.level)}: ${tooltipValue(item.value, unit)}`} />)}
      </span>
    </span>
    <span className="query-timeline-dates">{dates.map(index => <span key={index}>{dateLabel(buckets[index].date)}</span>)}</span>
    <span className="query-chart-axis-labels"><span data-overflow-title="">↑ {yLabel}</span><span data-overflow-title="">→ {xLabel}</span></span>
    {showLegend ? <ChartLegend items={series} columns={legendColumns} /> : null}
  </span>;
}
