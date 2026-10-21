// Shared harness for the structural browser tests.
//
// `open(t, options)` serves the real page shell from ui.py, every file under
// internal/archive/assets and a stubbed API (./fixtures.mjs) from an
// in-process HTTP server, and opens it in a new context of the run's one
// browser (./engine.mjs). Page errors, console errors, failed requests and API
// calls with no stub fail the test when it ends. Without an installed engine
// the tests skip with an install hint.
import assert from "node:assert/strict";
import { existsSync, mkdirSync, readFileSync } from "node:fs";
import { createServer } from "node:http";
import { extname, join, normalize, sep } from "node:path";
import { after } from "node:test";
import { fileURLToPath } from "node:url";
import { connectBrowser, engine, launchPlan } from "./engine.mjs";
import { defaultApi } from "./fixtures.mjs";

export const repo = fileURLToPath(new URL("../../..", import.meta.url));
const assetRoot = join(repo, "internal", "archive", "assets");

// The page shell, extracted once per process the way assets.go does.
const source = readFileSync(join(assetRoot, "ui.py"), "utf8");
export const html = source.slice(source.indexOf("r'''") + 4, source.lastIndexOf("'''"));

// Every file under assets/, by path, read once per process.
const types = { ".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8", ".json": "application/json", ".svg": "image/svg+xml", ".png": "image/png", ".md": "text/markdown; charset=utf-8" };
const assetCache = new Map();
function asset(path) {
  if (!assetCache.has(path)) {
    const file = normalize(join(assetRoot, path));
    assetCache.set(path, file.startsWith(assetRoot + sep) && existsSync(file) ? readFileSync(file) : null);
  }
  return assetCache.get(path);
}

// Every browser test skips, with the install hint, when the engine cannot run.
export const skip = launchPlan().skip ?? false;

// A configured header tool can be inline or in More at the current width.
export async function activateHeaderTool(page, id) {
  const tool = page.locator(`#${id}`);
  if (!await tool.isVisible()) await page.getByRole('button', { name: 'More tools', exact: true }).click();
  await tool.click();
}

let connection;
const browser = () => connection ??= connectBrowser();
after(async () => { if (connection) await (await connection).close(); });

// "METHOD /path/:param/*" -> matcher. `*` matches the rest of the path.
function compileRoute(key) {
  const [method, path] = key.includes(" ") ? key.split(" ") : ["GET", key];
  const names = [];
  const pattern = new RegExp(`^${path.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replace(/\/\*$/, () => (names.push("rest"), "(?:/(.*))?")).replace(/:(\w+)/g, (_, name) => (names.push(name), "([^/]+)"))}$`);
  return { method, match: candidate => { const found = pattern.exec(candidate); return found && Object.fromEntries(names.map((name, i) => [name, found[i + 1] && decodeURIComponent(found[i + 1])])); } };
}
function routeTable(handlers) {
  return Object.entries(handlers).map(([key, handler]) => ({ ...compileRoute(key), handler }));
}

const json = (response, status, body) => {
  response.writeHead(status, { "Content-Type": "application/json" });
  response.end(JSON.stringify(body));
};

// Serves one test's page: the shell for every page route, assets from memory,
// /assets/preferences.js from `state.preferences`, and the API from `routes`.
function serve(routes, state, record) {
  return createServer(async (request, response) => {
    const url = new URL(request.url, "http://localhost");
    const path = url.pathname;
    if (path === "/favicon.ico") return response.writeHead(204).end();
    if (path === "/assets/preferences.js") {
      response.writeHead(200, { "Content-Type": types[".js"], "Cache-Control": "no-store" });
      return response.end(`window.pharosPreferenceValues=${JSON.stringify(state.preferences)};window.pharosPreviewScope="browser-fixture";`);
    }
    if (path.startsWith("/assets/")) {
      const body = asset(path.slice("/assets/".length));
      if (!body) return json(response, 404, { error: "no such asset" });
      response.writeHead(200, { "Content-Type": types[extname(path)] ?? "application/octet-stream" });
      return response.end(body);
    }
    if (!path.startsWith("/api/")) {
      response.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store" });
      return response.end(state.concurrency ? html.replace("<html", "<html data-concurrency-prototype") : html);
    }
    const chunks = [];
    // The context can close mid-request when the test ends; drop that request.
    try { for await (const chunk of request) chunks.push(chunk); } catch { return; }
    const text = Buffer.concat(chunks).toString("utf8");
    let body;
    try { body = text ? JSON.parse(text) : undefined; } catch { body = text; }
    const call = { method: request.method, path, query: url.searchParams, body };
    record(call);
    for (const route of routes) {
      if (route.method !== request.method && route.method !== "*") continue;
      const params = route.match(path);
      if (!params) continue;
      try {
        const result = typeof route.handler === "function" ? await route.handler({ ...call, params, state }) : route.handler;
        // A handler that returns nothing passes the request on.
        if (result !== undefined) return json(response, 200, result);
      } catch (error) {
        state.stubErrors.push(`${request.method} ${path}: ${error.stack}`);
        return json(response, 500, { error: error.message });
      }
    }
    state.unexpected.push(`${request.method} ${path}${url.search}`);
    return json(response, 404, { error: `No stub for ${request.method} ${path}` });
  });
}

