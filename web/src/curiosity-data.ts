import { EMPTY_QUERY, toServerQuery, type AggregationClause, type FieldSchema, type QueryState, type WhereTerm } from "@pythia-software/query-table-core";
import { chartSeriesStyle } from "./chart-colors";
import { categoryForQuery, queryCategories, type QueryCategory } from "./query-categories";
import { foldSeries, type ChartBucket, type ChartSeries } from "./chart-series";
import { queryURL, resolveRollingQuery, rollingBounds, type QueryDataset, type RollingWindow } from "./query-links";

export type QueryRecipe = {
  id: string; idea: string; title: string; description: string; dataset: QueryDataset;
  field: string; days?: number[]; where?: WhereTerm[]; measures: AggregationClause[];
  unit: string; category: QueryCategory; scope?: string; note?: string;
};
export type QuerySuggestion = QueryRecipe & { query: QueryState; window: RollingWindow; url: string; instanceId: string };
export type QueryPreview = {
  state: "ready" | "empty"; total: number; summary: string; note?: string;
  ranking?: { label: string; value: number; color: string }[];
  buckets?: ChartBucket[]; series?: ChartSeries[]; excerpt?: string;
};
const agg = (op: AggregationClause["op"], field: string, groupBy: string[], label: string): AggregationClause => ({ id: `${op}:${field}:${groupBy.join(",")}`, op, ...(field ? { field } : {}), groupBy, label });
const count = (group: string[], label: string) => agg("count", "", group, label);
const sum = (field: string, group: string[], label: string) => agg("sum", field, group, label);
const positive = (field: string): WhereTerm => ({ field, op: ">", value: "0" });
const equal = (field: string, value: string): WhereTerm => ({ field, op: "=", value });
const calls = { dataset: "tool_calls" as const, field: "started_at", unit: "calls" };
const usage = { dataset: "usage" as const, field: "first_usage_at", unit: "tokens" };

