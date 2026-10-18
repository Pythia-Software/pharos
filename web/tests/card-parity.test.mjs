import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { buildSync } from "esbuild";
import * as core from "@pythia-software/query-table-core";

const require = createRequire(import.meta.url);
const bundled = buildSync({ stdin: { contents: `
  export * from './src/curiosity-data';
  export * from './src/card-cover';
  export * from './src/card-loading-model';
  export * from './src/card-library-model';
  export * from './src/bookmarks';
  export * from './src/query-links';
`, resolveDir: process.cwd() }, bundle: true, platform: "node", format: "cjs", write: false });
const loaded = { exports: {} };
new Function("module", "exports", "require", bundled.outputFiles[0].text)(loaded, loaded.exports, require);
const { queryRecipes, instantiateRecipe, defaultCardCover, coverChoices, decodeCover, availableChartKinds,
  cardLoadingPresentation, collectQuery, queryFromURL, loadQueryPreview, clearPreviewCache, updateBookmark, saveCardTemplate, cardTemplates } = loaded.exports;
const active = queryRecipes.filter(recipe => !recipe.retired);
const schemas = Object.fromEntries([...new Set(queryRecipes.map(recipe => recipe.dataset)), "library"].map(dataset => [dataset, core.loadSchema(JSON.parse(readFileSync(`../schemas/${dataset}.schema.json`, "utf8")))]));
const execution = { profile: "qt-sqlite-v1", planToken: "", fields: {}, resolvedRevisions: {} };
const queryCapabilities = { version: 3, profile: execution.profile, groupedRows: true, calendarGrouping: true, computedSourceFilters: true, regex: false, maxRows: 10000, maxGroups: 10000, maxPopulation: 250000 };
const now = Date.parse("2026-10-16T18:00:00Z"), day = 86400000;
const json = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value));

function population(schema) {
  return Array.from({ length: 96 }, (_, index) => {
    const at = new Date(now - (index % 40 + .25) * day).toISOString();
    const row = Object.fromEntries(schema.fields.map(field => [field.name, field.type === "datetime" ? at : field.type === "number" ? (index + 1) * 100 : field.type === "bool" ? index % 2 === 0 : `${field.name}-${index % 3}`]));
    return { ...row, id: `row-${index}`, day: at.slice(0, 10), title: `Work ${index % 8}`, conversation_id: `conversation-${index % 8}`, root_conversation_id: `conversation-${index % 8}`,
      work_id: `row-${index % 8}`, workspace_id: `row-${index % 8}`, location: `/repo/dir-${index % 4}`, repository_name: `repo-${index % 3}`, root_repository_name: `repo-${index % 3}`,
      provider: index % 2 ? "codex" : "claude", root_provider: index % 2 ? "codex" : "claude", session_kind: index % 3 ? "root" : "subagent",
      session_role: index % 3 ? "root" : "subagent", price_status: "priced", tool_name: ["Read", "Bash", "Edit"][index % 3], tool_category: ["read", "command", "edit", "web"][index % 4],
      status: index % 2 ? "ok" : "error", error_count: index % 2 ? 0 : 1, call_count: 1, result_tokens_source: "measured", evidence_type: "body-load",
      text: "Replace 3 fields because they repeat.", text_truncated: false, segments: [{ category: "typed", text: "Replace 3 fields because they repeat." }], pasted_words: 0, attachment_words: 0 };
  });
}

function fixture(t, empty = false) {
  const prior = { fetch: global.fetch, window: global.window, location: global.location };
  const values = new Map(), requests = [];
  const populations = Object.fromEntries(Object.entries(schemas).map(([dataset, schema]) => [dataset, empty ? [] : population(schema)]));
  global.location = { origin: "http://parity.test" };
  global.window = { dispatchEvent() {}, pharosPrefs: { get: (key, fallback) => values.get(key) ?? fallback, set: (key, value) => values.set(key, json(value)), remove: key => values.delete(key), keys: prefix => [...values.keys()].filter(key => key.startsWith(prefix)) } };
  t.mock.timers.enable({ apis: ["Date"], now });
  global.fetch = async (url, init = {}) => {
    const path = String(url), request = init.body ? JSON.parse(init.body) : undefined;
    requests.push({ path, request });
    if (path.includes("computed-columns")) return Response.json([]);
    if (path.endsWith("capabilities")) return Response.json({ metricCapabilities: { version: 2, expressions: true, distributions: true }, computedExecution: execution, queryCapabilities });
    const dataset = path.split("/")[3], schema = schemas[dataset], rows = populations[dataset];
    assert.ok(schema, `Unexpected request: ${path}`);
    if (path.endsWith("/query-v3")) {
      const state = { ...core.EMPTY_QUERY, ...request.result, select: request.result.select.map(field => ({ field })), aggregations: request.metrics,
        ...(request.grouping ? { grouping: { ...request.grouping, source: { ...core.EMPTY_QUERY, ...request.source, select: request.source.select.map(field => ({ field })) } } } : {}) };
      const prepared = state.grouping ? core.prepareGrouping(rows, schema, state.grouping, { now: request.now }) : undefined;
      const projected = prepared?.schema ?? schema, page = core.applyQuery(rows, state, schema, { now: request.now });
      return Response.json({ version: 3, now: request.now, sourceTotal: prepared?.inputs.length ?? rows.length, rows: request.rows ? page.rows : [], total: page.total,
        metrics: core.evaluateMetrics(rows, state, schema, { now: request.now, shownRows: page.rows }).metrics, execution,
        schema: { name: projected.name, idField: projected.idField, fields: projected.fields.map(({ name, type }) => ({ name, type })) } });
    }
    if (path.endsWith("/metrics")) return Response.json(core.evaluateMetrics(rows, { ...core.EMPTY_QUERY, ...request, aggregations: request.metrics }, schema, { now }));
    assert.ok(path.endsWith("/rows-v2"), path);
    return Response.json(core.applyQuery(rows, { ...core.EMPTY_QUERY, ...request }, schema, { now }));
  };
  t.after(() => { Object.assign(global, prior); clearPreviewCache(); });
  return { values, requests };
}

