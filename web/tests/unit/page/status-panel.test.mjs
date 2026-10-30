// status-panel.js: what the unified activity panel and the header chip
// show for a status snapshot (window.pharosStatusPanel). Words come from the
// snapshot; these tests check structure and numbers, not copy.
import { test } from "node:test";
import assert from "node:assert/strict";
import { loadClassicScript, plain } from "../helpers/classic-script.mjs";

const panel = loadClassicScript("status-panel.js").window.pharosStatusPanel;
const model = (view, options) => plain(panel.panelModel(view, options));

const operation = (id, changes = {}) => ({
  id, kind: "index.changes.manual", label: `Label ${id}`, state: "running", state_label: `Verb ${id}`, priority: "foreground",
  detail: `Detail ${id}`, on_eject: `On eject ${id}`, stoppable: true, stop: { method: "POST", path: "/api/operations/stop", body: { id } },
  step: null, time: null, children: [], children_count: 0, ...changes,
});
const view = (library = {}, extra = {}) => ({
  library: { library: { drive: { name: "Euclid", ejectable: true, location: "external" }, path: "/Volumes/Euclid/Pharos" },
    eject: { verdict: "safe", summary: "Verdict words", blockers: [], checked: "polled" }, operations: [], attention: [], freshness: null, resources: null, ...library },
  others: [], attention: [], ...extra,
});

test("each top-level operation is one line: label, step N of M, its measured amount and a range, with the step's name apart", () => {
  const line = plain(panel.operationLine(operation("a", {
    step: { index: 3, count: 6, name: "Claude Code", unit: "byte", done: 812e6, total: 1980e6, fraction: 0.41 },
    time: { state: "estimating", label: "server range" },
  })));
  assert.deepEqual(line.primary, ["step 3 of 6", "812 MB of 2.0 GB"]);
  assert.deepEqual(line.secondary, ["Claude Code"]);
  assert.equal(line.time, "server range");
  assert.equal(line.fraction, 0.41);
  assert.match(line.tooltip, /Detail a/);
  assert.match(line.tooltip, /On eject a/);
  // No unit, no total, no count: only what is known.
  const bare = plain(panel.operationLine(operation("b", { step: { index: 1, count: null, name: "Discovering", unit: null, done: 0, total: null } })));
  assert.deepEqual(bare.primary, []);
  assert.deepEqual(bare.secondary, ["Discovering"]);
  assert.equal(bare.time, "");
  assert.equal(bare.fraction, null);
  // A unit count, and a state that is not running says its word.
  const waiting = plain(panel.operationLine(operation("c", { state: "blocked", state_label: "Waiting", step: { index: 2, count: 4, name: "Indexing", unit: "conversation", done: 1, total: 1 } })));
  assert.deepEqual(waiting.primary, ["Waiting", "step 2 of 4", "1 of 1 conversation"]);
  assert.deepEqual(waiting.secondary, ["Indexing"]);
  // A state word that only repeats the label is left out.
  assert.deepEqual(plain(panel.operationLine(operation("d", { state: "blocked", state_label: "Label d" }))).primary, []);
});

test("Now shows foreground work, else the first background work", () => {
  const busy = model(view({ operations: [operation("a"), operation("b", { priority: "background" }), operation("old", { state: "succeeded" })] }));
  assert.deepEqual(busy.lines.map(line => line.id), ["a"]);
  const background = model(view({ operations: [operation("b", { priority: "background" }), operation("c", { priority: "background" })] }));
  assert.deepEqual(background.lines.map(line => line.id), ["b"]);
  assert.equal(model(view()).lines.length, 0);
});

test("the app's operations that touch the selected library are on the Now row and make the chip busy, even with the library's service stopped", () => {
  const transfer = operation("t", { kind: "library.transfer", label: "Copy library", priority: "foreground", stoppable: false, stop: undefined, libraries: ["local"] });
  const withService = model(view({}, { appOperations: [transfer] }));
  assert.deepEqual(withService.lines.map(line => line.id), ["t"]);
  assert.equal(plain(panel.chipModel(view({}, { appOperations: [transfer] }))).dot, "busy");
  const stopped = model({ library: null, appOperations: [transfer], others: [], attention: [], service: { name: "Euclid", stateLabel: "Copying the library" } });
  assert.equal(stopped.available, false);
  assert.deepEqual(stopped.lines.map(line => line.id), ["t"]);
  assert.equal(stopped.message, null, "running app work explains a stopped service; no error");
  const chip = plain(panel.chipModel({ library: null, appOperations: [transfer], attention: [], service: { name: "Euclid" } }));
  assert.equal(chip.state, "busy");
  assert.equal(chip.name, "Euclid");
});

