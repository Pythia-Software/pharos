// background.js: Settings → Background updates' model for a GET
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
      { id: "backup", label: "Item C", state: "unavailable", state_label: "Not set up", as_of: null, next_at: null, cost: null },
    ],
    cadences: [
      { id: "frequent", label: "Row A", state: "current", state_label: "Updated", as_of: minutesAgo(2), next_at: minutesAgo(-3), items: ["conversations"] },
      { id: "hourly", label: "Row B", state: "queued", state_label: "Up next", as_of: null, next_at: null, items: ["writing"] },
      { id: "daily", label: "Row C", state: "unavailable", state_label: "Not set up", items: ["backup", "gone"] },
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
  assert.deepEqual(model.columns[2].items.map(item => item.id), ["backup"], "an item the document does not list is left out");
  assert.equal(model.everything, "Everything words");
  assert.equal(background.scheduleModel({}, at), null);
});

test("an item carries its cost class, its choices and its state line", () => {
  const writing = plain(background.scheduleModel(document, at)).columns[1].items[0];
  assert.deepEqual(writing.cost, { class: "heavy", text: "Cost B" });
  assert.deepEqual(writing.choices.map(choice => choice.value), ["", "daily"]);
  assert.equal(writing.note, "Note B");
  assert.equal(writing.text, "Up next");
  const fixed = plain(background.scheduleModel(document, at)).columns[0].items[0];
  assert.deepEqual(fixed.choices, [], "a fixed item has no choices");
  assert.equal(plain(background.scheduleModel(document, at)).columns[2].items[0].cost, null);
});

test("state lines: an age and the next time when current, the retry when failed, the run when missed", () => {
  assert.equal(background.stateText({ state: "current", state_label: "Updated", as_of: minutesAgo(41), next_at: null }, at), "Updated 41 min ago");
  assert.equal(background.stateText({ state: "failed", state_label: "Failed", next_at: minutesAgo(-15) }, at), "Failed · retrying in 15 min");
  assert.equal(background.stateText({ state: "missed", state_label: "Missed", next_at: minutesAgo(-5) }, at), "Missed · runs in 5 min");
  assert.equal(background.stateText({ state: "running", state_label: "Updating now", as_of: minutesAgo(5) }, at), "Updating now");
  assert.equal(background.stateText(null, at), "");
});

test("the day's cost is each cadence's share of the total", () => {
  const cost = plain(background.costModel(document.cost));
  assert.equal(cost.total, "4.0 h");
  assert.deepEqual(cost.segments.map(segment => [segment.id, Number(segment.share.toFixed(2)), segment.measured]), [["frequent", 0.75, true], ["hourly", 0.25, false], ["daily", 0, false]]);
  assert.equal(cost.peakBytes, 9.5e9);
  assert.equal(background.costModel(null), null);
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
