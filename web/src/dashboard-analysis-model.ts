import { EMPTY_QUERY, MAX_WHERE_CLAUSES, coerceValue, groupKeyFields, predicatesOf, type GroupKey, type MetricDateGrouping, type WhereClause, inspectBucketQuery, metricDateFloor, metricDateNext, type AggOp, type AggregationBucket, type AggregationClause, type AggregationResultEntry, type FieldSchema, type MetricDisplay, type MetricValueFormat, type QueryState, type SummaryColumn, type WhereTerm } from "@pythia-software/query-table-core";
import { defaultChartScale, validChartScale, type ChartScale } from "./chart-scale";
import { chartPeriods, validPeriodCount, type ChartPeriod } from "./chart-period";
import { validChartDisplay, validRollingPeriods, type ChartDisplay } from "./chart-series";
import { isSliceTerm, readSlice, validChartPresentation, withSlice, type SliceSelection } from "./chart-selection";
import { timeBounds, withEnd } from "./chart-range";
import { cycleBoxes, cycleFrame, cycleGroups, cycleRangeError, cycleSamples, hoursOfDay, idleIsZero, maxCycleSlots, weekdays, type CalendarCycle, type CycleKey } from "./weekday-cover";

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
  op: AggOp; field?: string; expression?: string; ratio?: [numerator: RatioPart, denominator: RatioPart];
  // A per-record input makes the measure eligible for record distributions.
  value?: string;
  // Additive measures partition a total, so groups may be shown as shares.
  additive: boolean;
  // A population the measure implies, such as fully priced usage.
  where?: WhereTerm[];
  note: string;
};
// A ratio's part is a summed field, or a count of a field's non-empty values,
// as in a mean duration per timed call: sum(duration) / count(duration). A
// part may instead sum a row expression, which states a condition: zero-result
// searches are SUM(IF([result_count]=0,1,0)). Pooling still sums each part per
// period, window or unit. Such a part names what it counts (label).
export type RatioPart = string | { op: "sum" | "count"; field: string; label?: string } | { op: "sum"; expression: string; label: string };
type Part = { op: "sum" | "count"; field?: string; expression?: string; label?: string };
export const ratioPart = (part: RatioPart): Part => typeof part === "string" ? { op: "sum", field: part } : part;
const partExpression = (part: RatioPart) => { const { op, field, expression } = ratioPart(part); return `${op.toUpperCase()}(${expression ?? `[${field}]`})`; };
// A part as an aggregation clause, or a grouped row's summary column.
export const partClause = (part: RatioPart): Pick<AggregationClause, "op" | "field" | "expression"> => { const { op, field, expression } = ratioPart(part); return expression ? { op: "sum", expression: partExpression(part) } : { op, field }; };
const partColumn = (part: RatioPart): Pick<SummaryColumn, "op" | "field" | "expression"> => { const { op, field, expression } = ratioPart(part); return expression ? { op: "formula", expression: partExpression(part) } : { op, field }; };
// What a part adds up, in words: "summed input tokens", "the count of duration ms".
export const partLabel = (part: RatioPart) => { const { op, field, label } = ratioPart(part); return label ?? `${op === "sum" ? "summed" : "the count of"} ${field!.replaceAll("_", " ")}`; };
// The field a drill-down shows for a part, where it has one.
export const partField = (part: RatioPart) => ratioPart(part).field;
// A breakdown groups by one or more fields, such as provider and model. The
// trend draws its groups as series: in a fixed order where one is given,
// with labels for known values and for a missing one.
// A breakdown with an implied where names that population in its note.
export type AnalysisBreakdown = { label: string; fields: string[]; where?: WhereTerm[]; note?: string; order?: string[]; values?: Record<string, string>; empty?: string };
// A component breakdown splits a measure into its additive parts, such as
// tokens into uncached input, cache reads and writes, and output. Its series
// are fields, not groups of records, so only the trend draws it, and only for
// the measures it lists; any other measure moves to its fallback breakdown.
export type AnalysisComponent = { key: string; label: string; field?: string; color: string; optional?: boolean; fields?: string[] };
// A derived component breakdown computes its parts rather than summing them:
// the trend queries the summed input fields per period, grouped further by
// groupBy (Carbon's model tier), and derive turns one period's totals into
// that period's part values for the measure shown. Its parameters (the
// user's carbon factors) are closed over when the catalog is built, so new
// parameters are a new catalog. derive runs before smoothing, accumulating or
// shares, so the order of operations is the same as for summed parts. A
// derived part names the fields its drill-down shows (fields).
export type DerivedInputs = { fields: string[]; groupBy: string[] };
export type DerivedTotals = Array<{ keys: unknown[]; values: Record<string, number> }>;
export type AnalysisComponents = { label: string; measures: string[]; parts: AnalysisComponent[]; fallback: string;
  derived?: { inputs: DerivedInputs; derive: (totals: DerivedTotals, metric: string) => Record<string, number> } };
// Calendar fields the trend groups by; several hours or days sum the hourly
// or daily totals.
export type TrendPeriodField = "hour" | "day" | "week" | "month";
// How a catalog's records reach the service, beside the dashboard's filters:
// params are query-string parameters every request carries, such as the
// user's carbon assumptions, which the service applies to the records it
// returns; note says so beneath every view.
export type AnalysisRequest = { params?: Record<string, string>; note?: string };
// Breakdowns map to the fields that hold them at unit level. A breakdown is
// listed only when it is constant within one unit. One with an implied where
// is never offered for units: it would remove records from each unit and
// change the unit's own value, not just choose which units appear.
// A unit's key may combine fields, such as a conversation and a skill path:
// one observation is then one pair, and a breakdown constant within the pair
// (the skill's name) may be one of its fields.
export type AnalysisUnit = { label: string; plural: string; key: string | string[]; where: WhereTerm[]; breakdowns: Record<string, string[]>; note: string };
export type AnalysisCatalog = {
  dataset: string;
  time: { first: string; last: string };
  measures: Record<string, AnalysisMeasure>;
  breakdowns: Record<string, AnalysisBreakdown>;
  units: Record<string, AnalysisUnit>;
  records: { label: string; plural: string; note: string };
  // The calendar keys each cycle folds, and how they name their slots
  // (CycleKey). Without any, no cycle view; without one, that cycle is
  // offered disabled.
  cycle?: Partial<Record<CalendarCycle, CycleKey>>;
  components?: Record<string, AnalysisComponents>;
  // A dataset without one of them (no hour field, say) leaves it out, and the
  // trend offers it disabled.
  periods: Partial<Record<TrendPeriodField, string>>;
  // The day-boundary rule. Days, weeks and months are the recorded calendar
  // fields (periods), as the cards group them, so the trend runs on /metrics
  // and its supporting query is grouped. Records with a true timestamp group
  // it for hours, in the viewer's zone (UTC clock hours placed at the local
  // hour, dateGrouping), as the hour and weekday cycles do. A day-grained
  // ledger (usage) keeps its recorded hour field too.
  timestamp?: string;
  request?: AnalysisRequest;
  // The trend's default measure and breakdown.
  trend: { metric: string; split: string };
  // Reads a link or saved state written in an older vocabulary, such as a
  // metric name that meant a measure shown as shares. It must return a state
  // in the catalog's terms and leave a current one unchanged.
  legacy?: (state: AnalysisState) => AnalysisState;
  // What a records minimum counts when one record is not one observation: a
  // summed field, such as the calls a daily tools summary holds, or a field's
  // distinct values, such as the conversations usage records belong to.
  // unit names them ("calls"); label heads the supporting query's column.
  // Without it, a minimum counts records.
  samples?: { unit: string; label: string } & ({ sum: string } | { distinct: string });
  // Labels for a field's values where the schema has none, such as Agent
  // Time's activities.
  labels?: Record<string, (value: string) => string>;
  // Whether a link written before analysisVersion stored the trend legend's
  // selection as a filter on the breakdown's field (readAnalysisLink). Only
  // dashboards whose old chart had such a legend set it.
  legacySlices?: boolean;
  // Translates the dashboard's time bounds into predicates the dataset can
  // execute, for every query and drill-down that leaves the dashboard (the
  // tools rollup's days are text). The bounds stay as they are for drawing.
  executable?: (where: WhereTerm[], now: number) => WhereTerm[];
  // What the executed range covers, when it differs from the range shown
  // (the tools rollup counts whole days).
  rangeNote?: string;
  scatter: [string, string];
};

