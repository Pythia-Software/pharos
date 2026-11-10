import { DataReadinessIndicator, DataReadinessEmpty } from "./data-readiness";
import React, { useMemo } from "react";
import type { FieldSchema, WhereTerm } from "@pythia-software/query-table-core";
import { AnalysisControls, useAnalysisResult } from "./dashboard-analysis";
import { isTrendView, measureAxisUnit, changeState, canonicalState, viewChosen, noSplit, heatmapAxis, heatmapSource, percentileLabel, trendDrawing, normalizeAnalysis, partField, rollingSumUnit, trendAdjustments, trendKind, type AnalysisCatalog, type AnalysisMeasure, type AnalysisState, type AnalysisView } from "./dashboard-analysis-model";
import { trendPeriodCount, trendQuery, trendSeries } from "./dashboard-trend-model";
import { analysisCardCopy } from "./dashboard-card-copy";
import { StackedColumns, pickRange, timeBounds, withTimeRange, type FilterChange } from "./usage-chart";
import { updateSlice, updateSliceValues, type LegendAction } from "./chart-selection";
import { dashboardChartURL } from "./dashboard-chart-links";
import type { ChartInspectionRange } from "./chart-inspection";
import type { ChartSeries } from "./chart-series";
import { UsageDashboardLinks, useDashboardHeadline } from "./usage-workspace";
import type { UsageDataset } from "./usage-workspace-model";
import { useUsageChartPalette } from "./usage-chart-colors";
import { isOriginDimension, originLabel, useOriginMetadata } from "./origin-dimensions";
import { optionLabels } from "./option-labels";
import { CreateDashboardCard } from "./create-dashboard-card";
import { PopulationControl, usePopulation } from "./dashboard-population-control";
import { TrendHeatmap } from "./dashboard-heatmap";
import { trendHeatmap } from "./dashboard-heatmap-model";

export type AmountFormat = (value: number, measure: AnalysisMeasure) => string;
const percent = (value: number) => `${value.toLocaleString(undefined, { maximumFractionDigits: 1 })}%`;

