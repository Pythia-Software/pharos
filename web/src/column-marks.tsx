import React from "react";
import { columnScale } from "./chart-scale";
import type { ChartBucket, ChartSeries } from "./chart-series";

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
  return <>{parts.map(({ item, height }, index) => <i key={item.key} className={index === parts.length - 1 ? "top" : ""}
    style={{ height: `${stacked ? height : Math.max(1.5, height)}%`, background: item.color }} />)}</>;
}

export function CompactColumns({ buckets, series }: { buckets: ChartBucket[]; series: ChartSeries[] }) {
  const { y, split } = columnScale(buckets.map(bucket => series.reduce((sum, item) => sum + (bucket.values[item.key] ?? 0), 0)));
  return <span className={`query-cover-columns${split ? " broken" : ""}`} style={{ gap: buckets.length > 60 ? ".5px" : buckets.length > 30 ? "1px" : "2px" }} aria-hidden="true">{buckets.map(bucket => <span key={bucket.key}>
    <ColumnMarks bucket={bucket} series={series} scale={y} />
  </span>)}</span>;
}
