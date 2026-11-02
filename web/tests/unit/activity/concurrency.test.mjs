import assert from "node:assert/strict";
import test from "node:test";
import { mergeIntervals, workspaceSpans, concurrencyAverage, concurrencySpans, concurrencyUnit, conversationTimings, formatConcurrencyDuration, clipIntervals, workSessions, concurrencyMetrics, stackSegments, zonedDayBounds } from "../../../src/concurrency-data";
import { loadConcurrencyData, threadedSummaryWindow, quickStatsRuntimeWindow, openConcurrencyModal, closeConcurrencyModal } from "../../../src/concurrency-store";
import { normalizeConcurrencySettings, resolveConcurrencyRange } from "../../../src/concurrency-settings";
import { describeDataSource } from "../../../src/data-taxonomy-model";

const minute = 60_000;
const thread = (id, workspace_id, turns, model_spans = turns, agent_depth = 0) => ({ id, workspace_id, turns, model_spans, agent_depth });
const repoThread = (workspace, repository, spans) => ({ ...thread(workspace, workspace, spans), repository });
const rowsAt = (segments, time) => Object.fromEntries(segments.filter(s => s.start <= time && time < s.end).map(s => [s.workspace, s.bottom]));

test("timing data loads from the library, retries failures, and preserves explicit prototype previews", async t => {
  const captured = { timezone: "America/Denver", threads: [thread("captured", "workspace", [[0, minute]])] };
  const requests = [];
  t.mock.method(globalThis, "fetch", async url => {
    requests.push(url);
    return requests.length === 1 ? new Response(null, { status: 503 }) : Response.json(captured);
  });
  await assert.rejects(loadConcurrencyData(), /503/);
  const first = loadConcurrencyData(), second = loadConcurrencyData();
  assert.equal(first, second);
  assert.deepEqual(await first, captured);
  assert.deepEqual(await loadConcurrencyData(), captured);
  assert.deepEqual(requests, ["/api/usage/parallelism", "/api/usage/parallelism"]);
  await loadConcurrencyData(true);
  assert.equal(requests.length, 3);
  captured.timezone = "Local";
  const local = await loadConcurrencyData(true);
  assert.equal(local.timezone, Intl.DateTimeFormat().resolvedOptions().timeZone);
  assert.ok(Number.isFinite(zonedDayBounds("2026-11-01", local.timezone)[0]));
  captured.timezone = "UTC";
  const previousDocument = Object.getOwnPropertyDescriptor(globalThis, "document");
  t.after(() => { if (previousDocument) Object.defineProperty(globalThis, "document", previousDocument); else delete globalThis.document; });
  globalThis.document = { documentElement: { hasAttribute: name => name === "data-concurrency-prototype" } };
  assert.equal((await loadConcurrencyData(true)).timezone, "UTC");
  assert.equal(requests.at(-1), "/api/usage/concurrency-prototype");
});

test("duration rounding carries minutes into the next hour", () => {
  for (const [seconds, expected] of [[0, "0s"], [12.4, "12s"], [60, "1m"], [3590, "1h 0m"], [3580, "1h 0m"], [3540, "59m"], [7180, "2h 0m"], [7140, "1h 59m"], [7200, "2h 0m"]]) {
    assert.equal(formatConcurrencyDuration(seconds * 1000), expected);
  }
});

test("conversation table timings separate roots sharing a workspace and include only linked compute", () => {
  const first = thread("first", "same", [[0, minute], [3 * minute, 4 * minute]], [[0, minute]]);
  const second = thread("second", "same", [[minute, 3 * minute]], [[minute, 3 * minute]]);
  const child = { ...thread("child", "same", [[0, 4 * minute]], [[30_000, 90_000]], 1), root_conversation_id: "first" };
  const timings = conversationTimings([first, second, child], 15);
  assert.deepEqual([...timings], [["first", { model: 90_000, threaded: 4 * minute }], ["second", { model: 2 * minute, threaded: 2 * minute }]]);
  assert.equal(conversationTimings([first, second, child], 0).get("first").threaded, 2 * minute);
});

