// status-store.js: the unified activity panel's one copy of the status
// snapshot. It reads the stream, falls back to polling when the stream
// fails, holds nothing open while the page is hidden, and never connects
// while the preference is off (window.pharosStatusStore).
import { test } from "node:test";
import assert from "node:assert/strict";
import { loadClassicScript, plain } from "../helpers/classic-script.mjs";

const FLAG = "pharos-unified-activity-v1";

// A controllable browser: EventSource instances, fetch answers and timers.
function fakeBrowser({ answers = [] } = {}) {
  const sources = [], requests = [], timers = new Map();
  let clock = 0, nextTimer = 1, hidden = false;
  class Source {
    constructor(url) { this.url = url; this.listeners = {}; this.closed = false; sources.push(this); }
    addEventListener(type, listener) { (this.listeners[type] ||= []).push(listener); }
    emit(document) { for (const listener of this.listeners.snapshot || []) listener({ data: JSON.stringify(document) }); }
    fail() { this.onerror?.(new Error("stream failed")); }
    close() { this.closed = true; }
  }
  const fetch = async (url, options = {}) => {
    requests.push({ url, headers: options.headers || {} });
    const answer = answers.length ? answers.shift() : { status: 200, body: { library: {}, operations: [] } };
    return { status: answer.status, ok: answer.status >= 200 && answer.status < 300, headers: { get: name => (answer.headers || {})[name] ?? null }, json: async () => answer.body };
  };
  const browser = {
    EventSource: Source, fetch, now: () => clock, hidden: () => hidden,
    setTimeout: (callback, ms) => { const id = nextTimer++; timers.set(id, { callback, at: clock + ms }); return id; },
    clearTimeout: id => { timers.delete(id); },
  };
  return {
    browser, sources, requests, timers,
    setHidden(value) { hidden = value; },
    // Runs the earliest timer, advancing the clock to it.
    async tick() {
      const [id, timer] = [...timers.entries()].sort((a, b) => a[1].at - b[1].at)[0] || [];
      if (!timer) return false;
      timers.delete(id);
      clock = Math.max(clock, timer.at);
      await timer.callback();
      await new Promise(resolve => setImmediate(resolve));
      return true;
    },
    advance(ms) { clock += ms; },
  };
}
const settle = () => new Promise(resolve => setImmediate(resolve));

test("with the preference off the store never connects", () => {
  let connected = false;
  const script = loadClassicScript("status-store.js", { EventSource: class { constructor() { connected = true; } }, fetch: () => { connected = true; return new Promise(() => {}); } });
  assert.equal(script.window.pharosStatusStore.enabled(), false);
  assert.equal(script.window.pharosStatusStore.current(), null);
  assert.equal(connected, false);
});

test("with the preference on the store reads the stream and tells subscribers", () => {
  const sources = [];
  class Source { constructor(url) { this.url = url; this.listeners = {}; sources.push(this); } addEventListener(type, listener) { (this.listeners[type] ||= []).push(listener); } close() {} }
  const script = loadClassicScript("status-store.js", { EventSource: Source, pharosPrefs: { get: key => key === FLAG ? true : undefined, set() {} } });
  const store = script.window.pharosStatusStore;
  assert.equal(sources.length, 1);
  assert.equal(sources[0].url, "/api/status/stream");
  const seen = [];
  store.subscribe(state => seen.push(state.view?.library?.revision));
  for (const listener of sources[0].listeners.snapshot) listener({ data: JSON.stringify({ revision: 7, attention: [] }) });
  assert.deepEqual(seen, [7]);
  assert.equal(store.current().mode, "stream");
});

