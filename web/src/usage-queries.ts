import { EMPTY_QUERY, applyPatch, matchPatch, type GroupingDefinition, type QueryPatch, type QueryState, type SummaryColumn } from "@pythia-software/query-table-core";

export type UsageTablePreset = {
  label: string;
  title: string;
  grouping: GroupingDefinition;
  orderBy: QueryState["orderBy"];
  columns?: string[];
};

const sum = (field: string, label: string): SummaryColumn => ({ id: `usage_${field}`, op: "sum", field, label });
const cacheRate: SummaryColumn = { id: "usage_cache_hit_percent", op: "formula", expression: "SUM([cache_read_input_tokens])/NULLIF(SUM([cache_read_input_tokens])+SUM([uncached_input_tokens]),0)", label: "Cache hit %", display: { kind: "percent", decimals: 1 } };
const totals: SummaryColumn[] = [
  sum("cost_usd", "API cost"), sum("total_tokens", "Total tokens"), sum("output_tokens", "Output tokens"),
  { id: "usage_sessions", op: "count_distinct", field: "agent_session_id", label: "Agent sessions" }, cacheRate,
];
const ranked = [{ field: "usage_cost_usd", dir: "desc" as const, nulls: "last" as const }, { field: "usage_total_tokens", dir: "desc" as const }];
const grouped = (label: string, title: string, keys: string[], summaries = totals): UsageTablePreset => ({
  label, title, grouping: { keys, summaries, representative: null }, orderBy: ranked,
});
const calendar = (label: string, key: string) => ({ ...grouped(label, `Cost, tokens, distinct sessions and cache hit rate per ${label.toLowerCase()}`, [key]), orderBy: [{ field: key, dir: "desc" as const }] });
// The title opens the work itself; its row summary supplies the link label.
const workLink: SummaryColumn = { id: "usage_work", op: "link", label: "Work", link: { label: { column: "usage_work_title" }, to: { kind: "template", template: "/work/{field:workspace_id}" } } };
const work = grouped("Work", "Rank work by cost across days and models; each workspace retains its own identity", ["workspace_id"], [{ id: "usage_work_title", op: "row", field: "title", label: "Title" }, workLink, ...totals]);

export const usageGroupPresets: UsageTablePreset[] = [
  grouped("Repositories", "Compare repository cost, tokens, sessions and cache reuse", ["repository_name"]),
  grouped("Models", "Compare models within each provider", ["provider", "model_family"]),
  grouped("Model + effort", "Keep each provider, model and recorded effort level separate, including missing effort", ["provider", "model_family", "effort"]),
  grouped("Providers", "Compare provider cost, tokens, sessions and cache reuse", ["provider"]),
  grouped("Agents", "Compare root agents and subagents within each provider", ["provider", "session_kind"]),
  { ...work, grouping: { ...work.grouping, representative: { orderBy: [{ field: "last_usage_at", dir: "desc", nulls: "last" }] } },
    columns: [workLink.id, "workspace_id", ...totals.map(column => column.id)] },
  calendar("Day", "day"), calendar("Week", "week"), calendar("Month", "month"),
];

export const usageComparisonPresets: UsageTablePreset[] = [
  { ...grouped("Cache efficiency", "Compare weighted cache hit rates and cached, uncached and written input by model", ["provider", "model_family"], [
    ...totals, sum("uncached_input_tokens", "Uncached input"), sum("cache_read_input_tokens", "Cache reads"), sum("cache_creation_input_tokens", "Cache writes"),
  ]), orderBy: [{ field: cacheRate.id, dir: "asc", nulls: "last" }] },
  grouped("Price changes", "Compare historical and today's API prices for the same usage, per month", ["month"], [
    sum("cost_usd", "Historical API cost"), sum("cost_today_usd", "At today's prices"),
    // Compare only rows with both prices so missing prices cannot create a false difference.
    { id: "usage_price_delta", op: "formula", expression: "SUM([cost_today_usd]-[cost_usd])", label: "Price difference" },
    sum("total_tokens", "Total tokens"), { id: "usage_price_pairs", op: "formula", expression: "COUNT([cost_today_usd]-[cost_usd])", label: "Rows with both prices" },
  ]),
  grouped("Price coverage", "Keep priced, assumed, partial and unpriced usage visible alongside cost and tokens", ["price_status"]),
];

// A preset owns the grouping and the result layout. Applying it keeps the
// source stage (filters, columns, metrics) and the page size.
export function usageTablePresetPatch(preset: UsageTablePreset): QueryPatch {
  return { id: `usage:${preset.label}`, owns: ["grouping", "columns", "order"], query: {
    grouping: { ...preset.grouping, source: EMPTY_QUERY },
    select: (preset.columns ?? [...preset.grouping.keys, ...preset.grouping.summaries.map(column => column.id)]).map(field => ({ field })),
    orderBy: preset.orderBy,
  } };
}

export function applyUsageTablePreset(query: QueryState, preset: UsageTablePreset): QueryState {
  return applyPatch(query, usageTablePresetPatch(preset));
}

// Match calculations and keys, while allowing sorting, HAVING, paging and column layout edits.
export function usageTablePresetMatches(query: QueryState, preset: UsageTablePreset): boolean {
  if (!query.grouping) return false;
  const patch = usageTablePresetPatch(preset);
  return matchPatch({ ...query, where: [] }, { ...patch, owns: ["grouping"] }).status === "active";
}
