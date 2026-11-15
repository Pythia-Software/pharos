#!/usr/bin/env node
// Measure what each page costs the browser: main-thread time, heap, DOM size
// and long tasks while it loads, then CPU, style recalculations and requests
// while it sits idle, then a scroll pass. Routes come from the site map.
//
//   node tools/frontend-profile.mjs <signed-in base URL> [route filter] [--out file.json]
//   e.g. node tools/frontend-profile.mjs 'http://127.0.0.1:8799/?token=...' '^(home|cards)$'
//
// Extra routes (a workspace, a long conversation) can be listed in
// PHAROS_PROFILE_ROUTES as JSON: [["name","/work/..."]]. IDLE_MS (8000) and
// SETTLE_CAP (60000) bound the idle window and the wait for requests to stop.
// Needs web/node_modules (tools/setup.sh) and Google Chrome. Timings are local
// and noisy; compare runs taken back to back against the same library.
import { createRequire } from 'node:module';
import fs from 'node:fs';
const ROOT = new URL('..', import.meta.url).pathname;
const require = createRequire(ROOT + 'web/');
const { chromium } = require('playwright-core');
const args = process.argv.slice(2), outAt = args.indexOf('--out');
const out = outAt >= 0 ? args.splice(outAt, 2)[1] : null;
if (!args[0]) { console.error('Usage: node tools/frontend-profile.mjs <signed-in base URL> [route filter] [--out file.json]'); process.exit(2); }
const start = new URL(args[0]), BASE = start.origin;
const filter = args[1] ? new RegExp(args[1]) : null;
const IDLE_MS = Number(process.env.IDLE_MS || 8000);
const SETTLE_CAP = Number(process.env.SETTLE_CAP || 60000);
const map = JSON.parse(fs.readFileSync(ROOT + 'internal/archive/assets/site-map.json', 'utf8'));
let routes = [];
(function walk(nodes) {
  for (const node of Array.isArray(nodes) ? nodes : Object.values(nodes ?? {})) {
    if (!node || typeof node !== 'object') continue;
    if (node.route && !node.route.includes('{')) routes.push([node.path, node.route + (node.fixed ? '?' + new URLSearchParams(node.fixed) : '')]);
    walk(node.children);
  }
})(map.nodes);
routes = routes.concat(JSON.parse(process.env.PHAROS_PROFILE_ROUTES || '[]'));
if (filter) routes = routes.filter(([name]) => filter.test(name));
const browser = await chromium.launch({ channel: 'chrome', headless: true, args: ['--enable-precise-memory-info'] });
const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
await context.addInitScript(() => {
  window.__long = [];
  try {
    new PerformanceObserver(list => { for (const e of list.getEntries()) window.__long.push(Math.round(e.duration)); }).observe({ type: 'longtask', buffered: true });
  } catch {}
});
{
  const p = await context.newPage();
  await p.goto(start.href, { waitUntil: 'domcontentloaded' });
  await p.waitForTimeout(1500);
  await p.close();
}
const bcdp = await browser.newBrowserCDPSession();
const procCpu = async () => { const { processInfo } = await bcdp.send('SystemInfo.getProcessInfo'); const o = { gpu: 0, renderer: 0, browser: 0 }; for (const p of processInfo) if (p.type in o) o[p.type] += p.cpuTime; return o; };
const metrics = async cdp => Object.fromEntries((await cdp.send('Performance.getMetrics')).metrics.map(m => [m.name, m.value]));
const results = [];
for (const [name, route] of routes) {
  const page = await context.newPage();
  const cdp = await context.newCDPSession(page);
  await cdp.send('Performance.enable');
  let inflight = 0, requests = 0, lastActivity = Date.now(), bytes = 0;
  const apiCounts = {};
  const POLL = /^\/api\/(activity|sync\/|library\/status|status|logs|cloud-backup|background|sources|index|capture|health|backup|operations|menu-bar)/;
  const counted = r => { const u = new URL(r.url()); return r.resourceType() !== 'eventsource' && !POLL.test(u.pathname); };
  page.on('request', r => { requests++; const u = new URL(r.url()); if (u.pathname.startsWith('/api/')) apiCounts[u.pathname] = (apiCounts[u.pathname] || 0) + 1; if (counted(r)) { inflight++; lastActivity = Date.now(); } });
  const done = async r => { if (counted(r)) { inflight--; lastActivity = Date.now(); } };
  page.on('requestfinished', done); page.on('requestfailed', done);
  const errors = [];
  page.on('pageerror', e => errors.push(String(e).slice(0, 200)));
  const t0 = Date.now();
  const row = { name, route };
  try {
    await page.goto(BASE + route, { waitUntil: 'domcontentloaded', timeout: 60000 });
    // settle: no in-flight requests for 1.5 s (long-poll/streams excluded by cap)
    while (Date.now() - t0 < SETTLE_CAP) {
      if (inflight <= 0 && Date.now() - lastActivity > 1500) break;
      await page.waitForTimeout(250);
    }
    row.settleMs = Date.now() - t0 - 1500;
    row.inflightAtSettle = inflight;
    const m1 = await metrics(cdp);
    row.load = {
      task: +m1.TaskDuration.toFixed(2), script: +m1.ScriptDuration.toFixed(2), layout: +m1.LayoutDuration.toFixed(2), style: +m1.RecalcStyleDuration.toFixed(2),
      layouts: m1.LayoutCount, styles: m1.RecalcStyleCount, heapMB: +(m1.JSHeapUsedSize / 1048576).toFixed(1), nodes: m1.Nodes, listeners: m1.JSEventListeners, requests,
    };
    row.long = await page.evaluate(() => { const l = window.__long; return { n: l.length, total: l.reduce((a, b) => a + b, 0), max: Math.max(0, ...l) }; });
    row.dom = await page.evaluate(() => ({
      elements: document.getElementsByTagName('*').length,
      svgMarks: document.querySelectorAll('svg *').length,
      canvases: document.querySelectorAll('canvas').length,
      anims: document.getAnimations().filter(a => a.playState === 'running').length,
      infiniteAnims: document.getAnimations().filter(a => a.playState === 'running' && a.effect && a.effect.getTiming().iterations === Infinity).map(a => { const t = a.effect.target; return (a.animationName || 'transition') + '@' + (t ? t.tagName.toLowerCase() + (t.className && typeof t.className === 'string' ? '.' + t.className.split(' ')[0] : '') : '?'); }).slice(0, 12),
      scrollH: document.documentElement.scrollHeight,
    }));
    // idle window
    const reqBefore = requests; const apiBefore = { ...apiCounts };
    const pc1 = await procCpu();
    await page.waitForTimeout(IDLE_MS);
    const m2 = await metrics(cdp);
    const pc2 = await procCpu();
    row.idle = {
      gpuPct: +(((pc2.gpu - pc1.gpu) / (IDLE_MS / 1000)) * 100).toFixed(1), rendererPct: +(((pc2.renderer - pc1.renderer) / (IDLE_MS / 1000)) * 100).toFixed(1),
      taskPct: +(((m2.TaskDuration - m1.TaskDuration) / (IDLE_MS / 1000)) * 100).toFixed(1),
      scriptPct: +(((m2.ScriptDuration - m1.ScriptDuration) / (IDLE_MS / 1000)) * 100).toFixed(1),
      layouts: m2.LayoutCount - m1.LayoutCount, styles: m2.RecalcStyleCount - m1.RecalcStyleCount,
      requests: requests - reqBefore,
      api: Object.fromEntries(Object.entries(apiCounts).map(([k, v]) => [k, v - (apiBefore[k] || 0)]).filter(([, v]) => v > 0)),
    };
    // scroll pass
    const m3 = await metrics(cdp);
    const steps = await page.evaluate(async () => {
      let el = document.scrollingElement, best = el.scrollHeight - el.clientHeight;
      for (const e of document.querySelectorAll('*')) { const d = e.scrollHeight - e.clientHeight; if (d > best && e.clientHeight > 200 && /(auto|scroll)/.test(getComputedStyle(e).overflowY)) { best = d; el = e; } }
      const h = el.scrollHeight, vh = el.clientHeight; let n = 0;
      for (let y = 0; y < h && n < 40; y += vh * 0.9, n++) { el.scrollTop = y; await new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r))); }
      return n + ':' + h;
    });
    await page.waitForTimeout(1500);
    const m4 = await metrics(cdp);
    row.scroll = { steps, task: +(m4.TaskDuration - m3.TaskDuration).toFixed(2), script: +(m4.ScriptDuration - m3.ScriptDuration).toFixed(2), layout: +(m4.LayoutDuration - m3.LayoutDuration).toFixed(2), style: +(m4.RecalcStyleDuration - m3.RecalcStyleDuration).toFixed(2), heapMB: +(m4.JSHeapUsedSize / 1048576).toFixed(1), nodes: m4.Nodes, requests: requests - reqBefore };
    row.api = Object.entries(apiCounts).sort((a, b) => b[1] - a[1]).slice(0, 6);
  } catch (e) { row.error = String(e).slice(0, 300); }
  row.errors = errors.slice(0, 3);
  results.push(row);
  if (out) fs.writeFileSync(out, JSON.stringify(results, null, 1));
  const l = row.load || {};
  console.log(`${name.padEnd(38)} settle=${String(row.settleMs).padStart(6)} task=${l.task} script=${l.script} layout=${l.layout} style=${l.style} heap=${l.heapMB} nodes=${l.nodes} req=${l.requests} long=${row.long?.n}/${row.long?.max} idle=${row.idle?.taskPct}% gpu=${row.idle?.gpuPct}% rend=${row.idle?.rendererPct}% idleReq=${row.idle?.requests} anims=${row.dom?.anims} scrollTask=${row.scroll?.task} ${row.error || ''}`);
  await page.close();
}
await browser.close();
