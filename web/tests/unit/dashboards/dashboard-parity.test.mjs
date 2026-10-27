// Exemplar cards expressed as Tokens and Cost dashboard states. Each pair runs
// the card's own chart query and the dashboard's analysis query over the same
// usage ledger with Query Table's in-process evaluation, grouping stages
// included, and must report the same numbers.
import test from "node:test";
import assert from "node:assert/strict";
import { core, schemas, instantiateRecipe, recipeById, cardCoverQuery } from "../helpers/recipes.mjs";
import { now, usageRows } from "../helpers/usage-ledger.mjs";
import { analysisQuery } from "../../../src/dashboard-analysis-model";
import { tokenAnalysisCatalog } from "../../../src/token-analysis-catalog";
import { cardDashboardWhere } from "../../../src/card-exploration";
import { filterBoxSamples } from "../../../src/compact-box-model";

const rows = usageRows(), schema = schemas.usage;
const evaluate = query => core.evaluateMetrics(rows, query, schema, { now }).metrics[0];
const round = value => typeof value === "number" ? Number(value.toPrecision(12)) : value;

// The card's chart, and the dashboard opened from it: the card's population
// becomes the dashboard's filters, as cardDestination writes them.
function pair(id, state) {
  const recipe = recipeById(id), item = instantiateRecipe(recipe, 30, new Date(now));
  const card = cardCoverQuery(item.query, recipe);
  const where = cardDashboardWhere("usage", "usage", (item.query.grouping?.source ?? item.query).where);
  const dashboard = analysisQuery(tokenAnalysisCatalog, state, where);
  const cardResult = evaluate(card.query), dashboardResult = evaluate(dashboard.query);
  assert.equal(cardResult.error, undefined, `${id}: card`);
  assert.equal(dashboardResult.error, undefined, `${id}: dashboard`);
  return { recipe, card: cardResult, dashboard: dashboardResult, clause: dashboard.clause };
}
// Groups by their full keys: one model family has two providers here.
const byKeys = (buckets, value = bucket => bucket.value) => Object.fromEntries(buckets.map(bucket => [JSON.stringify(bucket.keys), round(value(bucket))]));

test("model-cost-share: compare, shown as shares, by model over fully priced usage", () => {
  const { card, dashboard, clause } = pair("model-cost-share", { view: "compare", metric: "cost", split: "provider_model", compareAs: "share" });
  assert.equal(clause.display.kind, "pie");
  assert.ok(byKeys(card.buckets)['["claude","opus"]'] && byKeys(card.buckets)['["antigravity","opus"]'], "one model family, two providers");
  assert.deepEqual(byKeys(dashboard.buckets), byKeys(card.buckets));
  // Shares divide by the same total.
  const total = buckets => buckets.reduce((sum, bucket) => sum + bucket.value, 0);
  assert.equal(round(total(dashboard.buckets)), round(total(card.buckets)));
});

test("cache-efficiency: a token-weighted ratio measure by model", () => {
  const { card, dashboard, clause } = pair("cache-efficiency", { view: "compare", metric: "cache_read_share", split: "provider_model" });
  assert.equal(clause.display.format.kind, "percent");
  assert.deepEqual(byKeys(dashboard.buckets), byKeys(card.buckets));
  assert.ok(Object.values(byKeys(card.buckets)).every(value => value > 0 && value < 1));
  // The denominator is all input, cache writes included: the older
  // reads / (reads + uncached) definition would give a different share.
  const opus = rows.filter(row => row.provider === "claude" && row.model_family === "opus" && row.input_tokens > 0);
  const sum = field => opus.reduce((n, row) => n + row[field], 0);
  assert.equal(byKeys(dashboard.buckets)['["claude","opus"]'], round(sum("cache_read_input_tokens") / sum("input_tokens")));
  assert.notEqual(byKeys(dashboard.buckets)['["claude","opus"]'], round(sum("cache_read_input_tokens") / (sum("cache_read_input_tokens") + sum("uncached_input_tokens"))));
});

test("model-session-costs: per-conversation cost distributions by root model, five-conversation minimum", () => {
  const { recipe, card, dashboard, clause } = pair("model-session-costs", { view: "distribution", unit: "conversation", metric: "cost", split: "provider_model", minSamples: 5 });
  assert.equal(clause.distribution.kind, "box");
  const cardBoxes = filterBoxSamples(card, recipe.cover.minSamples);
  const summary = bucket => bucket.distribution.summary;
  assert.deepEqual(Object.keys(byKeys(cardBoxes.buckets)), ['["claude","opus"]'], "only Claude Opus has five conversations");
  assert.deepEqual(byKeys(dashboard.buckets, bucket => JSON.stringify(summary(bucket), (key, value) => round(value))), byKeys(cardBoxes.buckets, bucket => JSON.stringify(summary(bucket), (key, value) => round(value))));
  assert.equal(dashboard.hiddenGroups, 3, "Sonnet, GPT-5 and Antigravity Opus are reported as hidden");
});

test("session-token-cost: one point per conversation, tokens against cost", () => {
  const { card, dashboard, clause } = pair("session-token-cost", { view: "scatter", unit: "conversation", metric: "tokens", metricY: "cost", split: "provider_model" });
  assert.equal(clause.display.kind, "scatter");
  const points = buckets => byKeys(buckets, bucket => [round(bucket.value), round(bucket.y)]);
  assert.ok(Object.keys(points(card.buckets)).length >= 10);
  assert.deepEqual(points(dashboard.buckets), points(card.buckets), "same keys: top-level provider, model, conversation");
});
