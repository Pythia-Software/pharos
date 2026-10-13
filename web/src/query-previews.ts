import { formatMetricOutput } from "@pythia-software/query-table-ui";
import { applyQuery, evaluateMetrics, prepareGrouping, withoutGrouping, validateComputedColumn, toMetricQuery, toServerQueryV2, type ComputedColumn, type AggregationClause, type AggregationResult, type AggregationResultEntry, type ComputedExecution, type FieldSchema, type MetricCapabilities, type MetricQuery, type QueryState } from "@pythia-software/query-table-core";
import { categoryForQuery, queryCategories } from "./query-categories";
import { foldSeries, type ChartBucket, type ChartSeries } from "./chart-series";
import { resolveRollingQuery, type QueryDataset, type RollingWindow } from "./query-links";
import type { QueryRecipe } from "./curiosity-data";
import { readTextSearch, textFindParameters } from "./text-search-model";

export type PreviewMetric = { clause: AggregationClause; result: AggregationResultEntry };
export type QueryPreview = {
  state: "ready" | "empty"; total: number; summary: string; note?: string;
  metrics?: PreviewMetric[]; fields?: FieldSchema["fields"]; coverMetricId?: string;
  ranking?: { label: string; value: number; color: string }[];
  buckets?: ChartBucket[]; series?: ChartSeries[]; excerpt?: string;
};
type PreviewOptions = { window?: RollingWindow; recipe?: QueryRecipe; url?: string; coverMetricId?: string; suggested?: boolean };
export type PreviewJob = { id: string; dataset: QueryDataset; query: QueryState; schema: FieldSchema; options?: PreviewOptions };
type Context = { schema: FieldSchema; execution: ComputedExecution; capabilities: MetricCapabilities };
type Payload = { metrics?: AggregationResult["metrics"]; rows?: Record<string, unknown>[]; total?: number; find?: { search?: { complete: boolean } } };
type Cached = { at: number; payload: Payload; schema: FieldSchema };
const cache = new Map<string, Cached>();
export function clearPreviewCache() { cache.clear(); }

