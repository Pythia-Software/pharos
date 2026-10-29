import { EMPTY_QUERY, decodeQueryStrict, inspectBucketQuery, isMetricCycle, type CalendarGroupKey, type GroupKey, type AggregationClause, type QueryState, type SummaryColumn, type CellDisplay, type WhereTerm, type OrderByClause } from "@pythia-software/query-table-core";
import { readUsageWorkspace, type UsageDataset, type UsageTopic } from "./usage-workspace-model";
import { withTimeRange } from "./chart-range";
import type { ConcurrencySelection } from "./concurrency";
import { localDay } from "./chart-period";
import { shiftCalendarDay } from "./concurrency-settings";

export function readDashboardQuery(params: URLSearchParams, fallback: QueryState): { query: QueryState; error: string } {
  const token = params.get("analyze_filters");
  if (!token) return { query: fallback, error: "" };
  try {
    const query = decodeQueryStrict(token);
    if (query && !query.grouping) return { query, error: "" };
  } catch { /* A broken filter link must not broaden the requested population. */ }
  return { query: fallback, error: "This dashboard link has invalid filters. Open the legacy view or choose another dashboard." };
}

// A dashboard link as the page opens it: its chart over the dashboard's
// defaults, if valid, and its filters, or the range analyze_days names
// (readUsageWorkspace) without them. migrate reads an older link's state and
// population (readAnalysisLink); migrated says the link must be rewritten.
export function readDashboardLink<T extends object>(params: URLSearchParams, defaults: T, valid: (value: T) => boolean, time: { first: string; last: string }, migrate?: (view: T, where: WhereTerm[]) => { state: T; where: WhereTerm[] }, now = new Date()) {
  let view = defaults, error = "";
  if (params.has("analyze_chart")) {
    try {
      const saved = JSON.parse(params.get("analyze_chart")!);
      if (!saved || typeof saved !== "object" || Array.isArray(saved)) throw new Error();
      const candidate = { ...defaults, ...saved };
      if (valid(candidate)) view = candidate; else throw new Error();
    }
    catch { error = "This dashboard link has invalid chart settings. Choose another dashboard to reset it."; }
  }
  const from = new Date(now); from.setHours(0, 0, 0, 0); from.setDate(from.getDate() - readUsageWorkspace(params).days + 1);
  const restored = readDashboardQuery(params, { ...EMPTY_QUERY, where: withTimeRange([], time, from) });
  const read = migrate && !error && !restored.error ? migrate(view, restored.query.where) : { state: view, where: restored.query.where };
  return { view: read.state, where: read.where, error: error || restored.error, migrated: read.where !== restored.query.where || read.state !== view };
}

// Materialize the graph's dimensions as result rows. Its filters belong to
// the source stage; the metric formulas remain complete native summaries.
function summaryDisplay(clause: AggregationClause): CellDisplay | undefined {
  if (clause.field === "duration_ms" || clause.field === "total_duration_ms" || clause.field === "max_duration_ms") return { kind: "duration", sourceUnit: "milliseconds" };
  const format = clause.display?.format;
  if (format?.kind === "number" || format?.kind === "percent") return { kind: format.kind, ...(format.decimals !== undefined ? { decimals: format.decimals } : {}) };
  if (format?.kind === "duration" && (!format.sourceUnit || ["milliseconds", "seconds", "minutes", "hours", "days"].includes(format.sourceUnit))) return {
    kind: "duration", ...(format.sourceUnit ? { sourceUnit: format.sourceUnit as "milliseconds" | "seconds" | "minutes" | "hours" | "days" } : {}), ...(format.decimals !== undefined ? { decimals: format.decimals } : {}),
  };
  return undefined;
}

