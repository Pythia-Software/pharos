import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { build } from "esbuild";
import * as core from "@pythia-software/query-table-core";

const compiled = await build({ entryPoints: ["src/curiosity-data.ts"], bundle: true, write: false, platform: "node", format: "esm" });
const { queryRecipes, instantiateRecipe, loadQueryPreviews, loadQueryPreview, clearPreviewCache } = await import(`data:text/javascript;base64,${Buffer.from(compiled.outputFiles[0].text).toString("base64")}`);
const schemas = Object.fromEntries(["tool_calls", "skill_usages", "writing_messages", "usage", "conversation_activity", "library", "tl1_attempts", "mcp_calls"].map(dataset => [dataset, core.loadSchema(JSON.parse(readFileSync(`../schemas/${dataset}.schema.json`, "utf8")))]));
const execution = { profile: "qt-sqlite-v1", planToken: "", fields: {}, resolvedRevisions: {} };
const rows = Array.from({ length: 10 }, (_, i) => ({ id: String(i), call_count: 1, error_count: i < 2 ? 1 : 0, tool_name: "Bash", command_name: "go test", duration_ms: (i + 1) * 100, result_tokens: i * 10, result_tokens_source: i % 2 ? "estimated" : "measured", repository_name: i % 2 ? "one" : "two", conversation_id: String(i), started_at: Date.now() }));
function query(aggregations, extra = {}) { return { ...core.EMPTY_QUERY, aggregations, ...extra }; }
function job(id, aggregations, options = {}, extra = {}) { return { id, dataset: "tool_calls", schema: schemas.tool_calls, query: query(aggregations, extra), options }; }
const response = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const queryCapabilities = { version: 3, profile: execution.profile, groupedRows: true, calendarGrouping: true, computedSourceFilters: true, regex: false, maxRows: 10000, maxGroups: 10000, maxPopulation: 250000 };
function serverQuery(request, population = rows) {
  const state = { ...core.EMPTY_QUERY, ...request.result, select: request.result.select.map(field => ({ field })), aggregations: request.metrics,
    ...(request.grouping ? { grouping: { ...request.grouping, source: { ...core.EMPTY_QUERY, ...request.source, select: request.source.select.map(field => ({ field })) } } } : {}) };
  const prepared = state.grouping ? core.prepareGrouping(population, schemas.tool_calls, state.grouping, { now: request.now }) : undefined;
  const schema = prepared?.schema ?? schemas.tool_calls;
  const page = core.applyQuery(population, state, schemas.tool_calls, { now: request.now });
  const metrics = core.evaluateMetrics(population, state, schemas.tool_calls, { now: request.now, shownRows: page.rows });
  return { version: 3, now: request.now, sourceTotal: prepared?.inputs.length ?? population.length, rows: request.rows ? page.rows : [], total: page.total, metrics: metrics.metrics,
    schema: { name: schema.name, idField: schema.idField, fields: schema.fields.map(({ name, type }) => ({ name, type })) }, execution };
}
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

test("every recipe projects completely to its execution contract and matches the tracker", () => {
  const tracker = JSON.parse(readFileSync("../docs/homepage-query-cards.json", "utf8"));
  assert.deepEqual(tracker.implemented_recipes, JSON.parse(JSON.stringify(queryRecipes)));
  for (const recipe of queryRecipes) {
    const state = query(recipe.measures, { where: recipe.where ?? [] });
    const request = recipe.measures.some(metric => metric.dateGrouping)
      ? core.toQueryRequestV3(state, schemas[recipe.dataset], queryCapabilities, execution, { now: Date.now(), rows: false })
      : core.toMetricQuery(state, schemas[recipe.dataset], execution);
    assert.deepEqual(request.diagnostics, [], recipe.id);
    assert.equal(request.metrics.length, recipe.measures.length, recipe.id);
  }
});

