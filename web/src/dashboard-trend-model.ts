import { EMPTY_QUERY, type AggregationClause, type AggregationResultEntry, type QueryState, type WhereTerm } from "@pythia-software/query-table-core";
import { earliestKey, nextPeriod, parseDay, periodBuckets, periodKey, periodLabel, periodStart, shiftPeriod } from "./chart-period";
import { chartSpan, smoothingWhere, timeBounds, widenStart, withEnd, withStart } from "./chart-range";
import { otherSeriesColor } from "./chart-colors";
import { chartBucketsWithHistory, cumulativeBuckets, foldSeries, rollingBuckets, shareBuckets, type ChartBucket, type ChartSeries } from "./chart-series";
import { isSliceTerm, readSlice } from "./chart-selection";
import { aggregate, completeStart, periodField, measureExpression, noSplit, normalizeAnalysis, ratioPart, rollingSumUnit, trendKind, unique, type AnalysisCatalog, type AnalysisState, type ResolvedAnalysis } from "./dashboard-analysis-model";

// Over time, driven by a catalog: any measure, over its own population, by
// any breakdown, a component breakdown or none. The query asks for interval
// amounts per calendar period; the series builder smooths, accumulates,
// pools, shares and folds them in that order, so the renderer only draws.

export const trendPeriodCount = (state: Pick<ResolvedAnalysis, "period" | "hours" | "days">) => state.period === "hours" ? state.hours : state.period === "days" ? state.days : 1;
const localZone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

export type TrendQuery = {
  // The executed query: interval amounts by period and series.
  query: QueryState;
  // What "View supporting query" opens: the dashboard's filters and the
  // measure's population, with the same interval clauses.
  supporting: QueryState;
  // The chart's range before smoothing widens it, and after. A legend
  // selection on a breakdown field is left out of both, so its alternatives
  // stay in the key.
  range: WhereTerm[]; requested: WhereTerm[];
  // The measure's and breakdown's implied populations, and for a distinct
  // count the records holding a value: what a drill-down adds to open only
  // the records behind a point.
  population: WhereTerm[]; records: WhereTerm[];
  // The breakdown field whose legend selection filters the dashboard.
  slice?: string;
  // The norm's points: the periods from normFrom up to, not including, the
  // latest one (normTo).
  normFrom?: Date; normTo?: Date;
};

