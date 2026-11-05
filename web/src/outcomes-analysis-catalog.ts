import type { WhereTerm } from "@pythia-software/query-table-core";
import type { AnalysisCatalog, AnalysisMeasure } from "./dashboard-analysis-model";
import { projectBreakdown, projectLegacy } from "./project-analysis";
import { outcomeLabels } from "./outcomes";

// Outcomes over the outcomes dataset: one record is one session, with its
// outcome label, the evidence tier that decided it, its priced usage and its
// Tier 1 facts (docs/projects-and-outcomes.md). A session counts at its start.

const count = { kind: "number", decimals: 0 } as const, usd = { kind: "number", decimals: 2 } as const, percent = { kind: "percent", decimals: 1 } as const;
const repository: WhereTerm[] = [{ field: "project_kind", op: "=", value: "repository" }];

const measures: Record<string, AnalysisMeasure> = {
  sessions: { label: "Sessions", unit: "sessions", format: count, op: "count", additive: true, note: "Each session counts once, at its start." },
  cost: { label: "API cost (USD)", unit: "USD", format: usd, op: "sum", field: "cost_usd", value: "cost_usd", additive: true, note: "A session's whole API-equivalent cost counts at its start, priced as the Library prices it; unpriced usage adds nothing." },
  tokens: { label: "Tokens", unit: "tokens", format: count, op: "sum", field: "tokens", value: "tokens", additive: true, note: "A session's reconciled tokens count at its start." },
  files_changed: { label: "Files changed", unit: "files", format: count, op: "sum", field: "files_changed", value: "files_changed", additive: true, note: "Files each session edited, from its own tool calls." },
  lines_added: { label: "Lines added", unit: "lines", format: count, op: "sum", field: "lines_added", value: "lines_added", additive: true, note: "Lines the sessions' edits added, where the harness records line counts." },
  lines_removed: { label: "Lines removed", unit: "lines", format: count, op: "sum", field: "lines_removed", value: "lines_removed", additive: true, note: "Lines the sessions' edits removed, where the harness records line counts." },
  rework_rate: { label: "Rework rate", unit: "percent", format: percent, op: "sum", ratio: ["rework_removed_lines", "kept_lines"], additive: false,
    note: "Of the lines sessions added and kept, the share later sessions removed within the rework window, pooled over every session's lines rather than averaged per session." },
  cost_per_kept_change: { label: "Cost per kept change (USD)", unit: "USD", format: usd, op: "sum", ratio: ["cost_usd", "kept_change"], additive: false,
    note: "All the sessions' cost divided by the sessions whose changes were kept (Landed, Committed, or Changed and kept): what a kept change cost, counting the sessions that changed nothing or were reworked." },
  kept_share: { label: "Kept share of sessions", unit: "percent", format: percent, op: "sum", ratio: ["kept_change", { op: "count", field: "id", label: "sessions" }], additive: false,
    note: "Sessions whose changes were kept, of all sessions." },
};

export const outcomeTierLabels: Record<string, string> = { local_git: "Local Git (Tier 2)", transcript: "Transcript (Tier 1)", none: "No evidence" };

const breakdowns: AnalysisCatalog["breakdowns"] = {
  outcome_label: { label: "Outcome", fields: ["outcome_label"], order: outcomeLabels.map(label => label.id), values: Object.fromEntries(outcomeLabels.map(label => [label.id, label.label])), empty: "Unknown" },
  evidence_tier: { label: "Evidence tier", fields: ["evidence_tier"], order: ["local_git", "transcript", "none"], values: outcomeTierLabels },
  project_name: projectBreakdown(),
  provider: { label: "Main provider", fields: ["provider"], order: ["claude", "antigravity", "codex", "chatgpt", "tl1", "canonical"], empty: "No usage" },
  model_family: { label: "Main model", fields: ["model_family"], empty: "No usage", note: "A session's main model is the one that took most of its cost." },
  source_kind: { label: "Harness", fields: ["source_kind"] },
  // Git facts: branch is only for sessions in a repository, and local Git
  // only where it checked something (outcomeCatalogFor).
  branch: { label: "Branch", fields: ["branch"], where: repository, empty: "No branch", note: "Branch counts sessions in a repository only: a folder has no branch." },
  git_check: { label: "Local Git (Tier 2)", fields: ["git_check"], order: ["checked", "not_checked", "not_applicable"], values: { checked: "Checked", not_checked: "Not checked", not_applicable: "Not applicable" } },
};

