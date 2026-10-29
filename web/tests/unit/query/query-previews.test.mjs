import assert from "node:assert/strict";
import test from "node:test";
import { core, execution, schemas } from "../helpers/recipes.mjs";
import { queryCapabilities, serverQuery as sharedServerQuery } from "../../support/query-server.mjs";
import { queryRecipes, instantiateRecipe, loadQueryPreviews, loadQueryPreview, clearPreviewCache } from "../../../src/curiosity-data";
import { compactTimelineBuckets } from "../../../src/column-marks";

const rows = Array.from({ length: 10 }, (_, i) => ({ id: String(i), call_count: 1, error_count: i < 2 ? 1 : 0, tool_name: "Bash", command_name: "go test", duration_ms: (i + 1) * 100, result_tokens: i * 10, result_tokens_source: i % 2 ? "estimated" : "measured", repository_name: i % 2 ? "one" : "two", conversation_id: String(i), started_at: Date.now() }));
function query(aggregations, extra = {}) { return { ...core.EMPTY_QUERY, aggregations, ...extra }; }
function job(id, aggregations, options = {}, extra = {}) { return { id, dataset: "tool_calls", schema: schemas.tool_calls, query: query(aggregations, extra), options }; }
const response = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const serverQuery = (request, population = rows, source = schemas.tool_calls) => sharedServerQuery(request, population, source);
function fixture(t, intercept = () => undefined) {
  clearPreviewCache();
  const priorFetch = global.fetch, priorLocation = global.location;
  const requests = [];
  global.location = { origin: "http://preview.test" };
  global.fetch = async (url, init = {}) => {
    const path = String(url), request = init.body ? JSON.parse(init.body) : undefined;
    requests.push({ path, request });
    const replacement = await intercept(path, request, init);
    if (replacement) return replacement;
    if (path.includes("computed-columns")) return response([]);
    if (path.endsWith("capabilities")) return response({ metricCapabilities: { version: 2, expressions: true, distributions: true }, computedExecution: execution, queryCapabilities });
    if (path.includes("query-v3")) return response(serverQuery(request));
    if (path.includes("rows-v2")) return response(core.applyQuery(rows, request, schemas.tool_calls));
    const metrics = request.metrics.map(metric => ({ ...metric, ...(metric.distribution ? { display: { kind: metric.distribution.kind } } : {}) }));
    return response(core.evaluateMetrics(rows, query(metrics, request), schemas.tool_calls));
  };
  t.after(() => { global.fetch = priorFetch; global.location = priorLocation; clearPreviewCache(); });
  return requests;
}

test("failure rate includes successful calls and preserves all dashboard results", async t => {
  const requests = fixture(t), recipe = queryRecipes.find(recipe => recipe.id === "tool-failure-rates");
  const preview = await loadQueryPreview(recipe.dataset, query(recipe.measures), schemas.tool_calls, { recipe, suggested: true });
  assert.equal(preview.state, "ready");
  assert.equal(preview.metrics[0].result.buckets[0].value, 0.2);
  assert.equal(preview.metrics[1].result.buckets[0].value, 2);
  assert.equal(preview.metrics[2].result.buckets[0].value, 10);
  assert.match(preview.summary, /10 observations/);
  assert.equal(requests.filter(request => request.path.endsWith("metrics")).length, 1);
});

test("compatible dashboards batch despite duplicate metric IDs and split at twenty", async t => {
  const requests = fixture(t), previews = new Map();
  await loadQueryPreviews(Array.from({ length: 23 }, (_, i) => job(String(i), [{ id: "same", op: "sum", field: "duration_ms", groupBy: [], expression: `SUM([duration_ms])+${i}` }])), (id, preview, error) => { assert.ifError(error); previews.set(id, preview); });
  const batches = requests.filter(request => request.path.endsWith("metrics"));
  assert.deepEqual(batches.map(batch => batch.request.metrics.length), [20, 3]);
  assert.equal(requests.filter(request => request.path.endsWith("capabilities")).length, 1);
  for (let i = 0; i < 23; i++) assert.equal(previews.get(String(i)).metrics[0].result.buckets[0].value, 5500 + i);
});

test("cover changes reuse full results and preserve distributions and paired axes", async t => {
  const requests = fixture(t);
  const metrics = [
    { id: "box", op: "count", groupBy: [], display: { kind: "box" }, distribution: { kind: "box", input: "[duration_ms]", whiskers: "tukey" } },
    { id: "pair", op: "count", groupBy: [], expression: "AVG([duration_ms])", expressionY: "AVG([result_tokens])", display: { kind: "scatter" } },
  ];
  const first = await loadQueryPreview("tool_calls", query(metrics), schemas.tool_calls);
  assert.equal(first.metrics[0].result.buckets[0].distribution.summary.median, 550);
  const second = await loadQueryPreview("tool_calls", query(metrics), schemas.tool_calls, { coverMetricId: "pair" });
  assert.equal(second.coverMetricId, "pair");
  assert.equal(second.metrics[1].result.buckets[0].y, 45);
  assert.equal(requests.filter(request => request.path.endsWith("metrics")).length, 1);
});

test("duration boxes select positive timing while nonempty output sizes preserve provenance", async t => {
  fixture(t, (path, request) => {
    if (!path.endsWith("metrics")) return;
    const observations = [...rows, { ...rows[0], id: "zero", duration_ms: 0, result_tokens: 0 }];
    return response(core.evaluateMetrics(observations, query(request.metrics.map(metric => ({ ...metric, ...(metric.distribution ? { display: { kind: metric.distribution.kind } } : {}) })), request), schemas.tool_calls));
  });
  const duration = queryRecipes.find(recipe => recipe.id === "duration-distribution");
  const preview = await loadQueryPreview(duration.dataset, query(duration.measures, { where: duration.where }), schemas.tool_calls);
  assert.equal(preview.metrics[0].result.buckets[0].distribution.summary.n, 10);
  assert.equal(preview.metrics[0].result.buckets[0].distribution.summary.median, 550);
  const output = queryRecipes.find(recipe => recipe.id === "output-distribution");
  const distribution = await loadQueryPreview(output.dataset, query(output.measures, { where: output.where }), schemas.tool_calls);
  assert.deepEqual(distribution.metrics[0].clause.groupBy, ["tool_name", "result_tokens_source"]);
  assert.deepEqual(distribution.metrics[0].result.buckets.map(bucket => bucket.keys.at(-1)).sort(), ["estimated", "measured"]);
  assert.equal(distribution.metrics[0].result.buckets.reduce((n, bucket) => n + bucket.distribution.summary.n, 0), 9);
});

test("changing the cover to evidence counts uses the selected metric's unit", async t => {
  fixture(t, (path, request) => path.endsWith("metrics") ? response({ metrics: request.metrics.map(metric => ({ id: metric.id, buckets: [{ keys: metric.groupBy.map(() => "review"), value: 10, count: 10 }], processedRows: 10 })) }) : undefined);
  const recipe = queryRecipes.find(recipe => recipe.id === "skill-reach");
  const preview = await loadQueryPreview(recipe.dataset, query(recipe.measures), schemas.skill_usages, { recipe, coverMetricId: "skill-evidence-types" });
  assert.equal(preview.summary, "10 records");
});

