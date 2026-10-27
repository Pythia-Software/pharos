import { EMPTY_QUERY, coerceValue, inspectBucketQuery, metricDateFloor, metricDateNext, metricDateTime, type AggOp, type AggregationBucket, type AggregationClause, type AggregationResultEntry, type FieldSchema, type MetricDisplay, type MetricValueFormat, type QueryState, type SummaryColumn, type WhereTerm } from "@pythia-software/query-table-core";
import type { ChartScale } from "./chart-scale";
import { cycleBoxes, cycleGroups, cycleSamples, hoursOfDay, idleIsZero, weekdays, type CalendarCycle } from "./weekday-cover";

// A dashboard analysis is a view over one catalog: which records count as
// one observation (the unit), which measure they carry, and how groups of
// observations are compared. A catalog lists only combinations a dashboard
// can state truthfully; the state is one flat object so it round-trips through
// `analyze_chart` beside a dashboard's existing trend fields.

export type AnalysisView = "trend" | "compare" | "distribution" | "scatter" | "cycle";
export type AnalysisReduce = "total" | "mean" | "median" | "max" | "count";
export type AnalysisMeasure = {
  label: string; unit: string; format: MetricValueFormat;
  // The aggregate over any set of records: one group, or one unit. A ratio
  // of two sums names its parts, so units can pool them rather than add
  // their ratios.
  op: AggOp; field?: string; expression?: string; ratio?: [numerator: string, denominator: string];
  // A per-record input makes the measure eligible for record distributions.
  value?: string;
  // Additive measures partition a total, so groups may be shown as shares.
  additive: boolean;
  // A population the measure implies, such as fully priced usage.
  where?: WhereTerm[];
  note: string;
};
// A breakdown groups by one or more fields, such as provider and model.
export type AnalysisBreakdown = { label: string; fields: string[]; where?: WhereTerm[] };
// Breakdowns map to the fields that hold them at unit level. A breakdown is
// listed only when it is constant within one unit. One with an implied where
// is never offered for units: it would remove records from each unit and
// change the unit's own value, not just choose which units appear.
export type AnalysisUnit = { label: string; plural: string; key: string; where: WhereTerm[]; breakdowns: Record<string, string[]>; note: string };
export type AnalysisCatalog = {
  dataset: string;
  time: { first: string; last: string };
  measures: Record<string, AnalysisMeasure>;
  breakdowns: Record<string, AnalysisBreakdown>;
  units: Record<string, AnalysisUnit>;
  records: { label: string; plural: string; note: string };
  // Calendar dimensions the cycle view folds: hour keys are instants, day
  // keys calendar dates recorded by the source. Without them, no cycle view.
  cycle?: Record<CalendarCycle, string>;
  // How the dashboard's own trend names its metrics and splits.
  trend: { metrics: Record<string, { measure: string; share: boolean }>; splits: string[]; metric: string; split: string };
  scatter: [string, string];
};

export type AnalysisState = {
  view?: AnalysisView; unit?: string; metric: string; metricY?: string; split: string;
  reduce?: AnalysisReduce; compareAs?: "bars" | "share"; minSamples?: number;
  scale?: ChartScale; scaleY?: ChartScale; cycle?: CalendarCycle;
};
export type ResolvedAnalysis = AnalysisState & Required<Pick<AnalysisState, "view" | "unit" | "metricY" | "reduce" | "compareAs" | "minSamples" | "scaleY" | "cycle">>;

// Reserved ids: no catalog unit may be named records, nor a breakdown none.
export const records = "records", noSplit = "none";
export const analysisViews: Array<[AnalysisView, string]> = [["trend", "Over time"], ["compare", "Compare groups"], ["distribution", "Distribution"], ["scatter", "Two measures"], ["cycle", "Daily cycle"]];
export const analysisReductions: Array<[AnalysisReduce, string]> = [["total", "Total"], ["mean", "Mean per unit"], ["median", "Median per unit"], ["max", "Largest unit"], ["count", "Number of units"]];
export const sampleThresholds = [0, 2, 3, 5, 10, 20, 50, 100];
const analysisDefaults = { unit: records, reduce: "total", compareAs: "bars", minSamples: 0, scaleY: "linear", cycle: "weekday" } as const;
// Groups drawn are fixed per display. The trend's seriesLimit is its own.
const maxGroups = 1000, maxPoints = 2000, barsShown = 12, slicesShown = 8;

