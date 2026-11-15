import { test } from "node:test";
import assert from "node:assert/strict";
import { loadClassicScript, plain } from "../helpers/classic-script.mjs";
import { appLocation, appLocationLabel, appSections } from "../../../src/app-navigation-model";
import { formatElapsed, logSourceFromParams, logSources, sourcesToRead, withLogSources } from "../../../src/logs-model";

const settings = [{ id: "/settings", href: "/settings", label: "Library health" }, { id: "/settings/logs", href: "/settings/logs", label: "Logs" }];

test("Settings → Logs lists its sources and marks the ones with something to read", () => {
  const pages = withLogSources(settings, { crashes: "error", page: "warning" });
  const logs = pages[1];
  assert.deepEqual(logs.children.map(page => page.href), logSources.map(source => source.id === "overview" ? "/settings/logs" : `/settings/logs?log_source=${source.id}`));
  assert.equal(logs.attention, "error", "the group carries its most severe mark, so a collapsed group still shows it");
  assert.deepEqual(logs.children.filter(page => page.attention).map(page => [page.id, page.attention]), [["logs:page", "warning"], ["logs:crashes", "error"]]);
  assert.equal(withLogSources(settings)[1].attention, undefined);
  assert.equal(pages[0], settings[0], "other settings pages are left alone");

  const sections = appSections(pages, new URLSearchParams());
  for (const [query, label] of [["log_source=crashes", "Crash reports"], ["", "Overview"], ["log_source=unknown", "Overview"]]) {
    const location = appLocation("/settings/logs", new URLSearchParams(query));
    assert.equal(location.section, "settings");
    assert.equal(appLocationLabel(sections, location).page.label, label);
  }
  assert.equal(logSourceFromParams(new URLSearchParams("log_source=requests")), "requests");
});

test("the overview reads errors first, then warnings, newest first", () => {
  const summary = { generated_at: "", folder: "", sources: [
    { id: "page", title: "Page errors", attention: "warning", count: 1, latest_at: "2026-11-02T10:00:00.000Z" },
    { id: "requests", title: "Requests", count: 0 },
    { id: "service", title: "Service output", attention: "error", count: 3, latest_at: "2026-11-01T10:00:00.000Z" },
    { id: "crashes", title: "Crash reports", attention: "error", count: 1, latest_at: "2026-11-02T09:00:00.000Z" },
  ] };
  assert.deepEqual(sourcesToRead(summary).map(source => source.id), ["crashes", "service", "page"]);
  assert.deepEqual([formatElapsed(250), formatElapsed(4200), formatElapsed(125_000)], ["250 ms", "4.2 s", "2 min 5 s"]);
});

function loadReporter(answer) {
  const sent = [], timers = [];
  const fetch = async (input, init) => {
    if (String(input) === "/api/logs/page") { sent.push(...JSON.parse(init.body).events); return { status: 200 }; }
    return answer(input, init);
  };
  const script = loadClassicScript("page-errors.js", {
    fetch, location: new URL("http://pharos.test/analyze?x=1"),
    setTimeout: (callback, delay) => { timers.push({ callback, delay }); return timers.length; }, clearTimeout: id => { if (timers[id - 1]) timers[id - 1].cleared = true; },
  });
  const run = delay => { for (const timer of timers) if (timer.delay === delay && !timer.cleared && !timer.ran) { timer.ran = true; timer.callback(); } };
  return { script, sent, run };
}

test("the page reports failed and stalled requests once each, and never its own reports", async () => {
  const { script, sent, run } = loadReporter(async input => {
    if (String(input).includes("broken")) return { status: 500 };
    if (String(input).includes("offline")) throw new TypeError("Load failed");
    return { status: 200 };
  });
  const fetch = script.window.fetch;
  await fetch("/api/ok");
  await fetch("/api/broken?q=1");
  await fetch("/api/broken?q=1");
  await assert.rejects(fetch("/api/offline", { method: "post" }));
  await fetch("/api/logs/summary");
  run(15000); // nothing is still pending
  run(1000);
  assert.deepEqual(plain(sent).map(event => [event.kind, event.message, event.page]), [
    ["request", "GET /api/broken?q=1 returned 500", "/analyze?x=1"],
    ["request", "POST /api/offline failed: TypeError: Load failed", "/analyze?x=1"],
  ]);

  const stalled = loadReporter(() => new Promise(() => {}));
  void stalled.script.window.fetch("/api/slow");
  void stalled.script.window.fetch("/api/action", { method: "POST" });
  void stalled.script.window.fetch("/api/query/usage/rows", { method: "POST" });
  stalled.run(15000);
  stalled.run(1000);
  assert.deepEqual(plain(stalled.sent).map(event => event.message), ["GET /api/slow had not answered after 15 s", "POST /api/query/usage/rows had not answered after 15 s"],
    "only reads stall, including Query Table's POST queries; actions may run long");
});

test("the page reports uncaught errors, scripts that do not load, and what the app saw", () => {
  const { script, sent, run } = loadReporter(async () => ({ status: 200 }));
  const failure = new Error("x is undefined");
  failure.name = "TypeError";
  script.window.dispatchEvent({ type: "error", target: script.window, error: failure, message: "x is undefined" });
  script.window.dispatchEvent({ type: "error", target: { tagName: "SCRIPT", src: "http://pharos.test/assets/query-tables.js" } });
  script.window.dispatchEvent({ type: "error", target: { tagName: "IMG", src: "http://pharos.test/favicon.png" } });
  script.window.dispatchEvent({ type: "unhandledrejection", reason: { name: "AbortError", message: "aborted" } });
  script.window.pharosReportEvents([{ kind: "webkit", message: "The page's web process stopped unexpectedly", page: "/library" }]);
  run(1000);
  assert.deepEqual(plain(sent).map(event => [event.kind, event.message]), [
    ["error", "TypeError: x is undefined"],
    ["resource", "Could not load /assets/query-tables.js"],
    ["webkit", "The page's web process stopped unexpectedly"],
  ]);
  assert.equal(sent[2].page, "/library", "the app's report keeps the page it failed on");
});

test("a failing render reports at most a bounded number of distinct events", () => {
  const { createPageErrorQueue } = loadReporter(async () => ({ status: 200 })).script;
  const batches = [];
  let flush;
  const queue = createPageErrorQueue(events => batches.push(events), next => { flush = next; }, 3);
  for (let i = 0; i < 10; i++) queue.report({ kind: "render", message: `failure ${i % 5}` });
  flush();
  assert.deepEqual(plain(batches).flat().map(event => event.message), ["failure 0", "failure 1", "failure 2"]);
});

test("a burst of distinct errors is sent in batches the service records whole", () => {
  const { createPageErrorQueue } = loadReporter(async () => ({ status: 200 })).script;
  const batches = [];
  let flush;
  const queue = createPageErrorQueue(events => batches.push(events), next => { flush = next; });
  for (let i = 0; i < 40; i++) queue.report({ kind: "error", message: `failure ${i}` });
  flush();
  assert.deepEqual(plain(batches).map(batch => batch.length), [20, 10], "30 accepted, at most 20 per request (recordPageEvents' limit)");
});
