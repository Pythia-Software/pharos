import test from "node:test";
import assert from "node:assert/strict";
import { capabilities, core, execution, schemas } from "../helpers/recipes.mjs";
import tracker from "../../../../docs/homepage-query-cards.json";
import { queryRecipes, instantiateRecipe, loadQueryPreview, clearPreviewCache } from "../../../src/curiosity-data";
import { queryFromURL, queryHasTimeFilter, queryURL, resolveRollingQuery } from "../../../src/query-links";
import { migrateSavedQueries, updateBookmark, replaceSavedQuery } from "../../../src/bookmarks";
import { cardCoverQuery, defaultCardCover } from "../../../src/card-cover";
import { wishlistCardRecipes } from "../../../src/wishlist-card-recipes";
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
  const rows = ["one", "one", "two"].map((repository_name, i) => ({ id: String(i), repository_name, conversation_id: "conversation", repo_path: "src/main.go", tool_category: "read", started_at: clock.getTime() - 1000 }));
  const result = core.applyQuery(rows, query, schemas.tool_calls, { now: clock.getTime() });
  assert.equal(result.total, 1);
  assert.equal(result.rows[0].repository_name, "one");
  assert.equal(result.rows[0].cc_calls, 2);
});

test("group drilldown preserves source predicates and metrics, handles null keys and drops HAVING", () => {
  const recipe = queryRecipes.find(r => r.id === "repeated-reads"), { query } = instantiateRecipe(recipe, 7, clock);
  const records = core.groupSourceQuery(query, { repository_name: null, repo_path: "src/main.go", conversation_id: "conversation" }, schemas.tool_calls);
  assert.equal(records.grouping, undefined);
  assert.equal(records.offset, 0);
  assert.deepEqual(records.aggregations, recipe.measures);
  assert.deepEqual(records.where, [...query.grouping.source.where, { field: "repository_name", op: "is_null", value: "" }, { field: "repo_path", op: "=", value: "src/main.go" }, { field: "conversation_id", op: "=", value: "conversation" }]);
  // Keys that cannot be matched exactly (unknown fields) have no record query.
  assert.equal(core.groupSourceQuery({ ...query, grouping: { ...query.grouping, keys: ["unknown"] } }, { unknown: "x" }, schemas.tool_calls), null);
});

test("cache efficiency is weighted by input tokens and preserves provider cohorts", () => {
  const { query } = instantiateRecipe(queryRecipes.find(r => r.id === "cache-efficiency"), 7, clock);
  const row = { first_usage_at: clock.getTime() - 1000, provider: "one", model_family: "model" };
  const result = core.applyQuery([
    { ...row, id: "a", input_tokens: 120, cache_creation_input_tokens: 20, cache_read_input_tokens: 90, uncached_input_tokens: 10, agent_session_id: "same" },
    { ...row, id: "b", input_tokens: 900, cache_read_input_tokens: 0, uncached_input_tokens: 900, agent_session_id: "same" },
    { ...row, id: "c", provider: "two", input_tokens: 100, cache_read_input_tokens: 100, uncached_input_tokens: 0, agent_session_id: "other" },
  ], query, schemas.usage, { now: clock.getTime() });
  assert.equal(result.total, 2);
  assert.equal(result.rows.find(r => r.provider === "one").cc_share, 90 / 1020);
  assert.equal(result.rows.find(r => r.provider === "one").cc_input, 1020);
});

test("work comparisons preserve workspace identity and take titles from the latest usage row", () => {
  const { query } = instantiateRecipe(queryRecipes.find(r => r.id === "usage-work"), 7, clock);
  const row = { first_usage_at: clock.getTime() - 1000, cost_usd: 1, total_tokens: 100, workspace_id: "one", root_repository_name: "repo", root_conversation_id: "one", root_title: "Current title", agent_session_id: "agent" };
  const result = core.applyQuery([
    { ...row, id: "old", title: "Old title", last_usage_at: new Date(clock.getTime() - 100).toISOString() },
    { ...row, id: "new", title: "Current title", last_usage_at: clock.toISOString() },
    { ...row, id: "other", workspace_id: "two", root_conversation_id: "two", title: "Current title", last_usage_at: clock.toISOString() },
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

const recentMessage = { sent_at: clock.getTime() - 1000, repository_name: "one", title: "Work", other_words: 0, typed_words: 5 };

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
  for (const recipe of wishlistCardRecipes) {
    const idea = tracker.cards.find(card => card.id === recipe.idea);
    assert.ok(idea, recipe.id);
    assert.ok(idea.implemented_recipes.includes(recipe.id), recipe.id);
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