test("Stop stays disabled once pressed, until the operations it stopped change", () => {
  const stop = model(view({ operations: [operation("a"), operation("c", { kind: "backup.local" })] }), { stopRequested: new Set(["a", "c"]) }).stop;
  assert.equal(stop.stopping, true);
  assert.equal(model(view({ operations: [operation("a"), operation("c")] }), { stopRequested: new Set(["a"]) }).stop.stopping, false, "a new stoppable operation enables it again");
});

test("the verdict row is the server's short headline, with the full sentence in its tooltip", () => {
  const verdict = model(view({ eject: { verdict: "safe-in", headline: "Short", summary: "Long sentence", blockers: [{ label: "L", on_eject: "O" }] } })).verdict;
  assert.equal(verdict.text, "Short");
  assert.match(verdict.tooltip, /Long sentence/);
  assert.match(verdict.tooltip, /L: O/);
});

test("the schedule and load stay out while the snapshot has none", () => {
  const idle = model(view());
  assert.equal(idle.schedule, null);
  assert.equal(idle.load, null);
  const filled = model(view({ background: { cadences: [], items: [] }, resources: { cpu_percent: 45 } }));
  assert.ok(filled.schedule);
  assert.ok(filled.load);
});

test("the load row is the server's line, each process's line in its tooltip; in the app, every Pharos process's", () => {
  const library = { label: "Library line", cpu_percent: 12, memory_bytes: 340e6, processes: [{ pid: 1, text: "Service line" }, { pid: 2, text: "Terminal line" }] };
  const own = model(view({ resources: library }));
  assert.deepEqual(own.load, { text: "Library line", tooltip: "Service line\nTerminal line" });
  // The broker's total, when the store has one, wins over the library's.
  const app = model(view({ resources: library }, { resources: { label: "Machine line", processes: [] } }));
  assert.deepEqual(app.load, { text: "Machine line", tooltip: "" });
  // A service without a line: numbers only, made here.
  assert.equal(model(view({ resources: { cpu_percent: 45.4, memory_bytes: 1.2e9 } })).load.text, "45% of one core · 1.2 GB");
  assert.equal(model(view({ resources: {} })).load, null);
});

const at = Date.parse("2026-10-27T12:00:00Z");
const minutesAgo = minutes => new Date(at - minutes * 60000).toISOString();
const background = {
  mode: "on",
  headline: { tone: "busy", title: "Title words", text: "Text words", tech: "Tech words", action: { label: "Action words", path: "/api/background/run", body: { cadence: "daily" } } },
  items: [
    { id: "conversations", label: "Item A", state: "current", state_label: "Updated", as_of: minutesAgo(2), next_at: minutesAgo(-3) },
    { id: "writing", label: "Item B", state: "running", state_label: "Updating now", as_of: minutesAgo(50), next_at: null },
    { id: "repositories", label: "Item C", state: "failed", state_label: "Failed", as_of: minutesAgo(120), next_at: minutesAgo(-15) },
    { id: "preserve", label: "Item D", state: "paused", state_label: "Paused", as_of: minutesAgo(60 * 30), next_at: null },
  ],
  cadences: [
    { id: "frequent", label: "Row A", state: "failed", state_label: "1 failed", as_of: minutesAgo(2), next_at: minutesAgo(-15), items: ["conversations", "repositories"] },
    { id: "hourly", label: "Row B", state: "running", state_label: "Updating now", as_of: null, next_at: null, items: ["writing", "missing"] },
    { id: "daily", label: "Row C", state: "current", state_label: "Updated", as_of: minutesAgo(600), next_at: null, items: ["preserve"] },
  ],
  everything: { seconds: 540, text: "Everything words" },
};

