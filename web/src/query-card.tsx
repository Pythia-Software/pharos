import React from "react";
import { CompactColumns } from "./column-marks";
import { type QueryPreview } from "./curiosity-data";
import { Icon } from "./icons";
import { queryCategories, type QueryCategory } from "./query-categories";
import "./query-card.css";

export function QueryChip({ title, description, onClick }: { title: string; description: string; onClick: () => void }) {
  return <button type="button" className="query-chip" title={description} onClick={onClick}><Icon name="compass" /><span>{title}</span><Icon name="arrow-right" /></button>;
}

export function QueryCard({ id, title, description, url, windowLabel, category, preview, error, discovery, xray, lean = 0, onOpen, onReveal, onKeep, onEdit, onRemove, onMove, onDragStart, onDragEnd, onDrop, before }: {
  id: string; title: string; description: string; url: string; windowLabel: string; category: QueryCategory;
  preview?: QueryPreview; error?: string; discovery?: boolean; xray?: boolean; lean?: number; before?: boolean;
  onOpen: () => void; onReveal?: () => void; onKeep?: () => void; onEdit?: () => void; onRemove?: () => void; onMove?: (direction: -1 | 1) => void;
  onDragStart?: (event: React.DragEvent) => void; onDragEnd?: () => void; onDrop?: (event: React.DragEvent) => void;
}) {
  const max = Math.max(1, ...preview?.ranking?.map(item => item.value) ?? []);
  const style = queryCategories[category];
  return <article className={`query-card${discovery ? " discovery" : ""}${before ? " insert-before" : ""}`} data-card-id={id} data-category={category} data-xray={xray ? "" : undefined} data-revealable={xray && onReveal ? "" : undefined} aria-hidden={xray && !onReveal || undefined}
    style={{ "--query-tone": style.tone, "--query-lean": `${lean}deg` } as React.CSSProperties}
    draggable={!xray} onDragStart={onDragStart} onDragEnd={onDragEnd} onDragOver={onDrop ? event => event.preventDefault() : undefined} onDrop={onDrop}>
    {discovery && !xray ? <span className="query-drag-tip"><Icon name="arrow-up" /> Drag up to collect</span> : null}
    <a className="query-card-open" href={url} aria-hidden={xray || undefined} tabIndex={xray ? -1 : undefined} draggable={false} onClick={event => { if (!event.metaKey && !event.ctrlKey && !event.shiftKey && event.button === 0) { event.preventDefault(); if (!xray) onOpen(); } }}
      onKeyDown={onMove ? event => { if (event.altKey && ["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(event.key)) { event.preventDefault(); onMove(event.key === "ArrowLeft" || event.key === "ArrowUp" ? -1 : 1); } } : undefined}>
      <span className="query-cover" title={preview?.note ?? error} aria-label={preview ? `${preview.summary}${preview.note ? `. ${preview.note}` : ""}` : error || "Loading preview"}>
        <span className="query-cover-head"><Icon name="grip" /><span className="query-category">{style.label}</span><span className="query-window" title={windowLabel === "Saved" ? "The time range saved with this query" : "Local calendar days, including today"}>{windowLabel}</span></span>
        {preview?.state === "ready" ? <>
          {preview.buckets && preview.series ? <CompactColumns buckets={preview.buckets} series={preview.series} />
            : preview.ranking ? <span className="query-ranking">{preview.ranking.map((item, i) => <span className="query-rank" key={i} title={`${item.label}: ${item.value.toLocaleString()}`}><span className="query-rank-label">{item.label}</span><span className="query-rank-track"><i style={{ width: `${Math.max(2, item.value / max * 100)}%`, background: item.color }} /></span></span>)}</span>
            : <span className="query-excerpt">{preview.excerpt}</span>}
          <span className="query-result-summary">{preview.summary}</span>
        </> : <span className="query-preview-status">{error ? "Preview unavailable" : preview ? "No recent matches" : "Finding the shape…"}</span>}
      </span>
      <span className="query-caption"><span className="query-title">{title}</span><span className="query-description">{description}</span></span>
    </a>
    {xray ? onReveal ? <button type="button" className="query-xray-open" aria-label={`Bring ${title} into view`} title="Bring into view" onClick={onReveal} /> : null
      : discovery ? <button className="query-keep" type="button" aria-label={`Collect ${title}`} title="Add to your collection" onClick={onKeep}><Icon name="plus" /></button>
      : <span className="query-card-tools"><button type="button" aria-label={`Edit ${title}`} title="Edit title and description" onClick={onEdit}><Icon name="edit" /></button><button type="button" aria-label={`Remove ${title}`} title="Remove from collection" onClick={onRemove}><Icon name="close" /></button></span>}
  </article>;
}