test("saved local-hour cards keep the catalog's 24 combined clock-hour buckets", async t => {
  const previousTZ = process.env.TZ;
  process.env.TZ = "America/Denver";
  t.after(() => { if (previousTZ === undefined) delete process.env.TZ; else process.env.TZ = previousTZ; });
  const message = (id, daysAgo, hour, typed_words) => {
    const sent = new Date();
    sent.setDate(sent.getDate() - daysAgo);
    sent.setHours(hour, 15, 0, 0);
    return { id, sent_at: sent.toISOString(), typed_words };
  };
  const population = [message("first", 1, 9, 100), message("second", 3, 9, 50), message("evening", 2, 17, 25), message("outside", 40, 9, 1000)];
  const requests = fixture(t, (path, request) => {
    if (path.includes("/writing_messages/rows-v2")) return response(core.applyQuery(population, request, schemas.writing_messages));
    if (path.includes("/writing_messages/query-v3")) return response(sharedServerQuery(request, population, schemas.writing_messages));
  });
  const recipe = queryRecipes.find(recipe => recipe.id === "writing-hours");
  assert.equal(recipe.cover, undefined, "this recipe relies on an inferred default cover");
  for (const days of [7, 30]) {
    const item = instantiateRecipe(recipe, days);
    const cover = { mode: "source", metricId: recipe.measures[0].id, kind: "bar-vertical" };
    const catalog = await loadQueryPreview(item.dataset, item.query, schemas.writing_messages, { recipe: item, window: item.window });
    for (const selection of [{ cover }, { coverMetricId: cover.metricId }, { cover, coverMetricId: cover.metricId }]) {
      const saved = await loadQueryPreview(item.dataset, item.query, schemas.writing_messages, { recipe, window: item.window, ...selection });
      assert.equal(saved.study?.kind, "hours");
      assert.deepEqual(saved.study, catalog.study);
      assert.equal(saved.study.rows.length, 24);
      assert.deepEqual(saved.study.rows.filter(row => row.value), [{ label: "09:00", value: 1150 }, { label: "17:00", value: 25 }]);
      assert.equal(saved.study.timeZone, "America/Denver");
    }
  }
  assert.ok(requests.some(({ path }) => path.endsWith("/query-v3")), "all-time covers fold native hourly reductions");
  assert.ok(requests.every(({ path }) => !path.endsWith("/rows-v2")), "all-time covers do not load message text");
});

test("weekday previews render daily distributions and keep all-time and edited chart covers usable", async t => {
  const now = Date.parse("2026-10-14T12:00:00Z");
  t.mock.timers.enable({ apis: ["Date"], now });
  let population = [{ id: "a", sent_at: "2026-10-12T12:00:00Z", typed_words: 40 }];
  const requests = fixture(t, (path, request) => {
    if (path.endsWith("capabilities")) return response({ metricCapabilities: { version: 2, expressions: true, distributions: true }, computedExecution: execution, queryCapabilities: { ...queryCapabilities, metricFilters: true } });
    if (path.includes("/writing_messages/query-v3")) return response(sharedServerQuery(request, population, schemas.writing_messages));
  });
  const recipe = queryRecipes.find(recipe => recipe.id === "human-words-weekday"), item = instantiateRecipe(recipe, 30);
  const preview = await loadQueryPreview(recipe.dataset, item.query, schemas.writing_messages, { recipe, window: item.window });
  assert.equal(preview.state, "ready");
  assert.equal(preview.metrics[0].clause.display.kind, "box");
  assert.equal(preview.metrics[0].result.buckets.length, 7);
  assert.equal(preview.buckets, undefined);
  assert.equal(requests.find(request => request.path.endsWith("query-v3")).request.metrics[0].dateGrouping.interval, "day");
  const native = await loadQueryPreview(recipe.dataset, item.query, schemas.writing_messages, { recipe, window: item.window, cover: { ...recipe.cover, kind: "line" } });
  assert.equal(native.metrics[0].clause.display.kind, "line");
  assert.equal(native.metrics[0].clause.distribution, undefined);
  const allTime = { ...item.query, where: recipe.where };
  assert.equal((await loadQueryPreview(recipe.dataset, allTime, schemas.writing_messages, { recipe })).state, "ready");
  population = [...population, { id: "b", sent_at: "2026-10-13T12:00:00Z", typed_words: 80 }];
  const filtered = { ...item.query, aggregations: item.query.aggregations.map(metric => ({ ...metric, where: [{ field: "sent_at", op: ">=", value: "2026-10-12T12:00:00Z" }], dateGrouping: { ...metric.dateGrouping, timeZone: "UTC" } })) };
  const restricted = await loadQueryPreview(recipe.dataset, filtered, schemas.writing_messages, { recipe });
  assert.equal(restricted.total, 1);
  assert.equal(restricted.metrics[0].result.buckets.find(bucket => bucket.keys[0] === "Monday").distribution.summary, null);
  assert.equal(restricted.metrics[0].result.buckets.find(bucket => bucket.keys[0] === "Tuesday").distribution.summary.median, 80);
  clearPreviewCache();
  population = [{ id: "monday", sent_at: "2026-10-05T12:00:00Z", typed_words: 40, day: "2026-10-05", week: "2026-10-05", month: "2026-10" }];
  const calendarQuery = { ...item.query, where: [...recipe.where, { field: "sent_at", op: ">=", value: "2026-09-28T00:00:00Z" }, { field: "sent_at", op: "<", value: "2026-10-12T00:00:00Z" }],
    aggregations: item.query.aggregations.map(metric => ({ ...metric, dateGrouping: { ...metric.dateGrouping, timeZone: "UTC" } })) };
  for (const [field, value] of [["day", "2026-10-05"], ["date", "2026-10-05"], ["week", "2026-10-05"], ["month", "2026-10"]]) {
    const predicate = { field, op: "=", value };
    for (const changed of [
      { ...calendarQuery, where: [...calendarQuery.where, predicate] },
      { ...calendarQuery, aggregations: calendarQuery.aggregations.map(metric => ({ ...metric, where: [predicate] })) },
    ]) {
      const daily = await loadQueryPreview(recipe.dataset, changed, schemas.writing_messages, { recipe });
      assert.equal(daily.metrics[0].clause.distribution, undefined, `${field} retains native daily results`);
      assert.equal(daily.metrics[0].result.buckets.length, 1, "an excluded Monday must not become a zero sample");
      assert.equal(daily.metrics[0].result.buckets[0].value, 40);
    }
  }
  const date = { field: "sent_at", op: ">=", value: "2026-10-12T12:00:00Z" };
  for (const predicate of [{ any: [date, { field: "repository_name", op: "=", value: "other" }] }, { ...date, negated: true }]) {
    for (const changed of [
      { ...item.query, where: [...item.query.where, predicate] },
      { ...item.query, aggregations: item.query.aggregations.map(metric => ({ ...metric, where: [predicate] })) },
    ]) {
      const daily = await loadQueryPreview(recipe.dataset, changed, schemas.writing_messages, { recipe });
      assert.equal(daily.metrics[0].clause.distribution, undefined, "complex date filters retain native daily results");
      assert.equal(daily.metrics[0].clause.dateGrouping.interval, "day");
    }
  }
  clearPreviewCache(); population = [];
  assert.equal((await loadQueryPreview(recipe.dataset, item.query, schemas.writing_messages, { recipe, window: item.window })).state, "empty");
});