test("conversation table timings respect the selected range and clipped gap bridges", () => {
  const first = thread("first", "same", [[0, minute], [3 * minute, 4 * minute]], [[0, minute]]);
  const second = thread("second", "same", [[minute, 3 * minute]]);
  const bounds = [minute, 3 * minute];
  const threads = [first, second].map(t => ({ ...t, turns: clipIntervals(t.turns, bounds), model_spans: clipIntervals(t.model_spans, bounds), continuous_spans: clipIntervals(mergeIntervals(t.turns, 15 * minute), bounds) }));
  assert.deepEqual([...conversationTimings(threads, 15)], [["first", { model: 0, threaded: 2 * minute }], ["second", { model: 2 * minute, threaded: 2 * minute }]]);
});

test("simultaneous workspaces start grouped by repository", () => {
  const threads = [repoThread("a", "alpha", [[0, 20]]), repoThread("b", "beta", [[0, 20]]), repoThread("c", "alpha", [[0, 20]])];
  assert.deepEqual(rowsAt(stackSegments(threads, "model", 0, ["a", "b", "c"], [0, 20]), 5), { a: 0, c: 1, b: 2 });
});

test("arrivals preserve continuing rows and same-repo handoffs reuse a row", () => {
  const threads = [repoThread("old", "alpha", [[0, 10]]), repoThread("other", "beta", [[0, 30]]), repoThread("new", "alpha", [[10, 30]]), repoThread("arrival", "gamma", [[20, 30]])];
  const segments = stackSegments(threads, "model", 0, ["old", "other", "new", "arrival"], [0, 30]);
  assert.deepEqual(rowsAt(segments, 5), { old: 0, other: 1 });
  assert.deepEqual(rowsAt(segments, 15), { new: 0, other: 1 });
  assert.deepEqual(rowsAt(segments, 25), { new: 0, other: 1, arrival: 2 });
});

test("shrinking stacks keep feasible rows and compact into a vacated same-repo row", () => {
  const threads = [repoThread("a", "alpha", [[0, 10], [20, 30]]), repoThread("b", "beta", [[5, 30]]), repoThread("c", "alpha", [[8, 30]])];
  const segments = stackSegments(threads, "model", 0, ["a", "b", "c"], [0, 30]);
  assert.deepEqual(rowsAt(segments, 9), { a: 0, c: 2, b: 1 });
  assert.deepEqual(rowsAt(segments, 15), { c: 0, b: 1 });
  assert.deepEqual(rowsAt(segments, 25), { a: 2, c: 0, b: 1 });
});

test("returning repos reuse rows across idle gaps", () => {
  const threads = [repoThread("a", "alpha", [[0, 10], [30, 50]]), repoThread("b", "beta", [[0, 10], [30, 50]]), repoThread("c", "alpha", [[10, 20]])];
  const full = stackSegments(threads, "model", 0, ["a", "b", "c"], [0, 50]);
  assert.deepEqual(rowsAt(full, 35), { a: 0, b: 1 });
});

test("zoom crops the full layout without reordering earlier arrivals", () => {
  const threads = [repoThread("a", "alpha", [[5, 50]]), repoThread("b", "beta", [[0, 50]])];
  const full = stackSegments(threads, "model", 0, ["a", "b"], [0, 50]);
  assert.deepEqual(rowsAt(full, 15), { a: 1, b: 0 });
  const zoomed = stackSegments(threads, "model", 0, ["a", "b"], [12, 38]);
  assert.deepEqual(zoomed, full.filter(s => s.start < 38 && s.end > 12).map(s => ({ ...s, start: Math.max(12, s.start), end: Math.min(38, s.end) })));
});

