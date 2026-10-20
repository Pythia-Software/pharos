import test from "node:test";
import assert from "node:assert/strict";
import { cardCoverQuery, recipeHarness } from "../helpers/recipes.mjs";
import { filterBoxSamples } from "../../../src/compact-box-model";
import { compactPieSlices } from "../../../src/compact-pie-model";
const now = Date.parse("2026-10-15T12:00:00Z"), { card, table: destination, metric } = recipeHarness(now);
const call = (id, extra = {}) => ({ id, started_at: now - 1000, tool_name: "Bash", tool_category: "command", command: "npm test", command_name: "npm test", command_category: "test", repository_name: "repo", repo_path: "src/main.ts", conversation_id: "one", call_count: 1, error_count: 1, status: "error", error_type: "nonzero_exit", error_signature: "command failed", test_failure: false, ...extra });

test("tool rates retain successes and no-result calls, and enforce ten calls before ranking", () => {
  const rows = Array.from({length: 10}, (_, i) => call(String(i), { error_count: i < 2 ? 1 : 0, status: i < 2 ? "error" : i === 2 ? "no_result" : "ok", error_type: i === 0 ? "user_rejected" : "nonzero_exit" }));
  rows.push(...Array.from({length: 9}, (_, i) => call(`sparse-${i}`, {tool_name: "Sparse"})));
  rows.push(...Array.from({length: 10}, (_, i) => call(`clean-${i}`, {tool_name: "Clean", repository_name: "clean", error_count: 0, status: "ok"})));
  rows.push(call("old", {started_at: now - 8 * 86400000}));
  const table = destination("tool-failure-rates", rows);
  assert.equal(table.length, 1);
  assert.equal(table[0].card_call_count, 10);
  assert.equal(table[0].card_error_count, 2, "recorded user rejection is included without implying a mistake");
  assert.equal(table[0].card_rate, .2);
  assert.equal(metric("tool-failure-rates", rows).buckets[0].value, .2);
  const repo = destination("repository-failure-rates", rows);
  assert.equal(repo.find(row => row.repository_name === "repo").card_rate, 11 / 19);
  assert.equal(repo.find(row => row.repository_name === "clean").card_rate, 0, "zero-error repositories remain eligible");
});

test("volume pie counts errors, not all calls, and folds a complete Other", () => {
  const rows = Array.from({length: 20}, (_, i) => call(String(i), {tool_name: `Tool-${i}`}));
  rows.push(call("successful", {error_count: 0, status: "ok"}));
  const result = metric("tool-failures", rows), pie = compactPieSlices(result, 5);
  assert.equal(pie.slices.length, 6);
  assert.equal(pie.total, 20);
  assert.equal(pie.slices.reduce((sum, slice) => sum + slice.value, 0), 20);
  assert.equal(pie.slices.at(-1).value, 15);
  const {recipe, query} = card("tool-failures");
  assert.equal(cardCoverQuery(query, recipe).query.aggregations[0].groupLimit, 10000);
});

test("signature recurrence, repository reach, and local hotspots use different grains", () => {
  const rows = [call("a"), call("b"), call("c", {repository_name: "other", conversation_id: "two"}), call("d", {repository_name: null, conversation_id: null}), call("local-a", {error_signature: "local"}), call("local-b", {error_signature: "local"}), call("blank", {error_signature: ""}), call("missing", {error_signature: null}), call("flag-only", {error_count: 0, status: "ok", test_failure: true})];
  const repeated = destination("error-signatures", rows);
  assert.equal(repeated.length, 2);
  assert.equal(repeated.find(row => row.error_signature === "command failed").card_count, 4);
  const reach = destination("error-reach", rows);
  assert.equal(reach.length, 1);
  assert.equal(reach[0].card_repository_name, 2, "null repository does not count as a named repository");
  assert.equal(reach[0].card_conversation_id, 2);
  const hotspots = destination("cross-repo-errors", rows);
  assert.equal(hotspots.find(row => row.repository_name === "repo" && row.error_signature === "command failed").card_count, 2);
  assert.equal(hotspots.length, 4);
});

test("conversation boxes condition on errors, share tool cohorts, and filter samples before display limits", () => {
  const rows = [1,2,4].flatMap((n, c) => Array.from({length:n}, (_, i) => call(`${c}-${i}`, {conversation_id: `c-${c}`})));
  rows.push(...Array.from({length: 20}, (_, i) => call(`success-${i}`, {conversation_id: `success-${i}`, error_count: 0, status: "ok"})));
  rows.push(call("missing", {conversation_id: null}), call("blank", {conversation_id: ""}), call("small-a", {tool_name: "Small", conversation_id: "small-a"}), call("small-b", {tool_name: "Small", conversation_id: "small-b"}));
  const raw = metric("conversation-error-counts", rows), result = filterBoxSamples(raw, 3), table = destination("conversation-error-counts", rows);
  assert.equal(table.length, 5, "small tools remain inspectable");
  assert.equal(result.buckets.length, 1);
  const summary = result.buckets[0].distribution.summary;
  assert.deepEqual([summary.n, summary.min, summary.median, summary.max], [3,1,2,4]);
  assert.ok(Math.abs(summary.mean - 7 / 3) < 1e-12);
  const {recipe, query} = card("conversation-error-counts");
  const clause = cardCoverQuery(query, recipe).query.aggregations[0];
  assert.equal(clause.groupLimit, 10000);
  assert.equal(clause.display.xScale.mode, "log");
  assert.equal(recipe.cover.density, "compact");
});

test("test signals remain independent of tool error status and edit paths preserve repository identity", () => {
  const rows = [call("flag", {error_count: 0, status: "ok", test_failure: true}), call("error", {test_failure: false}), call("edit-a", {tool_category: "edit"}), call("edit-b", {tool_category: "edit", repository_name: "other"}), call("unmapped", {tool_category: "edit", repo_path: null}), call("blank-path", {tool_category: "edit", repo_path: ""})];
  const tests = destination("test-failures", rows), edits = destination("edit-failures", rows);
  assert.equal(tests.length, 1);
  assert.equal(tests[0].card_count, 1, "a successful tool call can still contain a test-failure signal");
  assert.equal(edits.length, 2);
  assert.ok(edits.every(row => row.card_count === 1));
  assert.deepEqual(edits.map(row => row.repository_name).sort(), ["other", "repo"]);
});
