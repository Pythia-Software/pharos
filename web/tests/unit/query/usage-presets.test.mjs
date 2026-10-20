import assert from "node:assert/strict";
import test from "node:test";
import { EMPTY_QUERY, loadSchema, toMetricQuery } from "@pythia-software/query-table-core";
import writingDocument from "../../../../schemas/writing.schema.json";
import writingMessagesDocument from "../../../../schemas/writing_messages.schema.json";
import { execution } from "../helpers/recipes.mjs";
import { applyUsagePreset, messageMetricPresets, messagePresets, presetMatches, usageChartPresets, writingMetricPresets, writingPresets } from "../../../src/usage-presets";

const writing = loadSchema(writingDocument), messages = loadSchema(writingMessagesDocument);
const rows = [["writing sort", writingPresets, writing], ["writing metrics", writingMetricPresets, writing], ["message sort", messagePresets, messages], ["message metrics", messageMetricPresets, messages]];
const preset = (list, label) => list.find(item => item.label === label);
const chart = { metric: "tokens", split: "type", period: "week", hours: 6, days: 3, rollingPeriods: 0, scale: "half" };
const matching = (list, query, view) => list.filter(item => presetMatches(item, query, view)).map(item => item.label);

test("sort presets match the whole priority list, with nulls last by default", () => {
  const leastTyped = preset(messagePresets, "Least typed");
  const sorted = orderBy => ({ ...EMPTY_QUERY, orderBy });
  assert.equal(presetMatches(leastTyped, sorted([{ field: "typed_share", dir: "asc", nulls: "last" }, { field: "words", dir: "desc" }])), true);
  assert.equal(presetMatches(leastTyped, sorted([{ field: "typed_share", dir: "asc" }])), false, "a missing secondary sort is a different order");
  assert.equal(presetMatches(leastTyped, sorted([{ field: "words", dir: "desc" }, { field: "typed_share", dir: "asc" }])), false);
  assert.equal(presetMatches(leastTyped, sorted([{ field: "typed_share", dir: "asc", nulls: "first" }, { field: "words", dir: "desc" }])), false);
  assert.equal(presetMatches(leastTyped, sorted([{ field: "typed_share", dir: "desc" }, { field: "words", dir: "desc" }])), false);
  assert.equal(presetMatches(leastTyped, sorted([{ field: "typed_share", dir: "asc", extract: { regex: "\\d+" } }, { field: "words", dir: "desc" }])), false);
  const handsOn = preset(writingPresets, "Most hands-on");
  assert.equal(presetMatches(handsOn, { ...EMPTY_QUERY, orderBy: handsOn.orderBy, where: [{ field: "repository_name", op: "=", value: "example/repo" }], offset: 50 }), true, "filters and paging do not affect a sort preset");
});

test("metric presets ignore IDs, labels and presentation but not the calculation", () => {
  const byRepository = preset(writingMetricPresets, "By repository");
  const query = aggregations => ({ ...EMPTY_QUERY, aggregations, where: [{ field: "repository_name", op: "=", value: "example/repo" }], offset: 50 });
  const renamed = byRepository.aggregations.map((metric, index) => ({ ...metric, id: `manual-${index}`, label: "My metric", display: { kind: "pie" } }));
  assert.equal(presetMatches(byRepository, query(renamed)), true);
  assert.equal(presetMatches(byRepository, query([...renamed].reverse())), false, "metric order is part of the preset");
  assert.equal(presetMatches(byRepository, query(renamed.slice(0, 1))), false);
  assert.equal(presetMatches(byRepository, query([...renamed, { id: "extra", op: "count", groupBy: [] }])), false);
  assert.equal(presetMatches(byRepository, query(renamed.map(metric => ({ ...metric, groupBy: ["providers"] })))), false);
  assert.equal(presetMatches(byRepository, query(renamed.map(metric => ({ ...metric, op: "avg" })))), false);
  assert.equal(presetMatches(byRepository, query([])), false);
  assert.equal(presetMatches(byRepository, EMPTY_QUERY), false);
});