test("compact ribbons preserve exact hit tests, conversation changes, running states, and zooms", () => {
  const threads = [
    { ...repoThread("first", "alpha", [[0, 4], [8, 15]]), id:"a", workspace_id:"same", model_spans:[[0,3],[6,12]] },
    { ...repoThread("second", "alpha", [[2, 9], [12, 20]]), id:"b", workspace_id:"same", model_spans:[[2,9],[12,20]] },
    { ...repoThread("third", "beta", [[3, 7], [10, 17]]), id:"c", model_spans:[[3,7],[10,17]] },
    { ...repoThread("child", "alpha", [[1, 18]]), id:"child",workspace_id:"same",agent_depth:1,root_conversation_id:"a",model_spans:[[1,5],[10,18]] },
  ];
  for (const counting of ["workspace","agent"]) for (const mode of ["user","model"]) for (const gap of [0,15]) for (const bounds of [[0,20],[4,14]]) {
    const order=[...new Set(threads.map(row=>concurrencyUnit(row,counting)))];
    const exact=stackSegments(threads,mode,gap,order,bounds,counting);
    const compact=stackSegments(threads,mode,gap,order,bounds,counting,true);
    const boundaries=[...new Set([...bounds,...exact.flatMap(s=>[s.start,s.end]),...compact.flatMap(s=>[s.start,s.end])])].sort((a,b)=>a-b);
    const hits=(segments,time)=>segments.filter(s=>s.start<=time&&time<s.end).map(({unit,bottom,conversation,running})=>({unit,bottom,conversation,running})).sort((a,b)=>a.unit.localeCompare(b.unit));
    for (let i=1;i<boundaries.length;i++) assert.deepEqual(hits(compact,(boundaries[i-1]+boundaries[i])/2),hits(exact,(boundaries[i-1]+boundaries[i])/2));
    assert.ok(compact.length<=exact.length);
  }
});

test("packing stays deterministic, gap-free, and preserves workspace activity in both modes", () => {
  let seed = 123;
  const random = () => { seed = (seed * 1664525 + 1013904223) >>> 0; return seed / 2 ** 32; };
  const threads = Array.from({ length: 60 }, (_, i) => {
    const workspace = `w${i % 13}`, start = Math.floor(random() * 80) * minute;
    const spans = [[start, start + minute], [start + 5 * minute, start + 7 * minute]];
    return { ...thread(`t${i}`, workspace, spans, spans, i % 7 === 0 ? 1 : 0), repository: `repo${i % 13 % 3}` };
  });
  const order = Array.from(new Set(threads.map(t => t.workspace_id))), bounds = [13 * minute, 87 * minute];
  for (const mode of ["model", "user"]) {
    const segments = stackSegments(threads, mode, 15, order, bounds);
    assert.deepEqual(segments, stackSegments(threads, mode, 15, order, bounds));
    const spans = workspaceSpans(threads, mode, 15);
    const boundaries = [...new Set([ ...bounds, ...segments.flatMap(s => [s.start, s.end]), ...Array.from(spans.values()).flat(2).filter(t => t > bounds[0] && t < bounds[1]) ])].sort((a, b) => a - b);
    for (let i = 1; i < boundaries.length; i++) {
      const time = (boundaries[i - 1] + boundaries[i]) / 2;
      const cells = segments.filter(s => s.start <= time && time < s.end);
      const expected = Array.from(spans).filter(([, intervals]) => intervals.some(([a, b]) => a <= time && time < b)).map(([workspace]) => workspace).sort();
      assert.deepEqual(cells.map(s => s.workspace).sort(), expected);
      assert.deepEqual(cells.map(s => s.bottom).sort((a, b) => a - b), expected.map((_, row) => row));
    }
    const expectedArea = Array.from(spans.values()).flat().reduce((area, [a, b]) => area + Math.max(0, Math.min(b, bounds[1]) - Math.max(a, bounds[0])), 0);
    assert.equal(segments.reduce((area, s) => area + s.end - s.start, 0), expectedArea);
  }
});

test("daily averages include idle hours while working-time stats exclude idle breaks", () => {
  const threads = [thread("one", "a", [[0, 10 * minute]]), thread("mirror", "a", [[5 * minute, 15 * minute]]), thread("two", "b", [[5 * minute, 10 * minute]]), thread("later", "a", [[120 * minute, 130 * minute]])];
  const metrics = concurrencyMetrics(workspaceSpans(threads, "model", 60), workSessions(threads), [0, 24 * 60 * minute]);
  assert.equal(metrics.working, 25 * minute);
  assert.equal(metrics.average, 30 / (24 * 60));
  assert.equal(metrics.parallel, .2);
  assert.equal(metrics.peak, 2);
});

