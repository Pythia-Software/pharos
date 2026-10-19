import { EMPTY_QUERY, type AggregationClause, type AggregationResultEntry, type QueryState, type WhereTerm } from "@pythia-software/query-table-core";
import type { CardCover } from "./card-cover";
import type { HumanStudyPreview } from "./human-study";
import type { PreviewJob } from "./query-previews";
import { rollingQueryMatches } from "./query-links";

// A trend card plots a trailing-window value of its own population at each of
// the last 31 local days, from complete daily totals of one metric request.
// Ratios divide window totals, never averages of daily ratios.
type TrendMeasure = { op: "sum" | "count"; field?: string };
export type CardTrend = {
  value: TrendMeasure; per?: TrendMeasure; perDay?: boolean; scale?: number;
  // Trailing days per point; the card's own lookback when omitted.
  lookback?: number;
  // End on yesterday when today's partial day would understate the window.
  completeDays?: boolean;
  format?: "percent" | "USD";
  valueLabel: string; pointValueLabel: string; trendLabel?: string; sample: string; detail: string;
};
const endpoints = 31, chunk = 30;
// The sparkline replaces only the shipped cover. Any edited or alternative
// cover, in the library or a collection, renders its destination natively.
export const trendCover = (metricId: string): CardCover => ({ mode: "source", metricId, kind: "auto", height: 260 });

export function usesCardTrend(job: PreviewJob): boolean {
  const options = job.options, recipe = options?.recipe, trend = recipe?.trend;
  if (!trend || options?.coverMetricId || JSON.stringify(options?.cover ?? recipe.cover) !== JSON.stringify(trendCover(recipe.measures[0]?.id ?? ""))) return false;
  const window = options?.window;
  if (recipe.period ? window : !window || window.mode !== "relative" || !rollingQueryMatches(job.query, window)) return false;
  if (!trend.lookback && !window) return false;
  // Edited populations keep their native destination preview.
  const source = job.query.grouping?.source ?? job.query;
  const where = source.where.filter(term => !(window && "field" in term && term.field === window.field));
  return JSON.stringify(where) === JSON.stringify(recipe.where ?? []);
}

export const localDay = (time: number | Date) => { const date = new Date(time); return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`; };
// Calendar steps, not 24-hour multiples, so daylight-saving days stay whole.
export const shiftDay = (day: string, offset: number) => { const date = new Date(`${day}T12:00:00`); date.setDate(date.getDate() + offset); return localDay(date); };

export function cardTrendDays(lookback: number, completeDays: boolean, now: number): { days: string[]; ends: string[] } {
  const last = shiftDay(localDay(now), completeDays ? -1 : 0);
  const ends = Array.from({ length: endpoints }, (_, index) => shiftDay(last, index - endpoints + 1));
  const days = Array.from({ length: endpoints + lookback - 1 }, (_, index) => shiftDay(ends[0], index - lookback + 1));
  return { days, ends };
}

export function cardTrendQueries(where: WhereTerm[], trend: CardTrend, all: string[]): QueryState[] {
  const metric = (id: string, measure: TrendMeasure): AggregationClause => ({ id, op: measure.op, ...(measure.field ? { field: measure.field } : {}), groupBy: ["day"], groupLimit: 100 });
  const metrics = [metric("trend-value", trend.value), ...(trend.per ? [metric("trend-per", trend.per)] : []), metric("trend-rows", { op: "count" })];
  // Whole local days through the day field; a timestamp range would split them.
  // Balanced chunks, since an OR group needs at least two branches.
  const count = Math.ceil(all.length / chunk), size = Math.ceil(all.length / count);
  const chunks = Array.from({ length: count }, (_, index) => all.slice(index * size, (index + 1) * size));
  const days = (part: string[]): WhereTerm => part.length === 1 ? { field: "day", op: "=", value: part[0] } : { any: part.map(value => ({ field: "day", op: "=" as const, value })) };
  return chunks.map(part => ({ ...EMPTY_QUERY, where: [...where, days(part)], aggregations: metrics }));
}

// A truncated or partial day set must fail rather than read as zero activity.
function daily(results: AggregationResultEntry[][], id: string): Map<string, number> {
  const totals = new Map<string, number>();
  for (const metrics of results) {
    const result = metrics.find(metric => metric.id === id);
    if (!result || result.error || result.coverage === "partial" || result.groupCount !== undefined && result.groupCount > result.buckets.length || result.buckets.some(bucket => bucket.error)) throw new Error(result?.error ?? "Complete daily history is unavailable.");
    for (const bucket of result.buckets) totals.set(String(bucket.keys[0]), (totals.get(String(bucket.keys[0])) ?? 0) + (Number(bucket.value) || 0));
  }
  return totals;
}

export function buildCardTrend(trend: CardTrend, lookback: number, results: AggregationResultEntry[][], now: number): HumanStudyPreview {
  const { ends } = cardTrendDays(lookback, Boolean(trend.completeDays), now), today = localDay(now);
  const value = daily(results, "trend-value"), per = trend.per ? daily(results, "trend-per") : undefined, rows = daily(results, "trend-rows");
  const total = (map: Map<string, number>, end: string) => Array.from({ length: lookback }, (_, back) => map.get(shiftDay(end, -back)) ?? 0).reduce((sum, item) => sum + item, 0);
  const points = ends.map(end => {
    const numerator = total(value, end), denominator = per ? total(per, end) : trend.perDay ? lookback : 1;
    return denominator ? numerator / denominator * (trend.scale ?? 1) : null;
  });
  const pointTimes = ends.map(end => end === today ? now : new Date(`${shiftDay(end, 1)}T00:00:00`).getTime() - 1);
  const prior = points.slice(0, -1).filter((point): point is number => point !== null), latest = points.at(-1);
  const count = total(rows, ends.at(-1)!);
  const text = (value: string) => value.replaceAll("{days}", String(lookback));
  return { kind: "trend", value: latest ?? undefined, valueLabel: text(trend.valueLabel), pointValueLabel: text(trend.pointValueLabel), trendLabel: trend.trendLabel ?? `Trailing ${lookback}d`, format: trend.format,
    sampleLabel: `${count.toLocaleString()} ${trend.sample} in the latest ${lookback === 1 ? "day" : `${lookback} days`}`, points, pointTimes, lookbackDays: lookback,
    baseline: prior.length ? prior.reduce((sum, item) => sum + item, 0) / prior.length : undefined, detail: text(trend.detail) };
}
