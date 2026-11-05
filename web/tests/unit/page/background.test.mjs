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
