import { EMPTY_QUERY, encodeQuery, type AggregationClause, type OrderByClause, type QueryState } from "@pythia-software/query-table-core";
import type { ChartPeriod } from "./chart-period";
import type { ChartScale } from "./chart-scale";
import type { ChartPresentation } from "./chart-selection";
import type { ChartDisplay } from "./chart-series";

export type TokenSplit = "type" | "provider" | "model_family" | "model_effort" | "effort" | "repository_name" | "session_kind";
export type TokenChartView = ChartPresentation & { metric: "tokens" | "cost" | "percent" | "percent_cost"; split: TokenSplit; period: ChartPeriod; hours: number; days: number; rollingPeriods: number; scale: ChartScale; display?: ChartDisplay };
// Chart shortcuts and the writing page's metric/sort shortcuts.
export type UsagePreset = { label: string; title: string; aggregations?: AggregationClause[]; chart?: Partial<TokenChartView>; orderBy?: OrderByClause[] };
const sum = (field: string, groupBy: string[], label: string): AggregationClause => ({ id: `${field}:${groupBy.join(",")}`, op: "sum", field, groupBy, label });
export const usageChartPresets: UsagePreset[] = [
  { label: "Daily by provider", title: "Chart total tokens per day, split by provider", chart: { metric: "tokens", split: "provider", period: "day" } },
  { label: "Weekly cache mix", title: "Chart uncached input, cache reads, cache writes, and output per week", chart: { metric: "tokens", split: "type", period: "week" } },
  { label: "Daily cost", title: "Chart API-equivalent cost per day, split by provider", chart: { metric: "cost", split: "provider", period: "day" } },
  { label: "Model + effort", title: "Chart tokens for each recorded model and effort pair", chart: { metric: "tokens", split: "model_effort", period: "week" } },
  { label: "Subagents", title: "Chart root agent versus subagent tokens per week", chart: { metric: "tokens", split: "session_kind", period: "week" } },
  { label: "Subagent cost", title: "Chart root agent versus subagent cost per week", chart: { metric: "cost", split: "session_kind", period: "week" } },
];

const byDesc = (field: string): OrderByClause[] => [{ field, dir: "desc" }];
export const writingPresets: UsagePreset[] = [
  { label: "Deepest conversations", title: "Conversations with the most words you typed", orderBy: byDesc("typed_words") },
  { label: "Most hands-on", title: "Conversations with the most messages you typed into", orderBy: byDesc("typed_turns") },
  { label: "Longest messages", title: "Conversations holding your longest single typed message", orderBy: byDesc("longest_typed_words") },
  { label: "Most pasted-in", title: "Conversations with the most text that looks pasted", orderBy: byDesc("pasted_words") },
  { label: "Most copied from agents", title: "Conversations with the most agent output or earlier messages copied in", orderBy: byDesc("copied_words") },
];
export const writingMetricPresets: UsagePreset[] = [
  { label: "By repository", title: "Typed and pasted words by repository", aggregations: [sum("typed_words", ["repository_name"], "Typed words by repository"), sum("pasted_words", ["repository_name"], "Likely pasted words by repository")] },
  { label: "By provider", title: "Typed words and tokens by provider", aggregations: [sum("typed_words", ["providers"], "Typed words by provider"), sum("total_tokens", ["providers"], "Tokens by provider")] },
  { label: "By source", title: "Typed words by source app", aggregations: [sum("typed_words", ["source_kind"], "Typed words by source")] },
];

const byAsc = (field: string): OrderByClause[] => [{ field, dir: "asc" }];
export const messagePresets: UsagePreset[] = [
  { label: "Newest", title: "Most recent messages first", orderBy: byDesc("sent_at") },
  { label: "Longest typed", title: "Messages with the most words you typed", orderBy: byDesc("typed_words") },
  { label: "Most pasted", title: "Messages with the most text that looks pasted", orderBy: byDesc("pasted_words") },
  { label: "Most copied", title: "Messages with the most agent output or earlier messages copied in", orderBy: byDesc("copied_words") },
  { label: "Least typed", title: "Messages whose text is least your own", orderBy: [...byAsc("typed_share"), ...byDesc("words")] },
];
export const messageMetricPresets: UsagePreset[] = [
  { label: "By category", title: "Messages by the category holding most of their words", aggregations: [{ id: "messages:main_category", op: "count", groupBy: ["main_category"], label: "Messages by main category" }, sum("other_words", ["main_category"], "Words from elsewhere by main category")] },
  { label: "Per week", title: "Typed and pasted words per week", aggregations: [sum("typed_words", ["week"], "Typed words per week"), sum("pasted_words", ["week"], "Likely pasted words per week")] },
  { label: "By repository", title: "Typed words and messages by repository", aggregations: [sum("typed_words", ["repository_name"], "Typed words by repository"), sum("message_count", ["repository_name"], "Messages by repository")] },
];

// Compare what each preset controls. Filters, paging, and unrelated settings
// can change while its sort, metrics, or chart still match.
export function presetMatches(preset: UsagePreset, query: QueryState, chart?: TokenChartView): boolean {
  if (preset.chart) return Boolean(chart && Object.entries(preset.chart).every(([key, value]) => chart[key as keyof TokenChartView] === value));
  if (preset.orderBy) return query.orderBy.length === preset.orderBy.length && preset.orderBy.every((clause, index) => {
    const current = query.orderBy[index];
    return current.field === clause.field && current.dir === clause.dir && (current.nulls ?? "last") === (clause.nulls ?? "last") && current.extract?.regex === clause.extract?.regex;
  });
  // IDs and presentation choices do not change the metric being calculated.
  const metricToken = (aggregations: AggregationClause[]) => encodeQuery({ ...EMPTY_QUERY, aggregations: aggregations.map(({ id, label, display, layout, diagnostics, ...clause }, index) => ({ ...clause, id: String(index) })) });
  return Boolean(preset.aggregations && metricToken(query.aggregations ?? []) === metricToken(preset.aggregations));
}

// A sort preset replaces the sort and a metric preset the metrics; null clears
// the metrics. Either returns to the first page.
export function applyUsagePreset(query: QueryState, preset: UsagePreset | null): QueryState {
  return { ...query, ...(preset?.orderBy ? { orderBy: preset.orderBy } : { aggregations: preset?.aggregations ?? [] }), offset: 0 };
}
