// background.js: Settings → Background Processing' model for a GET
// /api/background document (window.pharosBackground). Words come from the
// server; these tests check structure, ages, times and shares, not copy.
import { test } from "node:test";
import assert from "node:assert/strict";
import { loadClassicScript, plain } from "../helpers/classic-script.mjs";

const background = loadClassicScript("background.js").window.pharosBackground;

const at = Date.parse("2026-10-29T12:00:00Z");
const minutesAgo = minutes => new Date(at - minutes * 60000).toISOString();
const document = {
  settings: { mode: "on", interval_seconds: 300, daily_at: "03:00", hold_when_busy: true, overrides: {} },
  rule: { frequent_seconds: 60, hourly_seconds: 600 },
  descriptions: { frequent: "Frequent words", hourly: "Hourly words", daily: "Daily words" },
  view: {
    mode: "on",
    items: [
      { id: "conversations", label: "Item A", sub: "Sub A", state: "current", state_label: "Updated", as_of: minutesAgo(2), next_at: minutesAgo(-3), fixed: true, choices: null,
        cost: { seconds: 50, runs: 50, class: "light", text: "Cost A" } },
      { id: "writing", label: "Item B", sub: "Sub B", state: "queued", state_label: "Up next", as_of: minutesAgo(50), next_at: minutesAgo(-1), note: "Note B",
        choices: [{ value: "", label: "Choice 1" }, { value: "daily", label: "Choice 2" }], override: "", cost: { seconds: 358, runs: 7, class: "heavy", text: "Cost B" } },
      { id: "preserve", label: "Item C", state: "unavailable", state_label: "Not set up", as_of: null, next_at: null, cost: null },
      { id: "backup", label: "Item D", state: "manual", state_label: "Only when you ask", as_of: minutesAgo(26 * 60), next_at: null, cost: null },
    ],
    unscheduled: ["backup", "gone"],
    cadences: [
      { id: "frequent", label: "Row A", state: "current", state_label: "Updated", as_of: minutesAgo(2), next_at: minutesAgo(-3), items: ["conversations"] },
      { id: "hourly", label: "Row B", state: "queued", state_label: "Up next", as_of: null, next_at: null, items: ["writing"] },
      { id: "daily", label: "Row C", state: "unavailable", state_label: "Not set up", items: ["preserve", "gone"] },
    ],
    everything: { seconds: 410, text: "Everything words" },
  },
  cost: { window_hours: 24, seconds: 0, peak_bytes: 9.5e9, cadences: [
    { id: "frequent", label: "Frequently", seconds: 3 * 3600, measured: true, text: "197 checks" },
    { id: "hourly", label: "Hourly", seconds: 3600, measured: false, text: "projected" },
    { id: "daily", label: "Daily", seconds: 0, measured: false, text: "" },
  ] },
};

test("one column per cadence, each with its state, its description and its items in the server's order", () => {
  const model = plain(background.scheduleModel(document, at));
  assert.equal(model.mode, "on");
  assert.deepEqual(model.columns.map(column => [column.id, column.description, column.dot]), [["frequent", "Frequent words", "current"], ["hourly", "Hourly words", "busy"], ["daily", "Daily words", "paused"]]);
  assert.equal(model.columns[0].text, "Updated 2 min ago · next in 3 min");
  assert.deepEqual(model.columns[2].items.map(item => item.id), ["preserve"], "an item the document does not list is left out");
  assert.deepEqual(model.unscheduled.map(item => [item.id, item.text]), [["backup", "Only when you ask · 26 h ago"]], "only when you ask is apart from the columns");
  assert.equal(model.everything, "Everything words");
  assert.equal(background.scheduleModel({}, at), null);
});

test("label and agent event settings remain available when no labeling work is scheduled", () => {
  for (const enabled of [false, true]) {
    const labels = { enabled, can_change: true, status: enabled ? 'Waiting for model' : 'Off' };
    const agentEvents = { ...labels, can_change: !enabled };
    const model = plain(background.scheduleModel({ ...document, labels, agent_events: agentEvents }, at));
    assert.deepEqual(model.labels, labels);
    assert.deepEqual(model.agentEvents, agentEvents);
    assert.ok(!model.columns.some(column => column.items.some(item => item.id === 'labels')));
  }
});

