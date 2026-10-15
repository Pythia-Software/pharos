import React, { useLayoutEffect, useRef } from "react";
import { MetricCard, formatMetricOutput, type MetricTheme } from "@pythia-software/query-table-ui";
import { compactMetricPresentation } from "./card-presentation";
import { chartSeriesStyle } from "./chart-colors";
import { formatPreviewValue, type QueryPreview } from "./query-previews";

export function CompactMetric({ preview }: { preview: QueryPreview }) {
  const host = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    if (!host.current) return;
    // Native marks expose precise hover labels. Remove their tab stops inside
    // the card link, and replace browser titles with the shared hover bubble.
    const presentation = compactMetricPresentation(preview);
    const prepare = () => {
      host.current?.querySelectorAll('[tabindex="0"]').forEach(mark => mark.setAttribute("tabindex", "-1"));
      host.current?.querySelectorAll(".qt-metric-bar-row").forEach((mark, index) => {
        // Native list rows retain input order. The optional combined-remainder
        // row uses the renderer's own label/value instead of guessing its sum.
        const bucket = presentation && index < presentation.budget ? presentation.metric.result.buckets[index] : undefined;
        if (!bucket || !presentation) return;
        const { clause } = presentation.metric;
        const label = mark.querySelector(".qt-metric-bar-label");
        const group = label?.getAttribute("title") ?? label?.getAttribute("data-chart-title") ?? label?.textContent;
        const value = bucket.error ?? (clause.display?.format ? formatMetricOutput(bucket.value, clause.display.format).text : typeof bucket.value === "number" ? bucket.value.toLocaleString(undefined, { maximumFractionDigits: 6 }) : String(bucket.value ?? "—"));
        mark.setAttribute("data-chart-tooltip", [group, value, clause.display?.valueLabel ?? clause.label].filter(Boolean).join("\n"));
      });
      host.current?.querySelectorAll(".qt-metric-legend li").forEach(mark => mark.setAttribute("data-chart-tooltip", mark.textContent ?? ""));
      if (presentation?.kind === "histogram") host.current?.querySelectorAll(".qt-metric-mark[aria-label]").forEach(mark => {
        const lines = mark.getAttribute("aria-label")!.split("\n");
        const count = lines.pop()?.split(" · ")[0];
        mark.setAttribute("data-chart-tooltip", [...lines, count].filter(Boolean).join("\n"));
      });
      host.current?.querySelectorAll("[title]").forEach(mark => {
        mark.setAttribute("data-chart-title", mark.getAttribute("title") ?? "");
        mark.removeAttribute("title");
      });
    };
    prepare();
    const observer = new MutationObserver(prepare);
    observer.observe(host.current, { childList: true, subtree: true });
    return () => observer.disconnect();
  }, [preview]);
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
  // Hover stays available while the card link remains the navigation target.
  return <div className={`query-compact-metric${scalar ? " is-scalar" : ""}${kind === "box" ? " is-box" : ""}${["pie", "donut"].includes(kind) ? " is-composition" : ""}`} aria-hidden="true" ref={host}>
    {scalar ? <span className="query-scalar-headline query-stat-line"><strong className="query-scalar-value">{scalarValue}</strong><span className="query-scalar-label">{clause.label ?? "Value"}</span></span> : <MetricCard compact clause={{ ...clause, groupBy: friendlyKeys ? ["Subject"] : clause.groupBy, layout: undefined, display: { ...clause.display, ...presentation.axisFormats, ...presentation.axisScales, kind, maxGroups: budget, yLabel: clause.display?.yLabel ?? valueField?.label ?? clause.display?.valueLabel ?? "Value", legendPosition: clause.display?.legendPosition ?? "none", list: presentation.listOptions } }}
      result={{ ...result, buckets }} fields={preview.fields.map(field => field.name === clause.field && field.type === "number" ? { ...field, render: "byte_size" } : field)} theme={theme}
      renderers={{ byte_size: ({ value }) => typeof value === "number" ? formatPreviewValue(value, valueUnit) : String(value ?? "") }} />}
    {scalar && groupLabel ? <span className="query-scalar-group" title={groupLabel}>{groupLabel}</span> : null}
  </div>;
}
