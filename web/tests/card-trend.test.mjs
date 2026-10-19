import test from "node:test";
import assert from "node:assert/strict";
import { build } from "esbuild";

const bundle = await build({ entryPoints: ["src/card-trend.ts"], bundle: true, write: false, platform: "node", format: "esm" });
const { buildCardTrend, cardTrendDays, cardTrendQueries, shiftDay, localDay } = await import(`data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString("base64")}`);
const now = new Date(2026, 9, 19, 15).getTime();
const results = (values, per) => [[
  { id: "trend-value", op: "sum", groupBy: ["day"], coverage: "exact", buckets: Object.entries(values).map(([day, value]) => ({ keys: [day], value, count: 1 })) },
  ...(per ? [{ id: "trend-per", op: "sum", groupBy: ["day"], coverage: "exact", buckets: Object.entries(per).map(([day, value]) => ({ keys: [day], value, count: 1 })) }] : []),
  { id: "trend-rows", op: "count", groupBy: ["day"], coverage: "exact", buckets: Object.entries(values).map(([day]) => ({ keys: [day], value: 2, count: 2 })) },
]];

test("trend days cover 31 endpoints plus the lookback, in calendar steps", () => {
  const { days, ends } = cardTrendDays(7, true, now);
  assert.equal(ends.length, 31);
  assert.equal(ends.at(-1), "2026-10-18", "complete days end yesterday");
  assert.equal(days.length, 37);
  assert.equal(days[0], shiftDay(ends[0], -6));
  // Daylight saving ends on Nov 1 in the US; calendar steps never skip or repeat a day.
  assert.equal(shiftDay("2026-10-31", 2), "2026-11-02");
  assert.equal(new Set(cardTrendDays(90, false, now).days).size, 120);
});

test("requests stay within the day predicate budget and keep the population filter", () => {
  const where = [{ field: "total_tokens", op: ">", value: "0" }];
  const queries = cardTrendQueries(where, { value: { op: "sum", field: "co2e_kg" }, per: { op: "count", field: "duration_ms" } }, cardTrendDays(30, false, now).days);
  assert.equal(queries.length, 2);
  for (const query of queries) {
    assert.deepEqual(query.where[0], where[0]);
    assert.ok(query.where[1].any.length <= 30);
    assert.deepEqual(query.aggregations.map(metric => metric.id), ["trend-value", "trend-per", "trend-rows"]);
    assert.ok(query.aggregations.every(metric => metric.groupBy[0] === "day"));
  }
});

test("per-day averages include idle days and ratios pool window totals", () => {
  const last = localDay(now);
  const averaged = buildCardTrend({ value: { op: "count" }, perDay: true, lookback: 7, valueLabel: "", pointValueLabel: "", sample: "calls", detail: "" }, 7, results({ [last]: 14, [shiftDay(last, -6)]: 7, [shiftDay(last, -7)]: 70 }), now);
  assert.equal(averaged.points.at(-1), 3, "(14 + 7) / 7 days, excluding the eighth day");
  assert.equal(averaged.pointTimes.at(-1), now, "today's endpoint is the calculation time");
  assert.match(averaged.sampleLabel, /^4 calls in the latest 7 days$/);
  const pooled = buildCardTrend({ value: { op: "sum", field: "duration_ms" }, per: { op: "count", field: "duration_ms" }, scale: 1 / 1000, lookback: 2, valueLabel: "", pointValueLabel: "", sample: "calls", detail: "" }, 2,
    results({ [last]: 9000, [shiftDay(last, -1)]: 1000 }, { [last]: 1, [shiftDay(last, -1)]: 9 }), now);
  assert.equal(pooled.points.at(-1), 1, "10 s over 10 timed calls, not the mean of 9 s and 0.11 s");
  assert.equal(pooled.points[0], null, "windows without timed calls are gaps");
});

test("incomplete daily results fail instead of reading as idle days", () => {
  const partial = results({ [localDay(now)]: 1 });
  partial[0][0].coverage = "partial";
  assert.throws(() => buildCardTrend({ value: { op: "count" }, lookback: 7, valueLabel: "", pointValueLabel: "", sample: "", detail: "" }, 7, partial, now), /Complete daily history/);
});
