import { boxSummary, coerceValue, loadSchema, metricDateFloor, metricDateNext, metricDateTime, resolveFieldName, type AggregationClause, type AggregationResultEntry, type QueryState, type WhereClause } from "@pythia-software/query-table-core";
import writingDocument from "../../schemas/writing_messages.schema.json";
import parallelismDocument from "../../schemas/parallelism.schema.json";
import type { QueryRecipe } from "./curiosity-data";
import type { CardCover } from "./card-cover";

export const weekdays = ["Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"];
const calendarSchemas = [loadSchema(writingDocument as never), loadSchema(parallelismDocument as never)];

// Only a conjunction of simple date bounds defines one interval that we can
// safely fill with idle days. Preserve native daily results for other date
// predicates instead of guessing which days or parts of days they selected.
function weekdayDatePredicates(clause: AggregationClause, query?: QueryState): WhereClause[] | undefined {
  const field = clause.dateGrouping?.field.trim().toLowerCase();
  const schema = calendarSchemas.find(schema => schema.fields.some(item => item.name === field));
  if (!schema) return;
  const name = (predicate: WhereClause) => resolveFieldName(schema, predicate.field);
  const isDate = (predicate: WhereClause) => name(predicate) === field;
  // Persisted Day/Week/Month/hour dimensions can exclude whole or partial
  // dates independently of the timestamp bounds. Resolve their aliases from
  // the dataset schema and leave these edited populations to the daily chart.
  const calendar = new Set(schema.fields.filter(item => item.group === "Time" && item.name !== field).map(item => item.name));
  const isCalendar = (predicate: WhereClause) => calendar.has(name(predicate) ?? "");
  const dates: WhereClause[] = [];
  const where = [...((query?.grouping?.source ?? query)?.where ?? []), ...(clause.where ?? [])];
  for (const term of where) {
    if ("any" in term) {
      if (term.any.some(predicate => isDate(predicate) || isCalendar(predicate))) return;
    } else {
      if (isCalendar(term)) return;
      if (isDate(term)) {
        if (term.negated || ![">=", ">", "<", "<=", "="].includes(term.op)) return;
        dates.push(term);
      }
    }
  }
  return dates;
}

// This is a Pharos cover over native daily reductions. The saved query retains
// its exact daily totals; the published Query Table supplies calendar buckets
// and box statistics, with no changes to its execution or renderer.
export function usesWeekdayCover(recipe: QueryRecipe | undefined, metric: AggregationClause, cover?: CardCover, query?: QueryState): boolean {
  const original = recipe?.measures.find(item => item.id === recipe.weekday?.metricId);
  return recipe?.weekday?.metricId === metric.id && (!cover || cover.mode === "source" && cover.metricId === metric.id && (!cover.kind || cover.kind === "auto"))
    && original?.op === metric.op && original.field === metric.field && original.expression === metric.expression
    && !metric.distribution && !metric.expressionY && metric.scope !== "shownRows" && metric.dateGrouping?.interval === "day" && metric.dateGrouping.includeEmpty
    && metric.dateGrouping.range === "observed"
    && metric.groupBy.length === 1 && metric.groupBy[0] === metric.dateGrouping.field
    && weekdayDatePredicates(metric, query) !== undefined;
}

