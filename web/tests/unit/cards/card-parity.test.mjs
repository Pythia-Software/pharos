// Saved cards must preview exactly like the catalog cards they came from.
// The default tier compares every definition at one window; `npm run
// test:full` (PHAROS_TEST_FULL=1) compares every window. cover-parity covers
// customized covers.
import test from "node:test";
import assert from "node:assert/strict";
import { fullMatrix, queryRecipes, instantiateRecipe } from "../helpers/recipes.mjs";
import { active, compare, fixture } from "../helpers/parity.mjs";
import { defaultCardCover } from "../../../src/card-cover";
import { collectQuery } from "../../../src/bookmarks";

const windows = fullMatrix ? [1, 7, 30, 90] : [7];

for (const empty of [false, true]) test(`all card definitions retain preview results and loading shapes after collection (${empty ? "empty" : "populated"})`, async t => {
  const { values } = fixture(t, empty), failures = [];
  let ready = 0, cases = 0;
  for (const recipe of queryRecipes) for (const days of windows) {
    const result = await compare(recipe, days, values);
    cases++; if (result.state === "ready") ready++;
    if (result.preview || result.analysis || result.loading || result.failures.length) failures.push({ id: recipe.id, days, ...result });
  }
  t.diagnostic(`${queryRecipes.length} definitions × ${windows.length} window(s): ${cases} comparisons; ${ready} populated previews, ${cases - ready} empty previews`);
  assert.deepEqual(failures, []);
});

test("collecting all active cards preserves distinct studies sharing the same source query", t => {
  const { values } = fixture(t);
  for (const recipe of active) {
    const item = instantiateRecipe(recipe, 7);
    const options = { templateId: item.instanceId, window: item.window, cover: item.cover ?? defaultCardCover(item) };
    const saved = collectQuery(item.dataset, item.query, item.title, options);
    assert.equal(saved.templateId, item.instanceId, recipe.id);
    assert.equal(collectQuery(item.dataset, item.query, item.title, options).id, saved.id, "repeated collection of the same card still deduplicates");
  }
  assert.equal(values.get("pharos-bookmarks-v1").length, active.length);
});
