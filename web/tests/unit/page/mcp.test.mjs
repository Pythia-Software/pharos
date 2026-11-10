import { capabilities, answerQuery } from "../../support/query-server.mjs";
import { schemas } from "../helpers/recipes.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { createMCPMonitor } from "../../../src/mcp-monitor";
import { loadMCPCall, mcpDetailNumber } from "../../../src/mcp-call-details";

const status = { enabled: true, command: "pharos", args: ["mcp"], note: "Ready" };

function harness(t) {
  const requests = [], events = [], errors = [], prior = global.fetch;
  global.fetch = (url, init) => new Promise(resolve => {
    requests.push({ url, signal: init.signal, reply: (body, code = 200) => resolve(Response.json(body, { status: code })) });
  });
  t.after(() => { global.fetch = prior; });
  const monitor = createMCPMonitor({
    status: value => events.push({ value }),
    error: error => { errors.push(error); if (error) events.push({ error }); },
  });
  t.after(() => monitor.stop());
  return { monitor, requests, events, errors };
}

test("connection settings are read alone, without overlapping polls", async t => {
  const { monitor, requests, events } = harness(t);
  const pending = monitor.refresh();
  assert.equal(monitor.refresh(), pending, "a poll cannot pile up another request");
  assert.deepEqual(requests.map(request => request.url), ["/api/mcp"]);
  requests[0].reply(status);
  await pending;
  assert.deepEqual(events, [{ value: status }]);
  const next = monitor.refresh();
  assert.equal(requests.length, 2, "polling resumes once the prior read settles");
  requests[1].reply(status);
  await next;
});

test("a failed settings read is reported, and a later refresh recovers", async t => {
  const { monitor, requests, events, errors } = harness(t);
  const pending = monitor.refresh();
  requests[0].reply({ error: "Settings unavailable" }, 503);
  await pending;
  assert.equal(events[0].error.message, "Settings unavailable");
  const next = monitor.refresh();
  assert.equal(errors.at(-1).message, "Settings unavailable", "a pending retry keeps the last error visible");
  requests[1].reply(status);
  await next;
  assert.equal(errors.at(-1), null);
  assert.deepEqual(events.at(-1), { value: status });
});

test("explicit refresh aborts the old read and prevents a late reply overwriting the new state", async t => {
  const { monitor, requests, events } = harness(t);
  const old = monitor.refresh();
  const next = monitor.refresh(true);
  assert.ok(requests[0].signal.aborted);
  requests[1].reply({ ...status, enabled: false });
  await next;
  requests[0].reply(status);
  await old;
  assert.deepEqual(events, [{ value: { ...status, enabled: false } }]);
});

test("leaving MCP cancels reads without publishing errors or stale state on return", async t => {
  const { monitor, requests, events } = harness(t);
  const old = monitor.refresh();
  monitor.stop();
  assert.ok(requests.every(request => request.signal.aborted));
  const next = monitor.refresh();
  requests[0].reply({ error: "late failure" }, 500);
  await old;
  assert.deepEqual(events, []);
  requests[1].reply(status);
  await next;
  assert.deepEqual(events, [{ value: status }]);
});

test("a stalled MCP read reaches a terminal timeout and remains retryable", async t => {
  const { monitor, requests, events } = harness(t);
  const deadlines = [];
  t.mock.method(AbortSignal, "timeout", () => {
    const controller = new AbortController(); deadlines.push(controller); return controller.signal;
  });
  const pending = monitor.refresh();
  deadlines[0].abort(new DOMException("Deadline exceeded", "TimeoutError"));
  requests[0].reply(status);
  await pending;
  assert.match(events[0].error.message, /took too long/);
  const next = monitor.refresh(true);
  requests[1].reply(status);
  await next;
  assert.deepEqual(events.at(-1), { value: status });
});

test("MCP details load the full call by ID independently of the table projection", async t => {
  const previous = global.fetch, controller = new AbortController();
  t.after(() => { global.fetch = previous; });
  const call = { id: 607, tool_name: "search_conversations", called_at: "2026-10-20T17:00:00Z", arguments_json: '{"query":"parser"}', status: "ok", error_text: null, duration_ms: 0, response_bytes: 7109, estimated_output_tokens: 2370, result_count: null, truncated: true };
  let received;
  global.fetch = async (url, init) => { received = init.signal; if(url.endsWith("capabilities"))return Response.json(capabilities());assert.equal(url,"/api/query/mcp_calls/query-v3");return Response.json(answerQuery("query-v3",JSON.parse(init.body),[call],schemas.mcp_calls)); };
  const projected = { id: 607, tool_name: "search_conversations" };
  const result = await loadMCPCall(String(projected.id), controller.signal);
  for(const [key,value]of Object.entries(call))assert.deepEqual(result[key],value,key);
  assert.equal(result.response_bytes, 7109);
  assert.equal(result.arguments_json, call.arguments_json);
  controller.abort();
  assert.equal(received.aborted, true);
});

test("a pruned MCP call or a failed detail read is reported rather than rendered as a partial record", async t => {
  const previous = global.fetch;
  t.after(() => { global.fetch = previous; });
  global.fetch = async (url,init) => url.endsWith("capabilities") ? Response.json(capabilities()) : Response.json(answerQuery("query-v3",JSON.parse(init.body),[],schemas.mcp_calls));
  await assert.rejects(loadMCPCall("607"), /no longer available/);
  global.fetch = async () => Response.json({ error: "Call read failed" }, { status: 500 });
  await assert.rejects(loadMCPCall("607"), /Call read failed/);
});

test("unavailable MCP detail numbers remain unknown and recorded zeroes stay visible", () => {
  for (const value of [undefined, null, NaN, Infinity, "", {}, false]) assert.equal(mcpDetailNumber(value), "—");
  assert.equal(mcpDetailNumber(0), "0");
  assert.equal(mcpDetailNumber(7109), (7109).toLocaleString());
});
