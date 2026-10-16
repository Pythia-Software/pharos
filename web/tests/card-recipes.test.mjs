import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { build } from "esbuild";
import * as core from "@pythia-software/query-table-core";

async function moduleOf(entry) {
  const bundle = await build({ entryPoints: [entry], bundle: true, write: false, platform: "node", format: "esm" });
  return import(`data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString("base64")}`);
}
const { queryRecipes, instantiateRecipe, loadQueryPreview, clearPreviewCache } = await moduleOf("src/curiosity-data.ts");
const { groupSourceQuery } = await moduleOf("src/group-drilldown.ts");
const { queryFromURL, queryHasTimeFilter, resolveRollingQuery } = await moduleOf("src/query-links.ts");
const { queryURL } = await moduleOf("src/query-links.ts");
const { migrateSavedQueries, updateBookmark, replaceSavedQuery } = await moduleOf("src/bookmarks.ts");
const { cardCoverQuery, defaultCardCover } = await moduleOf("src/card-cover.ts");
const { categoryForQuery } = await moduleOf("src/query-categories.ts");
const schemas = Object.fromEntries([...new Set(queryRecipes.map(r => r.dataset))].map(dataset => [dataset, core.loadSchema(JSON.parse(readFileSync(`../schemas/${dataset}.schema.json`, "utf8")))]));
const execution = { profile: "qt-sqlite-v1", fields: {}, resolvedRevisions: {}, planToken: "" };
const capabilities = { version: 3, profile: execution.profile, groupedRows: true, calendarGrouping: true, computedSourceFilters: true, regex: false, maxRows: 10000, maxGroups: 10000, maxPopulation: 250000 };
const clock = new Date(2026, 9, 13, 12);

test("cover visibility settings survive cover queries while destination data remains unchanged", () => {
  for (const id of ["cost-repos", "human-words"]) {
    const recipe = queryRecipes.find(recipe => recipe.id === id), item = instantiateRecipe(recipe, 7, clock);
    const before = structuredClone(item.query), cover = { ...defaultCardCover(recipe), maxGroups: 2, showOther: false, showLegend: false };
    const preview = cardCoverQuery(item.query, recipe, cover);
    const metric = preview.query.aggregations.find(metric => metric.id === preview.metricId);
    assert.equal(metric.display.maxGroups, 2);
    assert.equal(metric.display.showOther, false);
    assert.equal(metric.display.legendPosition, "none");
    assert.deepEqual(item.query, before);
  }
});

test("all card destinations compile and retain their source metrics and rolling dates", () => {
  for (const recipe of queryRecipes) {
    const suggestion = instantiateRecipe(recipe, 7, clock);
    if (!suggestion.query.grouping) continue;
    const { grouping } = suggestion.query;
    assert.deepEqual(grouping.source.aggregations, recipe.measures, recipe.id);
    assert.deepEqual(grouping.source.where.filter(term => term.field === recipe.field).map(term => term.value), ["-7d", "+0s"], recipe.id);
    const cover = cardCoverQuery(suggestion.query, recipe);
    const coverRequest = core.toQueryRequestV3(cover.query, schemas[recipe.dataset], capabilities, execution, { now: clock.getTime(), rows: false });
    assert.deepEqual(coverRequest.diagnostics, [], `${recipe.id} cover`);
    assert.deepEqual(suggestion.query.where, recipe.exploration.having ?? [], recipe.id);
    const request = core.toQueryRequestV3(suggestion.query, schemas[recipe.dataset], capabilities, execution, { now: clock.getTime(), rows: true });
    assert.deepEqual(request.diagnostics, [], recipe.id);
    const restored = core.decodeQueryStrict(new URL(suggestion.url, "http://home.test").searchParams.get(`q_${recipe.dataset}`));
    assert.deepEqual(restored, core.normalizeQueryState(suggestion.query), recipe.id);
    assert.equal(categoryForQuery(recipe.dataset, suggestion.query), categoryForQuery(recipe.dataset, grouping.source));
  }
});