const oneOf = <T,>(value: unknown, options: readonly T[]): value is T => options.includes(value as T);
const has = (record: object, key: unknown) => typeof key === "string" && Object.hasOwn(record, key);

export const availableViews = (catalog: AnalysisCatalog) => analysisViews.filter(([view]) => view !== "cycle" || catalog.cycle);
// Why the dashboard's own trend cannot draw this state yet, if it cannot.
// The trend is not catalog-driven (phase 2): it draws only the measures its
// metrics map to, over the dashboard's filters alone.
export function trendUnavailable(catalog: AnalysisCatalog, state: AnalysisState): string | undefined {
  if ((state.view ?? "trend") === "trend") return;
  const measure = catalog.measures[state.metric];
  const drawn = [...new Set(Object.values(catalog.trend.metrics).map(item => catalog.measures[item.measure].label))].join(" and ");
  if (!Object.values(catalog.trend.metrics).some(item => item.measure === state.metric)) {
    return measure?.where ? `Over time cannot yet keep the ${measure.label.toLowerCase()} population; it draws ${drawn} over your filters.` : `Over time draws ${drawn} only.`;
  }
  // The trend adds up records. A count, mean, median or largest of units is
  // another quantity, which the trend would silently replace.
  const resolved = normalizeAnalysis(catalog, state);
  if (resolved.view === "compare" && resolved.unit !== records && resolved.reduce !== "total") {
    return `Over time draws record totals of ${drawn}, not the ${reductionLabel(resolved.reduce, measure, catalog.units[resolved.unit]).toLowerCase()}.`;
  }
}
// Each view's option, disabled with its reason where this state cannot move.
export const viewOptions = (catalog: AnalysisCatalog, state: AnalysisState): Array<[AnalysisView, string, string?]> =>
  availableViews(catalog).map(([view, label]) => [view, label, view === "trend" ? trendUnavailable(catalog, state) : undefined]);
export function catalogProblems(catalog: AnalysisCatalog): string[] {
  const problems: string[] = [];
  if (has(catalog.units, records)) problems.push(`A unit may not be named ${records}.`);
  if (has(catalog.breakdowns, noSplit)) problems.push(`A breakdown may not be named ${noSplit}.`);
  for (const [id, unit] of Object.entries(catalog.units)) for (const split of Object.keys(unit.breakdowns)) {
    if (!has(catalog.breakdowns, split)) problems.push(`${id} maps unknown breakdown ${split}.`);
    else if (catalog.breakdowns[split].where?.length) problems.push(`${id} offers ${split}, whose implied filter would change each unit's value.`);
    else if (unit.breakdowns[split].length !== catalog.breakdowns[split].fields.length) problems.push(`${id} maps ${split} to a different number of fields.`);
  }
  for (const [id, measure] of Object.entries(catalog.measures)) {
    if (measure.ratio && measure.expression) problems.push(`${id} has both a ratio and an expression.`);
  }
  return problems;
}

// Field-level validity for a link or saved state. Combinations are coerced by
// normalizeAnalysis rather than rejected, so an older link still opens.
export function validAnalysisState(catalog: AnalysisCatalog, state: AnalysisState): boolean {
  if (state.view === undefined || state.view === "trend") return true;
  return oneOf(state.view, availableViews(catalog).map(([view]) => view))
    && (state.unit === undefined || state.unit === records || has(catalog.units, state.unit))
    && has(catalog.measures, state.metric) && (state.metricY === undefined || has(catalog.measures, state.metricY))
    && (state.split === noSplit || has(catalog.breakdowns, state.split))
    && (state.reduce === undefined || oneOf(state.reduce, analysisReductions.map(([reduce]) => reduce)))
    && (state.compareAs === undefined || oneOf(state.compareAs, ["bars", "share"]))
    && (state.minSamples === undefined || Number.isInteger(state.minSamples) && state.minSamples >= 0 && state.minSamples <= 1_000_000)
    && (state.scaleY === undefined || oneOf(state.scaleY, ["linear", "logarithmic", "half"]))
    && (state.cycle === undefined || oneOf(state.cycle, ["weekday", "hour"]));
}

