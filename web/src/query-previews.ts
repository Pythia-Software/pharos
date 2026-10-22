import { apiCostTrendQuery, apiCostTrendFacts, buildApiCostTrend, usesApiCostTrend, type CostTimeBucket } from "./api-cost-trend";
import { formatMetricOutput } from "@pythia-software/query-table-ui";
import { metricPopulationError, parseRelativeDuration, createGroupingModel, sourceExecutionSchema, toQueryRequestV3, validateQueryResultV3, validateComputedColumn, toMetricQuery, toServerQueryV2, type QueryCapabilitiesV3, type QueryResultV3, type ComputedColumn, type AggregationClause, type AggregationResult, type AggregationResultEntry, type ComputedExecution, type FieldSchema, type MetricCapabilities, type MetricQuery, type QueryState } from "@pythia-software/query-table-core";
import { categoryForQuery, queryCategories } from "./query-categories";
import { chartBucketsWithHistory, foldSeries, rollingAverageBuckets, validRollingPeriods, type ChartBucket, type ChartSeries } from "./chart-series";
import { earliestKey, parseDay, shiftPeriod } from "./chart-period";
import { resolveRollingQuery, rollingQueryMatches, rollingWindowField, type QueryDataset, type RollingWindow } from "./query-links";
import type { QueryRecipe } from "./curiosity-data";
import { readTextSearch, textFindParameters } from "./text-search-model";
import { cardCoverQuery, humanStudyCoverMatches, type CardCover } from "./card-cover";
import { filterBoxSamples } from "./compact-box-model";
import { createHumanStudyLoader, clearHumanStudyCache, type HumanStudyPreview } from "./human-study";
import { activityPresentation } from "./machine-activity-data";
import { buildCardTrend, cardTrendDays, cardTrendQueries, trendSchemas, usesCardTrend } from "./card-trend";
import { usesWeekdayCover, weekdayMetric } from "./weekday-cover";

export type PreviewMetric = { clause: AggregationClause; result: AggregationResultEntry };
export type QueryPreview = {
  refreshing?: boolean; refreshError?: string; updatedAt?: number;
  state: "ready" | "empty"; emptyLabel?: string; density?: "compact"; stackBy?: string; coverHeight?: number; legendColumns?: 1 | 2; total: number; summary: string; note?: string;
  headline?: string; action?: string; evidence?: string;
  headlineMetric?: { value: string; unit: string; label?: string };
  groupLabels?: Record<string, string>;
  groupEvidence?: Record<string, string>;
  groupSummary?: { count: number; subject: string };
  insights?: { label: string; value: string; context: string }[];
  metrics?: PreviewMetric[]; fields?: FieldSchema["fields"]; coverMetricId?: string;
  ranking?: { label: string; value: number; color: string }[];
  buckets?: ChartBucket[]; series?: ChartSeries[]; excerpt?: string;
  timeline?: CardCover["timeline"];
  study?: HumanStudyPreview;
};
type PreviewOptions = { window?: RollingWindow; recipe?: QueryRecipe; url?: string; coverMetricId?: string; cover?: CardCover; suggested?: boolean; sourceCover?: boolean };
export type PreviewJob = { priority?: () => number; id: string; dataset: QueryDataset; query: QueryState; schema: FieldSchema; options?: PreviewOptions };
type Context = { schema: FieldSchema; execution: ComputedExecution; capabilities: MetricCapabilities; queryCapabilities?: QueryCapabilitiesV3 };
type Payload = { now?: number; metrics?: AggregationResult["metrics"]; rows?: Record<string, unknown>[]; total?: number; sourceTotal?: number; find?: { limited?: boolean; search?: { complete: boolean } } };
type Cached = { at: number; payload: Payload; schema: FieldSchema };
const cache = new Map<string, Cached>();
export function clearPreviewCache() { cache.clear(); clearHumanStudyCache(); }

export function formatPreviewValue(value: number, unit: string): string {
  if (unit === "bytes") return value >= 1048576 ? `${(value / 1048576).toFixed(1)} MiB` : value >= 1024 ? `${(value / 1024).toFixed(1)} KiB` : `${value} B`;
  if (unit === "percent") return `${(value * 100).toLocaleString(undefined, { maximumFractionDigits: 1 })}%`;
  if (unit === "USD") return value.toLocaleString(undefined, { style: "currency", currency: "USD", maximumFractionDigits: value >= 100 ? 0 : 2 });
  if (unit === "ms") return value >= 3_600_000 ? `${(value / 3_600_000).toFixed(1)}h` : value >= 60_000 ? `${(value / 60_000).toFixed(1)}m` : value >= 1000 ? `${(value / 1000).toFixed(1)}s` : `${Math.round(value)}ms`;
  return Intl.NumberFormat(undefined, { notation: value >= 10000 ? "compact" : "standard", maximumFractionDigits: 1 }).format(value);
}

class PreviewHTTPError extends Error {
  constructor(message: string, readonly status: number) { super(message); }
}
async function fetchPreview<T>(url: string, signal?: AbortSignal, request?: unknown): Promise<T> {
  const controller = new AbortController(), abort = () => controller.abort(signal?.reason);
  signal?.addEventListener("abort", abort, { once: true });
  if (signal?.aborted) abort();
  const timer = setTimeout(() => controller.abort(new Error("Preview timed out. Open the view to explore it directly.")), 15_000);
  try {
    const response = await fetch(url, { signal: controller.signal, ...(request === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(request) }) });
    const body = await response.json();
    if (!response.ok) throw new PreviewHTTPError(typeof body.error === "string" ? body.error : body.error?.message ?? "Preview unavailable", response.status);
    return body;
  } finally { clearTimeout(timer); signal?.removeEventListener("abort", abort); }
}

