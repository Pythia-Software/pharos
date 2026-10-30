import test from "node:test";
import assert from "node:assert/strict";
import { capabilities, core, execution, schemas } from "../helpers/recipes.mjs";
import tracker from "../../../../docs/homepage-query-cards.json";
import { queryRecipes, supportsToday, instantiateRecipe, loadQueryPreview, clearPreviewCache, suggestionWindowLabel } from "../../../src/curiosity-data";
import { queryFromURL, queryHasTimeFilter, queryURL, resolveRollingQuery, rollingQueryMatches } from "../../../src/query-links";
import { migrateSavedQueries, readBookmarks, removeBookmark, savedQueries, updateBookmark, replaceSavedQuery } from "../../../src/bookmarks";
import { cardCoverQuery, defaultCardCover } from "../../../src/card-cover";
import { wishlistCardRecipes } from "../../../src/wishlist-card-recipes";
import { updateQueryWindowURL } from "../../../src/query-links";
import { parallelismSelectionQuery, readDashboardQuery, readDashboardLink } from "../../../src/usage-dashboard-model";
import { concurrencySettingsKey, readConcurrencySettings, resolveConcurrencyRange } from "../../../src/concurrency-settings";
import { cardDashboardWhere, cardDetailsURL } from "../../../src/card-exploration";
import { usageWorkspaceURL } from "../../../src/usage-workspace-model";
import { timeBounds, usageTime, writingTime } from "../../../src/usage-chart";
import { cardAnalysisJob, relatedCardDashboardURL, resolveCardAnalysisJob } from "../../../src/card-analysis-model";
import { todayBounds } from "../../../src/day-window";
import { windowedChart } from "../../../src/card-windows";
const clock = new Date(2026, 9, 13, 12);

test("TL1 cards open their grouped attempts in the shared Analyze table", t => {
  bookmarkFixture(t, []);
  const recipe = queryRecipes.find(recipe => recipe.id === "tl1-revisits");
  const suggestion = instantiateRecipe(recipe, 7, clock);
  const url = new URL(suggestion.url, location.origin);
  assert.equal(url.pathname, "/analyze");
  assert.equal(url.searchParams.get("analyze_mode"), "query");
  assert.equal(url.searchParams.get("analyze_data"), "tl1_attempts");
  const restored = queryFromURL(url.href);
  assert.equal(restored.dataset, "tl1_attempts");
  assert.deepEqual(restored.query, core.normalizeQueryState(suggestion.query));
  assert.ok(restored.query.grouping);
  assert.deepEqual(restored.window, suggestion.window);
});

test("origin cards explore the metric and breakdown promised by their covers", t => {
  bookmarkFixture(t, []);
  for (const field of ["channel", "machine_id", "execution_machine_id"]) for (const metric of ["tokens", "cost"]) {
    const recipe = queryRecipes.find(recipe => recipe.id === `origin-${metric}-${field}`);
    assert.ok(recipe);
    const params = new URL(instantiateRecipe(recipe, 30, clock).url, "http://home.test").searchParams;
    const chart = JSON.parse(params.get("analyze_chart"));
    assert.equal(chart.split, field);
    assert.equal(chart.metric, metric);
    const job = cardAnalysisJob(instantiateRecipe(recipe, 30, clock).url, schemas.usage);
    assert.deepEqual(job.options.cover, defaultCardCover(recipe));
    assert.deepEqual(job.query.grouping.keys, [field]);
  }
});

test("every expanded card executes the original population, calculation and presentation across windows", t => {
  bookmarkFixture(t, []);
  for (const recipe of queryRecipes.filter(recipe => !recipe.retired && recipe.destination)) {
    for (const days of [1, 7, 30, 90, 180, 365, null]) {
      const item = instantiateRecipe(recipe, days, clock);
      const job = cardAnalysisJob(item.url, schemas[recipe.dataset]);
      assert.equal(job.dataset, recipe.dataset, recipe.id);
      assert.deepEqual(job.query, core.normalizeQueryState(item.query), recipe.id);
      assert.deepEqual(job.options.cover, defaultCardCover(recipe), recipe.id);
      assert.deepEqual(job.options.window, item.window, recipe.id);
      assert.deepEqual(job.options.recipe.trend, recipe.trend, recipe.id);
      assert.deepEqual(job.options.recipe.weekday, recipe.weekday, recipe.id);
      assert.equal(job.options.recipe.humanStudy, recipe.humanStudy, recipe.id);
      const shown = cardCoverQuery(job.query, job.options.recipe, job.options.cover);
      const original = cardCoverQuery(core.normalizeQueryState(item.query), recipe, defaultCardCover(recipe));
      assert.deepEqual(shown, original, recipe.id);
    }
  }
});