export const shareable = (catalog: AnalysisCatalog, state: ResolvedAnalysis) => state.view === "compare" && state.split !== noSplit
  && (state.unit !== records && state.reduce === "count" || catalog.measures[state.metric].additive && state.reduce === "total");
export const splitValid = (catalog: AnalysisCatalog, unit: string, split: string) => split === noSplit || has(catalog.breakdowns, split) && (unit === records || has(catalog.units[unit].breakdowns, split));
// Units add up an additive measure, and pool a ratio's parts. Distinct counts
// and other non-additive values have no honest unit total.
export const unitTotal = (measure: AnalysisMeasure) => measure.additive || Boolean(measure.ratio);
// Slot values add up across days only for additive measures: a conversation
// active on two Mondays would otherwise count twice.
export const cycleTotal = (measure: AnalysisMeasure) => measure.additive;
// A threshold hides groups; a share display divides by all groups.
export const thresholded = (state: ResolvedAnalysis) => state.minSamples > 1 && state.split !== noSplit && (state.view === "distribution" || state.view === "compare" && state.compareAs !== "share");
export function reductionLabel(reduce: AnalysisReduce, measure: AnalysisMeasure, unit: AnalysisUnit) {
  if (reduce === "total" && measure.ratio) return `Pooled across ${unit.plural.toLowerCase()}`;
  if (reduce === "mean" && !measure.additive) return `Unweighted mean per ${unit.label.toLowerCase()}`;
  return analysisReductions.find(([id]) => id === reduce)![1].replace("units", unit.plural.toLowerCase()).replace("unit", unit.label.toLowerCase());
}

// Fill defaults and move every setting the view cannot use to the nearest
// one it can: a cycle profiles records; a scatter pairs two measures of an
// entity; a record distribution needs a per-record value.
export function normalizeAnalysis<T extends AnalysisState>(catalog: AnalysisCatalog, state: T): T & ResolvedAnalysis {
  const next = { ...analysisDefaults, ...state } as T & ResolvedAnalysis;
  next.view ??= "trend";
  if (next.view === "trend") return next;
  const firstUnit = Object.keys(catalog.units)[0];
  if (next.view === "cycle" && !catalog.cycle) next.view = "compare";
  if (!has(catalog.measures, next.metric)) next.metric = catalog.scatter[0];
  if (next.unit !== records && !has(catalog.units, next.unit)) next.unit = records;
  if (next.view === "cycle") { next.unit = records; next.split = noSplit; }
  if (next.view === "scatter") {
    if (next.unit === records) next.unit = firstUnit;
    if (!has(catalog.measures, next.metricY) || next.metricY === next.metric) next.metricY = catalog.scatter.find(id => id !== next.metric) ?? Object.keys(catalog.measures).find(id => id !== next.metric)!;
  } else if (!has(catalog.measures, next.metricY)) next.metricY = catalog.scatter[1];
  if (next.view === "distribution" && next.unit === records && !catalog.measures[next.metric].value) next.unit = firstUnit;
  if (!splitValid(catalog, next.unit, next.split)) next.split = noSplit;
  // A cycle summarizes slot values: a box per slot by default, or their mean,
  // or their total where slot values add up (not ratios or distinct counts).
  if (next.view === "cycle") { if (state.reduce === undefined || !["total", "mean", "median"].includes(next.reduce) || next.reduce === "total" && !cycleTotal(catalog.measures[next.metric])) next.reduce = "median"; }
  else if (next.unit === records) next.reduce = "total";
  else if (next.reduce === "total" && !unitTotal(catalog.measures[next.metric])) next.reduce = "mean";
  if (next.compareAs === "share" && !shareable(catalog, next)) next.compareAs = "bars";
  return next;
}

