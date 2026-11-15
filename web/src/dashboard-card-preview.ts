import { hasUsageEstimates, estimateExplanation, durationEstimateExplanation } from "./usage-estimate-model";
import { EMPTY_QUERY, type AggregationResultEntry, type FieldSchema } from "@pythia-software/query-table-core";
import factors from "../../carbon/co2_factors.json";
import { analysisCatalogFor } from "./analysis-catalogs";
import { runAnalysisQuery, type LocalRows } from "./analysis-request";
import { isTrendView, analysisEmptyLabel, analysisNote, analysisQuery, cycleResult, nativeCycleResult, analysisResult, analysisLabels, analysisFields, heatmapAxis, heatmapSource, normalizeAnalysis, trendDrawing } from "./dashboard-analysis-model";
import { heatmapGrid, trendHeatmap } from "./dashboard-heatmap-model";
import { formatMetricOutput } from "@pythia-software/query-table-ui";
import { trendQuery, trendSeries, trendTitle } from "./dashboard-trend-model";
import { DASHBOARD_CARD_ACTION, dashboardCardGroups, dashboardCardRibbon, readDashboardCard, type DashboardCardTarget } from "./dashboard-card-model";
import { timeBounds } from "./chart-range";
import { normalizeConcurrencySettings, readConcurrencySettings } from "./concurrency-settings";
import { loadConcurrencyData } from "./concurrency-store";
import type { ConcurrencyData } from "./concurrency-data";
import { ribbonCardTitle, ribbonCover } from "./parallelism-ribbon-card";
import { habitsCardPreview, isHabitsCardTarget, loadHabitsSummary, readHabitsCard } from "./habits-card";
import type { HabitsSummary } from "./habits-model";
import { orderThinkingGroups, type ThinkingResponse } from "./thinking-model";
import { loadThinkingData } from "./thinking-query";
import { gridCoverHeight } from "./grid-cover";
import { isOriginDimension, loadOriginMetadata, originLabel } from "./origin-dimensions";
import { optionLabels } from "./option-labels";
import { toolMetricPresentation } from "./tools-dashboard-model";
import { fittedPreview } from "./card-presentation";
import { defaultCardCover } from "./card-cover";
import type { CarbonFactors } from "./carbon-analysis-catalog";
import { loadQueryPreviews, type QueryPreview } from "./query-previews";
import { compactionColumns, isThinkingCardTarget, orderedBoxes, readThinkingCard, thinkingCardRecipe, thinkingCardTitle, thinkingGrid, thinkingGridView, thinkingPageOrder, type ThinkingCardTarget } from "./thinking-card";
import { hoursOfDay, weekdays } from "./weekday-cover";

// A dashboard address carries no authored cover, so its card fits the chart.
// A card's own group count replaces the dashboard's for its chart only.
// A ribbon card reads the captured timing snapshot the ribbon draws, and a
// Habits card the habits summary its section draws (timing and habits stand
// in for them in tests). A Thinking card draws as the Thinking card with its
// measures does, over the page's population. A card's own group count
// replaces the dashboard's for its chart only.
export async function loadDashboardCardPreview(address: string, schema: FieldSchema, signal?: AbortSignal, local?: LocalRows, now = Date.now(), target: DashboardCardTarget = "chart", timing?: ConcurrencyData, habits?: HabitsSummary, thinking?: ThinkingResponse, groups?: number): Promise<QueryPreview> {
  if (isHabitsCardTarget(target)) {
    const view = readHabitsCard(address);
    return habitsCardPreview(target, view, habits ?? await loadHabitsSummary(view, signal));
  }
  if (isThinkingCardTarget(target)) return thinkingPreview(address, schema, target, signal, now, thinking);
  return fittedPreview(target === "ribbon" ? ribbonPreview(address, timing ?? await loadConcurrencyData(), now) : await dashboardPreview(address, schema, signal, local, now, groups));
}

