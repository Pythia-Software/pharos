import {sliceSelectionActive} from './chart-selection';
import { EMPTY_QUERY, type AggregationClause, type AggregationResultEntry, type QueryState, type WhereTerm } from "@pythia-software/query-table-core";
import { earliestKey, nextPeriod, parseDay, periodBuckets, periodKey, periodLabel, periodStart, shiftPeriod } from "./chart-period";
import { chartSpan, smoothingWhere, timeBounds, widenStart, withEnd, withStart } from "./chart-range";
import { otherSeriesColor } from "./chart-colors";
import { chartBucketsWithHistory, cumulativeBuckets, foldSeries, rollingBuckets, shareBuckets, withoutZeroes, type ChartBucket, type ChartSeries } from "./chart-series";
import { drawsLevels, isTrendView, heatmapAxis, trendDrawing, aggregate, completeStart, partClause, predicateCount, selectionWhere, periodField, measureExpression, normalizeAnalysis, percentileFraction, trendKind, unique, type AnalysisCatalog, type PercentileLevel, type AnalysisState, type DerivedTotals, type ResolvedAnalysis } from "./dashboard-analysis-model";

// Over time, driven by a catalog: any measure, over its own population, by
// any breakdown, a component breakdown or none. The query asks for interval
// amounts per calendar period; the series builder smooths, accumulates,
// pools, shares and folds them in that order, so the renderer only draws.

export const trendPeriodCount = (state: Pick<ResolvedAnalysis, "period" | "hours" | "days">) => state.period === "hours" ? state.hours : state.period === "days" ? state.days : 1;
const localZone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";

// Whether this trend groups the catalog's timestamp: hours, where the
// dataset records no hour of its own.
export const timestampHours = (catalog: AnalysisCatalog, state: Pick<ResolvedAnalysis, "period">) => periodField(state.period) === "hour" && !catalog.periods.hour && Boolean(catalog.timestamp);

export type TrendQuery = {
  // The executed query: interval amounts by period and series.
  query: QueryState;
  // What "View supporting query" opens: the dashboard's filters and the
  // measure's population, with the same interval clauses.
  supporting: QueryState;
  // The chart's range before smoothing widens it, and after. Both are the
  // population: a legend selection is the state's own, so every series
  // stays in the key.
  range: WhereTerm[]; requested: WhereTerm[];
  // The measure's and breakdown's implied populations, and for a distinct
  // count the records holding a value: what a drill-down adds to open only
  // the records behind a point.
  population: WhereTerm[]; records: WhereTerm[];
  // A single-field breakdown's field: a series' drill-down filters on it.
  slice?: string;
  // The norm's points: the periods from normFrom up to, not including, the
  // latest one (normTo).
  normFrom?: Date; normTo?: Date;
};