const screenshots = process.env.PHAROS_UI_SCREENSHOTS;
let shotCount = 0;

// Opens `url` in a new context at `viewport`, with reduced motion, and waits
// for `ready` (a selector) and `settled()`. Options:
//   api          stubs that take precedence over the defaults,
//                { "GET /api/x/:id": body | ({ params, query, body, state }) => body }
//   concurrency  enable the captured timing snapshot, as the dev UI does
//   preferences  library preferences served at /assets/preferences.js
// Returns the page, `settled`, `shot(name)` (a screenshot, only when
// PHAROS_UI_SCREENSHOTS is set), `problems()` (what will fail the test when
// it ends) and the API `calls` made so far.
export async function open(t, { url = "/", viewport = { width: 1440, height: 900 }, api = {}, preferences = {}, concurrency = false, ready } = {}) {
  const state = { concurrency, preferences: { "pharos-theme": "dark", ...preferences }, unexpected: [], stubErrors: [] };
  const errors = [], failed = [], calls = [], inflight = new Set();
  let wake = () => {};
  const routes = [...routeTable(api), ...routeTable(defaultApi)];
  const server = serve(routes, state, call => calls.push(call));
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  const origin = `http://127.0.0.1:${server.address().port}`;
  const context = await (await browser()).newContext({ viewport, timezoneId: "America/Denver", locale: "en-US", reducedMotion: "reduce" });
  const problems = () => Object.entries({
    "API call with no stub": state.unexpected,
    "stub error": state.stubErrors,
    "page or console error": errors,
    "failed request": failed,
  }).flatMap(([kind, list]) => [...new Set(list)].map(item => `${kind}: ${item}`));
  t.after(async () => {
    // What closing the page cancels is not a problem; take stock first.
    const found = problems();
    await context.close().catch(() => {});
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
    assert.ok(!found.length, `${t.name}\n  ${found.join("\n  ")}`);
  });
  const page = await context.newPage();
  page.setDefaultTimeout(5000);
  // Loading the 1.7 MB bundle in a fresh context is the one slow step on a
  // loaded machine; every wait after it keeps the 5 s limit.
  page.setDefaultNavigationTimeout(20000);
  page.on("pageerror", error => errors.push(`pageerror: ${error.stack || error.message}`));
  page.on("console", message => { if (message.type() === "error") errors.push(`console: ${message.text()} (${message.location().url}:${message.location().lineNumber})`); });
  // A request counts as in flight until its response headers arrive: Chrome
  // reports no "finished" for a body the page never reads.
  const done = request => { inflight.delete(request); wake(); };
  page.on("request", request => inflight.add(request));
  page.on("response", response => {
    const request = response.request();
    if (response.status() >= 400) failed.push(`${response.status()} ${request.method()} ${request.url().replace(origin, "")}`);
    done(request);
  });
  page.on("requestfailed", request => {
    const reason = request.failure()?.errorText ?? "";
    // Navigations and AbortControllers cancel requests on purpose.
    if (!/ERR_ABORTED|cancelled/i.test(reason)) failed.push(`${reason} ${request.method()} ${request.url().replace(origin, "")}`);
    done(request);
  });

  // Resolves once no request is in flight across two animation frames. The
  // Dashboard Catalog previews every card through the fake server, which takes
  // several seconds on a busy machine; the limit only bounds a page that never
  // settles.
  async function settled(timeout = 20000) {
    const end = Date.now() + timeout;
    for (;;) {
      while (inflight.size) {
        if (Date.now() > end) throw new Error(`Requests still in flight: ${[...inflight].map(request => request.url().replace(origin, "")).join(", ")}`);
        await new Promise(resolve => {
          const timer = setTimeout(resolve, Math.max(0, end - Date.now()));
          wake = () => { clearTimeout(timer); resolve(); };
        });
      }
      await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
      if (!inflight.size) return;
    }
  }

  async function shot(name) {
    if (!screenshots) return;
    mkdirSync(screenshots, { recursive: true });
    const file = `${String(++shotCount).padStart(3, "0")}-${engine}-${`${t.name}-${name}`.replace(/[^a-z0-9]+/gi, "-").toLowerCase()}.png`;
    await page.screenshot({ path: join(screenshots, file) });
  }

  await page.goto(origin + url);
  if (ready) await page.locator(ready).first().waitFor();
  await settled();
  return { page, settled, shot, problems, calls };
}