// Metric clauses as the grouped rows they draw: one summary per clause over
// their shared keys. A calendar date grouping (a timestamp by hour, say)
// becomes a calendar group key, one row per local interval; a cycle,
// occurrences, whole intervals or filled empty intervals have no grouped
// rows to match.
export function groupedDashboardQuery(query: QueryState): QueryState {
  if (query.grouping) return query;
  const clauses = query.aggregations ?? [];
  if (!clauses.length) return query;
  const fields = clauses[0].groupBy, dateGrouping = clauses[0].dateGrouping;
  if (clauses.some(clause => JSON.stringify(clause.groupBy) !== JSON.stringify(fields) || JSON.stringify(clause.dateGrouping) !== JSON.stringify(dateGrouping))) throw new Error("Choose measures with the same grouping before opening grouped results.");
  const calendar = dateGrouping && !isMetricCycle(dateGrouping.interval) && !dateGrouping.occurrence && !dateGrouping.partialIntervals && !dateGrouping.includeEmpty && fields[0] === dateGrouping.field;
  if (dateGrouping && !calendar || clauses.some(clause => clause.where?.length || clause.expressionY || clause.distribution || clause.minCount || clause.window || clause.share || clause.scope && clause.scope !== "allMatching")) throw new Error("This graph needs its own grouped-query conversion.");
  const keys: GroupKey[] = fields.map((field, index) => index === 0 && calendar ? { field, interval: dateGrouping.interval as CalendarGroupKey["interval"], ...dateGrouping.timeZone ? { timeZone: dateGrouping.timeZone } : {} } : field);
  const summaries: SummaryColumn[] = clauses.map((clause, index) => ({
    id: `dashboard_${index + 1}`, op: clause.expression ? "formula" : clause.op,
    ...(clause.field && !clause.expression ? { field: clause.field } : {}),
    ...(clause.expression ? { expression: clause.expression } : {}),
    label: clause.label || clause.field || "Records",
    ...(summaryDisplay(clause) ? { display: summaryDisplay(clause) } : {}),
  }));
  const orderBy: OrderByClause[] = (clauses[0].sort ?? []).flatMap(sort => {
    const field = sort.key === "value" ? summaries[0].id : /^group\d+$/.test(sort.key) ? fields[Number(sort.key.slice(5))] : undefined;
    return field ? [{ field, dir: sort.dir }] : [];
  });
  for (const field of fields) if (!orderBy.some(sort => sort.field === field)) orderBy.push({ field, dir: "asc" });
  const { aggregations: _, ...source } = query;
  return { ...EMPTY_QUERY, limit: query.limit, grouping: { source: { ...source, offset: 0 }, keys, summaries, representative: null },
    select: [...fields, ...summaries.map(column => column.id)].map(field => ({ field })), orderBy };
}

// The ribbon is continuous; its native query dataset is hourly occupancy.
// Select all hours touching the chosen interval, and label that grain in UI.
export function parallelismSelectionQuery(selection: ConcurrencySelection): QueryState {
  const { settings, bounds, selected } = selection;
  return { ...EMPTY_QUERY, where: [
    { field: "started_at", op: ">", value: new Date(bounds[0] - 3_600_000).toISOString() },
    { field: "started_at", op: "<", value: new Date(bounds[1]).toISOString() },
    { field: "counting", op: "=", value: settings.counting },
    { field: "gap_minutes", op: "=", value: String(settings.gap) },
    ...settings.repository ? [{ field: "repository_name", op: "=" as const, value: settings.repository }] : [],
    ...selected ? [{ field: "unit_id", op: "=" as const, value: settings.counting === "agent" ? selected.root_conversation_id || selected.conversation_id : selected.workspace_id }] : [],
  ], aggregations: [
    { id: "active_units", op: "sum", field: "unit_hours", groupBy: ["repository_name"], label: "Active unit hours" },
    { id: "wall_time", op: "sum", field: "elapsed_hours", groupBy: ["repository_name"], label: "Threaded wall hours" },
    { id: "parallel_time", op: "sum", field: "parallel_elapsed_hours", groupBy: ["repository_name"], label: "Parallel threaded wall hours" },
  ] };
}


// Match a canonical YYYY-MM-DD string lexically using supported text filters.
// Wider and open ranges stay compact instead of enumerating years of days.
function dayBoundaryPattern(day: string, direction: "before" | "after"): string {
  const alternatives = [day];
  for (let index = 0; index < day.length; index++) {
    if (day[index] === "-") continue;
    const digit = Number(day[index]), low = direction === "after" ? digit + 1 : 0, high = direction === "after" ? 9 : digit - 1;
    if (low > high) continue;
    alternatives.push(day.slice(0, index) + (low === high ? String(low) : `[${low}-${high}]`) + day.slice(index + 1).replace(/\d/g, "[0-9]"));
  }
  return `^(${alternatives.join("|")})$`;
}

// Tool summary dates are text dimensions. Small ranges use indexed equality;
// other ranges use compact date patterns, including one-sided date pickers.
export function toolDashboardWhere(where: WhereTerm[], bounds: { from?: Date; to?: Date }, now = new Date()): WhereTerm[] {
  if (!bounds.from && !bounds.to) return where;
  const kept = where.filter(term => !("field" in term && term.field === "day" && !term.negated && [">", ">=", "<", "<="].includes(term.op)));
  const start = bounds.from ? localDay(bounds.from) : undefined;
  const end = localDay(bounds.to ? new Date(bounds.to.getTime() - 1) : now);
  const days: Array<{ field: string; op: "="; value: string }> = [];
  for (let day = start; day && day <= end && days.length <= 90; day = shiftCalendarDay(day, 1)) {
    days.push({ field: "day", op: "=", value: day });
  }
  if (!start || days.length > 90) return [...kept,
    ...start ? [{ field: "day", op: "matches_regex" as const, value: dayBoundaryPattern(start, "after") }] : [],
    { field: "day", op: "matches_regex", value: dayBoundaryPattern(end, "before") }];
  return [...kept, days.length === 1 ? days[0] : days.length ? { any: days } : { field: "day", op: "=", value: "No days in selected range" }];
}
