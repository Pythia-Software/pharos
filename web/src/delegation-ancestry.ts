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
/** Only hide usage when retained bounds prove the session (or branch) is outside the window. */
export function sessionOutsideWindow(row: AncestryRow, range: readonly [number, number] | null, inclusive = false): boolean {
  if (!range) return false;
  // A branch with unknown bounds may contain activity outside the parent's span.
  if (inclusive && row.child_count && (row.subtree_start == null || row.subtree_end == null)) return false;
  const start = inclusive ? row.subtree_start ?? row.start : row.start;
  const end = inclusive ? row.subtree_end ?? row.end : row.end;
  return start != null && start > range[1] || end != null && end < range[0];
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
