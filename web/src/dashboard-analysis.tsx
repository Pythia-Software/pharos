import React, { useEffect, useMemo, useState } from "react";
import type { AggregationClause, AggregationResultEntry, FieldDef, FieldSchema, QueryState, WhereTerm } from "@pythia-software/query-table-core";
import { MetricCard, defaultRenderers } from "@pythia-software/query-table-ui";
import { changeState, selectedShares, analysisEmptyLabel, analysisInspectQuery, analysisNote, analysisQuery, analysisReductions, viewOptions, cycleResult, cycleTotal, noSplit, normalizeAnalysis, records, reductionLabel, sampleThresholds, shareable, splitValid, trendLimits, unitTotal, type AnalysisCatalog, type AnalysisReduce, type AnalysisState, type AnalysisView, type ResolvedAnalysis } from "./dashboard-analysis-model";
import { runAnalysisQuery, type LocalRows } from "./analysis-request";
import { ChartSelect, DisplayControl, PeriodControl, RangeControl, RollingControl, ScaleControl, presetPeriod, timeBounds, withTimeRange, type ChartRange, type FilterChange, type TimeBounds } from "./usage-chart";
import { trendPeriodCount } from "./dashboard-trend-model";
import { UsageDashboardLinks } from "./usage-workspace";
import { usageWorkspaceURL, type UsageDataset } from "./usage-workspace-model";
import { orderedUsageQuery } from "./usage-query-order";
import { toolMetricPresentation } from "./tools-dashboard-model";
import { useOriginMetadata } from "./origin-dimensions";

// Runs an analysis query, again on Refresh, and tags results with their query
// so a stale response never renders under new settings.
export function useAnalysisResult(dataset: string, schema: FieldSchema<any>, query: QueryState, local?: LocalRows) {
  const key = JSON.stringify(query);
  const [state, setState] = useState<{ key: string; results: AggregationResultEntry[]; error: string; loading: boolean }>({ key: "", results: [], error: "", loading: true });
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    const bump = () => setRefresh(value => value + 1);
    window.addEventListener("pharos:usage-refresh", bump);
    return () => window.removeEventListener("pharos:usage-refresh", bump);
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    setState(previous => ({ ...previous, loading: true }));
    runAnalysisQuery(dataset, schema, JSON.parse(key) as QueryState, { signal: controller.signal, local })
      .then(results => setState({ key, results, error: "", loading: false }))
      .catch(failure => { if (!controller.signal.aborted) setState({ key, results: [], error: failure instanceof Error ? failure.message : String(failure), loading: false }); });
    return () => controller.abort();
  }, [dataset, key, refresh, local]);
  return { results: state.key === key ? state.results : [], loading: state.key !== key || state.loading, error: state.key === key ? state.error : "" };
}

export function AnalysisViewControl({ catalog, state, onChange }: { catalog: AnalysisCatalog; state: AnalysisState; onChange: (view: AnalysisView) => void }) {
  const options = viewOptions(catalog, state), blocked = options.find(([view, , reason]) => view === "trend" && reason)?.[2];
  return <ChartSelect label="View" value={state.view ?? "trend"} options={options} note={blocked} title="Over time, or one value per group across the range" onChange={onChange} />;
}

