import React, { useEffect, useMemo, useState } from "react";
import { MAX_WHERE_CLAUSES, type AggregationClause, type AggregationResultEntry, type FieldDef, type FieldSchema, type QueryState, type WhereTerm } from "@pythia-software/query-table-core";
import { MetricCard, defaultRenderers } from "@pythia-software/query-table-ui";
import { changeState, cycleUnavailable, notTrended, sampleSubject, selectGroups, analysisEmptyLabel, analysisInspectQuery, analysisNote, analysisQuery, analysisReductions, viewOptions, cycleResult, nativeCycleResult, cycleTotal, noSplit, normalizeAnalysis, records, reductionLabel, sampleThresholds, shareable, splitValid, trendLimits, unitTotal, type AnalysisCatalog, type AnalysisQuery as AnalysisQueryResult, type AnalysisReduce, type AnalysisUnit, type AnalysisState, type AnalysisView, type ResolvedAnalysis } from "./dashboard-analysis-model";
import { runAnalysisQuery, type LocalRows } from "./analysis-request";
import type { RequestParams } from "./metric-request";
import { onDatasetRefresh } from "./dataset-refresh";
import { ChartSelect, DisplayControl, PeriodControl, RangeControl, RollingControl, ScaleControl, presetPeriod, timeBounds, withTimeRange, type ChartRange, type FilterChange, type TimeBounds } from "./usage-chart";
import { trendPeriodCount } from "./dashboard-trend-model";
import { UsageDashboardLinks } from "./usage-workspace";
import { usageWorkspaceURL, type UsageDataset } from "./usage-workspace-model";
import { orderedUsageQuery } from "./usage-query-order";
import { toolMetricPresentation } from "./tools-dashboard-model";
import { useOriginMetadata } from "./origin-dimensions";

