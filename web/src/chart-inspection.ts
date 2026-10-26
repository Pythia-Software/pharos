import { nextPeriod, shiftPeriod, type ChartPeriod } from "./chart-period";
import type { ChartBucket, ChartDisplay } from "./chart-series";

export type ChartInspectionRange = { from: Date; to: Date; rolling: boolean };

// The release endpoints decide the action, including a drag out and back.
export function chartGesture(anchor: number, current: number) {
  const first = Math.min(anchor, current), last = Math.max(anchor, current);
  return { first, last, count: last - first + 1, inspect: first === last };
}

export function chartInspectionRange(bucket: ChartBucket, period: ChartPeriod, count: number, display: ChartDisplay, start?: Date): ChartInspectionRange {
  const rolling = display !== "cumulative" && (bucket.windowSize ?? 1) > 1;
  const from = display === "cumulative" ? new Date(start ?? bucket.date)
    : rolling ? shiftPeriod(bucket.date, period, 1 - bucket.windowSize!, count) ?? new Date(bucket.date) : new Date(bucket.date);
  const to = new Date(bucket.date);
  nextPeriod(to, period, count);
  return { from, to, rolling };
}
