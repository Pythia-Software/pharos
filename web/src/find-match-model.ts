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

// Markdown syntax a snippet drops, as the card's first ask and latest
// response do: fences, heading/quote/list markers, emphasis, code ticks and
// link targets. Each pattern's groups are the characters removed.
const markdownSyntax = [
  /^[ \t]*((?:`{3,}|~{3,}).*)$/gm,
  /^[ \t]*(#{1,6}[ \t]+|>[ \t]?|[-*+][ \t]+|\d+[.)][ \t]+)/gm,
  /(\*\*|__|~~|`)/g,
  /(?<![\w*])(\*)(?=\S)[^*\n]*?\S(\*)(?![\w*])/g,
  /(\[)[^\]\n]*(\]\([^)\s]*\))/g,
];

// A snippet's highlighted parts as plain prose: markdown syntax removed and
// whitespace, including line breaks, collapsed to single spaces.
export function plainSnippetParts(text: string, spans: unknown): HighlightPart[] {
  const drop = new Uint8Array(text.length);
  for (const pattern of markdownSyntax) for (const match of text.matchAll(pattern)) {
    let at = match.index!;
    for (let group = 1; group < match.length; group++) {
      const value = match[group];
      if (value === undefined) continue;
      const start = text.indexOf(value, at);
      drop.fill(1, start, start + value.length);
      at = start + value.length;
    }
  }
  const parts: HighlightPart[] = [];
  let offset = 0, space = false;
  for (const part of highlightParts(text, spans)) {
    let kept = "";
    for (let index = 0; index < part.text.length; index++, offset++) {
      if (drop[offset]) continue;
      const char = part.text[index];
      if (/\s/.test(char)) { space = true; continue; }
      // A space between parts stays outside the marks.
      const last = parts.at(-1);
      if (space && (kept || last && !part.match)) kept += " ";
      else if (space && last) last.text += " ";
      space = false; kept += char;
    }
    if (kept) {
      const last = parts.at(-1);
      if (last?.match === part.match) last.text += kept; else parts.push({ text: kept, match: part.match });
    }
  }
  return parts.length ? parts : [{ text: "", match: false }];
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
