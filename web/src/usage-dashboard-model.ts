import { EMPTY_QUERY, decodeQueryStrict, inspectBucketQuery, type AggregationClause, type QueryState, type SummaryColumn, type CellDisplay, type WhereTerm, type OrderByClause } from "@pythia-software/query-table-core";
import type { UsageDataset, UsageTopic } from "./usage-workspace-model";
import type { ConcurrencySelection } from "./concurrency";
import { localDay } from "./chart-period";
import { shiftCalendarDay } from "./concurrency-settings";

export type RecordDashboardTopic = Extract<UsageTopic, "skills" | "time">;
type Measure = { label: string; op: AggregationClause["op"]; field?: string; expression?: string; unit: string; divisor?: number };
type DashboardSpec = { dataset: UsageDataset; time: string; measures: Measure[]; splits: Array<[string, string]>; note: string };
export const recordDashboards: Record<RecordDashboardTopic, DashboardSpec> = {
  time: { dataset: "tool_calls", time: "started_at", measures: [
    { label: "Recorded tool time", op: "sum", field: "duration_ms", unit: "hours", divisor: 3_600_000 },
    { label: "Tool calls", op: "count", unit: "calls" },
  ], splits: [["activity", "Activity"], ["tool_category", "Tool category"], ["provider", "Provider"], ["channel", "Channel"], ["machine_id", "Machine"], ["execution_machine_id", "Execution machine"], ["repository_name", "Repository"]], note: "Recorded durations can overlap and may include approval waits. Their sum is tool time, not elapsed wall time. Use the legacy view for command-family comparisons and timelines." },
  skills: { dataset: "skill_usages", time: "created_at", measures: [
    { label: "Evidence records", op: "count", unit: "records" },
    { label: "Retained body volume", op: "sum", expression: "SUM(IF([content_bytes]>0,[content_bytes],0))", unit: "bytes" },
  ], splits: [["skill_name", "Skill"], ["evidence_type", "Evidence type"], ["provider", "Provider"], ["channel", "Channel"], ["machine_id", "Machine"], ["execution_machine_id", "Execution machine"], ["repository_name", "Repository"], ["status", "Status"]], note: "These are observed invocations, file loads, and harness-reported loads. A load does not prove a skill was followed. Body volume sums known positive recorded sizes, including repeated loads." },

};

export function recordDashboardMetric(topic: RecordDashboardTopic, metric: number, period: string, split: string): AggregationClause {
  const spec = recordDashboards[topic], measure = spec.measures[metric] ?? spec.measures[0];
  return { id: "usage_dashboard", op: measure.op, ...(measure.field ? { field: measure.field } : {}), ...(measure.expression ? { expression: measure.expression } : {}), groupBy: [period, split], label: measure.label };
}

export function readDashboardQuery(params: URLSearchParams, fallback: QueryState): { query: QueryState; error: string } {
  const token = params.get("analyze_filters");
  if (!token) return { query: fallback, error: "" };
  try {
    const query = decodeQueryStrict(token);
    if (query && !query.grouping) return { query, error: "" };
  } catch { /* A broken filter link must not broaden the requested population. */ }
  return { query: fallback, error: "This dashboard link has invalid filters. Open the legacy view or choose another dashboard." };
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

export function groupedDashboardQuery(query: QueryState): QueryState {
  if (query.grouping) return query;
  const clauses = query.aggregations ?? [];
  if (!clauses.length) return query;
  const keys = clauses[0].groupBy;
  if (clauses.some(clause => JSON.stringify(clause.groupBy) !== JSON.stringify(keys))) throw new Error("Choose measures with the same grouping before opening grouped results.");
  if (clauses.some(clause => clause.where?.length || clause.dateGrouping || clause.expressionY || clause.distribution || clause.minCount || clause.scope && clause.scope !== "allMatching")) throw new Error("This graph needs its own grouped-query conversion.");
  const summaries: SummaryColumn[] = clauses.map((clause, index) => ({
    id: `dashboard_${index + 1}`, op: clause.expression ? "formula" : clause.op,
    ...(clause.field && !clause.expression ? { field: clause.field } : {}),
    ...(clause.expression ? { expression: clause.expression } : {}),
    label: clause.label || clause.field || "Records",
    ...(summaryDisplay(clause) ? { display: summaryDisplay(clause) } : {}),
  }));
  const orderBy: OrderByClause[] = (clauses[0].sort ?? []).flatMap(sort => {
    const field = sort.key === "value" ? summaries[0].id : /^group\d+$/.test(sort.key) ? keys[Number(sort.key.slice(5))] : undefined;
    return field ? [{ field, dir: sort.dir }] : [];
  });
  for (const field of keys) if (!orderBy.some(sort => sort.field === field)) orderBy.push({ field, dir: "asc" });
  const { aggregations: _, ...source } = query;
  return { ...EMPTY_QUERY, limit: query.limit, grouping: { source: { ...source, offset: 0 }, keys, summaries, representative: null },
    select: [...keys, ...summaries.map(column => column.id)].map(field => ({ field })), orderBy };
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

// A cumulative point includes the full selected range through its interval.
// Inspect the non-time group, retaining the source dates and other filters,
// then cap its calendar key at this point (day, week, or month).
export function inspectDashboardBucket(query: QueryState, bucket: Parameters<typeof inspectBucketQuery>[1], clause: AggregationClause, schema: Parameters<typeof inspectBucketQuery>[3], cumulative = false): QueryState | null {
  if (!cumulative) return inspectBucketQuery(query, bucket, clause, schema);
  const [period, ...groupBy] = clause.groupBy ?? [];
  if (!period || bucket.keys[0] == null) return null;
  const records = inspectBucketQuery(query, { ...bucket, keys: bucket.keys.slice(1) }, { ...clause, groupBy }, schema);
  return records ? { ...records, where: [...records.where, { field: period, op: "<=", value: String(bucket.keys[0]) }] } : null;
}
