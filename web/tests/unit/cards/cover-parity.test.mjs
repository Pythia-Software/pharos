// Customized covers must survive saving and dashboard edits. The default tier
// tries every cover choice of every active card once, rotating chart kinds
// and edits so each still appears across the catalog; `npm run test:full`
// (PHAROS_TEST_FULL=1) tries every compatible chart kind with and without an
// edit.
import test from "node:test";
import assert from "node:assert/strict";
import { fullMatrix } from "../helpers/recipes.mjs";
import { active, compare, fixture, json } from "../helpers/parity.mjs";
import { defaultCardCover } from "../../../src/card-cover";
import { availableChartKinds, cardTemplates, coverChoices, decodeCover, saveCardTemplate } from "../../../src/card-library-model";

test("every active default cover can be saved without changing its settings", t => {
  const { values } = fixture(t), failures = [];
  for (const recipe of active) {
    values.clear();
    const cover = defaultCardCover(recipe);
    try {
      saveCardTemplate(recipe.id, { title: recipe.title, description: recipe.description, days: 7, cover });
      assert.deepEqual(json(defaultCardCover(cardTemplates().find(item => item.id === recipe.id))), json(cover), recipe.id);
    } catch (error) { failures.push({ id: recipe.id, error: error.message }); }
  }
  assert.deepEqual(failures, []);
});

test("every active card preserves customized covers and metadata-only dashboard edits", async t => {
  const { values } = fixture(t), failures = [];
  let cases = 0, rotation = 0;
  for (const recipe of active) {
    for (const choice of coverChoices(recipe)) {
      const baseCover = decodeCover(choice.value), compatible = availableChartKinds(recipe, baseCover);
      const kinds = fullMatrix ? compatible : [compatible[rotation % compatible.length]];
      const edits = fullMatrix ? [false, true] : [rotation % 2 === 1];
      rotation++;
      for (const kind of kinds) {
        const cover = { ...baseCover, kind, maxGroups: 4, showLegend: false, showOther: false, height: 310 };
        const edited = { ...recipe, title: `Custom ${recipe.id}`, cover };
        for (const edit of edits) {
          const result = await compare(edited, 7, values, { edit });
          cases++;
          if (result.preview || result.loading || result.failures.length) failures.push({ id: recipe.id, cover: choice.value, kind, edit, ...result });
        }
      }
    }
  }
  t.diagnostic(`${active.length} active cards: ${cases} comparisons across all cover metrics${fullMatrix ? ", compatible chart types, and dashboard metadata edits" : " (PHAROS_TEST_FULL=1 adds every chart type and edit)"}`);
  assert.deepEqual(failures, []);
});