test("failure-rate cover and destination agree, include successful calls and expose sample size", async t => {
  clearPreviewCache();
  const priorFetch = global.fetch, priorLocation = global.location;
  global.location = { origin: "http://home.test" };
  t.after(() => { global.fetch = priorFetch; global.location = priorLocation; clearPreviewCache(); });
  const requests = [];
  const rows = Array.from({ length: 12 }, (_, i) => ({ id: String(i), tool_name: i < 10 ? "Bash" : "Read", started_at: Date.now() - 1000, error_count: i < 2 ? 1 : 0, call_count: 1, conversation_id: String(i) }));
  global.fetch = async (url, init) => {
    const reply = body => new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
    if (url.includes("computed-columns")) return reply([]);
    if (url.endsWith("capabilities")) return reply({ queryCapabilities: capabilities, computedExecution: execution, metricCapabilities: { version: 2 } });
    const request = JSON.parse(init.body); requests.push(request);
    const query = { ...core.EMPTY_QUERY, ...request.result, select: request.result.select.map(field => ({ field })), aggregations: request.metrics, grouping: { ...request.grouping, source: { ...core.EMPTY_QUERY, ...request.source, select: request.source.select.map(field => ({ field })) } } };
    const result = core.applyQuery(rows, query, schemas.tool_calls, { now: request.now });
    const prepared = core.prepareGrouping(rows, schemas.tool_calls, query.grouping, { now: request.now });
    return reply({ version: 3, now: request.now, sourceTotal: prepared.inputs.length, ...result, rows: request.rows ? result.rows : [], metrics: core.evaluateMetrics(rows, query, schemas.tool_calls, { now: request.now }).metrics, execution, schema: { name: prepared.schema.name, idField: prepared.schema.idField, fields: prepared.schema.fields.map(({ name, type }) => ({ name, type })) } });
  };
  const recipe = queryRecipes.find(r => r.id === "tool-failure-rates"), suggestion = instantiateRecipe(recipe, 7);
  const preview = await loadQueryPreview(recipe.dataset, suggestion.query, schemas.tool_calls, { recipe, window: suggestion.window, suggested: true });
  assert.equal(preview.state, "ready");
  assert.equal(preview.metrics[0].result.buckets[0].value, 0.2);
  assert.deepEqual(preview.metrics[0].result.buckets[0].keys, ["Bash"]);
  assert.equal(preview.metrics[0].clause.display.kind, "bar-horizontal");
  assert.equal(requests[0].rows, false);
  assert.ok(!requests[0].source.where.some(term => term.field === "error_count"));
  assert.equal(preview.action, "Compare failure rates");
});

test("repeated-path HAVING excludes singleton reads and keeps repositories separate", () => {
  const recipe = queryRecipes.find(r => r.id === "repeated-reads"), { query } = instantiateRecipe(recipe, 7, clock);
  const rows = ["one", "one", "two"].map((repository_name, i) => ({ id: String(i), repository_name, repo_path: "src/main.go", tool_category: "read", started_at: clock.getTime() - 1000 }));
  const result = core.applyQuery(rows, query, schemas.tool_calls, { now: clock.getTime() });
  assert.equal(result.total, 1);
  assert.equal(result.rows[0].repository_name, "one");
  assert.equal(result.rows[0].card_count, 2);
});

test("group drilldown preserves source predicates and metrics, handles null keys and drops HAVING", () => {
  const recipe = queryRecipes.find(r => r.id === "repeated-reads"), { query } = instantiateRecipe(recipe, 7, clock);
  const records = groupSourceQuery(query, { repository_name: null, repo_path: "src/main.go" }, schemas.tool_calls);
  assert.equal(records.grouping, undefined);
  assert.equal(records.offset, 0);
  assert.deepEqual(records.aggregations, recipe.measures);
  assert.deepEqual(records.where, [...query.grouping.source.where, { field: "repository_name", op: "is_null", value: "" }, { field: "repo_path", op: "=", value: "src/main.go" }]);
  assert.throws(() => groupSourceQuery(query, { repo_path: "x" }, schemas.tool_calls), /Missing group key/);
});