// Clauses: one per series kind, grouped by [period, ...breakdown fields]; a
// ratio's numerator and denominator as separate sums; one sum per component.
export function trendQuery(catalog: AnalysisCatalog, input: AnalysisState, where: WhereTerm[], { now = Date.now(), zone = localZone() }: { now?: number; zone?: string } = {}): TrendQuery {
  const state = normalizeAnalysis(catalog, { ...input, view: isTrendView(input.view) ? input.view : "trend" }), measure = catalog.measures[state.metric];
  const breakdown = catalog.breakdowns[state.split], components = catalog.components?.[state.split];
  // Days, weeks and months are recorded calendar fields. Hours of a
  // timestamped dataset group the timestamp by UTC hour, as the hour cycle
  // does, and the chart places each at the viewer's hour.
  const interval = periodField(state.period), hourly = timestampHours(catalog, state), timestamped = !catalog.periods[interval] && Boolean(catalog.timestamp);
  const count = trendPeriodCount(state), field = timestamped ? catalog.timestamp! : catalog.periods[interval]!, cumulative = state.display === "cumulative";
  const dateGrouping = timestamped ? { dateGrouping: { field, interval, timeZone: hourly ? "UTC" : zone, includeEmpty: false, range: "query" as const } } : {};
  const slice = breakdown?.fields.length === 1 ? breakdown.fields[0] : undefined;
  const range = state.completePeriods ? completeStart(where, catalog.time, zone, now, periodField(state.period)) : where;
  const run = (terms: WhereTerm[]) => catalog.executable ? catalog.executable(terms, now) : terms;
  // The norm averages a fixed number of points before the latest, whatever
  // the range, so the query reaches back for them (and their windows).
  let normFrom: Date | undefined, normTo: Date | undefined;
  if (drawsLevels(state)) {
    const bounds = timeBounds(range, catalog.time, now), limit = Math.min(bounds.to?.getTime() ?? now, now);
    normTo = periodStart(new Date(limit - 1), state.period, count);
    const end = new Date(normTo); nextPeriod(end, state.period, count);
    if (state.completePeriods && end.getTime() > limit) normTo = shiftPeriod(normTo, state.period, -1, count) ?? normTo;
    normFrom = shiftPeriod(normTo, state.period, -state.normPeriods, count);
  }
  const requested = smoothingWhere(normFrom ? widenStart(range, catalog.time, normFrom, now) : range, catalog.time, state.period, cumulative ? 0 : state.rollingPeriods, count, now);
  const population = unique([...measure.where ?? [], ...breakdown?.where ?? []]);
  const groupBy = [field, ...breakdown?.fields ?? []];
  const derived = components?.derived;
  const clauses: AggregationClause[] = (derived ? derived.inputs.fields.map(input => ({ id: `trend:input:${input}`, op: "sum" as const, field: input, groupBy: [field, ...derived.inputs.groupBy], label: input }))
    : components ? components.parts.map(part => ({ id: `trend:${part.key}`, op: "sum" as const, field: part.field, groupBy: [field], label: part.label }))
    : measure.ratio ? [{ id: "trend", ...partClause(measure.ratio[0]), groupBy, label: `${measure.label}: numerator` }, { id: "trend:denominator", ...partClause(measure.ratio[1]), groupBy, label: `${measure.label}: denominator` }]
    : [{ id: "trend", ...aggregate(measure), groupBy, label: measure.label }]).map(clause => ({ ...clause, ...dateGrouping }));
  if(state.share&&measure.populationShare)clauses.push({id:'trend:population',...aggregate(measure),groupBy:[field],label:'All messages in interval',...dateGrouping});
  const supporting = measure.ratio ? [{ id: "trend:ratio", op: "sum" as const, expression: measureExpression(measure), groupBy, label: measure.label, ...dateGrouping }, ...clauses] : clauses.filter(clause=>clause.id!=="trend:population");
  // The supporting query covers what is drawn: with whole periods, from the
  // rounded start to the end of the last whole period, and the legend
  // selection as a filter on the breakdown's fields, where the predicate
  // budget allows it with room for whole periods' two bounds.
  const chosen = breakdown && state.selection && sliceSelectionActive(state.selection) ? selectionWhere(breakdown.fields, state.selection, predicateCount(run(unique([...where, ...population]))) + 2) ?? [] : [];
  let shown = [...where, ...chosen];
  if (state.completePeriods) {
    // From the first drawn bucket: a custom multi-day or multi-hour bucket
    // starting before the rounded start is not drawn.
    const bounds = timeBounds(range, catalog.time, now), end = periodStart(new Date(Math.min(bounds.to?.getTime() ?? now, now)), state.period, count);
    let first = bounds.from && periodStart(bounds.from, state.period, count);
    if (first && bounds.from && first < bounds.from) { first = new Date(first); nextPeriod(first, state.period, count); }
    shown = withEnd(first ? withStart(shown, catalog.time, first) : shown, catalog.time, end);
  }
  return {
    query: { ...EMPTY_QUERY, where: run(unique([...requested, ...population])), aggregations: clauses },
    // An hourly timestamp trend's clauses group by UTC hour, which the grouped
    // query keys as a calendar group key (groupedDashboardQuery).
    supporting: { ...EMPTY_QUERY, where: run(unique([...shown, ...population])), aggregations: supporting },
    range, requested, population, slice, normFrom, normTo,
    records: unique([...population, ...measure.drill ?? [], ...trendKind(measure) === "count" && measure.field ? [{ field: measure.field, op: "is_not_null" as const, value: "" }, { field: measure.field, op: "!=" as const, value: "" }] : []]),
  };
}

