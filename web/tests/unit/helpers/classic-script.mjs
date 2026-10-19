// Loads one of the page's classic scripts (internal/archive/assets/*.js) into
// a node:vm context with just enough browser around it to run its load-time
// code: a window that is the global object, a document whose lookups find
// nothing, an in-memory localStorage and pharosPrefs, and fetch and timers
// that never fire. Tests then call the functions the script defines.
//
//   const { conversationTokenReports } = loadClassicScript("reader-model.js");
//
// The result resolves any global the script declared, including top-level
// const/let bindings, which are not properties of the global object.
// Values made inside the script come from its own realm (its own Array and
// Object), which node:assert's strict deep equality rejects; compare
// plain(value) instead.
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import vm from "node:vm";

// Tests run from a bundle under web/.test-build, so find the assets by walking up.
function assetsDirectory() {
  const start = dirname(fileURLToPath(import.meta.url));
  for (let directory = start; ; directory = dirname(directory)) {
    const assets = join(directory, "internal", "archive", "assets");
    if (existsSync(join(assets, "ui.py"))) return assets;
    if (dirname(directory) === directory) throw new Error(`internal/archive/assets not found above ${start}`);
  }
}
export const assets = assetsDirectory();

function storage() {
  const values = new Map();
  return {
    getItem: key => values.has(String(key)) ? values.get(String(key)) : null,
    setItem: (key, value) => { values.set(String(key), String(value)); },
    removeItem: key => { values.delete(String(key)); },
    clear: () => values.clear(),
    key: index => [...values.keys()][index] ?? null,
    get length() { return values.size; },
  };
}

// An element that accepts what load-time code does to one (style sheets
// appended to head, listeners) and finds nothing.
function element(tag = "div") {
  const self = {
    tagName: String(tag).toUpperCase(), children: [], dataset: {}, style: { setProperty() {}, removeProperty() {} },
    classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
    append(...nodes) { self.children.push(...nodes); }, appendChild(node) { self.children.push(node); return node; },
    prepend() {}, remove() {}, replaceChildren(...nodes) { self.children = nodes; },
    setAttribute() {}, getAttribute: () => null, removeAttribute() {}, hasAttribute: () => false,
    addEventListener() {}, removeEventListener() {}, dispatchEvent: () => true,
    querySelector: () => null, querySelectorAll: () => [], closest: () => null, contains: () => false, focus() {},
  };
  return self;
}

class Event {
  constructor(type, options = {}) { this.type = type; this.bubbles = Boolean(options.bubbles); }
}
class CustomEvent extends Event {
  constructor(type, options = {}) { super(type, options); this.detail = options.detail ?? null; }
}

// Returns the script's globals. `globals` adds to or replaces the stubs
// (e.g. a fetch that answers, or a document that finds an element).
export function loadClassicScript(name, globals = {}) {
  const source = readFileSync(join(assets, name), "utf8");
  const listeners = new Map(), events = [];
  const preferences = new Map();
  const document = {
    head: element("head"), body: element("body"), documentElement: element("html"), hidden: false, activeElement: null,
    createElement: element, createElementNS: (_, tag) => element(tag), createTextNode: text => ({ textContent: String(text) }),
    createDocumentFragment: () => element("fragment"),
    getElementById: () => null, querySelector: () => null, querySelectorAll: () => [],
    addEventListener() {}, removeEventListener() {},
  };
  const sandbox = {
    console, URL, URLSearchParams, TextEncoder, TextDecoder, AbortController, structuredClone, queueMicrotask,
    Event, CustomEvent, document, localStorage: storage(), sessionStorage: storage(),
    location: new URL("http://pharos.test/"), history: { pushState() {}, replaceState() {} },
    navigator: { userAgent: "node", clipboard: { writeText: async () => {} } },
    innerWidth: 1280, innerHeight: 900, devicePixelRatio: 1,
    // Load-time polling stays idle: requests never settle and timers never fire.
    fetch: () => new Promise(() => {}),
    setTimeout: () => 0, clearTimeout() {}, setInterval: () => 0, clearInterval() {},
    requestAnimationFrame: () => 0, cancelAnimationFrame() {},
    matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
    addEventListener(type, listener) { if (!listeners.has(type)) listeners.set(type, []); listeners.get(type).push(listener); },
    removeEventListener(type, listener) { listeners.set(type, (listeners.get(type) || []).filter(item => item !== listener)); },
    dispatchEvent(event) { events.push(event); for (const listener of listeners.get(event.type) || []) listener(event); return true; },
    pharosPrefs: { get: (key, fallback) => preferences.has(key) ? preferences.get(key) : fallback, set: (key, value) => { preferences.set(key, value); } },
    ...globals,
  };
  sandbox.window = sandbox.self = sandbox;
  const context = vm.createContext(sandbox);
  vm.runInContext(source, context, { filename: join(assets, name) });
  const identifier = /^[A-Za-z_$][\w$]*$/;
  return new Proxy({ window: sandbox, events }, {
    get(target, key) {
      if (key in target) return target[key];
      if (typeof key !== "string" || !identifier.test(key)) return undefined;
      return vm.runInContext(`typeof ${key} === "undefined" ? undefined : ${key}`, context);
    },
  });
}

// Copies a value made inside a loaded script into this realm (Maps, Sets and
// nested values included) so strict deep equality can compare it.
export function plain(value) {
  return structuredClone(value);
}
