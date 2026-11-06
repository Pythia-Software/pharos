import { EMPTY_QUERY, type FieldSchema, type WhereTerm } from "@pythia-software/query-table-core";
import { isTrendView, noSplit, normalizeAnalysis, records, reductionLabel, sampleSubject, thresholded, type AnalysisCatalog, type AnalysisState } from "./dashboard-analysis-model";
import { trendPeriodCount, trendTitle } from "./dashboard-trend-model";
import { chartPeriods, periodLabel } from "./chart-period";
import { dashboardFilterDescriptions } from "./dashboard-query-links";

export type DashboardCardCopy = { title: string; description: string };
const lower = (text: string) => /^[A-Z]{2}/.test(text) ? text : text[0].toLowerCase() + text.slice(1);
// Defaults must always fit the same limits as an author's edited copy. The
// complete definition remains in the URL even when its reading needs an ellipsis.
export const fitCardCopy = (text: string, limit: number) => text.length <= limit ? text : `${text.slice(0, limit - 1).trimEnd()}…`;

// Shared by every catalog, including future ones. Name what is actually drawn,
// rather than assuming every view is a total or a trend.
export function analysisCardTitle(catalog: AnalysisCatalog, input: AnalysisState): string {
  const state = normalizeAnalysis(catalog, input), measure = catalog.measures[state.metric];
  const unit = catalog.units[state.unit], subject = unit?.label.toLowerCase() ?? catalog.records.label.toLowerCase();
  const split = (catalog.breakdowns[state.split] ?? catalog.components?.[state.split])?.label, by = split ? ` by ${split.toLowerCase()}` : "";
  if (isTrendView(state.view)) return `${state.display !== "cumulative" && state.rollingPeriods > 1 ? `${state.rollingPeriods * trendPeriodCount(state)}-${chartPeriods[state.period].unit} ${state.rollingOp === "sum" ? "total" : measure.ratio ? "pooled average" : "average"}: ` : ""}${trendTitle(catalog, state)}`;
  switch (state.view) {
    case "scatter": return `${measure.label} vs ${lower(catalog.measures[state.metricY].label)} per ${subject}${by}`;
    case "distribution": return `${measure.label} distribution per ${subject}${by}`;
    case "cycle": return `${state.reduce === "median" ? "Distribution of" : state.reduce === "total" ? "Total" : measure.additive ? "Mean" : "Unweighted mean"} ${lower(measure.label)} by ${state.cycle === "hour" ? "hour of day" : "weekday"}`;
    case "compare": {
      const value = unit ? state.reduce === "count" ? `Number of ${unit.plural.toLowerCase()}`
        : state.reduce === "total" ? measure.label : `${reductionLabel(state.reduce, measure, unit)}: ${lower(measure.label)}` : measure.label;
      return `${state.compareAs === "share" ? `Share of ${lower(value)}` : value}${by}`;
    }
  }
}

export function analysisCardSelection(catalog: AnalysisCatalog, input: AnalysisState): string {
  const state = normalizeAnalysis(catalog, input), split = catalog.breakdowns[state.split];
  // A selection survives visits to views without a selectable breakdown.
  // Component trends and comparisons select parts; other breakdowns select groups.
  const components = (isTrendView(state.view) || state.view === "compare") && catalog.components?.[state.split];
  if (state.view === "cycle" || !split?.fields.length && !components || !state.selection?.values.length) return "";
  return `${state.selection.excluded ? "Exclude" : "Only"} ${split?.label.toLowerCase() ?? "series"}: ${state.selection.values.map(value => {
    if (split?.values?.[value]) return split.values[value];
    if (split && split.fields.length > 1) {
      try { return (JSON.parse(value) as unknown[]).map((part, i) => catalog.labels?.[split.fields[i]]?.(String(part)) ?? String(part ?? "Unspecified")).join(" / "); } catch { /* Retain a legacy key. */ }
    }
    if (components) return components.parts.find(part => part.key === value)?.label ?? value;
    return split?.fields.length === 1 ? catalog.labels?.[split.fields[0]]?.(value) ?? value : value;
  }).join(", ")}`;
}

export function analysisCardContext(catalog: AnalysisCatalog, input: AnalysisState, groups?: number): string[] {
  const state = normalizeAnalysis(catalog, input), measure = catalog.measures[state.metric], trend = isTrendView(state.view);
  // Share trends force a linear percentage axis; pies and scalar comparisons
  // have no axis. Their retained scale still applies when switching back.
  const usesScale = trend ? !state.share : state.view !== "compare" || state.split !== noSplit && state.compareAs !== "share";
  return [
    analysisCardSelection(catalog, state),
    thresholded(state, catalog) ? `Minimum ${state.minSamples} ${sampleSubject(catalog, state)} per group` : "",
    trend ? `${state.display === "cumulative" ? "Cumulative" : "Interval"} values per ${periodLabel(state.period, trendPeriodCount(state))}`
      : state.view === "cycle" ? "Whole calendar periods, including idle periods" : `One observation per ${state.unit === records ? catalog.records.label.toLowerCase() : catalog.units[state.unit].label.toLowerCase()}`,
    trend && state.completePeriods ? "Whole periods only" : "",
    trend && state.nonzero && !state.share ? "Zero values omitted" : "",
    trend && state.norm ? `Reference: mean of ${state.normPeriods} earlier periods` : "",
    usesScale && state.scale === "logarithmic" ? "Logarithmic scale" : "",
    state.view === "scatter" && state.scaleY === "logarithmic" ? "Logarithmic Y scale" : "",
    groups ? `Show up to ${groups} groups` : "",
    measure.note,
    catalog.breakdowns[state.split]?.note ?? "",
    catalog.request?.note ?? "",
  ].filter(Boolean);
}

export function analysisCardCopy(catalog: AnalysisCatalog, state: AnalysisState, where: WhereTerm[], schema?: Pick<FieldSchema, "fields">, groups?: number, context: string[] = []): DashboardCardCopy {
  const selection = analysisCardSelection(catalog, state);
  return dashboardCardCopy(`${analysisCardTitle(catalog, state)}${selection ? ` · ${selection}` : ""}`, [...context, ...analysisCardContext(catalog, state, groups)], where, schema, [catalog.time.first, catalog.time.last]);
}

// Filters are read from the saved definition, never from ambient page headings.
// Keep AND/OR groups and negations in the same grammar as supporting queries.
export function dashboardCardCopy(title: string, context: string[], where: WhereTerm[] = [], schema?: Pick<FieldSchema, "fields">, timeFields: string[] = []): DashboardCardCopy {
  const filters = dashboardFilterDescriptions(schema ?? { fields: [] }, { ...EMPTY_QUERY, where }, where);
  const population = where.filter(term => !("field" in term && timeFields.includes(term.field) && !term.negated && [">=", ">", "<", "<="].includes(term.op)));
  const scope = dashboardFilterDescriptions(schema ?? { fields: [] }, { ...EMPTY_QUERY, where: population }, population).join(" and ");
  return {
    title: fitCardCopy(`${title}${scope ? ` · ${fitCardCopy(scope, 80)}` : ""}`, 200),
    description: fitCardCopy([...filters.length ? [`Filters: ${filters.join(" and ")}.`] : [], ...context].filter(Boolean).join(" · "), 500),
  };
}
