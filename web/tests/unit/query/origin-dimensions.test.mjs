import assert from "node:assert/strict";
import test from "node:test";
import { applyQuery, EMPTY_QUERY, encodeQuery, decodeQueryStrict } from "@pythia-software/query-table-core";
import { schemas, execution } from "../helpers/recipes.mjs";
import { originDimensions, originLabel, loadOriginMetadata } from "../../../src/origin-dimensions";
import { machineWhere, machineWindow } from "../../../src/machine-activity-data";
import { trendQuery } from "../../../src/dashboard-trend-model";
import { tokenAnalysisCatalog } from "../../../src/token-analysis-catalog";
import { toolSummaryCallFilters, toolMetricPresentation } from "../../../src/tools-dashboard-model";

test("origin filters preserve device IDs and null groups across query links", () => {
  const where = [{ field: "channel", op: "=", value: "desktop" }, { field: "machine_id", op: "=", value: "device-1" }];
  const query = { ...EMPTY_QUERY, where, select: ["id", "channel", "machine_id"].map(field => ({ field })) };
  assert.deepEqual(decodeQueryStrict(encodeQuery(query)), query);
  const rows = [{ id: "child", channel: "desktop", machine_id: "device-1", provider: "claude" }, { id: "unknown", channel: null, machine_id: null }, { id: "cli", channel: "cli", machine_id: "device-1" }];
  assert.deepEqual(applyQuery(rows, query, schemas.usage, execution).rows.map(row => row.id), ["child"]);
  assert.equal(originLabel("channel", null), "Unknown channel");
  assert.equal(originLabel("machine_id", "environment:web"), "Web (device unknown)");
  assert.equal(originLabel("channel", "desktop"), "Desktop app");
});

test("machine scopes and summary drilldowns carry initiation and execution separately", () => {
  const scope = { days: 0, repository: "", provider: "", model: "", channel: "desktop", machine_id: "laptop", execution_machine_id: "worker" };
  const where = machineWhere(scope, machineWindow(0));
  assert.deepEqual(where, originDimensions.map(([field]) => ({ field, op: "=", value: scope[field] })));
  const filters = toolSummaryCallFilters(scope);
  for (const [field] of originDimensions) {
    assert.deepEqual(filters.find(clause => clause.field === field), { field, op: "=", value: scope[field] });
    assert.deepEqual(trendQuery(tokenAnalysisCatalog, { metric: "tokens", period: "day", split: field }, []).query.aggregations[0].groupBy, ["day", field]);
  }
});

test("device discovery changes labels without changing saved filter values", async t => {
  const oldWindow = global.window, oldFetch = global.fetch;
  t.after(() => { global.window = oldWindow; global.fetch = oldFetch; });
  global.window = {};
  let name = "Laptop";
  global.fetch = async () => new Response(JSON.stringify({ machines: [{ id: "device-1", label: name }] }), { headers: { "Content-Type": "application/json" } });
  const metric = { id: "tools_calls", buckets: [{ keys: ["2026-10-26", "device-1"], value: 3 }, { keys: ["2026-10-26", null], value: 1 }] };
  assert.equal(toolMetricPresentation(metric, ["day", "machine_id"]).metric.buckets[0].keys[1], "device-1");
  await loadOriginMetadata(true);
  assert.equal(originLabel("machine_id", "device-1"), "Laptop");
  const presented = toolMetricPresentation(metric, ["day", "machine_id"]);
  assert.deepEqual(presented.metric.buckets.map(bucket => bucket.keys), [["2026-10-26", "Laptop"], ["2026-10-26", "Unknown machine"]]);
  for (let i = 0; i < metric.buckets.length; i++) assert.equal(presented.sourceBucket({ ...presented.metric.buckets[i] }), metric.buckets[i]);
  assert.equal(metric.buckets[0].keys[1], "device-1", "presentation cannot rewrite saved identities");
  const query = encodeQuery({ ...EMPTY_QUERY, where: [{ field: "machine_id", op: "=", value: "device-1" }] });
  name = "Renamed laptop";
  await loadOriginMetadata(true);
  assert.equal(originLabel("machine_id", "device-1"), name);
  assert.equal(toolMetricPresentation(metric, ["day", "machine_id"]).metric.buckets[0].keys[1], name);
  assert.equal(decodeQueryStrict(query).where[0].value, "device-1");
});

test("origin metadata retries failed HTTP, network, and malformed responses", async t => {
  const oldWindow = global.window, oldFetch = global.fetch;
  t.after(() => { global.window = oldWindow; global.fetch = oldFetch; });
  global.window = {};
  for (const fail of [async () => new Response("unavailable", { status: 503 }), async () => { throw new Error("offline"); }, async () => new Response("invalid json")]) {
    let calls = 0;
    global.fetch = async () => ++calls === 1 ? fail() : new Response(JSON.stringify({ machines: [{ id: "retry-device", label: "Recovered Mac" }] }));
    await loadOriginMetadata(true);
    await loadOriginMetadata();
    assert.equal(calls, 2);
    assert.equal(originLabel("machine_id", "retry-device"), "Recovered Mac");
  }
});

test("tool chart labels preserve distinct machines with the same name and channel drilldowns", async t => {
  const oldWindow = global.window, oldFetch = global.fetch;
  t.after(() => { global.window = oldWindow; global.fetch = oldFetch; });
  global.window = {};
  global.fetch = async () => new Response(JSON.stringify({ machines: ["machine-1", "machine-2"].map(id => ({ id, label: "Laptop" })) }));
  await loadOriginMetadata(true);
  const raw = { id: "calls", buckets: ["machine-1", "machine-2"].map(id => ({ keys: [id], value: 1 })) };
  const presentation = toolMetricPresentation(raw, ["execution_machine_id"]);
  assert.notEqual(presentation.metric.buckets[0].keys[0], presentation.metric.buckets[1].keys[0]);
  for (let i = 0; i < 2; i++) assert.equal(presentation.sourceBucket(presentation.metric.buckets[i]), raw.buckets[i]);
  const channels = { id: "calls", buckets: [{ keys: ["desktop"], value: 1 }] };
  const channelView = toolMetricPresentation(channels, ["channel"]);
  assert.equal(channelView.metric.buckets[0].keys[0], "Desktop app");
  assert.equal(channelView.sourceBucket(channelView.metric.buckets[0]).keys[0], "desktop");
});
