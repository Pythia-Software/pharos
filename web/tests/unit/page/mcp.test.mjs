import assert from "node:assert/strict";
import test from "node:test";
import { createMCPMonitor } from "../../../src/mcp-monitor";
import { loadMCPCall, mcpDetailNumber } from "../../../src/mcp-call-details";

const status = { enabled: true, command: "pharos", args: ["mcp"], note: "Ready" };
const history = { stats: { total_calls: 4, failed_calls: 1, average_output_tokens: 30, largest_output_tokens: 80, truncated_calls: 0 } };
const flush = () => new Promise(resolve => setImmediate(resolve));

function harness(t) {
  const requests = [], events = [], errors = new Map(), prior = global.fetch;
  global.fetch = (url, init) => new Promise(resolve => {
    requests.push({ url, signal: init.signal, reply: (body, code = 200) => resolve(Response.json(body, { status: code })) });
  });
  t.after(() => { global.fetch = prior; });
  const monitor = createMCPMonitor({
    status: value => events.push({ part: "status", value }),
    history: value => events.push({ part: "history", value }),
    error: (part, error) => { errors.set(part, error); if (error) events.push({ part, error }); },
  });
  t.after(() => monitor.stop());
  return { monitor, requests, events, errors };
}

test("connection settings are published while history is stalled, without overlapping polls", async t => {
  const { monitor, requests, events } = harness(t);
  const pending = monitor.refresh();
  assert.equal(monitor.refresh(), pending);
  assert.equal(requests.length, 2);
  assert.deepEqual(requests.map(request => request.url), ["/api/mcp", "/api/mcp/calls?limit=1"]);
  requests[0].reply(status);
  await flush();
  assert.deepEqual(events, [{ part: "status", value: status }]);
  assert.equal(monitor.refresh(), pending, "a poll cannot pile up another history request");
  requests[1].reply(history);
  await pending;
  assert.deepEqual(events[1], { part: "history", value: history });
  const next = monitor.refresh();
  assert.equal(requests.length, 4, "polling resumes once the prior reads settle");
  requests[2].reply(status); requests[3].reply(history);
  await next;
});

test("history remains usable if connection settings fail, and a later refresh recovers", async t => {
  const { monitor, requests, events, errors } = harness(t);
  const pending = monitor.refresh();
  requests[1].reply(history);
  await flush();
  assert.deepEqual(events, [{ part: "history", value: history }]);
  requests[0].reply({ error: "Settings unavailable" }, 503);
  await pending;
  assert.equal(events[1].part, "status");
  assert.equal(events[1].error.message, "Settings unavailable");
  const next = monitor.refresh();
  assert.equal(errors.get("status").message, "Settings unavailable", "a pending retry keeps the last error visible");
  requests[2].reply(status); requests[3].reply(history);
  await next;
  assert.equal(errors.get("status"), null);
  assert.ok(events.some(event => event.value?.command === status.command));
});

test("explicit refresh aborts old reads and prevents late replies overwriting the new state", async t => {
  const { monitor, requests, events } = harness(t);
  const old = monitor.refresh();
  const next = monitor.refresh(true);
  assert.ok(requests.slice(0, 2).every(request => request.signal.aborted));
  requests[2].reply({ ...status, enabled: false }); requests[3].reply(history);
  await next;
  requests[0].reply(status); requests[1].reply({ stats: { ...history.stats, total_calls: 100 } });
  await old;
  assert.deepEqual(events, [{ part: "status", value: { ...status, enabled: false } }, { part: "history", value: history }]);
});

test("leaving MCP cancels reads without publishing errors or stale state on return", async t => {
  const { monitor, requests, events } = harness(t);
  const old = monitor.refresh();
  monitor.stop();
  assert.ok(requests.every(request => request.signal.aborted));
  const next = monitor.refresh();
  requests[0].reply({ error: "late failure" }, 500); requests[1].reply(history);
  await old;
  assert.deepEqual(events, []);
  requests[2].reply(status); requests[3].reply(history);
  await next;
  assert.equal(events.length, 2);
});

test("a stalled MCP read reaches a terminal timeout and remains retryable", async t => {
  const { monitor, requests, events } = harness(t);
  const deadlines = [];
  t.mock.method(AbortSignal, "timeout", () => {
    const controller = new AbortController(); deadlines.push(controller); return controller.signal;
  });
  const pending = monitor.refresh();
  requests[0].reply(status);
  deadlines[1].abort(new DOMException("Deadline exceeded", "TimeoutError"));
  requests[1].reply(history);
  await pending;
  assert.match(events.find(event => event.part === "history").error.message, /took too long/);
  assert.ok(events.some(event => event.part === "status" && event.value));
  const next = monitor.refresh(true);
  requests[2].reply(status); requests[3].reply(history);
  await next;
  assert.deepEqual(events.at(-1), { part: "history", value: history });
});


test("MCP details load the full call by ID independently of the table projection", async t => {
  const previous = global.fetch, controller = new AbortController();
  t.after(() => { global.fetch = previous; });
  const call = { id: 607, tool_name: "search_conversations", called_at: "2026-10-20T17:00:00Z", arguments_json: '{"query":"parser"}', status: "ok", error_text: null, duration_ms: 0, response_bytes: 7109, estimated_output_tokens: 2370, result_count: null, truncated: true };
  let received;
  global.fetch = async (url, init) => { received = init.signal; assert.equal(url, "/api/query/mcp_calls/item?id=607"); return Response.json({ row: call }); };
  const projected = { id: 607, tool_name: "search_conversations" };
  const result = await loadMCPCall(String(projected.id), controller.signal);
  assert.deepEqual(result, call);
  assert.equal(result.response_bytes, 7109);
  assert.equal(result.arguments_json, call.arguments_json);
  controller.abort();
  assert.equal(received.aborted, true);
});

test("a pruned MCP call or a failed detail read is reported rather than rendered as a partial record", async t => {
  const previous = global.fetch;
  t.after(() => { global.fetch = previous; });
  global.fetch = async () => Response.json({ row: null });
  await assert.rejects(loadMCPCall("607"), /no longer available/);
  global.fetch = async () => Response.json({ error: "Call read failed" }, { status: 500 });
  await assert.rejects(loadMCPCall("607"), /Call read failed/);
});

test("unavailable MCP detail numbers remain unknown and recorded zeroes stay visible", () => {
  for (const value of [undefined, null, NaN, Infinity, "", {}, false]) assert.equal(mcpDetailNumber(value), "—");
  assert.equal(mcpDetailNumber(0), "0");
  assert.equal(mcpDetailNumber(7109), (7109).toLocaleString());
});
