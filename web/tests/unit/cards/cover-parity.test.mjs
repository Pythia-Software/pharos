// A collected card's cover must survive saving and dashboard edits. Home
// lets its owner pick another cover metric and a group count, so the default
// tier tries every cover metric of every active card once, rotating counts
// and edits across the catalog; `npm run test:full` (PHAROS_TEST_FULL=1)
// tries each with and without a count and an edit.
import test from "node:test";
import assert from "node:assert/strict";
import { fullMatrix } from "../helpers/recipes.mjs";
import { active, compare, fixture } from "../helpers/parity.mjs";
import { defaultCardCover } from "../../../src/card-cover";

// The cover metrics Home offers: grouped values and source observations.
function covers(recipe) {
  const initial = defaultCardCover(recipe);
  const choices = [...(recipe.exploration?.summaries.filter(column => column.op !== "row").map(column => ({ mode: "result", metricId: column.id })) ?? []), ...recipe.measures.map(metric => ({ mode: "source", metricId: metric.id }))];
  return [...initial ? [initial] : [], ...choices.filter(choice => choice.mode !== initial?.mode || choice.metricId !== initial?.metricId)];
}

test("every active card preserves chosen covers, group counts and metadata-only dashboard edits", async t => {
  const { values } = fixture(t), failures = [];
  let cases = 0, rotation = 0;
  for (const recipe of active) {
    for (const base of covers(recipe)) {
      const counts = fullMatrix ? [undefined, 4] : [rotation % 2 ? 4 : undefined];
      const edits = fullMatrix ? [false, true] : [rotation % 3 === 1];
      rotation++;
      for (const maxGroups of counts) {
        const cover = maxGroups === undefined ? base : { ...base, maxGroups };
        const edited = { ...recipe, title: `Custom ${recipe.id}`, cover };
        for (const edit of edits) {
          const result = await compare(edited, 7, values, { edit });
          cases++;
          if (result.preview || result.analysis || result.loading || result.failures.length) failures.push({ id: recipe.id, cover: `${base.mode}:${base.metricId}`, maxGroups, edit, ...result });
        }
      }
    }
  }
  t.diagnostic(`${active.length} active cards: ${cases} comparisons across all cover metrics${fullMatrix ? ", group counts, and dashboard metadata edits" : " (PHAROS_TEST_FULL=1 adds every count and edit)"}`);
  assert.deepEqual(failures, []);
});