test("full-window averages clip boundary activity and include completely idle windows", () => {
  const hour = 60 * minute;
  const workspaces = new Map([["a", [[-hour, 12 * hour]]], ["b", [[0, 12 * hour]]]]);
  assert.equal(concurrencyMetrics(workspaces, [[-hour, 12 * hour]], [0, 24 * hour]).average, 1);
  assert.equal(concurrencyMetrics(workspaces, [[-hour, 12 * hour]], [11 * hour, 13 * hour]).average, 1);
  assert.deepEqual(concurrencyMetrics(workspaces, [[-hour, 12 * hour]], [13 * hour, 24 * hour]), { average: 0, parallel: 0, peak: 0, working: 0 });
});

test("gap bridging belongs to a conversation, not a workspace, with no tail padding", () => {
  const threads = [thread("a", "workspace", [[0, minute]]), thread("b", "workspace", [[30 * minute, 31 * minute]])];
  assert.deepEqual(workspaceSpans(threads, "user", 60).get("workspace"), [[0, minute], [30 * minute, 31 * minute]]);
  assert.deepEqual(mergeIntervals([[0, minute], [61 * minute, 62 * minute]], 60 * minute), [[0, 62 * minute]]);
  assert.deepEqual(mergeIntervals([[0, minute], [61 * minute + 1, 62 * minute]], 60 * minute), [[0, minute], [61 * minute + 1, 62 * minute]]);
});

test("delegation contributes compute inside a workspace, never a user thread", () => {
  const threads = [thread("parent", "a", [[0, 10]], [[0, 4]]), thread("child", "a", [[2, 8]], [[2, 8]], 1)];
  assert.deepEqual(workspaceSpans(threads, "model", 0).get("a"), [[0, 8]]);
  assert.deepEqual(workspaceSpans(threads, "user", 0).get("a"), [[0, 10]]);
});

test("agent counting separates simultaneous conversations in a shared workspace", () => {
  assert.equal(describeDataSource("parallelism", "workspace").entity, "workspaces");
  assert.equal(describeDataSource("parallelism", "agent").entity, "conversations");
  const threads = [thread("one", "checkout", [[0, 10]]), thread("two", "checkout", [[0, 10]])];
  for (const mode of ["model", "user"]) {
    const workspace = concurrencySpans(threads, mode, 0, "workspace");
    const agents = concurrencySpans(threads, mode, 0, "agent");
    assert.equal(concurrencyMetrics(workspace, [[0, 10]], [0, 20]).average, .5);
    assert.equal(concurrencyMetrics(agents, [[0, 10]], [0, 20]).average, 1);
    assert.equal(concurrencyMetrics(agents, [[0, 10]], [0, 20]).peak, 2);
    const segments = stackSegments(threads, mode, 0, ["one", "two"], [0, 20], "agent");
    assert.deepEqual(segments.map(s => [s.unit, s.workspace, s.bottom]), [["one", "checkout", 0], ["two", "checkout", 1]]);
  }
});

test("agent model activity unions linked delegated compute into its root conversation", () => {
  const parent = thread("parent", "a", [[0, 10]], [[0, 4]]);
  const child = { ...thread("child", "a", [[2, 8]], [[2, 8]], 1), root_conversation_id: "parent" };
  const other = thread("other", "a", [[3, 9]], [[3, 9]]);
  const threads = [parent, child, other];
  assert.deepEqual(concurrencySpans(threads, "model", 0, "agent").get("parent"), [[0, 8]]);
  assert.deepEqual(concurrencySpans(threads, "user", 0, "agent").get("parent"), [[0, 10]]);
  const segments = stackSegments(threads, "model", 0, ["parent", "other"], [0, 10], "agent");
  assert.equal(Math.max(...segments.map(s => s.bottom + 1)), 2);
  assert.ok(!segments.some(s => s.unit === "child"));
});

