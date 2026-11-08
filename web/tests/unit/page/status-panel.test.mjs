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
    step: { index: 3, count: 6, name: "Claude Code", detail: "Saving prepared inputs", unit: "byte", done: 812e6, total: 1980e6, fraction: 0.41 },
    time: { state: "estimating", label: "server range" },
  })));
  assert.deepEqual(line.primary, ["step 3 of 6", "812 MB of 2.0 GB"]);
  assert.deepEqual(line.secondary, ["Claude Code", "Saving prepared inputs"]);
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
  assert.deepEqual(waiting.secondary, ["Indexing", "Detail c"]);
  // A state word that only repeats the label is left out.
  assert.deepEqual(plain(panel.operationLine(operation("d", { state: "blocked", state_label: "Label d" }))).primary, []);
});

test("Now keeps concurrent work in start order across snapshots and child stages", () => {
  const first = operation("first", { priority: "background", started_at: "2026-11-05T12:00:00Z" });
  const second = operation("second", { started_at: "2026-11-05T12:01:00Z" });
  const app = operation("app", { started_at: "2026-11-05T12:02:00Z" });
  const old = operation("old", { state: "succeeded", started_at: "2026-11-05T11:00:00Z" });
  const initial = view({ operations: [second, first, old] }, { appOperations: [app] });
  assert.deepEqual(model(initial).lines.map(line => line.id), ["first", "second", "app"]);
  assert.deepEqual(initial.library.operations.map(entry => entry.id), ["second", "first", "old"], "rendering leaves the snapshot unchanged");
  const newest = operation("newest", { started_at: "2026-11-05T12:03:00Z" });
  const advanced = {...first, children: [operation("child", { started_at: "2026-11-05T12:04:00Z" })]};
  const refreshed = view({ operations: [newest, advanced, second, old] }, { appOperations: [app] });
  assert.deepEqual(model(refreshed).lines.map(line => line.id), ["first", "second", "app", "newest"]);
  assert.deepEqual(model(view({ operations: [newest, second] }, { appOperations: [app] })).lines.map(line => line.id), ["second", "app", "newest"]);
  assert.deepEqual(model({ library: null, appOperations: [newest, app] }).lines.map(line => line.id), ["app", "newest"]);
  assert.equal(model(view()).lines.length, 0);
});

test("Now resolves equal or unknown start times consistently across snapshot orders", () => {
  const operations = [
    operation("b", { started_at: "2026-11-05T12:00:00Z" }),
    operation("a", { started_at: "2026-11-05T07:00:00-05:00" }),
    operation("missing"), operation("invalid", { started_at: "unknown" }),
  ];
  const ids = entries => model(view({ operations: entries })).lines.map(line => line.id);
  assert.deepEqual(ids(operations), ["a", "b", "invalid", "missing"]);
  assert.deepEqual(ids(operations.slice().reverse()), ids(operations));
});

test("unknown totals retain completed counts, a current phase, and elapsed time", () => {
  const started = '2026-11-05T12:00:00Z';
  const now = Date.parse(started) + 120000;
  const pending = plain(panel.operationLine(operation('labels', {started_at: started,
    step: {name: 'Indexing message labels', unit: 'conversation', done: 1200, total: null},
  }), now));
  assert.deepEqual(pending.primary, ['1,200 conversations processed']);
  assert.ok(pending.secondary.includes('Indexing message labels'));
  assert.ok(pending.secondary.includes('2 min elapsed'));
  assert.equal(pending.fraction, null);
  assert.equal(panel.stepAmount({unit: 'byte', done: 4200, total: null}), '4.2 KB processed');
  assert.equal(panel.stepAmount({unit: 'conversation', done: 0, total: 0}), '0 of 0 conversations');
  const starting = plain(panel.operationLine(operation('new', {verb: 'Finding work'})));
  assert.equal(starting.progressLabel, 'Finding work');
  assert.ok(starting.secondary.includes('Finding work'));
});

