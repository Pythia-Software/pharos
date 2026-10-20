import assert from "node:assert/strict";
import test from "node:test";
import { core, schemas } from "../helpers/recipes.mjs";
import { answerQuery, population } from "../../support/query-server.mjs";
import { MetricRequestError, metricRequest, metricResults, postMetrics, postQueryV3Metrics } from "../../../src/metric-request";
import { tokenChartMetrics, tokenChartSplit, tokenSummaryMetrics, tokenTypes } from "../../../src/token-chart-request";

const response = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

// Serves dataset's fake rows on the production transport and records each request.
function server(t, dataset, reply = () => undefined) {
  const prior = global.fetch, rows = population(schemas[dataset]), requests = [];
  global.fetch = async (url, init = {}) => {
    const path = String(url), body = init.body ? JSON.parse(init.body) : undefined;
    requests.push({ path, body });
    const replaced = reply(path, body);
    if (replaced) return replaced;
    return response(answerQuery(path.split("/").at(-1), body, rows, schemas[dataset]));
  };
  t.after(() => { global.fetch = prior; });
  return { requests, rows };
}

test("a metric request is a V2 body of the clauses' calculations over where", () => {
  const where = [{ any: [{ field: "day", op: "=", value: "2026-09-01" }, { field: "day", op: "=", value: "2026-09-02" }] }];
  const request = metricRequest(schemas.tool_calls, where, [{ id: "time", op: "sum", field: "duration_ms", groupBy: ["day"], label: "Time" }, { id: "population", op: "count", groupBy: [] }]);
  assert.equal(request.version, 2);
  assert.deepEqual(request.where, where);
  assert.deepEqual(request.metrics, [{ id: "time", op: "sum", field: "duration_ms", groupBy: ["day"] }, { id: "population", op: "count", groupBy: [] }], "labels are presentation");
  assert.deepEqual(request.diagnostics, []);
  assert.throws(() => metricRequest(schemas.tool_calls, [{ any: [{ field: "day", op: "=", value: "2026-09-01" }] }], [{ id: "n", op: "count", groupBy: [] }]), MetricRequestError, "a request the server would refuse fails before it is sent");
});

test("a failed or missing metric fails the read instead of reading as zero", () => {
  const clauses = [{ id: "a", op: "count", groupBy: [] }, { id: "b", op: "count", groupBy: ["tool_name"] }];
  const ok = { metrics: [{ id: "b", buckets: [{ keys: ["Bash"], value: 2, count: 2 }] }, { id: "a", buckets: [{ keys: [], value: 2, count: 2 }] }] };
  assert.deepEqual(metricResults(ok, clauses).map(metric => metric.id), ["a", "b"], "one result per clause, in order");
  const failed = { metrics: [ok.metrics[1], { id: "b", buckets: [], error: "group population exceeds host budget" }] };
  assert.throws(() => metricResults(failed, clauses), error => error instanceof MetricRequestError && error.resourceLimit && /group population/.test(error.message));
  assert.throws(() => metricResults({ metrics: [{ id: "b", buckets: [], error: "type_error" }, ok.metrics[1]] }, clauses), error => error instanceof MetricRequestError && !error.resourceLimit);
  assert.throws(() => metricResults({ metrics: [ok.metrics[1]] }, clauses), /omitted the metric b/);
  assert.throws(() => metricResults({ metrics: [ok.metrics[1], { id: "b", buckets: [], error: "group population exceeds the 50,000-group host budget" }] }, clauses), error => !error.resourceLimit, "Machine Time's own budget is not one a larger executor answers");
});

test("metrics post to the dataset's /metrics, and a population limit is a resource limit", async t => {
  const { requests, rows } = server(t, "tool_calls");
  const [calls] = await postMetrics("tool_calls", schemas.tool_calls, [], [{ id: "calls", op: "count", groupBy: [] }]);
  assert.equal(calls.buckets[0].value, rows.length);
  assert.equal(requests[0].path, "/api/query/tool_calls/metrics");
  server(t, "tool_calls", () => response({ error: "resource_limit: matching population exceeds host budget" }, 400));
  await assert.rejects(postMetrics("tool_calls", schemas.tool_calls, [], [{ id: "calls", op: "count", groupBy: [] }]), error => error instanceof MetricRequestError && error.resourceLimit);
  server(t, "tool_calls", () => response({ error: "boom" }, 500));
  await assert.rejects(postMetrics("tool_calls", schemas.tool_calls, [], [{ id: "calls", op: "count", groupBy: [] }]), error => error.message === "boom" && !error.resourceLimit);
});

test("the larger-budget read runs the same metrics through /query-v3 without rows", async t => {
  const { requests, rows } = server(t, "tool_calls");
  const where = [{ field: "status", op: "=", value: "ok" }];
  const [time, calls] = await postQueryV3Metrics("tool_calls", schemas.tool_calls, where, [{ id: "time", op: "sum", field: "duration_ms", groupBy: ["tool_name"] }, { id: "calls", op: "count", groupBy: [] }]);
  assert.deepEqual(requests.map(request => request.path), ["/api/query/tool_calls/capabilities", "/api/query/tool_calls/query-v3"]);
  const sent = requests[1].body;
  assert.equal(sent.version, 3);
  assert.equal(sent.rows, false);
  assert.deepEqual(sent.result.where, where);
  const kept = rows.filter(row => row.status === "ok");
  assert.equal(calls.buckets[0].value, kept.length);
  assert.equal(time.buckets.reduce((total, bucket) => total + bucket.value, 0), kept.reduce((total, row) => total + row.duration_ms, 0));
});

test("the Token chart sums by its period's field and split; cost by type is by provider", () => {
  const view = (period, split = "type", metric = "tokens") => ({ period, split, metric });
  for (const [period, field] of [["hour", "hour"], ["hours", "hour"], ["day", "day"], ["days", "day"], ["week", "week"], ["month", "month"]]) {
    assert.deepEqual(tokenChartMetrics(view(period)).map(metric => [metric.id, metric.field, metric.groupBy]), tokenTypes.map(type => [`${type.key}:${field}`, type.key, [field]]), period);
    for (const split of ["provider", "model_family", "model_effort", "effort", "repository_name", "session_kind"]) {
      assert.deepEqual(tokenChartMetrics(view(period, split)).map(metric => [metric.op, metric.field, metric.groupBy]), [["sum", "total_tokens", [field, split]]]);
      assert.deepEqual(tokenChartMetrics(view(period, split, "percent_cost")).map(metric => [metric.field, metric.groupBy]), [["cost_usd", [field, split]]]);
    }
  }
  assert.equal(tokenChartSplit(view("week", "type", "cost")), "provider");
  assert.equal(tokenChartSplit(view("week", "type", "percent")), "type");
  assert.deepEqual(tokenChartMetrics(view("week", "type", "cost")).map(metric => [metric.field, metric.groupBy]), [["cost_usd", ["week", "provider"]]]);
  assert.deepEqual(tokenSummaryMetrics.map(metric => metric.id), ["total_tokens:", "cost_usd:", "uncached_input_tokens:", "cache_read_input_tokens:", "output_tokens:", "agent_sessions"]);
  for (const clauses of [tokenSummaryMetrics, tokenChartMetrics(view("hour", "repository_name"))]) assert.deepEqual(core.toMetricQuery({ ...core.EMPTY_QUERY, aggregations: clauses }, schemas.usage).diagnostics, []);
});