test("agent stacks stay packed, preserve activity and keep their layout when zoomed", () => {
  let seed = 17;
  const threads = Array.from({ length: 30 }, (_, i) => {
    seed = (seed * 1664525 + 1013904223) >>> 0;
    const start = seed % 80;
    return { ...thread(`t${i}`, `w${i % 15 % 3}`, [[start, start + 5], [start + 9, start + 12]], undefined, i >= 15 ? 1 : 0), root_conversation_id: i >= 15 ? `t${i - 15}` : undefined, repository: `repo${i % 15 % 3}` };
  });
  for (const counting of ["workspace", "agent"]) for (const mode of ["model", "user"]) {
    const order = Array.from(new Set(threads.map(t => concurrencyUnit(t, counting))));
    const full = stackSegments(threads, mode, 0, order, [0, 100], counting);
    const zoom = stackSegments(threads, mode, 0, order, [13, 87], counting);
    assert.deepEqual(zoom, full.filter(s => s.start < 87 && s.end > 13).map(s => ({ ...s, start: Math.max(13, s.start), end: Math.min(87, s.end) })));
    const spans = concurrencySpans(threads, mode, 0, counting);
    for (let time = .5; time < 100; time++) {
      const cells = full.filter(s => s.start <= time && time < s.end);
      const expected = Array.from(spans).filter(([, intervals]) => intervals.some(([a, b]) => a <= time && time < b)).map(([unit]) => unit).sort();
      assert.deepEqual(cells.map(s => s.unit).sort(), expected);
      assert.deepEqual(cells.map(s => s.bottom).sort((a, b) => a - b), expected.map((_, row) => row));
    }
  }
});

test("half-open boundaries don't create a concurrency peak at a handoff", () => {
  const threads = [thread("a", "one", [[0, 10]]), thread("b", "two", [[10, 20]])];
  assert.equal(concurrencyMetrics(workspaceSpans(threads, "model", 0), [[0, 20]], [0, 20]).peak, 1);
  const segments = stackSegments(threads, "model", 0, ["one", "two"], [0, 20]);
  assert.deepEqual(segments.map(s => [s.workspace, s.start, s.end, s.bottom]), [["one", 0, 10, 0], ["two", 10, 20, 0]]);
});

test("daily views preserve a continuation crossing midnight", () => {
  const daily = thread("a", "workspace", [[30 * minute, 40 * minute]]);
  daily.continuous_spans = [[0, 40 * minute]];
  assert.deepEqual(workspaceSpans([daily], "user", 60).get("workspace"), [[0, 40 * minute]]);
  assert.equal(stackSegments([daily], "user", 60, ["workspace"], [0, 60 * minute])[0].running, false);
});

test("local days follow the snapshot timezone across daylight savings", () => {
  const [springStart, springEnd] = zonedDayBounds("2026-03-08", "America/Denver");
  const [fallStart, fallEnd] = zonedDayBounds("2026-11-01", "America/Denver");
  assert.equal(springEnd - springStart, 23 * 60 * minute);
  assert.equal(fallEnd - fallStart, 25 * 60 * minute);
});

test("header threaded averages use the same saved gap and repository settings", () => {
  const base = Date.parse("2026-10-06T10:00:00Z"), hour = 60 * minute;
  const a = { ...thread("a", "one", [[base, base + hour], [base + 2 * hour, base + 3 * hour]]), repository: "alpha" };
  const b = { ...thread("b", "two", [[base + hour, base + 2 * hour]]), repository: "beta" };
  const data = { period_start: "2026-10-01", period_end: "2026-10-07", timezone: "UTC", threads: [a, b] };
  const reference = Date.parse("2026-10-07T01:00:00Z");
  assert.equal(threadedSummaryWindow(data, 0, "", "24h", reference).average, 3 / 24);
  assert.equal(threadedSummaryWindow(data, 60, "", "24h", reference).average, 4 / 24);
  assert.equal(threadedSummaryWindow(data, 60, "alpha", "24h", reference).average, 3 / 24);
  assert.equal(threadedSummaryWindow(data, 0, "alpha", "24h", reference).average, 2 / 24);
  assert.equal(threadedSummaryWindow(data, 60, "", "1h", reference).average, 0);
  assert.equal(threadedSummaryWindow(data, 60, "", "30d", reference), null);
  assert.equal(threadedSummaryWindow(data, 60, "", "all", reference), null);
  assert.equal(threadedSummaryWindow(data, 60, "", "24h", Date.parse("2026-10-08T01:00:00Z")), null);
  assert.equal(threadedSummaryWindow(data, 60, "", "24h", Date.parse("2026-10-01T01:00:00Z")), null);
});