test("labeling offers thread choices only for CPU execution, independently for each model", () => {
  const saved = {threads: 4, thread_choices: [1, 2, 4], threads_can_change: true};
  for (const device of ['cpu', 'gpu', 'unknown', 'unavailable']) {
    const execution = {device, status: device, detail: 'From the worker'};
    const controls = plain(background.labelingControls({...saved, execution}));
    assert.deepEqual(controls.execution, execution);
    assert.deepEqual(controls.threadChoices, device === 'cpu' ? saved.thread_choices : []);
    assert.equal(controls.threadsCanChange, device === 'cpu');
  }
  assert.deepEqual(plain(background.labelingControls(saved)).threadChoices, []);
  assert.equal(background.labelingControls({...saved, execution: {device:'cpu'}, threads_can_change: false}).threadsCanChange, false);
  assert.equal(background.labelingControls({...saved, enabled:true, status:'Routine status', execution:{device:'gpu'}}).notice, '');
  const notice = 'A model download needs attention';
  assert.equal(background.labelingControls({...saved, notice, execution:{device:'unknown'}}).notice, notice);
  assert.ok(background.labelingControls({...saved, enabled:true, execution:{device:'unavailable'}}).notice);
  assert.equal(background.labelingControls({...saved, enabled:false, execution:{device:'unavailable'}}).notice, '');
});

test("an item carries its cost class, its choices and its state line", () => {
  const writing = plain(background.scheduleModel(document, at)).columns[1].items[0];
  assert.deepEqual(writing.cost, { class: "heavy", text: "Cost B" });
  assert.deepEqual(writing.choices.map(choice => choice.value), ["", "daily"]);
  assert.equal(writing.note, "Note B");
  assert.equal(writing.text, "Up next");
  const fixed = plain(background.scheduleModel(document, at)).columns[0].items[0];
  assert.deepEqual(fixed.choices, [], "a fixed item has no choices");
  assert.equal(fixed.sources, null);
  assert.equal(fixed.stop, null, "an item nothing stoppable runs for offers Run now");
  assert.deepEqual(plain(background.itemModel({ ...document.view.items[0], state: "running", stop: { id: "op-1" } }, at)).stop, { id: "op-1" }, "a stoppable run offers Stop");
  const filtered = plain(background.itemModel({ ...document.view.items[0], sources: [{ name: "claude", selected: true }, { name: "codex", selected: false }] }, at));
  assert.deepEqual(filtered.sources, [{ name: "claude", selected: true }, { name: "codex", selected: false }], "the sources scheduled indexing reads, to choose from");
  assert.equal(plain(background.scheduleModel(document, at)).columns[2].items[0].cost, null);
  const temporary = { cadence: "hourly", expires_at: "2026-11-05T12:00:00Z" };
  const moves = [{ cadence: "frequent", label: "Promote" }, { cadence: "daily", label: "Demote" }];
  const promoted = plain(background.itemModel({ ...document.view.items[1], cadence_moves: moves, temporary_cadence: temporary }, at));
  assert.deepEqual(promoted.moves, moves);
  assert.deepEqual(promoted.temporary, temporary);
  assert.deepEqual(writing.moves, []);
  assert.equal(writing.temporary, null);
});

test("an item's typical run time leads its state line", () => {
  const item = { ...document.view.items[3], cost: { seconds: 9000, runs: 7, class: "heavy", text: "Cost D", typical: "Typical D" } };
  assert.equal(background.itemModel(item, at).text, "Typical D · Only when you ask · 26 h ago");
  assert.equal(background.itemModel(document.view.items[1], at).text, "Up next", "an unmeasured typical time leaves the state alone");
});