// Move between views, carrying what both share. The trend's share metrics
// become a share display, and back. A state the trend cannot draw stays
// where it is (trendUnavailable says why); nothing is substituted.
export function switchView<T extends AnalysisState>(catalog: AnalysisCatalog, state: T, view: AnalysisView): T & ResolvedAnalysis {
  const current = state.view ?? "trend";
  if (view === "trend") {
    if (current === "trend" || trendUnavailable(catalog, state)) return normalizeAnalysis(catalog, state);
    const share = state.compareAs === "share" && current === "compare";
    const trend = Object.entries(catalog.trend.metrics);
    const metric = trend.find(([, item]) => item.measure === state.metric && item.share === share)?.[0] ?? trend.find(([, item]) => item.measure === state.metric && !item.share)![0];
    return normalizeAnalysis(catalog, { ...state, view, metric, split: catalog.trend.splits.includes(state.split) ? state.split : catalog.trend.split });
  }
  if (current === "trend") {
    const mapped = catalog.trend.metrics[state.metric];
    return normalizeAnalysis(catalog, { ...state, view, metric: mapped?.measure ?? state.metric, ...mapped ? { compareAs: mapped.share ? "share" as const : "bars" as const } : {}, ...view === "cycle" ? { reduce: "median" as const } : {},
      split: has(catalog.breakdowns, state.split) ? state.split : noSplit });
  }
  return normalizeAnalysis(catalog, { ...state, view, ...view === "cycle" ? { reduce: "median" as const } : {} });
}