test("header parallelism uses the saved counting choice and full time window", () => {
  const base = Date.parse("2026-10-06T10:00:00Z"), hour = 60 * minute;
  const threads = [thread("one", "same", [[base, base + hour]]), thread("two", "same", [[base, base + hour]])];
  const data = { period_start: "2026-10-01", period_end: "2026-10-07", timezone: "UTC", threads };
  const reference = Date.parse("2026-10-07T01:00:00Z");
  assert.equal(threadedSummaryWindow(data, 0, "", "24h", reference, "workspace").average, 1 / 24);
  assert.equal(threadedSummaryWindow(data, 0, "", "24h", reference, "agent").average, 2 / 24);
});

test("opening and closing parallelism preserve the underlying page and query", () => {
  const previous = { window: globalThis.window, location: globalThis.location, history: globalThis.history };
  const events = [], states = [];
  try {
    globalThis.location = new URL("http://localhost/library?search=agents&view=conversation#results");
    globalThis.window = { dispatchEvent: event => events.push(event.type), routeFromLocation: () => assert.fail("opening a modal must not reroute the page") };
    globalThis.history = {
      pushState: (_, __, url) => { states.push("push");globalThis.location = new URL(url); },
      replaceState: (_, __, url) => { states.push("replace");globalThis.location = new URL(url); },
    };
    openConcurrencyModal();
    assert.equal(location.href, "http://localhost/library?search=agents&view=conversation&parallelism=1#results");
    openConcurrencyModal();
    assert.deepEqual(states, ["push"]);
    closeConcurrencyModal();
    assert.equal(location.href, "http://localhost/library?search=agents&view=conversation#results");
    assert.deepEqual(states, ["push", "replace"]);
    assert.ok(!events.includes("pharos:route"));
  } finally {
    for (const [key, value] of Object.entries(previous)) {
      if (value === undefined) delete globalThis[key]; else globalThis[key] = value;
    }
  }
});

test("persistent settings reject invalid values and retain valid graph and zoom choices", () => {
  const saved = normalizeConcurrencySettings({ mode: "model", gap: 15, day: "2026-10-07", repository: "pharos", workspace: "worktree", zoom: [100, 200], showConversations: true });
  assert.deepEqual(saved, { mode: "model", counting: "workspace", agent: "", gap: 15, day: "2026-10-07", range: "day", startDay: "", endDay: "", repository: "pharos", workspace: "worktree", zoom: [100, 200], showConversations: true, smoothingMinutes: 0 });
  const invalid = normalizeConcurrencySettings({ mode: "unknown", gap: 90, day: 4, repository: [], workspace: 123, zoom: [200, 100] });
  assert.deepEqual(invalid, { mode: "user", counting: "workspace", agent: "", gap: 60, day: "", range: "day", startDay: "", endDay: "", repository: "", workspace: "", zoom: null, showConversations: false, smoothingMinutes: 0 });
  const agents = normalizeConcurrencySettings({ counting: "agent", agent: "conversation", workspace: "checkout" });
  assert.equal(agents.counting, "agent");
  assert.equal(agents.agent, "conversation");
  assert.equal(normalizeConcurrencySettings({ counting: "invalid", agent: 123 }).counting, "workspace");
  assert.equal(normalizeConcurrencySettings({ smoothingMinutes: 1440 }).smoothingMinutes, 1440);
  assert.equal(normalizeConcurrencySettings({ smoothingMinutes: -2 }).smoothingMinutes, 0);
});

test("parallelism smoothing weights elapsed occupancy and idle time without changing ribbons", () => {
  const spans = new Map([["a", [[0, 10 * minute]]], ["b", [[0, 5 * minute]]]]);
  const before = structuredClone(spans);
  const points = concurrencyAverage(spans, [0, 20 * minute], 10 * minute, 4);
  assert.deepEqual(points.map(point => point.value), [2, 2, 1.5, 0.5, 0]);
  assert.deepEqual(concurrencyAverage(spans, [0, 20 * minute], 100000 * minute, 4).map(point => point.value), [2, 2, 1.5, 1, 0.75]);
  assert.deepEqual(concurrencyAverage(spans, [0, 20 * minute], 0), []);
  assert.deepEqual(spans, before);
  const history = new Map([["earlier", [[-10 * minute, 0]]]]);
  assert.deepEqual(concurrencyAverage(history, [0,20 * minute],10 * minute,4,-20 * minute).map(point=>point.value),[1,0.5,0,0,0], 'the first visible point incorporates activity before the range');
  assert.deepEqual(concurrencyAverage(history, [0,20 * minute],10 * minute,4,-5 * minute).map(point=>point.value),[1,0.5,0,0,0], 'only available captured history contributes at the capture boundary');
});