// One control bar for every view: each view shows only the controls it uses,
// and the ones views share (View, Metric, Breakdown, Range, Scale) are the
// same controls. Changes go through set, which normalizes them.
export function AnalysisControls({ catalog, view, set, onView, bounds, onRange }: {
  catalog: AnalysisCatalog; view: ResolvedAnalysis; set: (change: Partial<AnalysisState>) => void; onView?: (view: AnalysisView) => void;
  bounds: TimeBounds; onRange: (from?: Date, to?: Date, preset?: ChartRange | "all") => void;
}) {
  const unit = view.unit === records ? undefined : catalog.units[view.unit];
  const subjects = (unit?.plural ?? catalog.records.plural).toLowerCase();
  const measure = catalog.measures[view.metric];
  const trend = view.view === "trend", limits = trendLimits(catalog, view);
  const grouped = view.view === "compare" || view.view === "distribution" || view.view === "scatter";
  const unitOptions: Array<[string, string, string?]> = [[records, catalog.records.plural, view.view === "scatter" ? "A scatter pairs two measures of each conversation, session or run." : view.view === "distribution" && !measure.value ? "This measure has no value per record." : undefined],
    ...Object.entries(catalog.units).map(([id, item]): [string, string] => [id, item.plural])];
  const minimum = sampleThresholds.includes(view.minSamples) ? sampleThresholds : [...sampleThresholds, view.minSamples].sort((a, b) => a - b);
  const breakdowns: Array<[string, string, string?]> = [[noSplit, "None"], ...Object.entries(catalog.breakdowns).map(([id, item]): [string, string, string?] => [id, item.label, trend || splitValid(catalog, view.unit, id) ? undefined : `Not constant within one ${unit?.label.toLowerCase()}`]),
    ...trend ? Object.entries(catalog.components ?? {}).map(([id, item]): [string, string, string?] => [id, item.label, limits.component(id)]) : []];
  const count = trendPeriodCount(view);
  const content = <>
    {onView ? <AnalysisViewControl catalog={catalog} state={view} onChange={onView} /> : null}
    {grouped ? <ChartSelect label="Count each" value={view.unit} options={unitOptions} title="What one observation is" onChange={next => set({ unit: next })} /> : null}
    <ChartSelect label={view.view === "scatter" ? "X metric" : "Metric"} value={view.metric} options={Object.entries(catalog.measures).map(([id, item]) => [id, item.label])} onChange={metric => set({ metric })} />
    {view.view === "scatter" ? <ChartSelect label="Y metric" value={view.metricY} options={Object.entries(catalog.measures).map(([id, item]): [string, string, string?] => [id, item.label, id === view.metric ? "Choose a different measure from X" : undefined])} onChange={metricY => set({ metricY })} /> : null}
    {grouped || trend ? <ChartSelect label={view.view === "scatter" ? "Color by" : "Breakdown"} value={view.split} options={breakdowns} title={trend ? "How each period is divided into series" : undefined}
      onChange={split => set({ split, ...trend ? { seriesLimit: 15, selection: { values: [], excluded: false } } : {} })} /> : null}
    {view.view === "compare" && unit ? <ChartSelect label="Combine" value={view.reduce} options={analysisReductions.map(([id]): [AnalysisReduce, string, string?] => [id, reductionLabel(id, measure, unit), id === "total" && !unitTotal(measure) ? `${measure.label} does not add up across ${subjects}` : undefined])} title={`How each group's ${subjects} combine`} onChange={reduce => set({ reduce })} /> : null}
    {view.view === "cycle" ? <ChartSelect label="Summarize" value={view.reduce} options={[["median", "Distribution"], ["mean", measure.additive ? "Mean" : "Unweighted mean"], ["total", "Total", cycleTotal(measure) ? undefined : `${measure.label} does not add up across ${view.cycle === "hour" ? "hours" : "days"}`]]} title="Each bar summarizes every complete day or hour that falls on it" onChange={reduce => set({ reduce })} /> : null}
    {view.view === "compare" ? <ChartSelect label="Show as" value={view.compareAs} options={[["bars", "Bars"], ["share", "Share of total", shareable(catalog, { ...view, compareAs: "share" }) ? undefined : "Only additive totals and counts form shares"]]} onChange={compareAs => set({ compareAs })} /> : null}
    {trend ? <ChartSelect label="Show as" value={view.share ? "share" : "values"} options={[["values", "Values"], ["share", "Share of total", limits.share]]} title="Values, or each series' share of its period's total" onChange={share => set({ share: share === "share" })} /> : null}
    {trend ? <DisplayControl value={view.display} disabled={limits.cumulative} onChange={display => set({ display })} /> : null}
    {(view.view === "distribution" || view.view === "compare" && view.compareAs !== "share") && view.split !== noSplit ? <ChartSelect label="Minimum" value={String(view.minSamples)} options={minimum.map(value => [String(value), value ? `${value.toLocaleString()} ${subjects}` : "No minimum"])} title={`Hide groups with fewer ${subjects}`} onChange={value => set({ minSamples: Number(value) })} /> : null}
    {view.view === "cycle" ? <ChartSelect label="Cycle" value={view.cycle} options={[["hour", "Hour of day"], ["weekday", "Day of week"]]} onChange={cycle => set({ cycle })} /> : null}
    {trend ? <ScaleControl value={view.scale ?? "linear"} percent={view.share} onChange={scale => set({ scale })} />
      : !(view.view === "compare" && view.compareAs === "share") ? <ChartSelect label={view.view === "scatter" ? "X scale" : "Scale"} value={axis(view.scale)} options={scales} onChange={scale => set({ scale })} /> : null}
    {view.view === "scatter" ? <ChartSelect label="Y scale" value={axis(view.scaleY)} options={scales} onChange={scaleY => set({ scaleY })} /> : null}
    <RangeControl bounds={bounds} onPresetChange={trend ? (range, oldest) => set(presetPeriod(range, oldest)) : undefined} onChange={onRange} />
    {trend ? <PeriodControl view={view} disabled={Object.fromEntries((["hour", "hours", "day", "days", "week", "month"] as const).map(period => [period, limits.period(period) ?? (period === "hours" || period === "days" ? limits.multiple : undefined)]))} onChange={set} /> : null}
    {trend ? <RollingControl value={view.rollingPeriods} display={view.display} period={view.period} count={count} onChange={rollingPeriods => set({ rollingPeriods })}
      note={view.rollingOp === "sum" ? "Trailing total · includes preceding history" : limits.rollingSum && measure.ratio ? "Pooled over the window · includes preceding history" : undefined} /> : null}
    {trend && view.rollingPeriods > 1 && view.display !== "cumulative" ? <ChartSelect label="Window" value={view.rollingOp} options={[["mean", measure.ratio ? "Pooled" : "Average"], ["sum", "Total", limits.rollingSum]]} title="Each point's trailing window: its average per period, or its total" onChange={rollingOp => set({ rollingOp })} /> : null}
    {trend ? <ChartSelect label="Periods" value={view.completePeriods ? "complete" : "all"} options={[["all", "Include partial"], ["complete", "Whole periods only"]]} title="Whole periods leave out the current period and a partial first one" onChange={value => set({ completePeriods: value === "complete" })} /> : null}
    {trend ? <ChartSelect label="Norm" value={view.norm ? "on" : "off"} options={[["off", "Off"], ["on", "Mean of earlier points", limits.norm]]} title={`A reference line at the mean of the ${view.normPeriods} periods before the latest point, whatever the range`} onChange={value => set({ norm: value === "on" })} /> : null}
  </>;
  // The trend's chart lays out its own control row.
  return trend ? content : <div className="usage-tools-controls usage-analysis-controls">{content}</div>;
}