test("a dragged item moves for a week to another cadence, and dropping it on its usual one ends the week", () => {
  const item = { ...background.itemModel(document.view.items[1], at), adaptive: true, column: "hourly", usual: "hourly" };
  assert.equal(background.dropCadence(item, "frequent"), "frequent");
  assert.equal(background.dropCadence(item, "daily"), "daily");
  assert.equal(background.dropCadence(item, "hourly"), null, "its own column");
  assert.equal(background.dropCadence(item, "manual"), "manual");
  assert.equal(background.dropCadence(item, "unknown"), null);
  for (const properties of [{ adaptive: true }, { fixed: true }, { opt_in: true }, {}]) {
    for (const from of ["frequent", "hourly", "daily", "manual"]) {
      for (const to of ["frequent", "hourly", "daily", "manual"]) {
        assert.equal(background.dropCadence({ ...item, ...properties, column: from }, to), from === to ? null : to);
      }
    }
  }
  const promoted = { ...item, column: "frequent", temporary: { cadence: "frequent" } };
  assert.equal(background.dropCadence(promoted, "hourly"), "");
  assert.equal(background.dropCadence(promoted, "daily"), "daily");
  assert.equal(background.dropCadence(null, "daily"), null);
  const unscheduled = background.itemModel({ ...document.view.items[1], adaptive: true, cadence: "manual", column: "hourly", usual_cadence: "manual" }, at);
  assert.equal(unscheduled.column, "manual", "only when you ask is drawn in its own column, whatever its placement");
  assert.equal(background.dropCadence(unscheduled, "hourly"), "hourly", "so its placement's column takes it");
});

test("every cadence keeps a column, an empty one too, to drag to", () => {
  const view = { ...document.view, cadences: document.view.cadences.filter(row => row.id !== "hourly") };
  const model = plain(background.scheduleModel({ ...document, view }, at));
  assert.deepEqual(model.columns.map(column => [column.id, column.empty]), [["frequent", false], ["hourly", true], ["daily", false]]);
  assert.equal(model.columns[1].description, "Hourly words");
  assert.deepEqual(model.columns[1].items, []);
});

test("pending moves appear immediately in scheduled and manual columns without changing the saved document", () => {
  const saved = structuredClone(document);
  const model = plain(background.scheduleModel(document, at, {writing: 'manual', backup: 'hourly', conversations: 'daily'}));
  assert.deepEqual(model.columns.map(column => [column.id, column.empty, column.items.map(item => item.id)]),
    [['frequent', true, []], ['hourly', false, ['backup']], ['daily', false, ['preserve', 'conversations']]]);
  assert.deepEqual(model.unscheduled.map(item => item.id), ['writing']);
  const moved = model.unscheduled[0];
  assert.equal(moved.column, 'manual');
  assert.deepEqual(moved.temporary, {cadence: 'manual'});
  assert.deepEqual(document, saved);
  const restored = plain(background.scheduleModel(document, at));
  assert.deepEqual(restored.columns[1].items.map(item => item.id), ['writing']);
});

test("returning a pending move to its usual cadence ends the temporary schedule", () => {
  const view = {...document.view, items: document.view.items.map(item => item.id === 'writing'
    ? {...item, cadence: 'hourly', usual_cadence: 'daily', temporary_cadence: {cadence: 'hourly', expires_at: minutesAgo(-60)}} : item)};
  const moved = background.scheduleModel({...document, view}, at, {writing: 'manual'}).unscheduled.find(item => item.id === 'writing');
  assert.equal(background.dropCadence(moved, 'daily'), '');
  const restored = background.scheduleModel({...document, view}, at, {writing: ''}).columns[2].items.find(item => item.id === 'writing');
  assert.equal(restored.column, 'daily');
  assert.equal(restored.temporary, null);
});