test("expanded cards preserve custom cadence, series folding and collected metric edits", t => {
  bookmarkFixture(t, []);
  const recipe = queryRecipes.find(recipe => recipe.id === "token-volume-trend");
  const cover = { ...recipe.cover, maxGroups: 3, showOther: false, timeline: { rollingPeriods: 14, display: "cumulative", scale: "logarithmic" } };
  const item = instantiateRecipe({ ...recipe, cover }, 90, clock);
  const job = cardAnalysisJob(item.url, schemas.usage);
  assert.deepEqual(job.options.cover, cover);
  const all = cardAnalysisJob(updateQueryWindowURL(item.url, null), schemas.usage);
  assert.equal(all.options.window, undefined);
  assert.deepEqual((all.query.grouping?.source ?? all.query).where, recipe.where);
  assert.deepEqual(all.options.cover, cover);
  const related = new URL(relatedCardDashboardURL(new URL(item.url, location.origin).searchParams), location.origin);
  assert.equal(related.searchParams.has("card_id"), false);
  assert.equal(related.searchParams.has("card_cover"), false);
  assert.equal(related.searchParams.has("q_usage"), false);
});

test("All time analysis copy follows the query window instead of the related dashboard lookback", t => {
  bookmarkFixture(t, []);
  for (const days of [1, 7, 30, 90, 180, 365]) {
    const recipe = queryRecipes.find(recipe => recipe.id === "human-words");
    const item = instantiateRecipe(recipe, days, clock);
    const job = cardAnalysisJob(updateQueryWindowURL(item.url, null), schemas[recipe.dataset]);
    assert.equal(job.options.window, undefined);
    assert.equal(job.options.recipe.window, undefined);
    assert.equal(job.options.recipe.title, "All-time writing");
    assert.equal(job.options.recipe.description, "Human-written words sent across all time, grouped by the day they were sent.");
    assert.equal(queryHasTimeFilter(job.query, recipe.field), false);
    const restored = cardAnalysisJob(updateQueryWindowURL(job.options.url, days), schemas[recipe.dataset]);
    assert.equal(restored.options.recipe.title, item.title);
    assert.equal(restored.options.recipe.description, item.description);
    const kept = new URL(item.url, location.origin);
    kept.searchParams.set("card_title", item.title);
    kept.searchParams.set("card_description", item.description);
    const keptAll = cardAnalysisJob(updateQueryWindowURL(kept.href, null), schemas[recipe.dataset]);
    assert.equal(keptAll.options.recipe.title, job.options.recipe.title, "saved default headings follow the range too");
    assert.equal(keptAll.options.recipe.description, job.options.recipe.description);
  }
  const trend = queryRecipes.find(recipe => recipe.id === "token-volume-trend");
  const all = cardAnalysisJob(updateQueryWindowURL(instantiateRecipe(trend, 180, clock).url, null), schemas.usage);
  assert.equal(all.options.recipe.description, "Daily tokens by provider across all time, averaged over seven calendar days including idle days.");
  const volume = queryRecipes.find(recipe => recipe.id === "human-word-total");
  assert.equal(cardAnalysisJob(updateQueryWindowURL(instantiateRecipe(volume, 7, clock).url, null), schemas[volume.dataset]).options.recipe.title, "All-time writing volume over time");
  const cumulative = queryRecipes.find(recipe => recipe.id === "cumulative-api-cost");
  assert.equal(cardAnalysisJob(updateQueryWindowURL(instantiateRecipe(cumulative, 365, clock).url, null), schemas.usage).options.recipe.description, "API-equivalent cost accumulated from the start of recorded history, split by provider; fully priced facts only.");
  const custom = instantiateRecipe({ ...trend, title: "My workload", description: "Keep my wording." }, 7, clock);
  const customAll = cardAnalysisJob(updateQueryWindowURL(custom.url, null), schemas.usage);
  assert.equal(customAll.options.recipe.title, custom.title);
  assert.equal(customAll.options.recipe.description, custom.description);
});