export const rollingOps = ["mean", "sum"] as const;
export type RollingOp = typeof rollingOps[number];
// How Compare draws its groups, and the cycles a cycle view folds.
export const compareModes = ["bars", "share"] as const;
export const analysisCycles: readonly CalendarCycle[] = ["weekday", "hour"];
// The analyze_chart format. Version 2 keeps a legend selection in the state
// (selection) and leaves where to the population; an unversioned link may
// hold the selection as a filter (readAnalysisLink).
export const analysisVersion = 2;
export type AnalysisState = {
  v?: number;
  view?: AnalysisView; unit?: string; metric: string; metricY?: string; split: string;
  reduce?: AnalysisReduce; compareAs?: "bars" | "share"; minSamples?: number;
  scale?: ChartScale; scaleY?: ChartScale; cycle?: CalendarCycle;
  // The trend's own settings.
  period?: ChartPeriod; hours?: number; days?: number; rollingPeriods?: number; rollingOp?: RollingOp; display?: ChartDisplay;
  share?: boolean; completePeriods?: boolean; norm?: boolean; normPeriods?: number; seriesLimit?: number; selection?: SliceSelection<string>;
};
export type ResolvedAnalysis = AnalysisState & Required<Pick<AnalysisState, "view" | "unit" | "metricY" | "reduce" | "compareAs" | "minSamples" | "scaleY" | "cycle"
  | "period" | "hours" | "days" | "rollingPeriods" | "rollingOp" | "display" | "share" | "completePeriods" | "norm" | "normPeriods" | "seriesLimit">>;

// Reserved ids: no catalog unit may be named records, nor a breakdown none.
export const records = "records", noSplit = "none";
export const analysisViews: Array<[AnalysisView, string]> = [["trend", "Over time"], ["compare", "Compare groups"], ["distribution", "Distribution"], ["scatter", "Two measures"], ["cycle", "Hour / weekday cycle"]];
export const analysisReductions: Array<[AnalysisReduce, string]> = [["total", "Total"], ["mean", "Mean per unit"], ["median", "Median per unit"], ["max", "Largest unit"], ["count", "Number of units"]];
export const sampleThresholds = [0, 2, 3, 5, 10, 20, 50, 100];
export const analysisDefaults = { unit: records, reduce: "total", compareAs: "bars", minSamples: 0, scaleY: "linear", cycle: "weekday",
  period: "day", hours: 6, days: 3, rollingPeriods: 0, rollingOp: "mean", display: "interval", share: false, completePeriods: false, norm: false, normPeriods: 30, seriesLimit: 15 } as const;
// Groups drawn are fixed per display. The trend's seriesLimit is its own.
const barsShown = 12, slicesShown = 8;
// The groups a view fetches, and the points a scatter draws; a test lowers
// them rather than inserting thousands of rows.
export const analysisLimits = { groups: 1000, points: 2000 };

const oneOf = <T,>(value: unknown, options: readonly T[]): value is T => options.includes(value as T);
const has = (record: object, key: unknown) => typeof key === "string" && Object.hasOwn(record, key);

// Why a view cannot show this catalog at all, if it cannot. A rollup row is
// no observation a person cares about, so without units or per-record values
// nothing can be distributed or paired.
export function viewUnavailable(catalog: AnalysisCatalog, view: AnalysisView): string | undefined {
  const units = Object.keys(catalog.units).length > 0, subject = catalog.records.plural.toLowerCase();
  if (view === "distribution" && !units && !Object.values(catalog.measures).some(measure => measure.value)) return `These ${subject} are summaries, with no value per observation to distribute`;
  if (view === "scatter" && !units) return `A scatter pairs two measures of each conversation or run; these ${subject} have no such unit`;
  if (view === "cycle" && !Object.keys(catalog.cycle ?? {}).length) return `These ${subject} have no hours or dates to fold into a cycle`;
}
export const availableViews = (catalog: AnalysisCatalog) => analysisViews.filter(([view]) => !viewUnavailable(catalog, view));
export const cycleUnavailable = (catalog: AnalysisCatalog, cycle: CalendarCycle) => catalog.cycle?.[cycle] ? undefined : `These ${catalog.records.plural.toLowerCase()} have no ${cycle === "hour" ? "time of day" : "dates"} to fold`;

// Why the trend cannot draw this state, if it cannot. The trend draws every
// measure over records. A count, mean, median or largest of units is another
// quantity, which the trend would silently replace.
// A largest (or smallest) value has no honest window or running value:
// averaging or adding maxima across periods or groups is another quantity.
export const notTrended = (measure: AnalysisMeasure) => !measure.ratio && !measure.expression && (measure.op === "max" || measure.op === "min")
  ? `${measure.label} is one ${measure.op === "max" ? "largest" : "smallest"} value per group; averaging or adding it across periods would be another quantity, so Over time does not draw it` : undefined;
export function trendUnavailable(catalog: AnalysisCatalog, state: AnalysisState): string | undefined {
  if ((state.view ?? "trend") === "trend") return;
  const resolved = normalizeAnalysis(catalog, state), measure = catalog.measures[resolved.metric];
  if (notTrended(measure)) return `${notTrended(measure)}.`;
  if (resolved.view === "compare" && resolved.unit !== records && resolved.reduce !== "total") {
    return `Over time draws ${measure.label.toLowerCase()} over records, not the ${reductionLabel(resolved.reduce, measure, catalog.units[resolved.unit]).toLowerCase()}.`;
  }
}
// Each view's option, disabled with its reason where this state cannot move.
export const viewOptions = (catalog: AnalysisCatalog, state: AnalysisState): Array<[AnalysisView, string, string?]> =>
  analysisViews.map(([view, label]) => [view, label, view === "trend" ? trendUnavailable(catalog, state) : viewUnavailable(catalog, view)]);
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
    // A mean of per-period means weights a quiet period like a busy one.
    if (measure.op === "avg" && !measure.ratio) problems.push(`${id} is a mean; declare it as a ratio of a sum and a count so periods and windows pool.`);
  }
  for (const [id, components] of Object.entries(catalog.components ?? {})) {
    if (has(catalog.breakdowns, id) || id === noSplit) problems.push(`Component breakdown ${id} shares its id with a breakdown.`);
    for (const measure of components.measures) if (!catalog.measures[measure]?.additive) problems.push(`${id} splits ${measure}, which is not an additive measure.`);
    if (components.fallback !== noSplit && !has(catalog.breakdowns, components.fallback)) problems.push(`${id} falls back to unknown breakdown ${components.fallback}.`);
  }
  if (!has(catalog.measures, catalog.trend.metric) || !trendSplit(catalog, catalog.trend.split)) problems.push("The trend's default measure or breakdown is unknown.");
  if (!catalog.periods.day) problems.push("The trend needs a day field.");
  for (const [id, measure] of Object.entries(catalog.measures)) for (const part of measure.ratio ?? []) if (typeof part !== "string" && "expression" in part && !part.label) problems.push(`${id} has an expression part without a label.`);
  return problems;
}

