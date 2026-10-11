import React from "react";
import { MetricCard, type MetricTheme } from "@pythia-software/query-table-ui";
import { chartSeriesStyle } from "./chart-colors";
import type { QueryPreview } from "./query-previews";

export function CompactMetric({ preview }: { preview: QueryPreview }) {
  const metric = preview.metrics?.find(item => item.clause.id === preview.coverMetricId);
  if (!metric || !preview.fields) return null;
  const { clause, result } = metric;
  const originalKind = clause.display?.kind ?? "auto";
  const compoundList = clause.groupBy.length > 1 && ["auto", "table", "list"].includes(originalKind);
  const kind = compoundList ? "list" : originalKind;
  const budget = kind === "scatter" || kind === "line" || kind === "bar-vertical" ? 24 : 4;
  const buckets = result.buckets.slice(0, budget).map(bucket => compoundList ? { ...bucket, keys: [bucket.keys.slice().reverse().map(key => String(key ?? "Unspecified")).join(" · ")] } : bucket);
  const theme: MetricTheme = {
    tokens: { accent: "var(--query-tone)", text: "var(--ink)", muted: "var(--muted)", grid: "var(--line)", surface: "transparent" },
    layers: [{ id: "subjects", colors: ["var(--query-tone)", "var(--sea)", "var(--gold)", "var(--heat-1)"] }],
    dimensions: Object.fromEntries(clause.groupBy.map((field, i) => [field, {
      layer: "subjects", overrides: ["repository_name", "model_family", "provider"].includes(field) ? buckets.map(bucket => ({ value: bucket.keys[i], color: chartSeriesStyle(field, String(bucket.keys[i] ?? "Unspecified")).color })) : [],
    }])),
  };
  // The bookmark is the sole interaction target. Native charts supply the same
  // marks/units as the full metric, without nested keyboard targets in its link.
  return <div className={`query-compact-metric${result.buckets.length > budget ? " has-limit" : ""}`} aria-hidden="true" {...{ inert: "" }}>
    <MetricCard clause={{ ...clause, groupBy: compoundList ? ["cover-group"] : clause.groupBy, layout: undefined, display: { ...clause.display, kind, legendPosition: "none" } }}
      result={{ ...result, buckets }} fields={preview.fields} theme={theme} />
    {result.buckets.length > budget ? <span className="query-compact-limit">{budget} of {result.buckets.length} groups in cover</span> : null}
  </div>;
}
