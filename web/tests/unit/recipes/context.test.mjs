import test from "node:test";
import assert from "node:assert/strict";
import { cardCoverQuery, recipeHarness } from "../helpers/recipes.mjs";
import { filterBoxSamples } from "../../../src/compact-box-model";
import { compactPieSlices } from "../../../src/compact-pie-model";
const now = Date.parse("2026-10-15T12:00:00Z"), { card, table: destination, metric } = recipeHarness(now);
const usage = (id, extra = {}) => ({ id, first_usage_at: now - 1000, day: "2026-10-15", provider: "one", model_family: "alpha", repository_name: "repo", input_tokens: 100, uncached_input_tokens: 20, cache_read_input_tokens: 60, cache_creation_input_tokens: 20, root_provider: "one", root_model_family: "alpha", root_conversation_id: "root", ...extra });
const call = (id, extra = {}) => ({ id, started_at: now - 1000, tool_name: "Read", tool_category: "read", repository_name: "repo", repo_path: "src/main.ts", conversation_id: "one", result_tokens: 100, result_tokens_source: "measured", carried_tokens: 300, truncated: false, ...extra });

test("cache shares weight all input, including cache writes, without dropping zero-reuse groups", () => {
  const rows = [usage("a"), usage("b", { input_tokens: 900, cache_read_input_tokens: 0, uncached_input_tokens: 600, cache_creation_input_tokens: 300 }), usage("zero", { model_family: "zero", cache_read_input_tokens: 0 }), usage("other-provider", { provider: "two" }), usage("missing", { input_tokens: null, cache_read_input_tokens: 999 }), usage("empty", { input_tokens: 0, cache_read_input_tokens: 999 }), usage("old", { first_usage_at: now - 8 * 86400000, cache_read_input_tokens: 999 })];
  const model = destination("cache-efficiency", rows), alpha = model.find(row => row.provider === "one" && row.model_family === "alpha");
  assert.equal(alpha.cc_share, .06, "ratio of sums, not the .3 average of fact rates");
  assert.equal(alpha.cc_input, 1000);
  assert.equal(alpha.cc_reads + alpha.cc_fresh + alpha.cc_writes, alpha.cc_input);
  assert.equal(model.find(row => row.model_family === "zero").cc_share, 0);
  assert.equal(model.find(row => row.provider === "two").cc_share, .6);
  const repo = destination("cache-repositories", rows)[0];
  assert.equal(repo.cc_share, 120 / 1200);
  for (const id of ["cache-efficiency", "cache-repositories"]) {
    const { recipe, query } = card(id), table = destination(id, rows), chart = metric(id, rows);
    for (const bucket of chart.buckets) assert.equal(bucket.value, table.find(row => JSON.stringify(query.grouping.keys.map(key => row[key])) === JSON.stringify(bucket.keys)).cc_share);
    assert.equal(recipe.exploration.unit, "percent");
  }
});

test("tool totals conserve exact Other and never blend attribution sources", () => {
  const rows = Array.from({length: 20}, (_, i) => call(String(i), { tool_name: `tool-${i}`, result_tokens_source: i % 2 ? "estimated" : "measured", result_tokens: i + 1 }));
  rows.push(call("zero", { result_tokens: 0 }), call("old", { started_at: now - 8 * 86400000 }));
  const pie = compactPieSlices(metric("large-outputs", rows), 5);
  assert.equal(pie.slices.length, 6);
  assert.equal(pie.total, 210);
  assert.equal(pie.slices.reduce((sum, slice) => sum + slice.value, 0), 210);
  assert.equal(pie.slices.at(-1).other, true);
  assert.equal(destination("large-outputs", rows).length, 20);
  const {recipe, query} = card("large-outputs");
  assert.equal(cardCoverQuery(query, recipe).query.aggregations[0].groupLimit, 10000, "fold Other from complete tool totals even if a host omits the remainder");
});

test("size boxes filter nonempty calls before cohort thresholds, fetching beyond the visible budget", () => {
  const rows = [0, 0, 10, 100, 1000].map((n, i) => call(String(i), { result_tokens: n }));
  rows.push(call("estimated-1", { result_tokens_source: "estimated" }), call("estimated-2", { result_tokens_source: "estimated" }));
  const result = filterBoxSamples(metric("output-distribution", rows), 3);
  assert.equal(result.buckets.length, 1);
  assert.equal(result.buckets[0].distribution.summary.n, 3);
  assert.equal(result.buckets[0].distribution.summary.median, 100);
  assert.equal(result.buckets[0].distribution.summary.mean, 370);
  assert.equal(destination("output-distribution", rows).length, 2, "small cohorts stay inspectable");
  const { recipe, query } = card("output-distribution"), clause = cardCoverQuery(query, recipe).query.aggregations[0];
  assert.equal(clause.groupLimit, 10000);
  assert.equal(clause.display.xLabel, "Result tokens");
});

test("truncation divides by all calls and applies its sample cutoff after counting", () => {
  const rows = Array.from({length: 10}, (_, i) => call(String(i), { truncated: i < 2 ? true : i === 2 ? null : false, result_tokens: 0 }));
  rows.push(...Array.from({length: 9}, (_, i) => call(`sparse-${i}`, { tool_name: "sparse", truncated: true })));
  const table = destination("truncated-outputs", rows);
  assert.equal(table.length, 1);
  assert.equal(table[0].cc_truncation_rate, .2);
  assert.equal(table[0].cc_truncated, 2);
  assert.equal(table[0].cc_calls, 10);
  assert.equal(metric("truncated-outputs", rows).buckets[0].value, .2);
});

test("path popularity and within-conversation rereads use distinct grains", () => {
  const rows = [call("a"), call("b"), call("c", { conversation_id: "two" }), call("other-repo", { repository_name: "another" }), call("missing-conversation", { conversation_id: null }), call("missing-path", { repo_path: null }), call("edit", { tool_category: "edit" })];
  const popular = destination("read-paths", rows), repeated = destination("repeated-reads", rows);
  assert.equal(popular.find(row => row.repository_name === "repo").cc_calls, 4);
  assert.equal(popular.find(row => row.repository_name === "repo").cc_conversations, 2);
  assert.equal(popular.length, 2, "repositories with the same path stay separate");
  assert.equal(repeated.length, 1);
  assert.equal(repeated[0].cc_calls, 2);
  assert.equal(repeated[0].conversation_id, "one");
});