export function weekdayMetric(clause: AggregationClause, result: AggregationResultEntry, query: QueryState, now: number, unit: string) {
  if (result.error || result.coverage === "partial" || result.hiddenGroups || (result.groupCount ?? 0) > result.buckets.length || result.buckets.some(bucket => bucket.error || bucket.yError || bucket.inputErrorCount)) {
    throw new Error(result.error ?? "Complete daily totals are required for weekday distributions.");
  }
  const config = clause.dateGrouping!, zone = config.timeZone ?? "UTC";
  const dates = weekdayDatePredicates(clause, query);
  if (!dates) throw new Error("Use the native daily preview for complex date predicates.");
  let from = -Infinity, to = now;
  for (const term of dates) {
    // Filters use the engine's datetime coercion (date-only means UTC
    // midnight); the chart timezone applies only when bucketing those instants.
    const value = Number(coerceValue("datetime", term.value, now));
    if (term.op === ">=") from = Math.max(from, value);
    if (term.op === ">") from = Math.max(from, value + 1);
    if (term.op === "<") to = Math.min(to, value);
    if (term.op === "<=") to = Math.min(to, value + 1);
    if (term.op === "=") { from = Math.max(from, value); to = Math.min(to, value + 1); }
  }
  const daily = new Map(result.buckets.map(bucket => [metricDateTime(String(bucket.keys[0]), zone), bucket]));
  // Native reductions fill internal gaps. Extend to the saved window's edges
  // so a quiet first/last week receives the same weight as every other week.
  if (!Number.isFinite(from)) from = Math.min(...daily.keys());
  let observations = 0;
  const samples = Number.isFinite(from) ? cycleSamples("weekday", from, to, zone, start => {
    const bucket = daily.get(start);
    observations += bucket?.count ?? 0;
    return bucket?.value ?? (idleIsZero(clause) ? 0 : null);
  }) : weekdays.map(() => [] as number[]);
  const { result: folded, days } = cycleBoxes("weekday", samples, result.id);
  const metric: AggregationClause = { id: clause.id, op: "count", groupBy: ["weekday"], label: clause.label, distribution: { kind: "box", input: "[daily_value]", whiskers: "tukey" }, display: { kind: "box", maxGroups: 7, xLabel: unit } };
  return { metric, result: folded, days, observations, zone };
}

export type CalendarCycle = "weekday" | "hour";
// Counts and sums of an idle day or hour are zero, as is a sum whose only
// inputs were missing (unpriced cost adds nothing). A ratio or average of
// nothing has no value.
export const idleIsZero = (clause: Pick<AggregationClause, "op" | "expression">) => !clause.expression && ["sum", "count", "count_distinct"].includes(clause.op);
export const hoursOfDay = Array.from({ length: 24 }, (_, hour) => `${String(hour).padStart(2, "0")}:00`);
const maxCycleSlots = { weekday: 10000, hour: 24 * 800 };

// Visit every complete calendar slot in [from, to) once, so idle days or
// hours count as observations. Days are local calendar days in zone. Hours
// step on UTC hour boundaries, the grain of both the usage ledger's hours and
// native hourly buckets, and take the local hour of their start; in zones
// offset by a fraction of an hour that start falls mid-hour locally.
export function cycleSamples(cycle: CalendarCycle, from: number, to: number, zone: string, value: (start: number) => unknown): number[][] {
  const slots = cycle === "weekday" ? weekdays : hoursOfDay;
  const samples = slots.map(() => [] as number[]);
  const label = cycle === "weekday" ? new Intl.DateTimeFormat("en-US", { weekday: "long", timeZone: zone }) : new Intl.DateTimeFormat("en-US", { hour: "numeric", hourCycle: "h23", timeZone: zone });
  const slot = (start: number) => cycle === "weekday" ? weekdays.indexOf(label.format(start)) : Number(label.format(start)) % 24;
  const floor = (time: number) => cycle === "weekday" ? metricDateFloor(time, "day", zone) : Math.floor(time / 3_600_000) * 3_600_000;
  const next = (time: number) => cycle === "weekday" ? metricDateNext(time, "day", zone) : time + 3_600_000;
  let start = floor(from), visited = 0;
  if (start < from) start = next(start);
  for (; next(start) <= to; start = next(start)) {
    if (++visited > maxCycleSlots[cycle]) throw new Error(cycle === "weekday" ? "Choose a shorter range for weekday distributions (at most 10,000 days)." : "Choose a shorter range for hour-of-day profiles (at most 800 days).");
    const amount = value(start);
    if (typeof amount === "number" && Number.isFinite(amount)) samples[slot(start)].push(amount);
  }
  return samples;
}

// One box per weekday or local hour over the slot values cycleSamples found.
export function cycleBoxes(cycle: CalendarCycle, samples: number[][], id: string) {
  const labels = cycle === "weekday" ? weekdays : hoursOfDay;
  const buckets = samples.map((values, index) => {
    const summary = boxSummary(values, "tukey");
    return { keys: [labels[index]], count: values.length, value: summary?.median ?? null, distribution: { kind: "box" as const, summary } };
  });
  const days = samples.reduce((n, values) => n + values.length, 0);
  return { result: { id, buckets, groupCount: labels.length, processedRows: days, coverage: "exact" as const }, days };
}