export function formatPreviewValue(value: number, unit: string): string {
  if (unit === "USD") return value.toLocaleString(undefined, { style: "currency", currency: "USD", maximumFractionDigits: value >= 100 ? 0 : 2 });
  if (unit === "ms") return value >= 3_600_000 ? `${(value / 3_600_000).toFixed(1)}h` : value >= 60_000 ? `${(value / 60_000).toFixed(1)}m` : `${(value / 1000).toFixed(1)}s`;
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

function previewOf(job: PreviewJob, query: QueryState, payload: Payload, schema: FieldSchema): QueryPreview {
  const options = job.options ?? {}, recipe = options.recipe;
  const searchNote = payload.find?.search ? payload.find.search.complete ? "This search batch" : "Partial search batch; open the search to continue" : undefined;
  const tone = queryCategories[recipe?.category ?? categoryForQuery(job.dataset, query)].tone;
  if (!query.aggregations?.length) {
    const total = Number(payload.total) || 0, row = payload.rows?.[0];
    return { state: total > 0 ? "ready" : "empty", total, summary: total > 0 ? `${formatPreviewValue(total, "")} matching ${recipe?.unit ?? "rows"}` : searchNote ? "No matches in this search batch" : "No matching observations", excerpt: String(row?.text ?? row?.title ?? row?.command ?? "Open the saved view").slice(0, 180), note: [recipe?.note, searchNote].filter(Boolean).join(" · ") || undefined };
  }
  const metrics = query.aggregations.map(clause => {
    const result = payload.metrics?.find(item => item.id === clause.id);
    if (!result) throw new Error(`No result returned for ${clause.label ?? clause.id}.`);
    return { clause, result };
  });
  const selected = metrics.find(item => item.clause.id === options.coverMetricId) ?? metrics[0];
  const { clause, result } = selected;
  if (result.error) throw new Error(result.error);
  const values = result.buckets.filter(bucket => !bucket.error && !bucket.yError && bucket.value !== null && bucket.count !== 0 && (!bucket.distribution || (bucket.distribution.kind === "histogram" ? bucket.distribution.n > 0 : (bucket.distribution.summary?.n ?? 0) > 0)));
  if (!values.length && result.buckets.some(bucket => bucket.error || bucket.yError)) throw new Error(result.buckets.find(bucket => bucket.error || bucket.yError)!.error ?? result.buckets.find(bucket => bucket.yError)!.yError!);
  const numeric = values.filter(bucket => typeof bucket.value === "number" && Number.isFinite(bucket.value));
  // Only plain SUM/COUNT are additive. Never sum rates, medians, distinct counts,
  // distribution medians or paired axes to manufacture a grand total.
  const additive = !clause.expression && !clause.expressionY && !clause.distribution && ["sum", "count"].includes(clause.op);
  const total = additive ? numeric.reduce((sum, bucket) => sum + Number(bucket.value), 0) : result.processedRows ?? values.reduce((sum, bucket) => sum + bucket.count, 0);
  const countUnit = ({ tool_calls: "calls", writing_messages: "messages", skill_usages: "records", tl1_attempts: "attempts" } as Partial<Record<QueryDataset, string>>)[job.dataset] ?? "rows";
  const unit = clause.display?.format ? "" : clause.op === "count" ? countUnit : clause.field?.includes("cost") ? "USD" : clause.field?.includes("tokens") ? "tokens" : clause.field?.includes("words") ? "words" : clause.field === "duration_ms" ? "ms" : clause.field?.endsWith("_count") ? countUnit : recipe?.unit ?? "";
  const scalar = !clause.groupBy.length && values.length === 1 && !clause.distribution;
  const limited = result.coverage === "partial" || result.groupCount !== undefined && result.groupCount > result.buckets.length;
  const summary = additive ? `${limited ? "Shown: " : ""}${formatPreviewValue(total, unit)}${unit && !["USD", "ms"].includes(unit) ? ` ${unit}` : ""}`
    : scalar ? formatMetricOutput(values[0].value, clause.display?.format).text
    : `${formatPreviewValue(result.processedRows ?? total, "")} observations · ${clause.label ?? clause.op}`;
  const notes = [recipe?.note, searchNote, result.scope === "shownRows" ? "Covers the saved table page" : undefined, result.coverage === "partial" ? "Partial results" : undefined,
    result.groupCount !== undefined && result.groupCount > result.buckets.length ? `${result.buckets.length} of ${result.groupCount} groups returned` : undefined,
    result.buckets.some(bucket => bucket.error || bucket.yError) ? "Some groups have calculation errors" : undefined];
  let ready = values.length > 0;
  if (options.suggested && recipe?.eligibility) {
    const evidence = metrics.find(item => item.clause.id === recipe.eligibility!.metricId)?.result;
    ready &&= Boolean(evidence && !evidence.error && evidence.buckets.some(bucket => !bucket.error && typeof bucket.value === "number" && bucket.value >= recipe.eligibility!.minValue));
  }
  const preview: QueryPreview = { state: ready ? "ready" : "empty", total, summary, note: notes.filter(Boolean).join(" · ") || undefined, metrics, fields: schema.fields, coverMetricId: clause.id };
  // Existing daily covers keep the Usage palette, calendar gaps and exact Other.
  if (!clause.expression && !clause.distribution && (!clause.display || clause.display.kind === "auto") && clause.groupBy[0] === "day" && clause.op === "sum" && numeric.length) {
    const entries = numeric.map(bucket => [String(bucket.keys[0]), bucket.keys.length > 1 ? bucket.keys.slice(1).map(key => String(key ?? "Unspecified")).join(" · ") : recipe?.title ?? "Total", Number(bucket.value)] as [string, string, number]);
    const observed = [...new Set(entries.map(([key]) => key))].sort();
    const days = options.window?.days ?? Math.min(90, observed.length);
    const bound = options.window && query.where.find(term => "field" in term && term.field === options.window!.field && term.op === ">=");
    const start = bound && "field" in bound ? new Date(bound.value) : new Date(`${observed[0]}T12:00:00`);
    const chart: ChartBucket[] = [];
    for (let i = 0; i < days; i++) { const date = new Date(start); date.setDate(date.getDate() + i); const key = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`; chart.push({ key, date, values: {} }); }
    if (!options.window) chart.splice(0, chart.length, ...observed.slice(-90).map(key => ({ key, date: new Date(`${key}T12:00:00`), values: {} })));
    if (observed.length > 90 && !options.window) preview.note = [preview.note, "Chart shows the last 90 observed day buckets"].filter(Boolean).join(" · ");
    preview.buckets = chart;
    preview.series = foldSeries(entries, new Map(chart.map(bucket => [bucket.key, bucket])), name => name || "Unspecified", undefined, [], 4, recipe?.scope ?? clause.groupBy.at(-1) ?? "query");
    if (clause.groupBy.length === 1 && preview.series[0]) preview.series[0].color = tone;
  }
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
          fetchPreview<{ metricCapabilities: MetricCapabilities; computedExecution: ComputedExecution }>(`/api/query/${job.dataset}/capabilities`, signal),
          fetchPreview<ComputedColumn[]>(`/api/query-computed-columns?dataset=${encodeURIComponent(job.schema.name)}`, signal).then(raw => { if (!Array.isArray(raw)) throw new Error("Invalid computed column catalogue."); return raw.map(validateComputedColumn); }),
        ]);
        if (handshake.metricCapabilities?.version !== 2 || !handshake.computedExecution?.profile) throw new Error("This dataset does not support metric previews.");
        const fields = definitions.flatMap(definition => {
          const name = `@computed/${definition.id}`, cap = handshake.computedExecution.fields[name];
          return cap ? [{ name, label: definition.label, type: cap.type, source: { kind: "backend" as const }, filter: { enabled: false }, sort: { enabled: cap.sort }, aggregate: { measure: cap.measure, groupable: cap.group } }] : [];
        });
        return { schema: { ...job.schema, fields: [...job.schema.fields, ...fields] }, execution: handshake.computedExecution, capabilities: handshake.metricCapabilities };
      })();
      contexts.set(job.dataset, pending);
    }
    return pending;
  }
  const clock = new Date();
  const resolved: { job: PreviewJob; query: QueryState; search: string }[] = [];
  for (const job of jobs) {
    try { resolved.push({ job, query: resolveRollingQuery(job.query, job.options?.window, clock), search: searchContext(job) }); }
    catch (error) { publish(job.id, undefined, error); }
  }
  type Item = typeof resolved[number];
  const groups = new Map<string, Item[]>();
  for (const item of resolved) {
    if (signal?.aborted) return;
    const key = JSON.stringify([item.job.dataset, item.query, item.search]), cached = cache.get(key);
    if (cached && Date.now() - cached.at < 60_000) {
      try { publish(item.job.id, previewOf(item.job, item.query, cached.payload, cached.schema)); } catch (error) { publish(item.job.id, undefined, error); }
      continue;
    }
    const grouped = Boolean(item.query.aggregations?.length);
    const groupKey = JSON.stringify([item.job.dataset, item.search, grouped, item.query.where, grouped && !item.query.aggregations?.some(metric => metric.scope === "shownRows") ? null : [item.query.select, item.query.orderBy, item.query.limit, item.query.offset]]);
    const group = groups.get(groupKey) ?? []; group.push(item); groups.set(groupKey, group);
  }
  const tasks: Item[][] = [];
  for (const group of groups.values()) {
    let batch: Item[] = [], size = 0;
    for (const item of group) {
      const count = item.query.aggregations?.length ?? 1;
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
          if (item.query.grouping || item.query.aggregations?.some(metric => metric.dateGrouping)) {
            const source = item.query.grouping?.source ?? item.query;
            const population = await fetchPreview<{ rows: Record<string, unknown>[]; total: number; complete: boolean; find?: Payload["find"] }>(
              `/api/query/${item.job.dataset}/population${item.search}`, signal, { where: source.where });
            if (!population.complete || population.rows.length !== population.total) throw new Error("The preview source is incomplete.");
            const now = clock.getTime();
            const prepared = item.query.grouping ? prepareGrouping(population.rows, item.job.schema, item.query.grouping, { now }) : undefined;
            const schema = prepared?.schema ?? item.job.schema;
            const rows = prepared?.rows ?? population.rows;
            const query = withoutGrouping(item.query);
            const page = applyQuery(rows, query, schema, { now });
            const metrics = evaluateMetrics(rows, query, schema, { now, shownRows: page.rows, maxRows: 10_000, maxGroups: 2000 });
            finish(item, { ...page, metrics: metrics.metrics, find: population.find }, schema);
          } else if (item.query.aggregations?.length) {
            const request = toMetricQuery(item.query, ctx.schema, ctx.execution);
            if (request.diagnostics.length) throw new Error(request.diagnostics.map(d => d.message).join(" · "));
            valid.push({ item, request });
          } else {
            const request = toServerQueryV2({ ...item.query, limit: Math.min(item.query.limit || 3, 3) }, ctx.schema, ctx.execution);
            if (request.diagnostics.length) throw new Error(request.diagnostics.map(d => d.message).join(" · "));
            finish(item, await fetchPreview<Payload>(`/api/query/${item.job.dataset}/rows-v2${item.search}`, signal, request), ctx.schema);
          }
        } catch (error) { if (!signal?.aborted) publish(item.job.id, undefined, error); }
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
    } catch (error) { if (!signal?.aborted) for (const { job } of items) publish(job.id, undefined, error); }
  }
  function finish(item: Item, payload: Payload, schema: FieldSchema) {
    if (signal?.aborted) return;
    if (cache.size >= 200) cache.delete(cache.keys().next().value!);
    cache.set(JSON.stringify([item.job.dataset, item.query, item.search]), { at: Date.now(), payload, schema });
    try { publish(item.job.id, previewOf(item.job, item.query, payload, schema)); } catch (error) { publish(item.job.id, undefined, error); }
  }
  let cursor = 0;
  async function work() { while (cursor < tasks.length && !signal?.aborted) await run(tasks[cursor++]); }
  await Promise.all([work(), work(), work()]);
}

export async function loadQueryPreview(dataset: QueryDataset, query: QueryState, schema: FieldSchema, options: PreviewOptions = {}, signal?: AbortSignal): Promise<QueryPreview> {
  let result: QueryPreview | undefined, failure: unknown;
  await loadQueryPreviews([{ id: "preview", dataset, query, schema, options }], (_id, preview, error) => { result = preview; failure = error; }, signal);
  if (failure) throw failure;
  if (!result) throw signal?.reason ?? new Error("Preview cancelled.");
  return result;
}
