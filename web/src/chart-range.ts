import { isOrGroup, parseRelativeDuration, type WhereClause, type WhereTerm } from "@pythia-software/query-table-core";
import { localDay, shiftPeriod, type ChartPeriod } from "./chart-period";
import { validRollingPeriods } from "./chart-series";

// The time filter the charts and tables share, without any DOM: reading its
// bounds, writing a range, and widening a chart's request for smoothing.

export type ChartRange = "1d" | "7d" | "30d" | "90d" | "6m" | "1y";

export type RangeChange = (from?: Date, to?: Date, preset?: ChartRange | "all") => void;

export type FilterChange = (change: (where: WhereTerm[]) => WhereTerm[]) => void;

// Range presets the charts offer.
export const chartRanges: Array<[ChartRange, string]> = [["1d", "1 day"], ["7d", "7 days"], ["30d", "30 days"], ["90d", "90 days"], ["6m", "6 months"], ["1y", "1 year"]];

const pad2 = (value: number) => String(value).padStart(2, "0");

// rangeStart is the local midnight a range preset starts from.
export function rangeStart(range: ChartRange): Date {
  const start = new Date();
  start.setHours(0, 0, 0, 0);
  if (range.endsWith("d")) start.setDate(start.getDate() - parseInt(range) + 1);
  else if (range === "6m") { start.setMonth(start.getMonth() - 6); start.setDate(start.getDate() + 1); }
  else { start.setFullYear(start.getFullYear() - 1); start.setDate(start.getDate() + 1); }
  return start;
}

// The charts and the table share one time filter: a row matches when its
// activity, from its first to its last timestamp, overlaps the range. Dragging
// across columns, a range preset, and a filter typed into the table all set it.
export type TimeFields = { first: string; last: string };

export const usageTime: TimeFields = { first: "first_usage_at", last: "last_usage_at" };

export const writingTime: TimeFields = { first: "first_message_at", last: "last_message_at" };

// to is exclusive.
export type TimeBounds = { from?: Date; to?: Date; preset?: ChartRange };

export const rangeDays = (range: ChartRange) => range === "6m" ? 180 : range === "1y" ? 365 : parseInt(range);

// parseStamp reads a datetime filter value as the service does: RFC 3339, or a
// zone-less date or time as UTC.
function parseStamp(raw: string, now: number): Date | undefined {
  const relative = parseRelativeDuration(raw.trim());
  if (relative !== null) return new Date(now + relative);
  let text = raw.trim().replace(" ", "T");
  if (/^\d{4}-\d{2}-\d{2}$/.test(text)) text += "T00:00:00";
  if (/T\d{2}:\d{2}(:\d{2}(\.\d+)?)?$/.test(text)) text += "Z";
  const date = new Date(text);
  return Number.isNaN(date.getTime()) ? undefined : date;
}

// localStamp writes a time as RFC 3339 with the local offset, so a day
// boundary picked in the chart is local midnight to the service too.
function localStamp(date: Date): string {
  const offset = -date.getTimezoneOffset(), hours = Math.floor(Math.abs(offset) / 60);
  return `${localDay(date)}T${pad2(date.getHours())}:${pad2(date.getMinutes())}:00${offset < 0 ? "-" : "+"}${pad2(hours)}:${pad2(Math.abs(offset) % 60)}`;
}

function isTimeTerm(term: WhereTerm, fields: TimeFields): term is WhereClause {
  return !isOrGroup(term) && (term.field === fields.first || term.field === fields.last) && !term.negated && [">", ">=", "<", "<="].includes(term.op);
}

export function timeBounds(where: WhereTerm[], fields: TimeFields, now = Date.now()): TimeBounds {
  const bounds: TimeBounds = {};
  for (const term of where) {
    if (!isTimeTerm(term, fields)) continue;
    const at = parseStamp(term.value, now);
    if (!at) continue;
    if (term.op === ">" || term.op === ">=") { if (!bounds.from || at > bounds.from) bounds.from = at; }
    else {
      const end = term.op === "<=" ? new Date(at.getTime() + 1) : at;
      if (!bounds.to || end < bounds.to) bounds.to = end;
    }
  }
  // Keep the saved relative intent alongside its resolved plotting bounds.
  // Only exact preset pairs qualify; extra time predicates remain custom.
  const terms = where.filter(term => isTimeTerm(term, fields));
  if (terms.length === 2 && terms.some(term => term.op === "<" && term.value === "+0s")) {
    const preset = chartRanges.find(([range]) => terms.some(term => term.op === ">=" && term.value === `-${rangeDays(range)}d`))?.[0];
    if (preset) bounds.preset = preset;
  }
  return bounds;
}

export function withTimeRange(where: WhereTerm[], fields: TimeFields, from?: Date, to?: Date, preset?: ChartRange | "all"): WhereTerm[] {
  const kept = where.filter(term => !isTimeTerm(term, fields));
  if (preset === "all") return kept;
  if (preset) return [...kept, { field: fields.last, op: ">=", value: `-${rangeDays(preset)}d` }, { field: fields.first, op: "<", value: "+0s" }];
  if (from) kept.push({ field: fields.last, op: ">=", value: localStamp(from) });
  if (to) kept.push({ field: fields.first, op: "<", value: localStamp(to) });
  return kept;
}

// Expand only the chart request. The visible filters, totals and drilldowns
// retain their original dates. N periods includes the current interval and
// N-1 preceding intervals, aligned to the chart's calendar grid.
export function smoothingWhere(where: WhereTerm[], fields: TimeFields, period: ChartPeriod, periods: number, count = 1, now = Date.now()): WhereTerm[] {
  const bounds = timeBounds(where, fields, now);
  if (!bounds.from || !validRollingPeriods(periods) || periods < 2) return where;
  const lower = (term: WhereTerm): term is WhereClause => isTimeTerm(term, fields) && [">", ">="].includes(term.op);
  const field = where.find(term => lower(term) && parseStamp(term.value, now)?.getTime() === bounds.from!.getTime());
  const start = shiftPeriod(bounds.from, period, 1 - periods, count);
  // Preserve fact-time vs overlap-time selection and relative upper bounds.
  // Resolving +0s into a new timestamp on each render would refetch forever.
  return [...where.filter(term => !lower(term)), ...(start ? [{ field: field && "field" in field ? field.field : fields.last, op: ">=" as const, value: localStamp(start) }] : [])];
}

// chartSpan is the first and last day a chart shows: the time filter's range,
// else from the earliest data to today.
export function chartSpan(bounds: TimeBounds, earliest?: Date, now = Date.now()): [Date, Date] {
  const end = bounds.to ? new Date(bounds.to.getTime() - 1) : new Date(now);
  return [bounds.from ?? (earliest && earliest < end ? earliest : end), end];
}
