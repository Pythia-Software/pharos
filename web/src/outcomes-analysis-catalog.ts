import type { WhereTerm } from "@pythia-software/query-table-core";
import type { AnalysisCatalog, AnalysisMeasure } from "./dashboard-analysis-model";
import { projectBreakdown, projectLegacy } from "./project-analysis";
import { outcomeLabels } from "./outcomes";
import { formatUSD } from "./currency";
import { tokenMetricFormat } from "./token-count";

// Outcomes over the outcomes dataset: one record is one Library workspace (its
// session), with its outcome label, the evidence tier that decided it, its
// priced usage and its Tier 1 facts (docs/projects-and-outcomes.md). A
// workspace counts at its start. Kept share and cost per kept change read only
// judged workspaces, and the rework rate only lines whose window has closed,
// so a recent change is never counted as not kept.
//
// Review measures and breakdowns (Tier 3) read what a forge said of the pull
// requests a workspace opened or committed to (docs/pull-request-reviews.md).
// They cover only workspaces with a pull request read; the rest are "No pull
// request read", never "not reviewed". They never change an outcome label.

const perPullRequest = { kind: "number", decimals: 1 } as const;
const count = { kind: "number", decimals: 0 } as const, usd = { kind: "currency", currency: "USD" } as const, percent = { kind: "percent", decimals: 1 } as const;
const repository: WhereTerm[] = [{ field: "project_kind", op: "=", value: "repository" }];

const measures: Record<string, AnalysisMeasure> = {
  sessions: { label: "Workspaces", unit: "workspaces", format: count, op: "count", additive: true, note: "Each workspace counts once, at its start." },
  cost: { label: "API cost (USD)", unit: "USD", format: usd, op: "sum", field: "cost_usd", value: "cost_usd", additive: true, note: "A workspace's whole API cost counts at its start, priced as the library prices it; unpriced usage adds nothing." },
  tokens: { label: "Tokens", unit: "tokens", format: tokenMetricFormat, op: "sum", field: "tokens", value: "tokens", additive: true, note: "A workspace's reconciled tokens count at its start." },
  files_changed: { label: "Files changed", unit: "files", format: count, op: "sum", field: "files_changed", value: "files_changed", additive: true, note: "Files each workspace edited, from its own tool calls." },
  lines_added: { label: "Lines added", unit: "lines", format: count, op: "sum", field: "lines_added", value: "lines_added", additive: true, note: "Lines the workspaces' edits added, where the agent app records line counts." },
  lines_removed: { label: "Lines removed", unit: "lines", format: count, op: "sum", field: "lines_removed", value: "lines_removed", additive: true, note: "Lines the workspaces' edits removed, where the agent app records line counts." },
  rework_rate: { label: "Rework rate", unit: "percent", format: percent, op: "sum", ratio: ["rework_removed_lines", "kept_lines"], additive: false,
    note: "Of the lines workspaces added and kept, the share later workspaces removed within the rework window, pooled over every workspace's lines rather than averaged per workspace. Lines whose window is still open wait until it closes." },
  cost_per_kept_change: { label: "Cost per kept change (USD)", unit: "USD", format: usd, op: "sum", ratio: [{ op: "sum", field: "judged_cost_usd", label: "cost of judged workspaces" }, { op: "sum", field: "kept_priced", label: "priced kept changes" }], additive: false,
    note: "The cost of judged workspaces (Landed, Committed, Changed and kept, or Reworked) divided by those whose changes were kept: what a kept change cost, reworked attempts included. Changes too recent to judge, No changes and Unknown count in neither part, and nor does a workspace without a price." },
  kept_share: { label: "Kept share", unit: "percent", format: percent, op: "sum", ratio: ["kept_change", { op: "sum", field: "judged", label: "judged workspaces" }], additive: false,
    note: "Workspaces whose changes were kept, of those whose outcome is judged (Landed, Committed, Changed and kept, or Reworked). Changes too recent to judge, No changes and Unknown are left out, not counted as not kept." },
  pull_requests: { label: "Pull requests read", unit: "pull requests", format: count, op: "sum", field: "pull_requests", value: "pull_requests", additive: true,
    note: "Pull requests the workspaces opened or committed to that were read from their forge. A workspace's pull requests count at its start." },
  review_comments: { label: "Review comments", unit: "comments", format: count, op: "sum", field: "review_comments", value: "review_comments", additive: true,
    note: "Comments other people or bots left on the workspaces' pull requests, on lines of the change or in its conversation. The author's own comments are not counted." },
  reviews: { label: "Reviews", unit: "reviews", format: count, op: "sum", field: "reviews", value: "reviews", additive: true,
    note: "Reviews other people or bots submitted on the workspaces' pull requests, whatever their verdict." },
  changes_requested_share: { label: "Changes requested share", unit: "percent", format: percent, op: "sum", ratio: ["changes_requested", { op: "sum", field: "reviewed", label: "reviewed workspaces" }], additive: false,
    note: "Of the workspaces whose pull requests someone other than the author reviewed or commented on, those where a reviewer requested changes. Workspaces with no pull request read, or with one nobody reviewed, are left out." },
  review_comments_per_pull_request: { label: "Review comments per pull request", unit: "comments", format: perPullRequest, op: "sum", ratio: ["review_comments", { op: "sum", field: "pull_requests", label: "pull requests read" }], additive: false,
    note: "Comments from other people or bots, over the pull requests read, pooled rather than averaged per workspace. A pull request nobody commented on counts as zero." },
  commits_after_review: { label: "Commits after first review", unit: "commits", format: count, op: "sum", field: "commits_after_review", value: "commits_after_review", additive: true,
    note: "Commits added to the workspaces' pull requests after their first review or comment from someone else." },
};