export type TrendSeries = {
  series: ChartSeries[]; buckets: ChartBucket[];
  // Series keys the legend selection draws, or hides when excluded.
  selected: string[]; excluded: boolean; allHidden?: boolean;
  // Each level the state asks for (its percentiles: the mean, or a
  // percentile) of the drawn values at the normPeriods points before the
  // latest one, and how many of those points had a value.
  references?: Array<{ level: PercentileLevel; value: number }>; normPoints?: number;
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
export function trendSeries(catalog: AnalysisCatalog, input: AnalysisState, trend: TrendQuery, results: AggregationResultEntry[], { now = Date.now(), label = (_field, value) => String(value) }: {
  now?: number; label?: (field: string, value: unknown) => string;
}): TrendSeries {
  const state = normalizeAnalysis(catalog, { ...input, view: isTrendView(input.view) ? input.view : "trend" }), measure = catalog.measures[state.metric], kind = trendKind(measure);
  const breakdown = catalog.breakdowns[state.split], components = catalog.components?.[state.split], drawing = trendDrawing(catalog, state);
  const count = trendPeriodCount(state), cumulative = state.display === "cumulative", grid = { period: state.period, count };
  const additive = kind === "additive", empty: TrendSeries = { series: [], buckets: [], selected: [], excluded: false, lines: drawing === "trend_line", additive };
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
  const historyStart = !cumulative && (state.rollingPeriods > 1 || drawsLevels(state)) ? timeBounds(trend.requested, catalog.time, now).from ?? earliest : undefined;
  const dates = keys.map(key => parseDay(String(key)));
  // Whole periods only: a period starting before the rounded range start
  // is history, which smoothing may read but nothing draws. The norm's
  // earlier points are history too, every period of them, idle ones included.
  const visible = periodStart(start, state.period, count);
  const layout = () => (drawsLevels(state) && historyStart ? periodBuckets(state.period, historyStart, end, count).map(bucket => bucket.date < visible ? { ...bucket, history: true } : bucket)
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
  const valueLabel = (field: string, value: unknown) => value === null || value === undefined || value === "" ? breakdown?.empty ?? "Unknown" : breakdown?.values?.[String(value)] ?? catalog.labels?.[field]?.(String(value)) ?? label(field, value);
  const seriesLabel = (text: string) => breakdown!.fields.length === 1 ? valueLabel(breakdown!.fields[0], text) : (JSON.parse(text) as unknown[]).map((value, index) => valueLabel(breakdown!.fields[index], value)).join(" · ");
  const chosen = state.selection ?? { values: [], excluded: false };
  const picked = chosen.values;

  let series: ChartSeries[];
  if (components?.derived) {
    // Each period's input totals, by the inputs' own grouping, become that
    // period's parts before any smoothing, accumulation or share.
    const { inputs, derive } = components.derived, totals = new Map<string, Map<string, DerivedTotals[number]>>();
    for (const input of inputs.fields) for (const bucket of result(`trend:input:${input}`)) {
      const key = plotted(bucket.keys[0]), group = JSON.stringify(bucket.keys.slice(1));
      const period = totals.get(key) ?? new Map(); totals.set(key, period);
      const entry = period.get(group) ?? { keys: bucket.keys.slice(1), values: {} }; period.set(group, entry);
      entry.values[input] = (entry.values[input] ?? 0) + (Number(bucket.value) || 0);
    }
    for (const bucket of amounts) {
      const period = totals.get(bucket.key);
      if (period) for (const [part, value] of Object.entries(derive([...period.values()], state.metric))) bucket.values[part] = (bucket.values[part] ?? 0) + value;
    }
    series = components.parts.filter(part => !part.optional || amounts.some(bucket => bucket.values[part.key])).map(part => ({ key: part.key, label: part.label, color: part.color, value: part.key }));
  } else if (components) {
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
    // All-series catalogs include history-only labels too: smoothing can
    // carry those values into the visible window. Saved numeric caps do not
    // hide ontology branches on a dashboard that always shows its full key.
    const seriesRows = catalog.trend.allSeries ? weights : weights.filter(([key]) => drawnKeys.has(key));
    series = foldSeries(seriesRows, byKey(weighted), seriesLabel, breakdown.order, picked, catalog.trend.allSeries ? Infinity : state.seriesLimit, breakdown.fields.length === 1 ? breakdown.fields[0] : state.split);
    // Series are keyed by their raw values; a label two of them share (Agent
    // Time's command:foo and tool:foo) names the value too.
    series = series.map(item => item.value !== undefined && series.some(other => other !== item && other.label === item.label) ? { ...item, label: `${item.label} (${item.value})` } : item);
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
    // foldSeries has assigned the rows it ranked: with every series, history
    // rows too, which must not count twice in a smoothing window.
    if (!catalog.trend.allSeries) assign(weights.filter(([key]) => !drawnKeys.has(key)), weighted);
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
    if(state.share&&measure.populationShare){
      const population=layout();for(const [key,,value] of rows('trend:population')){const bucket=population.find(b=>b.key===key);if(bucket)bucket.values.total=value;}
      const totals=byKey(window(population,state.rollingOp));
      buckets=buckets.map(bucket=>{const total=totals.get(bucket.key)?.values.total??0;return {...bucket,values:Object.fromEntries(Object.entries(bucket.values).flatMap(([id,value])=>total>0?[[id,value/total]]:[]))};});
    }else if (state.share) buckets = shareBuckets(buckets);
  }
  let references: TrendSeries["references"], normPoints: number | undefined;
  const nonzero = state.nonzero && !state.share;
  const selected = series.filter(item => item.value !== undefined && picked.includes(item.value)).map(item => item.key);
  const drawn = chosen.allHidden ? [] : selected.length ? series.filter(item => chosen.excluded ? !selected.includes(item.key) : selected.includes(item.key)) : series;
  // Additive series add up to the drawn total; any other measure has levels
  // only when one series is drawn, never the first of several.
  if (drawsLevels(state) && trend.normFrom && trend.normTo && (additive || drawn.length === 1)) {
    const from = trend.normFrom.getTime(), to = trend.normTo.getTime();
    const points = buckets.filter(bucket => bucket.date.getTime() >= from && bucket.date.getTime() < to).map(bucket => additive ? drawn.reduce((sum, item) => sum + (bucket.values[item.key] ?? 0), 0)
      : kind === "ratio" ? bucket.values[drawn[0]?.key] : bucket.values[drawn[0]?.key] ?? 0).filter((value): value is number => typeof value === "number" && !(nonzero && value === 0));
    normPoints = points.length;
    // A state with only the older norm draws its mean.
    if (points.length) references = (state.percentiles?.length ? state.percentiles : ["mean" as const]).map(level => ({ level, value: levelOf(points, level) }));
  }
  buckets = buckets.filter(bucket => !bucket.history);
  if (state.completePeriods) {
    // The current period, and one reaching past the range's end, are partial.
    const limit = Math.min(bounds.to?.getTime() ?? now, now);
    buckets = buckets.filter(bucket => { const next = new Date(bucket.date); nextPeriod(next, state.period, count); return next.getTime() <= limit; });
  }
  if (nonzero) buckets = withoutZeroes(buckets, drawn.map(item => item.key));

  return { series, buckets, selected, excluded: chosen.excluded, ...(chosen.allHidden?{allHidden:true}:{}), references, normPoints, cumulativeStart: bounds.from ?? earliest, lines: drawing === "trend_line" || drawing !== "trend_bar" && !additive && series.length > 1, additive };
}

// A level of some values: their mean, or a percentile interpolated between
// the two nearest values.
export function levelOf(values: number[], level: PercentileLevel): number {
  const fraction = percentileFraction(level);
  if (fraction === undefined) return values.reduce((sum, value) => sum + value, 0) / values.length;
  const sorted = [...values].sort((a, b) => a - b), at = (sorted.length - 1) * fraction, low = Math.floor(at);
  return low + 1 < sorted.length ? sorted[low] + (at - low) * (sorted[low + 1] - sorted[low]) : sorted[low];
}

// The chart's title, in the dashboards' plain style.
export function trendTitle(catalog: AnalysisCatalog, input: AnalysisState): string {
  const state = normalizeAnalysis(catalog, { ...input, view: isTrendView(input.view) ? input.view : "trend" }), measure = catalog.measures[state.metric];
  const split = catalog.breakdowns[state.split]?.label ?? catalog.components?.[state.split]?.label;
  // A heatmap counts the records, or totals the measure, by the value its
  // bins hold: "Sessions per day by messages per session".
  const heatmap = state.display === "heatmap" ? heatmapAxis(catalog, state) : undefined;
  if (heatmap) return `${heatmap.counted ? catalog.records.plural : measure.label} per ${periodLabel(state.period, trendPeriodCount(state))} by ${heatmap.bins.label.toLowerCase()}`;
  const cumulative = state.display === "cumulative", name = state.share ? `Share of ${measure.label.replace(/ \(USD\)$/, "").replace(/^(?!API)\w/, letter => letter.toLowerCase())}` : measure.label;
  return `${cumulative ? "Cumulative " : ""}${cumulative ? name[0].toLowerCase() + name.slice(1) : name}${cumulative ? "" : ` per ${periodLabel(state.period, trendPeriodCount(state))}`}${split ? ` by ${split.toLowerCase()}` : ""}`;
}