test("the schedule is the server's headline, one row per cadence with its items, and the cost of updating everything", () => {
  const schedule = plain(panel.scheduleModel(background, at));
  assert.deepEqual(schedule.headline, { tone: "busy", title: "Title words", text: "Text words", tech: "Tech words", action: { label: "Action words", path: "/api/background/run", body: { cadence: "daily" } } });
  assert.deepEqual(schedule.rows.map(row => [row.id, row.dot, row.open]), [["frequent", "bad", true], ["hourly", "busy", true], ["daily", "", false]],
    "a row opens when something in it is happening or wrong");
  assert.equal(schedule.rows[0].text, "1 failed · retrying in 15 min");
  assert.equal(schedule.rows[2].text, "Updated 10 h ago");
  assert.deepEqual(schedule.rows[1].items.map(item => item.id), ["writing"], "an item the snapshot does not list is left out");
  assert.equal(schedule.rows[0].what, "Item A, Item C");
  assert.deepEqual(schedule.rows[0].items.map(item => item.text), ["Updated 2 min ago · next in 3 min", "Failed · retrying in 15 min"]);
  assert.equal(schedule.rows[2].items[0].text, "Paused · 30 h ago");
  assert.equal(schedule.everything, "Everything words");
  assert.equal(model(view({ background })).schedule.rows.length, 3);
});

test("next times are minutes away, then a clock time", () => {
  assert.equal(panel.until(minutesAgo(-0.5), at), "now");
  assert.equal(panel.until(minutesAgo(-14), at), "in 14 min");
  assert.match(panel.until(minutesAgo(-120), at), /^at /);
  assert.match(panel.until(minutesAgo(-60 * 20), at), /^(at|tomorrow at) /);
});

test("ages are whole minutes, hours or days", () => {
  assert.equal(panel.span(minutesAgo(0.5), at), "<1 min");
  assert.equal(panel.span(minutesAgo(180), at), "3 h");
  assert.equal(panel.ago(minutesAgo(0.5), at), "just now");
  assert.equal(panel.ago(minutesAgo(59), at), "59 min ago");
  assert.equal(panel.ago(minutesAgo(60 * 47), at), "47 h ago");
  assert.equal(panel.ago(minutesAgo(60 * 72), at), "3 d ago");
});

test("Stop reaches every stoppable top-level operation through its declared stop", () => {
  const stop = model(view({ operations: [operation("a"), operation("b", { stoppable: false, stop: undefined }), operation("c", { kind: "backup.local", stop: { method: "POST", path: "/api/operations/stop", body: { id: "c" } } })] })).stop;
  assert.deepEqual(stop.targets.map(target => target.body.id), ["a", "c"]);
  assert.ok(stop.targets.every(target => target.path === "/api/operations/stop" && target.method === "POST"));
  assert.equal(model(view({ operations: [operation("a", { state: "stopping", stoppable: false, stop: undefined })] })).stop.stopping, true);
  assert.equal(model(view()).stop, null);
});

test("Eject appears only for an ejectable drive in the app, and acts only on a fresh verdict", () => {
  assert.equal(model(view()).eject, null, "outside the app there is no eject");
  assert.equal(model(view(), { nativeEject: true }).eject.phase, "idle");
  assert.equal(model(view({ library: { drive: { name: "Mac", ejectable: false } } }), { nativeEject: true }).eject, null);
  const refused = model(view(), { nativeEject: true, ejectState: { phase: "refused", eject: { verdict: "busy", summary: "Words", blockers: [{ label: "L", on_eject: "O" }] } } }).eject;
  assert.equal(refused.verdict, "busy");
  assert.deepEqual(refused.blockers, ["L: O"]);
  assert.equal(panel.ejectDecision({ verdict: "safe" }), "eject");
  assert.equal(panel.ejectDecision({ verdict: "safe-in" }), "confirm");
  for (const verdict of ["busy", "unsafe", undefined]) assert.equal(panel.ejectDecision({ verdict }), "refuse");
  assert.equal(panel.ejectDecision(null), "refuse");
});

