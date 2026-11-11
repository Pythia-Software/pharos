import assert from "node:assert/strict";
import test from "node:test";
import { combineServed, followStale, liveScope, loadFollowing, resultPolicyHeaders, stillStale, resultScope, resultServed, scopedRequest, staleRetryDelays } from "../../../src/result-cache";

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

test("a view loads under its policy, shows a stale answer at once and replaces it in place until it is fresh", async () => {
  const header = signal => new Headers(scopedRequest("/api/query/usage/query-v3", { method: "POST", signal }).init.headers).get("X-Pharos-Cache");
  const built = Date.parse("2026-11-10T10:00:00Z");
  const serve = (signal, state) => scopedRequest("/api/query/usage/query-v3", { method: "POST", signal }).note({ get: name => ({ "X-Pharos-Cache": state, "X-Pharos-Cache-Built": "2026-11-10T10:00:00Z" })[name] ?? null });
  const run = async (states, policy, { failAt = [] } = {}) => {
    const asked = [], shown = [], controller = new AbortController();
    let load = 0;
    await loadFollowing(async signal => { const at = load++; asked.push(header(signal)); if (failAt.includes(at)) throw new Error("offline"); serve(signal, states[at]); return at; },
      (value, readiness) => shown.push([value, readiness.refreshing ? "refreshing" : readiness.refreshError ?? "current", readiness.updatedAt === built]), controller.signal, policy,
      async ask => { for (let attempt = 0; attempt < 2; attempt++) if (!await ask(attempt)) return; });
    return { asked, shown };
  };
  assert.deepEqual(await run(["fresh"], "cached"), { asked: ["cached"], shown: [[0, "current", false]] }, "a current answer was checked now, and is not asked for again");
  assert.deepEqual(await run(["stale", "stale", "fresh"], "fresh"), { asked: ["fresh", "cached", "cached"], shown: [[0, "refreshing", true], [1, "refreshing", true], [2, "current", false]] }, "follow-ups take what is stored");
  assert.deepEqual((await run(["stale", "stale", "stale"], "cached")).shown.at(-1), [2, stillStale, true], "out of tries, the answer stops saying it is being updated");
  assert.deepEqual((await run(["stale", "", ""], "cached", { failAt: [1, 2] })).shown, [[0, "refreshing", true], [0, "offline", true]], "failed follow-ups leave the stale answer showing, and say why nothing replaced it");
  await assert.rejects(run([], "cached", { failAt: [0] }), /offline/, "the first load's failure is the view's");
});

test("a live scope computes its requests now and reports them to its parent's scope", () => {
  const parent = new AbortController(), scope = resultScope(parent.signal, "cached"), live = liveScope(scope.signal);
  const query = scopedRequest("/api/query/model_requests/query-v3", { method: "POST", signal: live });
  assert.equal(new Headers(query.init.headers).get("X-Pharos-Cache"), "live");
  query.note({ get: name => ({ "X-Pharos-Cache": "live", "X-Pharos-Cache-Built": "2026-11-10T10:00:00Z" })[name] ?? null });
  assert.deepEqual(scope.served(), { state: "live", built: Date.parse("2026-11-10T10:00:00Z") });
  parent.abort();
  assert.ok(live.aborted, "aborting the page aborts the live read");
  const plain = new AbortController().signal;
  assert.equal(liveScope(plain), plain, "outside a scope requests are made as before");
});
