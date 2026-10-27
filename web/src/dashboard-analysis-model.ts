import { EMPTY_QUERY, inspectBucketQuery, metricDateTime, type AggOp, type AggregationBucket, type AggregationClause, type AggregationResultEntry, type FieldSchema, type MetricDisplay, type MetricValueFormat, type QueryState, type SummaryColumn, type WhereTerm } from "@pythia-software/query-table-core";
import type { ChartScale } from "./chart-scale";
import { cycleBoxes, cycleSamples, hoursOfDay, weekdays, type CalendarCycle } from "./weekday-cover";

// A dashboard analysis is a view over one catalog: which records count as
// one observation (the unit), which measure they carry, and how groups of
// observations are compared. A catalog lists only combinations a dashboard
// can state truthfully; the state is one flat object so it round-trips through
// `analyze_chart` beside a dashboard's existing trend fields.

export type AnalysisView = "trend" | "compare" | "distribution" | "scatter" | "cycle";
export type AnalysisReduce = "total" | "mean" | "median" | "max" | "count";
export type AnalysisMeasure = {
  label: string; unit: string; format: MetricValueFormat;
  // The aggregate over any set of records: one group, or one unit.
  op: AggOp; field?: string; expression?: string;
  // A per-record input makes the measure eligible for record distributions.
  value?: string;
  // Additive measures partition a total, so groups may be shown as shares.
  additive: boolean;
  // A population the measure implies, such as fully priced usage.
  where?: WhereTerm[];
  note: string;
};
export type AnalysisBreakdown = { label: string; field: string; where?: WhereTerm[] };
// Breakdowns map to the field that holds them at unit level. A breakdown is
// listed only when it is constant within one unit.
export type AnalysisUnit = { label: string; plural: string; key: string; where: WhereTerm[]; breakdowns: Record<string, string>; note: string };
export type AnalysisCatalog = {
  dataset: string;
  time: { first: string; last: string };
  measures: Record<string, AnalysisMeasure>;
  breakdowns: Record<string, AnalysisBreakdown>;
  units: Record<string, AnalysisUnit>;
  records: { label: string; plural: string; note: string };
  // Calendar dimensions the cycle view folds: hour keys are instants, day
  // keys local calendar dates.
  cycle: Record<CalendarCycle, string>;
  // How the dashboard's own trend names its metrics and splits.
  trend: { metrics: Record<string, { measure: string; share: boolean }>; splits: string[]; metric: string; split: string };
  scatter: [string, string];
};

export type AnalysisState = {
  view?: AnalysisView; unit?: string; metric: string; metricY?: string; split: string;
  reduce?: AnalysisReduce; compareAs?: "bars" | "share"; minSamples?: number;
  scale?: ChartScale; scaleY?: ChartScale; cycle?: CalendarCycle; seriesLimit?: number;
};
export type ResolvedAnalysis = AnalysisState & Required<Pick<AnalysisState, "view" | "unit" | "metricY" | "reduce" | "compareAs" | "minSamples" | "scaleY" | "cycle">>;

export const records = "records", noSplit = "none";
export const analysisViews: Array<[AnalysisView, string]> = [["trend", "Over time"], ["compare", "Compare groups"], ["distribution", "Distribution"], ["scatter", "Two measures"], ["cycle", "Daily cycle"]];
export const analysisReductions: Array<[AnalysisReduce, string]> = [["total", "Total"], ["mean", "Mean per unit"], ["median", "Median per unit"], ["max", "Largest unit"], ["count", "Number of units"]];
export const sampleThresholds = [0, 2, 3, 5, 10, 20, 50, 100];
const analysisDefaults = { unit: records, reduce: "total", compareAs: "bars", minSamples: 0, scaleY: "linear", cycle: "weekday" } as const;
const maxGroups = 1000, maxPoints = 2000;

const oneOf = <T,>(value: unknown, options: readonly T[]): value is T => options.includes(value as T);
const has = (record: object, key: unknown) => typeof key === "string" && Object.hasOwn(record, key);

