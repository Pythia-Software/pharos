// Every recipe, active or retired, at every preview window: its measures, its
// destination and its cover compile against the server contract, and its
// saved URL round-trips. Retired recipes stay here because saved collections
// still open them. Active cards also follow the catalog's copy rules.
import test from "node:test";
import assert from "node:assert/strict";
import { capabilities, cardCoverQuery, core, execution, instantiateRecipe, queryRecipes, schemas } from "./helpers/recipes.mjs";
import { categoryForQuery } from "../../src/query-categories";
import { suggestionPool } from "../../src/curiosity-data";

const clock = new Date(2026, 9, 13, 12), now = clock.getTime();
// Current-state and all-time recipes (recipe.period) have no lookback to vary.
const windows = recipe => recipe.period ? [7] : [...new Set([1, 7, 30, 90, ...(recipe.days ?? [])])];
// The TL1 revisit card predates the reviewed card copy.
const legacyCopy = new Set(["tl1-revisits"]);

test("recipe IDs are unique", () => {
  assert.equal(new Set(queryRecipes.map(recipe => recipe.id)).size, queryRecipes.length);
});

for (const recipe of queryRecipes) test(`${recipe.id}${recipe.retired ? " (retired)" : ""} compiles at every window`, () => {
  const schema = schemas[recipe.dataset];
  assert.ok(schema, `${recipe.id}: unknown dataset ${recipe.dataset}`);
  const source = { ...core.EMPTY_QUERY, aggregations: recipe.measures, where: recipe.where ?? [] };
  const measures = recipe.measures.some(metric => metric.dateGrouping)
    ? core.toQueryRequestV3(source, schema, capabilities, execution, { now, rows: false })
    : core.toMetricQuery(source, schema, execution);
  assert.deepEqual(measures.diagnostics, [], "measures");
  assert.equal(measures.metrics.length, recipe.measures.length, "every measure is requested");
  // A description advertises the population, not how many groups the cover shows.
  assert.doesNotMatch(recipe.description, /\b(?:five|six|seven|eight|twelve|\d+) (?:largest|leading|busiest|repositories|models)\b|plus Other|top \d+/i);

  for (const days of windows(recipe)) {
    const suggestion = instantiateRecipe(recipe, days, clock), { query } = suggestion;
    for (const rows of [false, true]) assert.deepEqual(core.toQueryRequestV3(query, schema, capabilities, execution, { now, rows }).diagnostics, [], `destination @${days}d`);
    const cover = cardCoverQuery(query, recipe);
    assert.deepEqual(core.toQueryRequestV3(cover.query, schema, capabilities, execution, { now, rows: false }).diagnostics, [], `cover @${days}d`);
    const restored = core.decodeQueryStrict(new URL(suggestion.url, "http://home.test").searchParams.get(`q_${recipe.dataset}`));
    assert.deepEqual(restored, core.normalizeQueryState(query), `URL @${days}d`);
    if (query.grouping) {
      const { grouping } = query;
      assert.deepEqual(grouping.source.aggregations, recipe.measures, `source metrics @${days}d`);
      assert.deepEqual(grouping.source.where.filter(term => term.field === recipe.field).map(term => term.value), recipe.period ? [] : [`-${days}d`, "+0s"], `rolling dates @${days}d`);
      assert.deepEqual(query.where, recipe.exploration.having ?? [], `HAVING @${days}d`);
      assert.equal(categoryForQuery(recipe.dataset, query), categoryForQuery(recipe.dataset, grouping.source));
    }
    if (!recipe.retired && !legacyCopy.has(recipe.id)) {
      assert.doesNotMatch(suggestion.description, /[{}]|sparkline|in this window|selected window/i);
      assert.doesNotMatch(suggestion.title, /[?{}]/);
    }
  }
  if (!recipe.retired && !legacyCopy.has(recipe.id)) assert.ok(recipe.action, "active cards name their destination");
});

test("suggestions only offer active recipes", () => {
  const retired = new Set(queryRecipes.filter(recipe => recipe.retired).map(recipe => recipe.id));
  const offered = suggestionPool(clock);
  assert.equal(offered.length, queryRecipes.length - retired.size);
  assert.ok(offered.every(suggestion => !retired.has(suggestion.id)));
});