// Field-level validity for a link or saved state. Combinations are coerced by
// normalizeAnalysis rather than rejected, so an older link still opens.
export function validAnalysisState(catalog: AnalysisCatalog, input: AnalysisState): boolean {
  const state = canonicalState(catalog, input);
  // The trend's settings, whatever the view, so every dashboard validates
  // them the same way.
  const trendFields = (state.period === undefined || Object.hasOwn(chartPeriods, state.period))
    && [state.hours, state.days].every(value => value === undefined || validPeriodCount(value))
    && (state.rollingPeriods === undefined || validRollingPeriods(state.rollingPeriods))
    && validChartDisplay(state.display) && (state.scale === undefined || validChartScale(state.scale)) && validChartPresentation(state)
    && (state.rollingOp === undefined || oneOf(state.rollingOp, rollingOps))
    && [state.share, state.completePeriods, state.norm].every(value => value === undefined || typeof value === "boolean")
    && (state.normPeriods === undefined || Number.isInteger(state.normPeriods) && state.normPeriods >= 1 && state.normPeriods <= 1000);
  if (!trendFields || state.v !== undefined && state.v !== analysisVersion) return false;
  if (state.view === undefined || state.view === "trend") return has(catalog.measures, state.metric) && trendSplit(catalog, state.split)
    && (state.unit === undefined || state.unit === records || has(catalog.units, state.unit));
  return oneOf(state.view, availableViews(catalog).map(([view]) => view))
    && (state.unit === undefined || state.unit === records || has(catalog.units, state.unit))
    && has(catalog.measures, state.metric) && (state.metricY === undefined || has(catalog.measures, state.metricY))
    && (state.split === noSplit || has(catalog.breakdowns, state.split))
    && (state.reduce === undefined || oneOf(state.reduce, analysisReductions.map(([reduce]) => reduce)))
    && (state.compareAs === undefined || oneOf(state.compareAs, compareModes))
    && (state.minSamples === undefined || Number.isInteger(state.minSamples) && state.minSamples >= 0 && state.minSamples <= 1_000_000)
    && (state.scaleY === undefined || validChartScale(state.scaleY))
    && (state.cycle === undefined || oneOf(state.cycle, analysisCycles));
}

export const shareable = (catalog: AnalysisCatalog, state: ResolvedAnalysis) => state.view === "compare" && state.split !== noSplit
  && (state.unit !== records && state.reduce === "count" || catalog.measures[state.metric].additive && state.reduce === "total");
// The trend draws records by any breakdown, a component breakdown, or none.
export const trendSplit = (catalog: AnalysisCatalog, split: string) => split === noSplit || has(catalog.breakdowns, split) || has(catalog.components ?? {}, split);
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
// A state in the catalog's own terms: older links and saved states are read
// through its legacy mapping. Controls apply changes to this state, so an
// old name never overrides a later choice.
export const canonicalState = <T extends AnalysisState>(catalog: AnalysisCatalog, state: T): T => catalog.legacy ? catalog.legacy(state) as T : state;

// A link as the dashboard reads it. A link marked with analysisVersion keeps
// its filters as the population. An older one, on a dashboard whose legend
// wrote selections as filters (legacySlices), may hold the selection as a
// filter on the breakdown's single field: that filter becomes the state's
// selection, and the rest of where stays the population. Either way the
// result is marked, so it is read the same way again.
// A card's link is marked current where it stops being a card link
// (stampCardChart), since cards always wrote their filters as the population.
export function readAnalysisLink<T extends AnalysisState>(catalog: AnalysisCatalog, state: T, where: WhereTerm[]): { state: T; where: WhereTerm[] } {
  if (state.v === analysisVersion) return { state, where };
  const fields = catalog.breakdowns[canonicalState(catalog, state).split]?.fields ?? [], field = fields[0];
  if (!catalog.legacySlices || fields.length !== 1 || !where.some(term => isSliceTerm(term, field))) return { state: { ...state, v: analysisVersion }, where };
  const slice = readSlice(where, field);
  return { state: { ...state, v: analysisVersion, selection: { values: slice.values.map(value => value ?? ""), excluded: slice.excluded } }, where: where.filter(term => !isSliceTerm(term, field)) };
}

// A control's change, applied to the state as asked for rather than as drawn.
export const changeState = <T extends AnalysisState>(catalog: AnalysisCatalog, state: T, change: Partial<AnalysisState>): T => ({ ...canonicalState(catalog, state), ...change });

export function normalizeAnalysis<T extends AnalysisState>(catalog: AnalysisCatalog, state: T): T & ResolvedAnalysis {
  const next = { ...analysisDefaults, ...canonicalState(catalog, state) } as T & ResolvedAnalysis;
  next.view ??= "trend";
  if (next.view === "trend") return normalizeTrend(catalog, next);
  const firstUnit = Object.keys(catalog.units)[0];
  // A view this catalog cannot show at all becomes Compare; viewOptions
  // disables it with the reason.
  if (viewUnavailable(catalog, next.view)) next.view = "compare";
  if (!has(catalog.measures, next.metric)) next.metric = catalog.scatter[0];
  if (next.unit !== records && !has(catalog.units, next.unit)) next.unit = records;
  if (next.view === "cycle") { next.unit = records; next.split = noSplit; if (cycleUnavailable(catalog, next.cycle)) next.cycle = next.cycle === "hour" ? "weekday" : "hour"; }
  if (next.view === "scatter") {
    if (next.unit === records) next.unit = firstUnit;
    if (!has(catalog.measures, next.metricY) || next.metricY === next.metric) next.metricY = catalog.scatter.find(id => id !== next.metric) ?? Object.keys(catalog.measures).find(id => id !== next.metric)!;
  } else if (!has(catalog.measures, next.metricY)) next.metricY = catalog.scatter[1];
  // A record distribution needs a per-record value: a measure without one
  // moves to the first unit, or where there are no units to the first
  // measure with a value (the Metric control disables the others).
  if (next.view === "distribution" && next.unit === records && !catalog.measures[next.metric].value) {
    if (firstUnit) next.unit = firstUnit; else next.metric = Object.keys(catalog.measures).find(id => catalog.measures[id].value)!;
  }
  if (!splitValid(catalog, next.unit, next.split)) next.split = noSplit;
  // A cycle summarizes slot values: a box per slot by default, or their mean,
  // or their total where slot values add up (not ratios or distinct counts).
  if (next.view === "cycle") { if (state.reduce === undefined || !["total", "mean", "median"].includes(next.reduce) || next.reduce === "total" && !cycleTotal(catalog.measures[next.metric])) next.reduce = "median"; }
  else if (next.unit === records) next.reduce = "total";
  else if (next.reduce === "total" && !unitTotal(catalog.measures[next.metric])) next.reduce = "mean";
  if (next.compareAs === "share" && !shareable(catalog, next)) next.compareAs = "bars";
  return next;
}

export type TrendKind = "additive" | "ratio" | "count" | "level";
// How a measure behaves over time. Additive amounts add up across periods
// and groups. A ratio pools its parts over any window. A distinct count is
// counted per period, so periods and groups do not add up. Any other value
// is a level per period.
export const trendKind = (measure: Pick<AnalysisMeasure, "additive" | "ratio" | "op">): TrendKind => measure.additive ? "additive" : measure.ratio ? "ratio" : measure.op === "count_distinct" ? "count" : "level";
const singular = (unit: string) => unit.replace(/s$/, "");
export const periodField = (period: ChartPeriod): TrendPeriodField => period === "hour" || period === "hours" ? "hour" : period === "days" ? "day" : period;
const baseUnit = (period: ChartPeriod) => period === "hours" ? "hour" : period === "days" ? "day" : period;
const multiplePeriods = (state: Pick<AnalysisState, "period" | "hours" | "days">) => state.period === "hours" && (state.hours ?? 1) > 1 || state.period === "days" && (state.days ?? 1) > 1;

// Why each trend option is unavailable for this measure, breakdown and
// display, if it is. The controls disable the option with this reason, and
// normalizeTrend moves a state off it.
export function trendLimits(catalog: AnalysisCatalog, state: Pick<ResolvedAnalysis, "metric" | "split" | "display" | "share">) {
  const measure = catalog.measures[state.metric], kind = trendKind(measure), name = measure.label.toLowerCase();
  const component = (split: string) => {
    const parts = catalog.components?.[split];
    return parts && !parts.measures.includes(state.metric) ? `${parts.label} splits ${parts.measures.map(id => catalog.measures[id].label.toLowerCase()).join(" and ")} only` : undefined;
  };
  return {
    share: !measure.additive ? `${measure.label} does not add up across groups, so it has no shares` : state.split === noSplit ? "Shares need a breakdown" : undefined,
    cumulative: kind === "count" ? `A running count of distinct ${measure.unit} cannot be built from per-period counts` : kind === "level" ? `${measure.label} does not accumulate` : undefined,
    rollingSum: kind === "ratio" ? `${measure.label} pools its window; a sum of ratios means nothing` : kind === "level" ? `${measure.label} does not add up across periods` : undefined,
    multiple: kind === "count" ? `Distinct ${measure.unit} are counted per hour, day, week or month; several periods would add their counts` : undefined,
    period: (period: ChartPeriod) => catalog.periods[periodField(period)] || periodField(period) === "hour" && catalog.timestamp ? undefined : `These records have no ${periodField(period)} to group by`,
    norm: state.display === "cumulative" ? "A norm compares interval values, not running totals" : state.share ? "Shares have no norm" : !measure.additive && state.split !== noSplit ? `${measure.label} has no total across groups; choose no breakdown for a norm` : undefined,
    component, name,
  };
}

