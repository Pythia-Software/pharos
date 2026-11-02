import { EMPTY_QUERY, type WhereTerm } from "@pythia-software/query-table-core";
import type { ChartInspectionRange } from "./chart-inspection";
import type { ChartSeries } from "./chart-series";
import { withSlice } from "./chart-selection";
import { timeBounds, type TimeFields } from "./usage-chart";
import { orderedUsageQuery } from "./usage-query-order";
import { usageWorkspaceURL, usageDatasetTopics, type UsageDataset } from "./usage-workspace-model";

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
  return usageWorkspaceURL({ mode: "query", dataset, ...(!current.has("analyze_topic") ? { topic: usageDatasetTopics[dataset] } : {}), query: orderedUsageQuery(dataset, { ...EMPTY_QUERY, where: translate(filters) }) }, current);
}
