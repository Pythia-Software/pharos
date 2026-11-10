import type { WhereTerm } from "@pythia-software/query-table-core";
import type { AnalysisCatalog, AnalysisMeasure } from "./dashboard-analysis-model";
import { projectBreakdown, projectLegacy } from "./project-analysis";
import { outcomeLabels } from "./outcomes";
import { formatUSD } from "./currency";

// Outcomes over the outcomes dataset: one record is one Library workspace (its
// session), with its outcome label, the evidence tier that decided it, its
// priced usage and its Tier 1 facts (docs/projects-and-outcomes.md). A
// workspace counts at its start. Kept share and cost per kept change read only
// judged workspaces, and the rework rate only lines whose window has closed,
// so a recent change is never counted as not kept.

const count = { kind: "number", decimals: 0 } as const, usd = { kind: "currency", currency: "USD" } as const, percent = { kind: "percent", decimals: 1 } as const;
const repository: WhereTerm[] = [{ field: "project_kind", op: "=", value: "repository" }];

const measures: Record<string, AnalysisMeasure> = {
  sessions: { label: "Workspaces", unit: "workspaces", format: count, op: "count", additive: true, note: "Each Library workspace counts once, at its start." },
  cost: { label: "API cost (USD)", unit: "USD", format: usd, op: "sum", field: "cost_usd", value: "cost_usd", additive: true, note: "A session's whole API-equivalent cost counts at its start, priced as the Library prices it; unpriced usage adds nothing." },
  tokens: { label: "Tokens", unit: "tokens", format: count, op: "sum", field: "tokens", value: "tokens", additive: true, note: "A session's reconciled tokens count at its start." },
  files_changed: { label: "Files changed", unit: "files", format: count, op: "sum", field: "files_changed", value: "files_changed", additive: true, note: "Files each session edited, from its own tool calls." },
  lines_added: { label: "Lines added", unit: "lines", format: count, op: "sum", field: "lines_added", value: "lines_added", additive: true, note: "Lines the sessions' edits added, where the harness records line counts." },
  lines_removed: { label: "Lines removed", unit: "lines", format: count, op: "sum", field: "lines_removed", value: "lines_removed", additive: true, note: "Lines the sessions' edits removed, where the harness records line counts." },
  rework_rate: { label: "Rework rate", unit: "percent", format: percent, op: "sum", ratio: ["rework_removed_lines", "kept_lines"], additive: false,
    note: "Of the lines workspaces added and kept, the share later sessions removed within the rework window, pooled over every workspace's lines rather than averaged per workspace. Lines whose window is still open wait until it closes." },
  cost_per_kept_change: { label: "Cost per kept change (USD)", unit: "USD", format: usd, op: "sum", ratio: [{ op: "sum", field: "judged_cost_usd", label: "cost of judged workspaces" }, { op: "sum", field: "kept_priced", label: "priced kept changes" }], additive: false,
    note: "The cost of judged workspaces (Landed, Committed, Changed and kept, or Reworked) divided by those whose changes were kept: what a kept change cost, reworked attempts included. Changes too recent to judge, No changes and Unknown count in neither part, and nor does a workspace without a price." },
  kept_share: { label: "Kept share", unit: "percent", format: percent, op: "sum", ratio: ["kept_change", { op: "sum", field: "judged", label: "judged workspaces" }], additive: false,
    note: "Workspaces whose changes were kept, of those whose outcome is judged (Landed, Committed, Changed and kept, or Reworked). Changes too recent to judge, No changes and Unknown are left out, not counted as not kept." },
};

export const outcomeTierLabels: Record<string, string> = { local_git: "Local Git (Tier 2)", transcript: "Transcript (Tier 1)", none: "No evidence" };

const breakdowns: AnalysisCatalog["breakdowns"] = {
  outcome_label: { label: "Outcome", fields: ["outcome_label"], order: outcomeLabels.map(label => label.id), values: Object.fromEntries(outcomeLabels.map(label => [label.id, label.label])), empty: "Unknown" },
  evidence_tier: { label: "Evidence tier", fields: ["evidence_tier"], order: ["local_git", "transcript", "none"], values: outcomeTierLabels },
  project_name: projectBreakdown(),
  provider: { label: "Main provider", fields: ["provider"], order: ["claude", "antigravity", "codex", "chatgpt", "tl1", "canonical"], empty: "No usage" },
  model_family: { label: "Main model", fields: ["model_family"], empty: "No usage", note: "A session's main model is the one that took most of its cost." },
  source_kind: { label: "Source", fields: ["source_kind"] },
  // Git facts: branch is only for sessions in a repository, and local Git
  // only where it checked something (outcomeCatalogFor).
  branch: { label: "Branch", fields: ["branch"], where: repository, empty: "No branch", note: "Branch counts workspaces in a repository only: a folder has no branch." },
  git_check: { label: "Local Git (Tier 2)", fields: ["git_check"], order: ["checked", "not_checked", "not_applicable"], values: { checked: "Checked", not_checked: "Not checked", not_applicable: "Not applicable" } },
};

