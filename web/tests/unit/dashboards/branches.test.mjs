import test from "node:test";
import assert from "node:assert/strict";
import branchDocument from "../../../../schemas/branch_usage.schema.json";
import { catalogProblems } from "../../../src/dashboard-analysis-model";
import { branchAnalysisCatalog, branchCoverage, branchCoverageLabels, branchSummaryMetrics, branchUsageDisplay } from "../../../src/branch-analysis-catalog";
import { dashboardDefaults } from "../../../src/dashboard-defaults";

const field = name => branchDocument.fields.find(field => field.name === name);

test("the spend-by-branch catalog is valid and its coverage matches the dataset", () => {
  assert.deepEqual(catalogProblems(branchAnalysisCatalog), []);
  assert.deepEqual(field("coverage").filter.values.options.map(option => option.value), branchAnalysisCatalog.breakdowns.coverage.order);
  assert.deepEqual(field("coverage").filter.values.options.map(option => option.label), Object.values(branchCoverageLabels));
  for (const breakdown of Object.values(branchAnalysisCatalog.breakdowns)) for (const name of breakdown.fields) assert.ok(field(name), name);
  // A branch is drawn only where one was recorded; the rest is the coverage split.
  assert.deepEqual(branchAnalysisCatalog.breakdowns.branch.where, [{ field: "coverage", op: "=", value: "on_branch" }]);
  // Branch names repeat across repositories: the default view and the
  // Branches count key on the project and the branch together.
  const split = branchAnalysisCatalog.breakdowns[dashboardDefaults.branches.split];
  assert.deepEqual(split.fields, ["project_branch"]);
  assert.deepEqual(split.where, branchAnalysisCatalog.breakdowns.branch.where);
  assert.equal(branchSummaryMetrics.find(metric => metric.id === "branches").field, "project_branch");
  assert.ok(field("project_branch").aggregate.groupable);
});

test("the coverage split lists every coverage, and a row without a branch says why", () => {
  const split = branchCoverage([{ keys: ["on_branch"], value: 3 }, { keys: ["unknown"], value: 1 }]);
  assert.deepEqual(split.map(item => [item.label, item.cost, item.share]), [["On a branch", 3, 0.75], ["Unknown branch", 1, 0.25], ["No branch data", 0, 0]]);
  assert.deepEqual(branchCoverage([]).map(item => item.share), [0, 0, 0]);
  assert.deepEqual(branchUsageDisplay("feature/a", "on_branch"), { text: "feature/a", muted: false });
  assert.deepEqual(branchUsageDisplay(null, "unknown"), { text: "Unknown branch", muted: true });
  assert.equal(branchUsageDisplay("", "no_branch_data").text, "No branch data");
});
