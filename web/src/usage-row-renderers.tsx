import { ViewDisclosure } from "./view-disclosure";
import React from "react";
import { originRenderers } from "./origin-dimensions";
import { JsonValueView, type RenderRegistry } from "@pythia-software/query-table-ui";
import { conversationRowURL, primaryUsageSource, usageRowLinks, type UsageRowLink } from "./usage-row-model";
import type { UsageDataset } from "./usage-workspace-model";
import { usageDrilldownColumns, usageDrilldownField } from "./usage-query-columns";
import { navigateLink } from "./query-link-targets";

export function UsageSourceLink({ link, children }: { link: UsageRowLink; children?: React.ReactNode }) {
  return <a href={link.href} title={link.label} onClick={event => {
    event.stopPropagation();
    if (event.button || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || link.href.startsWith("/conversation/")) return;
    if (navigateLink(link.href)) event.preventDefault();
  }}>{children ?? link.label}</a>;
}
export function UsageRowActions({ dataset, row }: { dataset: UsageDataset; row: Record<string, any> }) {
  const links = usageRowLinks(dataset, row, new URLSearchParams(location.search));
  return links.length ? <span className="usage-row-links">{links.map(link => <UsageSourceLink key={link.label} link={link} />)}</span> : dataset === "mcp_calls" ? <span className="muted">No direct archive target in the recorded arguments.</span> : null;
}
export function usageRowRenderers(dataset: UsageDataset, shared = false): RenderRegistry<Record<string, any>> {
  return {
    ...originRenderers<Record<string, any>>(),
    ...Object.fromEntries(usageDrilldownColumns[dataset].map(column => [usageDrilldownField(column.kind), ({ row }: { row: Record<string, any> }) => {
      const link = !shared && usageRowLinks(dataset, row, new URLSearchParams(location.search)).find(link => link.kind === column.kind);
      return link ? <span className="usage-drilldown"><UsageSourceLink link={link}>{column.label}</UsageSourceLink></span> : <span className="muted">—</span>;
    }])),
    usage_conversation: ({ row, value }) => {
      const text = String(value || row.conversation_id || "Untitled conversation");
      const href = !shared && primaryUsageSource(dataset, row);
      return <span className="usage-row-title">{href ? <UsageSourceLink link={{ href, label: dataset === "parallelism" ? href.startsWith("/library?") ? "Works at this checkout" : "Conversation near this hour" : "Open source conversation" }}>{text}</UsageSourceLink> : text}</span>;
    },
    usage_code_snippet: ({ value, presentation }) => presentation === "property" ? <pre className="usage-row-code">{String(value ?? "")}</pre> : <span className="usage-row-snippet" title={String(value ?? "")}>{String(value ?? "")}</span>,
    usage_authorship_sources: ({ row }) => {
      const segments = (Array.isArray(row.segments) ? row.segments : []).filter((segment: any) => segment.source && conversationRowURL(segment.source, segment.source.message_id));
      return segments.length ? <ul className="usage-source-passages">{segments.map((segment: any, index: number) => <li key={index}><UsageSourceLink link={{ href: conversationRowURL(segment.source, segment.source.message_id)!, label: `Open ${segment.source.title || "matched source"}` }}>{segment.source.title || "Source passage"}</UsageSourceLink><span className="muted"> · {segment.category} · {String(segment.text || "").slice(0, 180)}</span></li>)}</ul> : <span className="muted">No matched source passage recorded.</span>;
    },
    usage_tool_urls: ({ value }) => Array.isArray(value) ? <ul className="usage-source-passages">{value.map((item, index) => {
      let href: string | undefined;
      try { const url = new URL(item.url); if (["https:", "http:"].includes(url.protocol)) href = url.href; } catch { /* Retain the source text without an unsafe link. */ }
      return <li key={index}>{href ? <a href={href} target="_blank" rel="noopener noreferrer" onClick={event => event.stopPropagation()}>{item.url}</a> : String(item.url ?? "")} <span className="muted">· {item.source}</span></li>;
    })}</ul> : "—",
    // Return the native element directly: ObjectProperties supplies its scoped
    // inspection state, change callback and attention registry through this API.
    usage_json: ({ value, field }) => <JsonValueView value={value} label={field.label} display={field.display?.kind === "json" ? field.display : { kind: "json", encoding: "auto" }} />,
    usage_payload: ({ value, row, field }) => {
      const text = String(value ?? ""), truncated = row[field.name === "input_text" ? "input_text_truncated" : "result_text_truncated"];
      if (!text) return <span className="muted">No retained text.</span>;
      if (text.length <= 600 && text.split("\n").length <= 8) return <pre className="usage-row-code">{text}</pre>;
      return <div><pre className="usage-payload-preview">{text.split("\n").slice(0, 4).join("\n").slice(0, 350)}…</pre><ViewDisclosure viewKey={`row:${dataset}:${row.id}:${field.name}`} className="usage-payload" summary={<>Read retained {field.name === "input_text" ? "input" : "result"} · {text.length.toLocaleString()} characters{truncated ? " · excerpt truncated" : ""}</>}><pre className="usage-row-code">{text}</pre></ViewDisclosure></div>;
    },
    usage_sources: ({ row }) => shared ? null : <UsageRowActions dataset={dataset} row={row} />,
    usage_timestamp: ({ value }) => {
      const date = new Date(value as string);
      return value && Number.isFinite(date.getTime()) ? <time dateTime={date.toISOString()} title={date.toISOString()}>{date.toLocaleString()} <span className="muted">({date.toISOString()})</span></time> : "—";
    },
  };
}