// Field-level validity for a link or saved state. Combinations are coerced by
// normalizeAnalysis rather than rejected, so an older link still opens.
export function validAnalysisState(catalog: AnalysisCatalog, state: AnalysisState): boolean {
  if (state.view === undefined || state.view === "trend") return true;
  return oneOf(state.view, analysisViews.map(([view]) => view))
    && (state.unit === undefined || state.unit === records || has(catalog.units, state.unit))
    && has(catalog.measures, state.metric) && (state.metricY === undefined || has(catalog.measures, state.metricY))
    && (state.split === noSplit || has(catalog.breakdowns, state.split))
    && (state.reduce === undefined || oneOf(state.reduce, analysisReductions.map(([reduce]) => reduce)))
    && (state.compareAs === undefined || oneOf(state.compareAs, ["bars", "share"]))
    && (state.minSamples === undefined || Number.isInteger(state.minSamples) && state.minSamples >= 0 && state.minSamples <= 1_000_000)
    && (state.scaleY === undefined || oneOf(state.scaleY, ["linear", "logarithmic", "half"]))
    && (state.cycle === undefined || oneOf(state.cycle, ["weekday", "hour"]));
}

export const shareable = (catalog: AnalysisCatalog, state: ResolvedAnalysis) => state.view === "compare"
  && (state.unit !== records && state.reduce === "count" || catalog.measures[state.metric].additive && state.reduce === "total");
export const splitValid = (catalog: AnalysisCatalog, unit: string, split: string) => split === noSplit || has(catalog.breakdowns, split) && (unit === records || has(catalog.units[unit].breakdowns, split));

// Fill defaults and move every setting the view cannot use to the nearest
// one it can: a cycle profiles records; a scatter pairs two measures of an
// entity; a record distribution needs a per-record value.
export function normalizeAnalysis<T extends AnalysisState>(catalog: AnalysisCatalog, state: T): T & ResolvedAnalysis {
  const next = { ...analysisDefaults, ...state } as T & ResolvedAnalysis;
  next.view ??= "trend";
  if (next.view === "trend") return next;
  const firstUnit = Object.keys(catalog.units)[0];
  if (!has(catalog.measures, next.metric)) next.metric = catalog.scatter[0];
  if (next.unit !== records && !has(catalog.units, next.unit)) next.unit = records;
  if (next.view === "cycle") { next.unit = records; next.split = noSplit; }
  if (next.view === "scatter") {
    if (next.unit === records) next.unit = firstUnit;
    if (!has(catalog.measures, next.metricY) || next.metricY === next.metric) next.metricY = catalog.scatter.find(id => id !== next.metric) ?? Object.keys(catalog.measures).find(id => id !== next.metric)!;
  } else if (!has(catalog.measures, next.metricY)) next.metricY = catalog.scatter[1];
  if (next.view === "distribution" && next.unit === records && !catalog.measures[next.metric].value) next.unit = firstUnit;
  if (!splitValid(catalog, next.unit, next.split)) next.split = noSplit;
  // A cycle summarizes slot values: a box per slot by default, or their total or mean.
  if (next.view === "cycle") { if (state.reduce === undefined || !["total", "mean", "median"].includes(next.reduce)) next.reduce = "median"; }
  else if (next.unit === records) next.reduce = "total";
  if (next.compareAs === "share" && !shareable(catalog, next)) next.compareAs = "bars";
  return next;
}

