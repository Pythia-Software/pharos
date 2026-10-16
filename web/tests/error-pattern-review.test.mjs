import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { build } from "esbuild";
import * as core from "@pythia-software/query-table-core";
const built = await build({ entryPoints: ["src/curiosity-data.ts", "src/card-cover.ts", "src/error-pattern-review-model.ts", "src/compact-box-model.ts", "src/compact-pie-model.ts"], bundle: true, write: false, platform: "node", format: "esm", outdir: "out" });
const mod = name => import(`data:text/javascript;base64,${Buffer.from(built.outputFiles.find(file => file.path.endsWith(`/${name}.js`)).text).toString("base64")}`);
const { queryRecipes, instantiateRecipe } = await mod("curiosity-data"), { cardCoverQuery } = await mod("card-cover");
const { errorPatternTheses, errorPatternFeedback, errorPatternReviewKey } = await mod("error-pattern-review-model");
const { filterBoxSamples } = await mod("compact-box-model"), { compactPieSlices } = await mod("compact-pie-model");
const schemas = Object.fromEntries(["tool_calls", "skill_usages"].map(name => [name, core.loadSchema(JSON.parse(readFileSync(`../schemas/${name}.schema.json`, "utf8")))]));
const now = Date.parse("2026-10-15T12:00:00Z");
const capabilities = { version: 3, profile: "qt-sqlite-v1", groupedRows: true, calendarGrouping: true, computedSourceFilters: true, maxRows: 10000, maxGroups: 10000, maxPopulation: 250000 }, execution = { profile: "qt-sqlite-v1", fields: {}, resolvedRevisions: {}, planToken: "" };
const call = (id, extra = {}) => ({ id, started_at: now - 1000, tool_name: "Bash", tool_category: "command", command: "npm test", command_name: "npm test", command_category: "test", repository_name: "repo", repo_path: "src/main.ts", conversation_id: "one", call_count: 1, error_count: 1, status: "error", error_type: "nonzero_exit", error_signature: "command failed", test_failure: false, ...extra });
function card(id, days = 7) { const recipe = queryRecipes.find(recipe => recipe.id === id); return { recipe, query: instantiateRecipe(recipe, days, new Date(now)).query }; }
function destination(id, rows) { const { recipe, query } = card(id); return core.applyQuery(rows, query, schemas[recipe.dataset], { now }).rows; }
function metric(id, rows) { const { recipe, query } = card(id), cover = cardCoverQuery(query, recipe); return core.evaluateMetrics(rows, cover.query, schemas[recipe.dataset], { now }).metrics.find(metric => metric.id === cover.metricId); }

test("error study includes every active error definition exactly once and compiles complete destinations/covers", () => {
  const ids = errorPatternTheses.flatMap(thesis => thesis.cards.map(card => card.id));
  assert.equal(new Set(ids).size, 9);
  assert.deepEqual([...ids].sort(), queryRecipes.filter(recipe => recipe.category === "errors" && !recipe.retired).map(recipe => recipe.id).sort());
  for (const id of ids) {
    const { recipe, query } = card(id, 30);
    for (const state of [query, cardCoverQuery(query, recipe).query]) assert.deepEqual(core.toQueryRequestV3(state, schemas[recipe.dataset], capabilities, execution, { now, rows: false }).diagnostics, [], id);
    const item = instantiateRecipe(recipe, 30);
    assert.ok(item.description.includes("30"), id);
    assert.ok(!/[?{}]/.test(item.title), id);
    assert.ok(item.action, id);
  }
  for (const id of ["repeated-failed-commands", "environment-failures", "skill-load-failures"]) {
    const {recipe, query} = card(id);
    assert.equal(recipe.retired, true, id);
    for (const state of [query, cardCoverQuery(query, recipe).query]) assert.deepEqual(core.toQueryRequestV3(state, schemas[recipe.dataset], capabilities, execution, {now, rows:false}).diagnostics, [], `${id} remains executable for saved cards`);
  }
});

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

test("repeated commands stay within one agent conversation and include the first error", () => {
  const rows = [call("a", {command_category: "run"}), call("b", {command_category: "run"}), call("success", {error_count: 0, status: "ok"}), call("other-conversation", {conversation_id: "two"}), call("other-repo", {repository_name: "other"}), call("variant", {command: "npm  test"}), call("missing", {conversation_id: null}), call("blank", {command: ""}), call("missing-command", {command: null}), call("read", {tool_category: "read", command_category: "read"}), call("old", {started_at: now - 8 * 86400000})];
  const table = destination("repeated-failed-commands", rows);
  assert.equal(table.length, 1);
  assert.deepEqual([table[0].repository_name, table[0].conversation_id, table[0].command, table[0].card_count], ["repo", "one", "npm test", 2]);
  assert.equal(metric("repeated-failed-commands", rows).buckets[0].value, 2);
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

test("setup and access selection uses an OR without counting overlapping calls twice", () => {
  const rows = [call("both", {command_category: "build", error_type: "permission_denied"}), call("package", {command_category: "package"}), call("access", {command_category: "read", error_type: "file_not_found"}), call("other", {command_category: "test", error_type: "nonzero_exit"}), call("success", {command_category: "build", error_count: 0, status: "ok"})];
  assert.equal(destination("environment-failures", rows).reduce((sum, row) => sum + row.card_count, 0), 3);
});

test("skill errors count parent calls once per skill/provider while retaining evidence totals", () => {
  const fact = (id, extra={}) => ({id,created_at:now-1000,status:"error",provider:"one",skill_name:"review",tool_call_id:"call",conversation_id:"one",...extra});
  const rows = [fact("a"),fact("duplicate"),fact("b",{tool_call_id:"other-call"}),fact("other-skill",{skill_name:"debug"}),fact("other-provider",{provider:"two"}),fact("missing",{tool_call_id:null}),fact("blank",{tool_call_id:""}),fact("success",{status:"ok"})];
  const table = destination("skill-load-failures", rows), review = table.find(row => row.provider === "one" && row.skill_name === "review");
  assert.equal(review.card_tool_call_id, 2);
  assert.equal(review.card_count, 3);
  assert.equal(table.length, 3);
  assert.equal(table.reduce((sum,row)=>sum+row.card_tool_call_id,0),4, "a shared parent can legitimately appear under different skills/providers");
});

test("error feedback is independently keyed, validates IDs, and exports card/group/overall feedback", () => {
  assert.equal(errorPatternReviewKey, "pharos-error-pattern-review-v2");
  const draft = errorPatternFeedback.readReviewFeedback(JSON.stringify({overall:"Keep the denominators.",theses:{attempts:"Inspect the calls."},cards:{"conversation-error-counts":{verdict:"change",note:"Show the range."},"repeated-failed-commands":{verdict:"drop",note:"Earlier review"},"cache-efficiency":{verdict:"keep",note:"Other study"}}}));
  assert.equal(draft.cards["cache-efficiency"],undefined);
  assert.equal(draft.cards["repeated-failed-commands"],undefined);
  assert.equal(errorPatternFeedback.reviewedCardCount(draft),1);
  const report=errorPatternFeedback.exportFeedback(draft,30,{"conversation-error-counts":"Failures per conversation"});
  for (const text of ["Error patterns cards — review 2","past 30 days","Keep the denominators.","Inspect the calls.","Failures per conversation (conversation-error-counts)","Verdict: change","Show the range.","no-result","three affected conversations"]) assert.ok(report.includes(text),text);
});