// Fill the trend's defaults and move each setting the measure cannot use to
// the nearest one it can. Only drawing and querying use the result; the saved
// state keeps what was asked for, so a setting returns with a measure that
// allows it.
function normalizeTrend<T extends AnalysisState>(catalog: AnalysisCatalog, next: T & ResolvedAnalysis): T & ResolvedAnalysis {
  if (!Number.isInteger(next.normPeriods) || next.normPeriods < 1) next.normPeriods = analysisDefaults.normPeriods;
  if (!has(catalog.measures, next.metric) || notTrended(catalog.measures[next.metric])) next.metric = catalog.trend.metric;
  if (!trendSplit(catalog, next.split)) next.split = catalog.trend.split;
  if (catalog.components?.[next.split] && !catalog.components[next.split].measures.includes(next.metric)) next.split = catalog.components[next.split].fallback;
  next.share = next.share === true; next.completePeriods = next.completePeriods === true; next.norm = next.norm === true;
  // The trend's scale, resolved once for the control and the chart: a state
  // without one draws the old trend charts' default, the linear + log split.
  if (!validChartScale(next.scale)) next.scale = defaultChartScale;
  if (!oneOf(next.rollingOp, rollingOps)) next.rollingOp = "mean";
  let limits = trendLimits(catalog, next);
  if (next.share && limits.share) next.share = false;
  if (next.display === "cumulative" && limits.cumulative) next.display = "interval";
  if (next.rollingOp === "sum" && limits.rollingSum) next.rollingOp = "mean";
  if (multiplePeriods(next) && limits.multiple) next.period = baseUnit(next.period);
  if (limits.period(next.period)) next.period = "day";
  limits = trendLimits(catalog, next);
  if (next.norm && limits.norm) next.norm = false;
  return next;
}

// What normalizeTrend changed that the state asked for, in words, so the
// dashboard can say why the chart differs from the link or the last choice.
export function trendAdjustments(catalog: AnalysisCatalog, input: AnalysisState): string[] {
  if ((input.view ?? "trend") !== "trend") return [];
  const asked = { ...analysisDefaults, ...canonicalState(catalog, input) };
  const state = normalizeAnalysis(catalog, input), measure = catalog.measures[state.metric];
  const limits = trendLimits(catalog, { ...state, split: asked.split, display: asked.display, share: asked.share === true });
  const notes: string[] = [];
  if (has(catalog.measures, asked.metric) && notTrended(catalog.measures[asked.metric])) notes.push(`${notTrended(catalog.measures[asked.metric])}; ${measure.label.toLowerCase()} is shown instead.`);
  const component = catalog.components?.[asked.split];
  if (has(catalog.measures, asked.metric) && component && state.split !== asked.split) notes.push(`${limits.component(asked.split)}; ${measure.label} is shown ${state.split === noSplit ? "as one total" : `by ${catalog.breakdowns[state.split].label.toLowerCase()}`}.`);
  if (asked.share === true && !state.share) notes.push(`${limits.share}; values are shown instead.`);
  if (asked.display === "cumulative" && state.display !== "cumulative") notes.push(`${limits.cumulative}; values are shown per ${chartPeriodUnit(state.period)}.`);
  if (asked.rollingOp === "sum" && state.rollingOp !== "sum") notes.push(`${limits.rollingSum}; the window is pooled instead.`);
  if (asked.period !== state.period) notes.push(`${limits.period(asked.period) ?? limits.multiple}; each bucket is one ${chartPeriodUnit(state.period)}.`);
  if (asked.norm === true && !state.norm) notes.push(`${trendLimits(catalog, state).norm}.`);
  return notes;
}
const chartPeriodUnit = (period: ChartPeriod) => baseUnit(period);
// A rolling sum of distinct counts adds each period's count: one conversation
// active on three days is three conversation-days.
export const rollingSumUnit = (measure: AnalysisMeasure, period: ChartPeriod) => trendKind(measure) === "count" ? `${singular(measure.unit)}-${baseUnit(period)}s` : measure.unit;

// Move between views, carrying what both share: the trend's shares become
// a share display, and back. A unit reduction the trend cannot draw stays
// where it is (trendUnavailable says why); nothing is substituted.
// The trend keeps what was asked for (its own settings survive a visit to
// another view); the other views are saved as drawn, as in phase 1.
export function switchView<T extends AnalysisState>(catalog: AnalysisCatalog, input: T, view: AnalysisView): T & ResolvedAnalysis {
  const state = canonicalState(catalog, input), current = state.view ?? "trend";
  if (view === "trend") {
    if (current === "trend" || trendUnavailable(catalog, state)) return normalizeAnalysis(catalog, state);
    return { ...state, view, share: current === "compare" && state.compareAs === "share" } as T & ResolvedAnalysis;
  }
  if (current === "trend") {
    const trend = normalizeTrend(catalog, { ...analysisDefaults, ...state, view: "trend" } as T & ResolvedAnalysis);
    return normalizeAnalysis(catalog, { ...state, view, metric: trend.metric, compareAs: trend.share ? "share" as const : "bars" as const, ...view === "cycle" ? { reduce: "median" as const } : {},
      split: has(catalog.breakdowns, trend.split) ? trend.split : noSplit });
  }
  return normalizeAnalysis(catalog, { ...state, view, ...view === "cycle" ? { reduce: "median" as const } : {} });
}

export const unique = (terms: WhereTerm[]) => terms.filter((term, index) => terms.findIndex(other => JSON.stringify(other) === JSON.stringify(term)) === index);
const log = (scale?: ChartScale) => ({ mode: scale === "logarithmic" ? "log" as const : "linear" as const });
export const measureExpression = (measure: AnalysisMeasure) => measure.ratio ? `${partExpression(measure.ratio[0])}/NULLIF(${partExpression(measure.ratio[1])},0)` : measure.expression;
// Grouped rows show a measure in its own format: a duration as a duration.
const cellDisplay = (format: MetricValueFormat): Pick<SummaryColumn, "display"> => format.kind === "duration" && format.sourceUnit === "milliseconds" ? { display: { kind: "duration", sourceUnit: "milliseconds" } }
  : format.kind === "number" || format.kind === "percent" ? { display: { kind: format.kind, ...format.decimals !== undefined ? { decimals: format.decimals } : {} } } : {};
function summary(id: string, measure: AnalysisMeasure): SummaryColumn {
  const expression = measureExpression(measure);
  return expression ? { id, op: "formula", expression, label: measure.label, ...cellDisplay(measure.format) }
    : { id, op: measure.op, ...(measure.field ? { field: measure.field } : {}), label: measure.label, ...cellDisplay(measure.format) };
}
// A ratio's parts beside it, so grouped rows show what each group pools.
const ratioParts = (measure: AnalysisMeasure): SummaryColumn[] => measure.ratio ? measure.ratio.map((part, index) => ({ id: index ? "analysis_denominator" : "analysis_numerator", ...partColumn(part),
  label: ratioPart(part).label ? `${ratioPart(part).label![0].toUpperCase()}${ratioPart(part).label!.slice(1)}` : `${ratioPart(part).op === "count" ? "Records with" : "Total"} ${ratioPart(part).field!.replaceAll("_", " ")}` })) : [];
export function aggregate(measure: AnalysisMeasure): Pick<AggregationClause, "op" | "field" | "expression"> {
  const expression = measureExpression(measure);
  return { op: measure.op, ...(measure.field && !expression ? { field: measure.field } : {}), ...(expression ? { expression } : {}) };
}