// Move between views, carrying what both share. The trend's share metrics
// become a share display, and back.
export function switchView<T extends AnalysisState>(catalog: AnalysisCatalog, state: T, view: AnalysisView): T & ResolvedAnalysis {
  const current = state.view ?? "trend";
  if (view === "trend") {
    if (current === "trend") return normalizeAnalysis(catalog, state);
    const share = state.compareAs === "share" && current === "compare";
    const trend = Object.entries(catalog.trend.metrics);
    const metric = trend.find(([, item]) => item.measure === state.metric && item.share === share)?.[0] ?? trend.find(([, item]) => item.measure === state.metric && !item.share)?.[0] ?? catalog.trend.metric;
    return normalizeAnalysis(catalog, { ...state, view, metric, split: catalog.trend.splits.includes(state.split) ? state.split : catalog.trend.split });
  }
  if (current === "trend") {
    const mapped = catalog.trend.metrics[state.metric];
    return normalizeAnalysis(catalog, { ...state, view, metric: mapped?.measure ?? state.metric, ...(mapped?.share ? { compareAs: "share" as const } : {}), ...view === "cycle" ? { reduce: "median" as const } : {},
      split: has(catalog.breakdowns, state.split) ? state.split : noSplit });
  }
  return normalizeAnalysis(catalog, { ...state, view, ...view === "cycle" ? { reduce: "median" as const } : {} });
}

const unique = (terms: WhereTerm[]) => terms.filter((term, index) => terms.findIndex(other => JSON.stringify(other) === JSON.stringify(term)) === index);
const log = (scale?: ChartScale) => ({ mode: scale === "logarithmic" ? "log" as const : "linear" as const });
function summary(id: string, measure: AnalysisMeasure): SummaryColumn {
  return measure.expression ? { id, op: "formula", expression: measure.expression, label: measure.label }
    : { id, op: measure.op, ...(measure.field ? { field: measure.field } : {}), label: measure.label };
}
function aggregate(measure: AnalysisMeasure) {
  return { op: measure.op, ...(measure.field && !measure.expression ? { field: measure.field } : {}), ...(measure.expression ? { expression: measure.expression } : {}) };
}

export type AnalysisQuery = {
  // The query the chart executes; its first aggregation is the chart's clause.
  query: QueryState; clause: AggregationClause;
  // What "View supporting query" opens: the same population and grouping.
  supporting: QueryState;
  grouped: boolean;
};

