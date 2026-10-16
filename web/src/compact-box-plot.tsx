import React from "react";
import type { AggregationBucket } from "@pythia-software/query-table-core";
import { sharedBoxScale, boxCost, boxNumber, boxDuration, boxBytes, boxDetails, boxAxisCost, boxAxisNumber, boxOutliers, boxWhiskerExtent } from "./compact-box-model";

export function CompactBoxPlot({ buckets, budget, colors, labels, compact = false, scale = "linear", unit = "USD", axisLabel = unit }: { buckets: AggregationBucket[]; budget: number; colors: string[]; labels: string[]; compact?: boolean; scale?: "linear" | "log"; unit?: string; axisLabel?: string }) {
  const currency = unit === "USD", duration = unit === "ms", bytes = unit === "bytes", format = currency ? boxCost : duration ? boxDuration : bytes ? boxBytes : boxNumber;
  const axis = sharedBoxScale(buckets, budget, scale, !currency && !duration);
  const left = (value: number) => `${axis.fraction(value) * 100}%`;
  return <div className={`query-shared-box${compact || Math.min(budget, buckets.length) > 8 ? " is-dense" : ""}`}>
    <span className="query-box-scale-label">Shared {axis.log ? "log " : ""}{axisLabel || "value"} axis · {Math.min(budget, buckets.length)} of {buckets.length} groups</span>
    <div className="query-box-rows">
      {axis.ticks.map(value => <i key={value} className="query-box-gridline" style={{ left: left(value) }} />)}
      {buckets.slice(0, budget).map((bucket, index) => {
        const summary = bucket.distribution?.kind === "box" ? bucket.distribution.summary : null;
        if (!summary || bucket.error) return <div key={index} className="query-box-row">{labels[index]} · No samples</div>;
        const color = colors[index], whisker = boxWhiskerExtent(summary);
        return <div key={index} className="query-box-row qt-metric-box-row" data-chart-tooltip={boxDetails(labels[index], summary, unit)} data-chart-colors={JSON.stringify([color])} style={{ "--box-color": color } as React.CSSProperties}>
          <div className="query-box-row-label"><span>{labels[index]}</span><strong>Median {format(summary.median)} <small>· Mean {format(summary.mean)}</small></strong></div>
          <div className="query-box-marks" data-axis-min={axis.min} data-axis-max={axis.max}>
            <i className="query-box-whisker" style={{ left: left(whisker.low), width: `${(axis.fraction(whisker.high) - axis.fraction(whisker.low)) * 100}%` }} />
            {[whisker.low, whisker.high].map((value, i) => <i key={i} className="query-box-cap" style={{ left: left(value) }} />)}
            <i className="query-box-quartiles" style={{ left: left(summary.q1), width: `${(axis.fraction(summary.q3) - axis.fraction(summary.q1)) * 100}%` }} />
            <i className="query-box-median" style={{ left: left(summary.median) }} />
            {boxOutliers(summary).map(value => <i key={value} className="query-box-outlier" style={{ left: left(value) }} />)}
          </div>
        </div>;
      })}
    </div>
    <div className="query-box-axis">{axis.ticks.map((value, index) => <span key={value} style={{ left: left(value), transform: index === 0 ? "none" : index === axis.ticks.length - 1 ? "translateX(-100%)" : "translateX(-50%)" }}>{currency ? boxAxisCost(value) : duration || bytes ? format(value) : boxAxisNumber(value)}</span>)}</div>
    <small className="query-box-key">Box: middle 50% · line: median · dots: sampled outliers and extremes</small>
  </div>;
}