export type AnalysisQuery = {
  // The query the chart executes; its first aggregation is the chart's clause.
  query: QueryState; clause: AggregationClause;
  // What "View supporting query" opens: the same population and grouping.
  supporting: QueryState;
  grouped: boolean;
  // A cycle Query Table computes itself (hour of day or day of week over a
  // timestamp): the chart draws its result as returned, and a slot opens
  // its occurrences' records. Otherwise cycleResult folds hourly or daily
  // totals. occurrences counts the whole hours or days in a bounded range;
  // problem says why the query does not run (too long a range).
  native?: boolean; occurrences?: number; problem?: string;
  // A share is of every group in the population the filters define. A
  // legend selection (the state's selection) only chooses which groups are
  // drawn, so it never enters the executed query and never changes a
  // denominator: selectGroups draws the selected groups, with the rest as
  // Other in a share, or hidden in bars, boxes and points. A selection is of
  // a breakdown group, so for a unit it maps through the unit's fields
  // (provider = codex selects the conversations whose top-level provider is
  // codex); mapped names the unit field when it differs. The supporting query
  // keeps the selection where a filter can state it. fields counts the
  // breakdown's keys.
  // late: a selection outside shares too large for the query's predicate
  // budget, so it is drawn from the groups the query returned.
  selection?: { values: string[]; excluded: boolean; mapped?: string; fields: number; share: boolean; late?: boolean };
};

// The executed query for a non-trend view. Records compare and distribute
// matching records directly. An entity unit first groups records into one row
// per unit (and its constant breakdown), as card recipes do, then compares,
// distributes or pairs those rows. A threshold is native minCount: it removes
// small groups before groupLimit, so they never crowd out eligible ones.
// A weekday cycle over recorded dates counts complete days only, and a trend
// with complete periods whole periods only. The range starts at the next
// local period boundary (midnight for days), so the first period's records
// are whole, not the part of a day after a preset's start time.
export function completeStart(where: WhereTerm[], time: AnalysisCatalog["time"], zone: string, now: number, unit: TrendPeriodField = "day"): WhereTerm[] {
  return where.map(term => {
    if (!("field" in term) || term.negated || ![">=", ">"].includes(term.op) || ![time.first, time.last].includes(term.field)) return term;
    const at = Number(coerceValue("datetime", term.value, now));
    if (!Number.isFinite(at)) return term;
    const floor = metricDateFloor(at, unit, zone), start = floor < at || floor === at && term.op === ">" ? metricDateNext(floor, unit, zone) : floor;
    return { ...term, op: ">=" as const, value: new Date(start).toISOString() };
  });
}

// What a minimum counts: rows (each one an observation), or the catalog's
// samples, a summed field or a field's distinct values, where one record is
// not one observation.
const minimumOf = (catalog: AnalysisCatalog, min: number): AggregationClause["minCount"] => catalog.samples
  ? "sum" in catalog.samples ? { min, sum: catalog.samples.sum } : { min, distinct: catalog.samples.distinct } : min;
const samplesColumn = (samples: NonNullable<AnalysisCatalog["samples"]>): SummaryColumn => "sum" in samples
  ? { id: "analysis_samples", op: "sum", field: samples.sum, label: samples.label } : { id: "analysis_samples", op: "count_distinct", field: samples.distinct, label: samples.label };

