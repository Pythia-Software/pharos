import type { MetricDisplay, MetricValue, MetricValueFormat } from "@pythia-software/query-table-core";
import { chartSeriesStyle } from "./chart-colors";
import type { QueryPreview } from "./query-previews";

function compactAxisFormat(format: MetricValueFormat | undefined, values: (MetricValue | undefined)[]): MetricValueFormat {
  if (format) return format;
  const numeric = values.filter((value): value is number => typeof value === "number" && Number.isFinite(value));
  const span = numeric.length ? Math.max(...numeric) - Math.min(...numeric) : 0;
  const scale = span || Math.max(0, ...numeric.map(Math.abs));
  // A small cover needs readable ticks, while sub-unit ranges still need
  // enough precision to distinguish them. Coordinates retain the raw values.
  return { kind: "number", decimals: scale ? Math.max(0, Math.min(6, 2 - Math.floor(Math.log10(scale)))) : 0 };
}

// Presentation-only decisions. Never collapse a partial result, distribution,
// relationship or timeline into a fabricated scalar or modify the saved query.
export function compactMetricPresentation(preview: QueryPreview) {
  let metric = preview.metrics?.find(item => item.clause.id === preview.coverMetricId);
  if (!metric) return;
  if (metric.clause.groupBy.length === 1 && metric.clause.groupBy[0] === "effort" && ["pie", "donut"].includes(metric.clause.display?.kind ?? "")) metric = { ...metric, result: { ...metric.result, buckets: [...metric.result.buckets].sort((a, b) => chartSeriesStyle("effort", String(a.keys[0] ?? "Unspecified")).order!.localeCompare(chartSeriesStyle("effort", String(b.keys[0] ?? "Unspecified")).order!, undefined, { numeric: true })) } };
  const { clause, result } = metric;
  const originalKind = clause.display?.kind ?? "auto";
  const scalar = !preview.stackBy && result.coverage !== "partial" && (result.groupCount ?? result.buckets.length) === 1 && result.buckets.length === 1
    && typeof result.buckets[0].value === "number" && Number.isFinite(result.buckets[0].value) && !result.buckets[0].error && !result.buckets[0].yError
    && !clause.distribution && !clause.expressionY && !clause.dateGrouping && clause.groupBy[0] !== "day";
  const compoundList = (clause.groupBy.length > 1 || Boolean(preview.groupLabels)) && ["auto", "table", "list", "bar-horizontal"].includes(originalKind);
  const kind = scalar ? "value" : compoundList || originalKind === "bar-horizontal" ? "list" : originalKind;
  const budget = clause.display?.maxGroups ?? (kind === "box" ? 3 : ["scatter", "line", "bar-vertical"].includes(kind) ? 24 : ["pie", "donut"].includes(kind) ? 8 : 4);
  const shown = Math.min(result.buckets.length, budget), total = result.groupCount ?? result.buckets.length;
  const groupLabel = scalar ? preview.groupLabels?.[JSON.stringify(result.buckets[0].keys)] ?? result.buckets[0].keys.map(key => String(key ?? "Unspecified")).join(" · ") : undefined;
  const ranked = !preview.buckets && !scalar && (["auto", "list", "table", "bar-horizontal"].includes(kind) || Boolean(preview.groupSummary));
  const longLabels = result.buckets.slice(0, budget).some(bucket => (preview.groupLabels?.[JSON.stringify(bucket.keys)] ?? bucket.keys.map(key => String(key ?? "Unspecified")).join(" · ")).length > 20);
  const listOptions: MetricDisplay["list"] = ranked && ["auto", "list"].includes(kind) ? { ...clause.display?.list, barPosition: clause.display?.list?.barPosition ?? (longLabels ? "below" : "inline") } : clause.display?.list;
  if (listOptions && clause.groupBy.some(field => field === "model_family" || field === "root_model_family") && listOptions.useGroupColors === undefined) listOptions.useGroupColors = true;
  if (ranked && ["auto", "list"].includes(kind) && listOptions?.barPosition === "below" && listOptions.maxLabelLines === undefined) listOptions.maxLabelLines = 2;
  let summary = scalar || ranked ? undefined : preview.summary;
  const headlineMetric = scalar || ranked ? undefined : preview.headlineMetric;
  const axisFormats = kind === "scatter" ? {
    xFormat: compactAxisFormat(clause.display?.xFormat, result.buckets.slice(0, budget).map(bucket => bucket.value)),
    yFormat: compactAxisFormat(clause.display?.yFormat, result.buckets.slice(0, budget).map(bucket => bucket.y)),
  } : {};
  const axisScales: Pick<MetricDisplay, "xScale" | "yScale"> = {
    xScale: { ...clause.display?.xScale, mode: kind === "scatter" ? clause.display?.xScale?.mode ?? "linear" : "linear" },
    yScale: { ...clause.display?.yScale, mode: kind === "scatter" ? clause.display?.yScale?.mode ?? "linear" : "linear" },
  };
  const omittedLogPoints = kind === "scatter" ? result.buckets.slice(0, budget).filter(bucket =>
    !bucket.error && !bucket.yError && typeof bucket.value === "number" && Number.isFinite(bucket.value) && typeof bucket.y === "number" && Number.isFinite(bucket.y)
    && (axisScales.xScale?.mode === "log" && bucket.value <= 0 || axisScales.yScale?.mode === "log" && bucket.y <= 0)).length : 0;
  if (omittedLogPoints) summary = [summary, `${omittedLogPoints.toLocaleString()} zero/negative ${omittedLogPoints === 1 ? "point" : "points"} omitted`].filter(Boolean).join(" · ");
  return { metric, scalar, kind, budget, shown, total, groupLabel, summary, headlineMetric, ranked, listOptions, axisFormats, axisScales, omittedLogPoints, friendlyKeys: !scalar && (compoundList || Boolean(preview.groupLabels) || clause.groupBy.length > 1 && ["pie", "donut", "scatter", "box"].includes(kind)) };
}
