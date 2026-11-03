import type { AggregationResultEntry, FieldSchema } from "@pythia-software/query-table-core";
import factors from "../../carbon/co2_factors.json";
import { analysisCatalogFor } from "./analysis-catalogs";
import { runAnalysisQuery, type LocalRows } from "./analysis-request";
import { analysisEmptyLabel, analysisNote, analysisQuery, cycleResult, nativeCycleResult, selectGroups } from "./dashboard-analysis-model";
import { trendQuery, trendSeries, trendTitle } from "./dashboard-trend-model";
import { DASHBOARD_CARD_ACTION, dashboardCardGroups, dashboardCardRibbon, readDashboardCard, type DashboardCardTarget } from "./dashboard-card-model";
import { timeBounds } from "./chart-range";
import { normalizeConcurrencySettings, readConcurrencySettings } from "./concurrency-settings";
import { loadConcurrencyData } from "./concurrency-store";
import type { ConcurrencyData } from "./concurrency-data";
import { ribbonCardTitle, ribbonCover } from "./parallelism-ribbon-card";
import { habitsCardPreview, isHabitsCardTarget, loadHabitsSummary, readHabitsCardView } from "./habits-card";
import type { HabitsSummary } from "./habits-model";
import { isOriginDimension, loadOriginMetadata, originLabel } from "./origin-dimensions";
import { optionLabels } from "./option-labels";
import { toolMetricPresentation } from "./tools-dashboard-model";
import { fittedPreview } from "./card-presentation";
import type { CarbonFactors } from "./carbon-analysis-catalog";
import type { QueryPreview } from "./query-previews";
import { hoursOfDay, weekdays } from "./weekday-cover";

// A dashboard address carries no authored cover, so its card fits the chart.
// A card's own group count replaces the dashboard's for its chart only.
// A ribbon card reads the captured timing snapshot the ribbon draws, and a
// Habits card the habits summary its section draws (timing and habits stand
// in for them in tests).
export async function loadDashboardCardPreview(address: string, schema: FieldSchema, signal?: AbortSignal, local?: LocalRows, now = Date.now(), target: DashboardCardTarget = "chart", timing?: ConcurrencyData, habits?: HabitsSummary, groups?: number): Promise<QueryPreview> {
  if (isHabitsCardTarget(target)) {
    const view = readHabitsCardView(readDashboardCard(address, new Date(now)).params);
    return habitsCardPreview(target, view, habits ?? await loadHabitsSummary(view, signal));
  }
  return fittedPreview(target === "ribbon" ? ribbonPreview(address, timing ?? await loadConcurrencyData(), now) : await dashboardPreview(address, schema, signal, local, now, groups));
}

function ribbonPreview(address: string, data: ConcurrencyData, now: number): QueryPreview {
  readDashboardCard(address, new Date(now));
  const settings = dashboardCardRibbon(address), cover = ribbonCover(data, settings, new Date(now));
  const ready = cover.buckets.some(bucket => Object.keys(bucket.values).length);
  return { state: ready ? "ready" : "empty", total: cover.buckets.length, summary: ribbonCardTitle(settings), action: DASHBOARD_CARD_ACTION,
    emptyLabel: "No captured activity in this range", buckets: cover.buckets, series: cover.series,
    note: `Average concurrent ${settings.counting === "agent" ? "sessions" : "workspaces"} in each period, including idle time, by repository.`,
    dashboardTrend: { prepared: true, xLabel: cover.hourly ? "hours" : "day", yLabel: cover.label, unit: "" } };
}

async function dashboardPreview(address: string, schema: FieldSchema, signal?: AbortSignal, local?: LocalRows, now = Date.now(), groups?: number): Promise<QueryPreview> {
  const opened = readDashboardCard(address, new Date(now)), { topic, dataset, where, params } = opened;
  const counted = groups !== undefined && dashboardCardGroups(address);
  const state = counted && opened.state.view === "trend" ? { ...opened.state, seriesLimit: groups } : opened.state;
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
    return { state: ready ? "ready" : "empty", total: built.buckets.length, summary: trendTitle(catalog, state), action: DASHBOARD_CARD_ACTION,
      buckets: built.buckets, series, note: analysisNote(catalog, state),
      dashboardTrend: { prepared: true, lines: built.lines || state.display === "cumulative", percent: state.share, nonzero: state.nonzero && !state.share, scale: state.scale, reference: built.norm,
        unit: percent ? "percent" : catalog.measures[state.metric].format.kind === "duration" ? "ms" : catalog.measures[state.metric].unit,
        xLabel: state.period, yLabel: trendTitle(catalog, state) } };
  }
  const analysis = analysisQuery(catalog, state, where, { now, zone });
  if (analysis.problem) throw new Error(analysis.problem);
  const result = (await run(analysis.query))[0];
  if (!result) throw new Error("The dashboard returned no chart result.");
  let clause = counted ? { ...analysis.clause, display: { ...analysis.clause.display!, maxGroups: groups } } : analysis.clause, entry = selectGroups(result, analysis.selection);
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
  return { state: ready ? "ready" : "empty", total: entry.processedRows ?? entry.buckets.length, summary: clause.label ?? "Dashboard chart", action: DASHBOARD_CARD_ACTION,
    emptyLabel: analysisEmptyLabel(catalog, state), note: analysisNote(catalog, state), metrics: [{ clause, result: presented }], coverMetricId: clause.id, preserveScale: true,
    groupLabels: state.view === "cycle" && analysis.native ? Object.fromEntries(entry.buckets.map(bucket => [JSON.stringify(bucket.keys), state.cycle === "hour" ? hoursOfDay[Number(bucket.keys[0])] : weekdays[Number(bucket.keys[0]) - 1]])) : undefined,
    fields: [...schema.fields, { name: "hour_of_day", label: "Local hour", type: "text", source: { kind: "derived" } }, { name: "weekday", label: "Weekday", type: "text", source: { kind: "derived" } }] };
}