// Only executable, evidence-based recipes belong here. The full idea tracker
// records richer variants we cannot answer at their required grain yet.
export const queryRecipes: QueryRecipe[] = [
  { id: "correction-phrases", category: "steering", idea: "conversation-02", title: "Where am I correcting course?", description: "Your messages containing explicit correction phrases.", dataset: "writing_messages", field: "sent_at", unit: "messages", where: [equal("typed_message", "true"), { any: ["you misunderstand", "you misunderstood", "that's not what", "that is not what", "I meant"].map(value => ({ field: "text", op: "contains" as const, value })) }], measures: [], note: "Literal phrase matches; not a semantic classification" },
  { ...calls, id: "tool-failures", category: "errors", idea: "tools-01", title: "Which tools keep failing?", description: "The calls behind recurring tool failures.", days: [1, 7, 30], where: [positive("error_count")], measures: [sum("error_count", ["tool_name"], "Failures by tool")] },
  { ...calls, id: "error-signatures", category: "errors", idea: "tools-02", title: "What error keeps coming back?", description: "Repeated error signatures, with the original calls.", where: [positive("error_count"), { field: "error_signature", op: "is_not_null", value: "" }], measures: [count(["error_signature"], "Calls by error signature")] },
  { ...calls, id: "slow-commands", category: "tools", idea: "tools-05", title: "Where is the waiting?", description: "Recorded elapsed time by command; includes approval waits.", where: [positive("duration_ms"), { field: "command_name", op: "is_not_null", value: "" }], measures: [sum("duration_ms", ["command_name"], "Recorded elapsed time by command")], unit: "ms" },
  { ...calls, id: "edit-failures", category: "errors", idea: "tools-07", title: "Where do edits fail?", description: "Failed edit calls, grouped by repository path.", where: [positive("error_count"), equal("tool_category", "edit")], measures: [count(["repo_path"], "Failed edits by path")] },
  { ...calls, id: "test-failures", category: "errors", idea: "verification-01", title: "Which checks need attention?", description: "Recorded test failures by command, ready to inspect.", where: [equal("test_failure", "true")], measures: [count(["command_name"], "Calls with test failures")] },
  { ...calls, id: "context-carried", category: "context", idea: "context-01", title: "What keeps riding in context?", description: "Tool output carried into later requests, in attributed tokens.", where: [positive("carried_tokens")], measures: [sum("carried_tokens", ["tool_name"], "Context carried by tool")], unit: "tokens", note: "Measured or estimated context attribution" },
  { ...calls, id: "large-outputs", category: "context", idea: "context-02", title: "What fills the context?", description: "Result tokens added by each tool; inspect the largest outputs.", where: [positive("result_tokens")], measures: [sum("result_tokens", ["tool_name"], "Result tokens by tool")], unit: "tokens", note: "Measured or estimated result tokens" },
  { ...calls, id: "truncated-outputs", category: "context", idea: "context-02", title: "What gets cut short?", description: "Truncated tool outputs and the calls that produced them.", where: [equal("truncated", "true")], measures: [count(["tool_name"], "Truncated outputs by tool")] },
  { ...calls, id: "read-paths", category: "context", idea: "context-08", title: "What do agents read?", description: "Read calls by repository path, with their source evidence.", where: [equal("tool_category", "read")], measures: [count(["repo_path"], "Read calls by path")] },
  { ...calls, id: "research-queries", category: "research", idea: "memory-02", title: "What keeps getting researched?", description: "Exact recorded search queries ranked by frequency.", where: [{ field: "search_query", op: "is_not_null", value: "" }], measures: [count(["search_query"], "Calls by exact search query")] },
  { ...calls, id: "primary-domains", category: "research", idea: "memory-01", title: "Where does research lead?", description: "Primary recorded hosts on tool calls; open the source URLs.", where: [{ field: "host", op: "is_not_null", value: "" }], measures: [count(["host"], "Calls by primary host")], note: "Primary hosts; not every reached URL" },
  { ...calls, id: "mcp-methods", category: "tools", idea: "skills-06", title: "Which MCP methods do I use?", description: "Recorded method calls across connected tools.", where: [{ field: "mcp_method", op: "is_not_null", value: "" }], measures: [count(["mcp_method"], "Calls by MCP method")] },
  { id: "skill-evidence", category: "skills", idea: "skills-01", title: "Which skills are showing up?", description: "Invocations and observed loads; evidence of use, not compliance.", dataset: "skill_usages", field: "created_at", unit: "records", measures: [count(["skill_name"], "Evidence by skill")] },
  { id: "skill-load-failures", category: "errors", idea: "skills-04", title: "Which skill loads fail?", description: "Skill evidence whose parent call recorded an error.", dataset: "skill_usages", field: "created_at", unit: "records", where: [equal("status", "error")], measures: [count(["skill_name"], "Failed skill evidence")] },
  { id: "human-words", category: "human", idea: "human-01", title: "How much am I writing?", description: "Words classified as typed or dictated, day by day.", dataset: "writing_messages", field: "sent_at", unit: "words", where: [positive("typed_words")], measures: [sum("typed_words", ["day"], "Your words per day")] },
  { id: "explaining-repos", category: "human", idea: "human-02", title: "Where do I explain the most?", description: "Your typed words across repositories in this window.", dataset: "writing_messages", field: "sent_at", unit: "words", where: [positive("typed_words")], measures: [sum("typed_words", ["repository_name"], "Your words by repository")], scope: "repository_name" },
  { ...usage, id: "usage-models", category: "models", idea: "models-01", title: "Which models do the work?", description: "Token use by model, day by day.", where: [positive("total_tokens")], measures: [sum("total_tokens", ["day", "model_family"], "Daily tokens by model")], scope: "model_family" },
  { ...usage, id: "usage-repos", category: "usage", idea: "cost-01", title: "Where does my usage go?", description: "Daily tokens across your repositories.", where: [positive("total_tokens")], measures: [sum("total_tokens", ["day", "repository_name"], "Daily tokens by repository")], scope: "repository_name" },
  { ...usage, id: "cost-repos", category: "cost", idea: "cost-01", title: "What is the API equivalent?", description: "Recorded usage priced at standard API rates, by repository.", where: [positive("cost_usd")], measures: [sum("cost_usd", ["day", "repository_name"], "Daily API equivalent by repository")], unit: "USD", scope: "repository_name", note: "API equivalent; partial and assumed pricing can contribute" },
  { ...usage, id: "cache-reads", category: "context", idea: "cost-03", title: "Where is caching used?", description: "Recorded cache-read tokens by model; inspect usage details.", where: [positive("cache_read_input_tokens")], measures: [sum("cache_read_input_tokens", ["day", "model_family"], "Daily cache-read tokens by model")], scope: "model_family" },
  { ...usage, id: "subagent-usage", category: "delegation", idea: "models-06", title: "What happens in subagents?", description: "Attributed subagent tokens across repositories.", where: [equal("session_kind", "subagent"), positive("total_tokens")], measures: [sum("total_tokens", ["day", "repository_name"], "Daily subagent tokens")], scope: "repository_name" },
];