// Over time for any catalog. The query and series are pure
// (dashboard-trend-model.ts); this component runs the query, wires the
// legend, drill-downs and range picking, and draws prepared buckets.
// Without onView it has no View control, and with links={false} no
// supporting-query links. With headline={false} it keeps its title beside
// the chart rather than in the page's heading, for a dashboard whose first
// chart is another (Parallelism's ribbon).
export function AnalysisTrend<T extends AnalysisState>({ catalog, schema, dataset, state, where, onChange, onView, onFilter, onTimeFilter = onFilter, format, tick = format, links = true, headline: titled = true, children }: {
  catalog: AnalysisCatalog; schema: FieldSchema<any>; dataset: UsageDataset; state: T; where: WhereTerm[];
  onChange: (next: Partial<T>) => void; onView?: (view: AnalysisView) => void; onFilter: FilterChange; onTimeFilter?: FilterChange;
  format: AmountFormat; tick?: AmountFormat; links?: boolean; headline?: boolean; children?: React.ReactNode;
}) {
  const originRevision = useOriginMetadata();
  const headline = useDashboardHeadline();
  const requested = { ...state, view: isTrendView(state.view) ? state.view : "trend" as const };
  const view = normalizeAnalysis(catalog, requested);
  const measure = catalog.measures[view.metric], count = trendPeriodCount(view);
  // A distribution heatmap counts the records in the measure's band
  // (heatmapSource); the query and series read that catalog and state, and
  // the controls the state asked for.
  const source = heatmapSource(catalog, view), queried = source.state;
  const breakdown = source.catalog.breakdowns[queried.split];
  // Relative bounds resolve to period boundaries, so the query is stable
  // between renders within one period.
  const trend = trendQuery(source.catalog, queried, where);
  const result = useAnalysisResult(dataset, schema, trend.query, catalog.request?.params);
  const labels = useMemo(() => optionLabels(schema.fields), [schema]);
  const label = (field: string, value: unknown) => isOriginDimension(field) ? originLabel(field, value) : labels.get(field)?.get(String(value)) ?? String(value);
  // A heatmap keeps every period, so an empty one is drawn as empty.
  const built = useMemo(() => trendSeries(source.catalog, view.display === "heatmap" ? { ...queried, nonzero: false } : queried, trend, result.results, { label }),
    [catalog, result.results, originRevision, JSON.stringify(view), JSON.stringify(trend.range), JSON.stringify(where)]);
  useUsageChartPalette(trend.slice ?? null, built.series);
  // Save what was asked for; only drawing and querying normalize, so a
  // setting a measure cannot use returns with one that can.
  const set = (change: Partial<AnalysisState>) => onChange(changeState(catalog, state, change) as Partial<T>);

  // A legend selection is the view's own: it chooses which series are
  // drawn, never the population (docs/dashboard-analysis.md).
  function select(key: string | null, action: LegendAction) {
    const value = key === null ? undefined : built.series.find(item => item.key === key)?.value;
    set({ selection: updateSlice(view.selection ?? { values: [], excluded: false }, value ?? "", action) });
  }
  function selectGroup(keys: string[], action: LegendAction) {
    const members = new Set(keys), values = built.series.flatMap(item => members.has(item.key) && item.value !== undefined ? [item.value] : []);
    set({selection:updateSliceValues(view.selection ?? {values:[],excluded:false},values,action)});
  }
  const share = view.share, ratio = measure.format.kind === "percent";
  const amount = (value: number) => share ? percent(value) : ratio ? percent(100 * value) : format(value, measure);
  const tickAmount = (value: number) => share ? `${value}%` : ratio ? percent(100 * value) : tick(value, measure);
  const kind = trendKind(measure);
  // Drill-downs open the records behind a point: the dashboard's filters,
  // the measure's population, the series, and the point's window.
  const translate = catalog.executable && ((terms: WhereTerm[]) => catalog.executable!(terms, Date.now()));
  const href = (range: ChartInspectionRange, item: ChartSeries) => {
    const base = [...range.rolling ? trend.requested : where, ...trend.records];
    const components = catalog.components?.[view.split];
    if (components) {
      const part = components.parts.find(candidate => candidate.key === item.key);
      return dashboardChartURL({ dataset, where: base, time: catalog.time, range, series: item, fields: part?.fields ?? [part?.field ?? measure.field ?? ""], current: new URLSearchParams(location.search), translate });
    }
    const fields = kind === "ratio" ? [partField(measure.ratio![1])].filter((field): field is string => Boolean(field)) : measure.op === "sum" && measure.field ? [measure.field] : [];
    if (breakdown && breakdown.fields.length > 1) {
      // A combined group is several equalities; Other has no single filter.
      if (item.value === undefined) return;
      const values = JSON.parse(item.value) as unknown[];
      const terms: WhereTerm[] = breakdown.fields.map((field, index) => values[index] === null || values[index] === undefined || values[index] === "" ? { field, op: "is_null", value: "" } : { field, op: "=", value: String(values[index]) });
      return dashboardChartURL({ dataset, where: [...base, ...terms], time: catalog.time, range, series: item, fields, current: new URLSearchParams(location.search), translate });
    }
    return dashboardChartURL({ dataset, where: base, time: catalog.time, range, series: item, split: trend.slice ?? null, fields, current: new URLSearchParams(location.search), translate });
  };
  const adjustments = trendAdjustments(catalog, requested);
  if (view.norm && !result.loading && built.normPoints !== undefined && built.normPoints < view.normPeriods) adjustments.push(built.normPoints
    ? `The reference levels read ${built.normPoints.toLocaleString()} earlier ${built.normPoints === 1 ? "point" : "points"}: fewer than ${view.normPeriods} have a value before the latest.`
    : "No earlier point has a value, so there is no reference level yet.");
  const bounds = timeBounds(where, catalog.time);
  const population = usePopulation(catalog, schema, where);
  const outputEmpty=!result.loading&&!result.error&&result.results.every(entry=>!entry.buckets.length);
  const title = analysisCardCopy(catalog, view, where, schema).title;
  const controls = <AnalysisControls catalog={catalog} view={view} asked={state} chosen={viewChosen(canonicalState(catalog, state))} set={set} onView={onView} bounds={bounds} onRange={(from, to, preset) => onTimeFilter(current => withTimeRange(current, catalog.time, from, to, preset), preset)}
    population={catalog.population ? <PopulationControl catalog={catalog} schema={schema} where={where} terms={population.terms} subjects={catalog.records.plural.toLowerCase()} onFilter={onFilter} /> : undefined} />;
  const action = <><DataReadinessIndicator id={`chart:${dataset}`} title={title} dataset={dataset} query={trend.query} empty={outputEmpty} {...result.readiness} />{onView ? <CreateDashboardCard source={{ catalog, state, where, schema }} /> : null}</>;
  // A heatmap draws the value its bins hold on that value's own axis, with
  // the band's edges as guides (heatmapAxis).
  const axis = view.display === "heatmap" ? heatmapAxis(catalog, view) : undefined;
  if (axis) {
    const band = catalog.breakdowns[axis.breakdown], weight = source.catalog.measures[queried.metric];
    const value = (amount: number) => axis.bins.format.kind === "percent" ? percent(100 * amount) : format(amount, { ...measure, format: axis.bins.format });
    const guides = (axis.bins.edges ?? []).flatMap((edge, index) => index === 0 || !band.order?.[index] ? [] : [{ value: edge, label: band.values?.[band.order[index]] ?? band.order[index] }]);
    return <section className="usage-analysis-trend" aria-label="Over time" aria-busy={result.loading}>
      <DataReadinessEmpty id={`chart:${dataset}`} title={title} dataset={dataset} query={trend.query} empty={outputEmpty}><TrendHeatmap title={title} controls={controls} action={action} titleSlot={links && titled ? headline : undefined} heatmap={trendHeatmap(built.buckets, built.series, axis.bins.scheme, view.percentiles)}
        period={view.period} noun={axis.unit ? catalog.units[axis.unit].plural.toLowerCase() : axis.counted ? catalog.records.plural.toLowerCase() : weight.unit} label={axis.bins.label} format={value} amount={axis.counted || weight.op === "count" ? undefined : amount => format(amount, weight)}
        rollingPeriods={view.rollingPeriods} guides={guides} loading={result.loading} error={result.error || (result.loading ? "" : built.error ?? "")} /></DataReadinessEmpty>
      {adjustments.length ? <p className="usage-dashboard-note" role="status">{adjustments.join(" ")}</p> : null}
      {children}
      {links ? <UsageDashboardLinks schema={schema} dataset={dataset} query={trend.supporting} context={{ catalog, state: view, where, supporting: trend.supporting }} /> : null}
    </section>;
  }
  return <section className="usage-analysis-trend" aria-label="Over time" aria-busy={result.loading}>
    <DataReadinessEmpty id={`chart:${dataset}`} title={title} dataset={dataset} query={trend.query} empty={outputEmpty}><StackedColumns title={title} controls={controls} action={action} series={built.series.length ? built.series : [{ key: "none", label: title, color: "var(--heat-1)" }]} buckets={built.buckets} prepared
      period={view.period} periodCount={count} rollingPeriods={view.rollingPeriods} display={view.display === "heatmap" ? "interval" : view.display} cumulativeStart={built.cumulativeStart} scale={view.scale}
      rolling={kind === "ratio" ? "pooled" : view.rollingOp === "sum" ? "sum" : "average"} lines={built.lines} bars={trendDrawing(catalog, view) === "trend_bar"} additive={built.additive} nonzero={view.nonzero && !view.share}
      references={built.references?.map(item => ({ value: item.value, level: item.level, label: `${percentileLabel(item.level)}, prior ${view.normPeriods}` }))}
      format={amount} tickFormat={tickAmount} axisUnit={measureAxisUnit(measure)} noun={share || ratio || measure.unit === "USD" ? "" : view.display !== "cumulative" && view.rollingOp === "sum" && view.rollingPeriods > 1 ? rollingSumUnit(measure, view.period) : measure.unit}
      loading={result.loading} error={result.error || (result.loading ? "" : built.error ?? "")} legend={built.series.length > 0} percent={share}
      selected={built.selected} excluded={built.excluded} allHidden={built.allHidden} onSelect={select} onSelectGroup={selectGroup} onExpand={catalog.trend.allSeries ? undefined : () => set({ seriesLimit: Math.min(1000, view.seriesLimit + 5) })}
      onRange={pickRange(onTimeFilter, catalog.time, view.period, count, period => set({ period }))} seriesHref={href} titleSlot={links && titled ? headline : undefined} />
    {adjustments.length ? <p className="usage-dashboard-note" role="status">{adjustments.join(" ")}</p> : null}
    {measure.populationShare && measure.note ? <p className="usage-dashboard-note">{measure.note}</p> : null}
    </DataReadinessEmpty>
    {children}
    {links ? <UsageDashboardLinks schema={schema} dataset={dataset} query={trend.supporting} context={{ catalog, state: view, where, supporting: trend.supporting }} /> : null}
  </section>;
}