// The executed query for a non-trend view. Records compare and distribute
// matching records directly. An entity unit first groups records into one row
// per unit (and its constant breakdown), as card recipes do, then compares,
// distributes or pairs those rows. A threshold is native minCount: it removes
// small groups before groupLimit, so they never crowd out eligible ones.
export function analysisQuery(catalog: AnalysisCatalog, input: AnalysisState, where: WhereTerm[]): AnalysisQuery {
  const state = normalizeAnalysis(catalog, input);
  if (state.view === "trend") throw new Error("The trend view has its own chart.");
  const measure = catalog.measures[state.metric], measureY = catalog.measures[state.metricY];
  const breakdown = state.split === noSplit ? undefined : catalog.breakdowns[state.split];
  const unit = state.unit === records ? undefined : catalog.units[state.unit];
  const sourceWhere = unique([...where, ...unit?.where ?? [], ...breakdown?.where ?? [], ...measure.where ?? [], ...state.view === "scatter" ? measureY.where ?? [] : []]);
  const minCount = state.minSamples > 1 && breakdown && state.view !== "scatter" ? { minCount: state.minSamples } : {};
  const valueLabel = state.view === "compare" && unit && state.reduce !== "total" ? `${analysisReductions.find(([reduce]) => reduce === state.reduce)![1].replace("unit", unit.label.toLowerCase())}${state.reduce === "count" ? "" : ` · ${measure.label}`}` : measure.label;
  const format = state.view === "compare" && state.reduce === "count" ? { kind: "number" as const, decimals: 0 } : measure.format;
  const compareDisplay = (groups: boolean): MetricDisplay => !groups ? { kind: "value", format, valueLabel }
    : state.compareAs === "share" ? { kind: "pie", maxGroups: state.seriesLimit ?? 8, showOther: true, legendPosition: "right", format }
    : { kind: "bar-horizontal", maxGroups: state.seriesLimit ?? 12, legendPosition: "none", xLabel: valueLabel, xScale: log(state.scale), format };
  const distributionDisplay = (box: boolean): MetricDisplay => ({ kind: box ? "box" : "histogram", maxGroups: state.seriesLimit ?? 12, xLabel: measure.label, xScale: log(state.scale), ...(box ? { format } : { xFormat: format }) });

  if (state.view === "cycle") {
    const field = catalog.cycle[state.cycle];
    const clause: AggregationClause = { id: "analysis", ...aggregate(measure), groupBy: [field], groupLimit: 10000, sort: [{ key: "group0", dir: "asc" }], label: measure.label };
    const query = { ...EMPTY_QUERY, where: sourceWhere, aggregations: [clause] };
    return { query, clause, grouped: false, supporting: groupedRecords(sourceWhere, [field], [summary("analysis_value", measure)], [], field) };
  }

  if (!unit) {
    const groupBy = breakdown ? [breakdown.field] : [];
    const clause: AggregationClause = state.view === "distribution"
      ? { id: "analysis", op: "count", groupBy, groupLimit: maxGroups, sort: [{ key: "samples", dir: "desc" }], label: measure.label, ...minCount,
        distribution: breakdown ? { kind: "box", input: `[${measure.value}]`, whiskers: "tukey" } : { kind: "histogram", input: `[${measure.value}]` }, display: distributionDisplay(Boolean(breakdown)) }
      : { id: "analysis", ...aggregate(measure), groupBy, groupLimit: maxGroups, sort: [{ key: "value", dir: "desc" }], label: valueLabel, ...minCount, display: compareDisplay(Boolean(breakdown)) };
    const query = { ...EMPTY_QUERY, where: sourceWhere, aggregations: [clause] };
    const summaries = [summary("analysis_value", measure), { id: "analysis_records", op: "count" as const, label: catalog.records.plural }];
    if (state.view === "distribution") summaries.splice(1, 0, { id: "analysis_median", op: "formula", expression: `MEDIAN([${measure.value}])`, label: `Median ${measure.label.toLowerCase()} per ${catalog.records.label.toLowerCase()}` });
    const having: WhereTerm[] = "minCount" in minCount ? [{ field: "analysis_records", op: ">=", value: String(state.minSamples) }] : [];
    return { query, clause, grouped: false, supporting: breakdown ? groupedRecords(sourceWhere, groupBy, summaries, having) : { ...EMPTY_QUERY, where: sourceWhere } };
  }

  // Breakdown first: a scatter colors its points by the first key.
  const unitSplit = breakdown ? unit.breakdowns[state.split] : undefined;
  const keys = unitSplit ? [unitSplit, unit.key] : [unit.key], groupBy = unitSplit ? [unitSplit] : [];
  const summaries = [summary("analysis_value", measure), ...state.view === "scatter" ? [summary("analysis_y", measureY)] : []];
  const reduce: Record<AnalysisReduce, Pick<AggregationClause, "op" | "field" | "expression">> = {
    total: { op: "sum", field: "analysis_value" }, mean: { op: "avg", field: "analysis_value" }, max: { op: "max", field: "analysis_value" },
    count: { op: "count" }, median: { op: "count", field: "analysis_value", expression: "MEDIAN([analysis_value])" },
  };
  const clause: AggregationClause = state.view === "scatter"
    ? { id: "analysis", op: "max", field: "analysis_value", expression: "MAX([analysis_value])", expressionY: "MAX([analysis_y])", groupBy: keys, groupLimit: maxPoints, sort: [{ key: "value", dir: "desc" }], label: unit.plural,
      display: { kind: "scatter", maxGroups: maxPoints, xLabel: measure.label, yLabel: measureY.label, xScale: log(state.scale), yScale: log(state.scaleY), xFormat: measure.format, yFormat: measureY.format, legendPosition: unitSplit ? "right" : "none" } }
    : state.view === "distribution"
      ? { id: "analysis", op: "count", field: "analysis_value", ...unitSplit ? { expression: "MEDIAN([analysis_value])" } : {}, groupBy, groupLimit: maxGroups, sort: [{ key: "samples", dir: "desc" }], label: `${measure.label} per ${unit.label.toLowerCase()}`, ...minCount,
        distribution: unitSplit ? { kind: "box", input: "[analysis_value]", whiskers: "tukey" } : { kind: "histogram", input: "[analysis_value]" }, display: distributionDisplay(Boolean(unitSplit)) }
      : { id: "analysis", ...reduce[state.reduce], groupBy, groupLimit: maxGroups, sort: [{ key: "value", dir: "desc" }], label: valueLabel, ...minCount, display: compareDisplay(Boolean(unitSplit)) };
  const query: QueryState = { ...EMPTY_QUERY, grouping: { source: { ...EMPTY_QUERY, where: sourceWhere }, keys, summaries, representative: null },
    select: [...keys, ...summaries.map(column => column.id)].map(field => ({ field })), orderBy: [{ field: "analysis_value", dir: "desc", nulls: "last" }, ...keys.map(field => ({ field, dir: "asc" as const }))], aggregations: [clause] };
  return { query, clause, grouped: true, supporting: query };
}