test("expanded cards resolve durable source queries and fail instead of substituting a template", async t => {
  bookmarkFixture(t, []);
  const item = instantiateRecipe(queryRecipes.find(recipe => recipe.id === "tool-failure-rates"), 7, clock);
  const query = core.normalizeQueryState(item.query);
  const params = new URL(item.url, location.origin);
  params.searchParams.set("q_tool_calls", "@aaaaaaaaaaaaaaaaaaaaaa");
  const fetch = global.fetch;
  t.after(() => { global.fetch = fetch; });
  global.fetch = async () => Response.json({ version: 1, code: "aaaaaaaaaaaaaaaaaaaaaa", token: core.encodeQuery(query), createdAt: 0 });
  const restored = await resolveCardAnalysisJob(params.href, schemas.tool_calls);
  assert.deepEqual(restored.query, query);
  assert.deepEqual(restored.query.where, query.where, "group thresholds remain HAVING");
  global.fetch = async () => new Response("Unavailable", { status: 404 });
  const missing = new URL(params); missing.searchParams.set("q_tool_calls", "@bbbbbbbbbbbbbbbbbbbbbb");
  await assert.rejects(resolveCardAnalysisJob(missing.href, schemas.tool_calls));
});

test("dashboard card windows and retained cover queries stay aligned after collection edits", t => {
  bookmarkFixture(t, []);
  for (const recipe of queryRecipes.filter(recipe => recipe.destination)) {
    const item = instantiateRecipe(recipe, 30, clock);
    assert.deepEqual(queryFromURL(item.url).query, core.normalizeQueryState(item.query), recipe.id);
    for (const days of [1, 7, 30, 90, 180, 365, null]) {
      const url = new URL(updateQueryWindowURL(item.url, days), location.origin);
      const spec = queryFromURL(url.href), filters = readDashboardQuery(url.searchParams, core.EMPTY_QUERY);
      assert.deepEqual(filters.query.where, cardDashboardWhere(recipe.dataset, recipe.destination.dataset, (spec.query.grouping?.source ?? spec.query).where), `${recipe.id} @${days}`);
      assert.equal(filters.error, "");
      // All time keeps the window the card was opened with (30 days).
      assert.deepEqual(JSON.parse(url.searchParams.get("analyze_chart")), windowedChart(recipe.id, recipe.destination.chart, days ?? 30));
      assert.equal(spec.window?.days, days ?? undefined);
    }
  }
  assert.equal(queryFromURL("/analyze?analyze_mode=dashboards&analyze_data=usage"), undefined, "ordinary dashboards have no card cover query");
});

test("all card dashboards restore rolling range presets instead of custom timestamps", () => {
  const fieldsFor = (topic) => topic === "human" ? { first: "sent_at", last: "sent_at" } : topic === "tools" ? { first: "day", last: "day" }
    : topic === "skills" ? { first: "created_at", last: "created_at" } : topic === "time" ? { first: "started_at", last: "started_at" }
    : topic === "mcp" ? { first: "called_at", last: "called_at" } : usageTime;
  for (const recipe of queryRecipes.filter(recipe => recipe.destination && recipe.destination.topic !== "parallelism")) {
    for (const days of [1, 7, 30, 90, 180, 365]) {
      const item = instantiateRecipe(recipe, days, clock), params = new URL(item.url, "http://home.test").searchParams;
      const { query, error } = readDashboardQuery(params, core.EMPTY_QUERY);
      assert.equal(error, "");
      const fields = fieldsFor(recipe.destination.topic);
      const initial = timeBounds(query.where, fields, clock.getTime());
      assert.equal(initial.preset, recipe.period ? undefined : days === 180 ? "6m" : days === 365 ? "1y" : `${days}d`, recipe.id);
      if (recipe.period) continue;
      const later = timeBounds(query.where, fields, clock.getTime() + 86400000);
      assert.equal(later.preset, initial.preset);
      assert.equal(later.from.getTime() - initial.from.getTime(), 86400000);
      assert.equal(later.to.getTime() - initial.to.getTime(), 86400000);
    }
  }
});