test("chart presets compare only the chart settings they name", () => {
  assert.deepEqual(matching(usageChartPresets, EMPTY_QUERY, chart), ["Weekly cache mix"]);
  assert.deepEqual(matching(usageChartPresets, EMPTY_QUERY, { ...chart, hours: 12, days: 5, rollingPeriods: 3, scale: "logarithmic" }), ["Weekly cache mix"]);
  assert.deepEqual(matching(usageChartPresets, EMPTY_QUERY, { ...chart, period: "month" }), []);
  assert.deepEqual(matching(usageChartPresets, EMPTY_QUERY, { ...chart, metric: "cost", split: "provider", period: "day" }), ["Daily cost"]);
  assert.deepEqual(matching(usageChartPresets, EMPTY_QUERY), [], "without a chart nothing matches");
  assert.deepEqual(matching(usageChartPresets, { ...EMPTY_QUERY, orderBy: [{ field: "day", dir: "desc" }], aggregations: writingMetricPresets[0].aggregations }, chart), ["Weekly cache mix"], "table queries do not affect chart presets");
});

test("applying a preset changes only what it controls and returns to the first page", () => {
  const where = [{ field: "repository_name", op: "=", value: "example/repo" }];
  const start = { ...EMPTY_QUERY, where, offset: 50, orderBy: preset(writingPresets, "Deepest conversations").orderBy, aggregations: preset(writingMetricPresets, "By source").aggregations };
  const sorted = applyUsagePreset(start, preset(writingPresets, "Most hands-on"));
  assert.deepEqual(sorted, { ...start, orderBy: preset(writingPresets, "Most hands-on").orderBy, offset: 0 });
  const measured = applyUsagePreset(sorted, preset(writingMetricPresets, "By repository"));
  assert.deepEqual(measured, { ...sorted, aggregations: preset(writingMetricPresets, "By repository").aggregations });
  assert.deepEqual(matching(writingPresets, measured), ["Most hands-on"]);
  assert.deepEqual(matching(writingMetricPresets, measured), ["By repository"]);
  const cleared = applyUsagePreset({ ...measured, offset: 25 }, null);
  assert.deepEqual(cleared, { ...measured, aggregations: [] }, "clearing metrics keeps the sort");
  assert.deepEqual(matching(writingMetricPresets, cleared), []);
  assert.deepEqual(matching(writingPresets, cleared), ["Most hands-on"]);
});

test("each preset row highlights exactly the preset just applied", () => {
  for (const [name, list] of rows) {
    assert.equal(new Set(list.map(item => item.label)).size, list.length, `${name}: labels are unique`);
    for (const item of list) {
      assert.equal([item.orderBy, item.aggregations, item.chart].filter(Boolean).length, 1, `${name} ${item.label}: one kind of setting`);
      assert.deepEqual(matching(list, applyUsagePreset(EMPTY_QUERY, item)), [item.label], `${name} ${item.label}`);
    }
  }
  assert.equal(new Set(usageChartPresets.map(item => item.label)).size, usageChartPresets.length);
  for (const item of usageChartPresets) assert.deepEqual(matching(usageChartPresets, EMPTY_QUERY, { ...chart, ...item.chart }), [item.label], item.label);
});

test("presets use real fields of their table and metrics compile against it", () => {
  for (const [name, list, schema] of rows) for (const item of list) {
    for (const clause of item.orderBy ?? []) assert.ok(schema.fields.some(field => field.name === clause.field), `${name} ${item.label}: ${clause.field}`);
    if (item.aggregations) assert.deepEqual(toMetricQuery({ ...EMPTY_QUERY, aggregations: item.aggregations }, schema, execution).diagnostics, [], `${name} ${item.label}`);
  }
});