test("failure rate includes successful calls and preserves all dashboard results", async t => {
  const requests = fixture(t), recipe = queryRecipes.find(recipe => recipe.id === "tool-failure-rates");
  const preview = await loadQueryPreview(recipe.dataset, query(recipe.measures), schemas.tool_calls, { recipe, suggested: true });
  assert.equal(preview.state, "ready");
  assert.equal(preview.metrics[0].result.buckets[0].value, 0.2);
  assert.equal(preview.metrics[1].result.buckets[0].value, 2);
  assert.equal(preview.metrics[2].result.buckets[0].value, 10);
  assert.match(preview.summary, /10 observations/);
  assert.equal(requests.filter(request => request.path.endsWith("metrics")).length, 1);
  assert.ok(requests.every(request => !request.path.endsWith("aggregations")));
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

test("distribution recipes include recorded zeros and keep output provenance in every summary", async t => {
  fixture(t, (path, request) => {
    if (!path.endsWith("metrics")) return;
    const observations = [...rows, { ...rows[0], id: "zero", duration_ms: 0, result_tokens: 0 }];
    return response(core.evaluateMetrics(observations, query(request.metrics.map(metric => ({ ...metric, ...(metric.distribution ? { display: { kind: metric.distribution.kind } } : {}) })), request), schemas.tool_calls));
  });
  const duration = queryRecipes.find(recipe => recipe.id === "duration-distribution");
  const preview = await loadQueryPreview(duration.dataset, query(duration.measures, { where: duration.where }), schemas.tool_calls);
  assert.equal(preview.metrics[0].result.buckets[0].distribution.summary.n, 11);
  assert.equal(preview.metrics[0].result.buckets[0].distribution.summary.median, 500);
  const output = queryRecipes.find(recipe => recipe.id === "output-distribution");
  const distribution = await loadQueryPreview(output.dataset, query(output.measures, { where: output.where }), schemas.tool_calls);
  assert.deepEqual(distribution.metrics[1].clause.groupBy, ["tool_name", "result_tokens_source"]);
  assert.deepEqual(distribution.metrics[1].result.buckets.map(bucket => bucket.keys.at(-1)).sort(), ["estimated", "measured"]);
  assert.equal(distribution.metrics[1].result.buckets.reduce((n, bucket) => n + bucket.distribution.summary.n, 0), 11);
});

test("changing the cover to evidence counts uses the selected metric's unit", async t => {
  fixture(t, (path, request) => path.endsWith("metrics") ? response({ metrics: request.metrics.map(metric => ({ id: metric.id, buckets: [{ keys: metric.groupBy.map(() => "review"), value: 10, count: 10 }], processedRows: 10 })) }) : undefined);
  const recipe = queryRecipes.find(recipe => recipe.id === "skill-reach");
  const preview = await loadQueryPreview(recipe.dataset, query(recipe.measures), schemas.skill_usages, { recipe, coverMetricId: "skill-evidence-types" });
  assert.equal(preview.summary, "10 records");
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
  const requests = fixture(t, path => path.includes("rows-v2") ? response({ total: 0, rows: [], find: { search: { complete: false } } }) : undefined);
  const draft = { editor: "visual", match: "rules", text: "", mode: "advanced", fuzzy: false, caseSensitive: false, separators: false,
    root: { kind: "group", join: "any", exclude: false, children: ["cache", "buffer"].map(text => ({ kind: "words", text, other: "", distance: "8", exclude: false })) },
    scope: { surface: "user", roles: [], kinds: [], repository: "pharos", provider: "", source: "", from: "", to: "", conversation_id: "", workspace_id: "" } };
  const url = `/usage?${new URLSearchParams({ message_query: JSON.stringify(draft), message_cursor: "saved-cursor" })}`;
  const preview = await loadQueryPreview("writing_messages", query([]), schemas.writing_messages, { url });
  const params = new URL(requests.find(r => r.path.includes("rows-v2")).path, global.location.origin).searchParams;
  assert.deepEqual(JSON.parse(params.get("find_where")), { op: "or", children: [{ op: "term", text: "cache" }, { op: "term", text: "buffer" }] });
  assert.equal(params.get("find_surface"), "user");
  assert.equal(params.get("find_repository"), "pharos");
  assert.equal(params.get("find_cursor"), "saved-cursor");
  assert.match(preview.note, /Partial search batch/);
  assert.equal(preview.summary, "No matches in this search batch");
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
 const population=Array.from({length:25},(_,i)=>({id:`u-${i}`,workspace_id:`w-${i}`,agent_session_id:`s-${i}`,title:`Friendly work ${i}`,first_usage_at:Date.now()-1000,last_usage_at:new Date().toISOString(),total_tokens:1000-i,cost_usd:100-i}));
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
 assert.equal(preview.groupLabels[JSON.stringify(["w-19"])],"Friendly work 19");
});
