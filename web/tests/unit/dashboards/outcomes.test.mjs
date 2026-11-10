import test from "node:test";
import assert from "node:assert/strict";
import outcomesDocument from "../../../../schemas/outcomes.schema.json";
import { catalogProblems, normalizeAnalysis, splitValid } from "../../../src/dashboard-analysis-model";
import { outcomeLabels, branchDisplay } from "../../../src/outcomes";
import { outcomeCatalogFor, outcomeCostText, outcomeGroupLabel, outcomeKeptCostCell, outcomeRatioRows, outcomeTierCoverage, outcomeTiersReached, outcomesAnalysisCatalog } from "../../../src/outcomes-analysis-catalog";

const field = name => outcomesDocument.fields.find(field => field.name === name);

test("the outcomes catalog is valid and its labels keep the Library's order and wording", () => {
  assert.deepEqual(catalogProblems(outcomesAnalysisCatalog), []);
  const label = outcomesAnalysisCatalog.breakdowns.outcome_label;
  assert.deepEqual(label.order, outcomeLabels.map(item => item.id));
  assert.deepEqual(field("outcome_label").filter.values.options.map(option => option.value), label.order);
  assert.deepEqual(outcomesAnalysisCatalog.breakdowns.evidence_tier.order, ["local_git", "transcript", "none"]);
  // Every breakdown and measure reads a field the dataset has.
  for (const breakdown of Object.values(outcomesAnalysisCatalog.breakdowns)) for (const name of breakdown.fields) assert.ok(field(name), name);
  for (const measure of Object.values(outcomesAnalysisCatalog.measures)) for (const part of measure.ratio ?? []) assert.ok(field(typeof part === "string" ? part : part.field), measure.label);
  // A row is a Library workspace, and the records say so.
  assert.equal(outcomesAnalysisCatalog.records.label, "Workspace");
  assert.equal(outcomesAnalysisCatalog.measures.sessions.label, "Workspaces");
});

test("kept share and cost per kept change divide only what can be judged and priced", () => {
  const { kept_share, cost_per_kept_change } = outcomesAnalysisCatalog.measures;
  // Kept share: kept over judged, so a change too recent to judge, No changes
  // and Unknown neither count against it nor dilute it.
  assert.deepEqual(kept_share.ratio.map(part => typeof part === "string" ? part : part.field), ["kept_change", "judged"]);
  // Cost per kept change: judged cost over kept changes with a price, so an
  // unpriced kept workspace is in neither part.
  assert.deepEqual(cost_per_kept_change.ratio.map(part => typeof part === "string" ? part : part.field), ["judged_cost_usd", "kept_priced"]);
  // 5 kept at $2 and 5 kept unpriced cost $2 per priced kept change, not $1.
  const [row] = outcomeRatioRows([{ keys: ["sol"], value: 10 }], [{ keys: ["sol"], value: 5 }], key => key, [{ keys: ["sol"], value: 10 }]);
  assert.equal(row.ratio, 2);
  assert.deepEqual(outcomeKeptCostCell(row), ["$2.00", "10 kept (5 unpriced) · $10.00 judged cost"]);
  const [unpriced] = outcomeRatioRows([{ keys: ["sol"], value: null }], [{ keys: ["sol"], value: 0 }], key => key, [{ keys: ["sol"], value: 3 }]);
  assert.equal(outcomeKeptCostCell(unpriced)[0], "Unpriced");
  // A group without a main model recorded no usage: it is not unpriced.
  const [none] = outcomeRatioRows([{ keys: [null], value: null }], [{ keys: [null], value: 0 }], key => key, [{ keys: [null], value: 2 }]);
  assert.deepEqual(outcomeKeptCostCell(none), ["No usage", "2 kept · no usage"]);
  // A total cost of nothing priced: unpriced usage, or no usage at all.
  assert.equal(outcomeCostText(null, 3), "Unpriced");
  assert.equal(outcomeCostText(null, 0), "No usage");
  assert.equal(outcomeCostText(0, 0), "$0.00");
});