const unique = (terms: WhereTerm[]) => terms.filter((term, index) => terms.findIndex(other => JSON.stringify(other) === JSON.stringify(term)) === index);
const log = (scale?: ChartScale) => ({ mode: scale === "logarithmic" ? "log" as const : "linear" as const });
export const measureExpression = (measure: AnalysisMeasure) => measure.ratio ? `SUM([${measure.ratio[0]}])/NULLIF(SUM([${measure.ratio[1]}]),0)` : measure.expression;
function summary(id: string, measure: AnalysisMeasure): SummaryColumn {
  const expression = measureExpression(measure);
  return expression ? { id, op: "formula", expression, label: measure.label }
    : { id, op: measure.op, ...(measure.field ? { field: measure.field } : {}), label: measure.label };
}
function aggregate(measure: AnalysisMeasure): Pick<AggregationClause, "op" | "field" | "expression"> {
  const expression = measureExpression(measure);
  return { op: measure.op, ...(measure.field && !expression ? { field: measure.field } : {}), ...(expression ? { expression } : {}) };
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
// A weekday cycle counts complete days only. Its range starts at the next
// local midnight, so the first date's records are whole days, not the part
// of a day after a preset's start time.
export function completeDays(where: WhereTerm[], time: AnalysisCatalog["time"], zone: string, now: number): WhereTerm[] {
  return where.map(term => {
    if (!("field" in term) || term.negated || ![">=", ">"].includes(term.op) || ![time.first, time.last].includes(term.field)) return term;
    const at = Number(coerceValue("datetime", term.value, now));
    if (!Number.isFinite(at)) return term;
    const day = metricDateFloor(at, "day", zone), start = day < at || day === at && term.op === ">" ? metricDateNext(day, "day", zone) : day;
    return { ...term, op: ">=" as const, value: new Date(start).toISOString() };
  });
}

export function analysisQuery(catalog: AnalysisCatalog, input: AnalysisState, inputWhere: WhereTerm[], { zone = "UTC", now = Date.now() }: { zone?: string; now?: number } = {}): AnalysisQuery {
  const state = normalizeAnalysis(catalog, input);
  if (state.view === "trend") throw new Error("The trend view has its own chart.");
  const where = state.view === "cycle" && state.cycle === "weekday" ? completeDays(inputWhere, catalog.time, zone, now) : inputWhere;
  const measure = catalog.measures[state.metric], measureY = catalog.measures[state.metricY];
  const breakdown = state.split === noSplit ? undefined : catalog.breakdowns[state.split];
  const unit = state.unit === records ? undefined : catalog.units[state.unit];
  const sourceWhere = unique([...where, ...unit?.where ?? [], ...breakdown?.where ?? [], ...measure.where ?? [], ...state.view === "scatter" ? measureY.where ?? [] : []]);
  // A threshold counts what is plotted: a distribution drops missing values
  // before grouping (records) or as a group filter (units).
  const minCount = thresholded(state) ? { minCount: state.minSamples } : {};
  const valueLabel = state.view === "compare" && unit && state.reduce !== "total" ? `${reductionLabel(state.reduce, measure, unit)}${state.reduce === "count" ? "" : ` · ${measure.label}`}` : measure.label;
  const format = state.view === "compare" && state.reduce === "count" ? { kind: "number" as const, decimals: 0 } : measure.format;
  const compareDisplay = (groups: boolean): MetricDisplay => !groups ? { kind: "value", format, valueLabel }
    : state.compareAs === "share" ? { kind: "pie", maxGroups: slicesShown, showOther: true, legendPosition: "right", format }
    : { kind: "bar-horizontal", maxGroups: barsShown, legendPosition: "none", xLabel: valueLabel, xScale: log(state.scale), format };
  const distributionDisplay = (box: boolean): MetricDisplay => ({ kind: box ? "box" : "histogram", maxGroups: barsShown, xLabel: measure.label, xScale: log(state.scale), ...(box ? { format } : { xFormat: format }) });

  if (state.view === "cycle") {
    const field = catalog.cycle![state.cycle];
    const clause: AggregationClause = { id: "analysis", ...aggregate(measure), groupBy: [field], groupLimit: cycleGroups, sort: [{ key: "group0", dir: "asc" }], label: measure.label };
    const query = { ...EMPTY_QUERY, where: sourceWhere, aggregations: [clause] };
    // Grouped usage queries read daily records, which carry no hour (see
    // docs/handoffs/usage-v3-hour-grouping.md), so hours open as records.
    return { query, clause, grouped: false, supporting: state.cycle === "hour" ? { ...EMPTY_QUERY, where: sourceWhere } : groupedRecords(sourceWhere, [field], [summary("analysis_value", measure)], [], field) };
  }

  if (!unit) {
    const groupBy = breakdown?.fields ?? [];
    if (state.view === "distribution") sourceWhere.push({ field: measure.value!, op: "is_not_null", value: "" });
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
  const groupBy = breakdown ? unit.breakdowns[state.split] : [], unitSplit = groupBy.length > 0;
  const keys = [...groupBy, unit.key];
  const pooled = state.view === "compare" && state.reduce === "total" && measure.ratio;
  const summaries = [summary("analysis_value", measure), ...state.view === "scatter" ? [summary("analysis_y", measureY)] : [],
    ...pooled ? [{ id: "analysis_numerator", op: "sum" as const, field: measure.ratio![0] }, { id: "analysis_denominator", op: "sum" as const, field: measure.ratio![1] }] : []];
  const reduce: Record<AnalysisReduce, Pick<AggregationClause, "op" | "field" | "expression">> = {
    total: pooled ? { op: "sum", expression: "SUM([analysis_numerator])/NULLIF(SUM([analysis_denominator]),0)" } : { op: "sum", field: "analysis_value" }, mean: { op: "avg", field: "analysis_value" }, max: { op: "max", field: "analysis_value" },
    count: { op: "count" }, median: { op: "count", field: "analysis_value", expression: "MEDIAN([analysis_value])" },
  };
  const clause: AggregationClause = state.view === "scatter"
    ? { id: "analysis", op: "max", field: "analysis_value", expression: "MAX([analysis_value])", expressionY: "MAX([analysis_y])", groupBy: keys, groupLimit: maxPoints, sort: [{ key: "value", dir: "desc" }], label: unit.plural,
      display: { kind: "scatter", maxGroups: maxPoints, xLabel: measure.label, yLabel: measureY.label, xScale: log(state.scale), yScale: log(state.scaleY), xFormat: measure.format, yFormat: measureY.format, legendPosition: unitSplit ? "right" : "none" } }
    : state.view === "distribution"
      ? { id: "analysis", op: "count", field: "analysis_value", ...unitSplit ? { expression: "MEDIAN([analysis_value])" } : {}, groupBy, groupLimit: maxGroups, sort: [{ key: "samples", dir: "desc" }], label: `${measure.label} per ${unit.label.toLowerCase()}`, ...minCount,
        distribution: unitSplit ? { kind: "box", input: "[analysis_value]", whiskers: "tukey" } : { kind: "histogram", input: "[analysis_value]" }, display: distributionDisplay(Boolean(unitSplit)) }
      : { id: "analysis", ...reduce[state.reduce], groupBy, groupLimit: maxGroups, sort: [{ key: "value", dir: "desc" }], label: valueLabel, ...minCount, display: compareDisplay(Boolean(unitSplit)) };
  // Only units with a value are plotted, so only they meet a minimum. A
  // count of units counts every unit.
  const having: WhereTerm[] = state.view === "distribution" || state.view === "compare" && state.reduce !== "count" ? [{ field: "analysis_value", op: "is_not_null", value: "" }] : [];
  const query: QueryState = { ...EMPTY_QUERY, grouping: { source: { ...EMPTY_QUERY, where: sourceWhere }, keys, summaries, representative: null },
    select: [...keys, ...summaries.map(column => column.id)].map(field => ({ field })), where: having, orderBy: [{ field: "analysis_value", dir: "desc", nulls: "last" }, ...keys.map(field => ({ field, dir: "asc" as const }))], aggregations: [clause] };
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
//
// Hour keys are instants, placed at the viewer's local hour. Day keys are
// calendar dates the recording Mac assigned in its own zone, which the
// dashboard does not know; a date's weekday does not depend on the viewer, so
// days fold as dates. Only complete dates in the viewer's range count, as in
// weekdayMetric: the query starts at a local midnight (completeDays), and
// dates from the viewer's current day on are incomplete. A recording Mac in
// another zone can still return a date before the first one; it is left out
// and reported, never counted as a complete day.
export function cycleResult(catalog: AnalysisCatalog, input: AnalysisState, result: AggregationResultEntry, bounds: { from?: number; to: number }, zone: string) {
  const state = normalizeAnalysis(catalog, input), measure = catalog.measures[state.metric];
  if (result.error || result.coverage === "partial" || result.hiddenGroups || (result.groupCount ?? 0) > result.buckets.length || result.buckets.some(bucket => bucket.error)) {
    throw new Error(result.error ?? "Complete totals are required for a daily cycle. Choose a shorter range.");
  }
  const hour = state.cycle === "hour", foldZone = hour ? zone : "UTC";
  const time = (key: unknown) => key === null || key === undefined || key === "" ? NaN : hour ? Date.parse(String(key)) : metricDateTime(String(key), "UTC");
  const values = new Map(result.buckets.map(bucket => [time(bucket.keys[0]), bucket]));
  // A missing key would silently become an idle slot.
  if ([...values.keys()].some(key => !Number.isFinite(key))) throw new Error(`Some totals have no ${hour ? "hour" : "day"}, so they cannot be placed in the cycle.`);
  const date = (instant: number) => { const day = metricDateFloor(instant, "day", zone); return metricDateTime(new Intl.DateTimeFormat("en-CA", { timeZone: zone }).format(day), "UTC") + (instant > day ? 86_400_000 : 0); };
  const from = bounds.from === undefined ? Math.min(...values.keys()) : hour ? bounds.from : date(bounds.from);
  const earlier = hour ? [] : result.buckets.filter(bucket => time(bucket.keys[0]) < from);
  const to = hour ? bounds.to : metricDateTime(new Intl.DateTimeFormat("en-CA", { timeZone: zone }).format(bounds.to), "UTC");
  const zero = idleIsZero(aggregate(measure)) ? 0 : null;
  let observations = 0;
  const samples = Number.isFinite(from) ? cycleSamples(state.cycle, from, to, foldZone, start => {
    const bucket = values.get(start);
    observations += bucket?.count ?? 0;
    return typeof bucket?.value === "number" ? bucket.value : zero;
  }) : (hour ? hoursOfDay : weekdays).map(() => [] as number[]);
  const slot = state.cycle === "hour" ? "Local hour" : "Weekday", per = state.cycle === "hour" ? "hour" : "day";
  const field = state.cycle === "hour" ? "hour_of_day" : "weekday";
  const boxes = cycleBoxes(state.cycle, samples, result.id);
  if (state.reduce === "total" || state.reduce === "mean") {
    const total = state.reduce === "total";
    const buckets = samples.map((values, index) => ({ keys: [boxes.result.buckets[index].keys[0]], count: values.length, value: values.length ? values.reduce((sum, value) => sum + value, 0) / (total ? 1 : values.length) : null }));
    const clause: AggregationClause = { id: result.id, op: "sum", groupBy: [field], label: total ? `Total ${measure.label.toLowerCase()}` : `${measure.additive ? "Mean" : "Unweighted mean"} ${measure.label.toLowerCase()} per ${per}`, display: { kind: "bar-vertical", maxGroups: samples.length, xLabel: slot, yLabel: measure.label, yScale: log(state.scale), format: measure.format, legendPosition: "none" } };
    return { clause, result: { ...boxes.result, buckets }, slots: boxes.days, observations, earlier: earlier.length };
  }
  const clause: AggregationClause = { id: result.id, op: "count", groupBy: [field], label: `${measure.label} per ${per}`, distribution: { kind: "box", input: "[slot_value]", whiskers: "tukey" }, display: { kind: "box", maxGroups: samples.length, xLabel: `${measure.label} / ${per}`, xScale: log(state.scale), format: measure.format } };
  return { clause, result: boxes.result, slots: boxes.days, observations, earlier: earlier.length };
}

// What one observation is, in the dashboard notes' plain style.
export function analysisNote(catalog: AnalysisCatalog, input: AnalysisState): string {
  const state = normalizeAnalysis(catalog, input), measure = catalog.measures[state.metric];
  const unit = state.unit === records ? undefined : catalog.units[state.unit];
  const subject = unit ? unit.plural.toLowerCase() : catalog.records.plural.toLowerCase();
  const parts = [measure.note];
  if (state.view === "cycle") parts.push(state.cycle === "hour"
    ? `One observation is one complete clock hour in the range, placed at its hour in your time zone, idle hours included${idleIsZero(aggregate(measure)) ? " as zero" : ", which have no value"}. Partial first and last hours are left out. An hour bar spans many dates, so it has no single drill-down; hourly totals cannot yet open as a grouped query, so the link opens the records.`
    : `One observation is one complete recorded day in the range, idle days included${idleIsZero(aggregate(measure)) ? " as zero" : ", which have no value"}. Days are the dates the recording Mac assigned in its own time zone. The range starts at your next local midnight; the current day is left out. When the recording Mac's time zone differs from yours, a date just before your first date can be returned with part of its usage; it is left out and reported. Select the grouped query for the daily totals; a weekday bar spans many dates, so it has no single drill-down.`);
  else if (unit) parts.push(`${unit.note} One observation is one ${unit.label.toLowerCase()}: its ${measure.label.toLowerCase()} over all its matching records in the range.`);
  else parts.push(`One observation is one ${catalog.records.label.toLowerCase()}. ${catalog.records.note}`);
  if (state.view === "compare" && unit) parts.push(state.reduce === "count" ? `Bars count ${subject} in each group.`
    : state.reduce === "total" ? measure.ratio ? `Bars pool the ${subject} in each group: the ratio of their summed parts, as if they were one ${unit.label.toLowerCase()}.` : `Bars add up the ${subject} in each group.`
    : `Bars show the ${state.reduce === "max" ? "largest" : state.reduce} ${unit.label.toLowerCase()} in each group${state.reduce === "mean" && !measure.additive ? `, every ${unit.label.toLowerCase()} weighted equally` : ""}.`);
  if (state.view === "cycle" && state.reduce === "mean" && !measure.additive) parts.push(`The mean weights every ${state.cycle === "hour" ? "hour" : "day"} equally${measure.ratio ? ", not by tokens" : ""}.`);
  if (state.view === "cycle" && state.reduce === "total") parts.push(`Totals add up every ${state.cycle === "hour" ? "hour" : "day"} that falls on each bar.`);
  if (state.view === "scatter") parts.push(`Each point is one ${unit!.label.toLowerCase()}; both measures use the records matching both measures' populations. Up to ${maxPoints.toLocaleString("en-US")} ${subject} with the largest ${measure.label.toLowerCase()} are drawn; the grouped query lists them all.`);
  if (state.view === "distribution" || state.view === "compare" && unit && state.reduce !== "count") parts.push(`${unit ? subject[0].toUpperCase() + subject.slice(1) : catalog.records.plural} without a ${measure.label.toLowerCase()} value are left out.`);
  if (thresholded(state)) parts.push(`Groups with fewer than ${state.minSamples.toLocaleString("en-US")} ${state.view === "distribution" ? "plotted " : ""}${subject} are hidden.`);
  if (state.compareAs === "share" && state.view === "compare") parts.push("Shares divide each group by the total of all groups, including those folded into Other.");
  return parts.join(" ");
}

export function analysisEmptyLabel(catalog: AnalysisCatalog, input: AnalysisState): string {
  const state = normalizeAnalysis(catalog, input);
  const subject = state.unit === records ? catalog.records.plural.toLowerCase() : catalog.units[state.unit].plural.toLowerCase();
  return thresholded(state) ? `No groups with at least ${state.minSamples.toLocaleString("en-US")} ${subject}` : `No matching ${subject}`;
}