test("settings saves run in order and retain newer moves while earlier responses arrive", async () => {
  const requests = [], snapshots = [];
  const queue = background.createSettingsQueue(body => {
    const request = {...Promise.withResolvers(), body: plain(body)};
    requests.push(request);
    return request.promise;
  }, next => snapshots.push(next), () => {});
  const before = queue.revision;
  const first = queue.save({temporary_cadences: {writing: 'daily'}});
  const second = queue.save({temporary_cadences: {writing: 'manual', backup: 'hourly'}});
  assert.equal(queue.busy, true);
  assert.ok(queue.revision > before, 'a refresh started before saving is stale');
  assert.deepEqual(plain(queue.cadences), {writing: 'manual', backup: 'hourly'});
  await Promise.resolve();
  assert.equal(requests.length, 1, 'only one settings request can run at a time');
  requests[0].resolve('first snapshot');
  await first;
  assert.deepEqual(snapshots, ['first snapshot']);
  assert.deepEqual(plain(queue.cadences), {writing: 'manual', backup: 'hourly'});
  await Promise.resolve();
  assert.equal(requests.length, 2);
  requests[1].resolve('second snapshot');
  await second;
  assert.deepEqual(snapshots, ['first snapshot', 'second snapshot']);
  assert.equal(queue.busy, false);
  assert.deepEqual(plain(queue.cadences), {});
  assert.ok(queue.revision > before, 'a pre-save refresh remains stale after all saves finish');
});

test("a failed settings save rolls back only its move and subsequent saves still run", async () => {
  const requests = [], snapshots = [];
  let updates = 0;
  const queue = background.createSettingsQueue(() => {
    const request = Promise.withResolvers();
    requests.push(request);
    return request.promise;
  }, next => snapshots.push(next), () => updates++);
  const first = queue.save({temporary_cadences: {writing: 'daily'}});
  const failed = assert.rejects(first, /Could not save/);
  const second = queue.save({temporary_cadences: {backup: 'frequent'}});
  await Promise.resolve();
  requests[0].reject(new Error('Could not save'));
  await failed;
  assert.deepEqual(plain(queue.cadences), {backup: 'frequent'});
  assert.deepEqual(snapshots, [], 'the last saved document remains the rollback state');
  await Promise.resolve();
  assert.equal(requests.length, 2);
  requests[1].resolve('saved');
  await second;
  assert.equal(queue.busy, false);
  assert.equal(updates, 4, 'each move and each settlement redraws');
});

test("a pause is the hour's or the day's by when it ends", () => {
  assert.equal(background.pauseLength(minutesAgo(-45), at), "hour");
  assert.equal(background.pauseLength(minutesAgo(-23 * 60), at), "day");
  const paused = { ...document, settings: { ...document.settings, mode: "paused", paused_until: minutesAgo(-600) } };
  assert.equal(plain(background.scheduleModel(paused, at)).pause, "day");
  assert.equal(plain(background.scheduleModel(document, at)).pause, "");
});

test("a column's run button waits while it runs; Only when you ask's while one of its items runs or none can", () => {
  const model = plain(background.scheduleModel(document, at));
  assert.deepEqual(model.columns.map(column => column.running), [false, false, false]);
  assert.equal(model.unscheduledRunnable, true);
  const running = { ...document, view: { ...document.view, cadences: document.view.cadences.map(row => row.id === "hourly" ? { ...row, state: "running" } : row),
    items: document.view.items.map(item => item.id === "backup" ? { ...item, state: "unavailable" } : item) } };
  const next = plain(background.scheduleModel(running, at));
  assert.equal(next.columns[1].running, true);
  assert.equal(next.unscheduledRunnable, false);
  const disabled = { ...document, view: { ...document.view, unscheduled: ['backup', 'labels'], items: [
    ...document.view.items.map(item => item.id === 'backup' ? { ...item, state: 'unavailable' } : item),
    {id: 'labels', label: 'Message labels', state: 'disabled', state_label: 'Off'},
  ] } };
  const off = plain(background.scheduleModel(disabled, at));
  assert.equal(off.unscheduledRunnable, false);
  assert.equal(off.unscheduled.find(item => item.id === 'labels').text, 'Off');
  assert.equal(off.unscheduled.find(item => item.id === 'labels').dot, 'paused');
});

