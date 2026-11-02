// DOM-free rules for page Find (page-find.ts): the term's pattern, matching,
// counting unrendered text, and the URL forms that open Find.

export type FindState = { text: string; caseSensitive: boolean; regex: boolean };
export const FIND_PARAMETERS = ["find", "find_case", "find_regex"] as const;
/** Typing a shorter term waits for Enter, so slow pages are not searched per keystroke. */
export const FIND_MINIMUM = 3;
export const FIND_LIMIT = 1000;

const escape = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

export type FindPattern =
  | { status: "empty" | "short" }
  | { status: "invalid"; error: string }
  | { status: "ready"; pattern: RegExp };

/** `typing` marks a term still being typed; a restored or Entered term searches at any length. */
export function findPattern(state: FindState, typing = false): FindPattern {
  if (!state.text) return { status: "empty" };
  if (typing && [...state.text].length < FIND_MINIMUM) return { status: "short" };
  try {
    // RegExp offsets index the original text. Lowercasing it first can
    // change its length (for example İ) and corrupt Range offsets.
    const pattern = new RegExp(state.regex ? state.text : escape(state.text), `g${state.caseSensitive ? "" : "i"}u`);
    return { status: "ready", pattern };
  } catch (cause) {
    return { status: "invalid", error: cause instanceof Error ? cause.message : String(cause) };
  }
}

/** Nonempty matches as [start, end) offsets, at most `limit`. */
export function findMatches(content: string, pattern: RegExp, limit = FIND_LIMIT): [number, number][] {
  const found: [number, number][] = [];
  if (limit <= 0) return found;
  for (const match of content.matchAll(pattern)) {
    if (!match[0].length) continue;
    found.push([match.index!, match.index! + match[0].length]);
    if (found.length >= limit) break;
  }
  return found;
}

/** Matches across texts that are not on the page yet, at most `limit`. */
export function countMatches(texts: Iterable<string>, pattern: RegExp, limit = FIND_LIMIT): number {
  let count = 0;
  for (const text of texts) {
    if (count >= limit) break;
    count += findMatches(text, pattern, limit - count).length;
  }
  return count;
}

/** A test for one text, for owners that decide what to render (no g-flag state). */
export function findTest(pattern: RegExp): (text: string) => boolean {
  const single = new RegExp(pattern.source, pattern.flags.replace("g", ""));
  return text => single.test(text);
}

/**
 * Attention `text:` targets are an alias for Find. One literal opens it as
 * typed; several become an escaped OR regex. Returns null without targets.
 */
export function findFromAttention(highlights: readonly string[], reveal: string): FindState | null {
  const texts = [...new Set([reveal, ...highlights].filter(target => target.startsWith("text:") && target.length > 5).map(target => target.slice(5)))];
  if (!texts.length) return null;
  return texts.length === 1
    ? { text: texts[0], caseSensitive: false, regex: false }
    : { text: texts.map(escape).join("|"), caseSensitive: false, regex: true };
}

/** Writes Find to a URL; false flags stay implicit and null closes Find. */
export function writeFindParameters(params: URLSearchParams, state: FindState | null): void {
  for (const name of FIND_PARAMETERS) params.delete(name);
  if (!state) return;
  params.set("find", state.text);
  if (state.caseSensitive) params.set("find_case", "1");
  if (state.regex) params.set("find_regex", "1");
}

/** The next match index, wrapping; from no selection, forward starts at `from`. */
export function stepIndex(current: number, delta: number, total: number, from = 0): number {
  if (!total) return -1;
  if (current < 0) return delta > 0 ? Math.min(Math.max(from, 0), total - 1) : (Math.min(Math.max(from, 0), total) - 1 + total) % total;
  return ((current + delta) % total + total) % total;
}