function searchContext(job: PreviewJob): string {
  const params = new URL(job.options?.url ?? "/", location.origin).searchParams, find = new URLSearchParams();
  if (job.dataset === "writing_messages" && params.has("message_query")) {
    const saved = readTextSearch(new URLSearchParams({ text_builder: params.get("message_query")! }));
    if (saved.error) throw new Error(saved.error);
    const search = saved.active && textFindParameters(saved.draft, params.get("message_cursor") ?? "");
    if (!search) throw new Error("This saved message search has unfinished rules. Open it to finish the search.");
    return `?${search}`;
  }
  if (job.dataset === "library" && (!params.has("kind") || params.get("kind") === "text")) {
    const saved = readTextSearch(params);
    if (saved.error) throw new Error(saved.error);
    if (!saved.active) return "";
    const search = textFindParameters(saved.draft, params.get("text_cursor") ?? "");
    if (!search) throw new Error("This saved search has unfinished rules. Open it to finish the search.");
    return `?${search}`;
  }
  if (job.dataset === "library" && params.get("search")) {
    find.set("find", params.get("search")!);
    for (const [source, target] of [["kind", "find_kind"], ["case", "find_case"], ["fuzzy", "find_fuzzy"], ["separators", "find_separators"]]) if (params.has(source)) find.set(target, params.get(source)!);
  }
  return find.size ? `?${find}` : "";
}

// Scatter counts are observations in the metric population: for a staged
// task chart these are root-task rows, not the underlying usage-fact count.
function filterScatterSamples(result: AggregationResultEntry, minimum?: number): AggregationResultEntry {
  if (!minimum) return result;
  const buckets = result.buckets.filter(bucket => bucket.error || bucket.yError || bucket.count >= minimum);
  return { ...result, buckets,
    groupCount: result.groupCount === undefined || result.groupCount === result.buckets.length ? buckets.length : result.groupCount,
    processedRows: buckets.reduce((n, bucket) => n + bucket.count, 0) };
}

const colorScopes = new Set(["provider", "effort", "model_family", "model_effort"]);
function keyLabel(schema: FieldSchema, field: string): ((key: unknown) => string) | undefined {
  if (field === "activity") return key => activityPresentation(String(key ?? "")).label;
  const values = schema.fields.find(item => item.name === field)?.filter?.values;
  if (colorScopes.has(field) || values?.source !== "static" || !values.options.some(option => typeof option === "object" && option.label !== option.value)) return;
  return key => { const option = values.options.find(option => typeof option === "object" && option.value === key); return typeof option === "object" ? option.label : String(key ?? "Unspecified"); };
}

const dailyColumns = (clause: AggregationClause) => !clause.expression && !clause.distribution && (!clause.display || clause.display.kind === "auto") && clause.groupBy[0] === "day" && clause.op === "sum";
function smoothingPeriods(job: PreviewJob, query: QueryState): number {
  const options = job.options, clause = query.aggregations?.find(metric => metric.id === options?.coverMetricId) ?? query.aggregations?.[0];
  const periods = (options?.cover ?? options?.recipe?.cover)?.timeline?.rollingPeriods;
  return clause && dailyColumns(clause) && validRollingPeriods(periods) && periods > 1 ? periods : 0;
}

function stamp(value: string, now: number): Date {
  const relative = parseRelativeDuration(value);
  return new Date(relative === null ? value : now + relative);
}
function lowerBound(query: QueryState, field: string, now: number): Date | undefined {
  const source = query.grouping?.source ?? query;
  const dates = source.where.flatMap(term => "field" in term && term.field === field && !term.negated && [">", ">="].includes(term.op) ? [stamp(term.value, now)] : []).filter(date => Number.isFinite(date.getTime()));
  return dates.length ? new Date(Math.max(...dates.map(date => date.getTime()))) : undefined;
}

// Fetch chart context separately: a relative display window can cut through
// its first calendar day, so its exact totals cannot be recovered by trimming
// a broader daily reduction. Saved predicates and result-stage filters survive.
function smoothingQuery(job: PreviewJob, query: QueryState, now: number): QueryState | undefined {
  const periods = smoothingPeriods(job, query), field = job.options?.window?.field ?? job.options?.recipe?.field ?? rollingWindowField(job.dataset);
  if (!periods || !field) return;
  const start = lowerBound(query, field, now);
  if (!start) return; // All history is already present in an unbounded query.
  const from = shiftPeriod(start, "day", 1 - periods), source = query.grouping?.source ?? query;
  const where = source.where.filter(term => !("field" in term && term.field === field && !term.negated && [">", ">="].includes(term.op)));
  if (from) where.push({ field, op: ">=", value: from.toISOString() });
  return query.grouping ? { ...query, grouping: { ...query.grouping, source: { ...source, where } } } : { ...query, where };
}

