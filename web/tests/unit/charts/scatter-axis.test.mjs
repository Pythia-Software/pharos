import test from "node:test";
import assert from "node:assert/strict";
import { scatterAxis, scatterTick } from "../../../src/scatter-axis";

test("wide log ranges use a sparse set of decades without expanding to rounded bounds", () => {
  const values = [.000451, .802, 1424.5], before = [...values];
  const axis = scatterAxis(values, { mode: "log" });
  assert.deepEqual(axis.ticks, [.001, .1, 10, 1000]);
  assert.deepEqual(axis.ticks.map(value => scatterTick(value)), ["0.001", "0.1", "10", "1K"]);
  assert.equal(axis.min, values[0]);
  assert.equal(axis.max, values.at(-1));
  assert.equal(axis.fraction(values[0]), 0);
  assert.equal(axis.fraction(values.at(-1)), 1);
  assert.deepEqual(values, before);
  const tokens = scatterAxis([14400, 1785000000], { mode: "log" }, 3);
  assert.deepEqual(tokens.ticks, [100000, 10000000, 1000000000]);
  assert.deepEqual(tokens.ticks.map(value => scatterTick(value)), ["100K", "10M", "1B"]);
});

test("narrow log ranges use 1–2–5 denominations or round sub-decade steps", () => {
  for (const [min, max, expected] of [[1, 9, [1, 2, 5]], [12, 80, [20, 50]], [.71, .89, [.75, .8, .85]], [90, 110, [90, 100, 110]]]) {
    const axis = scatterAxis([min, max], { mode: "log" });
    assert.deepEqual(axis.ticks, expected);
    assert.ok(axis.ticks.every(tick => tick >= min && tick <= max));
    assert.equal(new Set(axis.ticks.map(value => scatterTick(value))).size, expected.length);
  }
  assert.ok(scatterAxis([1, 15], { mode: "log" }, 3).ticks.length <= 3);
});

test("log axes retain explicit bounds and reject invalid bounds", () => {
  const axis = scatterAxis([10, 1000], { mode: "log", min: 1, max: 100 });
  assert.deepEqual(axis.ticks, [1, 10, 100]);
  assert.equal(axis.fraction(10), .5);
  assert.ok(axis.fraction(1000) > 1, "out-of-bounds points are clipped, not clamped onto an edge");
  for (const bounds of [{ min: 0 }, { min: -1 }, { min: 20, max: 10 }, { max: Infinity }]) assert.match(scatterAxis([1, 10], { mode: "log", ...bounds }).error, /Axis bounds/);
});

test("empty, nonpositive and constant inputs have usable axes without replacing zeros", () => {
  const empty = scatterAxis([0, -1, Infinity, NaN], { mode: "log" });
  assert.equal(empty.min, 1);
  assert.equal(empty.max, 10);
  assert.equal(empty.accepts(0), false);
  const single = scatterAxis([5, 5], { mode: "log" });
  assert.ok(single.min < 5 && single.max > 5);
  assert.ok(Math.abs(single.fraction(5) - .5) < 1e-12);
  const linear = scatterAxis([-2, 0, 5]);
  assert.equal(linear.accepts(0), true);
  assert.ok(linear.ticks.includes(0));
});

test("compact tick labels preserve small numbers and declared percent/duration units", () => {
  assert.equal(scatterTick(.0001), "0.0001");
  assert.equal(scatterTick(.001, { kind: "number", decimals: 0 }), "0.001");
  assert.equal(scatterTick(10000000), "10M");
  assert.equal(scatterTick(.2, { kind: "percent", decimals: 4 }), "20%");
  assert.match(scatterTick(1000, { kind: "duration", sourceUnit: "milliseconds" }), /s/);
  const narrow = scatterAxis([10000, 10001], { mode: "log" });
  assert.equal(new Set(narrow.ticks.map(value => scatterTick(value, undefined, narrow.ticks))).size, narrow.ticks.length, "close ticks remain distinct when extra precision is necessary");
});
