// Catalog-versus-saved-and-expanded preview comparisons: an
// in-memory server over a synthetic population of every dataset, and
// `compare`, which collects a card and diffs its preview and loading shape
// against the catalog card it came from.
import assert from "node:assert/strict";
import { capabilities as queryCapabilities, core, execution, queryRecipes, instantiateRecipe, schemas } from "./recipes.mjs";
import { population, serverQuery } from "../../support/query-server.mjs";
import { clearPreviewCache, loadQueryPreview } from "../../../src/curiosity-data";
import { defaultCardCover } from "../../../src/card-cover";
import { cardLoadingPresentation } from "../../../src/card-loading-model";
import { collectQuery, updateBookmark } from "../../../src/bookmarks";
import { queryFromURL } from "../../../src/query-links";
import { cardAnalysisJob } from "../../../src/card-analysis-model";

export const active = queryRecipes.filter(recipe => !recipe.retired);
const now = Date.parse("2026-10-16T18:00:00Z");
export const json = value => value === undefined ? undefined : JSON.parse(JSON.stringify(value));

export function fixture(t, empty = false) {
  const prior = { fetch: global.fetch, window: global.window, location: global.location };
  const values = new Map(), requests = [];
  const populations = Object.fromEntries(Object.entries(schemas).map(([dataset, schema]) => [dataset, empty ? [] : population(schema, { now })]));
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
    if (path.endsWith("/query-v3")) return Response.json(serverQuery(request, rows, schema));
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
  const saved = collectQuery(item.dataset, item.query, item.title, { description: item.description, templateId: item.instanceId, window: item.window, cover: item.cover ?? defaultCardCover(item), url: item.url });
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
  let analysis;
  if (item.destination) {
    const job = cardAnalysisJob(bookmark.url, schemas[item.dataset], bookmark.query);
    analysis = difference(catalog, await preview(job.query, job.options));
  }
  if (catalog.study && item.cover?.height !== undefined) assert.equal(catalog.coverHeight, item.cover.height, `${recipe.id}: customized study height`);
  const loading = difference(json(cardLoadingPresentation(item.query, { ...catalogOptions, days })), json(cardLoadingPresentation(spec.query, { ...savedOptions, metricId: bookmark.coverMetricId, days })));
  return { preview: difference(catalog, dashboard), analysis, loading, failures, state: catalog.state };
}
