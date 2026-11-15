import type { AggregationClause, WhereTerm } from "@pythia-software/query-table-core";
import type { AnalysisCatalog, AnalysisMeasure } from "./dashboard-analysis-model";
import { projectBreakdown, projectLegacy } from "./project-analysis";
import { tokenMetricFormat } from "./token-count";
import { modelNotRecorded } from "./model-label";

// Spend by branch over the branch_usage dataset: one record is one workspace's
// priced usage on one branch, with one model, in one hour. Only workspaces
// in Git repositories have records. A workspace that switched branches spends
// on each in turn (docs/projects-and-outcomes.md#spend-by-branch).

const count = { kind: "number", decimals: 0 } as const, usd = { kind: "number", decimals: 2 } as const;
const onBranch: WhereTerm[] = [{ field: "coverage", op: "=", value: "on_branch" }];

const measures: Record<string, AnalysisMeasure> = {
  cost: { label: "API cost (USD)", unit: "USD", format: usd, op: "sum", field: "cost_usd", value: "cost_usd", additive: true,
    note: "Estimated API list-price cost, not subscription spend. Within each hour, a workspace’s cost is split among its branches by the tokens its turns on each branch used." },
  tokens: { label: "Tokens", unit: "tokens", format: tokenMetricFormat, op: "sum", field: "tokens", value: "tokens", additive: true, note: "Reconciled tokens, split among branches the same way." },
  sessions: { label: "Workspaces", unit: "workspaces", format: count, op: "count_distinct", field: "workspace_id", additive: false, note: "Distinct workspaces; a workspace that switched branches counts once on each branch." },
};

export const branchCoverageLabels: Record<string, string> = { on_branch: "On a branch", unknown: "Unknown branch", no_branch_data: "No branch data" };

const breakdowns: AnalysisCatalog["breakdowns"] = {
  project_branch: { label: "Project and branch", fields: ["project_branch"], where: onBranch, empty: "No branch",
    note: "Usage on a recorded branch only; Coverage shows the rest. A branch name used in several projects is a separate branch in each." },
  branch: { label: "Branch name", fields: ["branch"], where: onBranch, empty: "No branch",
    note: "Usage on a recorded branch only; Coverage shows the rest. Branches with the same name in different projects are added together." },
  coverage: { label: "Coverage", fields: ["coverage"], order: Object.keys(branchCoverageLabels), values: branchCoverageLabels,
    note: "Unknown branch: usage before the first recorded branch, on a detached HEAD, or not tied to a turn. No branch data: usage in a workspace that recorded no branch." },
  project_name: projectBreakdown(),
  provider: { label: "Provider", fields: ["provider"], order: ["claude", "antigravity", "codex", "chatgpt", "tl1", "canonical"] },
  model_family: { label: "Model", fields: ["model_family"], empty: modelNotRecorded },
  source_kind: { label: "Source", fields: ["source_kind"],
    note: "The retained history source. Linked copies count once, so work started in Conductor can have a native Claude or Codex source." },
  channel: { label: "Channel", fields: ["channel"], empty: "Unknown channel",
    note: "How the top-level conversation started. Linked helpers inherit that channel, including when their usage comes from a native Claude or Codex transcript. Channel attribution says how the channel was established: direct, inherited, inferred, or not established." },
  branch_source: { label: "Branch recorded", fields: ["branch_source"], where: onBranch, order: ["turn", "session"], values: { turn: "Per turn", session: "Per workspace" } },
};

export const branchAnalysisCatalog: AnalysisCatalog = {
  dataset: "branch_usage", time: { first: "usage_at", last: "usage_at" }, measures, breakdowns,
  records: { label: "Usage row", plural: "Usage rows", note: "One workspace’s usage on one branch, with one model, in one hour." },
  units: {}, periods: {}, timestamp: "usage_at",
  cycle: { hour: { kind: "timestamp", field: "usage_at" }, weekday: { kind: "timestamp", field: "usage_at" } },
  trend: { metric: "cost", split: "coverage" }, scatter: ["cost", "tokens"], legacy: projectLegacy(),
};

// The dashboard's summary: spend per coverage, and how many branches had
// spend, each within its project (main in ten repositories is ten branches).
export const branchSummaryMetrics: AggregationClause[] = [
  { id: "cost", op: "sum", field: "cost_usd", groupBy: ["coverage"] },
  { id: "branches", op: "count_distinct", field: "project_branch", groupBy: [] },
];

// The coverage split: spend on each coverage, every coverage listed.
export function branchCoverage(buckets: { keys: unknown[]; value: unknown }[]): { coverage: string; label: string; cost: number; share: number }[] {
  const costs = new Map(buckets.map(bucket => [String(bucket.keys[0] ?? ""), Number(bucket.value) || 0]));
  const total = [...costs.values()].reduce((sum, value) => sum + value, 0);
  return Object.keys(branchCoverageLabels).map(coverage => ({ coverage, label: branchCoverageLabels[coverage]!, cost: costs.get(coverage) ?? 0, share: total ? (costs.get(coverage) ?? 0) / total : 0 }));
}

// A row's branch, or why it has none.
export function branchUsageDisplay(value: unknown, coverage: unknown): { text: string; muted: boolean } {
  if (value != null && value !== "") return { text: String(value), muted: false };
  return { text: branchCoverageLabels[String(coverage)] ?? "No branch", muted: true };
}
