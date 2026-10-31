import React, { useMemo } from "react";
import type { FieldSchema, WhereTerm } from "@pythia-software/query-table-core";
import { AnalysisControls, useAnalysisResult } from "./dashboard-analysis";
import { changeState, normalizeAnalysis, partField, rollingSumUnit, trendAdjustments, trendKind, type AnalysisCatalog, type AnalysisMeasure, type AnalysisState, type AnalysisView } from "./dashboard-analysis-model";
import { timestampHours, trendNote, trendPeriodCount, trendQuery, trendSeries, trendTitle } from "./dashboard-trend-model";
import type { LocalRows } from "./analysis-request";
import { StackedColumns, pickRange, timeBounds, withTimeRange, type FilterChange } from "./usage-chart";
import { updateSlice, type LegendAction } from "./chart-selection";
import { dashboardChartURL } from "./dashboard-chart-links";
import type { ChartInspectionRange } from "./chart-inspection";
import type { ChartSeries } from "./chart-series";
import { UsageDashboardLinks } from "./usage-workspace";
import type { UsageDataset } from "./usage-workspace-model";
import { useUsageChartPalette } from "./usage-chart-colors";
import { isOriginDimension, originLabel, useOriginMetadata } from "./origin-dimensions";
import { optionLabels } from "./option-labels";
import { CreateDashboardCard } from "./create-dashboard-card";

export type AmountFormat = (value: number, measure: AnalysisMeasure) => string;
const percent = (value: number) => `${value.toLocaleString(undefined, { maximumFractionDigits: 1 })}%`;

// Over time for any catalog. The query and series are pure
// (dashboard-trend-model.ts); this component runs the query, wires the
// legend, drill-downs and range picking, and draws prepared buckets.
// Without onView it has no View control (the legacy Usage page), and with
// links={false} no supporting-query links or note.
export function AnalysisTrend<T extends AnalysisState>({ catalog, schema, dataset, state, where, onChange, onView, onFilter, onTimeFilter = onFilter, local, format, tick = format, links = true, currentURL, children }: {
  catalog: AnalysisCatalog; schema: FieldSchema<any>; dataset: UsageDataset; state: T; where: WhereTerm[];
  onChange: (next: Partial<T>) => void; onView?: (view: AnalysisView) => void; onFilter: FilterChange; onTimeFilter?: FilterChange; local?: LocalRows;
  format: AmountFormat; tick?: AmountFormat; links?: boolean; currentURL?: string; children?: React.ReactNode;
}) {
  const originRevision = useOriginMetadata();
  const view = normalizeAnalysis(catalog, { ...state, view: "trend" as const });
  const measure = catalog.measures[view.metric], breakdown = catalog.breakdowns[view.split], count = trendPeriodCount(view);
  // Relative bounds resolve to period boundaries, so the query is stable
  // between renders within one period.
  const trend = trendQuery(catalog, view, where);
  const result = useAnalysisResult(dataset, schema, trend.query, local, catalog.request?.params);
  const labels = useMemo(() => optionLabels(schema.fields), [schema]);
  const label = (field: string, value: unknown) => isOriginDimension(field) ? originLabel(field, value) : labels.get(field)?.get(String(value)) ?? String(value);
  const built = useMemo(() => trendSeries(catalog, view, trend, result.results, { label }),
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
  const adjustments = trendAdjustments(catalog, { ...state, view: "trend" });
  if (view.norm && !result.loading && built.normPoints !== undefined && built.normPoints < view.normPeriods) adjustments.push(built.normPoints
    ? `The norm averages ${built.normPoints.toLocaleString()} earlier ${built.normPoints === 1 ? "point" : "points"}: fewer than ${view.normPeriods} have a value before the latest.`
    : "No earlier point has a value, so there is no norm yet.");
  const bounds = timeBounds(where, catalog.time);
  const title = trendTitle(catalog, view);
  const controls = <AnalysisControls catalog={catalog} view={view} set={set} onView={onView} bounds={bounds} onRange={(from, to, preset) => onTimeFilter(current => withTimeRange(current, catalog.time, from, to, preset), preset)} />;
  return <section className="usage-analysis-trend" aria-label="Over time" aria-busy={result.loading}>
    {onView ? <CreateDashboardCard catalog={catalog} state={state} where={where} /> : null}
    <StackedColumns title={title} controls={controls} series={built.series.length ? built.series : [{ key: "none", label: title, color: "var(--heat-1)" }]} buckets={built.buckets} prepared
      period={view.period} periodCount={count} rollingPeriods={view.rollingPeriods} display={view.display} cumulativeStart={built.cumulativeStart} scale={view.scale}
      rolling={kind === "ratio" ? "pooled" : view.rollingOp === "sum" ? "sum" : "average"} lines={built.lines} additive={built.additive}
      reference={built.norm === undefined ? undefined : { value: built.norm, label: "Norm" }}
      format={amount} tickFormat={tickAmount} noun={share || ratio || measure.unit === "USD" ? "" : view.display !== "cumulative" && view.rollingOp === "sum" && view.rollingPeriods > 1 ? rollingSumUnit(measure, view.period) : measure.unit}
      loading={result.loading} error={result.error || (result.loading ? "" : built.error ?? "")} legend={built.series.length > 0} percent={share}
      selected={built.selected} excluded={built.excluded} onSelect={select} onExpand={() => set({ seriesLimit: Math.min(1000, view.seriesLimit + 5) })}
      onRange={pickRange(onTimeFilter, catalog.time, view.period, count, period => set({ period }))} seriesHref={href} />
    {adjustments.length ? <p className="usage-dashboard-note" role="status">{adjustments.join(" ")}</p> : null}
    {children}
    {links ? <>
      <UsageDashboardLinks schema={schema} dataset={dataset} query={trend.supporting} currentURL={currentURL} />
      <p className="usage-dashboard-note">{trendNote(catalog, view)} Click a column or point to pin its key, then follow a series to its contributing records. Drag across two or more periods to filter dates. Select legend entries to compare or exclude series. Grouped rows show the interval amounts before smoothing, accumulation, shares or folding{timestampHours(catalog, view) ? ", one row per UTC clock hour" : ""}.</p>
    </> : null}
  </section>;
}