export function instantiateRecipe(recipe: QueryRecipe, days: number, clock = new Date()): QuerySuggestion {
  const window = { days, field: recipe.field };
  const query = resolveRollingQuery({ ...EMPTY_QUERY, where: recipe.where ?? [], aggregations: recipe.measures }, window, clock);
  return { ...recipe, window, query, url: queryURL(recipe.dataset, query, window), instanceId: `${recipe.id}:${days}` };
}

export function shuffled<T>(items: T[]): T[] {
  const result = [...items];
  for (let i = result.length - 1; i > 0; i--) { const j = Math.floor(Math.random() * (i + 1)); [result[i], result[j]] = [result[j], result[i]]; }
  return result;
}

export function suggestionPool(clock = new Date()): QuerySuggestion[] {
  // One explicit window per question per visit. Keep the mix broad, without
  // filling the belt with three nearly identical windows of the same card.
  return shuffled(queryRecipes).map((recipe, index) => instantiateRecipe(recipe, (recipe.days ?? [7, 30, 90])[index % (recipe.days?.length ?? 3)], clock));
}

export function formatPreviewValue(value: number, unit: string): string {
  if (unit === "USD") return value.toLocaleString(undefined, { style: "currency", currency: "USD", maximumFractionDigits: value >= 100 ? 0 : 2 });
  if (unit === "ms") return value >= 3_600_000 ? `${(value / 3_600_000).toFixed(1)}h` : value >= 60_000 ? `${(value / 60_000).toFixed(1)}m` : `${(value / 1000).toFixed(1)}s`;
  return Intl.NumberFormat(undefined, { notation: value >= 10000 ? "compact" : "standard", maximumFractionDigits: 1 }).format(value);
}

const cache = new Map<string, { at: number; value: QueryPreview }>();
export function clearPreviewCache() { cache.clear(); }
type Metric = { id: string; buckets: { keys: unknown[]; value: unknown; count?: number }[] };
const empty: QueryPreview = { state: "empty", total: 0, summary: "No matching observations" };

async function fetchPreview(url: string, request: unknown, signal?: AbortSignal) {
  const controller = new AbortController();
  const abort = () => controller.abort(signal?.reason);
  signal?.addEventListener("abort", abort, { once: true });
  if (signal?.aborted) abort();
  const timer = setTimeout(() => controller.abort(new Error("Preview timed out. Open the view to explore it directly.")), 15_000);
  try {
    const response = await fetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, signal: controller.signal, body: JSON.stringify(request) });
    const body = await response.json();
    if (!response.ok) throw new Error(body.error || "Preview unavailable");
    return body;
  } finally {
    clearTimeout(timer); signal?.removeEventListener("abort", abort);
  }
}

