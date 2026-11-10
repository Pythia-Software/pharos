import assert from "node:assert/strict";
import test from "node:test";
import { combineServed, followStale, resultPolicyHeaders, resultScope, resultServed, scopedRequest, staleRetryDelays } from "../../../src/result-cache";

test("a response says what the result cache served and when it was computed", () => {
  const headers = values => ({ get: name => values[name] ?? null });
  assert.deepEqual(resultServed(headers({ "X-Pharos-Cache": "stale", "X-Pharos-Cache-Built": "2026-11-10T10:00:00Z" })), { state: "stale", built: Date.parse("2026-11-10T10:00:00Z") });
  assert.deepEqual(resultServed(headers({ "X-Pharos-Cache": "live" }), 5), { state: "live", built: 5 });
  assert.equal(resultServed(headers({})), undefined, "an endpoint outside the cache says nothing");
  assert.deepEqual(resultPolicyHeaders("cached"), { "X-Pharos-Cache": "cached" });
  assert.deepEqual(resultPolicyHeaders(), {});
});

test("a view drawn from several answers is as stale and as old as its worst", () => {
  assert.deepEqual(combineServed({ state: "fresh", built: 9 }, undefined, { state: "stale", built: 4 }), { state: "stale", built: 4 });
  assert.deepEqual(combineServed({ state: "live", built: 9 }, { state: "fresh", built: 7 }), { state: "fresh", built: 7 });
  assert.deepEqual(combineServed({ state: "live", built: 9 }), { state: "live", built: 9 });
  assert.equal(combineServed(undefined), undefined);
});

test("stale answers are asked for again until fresh, shown, or out of tries", async () => {
  const run = async (answers, { hidden = [], abortAt } = {}) => {
    const waits = [], asked = [], controller = new AbortController();
    await followStale(async attempt => { asked.push(attempt); return answers[attempt]; }, controller.signal, staleRetryDelays,
      async ms => { waits.push(ms); if (waits.length === abortAt) controller.abort(); }, () => hidden.shift() ?? false);
    return { waits, asked };
  };
  assert.deepEqual(await run([true, false, true]), { waits: staleRetryDelays.slice(0, 2), asked: [0, 1] }, "stops once nothing is stale");
  assert.deepEqual((await run(staleRetryDelays.map(() => true))).asked, staleRetryDelays.map((_, i) => i), "gives up after the last delay");
  assert.deepEqual(await run([false], { hidden: [true, true] }), { waits: [staleRetryDelays[0], 1000, 1000], asked: [0] }, "a hidden page waits to be shown");
  assert.deepEqual((await run([true, true], { abortAt: 1 })).asked, [], "an abandoned page asks nothing");
});

test("a scope carries its policy to query requests made with its signal and collects what they were served", () => {
  const parent = new AbortController(), scope = resultScope(parent.signal, "cached");
  const post = { method: "POST", headers: { "Content-Type": "application/json" }, signal: scope.signal };
  const query = scopedRequest("/api/query/usage/query-v3?carbon_settings=x", post);
  assert.equal(new Headers(query.init.headers).get("X-Pharos-Cache"), "cached");
  assert.equal(new Headers(query.init.headers).get("Content-Type"), "application/json");
  query.note({ get: name => ({ "X-Pharos-Cache": "stale", "X-Pharos-Cache-Built": "2026-11-10T10:00:00Z" })[name] ?? null });
  query.note({ get: () => null });
  assert.deepEqual(scope.served(), { state: "stale", built: Date.parse("2026-11-10T10:00:00Z") });
  for (const [url, init] of [["/api/query/usage/capabilities", { signal: scope.signal }], ["/api/thinking", post], ["/api/query/usage/query-v3", { ...post, signal: parent.signal }], ["/api/query/usage/query-v3", { method: "POST" }]])
    assert.equal(scopedRequest(url, init).init, init, `${url} is outside the scope`);
  parent.abort(new Error("left the page"));
  assert.equal(scope.signal.aborted, true, "a scope ends with the load it belongs to");
});