export function analysisQuery(catalog: AnalysisCatalog, input: AnalysisState, inputWhere: WhereTerm[], { zone = "UTC", now = Date.now() }: { zone?: string; now?: number } = {}): AnalysisQuery {
  const state = normalizeAnalysis(catalog, input);
  if (state.view === "trend") throw new Error("The trend view has its own chart.");
  const shares = state.view === "compare" && state.compareAs === "share" && state.split !== noSplit;
  // The selection is of the breakdown's groups, drawn on the group fields of
  // the current unit.
  const recordFields = state.split === noSplit || state.view === "cycle" ? [] : catalog.breakdowns[state.split].fields;
  const groupFields = state.unit === records ? recordFields : catalog.units[state.unit]?.breakdowns[state.split] ?? [];
  const chosen = recordFields.length ? state.selection : undefined;
  const selection: AnalysisQuery["selection"] = chosen?.values.length ? { values: chosen.values, excluded: chosen.excluded, fields: recordFields.length, share: shares, ...groupFields[0] !== recordFields[0] ? { mapped: groupFields[0] } : {} } : undefined;
  const cycleKey = state.view === "cycle" ? catalog.cycle![state.cycle]! : undefined;
  const where = cycleKey?.kind === "recorded-date" ? completeStart(inputWhere, catalog.time, zone, now) : inputWhere;
  const run = (terms: WhereTerm[]) => catalog.executable ? catalog.executable(terms, now) : terms;
  const measure = catalog.measures[state.metric], measureY = catalog.measures[state.metricY];
  const breakdown = state.split === noSplit ? undefined : catalog.breakdowns[state.split];
  const unit = state.unit === records ? undefined : catalog.units[state.unit];
  const implied = [...unit?.where ?? [], ...breakdown?.where ?? [], ...measure.where ?? [], ...state.view === "scatter" ? measureY.where ?? [] : []];
  const sourceWhere = unique([...where, ...implied]);
  // The selected groups as a filter on fields, added to a where list holding
  // base: a record filter on the breakdown's fields, or a group filter on the
  // unit's. A bucket's drill-down adds one predicate per key to the same
  // list, so those stay free. Undefined when it would exceed the list's
  // predicate budget.
  const drilled = unit ? state.view === "scatter" ? new Set([...groupFields, ...[unit.key].flat()]).size : groupFields.length : recordFields.length;
  const stated = (fields: string[], base: WhereTerm[]) => selection && fields.length ? selectionWhere(fields, selection, predicateCount(base) + drilled) : undefined;
  // A threshold counts what is plotted: a distribution drops missing values
  // before grouping (records) or as a group filter (units).
  const valued: WhereTerm[] = !unit && state.view === "distribution" ? [{ field: measure.value!, op: "is_not_null", value: "" }] : [];
  const recordWhere = [...sourceWhere, ...valued];
  // Outside shares there is no denominator to protect, so the selection
  // filters the executed query: no group or point limit can crowd a selected
  // group out. A share keeps every group and selects afterwards.
  const unitHaving: WhereTerm[] = unit && (state.view === "distribution" || state.view === "compare" && state.reduce !== "count") ? [{ field: "analysis_value", op: "is_not_null", value: "" }] : [];
  const filter = selection && !shares ? unit ? stated(groupFields, unitHaving) : stated(recordFields, run(recordWhere)) : undefined;
  if (selection && !shares && !filter) selection.late = true;
  // A records compare counts the catalog's samples; distributions take row
  // minimums only, and each unit row is one unit.
  const counted = thresholded(state) ? { minCount: !unit && state.view === "compare" ? minimumOf(catalog, state.minSamples) : state.minSamples } : {};
  const valueLabel = state.view === "compare" && unit && state.reduce !== "total" ? `${reductionLabel(state.reduce, measure, unit)}${state.reduce === "count" ? "" : ` · ${measure.label}`}` : measure.label;
  const format = state.view === "compare" && state.reduce === "count" ? { kind: "number" as const, decimals: 0 } : measure.format;
  const compareDisplay = (groups: boolean): MetricDisplay => !groups ? { kind: "value", format, valueLabel }
    : state.compareAs === "share" ? { kind: "pie", maxGroups: Math.max(slicesShown, (selection?.values.length ?? 0) + 1), showOther: true, legendPosition: "right", format }
    : { kind: "bar-horizontal", maxGroups: barsShown, legendPosition: "none", xLabel: valueLabel, xScale: log(state.scale), format };
  const distributionDisplay = (box: boolean): MetricDisplay => ({ kind: box ? "box" : "histogram", maxGroups: barsShown, xLabel: measure.label, xScale: log(state.scale), ...(box ? { format } : { xFormat: format }) });

  if (cycleKey) {
    const { kind, field } = cycleKey;
    if (kind === "timestamp") return timestampCycle(catalog, state, field, run(sourceWhere), { zone, now });
    // Usage's ledger: hourly totals keyed by UTC instants, or recorded dates,
    // which cycleResult folds into slots.
    const clause: AggregationClause = { id: "analysis", ...aggregate(measure), groupBy: [field], groupLimit: cycleGroups, sort: [{ key: "group0", dir: "asc" }], label: measure.label };
    const query = { ...EMPTY_QUERY, where: run(sourceWhere), aggregations: [clause] };
    // Grouped usage queries read daily records, which carry no hour (see
    // docs/handoffs/usage-v3-hour-grouping.md), so the hour cycle opens as
    // records.
    return { query, clause, grouped: false, supporting: kind === "recorded-date" ? groupedRecords(run(sourceWhere), [field], [summary("analysis_value", measure)], [], field) : { ...EMPTY_QUERY, where: run(sourceWhere) } };
  }

  if (!unit) {
    const groupBy = breakdown?.fields ?? [];
    const clause: AggregationClause = state.view === "distribution"
      ? { id: "analysis", op: "count", groupBy, groupLimit: analysisLimits.groups, sort: [{ key: "samples", dir: "desc" }], label: measure.label, ...counted,
        distribution: breakdown ? { kind: "box", input: `[${measure.value}]`, whiskers: "tukey" } : { kind: "histogram", input: `[${measure.value}]` }, display: distributionDisplay(Boolean(breakdown)) }
      : { id: "analysis", ...aggregate(measure), groupBy, groupLimit: analysisLimits.groups, sort: [{ key: "value", dir: "desc" }], label: valueLabel, ...counted, display: compareDisplay(Boolean(breakdown)) };
    const query = { ...EMPTY_QUERY, where: run([...recordWhere, ...filter ?? []]), aggregations: [clause] };
    // The supporting query lists what is drawn: a share's selected groups too.
    const supported = run([...recordWhere, ...filter ?? stated(recordFields, run(recordWhere)) ?? []]);
    const samples = state.view === "compare" && catalog.samples ? catalog.samples : undefined;
    const summaries = [summary("analysis_value", measure), ...ratioParts(measure), { id: "analysis_records", op: "count" as const, label: catalog.records.plural }, ...samples ? [samplesColumn(samples)] : []];
    if (state.view === "distribution") summaries.splice(1, 0, { id: "analysis_median", op: "formula", expression: `MEDIAN([${measure.value}])`, label: `Median ${measure.label.toLowerCase()} per ${catalog.records.label.toLowerCase()}` });
    const having: WhereTerm[] = "minCount" in counted ? [{ field: samples ? "analysis_samples" : "analysis_records", op: ">=", value: String(state.minSamples) }] : [];
    return { query, clause, grouped: false, selection, supporting: breakdown ? groupedRecords(supported, groupBy, summaries, having) : { ...EMPTY_QUERY, where: supported } };
  }

  // Breakdown first: a scatter colors its points by the first key.
  const groupBy = breakdown ? unit.breakdowns[state.split] : [], unitSplit = groupBy.length > 0;
  const keys = [...groupBy, ...[unit.key].flat().filter(field => !groupBy.includes(field))];
  const pooled = state.view === "compare" && state.reduce === "total" && measure.ratio;
  const summaries = [summary("analysis_value", measure), ...state.view === "scatter" ? [summary("analysis_y", measureY)] : [],
    ...pooled ? [{ id: "analysis_numerator", ...partColumn(measure.ratio![0]) }, { id: "analysis_denominator", ...partColumn(measure.ratio![1]) }] : []];
  const reduce: Record<AnalysisReduce, Pick<AggregationClause, "op" | "field" | "expression">> = {
    total: pooled ? { op: "sum", expression: "SUM([analysis_numerator])/NULLIF(SUM([analysis_denominator]),0)" } : { op: "sum", field: "analysis_value" }, mean: { op: "avg", field: "analysis_value" }, max: { op: "max", field: "analysis_value" },
    count: { op: "count" }, median: { op: "count", field: "analysis_value", expression: "MEDIAN([analysis_value])" },
  };
  const clause: AggregationClause = state.view === "scatter"
    ? { id: "analysis", op: "max", field: "analysis_value", expression: "MAX([analysis_value])", expressionY: "MAX([analysis_y])", groupBy: keys, groupLimit: analysisLimits.points, sort: [{ key: "value", dir: "desc" }], label: unit.plural,
      display: { kind: "scatter", maxGroups: analysisLimits.points, xLabel: measure.label, yLabel: measureY.label, xScale: log(state.scale), yScale: log(state.scaleY), xFormat: measure.format, yFormat: measureY.format, legendPosition: unitSplit ? "right" : "none" } }
    : state.view === "distribution"
      ? { id: "analysis", op: "count", field: "analysis_value", ...unitSplit ? { expression: "MEDIAN([analysis_value])" } : {}, groupBy, groupLimit: analysisLimits.groups, sort: [{ key: "samples", dir: "desc" }], label: `${measure.label} per ${unit.label.toLowerCase()}`, ...counted,
        distribution: unitSplit ? { kind: "box", input: "[analysis_value]", whiskers: "tukey" } : { kind: "histogram", input: "[analysis_value]" }, display: distributionDisplay(Boolean(unitSplit)) }
      : { id: "analysis", ...reduce[state.reduce], groupBy, groupLimit: analysisLimits.groups, sort: [{ key: "value", dir: "desc" }], label: valueLabel, ...counted, display: compareDisplay(Boolean(unitSplit)) };
  // Only units with a value are plotted, so only they meet a minimum. A
  // count of units counts every unit.
  const query: QueryState = { ...EMPTY_QUERY, grouping: { source: { ...EMPTY_QUERY, where: run(sourceWhere) }, keys, summaries, representative: null },
    select: [...keys, ...summaries.map(column => column.id)].map(field => ({ field })), where: [...unitHaving, ...filter ?? []], orderBy: [{ field: "analysis_value", dir: "desc", nulls: "last" }, ...keys.map(field => ({ field, dir: "asc" as const }))], aggregations: [clause] };
  // The supporting query lists the selected groups' units: a group filter
  // on the unit's own field, never a record filter on another one.
  const groups = filter ? [] : stated(groupFields, unitHaving) ?? [];
  return { query, clause, grouped: true, selection, supporting: groups.length ? { ...query, where: [...query.where, ...groups] } : query };
}

// Hour of day or day of week over a timestamp, computed by Query Table in
// the viewer's zone (dateGrouping hourOfDay or dayOfWeek). An observation is
// one whole local hour or day in the range (an occurrence), idle ones
// included: a box over them, or their mean. A total adds up every record in
// a slot's whole hours or days. Occurrences the range cuts short, and the one
// in progress, are left out. The supporting query opens the hourly or daily
// totals the slots summarise, as grouped rows keyed by local hour or day.
function timestampCycle(catalog: AnalysisCatalog, state: ResolvedAnalysis, field: string, where: WhereTerm[], { zone, now }: { zone: string; now: number }): AnalysisQuery {
  const measure = catalog.measures[state.metric], hour = state.cycle === "hour", per = hour ? "hour" as const : "day" as const, slots = hour ? 24 : 7;
  // Idle occurrences fill the range from its start; without one, from the
  // first observed occurrence.
  const bounded = where.some(term => "field" in term && term.field === field && !term.negated && [">", ">=", "="].includes(term.op));
  const dateGrouping: MetricDateGrouping = { field, interval: hour ? "hourOfDay" : "dayOfWeek", timeZone: zone, includeEmpty: true, range: bounded ? "query" : "observed",
    ...state.reduce === "total" ? { partialIntervals: "exclude" as const } : { occurrence: { interval: per, includeEmpty: true } } };
  const bars = (label: string): Partial<AggregationClause> => ({ label, display: { kind: "bar-vertical", maxGroups: slots, xLabel: hour ? "Local hour" : "Weekday", yLabel: measure.label, yScale: log(state.scale), format: measure.format, legendPosition: "none" } });
  const clause: AggregationClause = { id: "analysis", ...aggregate(measure), groupBy: [field], dateGrouping, sort: [{ key: "group0", dir: "asc" }],
    ...state.reduce === "median" ? { label: `${measure.label} per ${per}`, distribution: { kind: "box", input: "", whiskers: "tukey" }, display: { kind: "box", maxGroups: slots, xLabel: `${measure.label} / ${per}`, xScale: log(state.scale), format: measure.format } }
      : bars(state.reduce === "total" ? `Total ${measure.label.toLowerCase()}` : `${measure.additive ? "Mean" : "Unweighted mean"} ${measure.label.toLowerCase()} per ${per}`) };
  const bounds = timeBounds(where, catalog.time, now), end = metricDateFloor(Math.min(bounds.to?.getTime() ?? now, now), per, zone);
  const whole = withEnd(completeStart(where, catalog.time, zone, now, per), catalog.time, new Date(end));
  const supporting = groupedRecords(whole, [{ field, interval: per, timeZone: zone }], [summary("analysis_value", measure), ...ratioParts(measure)], [], field);
  // The whole hours or days in the range, which the note counts: idle ones
  // too, though a ratio's have no value. A box or mean reads one calendar
  // group per occurrence, so past maxCycleSlots of them the query would
  // exceed the service's group budget; it does not run.
  let occurrences: number | undefined;
  if (bounds.from) {
    const from = bounds.from.getTime(), floor = metricDateFloor(from, per, zone), start = floor < from ? metricDateNext(floor, per, zone) : floor;
    occurrences = Math.max(0, wholeIntervals(start, end, per, zone));
  }
  const problem = state.reduce !== "total" && (occurrences ?? 0) > maxCycleSlots[state.cycle] ? cycleRangeError(state.cycle) : undefined;
  return { query: { ...EMPTY_QUERY, where, aggregations: problem ? [] : [clause] }, clause, grouped: false, native: true, supporting, ...occurrences === undefined || problem ? {} : { occurrences }, ...problem ? { problem } : {} };
}

