// Events per 1,000 agent messages read, by period and kind group. Counts of
// events follow how much agents worked; dividing by the messages the pass read
// in the same period (and under the same filters) makes periods comparable.
// The engine's parts split only additive measures, so the dashboard draws
// this rate itself from two grouped counts.

export type Bucket = { keys: unknown[]; value: unknown };
export type RatePeriod = { start: string; read: number; events: Record<string, number>; total: number };

// Monday of a YYYY-MM-DD day, as YYYY-MM-DD (the datasets' own week field).
export function weekStart(day: string): string {
  const date = new Date(`${day}T00:00:00Z`);
  const back = (date.getUTCDay() + 6) % 7;
  date.setUTCDate(date.getUTCDate() - back);
  return date.toISOString().slice(0, 10);
}

// Periods from two grouped counts: events by [day, group] and messages read by
// [day]. Days without messages read are left out (no rate can be said); days
// with messages and no events are kept at zero. Weeks pool their days.
export function eventRates(events: Bucket[], read: Bucket[], period: "day" | "week"): RatePeriod[] {
  const key = (day: string) => period === "week" ? weekStart(day) : day;
  const periods = new Map<string, RatePeriod>();
  const at = (start: string) => {
    let entry = periods.get(start);
    if (!entry) periods.set(start, entry = { start, read: 0, events: {}, total: 0 });
    return entry;
  };
  for (const bucket of read) {
    const day = String(bucket.keys[0] ?? ""), value = Number(bucket.value);
    if (!/^\d{4}-\d{2}-\d{2}$/.test(day) || !Number.isFinite(value)) continue;
    at(key(day)).read += value;
  }
  for (const bucket of events) {
    const day = String(bucket.keys[0] ?? ""), group = String(bucket.keys[1] ?? "Other"), value = Number(bucket.value);
    if (!/^\d{4}-\d{2}-\d{2}$/.test(day) || !Number.isFinite(value)) continue;
    const entry = periods.get(key(day));
    if (!entry) continue;
    entry.events[group] = (entry.events[group] ?? 0) + value;
    entry.total += value;
  }
  return [...periods.values()].filter(entry => entry.read > 0).sort((a, b) => a.start.localeCompare(b.start));
}

export const perThousand = (count: number, read: number) => read > 0 ? 1000 * count / read : 0;

// Days for up to about six weeks, weeks beyond.
export const ratePeriod = (days: number): "day" | "week" => days > 45 ? "week" : "day";