test("the verdict shows a less safe answer at once and a safer one only after it holds", () => {
  const safeIn = { verdict: "safe-in" }, safe = { verdict: "safe" }, unsafe = { verdict: "unsafe" };
  let shown = panel.settleVerdict(null, safe, 0);
  assert.equal(shown.eject.verdict, "safe");
  shown = panel.settleVerdict(shown, safeIn, 100);
  assert.equal(shown.eject.verdict, "safe-in", "less safe shows at once");
  shown = panel.settleVerdict(shown, safe, 200);
  assert.equal(shown.eject.verdict, "safe-in", "a flicker back to safe is held");
  assert.ok(shown.pending);
  shown = panel.settleVerdict(shown, unsafe, 300);
  assert.equal(shown.eject.verdict, "unsafe", "worse is never delayed");
  shown = panel.settleVerdict(shown, safe, 400);
  shown = panel.settleVerdict(shown, safe, 2500);
  assert.equal(shown.eject.verdict, "safe", "a safer verdict that held is shown");
});

test("the attention list keeps each item's words, severity and action", () => {
  const items = model(view({}, { attention: [{ id: "backup-failed", library: "a", severity: "error", title: "T", detail: "D", action: { label: "Open Backups", path: "/settings/backups" } }, { id: "x", severity: "warning", title: "W" }] })).attention;
  assert.equal(items.length, 2);
  assert.deepEqual(items[0].action, { label: "Open Backups", path: "/settings/backups" });
  assert.equal(items[1].severity, "warning");
  assert.equal(items[1].action, null, "an item without an action is not actionable");
});

test("the chip's dot: attention first, then running work, then idle", () => {
  const chip = (library, extra) => plain(panel.chipModel(view(library, extra)));
  assert.equal(chip().dot, "");
  assert.equal(chip().state, "idle");
  assert.equal(chip({ operations: [operation("a")] }).dot, "busy");
  assert.equal(chip({ operations: [operation("a")] }, { attention: [{ severity: "warning", title: "W" }] }).dot, "warn");
  assert.equal(chip({ operations: [operation("a")] }, { attention: [{ severity: "error", title: "E" }] }).dot, "bad");
  assert.match(chip({}, { attention: [{ severity: "error", title: "E" }] }).title, /E/);
  assert.equal(plain(panel.chipModel(null)).state, "unavailable");
  // No snapshot of the selected library, but the broker says why.
  const notOpen = plain(panel.chipModel({ library: null, appOperations: [], attention: [{ severity: "error", title: "Euclid is not open" }], service: { name: "Euclid" } }));
  assert.equal(notOpen.dot, "bad");
  assert.match(notOpen.title, /Euclid is not open/);
  assert.equal(chip().name, "Euclid");
  assert.equal(chip({ library: { drive: { ejectable: false }, portable: false } }).name, "This Mac");
  assert.equal(chip({ library: { drive: { ejectable: false }, portable: true } }).name, "Library");
});

test("Index changes waits for captures and indexes, not for other work", () => {
  assert.equal(panel.indexBusy(view({ operations: [operation("a", { kind: "index.changes.automatic" })] })), true);
  assert.equal(panel.indexBusy(view({ operations: [operation("a", { kind: "capture.cli" })] })), true);
  assert.equal(panel.indexBusy(view({ operations: [operation("a", { kind: "library.upgrade" })] })), true);
  assert.equal(panel.indexBusy(view({ operations: [operation("a", { kind: "backup.local" })] })), false);
  assert.equal(panel.indexBusy(view({ operations: [operation("a", { kind: "index.captures", state: "failed" })] })), false);
  assert.equal(panel.indexBusy(view(), true), true, "a run this page is starting");
});

test("other libraries say what runs there, or their verdict", () => {
  const others = model(view({}, { others: [
    { id: "b", name: "Archive", operations: [operation("t", { kind: "library.transfer", label: "Copy library", step: { index: 1, count: 2, name: "Copying" } })], eject: { summary: "S" } },
    { id: "c", name: "Spare", operations: [], eject: { headline: "Idle words", summary: "S" } },
  ] })).others;
  assert.match(others[0].text, /Copy library · step 1 of 2/);
  assert.equal(others[1].text, "Idle words");
  const idle = model(view({}, { others: [{ id: "c", name: "Spare", operations: [], stateLabel: "Open", eject: { headline: "H", summary: "Long" } }] })).others[0];
  assert.equal(idle.text, "Open", "an idle library gets its short state word");
});

test("without a snapshot the panel says why", () => {
  const unavailable = model(null, { unavailable: "No answer" });
  assert.equal(unavailable.available, false);
  assert.equal(unavailable.message, "No answer");
});