test("cache efficiency is weighted by input tokens and preserves provider cohorts", () => {
  const { query } = instantiateRecipe(queryRecipes.find(r => r.id === "cache-efficiency"), 7, clock);
  const row = { first_usage_at: clock.getTime() - 1000, provider: "one", model_family: "model" };
  const result = core.applyQuery([
    { ...row, id: "a", cache_read_input_tokens: 90, uncached_input_tokens: 10, agent_session_id: "same" },
    { ...row, id: "b", cache_read_input_tokens: 0, uncached_input_tokens: 900, agent_session_id: "same" },
    { ...row, id: "c", provider: "two", cache_read_input_tokens: 100, uncached_input_tokens: 0, agent_session_id: "other" },
  ], query, schemas.usage, { now: clock.getTime() });
  assert.equal(result.total, 2);
  assert.equal(result.rows.find(r => r.provider === "one").card_rate, 0.09);
  assert.equal(result.rows.find(r => r.provider === "one").card_agent_session_id, 1);
});

test("work comparisons preserve workspace identity and take titles from the latest usage row", () => {
  const { query } = instantiateRecipe(queryRecipes.find(r => r.id === "usage-work"), 7, clock);
  const row = { first_usage_at: clock.getTime() - 1000, cost_usd: 1, total_tokens: 100, workspace_id: "one", agent_session_id: "agent" };
  const result = core.applyQuery([
    { ...row, id: "old", title: "Old title", last_usage_at: new Date(clock.getTime() - 100).toISOString() },
    { ...row, id: "new", title: "Current title", last_usage_at: clock.toISOString() },
    { ...row, id: "other", workspace_id: "two", title: "Current title", last_usage_at: clock.toISOString() },
  ], query, schemas.usage, { now: clock.getTime() });
  assert.equal(result.total, 2);
  assert.equal(result.rows[0].card_work_title, "Current title");
  assert.equal(result.rows[0].card_total_tokens, 200);
  assert.equal(result.rows[0].card_agent_session_id, 1);
});

test("relative card URLs stay identical as the clock advances and survive DST", () => {
  const recipe = queryRecipes.find(recipe => recipe.id === "tool-failure-rates");
  const first = instantiateRecipe(recipe, 7, new Date("2026-03-07T18:00:00Z"));
  const later = instantiateRecipe(recipe, 7, new Date("2026-03-15T18:00:00Z"));
  assert.equal(first.url, later.url);
  assert.ok(!decodeURIComponent(first.url).includes("2026-"));
  assert.deepEqual(resolveRollingQuery(first.query, first.window, new Date("2027-01-01T00:00:00Z")), first.query);
  const paged = { ...first.query, offset: 50 };
  assert.equal(resolveRollingQuery(paged, first.window), paged, "resolving an already-relative query must preserve result paging");
  const population = [
    { id: "old", tool_name: "Bash", started_at: Date.parse("2026-03-06T18:00:00Z"), call_count: 10, error_count: 1 },
    { id: "new", tool_name: "Read", started_at: Date.parse("2026-03-14T18:00:00Z"), call_count: 10, error_count: 1 },
  ];
  assert.equal(core.applyQuery(population, first.query, schemas.tool_calls, { now: Date.parse("2026-03-07T18:00:00Z") }).rows[0].tool_name, "Bash");
  assert.equal(core.applyQuery(population, first.query, schemas.tool_calls, { now: Date.parse("2026-03-15T18:00:00Z") }).rows[0].tool_name, "Read");
});

test("distribution covers retain source observations while their destination ranks grouped rows", () => {
  const recipe = queryRecipes.find(recipe => recipe.id === "duration-distribution");
  const destination = instantiateRecipe(recipe, 7).query;
  const cover = cardCoverQuery(destination, recipe);
  assert.equal(cover.query.grouping, undefined);
  assert.equal(cover.query.aggregations[0].distribution.kind, "box");
  assert.deepEqual(cover.query.where, destination.grouping.source.where);
  assert.equal(destination.grouping.keys[0], "command_name");
  assert.equal(destination.aggregations.length, 0);
});

test("choosing a source metric keeps its dimensions in the grouped destination", () => {
  const recipe = queryRecipes.find(recipe => recipe.id === "skill-evidence");
  const cover = { mode: "source", metricId: recipe.measures[1].id, kind: "bar-horizontal" };
  const edited = { ...recipe, cover };
  const destination = instantiateRecipe(edited, 7).query;
  assert.deepEqual(destination.grouping.keys, recipe.measures[1].groupBy);
  const preview = cardCoverQuery(destination, edited);
  assert.deepEqual(preview.query.aggregations.find(metric => metric.id === preview.metricId).groupBy, destination.grouping.keys);
  assert.deepEqual(destination.grouping.source.where.filter(term => term.field === "created_at").map(term => term.value), ["-7d", "+0s"]);
});

