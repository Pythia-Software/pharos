import type { AggregationClause } from "@pythia-software/query-table-core";
import { isHourly } from "./chart-period";
import type { ChartSeries } from "./chart-series";
import type { TokenChartView, TokenSplit } from "./usage-presets";

// The metrics the Usage page's Token chart and summary ask /metrics for.

// Token types stack from the bottom in this order; adjacent colors pass the
// palette validator's normal-vision and color-blindness checks in both themes.
export const tokenTypes: ChartSeries[] = [
  { key: "uncached_input_tokens", label: "Uncached input", color: "var(--heat-3)" },
  { key: "cache_read_input_tokens", label: "Cache reads", color: "var(--heat-1)" },
  { key: "cache_creation_input_tokens", label: "Cache writes", color: "var(--gold)" },
  { key: "output_tokens", label: "Output", color: "var(--heat-2)" },
  { key: "unclassified_tokens", label: "Unclassified", color: "var(--muted)" },
];

const sum = (field: string, groupBy: string[], label: string): AggregationClause => ({ id: `${field}:${groupBy.join(",")}`, op: "sum", field, groupBy, label });

export const costMetric = (view: Pick<TokenChartView, "metric">) => view.metric === "cost" || view.metric === "percent_cost";

// Cost is priced per request, not per token type, so a cost chart split by
// type shows providers instead.
export function tokenChartSplit(view: Pick<TokenChartView, "metric" | "split">): TokenSplit {
  return costMetric(view) && view.split === "type" ? "provider" : view.split;
}

// tokenChartMetrics groups by the Usage field of the chart's period: several
// hours or days are summed from hourly or daily buckets in the browser. Split
// by token type it sums each type; otherwise it sums tokens or cost by split.
export function tokenChartMetrics(view: Pick<TokenChartView, "metric" | "split" | "period">): AggregationClause[] {
  const period = isHourly(view.period) ? "hour" : view.period === "days" ? "day" : view.period, split = tokenChartSplit(view);
  return split === "type" ? tokenTypes.map(type => sum(type.key, [period], type.label))
    : [sum(costMetric(view) ? "cost_usd" : "total_tokens", [period, split], "chart")];
}

export const tokenSummaryMetrics: AggregationClause[] = [
  sum("total_tokens", [], "Total tokens"),
  sum("cost_usd", [], "API cost"),
  sum("uncached_input_tokens", [], "Uncached input"),
  sum("cache_read_input_tokens", [], "Cache reads"),
  sum("output_tokens", [], "Output tokens"),
  { id: "agent_sessions", op: "count_distinct", field: "agent_session_id", groupBy: [], label: "Agent sessions" },
];
