import assert from "node:assert/strict";
import test from "node:test";
import { core, schemas } from "../helpers/recipes.mjs";
import { answerQuery, population } from "../../support/query-server.mjs";
import { MetricRequestError, metricRequest, metricResults, postMetrics, postQueryV3Metrics } from "../../../src/metric-request";
import { queryRequest, queryFetch, QueryRequestError, shouldSkipQueryOnRestore } from "../../../src/query-request";
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


function signalCompatibility(t, fallback) {
 if (!fallback) return;
 const descriptor=Object.getOwnPropertyDescriptor(AbortSignal,'any');
 Object.defineProperty(AbortSignal,'any',{configurable:true,writable:true,value:undefined});
 t.after(()=>Object.defineProperty(AbortSignal,'any',descriptor));
}

for (const fallback of [false,true]) {
const compatibility=fallback?'without AbortSignal.any':'with native signal composition';

test(`query deadlines abort stalled fetches and response bodies, with a recoverable error (${compatibility})`,async t=>{
 signalCompatibility(t,fallback);
 const original=global.fetch;
 t.after(()=>{global.fetch=original});
 let deadline;
 t.mock.method(AbortSignal,'timeout',()=>{deadline=new AbortController();return deadline.signal});
 for(const stage of ['headers','body'])for(const caller of [undefined,new AbortController().signal]){
  global.fetch=async (_url,{signal})=>{
   const pending=()=>new Promise((_resolve,reject)=>{if(signal.aborted)reject(signal.reason);else signal.addEventListener('abort',()=>reject(signal.reason),{once:true});});
   return stage==='headers'?pending():{ok:true,json:pending};
  };
  const request=queryRequest('/api/query/tool_calls/metrics',{signal:caller});
  const rejected=assert.rejects(request,error=>error instanceof QueryRequestError&&/took too long/.test(error.message));
  deadline.abort(new DOMException('Deadline exceeded','TimeoutError'));
  await rejected;
 }
});

test(`superseded queries preserve caller cancellation and service errors keep their status (${compatibility})`,async t=>{
 signalCompatibility(t,fallback);
 const original=global.fetch;
 t.after(()=>{global.fetch=original});
 const caller=new AbortController();
 global.fetch=async(_url,{signal})=>new Promise((_resolve,reject)=>signal.addEventListener('abort',()=>reject(signal.reason),{once:true}));
 const request=queryRequest('/api/query/usage/rows-v2',{signal:caller.signal});
 const reason=new DOMException('New query','AbortError'),rejected=assert.rejects(request,error=>error===reason);
 caller.abort(reason);await rejected;
 global.fetch=async(_url,{signal})=>{signal.throwIfAborted();return response({rows:[],total:0})};
 await assert.rejects(queryRequest('/api/query/usage/rows-v2',{signal:caller.signal}),error=>error===reason,'already cancelled callers stay cancelled');
 global.fetch=async()=>response({error:{message:'Unknown field'}},422);
 await assert.rejects(queryRequest('/api/query/usage/rows-v2'),error=>error instanceof QueryRequestError&&error.status===422&&error.message==='Unknown field');
 global.fetch=async()=>response({rows:[],total:0});
 assert.deepEqual(await queryRequest('/api/query/usage/rows-v2'),{rows:[],total:0},'empty results are successful');
});

test(`HTTP stores receive bounded fetches that still observe caller cancellation (${compatibility})`,async t=>{
 signalCompatibility(t,fallback);
 const original=global.fetch;
 t.after(()=>{global.fetch=original});
 let deadline;
 t.mock.method(AbortSignal,'timeout',()=>{deadline=new AbortController();return deadline.signal});
 let signal;
 global.fetch=async(_input,init)=>{signal=init.signal;return response({ok:true})};
 for(const source of ['caller','deadline'])for(const input of ['url','request']){
  const caller=new AbortController(),url='http://localhost/api/query-snapshots?dataset=usage';
  const result=await (input==='url'?queryFetch(url,{signal:caller.signal}):queryFetch(new Request(url,{signal:caller.signal})));
  assert.deepEqual(await result.json(),{ok:true});
  const reason=new DOMException(source,source==='caller'?'AbortError':'TimeoutError');
  (source==='caller'?caller:deadline).abort(reason);
  assert.equal(signal.aborted,true);
  assert.equal(signal.reason,reason);
  (source==='caller'?deadline:caller).abort(new Error('late abort'));
  assert.equal(signal.reason,reason,'the first reason wins');
 }
 const caller=AbortSignal.abort(new DOMException('Already cancelled','AbortError'));
 await queryFetch('/api/query-computed-columns?dataset=usage',{signal:caller});
 assert.equal(signal.reason,caller.reason);
});
}

test('only query rejection and deadline expiry skip automatic restoration',()=>{
 for(const status of [undefined,400,422])assert.equal(shouldSkipQueryOnRestore(new QueryRequestError('failed',status)),true);
 for(const status of [401,403,404,429,500,502,503,504])assert.equal(shouldSkipQueryOnRestore(new QueryRequestError('failed',status)),false);
 assert.equal(shouldSkipQueryOnRestore(new TypeError('Failed to fetch')),false);
 assert.equal(shouldSkipQueryOnRestore(new DOMException('Superseded','AbortError')),false);
});