// The folded cycle's slots are not dataset fields; name them for the chart.
const cycleFields: FieldDef[] = [{ name: "hour_of_day", label: "Local hour", type: "text", source: { kind: "backend" } }, { name: "weekday", label: "Weekday", type: "text", source: { kind: "backend" } }];
const scales: Array<["linear" | "logarithmic", string]> = [["linear", "Linear"], ["logarithmic", "Logarithmic"]];
const axis = (value?: string) => value === "logarithmic" ? "logarithmic" : "linear";

// Compare, distribution, scatter and cycle views over one catalog. Each view
// shows only the controls it uses; changes are normalized so the saved state
// always describes the chart on screen.
export function DashboardAnalysis<T extends AnalysisState>({ catalog, schema, dataset, state, where, onChange, onView, onFilter, local }: {
  catalog: AnalysisCatalog; schema: FieldSchema<any>; dataset: UsageDataset; state: T; where: WhereTerm[];
  onChange: (next: Partial<T>) => void; onView: (view: AnalysisView) => void; onFilter: FilterChange; local?: LocalRows;
}) {
  const originRevision = useOriginMetadata();
  const view = normalizeAnalysis(catalog, state);
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  // Rounding a relative start to midnight is stable within a day.
  const analysis = useMemo(() => analysisQuery(catalog, view, where, { zone }), [catalog, JSON.stringify(view), JSON.stringify(where), zone, new Date().toDateString()]);
  const result = useAnalysisResult(dataset, schema, analysis.query, local);
  const bounds = timeBounds(where, catalog.time);
  // Save what was asked for; only drawing and querying normalize.
  const set = (change: Partial<AnalysisState>) => onChange(changeState(catalog, state, change) as Partial<T>);
  const unit = view.unit === records ? undefined : catalog.units[view.unit];
  const subjects = (unit?.plural ?? catalog.records.plural).toLowerCase();

  let clause: AggregationClause = analysis.clause, entry: AggregationResultEntry | undefined = result.results[0] && selectedShares(result.results[0], analysis.selection), error = result.error, slots = "";
  if (view.view === "cycle" && entry) {
    try {
      const now = Date.now();
      const folded = cycleResult(catalog, view, entry, { from: bounds.from?.getTime(), to: Math.min(bounds.to?.getTime() ?? now, now) }, zone);
      clause = folded.clause; entry = folded.result;
      slots = `${folded.slots.toLocaleString()} complete ${view.cycle === "hour" ? "hours" : "days"} in ${zone}.${folded.earlier ? ` ${folded.earlier.toLocaleString()} earlier recorded ${folded.earlier === 1 ? "date was" : "dates were"} left out: recorded in another time zone, ${folded.earlier === 1 ? "it holds" : "they hold"} only part of a day in your range.` : ""}`;
    } catch (failure) { error = failure instanceof Error ? failure.message : String(failure); entry = undefined; }
  }
  const presentation = useMemo(() => toolMetricPresentation(entry, clause.groupBy), [entry, originRevision, clause]);
  const empty = !result.loading && !error && entry && (!entry.buckets.length || entry.buckets.every(bucket => bucket.distribution?.kind === "histogram" ? !bucket.distribution.n : bucket.distribution?.kind === "box" && !bucket.distribution.summary));
  const hidden = entry?.hiddenGroups ? `${entry.hiddenGroups.toLocaleString()} ${entry.hiddenGroups === 1 ? "group" : "groups"} hidden below the ${view.minSamples.toLocaleString()}-${unit?.label.toLowerCase() ?? "record"} minimum.` : "";

  // Box and bar rows have a fixed pitch; give a full day of hours its rows.
  const tall = (clause.display?.kind === "box" || clause.display?.kind === "bar-horizontal") && (entry?.buckets.length ?? 0) > 16;
  const controls = <AnalysisControls catalog={catalog} view={view} set={set} onView={onView} bounds={bounds} onRange={(from, to, preset) => onFilter(current => withTimeRange(current, catalog.time, from, to, preset))} />;

  return <section className="usage-analysis-dashboard" aria-label="Dashboard analysis" aria-busy={result.loading}>
    {controls}
    {error ? <p className="query-table-error" role="alert">{error}</p>
      : empty ? <p className="usage-analysis-empty" role="status">{analysisEmptyLabel(catalog, view)}</p>
      : <MetricCard className={tall ? "usage-analysis-tall" : undefined} clause={clause} result={presentation.metric as AggregationResultEntry | undefined} fields={view.view === "cycle" ? [...schema.fields, ...cycleFields] : schema.fields} loading={result.loading} renderers={defaultRenderers}
        onInspect={view.view === "cycle" ? undefined : bucket => {
          const query = analysisInspectQuery(analysis, presentation.sourceBucket(bucket) as typeof bucket, analysis.clause, schema, view.view);
          if (query) window.pharosNavigate?.(usageWorkspaceURL({ mode: "query", dataset, query: orderedUsageQuery(dataset, query) }, new URLSearchParams(location.search)));
        }} />}
    {hidden || slots || analysis.selection ? <p className="usage-dashboard-note" role="status">{[hidden, slots, analysis.selection ? `Your ${catalog.breakdowns[view.split].label.toLowerCase()} selection is drawn as slices and every other group as Other; shares are of every group in your other filters.` : ""].filter(Boolean).join(" ")}</p> : null}
    {/* No legacy link: that page has no analysis views, and would show this query's table under its default chart. */}
    <UsageDashboardLinks schema={schema} dataset={dataset} query={analysis.supporting} />
    <p className="usage-dashboard-note">{analysisNote(catalog, view)}{view.view === "cycle" ? "" : ` Select a ${view.view === "scatter" ? "point" : "bar or box"} to open its ${unit ? subjects : "records"}.`}</p>
  </section>;
}