test("date presets use inclusive calendar days ending at the latest captured day", () => {
  const data = { period_start: "2026-10-02", period_end: "2026-10-08", timezone: "America/Denver" };
  for (const [key, startDay, days] of [["day", "2026-10-06", 1], ["3d", "2026-10-06", 3], ["7d", "2026-10-02", 7], ["all", "2026-10-02", 7]]) {
    const range = resolveConcurrencyRange(data, normalizeConcurrencySettings({ range: key, day: "2026-10-06" }));
    assert.equal(range.startDay, startDay);
    assert.equal(range.endDay, key === "day" ? "2026-10-06" : "2026-10-08");
    assert.equal(range.bounds[1] - range.bounds[0], days * 24 * 60 * minute);
  }
  const longHistory = { period_start: "2025-01-01", period_end: "2026-10-08", timezone: "UTC" };
  for (const days of [30, 90, 180, 365]) {
    const range = resolveConcurrencyRange(longHistory, normalizeConcurrencySettings({ range: `${days}d` }));
    assert.equal(range.endDay, longHistory.period_end);
    assert.equal(range.bounds[1] - range.bounds[0], days * 86400000);
  }
});

test("custom ranges persist, include the final day, and constrain dates to timing coverage", () => {
  const data = { period_start: "2026-10-02", period_end: "2026-10-08", timezone: "UTC" };
  const settings = normalizeConcurrencySettings({ range: "custom", startDay: "2026-10-03", endDay: "2026-10-05" });
  assert.deepEqual(resolveConcurrencyRange(data, settings), { startDay: "2026-10-03", endDay: "2026-10-05", bounds: [Date.parse("2026-10-03T00:00:00Z"), Date.parse("2026-10-06T00:00:00Z")] });
  const clipped = resolveConcurrencyRange(data, normalizeConcurrencySettings({ range: "custom", startDay: "2026-09-01", endDay: "2026-10-20" }));
  assert.equal(clipped.startDay, data.period_start);
  assert.equal(clipped.endDay, data.period_end);
  const reversed = resolveConcurrencyRange(data, normalizeConcurrencySettings({ range: "custom", startDay: "2026-10-05", endDay: "2026-10-03" }));
  assert.equal(reversed.startDay, "2026-10-03");
  assert.equal(reversed.endDay, "2026-10-05");
  const invalid = normalizeConcurrencySettings({ range: "bogus", startDay: "2026-02-30", endDay: "not a date" });
  assert.equal(invalid.range, "day");
  assert.equal(invalid.startDay, "");
  assert.equal(invalid.endDay, "");
});

test("multi-day windows follow daylight savings and retain idle days in the average", () => {
  const data = { period_start: "2026-03-06", period_end: "2026-03-08", timezone: "America/Denver" };
  const range = resolveConcurrencyRange(data, normalizeConcurrencySettings({ range: "3d" }));
  assert.equal(range.bounds[1] - range.bounds[0], 71 * 60 * minute);
  const spans = new Map([["a", [[range.bounds[0], range.bounds[0] + 2 * 60 * minute]]]]);
  assert.equal(concurrencyMetrics(spans, [], range.bounds).average, 2 / 71);
});


