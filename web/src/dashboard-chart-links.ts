import {messageLabelConversationQuery} from "./message-label-results-model";
import { EMPTY_QUERY, type WhereClause, type WhereTerm } from "@pythia-software/query-table-core";
import type { ChartInspectionRange } from "./chart-inspection";
import type { ChartSeries } from "./chart-series";
import { withSlice } from "./chart-selection";
import { timeBounds, type TimeFields } from "./usage-chart";
import { orderedUsageQuery } from "./usage-query-order";
import { usageWorkspaceURL, usageDatasetTopics, type UsageDataset } from "./usage-workspace-model";
import { toolCallDimensions, toolWhereCanUseCalls } from "./generated-query-target";
import { mapTermPredicates } from "./where-terms";

const callMetrics = new Set(["call_count", "error_count", "result_tokens", "carried_tokens", "input_bytes", "result_bytes", "output_tokens", "lines_added", "lines_removed"]);
function toolCallChartWhere(where: WhereTerm[], fields: string[], translated: WhereTerm[]): WhereTerm[] | undefined {
  if (!toolWhereCanUseCalls(where) || !fields.every(field => callMetrics.has(field) || ["total_duration_ms", "timed_count", "truncated_count"].includes(field))) return;
  const metric = (clause: WhereClause): WhereClause | undefined => {
    if (toolCallDimensions.has(clause.field) || callMetrics.has(clause.field)) return clause;
    if (clause.op !== ">" || clause.value !== "0" || clause.negated) return;
    if (clause.field === "total_duration_ms") return { field: "duration_ms", op: ">", value: "0" };
    if (clause.field === "timed_count") return { field: "duration_ms", op: "is_not_null", value: "" };
    if (clause.field === "truncated_count") return { field: "truncated", op: "=", value: "true" };
  };
  const mapped = translated.map(term => mapTermPredicates(term, metric));
  return mapped.every((term): term is WhereTerm => Boolean(term)) ? mapped : undefined;
}

// Shared dashboard drilldowns keep record filters, raw dimension identities
// (including null/Other), and the actual contributing time window.
// translate turns the bounds into predicates the dataset can execute, as a
// catalog's executable does.
export function dashboardChartURL({ dataset, where, time, range, series, split, fields = [], current = new URLSearchParams(), translate = terms => terms }: {
  dataset: UsageDataset; where: WhereTerm[]; time: TimeFields; range: ChartInspectionRange;
  series: ChartSeries; split?: string | null; fields?: string[]; current?: URLSearchParams; translate?: (where: WhereTerm[]) => WhereTerm[];
}): string | undefined {
  if (split && series.value === undefined && !series.foldedValues?.length) return;
  const bounds = timeBounds(where, time);
  const from = bounds.from && bounds.from > range.from ? bounds.from : range.from;
  const to = bounds.to && bounds.to < range.to ? bounds.to : range.to;
  // Add the bucket's overlap bounds without replacing the source predicates:
  // first >= X and last >= X describe different populations. ISO timestamps
  // retain seconds and milliseconds when a saved range cuts through a bucket.
  let filters: WhereTerm[] = [...where,
    { field: time.last, op: ">=", value: from.toISOString() },
    { field: time.first, op: "<", value: to.toISOString() },
  ];
  if (split) filters = withSlice(filters, split, { values: (series.foldedValues ?? [series.value!]).map(value => value || null), excluded: false });
  const positive = fields.map(field => ({ field, op: ">" as const, value: "0" }));
  if (positive.length) filters.push(positive.length === 1 ? positive[0] : { any: positive });
  const translated = translate(filters);
  const calls = dataset === "tools" && time.first === "day" && time.last === "day"
    ? toolCallChartWhere(where, fields, translated) : undefined;
  const target = calls ? "tool_calls" : dataset;
  return usageWorkspaceURL({ mode: "query", dataset: target, ...(!current.has("analyze_topic") ? { topic: usageDatasetTopics[target] } : {}), query: dataset==="message_labels"?messageLabelConversationQuery({...EMPTY_QUERY,where:translated}):orderedUsageQuery(target, { ...EMPTY_QUERY, where: calls ?? translated }) }, current);
}