// Clauses: one per series kind, grouped by [period, ...breakdown fields]; a
// ratio's numerator and denominator as separate sums; one sum per component.
export function trendQuery(catalog: AnalysisCatalog, input: AnalysisState, where: WhereTerm[], { now = Date.now(), zone = localZone() }: { now?: number; zone?: string } = {}): TrendQuery {
  const state = normalizeAnalysis(catalog, { ...input, view: "trend" }), measure = catalog.measures[state.metric];
  const breakdown = catalog.breakdowns[state.split], components = catalog.components?.[state.split];
  const count = trendPeriodCount(state), field = catalog.periods[periodField(state.period)]!, cumulative = state.display === "cumulative";
  const slice = breakdown?.fields.length === 1 ? breakdown.fields[0] : undefined;
  const chartWhere = slice ? where.filter(term => !isSliceTerm(term, slice)) : where;
  const range = state.completePeriods ? completeStart(chartWhere, catalog.time, zone, now, periodField(state.period)) : chartWhere;
  // The norm averages a fixed number of points before the latest, whatever
  // the range, so the query reaches back for them (and their windows).
  let normFrom: Date | undefined, normTo: Date | undefined;
  if (state.norm) {
    const bounds = timeBounds(range, catalog.time, now), limit = Math.min(bounds.to?.getTime() ?? now, now);
    normTo = periodStart(new Date(limit - 1), state.period, count);
    const end = new Date(normTo); nextPeriod(end, state.period, count);
    if (state.completePeriods && end.getTime() > limit) normTo = shiftPeriod(normTo, state.period, -1, count) ?? normTo;
    normFrom = shiftPeriod(normTo, state.period, -state.normPeriods, count);
  }
  const requested = smoothingWhere(normFrom ? widenStart(range, catalog.time, normFrom, now) : range, catalog.time, state.period, cumulative ? 0 : state.rollingPeriods, count, now);
  const population = unique([...measure.where ?? [], ...breakdown?.where ?? []]);
  const groupBy = [field, ...breakdown?.fields ?? []];
  const clauses: AggregationClause[] = components ? components.parts.map(part => ({ id: `trend:${part.key}`, op: "sum", field: part.field, groupBy: [field], label: part.label }))
    : measure.ratio ? [{ id: "trend", ...ratioPart(measure.ratio[0]), groupBy, label: `${measure.label}: numerator` }, { id: "trend:denominator", ...ratioPart(measure.ratio[1]), groupBy, label: `${measure.label}: denominator` }]
    : [{ id: "trend", ...aggregate(measure), groupBy, label: measure.label }];
  const supporting = measure.ratio ? [{ id: "trend:ratio", op: "sum" as const, expression: measureExpression(measure), groupBy, label: measure.label }, ...clauses] : clauses;
  // The supporting query covers what is drawn: with whole periods, from the
  // rounded start to the end of the last whole period. It keeps the user's
  // filters, a legend selection included.
  let shown = where;
  if (state.completePeriods) {
    // From the first drawn bucket: a custom multi-day or multi-hour bucket
    // starting before the rounded start is not drawn.
    const bounds = timeBounds(range, catalog.time, now), end = periodStart(new Date(Math.min(bounds.to?.getTime() ?? now, now)), state.period, count);
    let first = bounds.from && periodStart(bounds.from, state.period, count);
    if (first && bounds.from && first < bounds.from) { first = new Date(first); nextPeriod(first, state.period, count); }
    shown = withEnd(first ? withStart(where, catalog.time, first) : where, catalog.time, end);
  }
  return {
    query: { ...EMPTY_QUERY, where: unique([...requested, ...population]), aggregations: clauses },
    supporting: { ...EMPTY_QUERY, where: unique([...shown, ...population]), aggregations: supporting },
    range, requested, population, slice, normFrom, normTo,
    records: unique([...population, ...trendKind(measure) === "count" && measure.field ? [{ field: measure.field, op: "is_not_null" as const, value: "" }, { field: measure.field, op: "!=" as const, value: "" }] : []]),
  };
}

export type TrendSeries = {
  series: ChartSeries[]; buckets: ChartBucket[];
  // Series keys the legend selection draws, or hides when excluded.
  selected: string[]; excluded: boolean;
  // The mean of the drawn values at the normPeriods points before the latest
  // one, and how many of those points had a value.
  norm?: number; normPoints?: number;
  cumulativeStart?: Date;
  // Non-additive series do not stack; they draw as lines. Their tooltip has
  // no total.
  lines: boolean; additive: boolean;
  error?: string;
};

type Rows = Array<[bucket: string, name: string, value: number]>;
const total = "total";

