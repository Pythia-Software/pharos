// library.js: which running activities form the library update, what each
// activity's progress bar and timing show, and when Index changes is busy
// (window.pharosLibrary).
import { test } from "node:test";
import assert from "node:assert/strict";
import { loadClassicScript, plain } from "../helpers/classic-script.mjs";

const script = loadClassicScript("library.js");
const library = script.window.pharosLibrary;

test('app updates distinguish available releases from download and install progress', () => {
  const present = state => plain(library.appUpdatePresentation(state));
  for (const state of [null, {}, {version: '0.8.0'}, {phase: 'available'}, {version: '0.8.0', phase: 'failed'}]) {
    assert.equal(present(state), null);
  }
  const available = present({version: '12.123.456-beta.123', phase: 'available'});
  assert.equal(available.busy, false);
  assert.equal(available.icon, 'download');
  assert.equal(available.progress, null);
  assert.ok(available.title.includes('12.123.456-beta.123'));
  assert.match(available.title, /restart/);
  for (const phase of ['downloading', 'preparing', 'installing']) {
    assert.equal(present({version: '0.8.0', phase}).busy, true);
  }
  const downloading = present({version: '0.8.0', phase: 'downloading', progress: 42.9});
  assert.equal(downloading.progress, 42);
  assert.match(downloading.title, /Downloading.*0\.8\.0.*42%/);
  for (const [progress, expected] of [[200, 100], [-10, 0], [0, 0], [NaN, null], [undefined, null]]) {
    assert.equal(present({version: '0.8.0', phase: 'downloading', progress}).progress, expected);
  }
  const preparing = present({version: '0.8.0', phase: 'preparing', progress: 42});
  assert.equal(preparing.icon, 'package');
  assert.equal(preparing.progress, null, 'the download percentage does not carry into preparation');
  assert.match(preparing.title, /Verifying and preparing.*0\.8\.0/);
  assert.equal(present({version: '0.8.0', phase: 'installing'}).icon, 'restart');
  assert.match(present({version: '0.8.0', phase: 'installing'}).title, /Installing.*0\.8\.0.*restarting/);
});

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