function bookmarkFixture(t, bookmarks) {
  const originalWindow = global.window, originalLocation = global.location;
  const values = new Map([["pharos-bookmarks-v1", bookmarks]]);
  global.location = { origin: "http://home.test" };
  global.window = { dispatchEvent() {}, pharosPrefs: {
    get: (key, fallback) => values.get(key) ?? fallback, set: (key, value) => values.set(key, value), remove: key => values.delete(key), keys: prefix => [...values.keys()].filter(key => key.startsWith(prefix)),
  } };
  t.after(() => { global.window = originalWindow; global.location = originalLocation; });
  return values;
}

test("extended bookmark windows round trip and all time clears rolling and fixed date bounds", t => {
  const recipe = queryRecipes.find(recipe => recipe.id === "tool-failures"), item = instantiateRecipe(recipe, 7);
  const values = bookmarkFixture(t, [{ id: "saved", savedQueryId: "original", title: item.title, url: `${item.url}&search=broken#tools`, query: item.query, templateId: item.instanceId }]);
  for (const days of [180, 365]) {
    const updated = updateBookmark("saved", item.title, "", days), spec = queryFromURL(updated.url, updated.query);
    assert.equal(spec.window.days, days);
    assert.equal(spec.window.mode, "relative");
    assert.deepEqual(spec.query.grouping.source.where.filter(term => term.field === recipe.field).map(term => term.value), [`-${days}d`, "+0s"]);
    assert.equal(updated.templateId, `${recipe.id}:${days}`);
  }
  const updated = updateBookmark("saved", item.title, "", null), spec = queryFromURL(updated.url, updated.query);
  assert.equal(spec.window, undefined);
  assert.equal(queryHasTimeFilter(spec.query, recipe.field), false);
  assert.deepEqual(spec.query.grouping.source.where, recipe.where);
  assert.deepEqual(spec.query.grouping.summaries, item.query.grouping.summaries);
  const url = new URL(updated.url, "http://home.test");
  for (const key of ["window_mode", "window_days", "window_field", "window_dataset"]) assert.equal(url.searchParams.has(key), false);
  assert.equal(url.searchParams.get("search"), "broken");
  assert.equal(url.hash, "#tools");
  assert.equal(updated.savedQueryId, "original");
  assert.equal(updated.templateId, `${recipe.id}:all`);
  assert.deepEqual(values.get("pharos-bookmarks-v1"), [updated]);

  const fixed = { ...core.EMPTY_QUERY, offset: 50, where: [
    { field: "started_at", op: ">=", value: "2026-01-01" },
    { field: "started_at", op: "<", value: "2026-02-01" },
    { field: "error_count", op: ">", value: "0" },
    { any: [{ field: "started_at", op: "<", value: "2026-01-15" }, { field: "tool_name", op: "=", value: "Bash" }] },
  ] };
  values.set("pharos-bookmarks-v1", [{ id: "fixed", title: "Fixed range", url: queryURL("tool_calls", fixed) }]);
  const cleared = queryFromURL(updateBookmark("fixed", "Fixed range", "", null).url);
  assert.deepEqual(cleared.query.where, fixed.where.slice(2), "compound logic and non-date predicates survive");
  assert.equal(cleared.query.offset, 0);
  assert.equal(queryHasTimeFilter(cleared.query, "started_at"), true, "compound date logic still counts as a saved range");

  const opaqueURL = "/tools?q_tool_calls=@existing-snapshot", unbounded = { ...core.EMPTY_QUERY, where: recipe.where };
  values.set("pharos-bookmarks-v1", [{ id: "opaque", title: "All history", url: opaqueURL, query: unbounded }]);
  const renamed = updateBookmark("opaque", "Renamed history", "", null);
  assert.equal(renamed.url, opaqueURL, "metadata edits keep an existing all-time snapshot link");
  assert.deepEqual(renamed.query, unbounded, "the snapshot's fallback query remains available");
});