test("state lines: an age and the next time when current, the retry when failed, the run when missed", () => {
  assert.equal(background.stateText({ state: "current", state_label: "Updated", as_of: minutesAgo(41), next_at: null }, at), "Updated 41 min ago");
  assert.equal(background.stateText({ state: "failed", state_label: "Failed", next_at: minutesAgo(-15) }, at), "Failed · retrying in 15 min");
  assert.equal(background.stateText({ state: "missed", state_label: "Missed", next_at: minutesAgo(-5) }, at), "Missed · runs in 5 min");
  assert.equal(background.stateText({ state: "running", state_label: "Updating now", as_of: minutesAgo(5) }, at), "Updating now");
  assert.equal(background.stateText(null, at), "");
});

test("durations and times", () => {
  assert.equal(background.duration(42), "42 s");
  assert.equal(background.duration(358), "6 min");
  assert.equal(background.duration(3 * 3600), "3.0 h");
  assert.equal(background.ago(minutesAgo(0.2), at), "just now");
  assert.equal(background.until(minutesAgo(-30), at), "in 30 min");
  assert.equal(background.until("not a time", at), "");
});

test("on battery: each cadence's choice and the low-battery rule, only on a Mac with a battery", () => {
  const settings = { ...document.settings, battery: { frequent: "slower", hourly: "wait", daily: "same", low_battery_waits: false },
    battery_choices: { frequent: ["same", "slower", "wait"], hourly: ["same", "wait"], daily: ["same", "wait"] }, battery_slower_minutes: 15, low_battery_percent: 20 };
  const model = plain(background.batteryModel({ settings, power: { battery: true, on_battery: true, percent: 64, low_power_mode: false } }));
  assert.equal(model.onBattery, true);
  assert.match(model.status, /64%/);
  assert.deepEqual(model.rows.map(row => [row.cadence, row.value, row.choices.map(choice => choice.value)]),
    [["frequent", "slower", ["same", "slower", "wait"]], ["hourly", "wait", ["same", "wait"]], ["daily", "same", ["same", "wait"]]]);
  assert.match(model.rows[0].choices[1].label, /15/);
  assert.equal(model.lowWaits, false);
  assert.equal(background.batteryModel({ settings, power: { battery: false, on_battery: false, percent: -1 } }), null);
  assert.equal(background.batteryModel({ settings }), null);
});

// Shared work (docs/work-execution-plan.md §10): the work descriptor of GET
// /api/background and the state of GET /api/work.
const work = { available: true, engine: "scheduled", cpu_slots: 4, max_cpu_slots: 10, scope: "this process", can_change: true };
const nanos = minutes => String(BigInt(at - minutes * 60000) * 1000000n);
const recentKey = `recent:${nanos(48 * 60)}:${nanos(0)}:[analysis.authorship analysis.labels]`;
const inspection = {
  authority: { cpu_cap: 4, cpu_allocated: 3, cpu_waiters: 1 },
  requests: [
    { id: 3, key: "automatic", kinds: ["analysis.labels"], priority: "live", state: "queued", requested_at: minutesAgo(1), members: 0, sealed: 0, unfinished: 0 },
    { id: 7, key: recentKey, kinds: ["analysis.authorship", "analysis.labels"], priority: "foreground", explicit: true, state: "running", requested_at: minutesAgo(5),
      membership_frozen_at: minutesAgo(4), members: 40, sealed: 10, failed: 1, exhausted: 1, unfinished: 28,
      tasks: [{ kind: "analysis.labels", state: "blocked", covered: 0, blocker: { reason: "dependency", depends_on: "analysis.authorship" } }] },
  ],
  ended: [
    { id: 2, key: "explicit-backfill:[analysis.labels]", explicit: true, state: "interrupted", reason: "inputs-keep-changing", requested_at: minutesAgo(90), members: 5, sealed: 3, unfinished: 2 },
    { id: 5, key: recentKey, explicit: true, state: "cancelled", requested_at: minutesAgo(30), members: 0, sealed: 0, unfinished: 0, membership_frozen_at: minutesAgo(29) },
  ],
  tasks: [
    { kind: "analysis.authorship", state: "running", running: { kind: "analysis.authorship", batch: 9, units: 12, cpus: 2 }, batches: 3 },
    { kind: "analysis.labels", state: "blocked", blocker: { reason: "held", detail: "stopped until Resume or a service restart" }, failures: 2,
      failure_examples: [{ kind: "analysis.labels", unit: { id: "c1" }, attempts: 3, error: "Error A", exhausted: true }] },
    { kind: "maintenance.parallelism", batches: 0 },
    { kind: "maintenance.labels.query", state: "blocked", blocker: { reason: "power" } },
  ],
};

