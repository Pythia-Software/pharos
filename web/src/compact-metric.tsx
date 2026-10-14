import React from "react";
import { MetricCard, formatMetricOutput, type MetricTheme } from "@pythia-software/query-table-ui";
import { compactMetricPresentation } from "./card-presentation";
import { chartSeriesStyle } from "./chart-colors";
import { formatPreviewValue, type QueryPreview } from "./query-previews";

export function CompactMetric({ preview }: { preview: QueryPreview }) {
  const presentation = compactMetricPresentation(preview);
  if (!presentation || !preview.fields) return null;
  const { metric, scalar, kind, budget, friendlyKeys, groupLabel } = presentation;
  const { clause, result } = metric;
  const buckets = result.buckets.map(bucket => friendlyKeys ? { ...bucket, keys: [preview.groupLabels?.[JSON.stringify(bucket.keys)] ?? bucket.keys.map(key => String(key ?? "Unspecified")).join(" · ")] } : bucket);
  const valueField = preview.fields.find(field => field.name === clause.field);
  const valueUnit = ["count", "count_distinct"].includes(clause.op) ? "" : clause.field?.includes("cost") || valueField?.render === "usd" ? "USD" : clause.field?.includes("bytes") ? "bytes" : clause.field?.includes("duration") ? "ms" : "";
  const scalarValue = scalar ? clause.display?.format ? formatMetricOutput(result.buckets[0].value, clause.display.format).text : formatPreviewValue(Number(result.buckets[0].value), valueUnit) : undefined;
  const theme: MetricTheme = {
    tokens: { accent: "var(--query-tone)", text: "var(--ink)", muted: "var(--muted)", grid: "var(--line)", surface: "transparent" },
    layers: [{ id: "subjects", colors: ["var(--query-tone)", "var(--sea)", "var(--gold)", "var(--heat-1)"] }],
    dimensions: Object.fromEntries(clause.groupBy.map((field, i) => [field, {
      layer: "subjects", overrides: ["repository_name", "model_family", "provider"].includes(field) ? buckets.map(bucket => ({ value: bucket.keys[i], color: chartSeriesStyle(field, String(bucket.keys[i] ?? "Unspecified")).color })) : [],
    }])),
  };
  // The bookmark is the sole interaction target. Native charts supply the same
  // marks/units as the full metric, without nested keyboard targets in its link.
  return <div className={`query-compact-metric${scalar ? " is-scalar" : ""}${kind === "box" ? " is-box" : ""}`} aria-hidden="true" {...{ inert: "" }}>
    {scalar ? <strong className="query-scalar-value">{scalarValue}</strong> : <MetricCard compact clause={{ ...clause, groupBy: friendlyKeys ? ["Subject"] : clause.groupBy, layout: undefined, display: { ...clause.display, ...presentation.axisFormats, ...presentation.axisScales, kind, maxGroups: budget, yLabel: clause.display?.yLabel ?? valueField?.label ?? clause.display?.valueLabel ?? "Value", legendPosition: clause.display?.legendPosition ?? "none", list: presentation.listOptions } }}
      result={{ ...result, buckets }} fields={preview.fields.map(field => field.name === clause.field && field.type === "number" ? { ...field, render: "byte_size" } : field)} theme={theme}
      renderers={{ byte_size: ({ value }) => typeof value === "number" ? formatPreviewValue(value, valueUnit) : String(value ?? "") }} />}
    {scalar ? <span className="query-scalar-label">{clause.label ?? "Value"}</span> : null}
    {scalar && groupLabel ? <span className="query-scalar-group" title={groupLabel}>{groupLabel}</span> : null}
  </div>;
}
