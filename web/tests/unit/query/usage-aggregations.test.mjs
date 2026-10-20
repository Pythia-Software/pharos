import assert from "node:assert/strict";
import test from "node:test";
import { loadSchema } from "@pythia-software/query-table-core";
import usageSchema from "../../../../schemas/usage.schema.json";
import { applyUsageAggregations } from "../../../src/usage-aggregations";

const schema = loadSchema(usageSchema);
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

// Daily facts that span hours: counts and averages stay per day, while hourly
// metrics count hours.
const days = [
  { id: "session|2026-09-20|model|", agent_session_id: "session", day: "2026-09-20", model: "model", total_tokens: 1020, first_usage_at: "2026-09-20T23:00:00Z" },
  { id: "session|2026-09-21|model|", agent_session_id: "session", day: "2026-09-21", model: "model", total_tokens: 2130, first_usage_at: "2026-09-21T01:00:00Z" },
];
const hours = [
  { ...days[0], hour: "2026-09-20T23:00:00Z", total_tokens: 1020 },
  { ...days[1], hour: "2026-09-21T01:00:00Z", total_tokens: 2030 },
  { ...days[1], hour: "2026-09-21T03:00:00Z", first_usage_at: "2026-09-21T03:00:00Z", total_tokens: 100 },
];
const dailyMetrics = [
  { id: "days", op: "count", groupBy: ["model"] },
  { id: "avg", op: "avg", field: "total_tokens", groupBy: [] },
  { id: "sum", op: "sum", field: "total_tokens", groupBy: [] },
];
const hourlyMetrics = [
  { id: "hours", op: "count", groupBy: ["hour"] },
  { id: "last-hour", op: "max", field: "hour", groupBy: [] },
];
const cases = [
  { name: "unfiltered", where: [], days: 2, hours: 3, average: 1575, total: 3150 },
  { name: "daily threshold", where: [{ field: "total_tokens", op: ">", value: "1500" }], days: 1, hours: 2, average: 2130, total: 2130 },
  { name: "timestamp", where: [{ field: "first_usage_at", op: "<", value: "2026-09-21T02:00:00Z" }], days: 2, hours: 2, average: 1575, total: 3150 },
  { name: "hour filter", where: [{ field: "hour", op: "=", value: "2026-09-21T03:00:00Z" }], days: 1, hours: 1, average: 2130, total: 2130 },
  { name: "mixed hour OR", where: [{ any: [{ field: "hour", op: "=", value: "2026-09-21T03:00:00Z" }, { field: "total_tokens", op: ">", value: "2000" }] }], days: 1, hours: 2, average: 2130, total: 2130 },
];
for (const sample of cases) {
  test(`shared metrics keep daily counts and averages: ${sample.name}`, () => {
    const request = { where: sample.where, aggregations: [dailyMetrics[0], hourlyMetrics[0], ...dailyMetrics.slice(1), hourlyMetrics[1]] };
    const before = JSON.stringify(request);
    const result = applyUsageAggregations(days, hours, request, schema);
    assert.deepEqual(result.metrics.map(metric => metric.id), request.aggregations.map(metric => metric.id));
    assert.deepEqual(result.metrics.slice(0, 4).map(metric => metric.buckets.reduce((sum, bucket) => sum + bucket.value, 0)), [sample.days, sample.hours, sample.average, sample.total]);
    for (const i of [0, 2, 3]) assert.equal(result.metrics[i].buckets[0].count, sample.days);
    assert.equal(JSON.stringify(request), before);
    if (!sample.where.some(term => term.field === "hour" || term.any)) {
      const alone = applyUsageAggregations(days, [], { where: sample.where, aggregations: dailyMetrics }, schema);
      assert.deepEqual([result.metrics[0], ...result.metrics.slice(2, 4)], alone.metrics);
    }
  });
}