// Breakdowns that need a tier: shown only when some session reached it.
// There is no Tier 3 (forge) data in Pharos, so no Tier 3 breakdown.
export const outcomeTierBreakdowns: Record<string, "local_git"> = { git_check: "local_git" };

export const outcomesAnalysisCatalog: AnalysisCatalog = {
  dataset: "outcomes", time: { first: "started_at", last: "started_at" }, measures, breakdowns,
  records: { label: "Workspace", plural: "Workspaces", note: "One Library workspace, counted at its start." },
  units: {}, periods: {}, timestamp: "started_at",
  cycle: { hour: { kind: "timestamp", field: "started_at" }, weekday: { kind: "timestamp", field: "started_at" } },
  trend: { metric: "cost", split: "outcome_label" }, scatter: ["cost", "files_changed"], legacy: projectLegacy(),
};

// The catalog as drawn for a library: a breakdown whose tier no session
// reached is offered disabled with the reason, never drawn empty. Until the
// tiers are known (loading, or their read failed) every breakdown stands, so
// a link naming one draws it rather than the default and then redrawing.
export function outcomeCatalogFor(tiers: Iterable<string> | undefined): AnalysisCatalog {
  if (!tiers) return outcomesAnalysisCatalog;
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
// A third, optional sum by the same field (others) explains the group, such
// as its kept changes priced or not.
export type OutcomeRatioRow = { key: string; label: string; numerator: number | null; denominator: number; ratio: number | null; others: number };
type Bucket = { keys: unknown[]; value: unknown };
export function outcomeRatioRows(numerators: Bucket[], denominators: Bucket[], label: (key: string) => string, others: Bucket[] = [], limit = 8): OutcomeRatioRow[] {
  const rows = new Map<string, OutcomeRatioRow>();
  const row = (bucket: Bucket) => {
    const key = bucket.keys[0] == null ? "" : String(bucket.keys[0]);
    if (!rows.has(key)) rows.set(key, { key, label: label(key), numerator: null, denominator: 0, ratio: null, others: 0 });
    return rows.get(key)!;
  };
  for (const bucket of numerators) {
    const item = row(bucket);
    if (bucket.value != null) item.numerator = (item.numerator ?? 0) + (Number(bucket.value) || 0);
  }
  for (const bucket of denominators) row(bucket).denominator += Number(bucket.value) || 0;
  for (const bucket of others) row(bucket).others += Number(bucket.value) || 0;
  return [...rows.values()].map(item => ({ ...item, ratio: item.denominator > 0 && item.numerator != null ? item.numerator / item.denominator : null }))
    .sort((a, b) => b.denominator - a.denominator || (b.numerator ?? -1) - (a.numerator ?? -1) || a.label.localeCompare(b.label)).slice(0, limit);
}

// A total cost that summed nothing priced: No usage when nothing in it
// recorded usage, Unpriced when its usage has no price. Neither is $0.
export function outcomeCostText(cost: unknown, usage: number): string {
  if (cost != null) return formatUSD(Number(cost) || 0);
  return usage > 0 ? "Unpriced" : "No usage";
}

// A cost per kept change panel row: judged cost over priced kept changes,
// with its kept changes (others) and how many of them have no price. A group
// without a main model recorded no usage.
export function outcomeKeptCostCell(row: OutcomeRatioRow): [amount: string, detail: string] {
  const unpriced = row.key ? Math.max(0, row.others - row.denominator) : 0;
  const kept = row.others ? `${row.others.toLocaleString("en-US")} kept${unpriced ? ` (${unpriced.toLocaleString("en-US")} unpriced)` : ""}` : "no kept change";
  const amount = row.ratio != null ? formatUSD(row.ratio) : !row.key ? "No usage" : unpriced ? "Unpriced" : "—";
  const total = row.numerator != null ? `${formatUSD(row.numerator)} judged cost` : !row.key ? "no usage" : "no priced cost";
  return [amount, `${kept} · ${total}`];
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
