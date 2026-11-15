import type { AggregationBucket } from "@pythia-software/query-table-core";
import type { QueryPreview } from "./query-previews";

// Whether a card's cover says anything about this library. A library mostly
// imported from chat apps has no tool calls, cache tokens or subagents, so
// many catalog cards draw nothing, a single bar, or a row of zeros. The
// catalog and Home leave those out unless a person asks to see every card.
//
// It is judged only from the cover a card already loaded, for the window it
// shows: no extra queries. A card still loading, being rebuilt or failing is
// never judged, so it stays visible with its own state.
export type CardInterestLevel = "interesting" | "sparse" | "empty";
export type CardInterest = { level: CardInterestLevel; reason?: string };

// Fewer records than this behind a cover are not a pattern.
export const MIN_COVER_RECORDS = 3;
// A breakdown whose largest group holds at least this share of an additive
// total draws one bar: everything is the same value.
export const DOMINANT_GROUP_SHARE = 0.99;

const interesting: CardInterest = { level: "interesting" };
const timeGroups = new Set(["day", "week", "month", "hour", "hour_of_day", "weekday", "day_of_week"]);
const numeric = (value: unknown): value is number => typeof value === "number" && Number.isFinite(value);
// No spread: one value, one histogram bin, or no samples at all.
function flat(bucket: AggregationBucket): boolean {
  const distribution = bucket.distribution;
  if (!distribution) return true;
  if (distribution.kind === "histogram") return distribution.counts.filter(count => count > 0).length <= 1;
  if (!distribution.summary) return true;
  return distribution.kind === "box" ? distribution.summary.min === distribution.summary.max : new Set(distribution.summary.values).size <= 1;
}
const plural = (count: number, one: string, many = `${one}s`) => `${count.toLocaleString()} ${count === 1 ? one : many}`;

// A card with an exploration groups its rows first (instantiateRecipe), so
// its cover's counts are groups, not records. A card whose empty cover says
// how to turn its data on (emptyIsSetup) is the only place that says so, so
// an empty one is kept.
export type CardInterestOptions = { error?: string; grouped?: boolean; keepEmpty?: boolean };
export const cardInterestOptions = (recipe: { exploration?: unknown; emptyIsSetup?: boolean }): CardInterestOptions => ({ grouped: Boolean(recipe.exploration), keepEmpty: recipe.emptyIsSetup === true });

export function cardInterest(preview: QueryPreview | undefined, { error, grouped = false, keepEmpty = false }: CardInterestOptions = {}): CardInterest {
  if (!preview || error) return interesting;
  // A result held back by indexing or a rebuild is not the library's answer.
  if (preview.incompleteReason && !preview.incompleteWithoutMissing) return interesting;
  if (preview.state === "empty") return keepEmpty ? interesting : { level: "empty", reason: preview.emptyLabel ?? "No data in this window" };
  const cover = preview.metrics?.find(item => item.clause.id === preview.coverMetricId);
  if (!cover) return interesting;
  const { clause, result } = cover;
  if (result.error) return interesting;
  const buckets: AggregationBucket[] = [...result.buckets, ...(result.other ? [result.other] : [])].filter(bucket => !bucket.error);
  if (!buckets.length) return { level: "empty", reason: "No data in this window" };
  // Partial or limited results still hold at least what they returned.
  const records = buckets.reduce((sum, bucket) => sum + (numeric(bucket.count) ? bucket.count : 0), 0);
  if (!grouped && records > 0 && records < MIN_COVER_RECORDS) return { level: "sparse", reason: `Only ${plural(records, "record")} in this window` };
  // A breakdown by a category with a single group is one bar or one box:
  // a library without projects, efforts or a second machine. Time axes are
  // exempt, as one active day is still a timeline of this window.
  const breakdown = clause.groupBy.length > 0 && !clause.dateGrouping && clause.groupBy.some(field => !timeGroups.has(field));
  const groups = Math.max(result.buckets.filter(bucket => !bucket.error).length + (result.other ? 1 : 0), result.groupCount ?? 0) + (result.hiddenGroups ?? 0);
  if (breakdown && groups === 1 && result.coverage !== "partial") return { level: "sparse", reason: "Everything falls in one group in this window" };
  // A distribution in which every record has the same value has no shape.
  if (clause.distribution) return buckets.every(flat) ? { level: "sparse", reason: "Every record has the same value in this window" } : interesting;
  if (clause.expressionY) return interesting;
  const values = buckets.map(bucket => bucket.value).filter(numeric);
  if (values.length && values.every(value => value === 0)) return { level: "empty", reason: "Every value is zero in this window" };
  // An additive breakdown whose largest group holds nearly all of it.
  const additive = clause.op === "count" || clause.op === "sum";
  if (breakdown && additive && !clause.share && result.coverage !== "partial" && !(result.hiddenGroups ?? 0)) {
    const positive = values.filter(value => value > 0), total = positive.reduce((sum, value) => sum + value, 0);
    if (positive.length && Math.max(...positive) / total >= DOMINANT_GROUP_SHARE) return { level: "sparse", reason: positive.length === 1 ? "Everything falls in one group in this window" : "Nearly everything falls in one group in this window" };
  }
  return interesting;
}

export const showsCard = (preview: QueryPreview | undefined, options?: CardInterestOptions) => cardInterest(preview, options).level === "interesting";

// The Card Library's note about the cards it leaves out. It appears only when
// at least one card in view would be left out, and not when all of them are:
// the empty grid says so with its own Show all cards.
export function catalogSparseNote(sparse: number, shown: number, showAll: boolean): string | undefined {
  if (!sparse || !shown) return undefined;
  const subject = sparse === 1 ? "1 card has" : `${sparse.toLocaleString()} cards have`;
  return `${subject} little or no data in this window${showAll ? sparse === 1 ? " and is shown anyway" : " and are shown anyway" : ""}.`;
}
