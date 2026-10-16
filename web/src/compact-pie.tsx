import React, { useId } from "react";
import type { AggregationClause, AggregationResultEntry } from "@pythia-software/query-table-core";
import { ChartLegend } from "./chart-legend";
import { metricSeriesColor, otherSeriesColor } from "./chart-colors";
import { compactPieSlices, pieWedgePath } from "./compact-pie-model";

export function CompactPie({ clause, result, budget, labels, columns = 2, format }: {
  clause: AggregationClause; result: AggregationResultEntry; budget: number; labels?: Record<string, string>; columns?: 1 | 2; format: (value: number) => string;
}) {
  const id = useId().replace(/:/g, ""), pie = compactPieSlices(result, budget, clause.display?.showOther !== false);
  if (clause.expression || clause.distribution || !["sum", "count"].includes(clause.op)) return <span className="query-pie-empty">Pie requires additive counts or sums.</span>;
  if (pie.error) return <span className="query-pie-empty">{pie.error}</span>;
  const slices = pie.slices.map((slice, index) => {
    const label = slice.other ? "Other" : labels?.[JSON.stringify(slice.bucket.keys)] ?? slice.bucket.keys.map(key => String(key ?? "Unspecified")).join(" · ");
    const color = slice.other ? otherSeriesColor : metricSeriesColor(clause.groupBy, slice.bucket.keys);
    const percent = (slice.value / pie.total).toLocaleString(undefined, { style: "percent", maximumFractionDigits: 2 });
    return { ...slice, key: String(index), label, color, detail: `${label}\n${format(slice.value)} · ${percent}\n${clause.label ?? "Recorded usage"}` };
  });
  const donut = clause.display?.kind === "donut";
  return <div className="query-pie-chart">
    <div className="query-pie-plot"><svg viewBox="0 0 100 100" preserveAspectRatio="xMidYMid meet" aria-label={clause.label ?? "Composition"}>
      <defs><clipPath id={id}><circle cx="50" cy="50" r="48" /></clipPath></defs>
      <g clipPath={`url(#${id})`}>{slices.filter(slice => slice.value > 0).map(slice => <g key={slice.key} className="qt-metric-mark" aria-label={slice.detail} data-chart-tooltip={slice.detail} data-chart-colors={JSON.stringify([slice.color])}>
        {slice.value === pie.total ? <circle cx="50" cy="50" r={donut ? 38 : 48} fill={donut ? "none" : slice.color} stroke={donut ? slice.color : "none"} strokeWidth={donut ? 20 : 0} /> : <path fill={slice.color} d={pieWedgePath(slice.start, slice.end, donut ? 28 : 0)} data-start-angle={slice.start} data-end-angle={slice.end} />}
      </g>)}</g>
    </svg></div>
    {clause.display?.legendPosition !== "none" ? <ChartLegend items={slices} columns={columns} /> : null}
  </div>;
}
