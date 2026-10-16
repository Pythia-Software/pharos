import test from "node:test";
import assert from "node:assert/strict";
import { build } from "esbuild";
const bundle = await build({ entryPoints: ["src/card-presentation.ts"], bundle: true, write: false, platform: "node", format: "esm" });
const { compactMetricPresentation } = await import(`data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString("base64")}`);
function preview(extra = {}) {
  const clause = { id: "tokens", op: "sum", field: "total_tokens", groupBy: ["price_status"], display: { kind: "donut" } };
  const result = { id: "tokens", groupCount: 1, coverage: "exact", buckets: [{ keys: ["priced"], value: 500, count: 20 }] };
  return { state: "ready", summary: "1 group", total: 20, coverMetricId: "tokens", metrics: [{ clause, result }], ...extra };
}
test("a complete single-group metric becomes a value without altering its query dimensions", () => {
  const input = preview(), before = structuredClone(input);
  const output = compactMetricPresentation(input);
  assert.equal(output.kind, "value");
  assert.equal(output.groupLabel, "priced");
  assert.equal(output.summary, undefined);
  assert.deepEqual(input, before);
  input.metrics[0].result.buckets[0].value = 0;
  assert.equal(compactMetricPresentation(input).scalar, true, "observed zero is still a value");
});
test("single distributions, relationships, timelines and partial populations retain their chart", () => {
  for (const variant of ["distribution", "paired", "calendar", "day", "partial", "limited", "null", "nonfinite"]) {
    const input = preview(), { clause, result } = input.metrics[0];
    if (variant === "distribution") clause.distribution = { kind: "histogram", input: "[total_tokens]" };
    if (variant === "paired") clause.expressionY = "SUM([total_tokens])";
    if (variant === "calendar") clause.dateGrouping = { field: "at", interval: "hour" };
    if (variant === "day") clause.groupBy = ["day"];
    if (variant === "partial") result.coverage = "partial";
    if (variant === "limited") result.groupCount = 10;
    if (variant === "null") result.buckets[0].value = null;
    if (variant === "nonfinite") result.buckets[0].value = Infinity;
    assert.equal(compactMetricPresentation(input).scalar, false, variant);
  }
});
test("compact scatter ticks remove excessive precision and retain declared units and raw coordinates", () => {
  const input = preview(), { clause, result } = input.metrics[0];
  clause.expressionY = "AVG([result_tokens])";
  clause.display = { kind: "scatter", xFormat: { kind: "duration", sourceUnit: "milliseconds" } };
  result.buckets = [{ keys: ["Read"], value: 2500, y: 11, count: 2 }, { keys: ["Bash"], value: 5000, y: 7742.5942, count: 3 }];
  const before = structuredClone(input);
  assert.deepEqual(compactMetricPresentation(input).axisFormats, { xFormat: clause.display.xFormat, yFormat: { kind: "number", decimals: 0 } });
  assert.deepEqual(input, before);
  result.buckets[0].y = .0123; result.buckets[1].y = .0246;
  assert.equal(compactMetricPresentation(input).axisFormats.yFormat.decimals, 4, "small ranges keep distinct ticks");
  clause.display.yFormat = { kind: "percent", decimals: 1 };
  assert.deepEqual(compactMetricPresentation(input).axisFormats.yFormat, clause.display.yFormat);
});
test("ranked covers show their rows without a population caption or headline total", () => {
  const input = preview({ summary: "540 workspaces", groupSummary: { count: 540, subject: "workspaces" } });
  const { clause, result } = input.metrics[0];
  clause.display.kind = "bar-horizontal";
  result.groupCount = 540;
  result.buckets = Array.from({ length: 12 }, (_, i) => ({ keys: [String(i)], value: 100 - i, count: 1 }));
  const output = compactMetricPresentation(input);
  assert.equal(output.shown, 4);
  assert.equal(output.summary, undefined);
  assert.equal(output.headlineMetric, undefined);
  assert.equal(output.ranked, true);
});
test("scatter covers respect logarithmic scales and bounds without changing the raw metric", () => {
  const input = preview(), { clause } = input.metrics[0];
  clause.expressionY = "AVG([result_tokens])";
  clause.display = { kind: "scatter", xScale: { mode: "log", min: 1, max: 1000 }, yScale: { mode: "log" } };
  const before = structuredClone(input);
  assert.deepEqual(compactMetricPresentation(input).axisScales, { xScale: { mode: "log", min: 1, max: 1000 }, yScale: { mode: "log" } });
  assert.deepEqual(input, before);
});

test("box covers budget three readable rows and disclose the same subset", () => {
  const input = preview(), { clause, result } = input.metrics[0];
  clause.display.kind = "box";
  clause.distribution = { kind: "box", input: "[duration_ms]" };
  result.groupCount = 468;
  result.buckets = Array.from({ length: 8 }, (_, i) => ({ keys: [String(i)], value: null, count: 1 }));
  const output = compactMetricPresentation(input);
  assert.equal(output.budget, 3);
  assert.equal(output.shown, 3);
  assert.equal(output.total, 468);
});

