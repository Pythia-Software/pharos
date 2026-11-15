// A catalog dashboard's population: the filters other than its range, the
// legend's population actions, the chips and title that name it, and the
// cross-language link site_link builds for it.
import test from "node:test";
import assert from "node:assert/strict";
import { decodeQuery, encodeQuery, EMPTY_QUERY } from "@pythia-software/query-table-core";
import document from "../../../../schemas/session_shape.schema.json";
import cases from "../../../../testdata/site-map-cases.json";
import { core } from "../helpers/recipes.mjs";
import { describePopulation, populationLabels, populationOf, populationSuffix, selectionPopulation, withPopulation, withPopulationGroup } from "../../../src/dashboard-population";
import { analysisQuery } from "../../../src/dashboard-analysis-model";
import { trendQuery, trendSeries } from "../../../src/dashboard-trend-model";
import { withTimeRange } from "../../../src/chart-range";
import { sessionShapeAnalysisCatalog as catalog } from "../../../src/session-shape-analysis-catalog";
import { isModelField, modelFieldLabel, modelLabel } from "../../../src/model-label";

const schema = core.loadSchema(document);
const labels = populationLabels(catalog, schema.fields);
const range = withTimeRange([], catalog.time, undefined, undefined, "30d");
const chips = where => describePopulation(catalog, schema.fields, populationOf(catalog, where), labels).map(item => item.chip);
const leaveOut = (field, value) => ({ any: [{ field, op: "!=", value }, { field, op: "is_null", value: "" }] });

test("the population is the filters other than the range, and replacing it keeps the range", () => {
  const harness = { field: "harness", op: "=", value: "claude-code" };
  assert.deepEqual(populationOf(catalog, [...range, harness]), [harness]);
  const replaced = withPopulation(catalog, [...range, harness], [{ field: "agent_kind", op: "=", value: "root" }]);
  assert.deepEqual(populationOf(catalog, replaced), [{ field: "agent_kind", op: "=", value: "root" }]);
  assert.deepEqual(replaced.filter(term => !populationOf(catalog, [term]).length), range);
});

test("the stored model sentinel reads Model not recorded, and its filter keeps the stored value", () => {
  assert.deepEqual(["Unknown model", "unknown model", "gpt-6-sol", ""].map(modelLabel), ["Model not recorded", "Model not recorded", "gpt-6-sol", ""]);
  assert.deepEqual(["model", "root_model", "model_family", "root_model_family", "model_effort", "harness", "remodel"].map(isModelField), [true, true, true, true, false, false, false]);
  assert.equal(modelFieldLabel("title", "Unknown model"), "Unknown model", "only a model field holds the sentinel");
  assert.equal(labels("model_family", "unknown model"), "Model not recorded");
  assert.equal(labels("model_family", "opus-4-8"), "opus-4-8");
  assert.equal(labels("model_family", null), "Model not recorded", "a session with no model at all reads the same way");
  const where = withPopulationGroup(catalog, range, "model_family", "unknown model", "drop");
  assert.deepEqual(populationOf(catalog, where), [leaveOut("model_family", "unknown model")]);
  // A trend's legend names the sentinel the same way, and its series stay on the stored value.
  const now = Date.UTC(2026, 10, 10, 12), day = new Date(now - 20 * 86400000).toISOString().slice(0, 10);
  const state = { view: "trend", metric: "sessions", split: "model_family" }, trend = trendQuery(catalog, state, range, { now, zone: "UTC" });
  const results = trend.query.aggregations.map(clause => ({ id: clause.id, coverage: "exact", buckets: [["unknown model", 3], ["opus-4-8", 2]].map(([family, value]) => ({ keys: [day, family], value, count: value })) }));
  const built = trendSeries(catalog, state, trend, results, { now });
  assert.equal(built.error, undefined);
  assert.deepEqual(Object.fromEntries(built.series.map(item => [item.value, item.label])), { "unknown model": "Model not recorded", "opus-4-8": "opus-4-8" });
});

