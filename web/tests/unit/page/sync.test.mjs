// sync.js: when Home refreshes after a run, when Index changes is busy, and
// how the Sync page's run history is formatted (window.pharosSync).
import { test } from "node:test";
import assert from "node:assert/strict";
import { loadClassicScript, plain } from "../helpers/classic-script.mjs";

// Relative times are measured from a fixed clock.
const reference = Date.parse("2026-10-01T18:00:00Z");
class FixedDate extends Date { static now() { return reference; } }
const { pharosSync } = loadClassicScript("sync.js", { Date: FixedDate }).window;

const run = (id, state, conversations = 0, workspaces = conversations) => ({ id, kind: "automatic-sync", state, phase: "checking", conversations, workspaces });

// Each step: the runs /api/activity reports, and whether Home refreshes.
// sync.js follows the runs that were running at the step before.
const steps = [
  [[run("quiet", "running")], false],
  [[run("quiet", "complete")], false], // a run with no changes leaves Home alone
  [[run("busy", "running"), run("quiet", "complete")], false], // only followed runs count
  [[run("busy", "complete", 3)], true], // a run that indexed conversations
  [[run("first", "running"), run("second", "running")], false],
  [[run("first", "interrupted", 2), run("second", "running")], true], // a run finishing beside another
  [[run("first", "interrupted", 2), run("second", "complete")], false],
  [[run("restarted", "running")], false],
  [[], true], // a run that left the list may have changed data
];

test("Home refreshes only when a followed run finishes having indexed something", () => {
  let followed = new Set();
  for (const [runs, refresh] of steps) {
    assert.equal(pharosSync.finishedRunsChanged(followed, runs), refresh, `following ${[...followed]}: ${JSON.stringify(runs)}`);
    followed = new Set(runs.filter(item => item.state === "running").map(item => item.id));
  }
  // Either count is a change, from a run that completed, failed or stopped.
  assert.equal(pharosSync.finishedRunsChanged(new Set(["a"]), [run("a", "complete", 2, 0)]), true);
  assert.equal(pharosSync.finishedRunsChanged(new Set(["a"]), [run("a", "failed", 0, 1)]), true);
  assert.equal(pharosSync.finishedRunsChanged(new Set(["a"]), [run("a", "running", 5)]), false);
});

test("each poll announces a data refresh for the runs it saw finish", async () => {
  let runs = [];
  const fetch = async path => ({ ok: true, json: async () => path === "/api/activity" ? { runs } : path === "/api/sync/issues" ? { issues: [] } : {} });
  const script = loadClassicScript("sync.js", { fetch });
  // Let the poll sync.js starts when it loads finish first.
  await new Promise(resolve => setImmediate(resolve));
  const refreshes = () => script.events.filter(event => event.type === "pharos:data-refresh").length;
  let expected = 0;
  for (const [next, refresh] of steps) {
    runs = next;
    await script.window.pharosSync.refresh();
    if (refresh) expected++;
    assert.equal(refreshes(), expected, JSON.stringify(next));
  }
});

test("Index changes is busy while, and only while, a run is running", () => {
  const cases = [
    [{}, undefined],
    [{ runs: [] }, undefined],
    [{ runs: [run("done", "complete"), run("stopped", "interrupted"), run("broke", "failed")] }, undefined],
    [{ runs: [run("done", "complete"), run("external", "running")] }, "external"],
  ];
  for (const [activity, id] of cases) assert.equal(pharosSync.runningRun(activity)?.id, id, JSON.stringify(activity));
});

test("run history rounds durations and memory, and says how long ago a run finished", () => {
  const { seconds, memory, when } = pharosSync.format;
  assert.deepEqual([22.123456789, 8.7123456789, 0.23123456789, 0, 59.6, 61.8123456789, 3600, 3661.2, -3, undefined, null, "n/a"].map(seconds),
    ["22s", "9s", "<1s", "0s", "1m", "1m 2s", "1h", "1h 1m", "0s", "—", "—", "—"]);
  assert.deepEqual([129 * 1048576, 1.5 * 1073741824, undefined].map(memory), ["129 MiB", "1.5 GiB", "—"]);
  const before = milliseconds => new Date(reference - milliseconds).toISOString();
  assert.deepEqual([before(120000), before(480000), before(59 * 60000), before(2 * 3600000), before(3 * 86400000), before(-180000)].map(when),
    ["2m ago", "8m ago", "59m ago", "2h ago", "3d ago", "In 3m"]);
});

test("a run's sources come from its record, else from its results and per-source phases", () => {
  const phases = names => names.map(name => ({ name, seconds: 1 }));
  assert.deepEqual(plain(pharosSync.sourceNames({ sources: ["codex", "claude"], phases: phases(["gemini"]) })), ["codex", "claude"]);
  assert.deepEqual(plain(pharosSync.sourceNames({ phases: phases(["codex", "projections", "audit"]) })), ["codex"]);
  assert.deepEqual(plain(pharosSync.sourceNames({ sources: [], source_results: [{ source: "codex" }, { source: "claude" }], phases: phases(["claude", "git", "findings"]) })), ["codex", "claude"]);
  assert.deepEqual(plain(pharosSync.sourceNames({})), []);
});

test("stage workload distinguishes a known empty inventory from an unknown total", () => {
  assert.equal(pharosSync.stageWork({unit: "group", done: 0, total: 0, total_known: true}), "0 / 0 group");
  assert.equal(pharosSync.stageWork({unit: "group", done: 3, total: 0}), "3 group");
  assert.equal(pharosSync.stageWork({unit: "message", done: 4, total: 10}), "4 / 10 message");
});

test('forecast errors distinguish missing predictions and interrupted outcomes', () => {
  const error = pharosSync.forecastError;
  assert.equal(error({estimate: {state: 'partial'}}), 'Not available');
  assert.equal(error({estimate: {point_seconds: 60}}), 'Not evaluated');
  assert.equal(error({estimate: {point_seconds: 60}, actual_remaining_seconds: null}), 'Not evaluated');
  assert.match(error({estimate: {point_seconds: 60}, actual_remaining_seconds: 90}), /under$/);
  assert.match(error({estimate: {point_seconds: 90}, actual_remaining_seconds: 60}), /over$/);
});
