import type { AggregationClause, FieldSchema } from "@pythia-software/query-table-core";
import { postMetrics, type MetricResult } from "./metric-request";
import { loadFollowing, type ResultPolicy } from "./result-cache";

// Settings' Health card "Words you wrote": the writing in every classified
// user message, and in the last 30 days, read like the Human Words dashboard
// (writing_messages through query-v3). Whether the estimates are built is
// /api/authorship's status, which the card reads separately.

export type WritingTotals = { typed: number; typedMessages: number; recentTyped: number };

// The last 30 days are today and the 29 before it, from local midnight: a
// calendar bound, so a stored answer turns over once a day.
export function writingTotalMetrics(now: Date): AggregationClause[] {
  const since = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 29);
  return [
    { id: "typed", op: "sum", field: "typed_words", groupBy: [] },
    { id: "typed_messages", op: "sum", expression: "SUM(IF([typed_words]>0,1,0))", groupBy: [] },
    { id: "recent_typed", op: "sum", field: "typed_words", groupBy: [], where: [{ field: "sent_at", op: ">=", value: since.toISOString() }] },
  ];
}

// A sum over no messages is no words. A metric that failed has already
// failed the read (metricResults), so nothing here stands in for it.
export function readWritingTotals(results: MetricResult[]): WritingTotals {
  const value = (id: string) => Number(results.find(metric => metric.id === id)?.buckets[0]?.value ?? 0);
  return { typed: value("typed"), typedMessages: value("typed_messages"), recentTyped: value("recent_typed") };
}

// Publishes the totals, and again in place while a stored answer is being
// brought up to date.
export function loadWritingTotals(schema: FieldSchema<any>, publish: (totals: WritingTotals) => void, signal: AbortSignal, policy: ResultPolicy = "cached", now = new Date()): Promise<void> {
  const clauses = writingTotalMetrics(now);
  return loadFollowing(scoped => postMetrics("writing_messages", schema, [], clauses, scoped).then(readWritingTotals), totals => publish(totals), signal, policy);
}