test("timeline headlines keep their measure and never reuse a ranked list total", () => {
  const input = preview({ headlineMetric: { value: "$2,686", unit: "", label: "API equivalent" }, buckets: [{ key: "today" }] });
  const { clause, result } = input.metrics[0];
  clause.groupBy = ["day", "repository_name"];
  result.groupCount = 2;
  result.buckets = [{ keys: ["today", "pharos"], value: 100, count: 1 }, { keys: ["today", "qt"], value: 200, count: 1 }];
  const before = structuredClone(input), presentation = compactMetricPresentation(input);
  assert.deepEqual(presentation.headlineMetric, input.headlineMetric);
  assert.equal(presentation.ranked, false);
  assert.deepEqual(input, before);
  delete input.buckets;
  clause.display.kind = "bar-horizontal";
  assert.equal(compactMetricPresentation(input).headlineMetric, undefined);
});

test("long list labels use underline bars while explicit placement and query definitions survive", () => {
  const input = preview(), { clause, result } = input.metrics[0];
  clause.display.kind = "bar-horizontal";
  result.groupCount = 2;
  result.buckets = [{ keys: ["A long cross-project error signature"], value: 4, count: 1 }, { keys: ["Another long signature"], value: 3, count: 1 }];
  const before = structuredClone(input);
  assert.equal(compactMetricPresentation(input).listOptions.barPosition, "below");
  assert.equal(compactMetricPresentation(input).listOptions.maxLabelLines, 2);
  assert.deepEqual(input, before);
  clause.display.list = { barPosition: "below", maxLabelLines: 3 };
  assert.deepEqual(compactMetricPresentation(input).listOptions, clause.display.list);
  clause.display.list = { barPosition: "inline", showValues: false };
  assert.deepEqual(compactMetricPresentation(input).listOptions, clause.display.list);
  delete clause.display.list;
  result.buckets.forEach(bucket => bucket.keys = ["Short"]);
  assert.equal(compactMetricPresentation(input).listOptions.barPosition, "inline");
  assert.equal(compactMetricPresentation(input).listOptions.maxLabelLines, undefined);
  input.groupLabels = { '["Short"]': "A long representative work title" };
  assert.equal(compactMetricPresentation(input).listOptions.barPosition, "below");
});

test("explicit cover group limits override the default presentation budget without mutating queries", () => {
  const input = preview(), { clause, result } = input.metrics[0];
  clause.display = { kind: "list", maxGroups: 2, showOther: true, legendPosition: "none" };
  result.buckets = [1, 2, 3, 4, 5].map(n => ({ keys: [String(n)], value: n, count: 1 }));
  result.groupCount = 5;
  const before = structuredClone(input), output = compactMetricPresentation(input);
  assert.equal(output.budget, 2);
  assert.equal(output.shown, 2);
  assert.deepEqual(input, before);
});


test("model lists opt into group colors and compound pies/scatters retain display labels", () => {
  const input = preview();
  input.metrics[0].clause.groupBy = ["provider", "model_family"];
  input.metrics[0].result.buckets = [{ keys: ["one", "alpha"], value: 10, count: 1 }, { keys: ["two", "beta"], value: 20, count: 1 }];
  input.metrics[0].result.groupCount = 2;
  for (const kind of ["bar-horizontal", "pie", "scatter"]) {
    input.metrics[0].clause.display = { kind };
    const presentation = compactMetricPresentation(input);
    assert.equal(presentation.friendlyKeys, true, kind);
    if (kind === "bar-horizontal") assert.equal(presentation.listOptions.useGroupColors, true);
  }
  input.metrics[0].clause.display = { kind: "bar-horizontal", list: { useGroupColors: false } };
  assert.equal(compactMetricPresentation(input).listOptions.useGroupColors, false);
});


test("log scatter disclosure counts nonpositive pairs once and leaves missing data and source values intact", () => {
  const input = preview(), { clause, result } = input.metrics[0];
  clause.expressionY = "SUM([cost_usd])";
  clause.display = { kind: "scatter", xScale: { mode: "log" }, yScale: { mode: "log" } };
  result.groupCount = 6;
  result.buckets = [
    { keys: ["positive"], value: 10, y: 1, count: 1 },
    { keys: ["zero-x"], value: 0, y: 1, count: 1 },
    { keys: ["zero-y"], value: 10, y: 0, count: 1 },
    { keys: ["negative-both"], value: -1, y: -2, count: 1 },
    { keys: ["missing"], value: 10, y: null, count: 1 },
    { keys: ["error"], value: 0, y: 0, error: "Unavailable", count: 1 },
  ];
  const before = structuredClone(input), output = compactMetricPresentation(input);
  assert.equal(output.omittedLogPoints, 3);
  assert.match(output.summary, /3 zero\/negative points omitted/);
  assert.deepEqual(input, before);
  clause.display = { kind: "scatter" };
  assert.deepEqual(compactMetricPresentation(input).axisScales, { xScale: { mode: "linear" }, yScale: { mode: "linear" } });
  assert.equal(compactMetricPresentation(input).omittedLogPoints, 0);
});
