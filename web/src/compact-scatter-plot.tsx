import React, { useId, useLayoutEffect, useRef, useState } from "react";
import type { AggregationClause, AggregationResultEntry, MetricValue, MetricValueFormat } from "@pythia-software/query-table-core";
import { formatMetricOutput } from "@pythia-software/query-table-ui";
import { metricSeriesColor } from "./chart-colors";
import { ChartLegend } from "./chart-legend";
import { scatterAxis, scatterTick } from "./scatter-axis";

const detailValue = (value: MetricValue | undefined, format?: MetricValueFormat) =>
  typeof value === "number" && (!format || format.kind === "number") ? value.toLocaleString(undefined, { maximumSignificantDigits: 12 }) : formatMetricOutput(value, format).text;

export function CompactScatterPlot({ clause, result, budget, labels }: {
  clause: AggregationClause; result: AggregationResultEntry; budget: number; labels?: Record<string, string>;
}) {
  const plot = useRef<HTMLDivElement>(null), yLabels = useRef<SVGGElement>(null), clipId = useId();
  const [size, setSize] = useState({ width: 218, height: 170 }), [gutter, setGutter] = useState(32);
  useLayoutEffect(() => {
    const element = plot.current;
    if (!element) return;
    const resize = () => {
      const { width, height } = element.getBoundingClientRect();
      if (width > 0 && height > 0) setSize(previous => previous.width === width && previous.height === height ? previous : { width, height });
    };
    resize();
    const observer = new ResizeObserver(resize);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);
  const display = clause.display;
  const points = result.buckets.slice(0, budget).filter(bucket => !bucket.error && !bucket.yError && typeof bucket.value === "number" && Number.isFinite(bucket.value) && typeof bucket.y === "number" && Number.isFinite(bucket.y));
  // An omitted log pair must not stretch the other axis either.
  const drawable = points.filter(bucket => (display?.xScale?.mode !== "log" || Number(bucket.value) > 0) && (display?.yScale?.mode !== "log" || Number(bucket.y) > 0));
  const xa = scatterAxis(drawable.map(bucket => Number(bucket.value)), display?.xScale, 3);
  const ya = scatterAxis(drawable.map(bucket => Number(bucket.y)), display?.yScale);
  const xTicks = xa.ticks.map(value => ({ value, label: scatterTick(value, display?.xFormat, xa.ticks) }));
  const yTicks = ya.ticks.map(value => ({ value, label: scatterTick(value, display?.yFormat, ya.ticks) }));
  const yLabelKey = JSON.stringify(yTicks.map(tick => tick.label));
  useLayoutEffect(() => {
    const widths = [...yLabels.current?.querySelectorAll("text") ?? []].map(label => label.getComputedTextLength());
    setGutter(Math.ceil(Math.max(14, ...widths)) + 7);
  }, [yLabelKey]);
  const left = gutter, right = size.width - 6, top = 6, bottom = size.height - 20;
  // Give edge observations a full marker radius inside the clipping rectangle.
  const x = (value: number) => left + 4 + xa.fraction(value) * (right - left - 8), y = (value: number) => bottom - 4 - ya.fraction(value) * (bottom - top - 8);
  const problem = xa.error ?? ya.error;
  return <div className="query-scatter-chart">
    <div className="qt-metric-plot query-scatter-plot" ref={plot}>
      {problem ? <p className="qt-metric-message">{problem}</p> : <svg viewBox={`0 0 ${size.width} ${size.height}`} role="group" aria-label={clause.label ?? "Scatterplot"}>
        <defs><clipPath id={clipId}><rect x={left} y={top} width={Math.max(0, right - left)} height={Math.max(0, bottom - top)} /></clipPath></defs>
        <g className="query-scatter-grid">{xTicks.map(tick => <line key={tick.value} x1={x(tick.value)} x2={x(tick.value)} y1={top} y2={bottom} />)}{yTicks.map(tick => <line key={tick.value} x1={left} x2={right} y1={y(tick.value)} y2={y(tick.value)} />)}</g>
        <g className="query-scatter-x-ticks">{xTicks.map(tick => <text className="qt-metric-axis" key={tick.value} x={x(tick.value)} y={bottom + 15} textAnchor={x(tick.value) < left + tick.label.length * 2.5 ? "start" : x(tick.value) > size.width - tick.label.length * 2.5 ? "end" : "middle"}>{tick.label}</text>)}</g>
        <g className="query-scatter-y-ticks" ref={yLabels}>{yTicks.map(tick => <text className="qt-metric-axis" key={tick.value} x={left - 5} y={y(tick.value) + 3} textAnchor="end">{tick.label}</text>)}</g>
        <g clipPath={`url(#${clipId})`}>{drawable.map((bucket, index) => {
          const label = labels?.[JSON.stringify(bucket.keys)] ?? bucket.keys.map(key => String(key ?? "Unspecified")).join(" · ");
          const color = metricSeriesColor(clause.groupBy, bucket.keys);
          const detail = `${label}\n${detailValue(bucket.value, display?.xFormat)} / ${detailValue(bucket.y, display?.yFormat)}\n${bucket.count.toLocaleString()} rows`;
          return <g key={index} className="qt-metric-mark" aria-label={detail}>
            <circle cx={x(Number(bucket.value))} cy={y(Number(bucket.y))} r="3.5" fill={color} />
            <circle cx={x(Number(bucket.value))} cy={y(Number(bucket.y))} r="9" fill="transparent" />
          </g>;
        })}</g>
      </svg>}
    </div>
    <div className="qt-metric-axis-labels"><span>↑ {display?.yLabel ?? "Y value"}</span><span>→ {display?.xLabel ?? "X value"}</span></div>
    {display?.legendPosition && display.legendPosition !== "none" ? <ChartLegend items={drawable.map((bucket, index) => ({ key: String(index), label: labels?.[JSON.stringify(bucket.keys)] ?? bucket.keys.map(key => String(key ?? "Unspecified")).join(" · "), color: metricSeriesColor(clause.groupBy, bucket.keys) }))} /> : null}
  </div>;
}