// Interval amounts in, drawable series out. Order matters and follows the
// cards: smooth (or accumulate) amounts first, then divide a ratio's pooled
// parts or take shares, so a busy day keeps its weight.
export function trendSeries(catalog: AnalysisCatalog, input: AnalysisState, trend: TrendQuery, results: AggregationResultEntry[], { where, now = Date.now(), label = (_field, value) => String(value) }: {
  where: WhereTerm[]; now?: number; label?: (field: string, value: unknown) => string;
}): TrendSeries {
  const state = normalizeAnalysis(catalog, { ...input, view: "trend" }), measure = catalog.measures[state.metric], kind = trendKind(measure);
  const breakdown = catalog.breakdowns[state.split], components = catalog.components?.[state.split];
  const count = trendPeriodCount(state), cumulative = state.display === "cumulative", grid = { period: state.period, count };
  const additive = kind === "additive", empty: TrendSeries = { series: [], buckets: [], selected: [], excluded: false, lines: false, additive };
  // No results yet: the query is loading, which is not an error.
  if (!results.length) return empty;
  // A missing, partial or truncated clause would read as idle periods.
  for (const clause of trend.query.aggregations ?? []) {
    const found = results.find(item => item.id === clause.id);
    if (found?.error) return { ...empty, error: found.error };
    if (!found || found.coverage === "partial" || found.groupCount !== undefined && found.groupCount > found.buckets.length || (found.hiddenGroups ?? 0) > 0 || found.buckets.some(bucket => bucket.error)) {
      return { ...empty, error: found?.buckets.find(bucket => bucket.error)?.error ?? "The trend needs complete totals for every period. Choose a shorter range or a longer time bucket." };
    }
  }
  const result = (id: string) => results.find(item => item.id === id)?.buckets ?? [];

  const bounds = timeBounds(trend.range, catalog.time, now);
  const keys = results.flatMap(item => item.buckets.map(bucket => bucket.keys[0]));
  const earliest = earliestKey(keys), [start, end] = chartSpan(bounds, earliest, now);
  const historyStart = !cumulative && (state.rollingPeriods > 1 || state.norm) ? timeBounds(trend.requested, catalog.time, now).from ?? earliest : undefined;
  const dates = keys.map(key => parseDay(String(key)));
  // Whole periods only: a period starting before the rounded range start
  // is history, which smoothing may read but nothing draws. The norm's
  // earlier points are history too, every period of them, idle ones included.
  const visible = periodStart(start, state.period, count);
  const layout = () => (state.norm && historyStart ? periodBuckets(state.period, historyStart, end, count).map(bucket => bucket.date < visible ? { ...bucket, history: true } : bucket)
    : chartBucketsWithHistory(state.period, start, end, count, dates, historyStart))
    .map(bucket => state.completePeriods && bounds.from && bucket.date < bounds.from ? { ...bucket, history: true } : bucket);
  const amounts = layout(), denominators = kind === "ratio" ? layout() : [];
  // A cumulative plot capped at 5,000 periods opens on the earlier balance.
  // A partial first period is history, so it adds nothing.
  const first = amounts[0]?.key;
  const plotted = (value: unknown) => { const key = periodKey(parseDay(String(value)), state.period, count); return cumulative && first && key < first ? first : key; };
  const name = (bucketKeys: unknown[]) => !breakdown ? total : breakdown.fields.length === 1 ? String(bucketKeys[0] ?? "") : JSON.stringify(bucketKeys);
  const rows = (id: string): Rows => result(id).map(bucket => [plotted(bucket.keys[0]), name(bucket.keys.slice(1)), Number(bucket.value) || 0]);
  const byKey = (buckets: ChartBucket[]) => new Map(buckets.map(bucket => [bucket.key, bucket]));
  const valueLabel = (field: string, value: unknown) => value === null || value === undefined || value === "" ? breakdown?.empty ?? "Unknown" : breakdown?.values?.[String(value)] ?? label(field, value);
  const seriesLabel = (text: string) => breakdown!.fields.length === 1 ? valueLabel(breakdown!.fields[0], text) : (JSON.parse(text) as unknown[]).map((value, index) => valueLabel(breakdown!.fields[index], value)).join(" · ");
  const chosen = trend.slice ? readSlice(where, trend.slice) : state.selection ?? { values: [], excluded: false };
  const picked = chosen.values.map(value => value ?? "");

  let series: ChartSeries[];
  if (components) {
    const targets = byKey(amounts);
    for (const part of components.parts) for (const [key, , value] of rows(`trend:${part.key}`)) {
      const target = targets.get(key);
      if (target) target.values[part.key] = (target.values[part.key] ?? 0) + value;
    }
    series = components.parts.filter(part => !part.optional || amounts.some(bucket => bucket.values[part.key])).map(part => ({ key: part.key, label: part.label, color: part.color, value: part.key }));
  } else if (!breakdown) {
    const targets = byKey(amounts), divisors = byKey(denominators);
    for (const [key, , value] of rows("trend")) { const target = targets.get(key); if (target) target.values[total] = (target.values[total] ?? 0) + value; }
    for (const [key, , value] of kind === "ratio" ? rows("trend:denominator") : []) { const target = divisors.get(key); if (target) target.values[total] = (target.values[total] ?? 0) + value; }
    series = [{ key: total, label: measure.label, color: "var(--heat-1)" }];
  } else {
    // A ratio's groups rank by their denominator, the volume behind them;
    // its numerators then follow the same series.
    // Series are ranked by the drawn periods only; smoothing and norm
    // history then follow the same series, so turning the norm on or
    // widening a window never changes which groups are shown.
    const weights = kind === "ratio" ? rows("trend:denominator") : rows("trend"), weighted = kind === "ratio" ? denominators : amounts;
    // With whole periods, a partial last period is not drawn either.
    const limit = Math.min(bounds.to?.getTime() ?? now, now);
    const drawnKeys = new Set(weighted.filter(bucket => !bucket.history && (!state.completePeriods || (() => { const next = new Date(bucket.date); nextPeriod(next, state.period, count); return next.getTime() <= limit; })())).map(bucket => bucket.key));
    series = foldSeries(weights.filter(([key]) => drawnKeys.has(key)), byKey(weighted), seriesLabel, breakdown.order, picked, state.seriesLimit, breakdown.fields.length === 1 ? breakdown.fields[0] : state.split);
    const shown = new Set(series.flatMap(item => item.value === undefined ? [] : [item.value]));
    const assign = (entries: Rows, buckets: ChartBucket[]) => {
      const targets = byKey(buckets);
      for (const [key, text, value] of entries) {
        const target = targets.get(key), item = shown.has(text) ? `s:${text}` : "other";
        if (!target) continue;
        target.values[item] = (target.values[item] ?? 0) + value;
        if (item !== "other" || value <= 0) continue;
        const parts = ((target.parts ??= {}).other ??= []), part = parts.find(([name]) => name === seriesLabel(text));
        if (part) part[1] += value; else parts.push([seriesLabel(text), value]);
      }
    };
    assign(weights.filter(([key]) => !drawnKeys.has(key)), weighted);
    if (kind === "ratio") assign(rows("trend"), amounts);
    // A group outside the chosen series that only history holds still adds
    // to drawn windows, shares and the norm, so Other lists it.
    const history = new Set(weighted.filter(bucket => bucket.history).map(bucket => bucket.key));
    const unseen = [...new Set(weights.filter(([key]) => history.has(key)).map(([, text]) => text))].filter(text => !shown.has(text));
    if (kind === "additive" || kind === "ratio") {
      const other = series.find(item => item.key === "other"), folded = [...new Set([...other?.foldedValues ?? [], ...unseen])];
      if (folded.length) series = [...series.filter(item => item.key !== "other"), { key: "other", label: `Other (${folded.length})`, color: otherSeriesColor, hiddenCount: folded.length, foldedValues: folded }];
    }
    if (kind === "count" || kind === "level") {
      // Distinct counts of several groups do not add up, so Other has no
      // honest value; it stays in the key only to show more groups.
      for (const bucket of amounts) { delete bucket.values.other; delete bucket.parts; }
      series = series.map(item => item.hiddenCount ? { ...item, label: `${item.label}, not drawn` } : item);
    }
  }

  const window = (buckets: ChartBucket[], op: "mean" | "sum") => cumulative ? cumulativeBuckets(buckets) : rollingBuckets(buckets, state.rollingPeriods, grid, op);
  let buckets: ChartBucket[];
  if (kind === "ratio") {
    // Pool the parts over each window or running total, then divide. The
    // averaging divisor would cancel, so windows sum.
    const numerators = window(amounts, "sum"), divisors = byKey(window(denominators, "sum"));
    buckets = numerators.map(bucket => {
      const parts = divisors.get(bucket.key)?.values ?? {};
      return { ...bucket, parts: undefined, values: Object.fromEntries(Object.entries(parts).filter(([, value]) => value > 0).map(([key, value]) => [key, (bucket.values[key] ?? 0) / value])) };
    });
  } else {
    buckets = window(amounts, state.rollingOp);
    if (state.share) buckets = shareBuckets(buckets);
  }
  let norm: number | undefined, normPoints: number | undefined;
  const selected = series.filter(item => item.value !== undefined && picked.includes(item.value)).map(item => item.key);
  const drawn = selected.length ? series.filter(item => chosen.excluded ? !selected.includes(item.key) : selected.includes(item.key)) : series;
  if (state.norm && trend.normFrom && trend.normTo) {
    const from = trend.normFrom.getTime(), to = trend.normTo.getTime();
    const points = buckets.filter(bucket => bucket.date.getTime() >= from && bucket.date.getTime() < to).map(bucket => additive ? drawn.reduce((sum, item) => sum + (bucket.values[item.key] ?? 0), 0)
      : kind === "ratio" ? bucket.values[drawn[0]?.key] : bucket.values[drawn[0]?.key] ?? 0).filter((value): value is number => typeof value === "number");
    normPoints = points.length;
    norm = points.length ? points.reduce((sum, value) => sum + value, 0) / points.length : undefined;
  }
  buckets = buckets.filter(bucket => !bucket.history);
  if (state.completePeriods) {
    // The current period, and one reaching past the range's end, are partial.
    const limit = Math.min(bounds.to?.getTime() ?? now, now);
    buckets = buckets.filter(bucket => { const next = new Date(bucket.date); nextPeriod(next, state.period, count); return next.getTime() <= limit; });
  }

  return { series, buckets, selected, excluded: chosen.excluded, norm, normPoints, cumulativeStart: bounds.from ?? earliest, lines: !additive && series.length > 1, additive };
}