async function thinkingPreview(address: string, schema: FieldSchema, target: ThinkingCardTarget, signal: AbortSignal | undefined, now: number, thinking?: ThinkingResponse): Promise<QueryPreview> {
  const { state, where, native, filters } = readThinkingCard(address, new Date(now));
  const load = () => loadThinkingData({ state, overview: false, where: native, filters, now }, signal);
  if (thinkingGridView(target, state)) {
    const data = thinking ?? await load();
    const grid = thinkingGrid(data, state), ready = Boolean(grid?.cells.some(row => row.some(cell => cell !== null)));
    return { state: ready ? "ready" : "empty", total: data.Timed, summary: thinkingCardTitle(target, state), action: DASHBOARD_CARD_ACTION, emptyLabel: "No observations in this range",
      note: `${data.Timed.toLocaleString()} usable timing gaps from ${data.Retained.toLocaleString()} retained requests.`, ...grid && ready ? { grid, coverHeight: gridCoverHeight(grid) } : {} };
  }
  if (schema.name !== "pharos_model_requests") throw new Error("This dashboard card's dataset is unavailable.");
  const recipe = thinkingCardRecipe(target, state);
  const query = { ...EMPTY_QUERY, where: [...recipe.where ?? [], ...where], aggregations: recipe.measures };
  let drawn: QueryPreview | undefined, failure: unknown;
  await loadQueryPreviews([{ id: target, dataset: "model_requests", query, schema, options: { recipe, cover: defaultCardCover(recipe) } }], (_, preview, error) => { drawn = preview; failure = error; }, signal);
  if (!drawn) throw failure instanceof Error ? failure : new Error(String(failure ?? "The dashboard returned no chart result."));
  const result = drawn.metrics?.[0]?.result;
  if (result && thinkingPageOrder(target, state)) {
    const groups = orderThinkingGroups((thinking ?? await load()).groups, state);
    drawn = { ...drawn, metrics: drawn.metrics!.map((metric, i) => i ? metric : { ...metric, result: orderedBoxes(result, groups) }) };
  }
  if (target !== "thinking-compactions" || !result) return fittedPreview({ ...drawn, action: DASHBOARD_CARD_ACTION, preserveScale: true });
  const { buckets, series } = compactionColumns(result);
  return fittedPreview({ state: buckets.length ? "ready" : "empty", total: buckets.length, summary: "Compactions by day", note: drawn.note, action: DASHBOARD_CARD_ACTION,
    emptyLabel: "No compactions in this range", buckets, series, dashboardTrend: { prepared: true, xLabel: "day", yLabel: "Compactions", unit: "" } });
}


function ribbonPreview(address: string, data: ConcurrencyData, now: number): QueryPreview {
  readDashboardCard(address, new Date(now));
  const settings = dashboardCardRibbon(address), cover = ribbonCover(data, settings, new Date(now));
  const ready = cover.buckets.some(bucket => Object.keys(bucket.values).length);
  return { state: ready ? "ready" : "empty", total: cover.buckets.length, summary: ribbonCardTitle(settings), action: DASHBOARD_CARD_ACTION,
    emptyLabel: "No captured activity in this range", buckets: cover.buckets, series: cover.series,
    note: `Average concurrent ${settings.counting === "agent" ? "conversations" : "workspaces"} in each period, including idle time, by repository.`,
    dashboardTrend: { prepared: true, xLabel: cover.hourly ? "hours" : "day", yLabel: cover.label, unit: "" } };
}