test("rolling bookmark migration preserves IDs and fixed ranges while replacing hard endpoints", t => {
  const values = bookmarkFixture(t, []);
  const recipe = queryRecipes.find(recipe => recipe.id === "tool-failures");
  const suggestion = instantiateRecipe(recipe, 7);
  const calendar = { ...suggestion.window, mode: "calendar" };
  const query = resolveRollingQuery(suggestion.query, calendar, clock);
  const legacy = { id: "legacy", savedQueryId: "saved-id", title: "Rolling", url: queryURL(recipe.dataset, query, calendar) };
  const fixed = { id: "fixed", title: "Fixed", url: queryURL(recipe.dataset, query) };
  values.set("pharos-bookmarks-v1", [legacy, fixed]);
  migrateSavedQueries();
  const saved = values.get("pharos-bookmarks-v1");
  assert.equal(saved[0].savedQueryId, "saved-id");
  assert.equal(saved[0].id, "legacy");
  assert.deepEqual(queryFromURL(saved[0].url).query.grouping.source.where.filter(term => term.field === recipe.field).map(term => term.value), ["-7d", "+0s"]);
  assert.equal(saved[1].url, fixed.url);
});

test("editing a collected cover changes destination dimensions with it", t => {
  const recipe = queryRecipes.find(recipe => recipe.id === "skill-evidence");
  const item = instantiateRecipe(recipe, 7);
  bookmarkFixture(t, [{ id: "saved", title: item.title, url: item.url, query: item.query }]);
  const updated = updateBookmark("saved", item.title, item.description, undefined, recipe.measures[1].id);
  const spec = queryFromURL(updated.url, updated.query);
  assert.equal(updated.id, "saved");
  assert.equal(updated.cover.mode, "source");
  assert.deepEqual(spec.query.grouping.keys, recipe.measures[1].groupBy);
  assert.deepEqual(spec.query.grouping.source.where, item.query.grouping.source.where);
});

const { availableChartKinds } = await moduleOf("src/card-library-model.ts");
const { wishlistCardRecipes } = await moduleOf("src/wishlist-card-recipes.ts");
function executeCard(id, rows) {
  const recipe = queryRecipes.find(recipe => recipe.id === id);
  const suggestion = instantiateRecipe(recipe, 7, clock);
  return { recipe, suggestion, result: core.applyQuery(rows, suggestion.query, schemas[recipe.dataset], { now: clock.getTime() }) };
}
const recentMessage = { sent_at: clock.getTime() - 1000, repository_name: "one", title: "Work", other_words: 0, typed_words: 5 };

test("wishlist repeats require three distinct conversations and exclude machine/pasted text", () => {
  const base = { ...recentMessage, text: "Please show evidence", resent_words: 0, pasted_words: 0, quoted_words: 0, template_words: 0, attachment_words: 0, harness_words: 0, automated_words: 0 };
  const rows = Array.from({ length: 4 }, (_, i) => ({ ...base, id: String(i), conversation_id: `conversation-${i === 3 ? 2 : i}` }));
  rows.push({ ...base, id: "resent", conversation_id: "conversation-4", typed_words: 0, resent_words: 5 });
  rows.push({ ...base, id: "machine", conversation_id: "conversation-5", automated_words: 10 });
  rows.push({ ...base, id: "pasted", conversation_id: "conversation-6", pasted_words: 10 });
  rows.push(...Array.from({ length: 20 }, (_, i) => ({ ...base, id: `single-${i}`, text: "One conversation only", conversation_id: "single" })));
  const { result } = executeCard("repeated-messages", rows);
  assert.equal(result.total, 1);
  assert.equal(result.rows[0].card_conversation_id, 4);
  assert.equal(result.rows[0].card_count, 5);
});

test("conversation histogram reduces windowed message counts, not individual word values", () => {
  const rows = [
    { ...recentMessage, id: "a", conversation_id: "a", typed_words: 100 },
    { ...recentMessage, id: "b", conversation_id: "a", typed_words: 200 },
    { ...recentMessage, id: "c", conversation_id: "b", typed_words: 500 },
    { ...recentMessage, id: "old", conversation_id: "a", sent_at: clock.getTime() - 8 * 86400000 },
    { ...recentMessage, id: "automated", conversation_id: "a", typed_words: 0 },
  ];
  const { recipe, suggestion, result } = executeCard("conversation-writing", rows);
  assert.equal(result.total, 2);
  assert.deepEqual(result.rows.map(row => row.card_count), [2, 1]);
  const cover = cardCoverQuery(suggestion.query, recipe);
  const metrics = core.evaluateMetrics(rows, cover.query, schemas.writing_messages, { now: clock.getTime() }).metrics;
  assert.equal(metrics[0].buckets[0].distribution.n, 2);
  assert.equal(metrics[0].buckets[0].distribution.edges[0], 1);
  assert.equal(metrics[0].buckets[0].distribution.edges.at(-1), 2);
  assert.deepEqual(availableChartKinds(recipe), ["histogram"]);
});