export async function loadQueryPreview(dataset: QueryDataset, query: QueryState, schema: FieldSchema<Record<string, any>>, options: { window?: RollingWindow; recipe?: QueryRecipe; url?: string } = {}, signal?: AbortSignal): Promise<QueryPreview> {
  const tone = queryCategories[options.recipe?.category ?? categoryForQuery(dataset, query)].tone;
  const resolved = resolveRollingQuery(query, options.window), key = JSON.stringify([dataset, resolved, options.url]);
  const cached = cache.get(key);
  if (cached && Date.now() - cached.at < 60_000) return cached.value;
  const request = toServerQuery(resolved, schema), params = new URL(options.url ?? "/", location.origin).searchParams;
  const predicateCount = (terms: WhereTerm[]) => terms.reduce((count, term) => count + ("any" in term ? term.any.length : 1), 0);
  if (request.where.length !== resolved.where.length || predicateCount(request.where) !== predicateCount(resolved.where)) throw new Error("This saved view needs computed filters that cannot yet run over the full dataset.");
  const find = new URLSearchParams();
  if (dataset === "library" && params.get("search")) {
    find.set("find", params.get("search")!);
    for (const [source, target] of [["kind", "find_kind"], ["case", "find_case"], ["fuzzy", "find_fuzzy"], ["separators", "find_separators"]]) if (params.has(source)) find.set(target, params.get(source)!);
  }
  const aggregated = (resolved.aggregations ?? []).length > 0;
  const body = await fetchPreview(`/api/query/${dataset}${aggregated ? "/aggregations" : ""}${find.size ? `?${find}` : ""}`,
    aggregated ? { where: request.where, aggregations: resolved.aggregations } : { ...request, limit: 3, offset: 0 }, signal);
  let preview: QueryPreview = empty;
  if (!aggregated) {
    const total = Number(body.total) || 0, row = body.rows?.[0];
    if (total > 0) preview = { state: "ready", total, summary: `${formatPreviewValue(total, "")} matching ${options.recipe?.unit ?? "rows"}`, excerpt: String(row?.text ?? row?.title ?? row?.command ?? "Open the saved view").slice(0, 180), note: options.recipe?.note };
  } else {
    const metric: Metric | undefined = body.metrics?.find((item: Metric) => item.id === resolved.aggregations![0].id);
    const supported = metric?.buckets.filter(bucket => bucket.value !== null && bucket.value !== undefined && bucket.count !== 0) ?? [];
    const buckets = supported.filter(bucket => Number.isFinite(Number(bucket.value)));
    if (supported.length && !buckets.length) preview = { state: "ready", total: supported.length, summary: `${supported.length} result groups`, excerpt: String(supported[0].value).slice(0, 180) };
    const clause = resolved.aggregations![0], recipe = options.recipe, unit = recipe?.unit ?? (clause.op === "count" ? "rows" : clause.field === "cost_usd" ? "USD" : clause.field?.includes("tokens") ? "tokens" : clause.field?.includes("words") ? "words" : clause.field === "duration_ms" ? "ms" : "");
    const total = buckets.reduce((sum, bucket) => sum + Number(bucket.value), 0);
    if (buckets.length && (total !== 0 || buckets.some(bucket => bucket.keys.length > 0 || (bucket.count ?? 0) > 0))) {
      const scope = recipe?.scope ?? clause.groupBy.at(-1) ?? "query";
      const label = (keys: unknown[]) => keys.map((key, index) => key === null || key === "" ? "Unspecified" : String(key)).join(" · ");
      const additive = ["sum", "count"].includes(clause.op);
      preview = { state: "ready", total, summary: additive || clause.groupBy.length === 0 ? `${formatPreviewValue(total, unit)}${unit && !["USD", "ms"].includes(unit) ? ` ${unit}` : ""}` : `${buckets.length} groups · ${clause.label ?? clause.op}`, note: recipe?.note };
      if (buckets.length >= 2000 && ["tool_calls", "skill_usages", "tools", "writing_messages"].includes(dataset)) {
        preview.summary = `Shown: ${preview.summary}`;
        preview.note = [preview.note, "This result may have reached the server's group limit."].filter(Boolean).join(" · ");
      }
      if (clause.groupBy[0] === "day" && clause.op === "sum") {
        const entries = buckets.map(bucket => [String(bucket.keys[0]), bucket.keys.length > 1 ? bucket.keys.slice(1).map(key => String(key ?? "Unspecified")).join(" · ") : recipe?.title ?? "Total", Number(bucket.value)] as [string, string, number]);
        const days = options.window ? options.window.days : Math.min(90, new Set(entries.map(([day]) => day)).size);
        const start = options.window ? new Date(rollingBounds(days)[0]) : new Date(`${entries.map(([day]) => day).sort()[0]}T12:00:00`);
        const chart: ChartBucket[] = [];
        for (let i = 0; i < days; i++) { const date = new Date(start); date.setDate(date.getDate() + i); const key = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`; chart.push({ key, date, values: {} }); }
        if (!options.window) {
          const observed = [...new Set(entries.map(([key]) => key))].sort();
          chart.splice(0, chart.length, ...observed.slice(-90).map(key => ({ key, date: new Date(`${key}T12:00:00`), values: {} })));
          if (observed.length > 90) preview.note = [preview.note, "Chart shows the last 90 observed day buckets."].filter(Boolean).join(" · ");
        }
        const byKey = new Map(chart.map(bucket => [bucket.key, bucket]));
        preview.buckets = chart;
        preview.series = foldSeries(entries, byKey, name => name || "Unspecified", undefined, [], 4, scope);
        if (clause.groupBy.length === 1 && preview.series[0]) preview.series[0].color = tone;
      } else preview.ranking = [...buckets].sort((a, b) => Number(b.value) - Number(a.value)).slice(0, 3).map(bucket => ({ label: label(bucket.keys) || clause.label || "Total", value: Number(bucket.value), color: ["repository_name", "model_family", "provider"].includes(scope) ? chartSeriesStyle(scope, String(bucket.keys[0])).color : tone }));
    }
  }
  if (cache.size >= 200) cache.delete(cache.keys().next().value!);
  cache.set(key, { at: Date.now(), value: preview });
  return preview;
}