test("a failed stream becomes polling every 5 s, with the snapshot's ETag, and tries the stream again later", async () => {
  const { createStore, POLL_MS } = loadClassicScript("status-store.js").window.pharosStatusStore;
  const fake = fakeBrowser({ answers: [{ status: 200, headers: { ETag: '"a"' }, body: { revision: 1, attention: [] } }, { status: 304 }] });
  const store = createStore(fake.browser);
  store.start();
  assert.equal(fake.sources.length, 1);
  fake.sources[0].fail();
  await settle();
  assert.equal(fake.sources[0].closed, true, "the failed stream is closed, not left to reconnect behind the poll");
  assert.equal(fake.requests[0].url, "/api/status");
  assert.equal(store.current().mode, "polling");
  assert.equal(store.current().view.library.revision, 1);
  const [timer] = [...fake.timers.values()];
  assert.equal(timer.at, POLL_MS);
  await fake.tick();
  assert.equal(fake.requests[1].headers["If-None-Match"], '"a"');
  assert.equal(store.current().view.library.revision, 1, "a 304 keeps the document");
  // After 30 s of polling, the stream is tried again.
  for (let index = 0; index < 8 && fake.sources.length === 1; index++) await fake.tick();
  assert.equal(fake.sources.length, 2);
  store.stop();
  assert.equal(fake.sources[1].closed, true);
  assert.equal(fake.timers.size, 0);
});

test("a hidden page holds no stream and polls nothing, and reconnects when shown", async () => {
  const { createStore } = loadClassicScript("status-store.js").window.pharosStatusStore;
  const fake = fakeBrowser();
  const store = createStore(fake.browser);
  store.start();
  fake.setHidden(true);
  store.visibility();
  assert.equal(fake.sources[0].closed, true);
  assert.equal(fake.timers.size, 0);
  assert.equal(store.current().mode, "paused");
  fake.setHidden(false);
  store.visibility();
  assert.equal(fake.sources.length, 2);
  assert.equal(fake.sources[1].closed, false);
  // Starting while hidden waits for the page to be shown.
  const hidden = fakeBrowser();
  hidden.setHidden(true);
  const later = createStore(hidden.browser);
  later.start();
  assert.equal(hidden.sources.length, 0);
});

test("in the app the store reads the broker's document: the selected library, the others and the union of attention", () => {
  const { normalize, endpoints } = loadClassicScript("status-store.js").window.pharosStatusStore;
  assert.deepEqual(plain(endpoints(true)), { stream: "/api/app/status/stream", poll: "/api/app/status" });
  const view = normalize({
    libraries: [
      { id: "a", name: "Euclid", selected: true, connected: true, eject: { verdict: "busy", summary: "broker" }, snapshot: { eject: { verdict: "safe" }, operations: [] } },
      { id: "b", name: "Archive", selected: false, connected: true, eject: { verdict: "safe" }, snapshot: { operations: [{ id: "x", state: "running" }] } },
    ],
    app: { operations: [] }, attention: [{ id: "backup-failed", library: "b" }],
  }, { managed: true });
  assert.equal(view.library.eject.verdict, "busy", "the broker's verdict adds its own operations");
  assert.equal(view.others.length, 1);
  assert.equal(view.others[0].name, "Archive");
  assert.equal(view.attention.length, 1);
  // The broker's own operations: those touching the selected library go with
  // it, the rest with the library they touch, or a row of their own.
  const withApp = normalize({ libraries: [{ id: "a", selected: true, snapshot: { operations: [] } }, { id: "b", name: "Archive", snapshot: null }],
    app: { operations: [{ id: "t", state: "running", libraries: ["a", "b"] }, { id: "c", state: "running", label: "Create a library", libraries: [] }, { id: "old", state: "succeeded", libraries: ["a"] }] }, attention: [] }, { managed: true });
  assert.deepEqual(plain(withApp.appOperations.map(operation => operation.id)), ["t"]);
  assert.deepEqual(plain(withApp.others.map(other => other.operations.map(operation => operation.id))), [["t"], ["c"]]);
  const none = normalize({ libraries: [], app: {}, attention: [] }, { managed: true });
  assert.equal(none.library, null);
  assert.equal(normalize({ revision: 1 }).library.revision, 1);
});