test("parallelism card destinations and edited windows retain saved record settings and bound the chart", t => {
  const prefs = bookmarkFixture(t, []);
  const data = { period_start: "2025-01-01", period_end: "2026-10-21", timezone: "UTC" };
  for (const counting of ["workspace", "agent"]) for (const gap of [0, 15, 30, 60]) {
    prefs.set(concurrencySettingsKey, { counting, gap, repository: "previous-repo", day: "2025-03-01", zoom: [1, 2] });
    for (const recipe of queryRecipes.filter(recipe => recipe.destination?.topic === "parallelism")) {
      const original = instantiateRecipe(recipe, 7);
      for (const days of [1, 7, 30, 90, 180, 365, null]) {
        const urls = [updateQueryWindowURL(original.url, days)];
        if (days !== null) urls.push(instantiateRecipe(recipe, days).url);
        for (const url of urls) {
          const route = JSON.parse(new URL(url, location.origin).searchParams.get("analyze_parallelism"));
          const settings = readConcurrencySettings(route), range = resolveConcurrencyRange(data, settings);
          assert.equal(settings.counting, counting, recipe.id);
          assert.equal(settings.gap, gap, recipe.id);
          assert.equal(settings.repository, ""); assert.equal(settings.day, ""); assert.equal(settings.zoom, null);
          assert.equal(range.endDay, data.period_end);
          if (days === null) assert.equal(range.startDay, data.period_start);
          else assert.equal((range.bounds[1] - range.bounds[0]) / 86400000, days, `${recipe.id} @${days}`);
          const records = parallelismSelectionQuery({ settings, bounds: range.bounds, selected: null });
          assert.ok(records.where.some(term => term.field === "counting" && term.value === counting));
          assert.ok(records.where.some(term => term.field === "gap_minutes" && term.value === String(gap)));
        }
      }
    }
  }
});

test("editing a card cover keeps the dashboard as its exploration entry point", () => {
  for (const id of ["cost-repos", "token-volume-trend"]) {
    const recipe = queryRecipes.find(recipe => recipe.id === id);
    const cover = defaultCardCover(recipe);
    const alternate = { ...recipe, cover: { ...cover, mode: "source", metricId: "different" } };
    assert.equal(new URL(instantiateRecipe(alternate, 7).url, "http://home.test").searchParams.get("analyze_mode"), "dashboards");
    const cosmetic = { ...recipe, cover: { ...cover, height: 400 } };
    assert.equal(new URL(instantiateRecipe(cosmetic, 7).url, "http://home.test").searchParams.get("analyze_mode"), "dashboards");
  }
  const recipe = queryRecipes.find(recipe => recipe.id === "token-volume-trend");
  const item = instantiateRecipe({ ...recipe, cover: { ...recipe.cover, timeline: { rollingPeriods: 14 } } }, 30);
  assert.equal(new URL(item.url, "http://home.test").searchParams.get("analyze_mode"), "dashboards");
});

test("changing a collected cover preserves the dashboard and the matching detail query", t => {
  const recipe = queryRecipes.find(recipe => recipe.id === "cost-repos"), item = instantiateRecipe(recipe, 30);
  bookmarkFixture(t, [{ id: "saved", title: item.title, url: item.url, query: item.query, cover: defaultCardCover(recipe) }]);
  const updated = updateBookmark("saved", item.title, item.description, undefined, "card_total_tokens");
  assert.equal(new URL(updated.url, location.origin).searchParams.get("analyze_mode"), "dashboards");
  const restored = queryFromURL(updated.url);
  assert.equal(restored.window.days, 30);
  assert.deepEqual(restored.query.grouping.source.where, item.query.grouping.source.where);
  const job = cardAnalysisJob(updated.url, schemas.usage);
  assert.equal(job.options.cover.metricId, "card_total_tokens");
  assert.equal(job.options.coverMetricId, "card_total_tokens");
});

test("dashboard context projects shared cohorts without treating per-record evidence as summary filters", () => {
  const where = [{ field: "tool_category", op: "=", value: "edit" }, { field: "repo_path", op: "=", value: "main.go" }, { field: "error_count", op: ">", value: "0" },
    { field: "started_at", op: ">=", value: "-7d" }, { field: "started_at", op: "<", value: "+0s" },
    { any: [{ field: "tool_name", op: "=", value: "Read" }, { field: "repo_path", op: "=", value: "another.go" }] }];
  assert.deepEqual(cardDashboardWhere("tool_calls", "tools", where), [where[0], { field: "day", op: ">=", value: "-7d" }, { field: "day", op: "<", value: "+0s" }]);
  assert.deepEqual(cardDashboardWhere("writing_messages", "writing", [{ field: "repository_name", op: "=", value: "repo" }, { field: "text", op: "contains", value: "try again" }, { field: "sent_at", op: ">=", value: "-7d" }, { field: "sent_at", op: "<", value: "+0s" }]),
    [{ field: "repository_name", op: "=", value: "repo" }, { field: "last_message_at", op: ">=", value: "-7d" }, { field: "first_message_at", op: "<", value: "+0s" }]);
});