test("no Work card without shared work: an older service or the classic engine", () => {
  assert.equal(background.workModel(document, null, at), null, "an older service has no work descriptor");
  assert.equal(background.workModel({ ...document, work: { ...work, available: false } }, null, at), null);
  const bare = plain(background.workModel({ ...document, work }, null, at));
  assert.equal(bare.inspected, false, "without GET /api/work the card keeps its allowance and actions");
  assert.deepEqual([bare.requests, bare.tasks, bare.allowance.inUse], [[], [], ""]);
});

test("the CPU allowance offers whole slots up to this Mac's CPUs and clamps the saved value", () => {
  const allowance = plain(background.cpuAllowance(work, inspection.authority));
  assert.deepEqual([allowance.value, allowance.max, allowance.choices.length, allowance.choices[0], allowance.choices.at(-1)], [4, 10, 10, 1, 10]);
  assert.equal(allowance.canChange, true);
  assert.equal(allowance.locked, "");
  assert.match(allowance.inUse, /3\D+4/);
  assert.match(allowance.inUse, /1/, "waiting grants are counted");
  assert.equal(background.cpuAllowance({ ...work, cpu_slots: 64 }).value, 10);
  assert.equal(background.cpuAllowance({ ...work, cpu_slots: 0 }).value, 1);
  assert.equal(background.cpuAllowance({ ...work, cpu_slots: 2.7 }).value, 2);
  assert.equal(background.cpuAllowance({ ...work, max_cpu_slots: null }).choices.length, 1);
  assert.match(background.cpuAllowance({ ...work, cpu_slots: 2 }, { cpu_cap: 2, cpu_allocated: 3, cpu_over_budget: 1 }).inUse, /1 slot\b/, "a shrunken allowance reports its drain");
});

test("an allowance the process start owns cannot change here, and says why", () => {
  const locked = background.cpuAllowance({ ...work, can_change: false });
  assert.equal(locked.canChange, false);
  assert.match(locked.locked, /PHAROS_BACKGROUND_CPUS/);
  assert.equal(background.cpuAllowance({ ...work, can_change: false, locked_reason: "Reason A" }).locked, "Reason A", "the server's reason wins");
});

test("requests: scope from the key, progress in conversations, reasons, and Cancel only for live explicit ones", () => {
  const model = plain(background.workModel({ ...document, work }, inspection, at));
  assert.deepEqual(model.requests.map(request => request.id), [7, 3], "explicit requests first");
  const [recent, automatic] = model.requests;
  assert.equal(recent.scope, "recent");
  assert.match(recent.title, /2 days/);
  assert.equal(recent.fraction, 0.25);
  assert.match(recent.progress, /^10 of 40 conversations done, 2 failed$/);
  assert.equal(recent.cancellable, true);
  assert.equal(recent.waiting.length, 1);
  assert.match(recent.waiting[0], /Update writing estimates/, "a dependency names the work it waits for");
  assert.equal(automatic.scope, "automatic");
  assert.equal(automatic.cancellable, false, "automatic demand is not cancelled from here");
  assert.equal(automatic.fraction, null);
  assert.ok(automatic.progress, "a request still listing its conversations says so");
  assert.equal(model.recentLive, true);
  assert.equal(model.backfillLive, false);
  assert.deepEqual(model.ended.map(request => [request.id, request.scope, request.live, request.cancellable]), [[5, "recent", false, false], [2, "backfill", false, false]]);
  assert.ok(model.ended[1].reason && model.ended[1].reason !== "inputs-keep-changing", "an ended request's reason is in words");
  assert.ok(model.ended[0].progress, "a frozen empty request says nothing needed updating");
  const hours = background.requestModel({ key: `recent:${nanos(6 * 60)}:${nanos(0)}:[a]`, state: "queued", explicit: true }, at);
  assert.match(hours.title, /6 hours/);
  assert.equal(background.requestModel({ key: "x", scope: "backfill", state: "queued" }, at).scope, "backfill", "the server's scope wins");
});