test("legacy positive-feedback and reported-action candidates exclude nonhuman and mixed text", () => {
  for (const [id, text] of [["positive-feedback", "Great job, that worked"], ["human-action-phrases", "I fixed the parser"]]) {
    const base = { ...recentMessage, text, conversation_id: "a" };
    const { result } = executeCard(id, [
      { ...base, id: "human" },
      { ...base, id: "automated", typed_words: 0 },
      { ...base, id: "mixed", other_words: 10 },
    ]);
    assert.equal(result.total, 1, id);
    assert.equal(result.rows[0].card_count, 1, id);
  }
});

test("skill reloads count distinct successful file calls, excluding harness snapshots", () => {
  const base = { created_at: clock.getTime() - 1000, repository_name: "one", conversation_id: "a", skill_path: "skills/check/SKILL.md", status: "ok", evidence_type: "file_load", content_bytes: 100 };
  const { result } = executeCard("skill-reloads", [
    { ...base, id: "one", tool_call_id: "one" },
    { ...base, id: "two", tool_call_id: "two" },
    { ...base, id: "duplicate", tool_call_id: "two" },
    { ...base, id: "error", status: "error", tool_call_id: "three" },
    { ...base, id: "harness", evidence_type: "harness_load", tool_call_id: "four" },
    { ...base, id: "other-agent", conversation_id: "b", tool_call_id: "five" },
  ]);
  assert.equal(result.total, 1);
  assert.equal(result.rows[0].card_tool_call_id, 2);
});

test("zero-item search cards distinguish unknown result counts from explicit successful zeros", () => {
  const base = { called_at: clock.getTime() - 1000, status: "ok", tool_name: "search_work", result_count: 0 };
  const { result } = executeCard("empty-mcp-searches", [
    { ...base, id: 1 }, { ...base, id: 2, result_count: null },
    { ...base, id: 3, status: "error" }, { ...base, id: 4, tool_name: "get_work" },
    { ...base, id: 5, result_count: 2 },
  ]);
  assert.equal(result.total, 1);
  assert.equal(result.rows[0].card_count, 1);
});

test("candidate comparisons require mixed configurations and dispositions at the same candidate grain", () => {
  const base = { started_at: clock.getTime() - 1000, project: "one", task_id: "t", candidate_id: "c", execution_class: "test", shape_version: "v1", attempt_count: 1 };
  const { result } = executeCard("tl1-mixed-dispositions", [
    { ...base, id: "a", model_effort: "model a", disposition: "advanced" },
    { ...base, id: "b", model_effort: "model b", disposition: "error" },
    { ...base, id: "different-task", task_id: "another", model_effort: "model c", disposition: "escalated" },
    { ...base, id: "different-version", shape_version: "v2", model_effort: "model c", disposition: "escalated" },
    { ...base, id: "one-model-a", candidate_id: "single-model", model_effort: "model a", disposition: "advanced" },
    { ...base, id: "one-model-b", candidate_id: "single-model", model_effort: "model a", disposition: "error" },
  ]);
  assert.equal(result.total, 1);
  assert.equal(result.rows[0].card_attempt_count, 2);
  assert.equal(result.rows[0].card_disposition, 2);
});

test("work writing applies the window to constituent messages and preserves work identity", () => {
  const { result } = executeCard("work-writing", [
    { ...recentMessage, id: "a", work_id: "a", conversation_id: "a", typed_words: 20 },
    { ...recentMessage, id: "b", work_id: "b", conversation_id: "b", typed_words: 10 },
    { ...recentMessage, id: "old", work_id: "a", conversation_id: "a", typed_words: 1000, sent_at: clock.getTime() - 8 * 86400000 },
  ]);
  assert.equal(result.total, 2);
  assert.equal(result.rows[0].card_typed_words, 20);
  assert.equal(result.rows[1].card_typed_words, 10);
});