test("the legend's Leave out and Count only write terms on the breakdown's field", () => {
  let where = withPopulationGroup(catalog, range, "harness", "antigravity-cli", "drop");
  assert.deepEqual(populationOf(catalog, where), [leaveOut("harness", "antigravity-cli")]);
  // A second exclusion adds to the first; a missing value is left out by is_not_null.
  where = withPopulationGroup(catalog, where, "harness", "codex-cli", "drop");
  where = withPopulationGroup(catalog, where, "harness", "", "drop");
  assert.deepEqual(chips(where), ["Agent app is not antigravity-cli", "Agent app is not codex-cli", "Agent app is set"]);
  // Count only replaces the field's slice; leaving out one of several kept values narrows it.
  where = withPopulationGroup(catalog, where, "harness", "claude-code", "keep");
  assert.deepEqual(populationOf(catalog, where), [{ field: "harness", op: "=", value: "claude-code" }]);
  where = selectionPopulation(catalog, range, "harness", { values: ["claude-code", "codex-cli"], excluded: false });
  where = withPopulationGroup(catalog, where, "harness", "codex-cli", "drop");
  assert.deepEqual(populationOf(catalog, where), [{ field: "harness", op: "=", value: "claude-code" }]);
  // A term on another field is kept, and the range is never touched.
  where = withPopulationGroup(catalog, [...where, { field: "agent_kind", op: "=", value: "root" }], "archetype", "automated", "drop");
  assert.equal(populationOf(catalog, where).length, 3);
  assert.deepEqual(where.filter(term => !populationOf(catalog, [term]).length), range);
});

test("a legend selection made the population keeps or leaves out each group", () => {
  const kept = selectionPopulation(catalog, range, "archetype", { values: ["deep", "marathon"], excluded: false });
  assert.deepEqual(chips(kept), ["Archetype is one of Deep or Marathon"]);
  const left = selectionPopulation(catalog, range, "archetype", { values: ["automated", "quick"], excluded: true });
  assert.deepEqual(chips(left), ["Archetype is not Automated", "Archetype is not Quick"]);
});

test("chips and the title suffix name values as the legend does, and other terms as the query editor does", () => {
  const where = [...range, leaveOut("archetype", "automated"), { field: "agent_kind", op: "=", value: "root" }, { field: "active_seconds", op: "is_not_null", value: "" }];
  const terms = describePopulation(catalog, schema.fields, populationOf(catalog, where), labels);
  assert.deepEqual(terms.map(item => item.chip), ["Archetype is not Automated", "Agent kind is Main agent", "Active time (s) is not empty"]);
  assert.equal(populationSuffix(terms), " · excluding Automated; only Main agent; active time (s) is not empty");
  assert.equal(populationSuffix([]), "");
});

test("every view counts the population", () => {
  const where = [...range, leaveOut("harness", "antigravity-cli")];
  // A grouped view filters its records before grouping them into units.
  const has = query => JSON.stringify(query).includes("antigravity-cli");
  for (const view of ["compare", "distribution", "scatter", "cycle"]) {
    assert.ok(has(analysisQuery(catalog, { view, metric: "sessions", split: "length_band", unit: view === "scatter" ? "session" : "records" }, where).query), view);
  }
  assert.ok(has(trendQuery(catalog, { view: "trend", metric: "sessions", split: "provider" }, where).query));
});

test("a population round-trips through analyze_filters and matches the link site_link builds", () => {
  const example = cases.population;
  const read = decodeQuery(new URL(example.url, "http://localhost").searchParams.get("analyze_filters")).where;
  assert.deepEqual(populationOf(catalog, read), example.filters);
  assert.deepEqual(chips(read), ["Agent app is not antigravity-cli", "Agent kind is Main agent"]);
  // The page writes the same token back.
  assert.deepEqual(decodeQuery(encodeQuery({ ...EMPTY_QUERY, where: read })).where, read);
});
