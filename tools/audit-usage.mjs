#!/usr/bin/env node
// Live, serial Usage audit. Example:
// node tools/audit-usage.mjs --config /path/to/dev/library.toml --origin http://127.0.0.1:18879
// Samples, URLs, and raw results are private (.context); the public report has
// case IDs, timings and findings only. Preference writes are sandboxed in each
// browser context so this audit never edits library settings.
import { readFileSync, writeFileSync, appendFileSync, mkdirSync, existsSync, openSync, unlinkSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { createHash } from 'node:crypto';
import { createRequire } from 'node:module';
import { performance } from 'node:perf_hooks';
const require = createRequire(new URL('../web/package.json', import.meta.url));
const { build } = require('esbuild');
const { chromium } = require('playwright-core');
const core = require('@pythia-software/query-table-core');
const args = process.argv.slice(2), option = (name, fallback) => { const i = args.indexOf(`--${name}`); return i < 0 ? fallback : args[i + 1]; };
const origin = option('origin', 'http://127.0.0.1:18879');
const config = option('config', '/Volumes/euclid/pharos-dev-riyadh-v18/library.toml');
const runName = option('run', new Date().toISOString().replace(/[:.]/g, '-'));
const output = resolve(option('output', `.context/usage-audit/${runName}`));
const report = resolve(option('report', 'docs/usage-audit.md'));
const timeout = 30000, count = Number(option('cases', '25'));
const pageFilter = option('pages', '').split(',').filter(Boolean);
const idFilter = option('ids', '').split(',').filter(Boolean);
mkdirSync(output, { recursive: true });
const lock = resolve('.context/usage-audit/active.lock');
if (existsSync(lock)) {
  const previous = Number(readFileSync(lock, 'utf8'));
  let active = false; try { process.kill(previous, 0); active = true; } catch {}
  if (active) throw new Error(`An audit is already running (PID ${previous})`);
  unlinkSync(lock);
}
writeFileSync(openSync(lock, 'wx'), String(process.pid));
process.on('exit', () => { try { unlinkSync(lock); } catch {} });
const modelFile = join(output, 'model.mjs');
await build({ entryPoints: ['tools/usage-audit-cases.ts'], bundle: true, platform: 'node', format: 'esm', outfile: modelFile, nodePaths: [resolve('web/node_modules')], logLevel: 'silent' });
const model = await import(pathToFileURL(modelFile));
const token = JSON.parse(readFileSync(config, 'utf8').match(/^api_token\s*=\s*(.+)$/m)[1]);
async function api(path, body) {
  const response = await fetch(origin + path, { method: body ? 'POST' : 'GET', headers: { Authorization: `Bearer ${token}`, Cookie: `pharos_dev_token_${new URL(origin).port}=${encodeURIComponent(token)}`, ...(body ? { 'Content-Type': 'application/json' } : {}) }, body: body ? JSON.stringify(body) : undefined, signal: AbortSignal.timeout(timeout) });
  if (!response.ok) throw new Error(`${path}: ${response.status} ${(await response.text()).slice(0, 250)}`);
  return response.json();
}
const documents = Object.fromEntries(Object.keys(model.usageDatasets).map(dataset => [dataset, JSON.parse(readFileSync(`schemas/${dataset}.schema.json`, 'utf8'))]));
const baselinePrefs = (await api('/api/preferences')).values ?? {};
const samples = {};
for (const [dataset, document] of Object.entries(documents)) {
  try { samples[dataset] = (await api(`/api/query/${dataset}`, { select: model.sampleSelect(document).map(item => item.field), where: [], orderBy: document.defaultSort ?? [], limit: 8, offset: 0 })).rows ?? []; }
  catch (error) { samples[dataset] = []; console.log(`Sample ${dataset}: ${error.message}`); }
}
const concurrency = existsSync('.context/concurrency-real-data.json') ? JSON.parse(readFileSync('.context/concurrency-real-data.json', 'utf8')) : { period_start: '2026-10-01', period_end: '2026-10-14', threads: [] };
let cases = [...Object.keys(model.usageTopics).flatMap(topic => model.dashboardCases(topic, documents, samples, concurrency)), ...Object.keys(model.usageDatasets).flatMap(dataset => model.queryCases(dataset, documents[dataset], samples[dataset])), ...(args.includes('--controls') ? model.dashboardControlCases(documents, samples, concurrency) : [])];
// Run the known expensive raw-call surfaces last, so their cleanup does not
// obscure coverage of every other page. Execution remains strictly serial.
cases.sort((a, b) => Number(['dashboard:time','query:tool_calls'].includes(a.page)) - Number(['dashboard:time','query:tool_calls'].includes(b.page)));
cases = cases.filter(item => (!pageFilter.length || pageFilter.includes(item.page)) && (!idFilter.length || idFilter.includes(item.id)) && Number(item.id.slice(-2)) <= count);
writeFileSync(join(output, 'manifest.json'), JSON.stringify({ origin, runName, timeout, cases, samples, backendHash: args.includes('--binary') ? createHash('sha256').update(readFileSync(option('binary'))).digest('hex') : null, readiness: await api('/api/ready?projections=1'), sourceHash: createHash('sha256').update(readFileSync('internal/archive/assets/query-tables.js')).digest('hex') }, null, 2));
const resultsFile = join(output, 'results.jsonl');
const results = existsSync(resultsFile) ? readFileSync(resultsFile, 'utf8').trim().split('\n').filter(Boolean).map(line => JSON.parse(line)) : [];
const completed = new Set(results.map(result => result.id));
const band = ms => ms >= 30000 ? 'Black' : ms >= 4000 ? 'Red' : ms >= 1000 ? 'Yellow' : 'Green';
const stable = value => JSON.stringify(value, (_key, v) => v && typeof v === 'object' && !Array.isArray(v) ? Object.fromEntries(Object.entries(v).sort(([a], [b]) => a.localeCompare(b))) : v);
// Compare the published query model's canonical predicates. For example,
// a one-value OR is serialized as a single predicate by the editor.
const canonicalWhere = where => core.normalizeQueryState({ ...core.EMPTY_QUERY, where }).where;
const retainsWhere = (actual, requested) => {
  const retained = canonicalWhere(actual);
  return canonicalWhere(requested).every(term => retained.some(candidate => stable(candidate) === stable(term)));
};
const hash = value => createHash('sha256').update(stable(value)).digest('hex').slice(0, 16);
const compact = text => String(text).replace(/\s+/g, ' ').trim().slice(0, 500);
const privateError = error => compact(error.message ?? error).replaceAll(token, '[credential removed]');
function writeReport(state = 'Running') {
  const pages = [...new Set(cases.map(item => item.page))];
  const lines = ['# Usage audit', '', `Status: **${state}**. Updated ${new Date().toISOString()}.`, '', `Run: \`${runName}\`. Serial Chrome against the existing isolated dev snapshot. ${results.length}/${cases.length} cases completed.`, '',
    'Timing is the slower of initial navigation, changed-control completion, and fresh-context replay to a settled data view, including rendering and dependent requests. API maximum and replay timings are retained in private checkpoints. Green <1 s; Yellow 1–<4 s; Red 4–<30 s; Black ≥30 s without a settled result. Errors are recorded separately from speed.', '',
    'Each page gets 25 deterministic combinations of supported dates, categorical/keyword/numeric/null/OR filters, grouping, formulas, sorting, and controls as applicable. Empty results count as successful only after a completed response. Every case replays its resulting URL in a fresh browser context with different saved display preferences. Dashboard links are checked for source-filter preservation and every distinct linked query is executed; five representative links per dashboard also open in fresh browsers; equivalent native queries reuse a completed read within the frozen snapshot; grouped queries inspect their first group when present. Preference writes are intercepted and stay inside the test context.', '',
    'This is sampled coverage, not a proof about every possible query. API/user data, query URLs, and detailed request evidence remain under `.context/usage-audit/`.', '',
    '| Page | Cases | Green | Yellow | Red | Black | Errors | URL mismatches | Link issues | p50 | p95 |', '| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |'];
  for (const page of pages) {
    const rows = results.filter(row => row.page === page), values = rows.map(row => row.ms).sort((a, b) => a - b), percentile = p => values.length ? `${(values[Math.ceil(values.length * p) - 1] / 1000).toFixed(2)} s` : '—';
    lines.push(`| ${page} | ${rows.length} | ${['Green', 'Yellow', 'Red', 'Black'].map(color => rows.filter(row => row.band === color).length).join(' | ')} | ${rows.filter(row => row.errors.length).length} | ${rows.filter(row => row.urlIssues.length).length} | ${rows.filter(row => row.linkIssues.length).length} | ${percentile(.5)} | ${percentile(.95)} |`);
  }
  lines.push('', '## Findings observed', '', '| Case | Kind | Evidence |', '| --- | --- | --- |');
  for (const row of results) for (const [kind, issues] of [['Error', row.errors], ['URL', row.urlIssues], ['Link', row.linkIssues], ...(row.band === 'Red' || row.band === 'Black' ? [['Performance', [`${row.band}: ${(row.ms / 1000).toFixed(2)} s`]]] : [])]) for (const issue of issues) lines.push(`| ${row.id} | ${kind} | ${compact(issue).replaceAll('|', '\\|').replace(/https?:\/\/\S+/g, '[private URL]')} |`);
  if (!results.some(row => row.errors.length || row.urlIssues.length || row.linkIssues.length || ['Red', 'Black'].includes(row.band))) lines.push('| — | — | No failures observed in completed cases. |');
  lines.push('', '## Checkpoints', '', 'The run manifest, one JSON record per completed case, and a machine-readable summary are saved in the run directory. Reuse `--run` to resume completed cases, and `--pages dashboard:tokens,query:usage` to target a repair. Screenshots are captured only when `PHAROS_UI_SCREENSHOTS` is set.', '');
  writeFileSync(report, lines.join('\n'));
  writeFileSync(join(output, 'summary.json'), JSON.stringify({ state, runName, completed: results.length, planned: cases.length, counts: Object.fromEntries(['Green', 'Yellow', 'Red', 'Black'].map(color => [color, results.filter(row => row.band === color).length])), errors: results.filter(row => row.errors.length).length, urlIssues: results.filter(row => row.urlIssues.length).length, linkIssues: results.filter(row => row.linkIssues.length).length }, null, 2));
}
writeReport();
const browser = await chromium.launch({ handleSIGTERM: false, handleSIGINT: false, headless: true, executablePath: process.env.CHROME_PATH ?? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
let stopping = false;
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => { stopping = true; void browser.close(); });
async function session(poison = false) {
  const prefs = structuredClone(baselinePrefs);
  if (poison) {
    prefs['pharos-usage-chart'] = { metric: 'cost', split: 'effort', period: 'month', hours: 12, days: 7, rollingPeriods: 7, scale: 'linear' };
    prefs['pharos-writing-view'] = { scope: 'all', split: 'source_kind', period: 'month', hours: 12, days: 7, rollingPeriods: 7, scale: 'linear' };
    prefs['pharos-carbon-chart'] = { metric: 'energy', split: 'provider', period: 'day', hours: 12, days: 7 };
    prefs['pharos-concurrency-v1'] = { mode: 'model', counting: 'agent', gap: 0, range: 'all' };
    prefs['pharos-carbon-v1'] = { ...(prefs['pharos-carbon-v1'] ?? {}), scenario: 'high', pueScenario: 'high', grid: 'custom', customGrid: 777, inputSensitivity: 'third' };
  }
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, timezoneId: 'America/Denver', locale: 'en-US', reducedMotion: 'reduce' });
  await context.addCookies([{ name: `pharos_dev_token_${new URL(origin).port}`, value: token, url: origin }]);
  await context.route('**/assets/preferences.js', route => route.fulfill({ contentType: 'text/javascript', body: `window.pharosPreferenceValues=${JSON.stringify(prefs)};` }));
  await context.route('**/api/preferences', async route => {
    if (route.request().method() === 'POST') { const body = route.request().postDataJSON(); Object.assign(prefs, body?.set ?? {}); for (const key of body?.delete ?? []) delete prefs[key]; }
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ values: prefs }) });
  });
  const page = await context.newPage(); page.setDefaultTimeout(timeout); page.setDefaultNavigationTimeout(timeout);
  const requests = [], pending = new Set(), errors = [], responses = [], processed = new Set();
  let generation = 0; const requestGeneration = new WeakMap();
  let wake; const notify = () => { wake?.(); wake = null; };
  const critical = request => /\/(?:query-v3|rows-v2|metrics|series|carbon)(?:\?|$)|\/api\/concurrency/.test(request.url());
  const relevant = request => new URL(request.url()).pathname.startsWith('/api/query/') || /\/api\/concurrency/.test(request.url());
  page.on('pageerror', error => errors.push(privateError(error)));
  page.on('request', request => { if (request.isNavigationRequest() && request.resourceType() === 'document') { generation++; pending.clear(); processed.clear(); responses.length = 0; } if (relevant(request)) { requestGeneration.set(request, generation); if (critical(request)) pending.add(request); requests.push({ request, path: new URL(request.url()).pathname, body: request.postDataJSON?.(), started: performance.now() }); } });
  page.on('requestfinished', request => { pending.delete(request); const item = requests.find(item => item.request === request); if (item) item.ms = performance.now() - item.started; notify(); });
  page.on('requestfailed', request => { pending.delete(request); notify(); if (!/ABORTED|cancelled/i.test(request.failure()?.errorText ?? '')) errors.push(`request failed: ${new URL(request.url()).pathname}`); });
  page.on('response', response => {
    if (!relevant(response.request()) || requestGeneration.get(response.request()) !== generation) return;
    const task = (async () => {
      const body = await response.json().catch(() => null), path = new URL(response.url()).pathname;
      pending.delete(response.request()); const tracked = requests.find(item => item.request === response.request()); if (tracked && !tracked.ms) tracked.ms = performance.now() - tracked.started; notify();
      responses.push({ path, body, request: response.request().postDataJSON?.() });
      if (response.status() >= 400) errors.push(`${path}: HTTP ${response.status()} ${compact(body?.error ?? '')}`);
      for (const metric of body?.metrics ?? []) if (metric.error) errors.push(`${path}: metric ${metric.id}: ${compact(metric.error)}`);
    })(); if (critical(response.request())) processed.add(task); task.finally(() => { processed.delete(task); notify(); });
  });
  const checkpoint = () => ({ requests: requests.length, responses: responses.length, errors: errors.length });
  async function settle(item, since = 0) {
    await page.locator('.usage-workspace-content').waitFor();
    const selector = item.page.startsWith('query:') ? '.usage-workspace-table .qt-table, .usage-workspace-table .qt-empty, .usage-workspace-table .query-table-error' : item.page === 'dashboard:parallelism' ? '.concurrency-chart svg, .concurrency-error' : '.usage-dashboard .usage-chart, .usage-dashboard .qt-metric, .usage-dashboard .query-table-error';
    await page.locator(selector).first().waitFor();
    const deadline = performance.now() + timeout;
    for (;;) {
      if (performance.now() > deadline) throw new Error(`No settled result after 30 seconds; pending=${[...pending].map(request => new URL(request.url()).pathname).join(',')}; responses=${responses.length}; busy=${await page.locator('.usage-workspace-content [aria-busy=true], .usage-workspace-content .qt-loading-bar').count()}`);
      await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
      if (pending.size || processed.size) { await new Promise(resolve => { const timer = setTimeout(resolve, Math.max(1, deadline - performance.now())); wake = () => { clearTimeout(timer); resolve(); }; }); continue; }
      const busy = await page.locator('.usage-workspace-content [aria-busy=true], .usage-workspace-content .qt-metric-loading, .usage-workspace-content .qt-loading-bar').count();
      if (busy) continue;
      if (!responses.some(response => /\/query-v3$|\/rows-v2$|\/metrics$|\/series$|\/carbon$/.test(response.path)) && item.page !== 'dashboard:parallelism' && !errors.length) continue;
      await page.evaluate(() => new Promise(resolve => requestAnimationFrame(resolve)));
      if (!pending.size) return;
    }
  }
  const snapshot = async () => page.evaluate(() => {
    const root = document.querySelector('.usage-workspace-content');
    const visible = element => Boolean(element.getClientRects().length);
    const controls = [...root.querySelectorAll('select,input:not([type=hidden]),textarea')].filter(visible).map(element => [element.getAttribute('aria-label') ?? element.closest('label')?.textContent.replace(/\s+/g, ' ').trim().slice(0, 50) ?? element.getAttribute('data-carbon-control') ?? element.id, ['checkbox','radio'].includes(element.type) ? element.checked : element.value]);
    const selected = [...root.querySelectorAll('.usage-legend-item[aria-pressed=true], [data-carbon-control][aria-pressed=true]')].filter(visible).map(element => element.getAttribute('aria-label') ?? element.textContent.trim());
    const legends = [...root.querySelectorAll('.usage-legend-item')].filter(visible).map(element => element.getAttribute('aria-label') ?? element.textContent.trim());
    const zoom = root.querySelector('.machine-zoom-status')?.textContent;
    const errors = [...root.querySelectorAll('.query-table-error,.qt-metric-error,.usage-chart-error,[role=alert]')].filter(visible).map(element => element.textContent.trim()).filter(Boolean);
    const links = [...root.querySelectorAll('.usage-dashboard-links a')].map(element => ({ label: element.textContent.trim(), href: element.getAttribute('href') }));
    const contextLinks = [...root.querySelectorAll('.usage-workspace-search-note a,.usage-dashboard-note a')].filter(visible).map(element => ({ label: element.textContent.trim(), href: element.getAttribute('href') }));
    return { controls, selected, legends, zoom, errors, links, contextLinks, title: root.querySelector('h1')?.textContent };
  });
  return { context, page, requests, responses, errors, checkpoint, settle, snapshot };
}
async function interact(s, item) {
  const root = s.page.locator('.usage-workspace-content');
  if (item.interaction === 'select' || item.interaction === 'range') {
    const candidates = item.interaction === 'range' ? root.getByRole('combobox', { name: 'Range', exact: true }) : root.locator('select:visible');
    const total = await candidates.count();
    for (let n = 0; n < total; n++) {
      const control = candidates.nth((n + Number(item.id.slice(-2))) % total);
      if (await control.isDisabled()) continue;
      const values = await control.evaluate(element => [...element.options].filter(option => !option.disabled && option.value !== element.value && !['custom','selection'].includes(option.value)).map(option => option.value));
      if (values.length) { await control.selectOption(values[Number(item.id.slice(-2)) % values.length]); return true; }
    }
  }
  if (item.interaction === 'legend') {
    const legends = root.locator('.usage-legend-item[aria-haspopup=menu]');
    if (await legends.count()) { await legends.first().click(); await s.page.getByRole('menuitem', { name: /Show only this value/ }).click(); return true; }
  }
  if (item.interaction === 'expand') {
    const more = root.locator('.usage-legend-item[title^="Show next"]');
    if (await more.count()) { await more.first().click(); return true; }
  }
  if (item.interaction === 'carbon-assumption') {
    const choice = root.locator('[data-carbon-control="estimate"][data-carbon-value="low"]');
    if (await choice.count()) { await choice.click(); return true; }
  }
  if (item.interaction === 'select-conversation') {
    const conversation = root.locator('.concurrency-thread-button').first();
    if (await conversation.count()) { await conversation.click(); return true; }
  }
  if (item.interaction === 'zoom') {
    const plot = root.locator('.concurrency-chart svg'), box = await plot.boundingBox();
    if (box) {
      await s.page.mouse.move(box.x + box.width * .3, box.y + 110);
      await s.page.mouse.down();
      await s.page.mouse.move(box.x + box.width * .7, box.y + 110, { steps: 8 });
      await s.page.mouse.up();
      return true;
    }
  }
  return false;
}
function responseFingerprint(s) {
  const latest = [...s.responses].reverse().find(response => /\/query-v3$|\/rows-v2$/.test(response.path));
  if (!latest) return undefined;
  return hash({ total: latest.body?.total, rows: latest.body?.rows, metrics: latest.body?.metrics });
}
function sourceQuery(url) {
  const parsed = new URL(url, origin), dataset = parsed.searchParams.get('analyze_data');
  const query = dataset && parsed.searchParams.has(`q_${dataset}`) ? core.decodeQueryStrict(parsed.searchParams.get(`q_${dataset}`)) : undefined;
  const params = new URLSearchParams();
  if (dataset === 'usage' && parsed.searchParams.has('analyze_carbon')) params.set('carbon_settings', parsed.searchParams.get('analyze_carbon'));
  return { query, dataset, params: params.toString(), source: query?.grouping?.source ?? query };
}
const criticalPath = path => /\/(?:query-v3|rows-v2|metrics|series|carbon)$|concurrency/.test(path);
const linkCacheFile = join(output, 'verified-links.json');
const verifiedLinkQueries = new Map(existsSync(linkCacheFile) ? JSON.parse(readFileSync(linkCacheFile, 'utf8')) : []);
const progress = (id, phase) => writeFileSync(join(output, 'progress.json'), JSON.stringify({ id, phase, at: new Date().toISOString() }));
async function runCase(item) {
  const result = { id: item.id, page: item.page, label: item.label, ms: 0, band: '', errors: [], urlIssues: [], linkIssues: [], transitions: [], interaction: null };
  let s, replay;
  try {
    progress(item.id, 'initial load'); s = await session(); const start = performance.now();
    await s.page.goto(origin + item.url); await s.settle(item); result.ms = performance.now() - start;
    result.loadMs = result.ms;
    const beforeAction = s.checkpoint(), actionStarted = performance.now();
    if (item.interaction && item.interaction !== 'inspect-group') {
      result.interaction = await interact(s, item);
      if (result.interaction) { await s.settle(item, beforeAction.requests); result.interactionMs = performance.now() - actionStarted; }
    }
    const first = await s.snapshot(), finalURL = s.page.url(); result.url = finalURL.replace(origin, '');
    result.linkTargets = first.links; result.errors.push(...s.errors, ...first.errors); result.sourceFingerprint = responseFingerprint(s);
    progress(item.id, 'URL replay'); replay = await session(true); const replayStart = performance.now();
    await replay.page.goto(finalURL); await replay.settle(item); result.replayMs = performance.now() - replayStart;
    const second = await replay.snapshot();
    result.errors.push(...replay.errors, ...second.errors.map(error => `Replay: ${error}`));
    for (const key of ['controls', 'selected', 'legends', 'zoom']) if (stable(first[key]) !== stable(second[key])) result.urlIssues.push(`${key} differ after fresh-context URL replay`);
    result.snapshots = { first, replay: second };
    const sourceWhere = item.query ? (item.query.grouping?.source ?? item.query).where : item.where ?? [];
    for (const link of first.contextLinks) {
      const destination = new URL(link.href, origin);
      if (destination.pathname === '/library' && sourceWhere.length && !destination.searchParams.has('q_library') && !/all conversation text|reset/i.test(link.label)) result.linkIssues.push(`${link.label}: record filters reset without an explicit reset label`);
      if (/MCP settings/i.test(link.label) && destination.pathname !== '/settings/mcp') result.linkIssues.push('MCP settings link has an unexpected destination');
    }
    if (item.page.startsWith('query:')) {
      const spec = sourceQuery(finalURL);
      if (!spec.query) result.urlIssues.push('Resulting URL has no decodable query');
      else if (item.query) {
        const expected = core.normalizeQueryState(item.query), actual = core.normalizeQueryState(spec.query);
        for (const key of Object.keys(expected)) if (stable(actual[key]) !== stable(expected[key])) result.urlIssues.push(`Query ${key} changed silently on load`);
      }
      const fresh = responseFingerprint(replay);
      if (result.sourceFingerprint && fresh && fresh !== result.sourceFingerprint) result.urlIssues.push('Result rows/counts/metrics differ on URL replay');
    }
    result.ms = Math.max(result.ms, result.replayMs ?? 0, result.interactionMs ?? 0);
    result.apiMaxMs = Math.max(0, ...s.requests.map(request => request.ms ?? 0));
    result.requestTimings = s.requests.map(({ path, ms }) => ({ path, ms }));
    result.pendingMetadata = s.requests.filter(item => !item.ms && !criticalPath(item.path)).map(item => item.path);
    await replay.context.close(); replay = null;
    if (item.page.startsWith('dashboard:')) {
      let reference;
      for (const link of first.links) {
        if (/grouped|records/i.test(link.label)) {
          const spec = sourceQuery(link.href);
          if (!spec.query) { result.linkIssues.push(`${link.label}: missing decodable query`); continue; }
          const source = stable(spec.source.where);
          if (reference && reference !== source) result.linkIssues.push(`${link.label}: source filters differ from other dashboard drilldowns`);
          reference ??= source;
          if (item.where?.length) {
            const requested = core.decodeQueryStrict(new URL(finalURL).searchParams.get('analyze_filters') ?? '')?.where ?? item.where;
            // Tools converts its calendar range into local-day equalities;
            // its model tests verify that conversion. Other predicates still
            // have to survive the link unchanged.
            const comparable = item.page === 'dashboard:tools' ? requested.filter(term => !('field' in term && term.field === 'day')) : requested;
            if (!retainsWhere(spec.source.where, comparable)) result.linkIssues.push(`${link.label}: source filter missing`);
          }
        } else {
          const next = new URL(link.href, origin);
          if (item.where?.length && !next.searchParams.has('analyze_filters') && ![...next.searchParams.keys()].some(key => key.startsWith('q_')) && !/reset/i.test(link.label)) result.linkIssues.push(`${link.label}: filters reset without an explicit reset label`);
        }
      }
      // Run every distinct outgoing query. One link per case is also opened
      // in a fresh browser; equivalent record/search URLs share one API read.
      const outgoing = first.links.filter(link => /grouped|records/i.test(link.label));
      const number = Number(item.id.slice(-2));
      const chosenLink = number <= 3 || number === 12 || number === 24 ? outgoing[number % Math.max(1, outgoing.length)] : undefined;
      const executed = new Map();
      for (const link of outgoing) {
        progress(item.id, `link: ${link.label}`); let started = performance.now();
        const native = sourceQuery(link.href), signature = stable([native.dataset, native.query, native.params]);
        const transport = native.query.grouping ? 'query-v3' : 'rows-v2';
        let linkSession;
        try {
          let kind = 'equivalent';
          let cached = verifiedLinkQueries.get(signature);
          if (cached && !native.query.grouping && cached.transport !== transport) { verifiedLinkQueries.delete(signature); cached = undefined; }
          if (cached?.error && !(link === chosenLink && args.includes('--in-place'))) throw new Error(`Same linked query already failed: ${cached.error}`);
          if (link === chosenLink && args.includes('--in-place')) {
            kind = 'browser-in-place';
            started = performance.now();
            await s.page.locator('.usage-dashboard-links').getByRole('link', { name: link.label, exact: true }).click();
            await s.page.waitForURL(url => url.searchParams.get('analyze_mode') === 'query');
            await s.settle({ ...item, page: `query:${native.dataset}` });
            const loaded = sourceQuery(s.page.url());
            if (loaded.dataset !== native.dataset || loaded.params !== native.params || stable(core.normalizeQueryState(loaded.query)) !== stable(core.normalizeQueryState(native.query))) throw new Error('In-app navigation changed the linked query or assumptions');
            const snapshot = await s.snapshot();
            if (snapshot.errors.length) throw new Error(snapshot.errors.join('; '));
          } else if (link === chosenLink) {
            kind = 'browser'; linkSession = await session();
            await linkSession.page.goto(new URL(link.href, origin).href);
            await linkSession.settle({ ...item, page: `query:${native.dataset}` });
            const snapshot = await linkSession.snapshot();
            if (linkSession.errors.length || snapshot.errors.length) throw new Error([...linkSession.errors, ...snapshot.errors].join('; '));
          } else if (!executed.has(signature) && !verifiedLinkQueries.has(signature)) {
            kind = 'api';
            const handshake = await api(`/api/query/${native.dataset}/capabilities`);
            const schema = core.loadSchema(documents[native.dataset]);
            const request = native.query.grouping ? core.toQueryRequestV3(native.query, schema, handshake.queryCapabilities, handshake.computedExecution) : core.toServerQueryV2(native.query, schema, handshake.computedExecution);
            const fields = native.dataset === 'tool_calls' ? ['command','file_path'] : native.dataset === 'writing_messages' ? ['text'] : native.dataset === 'mcp_calls' ? ['arguments_json','error_text'] : [];
            const budget = !native.query.grouping && request.where.some(term => ('any' in term ? term.any : [term]).some(clause => fields.includes(clause.field)));
            const params = new URLSearchParams(native.params);
            if (budget) params.set('interactive_search', '1');
            const response = await api(`/api/query/${native.dataset}/${native.query.grouping ? 'query-v3' : 'rows-v2'}${params.size ? `?${params}` : ''}`, request);
            const failures = (response.metrics ?? []).filter(metric => metric.error).map(metric => metric.error);
            if (failures.length) throw new Error(failures.join('; '));
          }
          executed.set(signature, true); verifiedLinkQueries.set(signature, { ok: true, transport });
          result.transitions.push({ label: link.label, kind, ms: performance.now() - started });
        } catch (error) { const message = privateError(error); result.linkIssues.push(`${link.label}: ${message}`); if (!message.startsWith('Same linked query')) { verifiedLinkQueries.set(signature, { error: message, transport }); result.transitions.push({ label: link.label, kind: 'failed', ms: performance.now() - started, error: message }); } }
        finally {
          if (args.includes('--requests') && linkSession) result.linkRequests = [...(result.linkRequests ?? []), ...linkSession.requests.map(({path,body,ms})=>({path,body,ms}))];
          await linkSession?.context.close().catch(() => {});
        }
      }
    } else {
      const backLink = s.page.getByRole('link', { name: /Back to dashboard|dashboard.*reset|Return to.*dashboard/i }).first();
      const back = await backLink.getAttribute('href');
      const sourceWhere = (item.query.grouping?.source ?? item.query).where;
      if (back && sourceWhere.length) {
        const params = new URL(back, origin).searchParams;
        const retained = core.decodeQueryStrict(params.get('analyze_filters') ?? '')?.where ?? [];
        if (!retainsWhere(retained, sourceWhere) && !/reset/i.test(await backLink.textContent())) result.linkIssues.push('Back to dashboard: query source filters reset without an explicit reset label');
      }
      // Exercise identity-based row actions with default, reduced-column,
      // and paged projections. The exact row payload remains private.
      if (!item.query.grouping && [1, 4, 24].includes(Number(item.id.slice(-2)))) {
        const visible = [...s.responses].reverse().find(response => /\/(query-v3|rows-v2)$/.test(response.path));
        const row = visible?.body?.rows?.[0];
        if (row && item.dataset === 'tools') {
          const link = s.page.locator('.usage-workspace-table .qt-row').first().getByRole('link', { name: 'Calls', exact: true });
          if (await link.count()) {
            const started = performance.now();
            try {
              const href = await link.getAttribute('href'), native = sourceQuery(href);
              result.rowActionURL = href;
              const handshake = await api('/api/query/tool_calls/capabilities');
              const response = await api('/api/query/tool_calls/rows-v2', core.toServerQueryV2(native.query, core.loadSchema(documents.tool_calls), handshake.computedExecution));
              const full = await api(`/api/query/tools/item?id=${encodeURIComponent(String(row.id))}`);
              result.rowActionEvidence = { fullRowFound: Boolean(full.row), projectedFields: Object.keys(row), sourceCount: Number(full.row?.call_count), destinationTotal: response.total };
              if (!full.row) throw new Error('Source summary identity could not be resolved');
              if (Number(full.row?.call_count) > 0 && response.total === 0) result.linkIssues.push('Summary Calls action opens an empty population for a nonempty summary');
              else if (Number(full.row?.call_count) !== response.total) result.linkIssues.push('Summary Calls action does not preserve the source row call count');
              result.transitions.push({ label: 'Summary Calls row action', kind: 'api', ms: performance.now() - started });
            } catch (error) { result.linkIssues.push(`Summary Calls row action: ${privateError(error)}`); }
          }
        }
        if (row && item.dataset === 'skill_usages') {
          const button = s.page.locator('.usage-workspace-table .qt-row').first().getByRole('button', { name: 'Evidence', exact: true });
          if (await button.count()) {
            try {
              const full = await api(`/api/query/skill_usages/item?id=${encodeURIComponent(String(row.id))}`);
              if (!full.row) throw new Error('Source skill identity could not be resolved');
              await button.click(); await s.page.waitForURL(url => url.pathname.startsWith('/work/'));
              const actual = new URL(s.page.url());
              result.rowActionURL = actual.pathname + actual.search;
              result.rowActionEvidence = { fullRowFound: true, projectedFields: Object.keys(row), expectedWorkspace: full.row.workspace_id, expectedConversation: full.row.conversation_id };
              if (decodeURIComponent(actual.pathname.slice(6)) !== String(full.row?.workspace_id) || actual.searchParams.get('conversation') !== String(full.row?.conversation_id)) result.linkIssues.push('Skill Evidence action loses the source identity when its columns are hidden');
            } catch (error) { result.linkIssues.push(`Skill Evidence row action: ${privateError(error)}`); }
          }
        }
      }
      if (item.interaction === 'inspect-group') {
      const records = s.page.getByRole('button', { name: 'Records', exact: true }).first();
      if (await records.count()) {
        const started = performance.now();
        try {
          const checkpoint = s.checkpoint(); await records.click(); await s.settle(item, checkpoint.requests);
          result.groupRecordsURL = s.page.url().replace(origin, '');
          const decoded = sourceQuery(s.page.url()).query;
          if (decoded?.grouping) result.linkIssues.push('Inspect group did not open source records');
          if (decoded && !retainsWhere(decoded.where, item.query.grouping.source.where)) result.linkIssues.push('Inspect group dropped a source predicate');
          result.transitions.push({ label: 'Inspect group records', kind: 'browser', ms: performance.now() - started });
        } catch (error) {
          const message = privateError(error);
          result.linkIssues.push(`Inspect group records: ${message}`);
          result.transitions.push({ label: 'Inspect group records', kind: 'failed', ms: performance.now() - started, error: message });
        }
      }
      }
    }
  } catch (error) { if (stopping || !browser.isConnected()) throw error; result.errors.push(privateError(error)); if (/timeout|30 seconds/i.test(error.message)) result.ms = Math.max(result.ms, timeout); }
  finally {
    if (args.includes('--requests')) result.requests = s?.requests.map(({path,body,ms})=>({path,body,ms}));
    await replay?.context.close().catch(() => {}); await s?.context.close().catch(() => {});
  }
  result.band = band(result.ms); result.errors = [...new Set(result.errors)]; result.linkIssues = [...new Set(result.linkIssues)];
  return result;
}
try {
  for (const item of cases) {
    if (stopping) break;
    if (completed.has(item.id)) continue;
    console.log(`START ${item.id}`);
    const result = await runCase(item); if (stopping) break; results.push(result); appendFileSync(resultsFile, JSON.stringify(result) + '\n'); writeFileSync(linkCacheFile, JSON.stringify([...verifiedLinkQueries])); writeReport();
    console.log(`${result.band.padEnd(6)} ${(result.ms / 1000).toFixed(2)}s ${result.id} errors=${result.errors.length} url=${result.urlIssues.length} links=${result.linkIssues.length}`);
  }
  writeReport(stopping ? 'Interrupted' : 'Complete');
} catch (error) { writeReport('Interrupted: harness or browser stopped'); if (!stopping) throw error; } finally { await browser.close(); }
