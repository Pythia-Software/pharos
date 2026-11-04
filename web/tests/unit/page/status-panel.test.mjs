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

test("a Stop the server refuses leaves Stop enabled for that operation and says why", async () => {
  const operations = [operation("a"), operation("b")], requested = new Set(["a", "b"]), sent = [], reported = [];
  const targets = model(view({ operations })).stop.targets;
  await panel.sendStops(targets, requested, async (path, request) => {
    const { id } = JSON.parse(request.body);
    sent.push([path, request.method, id]);
    return id === "b" ? { ok: false, status: 409, statusText: "Conflict", json: async () => ({ error: "Refused" }) } : { ok: true, json: async () => ({ stopping: true }) };
  }, message => reported.push(message));
  assert.deepEqual(sent, [["/api/operations/stop", "POST", "a"], ["/api/operations/stop", "POST", "b"]]);
  assert.deepEqual([...requested], ["a"]);
  assert.deepEqual(reported, ["Refused"]);
  assert.equal(model(view({ operations }), { stopRequested: requested }).stop.stopping, false);
  await panel.sendStops([{ id: "a", path: "/x", method: "POST", body: {} }], requested, async () => { throw Error("offline"); }, message => reported.push(message));
  assert.equal(requested.size, 0, "a failed request also lets the user try again");
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

test("the load row is CPU, RAM and disk in one row, the server's line and each process's in its tooltip; in the app, every Pharos process's", () => {
  const library = { label: "Library line", cpu_percent: 12, memory_bytes: 340e6, disk_bytes_per_second: 12e6, processes: [{ pid: 1, text: "Service line" }, { pid: 2, text: "Terminal line" }] };
  const own = plain(model(view({ resources: library, library: { drive: { name: "Euclid", ejectable: true }, path: "/Volumes/Euclid/Pharos", storage_bytes: 48e9, wal_bytes: 64e6 } })).load);
  assert.deepEqual(own.metrics.map(metric => [metric.id, metric.label, metric.value]), [["cpu", "CPU", "12%"], ["memory", "RAM", "340 MB"], ["io", "I/O", "12 MB/s"], ["disk", "Storage", "48 GB"], ["wal", "WAL", "64 MB"]]);
  assert.ok(own.tooltip.startsWith("Library line\n"));
  assert.ok(own.tooltip.endsWith("Service line\nTerminal line"));
  // The broker's total, when the store has one, wins over the library's.
  const app = plain(model(view({ resources: library }, { resources: { label: "Machine line", cpu_percent: 337, memory_bytes: 8.2e9, disk_bytes_per_second: null, processes: [] } })).load);
  assert.deepEqual(app.metrics.map(metric => metric.value), ["337%", "8.2 GB", "—", "—", "—"], "a figure not measured yet is a dash");
  // A service that does not measure I/O: no I/O.
  assert.deepEqual(plain(model(view({ resources: { cpu_percent: 45.4, memory_bytes: 1.2e9 } })).load.metrics).map(metric => metric.id), ["cpu", "memory", "disk", "wal"]);
  assert.equal(model(view({ resources: {} })).load, null);
});

test("the broker load remains visible during a service timeout, and partial rates say they are lower bounds", () => {
  const unavailable = model({library: null, service: {name: "Euclid", error: "timeout", resources: {memory_bytes: 1e9}},
    resources: {cpu_percent: 12, cpu_partial: true, memory_bytes: 2e9, disk_bytes_per_second: 4e6, disk_partial: true, label: "Pharos is using resources"},
    lastLibrarySizes: {storage: 48e9, wal: 64e6}, appOperations: [], attention: []});
  assert.equal(unavailable.available, false);
  assert.deepEqual(unavailable.load.metrics.map(metric => metric.value), ["≥12%", "2.0 GB", "≥4.0 MB/s", "last 48 GB", "last 64 MB"]);
  assert.match(unavailable.load.tooltip, /lower bound/);
  assert.match(unavailable.load.tooltip, /last reported/);
});

const at = Date.parse("2026-10-27T12:00:00Z");
const minutesAgo = minutes => new Date(at - minutes * 60000).toISOString();
const background = {
  mode: "on",
  headline: { tone: "bad", title: "Title words", text: "Text words", tech: "Tech words", action: { label: "Action words", path: "/api/background/run", body: { cadence: "daily" } } },
  items: [
    { id: "conversations", label: "Item A", state: "current", state_label: "Updated", as_of: minutesAgo(2), next_at: minutesAgo(-3) },
    { id: "writing", label: "Item B", state: "running", state_label: "Updating now", as_of: minutesAgo(50), next_at: null },
    { id: "repositories", label: "Item C", state: "failed", state_label: "Failed", as_of: minutesAgo(120), next_at: minutesAgo(-15) },
    { id: "preserve", label: "Item D", state: "paused", state_label: "Paused", as_of: minutesAgo(60 * 30), next_at: null },
    { id: "backup", label: "Item E", state: "manual", state_label: "Only when you ask", as_of: minutesAgo(26 * 60), next_at: null },
  ],
  unscheduled: ["backup"],
  cadences: [
    { id: "frequent", label: "Row A", state: "failed", state_label: "1 failed", as_of: minutesAgo(2), next_at: minutesAgo(-15), items: ["conversations", "repositories"] },
    { id: "hourly", label: "Row B", state: "running", state_label: "Updating now", as_of: null, next_at: null, items: ["writing", "missing"] },
    { id: "daily", label: "Row C", state: "current", state_label: "Updated", as_of: minutesAgo(600), next_at: null, items: ["preserve"] },
  ],
  everything: { seconds: 540, text: "Everything words" },
};

test("the schedule is the server's headline, one row per cadence with its items", () => {
  const schedule = plain(panel.scheduleModel(background, at));
  assert.deepEqual(schedule.headline, { tone: "bad", title: "Title words", text: "Text words", tech: "Tech words", action: { label: "Action words", path: "/api/background/run", body: { cadence: "daily" } } });
  assert.deepEqual(schedule.rows.map(row => [row.id, row.dot, row.open]), [["frequent", "bad", true], ["hourly", "busy pulse", true], ["daily", "", false], ["manual", "paused", false]],
    "a row opens when something in it is happening or wrong");
  assert.equal(schedule.rows[0].text, "1 failed · retrying in 15 min");
  assert.equal(schedule.rows[2].text, "Updated 10 h ago");
  assert.deepEqual(schedule.rows[1].items.map(item => item.id), ["writing"], "an item the snapshot does not list is left out");
  assert.equal(schedule.rows[0].what, "Item A, Item C");
  assert.deepEqual(schedule.rows[0].items.map(item => item.text), ["Updated 2 min ago · next in 3 min", "Failed · retrying in 15 min"]);
  assert.equal(schedule.rows[2].items[0].text, "Paused · 30 h ago");
  assert.equal(schedule.everything, undefined, "the panel leaves updating everything to Settings");
  // Only when you ask is one more row, Ad hoc, run as the manual cadence.
  const adhoc = schedule.rows.at(-1);
  assert.deepEqual([adhoc.id, adhoc.label, adhoc.dot, adhoc.run.cadence, adhoc.open], ["manual", "Ad hoc", "paused", "manual", false]);
  assert.deepEqual(adhoc.items.map(item => [item.id, item.text]), [["backup", "Last run 26 h ago"]]);
  // Each row runs its own cadence now, unless it is already running.
  assert.deepEqual(schedule.rows.map(row => [row.run.cadence, row.run.disabled]), [["frequent", false], ["hourly", true], ["daily", false], ["manual", false]]);
  // Updating now pulses; Up next is steady.
  const queued = plain(panel.scheduleModel({ ...background, items: [{ ...background.items[1], state: "queued", state_label: "Up next" }] }, at));
  assert.equal(queued.rows[1].items[0].dot, "busy");
  // A running item's tooltip says what it feeds.
  const feeding = plain(panel.scheduleModel({ ...background, items: [{ ...background.items[1], feeds: "Writing pages" }] }, at));
  assert.equal(feeding.rows[1].items[0].tooltip, "Writing pages update when this finishes.");
  // Routine headlines (up to date, or the item the rows already show
  // running) stay out; the out of the ordinary ones stay in.
  for (const tone of ["good", "busy"]) assert.equal(panel.scheduleModel({ ...background, headline: { tone, title: "T", text: "X" } }, at).headline, null, tone);
  for (const tone of ["bad", "behind", "paused"]) assert.ok(panel.scheduleModel({ ...background, headline: { tone, title: "T", text: "X" } }, at).headline, tone);
  assert.equal(model(view({ background })).schedule.rows.length, 4);
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
  const confirm = model(view(), { nativeEject: true, ejectState: { phase: "confirm", eject: { verdict: "safe-in", summary: "Words", blockers: [{ label: "L", on_eject: "O", verdict: "safe-in" }] } } }).eject;
  assert.equal(confirm.verdict, "safe-in");
  assert.deepEqual(confirm.blockers, ["L: O"]);
  // Waiting lists only what keeps it waiting; the release stops the rest.
  const waiting = model(view(), { nativeEject: true, ejectState: { phase: "waiting", eject: { verdict: "unsafe", summary: "Words",
    blockers: [{ label: "U", on_eject: "O", verdict: "unsafe" }, { label: "S", on_eject: "O", verdict: "safe-in" }] } } }).eject;
  assert.deepEqual(waiting.blockers, ["U: O"]);
  assert.equal(waiting.label, "Waiting to eject…");
  assert.equal(panel.ejectDecision({ verdict: "safe" }), "eject");
  assert.equal(panel.ejectDecision({ verdict: "safe-in" }), "confirm");
  for (const verdict of ["busy", "unsafe", undefined]) assert.equal(panel.ejectDecision({ verdict }), "refuse");
  assert.equal(panel.ejectDecision(null), "refuse");
});

test("Eject says what it does under the shown verdict", () => {
  const label = verdict => model(view({ eject: { verdict, summary: "S", blockers: [] } }), { nativeEject: true }).eject.label;
  assert.equal(label("safe"), "⏏ Eject");
  assert.equal(label("safe-in"), "⏏ Stop and Eject");
  assert.equal(label("busy"), "⏏ Eject when safe");
  assert.equal(label("unsafe"), "⏏ Eject when safe");
  assert.equal(model(view(), { nativeEject: true, ejectState: { phase: "checking" } }).eject.label, "Checking…");
  assert.equal(model(view(), { nativeEject: true, ejectState: { phase: "ejecting", message: "Releasing…" } }).eject.label, "Releasing…");
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

test("other libraries are only counted in the library line; an app operation touching none is listed", () => {
  const shown = model(view({}, { service: { name: "Pending history", position: 1, count: 3 }, others: [
    { id: "b", name: "Archive", drive: "euclid", operations: [operation("t", { kind: "library.transfer", label: "Copy library" })] },
    { id: "c", name: "Spare", operations: [] },
    { id: "app:op", name: "Download model", operations: [operation("d", { kind: "labels.download", label: "Download model", step: { index: 1, count: 2, name: "Fetching" } })] },
  ] }));
  assert.equal(shown.library.of, "1/3");
  assert.deepEqual(shown.others.map(other => other.name), ["Download model"]);
  assert.match(shown.others[0].text, /Download model · step 1 of 2/);
  assert.equal(model(view({}, { service: { name: "Only", position: 1, count: 1 } })).library.of, "", "one library: no count");
});

test("non-background work says what started it", () => {
  const origin = changes => plain(panel.operationLine(operation("a", changes))).secondary[0];
  assert.equal(origin({ trigger: "user" }), "Started by you");
  assert.equal(origin({ trigger: "chained" }), "Follows other work");
});

test("a background pass leads with the current task and its planned position", () => {
  const pass = changes => plain(panel.operationLine(operation("a", {kind: "background.update", variant: "frequent", priority: "background", label: "Frequent update", ...changes})));
  const current = pass({step: {index: 2, count: 4, name: "Repositories & merges", unit: null, done: 0, total: null}});
  assert.equal(current.label, "Repositories & merges");
  assert.equal(current.cadence, "Frequently");
  assert.deepEqual(current.position, {index: 2, count: 4});
  assert.deepEqual(current.primary, []);
  assert.deepEqual(current.secondary, []);
  assert.equal(current.fraction, null, "task position is not a measured percentage");
  assert.match(plain(panel.chipModel(view({operations: [operation("a", {kind: "background.update", variant: "frequent", priority: "background", label: "Frequent update", step: {index: 2, count: 4, name: "Repositories & merges"}})]})).title), /Repositories & merges · Frequently/);
  assert.equal(pass({step: {index: 2, count: null, name: "Repositories & merges"}}).position, null);
  assert.equal(pass({step: {index: 5, count: 4, name: "Repositories & merges"}}).position, null);
  assert.equal(pass({step: null}).label, "Frequent update", "before the first task starts, retain the operation name");
});

test("the panel is titled for what it shows, under the library and its drive", () => {
  const shown = model(view(), { nativeEject: true });
  assert.equal(shown.title, "Background Processing");
  assert.deepEqual([shown.library.name, shown.library.drive], ["Pharos", "Euclid"]);
  assert.equal(model(view({}, { service: { name: "My Library" } })).library.name, "My Library", "the app's name for the library wins over its folder");
  assert.equal(model(view({ library: { drive: { name: "Mac", ejectable: false }, path: "/Users/me/Pharos" } })).library.drive, "this Mac");
});

test("without a snapshot the panel says why", () => {
  const unavailable = model(null, { unavailable: "No answer" });
  assert.equal(unavailable.available, false);
  assert.equal(unavailable.message, "No answer");
});