// Breakdowns that need a tier: shown only when some session reached it.
// There is no Tier 3 (forge) data in Pharos, so no Tier 3 breakdown.
export const outcomeTierBreakdowns: Record<string, "local_git"> = { git_check: "local_git" };

export const outcomesAnalysisCatalog: AnalysisCatalog = {
  dataset: "outcomes", time: { first: "started_at", last: "started_at" }, measures, breakdowns,
  records: { label: "Session", plural: "Sessions", note: "One session, counted at its start." },
  units: {}, periods: {}, timestamp: "started_at",
  cycle: { hour: { kind: "timestamp", field: "started_at" }, weekday: { kind: "timestamp", field: "started_at" } },
  trend: { metric: "cost", split: "outcome_label" }, scatter: ["cost", "files_changed"], legacy: projectLegacy(),
};

// The catalog as drawn for a library: a breakdown whose tier no session
// reached is offered disabled with the reason, never drawn empty.
export function outcomeCatalogFor(tiers: Iterable<string>): AnalysisCatalog {
  const reached = new Set(tiers);
  const missing = Object.entries(outcomeTierBreakdowns).filter(([, tier]) => !reached.has(tier));
  if (!missing.length) return outcomesAnalysisCatalog;
  return { ...outcomesAnalysisCatalog, breakdowns: { ...outcomesAnalysisCatalog.breakdowns, ...Object.fromEntries(missing.map(([id]) => [id,
    { ...outcomesAnalysisCatalog.breakdowns[id], unavailable: "No session has local Git evidence: local Git checks only repositories on this Mac" }])) } };
}

// The Outcomes dashboard's panels: a pooled ratio per group, from two sums
// grouped by the same field, largest denominator first; a group with no
// denominator has no ratio (null), never zero, and nor does a group whose
// numerator is unknown (a sum of nothing priced), which is not zero either.
export type OutcomeRatioRow = { key: string; label: string; numerator: number | null; denominator: number; ratio: number | null };
type Bucket = { keys: unknown[]; value: unknown };
export function outcomeRatioRows(numerators: Bucket[], denominators: Bucket[], label: (key: string) => string, limit = 8): OutcomeRatioRow[] {
  const rows = new Map<string, OutcomeRatioRow>();
  const row = (bucket: Bucket) => {
    const key = bucket.keys[0] == null ? "" : String(bucket.keys[0]);
    if (!rows.has(key)) rows.set(key, { key, label: label(key), numerator: null, denominator: 0, ratio: null });
    return rows.get(key)!;
  };
  for (const bucket of numerators) {
    const item = row(bucket);
    if (bucket.value != null) item.numerator = (item.numerator ?? 0) + (Number(bucket.value) || 0);
  }
  for (const bucket of denominators) row(bucket).denominator += Number(bucket.value) || 0;
  return [...rows.values()].map(item => ({ ...item, ratio: item.denominator > 0 && item.numerator != null ? item.numerator / item.denominator : null }))
    .sort((a, b) => b.denominator - a.denominator || (b.numerator ?? -1) - (a.numerator ?? -1) || a.label.localeCompare(b.label)).slice(0, limit);
}

// Coverage: sessions at each evidence tier, every tier listed, highest first.
export function outcomeTierCoverage(buckets: Bucket[]): { tier: string; label: string; sessions: number; share: number }[] {
  const counts = new Map(buckets.map(bucket => [String(bucket.keys[0] ?? ""), Number(bucket.value) || 0]));
  const total = [...counts.values()].reduce((sum, value) => sum + value, 0);
  return Object.keys(outcomeTierLabels).map(tier => ({ tier, label: outcomeTierLabels[tier]!, sessions: counts.get(tier) ?? 0, share: total ? (counts.get(tier) ?? 0) / total : 0 }));
}

// The tiers some session reached, for outcomeCatalogFor.
export const outcomeTiersReached = (buckets: Bucket[]) => buckets.filter(bucket => Number(bucket.value) > 0).map(bucket => String(bucket.keys[0]));

// A breakdown's label for a group key, its empty label for a missing one.
export function outcomeGroupLabel(breakdown: string, key: string): string {
  const item = breakdowns[breakdown];
  if (!key) return item?.empty ?? "None";
  return item?.values?.[key] ?? key;
}
