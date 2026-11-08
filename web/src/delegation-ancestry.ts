// Session ancestry for the explorer's tree and Timeline lanes. Parents come
// from the server's resolved parent IDs and explicit ancestor chains, never
// from display numbering. Unresolved ancestry (an ambiguous, missing or cyclic
// parent, or an identity conflict) is reported, never treated as a known root.
export type AncestryState = "root" | "resolved" | "ambiguous" | "missing" | "cycle" | "conflict";
export type SessionAncestry = {
  state: AncestryState; depth?: number;
  /** Weakest parent link behind depth: native, derived, legacy, synthesized or inferred. */
  certainty?: string; depth_quality?: string; root_quality?: string; conflicted?: boolean;
};
export type AncestryRow = {
  id: string;
  parent_id?: string;
  /** Resolved ancestors, nearest first, from the same snapshot as the row. */
  ancestor_ids?: string[];
  ancestry?: SessionAncestry;
  child_count?: number;
  start: number | null;
  end: number | null;
  subtree_start?: number | null;
  subtree_end?: number | null;
};
export type LaneCoverage = {
  /** Known ancestors of a shown lane that the page did not load. */
  omitted: string[];
  /** Shown lanes whose ancestry is ambiguous, missing or cyclic. */
  unresolved: string[];
};

/** Resolved ancestors, nearest first. The explicit chain wins; otherwise walk loaded parent IDs. */
export function sessionAncestors(row: AncestryRow, byID: ReadonlyMap<string, AncestryRow>): string[] {
  if (row.ancestor_ids) return row.ancestor_ids;
  const chain: string[] = [], seen = new Set([row.id]);
  for (let parent = row.parent_id; parent && !seen.has(parent); parent = byID.get(parent)?.parent_id) {
    seen.add(parent); chain.push(parent);
  }
  return chain;
}
export const unresolvedAncestry = (row: AncestryRow) => !!row.ancestry && !["root", "resolved"].includes(row.ancestry.state);
const overlaps = (start: number | null | undefined, end: number | null | undefined, range: readonly [number, number]) =>
  start != null && end != null && start <= range[1] && end >= range[0];

/**
 * Lanes active in the window, plus every ancestor of an active lane, in lane
 * order. An inactive parent stays when a loaded descendant is active, or when
 * children it did not load (folded or on another page) are active in its
 * subtree span.
 */
export function activeLaneRows<T extends AncestryRow>(rows: readonly AncestryRow[], laneRows: readonly T[], range: readonly [number, number]): T[] {
  const byID = new Map(rows.map(row => [row.id, row]));
  const loadedChildren = new Map<string, number>();
  for (const row of rows) if (row.parent_id && byID.has(row.parent_id)) loadedChildren.set(row.parent_id, (loadedChildren.get(row.parent_id) ?? 0) + 1);
  const kept = new Set<string>();
  for (const row of rows) {
    const unloaded = (row.child_count ?? 0) > (loadedChildren.get(row.id) ?? 0);
    if (!overlaps(row.start, row.end, range) && !(unloaded && overlaps(row.subtree_start, row.subtree_end, range))) continue;
    kept.add(row.id);
    for (const id of sessionAncestors(row, byID)) kept.add(id);
  }
  return laneRows.filter(row => kept.has(row.id));
}

/** Ancestors the shown lanes name but the page did not load, and lanes with unresolved ancestry. */
export function laneCoverage(rows: readonly AncestryRow[], shown: readonly AncestryRow[]): LaneCoverage {
  const byID = new Map(rows.map(row => [row.id, row])), shownIDs = new Set(shown.map(row => row.id));
  const omitted = new Set<string>(), unresolved: string[] = [];
  for (const row of shown) {
    if (unresolvedAncestry(row)) unresolved.push(row.id);
    for (const id of sessionAncestors(row, byID)) if (!shownIDs.has(id)) omitted.add(id);
  }
  return { omitted: [...omitted], unresolved };
}

/** A launch this execution issued that created no confirmed execution. */
export type LaunchAttempt = { id: string; outcome: "rejected" | "unresolved"; message_id?: string };

/** Short metadata for a session's launch attempts; they are never child rows or agent counts. */
export function launchAttemptNote(attempts: readonly LaunchAttempt[] | undefined): string {
  if (!attempts?.length) return "";
  const rejected = attempts.filter(a => a.outcome === "rejected").length, unresolved = attempts.length - rejected;
  const plural = (n: number) => `${n} launch${n === 1 ? "" : "es"}`;
  return [rejected && `${plural(rejected)} rejected`, unresolved && `${plural(unresolved)} without an outcome`].filter(Boolean).join(" · ");
}

/** One transcript of an execution (a continuation segment), where it is retained. */
export type ExecutionSegment = { conversation_id: string; workspace_id: string };

/** Where a transcript sits among its execution's segments, with its neighbours to navigate to. */
export function segmentPosition(segments: readonly ExecutionSegment[] | undefined, conversation: string): { index: number; total: number; previous?: ExecutionSegment; next?: ExecutionSegment } | null {
  const index = segments?.findIndex(segment => segment.conversation_id === conversation) ?? -1;
  if (!segments || segments.length < 2 || index < 0) return null;
  return { index, total: segments.length, previous: segments[index - 1], next: segments[index + 1] };
}