export const reviewStatusLabels: Record<string, string> = { changes_requested: "Changes requested", approved: "Approved", commented: "Commented", none: "Not reviewed" };

export const outcomeTierLabels: Record<string, string> = { local_git: "Local Git (Tier 2)", transcript: "Transcript (Tier 1)", none: "No evidence" };

const breakdowns: AnalysisCatalog["breakdowns"] = {
  outcome_label: { label: "Outcome", fields: ["outcome_label"], order: outcomeLabels.map(label => label.id), values: Object.fromEntries(outcomeLabels.map(label => [label.id, label.label])), empty: "Unknown" },
  evidence_tier: { label: "Evidence tier", fields: ["evidence_tier"], order: ["local_git", "transcript", "none"], values: outcomeTierLabels },
  project_name: projectBreakdown(),
  provider: { label: "Main provider", fields: ["provider"], order: ["claude", "antigravity", "codex", "chatgpt", "tl1", "canonical"], empty: "No usage" },
  model_family: { label: "Main model", fields: ["model_family"], empty: "No usage", note: "A workspace's main model is the one that took most of its cost." },
  source_kind: { label: "Source", fields: ["source_kind"] },
  // Git facts: branch is only for sessions in a repository, and local Git
  // only where it checked something (outcomeCatalogFor).
  branch: { label: "Branch", fields: ["branch"], where: repository, empty: "No branch", note: "Branch counts workspaces in a repository only: a folder has no branch." },
  git_check: { label: "Local Git (Tier 2)", fields: ["git_check"], order: ["checked", "not_checked", "not_applicable"], values: { checked: "Checked", not_checked: "Not checked", not_applicable: "Not applicable" } },
  // Forge facts: only where a pull request of the workspace's was read.
  review_status: { label: "Review (Tier 3)", fields: ["review_status"], order: ["changes_requested", "approved", "commented", "none"], values: reviewStatusLabels, empty: "No pull request read",
    note: "The strongest verdict a reviewer other than the author gave: changes requested, else approved, else comments without a verdict." },
  reviewer_kind: { label: "Reviewed by", fields: ["reviewer_kind"], order: ["human", "both", "bot", "none"], values: { human: "People", both: "People and bots", bot: "Bots", none: "Nobody" }, empty: "No pull request read" },
  pull_request_state: { label: "Pull request", fields: ["pull_request_state"], order: ["merged", "open", "closed"], values: { merged: "Merged", open: "Open", closed: "Closed, not merged" }, empty: "No pull request read" },
};

// Breakdowns that need a tier: shown only when some session reached it. Forge
// is Tier 3: a pull request of the workspace's was read from its forge. It is
// not an evidence_tier value, since it never decides a label.
export const outcomeTierBreakdowns: Record<string, "local_git" | "forge"> = { git_check: "local_git", review_status: "forge", reviewer_kind: "forge", pull_request_state: "forge" };
const tierMissing = {
  local_git: "No workspace has local Git evidence: local Git checks only repositories on this Mac",
  forge: "No pull request has been read: turn on Pull request reviews in Settings → Background Processing",
};

export const outcomesAnalysisCatalog: AnalysisCatalog = {
  dataset: "outcomes", time: { first: "started_at", last: "started_at" }, measures, breakdowns,
  records: { label: "Workspace", plural: "Workspaces", note: "One workspace, counted at its start." },
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
    { ...outcomesAnalysisCatalog.breakdowns[id], unavailable: tierMissing[outcomeTierBreakdowns[id]!] }])) } };
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

// The tiers some session reached, for outcomeCatalogFor: its evidence tiers,
// and the forge when any pull request was read.
export const outcomeTiersReached = (buckets: Bucket[], pullRequests: Bucket[] = []) => [
  ...buckets.filter(bucket => Number(bucket.value) > 0).map(bucket => String(bucket.keys[0])),
  ...(pullRequests.some(bucket => Number(bucket.value) > 0) ? ["forge"] : []),
];

// A breakdown's label for a group key, its empty label for a missing one.
export function outcomeGroupLabel(breakdown: string, key: string): string {
  const item = breakdowns[breakdown];
  if (!key) return item?.empty ?? "None";
  return item?.values?.[key] ?? key;
}