test("requests and finished requests are bounded", () => {
  const many = Array.from({ length: 8 }, (_, index) => ({ id: index + 1, key: "automatic", state: "queued", requested_at: minutesAgo(index), members: 0, sealed: 0 }));
  const model = plain(background.workModel({ ...document, work }, { requests: many, ended: many.map(request => ({ ...request, state: "succeeded" })), tasks: [] }, at));
  assert.equal(model.requests.length, 5);
  assert.equal(model.moreRequests, 3);
  assert.equal(model.ended.length, 5);
  assert.deepEqual(model.ended.map(request => request.id), [8, 7, 6, 5, 4], "newest finished first");
});

test("tasks: Stop while they have work, Resume while held, Retry after failures; idle tasks are listed apart", () => {
  const model = plain(background.workModel({ ...document, work }, inspection, at));
  const byKind = Object.fromEntries(model.tasks.map(task => [task.kind, task]));
  assert.deepEqual(Object.keys(byKind).sort(), ["analysis.authorship", "analysis.labels", "maintenance.labels.query"]);
  assert.deepEqual(byKind["analysis.authorship"].actions, ["stop"]);
  assert.match(byKind["analysis.authorship"].text, /12 conversations\D+2 CPU/);
  assert.deepEqual(byKind["analysis.labels"].actions, ["resume", "retry"]);
  assert.equal(byKind["analysis.labels"].state, "held");
  assert.ok(!byKind["analysis.labels"].text.includes("service restart"), "a hold's implementation detail is not repeated");
  assert.deepEqual(byKind["analysis.labels"].examples, ["Error A"]);
  assert.deepEqual(byKind["maintenance.labels.query"].actions, ["stop"]);
  assert.deepEqual(model.idleTasks, ["Measure parallel work"]);
  assert.equal(background.taskModel({ kind: "analysis.new", state: "running", running: { units: 1, cpus: 1, coarse: true } }).label, "analysis.new", "an unknown kind keeps its identity");
  assert.equal(background.taskModel({ kind: "analysis.new", label: "Label A" }).label, "Label A", "the server's label wins");
  assert.deepEqual(plain(background.taskModel({ kind: "k", state: "blocked", blocker: { reason: "failures-full" } }).actions), ["stop", "retry"]);
});

test("reasons: every runner reason has words; an unknown one keeps its identity", () => {
  for (const reason of ["dependency", "capacity", "power", "policy", "writer", "external", "held", "failed", "failures-full", "retry-backoff",
    "allocation-exceeds-cap", "request-memory", "request-limit", "recent-first", "paused", "inputs-keep-changing", "units-failed", "dependency-failed", "membership-unavailable"]) {
    const words = background.workReason({ reason });
    assert.ok(words && words !== reason, reason);
  }
  assert.equal(background.workReason({ reason: "new-reason" }), "new-reason");
  assert.equal(background.workReason(null), "");
  assert.ok(background.workReason({ reason: "external", detail: "Detail A" }).endsWith("Detail A"));
});

test("action bodies match POST /api/work and refuse a target it would reject", () => {
  assert.deepEqual(plain(background.workRequestBody("recent")), { action: "recent" });
  assert.deepEqual(plain(background.workRequestBody("backfill")), { action: "backfill" });
  assert.deepEqual(plain(background.workRequestBody("cancel", { id: 7 })), { action: "cancel", request_id: 7 });
  for (const id of [0, -1, 1.5, "7", undefined, 2 ** 53]) assert.equal(background.workRequestBody("cancel", { id }), null, String(id));
  for (const action of ["stop", "resume", "retry"]) assert.deepEqual(plain(background.workRequestBody(action, { kind: "analysis.labels" })), { action, kind: "analysis.labels" });
  assert.equal(background.workRequestBody("stop", {}), null);
  assert.equal(background.workRequestBody("delete", { kind: "analysis.labels" }), null);
});
