// How certain the explorer may present what the shared execution resolution
// decided. Parents, certainty and capability gaps come from the server; these
// helpers only choose the words and never upgrade an uncertain answer.
import { unresolvedAncestry, type AncestryRow } from "./delegation-ancestry";

/** Provenance strength of the winning parent claim, from the resolution. */
export type ParentCertainty = "native" | "derived" | "legacy";
export type ParentRow = AncestryRow & { parent_certainty?: ParentCertainty; parent_evidence?: string };

/**
 * Metadata for a session's parent. A parent the provider recorded, or a
 * verified acknowledgement, needs no note. An unverified stored link or an
 * inferred command launch is shown as such, never as confirmed ancestry.
 */
export function parentNote(row: ParentRow): string {
  if (unresolvedAncestry(row)) return "Parent unresolved";
  if (!row.parent_id) return "";
  if (row.parent_certainty === "legacy") return "Parent not verified";
  if (row.parent_evidence === "inferred-command-launch") return "Parent inferred from a command";
  return "";
}

/** What this snapshot could not establish, as the server counted it. */
export type ExecutionCoverage = {
  /** Evidence capabilities the library lacks (resolution coverage). */
  missing?: string[];
  unresolved_parents?: number;
  unverified_parents?: number;
  conflicts?: number;
  /** Sessions with activity in another workspace that this view did not read. */
  unqueried?: number;
};

const capabilityNotes: Record<string, string> = {
  "typed-execution-evidence": "Parts of this library were indexed before session provenance was recorded, so some parent links and copied history cannot be verified.",
  "cursor-copy-evidence": "Cursor forks indexed earlier may still include history copied from the conversation they forked.",
};

/** Short counts for the hierarchy, then fuller explanations for the definitions. */
export function coverageSummary(coverage: ExecutionCoverage | undefined): { counts: string; notes: string[] } {
  const plural = (n: number, one: string, many: string) => `${n.toLocaleString()} ${n === 1 ? one : many}`;
  const c = coverage ?? {};
  const counts = [
    c.unresolved_parents ? plural(c.unresolved_parents, "session has an unresolved parent", "sessions have unresolved parents") : "",
    c.unverified_parents ? plural(c.unverified_parents, "parent link is not verified", "parent links are not verified") : "",
    c.conflicts ? plural(c.conflicts, "session's identity is unresolved", "sessions' identities are unresolved") : "",
    c.unqueried ? plural(c.unqueried, "session has activity in another workspace that is not counted here", "sessions have activity in another workspace that is not counted here") : "",
  ].filter(Boolean).join(" · ");
  const notes = (c.missing ?? []).map(id => capabilityNotes[id] ?? `Some execution evidence is unavailable (${id}).`);
  return { counts, notes };
}

type Measured = { value: number | null; quality: string };

/**
 * A sum over several sessions' metric. Unknown members leave the sum partial
 * rather than counting as zero; no known member leaves it unknown.
 */
export function sumMetric(metrics: readonly Measured[]): { value: number | null; partial: boolean } {
  const known = metrics.filter(m => m.value !== null);
  if (!known.length) return { value: null, partial: false };
  const partial = known.length < metrics.length || known.some(m => m.quality === "partial" || m.quality === "unknown");
  return { value: known.reduce((sum, m) => sum + (m.value as number), 0), partial };
}

/**
 * The mark beside a metric value: "+" when coverage is partial, "?" when the
 * value rests on an unverified or inferred link (a depth through a stored
 * parent), "" when observed or reported.
 */
/**
 * A session count, approximate when some sessions' identity is in an
 * unresolved conflict: the resolver keeps each partition, so it is not exact.
 */
export function sessionCountText(total: number, uncertain = 0): string {
  const count = `${uncertain ? "≈" : ""}${total.toLocaleString()} session${total === 1 ? "" : "s"}`;
  return uncertain ? `${count} (${uncertain.toLocaleString()} with unresolved identity)` : count;
}

/** A row or lane whose agent events the server withheld or is rebuilding. */
export const eventsPendingNote = "agent events are being rebuilt for changed session evidence";

/**
 * The Timeline's own pending-events status: sessions whose agent events the
 * server withheld (the row's or lane's events_pending, or the server's
 * count, which includes rows a collapse or page leaves out), and how many of
 * them have no lane drawn, so no lane caption says so.
 */
export function timelineEventsPending(
  rows: readonly { id: string; events_pending?: boolean }[],
  lanes: ReadonlyMap<string, { events_pending?: boolean } | undefined>,
  shown: readonly { id: string }[],
  reported = 0,
): { sessions: number; notShown: number } {
  const pending = new Set(rows.filter(row => row.events_pending).map(row => row.id));
  for (const [id, lane] of lanes) if (lane?.events_pending) pending.add(id);
  const drawn = new Set(shown.map(row => row.id));
  const sessions = Math.max(pending.size, Number(reported) || 0);
  return { sessions, notShown: sessions - [...pending].filter(id => drawn.has(id)).length };
}

/** The sentence for timelineEventsPending, or "" when nothing is pending. */
export function timelineEventsPendingNote({ sessions, notShown }: { sessions: number; notShown: number }): string {
  if (!sessions) return "";
  const lead = `Agent events for ${sessions.toLocaleString()} session${sessions === 1 ? " are" : "s are"} left out until they are rebuilt`;
  return notShown ? `${lead} · ${notShown === sessions ? (sessions === 1 ? "it is" : "they are") : `${notShown.toLocaleString()} of them ${notShown === 1 ? "is" : "are"}`} not shown here` : lead;
}

export function qualityMark(quality: string): string {
  if (quality === "partial") return "+";
  if (quality === "unverified" || quality === "inferred") return "?";
  return "";
}

const occurrenceNotes: Record<string, string> = {
  "untyped-ancestry/v1": "indexed before session provenance was recorded",
  "retained-history-undeclared/v1": "its history declaration was not read again",
  "declared-copy-source-missing/v1": "the conversation it copied is not retained",
  "unresolved-observations": "some messages could not be told apart from copies",
};

/**
 * Why a session's own activity may still include copied history, in product
 * words, or "" when its coverage is complete. Unknown reasons stay visible.
 */
export function occurrenceNote(reason: string | undefined): string {
  if (!reason) return "";
  return `Copied history may be counted: ${occurrenceNotes[reason] ?? "copy evidence is incomplete"}`;
}

/** Segments of an execution a lane does not read, as a short note. */
export function omittedSegmentsNote(segments: readonly { workspace_id: string }[] | undefined): string {
  const n = segments?.length ?? 0;
  if (!n) return "";
  return `${n.toLocaleString()} segment${n === 1 ? "" : "s"} retained in other work ${n === 1 ? "is" : "are"} not shown here`;
}
