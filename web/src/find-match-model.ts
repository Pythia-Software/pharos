import { findFromAttention, type FindState } from "./page-find-model";

export type HighlightPart = { text: string; match: boolean };

// Splits a keyword match's snippet at the service's highlight spans (UTF-16
// offsets, sorted). Out-of-range or overlapping spans are clamped, so stale or
// malformed evidence still renders the whole snippet.
export function highlightParts(text: string, spans: unknown): HighlightPart[] {
  const parts: HighlightPart[] = [];
  let at = 0;
  for (const span of Array.isArray(spans) ? spans : []) {
    if (!Array.isArray(span)) continue;
    const start = Math.max(at, Math.min(text.length, Number(span[0]) || 0)), end = Math.max(start, Math.min(text.length, Number(span[1]) || 0));
    if (end === start) continue;
    if (start > at) parts.push({ text: text.slice(at, start), match: false });
    parts.push({ text: text.slice(start, end), match: true });
    at = end;
  }
  if (at < text.length || !parts.length) parts.push({ text: text.slice(at), match: false });
  return parts;
}

// /api/work/{id}/matches: a work's newest matching messages, in order with
// its first ask and latest response, and how many messages lie between.
export type WorkMatchItem = { kind: "first" | "match" | "last"; message_id: string; conversation_id?: string; role?: string; message_kind?: string; snippet?: string; highlights?: [number, number][]; preview?: "first" | "last" };
export type WorkMatches = { items: WorkMatchItem[]; between: number[]; complete: boolean };
export type SummaryEntry = { type: "message"; item: WorkMatchItem; label: string } | { type: "gap"; count: number; label: string };

export const betweenLabel = (count: number) => count === 0 ? "Directly after" : `${count.toLocaleString()} message${count === 1 ? "" : "s"} between`;

// The card summary: each message, labelled, with a gap entry between
// neighbours. The first match names how many matched in all.
export function summaryEntries(matches: WorkMatches, total: number): SummaryEntry[] {
  const shown = matches.items.filter(item => item.kind === "match").length;
  const tally = total > shown ? ` · ${shown} of ${total.toLocaleString()} matching messages` : shown > 1 ? ` · ${shown} matching messages` : "";
  const entries: SummaryEntry[] = [];
  let first = true;
  matches.items.forEach((item, index) => {
    if (index) { const count = matches.between[index - 1] ?? 0; entries.push({ type: "gap", count, label: betweenLabel(count) }); }
    const prefix = item.preview === "first" || item.kind === "first" ? "First ask" : item.preview === "last" || item.kind === "last" ? "Latest response" : "";
    let label = prefix;
    if (item.kind === "match") { label = `${prefix ? `${prefix} · ` : ""}Match${first ? tally : ""}`; first = false; }
    entries.push({ type: "message", item, label });
  });
  return entries;
}

// Opening a match hands its marked terms to page Find on the work page, the
// way `text:` attention targets do, so the reader shows the same matches.
export function matchFindState(text: string, spans: unknown): FindState | null {
  const seen = new Set<string>(), targets: string[] = [];
  for (const part of highlightParts(text, spans)) {
    const key = part.text.toLocaleLowerCase();
    if (part.match && part.text.trim() && !seen.has(key)) { seen.add(key); targets.push(`text:${part.text}`); }
  }
  return findFromAttention(targets, "");
}
