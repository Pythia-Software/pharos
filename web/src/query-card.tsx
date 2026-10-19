import React, { useRef } from "react";
import { CompactColumns } from "./column-marks";
import { compactMetricPresentation } from "./card-presentation";
import { boxCoverHeight } from "./compact-box-model";
import { CompactMetric } from "./compact-metric";
import { type QueryPreview } from "./curiosity-data";
import { Icon } from "./icons";
import { queryCategories, type QueryCategory } from "./query-categories";
import "./query-card.css";
import { CardLoading } from "./card-loading";
import type { CardLoadingSpec } from "./card-loading-model";
import { ChartTooltip } from "./chart-tooltip";
import { HumanStudyCover } from "./human-study-cover";
import { DraftCover } from "./draft-cover";
import "./draft-cover.css";

export function QueryChip({ title, description, active, onClick }: { title: string; description: string; active?: boolean; onClick: () => void }) {
  return <button type="button" className="query-chip" title={description} aria-pressed={active} onClick={onClick}><Icon name="compass" /><span>{title}</span><Icon name="arrow-right" /></button>;
}

export function QueryCard({ id, title, description, actionLabel, url, windowLabel, category, preview, loadingSpec, error, discovery, xray, lean = 0, onOpen, onReveal, onKeep, onEdit, onRemove, onMove, onReload, onDragStart, onDragEnd, onDrop, before }: {
  id: string; title: string; description: string; actionLabel?: string; url: string; windowLabel: string; category: QueryCategory;
  preview?: QueryPreview; error?: string; discovery?: boolean; xray?: boolean; lean?: number; before?: boolean;
  loadingSpec?: CardLoadingSpec;
  onOpen: () => void; onReveal?: () => void; onKeep?: () => void; onEdit?: () => void; onRemove?: () => void; onMove?: (direction: -1 | 1) => void; onReload?: () => void;
  onDragStart?: (event: React.DragEvent) => void; onDragEnd?: () => void; onDrop?: (event: React.DragEvent) => void;
}) {
  const card = useRef<HTMLElement>(null);
  const max = Math.max(1, ...preview?.ranking?.map(item => item.value) ?? []);
  const style = queryCategories[category];
  const presentation = preview && compactMetricPresentation(preview);
  const coverMetric = preview?.metrics?.find(metric => metric.clause.id === preview.coverMetricId)?.clause;
  const configuredRows = coverMetric?.display?.maxGroups;
  const denseList = Boolean(configuredRows && configuredRows >= 7 && presentation?.ranked && presentation.listOptions?.barPosition !== "below");
  const coverHeight = preview?.coverHeight ?? (!preview ? loadingSpec?.height : undefined) ?? (preview?.study ? preview.study.kind === "profile" ? 240 : preview.study.kind === "composition" ? 210 : preview.study.kind === "trend" ? 260 : 205 : presentation && ["pie", "donut"].includes(presentation.kind) ? 300 : presentation?.kind === "scatter" ? 240 : preview?.buckets && preview.series && coverMetric?.display?.legendPosition !== "none" && preview.series.length > 6
    ? 135 + Math.ceil(preview.series.length / 2) * 12
    : (configuredRows || coverMetric?.display?.showOther) && presentation && !preview?.buckets && ["box", "auto", "list", "table"].includes(presentation.kind)
    ? presentation.kind === "box" ? boxCoverHeight(presentation.shown, preview?.density === "compact") : Math.max(170, 41 + (presentation.shown + (presentation.ranked && coverMetric?.display?.showOther ? 1 : 0)) * (presentation.listOptions?.barPosition === "below" ? 8 + 12 * (presentation.listOptions.maxLabelLines ?? 2) : denseList ? 20 : 24)) : undefined);
  const ranked = presentation?.ranked || Boolean(!presentation && (preview?.insights || preview?.ranking || preview?.groupSummary));
  const summary = ranked || preview?.study ? undefined : preview && (presentation ? presentation.summary : preview.summary);
  const headlineMetric = preview && (presentation ? presentation.headlineMetric : ranked ? undefined : preview.headlineMetric);
  const coverLabel = preview?.study ? `${preview.study.value === undefined ? "" : preview.study.value.toLocaleString(undefined, { ...(preview.study.format === "USD" ? { style: "currency" as const, currency: "USD" } : {}), ...(preview.study.format === "percent" || preview.study.kind === "composition" ? { style: "percent" as const } : {}), maximumFractionDigits: 1 })} ${preview.study.valueLabel}. ${preview.study.sampleLabel}` : headlineMetric ? [headlineMetric.value, headlineMetric.unit, headlineMetric.label].filter(Boolean).join(" ") : summary ?? (presentation?.scalar ? [presentation.metric.clause.label, presentation.metric.result.buckets[0].value, presentation.groupLabel].filter(value => value !== undefined).join(": ") : presentation?.metric.clause.label ?? preview?.headline ?? title);
  const linkProps: React.AnchorHTMLAttributes<HTMLAnchorElement> = {
    href: url, "aria-hidden": xray || undefined, tabIndex: xray ? -1 : undefined, draggable: false,
    onClick: event => { if (!event.metaKey && !event.ctrlKey && !event.shiftKey && event.button === 0) { event.preventDefault(); if (!xray) onOpen(); } },
    onKeyDown: onMove ? event => { if (event.altKey && ["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(event.key)) { event.preventDefault(); onMove(event.key === "ArrowLeft" || event.key === "ArrowUp" ? -1 : 1); } } : undefined,
  };
  const cover = <ChartTooltip><span className={`query-cover${denseList ? " query-cover-dense-list" : ""}${presentation?.kind === "box" ? " query-cover-box" : ""}${headlineMetric ? " query-cover-headline" : ""}`} style={{ height: error || !preview ? loadingSpec?.height : coverHeight }} title={error} aria-busy={!preview && !error || undefined} aria-label={error ? "Preview unavailable" : preview ? `${coverLabel}${preview.note ? `. ${preview.note}` : ""}` : "Loading preview"}>
        <span className="query-cover-head"><Icon name="grip" /><span className="query-category">{style.label}</span><span className="query-window" title={windowLabel === "Saved" ? "The time range saved with this query" : "Rolling lookback; evaluated when the query runs"}>{windowLabel}</span></span>
        {error ? <span className="query-preview-unavailable" role="status">
          <svg className="query-unavailable-symbol" viewBox="0 0 56 56" aria-hidden="true" focusable="false">
            {["  ###  ", " #   # ", "#     #", "#     #", "#     #", " #   # ", "  ###  "].flatMap((row, y) => [...row].flatMap((cell, x) => cell === "#" ? [<rect key={`${x}:${y}`} x={x * 8 + 1} y={y * 8 + 1} width="6" height="6" rx="1" />] : []))}
            <path d="M41 1h6v6h-6z M41 9h6v6h-6z M33 17h6v6h-6z M25 25h6v6h-6z M17 33h6v6h-6z M9 41h6v6H9z M9 49h6v6H9z" />
          </svg>
          <span className="query-preview-status">Preview unavailable</span>
          {onReload && !xray ? <button type="button" className="query-preview-reload" aria-label={`Reload preview for ${title}`} onClick={event => {
            event.stopPropagation(); onReload();
            requestAnimationFrame(() => card.current?.querySelector<HTMLAnchorElement>(".query-card-open")?.focus({ preventScroll: true }));
          }}><Icon name="refresh" />Reload preview</button> : null}
        </span> : preview?.state === "ready" ? <>
          {headlineMetric ? <span className="query-headline-metric">
            <span className="query-result-summary is-headline query-stat-line"><strong className="query-headline-value">{headlineMetric.value}</strong>{headlineMetric.unit ? <> <span className="query-headline-unit">{headlineMetric.unit}</span></> : null}
            {headlineMetric.label ? <span className="query-headline-label">{headlineMetric.label}</span> : null}</span>
          </span> : null}
          {preview.draft ? <DraftCover cover={preview.draft} /> : preview.study ? <HumanStudyCover study={preview.study} /> : preview.buckets && preview.series ? <CompactColumns buckets={preview.buckets} series={preview.series} {...preview.timeline} showLegend={coverMetric?.display?.legendPosition ? coverMetric.display.legendPosition !== "none" : undefined} xLabel={coverMetric?.display?.xLabel ?? "Day"} yLabel={coverMetric?.display?.yLabel} unit={coverMetric?.field?.includes("cost") ? "USD" : coverMetric?.field?.includes("words") ? "words" : "tokens"} />
            : preview.metrics ? <CompactMetric preview={preview} />
            : preview.insights ? <span className="query-findings">
            <span className="query-finding-heading">{preview.headline}</span>
            {preview.insights.map((item, i) => <span className="query-finding" key={i} data-chart-tooltip={`${item.label}\n${item.value}${item.context ? `\n${item.context}` : ""}`} title="">
              <span className="query-finding-label">{item.label}</span><strong>{item.value}</strong>
              {item.context ? <small>{item.context}</small> : null}
            </span>)}
          </span>
            : preview.ranking ? <span className="query-ranking">{preview.ranking.map((item, i) => <span className="query-rank" key={i} data-chart-tooltip={`${item.label}\n${item.value.toLocaleString()}`} title=""><span className="query-rank-label">{item.label}</span><span className="query-rank-track"><i style={{ width: `${Math.max(2, item.value / max * 100)}%`, background: item.color }} /></span></span>)}</span>
            : <span className="query-excerpt">{preview.excerpt}</span>}
          {summary && !headlineMetric ? <span className="query-result-summary">{summary}</span> : null}
        </> : preview ? <span className="query-preview-status">{preview.emptyLabel ?? "No recent matches"}</span> : <CardLoading spec={loadingSpec} />}
      </span></ChartTooltip>;
  const caption = <span className="query-caption"><span className="query-title">{title}</span><span className="query-description">{description}</span>
        <span className="query-destination">{actionLabel ?? preview?.action ?? (preview?.buckets ? "Explore the timeline" : "Explore this view")}<Icon name="arrow-right" /></span>
      </span>;
  return <article ref={card} className={`query-card${discovery ? " discovery" : ""}${before ? " insert-before" : ""}`} data-card-id={id} data-category={category} data-xray={xray ? "" : undefined} data-revealable={xray && onReveal ? "" : undefined} aria-hidden={xray && !onReveal || undefined}
    style={{ "--query-tone": style.tone, "--query-lean": `${lean}deg` } as React.CSSProperties}
    draggable={!xray && Boolean(onDragStart)} onDragStart={onDragStart} onDragEnd={onDragEnd} onDragOver={onDrop ? event => event.preventDefault() : undefined} onDrop={onDrop}>
    {discovery && !xray ? <span className="query-drag-tip"><Icon name="arrow-up" /> Drag up to collect</span> : null}
    {error ? <div className="query-card-unavailable">{cover}<a className="query-card-open" {...linkProps}>{caption}</a></div>
      : <a className="query-card-open" {...linkProps}>{cover}{caption}</a>}
    {xray ? onReveal ? <button type="button" className="query-xray-open" aria-label={`Bring ${title} into view`} title="Bring into view" onClick={onReveal} /> : null
      : discovery ? onKeep ? <button className="query-keep" type="button" aria-label={`Collect ${title}`} title="Add to your collection" onClick={onKeep}><Icon name="plus" /></button> : null
      : <span className="query-card-tools">{onReload ? <button type="button" aria-label={`Refresh ${title}`} title="Refresh this card" onClick={onReload}><Icon name="refresh" /></button> : null}<button type="button" aria-label={`Edit ${title}`} title="Edit saved view" onClick={onEdit}><Icon name="edit" /></button><button type="button" aria-label={`Remove ${title}`} title="Remove from collection" onClick={onRemove}><Icon name="close" /></button></span>}
  </article>;
}