// The whole hours or days from start to end, both boundaries of that unit in
// zone. Hours are fixed lengths; days count local calendar dates, so a DST
// day of 23 or 25 hours is one day.
const localDate = (time: number, zone: string) => Date.parse(`${new Intl.DateTimeFormat("en-CA", { timeZone: zone }).format(time)}T00:00:00Z`);
function wholeIntervals(start: number, end: number, per: "hour" | "day", zone: string) {
  return per === "hour" ? Math.round((end - start) / 3_600_000) : Math.round((localDate(end, zone) - localDate(start, zone)) / 86_400_000);
}

// A native cycle's result, checked as the fold checks its totals: complete,
// with every slot's value. slots counts the whole hours or days in the range
// (analysisQuery's occurrences); without a range start, the occurrences
// Query Table counted, which leave out a ratio's idle ones, so only where
// idle ones count as zero. observations counts the records. A range whose
// start was not known up front can still exceed the service's group budget
// with its occurrences; that, and only that, reads as the range limit.
export function nativeCycleResult(analysis: AnalysisQuery, result: AggregationResultEntry) {
  const cycle: CalendarCycle = analysis.clause.dateGrouping?.interval === "hourOfDay" ? "hour" : "weekday";
  if (result.error || result.coverage === "partial" || result.hiddenGroups || (result.groupCount ?? 0) > result.buckets.length || result.buckets.some(bucket => bucket.error)) {
    const error = result.error ?? result.buckets.find(bucket => bucket.error)?.error;
    const groups = analysis.clause.dateGrouping?.occurrence && (!error || /\bgroup population exceeds host budget|exceeds \d+ groups|^Occurrences need every interval/.test(error));
    throw new Error(groups ? cycleRangeError(cycle) : error ?? "Complete totals are required for an hour or weekday cycle. Choose a shorter range.");
  }
  const counted = result.buckets.reduce((n, bucket) => n + (bucket.occurrences ?? 0), 0);
  const slots = analysis.occurrences ?? (analysis.clause.dateGrouping?.occurrence && idleIsZero(analysis.clause) ? counted : undefined);
  return { result, slots, observations: result.buckets.reduce((n, bucket) => n + bucket.count, 0) };
}

// The predicates a where list holds, every OR branch's included: Query
// Table allows MAX_WHERE_CLAUSES of them in one list.
export const predicateCount = (terms: WhereTerm[]) => terms.reduce((count, term) => count + predicatesOf(term).length, 0);

// A legend selection as a filter on its group fields. One field is a slice.
// Combined groups are included as one OR of AND branches, one branch per
// group's equalities, and excluded as one OR per group (field != value, or
// missing). Every predicate counts toward the query's budget: where the
// selection would take a where list already holding `used` predicates past
// it, the selection has no filter (undefined) and is drawn after the query.
export function selectionWhere(fields: string[], selection: { values: string[]; excluded: boolean }, used = 0): WhereTerm[] | undefined {
  if (!selection.values.length) return [];
  const terms = selectionTerms(fields, selection);
  return used + predicateCount(terms) > MAX_WHERE_CLAUSES ? undefined : terms;
}
function selectionTerms(fields: string[], selection: { values: string[]; excluded: boolean }): WhereTerm[] {
  if (fields.length === 1) return withSlice([], fields[0], { values: selection.values.map(value => value || null), excluded: selection.excluded });
  const groups = selection.values.map(value => (JSON.parse(value) as unknown[]).map((key, index) => ({ field: fields[index], key: key === null || key === undefined || key === "" ? null : String(key) })));
  const equal = ({ field, key }: { field: string; key: string | null }): WhereClause => key === null ? { field, op: "is_null", value: "" } : { field, op: "=", value: key };
  if (selection.excluded) return groups.map(group => ({ any: group.flatMap(({ field, key }): WhereClause[] => key === null ? [{ field, op: "is_not_null", value: "" }] : [{ field, op: "!=", value: key }, { field, op: "is_null", value: "" }]) }));
  // One group is its equalities; a lone branch would be normalized to them.
  if (groups.length === 1) return groups[0].map(equal);
  return [{ any: groups.map(group => ({ all: group.map(equal) })) }];
}

// A result drawn under a legend selection: the selected groups (or all but
// the excluded ones). A share draws every other group as Other, so a small
// selected group is never folded away and the shares keep every group as
// their denominator. Bars, boxes and points hide the other groups; a scatter
// matches a point by its breakdown keys, before the unit's.
// Groups beyond the query's group limit are already folded into Other; a
// selected one among them is counted there, or not drawn (missing says how
// many).
export function selectGroups(entry: AggregationResultEntry, selection?: AnalysisQuery["selection"]): AggregationResultEntry & { missing?: number } {
  if (!selection?.values.length) return entry;
  const key = (bucket: AggregationBucket) => selection.fields === 1 ? String(bucket.keys[0] ?? "") : JSON.stringify(bucket.keys.slice(0, selection.fields).map(value => value ?? null));
  const truncated = Boolean(entry.other) || (entry.groupCount ?? 0) > entry.buckets.length;
  const returned = new Set(entry.buckets.map(key));
  const missing = !selection.excluded && truncated ? selection.values.filter(value => !returned.has(value)).length : 0;
  const drawn = (bucket: AggregationBucket) => selection.values.includes(key(bucket)) !== selection.excluded;
  const shown = { ...entry, buckets: entry.buckets.filter(drawn), ...missing ? { missing } : {} };
  if (!selection.share) return { ...shown, groupCount: shown.buckets.length, other: undefined };
  const rest = [...entry.buckets.filter(bucket => !drawn(bucket)), ...entry.other ? [entry.other] : []];
  const value = rest.reduce((sum, bucket) => sum + (Number(bucket.value) || 0), 0), count = rest.reduce((sum, bucket) => sum + bucket.count, 0);
  return { ...shown, ...rest.length ? { other: { keys: [], value, count } } : { other: undefined } };
}

function groupedRecords(where: WhereTerm[], keys: GroupKey[], summaries: SummaryColumn[], having: WhereTerm[], order = summaries[0].id): QueryState {
  const fields = groupKeyFields(keys);
  return { ...EMPTY_QUERY, grouping: { source: { ...EMPTY_QUERY, where }, keys, summaries, representative: null },
    select: [...fields, ...summaries.map(column => column.id)].map(field => ({ field })), where: having,
    orderBy: [{ field: order, dir: order === fields[0] ? "asc" : "desc", nulls: "last" }, ...fields.filter(field => field !== order).map(field => ({ field, dir: "asc" as const }))] };
}

