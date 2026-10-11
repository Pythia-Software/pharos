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
