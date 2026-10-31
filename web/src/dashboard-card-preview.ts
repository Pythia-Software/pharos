import type { AggregationResultEntry, FieldSchema } from "@pythia-software/query-table-core";
import factors from "../../carbon/co2_factors.json";
import { analysisCatalogFor } from "./analysis-catalogs";
import { runAnalysisQuery, type LocalRows } from "./analysis-request";
import { analysisEmptyLabel, analysisNote, analysisQuery, cycleResult, nativeCycleResult, selectGroups } from "./dashboard-analysis-model";
import { trendQuery, trendSeries, trendTitle } from "./dashboard-trend-model";
import { readDashboardCard } from "./dashboard-card-model";
import { timeBounds } from "./chart-range";
import { normalizeConcurrencySettings, readConcurrencySettings } from "./concurrency-settings";
import { isOriginDimension, loadOriginMetadata, originLabel } from "./origin-dimensions";
import { optionLabels } from "./option-labels";
import { toolMetricPresentation } from "./tools-dashboard-model";
import type { CarbonFactors } from "./carbon-analysis-catalog";
import type { QueryPreview } from "./query-previews";
import { hoursOfDay, weekdays } from "./weekday-cover";

export async function loadDashboardCardPreview(address: string, schema: FieldSchema, signal?: AbortSignal, local?: LocalRows, now = Date.now()): Promise<QueryPreview> {
  const { topic, dataset, state, where, params } = readDashboardCard(address, new Date(now));
  if (schema.name !== `pharos_${dataset}`) throw new Error("This dashboard card's dataset is unavailable.");
  const calculator = window.pharosCarbon;
  if (topic === "carbon" && !calculator) throw new Error("Carbon assumptions are still loading. Refresh this card to try again.");
  const settings = topic === "carbon" ? JSON.parse(params.get("analyze_carbon") ?? "null") ?? calculator!.settingsSnapshot(factors) : undefined;
  if (topic === "carbon" && (!settings || typeof settings !== "object" || Array.isArray(settings))) throw new Error("This card's Carbon assumptions are invalid.");
  const parallelism = params.has("analyze_parallelism") ? normalizeConcurrencySettings(JSON.parse(params.get("analyze_parallelism")!)) : readConcurrencySettings();
  const catalog = analysisCatalogFor(topic, {
    parallelism: parallelism.counting,
    carbon: calculator && settings ? { settings, factors: factors as CarbonFactors, estimate: (factors, period) => calculator.estimate(factors, period, settings) } : undefined,
  });
  if (!catalog) throw new Error("This dashboard card is unavailable.");
  const fields = catalog.breakdowns[state.split]?.fields ?? [];
  if (fields.some(isOriginDimension)) await loadOriginMetadata();
  const labels = optionLabels(schema.fields);
  const label = (field: string, value: unknown) => isOriginDimension(field) ? originLabel(field, value) : catalog.labels?.[field]?.(String(value)) ?? labels.get(field)?.get(String(value)) ?? String(value ?? "Unspecified");
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  const request = { ...catalog.request?.params, ...topic === "parallelism" ? { parallelism_settings: JSON.stringify({ counting: parallelism.counting, gap: parallelism.gap }) } : {} };
  const run = (query: Parameters<typeof runAnalysisQuery>[2]) => runAnalysisQuery(dataset, schema, query, { signal, local, params: request, now });
  if (state.view === "trend") {
    const trend = trendQuery(catalog, state, where, { now, zone });
    const built = trendSeries(catalog, state, trend, await run(trend.query), { now, label });
    if (built.error) throw new Error(built.error);
    const percent = state.share || catalog.measures[state.metric].format.kind === "percent";
    // The engine has already applied smoothing, cumulative totals and shares.
    // Retain only the selected series and never smooth these amounts twice.
    const series = built.series.filter(item => !built.selected.length || built.selected.includes(item.key) !== built.excluded);
    const ready = built.buckets.some(bucket => series.some(item => bucket.values[item.key] !== undefined));
    return { state: ready ? "ready" : "empty", total: built.buckets.length, summary: trendTitle(catalog, state), action: "Explore this dashboard",
      buckets: built.buckets, series, note: analysisNote(catalog, state),
      dashboardTrend: { prepared: true, lines: built.lines || state.display === "cumulative", percent: state.share, scale: state.scale, reference: built.norm,
        unit: percent ? "percent" : catalog.measures[state.metric].format.kind === "duration" ? "ms" : catalog.measures[state.metric].unit,
        xLabel: state.period, yLabel: trendTitle(catalog, state) } };
  }
  const analysis = analysisQuery(catalog, state, where, { now, zone });
  if (analysis.problem) throw new Error(analysis.problem);
  const result = (await run(analysis.query))[0];
  if (!result) throw new Error("The dashboard returned no chart result.");
  let clause = analysis.clause, entry = selectGroups(result, analysis.selection);
  if (entry.error) throw new Error(entry.error);
  if (state.view === "cycle") {
    if (analysis.native) nativeCycleResult(analysis, entry);
    else {
      const bounds = timeBounds(where, catalog.time, now);
      const folded = cycleResult(catalog, state, entry, { from: bounds.from?.getTime(), to: Math.min(bounds.to?.getTime() ?? now, now) }, zone);
      clause = folded.clause; entry = folded.result;
    }
  }
  const presented = toolMetricPresentation(entry, clause.groupBy, catalog.labels).metric as AggregationResultEntry;
  const ready = entry.buckets.some(bucket => bucket.distribution?.kind === "box" ? Boolean(bucket.distribution.summary) : bucket.distribution?.kind === "histogram" ? bucket.distribution.n > 0 : bucket.value !== null);
  return { state: ready ? "ready" : "empty", total: entry.processedRows ?? entry.buckets.length, summary: clause.label ?? "Dashboard chart", action: "Explore this dashboard",
    emptyLabel: analysisEmptyLabel(catalog, state), note: analysisNote(catalog, state), metrics: [{ clause, result: presented }], coverMetricId: clause.id, preserveScale: true,
    groupLabels: state.view === "cycle" && analysis.native ? Object.fromEntries(entry.buckets.map(bucket => [JSON.stringify(bucket.keys), state.cycle === "hour" ? hoursOfDay[Number(bucket.keys[0])] : weekdays[Number(bucket.keys[0]) - 1]])) : undefined,
    fields: [...schema.fields, { name: "hour_of_day", label: "Local hour", type: "text", source: { kind: "derived" } }, { name: "weekday", label: "Weekday", type: "text", source: { kind: "derived" } }] };
}