function groupedRecords(where: WhereTerm[], keys: string[], summaries: SummaryColumn[], having: WhereTerm[], order = summaries[0].id): QueryState {
  return { ...EMPTY_QUERY, grouping: { source: { ...EMPTY_QUERY, where }, keys, summaries, representative: null },
    select: [...keys, ...summaries.map(column => column.id)].map(field => ({ field })), where: having,
    orderBy: [{ field: order, dir: order === keys[0] ? "asc" : "desc", nulls: "last" }, ...keys.filter(field => field !== order).map(field => ({ field, dir: "asc" as const }))] };
}

// The records behind one chart bucket. Records use the native bucket filter;
// units open the grouped query restricted to that group's rows. A cycle slot
// spans many separate days or hours and has no single filter.
export function analysisInspectQuery(analysis: AnalysisQuery, bucket: AggregationBucket, clause: AggregationClause, schema: FieldSchema, view: AnalysisView): QueryState | null {
  if (view === "cycle") return null;
  if (!analysis.grouped) return inspectBucketQuery({ ...EMPTY_QUERY, where: analysis.query.where }, bucket, clause, schema);
  const { aggregations: _, ...units } = analysis.query;
  return inspectBucketQuery(units, bucket, clause, schema);
}

// Fold a cycle view's native hourly or daily totals into 24 local hours or 7
// weekdays. Every complete slot in [from, to) is an observation, idle ones
// included as zero for additive measures; an idle ratio has no value.
export function cycleResult(catalog: AnalysisCatalog, input: AnalysisState, result: AggregationResultEntry, bounds: { from?: number; to: number }, zone: string) {
  const state = normalizeAnalysis(catalog, input), measure = catalog.measures[state.metric];
  if (result.error || result.coverage === "partial" || result.hiddenGroups || (result.groupCount ?? 0) > result.buckets.length || result.buckets.some(bucket => bucket.error)) {
    throw new Error(result.error ?? "Complete totals are required for a daily cycle. Choose a shorter range.");
  }
  const time = (key: unknown) => state.cycle === "hour" ? Date.parse(String(key)) : metricDateTime(String(key), zone);
  const values = new Map(result.buckets.map(bucket => [time(bucket.keys[0]), bucket]));
  const from = bounds.from ?? Math.min(...values.keys());
  const zero = !measure.expression && ["sum", "count"].includes(measure.op) ? 0 : null;
  let observations = 0;
  const samples = Number.isFinite(from) ? cycleSamples(state.cycle, from, bounds.to, zone, start => {
    const bucket = values.get(start);
    observations += bucket?.count ?? 0;
    return typeof bucket?.value === "number" ? bucket.value : bucket ? null : zero;
  }) : (state.cycle === "hour" ? hoursOfDay : weekdays).map(() => [] as number[]);
  const slot = state.cycle === "hour" ? "Local hour" : "Weekday", per = state.cycle === "hour" ? "hour" : "day";
  const field = state.cycle === "hour" ? "hour_of_day" : "weekday";
  const boxes = cycleBoxes(state.cycle, samples, result.id);
  if (state.reduce === "total" || state.reduce === "mean") {
    const total = state.reduce === "total";
    const buckets = samples.map((values, index) => ({ keys: [boxes.result.buckets[index].keys[0]], count: values.length, value: values.length ? values.reduce((sum, value) => sum + value, 0) / (total ? 1 : values.length) : null }));
    const clause: AggregationClause = { id: result.id, op: "sum", groupBy: [field], label: `${total ? "Total" : "Mean"} ${measure.label.toLowerCase()}${total ? "" : ` per ${per}`}`, display: { kind: "bar-vertical", maxGroups: samples.length, xLabel: slot, yLabel: measure.label, yScale: log(state.scale), format: measure.format, legendPosition: "none" } };
    return { clause, result: { ...boxes.result, buckets }, slots: boxes.days, observations };
  }
  const clause: AggregationClause = { id: result.id, op: "count", groupBy: [field], label: `${measure.label} per ${per}`, distribution: { kind: "box", input: "[slot_value]", whiskers: "tukey" }, display: { kind: "box", maxGroups: samples.length, xLabel: `${measure.label} / ${per}`, xScale: log(state.scale), format: measure.format } };
  return { clause, result: boxes.result, slots: boxes.days, observations };
}

