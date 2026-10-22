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
  const format = new Intl.DateTimeFormat("en-US", { weekday: "long", timeZone: zone });
  const samples = weekdays.map(() => [] as number[]);
  let observations = 0;
  const daily = new Map(result.buckets.map(bucket => [metricDateTime(String(bucket.keys[0]), zone), bucket]));
  // Native reductions fill internal gaps. Extend to the saved window's edges
  // so a quiet first/last week receives the same weight as every other week.
  if (!Number.isFinite(from)) from = Math.min(...daily.keys());
  if (!Number.isFinite(from)) return summarize();
  let start = metricDateFloor(from, "day", zone);
  if (start < from) start = metricDateNext(start, "day", zone);
  let visited = 0;
  for (; metricDateNext(start, "day", zone) <= to; start = metricDateNext(start, "day", zone)) {
    if (++visited > 10000) throw new Error("Choose a shorter range for weekday distributions (at most 10,000 days).");
    const bucket = daily.get(start);
    observations += bucket?.count ?? 0;
    // An idle-day ratio is null. SUM and COUNT supply real zero-day totals.
    const value = bucket?.value ?? (!bucket?.count && !clause.expression && ["sum", "count"].includes(clause.op) ? 0 : null);
    if (typeof value === "number" && Number.isFinite(value)) samples[weekdays.indexOf(format.format(start))].push(value);
  }
  return summarize();

  function summarize() {
    const buckets = samples.map((values, index) => {
      const summary = boxSummary(values, "tukey");
      return { keys: [weekdays[index]], count: values.length, value: summary?.median ?? null, distribution: { kind: "box" as const, summary } };
    });
    const days = samples.reduce((n, values) => n + values.length, 0);
    const metric: AggregationClause = { id: clause.id, op: "count", groupBy: ["weekday"], label: clause.label, distribution: { kind: "box", input: "[daily_value]", whiskers: "tukey" }, display: { kind: "box", maxGroups: 7, xLabel: unit } };
    return { metric, result: { id: result.id, buckets, groupCount: 7, processedRows: days, coverage: "exact" as const }, days, observations, zone };
  }
}
