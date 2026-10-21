// library.js: which running activities form the library update, what each
// activity's progress bar and timing show, and when Index changes is busy
// (window.pharosLibrary).
import { test } from "node:test";
import assert from "node:assert/strict";
import { loadClassicScript } from "../helpers/classic-script.mjs";

const script = loadClassicScript("library.js");
const library = script.window.pharosLibrary;

const started = Date.parse("2026-10-01T16:00:00Z");
const labels = update => update && [...update.updating].map(activity => activity.label);

test("progress follows input size, then the unit count, then the activity's own fraction", () => {
  const work = (done, total, size) => ({ phase: "indexing", unit: "conversation group", done, total, ...(size ? { size: { unit: "byte", ...size } } : {}) });
  const cases = [
    [{ progress: 0.9, work: work(120, 600, { done: 150e6, total: 300e6 }) }, 0.5],
    [{ work: work(120, 600, { done: 0, total: 0 }) }, 0.2],
    [{ work: work(1, 100) }, 0.01],
    [{ work: work(0, 100) }, 0],
    // Work without a total has no known progress, whatever the activity says.
    [{ progress: 0.7, work: { phase: "counting", unit: "conversation" } }, null],
    [{ kind: "index", progress: 0.5 }, 0.5],
    [{ kind: "maintenance" }, undefined],
  ];
  for (const [activity, progress] of cases) assert.equal(library.activityProgress(activity), progress, JSON.stringify(activity));
});

test("the timing column shows elapsed time and either a range left or the estimate's state", () => {
  const now = started + 120_000;
  const timing = (estimate, extra = {}, unavailable = false) => library.activityTiming({ started_at: new Date(started).toISOString(), estimate, ...extra }, { now, unavailable });
  const estimating = (low_seconds, high_seconds) => ({ state: "estimating", low_seconds, high_seconds, basis: "Recent pace" });

  const first = timing({ ...estimating(180, 300), unit: "byte", units_per_second: 1e6 });
  assert.deepEqual([first.elapsed, first.state, first.range, first.basis], [120, "estimating", "3–5 min", "Recent pace"]);
  const ranges = [[30, 50, "30–50 sec"], [300, 300, "5 min"], [3, 4, "5 sec"], [45, 120, "45 sec–2 min"], [5400, 9000, "1.5–2.5 hr"]];
  for (const [low, high, range] of ranges) assert.equal(timing(estimating(low, high)).range, range, `${low}–${high} s`);

  // Without a usable range, the estimate's state is shown.
  const states = [
    [{ state: "learning", basis: "Measuring the pace of this stage" }, "learning"],
    [{ state: "stalled" }, "stalled"],
    [{ state: "finishing" }, "finishing"],
    [{ state: "stopping" }, "stopping"],
    [{ state: "unknown" }, "unknown"],
    [estimating(0, 30), "unknown"],
    [estimating(60, 30), "unknown"],
    [{ state: "overdue" }, "unknown"],
    [undefined, "unknown"],
  ];
  for (const [estimate, state] of states) {
    const shown = timing(estimate);
    assert.deepEqual([shown.state, shown.range], [state, null], JSON.stringify(estimate));
  }
  // While the status is unavailable, no estimate is trusted.
  const unavailable = timing(estimating(180, 300), {}, true);
  assert.deepEqual([unavailable.state, unavailable.range], ["unknown", null]);
  assert.notEqual(unavailable.basis, "Recent pace");

  assert.equal(timing({ state: "learning" }, { started_at: undefined }).elapsed, null);
  assert.equal(timing({ state: "learning" }, { started_at: new Date(now + 5000).toISOString() }).elapsed, 0);
  assert.equal(timing({ state: "learning" }, { started_at: new Date(now - 3_725_000).toISOString() }).elapsed, 3725);
});

test("an estimate's pace is bytes per second, or other units per minute", () => {
  assert.equal(library.estimateRate({ units_per_second: 1e6 }, "byte"), "1.0 MB/sec");
  assert.equal(library.estimateRate({ units_per_second: 2.5e9 }, "byte"), "2.5 GB/sec");
  assert.equal(library.estimateRate({ units_per_second: 1.5 }, "conversation group"), "90 conversation groups/min");
  assert.equal(library.estimateRate({ units_per_second: 0.0125 }, "workspace"), "0.8 workspaces/min");
  assert.equal(library.estimateRate({ units_per_second: 0 }, "byte"), "");
  assert.equal(library.estimateRate({ units_per_second: 5 }, undefined), "");

  assert.match(library.workCount({ unit: "conversation group", done: 120, total: 600 }), /\b120\b.*\b600 conversation groups\b/);
  assert.match(library.workCount({ unit: "byte", done: 60e6, total: 300e6 }), /\b60 MB\b.*\b300 MB\b/);
  assert.equal(library.workCount({ phase: "counting" }), "");
});

