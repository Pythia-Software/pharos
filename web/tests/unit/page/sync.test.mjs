// sync.js: when Home refreshes after a run, when Index changes is busy, and
// how the Sync page's run history is formatted (window.pharosSync).
import { sourceNames, syncTables, runTables } from "../../../src/sync-table-model";
import { applyQuery, EMPTY_QUERY } from "@pythia-software/query-table-core";
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
  assert.deepEqual(plain(sourceNames({ sources: ["codex", "claude"], phases: phases(["gemini"]) })), ["codex", "claude"]);
  assert.deepEqual(plain(sourceNames({ phases: phases(["codex", "projections", "audit"]) })), ["codex"]);
  assert.deepEqual(plain(sourceNames({ sources: [], source_results: [{ source: "codex" }, { source: "claude" }], phases: phases(["claude", "git", "findings"]) })), ["codex", "claude"]);
  assert.deepEqual(plain(sourceNames({})), []);
});

test('run history queries use measured values across the entire retained history', () => {
  const records = Array.from({ length: 7 }, (_, index) => ({ run_id: `run-${index}`, state: index === 2 ? 'failed' : 'complete',
    finished_at: new Date(reference - index * 60000).toISOString(), seconds: [2, 100, 12, 1, 6, 60, 9][index], cpu_seconds: index,
    conversations: index, messages_in_changed_groups: index * 10, rss_observed_peak_bytes: 1048576 * (index + 1), sources: ['codex'] }));
  const { history } = syncTables({ history: records });
  assert.equal(history.rows.length, records.length);
  const query = { ...EMPTY_QUERY, where: [{ field: 'seconds', op: '>=', value: 9 }], orderBy: [{ field: 'seconds', dir: 'desc' }], limit: 2 };
  const result = applyQuery(history.rows, query, history.schema);
  assert.equal(result.total, 4);
  assert.deepEqual(result.rows.map(row => row.id), ['run-1', 'run-5']);
  assert.equal(history.rows[2].result, 'Failed');
  assert.equal(history.rows[5].cpu_seconds, 5);
  const recent = applyQuery(history.rows, { ...EMPTY_QUERY, where: [{ field: 'finished_at', op: '>=', value: new Date(reference - 60000).toISOString() }] }, history.schema);
  assert.deepEqual(recent.rows.map(row => row.id).sort(), ['run-0', 'run-1']);
});

test('source status keeps error and preservation state while missing dates stay null', () => {
  const { sources, history } = syncTables({ source_states: [
    { source_name: 'codex', pending_preservation: true, last_capture_at: '', succeeded_at: 'invalid' },
    { source_name: 'claude', pending_preservation: true, error: 'Permission denied', attempted_at: new Date(reference).toISOString() },
    { source_name: 'gemini' },
  ], history: [{ finished_at: new Date(reference).toISOString(), state: 'complete', audit: { outcome: 'mismatch' } }] });
  assert.deepEqual(sources.rows.map(row => row.status), ['Needs capture', 'Error', 'Up to date']);
  assert.equal(sources.rows[0].last_capture_at, null);
  assert.equal(sources.rows[0].succeeded_at, null);
  assert.equal(sources.rows[1].error, 'Permission denied');
  assert.equal(history.rows[0].id, new Date(reference).toISOString());
  assert.equal(history.rows[0].result, 'Needs attention');
  assert.equal(history.rows[0].seconds, null);
  const errors = applyQuery(sources.rows, { ...EMPTY_QUERY, where: [{ field: 'status', op: '=', value: 'Error' }] }, sources.schema);
  assert.deepEqual(errors.rows.map(row => row.id), ['claude']);
});

test('diagnostic queries preserve separate numeric distributions and incomplete forecast coverage', () => {
  const tables = syncTables({ statistics: { no_changes: { samples: 2, elapsed_seconds: { median: 1.5, p95: 100 }, cpu_seconds: { median: 0.5, p95: 10 }, rss_observed_peak_bytes: { last: 1e9 }, estimated_cpu_duty_percent_one_core: 0.1 } },
    forecast_accuracy: { 'capture-index': { launch: { attempts: 3, predicted: 1, evaluated: 0, covered: 0, median_absolute_error_seconds: 0 },
      discovered: { attempts: 2, predicted: 2, evaluated: 2, covered: 1, median_absolute_error_seconds: 10, median_relative_error: 0.25, median_interval_width_seconds: 30 } } },
    coverage: [{ source_name: 'codex', outcome: 'passed', units: 2 }, { source_name: 'codex', outcome: 'deferred', units: 1 }], deferred: [{ name: 'tools', changed_at: new Date(reference).toISOString() }] });
  assert.equal(tables.costs.rows[0].elapsed_median, 1.5);
  assert.equal(tables.costs.rows[0].elapsed_p95, 100);
  assert.equal(tables.accuracy.rows[0].error_seconds, null);
  assert.equal(tables.accuracy.rows[0].covered, null);
  assert.equal(tables.accuracy.rows[1].error_percent, 25);
  assert.equal(new Set(tables.coverage.rows.map(row => row.id)).size, 2);
  assert.equal(tables.deferred.rows[0].completed_at, null);
  for (const table of Object.values(syncTables({}))) assert.deepEqual(table.rows, []);
});

test('run detail queries distinguish unknown work totals and forecasts from measured zeroes', () => {
  const tables = runTables({ source_results: [{ source: 'codex', yielded: true }, { source: 'claude', error: 'Unavailable' }],
    phases: [{ name: 'audit', seconds: 3, work: [
      { phase: 'discover', unit: 'group', done: 0, total: 0, total_known: true, seconds: 0, features: { load_one_minute: 0 } },
      { phase: 'index', unit: 'byte', done: 1024, total: 0, features: { bytes: 1024, bytes_basis: 'message_text' } },
    ] }], forecasts: [{ reason: 'launch', estimate: { predicted_steps: 0, remaining_steps: 3 } },
      { reason: 'discovered', estimate: { point_seconds: 10, low_seconds: 0, high_seconds: 15 }, actual_remaining_seconds: 0 },
      { reason: 'checkpoint', estimate: { point_seconds: 10 }, actual_remaining_seconds: 12 }] });
  assert.deepEqual(tables.sourceResults.rows.map(row => row.status), ['Partial', 'Failed']);
  assert.equal(tables.phases.rows[0].name, 'Verification');
  assert.equal(tables.stages.rows[0].total, 0);
  assert.equal(tables.stages.rows[0].load_one_minute, 0);
  assert.equal(tables.stages.rows[1].total, null);
  assert.equal(tables.stages.rows[1].bytes, 1024);
  assert.equal(tables.stages.rows[1].bytes_basis, 'message_text');
  assert.equal(tables.forecasts.rows[0].point_seconds, null);
  assert.equal(tables.forecasts.rows[0].error_seconds, null);
  assert.equal(tables.forecasts.rows[1].actual_remaining_seconds, 0);
  assert.equal(tables.forecasts.rows[1].low_seconds, 0);
  assert.equal(tables.forecasts.rows[1].error_seconds, 10);
  assert.equal(tables.forecasts.rows[2].error_seconds, -2);
});
