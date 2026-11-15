import type { AggregationClause } from "@pythia-software/query-table-core";
import { recordedCacheLookups, recordedCacheReads } from "./usage-estimate-model";
import type { ChartSeries } from "./chart-series";

// The Tokens and Cost summary's metrics, and the token types its trend
// splits tokens into (see token-analysis-catalog.ts).

// Token types stack from the bottom in this order; adjacent colors pass the
// palette validator's normal-vision and color-blindness checks in both themes.
export const tokenTypes: ChartSeries[] = [
  { key: "uncached_input_tokens", label: "Uncached input", color: "var(--heat-3)" },
  { key: "cache_read_input_tokens", label: "Cache reads", color: "var(--heat-1)" },
  { key: "cache_creation_input_tokens", label: "Cache writes", color: "var(--gold)" },
  { key: "output_tokens", label: "Output", color: "var(--heat-2)" },
  { key: "unclassified_tokens", label: "Unclassified", color: "var(--muted)" },
];

const sum = (field: string, label: string): AggregationClause => ({ id: `${field}:`, op: "sum", field, groupBy: [], label });

export const tokenSummaryMetrics: AggregationClause[] = [
  sum("total_tokens", "Total tokens"),
  sum("cost_usd", "API cost"),
  sum("uncached_input_tokens", "Uncached input"),
  sum("cache_read_input_tokens", "Cache reads"),
  // The hit rate reads recorded usage only; see usage-estimate-model.
  { id: "recorded_cache_reads", op: "sum", expression: `SUM(${recordedCacheReads})`, groupBy: [], label: "Recorded cache reads" },
  { id: "recorded_cache_lookups", op: "sum", expression: `SUM(${recordedCacheLookups})`, groupBy: [], label: "Recorded cached and uncached input" },
  sum("output_tokens", "Output tokens"),
  { id: "agent_sessions", op: "count_distinct", field: "agent_session_id", groupBy: [], label: "Agent sessions" },
];
