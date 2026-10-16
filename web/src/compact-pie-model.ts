import type { AggregationResultEntry } from "@pythia-software/query-table-core";

export function compactPieSlices(result: AggregationResultEntry, limit: number, showOther = true) {
  const all = [...result.buckets, ...(result.other ? [result.other] : [])];
  if (result.coverage === "partial" || result.groupCount && result.groupCount > result.buckets.length && !result.other) return { error: "Pie requires a complete, exact denominator.", slices: [], total: 0 };
  if (all.some(bucket => bucket.error || typeof bucket.value !== "number" || !Number.isFinite(bucket.value) || bucket.value < 0)) return { error: "Pie requires nonnegative additive values.", slices: [], total: 0 };
  const total = all.reduce((sum, bucket) => sum + Number(bucket.value), 0);
  if (!(total > 0) || !Number.isFinite(total)) return { error: "No positive values to display.", slices: [], total: 0 };
  const slices = result.buckets.slice(0, limit).map(bucket => ({ bucket, value: Number(bucket.value), other: false }));
  const remainder = result.buckets.slice(limit).reduce((sum, bucket) => sum + Number(bucket.value), 0) + Number(result.other?.value ?? 0);
  if (showOther && remainder > 0) slices.push({ bucket: { keys: ["Other"], value: remainder, count: 0 }, value: remainder, other: true });
  let cumulative = 0;
  return { total, slices: slices.map(slice => {
    const start = cumulative / total * 2 * Math.PI - Math.PI / 2;
    cumulative += slice.value;
    const end = cumulative / total * 2 * Math.PI - Math.PI / 2;
    return { ...slice, start, end };
  }) };
}

export function pieWedgePath(start: number, end: number, inner = 0): string {
  const point = (angle: number, radius: number) => `${50 + Math.cos(angle) * radius} ${50 + Math.sin(angle) * radius}`;
  const large = end - start > Math.PI ? 1 : 0;
  return inner ? `M ${point(start, 48)} A 48 48 0 ${large} 1 ${point(end, 48)} L ${point(end, inner)} A ${inner} ${inner} 0 ${large} 0 ${point(start, inner)} Z`
    : `M 50 50 L ${point(start, 48)} A 48 48 0 ${large} 1 ${point(end, 48)} Z`;
}