test("captures, indexes, upgrades and an index's follow-up form the library update; other work stays separate", () => {
  const now = started + 600_000;
  const index = { kind: "index", label: "Indexing captures", work: { phase: "indexing", unit: "conversation group", done: 20, total: 100 } };
  const upgrading = { kind: "maintenance", label: "Upgrading the library", work: { phase: "Building tool ledger", unit: "conversation", done: 1, total: 50 } };
  const substring = { kind: "maintenance", label: "Building substring search", detail: "50 of 100 conversations", progress: 0.5 };
  const tools = { kind: "maintenance", label: "Building the Tools ledger", detail: "10 of 20 conversations", progress: 0.5 };
  const libraryView = { kind: "maintenance", label: "Indexing the Library view", detail: "5 workspaces to index", progress: null };
  const firstScan = { kind: "git", label: "Looking up merges in Git", detail: "first scan of this catalog", progress: null };
  const lastIndex = { id: "recent-index", kind: "capture-index", state: "complete", started_at: new Date(now - 10_000).toISOString(), completed_at: new Date(now).toISOString() };
  const followUp = { kind: "git", label: "Looking up merges in Git", detail: "Main-branch merges of indexed work, after the last index", started_at: lastIndex.started_at, progress: null };
  const findings = { kind: "maintenance", label: "Updating findings" };
  const group = (activities, context = {}) => library.libraryUpdate(activities, { lastIndex: null, upgradeState: null, now, ...context });

  // Standalone maintenance and Git lookups are not a library update.
  assert.equal(group([substring, tools, libraryView, firstScan]), null);
  assert.equal(group([]), null);

  // Two activities of one update keep their own progress; the index leads.
  const concurrent = group([index, upgrading]);
  assert.deepEqual(labels(concurrent), ["Indexing captures", "Upgrading the library"]);
  assert.equal(concurrent.indexing, index);
  assert.equal(concurrent.capturing, undefined);
  assert.equal(concurrent.progress, 0.2);
  assert.equal(concurrent.label, "Indexing");

  // A capture leads an index, and other work stays out.
  const capture = { kind: "capture", label: "Capturing this Mac", progress: 0.4 };
  const both = group([substring, index, capture]);
  assert.deepEqual(labels(both), ["Indexing captures", "Capturing this Mac"]);
  assert.equal(both.capturing, capture);
  assert.equal(both.progress, 0.4);

  // The Git lookup and findings update after an index completed in the last
  // five minutes join it; work that started before the index does not.
  assert.deepEqual(labels(group([followUp, substring], { lastIndex })), ["Looking up merges in Git"]);
  assert.equal(group([followUp, substring], { lastIndex }).progress, null);
  assert.deepEqual(labels(group([followUp, findings, substring], { lastIndex })), ["Looking up merges in Git", "Updating findings"]);
  assert.equal(group([findings, substring], { lastIndex }), null);
  assert.equal(group([followUp], { lastIndex, now: now + 300_000 }), null);
  assert.equal(group([followUp], { lastIndex: { ...lastIndex, state: "failed" } }), null);
  assert.equal(group([{ ...followUp, started_at: new Date(now - 20_000).toISOString() }], { lastIndex }), null);
  assert.equal(group([firstScan], { lastIndex }), null);

  // The Tools ledger is part of a running upgrade only at its tools step.
  const upgrade = step => ({ running: true, step, steps: [{ id: "usage", label: "Correct token attribution" }, { id: "tools", label: "Build the Tools ledger" }] });
  assert.deepEqual(labels(group([tools], { upgradeState: upgrade("tools") })), ["Building the Tools ledger"]);
  assert.equal(group([tools], { upgradeState: upgrade("usage") }), null);
  assert.equal(group([tools], { upgradeState: { ...upgrade("tools"), running: false } }), null);
  // An upgrade without a stage of its own is labelled by its current step.
  assert.equal(group([{ kind: "maintenance", label: "Upgrading the library", progress: 0.2 }], { upgradeState: upgrade("usage") }).label, "Correct token attribution");
  // Index changes says which step it is on.
  assert.equal(group([{ kind: "sync", automatic: true, label: "Indexing changes: checking for changes" }]).label, "Checking for changes");
});

test("the page's own upgrade status decides whether the Tools ledger is part of an update", () => {
  const { window } = loadClassicScript("library.js");
  const tools = { kind: "maintenance", label: "Building the Tools ledger", progress: 0.5 };
  assert.equal(window.pharosLibrary.libraryUpdate([tools]), null);
  window.dispatchEvent(new window.CustomEvent("pharos:upgrade-status", { detail: { running: true, step: "tools", steps: [] } }));
  assert.deepEqual(labels(window.pharosLibrary.libraryUpdate([tools])), ["Building the Tools ledger"]);
});

test("Index changes is busy while a capture or index holds the library, or other work this page follows runs", () => {
  const cases = [
    [{ activities: [{ kind: "capture" }] }, true],
    [{ activities: [{ kind: "capture-other" }] }, true],
    [{ activities: [{ kind: "sync" }] }, true],
    [{ activities: [{ kind: "maintenance" }, { kind: "index" }] }, true],
    [{ activities: [{ kind: "maintenance" }, { kind: "git" }, { kind: "backup" }] }, false],
    [{ activities: [] }, false],
    [{}, false],
    [{ activities: [], incremental: true }, true],
    [{ activities: [], local: "capture" }, true],
    [{ activities: [], upgrading: true }, true],
  ];
  for (const [state, busy] of cases) assert.equal(library.headerBusy(state), busy, JSON.stringify(state));

  // The standalone action must receive busy updates even when the drive
  // control is absent, and recover after the work finishes.
  const { window } = loadClassicScript('library.js');
  const attributes = new Map(), syncOnly = { setAttribute: (key, value) => attributes.set(key, value) };
  window.document.getElementById = id => id === 'headerSyncOnly' ? syncOnly : null;
  for (const running of [true, false]) {
    window.dispatchEvent(new window.CustomEvent('pharos:upgrade-status', { detail: { running, steps: [] } }));
    assert.equal(syncOnly.disabled, running);
    assert.equal(attributes.get('aria-busy'), String(running));
  }
});