test("relative daily covers chart every local date their window touches, across daylight saving", async t => {
  const previousTZ = process.env.TZ;
  process.env.TZ = "America/Denver";
  t.after(() => { if (previousTZ === undefined) delete process.env.TZ; else process.env.TZ = previousTZ; });
  // Preview requests take their clock from Date.
  t.mock.timers.enable({ apis: ["Date"] });
  const localDate = time => { const date = new Date(time); return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`; };
  const recipe = queryRecipes.find(recipe => recipe.id === "human-words");
  let population = [];
  fixture(t, (path, request) => {
    if (path.includes("/writing_messages/query-v3")) return response(serverQuery(request, population, schemas.writing_messages));
  });
  // The second clock's 90-day window starts in daylight time and ends in standard time.
  for (const now of [Date.parse("2026-10-16T18:00:00Z"), Date.parse("2026-12-15T18:00:00Z")]) {
    t.mock.timers.setTime(now);
    clearPreviewCache();
    population = [{ id: "recent", sent_at: new Date(now - 3 * 86400000).toISOString(), day: localDate(now - 3 * 86400000), typed_words: 10, typed_message: true }];
    for (const days of [7, 30, 90]) {
      const item = instantiateRecipe(recipe, days);
      const preview = await loadQueryPreview(item.dataset, item.query, schemas.writing_messages, { recipe: item, window: item.window });
      assert.equal(preview.state, "ready");
      assert.equal(preview.buckets.length, days + 1, `${days}d: the partial first day and today both get a column`);
      assert.equal(preview.buckets[0].key, localDate(now - days * 86400000));
      assert.equal(preview.buckets.at(-1).key, localDate(now));
      assert.equal(new Set(preview.buckets.map(bucket => bucket.key)).size, days + 1, "each local date appears once");
    }
  }
});

test("sparse tool-size cohorts explain their cutoff in calls rather than conversations", async t => {
  fixture(t, (path, request) => path.endsWith("metrics") ? response(core.evaluateMetrics(rows.slice(1, 3), query(request.metrics), schemas.tool_calls)) : undefined);
  const recipe = queryRecipes.find(recipe => recipe.id === "output-distribution");
  const preview = await loadQueryPreview(recipe.dataset, query(recipe.measures, { where: recipe.where }), schemas.tool_calls, { recipe });
  assert.equal(preview.state, "empty");
  assert.equal(preview.emptyLabel, "No cohorts with at least 3 calls");
});

test("sparse skill boxes name their actual sample population", async t => {
  fixture(t, (path, request) => path.endsWith("metrics") ? response({ metrics: request.metrics.map(metric => ({ id: metric.id, buckets: [{ keys: ["example", "explicit_invocation"], value: 200, count: 2, distribution: { kind: "box", summary: { n: 2, min: 100, q1: 125, median: 150, q3: 175, max: 200, mean: 150 } } }], processedRows: 2 })) }) : undefined);
  const recipe = queryRecipes.find(recipe => recipe.id === "skill-body-sizes");
  const state = query(recipe.measures, { where: recipe.where });
  const preview = await loadQueryPreview(recipe.dataset, state, schemas.skill_usages, { recipe });
  assert.equal(preview.state, "empty");
  assert.equal(preview.emptyLabel, "No cohorts with at least 3 body records");
  const overridden = await loadQueryPreview(recipe.dataset, state, schemas.skill_usages, { recipe, cover: { ...recipe.cover, sampleUnit: "conversation/path groups" } });
  assert.equal(overridden.emptyLabel, "No cohorts with at least 3 conversation/path groups");
});

test("empty diagnostic covers explain the exact missing population", async t => {
  fixture(t);
  const recipe = queryRecipes.find(recipe => recipe.id === "edit-failures"), item = instantiateRecipe(recipe, 7);
  const preview = await loadQueryPreview(recipe.dataset, item.query, schemas.tool_calls, { recipe, window: item.window });
  assert.equal(preview.state, "empty");
  assert.equal(preview.emptyLabel, "No recorded edit errors with mapped paths");
});

test("canonical computed fields carry current revisions, labels and typed results", async t => {
  const definition = { id: "seconds", label: "Seconds", expression: { language: "qt-expr", version: 1, source: "[duration_ms]/1000" }, revision: "2" };
  let sent;
  fixture(t, (path, request) => {
    if (path.includes("computed-columns")) return response([definition]);
    if (path.endsWith("capabilities")) return response({ metricCapabilities: { version: 2, computedFields: true }, computedExecution: { ...execution, fields: { "@computed/seconds": { type: "number", select: true, sort: true, measure: true, group: false } }, resolvedRevisions: { seconds: "2" } } });
    if (path.endsWith("metrics")) { sent = request; return response({ metrics: request.metrics.map(metric => ({ id: metric.id, buckets: [{ keys: [], value: metric.field === "@computed/seconds" ? 5.5 : 5500, count: 10 }], processedRows: 10, coverage: "exact" })) }); }
  });
  const results = new Map();
  await loadQueryPreviews([job("raw", [{ id: "raw", op: "sum", field: "duration_ms", groupBy: [] }]), job("computed", [{ id: "seconds", op: "sum", field: "@computed/seconds", groupBy: [] }])], (id, preview, error) => { assert.ifError(error); results.set(id, preview); });
  const preview = results.get("computed");
  assert.equal(sent.metrics.length, 2);
  assert.deepEqual(sent.expectedRevisions, { seconds: "2" });
  assert.equal(preview.fields.find(field => field.name === "@computed/seconds").label, "Seconds");
  assert.equal(preview.total, 5.5);
  assert.equal(results.get("raw").total, 5500);
});

test("residual computed predicates fail locally while healthy sibling queries complete", async t => {
  const requests = fixture(t), results = new Map();
  const metric = [{ id: "count", op: "count", groupBy: [] }];
  await loadQueryPreviews([job("bad", metric, {}, { where: [{ field: "@computed/missing", op: ">", value: "0" }] }), job("good", metric)], (id, preview, error) => results.set(id, { preview, error }));
  assert.match(String(results.get("bad").error), /residual|filter/i);
  assert.equal(results.get("good").preview.state, "ready");
  assert.equal(requests.filter(request => request.path.endsWith("metrics")).length, 1);
});

test("server-rejected batches isolate the bad formula without replaying successful work", async t => {
  const requests = fixture(t, (path, request) => path.endsWith("metrics") && request.metrics.some(metric => metric.expression === "SUM([duration_ms])") ? response({ error: "backend failure for SUM" }, 400) : undefined);
  const results = new Map();
  await loadQueryPreviews([job("bad", [{ id: "x", op: "count", groupBy: [], expression: "SUM([duration_ms])" }]), job("good", [{ id: "x", op: "count", groupBy: [], expression: "COUNT()" }])], (id, preview, error) => {
    assert.ok(!results.has(id), `published ${id} more than once`);
    results.set(id, { preview, error });
  });
  assert.match(String(results.get("bad").error), /backend failure/);
  assert.equal(results.get("good").preview.total, 10);
  assert.deepEqual(requests.filter(request => request.path.endsWith("metrics")).map(request => request.request.metrics.map(metric => metric.expression)), [["SUM([duration_ms])", "COUNT()"], ["SUM([duration_ms])"], ["COUNT()"]]);
});

test("shown-row scopes preserve saved paging and stay separate from the complete population", async t => {
  const requests = fixture(t), metric = { id: "n", op: "count", groupBy: [] };
  await loadQueryPreviews([job("all", [metric]), job("page", [{ ...metric, scope: "shownRows" }], {}, { limit: 3, offset: 2 })], (_id, _preview, error) => assert.ifError(error));
  const batches = requests.filter(request => request.path.endsWith("metrics"));
  assert.equal(batches.length, 2);
  assert.equal(batches.find(batch => batch.request.metrics[0].scope === "shownRows").request.offset, 2);
});

test("Library searches retain indexed options and never share batches or cached populations", async t => {
  const requests = fixture(t), metric = { id: "n", op: "count", groupBy: [] };
  const jobs = ["first", "second"].map(id => ({ ...job(id, [metric], { url: `/library?search=${id}&kind=file&case=1&fuzzy=0&separators=1` }), dataset: "library", schema: schemas.library }));
  await loadQueryPreviews(jobs, (_id, _preview, error) => assert.ifError(error));
  const batches = requests.filter(request => request.path.includes("/metrics"));
  assert.equal(batches.length, 2);
  for (const [i, batch] of batches.entries()) {
    const params = new URL(batch.path, global.location.origin).searchParams;
    assert.equal(params.get("find"), jobs[i].id);
    assert.equal(params.get("find_kind"), "file");
    assert.equal(params.get("find_case"), "1");
    assert.equal(params.get("find_fuzzy"), "0");
    assert.equal(params.get("find_separators"), "1");
  }
  await loadQueryPreviews(jobs, (_id, _preview, error) => assert.ifError(error));
  assert.equal(requests.filter(request => request.path.includes("/metrics")).length, 2);
});

test("legacy Library previews preserve default-on fuzzy matching and explicit opt-out", async t => {
  const requests = fixture(t);
  const jobs = ["", "&fuzzy=0"].map((suffix, index) => ({ ...job(`legacy-${index}`, [], { url: `/library?search=migration${suffix}` }), dataset: "library", schema: schemas.library }));
  await loadQueryPreviews(jobs, (_id, _preview, error) => assert.ifError(error));
  const fuzzy = requests.filter(request => request.path.includes("rows-v2")).map(request => new URL(request.path, global.location.origin).searchParams.get("find_fuzzy"));
  assert.deepEqual(fuzzy.sort(), ["0", "1"]);
});

test("visual Library bookmarks preserve their tree, scope, cursor, and incomplete coverage", async t => {
  const requests = fixture(t, path => path.includes("rows-v2") ? response({ total: 0, rows: [], find: { search: { complete: false } } }) : undefined);
  const draft = { editor: "visual", match: "rules", text: "", mode: "advanced", fuzzy: false, caseSensitive: false, separators: false,
    root: { kind: "group", join: "any", exclude: false, children: ["cache", "buffer"].map(text => ({ kind: "words", text, other: "", distance: "8", exclude: false })) },
    scope: { surface: "user", roles: [], kinds: [], repository: "pharos", provider: "", source: "", from: "", to: "", conversation_id: "", workspace_id: "" } };
  const url = `/library?${new URLSearchParams({ text_builder: JSON.stringify(draft), text_cursor: "saved-cursor" })}`;
  const preview = await loadQueryPreview("library", query([]), schemas.library, { url });
  const params = new URL(requests.find(r => r.path.includes("rows-v2")).path, global.location.origin).searchParams;
  assert.deepEqual(JSON.parse(params.get("find_where")), { op: "or", children: [{ op: "term", text: "cache" }, { op: "term", text: "buffer" }] });
  assert.equal(params.get("find_surface"), "user");
  assert.equal(params.get("find_repository"), "pharos");
  assert.equal(params.get("find_cursor"), "saved-cursor");
  assert.match(preview.note, /Partial search batch/);
  assert.equal(preview.summary, "No matches in this search batch");
});

test("Human Words bookmark previews preserve the bounded message search and cursor", async t => {
  t.mock.timers.enable({ apis: ["Date"], now: Date.parse("2026-10-14T12:00:00Z") });
  let complete = false, limited = false, population = [];
  const requests = fixture(t, (path, request) => {
    if (path.includes("rows-v2")) return response({ total: 0, rows: [], find: { search: { complete } } });
    if (path.includes("/writing_messages/query-v3")) {
      const result = sharedServerQuery(request, population, schemas.writing_messages);
      return response({ ...result, metrics: result.metrics.map(metric => ({ ...metric, coverage: "exact" })), find: { limited, search: { complete } } });
    }
  });
  const draft = { editor: "visual", match: "rules", text: "", mode: "advanced", fuzzy: false, caseSensitive: false, separators: false,
    root: { kind: "group", join: "any", exclude: false, children: ["cache", "buffer"].map(text => ({ kind: "words", text, other: "", distance: "8", exclude: false })) },
    scope: { surface: "user", roles: [], kinds: [], repository: "pharos", provider: "", source: "", from: "", to: "", conversation_id: "", workspace_id: "" } };
  const url = `/analyze?${new URLSearchParams({ message_query: JSON.stringify(draft), message_cursor: "saved-cursor" })}`;
  const preview = await loadQueryPreview("writing_messages", query([]), schemas.writing_messages, { url });
  const params = new URL(requests.find(r => r.path.includes("rows-v2")).path, global.location.origin).searchParams;
  assert.deepEqual(JSON.parse(params.get("find_where")), { op: "or", children: [{ op: "term", text: "cache" }, { op: "term", text: "buffer" }] });
  assert.equal(params.get("find_surface"), "user");
  assert.equal(params.get("find_repository"), "pharos");
  assert.equal(params.get("find_cursor"), "saved-cursor");
  assert.match(preview.note, /Partial search batch/);
  assert.equal(preview.summary, "No matches in this search batch");
  for (const id of ["human-words-weekday", "human-messages-weekday"]) {
    const recipe = queryRecipes.find(recipe => recipe.id === id), item = instantiateRecipe(recipe, 30);
    for (const coverage of [{ complete: false, limited: false }, { complete: true, limited: true }]) {
      ({ complete, limited } = coverage);
      for (const messages of [[], [{ id: "match", sent_at: "2026-10-12T12:00:00Z", typed_words: 40 }]]) {
        population = messages; clearPreviewCache();
        await assert.rejects(loadQueryPreview(recipe.dataset, item.query, schemas.writing_messages, { recipe, url }), /Partial search batch.*complete/i);
      }
    }
    complete = true; limited = false; clearPreviewCache();
    const finished = await loadQueryPreview(recipe.dataset, item.query, schemas.writing_messages, { recipe, url });
    assert.equal(finished.state, "ready");
    assert.equal(finished.metrics[0].clause.distribution.kind, "box");
    assert.match(finished.note, /This search batch/);
    complete = false; clearPreviewCache();
    await assert.rejects(loadQueryPreview(recipe.dataset, item.query, schemas.writing_messages, { recipe, url }), /Partial search batch.*complete/i);
  }
});

test("a broken visual bookmark fails visibly without unfiltering itself or other previews", async t => {
  const requests = fixture(t), results = new Map();
  await loadQueryPreviews([{ ...job("broken", [], { url: "/library?text_builder=broken" }), dataset: "library", schema: schemas.library }, job("good", [])], (id, preview, error) => results.set(id, { preview, error }));
  assert.match(String(results.get("broken").error), /saved search could not be opened/);
  assert.equal(results.get("good").preview.total, 10);
  assert.equal(requests.some(request => request.path.startsWith("/api/query/library/")), false);
});

test("row excerpts preserve the saved offset and cap only the preview length", async t => {
  const requests = fixture(t);
  await loadQueryPreview("tool_calls", query([], { limit: 2, offset: 5 }), schemas.tool_calls);
  const request = requests.find(request => request.path.includes("rows-v2")).request;
  assert.equal(request.limit, 2);
  assert.equal(request.offset, 5);
});

test("empty distributions stay empty while observed zero rates remain valid saved views", async t => {
  fixture(t);
  const box = { id: "box", op: "count", groupBy: [], display: { kind: "box" }, distribution: { kind: "box", input: "[duration_ms]" } };
  const empty = await loadQueryPreview("tool_calls", query([box], { where: [{ field: "duration_ms", op: ">", value: "100000" }] }), schemas.tool_calls);
  assert.equal(empty.state, "empty");
  const recipe = queryRecipes.find(recipe => recipe.id === "tool-failure-rates");
  const zero = query(recipe.measures, { where: [{ field: "error_count", op: "=", value: "0" }] });
  const saved = await loadQueryPreview(recipe.dataset, zero, schemas.tool_calls, { recipe });
  assert.equal(saved.state, "ready");
  assert.equal(saved.metrics[0].result.buckets[0].value, 0);
  assert.equal((await loadQueryPreview(recipe.dataset, zero, schemas.tool_calls, { recipe, suggested: true })).state, "empty");
});

test("cancellation also suppresses cached publications", async t => {
  const requests = fixture(t), controller = new AbortController();
  const jobs = [job("first", [{ id: "n", op: "count", groupBy: [] }]), job("second", [{ id: "n", op: "count", groupBy: [] }])];
  await loadQueryPreviews(jobs, (_id, _preview, error) => assert.ifError(error));
  let publications = 0;
  await loadQueryPreviews(jobs, () => { publications++; controller.abort(); }, controller.signal);
  assert.equal(publications, 1);
  await loadQueryPreviews(jobs, () => assert.fail("already aborted cache hit published"), controller.signal);
  assert.equal(requests.filter(request => request.path.endsWith("metrics")).length, 1);
});

test("cancellation prevents publication and leaves no aborted result in the cache", async t => {
  const controller = new AbortController();
  const requests = fixture(t, (path) => { if (path.endsWith("metrics")) { controller.abort(); return response({ metrics: [] }); } });
  await loadQueryPreviews([job("n", [{ id: "n", op: "count", groupBy: [] }])], () => assert.fail("aborted result published"), controller.signal);
  assert.equal(requests.filter(request => request.path.endsWith("metrics")).length, 1);
});

test("grouped saved views preview the full source before HAVING and result paging", async t => {
 const requests=fixture(t);
 const grouped=core.normalizeQueryState({
  grouping:{source:query([],{where:[{field:'duration_ms',op:'>',value:'200'}],limit:1,offset:99}),keys:['repository_name'],summaries:[{id:'group_rows',op:'count'}]},
  select:[{field:'repository_name'},{field:'group_rows'}],where:[{field:'group_rows',op:'>=',value:'4'}],orderBy:[{field:'repository_name',dir:'asc'}],limit:1,offset:1,
  aggregations:[{id:'groups',op:'count',expression:'COUNT()',groupBy:[]}],
 });
 const preview=await loadQueryPreview('tool_calls',grouped,schemas.tool_calls);
 assert.equal(preview.state,'ready');assert.equal(preview.metrics[0].result.buckets[0].value,2);
 assert.equal(requests.filter(r=>r.path.includes('/query-v3')).length,1);
 assert.equal(requests.filter(r=>r.path.includes('/population')).length,0);
 assert.equal(requests.filter(r=>r.path.includes('/rows-v2')||r.path.endsWith('/metrics')).length,0);
});

test("calendar previews execute on the server and preserve server failures", async t => {
 const population=[{...rows[0],started_at:'2026-03-08T08:00:00Z'},{...rows[1],started_at:'2026-03-09T07:00:00Z'}];
 let complete=true;
 const requests=fixture(t,(path,request)=>path.includes('/query-v3') ? complete ? response(serverQuery(request,population)) : response({error:'The search is incomplete.'},400) : undefined);
 const q=query([{id:'days',op:'count',expression:'COUNT()',groupBy:['started_at'],dateGrouping:{field:'started_at',interval:'day',timeZone:'America/Denver',includeEmpty:true,range:'custom',start:'2026-03-07',end:'2026-03-10'}}]);
 const preview=await loadQueryPreview('tool_calls',q,schemas.tool_calls);
 assert.equal(preview.metrics[0].result.buckets.reduce((sum,bucket)=>sum+bucket.count,0),2);
 assert.ok(preview.metrics[0].result.buckets.some(bucket=>bucket.count===0));
 complete=false;clearPreviewCache();
 await assert.rejects(loadQueryPreview('tool_calls',q,schemas.tool_calls),/incomplete/);
 assert.equal(requests.filter(r=>r.path.endsWith('/metrics')||r.path.includes('/population')).length,0);
});

test("all-time writing hours combine older dates and repeated DST hours into the clock-hour cover", async t => {
  const schema = schemas.writing_messages;
  const population = [
    { id: "old", sent_at: "2025-01-01T08:30:00Z", typed_words: 40 },
    { id: "first", sent_at: "2026-11-01T07:30:00Z", typed_words: 10 },
    { id: "second", sent_at: "2026-11-01T08:30:00Z", typed_words: 20 },
    { id: "automated", sent_at: "2026-11-01T08:30:00Z", typed_words: 0 },
  ];
  let partial = false;
  fixture(t, (path, request) => {
    if (!path.includes("/writing_messages/query-v3")) return;
    const result = sharedServerQuery(request, population, schema);
    if (partial) result.metrics[0].coverage = "partial";
    return response(result);
  });
  const recipe = queryRecipes.find(recipe => recipe.id === "writing-hours"), item = instantiateRecipe(recipe, 7);
  item.query.aggregations[0].dateGrouping.timeZone = "America/Denver";
  const preview = await loadQueryPreview(item.dataset, item.query, schema, { recipe, cover: item.cover, url: item.url });
  assert.equal(preview.state, "ready");
  assert.equal(preview.total, 70);
  assert.equal(preview.study.rows.length, 24);
  assert.equal(preview.study.rows[1].value, 70, "both DST offsets and the prior year contribute to 01:00");
  assert.match(preview.study.sampleLabel, /^3 written messages/);
  partial = true; clearPreviewCache();
  await assert.rejects(loadQueryPreview(item.dataset, item.query, schema, { recipe }), /Complete hourly totals/);
});

test("daily repository words average seven calendar days with history before the 30-day window", async t => {
  const now = new Date(2026, 9, 15, 12).getTime();
  t.mock.timers.enable({ apis: ["Date"], now });
  const recipe = queryRecipes.find(recipe => recipe.id === "human-repository-words-trend"), item = instantiateRecipe(recipe, recipe.days[0]);
  const schema = schemas.writing_messages;
  const dated = (id, date, typed_words, repository_name) => ({ id, sent_at: new Date(date).toISOString(), day: new Date(date).toLocaleDateString("en-CA"), typed_words, repository_name });
  const first = new Date(now); first.setDate(first.getDate() - 30);
  const history = new Date(first); history.setDate(history.getDate() - 1);
  const population = [dated("history", history, 70, "one"), dated("first", first, 140, "one"), dated("other-repo", first, 35, "two"), dated("automated", first, 0, "one")];
  fixture(t, (path, request) => {
    if (path.includes("/writing_messages/metrics")) return response(core.evaluateMetrics(population, query(request.metrics, request), schema, { now }));
  });
  const preview = await loadQueryPreview(item.dataset, item.query, schema, { recipe, window: item.window, cover: item.cover, url: item.url });
  assert.equal(preview.state, "ready");
  assert.equal(preview.total, 175, "smoothing history does not inflate the selected-window total");
  const buckets = compactTimelineBuckets(preview.buckets, preview.timeline);
  assert.equal(buckets[0].values["s:one"], 30, "(70 historical + 140 current) / 7, including five idle days");
  assert.equal(buckets[0].values["s:two"], 5);
  assert.equal(buckets[7].values["s:one"] ?? 0, 0, "old writing rolls out of the trailing window");
  const url = new URL(item.url, "http://preview.test"), chart = JSON.parse(url.searchParams.get("analyze_chart"));
  assert.equal(url.searchParams.get("analyze_days"), "30");
  assert.equal(chart.split, "repository_name");
  assert.equal(chart.rollingPeriods, 7);
});

test("headline numbers represent complete additive measures with their units", async t => {
  fixture(t);
  const metric = { id: "calls", op: "count", groupBy: [], label: "Calls" };
  const complete = await loadQueryPreview("tool_calls", query([metric]), schemas.tool_calls);
  assert.deepEqual(complete.headlineMetric, { value: "10", unit: "calls" });
  const hidden = await loadQueryPreview("tool_calls", query([metric]), schemas.tool_calls, { cover: { mode: "source", metricId: metric.id, showHeadline: false, legendColumns: 2 } });
  assert.equal(hidden.headlineMetric, undefined);
  assert.equal(hidden.legendColumns, 2);
  assert.equal(hidden.metrics[0].result.buckets[0].value, complete.metrics[0].result.buckets[0].value);
  const formatted = await loadQueryPreview("tool_calls", query([{ ...metric, id: "formatted", display: { kind: "value", format: { kind: "number", decimals: 2 } } }]), schemas.tool_calls);
  assert.deepEqual(formatted.headlineMetric, { value: "10.00", unit: "" });
  const zero = await loadQueryPreview("tool_calls", query([{ id: "zero", op: "sum", field: "error_count", groupBy: [] }], { where: [{ field: "error_count", op: "=", value: "0" }] }), schemas.tool_calls);
  assert.equal(zero.headlineMetric.value, "0", "measured zero remains a real headline");
  const average = await loadQueryPreview("tool_calls", query([{ id: "average", op: "avg", field: "duration_ms", groupBy: [] }]), schemas.tool_calls);
  assert.equal(average.headlineMetric, undefined, "observation counts do not become fabricated average headlines");
});

test("truncated and errored measures never produce a headline total", async t => {
  let partial = true;
  const requests = fixture(t, (path, request) => {
    if (path.endsWith("metrics")) return response({ metrics: request.metrics.map(metric => ({ id: metric.id, coverage: partial ? "partial" : "exact", groupCount: partial ? 20 : 2, buckets: [{ keys: ["Bash"], value: 5, count: 5 }, ...partial ? [] : [{ keys: ["Read"], value: null, count: 1, error: "Cannot calculate" }]], processedRows: 100 })) });
  });
  const metric = { id: "calls", op: "count", groupBy: ["tool_name"] };
  const preview = await loadQueryPreview("tool_calls", query([metric]), schemas.tool_calls);
  assert.equal(preview.headlineMetric, undefined);
  assert.match(preview.summary, /^Shown:/);
  assert.match(preview.note, /Partial results/);
  assert.doesNotMatch(preview.note, /groups returned|\d+ of \d+/);
  partial = false;
  const errored = await loadQueryPreview("tool_calls", query([{ ...metric, id: "errored" }]), schemas.tool_calls);
  assert.equal(errored.headlineMetric, undefined);
  assert.match(errored.note, /calculation errors/);
  assert.ok(requests.length);
});


test("labelled covers fetch friendly titles beyond twelve visible groups", async t => {
 const population=Array.from({length:25},(_,i)=>({id:`u-${i}`,workspace_id:`w-${i}`,root_conversation_id:`root-${i}`,root_repository_name:'repo',root_title:`Friendly work ${i}`,agent_session_id:`s-${i}`,title:`Friendly work ${i}`,first_usage_at:Date.now()-1000,last_usage_at:new Date().toISOString(),total_tokens:1000-i,cost_usd:100-i}));
 const requests=fixture(t,(path,request)=>{
  if(!path.includes("/usage/query-v3"))return;
  const state={...core.EMPTY_QUERY,...request.result,select:request.result.select.map(field=>({field})),aggregations:request.metrics,grouping:{...request.grouping,source:{...core.EMPTY_QUERY,...request.source,select:request.source.select.map(field=>({field}))}}};
  const prepared=core.prepareGrouping(population,schemas.usage,state.grouping,{now:request.now});
  const page=core.applyQuery(population,state,schemas.usage,{now:request.now});
  return response({version:3,now:request.now,sourceTotal:population.length,...page,metrics:core.evaluateMetrics(population,state,schemas.usage,{now:request.now,shownRows:page.rows}).metrics,execution,schema:{name:prepared.schema.name,idField:prepared.schema.idField,fields:prepared.schema.fields.map(({name,type})=>({name,type}))}});
 });
 const recipe=queryRecipes.find(recipe=>recipe.id==="usage-work"),item=instantiateRecipe(recipe,7),cover={mode:"result",metricId:recipe.exploration.valueField,maxGroups:20};
 const preview=await loadQueryPreview("usage",item.query,schemas.usage,{recipe,cover});
 assert.equal(requests.find(({path})=>path.includes("/usage/query-v3")).request.result.limit,20);
 assert.equal(Object.keys(preview.groupLabels).length,20);
 assert.equal(preview.groupLabels[JSON.stringify(["repo","root-19"])],"Friendly work 19 · repo");
});

test("cost history shares identical loads, preserves mixed windows, and refetches truncated timestamp ranges", async t => {
 const now=Date.now(),d=86400000,population=[{id:'recent',first_usage_at:new Date(now-d).toISOString(),total_tokens:1,price_status:'priced',cost_usd:70},{id:'older',first_usage_at:new Date(now-20*d).toISOString(),total_tokens:1,price_status:'priced',cost_usd:140}];
 let truncated=false;
 const requests=fixture(t,(path,request)=>{
  if(!path.endsWith('/usage/metrics'))return;
  assert.deepEqual(request.metrics[0].groupBy,['first_usage_at']);
  const body=core.evaluateMetrics(population,{...core.EMPTY_QUERY,...request,aggregations:request.metrics},schemas.usage);
  if(!truncated){truncated=true;body.metrics[0].groupCount=10001;body.metrics[0].buckets=body.metrics[0].buckets.slice(0,1);}
  return response(body);
 });
 const recipe=queryRecipes.find(r=>r.id==='api-cost-total');
 const jobs=[7,7,30].map((days,i)=>{const item=instantiateRecipe(recipe,days);return{id:String(i),dataset:'usage',query:item.query,schema:schemas.usage,options:{recipe:item,window:item.window,cover:item.cover}};});
 const result=new Map();await loadQueryPreviews(jobs,(id,preview,error)=>{assert.equal(error,undefined);result.set(id,preview);});
 assert.equal(result.get('0').study.value,70);assert.equal(result.get('1').study.value,70);assert.equal(result.get('2').study.value,210);assert.equal(result.get('0').study.lookbackDays,7);assert.equal(result.get('2').study.lookbackDays,7);assert.equal(result.get('0').study.points.at(-1),10);
 assert.equal(requests.filter(r=>r.path.endsWith('/usage/metrics')).length,4,'two periods plus the two replacement ranges; the duplicate seven-day card shares its load');
 assert.equal(requests.filter(r=>r.path.includes('/rows')||r.path.includes('/query-v3')).length,0,'only reduced timestamp totals are downloaded');
});

test("partial cost history is rejected instead of manufacturing a total", async t => {
 fixture(t,(path,request)=>path.endsWith('/usage/metrics')?response({metrics:[{id:request.metrics[0].id,coverage:'partial',buckets:[],processedRows:0}]}):undefined);
 const recipe=queryRecipes.find(r=>r.id==='api-cost-total'),item=instantiateRecipe(recipe,7);
 await assert.rejects(loadQueryPreview('usage',item.query,schemas.usage,{recipe:item,window:item.window,cover:item.cover}),/Complete API cost history/);
});

test("model tail previews apply task-count support before the visible point budget and keep sparse tasks in destinations", async t => {
 const now=Date.now();let population=[{id:'sparse',root_conversation_id:'sparse',root_provider:'codex',root_model_family:'A sparse',first_usage_at:now-1000,total_tokens:100000},...Array.from({length:3},(_,i)=>({id:`root-${i}`,root_conversation_id:`root-${i}`,root_provider:'codex',root_model_family:'B supported',first_usage_at:now-1000,total_tokens:(i+1)*100}))];
 fixture(t,(path,request)=>{
  if(!path.includes('/usage/query-v3'))return;
  const state={...core.EMPTY_QUERY,...request.result,select:request.result.select.map(field=>({field})),aggregations:request.metrics,grouping:{...request.grouping,source:{...core.EMPTY_QUERY,...request.source,select:request.source.select.map(field=>({field}))}}};
  const prepared=core.prepareGrouping(population,schemas.usage,state.grouping,{now:request.now}),page=core.applyQuery(population,state,schemas.usage,{now:request.now});
  return response({version:3,now:request.now,sourceTotal:population.length,...page,metrics:core.evaluateMetrics(population,state,schemas.usage,{now:request.now,shownRows:page.rows}).metrics,execution,schema:{name:prepared.schema.name,idField:prepared.schema.idField,fields:prepared.schema.fields.map(({name,type})=>({name,type}))}});
 });
 const recipe=queryRecipes.find(r=>r.id==='model-task-token-spread'),item=instantiateRecipe(recipe,7),cover={...recipe.cover,maxGroups:1};
 const preview=await loadQueryPreview('usage',item.query,schemas.usage,{recipe,cover,window:item.window});assert.equal(preview.state,'ready');assert.equal(preview.groupSummary,undefined,'scatter support and omission summaries are not hidden as ranked-list captions');assert.equal(preview.metrics[0].result.buckets.length,1);assert.deepEqual(preview.metrics[0].result.buckets[0].keys,['codex','B supported']);assert.equal(preview.metrics[0].result.buckets[0].count,3);assert.equal(preview.metrics[0].clause.display.maxGroups,1);assert.equal(core.applyQuery(population,item.query,schemas.usage,{now}).total,4);
 population=population.slice(0,1);clearPreviewCache();const empty=await loadQueryPreview('usage',item.query,schemas.usage,{recipe,cover,window:item.window});assert.equal(empty.state,'empty');assert.match(empty.emptyLabel,/at least 3 root tasks/);
});

test("metric filters and minimum group sizes run only where advertised and disclose hidden groups", async t => {
  const errors = [{ field: "error_count", op: ">", value: "0" }];
  const measures = [{ id: "calls", op: "sum", field: "call_count", groupBy: ["repository_name"], minCount: 4, where: [{ field: "duration_ms", op: ">=", value: "400" }] }];
  // Without the handshake flags the card fails on its own instead of dropping the controls.
  fixture(t);
  await assert.rejects(loadQueryPreview("tool_calls", query(measures), schemas.tool_calls), /filter|minimum|minCount/i);
  await assert.rejects(loadQueryPreview("tool_calls", query([{ id: "errors", op: "count", groupBy: [], where: errors }]), schemas.tool_calls), /filter/i);
  clearPreviewCache();
  fixture(t, path => path.endsWith("capabilities") ? response({ metricCapabilities: { version: 2, expressions: true, distributions: true, filters: true, minCount: true }, computedExecution: execution, queryCapabilities }) : undefined);
  const errorsOnly = await loadQueryPreview("tool_calls", query([{ id: "errors", op: "count", groupBy: [], where: errors }]), schemas.tool_calls);
  assert.equal(errorsOnly.total, 2);
  // From 400 ms, "one" keeps four calls and "two" three: minCount 4 hides "two",
  // so the shown sum is not presented as a complete total.
  const preview = await loadQueryPreview("tool_calls", query(measures), schemas.tool_calls);
  assert.equal(preview.metrics[0].result.hiddenGroups, 1);
  assert.deepEqual(preview.metrics[0].result.buckets.map(bucket => [bucket.keys[0], bucket.value]), [["one", 4]]);
  assert.match(preview.note, /1 group hidden \(fewer than 4 rows\)/);
  assert.equal(preview.headlineMetric, undefined);
  assert.match(preview.summary, /^Shown: /);
});

test("grouped previews run distinct minimums only where query-v3 advertises them", async t => {
  const measures = [{ id: "calls", op: "count", groupBy: ["started_at"], dateGrouping: { field: "started_at", interval: "day", range: "observed", includeEmpty: false }, minCount: { min: 1, distinct: "conversation_id" } }];
  const dated = rows.map(row => ({ ...row, started_at: new Date(row.started_at).toISOString() }));
  const handshake = flags => (path, request) => path.endsWith("capabilities") ? response({ metricCapabilities: { version: 2 }, computedExecution: execution, queryCapabilities: { ...queryCapabilities, metricMinCount: true, ...flags } })
    : path.includes("query-v3") ? response(serverQuery(request, dated)) : undefined;
  fixture(t, handshake({}));
  await assert.rejects(loadQueryPreview("tool_calls", query(measures), schemas.tool_calls), /distinct values or a sum/);
  fixture(t, handshake({ metricMinCountMeasures: true }));
  const preview = await loadQueryPreview("tool_calls", query(measures), schemas.tool_calls);
  assert.equal(preview.state, "ready");
});

test("grouped cards with link columns execute without sending the links", async t => {
  const requests = fixture(t);
  const source = { ...core.EMPTY_QUERY };
  const grouped = { ...core.withGroupingKeys(source, ["repository_name"]), select: [{ field: "open" }, { field: "calls" }] };
  grouped.grouping = { ...grouped.grouping, summaries: [{ id: "calls", op: "sum", field: "call_count" },
    { id: "open", op: "link", label: "Repository", link: { label: { column: "repository_name" }, to: { kind: "query", target: "tool_calls" } } }] };
  const preview = await loadQueryPreview("tool_calls", grouped, schemas.tool_calls);
  assert.equal(preview.state, "ready");
  const request = requests.find(item => item.path.includes("query-v3")).request;
  assert.deepEqual(request.grouping.summaries.map(column => column.id), ["calls"]);
  // Metric cards over the same grouping also run.
  const measured = await loadQueryPreview("tool_calls", { ...grouped, aggregations: [{ id: "total", op: "sum", field: "calls", groupBy: [] }] }, schemas.tool_calls);
  assert.equal(measured.total, 10);
});

test("parallel hours by day headline is the share of active hours with several agents", async t => {
  const population = [["2026-10-19", "1", 6], ["2026-10-19", "2", 2], ["2026-10-20", "3", 1], ["2026-10-20", "4+", 1]].map(([day, level_band, elapsed_hours], i) => ({ id: String(i), day, level_band, elapsed_hours }));
  fixture(t, (path, request) => path.endsWith("/metrics") ? response(core.evaluateMetrics(population, query(request.metrics, request), schemas.parallelism)) : undefined);
  const recipe = queryRecipes.find(recipe => recipe.id === "parallel-days");
  const preview = await loadQueryPreview("parallelism", query(recipe.measures), schemas.parallelism, { recipe });
  assert.equal(preview.headlineMetric.value, "40%");
  assert.match(preview.headlineMetric.label, /2\+ agents/);
});

// Card covers can be shown from a prior visit while canonical queries refresh.
import { cardPreviewKey, invalidateCardPreviews, loadCardPreviews } from "../../../src/card-previews";
import { createCardPreviewCache } from "../../../src/card-preview-cache";

test("cached card covers revalidate and remain visible after refresh failure", async t => {
  const priorWindow = global.window;
  global.window = { pharosPreviewScope: t.name };
  t.after(() => { global.window = priorWindow; });
  let fail = false, release, requestStarted;
  const refreshingRequest = new Promise(resolve => { requestStarted = resolve; });
  const requests = fixture(t, async path => {
    if (fail && path.endsWith("metrics")) {
      await new Promise(resolve => { release = resolve; requestStarted(); });
      return response({ error: "Offline" }, 503);
    }
  });
  const item = job("saved", [{ id: "total", op: "sum", field: "duration_ms", groupBy: [] }]);
  const initial = [];
  await loadCardPreviews([item], (_id, preview, error) => { assert.ifError(error); initial.push(preview); });
  assert.equal(initial.length, 1);
  assert.equal(initial[0].metrics[0].result.buckets[0].value, 5500);
  const before = requests.length, hit = [];
  await loadCardPreviews([{ ...item, id: "catalog" }], (_id, preview, error) => { assert.ifError(error); hit.push(preview); });
  assert.equal(requests.length, before, "a recent identical cover is shared across caller IDs");
  assert.equal(hit[0].refreshing, false);

  clearPreviewCache(); invalidateCardPreviews(); fail = true;
  const updates = [];
  let sawCached;
  const cached = new Promise(resolve => { sawCached = resolve; });
  const refresh = loadCardPreviews([item], (_id, preview, error) => {
    assert.ifError(error); updates.push(preview);
    if (preview?.refreshing) sawCached();
  });
  await cached;
  assert.equal(updates[0].metrics[0].result.buckets[0].value, 5500, "the chart remains available during revalidation");
  // Wait for the intercepted request rather than a timer.
  await refreshingRequest;
  release(); await refresh;
  assert.equal(updates.at(-1).refreshing, false);
  assert.match(updates.at(-1).refreshError, /Offline/);
  assert.equal(updates.at(-1).metrics[0].result.buckets[0].value, 5500);
});

test("card preview identity separates query, cover, template, schema and search changes", async () => {
  const item = job("one", [{ id: "total", op: "sum", field: "duration_ms", groupBy: [] }]);
  const key = await cardPreviewKey(item);
  assert.equal(key, await cardPreviewKey({ ...item, id: "two" }));
  assert.equal(key, await cardPreviewKey(Object.fromEntries(Object.entries(item).reverse())), "property order does not change a cover");
  for (const changed of [
    { ...item, query: { ...item.query, where: [{ field: "repository_name", op: "=", value: "one" }] } },
    { ...item, options: { coverMetricId: "other" } },
    { ...item, options: { url: "/library?search=cache" } },
    { ...item, schema: { ...item.schema, fields: item.schema.fields.slice(1) } },
    { ...item, options: { recipe: { ...queryRecipes[0], title: "Edited" } } },
  ]) assert.notEqual(key, await cardPreviewKey(changed));
});

test("persisted covers are stale on a new page and storage failures do not block live results", async t => {
  let saved = [];
  const storage = { read: async () => saved, write: async entries => { saved = entries; } };
  const first = createCardPreviewCache(storage);
  await first.ready;
  first.put("one", { state: "empty", total: 0, summary: "No observations", refreshing: true });
  assert.equal(first.get("one").fresh, true);
  await first.save();
  assert.equal(saved[0].preview.refreshing, undefined, "transient refresh status is never persisted");
  const next = createCardPreviewCache(storage);
  await next.ready;
  assert.equal(next.get("one").fresh, false);
  assert.equal(next.get("one").preview.state, "empty", "empty covers are cached too");
  const generation = next.generation();
  next.invalidate(); next.put("one", { state: "ready", total: 1, summary: "New" }, generation);
  assert.equal(next.get("one").fresh, false, "an older in-flight computation cannot undo invalidation");
  const broken = createCardPreviewCache({ read: async () => { throw Error("blocked"); }, write: async () => { throw Error("full"); } });
  await broken.ready;
  broken.put("one", { state: "ready", total: 1, summary: "Live" });
  await broken.save();
  assert.equal(broken.get("one").preview.summary, "Live");
});

test("grouped card requests do not wait for every sibling in their metrics batch", async t => {
  const waiting = [], completed = new Map();
  let started, release;
  const twoStarted = new Promise(resolve => { started = resolve; });
  const requests = fixture(t, async path => {
    if (!path.includes("query-v3")) return;
    waiting.push(new Promise(resolve => { if (!release) release = resolve; else resolve(); }));
    if (waiting.length === 2) started();
    await waiting.at(-1);
  });
  const items = Array.from({ length: 3 }, (_, i) => job(String(i), [{ id: "metric", op: "sum", field: "total", groupBy: [] }], {}, {
    grouping: { source: core.EMPTY_QUERY, keys: ["repository_name"], summaries: [{ id: "total", op: "sum", field: "duration_ms" }], representative: null },
  }));
  const work = loadQueryPreviews(items, (id, preview, error) => { assert.ifError(error); completed.set(id, preview); });
  await twoStarted;
  assert.ok(requests.filter(request => request.path.includes("query-v3")).length >= 2, "another grouped query can start while the first is pending");
  release(); await work;
  assert.equal(completed.size, 3);
});


test("card snapshot retention is bounded and expired or malformed entries are ignored", async () => {
  const now = Date.now();
  let saved = [
    { key: "expired", at: now - 8 * 86400000, preview: { state: "ready" } },
    { key: "future", at: now + 86400000, preview: { state: "ready" } },
    { key: "bad", at: now, preview: { state: "broken" } },
    { key: "one", at: now - 2000, preview: { state: "empty", total: 0, summary: "One" } },
    { key: "two", at: now - 1000, preview: { state: "empty", total: 0, summary: "Two" } },
  ];
  const store = createCardPreviewCache({ read: async () => saved, write: async entries => { saved = entries; } }, { entries: 2, bytes: 1000 });
  await store.ready;
  for (const key of ["expired", "future", "bad"]) assert.equal(store.get(key), undefined);
  store.put("three", { state: "ready", total: 1, summary: "Three" });
  store.put("huge", { state: "ready", total: 1, summary: "x".repeat(1000) });
  await store.save();
  assert.deepEqual(saved.map(entry => entry.key), ["three", "two"]);
  assert.equal(store.get("one"), undefined);
  assert.equal(store.get("huge"), undefined);
});

test("cancelled card loads never publish a cached result or issue requests", async t => {
  const priorWindow = global.window;
  global.window = { pharosPreviewScope: t.name };
  t.after(() => { global.window = priorWindow; });
  const requests = fixture(t);
  const item = job("saved", [{ id: "total", op: "sum", field: "duration_ms", groupBy: [] }]);
  await loadCardPreviews([item], () => {});
  const before = requests.length, controller = new AbortController();
  let published = 0;
  const work = loadCardPreviews([item], () => { published++; }, controller.signal);
  controller.abort(); await work;
  assert.equal(published, 0);
  assert.equal(requests.length, before);
  global.window = { pharosPreviewScope: `${t.name}-other-library` };
  await loadCardPreviews([item], () => {});
  assert.ok(requests.length > before, "another library cannot reuse the saved cover");
});


test("scrolling promotes newly visible cards ahead of the remaining cold queue", async t => {
  let promoted = false, release, began;
  const gate = new Promise(resolve => { release = resolve; });
  const started = new Promise(resolve => { began = resolve; });
  const order = [];
  fixture(t, async (path, request) => {
    if (!path.endsWith("rows-v2")) return;
    order.push(request.where[0].value);
    if (order.length <= 3) { if (order.length === 3) began(); await gate; }
  });
  const jobs = Array.from({ length: 6 }, (_, index) => ({
    ...job(String(index), [], {}, { where: [{ field: "id", op: "=", value: String(index) }] }),
    priority: () => promoted && index === 5 ? 0 : 1,
  }));
  const work = loadQueryPreviews(jobs, (_id, _preview, error) => assert.ifError(error));
  await started; promoted = true; release(); await work;
  assert.deepEqual(order.slice(0, 4), ["0", "1", "2", "5"]);
});


test("a stalled browser cache cannot delay live card queries indefinitely", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const store = createCardPreviewCache({ read: () => new Promise(() => {}), write: async () => {} });
  t.mock.timers.tick(200);
  await store.ready;
  store.put("live", { state: "ready", total: 1, summary: "Live" });
  assert.equal(store.get("live").fresh, true);
});
