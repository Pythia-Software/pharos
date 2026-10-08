import type { ChartBucket } from "./chart-series";

export type ChartPeriod = "hour" | "hours" | "day" | "days" | "week" | "month";
export const chartPeriods: Record<ChartPeriod, { unit: string }> = {
  hour: { unit: "hour" }, hours: { unit: "hour" }, day: { unit: "day" }, days: { unit: "day" }, week: { unit: "week" }, month: { unit: "month" },
};
export const validPeriodCount = (value: unknown): value is number => typeof value === "number" && Number.isInteger(value) && value >= 1 && value <= 10000;
export const isHourly = (period: ChartPeriod) => period === "hour" || period === "hours";
export const periodLabel = (period: ChartPeriod, count = 1) => period === "hours" || period === "days" ? `${count} ${chartPeriods[period].unit}${count === 1 ? "" : "s"}` : chartPeriods[period].unit;
export const periodUnit = (period: ChartPeriod, count = 1) => period === "hours" || period === "days" ? `${count}-${chartPeriods[period].unit} period` : chartPeriods[period].unit;
const pad2 = (value: number) => String(value).padStart(2, "0");
export const localDay = (date: Date) => `${date.getFullYear()}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())}`;
const hourMs = 3600000, dayMs = 24 * hourMs;

// Divide local calendar days evenly; single hours keep both occurrences at DST fall-back.
const localHours = (period: ChartPeriod, count: number) => period === "hours" && count > 1 && 24 % count === 0;

export function periodStart(date: Date, period: ChartPeriod, count = 1): Date {
  if (localHours(period, count)) {
    const start = new Date(date);
    start.setHours(Math.floor(date.getHours() / count) * count, 0, 0, 0);
    return start;
  }
  if (isHourly(period)) {
    const width = hourMs * (period === "hours" ? count : 1);
    return new Date(Math.floor(date.getTime() / width) * width);
  }
  const start = new Date(date);
  start.setHours(0, 0, 0, 0);
  if (period === "week") start.setDate(start.getDate() - ((start.getDay() + 6) % 7));
  if (period === "month") start.setDate(1);
  if (period === "days") {
    // Align calendar days independently of timezone offsets and daylight saving.
    const day = Date.UTC(start.getFullYear(), start.getMonth(), start.getDate()) / dayMs;
    const aligned = new Date(Math.floor(day / count) * count * dayMs);
    start.setFullYear(aligned.getUTCFullYear(), aligned.getUTCMonth(), aligned.getUTCDate());
  }
  return start;
}

export function periodKey(date: Date, period: ChartPeriod, count = 1): string {
  const start = periodStart(date, period, count);
  if (isHourly(period)) return start.toISOString();
  const day = localDay(start);
  return period === "month" ? day.slice(0, 7) : day;
}

export function parseDay(key: string): Date {
  if (key.includes("T")) return new Date(key);
  const [year, month, day] = key.split("-").map(Number);
  return new Date(year, (month || 1) - 1, day || 1);
}

export function earliestKey(values: unknown[]): Date | undefined {
  const dates = values.map(String).filter(value => /^\d{4}-\d{2}/.test(value)).map(parseDay).filter(date => Number.isFinite(date.getTime()));
  return dates.length ? new Date(Math.min(...dates.map(date => date.getTime()))) : undefined;
}

export function nextPeriod(date: Date, period: ChartPeriod, count = 1) {
  if (localHours(period, count)) date.setHours((Math.floor(date.getHours() / count) + 1) * count, 0, 0, 0);
  else if (isHourly(period)) date.setTime(date.getTime() + hourMs * (period === "hours" ? count : 1));
  else if (period === "day" || period === "days") date.setDate(date.getDate() + (period === "days" ? count : 1));
  else if (period === "week") date.setDate(date.getDate() + 7);
  else date.setMonth(date.getMonth() + 1);
}

export function periodBuckets(period: ChartPeriod, start: Date, end: Date, count = 1): ChartBucket[] {
  const first = periodStart(start, period, count), last = periodStart(end, period, count), buckets: ChartBucket[] = [];
  // Build backwards so a long range retains the most recent 5,000 buckets.
  for (let date = new Date(last); date >= first && buckets.length < 5000;) {
    buckets.push({ key: periodKey(date, period, count), date: new Date(date), values: {} });
    if (localHours(period, count)) date.setHours((Math.floor(date.getHours() / count) - 1) * count, 0, 0, 0);
    else if (isHourly(period)) date.setTime(date.getTime() - hourMs * (period === "hours" ? count : 1));
    else if (period === "month") date.setMonth(date.getMonth() - 1);
    else date.setDate(date.getDate() - (period === "week" ? 7 : period === "days" ? count : 1));
  }
  return buckets.reverse();
}