test("card detail links retain durable source tokens across dashboard datasets and navigation", t => {
  bookmarkFixture(t, []);
  const recipe = queryRecipes.find(recipe => recipe.id === "tool-failure-rates"), item = instantiateRecipe(recipe, 7);
  const url = new URL(item.url, location.origin);
  url.searchParams.set("q_tool_calls", "@immutable-card-query");
  const details = new URL(cardDetailsURL(url.searchParams), location.origin);
  assert.equal(details.searchParams.get("analyze_mode"), "query");
  assert.equal(details.searchParams.get("analyze_data"), "tool_calls");
  assert.equal(details.searchParams.get("q_tool_calls"), "@immutable-card-query");
  const tokenCard = instantiateRecipe(queryRecipes.find(recipe => recipe.id === "task-token-ranges"), 7);
  const records = { ...core.EMPTY_QUERY, where: [{ field: "repository_name", op: "=", value: "chosen-repo" }] };
  const drilled = new URL(usageWorkspaceURL({ mode: "query", dataset: "usage", query: records }, new URL(tokenCard.url, location.origin).searchParams), location.origin);
  assert.deepEqual(core.decodeQueryStrict(drilled.searchParams.get("q_usage")), core.normalizeQueryState({ ...records, fitToScreen: true }), "dashboard drilldowns use the newly selected records and fit their viewport");
  assert.equal(drilled.searchParams.has("card_id"), false, "new analysis is distinct from the original card details");
});

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
  assert.equal(preview.action, recipe.action);
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
  const cover = { ...defaultCardCover(recipe), maxGroups: 3, showOther: false, timeline: { rollingPeriods: 14 } };
  const address = new URL(suggestion.url, location.origin);
  address.searchParams.delete("card_cover");
  const collected = { id: "old-card", title: "Kept title", description: "Kept description", url: address.pathname + address.search, query: suggestion.query, cover };
  values.set("pharos-bookmarks-v1", [legacy, fixed, collected]);
  migrateSavedQueries();
  const saved = values.get("pharos-bookmarks-v1");
  assert.equal(saved[0].savedQueryId, "saved-id");
  assert.equal(saved[0].id, "legacy");
  assert.deepEqual(queryFromURL(saved[0].url).query.grouping.source.where.filter(term => term.field === recipe.field).map(term => term.value), ["-7d", "+0s"]);
  assert.equal(saved[1].url, fixed.url);
  const expanded = cardAnalysisJob(saved[2].url, schemas[recipe.dataset]);
  assert.deepEqual(expanded.options.cover, cover, "existing collection settings migrate into the destination");
  assert.equal(expanded.options.recipe.title, collected.title);
  assert.equal(expanded.options.recipe.description, collected.description);
});