async function dashboardPreview(address: string, schema: FieldSchema, signal?: AbortSignal, local?: LocalRows, now = Date.now(), groups?: number): Promise<QueryPreview> {
  const opened = readDashboardCard(address, new Date(now)), { topic, dataset, where, params } = opened;
  const counted = groups !== undefined && dashboardCardGroups(address);
  const state = counted && isTrendView(opened.state.view) ? { ...opened.state, seriesLimit: groups } : opened.state;
  if (schema.name !== `pharos_${dataset}`) throw new Error("This dashboard card's dataset is unavailable.");
  const calculator = window.pharosCarbon;
  if (topic === "carbon" && !calculator) throw new Error("Carbon assumptions are still loading. Refresh this card to try again.");
  const settings = topic === "carbon" ? JSON.parse(params.get("analyze_carbon") ?? "null") ?? calculator!.settingsSnapshot(factors) : undefined;
  if (topic === "carbon" && (!settings || typeof settings !== "object" || Array.isArray(settings))) throw new Error("This card's Carbon assumptions are invalid.");
  const parallelism = params.has("analyze_parallelism") ? normalizeConcurrencySettings(JSON.parse(params.get("analyze_parallelism")!)) : readConcurrencySettings();
  const catalog = analysisCatalogFor(topic, {
    parallelism: parallelism.counting,
    carbon: calculator && settings ? { settings, factors: factors as CarbonFactors, estimate: (factors, period) => calculator.estimate(factors, period, settings) } : undefined,
  },state);
  if (!catalog) throw new Error("This dashboard card is unavailable.");
  const fields = catalog.breakdowns[state.split]?.fields ?? [];
  if (fields.some(isOriginDimension)) await loadOriginMetadata();
  const labels = optionLabels(schema.fields);
  const label = (field: string, value: unknown) => isOriginDimension(field) ? originLabel(field, value) : catalog.labels?.[field]?.(String(value)) ?? labels.get(field)?.get(String(value)) ?? String(value ?? "Unspecified");
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  const request = { ...catalog.request?.params, ...topic === "parallelism" ? { parallelism_settings: JSON.stringify({ counting: parallelism.counting, gap: parallelism.gap }) } : {} };
  let estimated = false;
  const run = async (query: Parameters<typeof runAnalysisQuery>[2]) => {
    const result = await runAnalysisQuery(dataset, schema, query, { signal, local, params: request, now });
    estimated ||= hasUsageEstimates(result);
    return result;
  };
  const estimateNote = () => estimated ? ` ${["tools", "tool_calls"].includes(dataset) ? durationEstimateExplanation : estimateExplanation}` : "";

  if (isTrendView(state.view)) {
    const drawn = normalizeAnalysis(catalog, state), drawing = trendDrawing(catalog, drawn);
    // A heatmap is drawn as the dashboard draws it, as a grid of cells.
    if (drawn.display === "heatmap") {
      const axis = heatmapAxis(catalog, drawn)!, source = heatmapSource(catalog, drawn), weight = source.catalog.measures[source.state.metric];
      const trend = trendQuery(source.catalog, source.state, where, { now, zone });
      const built = trendSeries(source.catalog, { ...source.state, nonzero: false }, trend, await run(trend.query), { now, label });
      if (built.error) throw new Error(built.error);
      const value = (amount: number) => formatMetricOutput(amount, axis.bins.format).text;
      const grid = heatmapGrid(trendHeatmap(built.buckets, built.series, axis.bins.scheme), { period: drawn.period, noun: axis.unit ? catalog.units[axis.unit].plural.toLowerCase() : axis.counted ? catalog.records.plural.toLowerCase() : weight.unit, label: axis.bins.label,
        rollingPeriods: drawn.rollingPeriods, format: value, amount: axis.counted || weight.op === "count" ? undefined : amount => formatMetricOutput(amount, weight.format).text });
      const ready = grid.cells.some(row => row.some(cell => cell !== null));
      return { state: ready ? "ready" : "empty", total: built.buckets.length, summary: trendTitle(catalog, drawn)+(estimated?"*":""), action: DASHBOARD_CARD_ACTION, note: analysisNote(catalog, drawn)+estimateNote(),
        ...ready ? { grid, coverHeight: gridCoverHeight(grid) } : {} };
    }
    const trend = trendQuery(catalog, drawn, where, { now, zone });
    const built = trendSeries(catalog, drawn, trend, await run(trend.query), { now, label });
    if (built.error) throw new Error(built.error);
    const percent = drawn.share || catalog.measures[drawn.metric].format.kind === "percent";
    // The engine has already applied smoothing, cumulative totals and shares.
    // Retain only the selected series and never smooth these amounts twice.
    const series = built.allHidden?[]:built.series.filter(item => !built.selected.length || built.selected.includes(item.key) !== built.excluded);
    const ready = built.buckets.some(bucket => series.some(item => bucket.values[item.key] !== undefined));
    return { state: ready ? "ready" : "empty", total: built.buckets.length, summary: trendTitle(catalog, drawn)+(estimated?"*":""), action: DASHBOARD_CARD_ACTION,
      buckets: built.buckets, series, note: analysisNote(catalog, drawn)+estimateNote(),
      dashboardTrend: { prepared: true, lines: drawing !== "trend_bar" && (built.lines || drawn.display === "cumulative"), bars: drawing === "trend_bar", grouped: drawing === "trend_bar" && (!built.additive || drawn.scale === "logarithmic" && !drawn.share), additive: built.additive, percent: drawn.share, nonzero: drawn.nonzero && !drawn.share, scale: drawn.scale, references: built.references,
        unit: percent ? "percent" : catalog.measures[drawn.metric].format.kind === "duration" ? "ms" : catalog.measures[drawn.metric].unit,
        xLabel: drawn.period, yLabel: trendTitle(catalog, drawn) } };
  }
  const analysis = analysisQuery(catalog, state, where, { now, zone });
  if (analysis.problem) throw new Error(analysis.problem);
  const result = analysisResult(analysis, await run(analysis.query));
  if (!result) throw new Error("The dashboard returned no chart result.");
  let clause = counted ? { ...analysis.clause, display: { ...analysis.clause.display!, maxGroups: groups } } : analysis.clause, entry = result;
  if (entry.error) throw new Error(entry.error);
  if (state.view === "cycle") {
    if (analysis.native) nativeCycleResult(analysis, entry);
    else {
      const bounds = timeBounds(where, catalog.time, now);
      const folded = cycleResult(catalog, state, entry, { from: bounds.from?.getTime(), to: Math.min(bounds.to?.getTime() ?? now, now) }, zone);
      clause = folded.clause; entry = folded.result;
    }
  }
  const presented = toolMetricPresentation(entry, clause.groupBy, analysisLabels(catalog, state)).metric as AggregationResultEntry;
  const ready = Boolean(analysis.components && Number(entry.other?.value) > 0) || entry.buckets.some(bucket => analysis.components ? Number(bucket.value) > 0 : bucket.distribution?.kind === "box" ? Boolean(bucket.distribution.summary) : bucket.distribution?.kind === "histogram" ? bucket.distribution.n > 0 : bucket.value !== null);
  return { state: ready ? "ready" : "empty", total: entry.processedRows ?? entry.buckets.length, summary: (clause.label ?? "Dashboard chart")+(estimated?"*":""), action: DASHBOARD_CARD_ACTION,
    emptyLabel: analysisEmptyLabel(catalog, state), note: analysisNote(catalog, state)+estimateNote(), metrics: [{ clause, result: presented }], coverMetricId: clause.id, preserveScale: true,
    groupLabels: state.view === "cycle" && analysis.native ? Object.fromEntries(entry.buckets.map(bucket => [JSON.stringify(bucket.keys), state.cycle === "hour" ? hoursOfDay[Number(bucket.keys[0])] : weekdays[Number(bucket.keys[0]) - 1]])) : undefined,
    fields: [...analysisFields(catalog, state, schema), { name: "hour_of_day", label: "Local hour", type: "text", source: { kind: "derived" } }, { name: "weekday", label: "Weekday", type: "text", source: { kind: "derived" } }] };
}