test("the overview groups background work under sync and keeps the current step's progress", () => {
  const refresh = {kind: 'maintenance', label: 'Indexing the Library view', detail: '2 workspaces to index',
    progress: .4, stoppable: true, stop_path: '/api/library/refresh/stop'};
  const findings = {kind: 'maintenance', label: 'Updating findings', detail: 'Measuring watched findings'};
  const git = {kind: 'git', label: 'Looking up merges in Git'};
  const backup = {kind: 'backup', label: 'Backing up'};
  const upgrade = {kind: 'maintenance', label: 'Upgrading the library'};
  const background = library.overviewActivities([refresh, findings, git, backup, upgrade]);
  assert.equal(background.length, 3);
  assert.equal(background[0].label, 'Automatic sync in progress');
  assert.equal(library.activityPresentation(background[0]).title, refresh.label);
  assert.equal(library.activityProgress(background[0]), .4);
  assert.equal(library.activityOverallProgress(background[0]), null);
  assert.equal(background[1], backup);
  assert.equal(background[2], upgrade);
  assert.match(library.activityPresentation(background[0]).details.join('\n'), /Updating findings.*Measuring watched findings/);
  assert.ok(library.activityPresentation(background[0]).details.includes(git.label));

  const sync = {kind: 'sync', label: 'Manual sync in progress', work: {phase: 'indexing', unit: 'conversation', done: 3, total: 4},
    forecast: {state: 'provisional', current_step: 4, total_steps: 8, progress: .6}, stoppable: true, stop_path: '/api/index/cancel'};
  const activities = [refresh, sync, findings];
  const grouped = library.overviewActivities(activities);
  assert.equal(grouped.length, 1);
  assert.equal(grouped[0].label, sync.label);
  assert.equal(library.activityPresentation(grouped[0]).title, 'Indexing');
  assert.equal(library.activityPresentation(grouped[0]).stepLabel, 'Step 4/8');
  assert.equal(library.activityProgress(grouped[0]), .75);
  assert.equal(library.activityOverallProgress(grouped[0]), .6);
  assert.match(library.activityPresentation(grouped[0]).details.join('\n'), /Indexing the Library view.*2 workspaces to index/);
  assert.equal(activities.length, 3);
  assert.equal(refresh.label, 'Indexing the Library view');
  assert.equal(library.stopTargets(activities).length, 2);
  assert.equal(library.overviewActivities([]).length, 0);
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

test("whole-operation forecasts take priority while stage progress stays measured", () => {
  const activity = {started_at: new Date(started).toISOString(), work: {unit: 'group', done: 20, total: 100},
    estimate: {state: 'estimating', low_seconds: 30, high_seconds: 50},
    forecast: {state: 'provisional', low_seconds: 180, high_seconds: 300, basis: 'All sources and analyses'}};
  const whole = library.activityTiming(activity, {now: started + 10_000});
  assert.deepEqual([whole.range, whole.state, whole.scope, whole.basis], ['3–5 min', 'provisional', 'operation', 'All sources and analyses']);
  const stage = library.activityTiming(activity, {scope: 'stage', now: started + 10_000});
  assert.deepEqual([stage.range, stage.scope], ['30–50 sec', 'stage']);
  assert.equal(library.activityProgress(activity), .2);
  activity.forecast.progress = .63;
  assert.equal(library.activityOverallProgress(activity), .63);
  assert.equal(library.activityOverallProgress({...activity, progress: .9, forecast: undefined}), null);
  assert.equal(library.activityOverallProgress(activity, {unavailable: true}), null);
  assert.equal(library.activityOverallProgress({...activity, forecast: {...activity.forecast, progress: 1}}), .99);
  assert.equal(library.activityOverallProgress({...activity, forecast: {state: 'complete', progress: 1}}), 1);
  assert.equal(library.activityOverallProgress({...activity, forecast: {state: 'estimating', progress: NaN}}), null);
  activity.forecast = {state: 'partial', predicted_steps: 3, remaining_steps: 4};
  const partial = library.activityTiming(activity, {now: started});
  assert.deepEqual([partial.range, partial.state, partial.scope], [null, 'partial', 'operation']);
  assert.equal(library.activityTiming(activity, {now: started, unavailable: true}).range, null);
  assert.equal(library.activityOverallProgress(activity), null);
  activity.forecast = {state: 'stopping'};
  assert.equal(library.activityTiming(activity, {now: started}).state, 'stopping');
  assert.equal(library.activityOverallProgress(activity), null);
});

test("activity summaries keep unique context and measured progress, with explanations available separately", () => {
  const activity = {label: 'Capture & index', detail: 'Codex · discovering inputs · 0 of 4 sources finished',
    work: {phase: 'discovering inputs', source: 'Codex', unit: 'conversation group', done: 0, total: 0, next: 'Index conversations'},
    estimate: {state: 'unknown', basis: 'Discovering inputs before the count is available'},
    forecast: {state: 'partial', predicted_steps: 3, remaining_steps: 4, basis: 'Some remaining steps have no comparable history yet', unknown_steps: ['Classify authorship']}};
  const discovery = library.activityPresentation(activity);
  assert.equal(discovery.title, 'Discovering inputs');
  assert.equal(discovery.stepLabel, 'Step');
  for (const state of ['partial', 'provisional', 'stopping']) {
    const numbered = library.activityPresentation({...activity, forecast: {state, current_step: 4, total_steps: 8}});
    assert.equal(numbered.stepLabel, 'Step 4/8');
  }
  assert.equal(discovery.context, 'Capture & index · Codex · 0 of 4 sources finished');
  assert.equal(discovery.caption, ''); // Discovery has no measurable progress yet.
  const explanations = discovery.details.join('\n');
  for (const fact of [activity.estimate.basis, activity.forecast.basis, '3 of 4', 'Classify authorship', activity.work.next]) assert.ok(explanations.includes(fact), fact);

  const indexing = library.activityPresentation({...activity,
    work: {...activity.work, phase: 'indexing', done: 120, total: 600, size: {unit: 'byte', done: 150e6, total: 300e6}},
    estimate: {state: 'estimating', unit: 'byte', units_per_second: 1e6, basis: 'Recent pace'}});
  for (const fact of ['120 of 600 conversation groups', '150 MB of 300 MB', '50% of input data', '1.0 MB/sec']) assert.ok(indexing.caption.includes(fact), fact);

  const maintenance = library.activityPresentation({label: 'Building substring search', detail: '10 of 20 conversations',
    work: {phase: 'Building substring search', unit: 'conversation', done: 10, total: 20}});
  assert.equal(maintenance.context, '');
  assert.match(maintenance.caption, /10 of 20 conversations/);
  const unstructured = library.activityPresentation({label: 'Looking up merges in Git', detail: 'First scan of this catalog'});
  assert.equal(unstructured.title, 'Looking up merges in Git');
  assert.equal(unstructured.context, 'First scan of this catalog');
  assert.equal(unstructured.caption, '');

  const unavailable = library.activityPresentation({...activity, estimate: {units_per_second: 10, unit: 'conversation', basis: 'Stale estimate'}}, {unavailable: true});
  assert.equal(unavailable.caption, '');
  assert.deepEqual(plain(unavailable.details), ['Next: Index conversations']);
});

test("Capture & index availability follows running work, source settings, upgrades and release", () => {
  for (const state of [{activities: [{kind: 'index'}]}, {activities: [{kind: 'capture-other'}]}, {incremental: true},
    {local: true}, {upgrading: true}, {sourcesEnabled: false}, {releasing: true}]) {
    assert.equal(library.captureIndexDisabled(state), true, JSON.stringify(state));
  }
  for (const state of [{}, {activities: []}, {sourcesEnabled: true}, {sourcesEnabled: false, upgradeNeeded: true}]) {
    assert.equal(library.captureIndexDisabled(state), false, JSON.stringify(state));
  }
});

test("one Stop reaches each stoppable task once and stays available until every target is stopping", () => {
  const index = {kind: 'index', stoppable: true, stop_path: '/api/index/cancel'};
  const refresh = {kind: 'maintenance', stoppable: true, stop_path: '/api/library/refresh/stop'};
  assert.deepEqual(plain(library.stopTargets([index, {...index, kind: 'sync'}, refresh, {kind: 'capture'}])), [
    {path: index.stop_path, stopping: false}, {path: refresh.stop_path, stopping: false},
  ]);
  assert.deepEqual(plain(library.stopTargets([{...index, stopping: true}, refresh])), [
    {path: index.stop_path, stopping: true}, {path: refresh.stop_path, stopping: false},
  ]);
  assert.deepEqual(plain(library.stopTargets([{stoppable: true, stopping: true}])), [{path: index.stop_path, stopping: true}]);
  assert.equal(library.stopTargets([{kind: 'capture'}, {kind: 'backup'}]).length, 0);
});
