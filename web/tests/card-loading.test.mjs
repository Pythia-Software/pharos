import test from "node:test";
import assert from "node:assert/strict";
import { build } from "esbuild";
import * as core from "@pythia-software/query-table-core";
const bundle = await build({ entryPoints: ["src/card-loading-model.ts", "src/curiosity-data.ts"], bundle: true, write: false, platform: "node", format: "esm", outdir: "out" });
const importBundle = name => import(`data:text/javascript;base64,${Buffer.from(bundle.outputFiles.find(file => file.path.endsWith(`/${name}.js`)).text).toString("base64")}`);
const { cardLoadingPresentation } = await importBundle("card-loading-model");
const { instantiateRecipe, queryRecipes } = await importBundle("curiosity-data");
const query = clause => ({ ...core.EMPTY_QUERY, aggregations: [{ id: "m", op: "count", groupBy: ["tool_name"], ...clause }] });

test("loading shapes use the selected display and configured group count", () => {
  for (const [display, kind] of [["bar-vertical", "columns"], ["bar-horizontal", "list"], ["scatter", "scatter"], ["line", "line"], ["pie", "pie"], ["donut", "donut"], ["table", "list"]]) {
    const input = query({ display: { kind: display, maxGroups: 4 } }), before = structuredClone(input);
    const spec = cardLoadingPresentation(input);
    assert.equal(spec.kind, kind);
    assert.equal(spec.count, 4);
    assert.deepEqual(input, before);
  }
  assert.equal(cardLoadingPresentation(query({ groupBy: [] })).kind, "value");
  assert.equal(cardLoadingPresentation(query({ display: { kind: "value", maxGroups: 4 } })).count, 1);
});

test("histogram bins, paired values and box distributions retain their shapes", () => {
  assert.equal(cardLoadingPresentation(query({ expressionY: "AVG([tokens])", display: { kind: "auto" } })).kind, "scatter");
  assert.equal(cardLoadingPresentation(query({ distribution: { kind: "histogram", input: "[tokens]", bins: 9 }, display: { kind: "histogram", maxGroups: 2 } })).count, 9);
  const box = cardLoadingPresentation(query({ distribution: { kind: "box", input: "[duration]" }, display: { kind: "box", maxGroups: 5 } }));
  assert.equal(box.kind, "box");
  assert.equal(box.count, 5);
  assert.equal(box.height, 365);
});

test("cover projection follows source versus grouped-result choices and the selected metric", () => {
  const recipe = queryRecipes.find(recipe => recipe.id === "output-duration"), suggestion = instantiateRecipe(recipe, 7);
  const before = structuredClone(suggestion.query);
  assert.equal(cardLoadingPresentation(suggestion.query, { recipe }).kind, "scatter");
  const spec = cardLoadingPresentation(suggestion.query, { recipe, cover: { mode: "result", metricId: "card_count", kind: "bar-vertical", maxGroups: 4 } });
  assert.equal(spec.kind, "columns");
  assert.equal(spec.count, 4);
  assert.deepEqual(suggestion.query, before);
  const saved = { ...core.EMPTY_QUERY, aggregations: [{ id: "first", op: "count", groupBy: ["group"] }, { id: "second", op: "count", groupBy: [] }] };
  assert.equal(cardLoadingPresentation(saved, { metricId: "second" }).kind, "value");
});

test("daily covers reserve their headline and calendar bars, and nonmetrics use text or table placeholders", () => {
  const recipe = queryRecipes.find(recipe => recipe.id === "human-words"), suggestion = instantiateRecipe(recipe, 7);
  const spec = cardLoadingPresentation(suggestion.query, { recipe, days: 7 });
  assert.equal(spec.kind, "columns");
  assert.equal(spec.count, 8);
  assert.equal(spec.headline, true);
  assert.equal(spec.height, 208);
  assert.equal(cardLoadingPresentation(core.EMPTY_QUERY).kind, "text");
  assert.equal(cardLoadingPresentation({ ...core.EMPTY_QUERY, grouping: { source: core.EMPTY_QUERY, keys: ["group"], summaries: [] } }).kind, "table");
});