test("all operation kinds show their active child's progress and actual wait state", () => {
  const child = operation('child', {kind: 'analysis.git', label: 'Check repositories', state: 'blocked',
    state_label: 'Waiting', detail: 'Waiting for the catalog writer',
    step: {name: 'Checking Git history', unit: 'repository', done: 2, total: 5, fraction: .4}});
  const parent = operation('parent', {step: {index: 2, count: 4, name: 'Analysis'}, children: [child]});
  const shown = plain(panel.operationLine(parent));
  assert.equal(shown.label, parent.label);
  assert.equal(shown.currentLabel, child.label);
  assert.deepEqual(shown.primary, ['Waiting', 'step 2 of 4', '2 of 5 repositories']);
  assert.ok(shown.secondary.includes('Waiting for the catalog writer'));
  assert.equal(shown.fraction, .4);
  assert.equal(shown.waiting, true);
  assert.equal(plain(panel.operationLine({...parent, state: 'stopping', state_label: 'Stopping…'})).state, 'stopping');
  child.step = null;
  assert.equal(plain(panel.operationLine(parent)).fraction, null, 'an unmeasured child never inherits an old parent percentage');

  // An ancestor can wait on its own write while its child remains live.
  // Keep the child's measurements and the actual blocking ancestor's state.
  child.step = {name: 'Checking Git history', unit: 'repository', done: 2, total: 5, fraction: .4};
  child.state = 'running';
  child.state_label = 'Checking repositories';
  child.detail = null;
  const blocked = {...parent, state: 'blocked', state_label: 'Waiting', detail: 'Waiting for the catalog writer',
    time: {label: 'about 3 min'}};
  for (const ancestor of [blocked, {...parent, children: [{...blocked, id: 'chain', children: [child]}]}]) {
    const line = plain(panel.operationLine(ancestor));
    assert.equal(line.state, 'blocked');
    assert.equal(line.waiting, true);
    assert.ok(line.primary.includes('Waiting'));
    assert.ok(line.secondary.includes('Waiting for the catalog writer'));
    assert.equal(line.currentLabel, child.label);
    assert.equal(line.fraction, .4);
    assert.equal(line.time, '', 'a blocked ancestor must not keep showing a running countdown');
  }
  assert.equal(plain(panel.operationLine({...blocked, children: [{...child, state: 'stopping', state_label: 'Stopping…'}]})).state, 'stopping');
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

test("the load row shows CPU, RAM and disk for the library or the app process tree", () => {
  const library = { label: "Library line", cpu_percent: 12, memory_bytes: 340e6, disk_bytes_per_second: 12e6, processes: [{ pid: 1, text: "Service line" }, { pid: 2, text: "Terminal line" }] };
  const own = plain(model(view({ resources: library, library: { drive: { name: "Euclid", ejectable: true }, path: "/Volumes/Euclid/Pharos", storage_bytes: 48e9, wal_bytes: 64e6 } })).load);
  assert.deepEqual(own.metrics.map(metric => [metric.id, metric.label, metric.value]), [["cpu", "CPU", "12%"], ["memory", "RAM", "340 MB"], ["io", "I/O", "12 MB/s"], ["disk", "Storage", "48 GB"]]);
  // The broker's total, when the store has one, wins over the library's.
  const app = plain(model(view({ resources: library }, { resources: { label: "Machine line", cpu_percent: 337, memory_bytes: 8.2e9, disk_bytes_per_second: null, processes: [] } })).load);
  assert.deepEqual(app.metrics.map(metric => metric.value), ["337%", "8.2 GB", "—", "—"], "a figure not measured yet is a dash");
  // A service that does not measure I/O: no I/O.
  assert.deepEqual(plain(model(view({ resources: { cpu_percent: 45.4, memory_bytes: 1.2e9 } })).load.metrics).map(metric => metric.id), ["cpu", "memory", "disk"]);
  assert.equal(model(view({ resources: {} })).load, null);
});

test("the broker load remains visible during a service timeout, and partial rates disclose undercounting", () => {
  const unavailable = model({library: null, service: {name: "Euclid", error: "timeout", resources: {memory_bytes: 1e9}},
    resources: {cpu_percent: 12, cpu_partial: true, memory_bytes: 2e9, disk_bytes_per_second: 4e6, disk_partial: true, label: "Pharos is using resources"},
    lastLibrarySizes: {storage: 48e9, wal: 64e6}, appOperations: [], attention: []});
  assert.equal(unavailable.available, false);
  assert.deepEqual(unavailable.load.metrics.map(metric => metric.value), ["≥12%", "2.0 GB", "≥4.0 MB/s", "last 48 GB"]);
  for (const id of ["cpu", "io"]) assert.match(unavailable.load.metrics.find(metric => metric.id === id).warning, /waiting for another sample.*may undercount/);
  assert.equal(unavailable.load.metrics.find(metric => metric.id === "memory").warning, undefined);
  // A process whose memory could not be read makes the total a lower bound;
  // memory nobody could read is a dash, not zero.
  const memory = resources => model(view({ resources })).load.metrics.find(metric => metric.id === "memory");
  const partial = memory({ cpu_percent: 1, memory_bytes: 2e9, memory_partial: true });
  assert.equal(partial.value, "≥2.0 GB");
  assert.match(partial.warning, /memory could not be read.*may undercount/);
  assert.equal(memory({ cpu_percent: 1, memory_bytes: null, memory_partial: true }).value, "—");
});

test("GPU load is scoped to Pharos processes, including idle and unavailable states", () => {
  const load = gpu => model(view({ resources: { cpu_percent: 12, gpu } })).load;
  for (const [percent, value] of [[62.4, "62%"], [0, "0%"], [null, "—"]]) for (const partial of [false, true]) {
    const result = load({ utilization_percent: percent, partial });
    const warming = partial && percent !== null;
    assert.equal(result.metrics.find(metric => metric.id === "gpu").value, warming ? "≥" + value : value);
    assert.equal(result.metrics.find(metric => metric.id === "gpu").label, "GPU");
    if (warming) assert.match(result.metrics.find(metric => metric.id === "gpu").warning, /may undercount/);
    else assert.equal(result.metrics.find(metric => metric.id === "gpu").warning, undefined);
  }
  assert.ok(!load(undefined).metrics.some(metric => metric.id === "gpu"));
  const app = model(view({ resources: { cpu_percent: 12, gpu: { utilization_percent: 10 } } },
    { resources: { cpu_percent: 20, gpu: { utilization_percent: 60 } } })).load;
  assert.equal(app.metrics.find(metric => metric.id === "gpu").value, "60%", "use the broker process-tree reading");
});


test("WAL appears only above one decimal GB, including a last known size", () => {
  const load = (wal, stale = false) => model(stale
    ? {library: null, resources: {cpu_percent: 1}, lastLibrarySizes: {storage: 48e9, wal}, appOperations: [], attention: []}
    : view({resources: {cpu_percent: 1}, library: {wal_bytes: wal}})).load;
  for (const wal of [undefined, null, 0, 64e6, 1e9]) {
    assert.ok(!load(wal).metrics.some(metric => metric.id === "wal"));
  }
  assert.equal(load(1e9 + 1).metrics.find(metric => metric.id === "wal").value, "1.0 GB");
  assert.equal(load(2e9, true).metrics.find(metric => metric.id === "wal").value, "last 2.0 GB");
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
  const disabled = plain(panel.scheduleModel({...background, unscheduled: ['labels'], items: [
    ...background.items, {id: 'labels', label: 'Message labels', state: 'disabled', state_label: 'Off'},
  ]}, at));
  assert.equal(disabled.rows.some(row => row.id === 'manual'), false, 'disabled model settings do not offer work in the drive popover');
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
  const secondary = changes => plain(panel.operationLine(operation("a", changes))).secondary;
  assert.equal(secondary({ trigger: "user" })[0], "Started by you");
  assert.equal(secondary({ trigger: "schedule" })[0], "Scheduled");
  assert.deepEqual(secondary({ trigger: "chained" }), secondary({}));
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
  const classified = operation("rebuild", {kind: "analysis.authorship", label: "Classify your writing",
    step: {name: "classifying authorship", unit: "message", done: 601, total: 42918, fraction: 601 / 42918}});
  const nested = {step: {index: 2, count: 3, name: "Your writing"}, children: [
    operation("upgrade", {kind: "library.upgrade", state: "succeeded"}),
    operation("chain", {kind: "analysis.chain", children: [operation("authorship", {kind: "analysis.authorship", children: [classified]})]}),
  ]};
  const working = pass(nested);
  assert.equal(working.label, classified.label, "the deepest live stage names the work in progress");
  assert.deepEqual(working.primary, ["601 of 42,918 messages"]);
  assert.equal(working.fraction, 601 / 42918);
  assert.deepEqual(working.position, {index: 2, count: 3}, "task position remains the parent's plan");
  nested.children[1].children[0].children = [];
  assert.equal(pass(nested).fraction, null, "a stage without measured progress does not inherit an old task's amount");
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

// Resource history (docs/work-resource-accounting.md): four series with
// their own units on one time axis. Gaps, restarts and missing GPU values
// break a line; nothing is drawn as zero or repeated.
const historyNow = Date.parse("2026-11-07T12:00:00Z");
const ago_ = seconds => historyNow - seconds * 1000;
const history = (changes = {}) => ({
  version: 1, available: true, scope: "this library service", now: historyNow, window_seconds: 900,
  series: { cpu: { available: true, unit: "percent_one_core" }, memory: { available: true, unit: "bytes" }, disk: { available: false, unit: "bytes_per_second" }, gpu: { available: true, unit: "percent_driver_time" } },
  samples: { t: [ago_(60), ago_(58), ago_(56), ago_(20), ago_(18), ago_(16)], cpu: [null, 50, 250, 10, 12, null],
    memory: [1e9, 1e9, 1.2e9, 1e9, 1e9, 1e9], disk: [null, null, null, null, null, null],
    cpu_partial: [false, false, false, true, false, false], disk_partial: [false, false, false, false, false, false], processes: [1, 1, 1, 2, 2, 2] },
  gpu: { t: [ago_(59), ago_(54), ago_(49), ago_(19)], value: [10, 20, null, 30], partial: [false, false, true, false] },
  segments: [{ t: ago_(60), reason: "started" }, { t: ago_(20), reason: "gap" }],
  coverage: [{ t: ago_(60), covered: [{ pid: 1, role: "service" }], omitted: [] },
    { t: ago_(20), covered: [{ pid: 1, role: "service" }, { pid: 2, role: "cli:ingest" }], omitted: ["Pharos Mac app: ownership is not verified, so it is not measured"] }],
  markers: [{ t: ago_(57), event: "start", id: "a", kind: "analysis.labels" }, { t: ago_(17), event: "end", id: "a", kind: "analysis.labels" }],
  ...changes,
});

test("history draws separate series, breaking lines at gaps and missing readings", () => {
  const strip = plain(panel.historyModel(history()));
  assert.deepEqual(strip.series.map(series => series.id), ["cpu", "gpu", "memory"], "I/O that is not measured has no row");
  const series = id => strip.series.find(entry => entry.id === id);
  assert.equal(series("cpu").paths.length, 2, "the gap segment breaks CPU, and a null reading is not drawn");
  assert.equal(series("cpu").current, "—", "the latest reading is missing, so now is a dash");
  assert.equal(series("cpu").peak, "250%", "CPU can exceed one core");
  assert.equal(series("memory").paths.length, 2);
  assert.equal(series("memory").current, "1.0 GB");
  // GPU keeps its own sample times: its first point is at its own time, and
  // the null value splits it, leaving a lone sample as a short dash.
  const gpu = series("gpu");
  assert.equal(gpu.paths.length, 2);
  const x = time => Math.round(((time - (historyNow - 900e3)) / 900e3) * strip.width * 10) / 10;
  assert.ok(gpu.paths[0].startsWith(`M${x(ago_(59))} `), gpu.paths[0]);
  const zero = String(strip.height - 1);
  assert.ok(!gpu.paths.some(path => path.split(/[ML]/).filter(Boolean).some(point => point.split(" ")[1] === zero)), "a missing GPU value is never drawn as zero");
  assert.equal(strip.markers.length, 2);
  assert.match(strip.caption, /15 min.*this library service.*2 processes/);
  assert.equal(strip.omitted.length, 1);
  for (const entry of strip.series) assert.ok(strip.summary.includes(entry.summary), "the accessible summary covers every series");
  assert.match(series("cpu").summary, /% of one core/);
  assert.match(series("cpu").summary, /gaps/);
  assert.match(series("cpu").summary, /lower bounds/);

  const runtimeOnly = history();
  runtimeOnly.series.cpu.available = false;
  runtimeOnly.gpu = null;
  runtimeOnly.series.memory = { available: true, basis: "go-runtime", note: "Go runtime-managed memory; not RSS." };
  runtimeOnly.samples.memory_partial = runtimeOnly.samples.t.map(() => true);
  const runtimeStrip = plain(panel.historyModel(runtimeOnly));
  assert.deepEqual(runtimeStrip.series.map(entry => entry.id), ["memory"], "unavailable CPU and GPU do not gain rows");
  assert.match(runtimeStrip.series[0].summary, /Go runtime-managed memory/);
  assert.match(runtimeStrip.series[0].summary, /lower bounds/);
  assert.equal(runtimeStrip.series[0].current, "≥1.0 GB");
  assert.equal(plain(panel.historyInspect(runtimeOnly, ago_(16))).values.find(entry => entry.id === "memory").text, "≥1.0 GB");
  // No history (classic execution, an older service): no strip.
  assert.equal(panel.historyModel({ version: 1, available: false }), null);
  assert.equal(panel.historyModel(null), null);
  assert.equal(panel.historyModel(history({ samples: { t: [] } })), null);
  // Samples outside the window are not drawn.
  assert.equal(plain(panel.historyModel(history({ now: ago_(-900) }))).series.find(entry => entry.id === "cpu").paths.length, 0);
});

test("inspecting the history shows the nearest values and the tasks running then", () => {
  const at = plain(panel.historyInspect(history(), ago_(18.5)));
  assert.equal(at.t, ago_(18));
  assert.deepEqual(at.values.map(value => [value.id, value.text]), [["cpu", "12%"], ["gpu", "30%"], ["memory", "1.0 GB"]]);
  assert.deepEqual(at.tasks, ["analysis.labels"], "running at the same time, not attributed");
  assert.equal(at.covered, 2);
  assert.equal(at.omitted.length, 1);
  const partial = plain(panel.historyInspect(history(), ago_(20)));
  assert.equal(partial.values[0].text, "≥10%", "a lower bound says so");
  const ended = plain(panel.historyInspect(history(), ago_(16)));
  assert.deepEqual(ended.tasks, []);
  assert.equal(ended.values[0].text, "—");
  const far = plain(panel.historyInspect(history(), ago_(500)));
  assert.ok(far.values.every(value => value.text === "—"), "no sample nearby is unknown");
  assert.equal(panel.historyInspect({ available: false }, historyNow), null);
});

test("the strip appears only beside a Load row, in the same scope", () => {
  const withLoad = model(view({ resources: { cpu_percent: 12, memory_bytes: 1e9 } }), { history: history() });
  assert.ok(withLoad.load && withLoad.history);
  assert.equal(withLoad.history.scope, "this library service");
  assert.equal(model(view({ resources: { cpu_percent: 12 } })).history, null, "no document, no strip");
  const unavailable = model({ library: null, service: { name: "Euclid", resources: null }, resources: null, appOperations: [], attention: [] }, { history: history() });
  assert.equal(unavailable.history, null);
});

test("a task's resources keep basis and allocation apart, and never guess", () => {
  assert.equal(panel.taskResourceModel(null), null);
  assert.equal(panel.taskResourceModel({}), null);
  const resources = {
    version: 1,
    allocation: { current: { cpu_slots: 4, gpu: 0 }, slot_seconds: { value: 40, unit: "slot_seconds", basis: "allocated" } },
    time: { active: { value: 80, unit: "seconds", basis: "measured" } },
    waits: { capacity: { value: 3, unit: "seconds", basis: "measured" }, writer: { value: 1, unit: "seconds", basis: "measured" }, power: { value: 0, unit: "seconds", basis: "measured" } },
    cpu: { go_process: { value: null, unit: "cpu_seconds", basis: "shared", coverage: "Shared service process." },
      helpers: { value: 2.5, unit: "cpu_seconds", basis: "measured", coverage: "Dedicated helpers." } },
    gpu: { time: { value: null, unit: "gpu_seconds", basis: "unavailable" } },
    memory: { owned_buffers_peak_bytes: { value: 64e6, unit: "bytes", basis: "counted" }, process: { value: null, unit: "bytes", basis: "shared", coverage: "Shared heap." } },
    io: { logical_read_bytes: { value: 12e6, unit: "bytes", basis: "counted", coverage: "Instrumented logical bytes only; coverage is partial." }, logical_written_bytes: { value: 0, unit: "bytes", basis: "counted" } },
  };
  const shown = plain(panel.taskResourceModel(resources));
  const item = id => shown.items.find(entry => entry.id === id);
  // The grant is an allowance, not usage, and leads to where it is set.
  assert.match(item("allocation").text, /4 CPU slots/);
  assert.match(item("allocation").text, /not usage/);
  assert.equal(item("allocation").link, "/settings/sync");
  assert.match(item("active").text, /1 min/);
  // Waits: the total and each kind's time, longest first, in words rather
  // than wait keys; a zero wait is left out.
  assert.match(item("waits").text, /4 s/);
  assert.ok(item("waits").text.indexOf("3 s") < item("waits").text.indexOf("1 s"));
  // Every CPU, memory, I/O and GPU figure carries its basis in its text.
  assert.match(item("cpu").text, /2\.5 s measured/);
  assert.doesNotMatch(item("cpu").text, /partial/, "full coverage is not a lower bound");
  assert.match(item("service").text, /shared/, "shared service CPU and memory show their basis, not a number");
  assert.doesNotMatch(item("service").text, /\d/);
  assert.match(item("memory").text, /64 MB counted/);
  assert.match(item("io").text, /12 MB.*counted, partial/, "instrumented bytes are a lower bound and say so");
  for (const direction of ["logical_read_bytes", "logical_written_bytes"]) {
    const unknown = plain(panel.taskResourceModel({...resources, io: {...resources.io, [direction]: {value: null, unit: "bytes", basis: "unavailable"}}}));
    const io = unknown.items.find(entry => entry.id === "io");
    const label = direction === "logical_read_bytes" ? "read" : "wrote";
    assert.match(io.text, new RegExp(`${label} not measured`));
    assert.doesNotMatch(io.text, new RegExp(`${label} 0 B`), "a missing counter cannot become measured zero");
  }
  assert.equal(item("gpu"), undefined, "an unavailable GPU figure without a GPU grant is left out, not shown as zero");
  assert.match(shown.tooltip, /Shared service process/);
  assert.match(shown.tooltip, /Dedicated helpers/);
  // A lower bound from the helpers, and a GPU grant without a timing.
  const partial = plain(panel.taskResourceModel({ ...resources, allocation: { current: { cpu_slots: 0, gpu: 1 } },
    cpu: { helpers: { value: 1, unit: "cpu_seconds", basis: "measured", coverage: "Some helper intervals were shared or unreadable; this is a lower bound." } } }));
  assert.match(partial.items.find(entry => entry.id === "cpu").text, /measured, partial/);
  assert.match(partial.items.find(entry => entry.id === "gpu").text, /not measured/);
  assert.match(partial.items.find(entry => entry.id === "allocation").text, /GPU/);
  const line = plain(panel.operationLine(operation("r", { kind: "analysis.labels", resources })));
  assert.equal(line.resources.text, shown.text);
  assert.equal(line.export.path, "/api/operations/resources?kind=analysis.labels");
  // Without a record (classic execution, an older service): no line, no export.
  assert.equal(plain(panel.operationLine(operation("s"))).resources, null);
  assert.equal(plain(panel.operationLine(operation("s"))).export, null);
});

test("the resource exports and profile belong to the library service's own tasks", () => {
  const resources = { version: 1, time: { active: { value: 5, unit: "seconds", basis: "measured" } } };
  const library = model(view({ operations: [operation("lib", { kind: "analysis.labels", resources })] }));
  assert.ok(library.lines[0].export);
  assert.match(library.profile.path, /^\/api\/diagnostics\/profile\?seconds=5$/);
  // The app's own work is not in the service's ledger, and the profile is
  // offered only beside a service that keeps task records.
  const app = model(view({ operations: [] }, { appOperations: [operation("app", { kind: "library.transfer", resources })] }));
  assert.equal(app.lines[0].export, null);
  assert.equal(app.profile, null);
  assert.equal(model(view({ operations: [operation("old", { kind: "analysis.labels" })] })).profile, null, "an older service gets no profile link");
});

test("history markers tell starts from ends, name their task and are honest about observed ends", () => {
  const doc = history({ markers: [
    { t: ago_(57), event: "start", id: "a", kind: "analysis.labels" },
    { t: ago_(18), event: "end", id: "a", kind: "analysis.labels", observed: true },
    { t: ago_(19), event: "start", id: "b", kind: "index.search" },
    { t: ago_(17), event: "end", id: "b", kind: "index.search", observed: false },
  ] });
  const strip = plain(panel.historyModel(doc, { labels: { "analysis.labels": "Message labels" } }));
  assert.deepEqual(strip.markers.map(marker => marker.event), ["start", "end", "start", "end"]);
  const [start, observed, , exact] = strip.markers;
  assert.match(start.text, /Message labels/, "a known kind uses its name");
  assert.match(exact.text, /index\.search/, "an unknown kind keeps its identity");
  // An observed end happened after the previous sample; an exact end has one time.
  assert.match(observed.text, /between/);
  assert.doesNotMatch(exact.text, /between/);
  assert.notEqual(start.text.replace(/Message labels/, ""), exact.text.replace(/index\.search/, ""), "starts and ends read differently");
  assert.match(strip.summary, /2 starts, 2 ends/);
  // Inspecting near a marker names it; far from any it names none.
  const near = plain(panel.historyInspect(doc, ago_(18), { labels: { "analysis.labels": "Message labels" } }));
  assert.ok(near.marks.some(mark => /Message labels/.test(mark)));
  assert.ok(near.text.includes(near.marks[0]));
  assert.deepEqual(plain(panel.historyInspect(doc, ago_(300))).marks, []);
  // Running tasks keep their kinds; the readout uses names.
  const running = plain(panel.historyInspect(doc, ago_(40), { labels: { "analysis.labels": "Message labels" } }));
  assert.deepEqual(running.tasks, ["analysis.labels"]);
  assert.match(running.text, /Message labels/);
  assert.doesNotMatch(running.text, /analysis\.labels/, "a named kind is not shown by its key");
});

test("task names for markers come from the snapshot's operations and their stages", () => {
  const labels = plain(panel.operationLabels(view({ operations: [operation("p", { kind: "background.update", label: "Background update",
    children: [operation("c", { kind: "analysis.labels", label: "Message labels" })] })] })));
  assert.equal(labels["analysis.labels"], "Message labels");
  assert.equal(labels["background.update"], "Background update");
  // Names seen earlier are kept when a later snapshot no longer has them.
  const kept = panel.operationLabels(view({ operations: [] }), { "analysis.labels": "Message labels" });
  assert.equal(kept["analysis.labels"], "Message labels");
});

const store = loadClassicScript("status-store.js").window.pharosStatusStore;

test("history fetched with since= appends without repeating a time and drops what left the window", () => {
  const N = historyNow;
  const columns = (t, cpu) => ({ t, cpu, memory: t.map(() => 1e9), disk: t.map(() => null), cpu_partial: t.map(() => false), disk_partial: t.map(() => false), memory_partial: t.map(() => false), processes: t.map(() => 1) });
  const previous = { version: 1, available: true, library: "", now: N, window_seconds: 60, samples: columns([N - 50e3, N - 10e3], [1, 2]),
    gpu: { t: [N - 45e3], value: [5], partial: [false] }, segments: [{ t: N - 50e3, reason: "started" }],
    coverage: [{ t: N - 50e3, covered: [{ pid: 1 }], omitted: [] }], markers: [{ t: N - 55e3, event: "start", id: "old", kind: "k" }] };
  const next = { version: 1, available: true, library: "", since: N - 10e3, now: N + 20e3, window_seconds: 60,
    samples: columns([N - 10e3, N + 5e3], [99, 3]), gpu: { t: [N + 2e3], value: [7], partial: [false] }, segments: [], coverage: [],
    markers: [{ t: N + 1e3, event: "start", id: "x", kind: "k" }] };
  previous.samples.memory_partial[1] = true;
  const merged = plain(store.mergeHistory(previous, next));
  assert.deepEqual(merged.samples.t, [N - 10e3, N + 5e3]);
  assert.deepEqual(merged.samples.cpu, [2, 3], "a repeated time keeps the first reading");
  assert.deepEqual(merged.samples.memory_partial, [true, false], "incremental history preserves memory coverage");
  assert.deepEqual(merged.gpu.t, [N + 2e3], "GPU samples merge on their own times");
  assert.deepEqual(merged.segments.map(segment => segment.reason), ["started"], "the segment in effect at the window's start stays");
  assert.equal(merged.coverage.length, 1);
  assert.deepEqual(merged.markers.map(marker => marker.id), ["x"]);
  assert.equal(merged.now, N + 20e3);
  const late = {t: N - 20e3, event: "start", id: "late", kind: "k"};
  const complete = plain(store.mergeHistory(previous, {...next, markers_complete: true, markers: [late]}));
  assert.deepEqual(complete.markers, [late], "complete marker snapshots include late starts and retire corrected/removed markers");
  assert.deepEqual(complete.samples.cpu, [2, 3], "only samples still use the incremental cursor");
  // A full document, an unavailable one or another scope replaces.
  assert.equal(store.mergeHistory(previous, { ...next, since: undefined }).samples.cpu[0], 99);
  assert.equal(store.mergeHistory(previous, { version: 1, available: false }).available, false);
  assert.equal(store.mergeHistory(previous, { ...next, library: "other" }).library, "other");
  assert.equal(store.mergeHistory(null, next), next);
  assert.equal(store.historyEndpoint(true), "/api/app/resources/history");
  assert.equal(store.historyEndpoint(false), "/api/resources/history");
});

test("the store fetches history only while the panel shows it", async () => {
  let clock = historyNow, sampled = historyNow;
  const requests = [];
  const fetch = async url => {
    requests.push(url);
    if (url.startsWith("/api/resources/history")) return { ok: true, status: 200, json: async () => history({ since: url.includes("since=") ? 1 : undefined }) };
    return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ resources: { sampled_at: new Date(sampled).toISOString() } }) };
  };
  const settle = () => new Promise(resolve => setImmediate(resolve));
  const live = store.createStore({ EventSource: null, fetch, setTimeout: () => 0, clearTimeout() {}, hidden: () => false, now: () => clock });
  live.start();
  await settle();
  assert.ok(!requests.some(url => url.includes("history")), "no history while the panel is closed");
  live.openHistory();
  await settle();
  assert.equal(live.current().history.available, true);
  // A newer reading brings one since= fetch, no sooner than HISTORY_MS.
  clock += store.HISTORY_MS;
  sampled += 2000;
  await live.refresh();
  await settle();
  assert.ok(requests.at(-1).startsWith(`/api/resources/history?since=${history().samples.t.at(-1)}`), requests.at(-1));
  live.closeHistory();
  assert.equal(live.current().history, null);
  const before = requests.length;
  clock += store.HISTORY_MS;
  sampled += 2000;
  await live.refresh();
  await settle();
  assert.ok(!requests.slice(before).some(url => url.includes("history")));
  live.stop();
});

test("without a collector the store asks for history once until the panel is reopened", async () => {
  for (const answer of [
    { ok: true, status: 200, json: async () => ({ version: 1, available: false, reason: "No resource history is collected in this process." }) },
    { ok: false, status: 404, json: async () => ({}) },
  ]) {
    let clock = historyNow, sampled = historyNow;
    const requests = [];
    const fetch = async url => {
      if (url.startsWith("/api/resources/history")) { requests.push(url); return answer; }
      return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ resources: { sampled_at: new Date(sampled).toISOString() } }) };
    };
    const settle = () => new Promise(resolve => setImmediate(resolve));
    const live = store.createStore({ EventSource: null, fetch, setTimeout: () => 0, clearTimeout() {}, hidden: () => false, now: () => clock });
    live.start();
    live.openHistory();
    await settle();
    assert.equal(live.current().history.available, false);
    for (let i = 0; i < 3; i++) {
      clock += store.HISTORY_MS;
      sampled += 2000;
      await live.refresh();
      await settle();
    }
    assert.equal(requests.length, 1, `newer snapshots do not ask again: ${requests}`);
    live.closeHistory();
    live.openHistory();
    await settle();
    assert.equal(requests.length, 2, "reopening the panel asks once more");
    live.stop();
  }
});

test("a collector without this scope yet is asked again", async () => {
  let clock = historyNow, sampled = historyNow;
  const requests = [];
  const fetch = async url => {
    if (url.startsWith("/api/resources/history")) { requests.push(url); return { ok: true, status: 200, json: async () => ({ version: 1, available: false, pending: true }) }; }
    return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({ resources: { sampled_at: new Date(sampled).toISOString() } }) };
  };
  const settle = () => new Promise(resolve => setImmediate(resolve));
  const live = store.createStore({ EventSource: null, fetch, setTimeout: () => 0, clearTimeout() {}, hidden: () => false, now: () => clock });
  live.start();
  live.openHistory();
  await settle();
  clock += store.HISTORY_MS;
  sampled += 2000;
  await live.refresh();
  await settle();
  assert.equal(requests.length, 2);
  live.stop();
});