test("bookmark migration preserves separately named saves of the same query and their default IDs", t => {
  const values = bookmarkFixture(t, []);
  const item = instantiateRecipe(queryRecipes.find(recipe => recipe.id === "human-words"), 7);
  values.set("query-table:saved:pharos_writing_messages", [
    { id: "save-a", name: "Writing practice", savedAt: 100, query: item.query },
    { id: "save-b", name: "My daily words", savedAt: 200, query: item.query },
  ]);
  values.set("query-table:default:pharos_writing_messages", "save-a");
  migrateSavedQueries();
  assert.deepEqual(savedQueries("writing_messages").map(saved => [saved.id, saved.name]), [["save-a", "Writing practice"], ["save-b", "My daily words"]]);
  assert.equal(values.get("query-table:default:pharos_writing_messages"), "save-a");
  removeBookmark(readBookmarks()[0].id);
  migrateSavedQueries();
  assert.deepEqual(savedQueries("writing_messages").map(saved => saved.id), ["save-b"], "a removed save is not migrated again");
  assert.equal(values.has("query-table:default:pharos_writing_messages"), false, "removing the default save clears the default");
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
  assert.equal(suggestionWindowLabel(suggestion), "All time");
  assert.equal(suggestion.window, undefined);
  assert.equal(queryHasTimeFilter(suggestion.query, "sent_at"), false);
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


test("eligible Today cards preserve calendar intent through saved windows and re-evaluate on the next day", t => {
  bookmarkFixture(t, []);
  for (const recipe of queryRecipes.filter(supportsToday)) {
    const card = instantiateRecipe(recipe, "today", clock);
    assert.equal(suggestionWindowLabel(card), "Today", recipe.id);
    const spec = queryFromURL(card.url, card.query);
    assert.equal(spec.window.mode, "today", recipe.id);
    const tomorrow = new Date(clock); tomorrow.setDate(tomorrow.getDate() + 1);
    const resolved = resolveRollingQuery(spec.query, spec.window, tomorrow);
    const source = resolved.grouping?.source ?? resolved;
    const midnight = new Date(tomorrow); midnight.setHours(0, 0, 0, 0);
    assert.ok(source.where.some(term => term.field === recipe.field && term.op === ">=" && term.value === midnight.toISOString()), recipe.id);
    assert.ok(source.where.some(term => term.field === recipe.field && term.op === "<" && term.value === "+0s"), recipe.id);
    const elapsed = queryFromURL(updateQueryWindowURL(card.url, 1, card.query), card.query);
    assert.equal(elapsed.window.mode, "relative", recipe.id);
    assert.equal(queryFromURL(updateQueryWindowURL(card.url, null, card.query), card.query).window, undefined);
  }
  for (const recipe of queryRecipes.filter(recipe => !supportsToday(recipe))) assert.notEqual(instantiateRecipe(recipe, "today", clock).window?.mode, "today", recipe.id);
});


test("Today query windows retain saved timezone identity while manual datetime edits release it", t => {
  bookmarkFixture(t, []);
  const query = { ...core.EMPTY_QUERY, where: [{ field: "started_at", op: ">=", value: "2026-10-29T00:00:00.000Z" }, { field: "started_at", op: "<", value: "+0s" }] };
  const window = { mode: "today", days: 1, field: "started_at", timezone: "UTC" };
  assert.equal(rollingQueryMatches(query, window), true);
  const resolved = resolveRollingQuery(query, window, new Date("2026-10-29T15:00:00Z"));
  const paged = { ...resolved, offset: 50 };
  assert.equal(resolveRollingQuery(paged, window, new Date("2026-10-29T16:00:00Z")), paged);
  // Go's RFC3339Nano omits zero fractional seconds, and UTC can be written
  // with an offset. Both forms carry the same calendar intent as JS ISO text.
  for (const [from, timezone] of [["2026-10-29T00:00:00Z", "UTC"], ["2026-10-29T00:00:00+00:00", "UTC"], ["2026-10-29T06:00:00Z", "America/Denver"], ["2026-11-01T06:00:00Z", "America/Denver"], ["2026-10-28T18:15:00Z", "Asia/Kathmandu"]]) {
    const linked = { ...query, where: [{ ...query.where[0], value: from }, query.where[1]] };
    assert.equal(rollingQueryMatches(linked, { ...window, timezone }), true, from);
  }
  const openedAt = new Date("2026-10-29T15:00:00Z"), [midnight] = todayBounds(openedAt, window.timezone);
  const goQuery = { ...query, offset: 50, where: [{ ...query.where[0], value: midnight.replace(".000Z", "Z") }, query.where[1]] };
  assert.equal(resolveRollingQuery(goQuery, window, openedAt), goQuery, "equivalent timestamps preserve the current page");
  const tomorrow = new Date(openedAt); tomorrow.setDate(tomorrow.getDate() + 1);
  const renewed = resolveRollingQuery(goQuery, window, tomorrow);
  assert.equal(renewed.offset, 0);
  assert.equal(renewed.where.find(term => term.op === ">=").value, todayBounds(tomorrow, window.timezone)[0]);
  const manual = { ...query, where: [{ ...query.where[0], value: "2026-10-29T12:34:56.000Z" }, query.where[1]] };
  assert.equal(rollingQueryMatches(manual, window), false);
  const tl1 = queryURL("tl1_attempts", query, { days: 1, field: "started_at", mode: "relative" });
  assert.throws(() => updateQueryWindowURL(tl1, "today"), /does not support/);
});


test("selecting a time window returns a query that replaces every direct date comparison", t => {
  bookmarkFixture(t, []);
  const kept = { field: "provider", op: "=", value: "codex" };
  for (const dataset of ["writing_messages", "usage"]) {
    const fields = dataset === "usage" ? ["first_usage_at", "last_usage_at"] : ["sent_at"];
    for (const op of ["=", ">", ">=", "<", "<="]) for (const choice of ["today", 1]) for (const grouped of [false, true]) {
      const source = { ...core.EMPTY_QUERY, offset: 50, where: [...fields.map(field => ({ field, op, value: "2026-01-31" })), kept] };
      const query = grouped ? { ...core.EMPTY_QUERY, offset: 100, grouping: { source, keys: ["provider"], summaries: [{ id: "n", op: "count", label: "Rows" }], representative: null } } : source;
      const adjusted = queryFromURL(updateQueryWindowURL(queryURL(dataset, query), choice, query));
      const applied = adjusted.query.grouping?.source ?? adjusted.query;
      assert.deepEqual(applied.where.filter(term => !fields.includes(term.field)), [kept]);
      assert.equal(applied.where.length, 3, `${dataset}: ${op} → ${choice}`);
      assert.ok(applied.where.some(term => term.op === "<" && term.value === "+0s"));
      assert.ok(applied.where.some(term => term.op === ">=" && term.value === (choice === "today" ? todayBounds()[0] : "-1d")));
      assert.equal(rollingQueryMatches(adjusted.query, adjusted.window), true);
      assert.equal(applied.offset, 0);
      assert.equal(adjusted.query.offset, 0);
      if (grouped) assert.deepEqual(adjusted.query.grouping.summaries, query.grouping.summaries);
    }
  }
});

test("Today cards keep advancing when opened as related dashboard overviews", t => {
  bookmarkFixture(t, []);
  const openedAt = new Date(2026, 10, 1, 9), tomorrow = new Date(2026, 10, 2, 9);
  t.mock.timers.enable({ apis: ["Date"], now: openedAt });
  let checked = 0;
  for (const recipe of queryRecipes.filter(supportsToday)) {
    const card = instantiateRecipe(recipe, "today", openedAt), params = new URL(card.url, location.origin).searchParams;
    if (!params.has("card_query") || !params.has("analyze_filters")) continue;
    const overview = new URL(relatedCardDashboardURL(params), location.origin).searchParams;
    assert.equal(overview.get("analyze_window"), "today", recipe.id);
    assert.equal(overview.has("window_mode"), false, recipe.id);
    assert.equal(overview.has("card_query"), false, recipe.id);
    const where = core.decodeQueryStrict(overview.get("analyze_filters")).where;
    const time = params.get("analyze_data") === "tools" ? { first: "day", last: "day" } : params.get("analyze_data") === "usage" ? usageTime : params.get("analyze_data") === "writing_messages" ? { first: "sent_at", last: "sent_at" } : { first: recipe.field, last: recipe.field };
    const before = readDashboardLink(overview, {}, () => true, time, undefined, openedAt);
    const after = readDashboardLink(overview, {}, () => true, time, undefined, tomorrow);
    assert.equal(before.error, "", recipe.id);
    assert.equal(after.error, "", recipe.id);
    assert.equal(before.where.find(term => term.field === time.last && term.op === ">=").value, todayBounds(openedAt)[0], recipe.id);
    assert.equal(after.where.find(term => term.field === time.last && term.op === ">=").value, todayBounds(tomorrow)[0], recipe.id);
    assert.deepEqual(after.where.filter(term => ![time.first, time.last].includes(term.field)), where.filter(term => ![time.first, time.last].includes(term.field)), recipe.id);
    const elapsed = new URL(instantiateRecipe(recipe, 1, openedAt).url, location.origin).searchParams;
    assert.equal(new URL(relatedCardDashboardURL(elapsed), location.origin).searchParams.has("analyze_window"), false, recipe.id);
    const fixed = new URLSearchParams(params);
    for (const key of [...fixed.keys()]) if (key.startsWith("window_")) fixed.delete(key);
    assert.equal(new URL(relatedCardDashboardURL(fixed), location.origin).searchParams.has("analyze_window"), false, recipe.id);
    const switched = new URL(usageWorkspaceURL({ mode: "dashboards", topic: params.get("analyze_topic") === "human" ? "tokens" : "human" }, params), location.origin).searchParams;
    assert.equal(switched.has("analyze_window"), false, recipe.id);
    checked++;
  }
  assert.ok(checked > 0);
});