// The records behind one chart bucket. Records use the native bucket filter;
// units open the grouped query restricted to that group's rows. A native
// cycle slot opens the records of its whole hours or days in the range, as
// ranges with one OR per gap; without a start, or with more occurrences than
// the predicate budget holds, it has none (null), nor has a folded slot.
// A query past the predicate budget in any where list has none: a link
// would drop its last predicates, the bucket's own keys, and open more.
export function analysisInspectQuery(analysis: AnalysisQuery, bucket: AggregationBucket, clause: AggregationClause, schema: FieldSchema, view: AnalysisView): QueryState | null {
  if (view === "cycle" && !analysis.native) return null;
  const { aggregations: _, ...units } = analysis.query;
  const opened = inspectBucketQuery(analysis.grouped ? units : { ...EMPTY_QUERY, where: analysis.query.where }, bucket, clause, schema);
  return opened && withinBudget(opened) ? opened : null;
}
// Whether every where list of a query, its grouping source's included, fits
// Query Table's predicate budget, which links and normalization enforce by
// dropping what is past it.
export const withinBudget = (query: QueryState) => predicateCount(query.where) <= MAX_WHERE_CLAUSES && (!query.grouping || predicateCount(query.grouping.source.where) <= MAX_WHERE_CLAUSES);

// Fold a cycle view's native hourly or daily totals into 24 local hours or 7
// weekdays. Every complete slot in [from, to) is an observation, idle ones
// included as zero for additive measures; an idle ratio has no value.
//
// cycleFrame reads the keys by the catalog's key kind, the same rule as the
// weekday cards' weekdayMetric. Recorded dates fold as dates: only complete
// dates in the viewer's range count (the query starts at a local midnight,
// completeStart, and dates from the viewer's current day on are incomplete).
// A recording Mac in another zone can still return a date before the first
// one; it is left out and reported, never counted as a complete day.
// Instants and timestamps are placed in the viewer's zone.
export function cycleResult(catalog: AnalysisCatalog, input: AnalysisState, result: AggregationResultEntry, bounds: { from?: number; to: number }, zone: string) {
  const state = normalizeAnalysis(catalog, input), measure = catalog.measures[state.metric];
  if (result.error || result.coverage === "partial" || result.hiddenGroups || (result.groupCount ?? 0) > result.buckets.length || result.buckets.some(bucket => bucket.error)) {
    throw new Error(result.error ?? "Complete totals are required for an hour or weekday cycle. Choose a shorter range.");
  }
  const hour = state.cycle === "hour", kind = catalog.cycle![state.cycle]!.kind;
  const frame = cycleFrame(kind, result.buckets.map(bucket => bucket.keys[0]), bounds, zone);
  const values = new Map(result.buckets.map(bucket => [frame.time(bucket.keys[0]), bucket]));
  // A missing key would silently become an idle slot.
  if ([...values.keys()].some(key => !Number.isFinite(key))) throw new Error(`Some totals have no ${hour ? "hour" : "day"}, so they cannot be placed in the cycle.`);
  const { from, to } = frame;
  const earlier = kind === "recorded-date" ? result.buckets.filter(bucket => frame.time(bucket.keys[0]) < from) : [];
  const zero = idleIsZero(aggregate(measure)) ? 0 : null;
  let observations = 0;
  const samples = Number.isFinite(from) ? cycleSamples(state.cycle, from, to, frame.zone, start => {
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
  const parts = [measure.note, ...catalog.rangeNote ? [catalog.rangeNote] : [], ...catalog.request?.note ? [catalog.request.note] : []];
  const idle = idleIsZero(aggregate(measure)) ? " as zero" : ", which have no value", kind = state.view === "cycle" ? catalog.cycle![state.cycle]!.kind : undefined;
  if (state.view === "cycle") {
    const per = state.cycle === "hour" ? "hour" : "day";
    parts.push(kind === "timestamp"
      ? `One observation is one whole ${per} in your time zone in the range, idle ${per}s included${idle}. ${per === "hour" ? "Partial first and last hours, and the current hour," : "A partial first day, and the current day,"} are left out. A slot opens the records of its whole ${per}s; in a range without a start, or with more than about 48 of them, it has no single drill-down. The grouped query lists the ${per === "hour" ? "hourly" : "daily"} totals.`
      : state.cycle === "hour" ? `One observation is one complete clock hour in the range, placed at its hour in your time zone, idle hours included${idle}. Partial first and last hours are left out. An hour bar spans many dates, so it has no single drill-down; hourly usage cannot yet open as a grouped query, so the link opens the records.`
      : `One observation is one complete recorded day in the range, idle days included${idle}. Days are the dates the recording Mac assigned in its own time zone. The range starts at your next local midnight; the current day is left out. When the recording Mac's time zone differs from yours, a date just before your first date can be returned with part of its records; it is left out and reported. Select the grouped query for the daily totals; a weekday bar spans many dates, so it has no single drill-down.`);
  }
  else if (unit) parts.push(`${unit.note} One observation is one ${unit.label.toLowerCase()}: its ${measure.label.toLowerCase()} over all its matching records in the range.`);
  else parts.push(`One observation is one ${catalog.records.label.toLowerCase()}. ${catalog.records.note}`);
  // A breakdown's implied population follows the measure's note.
  const breakdown = state.split === noSplit ? undefined : catalog.breakdowns[state.split];
  if (breakdown?.note && state.view !== "cycle") parts.splice(1, 0, breakdown.note);
  if (state.view === "compare" && unit) parts.push(state.reduce === "count" ? `Bars count ${subject} in each group.`
    : state.reduce === "total" ? measure.ratio ? `Bars pool the ${subject} in each group: the ratio of their summed parts, as if they were one ${unit.label.toLowerCase()}.` : `Bars add up the ${subject} in each group.`
    : `Bars show the ${state.reduce === "max" ? "largest" : state.reduce} ${unit.label.toLowerCase()} in each group${state.reduce === "mean" && !measure.additive ? `, every ${unit.label.toLowerCase()} weighted equally` : ""}.`);
  if (state.view === "cycle" && state.reduce === "mean" && !measure.additive) parts.push(`The mean weights every ${state.cycle === "hour" ? "hour" : "day"} equally${measure.ratio ? ", not by tokens" : ""}.`);
  if (state.view === "cycle" && state.reduce === "total") parts.push(`Totals add up every ${state.cycle === "hour" ? "hour" : "day"} that falls on each bar.`);
  const narrowed = state.view === "scatter" ? [[measure, catalog.measures[state.metricY]], [catalog.measures[state.metricY], measure]].filter(([own, other]) => own.where?.length && !other.where?.length).map(([own, other]) => `${other.label} counts only ${own.label.toLowerCase()}'s records here.`) : [];
  if (state.view === "scatter") parts.push(`Each point is one ${unit!.label.toLowerCase()}; both measures use the records matching both measures' populations.${narrowed.length ? ` ${narrowed.join(" ")}` : ""} Up to ${analysisLimits.points.toLocaleString("en-US")} ${subject} with the largest ${measure.label.toLowerCase()} are drawn; the grouped query lists them all.`);
  if (state.view === "distribution" || state.view === "compare" && unit && state.reduce !== "count") parts.push(`${unit ? subject[0].toUpperCase() + subject.slice(1) : catalog.records.plural} without a ${measure.label.toLowerCase()} value are left out.`);
  if (thresholded(state)) parts.push(`Groups with fewer than ${state.minSamples.toLocaleString("en-US")} ${state.view === "distribution" ? "plotted " : ""}${sampleSubject(catalog, state)} are hidden.`);
  if (state.compareAs === "share" && state.view === "compare") parts.push("Shares divide each group by the total of all groups, including those folded into Other.");
  return parts.join(" ");
}

// What a minimum counts: units, records, or the catalog's sample measure.
export const sampleSubject = (catalog: AnalysisCatalog, state: ResolvedAnalysis) => state.unit !== records ? catalog.units[state.unit].plural.toLowerCase()
  : catalog.samples && state.view === "compare" ? catalog.samples.unit : catalog.records.plural.toLowerCase();

export function analysisEmptyLabel(catalog: AnalysisCatalog, input: AnalysisState): string {
  const state = normalizeAnalysis(catalog, input);
  const subject = state.unit === records ? catalog.records.plural.toLowerCase() : catalog.units[state.unit].plural.toLowerCase();
  return thresholded(state) ? `No groups with at least ${state.minSamples.toLocaleString("en-US")} ${sampleSubject(catalog, state)}` : `No matching ${subject}`;
}
