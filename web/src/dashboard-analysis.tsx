import React, { useEffect, useMemo, useState } from "react";
import type { AggregationClause, AggregationResultEntry, FieldDef, FieldSchema, QueryState, WhereTerm } from "@pythia-software/query-table-core";
import { MetricCard, defaultRenderers } from "@pythia-software/query-table-ui";
import { analysisEmptyLabel, analysisInspectQuery, analysisNote, analysisQuery, analysisReductions, availableViews, cycleResult, noSplit, normalizeAnalysis, records, reductionLabel, sampleThresholds, shareable, splitValid, unitTotal, type AnalysisCatalog, type AnalysisReduce, type AnalysisState, type AnalysisView } from "./dashboard-analysis-model";
import { runAnalysisQuery, type LocalRows } from "./analysis-request";
import { ChartSelect, RangeControl, timeBounds, withTimeRange, type FilterChange } from "./usage-chart";
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

export function AnalysisViewControl({ catalog, value, onChange }: { catalog: AnalysisCatalog; value: AnalysisView; onChange: (view: AnalysisView) => void }) {
  return <ChartSelect label="View" value={value} options={availableViews(catalog)} title="Over time, or one value per group across the range" onChange={onChange} />;
}

// The folded cycle's slots are not dataset fields; name them for the chart.
const cycleFields: FieldDef[] = [{ name: "hour_of_day", label: "Local hour", type: "text", source: { kind: "backend" } }, { name: "weekday", label: "Weekday", type: "text", source: { kind: "backend" } }];
const scales: Array<["linear" | "logarithmic", string]> = [["linear", "Linear"], ["logarithmic", "Logarithmic"]];
const axis = (value?: string) => value === "logarithmic" ? "logarithmic" : "linear";

