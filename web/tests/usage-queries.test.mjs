import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { build } from "esbuild";
import { EMPTY_QUERY, applyQuery, decodeQueryStrict, encodeQuery, loadSchema } from "@pythia-software/query-table-core";

const compiled = await build({ entryPoints: ["src/usage-queries.ts"], bundle: true, write: false, platform: "node", format: "esm" });
const { applyUsageTablePreset, usageGroupPresets, usageComparisonPresets, usageTablePresetMatches } = await import(`data:text/javascript;base64,${Buffer.from(compiled.outputFiles[0].text).toString("base64")}`);
const schema = loadSchema(JSON.parse(readFileSync("../schemas/usage.schema.json", "utf8")));
const preset = label => [...usageGroupPresets, ...usageComparisonPresets].find(item => item.label === label);
const fact = (id, extra = {}) => ({
  id, agent_session_id: "session-a", workspace_id: "work-a", title: "Same title", repository_name: "one", provider: "codex",
  model_family: "gpt-6", effort: "high", session_kind: "root", day: "2026-10-12", week: "2026-10-12", month: "2026-10",
  first_usage_at: "2026-10-12T12:00:00Z", last_usage_at: "2026-10-12T12:00:00Z", price_status: "priced",
  cost_usd: 2, cost_today_usd: 3, total_tokens: 100, output_tokens: 10, input_tokens: 90,
  uncached_input_tokens: 80, cache_read_input_tokens: 10, cache_creation_input_tokens: 0, ...extra,
});
const rows = [fact("a"), fact("b", { day: "2026-10-13", total_tokens: 1010, output_tokens: 20, input_tokens: 990, uncached_input_tokens: 90, cache_read_input_tokens: 900 }),
  fact("c", { workspace_id: "work-b", agent_session_id: "session-b", effort: null, repository_name: "two", price_status: "unpriced", cost_usd: null, cost_today_usd: null, total_tokens: 0, input_tokens: 0, uncached_input_tokens: 0, cache_read_input_tokens: 0 })];
const run = (label, query = EMPTY_QUERY, data = rows) => applyQuery(data, applyUsageTablePreset(query, preset(label)), schema);

test("repository summaries count sessions once across days and compute a weighted cache rate", () => {
  const result = run("Repositories");
  const one = result.rows.find(row => row.repository_name === "one");
  assert.equal(one.usage_sessions, 1);
  assert.equal(one.usage_total_tokens, 1110);
  assert.equal(one.usage_cost_usd, 4);
  assert.equal(one.usage_cache_hit_percent, 910 / 1080, "a fraction, presented by its percent display");
  const two = result.rows.find(row => row.repository_name === "two");
  assert.equal(two.usage_cost_usd, null);
  assert.equal(two.usage_cache_hit_percent, null);
});

test("every preset runs as a complete grouped table and survives the URL codec", () => {
  for (const item of [...usageGroupPresets, ...usageComparisonPresets]) {
    const query = applyUsageTablePreset(EMPTY_QUERY, item);
    assert.deepEqual(decodeQueryStrict(encodeQuery(query)), query, item.label);
    assert.doesNotThrow(() => applyQuery(rows, query, schema), item.label);
    for (const key of query.grouping.keys) assert.ok(query.select.some(column => column.field === key), `${item.label} must return ${key} for Records drill-in`);
  }
});

test("source filters run before grouping and HAVING sorts and pages the group population", () => {
  const source = { ...EMPTY_QUERY, where: [{ field: "repository_name", op: "=", value: "one" }], offset: 50, limit: 1 };
  const query = applyUsageTablePreset(source, preset("Model + effort"));
  assert.equal(query.offset, 0);
  assert.equal(applyQuery(rows, query, schema).rows[0].usage_total_tokens, 1110, "Source pagination must not truncate the grouped population");
  const having = { ...applyUsageTablePreset(EMPTY_QUERY, preset("Repositories")), where: [{ field: "usage_total_tokens", op: ">", value: 500 }] };
  assert.deepEqual(applyQuery(rows, having, schema).rows.map(row => row.repository_name), ["one"]);
});

test("switching grouped presets keeps the original source and drops old HAVING and group metrics", () => {
  const source = { ...EMPTY_QUERY, select: [{ field: "day", width: 105 }], where: [{ field: "provider", op: "=", value: "codex" }], offset: 50,
    aggregations: [{ id: "source-tokens", op: "sum", field: "total_tokens", groupBy: [] }] };
  const first = { ...applyUsageTablePreset(source, preset("Repositories")), where: [{ field: "usage_total_tokens", op: ">", value: 500 }] };
  const second = applyUsageTablePreset(first, preset("Models"));
  // Leaving the stage later starts the source records from their first page.
  assert.deepEqual(second.grouping.source, { ...source, offset: 0 });
  assert.deepEqual(second.where, []);
  assert.deepEqual(second.aggregations ?? [], []);
  assert.deepEqual(first.grouping.source, { ...source, offset: 0 });
});

test("work identity and unknown effort remain separate", () => {
  assert.equal(run("Work").total, 2, "Identical titles must not merge workspaces");
  assert.equal(run("Model + effort").total, 2);
  assert.ok(run("Model + effort").rows.some(row => row.effort === null));
});

test("price differences use matched price pairs and preserve unpriced usage", () => {
  const data = [fact("both"), fact("historical-only", { cost_usd: 100, cost_today_usd: null }), fact("today-only", { cost_usd: null, cost_today_usd: 200 })];
  const row = run("Price changes", EMPTY_QUERY, data).rows[0];
  assert.equal(row.usage_price_delta, 1);
  assert.equal(row.usage_price_pairs, 1);
  assert.equal(row.usage_total_tokens, 300);
});

test("preset highlighting follows manual edits but ignores result presentation and source filters", () => {
  const query = applyUsageTablePreset(EMPTY_QUERY, preset("Repositories"));
  assert.equal(usageTablePresetMatches({ ...query, select: [], orderBy: [], offset: 50, where: [{ field: "usage_sessions", op: ">", value: 1 }] }, preset("Repositories")), true);
  const edited = { ...query, grouping: { ...query.grouping, summaries: query.grouping.summaries.map(column => column.id === "usage_cost_usd" ? { ...column, op: "avg" } : column) } };
  assert.equal(usageTablePresetMatches(edited, preset("Repositories")), false);
});

test("the Work preset links each title to its work and keeps link columns out of server requests", () => {
  const query = applyUsageTablePreset(EMPTY_QUERY, preset("Work"));
  const link = query.grouping.summaries.find(column => column.op === "link");
  assert.equal(query.select[0].field, link.id);
  assert.deepEqual(link.link, { label: { column: "usage_work_title" }, to: { kind: "template", template: "/work/{field:workspace_id}" } });
  assert.deepEqual(decodeQueryStrict(encodeQuery(query)).grouping.summaries, query.grouping.summaries);
  assert.equal(usageTablePresetMatches(query, preset("Work")), true);
  assert.equal(usageTablePresetMatches(query, preset("Repositories")), false);
});