test("a missing value has its own label, never a blank", () => {
  assert.equal(outcomeGroupLabel("outcome_label", ""), "Unknown");
  assert.equal(outcomeGroupLabel("provider", ""), "No usage");
  assert.equal(outcomeGroupLabel("model_family", ""), "No usage");
  assert.equal(outcomeGroupLabel("branch", ""), "No branch");
  assert.equal(outcomeGroupLabel("evidence_tier", "local_git"), "Local Git (Tier 2)");
  // The branch breakdown counts only sessions in a repository; in a row, a
  // folder's branch is Not a repository and a repository's missing one No branch.
  assert.deepEqual(outcomesAnalysisCatalog.breakdowns.branch.where, [{ field: "project_kind", op: "=", value: "repository" }]);
  assert.equal(branchDisplay(null, "folder").text, "Not a repository");
  assert.equal(branchDisplay("", "repository").text, "No branch");
  assert.equal(branchDisplay(null, "none").text, "No git data");
  assert.deepEqual(branchDisplay("main", "repository"), { text: "main", muted: false });
});

test("a Tier 2 breakdown is disabled with its reason when no session reached Tier 2", () => {
  const plain = outcomeCatalogFor(outcomeTiersReached([{ keys: ["transcript"], value: 12 }, { keys: ["none"], value: 3 }, { keys: ["local_git"], value: 0 }]));
  assert.match(plain.breakdowns.git_check.unavailable, /No session has local Git evidence/);
  assert.deepEqual(catalogProblems(plain), []);
  // Disabled, it cannot be chosen, and a link asking for it draws without it.
  assert.equal(splitValid(plain, "records", "git_check"), false);
  assert.equal(normalizeAnalysis(plain, { view: "trend", metric: "cost", split: "git_check" }).split, "outcome_label");
  assert.equal(normalizeAnalysis(plain, { view: "compare", metric: "cost", split: "git_check" }).split, "none");
  // Every other breakdown stays, and a library with Tier 2 offers it.
  assert.equal(plain.breakdowns.outcome_label.unavailable, undefined);
  assert.equal(outcomeCatalogFor(["local_git", "transcript"]), outcomesAnalysisCatalog);
  assert.equal(outcomesAnalysisCatalog.breakdowns.git_check.unavailable, undefined);
  // Until the tiers are read (loading, or the read failed), every breakdown
  // stands: a link to the Git breakdown draws it, never "no Git evidence".
  assert.equal(outcomeCatalogFor(undefined), outcomesAnalysisCatalog);
  assert.equal(normalizeAnalysis(outcomeCatalogFor(undefined), { view: "trend", metric: "cost", split: "git_check" }).split, "git_check");
});

test("panel ratios pool sums per group and have no ratio without a denominator", () => {
  const rows = outcomeRatioRows(
    [{ keys: ["pharos"], value: 30 }, { keys: ["notes"], value: 0 }, { keys: [null], value: 5 }],
    [{ keys: ["pharos"], value: 300 }, { keys: ["notes"], value: 40 }],
    key => outcomeGroupLabel("project_name", key));
  assert.deepEqual(rows.map(row => [row.label, row.ratio]), [["pharos", 0.1], ["notes", 0], [outcomesAnalysisCatalog.breakdowns.project_name.empty ?? "None", null]]);
  // A cost of nothing priced is unknown, not $0; a priced $0 stays zero.
  const costs = outcomeRatioRows([{ keys: ["unpriced"], value: null }, { keys: ["free"], value: 0 }], [{ keys: ["unpriced"], value: 1 }, { keys: ["free"], value: 1 }], key => key);
  assert.deepEqual(costs.map(row => [row.key, row.numerator, row.ratio]), [["free", 0, 0], ["unpriced", null, null]]);
  const coverage = outcomeTierCoverage([{ keys: ["transcript"], value: 3 }, { keys: ["none"], value: 1 }]);
  assert.deepEqual(coverage.map(row => [row.tier, row.sessions, row.share]), [["local_git", 0, 0], ["transcript", 3, 0.75], ["none", 1, 0.25]]);
});