function difference(left, right, path = "") {
  if (Object.is(left, right)) return;
  if (!left || !right || typeof left !== "object" || typeof right !== "object") return `${path}: ${JSON.stringify(left)} != ${JSON.stringify(right)}`;
  for (const key of new Set([...Object.keys(left), ...Object.keys(right)])) {
    const found = difference(left[key], right[key], `${path}/${key}`);
    if (found) return found;
  }
}

async function compare(recipe, days, values, { edit = false } = {}) {
  values.clear();
  const item = instantiateRecipe(recipe, days), canonical = queryRecipes.find(candidate => candidate.id === recipe.id);
  const saved = collectQuery(item.dataset, item.query, item.title, { description: item.description, templateId: item.instanceId, window: item.window, cover: item.cover ?? defaultCardCover(item) });
  const bookmark = edit ? updateBookmark(saved.id, saved.title, saved.description, days, saved.cover?.metricId ?? "") : saved;
  const spec = queryFromURL(bookmark.url, json(bookmark.query));
  assert.ok(spec, recipe.id);
  assert.deepEqual(core.normalizeQueryState(spec.query), core.normalizeQueryState(item.query), `${recipe.id}: saved query`);
  const catalogOptions = { recipe: item, cover: item.cover, window: item.window, url: item.url };
  const savedOptions = { recipe: canonical, cover: bookmark.cover, coverMetricId: bookmark.coverMetricId, window: spec.window, url: bookmark.url };
  const failures = [];
  async function preview(query, options) {
    clearPreviewCache();
    try { return json(await loadQueryPreview(item.dataset, query, schemas[item.dataset], options)); }
    catch (error) { failures.push(error.message); return { error: error.message }; }
  }
  const catalog = await preview(item.query, catalogOptions), dashboard = await preview(spec.query, savedOptions);
  if (catalog.study && item.cover?.height !== undefined) assert.equal(catalog.coverHeight, item.cover.height, `${recipe.id}: customized study height`);
  const loading = difference(json(cardLoadingPresentation(item.query, { ...catalogOptions, days })), json(cardLoadingPresentation(spec.query, { ...savedOptions, metricId: bookmark.coverMetricId, days })));
  return { preview: difference(catalog, dashboard), loading, failures, state: catalog.state };
}

for (const empty of [false, true]) test(`all card definitions retain preview results and loading shapes after collection (${empty ? "empty" : "populated"})`, async t => {
  const { values } = fixture(t, empty), failures = [];
  let ready = 0, cases = 0;
  for (const recipe of queryRecipes) for (const days of [1, 7, 30, 90]) {
    const result = await compare(recipe, days, values);
    cases++; if (result.state === "ready") ready++;
    if (result.preview || result.loading || result.failures.length) failures.push({ id: recipe.id, days, ...result });
  }
  t.diagnostic(`${queryRecipes.length} definitions × 4 windows: ${cases} comparisons; ${ready} populated previews, ${cases - ready} empty previews`);
  assert.deepEqual(failures, []);
});

test("collecting all active cards preserves distinct studies sharing the same source query", t => {
  const { values } = fixture(t);
  for (const recipe of active) {
    const item = instantiateRecipe(recipe, 7);
    const options = { templateId: item.instanceId, window: item.window, cover: item.cover ?? defaultCardCover(item) };
    const saved = collectQuery(item.dataset, item.query, item.title, options);
    assert.equal(saved.templateId, item.instanceId, recipe.id);
    assert.equal(collectQuery(item.dataset, item.query, item.title, options).id, saved.id, "repeated collection of the same card still deduplicates");
  }
  assert.equal(values.get("pharos-bookmarks-v1").length, active.length);
});

test("every active default cover can be saved without changing its settings", t => {
  const { values } = fixture(t), failures = [];
  for (const recipe of active) {
    values.clear();
    const cover = defaultCardCover(recipe);
    try {
      saveCardTemplate(recipe.id, { title: recipe.title, description: recipe.description, days: 7, cover });
      assert.deepEqual(json(defaultCardCover(cardTemplates().find(item => item.id === recipe.id))), json(cover), recipe.id);
    } catch (error) { failures.push({ id: recipe.id, error: error.message }); }
  }
  assert.deepEqual(failures, []);
});

test("every active card preserves customized covers and metadata-only dashboard edits", async t => {
  const { values } = fixture(t), failures = [];
  let cases = 0;
  for (const recipe of active) {
    for (const choice of coverChoices(recipe)) {
      const baseCover = decodeCover(choice.value), kinds = availableChartKinds(recipe, baseCover);
      for (const kind of kinds) {
        const cover = { ...baseCover, kind, maxGroups: 4, showLegend: false, showOther: false, height: 310 };
        const edited = { ...recipe, title: `Custom ${recipe.id}`, cover };
        for (const edit of [false, true]) {
          const result = await compare(edited, 7, values, { edit });
          cases++;
          if (result.preview || result.loading || result.failures.length) failures.push({ id: recipe.id, cover: choice.value, kind, edit, ...result });
        }
      }
    }
  }
  t.diagnostic(`${active.length} active cards: ${cases} comparisons across all cover metrics, compatible chart types, and dashboard metadata edits`);
  assert.deepEqual(failures, []);
});
