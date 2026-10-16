import React, { useLayoutEffect, useRef } from "react";
import { MetricCard, formatMetricOutput, type MetricTheme } from "@pythia-software/query-table-ui";
import { CompactBoxPlot } from "./compact-box-plot";
import { CompactStackedBars } from "./compact-stacked-bars";
import { CompactPie } from "./compact-pie";
import { compactMetricPresentation } from "./card-presentation";
import { chartSeriesStyle, metricSeriesColor, otherSeriesColor } from "./chart-colors";
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
        mark.setAttribute("data-chart-tooltip", [group, value, clause.display?.valueLabel ?? clause.label, preview.groupEvidence?.[JSON.stringify(bucket.keys)]].filter(Boolean).join("\n"));
        mark.setAttribute("data-chart-colors", JSON.stringify([metricSeriesColor(clause.groupBy, bucket.keys)]));
      });
      host.current?.querySelectorAll(".qt-metric-legend li").forEach(mark => mark.setAttribute("data-chart-tooltip", mark.textContent ?? ""));
      if (presentation?.kind === "scatter") host.current?.querySelectorAll(".qt-metric-mark[aria-label]").forEach(mark => {
        const [group, pair, count] = mark.getAttribute("aria-label")!.split("\n"), coordinates = pair?.split(" / ");
        if (coordinates?.length === 2) mark.setAttribute("data-chart-tooltip", [group, `${presentation.metric.clause.display?.xLabel ?? "X"}: ${coordinates[0]}`, `${presentation.metric.clause.display?.yLabel ?? "Y"}: ${coordinates[1]}`, count].filter(Boolean).join("\n"));
      });
      if (presentation?.kind === "histogram") host.current?.querySelectorAll(".qt-metric-mark[aria-label]").forEach(mark => {
        const lines = mark.getAttribute("aria-label")!.split("\n");
        const count = lines.pop()?.split(" · ")[0];
        mark.setAttribute("data-chart-tooltip", [...lines, count].filter(Boolean).join("\n"));
      });
      host.current?.querySelectorAll(".qt-metric-mark[aria-label], .qt-metric-legend li").forEach(mark => {
        const paint = mark.querySelector('[fill]:not([fill="none"])')?.getAttribute("fill") ?? (mark.querySelector(".qt-metric-swatch") as HTMLElement | null)?.style.backgroundColor;
        if (paint) mark.setAttribute("data-chart-colors", JSON.stringify([paint]));
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
  const boxUnit = clause.field?.includes("cost") || valueField?.render === "usd" ? "USD" : clause.field?.includes("duration") || valueField?.render === "duration_ms" || clause.display?.format?.kind === "duration" && clause.display.format.sourceUnit === "milliseconds" ? "ms" : clause.field?.includes("bytes") ? "bytes" : clause.display?.xLabel ?? "";
  const valueUnit = ["count", "count_distinct"].includes(clause.op) ? "" : clause.field?.includes("cost") || valueField?.render === "usd" ? "USD" : clause.field?.includes("bytes") ? "bytes" : clause.field?.includes("duration") ? "ms" : "";
  const pieFormat = ["pie", "donut"].includes(kind) ? { kind: "number" as const, decimals: valueUnit === "USD" || buckets.some(bucket => typeof bucket.value === "number" && !Number.isInteger(bucket.value)) ? 2 : 0 } : undefined;
  const scalarValue = scalar ? clause.display?.format ? formatMetricOutput(result.buckets[0].value, clause.display.format).text : formatPreviewValue(Number(result.buckets[0].value), valueUnit) : undefined;
  const colorField = friendlyKeys ? "Subject" : clause.groupBy[0];
  const boxLabels = kind === "box" ? result.buckets.map(bucket => preview.groupLabels?.[JSON.stringify(bucket.keys)] ?? bucket.keys.map((key, i) => {
    const values = preview.fields!.find(field => field.name === clause.groupBy[i])?.filter?.values;
    const option = values?.source === "static" ? values.options.find(option => typeof option !== "string" && option.value === key) : undefined;
    return typeof option === "object" ? option.label : String(key ?? "Unspecified");
  }).join(" · ")) : [];
  const theme: MetricTheme = {
    tokens: { accent: "var(--query-tone)", text: "var(--ink)", muted: "var(--muted)", grid: "var(--line)", surface: "transparent", other: otherSeriesColor },
    layers: [{ id: "subjects", colors: ["var(--query-tone)", "var(--sea)", "var(--gold)", "var(--heat-1)"] }],
    dimensions: { ...Object.fromEntries(clause.groupBy.map((field, i) => [field, { layer: "subjects", overrides: result.buckets.map(bucket => ({ value: bucket.keys[i], color: chartSeriesStyle(field, String(bucket.keys[i] ?? "Unspecified")).color })) }])), ...(colorField ? { [colorField]: { layer: "subjects", overrides: buckets.map((bucket, i) => ({ value: bucket.keys[0], color: metricSeriesColor(clause.groupBy, result.buckets[i].keys) })) } } : {}) },
  };
  // Hover stays available while the card link remains the navigation target.
  return <div className={`query-compact-metric${scalar ? " is-scalar" : ""}${kind === "box" ? " is-box" : ""}${kind === "scatter" && (presentation.axisScales.xScale?.mode === "log" || presentation.axisScales.yScale?.mode === "log") ? " is-log-scatter" : ""}${["pie", "donut"].includes(kind) ? " is-composition" : ""}${preview.legendColumns === 2 ? " has-two-column-key" : ""}`} aria-hidden="true" ref={host}>
    {preview.stackBy && kind === "list" ? <CompactStackedBars clause={clause} result={result} splitBy={preview.stackBy} budget={budget} /> : scalar ? <span className="query-scalar-headline query-stat-line"><strong className="query-scalar-value">{scalarValue}</strong><span className="query-scalar-label">{clause.label ?? "Value"}</span></span> : ["pie", "donut"].includes(kind) ? <CompactPie clause={{ ...clause, display: { ...clause.display, kind } }} result={result} budget={budget} labels={preview.groupLabels} columns={preview.legendColumns} format={value => valueUnit === "USD" ? value.toLocaleString(undefined, { style: "currency", currency: "USD", maximumFractionDigits: 4 }) : value.toLocaleString(undefined, { maximumFractionDigits: 3 })} /> : kind === "box" ? <CompactBoxPlot compact={preview.density === "compact"} buckets={result.buckets} budget={budget} colors={result.buckets.map(bucket => metricSeriesColor(clause.groupBy, bucket.keys))} labels={boxLabels} scale={clause.display?.xScale?.mode} unit={boxUnit} axisLabel={clause.display?.xLabel} /> : <MetricCard compact clause={{ ...clause, groupBy: friendlyKeys ? ["Subject"] : clause.groupBy, layout: undefined, display: { ...clause.display, format: clause.display?.format ?? pieFormat, ...presentation.axisFormats, ...presentation.axisScales, kind, maxGroups: budget, yLabel: clause.display?.yLabel ?? valueField?.label ?? clause.display?.valueLabel ?? "Value", legendPosition: clause.display?.legendPosition ?? "none", list: presentation.listOptions } }}
      result={{ ...result, buckets }} fields={preview.fields.map(field => field.name === clause.field && field.type === "number" ? { ...field, render: "byte_size" } : field)} theme={theme}
      renderers={{ byte_size: ({ value }) => typeof value === "number" ? formatPreviewValue(value, valueUnit) : String(value ?? "") }} />}
    {scalar && groupLabel ? <span className="query-scalar-group" title={groupLabel}>{groupLabel}</span> : null}
  </div>;
}
