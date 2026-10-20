import test from "node:test";
import assert from "node:assert/strict";
import { cardCoverQuery, core, recipeById, recipeHarness, schemas } from "../helpers/recipes.mjs";
const schema = schemas.writing_messages;
const now = Date.parse("2026-10-15T12:00:00Z"), { card } = recipeHarness(now);
const row = (id, typed_words, conversation_id, work_id, repository_name = "one") => ({ id, typed_words, conversation_id, work_id, repository_name, title: "Same title", words: typed_words + 100, sent_at: now - 1000 });
const population = [row("a1", 10, "a", "w1"), row("a2", 30, "a", "w1"), row("b", 100, "b", "w1"), row("c", 60, "c", "w2", "two"), row("d", 50, "d", null, "two"), row("auto", 0, "auto", "w3"), { ...row("old", 10000, "old", "old"), sent_at: now - 8 * 86400000 }];
function value(id, metricId, rows = population) {
  const { recipe, query } = card(id);
  const cover = cardCoverQuery(query, recipe);
  return core.evaluateMetrics(rows, cover.query, schema, { now }).metrics.find(metric => metric.id === metricId).buckets[0]?.value;
}

test("word averages use windowed human messages and the intended identity denominator", () => {
  assert.equal(value("human-word-total", "human_words"), 250);
  assert.equal(value("human-words-per-message", "words_per_message"), 50);
  assert.equal(value("human-words-per-conversation", "words_per_conversation"), 62.5);
  assert.equal(value("human-words-per-message", "human_messages"), 5);
  assert.equal(value("human-words-per-conversation", "human_conversations"), 4);
  const { query } = card("human-repo-conversation-depth");
  const result = core.applyQuery(population, query, schema, { now });
  assert.deepEqual(result.rows.map(row => [row.repository_name, row.words_per_conversation, row.human_conversations]), [["one", 70, 2], ["two", 55, 2]]);
});

test("message-length distribution samples are conversation word totals", () => {
  const { recipe, query: histogram } = card("human-message-lengths");
  const distribution = core.evaluateMetrics(population, cardCoverQuery(histogram, recipe).query, schema, { now }).metrics[0].buckets[0].distribution;
  assert.equal(distribution.n, 4);
  assert.equal(distribution.edges[0], 40);
  assert.equal(distribution.edges.at(-1), 100);
});

test("the writing-volume destination exposes daily trends", () => {
  assert.ok(recipeById("human-word-total").measures.some(metric => metric.groupBy[0] === "day"));
});

test("daily message and conversation cards are trailing seven-day trends of the writing population", () => {
  for (const [id, metric, op] of [["human-messages-per-day", "human_daily_messages", "count"], ["human-conversations-per-day", "human_daily_conversations", "count_distinct"]]) {
    const recipe = recipeById(id), daily = recipe.measures.find(item => item.id === metric);
    assert.equal(recipe.category, "human");
    assert.equal(recipe.trend.value.op, op);
    assert.equal(recipe.trend.lookback, 7);
    assert.deepEqual(daily.groupBy, ["day"]);
    assert.deepEqual(recipe.where, [{ field: "typed_words", op: ">", value: "0" }], "automated-only messages are not counted");
  }
  assert.deepEqual(recipeById("human-conversations-per-day").trend.count, recipeById("human-conversations-per-day").trend.value, "the sample counts conversation-days, not messages");
  const days = rows => core.evaluateMetrics(rows, { ...core.EMPTY_QUERY, where: recipeById("human-conversations-per-day").where, aggregations: recipeById("human-conversations-per-day").measures }, schema, { now }).metrics[0].buckets;
  const sameDay = [row("a1", 10, "a", "w1"), row("a2", 30, "a", "w1"), row("b", 5, "b", "w1"), row("auto", 0, "auto", "w1")];
  assert.deepEqual(days(sameDay).map(bucket => bucket.value), [2], "two messages in one conversation are one conversation that day; automated-only messages do not count");
});

test("repository pie preserves the full word denominator beyond the returned group limit", () => {
  const { recipe, query } = card("explaining-repos");
  const cover = cardCoverQuery(query, recipe);
  const metric = cover.query.aggregations.find(metric => metric.id === cover.metricId);
  const rows = Array.from({ length: 15 }, (_, index) => row(String(index), (index + 1) * 10, String(index), String(index), `repo-${index}`));
  const result = core.evaluateMetrics(rows, cover.query, schema, { now }).metrics.find(metric => metric.id === cover.metricId);
  assert.equal(result.coverage, "exact");
  assert.ok(result.buckets.length < result.groupCount, "exercise an exact remainder outside the returned groups");
  const named = result.buckets.slice(0, metric.display.maxGroups);
  const other = result.buckets.slice(metric.display.maxGroups).reduce((sum, bucket) => sum + bucket.value, 0) + result.other.value;
  const total = named.reduce((sum, bucket) => sum + bucket.value, 0) + other;
  assert.equal(named.length, 7);
  assert.equal(total, rows.reduce((sum, row) => sum + row.typed_words, 0));
  assert.equal(total, 1200);
  assert.ok(other > result.other.value, "Other includes both unnamed returned groups and the exact server remainder");
});