// The chart's title, in the dashboards' plain style.
export function trendTitle(catalog: AnalysisCatalog, input: AnalysisState): string {
  const state = normalizeAnalysis(catalog, { ...input, view: "trend" }), measure = catalog.measures[state.metric];
  const split = catalog.breakdowns[state.split]?.label ?? catalog.components?.[state.split]?.label;
  const cumulative = state.display === "cumulative", name = state.share ? `Share of ${measure.label.replace(/ \(USD\)$/, "").replace(/^(?!API)\w/, letter => letter.toLowerCase())}` : measure.label;
  return `${cumulative ? "Cumulative " : ""}${cumulative ? name[0].toLowerCase() + name.slice(1) : name}${cumulative ? "" : ` per ${periodLabel(state.period, trendPeriodCount(state))}`}${split ? ` by ${split.toLowerCase()}` : ""}`;
}

// How each point is computed: the measure, its window, any share, the
// whole-period rule and the norm.
export function trendNote(catalog: AnalysisCatalog, input: AnalysisState): string {
  const state = normalizeAnalysis(catalog, { ...input, view: "trend" }), measure = catalog.measures[state.metric], kind = trendKind(measure);
  const unit = periodLabel(state.period, trendPeriodCount(state)), window = `${state.rollingPeriods} ${state.rollingPeriods === 1 ? unit : `${unit}s`}`;
  const rolling = state.display !== "cumulative" && state.rollingPeriods > 1, parts = [measure.note];
  if (catalog.components?.[state.split]) parts.push(`${catalog.components[state.split].label} splits each ${unit}'s ${measure.label.toLowerCase()} into its parts.`);
  const part = (index: 0 | 1) => { const { op, field } = ratioPart(measure.ratio![index]); return `${op === "sum" ? "summed" : "the count of"} ${field.replaceAll("_", " ")}`; };
  if (kind === "ratio") parts.push(`Each point pools its records: ${part(0)} divided by ${part(1)}, never an average of ratios.`);
  if (kind === "count") parts.push(`Distinct ${measure.unit} are counted in each ${unit}.${state.split === noSplit ? "" : ` One active in several groups counts in each, so groups do not add up, and Other is not drawn.`}`);
  if (state.display === "cumulative") parts.push(kind === "ratio" ? "Each point pools everything from the range start." : "Each point is the running total from the range start.");
  else if (rolling) parts.push(kind === "ratio" ? `Each point pools the parts of its trailing ${window}, including history before the range.`
    : kind === "count" && state.rollingOp === "mean" ? `Each point adds the distinct counts of its trailing ${window} and divides by ${state.rollingPeriods}, idle periods as zero; one active on several ${unit}s counts once in each.`
    : kind === "count" ? `Each point adds the distinct counts of its trailing ${window}: ${rollingSumUnit(measure, state.period)}, so one active on several ${unit}s counts once in each.`
    : state.rollingOp === "sum" ? `Each point is the total of its trailing ${window}, including history before the range.`
    : `Each point is the mean of its trailing ${window}, idle ones as zero, including history before the range.`);
  if (state.share) parts.push(`Shares divide each series by the ${unit}'s total of all series, Other included${rolling ? ", after smoothing" : ""}; a legend selection hides series without changing that total.`);
  if (state.completePeriods) parts.push(`Only whole local ${periodLabel(state.period, 1)}s are drawn: the range starts at the next ${periodLabel(state.period, 1)} boundary, and the current ${periodLabel(state.period, 1)} is left out.`);
  if (state.norm) parts.push(`The norm is the mean of the ${state.normPeriods} ${periodLabel(state.period, trendPeriodCount(state))}${state.normPeriods === 1 ? "" : "s"} before the latest point, over the drawn series, whatever the range.`);
  return parts.join(" ");
}
