import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { build } from "esbuild";
import * as core from "@pythia-software/query-table-core";

const bundle = await build({ stdin: { contents: "export * from './src/curiosity-data'; export * from './src/card-trend';", resolveDir: process.cwd() }, bundle: true, write: false, platform: "node", format: "esm" });
const { queryRecipes, instantiateRecipe, loadQueryPreview, clearPreviewCache, usesCardTrend } = await import(`data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString("base64")}`);
const schema = core.loadSchema(JSON.parse(readFileSync("../schemas/usage.schema.json", "utf8")));
const execution = { profile: "qt-sqlite-v1", planToken: "", fields: {}, resolvedRevisions: {} };
const recipe = queryRecipes.find(recipe => recipe.id === "carbon-trend");
const job = (query, item) => ({ id: "card", dataset: "usage", schema, query, options: { recipe, window: item.window, cover: item.cover, url: item.url } });

test("the sparkline stands in only for the shipped population, metric and grouping", () => {
  const item = instantiateRecipe(recipe, 7), query = structuredClone(item.query);
  assert.ok(query.grouping, "carbon-trend opens a grouped daily destination");
  assert.equal(usesCardTrend(job(query, item)), true);
  const energy = structuredClone(query);
  energy.grouping.source.aggregations = energy.grouping.source.aggregations.map(metric => ({ ...metric, field: "energy_kwh" }));
  assert.equal(usesCardTrend(job(energy, item)), false, "an edited measure that keeps its ID must not show emissions");
  const regrouped = structuredClone(query);
  regrouped.grouping.keys = ["repository_name"];
  assert.equal(usesCardTrend(job(regrouped, item)), false, "edited grouping keys");
  const having = structuredClone(query);
  having.where = [{ field: query.grouping.summaries[0].id, op: ">", value: "1" }];
  assert.equal(usesCardTrend(job(having, item)), false, "an added HAVING filter");
  const filtered = structuredClone(query);
  filtered.grouping.source.where = [...filtered.grouping.source.where, { field: "provider", op: "=", value: "codex" }];
  assert.equal(usesCardTrend(job(filtered, item)), false, "an edited population");
});

test("an observed history of zero emissions is a ready trend, not an empty card", async t => {
  const prior = { fetch: global.fetch, location: global.location };
  global.location = { origin: "http://trend.test" };
  t.after(() => { Object.assign(global, prior); clearPreviewCache(); });
  global.fetch = async (url, init = {}) => {
    const path = String(url), request = init.body ? JSON.parse(init.body) : undefined;
    if (path.includes("computed-columns")) return Response.json([]);
    if (path.endsWith("capabilities")) return Response.json({ metricCapabilities: { version: 2, expressions: true, distributions: true }, computedExecution: execution });
    assert.ok(path.endsWith("/usage/metrics"), path);
    // Every day has usage records whose estimated CO₂e is exactly zero (custom grid 0).
    const days = request.where.at(-1).any?.map(term => term.value) ?? [request.where.at(-1).value];
    return Response.json({ metrics: request.metrics.map(metric => ({ id: metric.id, coverage: "exact", buckets: days.map(day => ({ keys: [day], value: metric.id === "trend-rows" ? 3 : 0, count: 3 })) })) });
  };
  const item = instantiateRecipe(recipe, 7);
  const preview = await loadQueryPreview("usage", item.query, schema, { recipe, window: item.window, cover: item.cover, url: item.url, suggested: true });
  assert.equal(preview.state, "ready");
  assert.equal(preview.study.value, 0);
  assert.ok(preview.study.points.every(point => point === 0));
});