type SmoothingHistory = { query: QueryState; payload: Payload };
function previewOf(job: PreviewJob, query: QueryState, payload: Payload, schema: FieldSchema, history?: SmoothingHistory): QueryPreview {
  const options = job.options ?? {}, recipe = options.recipe;
  const searchNote = payload.find?.search ? payload.find.search.complete ? "This search batch" : "Partial search batch; open the search to continue" : undefined;
  const tone = queryCategories[recipe?.category ?? categoryForQuery(job.dataset, query)].tone;
  if (query.grouping && !query.aggregations?.length) {
    const total = Number(payload.total) || 0;
    const cover = recipe?.exploration;
    const valueField = options.coverMetricId && query.grouping.summaries.some(column => column.id === options.coverMetricId && column.op !== "row") ? options.coverMetricId
      : cover && query.grouping.summaries.some(column => column.id === cover.valueField) ? cover.valueField
      : query.orderBy.find(term => query.grouping!.summaries.some(column => column.id === term.field && column.op !== "row"))?.field
        ?? query.grouping.summaries.find(column => column.op !== "row")?.id;
    const column = query.grouping.summaries.find(column => column.id === valueField);
    const unit = cover && valueField === cover.valueField ? cover.unit : column?.field?.includes("cost") ? "USD" : column?.field === "duration_ms" || column?.expression === "MEDIAN([duration_ms])" ? "ms" : column?.field?.includes("tokens") ? "tokens" : column?.field?.includes("words") ? "words" : ["card_rate", "card_escalation"].includes(valueField ?? "") ? "percent" : "";
    const labelFields = cover?.labelFields ?? query.grouping.keys;
    const insights = (payload.rows ?? []).slice(0, 3).map(row => ({
      label: labelFields.map(field => String(row[field] ?? "Unspecified")).join(" · "),
      value: valueField && typeof row[valueField] === "number" ? formatPreviewValue(row[valueField] as number, unit) : "—",
      context: (cover?.context ?? []).map(item => typeof row[item.field] === "number" ? `${formatPreviewValue(row[item.field] as number, item.unit ?? "")} ${item.label}` : "").filter(Boolean).join(" · "),
    }));
    return { state: total > 0 ? "ready" : "empty", total,
      summary: `${formatPreviewValue(total, "")} ${total === 1 ? "group" : cover?.subjects ?? "groups"}`,
      headline: column?.label ?? "Grouped results", action: recipe?.action ?? cover?.action ?? "Explore grouped results", insights,
      note: [recipe?.note, searchNote].filter(Boolean).join(" · ") || undefined,
    };
  }
  if (!query.aggregations?.length) {
    const total = Number(payload.total) || 0, row = payload.rows?.[0];
    return { state: total > 0 ? "ready" : "empty", total, summary: total > 0 ? `${formatPreviewValue(total, "")} matching ${recipe?.unit ?? "rows"}` : searchNote ? "No matches in this search batch" : "No matching observations", excerpt: String(row?.text ?? row?.title ?? row?.command ?? "Open the saved view").slice(0, 180), note: [recipe?.note, searchNote].filter(Boolean).join(" · ") || undefined };
  }
  const metrics = query.aggregations.map(clause => {
    const result = payload.metrics?.find(item => item.id === clause.id);
    if (!result) throw new Error(`No result returned for ${clause.label ?? clause.id}.`);
    const minimum = (options.cover ?? recipe?.cover)?.minSamples;
    return { clause, result: clause.distribution?.kind === "box" ? filterBoxSamples(result, minimum) : clause.expressionY ? filterScatterSamples(result, minimum) : result };
  });
  const selected = metrics.find(item => item.clause.id === options.coverMetricId) ?? metrics[0];
  const { clause, result } = selected;
  if (result.error) throw new Error(result.error);
  if (usesWeekdayCover(recipe, clause, options.cover ?? recipe?.cover, query)) {
    // Exact metric coverage can describe only the current search batch. Its
    // unchecked dates must never be filled as idle-day observations.
    if (payload.find?.limited || payload.find?.search?.complete === false) throw new Error("Partial search batch; open the search to complete it before viewing weekday distributions.");
    const weekday = weekdayMetric(clause, result, query, payload.now ?? Date.now(), recipe!.weekday!.unit);
    const cover = options.cover ?? recipe?.cover;
    const ready = weekday.days > 0 && weekday.observations > 0;
    weekday.metric.display = { ...weekday.metric.display, kind: "box", maxGroups: cover?.maxGroups ?? 7 };
    return { state: ready ? "ready" : "empty", total: weekday.days, emptyLabel: "No complete days with recorded activity", summary: `${weekday.days} complete local days · ${weekday.zone}`, note: [recipe?.note, searchNote].filter(Boolean).join(" · ") || undefined, action: recipe?.action,
      density: cover?.density, coverHeight: cover?.height,
      metrics: [{ clause: weekday.metric, result: weekday.result }], fields: [...schema.fields, { name: "weekday", label: "Weekday", type: "text", source: { kind: "derived" } }], coverMetricId: clause.id };
  }
  const values = result.buckets.filter(bucket => !bucket.error && !bucket.yError && bucket.value !== null && bucket.count !== 0 && (!bucket.distribution || (bucket.distribution.kind === "histogram" ? bucket.distribution.n > 0 : (bucket.distribution.summary?.n ?? 0) > 0)));
  if (!values.length && result.buckets.some(bucket => bucket.error || bucket.yError)) throw new Error(result.buckets.find(bucket => bucket.error || bucket.yError)!.error ?? result.buckets.find(bucket => bucket.yError)!.yError!);
  const numeric = values.filter(bucket => typeof bucket.value === "number" && Number.isFinite(bucket.value));
  // Only plain SUM/COUNT are additive. Never sum rates, medians, distinct counts,
  // distribution medians or paired axes to manufacture a grand total.
  const additive = !clause.expression && !clause.expressionY && !clause.distribution && ["sum", "count"].includes(clause.op);
  const total = additive ? numeric.reduce((sum, bucket) => sum + Number(bucket.value), 0) : result.processedRows ?? values.reduce((sum, bucket) => sum + bucket.count, 0);
  const countUnit = ({ tool_calls: "calls", writing_messages: "messages", skill_usages: "records", tl1_attempts: "attempts", findings: "findings" } as Partial<Record<QueryDataset, string>>)[job.dataset] ?? "rows";
  const unit = clause.display?.format ? "" : clause.op === "count" ? countUnit : clause.field?.includes("cost") ? "USD" : clause.field?.includes("tokens") ? "tokens" : clause.field?.includes("words") ? "words" : clause.field === "duration_ms" ? "ms" : clause.field?.endsWith("_count") ? countUnit : recipe?.unit ?? "";
  const scalar = !clause.groupBy.length && values.length === 1 && !clause.distribution;
  // Groups hidden by minCount are outside groupCount and Other, so the shown buckets are not the whole population.
  const hidden = result.hiddenGroups ?? 0;
  const limited = result.coverage === "partial" || hidden > 0 || result.groupCount !== undefined && result.groupCount > result.buckets.length;
  const pairCount = values.filter(bucket => typeof bucket.value === "number" && Number.isFinite(bucket.value) && typeof bucket.y === "number" && Number.isFinite(bucket.y) && !bucket.error && !bucket.yError).length;
  const summary = clause.expressionY ? `${pairCount.toLocaleString()} ${clause.label ?? "paired points"}${limited ? ` shown of ${result.groupCount?.toLocaleString() ?? "more"}` : ""}` : additive ? `${limited ? "Shown: " : ""}${formatPreviewValue(total, unit)}${unit && !["USD", "ms"].includes(unit) ? ` ${unit}` : ""}`
    : scalar ? formatMetricOutput(values[0].value, clause.display?.format).text
    : `${formatPreviewValue(result.processedRows ?? total, "")} observations · ${clause.label ?? clause.op}`;
  const notes = [recipe?.note, searchNote, options.sourceCover ? "Chart covers source observations before grouped filters" : undefined, result.scope === "shownRows" ? "Covers the saved table page" : undefined, result.coverage === "partial" ? "Partial results" : undefined,
    result.groupCount !== undefined && result.groupCount > result.buckets.length ? "Open the view for all results" : undefined,
    hidden ? `${hidden.toLocaleString()} ${hidden === 1 ? "group" : "groups"} hidden (fewer than ${clause.minCount?.toLocaleString()} rows)` : undefined,
    result.buckets.some(bucket => bucket.error || bucket.yError) ? "Some groups have calculation errors" : undefined];
  let ready = values.length > 0, eligible = true;
  if (options.suggested && recipe?.eligibility && !query.grouping) {
    const evidence = metrics.find(item => item.clause.id === recipe.eligibility!.metricId)?.result;
    eligible = Boolean(evidence && !evidence.error && evidence.buckets.some(bucket => !bucket.error && typeof bucket.value === "number" && bucket.value >= recipe.eligibility!.minValue));
    ready &&= eligible;
  }
  const preview: QueryPreview = { density: (options.cover ?? recipe?.cover)?.density, stackBy: (options.cover ?? recipe?.cover)?.stackBy, coverHeight: (options.cover ?? recipe?.cover)?.height, legendColumns: (options.cover ?? recipe?.cover)?.legendColumns, state: ready ? "ready" : "empty", total, summary, note: notes.filter(Boolean).join(" · ") || undefined, metrics, fields: schema.fields, coverMetricId: clause.id };
  const minimum = (options.cover ?? recipe?.cover)?.minSamples;
  if (!ready && recipe?.emptyLabel) preview.emptyLabel = recipe.emptyLabel;
  if (!ready && minimum && (clause.distribution?.kind === "box" || clause.expressionY)) preview.emptyLabel = `No cohorts with at least ${minimum} ${(options.cover ?? recipe?.cover)?.sampleUnit ?? (!query.grouping && ["tool_calls", "mcp_calls"].includes(job.dataset) ? "calls" : "conversations")}`;
  if (query.grouping && recipe?.exploration?.context && payload.rows?.length) {
    preview.groupEvidence = Object.fromEntries(payload.rows.map(row => [JSON.stringify(query.grouping!.keys.map(key => row[key] ?? null)), recipe.exploration!.context!.map(item => typeof row[item.field] === "number" ? `${formatPreviewValue(row[item.field] as number, item.unit ?? "")} ${item.label}` : "").filter(Boolean).join(" · ")]));
  }
  if (query.grouping && recipe?.exploration?.context && values[0]) {
    preview.evidence = recipe.exploration.context.map(item => {
      const value = metrics.find(metric => metric.clause.id === item.field)?.result.buckets.find(bucket => JSON.stringify(bucket.keys) === JSON.stringify(values[0].keys))?.value;
      return typeof value === "number" ? `${formatPreviewValue(value, item.unit ?? "")} ${item.label}` : "";
    }).filter(Boolean).join(" · ") || preview.groupEvidence?.[JSON.stringify(values[0].keys)];
  }
  if (query.grouping && recipe?.exploration?.labelFields && payload.rows?.length) {
    preview.groupLabels = Object.fromEntries(payload.rows.map(row => [JSON.stringify(query.grouping!.keys.map(key => row[key] ?? null)), recipe.exploration!.labelFields!.map(field => keyLabel(schema, field)?.(row[field]) ?? String(row[field] ?? "Unspecified")).join(" · ")]));
  }
  // A ranked cover needs values and evidence as well as bars. Match companion
  // metrics by their actual group keys, never by bucket position.
  if (clause.groupBy.length && clause.groupBy[0] !== "day" && !clause.distribution && !clause.expressionY && ["auto", "list", "table"].includes(clause.display?.kind ?? "auto")) {
    preview.headline = clause.label ?? "Top results";
    preview.insights = values.slice(0, 3).map(bucket => {
      const companion = metrics.find(item => item !== selected && JSON.stringify(item.clause.groupBy) === JSON.stringify(clause.groupBy) && !item.clause.distribution && !item.clause.expressionY);
      const evidence = companion?.result.buckets.find(item => JSON.stringify(item.keys) === JSON.stringify(bucket.keys));
      return { label: bucket.keys.map(key => String(key ?? "Unspecified")).join(" · "),
        value: clause.display?.format ? formatMetricOutput(bucket.value, clause.display.format).text : formatPreviewValue(Number(bucket.value), unit),
        context: evidence && typeof evidence.value === "number" ? `${formatPreviewValue(evidence.value, "")} ${companion!.clause.label ?? "records"}` : `${bucket.count.toLocaleString()} ${countUnit}` };
    });
  }
  // Covers name grouped keys the way their pages do: machine activities, and
  // labelled enum options whose values are identifiers rather than words.
  const keyLabels = clause.groupBy.map(field => keyLabel(schema, field));
  if (!preview.groupLabels && clause.groupBy[0] !== "day" && keyLabels.some(Boolean)) {
    preview.groupLabels = Object.fromEntries(values.map(bucket => [JSON.stringify(bucket.keys), bucket.keys.map((key, i) => keyLabels[i]?.(key) ?? String(key ?? "Unspecified")).join(" · ")]));
  }
  // Existing daily covers keep the Usage palette, calendar gaps and exact Other.
  if (dailyColumns(clause) && (numeric.length || history)) {
    preview.timeline = (options.cover ?? recipe?.cover)?.timeline;
    if ((preview.timeline?.percent || preview.timeline?.display === "cumulative") && (limited || result.buckets.some(bucket => bucket.error || bucket.yError))) throw new Error("Complete daily totals are required for percentage shares or cumulative totals.");
    const periods = smoothingPeriods(job, query);
    const chartResult = history ? history.payload.metrics?.find(metric => metric.id === clause.id) : result;
    if (!chartResult) throw new Error(`No smoothing history returned for ${clause.label ?? clause.id}.`);
    if (periods && (chartResult.error || chartResult.coverage === "partial" || (chartResult.hiddenGroups ?? 0) > 0 || (chartResult.groupCount ?? 0) > chartResult.buckets.length || chartResult.buckets.some(bucket => bucket.error || bucket.yError))) throw new Error("Complete daily totals are required for smoothing.");
    const chartValues = periods ? chartResult.buckets.filter(bucket => typeof bucket.value === "number" && Number.isFinite(bucket.value) && bucket.count !== 0) : numeric;
    // Recorded durations chart as hours; milliseconds make unreadable axes.
    const scale = clause.field === "duration_ms" ? 36e5 : 1;
    const entries = chartValues.map(bucket => [String(bucket.keys[0]), bucket.keys.length > 1 ? bucket.keys.slice(1).map((key, i) => keyLabels[i + 1]?.(key) ?? String(key ?? "Unspecified")).join(" · ") : clause.label ?? "Total", Number(bucket.value) / scale] as [string, string, number]);
    const observed = [...new Set(numeric.map(bucket => String(bucket.keys[0])))].sort();
    const days = options.window?.days ?? Math.min(90, observed.length);
    const bound = options.window && (query.grouping?.source.where ?? query.where).find(term => "field" in term && term.field === options.window!.field && term.op === ">=");
    const now = new Date(payload.now ?? Date.now());
    const start = bound && "field" in bound ? new Date(parseRelativeDuration(bound.value) !== null ? now.getTime() + parseRelativeDuration(bound.value)! : bound.value) : new Date(`${observed[0]}T12:00:00`);
    const count = options.window?.mode === "relative" ? Math.round((Date.UTC(now.getFullYear(), now.getMonth(), now.getDate()) - Date.UTC(start.getFullYear(), start.getMonth(), start.getDate())) / 86400000) + 1 : days;
    const chart: ChartBucket[] = [];
    for (let i = 0; i < count; i++) { const date = new Date(start); date.setHours(0, 0, 0, 0); date.setDate(date.getDate() + i); const key = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`; chart.push({ key, date, values: {} }); }
    if (!options.window) chart.splice(0, chart.length, ...observed.slice(-90).map(key => ({ key, date: new Date(`${key}T12:00:00`), values: {} })));
    if (observed.length > 90 && !options.window) preview.note = [preview.note, periods ? "Chart fills calendar gaps across the last 90 observed dates" : "Chart shows the last 90 observed day buckets"].filter(Boolean).join(" · ");
    if (periods && chart.length) {
      const field = options.window?.field ?? recipe?.field ?? rollingWindowField(job.dataset);
      const historyStart = field ? lowerBound(history?.query ?? query, field, now.getTime()) : undefined;
      const extended = chartBucketsWithHistory("day", chart[0].date, chart.at(-1)!.date, 1, entries.map(([key]) => parseDay(key)), historyStart ?? earliestKey(entries.map(([key]) => key)));
      chart.splice(0, chart.length, ...extended);
    }
    preview.buckets = chart;
    preview.series = foldSeries(entries, new Map(chart.map(bucket => [bucket.key, bucket])), name => name || "Unspecified", undefined, [], Math.max(clause.display?.maxGroups ?? 4, clause.groupBy.at(-1) === "level_band" ? 4 : 0), recipe?.scope ?? clause.groupBy.at(-1) ?? "query");
    if (clause.display?.showOther === false) preview.series = preview.series.filter(item => !item.hiddenCount);
    if (clause.groupBy.length === 1 && preview.series[0]) preview.series[0].color = tone;
    if (!ready && eligible && periods && rollingAverageBuckets(chart, periods, { period: "day" }).some(bucket => !bucket.history && Object.values(bucket.values).some(value => value > 0))) preview.state = "ready";
  }
  if (query.grouping && recipe?.exploration && !clause.distribution && !clause.expressionY && (!additive || !recipe.exploration.coverMetric)) {
    const count = result.groupCount ?? result.processedRows ?? values.length;
    preview.groupSummary = { count, subject: count === 1 ? "group" : recipe.exploration.subjects };
    preview.summary = `${formatPreviewValue(count, "")} ${preview.groupSummary.subject}`;
  }
  // A headline is a real complete additive measure, never a sum of rates,
  // distributions, errors, or the returned subset of a ranked population.
  if ((options.cover ?? recipe?.cover)?.showHeadline !== false && ready && additive && !limited && !preview.groupSummary && numeric.length === values.length && !result.buckets.some(bucket => bucket.error || bucket.yError) && Number.isFinite(total)) {
    preview.headlineMetric = { value: clause.display?.format ? formatMetricOutput(total, clause.display.format).text : formatPreviewValue(total, unit), unit: unit && !["USD", "ms"].includes(unit) ? unit : "", ...(unit === "USD" ? { label: job.dataset === "findings" ? "likely a month" : "API equivalent" } : {}) };
  }
  // Hours by concurrency band summarize as the share of active hours with more than one agent,
  // since the plain total (hours with any agent active) says nothing about parallelism.
  const band = clause.groupBy.indexOf("level_band");
  if (preview.headlineMetric && job.dataset === "parallelism" && clause.field === "elapsed_hours" && band >= 0 && total > 0) {
    const multiple = numeric.reduce((sum, bucket) => sum + (String(bucket.keys[band]) === "1" ? 0 : Number(bucket.value)), 0);
    preview.headlineMetric = { value: (multiple / total).toLocaleString(undefined, { style: "percent", maximumFractionDigits: 0 }), unit: "", label: "of active hours had 2+ agents" };
  }
  preview.action = recipe?.action ?? recipe?.exploration?.action;
  return preview;
}

// Metadata lives for this execution only. A refresh always reads the current
// canonical revisions; an aborted caller never lends its signal to another run.
export async function loadQueryPreviews(jobs: PreviewJob[], publish: (id: string, preview?: QueryPreview, error?: unknown) => void, signal?: AbortSignal): Promise<void> {
  if (signal?.aborted) return;
  const contexts = new Map<QueryDataset, Promise<Context>>();
  function context(job: PreviewJob) {
    let pending = contexts.get(job.dataset);
    if (!pending) {
      pending = (async () => {
        const [handshake, definitions] = await Promise.all([
          fetchPreview<{ metricCapabilities: MetricCapabilities; queryCapabilities?: QueryCapabilitiesV3; computedExecution: ComputedExecution }>(`/api/query/${job.dataset}/capabilities`, signal),
          fetchPreview<ComputedColumn[]>(`/api/query-computed-columns?dataset=${encodeURIComponent(job.schema.name)}`, signal).then(raw => { if (!Array.isArray(raw)) throw new Error("Invalid computed column catalogue."); return raw.map(validateComputedColumn); }),
        ]);
        if (handshake.metricCapabilities?.version !== 2 || !handshake.computedExecution?.profile) throw new Error("This dataset does not support metric previews.");
        const fields = definitions.flatMap(definition => {
          const name = `@computed/${definition.id}`, cap = handshake.computedExecution.fields[name];
          return cap ? [{ name, label: definition.label, type: cap.type, source: { kind: "backend" as const }, filter: { enabled: false }, sort: { enabled: cap.sort }, aggregate: { measure: cap.measure, groupable: cap.group } }] : [];
        });
        return { schema: { ...job.schema, fields: [...job.schema.fields, ...fields] }, execution: handshake.computedExecution, capabilities: handshake.metricCapabilities, queryCapabilities: handshake.queryCapabilities };
      })();
      contexts.set(job.dataset, pending);
    }
    return pending;
  }
  const clock = new Date();
  const studyJobs = jobs.filter(job => job.dataset === "writing_messages" && (!job.query.grouping || job.options?.recipe?.humanStudy === "conversation-lengths" && JSON.stringify(job.query.grouping.keys) === '["conversation_id"]' && JSON.stringify(job.query.grouping.summaries) === JSON.stringify(job.options.recipe.exploration?.summaries) && !job.query.where.length) && job.options?.recipe?.humanStudy && job.options.window?.mode === "relative" && rollingQueryMatches(job.query, job.options.window) && !searchContext(job)
    && humanStudyCoverMatches(job.options.recipe, job.options.cover, job.options.coverMetricId));
  const studies = new Set(studyJobs);
  const studyLoader = studyJobs.length ? createHumanStudyLoader(clock.getTime(), studyJobs[0].schema, signal) : undefined;
  const studyWork = Promise.all(studyJobs.map(async job => {
    try {
      const study = await studyLoader!(job.options!.recipe!.humanStudy!, job.query, job.options!.window!.days);
      const ready = study.value !== undefined || study.rows?.some(row => row.count ? row.count > 0 : row.value > 0);
      if (!signal?.aborted) publish(job.id, { coverHeight: (job.options!.cover ?? job.options!.recipe!.cover)?.height, state: ready ? "ready" : "empty", total: study.value ?? 0, summary: study.sampleLabel, note: [job.options!.recipe!.note, study.detail].filter(Boolean).join(" · "), action: job.options!.recipe!.action ?? "Inspect source messages", study });
    } catch (error) { if (!signal?.aborted) publish(job.id, undefined, error); }
  }));
  const costJobs = jobs.filter(job => usesApiCostTrend(job) && !searchContext(job));
  const costStudies = new Set(costJobs), costLoads = new Map<number, Promise<HumanStudyPreview>>();
  const costWork = Promise.all(costJobs.map(async job => {
    try {
      const days = job.options!.window!.days;
      let pending = costLoads.get(days);
      if (!pending) {
        pending = (async () => {
          const ctx = await context(job);
          async function history(from: number, to: number): Promise<CostTimeBucket[]> {
            const query = apiCostTrendQuery(job.query.grouping!.source, from, to);
            const request = toMetricQuery(query, ctx.schema, ctx.execution);
            if (request.diagnostics.length) throw new Error(request.diagnostics.map(d => d.message).join(" · "));
            const body = await fetchPreview<AggregationResult>("/api/query/usage/metrics", signal, request);
            const facts = apiCostTrendFacts(body.metrics.find(metric => metric.id === "cost-history"));
            if (facts) return facts;
            // Partition only truncated timestamp reductions; exact half-open
            // ranges prevent overlap and preserve the original fact placement.
            const middle = Math.floor((from + to) / 2);
            if (middle <= from || middle >= to) throw new Error("API cost history exceeds the timestamp group limit.");
            return [...await history(from, middle), ...await history(middle, to)];
          }
          return buildApiCostTrend(await history(clock.getTime() - Math.max(37, days) * 86400000, clock.getTime()), days, clock.getTime());
        })();
        costLoads.set(days, pending);
      }
      const study = await pending;
      if (!signal?.aborted) publish(job.id, { coverHeight: (job.options!.cover ?? job.options!.recipe!.cover)?.height, state: study.value === undefined ? "empty" : "ready", total: study.value ?? 0, summary: study.sampleLabel, note: [job.options!.recipe!.note, study.detail].filter(Boolean).join(" · "), action: job.options!.recipe!.action, study });
    } catch (error) { if (!signal?.aborted) publish(job.id, undefined, error); }
  }));
  const trendJobs = jobs.filter(job => !costStudies.has(job) && usesCardTrend(job) && !searchContext(job));
  const trends = new Set(trendJobs);
  const trendWork = Promise.all(trendJobs.map(async job => {
    try {
      const recipe = job.options!.recipe!, trend = recipe.trend!, lookback = trend.lookback ?? job.options!.window!.days;
      const source = trend.dataset ? { ...job, dataset: trend.dataset, schema: trendSchemas[trend.dataset] } : job, ctx = await context(source);
      const results = await Promise.all(cardTrendQueries(recipe.where ?? [], trend, cardTrendDays(lookback, Boolean(trend.completeDays), clock.getTime()).days).map(async query => {
        const request = toMetricQuery(query, ctx.schema, ctx.execution);
        if (request.diagnostics.length) throw new Error(request.diagnostics.map(d => d.message).join(" · "));
        return (await fetchPreview<AggregationResult>(`/api/query/${source.dataset}/metrics`, signal, request)).metrics;
      }));
      const { study, observations } = buildCardTrend(trend, lookback, results, clock.getTime()), ready = observations > 0 && Boolean(study.points?.some(point => point !== null));
      if (!signal?.aborted) publish(job.id, { coverHeight: (job.options!.cover ?? recipe.cover)?.height, state: ready ? "ready" : "empty", emptyLabel: ready ? undefined : recipe.emptyLabel, total: study.value ?? 0, summary: study.sampleLabel, note: [recipe.note, study.detail].filter(Boolean).join(" · "), action: recipe.action, study });
    } catch (error) { if (!signal?.aborted) publish(job.id, undefined, error); }
  }));
  const resolved: { job: PreviewJob; query: QueryState; search: string }[] = [];
  for (const original of jobs) {
    if (studies.has(original) || costStudies.has(original) || trends.has(original)) continue;
    let job = original;
    try {
      if (original.options?.recipe && (original.query.grouping || original.options.cover)) {
        const options = original.options, recipe = options.recipe!;
        const cover = options.cover ?? (options.coverMetricId ? { mode: original.query.grouping && !original.query.aggregations?.length ? "result" as const : "source" as const, metricId: options.coverMetricId } : undefined);
        const chart = cardCoverQuery(original.query, recipe, cover);
        job = { ...original, options: { ...options, coverMetricId: chart.metricId, sourceCover: Boolean(original.query.grouping && !chart.query.grouping && original.query.where.length) } };
        resolved.push({ job, query: resolveRollingQuery(chart.query, options.window, clock), search: searchContext(job) });
      } else resolved.push({ job, query: resolveRollingQuery(job.query, job.options?.window, clock), search: searchContext(job) });
    } catch (error) { publish(job.id, undefined, error); }
  }
  type Item = typeof resolved[number] & { now: number; accept: (data: Cached) => void; fail: (error: unknown) => void };
  const groups = new Map<string, Item[]>();
  function enqueue(item: Item) {
    if (signal?.aborted) return;
    const key = JSON.stringify([item.job.dataset, item.query, item.search]), cached = cache.get(key);
    if (cached && Date.now() - cached.at < 60_000) {
      item.accept(cached);
      return;
    }
    const grouped = Boolean(item.query.aggregations?.length);
    const groupKey = JSON.stringify([item.job.dataset, item.search, item.now, grouped, item.query.where, item.query.grouping?.source.where, grouped && !item.query.aggregations?.some(metric => metric.scope === "shownRows") ? null : [item.query.select, item.query.orderBy, item.query.limit, item.query.offset]]);
    const group = groups.get(groupKey) ?? []; group.push(item); groups.set(groupKey, group);
  }
  for (const item of resolved) {
    const cached = cache.get(JSON.stringify([item.job.dataset, item.query, item.search]));
    const now = cached && Date.now() - cached.at < 60_000 ? cached.payload.now ?? clock.getTime() : clock.getTime();
    const historyQuery = smoothingQuery(item.job, item.query, now);
    let visible: Cached | undefined, history: Cached | undefined, done = false;
    const fail = (error: unknown) => { if (!done && !signal?.aborted) { done = true; publish(item.job.id, undefined, error); } };
    const complete = () => {
      if (done || signal?.aborted || !visible || historyQuery && !history) return;
      try {
        const preview = previewOf(item.job, item.query, visible.payload, visible.schema, historyQuery && history ? { query: historyQuery, payload: history.payload } : undefined);
        done = true; publish(item.job.id, preview);
      } catch (error) { fail(error); }
    };
    enqueue({ ...item, now, fail, accept: data => { visible = data; complete(); } });
    if (historyQuery) enqueue({ ...item, query: historyQuery, now, fail, accept: data => { history = data; complete(); } });
  }
  const tasks: Item[][] = [];
  for (const group of groups.values()) {
    let batch: Item[] = [], size = 0;
    for (const item of group) {
      if (item.query.grouping || item.query.aggregations?.some(metric => metric.dateGrouping) || !item.query.aggregations?.length) {
        if (batch.length) tasks.push(batch);
        batch = []; size = 0; tasks.push([item]); continue;
      }
      const count = item.query.aggregations.length;
      if (size + count > 20 || !item.query.aggregations?.length) { if (batch.length) tasks.push(batch); batch = []; size = 0; }
      batch.push(item); size += count;
    }
    if (batch.length) tasks.push(batch);
  }
  async function run(items: Item[]): Promise<void> {
    if (signal?.aborted) return;
    try {
      const ctx = await context(items[0].job);
      const valid: { item: Item; request: MetricQuery }[] = [];
      for (const item of items) {
        try {
          // A metric filter or minimum group size the executor does not run fails its card, never silently dropped.
          const grouped = Boolean(item.query.grouping || item.query.aggregations?.some(metric => metric.dateGrouping));
          const support = grouped ? { filters: ctx.queryCapabilities?.metricFilters, minCount: ctx.queryCapabilities?.metricMinCount } : ctx.capabilities;
          const unsupported = item.query.aggregations?.map(metric => metricPopulationError(metric, support)).find(Boolean);
          if (unsupported) throw new Error(unsupported);
          if (grouped) {
            if (ctx.queryCapabilities?.version !== 3) throw new Error("Server grouping is unavailable. Refresh this view after updating the backend.");
            const labels = Boolean(item.query.grouping && item.job.options?.recipe?.exploration?.labelFields);
            const labelLimit = Math.max(12, ...(item.query.aggregations ?? []).map(metric => Math.max(metric.groupLimit ?? 0, metric.display?.maxGroups ?? 0)));
            const query = labels ? { ...item.query, limit: labelLimit } : item.query.aggregations?.length ? item.query : { ...item.query, limit: Math.min(item.query.limit || 3, 3) };
            const request = toQueryRequestV3(query, item.job.schema, ctx.queryCapabilities, ctx.execution, { now: item.now, rows: labels || !query.aggregations?.length });
            if (request.diagnostics.length) throw new Error(request.diagnostics.join(" · "));
            const schema = query.grouping ? createGroupingModel(sourceExecutionSchema(item.job.schema, ctx.execution)).resultSchema(query.grouping.keys, query.grouping.summaries) : ctx.schema;
            const result = await fetchPreview<QueryResultV3 & Pick<Payload, "find">>(`/api/query/${item.job.dataset}/query-v3${item.search}`, signal, request);
            validateQueryResultV3(result, request, schema);
            finish(item, result, schema);
          } else if (item.query.aggregations?.length) {
            const request = toMetricQuery(item.query, ctx.schema, ctx.execution);
            if (request.diagnostics.length) throw new Error(request.diagnostics.map(d => d.message).join(" · "));
            valid.push({ item, request });
          } else {
            const request = toServerQueryV2({ ...item.query, limit: Math.min(item.query.limit || 3, 3) }, ctx.schema, ctx.execution);
            if (request.diagnostics.length) throw new Error(request.diagnostics.map(d => d.message).join(" · "));
            finish(item, await fetchPreview<Payload>(`/api/query/${item.job.dataset}/rows-v2${item.search}`, signal, request), ctx.schema);
          }
        } catch (error) { item.fail(error); }
      }
      if (!valid.length || signal?.aborted) return;
      const request = { ...valid[0].request, expectedRevisions: Object.assign({}, ...valid.map(({ request }) => request.expectedRevisions)), metrics: valid.flatMap(({ request }, i) => request.metrics.map((metric, j) => ({ ...metric, id: `preview:${i}:${j}` }))) };
      try {
        const body = await fetchPreview<AggregationResult & Pick<Payload, "find">>(`/api/query/${valid[0].item.job.dataset}/metrics${valid[0].item.search}`, signal, request);
        valid.forEach(({ item, request }, i) => {
          const metrics = request.metrics.map((metric, j) => {
            const result = body.metrics.find(result => result.id === `preview:${i}:${j}`);
            return result ? { ...result, id: metric.id } : undefined;
          }).filter((result): result is AggregationResultEntry => Boolean(result));
          finish(item, { metrics, find: body.find }, ctx.schema);
        });
      } catch (error) {
        // A server-rejected formula must not take healthy siblings down with it.
        if (valid.length > 1 && error instanceof PreviewHTTPError && [400, 409, 422].includes(error.status)) {
          for (const { item } of valid) await run([item]);
        } else throw error;
      }
    } catch (error) { for (const item of items) item.fail(error); }
  }
  function finish(item: Item, payload: Payload, schema: FieldSchema) {
    if (signal?.aborted) return;
    if (cache.size >= 200) cache.delete(cache.keys().next().value!);
    payload = { ...payload, now: payload.now ?? item.now };
    cache.set(JSON.stringify([item.job.dataset, item.query, item.search]), { at: Date.now(), payload, schema });
    item.accept({ at: Date.now(), payload, schema });
  }
  async function work() {
    while (tasks.length && !signal?.aborted) {
      // Re-evaluate between requests: scrolling may bring a different set of
      // cold cards into view while earlier queries are still running.
      let next = 0, priority = Infinity;
      tasks.forEach((task, index) => {
        const rank = Math.min(...task.map(item => item.job.priority?.() ?? 1));
        if (rank < priority) { next = index; priority = rank; }
      });
      await run(tasks.splice(next, 1)[0]);
    }
  }
  await Promise.all([work(), work(), work(), studyWork, costWork, trendWork]);
}

export async function loadQueryPreview(dataset: QueryDataset, query: QueryState, schema: FieldSchema, options: PreviewOptions = {}, signal?: AbortSignal): Promise<QueryPreview> {
  let result: QueryPreview | undefined, failure: unknown;
  await loadQueryPreviews([{ id: "preview", dataset, query, schema, options }], (_id, preview, error) => { result = preview; failure = error; }, signal);
  if (failure) throw failure;
  if (!result) throw signal?.reason ?? new Error("Preview cancelled.");
  return result;
}
