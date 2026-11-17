import test from "node:test";
import assert from "node:assert/strict";
import fixture from "../../../../testdata/teams/typed-usage-query-v2.json";
import registry from "../../../../testdata/teams/usage-registry-v1.json";
import { applyAggregations, decodeQueryStrict, loadSchema, toQueryRequestV3 } from "@pythia-software/query-table-core";
import usage from "../../../../schemas/usage.schema.json";
import { capabilities, execution } from "../helpers/recipes.mjs";


test("Teams usage fixture preserves the browser V3 population and metrics", () => {
  const url = new URL(fixture.query_url, "https://synthetic.invalid");
  const query = decodeQueryStrict(url.searchParams.get("q_usage"));
  const request = toQueryRequestV3(query, loadSchema(usage), capabilities, execution, { rows: false, now: 0 });
  assert.deepEqual(request.diagnostics ?? [], []);
  assert.deepEqual(request.source.where, [
    { field: "first_usage_at", op: ">=", value: fixture.window.start_at },
    { field: "first_usage_at", op: "<", value: fixture.window.end_at },
  ]);
  assert.equal(request.metrics.length, fixture.metrics.length);
  for (let i = 0; i < fixture.metrics.length; i++) {
    assert.equal(request.metrics[i].id, fixture.metrics[i].id);
    assert.equal(request.metrics[i].field, fixture.metrics[i].field);
    assert.equal(request.metrics[i].op, fixture.metrics[i].op);
    assert.deepEqual(request.metrics[i].groupBy, fixture.groups);
  }
});

test("Teams synthetic registry fixes units, scope and rounding", () => {
  assert.equal(registry.registry, fixture.registry);
  assert.equal(registry.grain, "request");
  assert.deepEqual(registry.group_keys.map(key => key.id), fixture.groups);
  assert.equal(registry.measures[1].scale, 6);
  assert.equal(registry.measures[1].rounding, "half_up_per_request");
  assert.equal(registry.group_keys[2].witness, "owned_non_private_same_population");
});

test("Teams scoped population agrees with browser metric aggregation", () => {
  const query = decodeQueryStrict(new URL(fixture.query_url, "https://synthetic.invalid").searchParams.get("q_usage"));
  const eligible = fixture.synthetic_facts.filter(f => f.owned && f.first_usage_at >= fixture.window.start_at && f.first_usage_at < fixture.window.end_at);
  const witnesses = new Set(eligible.filter(f => f.protection === "non_private").map(f => f.project_name));
  const rows = eligible.filter(f => witnesses.has(f.project_name)).map(f => ({ ...f, cost_usd: f.cost_micros === null ? null : f.cost_micros / 1_000_000 }));
  const metrics = applyAggregations(rows, query, loadSchema(usage)).metrics;
  const tokens = metrics.find(m => m.id === "total_tokens");
  assert.deepEqual(tokens.buckets.map(b => [b.keys[0] + "T00:00:00Z", ...b.keys.slice(1), b.value]).sort(), fixture.expected_rows.map(r => [...r.key, r.values.total_tokens]).sort());
  const cost = metrics.find(m => m.id === "cost_usd");
  // Browser buckets are civil UTC dates; the wire uses their midnight UTC
  // instant. Price completeness comes from the same eligible/priced reducers.
  for (const bucket of cost.buckets) {
    const key = [bucket.keys[0] + "T00:00:00Z", ...bucket.keys.slice(1)];
    const members = rows.filter(r => r.day === bucket.keys[0] && r.model_family === bucket.keys[1] && r.project_name === bucket.keys[2]);
    const counts = members.reduce((a, r) => ({ eligible: a.eligible + 1, priced: a.priced + Number(r.cost_micros !== null) }), { eligible: 0, priced: 0 });
    const value = counts.eligible === counts.priced ? Math.round(bucket.value * 1_000_000) : null;
    const expected = fixture.expected_rows.find(r => JSON.stringify(r.key) === JSON.stringify(key));
    assert.ok(expected);
    assert.equal(value, expected.values.cost_usd);
  }
  assert.equal(cost.buckets.length, fixture.expected_rows.length);
});

test("Teams canonical keys reject unpaired surrogates and forbidden labels", () => {
  const key = (day, model, project) => {
    if (![day, model, project].every(value => value.isWellFormed()) || /[\u0000\u2028\u2029]/u.test(project)) throw new Error("invalid key");
    return JSON.stringify([day, model, project]);
  };
  for (const vector of fixture.canonical_key_vectors) assert.equal(key("2026-11-09T00:00:00Z", "family_a", vector.project), vector.canonical);
  assert.equal(key("2026-11-09T00:00:00Z", "family_a", 'A<&"'), '["2026-11-09T00:00:00Z","family_a","A<&\\\""]');
  assert.equal(key("2026-11-09T00:00:00Z", "family_a", "😀"), '["2026-11-09T00:00:00Z","family_a","😀"]');
  for (const project of ["\ud800", "\udc00", "x\u0000y", "x\u2028y", "x\u2029y"]) assert.throws(() => key("2026-11-09T00:00:00Z", "family_a", project));
});