test("Today parallelism includes elapsed idle time without substituting the last captured date", () => {
  const now = new Date(2026, 10, 1, 9), midnight = new Date(now); midnight.setHours(0, 0, 0, 0);
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  const day = new Intl.DateTimeFormat("en-CA").format(now);
  const data = { timezone: zone, period_start: day, period_end: day, threads: [repoThread("w", "repo", [[+midnight, +midnight + 3600000]])] };
  const settings = normalizeConcurrencySettings({ range: "today" });
  assert.equal(settings.range, "today");
  assert.deepEqual(resolveConcurrencyRange(data, settings, now).bounds, [+midnight, +now]);
  const result = threadedSummaryWindow(data, 0, "", "today", +now, "workspace", midnight.toISOString());
  assert.equal(result.average, 3600000/(+now-+midnight));
  assert.equal(threadedSummaryWindow({ ...data, threads: [] }, 0, "", "today", +now).average, 0);
  const old = { ...data, period_start: "2026-01-01", period_end: "2026-01-02" };
  assert.deepEqual(resolveConcurrencyRange(old, settings, now).bounds, [+midnight, +now]);
  assert.equal(threadedSummaryWindow(old, 0, "", "today", +now), null);
  assert.equal(threadedSummaryWindow(data, 0, "", "today", +midnight), null);
});

test('linked parallelism restoration is read-only and deliberate changes save only chosen defaults',async t=>{
 const {readConcurrencySettings,saveConcurrencyChanges,concurrencySettingsKey}=await import('../../../src/concurrency-settings');
 const originals=new Map(['location','window'].map(key=>[key,Object.getOwnPropertyDescriptor(globalThis,key)]));
 t.after(()=>{for(const[key,descriptor]of originals){if(descriptor)Object.defineProperty(globalThis,key,descriptor);else delete globalThis[key]}});
 let stored={gap:60,counting:'workspace',repository:'saved',range:'7d'};const writes=[];
 globalThis.window={pharosPrefs:{get:()=>stored,set:(key,value)=>{assert.equal(key,concurrencySettingsKey);stored=value;writes.push(value)}},dispatchEvent(){}};
 globalThis.location=new URL('https://pharos.test/analyze?'+new URLSearchParams({analyze_parallelism:JSON.stringify({gap:0,counting:'agent',repository:'linked',range:'30d'})}));
 assert.equal(readConcurrencySettings().gap,0);assert.equal(writes.length,0);
 saveConcurrencyChanges({gap:15});
 assert.equal(stored.gap,15);assert.equal(stored.counting,'workspace');assert.equal(stored.repository,'saved');assert.equal(stored.range,'7d');
 globalThis.location=new URL('https://pharos.test/analyze?analyze_topic=parallelism');
 assert.equal(readConcurrencySettings().gap,15);
});

test("Quick Stats runtimes count parallel sessions separately and union wall time", () => {
  const base = Date.parse("2026-10-06T10:00:00Z"), hour = 60 * minute;
  const roots = [thread("one", "same-checkout", [[base, base + hour]]), thread("two", "same-checkout", [[base, base + hour]])];
  const child = {...thread("child", "same-checkout", [[base, base + hour]], undefined, 1), root_conversation_id: "one"};
  const data = {period_start:"2026-10-01",period_end:"2026-10-07",timezone:"UTC",threads:[...roots,child]};
  assert.deepEqual(quickStatsRuntimeWindow(data, 0, "1h", base + hour), {agent:2*hour,active:hour});
  assert.deepEqual(quickStatsRuntimeWindow(data, 0, "15m", base + hour), {agent:30*minute,active:15*minute});
  assert.deepEqual(quickStatsRuntimeWindow(data, 0, "5m", base + hour), {agent:10*minute,active:5*minute});
  assert.deepEqual(quickStatsRuntimeWindow(data, 0, "yesterday", base + hour, new Date(base + 30*minute).toISOString()), {agent:hour,active:30*minute});
  assert.deepEqual(quickStatsRuntimeWindow({...data,threads:[]}, 0, "1h", base + hour), {agent:0,active:0});
  assert.equal(quickStatsRuntimeWindow(data, 0, "all", base + hour), null);
  assert.equal(quickStatsRuntimeWindow(data, 0, "month", base + hour, "2026-09-30T23:00:00Z"), null);
  assert.equal(quickStatsRuntimeWindow(data, 0, "today", NaN), null);
  const gaps = {...data,threads:[thread("gaps", "same", [[base,base+minute],[base+10*minute,base+11*minute]])]};
  assert.equal(quickStatsRuntimeWindow(gaps, 0, "1h", base + hour).agent, 2*minute);
  assert.equal(quickStatsRuntimeWindow(gaps, 15, "1h", base + hour).agent, 11*minute);
});