test("the eject check asks for a fresh verdict; in the app only the broker's fresh verdict for the selected library counts", async () => {
  const { createStore, brokerEjectVerdict } = loadClassicScript("status-store.js").window.pharosStatusStore;
  const plainStore = fakeBrowser({ answers: [{ status: 200, body: { eject: { verdict: "safe", checked: "fresh" } } }] });
  assert.equal((await createStore(plainStore.browser).fresh()).eject.verdict, "safe");
  assert.equal(plainStore.requests[0].url, "/api/status?fresh=1");
  const broker = fakeBrowser({ answers: [{ status: 200, body: { libraries: [{ id: "a", selected: true, eject: { verdict: "busy", checked: "fresh", summary: "Delivering history" } }] } }] });
  const managed = createStore({ ...broker.browser, managed: true, selectedId: "a" });
  assert.equal((await managed.fresh()).eject.verdict, "busy");
  assert.equal(broker.requests[0].url, "/api/app/status?fresh=1");
  assert.equal(broker.requests.length, 1, "the library's own verdict is never asked for instead");
  // Fail closed: no answer, another library, or a verdict that is not fresh.
  const failing = fakeBrowser({ answers: [{ status: 503, body: {} }] });
  assert.equal((await createStore({ ...failing.browser, managed: true, selectedId: "a" }).fresh()).eject.verdict, "unsafe");
  assert.equal(brokerEjectVerdict({ libraries: [{ id: "b", selected: true, eject: { verdict: "safe", checked: "fresh" } }] }, "a").verdict, "unsafe");
  assert.equal(brokerEjectVerdict({ libraries: [{ id: "a", selected: true, eject: { verdict: "safe", checked: "polled" } }] }, "a").verdict, "unsafe");
  assert.equal(brokerEjectVerdict(null, "a").verdict, "unsafe");
  assert.ok(brokerEjectVerdict(null, "a").summary);
  assert.equal(brokerEjectVerdict({ libraries: [{ id: "a", selected: true, eject: { verdict: "safe", checked: "fresh" } }] }, "a").verdict, "safe");
});

test("after the stream, a poll compares with what the stream last showed, so it never gets a stale 304", async () => {
  const { createStore } = loadClassicScript("status-store.js").window.pharosStatusStore;
  const fake = fakeBrowser({ answers: [{ status: 200, headers: { ETag: '"a"' }, body: { revision: 1 } }] });
  const store = createStore(fake.browser);
  store.start();
  for (const listener of fake.sources[0].listeners.snapshot) listener({ data: JSON.stringify({ revision: 2 }), lastEventId: '"b"' });
  fake.sources[0].fail();
  await settle();
  assert.equal(fake.requests[0].headers["If-None-Match"], '"b"');
  store.stop();
});

test("a refresh with the stream open is one fetch that schedules nothing", async () => {
  const { createStore } = loadClassicScript("status-store.js").window.pharosStatusStore;
  const fake = fakeBrowser({ answers: [{ status: 200, body: { revision: 5 } }] });
  const store = createStore(fake.browser);
  store.start();
  await store.refresh();
  assert.equal(fake.requests.length, 1);
  assert.equal(fake.timers.size, 0, "a refresh started a polling feed beside the stream");
  assert.equal(store.current().mode, "stream");
  assert.equal(store.current().view.library.revision, 5);
  store.stop();
});

test("a poll in flight while the page is hidden and shown again is dropped: one feed, never two", async () => {
  const { createStore } = loadClassicScript("status-store.js").window.pharosStatusStore;
  let release;
  const fake = fakeBrowser();
  const browser = { ...fake.browser, fetch: url => { fake.requests.push({ url }); return new Promise(resolve => { release = () => resolve({ status: 200, ok: true, headers: { get: () => null }, json: async () => ({ revision: 9 }) }); }); } };
  const store = createStore(browser);
  store.start();
  fake.sources[0].fail(); // polling, request in flight
  fake.setHidden(true);
  store.visibility();
  fake.setHidden(false);
  store.visibility(); // the stream again
  assert.equal(fake.sources.length, 2);
  release();
  await settle();
  await settle();
  assert.equal(fake.timers.size, 0, "the stale poll scheduled another");
  assert.equal(store.current().mode, "stream");
  assert.notEqual(store.current().view?.library?.revision, 9, "the stale poll's answer was taken");
  store.stop();
});
