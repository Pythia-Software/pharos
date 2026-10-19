import test from "node:test";
import assert from "node:assert/strict";
import * as core from "@pythia-software/query-table-core";
import {schemas} from "../helpers/recipes.mjs";
import { queryRecipes, instantiateRecipe, suggestionPool } from "../../../src/curiosity-data";
import { cardCoverQuery } from "../../../src/card-cover";
import { humanWordTheses, readReviewFeedback, exportHumanWordFeedback, reviewedCardCount } from "../../../src/human-word-review-model";

const schema = schemas.writing_messages;
const now = Date.parse("2026-10-15T12:00:00Z");
const capabilities = { version: 3, profile: "qt-sqlite-v1", groupedRows: true, calendarGrouping: true, computedSourceFilters: true, regex: false, maxRows: 10000, maxGroups: 10000, maxPopulation: 250000 };
const execution = { profile: "qt-sqlite-v1", fields: {}, resolvedRevisions: {}, planToken: "" };
const row = (id, typed_words, conversation_id, work_id, repository_name = "one") => ({ id, typed_words, conversation_id, work_id, repository_name, title: "Same title", words: typed_words + 100, sent_at: now - 1000 });
const population = [row("a1", 10, "a", "w1"), row("a2", 30, "a", "w1"), row("b", 100, "b", "w1"), row("c", 60, "c", "w2", "two"), row("d", 50, "d", null, "two"), row("auto", 0, "auto", "w3"), { ...row("old", 10000, "old", "old"), sent_at: now - 8 * 86400000 }];
function card(id) {
  const recipe = queryRecipes.find(recipe => recipe.id === id);
  return { recipe, query: instantiateRecipe(recipe, 7, new Date(now)).query };
}
function value(id, metricId, rows = population) {
  const { recipe, query } = card(id);
  const cover = cardCoverQuery(query, recipe);
  return core.evaluateMetrics(rows, cover.query, schema, { now }).metrics.find(metric => metric.id === metricId).buckets[0]?.value;
}

test("the review includes every human card exactly once and every cover compiles", () => {
  const ids = humanWordTheses.flatMap(thesis => thesis.cards.map(card => card.id));
  assert.equal(new Set(ids).size, 12);
  assert.deepEqual([...ids].sort(), queryRecipes.filter(recipe => recipe.category === "human" && !recipe.retired).map(recipe => recipe.id).sort());
  for (const id of ids) {
    const { recipe, query } = card(id);
    const cover = cardCoverQuery(query, recipe);
    assert.deepEqual(core.toQueryRequestV3(cover.query, schema, capabilities, execution, { now, rows: false }).diagnostics, [], id);
  }
});

test("retired cards stay resolvable but leave suggestions, and active human titles are descriptive", () => {
  const retired = ["work-writing", "human-conversation-messages", "conversation-writing", "human-action-phrases", "human-contribution-patterns"];
  assert.ok(retired.every(id => queryRecipes.find(recipe => recipe.id === id)?.retired));
  assert.ok(suggestionPool(new Date(now)).every(recipe => !retired.includes(recipe.id)));
  assert.ok(queryRecipes.filter(recipe => recipe.category === "human" && !recipe.retired).every(recipe => !recipe.title.includes("?")));
  const writing = queryRecipes.find(recipe => recipe.id === "human-words");
  assert.equal(instantiateRecipe(writing, 30).title, "Last 30 days of writing");
});

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

test("retired conversation counts preserve identities and distribution samples are conversation word totals", () => {
  const { query } = card("human-conversation-messages");
  const result = core.applyQuery(population, query, schema, { now });
  assert.equal(result.total, 4, "shared titles do not merge conversations");
  assert.equal(result.rows[0].human_messages, 2);
  assert.equal(result.rows[0].human_words, 40);
  const { recipe, query: histogram } = card("human-message-lengths");
  const distribution = core.evaluateMetrics(population, cardCoverQuery(histogram, recipe).query, schema, { now }).metrics[0].buckets[0].distribution;
  assert.equal(distribution.n, 4);
  assert.equal(distribution.edges[0], 40);
  assert.equal(distribution.edges.at(-1), 100);
});

test("review drafts tolerate invalid storage and export every feedback level with stable IDs", () => {
  assert.deepEqual(readReviewFeedback("not json"), { overall: "", theses: {}, cards: {} });
  const feedback = readReviewFeedback(JSON.stringify({ overall: "Keep the set small.", theses: { depth: "Conversation is the useful denominator." }, cards: { "human-words-per-message": { verdict: "drop", note: "Not useful to me." }, "human-word-total": { verdict: "wrong", note: 42 }, unknown: { verdict: "keep", note: "ignore" } } }));
  assert.equal(reviewedCardCount(feedback), 1);
  assert.equal(feedback.cards.unknown, undefined);
  assert.equal(feedback.cards["human-word-total"].verdict, undefined);
  assert.deepEqual(readReviewFeedback(JSON.stringify(feedback)), feedback);
  const report = exportHumanWordFeedback(feedback, 30, { "human-words-per-message": "Average message length" });
  for (const expected of ["past 30 days", "Keep the set small.", "Conversation is the useful denominator.", "Average message length (human-words-per-message)", "Verdict: drop", "Not useful to me."]) assert.ok(report.includes(expected), expected);
  assert.ok(!report.includes("unknown"));
});

test("human card copy identifies the selected window and destinations", () => {
  for (const recipe of queryRecipes.filter(recipe => recipe.category === "human" && !recipe.retired)) {
    const item = instantiateRecipe(recipe, 30);
    assert.ok(item.description.includes("30"), recipe.id);
    assert.ok(!/sparkline|in this window|selected window/i.test(item.description), recipe.id);
    assert.ok(!item.description.includes("{days}"), recipe.id);
    assert.ok(item.action, recipe.id);
  }
  assert.equal(instantiateRecipe(queryRecipes.find(recipe => recipe.id === "human-word-total"), 7).title, "Weekly writing volume over time");
  assert.equal(queryRecipes.find(recipe => recipe.id === "human-word-total").action, "Explore daily writing trends");
  assert.equal(queryRecipes.find(recipe => recipe.id === "explaining-repos").cover.maxGroups, 7);
  assert.equal(queryRecipes.find(recipe => recipe.id === "explaining-repos").action, "See breakdown by repo");
  assert.equal(queryRecipes.find(recipe => recipe.id === "human-word-mix").cover.maxGroups, 8);
  assert.ok(queryRecipes.find(recipe => recipe.id === "human-word-total").measures.some(metric => metric.groupBy[0] === "day"), "volume destination exposes actual daily trends");
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
