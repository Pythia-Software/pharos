import test from "node:test";
import assert from "node:assert/strict";
import outcomesDocument from "../../../../schemas/outcomes.schema.json";
import { catalogProblems, normalizeAnalysis, splitValid } from "../../../src/dashboard-analysis-model";
import { outcomeLabels, branchDisplay } from "../../../src/outcomes";
import { outcomeCatalogFor, outcomeGroupLabel, outcomeRatioRows, outcomeTierCoverage, outcomeTiersReached, outcomesAnalysisCatalog } from "../../../src/outcomes-analysis-catalog";

const field = name => outcomesDocument.fields.find(field => field.name === name);

test("the outcomes catalog is valid and its labels keep the Library's order and wording", () => {
  assert.deepEqual(catalogProblems(outcomesAnalysisCatalog), []);
  const label = outcomesAnalysisCatalog.breakdowns.outcome_label;
  assert.deepEqual(label.order, outcomeLabels.map(item => item.id));
  assert.deepEqual(field("outcome_label").filter.values.options.map(option => option.value), label.order);
  assert.deepEqual(outcomesAnalysisCatalog.breakdowns.evidence_tier.order, ["local_git", "transcript", "none"]);
  // Every breakdown and measure reads a field the dataset has.
  for (const breakdown of Object.values(outcomesAnalysisCatalog.breakdowns)) for (const name of breakdown.fields) assert.ok(field(name), name);
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
});

test("panel ratios pool sums per group and have no ratio without a denominator", () => {
  const rows = outcomeRatioRows(
    [{ keys: ["pharos"], value: 30 }, { keys: ["notes"], value: 0 }, { keys: [null], value: 5 }],
    [{ keys: ["pharos"], value: 300 }, { keys: ["notes"], value: 40 }],
    key => outcomeGroupLabel("project_name", key));
  assert.deepEqual(rows.map(row => [row.label, row.ratio]), [["pharos", 0.1], ["notes", 0], [outcomesAnalysisCatalog.breakdowns.project_name.empty ?? "None", null]]);
  const coverage = outcomeTierCoverage([{ keys: ["transcript"], value: 3 }, { keys: ["none"], value: 1 }]);
  assert.deepEqual(coverage.map(row => [row.tier, row.sessions, row.share]), [["local_git", 0, 0], ["transcript", 3, 0.75], ["none", 1, 0.25]]);
});
