import test from "node:test";
import assert from "node:assert/strict";
import * as core from "@pythia-software/query-table-core";
import { cardLoadingPresentation } from "../../../src/card-loading-model";
import { instantiateRecipe, queryRecipes } from "../../../src/curiosity-data";
import { availableChartKinds, cardTemplates, saveCardTemplate } from "../../../src/card-library-model";
import { boxCoverHeight } from "../../../src/compact-box-model";
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
  const log = queryRecipes.find(recipe => recipe.id === "token-volume-log");
  assert.equal(cardLoadingPresentation(instantiateRecipe(log, 30).query, { recipe: log }).kind, "line");
  assert.equal(cardLoadingPresentation(core.EMPTY_QUERY).kind, "text");
  assert.equal(cardLoadingPresentation({ ...core.EMPTY_QUERY, grouping: { source: core.EMPTY_QUERY, keys: ["group"], summaries: [] } }).kind, "table");
});

test("compact box covers size their placeholder to the visible rows", () => {
  const recipe = queryRecipes.find(recipe => recipe.id === "model-session-costs");
  assert.equal(recipe.cover.density, "compact");
  assert.equal(cardLoadingPresentation(instantiateRecipe(recipe, 7).query, { recipe }).height, boxCoverHeight(recipe.cover.maxGroups, true));
});

test("chart heights persist through template edits and pairing pies remain editable", t => {
  const before = global.window, storage = new Map();
  global.window = { dispatchEvent() {}, pharosPrefs: { get: (key, fallback) => storage.get(key) ?? fallback, set: (key, value) => storage.set(key, value) } };
  t.after(() => { global.window = before; });
  const recipe = queryRecipes.find(recipe => recipe.id === "usage-models");
  saveCardTemplate(recipe.id, { title: recipe.title, description: recipe.description, days: 7, cover: { ...recipe.cover, height: 360 } });
  const saved = cardTemplates().find(item => item.id === recipe.id);
  assert.equal(saved.cover.height, 360);
  assert.equal(cardLoadingPresentation(instantiateRecipe(saved, 7).query, { recipe: saved }).height, 360);
  assert.throws(() => saveCardTemplate(recipe.id, { title: recipe.title, description: recipe.description, days: 7, cover: { ...recipe.cover, height: 20 } }), /height/);
  const pairing = queryRecipes.find(recipe => recipe.id === "model-conversation-reach");
  assert.ok(availableChartKinds(pairing).includes("pie"));
  saveCardTemplate(pairing.id, { title: pairing.title, description: pairing.description, days: 7, cover: pairing.cover });
  assert.equal(cardTemplates().find(item => item.id === pairing.id).cover.kind, "pie");
});


test("eligible template defaults retain Today while fixed and completed-day cards reject it", t => {
  const before = global.window, storage = new Map();
  global.window = { dispatchEvent() {}, pharosPrefs: { get: (key, fallback) => storage.get(key) ?? fallback, set: (key, value) => storage.set(key, value) } };
  t.after(() => { global.window = before; });
  const recipe = queryRecipes.find(recipe => recipe.id === "usage-models");
  saveCardTemplate(recipe.id, { title: recipe.title, description: recipe.description, days: 1, window: "today" });
  const saved = cardTemplates().find(item => item.id === recipe.id);
  assert.equal(saved.defaultWindow, "today");
  assert.equal(instantiateRecipe(saved, saved.defaultWindow).window.mode, "today");
  for (const other of queryRecipes.filter(recipe => recipe.dataset === "tl1_attempts" || recipe.weekday || recipe.trend?.completeDays || recipe.period)) {
    assert.throws(() => saveCardTemplate(other.id, { title: other.title, description: other.description, days: 1, window: "today" }), /complete days|fixed period/);
  }
});
