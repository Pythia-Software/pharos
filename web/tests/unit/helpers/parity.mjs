// Catalog-versus-saved preview comparisons for the card parity tests: an
// in-memory server over a synthetic population of every dataset, and
// `compare`, which collects a card and diffs its preview and loading shape
// against the catalog card it came from.
import assert from "node:assert/strict";
import { capabilities as queryCapabilities, core, execution, queryRecipes, instantiateRecipe, schemas } from "./recipes.mjs";
import { clearPreviewCache, loadQueryPreview } from "../../../src/curiosity-data";
import { defaultCardCover } from "../../../src/card-cover";
import { cardLoadingPresentation } from "../../../src/card-loading-model";
import { collectQuery, updateBookmark } from "../../../src/bookmarks";
import { queryFromURL } from "../../../src/query-links";

export const active = queryRecipes.filter(recipe => !recipe.retired);
const now = Date.parse("2026-10-16T18:00:00Z"), day = 86400000;
export const json = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value));

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

export function fixture(t, empty = false) {
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

export async function compare(recipe, days, values, { edit = false } = {}) {
  values.clear();
  const item = instantiateRecipe(recipe, days), canonical = queryRecipes.find(candidate => candidate.id === recipe.id);
  const saved = collectQuery(item.dataset, item.query, item.title, { description: item.description, templateId: item.instanceId, window: item.window, cover: item.cover ?? defaultCardCover(item) });
  // Windowless (current-state and all-time) cards offer no lookback to edit.
  const bookmark = edit ? updateBookmark(saved.id, saved.title, saved.description, item.window ? days : undefined, saved.cover?.metricId ?? "") : saved;
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
