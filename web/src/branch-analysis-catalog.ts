import type { WhereTerm } from "@pythia-software/query-table-core";
import type { AnalysisCatalog, AnalysisMeasure } from "./dashboard-analysis-model";
import { projectBreakdown, projectLegacy } from "./project-analysis";

// Spend by branch over the branch_usage dataset: one record is one session's
// priced usage on one branch, with one model, on one local day. Only sessions
// in Git repositories have records. A session that switched branches spends
// on each in turn (docs/projects-and-outcomes.md#spend-by-branch).

const count = { kind: "number", decimals: 0 } as const, usd = { kind: "number", decimals: 2 } as const;
const onBranch: WhereTerm[] = [{ field: "coverage", op: "=", value: "on_branch" }];

const measures: Record<string, AnalysisMeasure> = {
  cost: { label: "API cost (USD)", unit: "USD", format: usd, op: "sum", field: "cost_usd", value: "cost_usd", additive: true,
    note: "API-equivalent cost, priced as the Library prices it. Within an hour, a session's cost is split among its branches by the tokens each branch's turns used." },
  tokens: { label: "Tokens", unit: "tokens", format: count, op: "sum", field: "tokens", value: "tokens", additive: true, note: "Reconciled tokens, split among branches the same way." },
  sessions: { label: "Sessions", unit: "sessions", format: count, op: "count_distinct", field: "workspace_id", additive: false, note: "Distinct sessions; a session that switched branches counts once on each." },
};

export const branchCoverageLabels: Record<string, string> = { on_branch: "On a branch", unknown: "Unknown branch", no_branch_data: "No branch data" };

const breakdowns: AnalysisCatalog["breakdowns"] = {
  branch: { label: "Branch", fields: ["branch"], where: onBranch, empty: "No branch", note: "Usage on a recorded branch only; Coverage shows the rest." },
  coverage: { label: "Coverage", fields: ["coverage"], order: Object.keys(branchCoverageLabels), values: branchCoverageLabels,
    note: "Unknown branch: in a session that recorded a branch, but before its first, on a detached HEAD, or not placed on a turn. No branch data: a session that recorded none." },
  project_name: projectBreakdown(),
  provider: { label: "Provider", fields: ["provider"], order: ["claude", "antigravity", "codex", "chatgpt", "tl1", "canonical"] },
  model_family: { label: "Model", fields: ["model_family"], empty: "Unknown model" },
  source_kind: { label: "Harness", fields: ["source_kind"] },
  branch_source: { label: "Branch recorded", fields: ["branch_source"], where: onBranch, order: ["turn", "session"], values: { turn: "Per turn", session: "Per session" } },
};

export const branchAnalysisCatalog: AnalysisCatalog = {
  dataset: "branch_usage", time: { first: "usage_at", last: "usage_at" }, measures, breakdowns,
  records: { label: "Usage row", plural: "Usage rows", note: "One session's usage on one branch, with one model, on one local day." },
  units: {}, periods: {}, timestamp: "usage_at",
  cycle: { hour: { kind: "timestamp", field: "usage_at" }, weekday: { kind: "timestamp", field: "usage_at" } },
  trend: { metric: "cost", split: "coverage" }, scatter: ["cost", "tokens"], legacy: projectLegacy(),
};

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