// Runs an analysis query, again on Refresh, and tags results with their query
// so a stale response never renders under new settings.
export function useAnalysisResult(dataset: string, schema: FieldSchema<any>, query: QueryState, local?: LocalRows, params?: RequestParams) {
  const key = JSON.stringify([query, params ?? {}]);
  // A result belongs to its query and to the refresh it was fetched for, so
  // results from before a refresh read as loading, never as current.
  const [state, setState] = useState<{ key: string; refresh: number; results: AggregationResultEntry[]; error: string; loading: boolean }>({ key: "", refresh: 0, results: [], error: "", loading: true });
  const [refresh, setRefresh] = useState(0);
  useEffect(() => onDatasetRefresh(dataset, () => setRefresh(value => value + 1)), [dataset]);
  useEffect(() => {
    const controller = new AbortController();
    setState(previous => ({ ...previous, loading: true }));
    const [request, parameters] = JSON.parse(key) as [QueryState, RequestParams];
    runAnalysisQuery(dataset, schema, request, { signal: controller.signal, local, params: parameters })
      .then(results => setState({ key, refresh, results, error: "", loading: false }))
      .catch(failure => { if (!controller.signal.aborted) setState({ key, refresh, results: [], error: failure instanceof Error ? failure.message : String(failure), loading: false }); });
    return () => controller.abort();
  }, [dataset, key, refresh, local]);
  const current = state.key === key;
  return { results: current ? state.results : [], loading: !current || state.refresh !== refresh || state.loading, error: current ? state.error : "" };
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
  const grouped = (view.view === "compare" || view.view === "distribution" || view.view === "scatter") && Object.keys(catalog.units).length > 0;
  const unitOptions: Array<[string, string, string?]> = [[records, catalog.records.plural, view.view === "scatter" ? "A scatter pairs two measures of each conversation, session or run." : view.view === "distribution" && !measure.value ? "This measure has no value per record." : undefined],
    ...Object.entries(catalog.units).map(([id, item]): [string, string] => [id, item.plural])];
  const minimum = sampleThresholds.includes(view.minSamples) ? sampleThresholds : [...sampleThresholds, view.minSamples].sort((a, b) => a - b);
  const breakdowns: Array<[string, string, string?]> = [[noSplit, "None"], ...Object.entries(catalog.breakdowns).map(([id, item]): [string, string, string?] => [id, item.label, trend || splitValid(catalog, view.unit, id) ? undefined : `Not constant within one ${unit?.label.toLowerCase()}`]),
    ...trend ? Object.entries(catalog.components ?? {}).map(([id, item]): [string, string, string?] => [id, item.label, limits.component(id)]) : []];
  const count = trendPeriodCount(view);
  const content = <>
    {onView ? <AnalysisViewControl catalog={catalog} state={view} onChange={onView} /> : null}
    {grouped ? <ChartSelect label="Count each" value={view.unit} options={unitOptions} title="What one observation is" onChange={next => set({ unit: next })} /> : null}
    <ChartSelect label={view.view === "scatter" ? "X metric" : "Metric"} value={view.metric} options={Object.entries(catalog.measures).map(([id, item]): [string, string, string?] => [id, item.label, view.view === "distribution" && view.unit === records && !unit && !item.value ? `${item.label} has no value per ${catalog.records.label.toLowerCase()} to distribute` : trend ? notTrended(item) : undefined])} onChange={metric => set({ metric })} />
    {view.view === "scatter" ? <ChartSelect label="Y metric" value={view.metricY} options={Object.entries(catalog.measures).map(([id, item]): [string, string, string?] => [id, item.label, id === view.metric ? "Choose a different measure from X" : undefined])} onChange={metricY => set({ metricY })} /> : null}
    {view.view !== "cycle" ? <ChartSelect label={view.view === "scatter" ? "Color by" : "Breakdown"} value={view.split} options={breakdowns} title={trend ? "How each period is divided into series" : undefined}
      onChange={split => set({ split, selection: { values: [], excluded: false }, ...trend ? { seriesLimit: 15 } : {} })} /> : null}
    {view.view === "compare" && unit ? <ChartSelect label="Combine" value={view.reduce} options={analysisReductions.map(([id]): [AnalysisReduce, string, string?] => [id, reductionLabel(id, measure, unit), id === "total" && !unitTotal(measure) ? `${measure.label} does not add up across ${subjects}` : undefined])} title={`How each group's ${subjects} combine`} onChange={reduce => set({ reduce })} /> : null}
    {view.view === "cycle" ? <ChartSelect label="Summarize" value={view.reduce} options={[["median", "Distribution"], ["mean", measure.additive ? "Mean" : "Unweighted mean"], ["total", "Total", cycleTotal(measure) ? undefined : `${measure.label} does not add up across ${view.cycle === "hour" ? "hours" : "days"}`]]} title="Each bar summarizes every complete day or hour that falls on it" onChange={reduce => set({ reduce })} /> : null}
    {view.view === "compare" ? <ChartSelect label="Show as" value={view.compareAs} options={[["bars", "Bars"], ["share", "Share of total", shareable(catalog, { ...view, compareAs: "share" }) ? undefined : "Only additive totals and counts form shares"]]} onChange={compareAs => set({ compareAs })} /> : null}
    {trend ? <ChartSelect label="Show as" value={view.share ? "share" : "values"} options={[["values", "Values"], ["share", "Share of total", limits.share]]} title="Values, or each series' share of its period's total" onChange={share => set({ share: share === "share" })} /> : null}
    {trend ? <DisplayControl value={view.display} disabled={limits.cumulative} onChange={display => set({ display })} /> : null}
    {(view.view === "distribution" || view.view === "compare" && view.compareAs !== "share") && view.split !== noSplit ? <ChartSelect label="Minimum" value={String(view.minSamples)} options={minimum.map(value => [String(value), value ? `${value.toLocaleString()} ${sampleSubject(catalog, view)}` : "No minimum"])} title={`Hide groups with fewer ${sampleSubject(catalog, view)}`} onChange={value => set({ minSamples: Number(value) })} /> : null}
    {view.view === "cycle" ? <ChartSelect label="Cycle" value={view.cycle} options={[["hour", "Hour of day", cycleUnavailable(catalog, "hour")], ["weekday", "Day of week", cycleUnavailable(catalog, "weekday")]]} onChange={cycle => set({ cycle })} /> : null}
    {trend ? <ScaleControl value={view.scale!} percent={view.share} onChange={scale => set({ scale })} />
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

const selectionNote = (group: string, unit: AnalysisUnit | undefined, selection: NonNullable<AnalysisQueryResult["selection"]>, missing = 0) =>
  (selection.share ? `Your ${group} legend selection is drawn as slices and every other group as Other; shares are of every group in your filters.`
    : `Only your ${group} legend selection is drawn; your filters are unchanged.`)
  + (selection.mapped && unit ? ` Each ${unit.label.toLowerCase()} is counted in its top-level ${group} group, so the selection is shown as the top-level ${group}.` : "")
  + (selection.late ? ` The selection holds more conditions than one query allows (${MAX_WHERE_CLAUSES} with your filters), so it is drawn from the ${unit ? unit.plural.toLowerCase() : "groups"} the query returns; one beyond its limit is not drawn.` : "")
  + (missing ? ` ${missing.toLocaleString()} selected ${missing === 1 ? "group is" : "groups are"} not among the largest groups returned; any ${missing === 1 ? "value it has is" : "values they have are"} ${selection.share ? "counted in Other" : "not drawn"}.` : "");

// The folded cycle's slots are not dataset fields; name them for the chart,
// as a native cycle's timestamp is named for its slots.
const cycleFields: FieldDef[] = [{ name: "hour_of_day", label: "Local hour", type: "text", source: { kind: "backend" } }, { name: "weekday", label: "Weekday", type: "text", source: { kind: "backend" } }];
const scales: Array<["linear" | "logarithmic", string]> = [["linear", "Linear"], ["logarithmic", "Logarithmic"]];
const axis = (value?: string) => value === "logarithmic" ? "logarithmic" : "linear";

// Compare, distribution, scatter and cycle views over one catalog. Each view
// shows only the controls it uses; changes are normalized so the saved state
// always describes the chart on screen.
export function DashboardAnalysis<T extends AnalysisState>({ catalog, schema, dataset, state, where, onChange, onView, onFilter, local, legacyURL, children }: {
  catalog: AnalysisCatalog; schema: FieldSchema<any>; dataset: UsageDataset; state: T; where: WhereTerm[];
  onChange: (next: Partial<T>) => void; onView: (view: AnalysisView) => void; onFilter: FilterChange; local?: LocalRows; legacyURL?: string; children?: React.ReactNode;
}) {
  const originRevision = useOriginMetadata();
  const view = normalizeAnalysis(catalog, state);
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  // Rounding a relative start to midnight is stable within a day.
  const analysis = useMemo(() => analysisQuery(catalog, view, where, { zone }), [catalog, JSON.stringify(view), JSON.stringify(where), zone, new Date().toDateString()]);
  const result = useAnalysisResult(dataset, schema, analysis.query, local, catalog.request?.params);
  const bounds = timeBounds(where, catalog.time);
  // Save what was asked for; only drawing and querying normalize.
  const set = (change: Partial<AnalysisState>) => onChange(changeState(catalog, state, change) as Partial<T>);
  const unit = view.unit === records ? undefined : catalog.units[view.unit];
  const subjects = (unit?.plural ?? catalog.records.plural).toLowerCase();

  const selected = result.results[0] && selectGroups(result.results[0], analysis.selection);
  let clause: AggregationClause = analysis.clause, entry: AggregationResultEntry | undefined = selected, error = result.error, slots = "";
  if (view.view === "cycle" && entry) {
    const per = view.cycle === "hour" ? "hours" : "days";
    try {
      if (analysis.native) {
        // Query Table computed the slots; the chart draws them as returned.
        const native = nativeCycleResult(entry);
        if (native.slots) slots = `${native.slots.toLocaleString()} complete ${per} in ${zone}.`;
      } else {
        const now = Date.now();
        const folded = cycleResult(catalog, view, entry, { from: bounds.from?.getTime(), to: Math.min(bounds.to?.getTime() ?? now, now) }, zone);
        clause = folded.clause; entry = folded.result;
        slots = `${folded.slots.toLocaleString()} complete ${per} in ${zone}.${folded.earlier ? ` ${folded.earlier.toLocaleString()} earlier recorded ${folded.earlier === 1 ? "date was" : "dates were"} left out: recorded in another time zone, ${folded.earlier === 1 ? "it holds" : "they hold"} only part of a day in your range.` : ""}`;
      }
    } catch (failure) { error = failure instanceof Error ? failure.message : String(failure); entry = undefined; }
  }
  const presentation = useMemo(() => toolMetricPresentation(entry, clause.groupBy, catalog.labels), [entry, originRevision, clause, catalog]);
  const empty = !result.loading && !error && entry && (!entry.buckets.length || entry.buckets.every(bucket => bucket.distribution?.kind === "histogram" ? !bucket.distribution.n : bucket.distribution?.kind === "box" && !bucket.distribution.summary));
  const hidden = entry?.hiddenGroups ? `${entry.hiddenGroups.toLocaleString()} ${entry.hiddenGroups === 1 ? "group" : "groups"} hidden below the minimum of ${view.minSamples.toLocaleString()} ${sampleSubject(catalog, view)}.` : "";

  // Box and bar rows have a fixed pitch; give a full day of hours its rows.
  const tall = (clause.display?.kind === "box" || clause.display?.kind === "bar-horizontal") && (entry?.buckets.length ?? 0) > 16;
  const controls = <AnalysisControls catalog={catalog} view={view} set={set} onView={onView} bounds={bounds} onRange={(from, to, preset) => onFilter(current => withTimeRange(current, catalog.time, from, to, preset))} />;

  return <section className="usage-analysis-dashboard" aria-label="Dashboard analysis" aria-busy={result.loading}>
    {controls}
    {error ? <p className="query-table-error" role="alert">{error}</p>
      : empty ? <p className="usage-analysis-empty" role="status">{analysisEmptyLabel(catalog, view)}</p>
      : <MetricCard className={tall ? "usage-analysis-tall" : undefined} clause={clause} result={presentation.metric as AggregationResultEntry | undefined} fields={view.view !== "cycle" ? schema.fields : analysis.native ? schema.fields.map(field => field.name === clause.dateGrouping?.field ? { ...field, label: view.cycle === "hour" ? "Local hour" : "Weekday" } : field) : [...schema.fields, ...cycleFields]} loading={result.loading} renderers={defaultRenderers}
        onInspect={view.view === "cycle" && !analysis.native ? undefined : bucket => {
          const query = analysisInspectQuery(analysis, presentation.sourceBucket(bucket) as typeof bucket, analysis.clause, schema, view.view);
          if (query) window.pharosNavigate?.(usageWorkspaceURL({ mode: "query", dataset, query: orderedUsageQuery(dataset, query) }, new URLSearchParams(location.search)));
        }} />}
    {hidden || slots || analysis.selection ? <p className="usage-dashboard-note" role="status">{[hidden, slots, analysis.selection ? selectionNote(catalog.breakdowns[view.split].label.toLowerCase(), unit, analysis.selection, selected?.missing) : ""].filter(Boolean).join(" ")}
      {analysis.selection ? <> <button type="button" className="usage-preset-clear" onClick={() => set({ selection: { values: [], excluded: false } })}>Show every group</button></> : null}</p> : null}
    {children}
    {/* A legacy link only where that page still has something of its own
        (Tools' failure kinds); otherwise it would show this query's table
        under its default chart. */}
    <UsageDashboardLinks schema={schema} dataset={dataset} query={analysis.supporting} currentURL={legacyURL} />
    <p className="usage-dashboard-note">{analysisNote(catalog, view)}{view.view === "cycle" && !analysis.native ? "" : ` Select a ${view.view === "scatter" ? "point" : "bar or box"} to open its ${unit ? subjects : "records"}.`}</p>
  </section>;
}
