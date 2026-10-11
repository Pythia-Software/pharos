import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { loadSchema } from "@pythia-software/query-table-core";
import { build } from "esbuild";

const compiled = await build({ entryPoints: ["src/usage-aggregations.ts"], bundle: true, write: false, platform: "node", format: "esm" });
const { applyUsageAggregations } = await import(`data:text/javascript;base64,${Buffer.from(compiled.outputFiles[0].text).toString("base64")}`);
const schema = loadSchema(JSON.parse(readFileSync("../schemas/usage.schema.json", "utf8")));
const daily = [[null, 11], ["low", 120], ["high", 230]].map(([effort, total_tokens]) => ({
  id: `session|2026-09-20|gpt-6|${effort ?? ""}`, agent_session_id: "session", day: "2026-09-20", model: "gpt-6", effort, total_tokens,
  model_effort: `gpt-6 · ${effort ?? "Unknown effort"}`,
}));
const hourly = daily.map(row => ({ ...row, id: `hour|${row.effort ?? ""}`, hour: "2026-09-20T12:00:00Z" }));
const metric = { id: "tokens", op: "sum", field: "total_tokens", groupBy: ["effort"] };

for (const effort of ["low", "high"]) {
  test(`shared hourly metrics respect the ${effort} daily filter`, () => {
    const result = applyUsageAggregations(daily, hourly, {
      where: [{ field: "effort", op: "=", value: effort }],
      aggregations: [{ ...metric, groupBy: ["hour", "effort"] }],
    }, schema);
    assert.equal(result.metrics[0].buckets.length, 1);
    assert.equal(result.metrics[0].buckets[0].value, effort === "low" ? 120 : 230);
    assert.deepEqual(result.metrics[0].buckets[0].keys, ["2026-09-20T12:00:00Z", effort]);
  });
}

test("shared hour filters retain separate daily effort buckets and unknowns", () => {
  const result = applyUsageAggregations(daily, hourly, {
    where: [{ field: "hour", op: "=", value: "2026-09-20T12:00:00Z" }], aggregations: [metric],
  }, schema);
  assert.equal(result.metrics[0].buckets.length, 3);
  assert.equal(result.metrics[0].buckets.reduce((sum, bucket) => sum + bucket.value, 0), 361);
});

test("model and effort can filter and group together at daily and hourly granularity", () => {
  for (const groupBy of [["model", "effort"], ["model_effort"], ["hour", "model_effort"]]) {
    const result = applyUsageAggregations(daily, hourly, {
      where: [{ field: "model", op: "=", value: "gpt-6" }, { field: "effort", op: "=", value: "low" }],
      aggregations: [{ ...metric, groupBy }],
    }, schema);
    assert.equal(result.metrics[0].buckets.length, 1);
    assert.equal(result.metrics[0].buckets[0].value, 120);
  }
  const result = applyUsageAggregations(daily, hourly, {
    where: [{ field: "model_effort", op: "=", value: "gpt-6 · high" }],
    aggregations: [{ ...metric, groupBy: ["hour", "model_effort"] }],
  }, schema);
  assert.deepEqual(result.metrics[0].buckets.map(bucket => [bucket.keys, bucket.value]), [[["2026-09-20T12:00:00Z", "gpt-6 · high"], 230]]);
});

test("combined model-effort chart buckets preserve unknowns and separate levels", () => {
  const result = applyUsageAggregations(daily, hourly, { where: [], aggregations: [{ ...metric, groupBy: ["model_effort"] }] }, schema);
  assert.equal(result.metrics[0].buckets.length, 3);
  assert.deepEqual(new Map(result.metrics[0].buckets.map(bucket => [bucket.keys[0], bucket.value])), new Map([
    ["gpt-6 · Unknown effort", 11], ["gpt-6 · low", 120], ["gpt-6 · high", 230],
  ]));
});