// Compare, distribution, scatter and cycle views over one catalog. Each view
// shows only the controls it uses; changes are normalized so the saved state
// always describes the chart on screen.
export function DashboardAnalysis<T extends AnalysisState>({ catalog, schema, dataset, state, where, onChange, onView, onFilter, local, currentURL }: {
  catalog: AnalysisCatalog; schema: FieldSchema<any>; dataset: UsageDataset; state: T; where: WhereTerm[];
  onChange: (next: Partial<T>) => void; onView: (view: AnalysisView) => void; onFilter: FilterChange; local?: LocalRows; currentURL?: string;
}) {
  const originRevision = useOriginMetadata();
  const view = normalizeAnalysis(catalog, state);
  const analysis = useMemo(() => analysisQuery(catalog, view, where), [catalog, JSON.stringify(view), JSON.stringify(where)]);
  const result = useAnalysisResult(dataset, schema, analysis.query, local);
  const bounds = timeBounds(where, catalog.time);
  const set = (change: Partial<AnalysisState>) => onChange(normalizeAnalysis(catalog, { ...state, ...change }) as Partial<T>);
  const unit = view.unit === records ? undefined : catalog.units[view.unit];
  const subjects = (unit?.plural ?? catalog.records.plural).toLowerCase();

  let clause: AggregationClause = analysis.clause, entry: AggregationResultEntry | undefined = result.results[0], error = result.error, slots = "";
  if (view.view === "cycle" && entry) {
    try {
      const now = Date.now(), zone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
      const folded = cycleResult(catalog, view, entry, { from: bounds.from?.getTime(), to: Math.min(bounds.to?.getTime() ?? now, now) }, zone);
      clause = folded.clause; entry = folded.result;
      slots = `${folded.slots.toLocaleString()} complete ${view.cycle === "hour" ? "hours" : "days"} in ${zone}.`;
    } catch (failure) { error = failure instanceof Error ? failure.message : String(failure); entry = undefined; }
  }
  const presentation = useMemo(() => toolMetricPresentation(entry, clause.groupBy), [entry, originRevision, clause]);
  const empty = !result.loading && !error && entry && (!entry.buckets.length || entry.buckets.every(bucket => bucket.distribution?.kind === "histogram" ? !bucket.distribution.n : bucket.distribution?.kind === "box" && !bucket.distribution.summary));
  const hidden = entry?.hiddenGroups ? `${entry.hiddenGroups.toLocaleString()} ${entry.hiddenGroups === 1 ? "group" : "groups"} hidden below the ${view.minSamples.toLocaleString()}-${unit?.label.toLowerCase() ?? "record"} minimum.` : "";

  // Box and bar rows have a fixed pitch; give a full day of hours its rows.
  const tall = (clause.display?.kind === "box" || clause.display?.kind === "bar-horizontal") && (entry?.buckets.length ?? 0) > 16;
  const grouped = view.view === "compare" || view.view === "distribution" || view.view === "scatter";
  const unitOptions: Array<[string, string, string?]> = [[records, catalog.records.plural, view.view === "scatter" ? "A scatter pairs two measures of each conversation, session or run." : view.view === "distribution" && !catalog.measures[view.metric].value ? "This measure has no value per record." : undefined],
    ...Object.entries(catalog.units).map(([id, item]): [string, string] => [id, item.plural])];
  const minimum = sampleThresholds.includes(view.minSamples) ? sampleThresholds : [...sampleThresholds, view.minSamples].sort((a, b) => a - b);
  const controls = <div className="usage-tools-controls usage-analysis-controls">
    <AnalysisViewControl catalog={catalog} value={view.view} onChange={onView} />
    {grouped ? <ChartSelect label="Count each" value={view.unit} options={unitOptions} title="What one observation is" onChange={next => set({ unit: next })} /> : null}
    <ChartSelect label={view.view === "scatter" ? "X metric" : "Metric"} value={view.metric} options={Object.entries(catalog.measures).map(([id, item]) => [id, item.label])} onChange={metric => set({ metric })} />
    {view.view === "scatter" ? <ChartSelect label="Y metric" value={view.metricY} options={Object.entries(catalog.measures).map(([id, item]): [string, string, string?] => [id, item.label, id === view.metric ? "Choose a different measure from X" : undefined])} onChange={metricY => set({ metricY })} /> : null}
    {grouped ? <ChartSelect label={view.view === "scatter" ? "Color by" : "Breakdown"} value={view.split} options={[[noSplit, "None"], ...Object.entries(catalog.breakdowns).map(([id, item]): [string, string, string?] => [id, item.label, splitValid(catalog, view.unit, id) ? undefined : `Not constant within one ${unit?.label.toLowerCase()}`])]} onChange={split => set({ split })} /> : null}
    {view.view === "compare" && unit ? <ChartSelect label="Combine" value={view.reduce} options={analysisReductions.map(([id]): [AnalysisReduce, string, string?] => [id, reductionLabel(id, catalog.measures[view.metric], unit), id === "total" && !unitTotal(catalog.measures[view.metric]) ? `${catalog.measures[view.metric].label} does not add up across ${subjects}` : undefined])} title={`How each group's ${subjects} combine`} onChange={reduce => set({ reduce })} /> : null}
    {view.view === "cycle" ? <ChartSelect label="Summarize" value={view.reduce} options={[["median", "Distribution"], ["mean", "Mean"], ["total", "Total"]]} title="Each bar summarizes every complete day or hour that falls on it" onChange={reduce => set({ reduce })} /> : null}
    {view.view === "compare" ? <ChartSelect label="Show as" value={view.compareAs} options={[["bars", "Bars"], ["share", "Share of total", shareable(catalog, { ...view, compareAs: "share" }) ? undefined : "Only additive totals and counts form shares"]]} onChange={compareAs => set({ compareAs })} /> : null}
    {(view.view === "distribution" || view.view === "compare" && view.compareAs !== "share") && view.split !== noSplit ? <ChartSelect label="Minimum" value={String(view.minSamples)} options={minimum.map(value => [String(value), value ? `${value.toLocaleString()} ${subjects}` : "No minimum"])} title={`Hide groups with fewer ${subjects}`} onChange={value => set({ minSamples: Number(value) })} /> : null}
    {view.view === "cycle" ? <ChartSelect label="Cycle" value={view.cycle} options={[["hour", "Hour of day"], ["weekday", "Day of week"]]} onChange={cycle => set({ cycle })} /> : null}
    {!(view.view === "compare" && view.compareAs === "share") ? <ChartSelect label={view.view === "scatter" ? "X scale" : "Scale"} value={axis(view.scale)} options={scales} onChange={scale => set({ scale })} /> : null}
    {view.view === "scatter" ? <ChartSelect label="Y scale" value={axis(view.scaleY)} options={scales} onChange={scaleY => set({ scaleY })} /> : null}
    <RangeControl bounds={bounds} onChange={(from, to, preset) => onFilter(current => withTimeRange(current, catalog.time, from, to, preset))} />
  </div>;

  return <section className="usage-analysis-dashboard" aria-label="Dashboard analysis" aria-busy={result.loading}>
    {controls}
    {error ? <p className="query-table-error" role="alert">{error}</p>
      : empty ? <p className="usage-analysis-empty" role="status">{analysisEmptyLabel(catalog, view)}</p>
      : <MetricCard className={tall ? "usage-analysis-tall" : undefined} clause={clause} result={presentation.metric as AggregationResultEntry | undefined} fields={view.view === "cycle" ? [...schema.fields, ...cycleFields] : schema.fields} loading={result.loading} renderers={defaultRenderers}
        onInspect={view.view === "cycle" ? undefined : bucket => {
          const query = analysisInspectQuery(analysis, presentation.sourceBucket(bucket) as typeof bucket, analysis.clause, schema, view.view);
          if (query) window.pharosNavigate?.(usageWorkspaceURL({ mode: "query", dataset, query: orderedUsageQuery(dataset, query) }, new URLSearchParams(location.search)));
        }} />}
    {hidden || slots ? <p className="usage-dashboard-note" role="status">{[hidden, slots].filter(Boolean).join(" ")}</p> : null}
    <UsageDashboardLinks schema={schema} dataset={dataset} query={analysis.supporting} currentURL={currentURL} />
    <p className="usage-dashboard-note">{analysisNote(catalog, view)}{view.view === "cycle" ? "" : ` Select a ${view.view === "scatter" ? "point" : "bar or box"} to open its ${unit ? subjects : "records"}.`}</p>
  </section>;
}