// What one observation is, in the dashboard notes' plain style.
export function analysisNote(catalog: AnalysisCatalog, input: AnalysisState): string {
  const state = normalizeAnalysis(catalog, input), measure = catalog.measures[state.metric];
  const unit = state.unit === records ? undefined : catalog.units[state.unit];
  const subject = unit ? unit.plural.toLowerCase() : catalog.records.plural.toLowerCase();
  const parts = [measure.note];
  if (state.view === "cycle") parts.push(`One observation is one complete ${state.cycle === "hour" ? "clock hour, placed at its local hour" : "local calendar day"} in the range, idle ones included${measure.additive ? " as zero" : "; idle periods have no ratio"}. Partial first and last ${state.cycle === "hour" ? "hours" : "days"} are left out. Select the grouped query for the ${state.cycle === "hour" ? "hourly" : "daily"} totals; a weekday or hour bar spans many dates, so it has no single drill-down.`);
  else if (unit) parts.push(`${unit.note} One observation is one ${unit.label.toLowerCase()}: its ${measure.label.toLowerCase()} over all its matching records in the range.`);
  else parts.push(`One observation is one ${catalog.records.label.toLowerCase()}. ${catalog.records.note}`);
  if (state.view === "compare" && unit) parts.push(state.reduce === "count" ? `Bars count ${subject} in each group.` : state.reduce === "total" ? `Bars add up the ${subject} in each group.` : `Bars show the ${state.reduce === "max" ? "largest" : state.reduce} ${unit.label.toLowerCase()} in each group.`);
  if (state.view === "scatter") parts.push(`Each point is one ${unit!.label.toLowerCase()}; both measures use the records matching both measures' populations. Up to ${maxPoints.toLocaleString("en-US")} ${subject} with the largest ${measure.label.toLowerCase()} are drawn; the grouped query lists them all.`);
  if (state.minSamples > 1 && state.split !== noSplit && state.view !== "scatter" && state.view !== "cycle") parts.push(`Groups with fewer than ${state.minSamples.toLocaleString("en-US")} ${subject} are hidden.`);
  if (state.compareAs === "share" && state.view === "compare") parts.push("Shares divide each group by the total of all groups, including those folded into Other.");
  return parts.join(" ");
}

export function analysisEmptyLabel(catalog: AnalysisCatalog, input: AnalysisState): string {
  const state = normalizeAnalysis(catalog, input);
  const subject = state.unit === records ? catalog.records.plural.toLowerCase() : catalog.units[state.unit].plural.toLowerCase();
  return state.minSamples > 1 && state.split !== noSplit && state.view !== "scatter" ? `No groups with at least ${state.minSamples.toLocaleString("en-US")} ${subject}` : `No matching ${subject}`;
}