test("writing hours capture the device timezone in durable URLs and retain DST offsets", () => {
  const recipe = queryRecipes.find(recipe => recipe.id === "writing-hours");
  const suggestion = instantiateRecipe(recipe, 7, clock);
  assert.equal(suggestion.query.aggregations[0].dateGrouping.timeZone, Intl.DateTimeFormat().resolvedOptions().timeZone);
  const restored = core.decodeQueryStrict(new URL(suggestion.url, "http://home.test").searchParams.get("q_writing_messages"));
  assert.deepEqual(restored.aggregations[0].dateGrouping, suggestion.query.aggregations[0].dateGrouping);
  const query = { ...restored, where: [], aggregations: restored.aggregations.map(metric => ({ ...metric, dateGrouping: { ...metric.dateGrouping, timeZone: "America/Denver" } })) };
  const rows = ["2026-11-01T07:30:00Z", "2026-11-01T08:30:00Z"].map((sent_at, i) => ({ ...recentMessage, id: String(i), sent_at, typed_words: 10 }));
  const result = core.evaluateMetrics(rows, query, schemas.writing_messages).metrics[0];
  assert.equal(result.error, undefined);
  assert.equal(result.buckets.length, 2);
  assert.equal(new Set(result.buckets.map(bucket => bucket.keys[0])).size, 2, "repeated local hours must remain separate intervals");
});

test("every new wishlist recipe stays mapped to a real catalog idea", () => {
  const tracker = JSON.parse(readFileSync("../docs/homepage-query-cards.json", "utf8"));
  for (const recipe of wishlistCardRecipes) {
    const idea = tracker.cards.find(card => card.id === recipe.idea);
    assert.ok(idea, recipe.id);
    assert.ok(idea.implemented_recipes.includes(recipe.id), recipe.id);
    assert.equal(idea.capacity_audit.date, "2026-10-14", recipe.id);
  }
});


test("renaming a collected metric preserves its presentation settings", t => {
  const recipe = queryRecipes.find(recipe => recipe.id === "skill-evidence"), item = instantiateRecipe(recipe, 7);
  const cover = { ...defaultCardCover(recipe), kind: "bar-horizontal", maxGroups: 3, showOther: false, showLegend: false };
  bookmarkFixture(t, [{ id: "saved", title: item.title, url: item.url, query: item.query, cover }]);
  const updated = updateBookmark("saved", "Renamed", "New description", 7, cover.metricId);
  assert.deepEqual(updated.cover, cover);
  const changed = updateBookmark("saved", "Renamed", "New description", 7, recipe.measures[1].id);
  assert.equal(changed.cover.maxGroups, undefined, "a different metric starts with its own presentation");
});


test("replacing a kept query clears obsolete cover IDs and permits renaming", t => {
 const item=instantiateRecipe(queryRecipes.find(recipe=>recipe.id==="tool-failures"),7);
 const cover={...defaultCardCover(queryRecipes.find(recipe=>recipe.id==="tool-failures")),maxGroups:20,showLegend:false};
 const values=bookmarkFixture(t,[{id:"kept",dataset:"tool_calls",savedQueryId:"saved",title:item.title,url:item.url,query:item.query,cover,coverMetricId:cover.metricId}]);
 const next={...core.EMPTY_QUERY,select:[{field:"id"}]};
 replaceSavedQuery("tool_calls","saved",next,200,queryURL("tool_calls",next));
 const replaced=values.get("pharos-bookmarks-v1")[0];
 assert.equal(replaced.cover,undefined);assert.equal(replaced.coverMetricId,undefined);
 assert.equal(updateBookmark("kept","Renamed","Editable again",undefined,"").title,"Renamed");
});

test("replacing a kept query retains a still-valid cover and its settings", t => {
 const recipe=queryRecipes.find(recipe=>recipe.id==="tool-failures"),item=instantiateRecipe(recipe,7),cover={...defaultCardCover(recipe),maxGroups:20,showOther:false};
 const values=bookmarkFixture(t,[{id:"kept",dataset:"tool_calls",savedQueryId:"saved",title:item.title,url:item.url,query:item.query,cover}]);
 const next={...item.query,limit:25};
 replaceSavedQuery("tool_calls","saved",next,200,queryURL("tool_calls",next,item.window));
 assert.deepEqual(values.get("pharos-bookmarks-v1")[0].cover,cover);
});
