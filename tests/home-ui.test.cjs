const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');
const { buildSync } = require('../web/node_modules/esbuild');
const root = path.resolve(__dirname, '..');
const { chromium } = createRequire(path.join(root, '.context/browser-tests/package.json'))('playwright');
const source = fs.readFileSync(path.join(root, 'internal/archive/assets/ui.py'), 'utf8');
const html = source.slice(source.indexOf("r'''" ) + 4, source.lastIndexOf("'''"));
function moduleOf(entry) {
  const loaded = { exports: {} };
  new Function('module', 'exports', 'require', buildSync({ entryPoints: [path.join(root, entry)], bundle: true, platform: 'node', format: 'cjs', write: false }).outputFiles[0].text)(loaded, loaded.exports, require);
  return loaded.exports;
}
const core = moduleOf('web/node_modules/@pythia-software/query-table-core/dist/index.js');
const links = moduleOf('web/src/query-links.ts');
async function readQuery(page, dataset) {
  let token = new URL(page.url()).searchParams.get(`q_${dataset}`);
  if (token?.startsWith('@')) {
    const snapshot = await page.evaluate(async ({ dataset, code }) => {
      const response = await fetch(`/api/query-snapshots?dataset=pharos_${dataset}&code=${encodeURIComponent(code)}`);
      return response.ok ? response.json() : null;
    }, { dataset, code: token.slice(1) });
    token = snapshot?.token;
  }
  return token && core.decodeQuery(token);
}
async function waitForQuery(page, dataset, predicate) {
  await assertEventually(async () => { const query = await readQuery(page, dataset); return query && predicate(query); });
}
// The catalog shares Home and the preview cache with the card library, and
// may already have run a library card's window; a data refresh clears it.
async function clearPreviewCache(page) { await page.evaluate(() => window.dispatchEvent(new Event('pharos:data-refresh'))); }
// Home re-lays out the catalog on resize too; measure covers once their bars settle.
async function waitForBarsInCover(card) {
  await assertEventually(() => card.evaluate(element => {
    const cover = element.querySelector('.query-cover').getBoundingClientRect();
    return [...element.querySelectorAll('.qt-metric-bar-track')].every(bar => bar.getBoundingClientRect().bottom <= cover.bottom + 1);
  }));
}
async function assertEventually(check) {
  const end = Date.now() + 5000;
  while (Date.now() < end) { if (await check()) return; await new Promise(resolve => setTimeout(resolve, 25)); }
  assert.ok(await check(), 'expected state did not arrive');
}
const { queryRecipes, instantiateRecipe } = moduleOf('web/src/curiosity-data.ts');
const { categoryForQuery } = moduleOf('web/src/query-categories.ts');
const schemas = Object.fromEntries(links.queryDatasets.map(dataset => [dataset, core.loadSchema(JSON.parse(fs.readFileSync(path.join(root, `schemas/${dataset}.schema.json`))))]));

function fixtures() {
  const calls = [], usage = [], writing = [], skills = [], library = [], attempts = [];
  for (let i = 0; i < 28; i++) {
    const date = new Date(); date.setDate(date.getDate() - i % 7); date.setHours(0, 0, 1, 0);
    const day = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`;
    const repository_name = ['pharos', 'query-table', 'workbench'][i % 3];
    calls.push({ id: `call-${i}`, day, started_at: date.toISOString(), tool_name: ['Edit', 'Bash', 'Read'][i % 3], tool_category: ['edit', 'command', 'read'][i % 3], status: i % 2 ? 'ok' : 'error', error_count: i % 2 ? 0 : 1, call_count: 1, error_signature: 'file_not_read', duration_ms: (i + 1) * 5000, command_name: 'go test', repo_path: `src/module-${i % 3}.go`, test_failure: i % 2 === 0, carried_tokens: (i + 1) * 1200, result_tokens: 300 + i * 40, truncated: i % 4 === 0, search_query: 'SQLite indexing', host: 'go.dev', mcp_method: 'search_work', repository_name, title: 'Archive indexing' });
    usage.push({ id: `usage-${i}`, day, first_usage_at: date.toISOString(), last_usage_at: date.toISOString(), repository_name, total_tokens: 50000 + i * 7000, model_family: ['sonnet-4-6', 'gpt-5.4'][i % 2], cost_usd: i / 2 + 1, price_status: 'priced', workspace_id: `work-${i}`, cache_read_input_tokens: i * 400, session_kind: i % 3 ? 'root' : 'subagent' });
    writing.push({ id: `writing-${i}`, day, sent_at: date.toISOString(), text: i % 2 ? 'Please inspect how indexes affect the query plan.' : 'I meant the timestamp index, not the title index.', typed_message: true, typed_words: i + 20, words: i + 40, repository_name, title: 'Archive indexing' });
    skills.push({ id: `skill-${i}`, day, created_at: date.toISOString(), skill_name: ['review', 'debug', 'visualize'][i % 3], status: i % 3 ? 'ok' : 'error', repository_name: ['pharos', 'query-table', 'workbench'][Math.floor(i / 3) % 3], conversation_id: `conversation-${i}`, evidence_type: i % 2 ? 'invocation' : 'body-load' });
    library.push({ id: `work-${i}`, title: `Archive work ${i + 1}`, activity_at: date.toISOString(), repository_name, cost_usd: i + 1, token_count: 1000, price_status: 'priced' });
    attempts.push({ id: `attempt-${i}`, started_at: date.toISOString(), project: 'pharos', flavor: 'fix', execution_class: 'llm', shape_version: 'v1', configuration: ['fast', 'careful'][i % 2], attempt_count: 1, advanced_count: i % 3 ? 1 : 0, escalated_count: i % 3 ? 0 : 1 });
    calls.at(-1).conversation_id = `conversation-${i}`;
    calls.at(-1).result_tokens_source = i % 2 ? 'estimated' : 'measured';
    usage.at(-1).cost_today_usd = i / 3 + 1;
  }
  return { tool_calls: calls, usage, writing_messages: writing, skill_usages: skills, library, tl1_attempts: attempts };
}
async function setup({ saved = false, reducedMotion = false, clock = false, viewport = { width: 1180, height: 920 }, deviceScaleFactor = 1, url = '/', waitForHome = url === '/', preferenceValues = {}, localValues = {}, now } = {}) {
  const browser = await chromium.launch({ headless: true, executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
  const page = await browser.newPage({ viewport, deviceScaleFactor, timezoneId: 'America/Denver', reducedMotion: reducedMotion ? 'reduce' : 'no-preference' });
  if (clock) await page.clock.install();
  if (now) await page.clock.setFixedTime(now);
  await page.addInitScript(values => { for (const [key, value] of Object.entries(values)) localStorage.setItem(key, JSON.stringify(value)); }, localValues);
  const data = { rows: fixtures(), empty: false, requests: [], metadataRequests: [], snapshots: {}, errors: [], pending: [], holdPreviews: false, previewFailures: new Set(), preferences: { 'pharos-theme': 'dark', ...preferenceValues } };
  if (saved) {
    const recipe = instantiateRecipe(queryRecipes.find(item => item.id === 'human-words'), 7);
    data.preferences['query-table:saved:pharos_writing_messages'] = [{ id: 'original-query', name: 'My words', savedAt: 100, query: recipe.query }];
    data.preferences['query-table:default:pharos_writing_messages'] = 'original-query';
  }
  page.on('pageerror', error => data.errors.push(error.stack));
  await page.route('http://home.test/**', async route => {
    const url = new URL(route.request().url());
    const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
    if (['/', '/home', '/library', '/usage', '/tools'].includes(url.pathname)) return route.fulfill({ contentType: 'text/html', body: html });
    if (url.pathname === '/assets/preferences.js') return route.fulfill({ contentType: 'application/javascript', body: `window.pharosPreferenceValues=${JSON.stringify(data.preferences)}` });
    if (url.pathname === '/api/query-snapshots') {
      const key = url.search;
      if (route.request().method() === 'PUT') { data.snapshots[key] = route.request().postDataJSON(); return json(data.snapshots[key]); }
      return data.snapshots[key] ? json(data.snapshots[key]) : route.fulfill({ status: 404 });
    }
    if (url.pathname === '/api/preferences') {
      const body = route.request().postDataJSON(); Object.assign(data.preferences, body.set); for (const key of body.delete ?? []) delete data.preferences[key]; return json({});
    }
    if (['/assets/query-tables.js', '/assets/query-tables.css'].includes(url.pathname)) return route.fulfill({ contentType: url.pathname.endsWith('.css') ? 'text/css' : 'application/javascript', body: fs.readFileSync(path.join(root, 'internal/archive/assets', path.basename(url.pathname))) });
    if (url.pathname === '/api/query/writing/series') return json({ works: 0, daily: [], totals: {} });
    if (url.pathname === '/api/query-computed-columns') return json([]);
    if (url.pathname.endsWith('/capabilities')) return json({ queryCapabilities: { version: 3, profile: 'qt-sqlite-v1', groupedRows: true, calendarGrouping: true, computedSourceFilters: true, maxRows: 10000, maxGroups: 10000, maxPopulation: 250000 }, metricCapabilities: { version: 2, expressions: true, shownRows: true, computedFields: true, distributions: true, profile: 'qt-sqlite-v1' }, computedExecution: { profile: 'qt-sqlite-v1', planToken: '', fields: {}, resolvedRevisions: {} } });
    if (url.pathname.startsWith('/api/query/') && route.request().method() === 'POST') {
      const dataset = url.pathname.split('/')[3], request = route.request().postDataJSON(); data.requests.push({ dataset, request, path: url.pathname });
      if (data.previewFailures.has(dataset)) return route.fulfill({ status: 503, contentType: 'application/json', body: JSON.stringify({ error: 'Preview service temporarily unavailable.' }) });
      // Calendar execution receives the server's ISO datetime shape. The local
      // interpreter also accepts epochs for ordinary row predicates.
      const calendar = request.metrics?.some(metric => metric.dateGrouping);
      const rows = (data.empty ? [] : data.rows[dataset] ?? []).map(row => Object.fromEntries(Object.entries(row).map(([key, value]) => [key, !calendar && schemas[dataset].fields.find(field => field.name === key)?.type === 'datetime' ? Date.parse(value) : value])));
      if (data.holdPreviews) await new Promise(resolve => data.pending.push({ release: resolve, dataset, request }));
      if (url.pathname.endsWith('/query-v3')) {
        const state = { ...core.EMPTY_QUERY, ...request.result, select: request.result.select.map(field => ({ field })), aggregations: request.metrics,
          ...(request.grouping ? { grouping: { ...request.grouping, source: { ...core.EMPTY_QUERY, ...request.source, select: request.source.select.map(field => ({ field })) } } } : {}) };
        const prepared = state.grouping ? core.prepareGrouping(rows, schemas[dataset], state.grouping, { now: request.now }) : undefined;
        const schema = prepared?.schema ?? schemas[dataset], result = core.applyQuery(rows, state, schemas[dataset], { now: request.now });
        const metrics = core.evaluateMetrics(rows, state, schemas[dataset], { now: request.now, shownRows: result.rows });
        return json({ version: 3, now: request.now, sourceTotal: prepared?.inputs.length ?? rows.length, rows: request.rows ? result.rows : [], total: result.total, metrics: metrics.metrics,
          schema: { name: schema.name, idField: schema.idField, fields: schema.fields.map(({ name, type }) => ({ name, type })) }, execution: { profile: 'qt-sqlite-v1', planToken: '', fields: {}, resolvedRevisions: {} } });
      }
      if (url.pathname.endsWith('/aggregations')) return json(core.applyAggregations(rows, { ...core.EMPTY_QUERY, ...request }, schemas[dataset]));
      if (url.pathname.endsWith('/metrics')) return json({ version: 2, execution: { profile: 'qt-sqlite-v1', planToken: '', fields: {}, resolvedRevisions: {} }, ...core.evaluateMetrics(rows, { ...core.EMPTY_QUERY, ...request, aggregations: request.metrics.map(metric => ({ ...metric, ...(metric.distribution ? { display: { kind: metric.distribution.kind } } : {}) })) }, schemas[dataset]) });
      const result = core.applyQuery(rows, { ...core.EMPTY_QUERY, ...request }, schemas[dataset]);
      // Match the server's ISO datetime serialization after local evaluation.
      const dates = new Set(schemas[dataset].fields.filter(field => field.type === 'datetime').map(field => field.name));
      result.rows = result.rows.map(row => Object.fromEntries(Object.entries(row).map(([key, value]) => [key, dates.has(key) && typeof value === 'number' ? new Date(value).toISOString() : value])));
      return json({ version: 2, ...result, computed: [], execution: { profile: 'qt-sqlite-v1', planToken: '', fields: {}, resolvedRevisions: {} } });
    }
    if (url.pathname.endsWith('/distinct')) { data.metadataRequests.push({ dataset: url.pathname.split('/')[3], field: url.searchParams.get('field') }); return json({ values: [], hasMore: false }); }
    if (url.pathname.endsWith('/field-stats')) return json({});
    if (url.pathname === '/api/tools/status') return json({ pending_conversations: 0, backfill: { running: false, error: null } });
    if (url.pathname === '/api/findings') return json({ status: { running: false }, items: [] });
    if (url.pathname.startsWith('/api/')) return json({ items: [], metrics: [], totals: {}, last_30_days: {}, sources: [], unpriced_models: [] });
    return route.fulfill({ status: 404 });
  });
  await page.goto(`http://home.test${url}`);
  if (clock) await page.clock.runFor(5000);
  if (url.includes('cards=library')) await page.locator('.card-library-entry').first().waitFor();
  else if (waitForHome) await page.waitForFunction(() => document.querySelector('.home-conveyor')?.getAttribute('aria-busy') === 'false' && document.querySelector('.home-belt-row .query-card'));
  else await page.waitForFunction(() => document.querySelector('.view.active .qt-qb'));
  return { browser, page, data };
}

test('rolling URLs retain the same query while renewing calendar bounds and dataset routes', () => {
  global.location = { origin: 'http://home.test' };
  assert.equal(links.queryFromURL('/usage?q_library=&q_usage=').dataset, 'usage');
  assert.equal(links.queryFromURL('/tools?tools_view=skills&q_library=&q_skill_usages=').dataset, 'skill_usages');
  delete global.location;
  const clock = new Date(2026, 9, 8, 15), next = new Date(2026, 9, 15, 15);
  const recipe = instantiateRecipe(queryRecipes.find(item => item.id === 'tool-failures'), 7, clock);
  const resolved = links.resolveRollingQuery(recipe.query, recipe.window, next);
  assert.deepEqual(resolved.grouping.source.where.filter(term => term.field === 'started_at').map(term => term.value), ["-7d", "+0s"]);
  assert.deepEqual(resolved.aggregations, recipe.query.aggregations);
  assert.equal(resolved.grouping.source.where.filter(term => term.field === 'error_count').length, 1);
  const writing = instantiateRecipe(queryRecipes.find(item => item.id === 'human-words'), 7, clock);
  assert.match(writing.url, /usage=writing/); assert.match(writing.url, /writing=messages/);
  for (const item of queryRecipes) {
    const query = instantiateRecipe(item, 7, clock).query;
    if (!query.grouping) core.toServerQuery(query, schemas[item.dataset]);
  }
});

test('rolling windows recognize whole local calendar spans across midnight and daylight saving', () => {
  const previousTZ = process.env.TZ;
  process.env.TZ = 'America/Denver';
  try {
    const window = { days: 7, field: 'started_at' };
    for (const date of [new Date(2026, 9, 8, 23, 59), new Date(2026, 2, 10), new Date(2026, 10, 3)]) {
      const query = links.resolveRollingQuery(core.EMPTY_QUERY, window, date);
      assert.equal(links.rollingQueryMatches(query, window), true, 'a saved window need not end on the current day');
      assert.equal(links.rollingQueryMatches({ ...query, offset: 20, orderBy: [{ field: 'duration_ms', dir: 'desc' }] }, window), true);
      assert.equal(links.rollingQueryMatches(query, { ...window, days: 30 }), false);
      assert.equal(links.rollingQueryMatches({ ...query, where: query.where.slice(1) }, window), false);
      const partial = { ...query, where: query.where.map(term => term.op === '>=' ? { ...term, value: new Date(Date.parse(term.value) + 3600000).toISOString() } : term) };
      assert.equal(links.rollingQueryMatches(partial, window), false, 'manual partial-day bounds stop being rolling');
    }
  } finally { if (previousTZ === undefined) delete process.env.TZ; else process.env.TZ = previousTZ; }
});

test('Home shows the Dashboard Catalog and retired study URLs open Home', async () => {
  const { browser, page, data } = await setup({ viewport: { width: 1470, height: 797 }, deviceScaleFactor: 2, reducedMotion: true });
  try {
    assert.equal(await page.locator('.home-empty small').innerText(), 'Collect cards that help you see your work from different angles');
    assert.equal(await page.getByRole('heading', { name: 'Dashboard Catalog', exact: true }).count(), 1);
    assert.equal(await page.getByRole('region', { name: 'Dashboard Catalog', exact: true }).count(), 1);
    assert.equal(await page.locator('.home-card-library-nav,a[href*="-review"]').count(), 0);
    await page.screenshot({ path: path.join(root, '.context/home-feedback-desktop.png') });
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok(await page.locator('.home-empty small').evaluate(el => el.getBoundingClientRect().right <= innerWidth));
    assert.ok(await page.locator('.home-conveyor-head').evaluate(el => el.scrollWidth <= el.clientWidth + 1));
    await page.screenshot({ path: path.join(root, '.context/home-feedback-mobile.png') });
    for (const category of ['human', 'model', 'context', 'error', 'tool', 'skills', 'research', 'cost', 'token', 'delegation']) {
      await page.goto(`http://home.test/home?cards=${category}-review`);
      await page.getByRole('heading', { name: 'Dashboard Catalog', exact: true }).waitFor();
      assert.equal(await page.locator('.human-review,.human-review-entry').count(), 0, category);
    }
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('Home nav and logo activate previews and leaving Home suspends scans and autoplay', async () => {
  const { browser, page, data } = await setup({ url: '/library', saved: true, clock: true });
  try {
    for (const target of ['.tabs [data-view="home"]', '.brand']) {
      await page.locator(target).click();
      assert.equal(new URL(page.url()).pathname, '/');
      assert.equal(new URL(page.url()).searchParams.has('q_library'), false, 'Home URLs must not resemble old Library links');
      await page.clock.runFor(5000);
      // Keep frames moving while the asynchronously arriving native covers render.
      for (let attempt = 0; attempt < 100; attempt++) {
        if (await page.evaluate(() => document.querySelector('.home-conveyor')?.getAttribute('aria-busy') === 'false' && Boolean(document.querySelector('.home-collection .query-result-summary')))) break;
        await page.clock.runFor(100);
      }
      assert.equal(await page.locator('.home-conveyor').getAttribute('aria-busy'), 'false');
      assert.ok(await page.locator('.home-collection .query-result-summary').count());
      const ids = await page.locator('.home-belt-row .query-card').evaluateAll(cards => cards.map(card => card.dataset.cardId));
      await page.mouse.move(5, 5);
      await page.locator('.tabs [data-view="library"]').click();
      await page.clock.runFor(1000);
      // The active Library may refresh its row page. Count Home's preview
      // execution paths, not that legitimate foreground request.
      const previewRequests = () => data.requests.filter(({ dataset, path }) => dataset !== 'library' || path.endsWith('/metrics') || path.endsWith('/query-v3')).length;
      const requests = previewRequests();
      await page.evaluate(() => window.dispatchEvent(new Event('pharos:data-refresh')));
      await page.clock.runFor(65000);
      assert.equal(previewRequests(), requests, 'hidden Home must not scan on a data refresh');
      assert.deepEqual(await page.locator('.home-belt-row .query-card').evaluateAll(cards => cards.map(card => card.dataset.cardId)), ids);
      assert.equal(await page.locator('.home-countdown').innerText(), 'Waiting');
    }
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('old root Library URLs and history links retain queries, searches and their Library labels', async () => {
  const query = { ...core.EMPTY_QUERY, where: [{ field: 'repository_name', op: '=', value: 'pharos' }] };
  const legacy = `/?q_library=${core.encodeQuery(query)}&search=indexing&case=1&fuzzy=0&view=conversation#library`;
  const { browser, page, data } = await setup({ url: legacy });
  try {
    assert.equal(new URL(page.url()).pathname, '/library');
    assert.equal(new URL(page.url()).hash, '#library');
    assert.equal(await page.locator('#library').getByRole('textbox', { name: 'Text for rule 1.1', exact: true }).inputValue(), 'indexing');
    assert.equal(await page.getByRole('button', { name: 'Conversations', exact: true }).getAttribute('aria-pressed'), 'true');
    assert.deepEqual(core.decodeQuery(new URL(page.url()).searchParams.get('q_library')).where, query.where);
    await page.evaluate(url => {
      localStorage.setItem('pharos-history-v1', JSON.stringify([{ url, title: 'Library · old saved search', at: Date.now() - 1000 }]));
      document.querySelector('#historyDrawer').classList.add('open'); renderHistory();
    }, legacy);
    await page.locator('#historyList .history-link').click();
    assert.equal(new URL(page.url()).pathname, '/library');
    assert.match(await page.locator('#historyList .history-title').first().innerText(), /^Library/);
    for (const old of ['/?q_library=', '/?search=indexing&kind=file&case=1']) {
      await page.evaluate(url => window.pharosNavigate(url), old);
      assert.equal(new URL(page.url()).pathname, '/library');
    }
    await page.locator('.tabs [data-view="home"]').click();
    assert.equal(new URL(page.url()).searchParams.has('search'), false);
    await page.locator('.tabs [data-view="library"]').click();
    await page.waitForFunction(() => new URL(location.href).searchParams.get('search') === 'indexing');
    assert.equal(await page.locator('#library').getByRole('textbox', { name: 'Search library', exact: true }).inputValue(), 'indexing');
    assert.equal(new URL(page.url()).searchParams.get('kind'), 'file');
    assert.equal(new URL(page.url()).searchParams.get('case'), '1');
    // Exercise a controlled Library → Home history transition; the earlier
    // route restorations also retain independent query/view history entries.
    await page.locator('.tabs [data-view="home"]').click();
    await page.waitForURL(url => url.pathname === '/');
    await page.goBack();
    await page.waitForURL(url => url.pathname === '/library');
    assert.equal(new URL(page.url()).searchParams.get('kind'), 'file');
    await page.goForward();
    await page.waitForURL(url => url.pathname === '/');
    assert.equal(await page.locator('#home').evaluate(home => home.classList.contains('active')), true);
    assert.equal(await page.evaluate(() => JSON.parse(localStorage.getItem('pharos-history-v1'))[0].title), 'Home');
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('showing tables serializes restored queries for Library, Usage, Writing and Tools tabs', async () => {
  const datasets = ['library', 'usage', 'writing', 'writing_messages', 'tools', 'tool_calls', 'skill_usages'];
  const queries = Object.fromEntries(datasets.map(dataset => [dataset, { ...core.EMPTY_QUERY, where: [{ field: 'repository_name', op: '=', value: `stored-${dataset}` }] }]));
  const localValues = Object.fromEntries(datasets.map(dataset => [`query-table:last:pharos_${dataset}`, queries[dataset]]));
  const { browser, page, data } = await setup({ localValues, preferenceValues: { 'pharos-show-nav-button': true } });
  const check = async dataset => {
    await waitForQuery(page, dataset, query => query.where.some(term => term.value === `stored-${dataset}`));
    assert.deepEqual(core.decodeQuery(new URL(page.url()).searchParams.get(`q_${dataset}`)).where, queries[dataset].where);
  };
  try {
    await page.locator('.tabs [data-view="library"]').click(); await check('library');
    const libraryURL = page.url();
    await page.getByRole('button', { name: 'Navigation bar', exact: true }).click();
    await page.locator('#bookmarkStar').click();
    await page.waitForFunction(() => window.pharosPrefs.get('pharos-bookmarks-v1', []).some(item => item.dataset === 'library' || item.url.startsWith('/library')));
    const libraryBookmark = await page.evaluate(() => window.pharosPrefs.get('pharos-bookmarks-v1', []).find(item => item.url.startsWith('/library')));
    assert.ok(new URL(libraryBookmark.url, page.url()).searchParams.get('q_library').startsWith('@'), 'bookmarks use a permanent snapshot');
    assert.deepEqual(core.normalizeQueryState(libraryBookmark.query), core.decodeQuery(new URL(libraryURL).searchParams.get('q_library')));
    await page.locator('.tabs [data-view="usage"]').click(); await check('usage');
    await page.getByRole('group', { name: 'Usage view', exact: true }).getByRole('button', { name: 'Human Words', exact: true }).click(); await check('writing');
    await page.getByRole('group', { name: 'List', exact: true }).getByRole('button', { name: 'Messages', exact: true }).click(); await check('writing_messages');
    await page.getByRole('group', { name: 'List', exact: true }).getByRole('button', { name: 'Conversations', exact: true }).click(); await check('writing');
    await page.locator('.tabs [data-view="tools"]').click(); await check('tool_calls');
    const toolView = page.getByRole('group', { name: 'Tool view', exact: true });
    await toolView.getByRole('button', { name: 'Summary', exact: true }).click(); await check('tools');
    await toolView.getByRole('button', { name: 'Skills', exact: true }).click(); await check('skill_usages');
    const incoming = { ...queries.library, where: [{ field: 'repository_name', op: '=', value: 'incoming-library' }] };
    await page.evaluate(url => window.pharosNavigate(url), links.queryURL('library', incoming));
    await waitForQuery(page, 'library', query => query.where.some(term => term.value === 'incoming-library'));
    await page.getByRole('button', { name: 'Edit workspace filters', exact: true }).click();
    await page.waitForFunction(() => [...document.querySelectorAll('#library .qt-qb input')].some(input => input.value === 'incoming-library'));
    assert.equal(await page.locator('#library .query-table-error').count(), 0);
    await page.screenshot({ path: path.join(root, '.context/review-incoming-query.png') });
    assert.deepEqual(core.decodeQuery(new URL(page.url()).searchParams.get('q_library')).where, incoming.where, 'route restoration must win over visibility synchronization');
    await page.goBack();
    assert.equal(await toolView.getByRole('button', { name: 'Skills', exact: true }).getAttribute('aria-pressed'), 'true');
    await check('skill_usages');
    await page.goForward();
    await waitForQuery(page, 'library', query => query.where.some(term => term.value === 'incoming-library'));
    await page.locator('.tabs [data-view="home"]').click();
    await page.locator('.tabs [data-view="library"]').click();
    await page.waitForFunction(() => new URL(location.href).searchParams.has('q_library'));
    assert.deepEqual(core.decodeQuery(new URL(page.url()).searchParams.get('q_library')).where, incoming.where);
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('query edits and saving after local midnight preserve a rolling window', async () => {
  const before = new Date('2026-10-08T23:59:00-06:00'), after = new Date('2026-10-09T00:01:00-06:00');
  const item = instantiateRecipe(queryRecipes.find(recipe => recipe.id === 'tool-failures'), 7, before);
  const source = item.query.grouping.source;
  const { browser, page, data } = await setup({ url: links.queryURL('tool_calls', source, item.window), now: before });
  try {
    await page.clock.setFixedTime(after);
    await page.getByRole('button', { name: 'Slowest calls', exact: true }).click();
    await waitForQuery(page, 'tool_calls', query => query.orderBy.some(sort => sort.field === 'duration_ms'));
    assert.equal(new URL(page.url()).searchParams.get('window_mode'), 'relative');
    assert.equal(new URL(page.url()).searchParams.get('window_days'), '7');
    assert.equal(new URL(page.url()).searchParams.get('window_field'), 'started_at');
    assert.equal(new URL(page.url()).searchParams.get('window_dataset'), 'tool_calls');
    await page.locator('#tools .record-search-editor summary').click();
    await page.locator('#tools .qt-qb').filter({ visible: true }).getByRole('button', { name: 'Save query', exact: true }).click();
    const name = page.locator('#tools .qt-qb-saved-input').filter({ visible: true });
    await name.fill('Failures after midnight'); await page.keyboard.press('Enter');
    await page.waitForFunction(() => window.pharosPrefs.get('pharos-bookmarks-v1', []).some(item => item.title === 'Failures after midnight'));
    const saved = await page.evaluate(() => window.pharosPrefs.get('pharos-bookmarks-v1', []).find(item => item.title === 'Failures after midnight'));
    assert.equal(new URL(saved.url, page.url()).searchParams.get('window_days'), '7');
    await page.evaluate(url => window.pharosNavigate(url), saved.url);
    await waitForQuery(page, 'tool_calls', query => query.where.some(term => term.field === 'started_at' && term.op === '>=' && term.value === '-7d'));
    assert.equal(new URL(saved.url, page.url()).searchParams.get('window_mode'), 'relative');
    const query = await readQuery(page, 'tool_calls');
    assert.deepEqual(query.where.filter(term => term.field === 'started_at').map(term => term.value), ["-7d", "+0s"], 'reopening retains relative dates');
    const partialDay = new Date(Date.parse(links.rollingBounds(7, after)[0]) + 3600000).toISOString();
    const position = query.where.findIndex(term => term.field === 'started_at' && term.op === '>=') + 1;
    await page.locator('#tools .record-search-editor').getByRole('textbox', { name: `Filter value ${position}`, exact: true }).fill(partialDay);
    await page.getByRole('button', { name: 'Search tool calls', exact: true }).click();
    await page.waitForFunction(() => !new URL(location.href).searchParams.has('window_days'));
    assert.equal(new URL(page.url()).searchParams.has('window_mode'), false);
    assert.equal(new URL(page.url()).searchParams.has('window_field'), false);
    assert.equal(new URL(page.url()).searchParams.has('window_dataset'), false, 'a manually changed date range becomes fixed');
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('bookmark migration preserves separately named saves of the same query and their default IDs', () => {
  const bookmarks = moduleOf('web/src/bookmarks.ts');
  const recipe = instantiateRecipe(queryRecipes.find(item => item.id === 'human-words'), 7);
  const values = new Map([
    ['query-table:saved:pharos_writing_messages', [
      { id: 'save-a', name: 'Writing practice', savedAt: 100, query: recipe.query },
      { id: 'save-b', name: 'My daily words', savedAt: 200, query: recipe.query },
    ]],
    ['query-table:default:pharos_writing_messages', 'save-a'],
  ]);
  const previousWindow = global.window, previousLocation = global.location;
  global.location = { origin: 'http://home.test' };
  global.window = { dispatchEvent() {}, pharosPrefs: {
    get: (key, fallback) => values.has(key) ? values.get(key) : fallback,
    set: (key, value) => values.set(key, value), remove: key => values.delete(key), keys: () => [],
  } };
  try {
    bookmarks.migrateSavedQueries();
    assert.deepEqual(bookmarks.savedQueries('writing_messages').map(item => [item.id, item.name]), [['save-a', 'Writing practice'], ['save-b', 'My daily words']]);
    assert.equal(values.get('query-table:default:pharos_writing_messages'), 'save-a');
    bookmarks.removeBookmark(bookmarks.readBookmarks()[0].id);
    bookmarks.migrateSavedQueries();
    assert.deepEqual(bookmarks.savedQueries('writing_messages').map(item => item.id), ['save-b']);
    assert.equal(values.has('query-table:default:pharos_writing_messages'), false);
  } finally { global.window = previousWindow; global.location = previousLocation; }
});

test('changing a saved range preserves query structure, indexed search context, and unrelated URL controls', () => {
  global.location = { origin: 'http://home.test' };
  try {
    const query = { ...core.EMPTY_QUERY, offset: 100, where: [
      { field: 'activity_at', op: '>', value: '2025-01-01T00:00:00Z' },
      { field: 'activity_at', op: '<=', value: '2025-12-31T00:00:00Z' },
      { any: [{ field: 'repository_name', op: '=', value: 'pharos' }, { field: 'repository_name', op: '=', value: 'query-table' }] },
    ], aggregations: [{ id: 'cost', op: 'sum', field: 'cost_usd', groupBy: ['repository_name'] }] };
    const original = `${links.queryURL('library', query)}&search=indexing&case=1&fuzzy=0&heat_grain=week#library`;
    const result = links.updateQueryWindowURL(original, 30), spec = links.queryFromURL(result), url = new URL(result, location.origin);
    assert.deepEqual(spec.window, { days: 30, field: 'activity_at', mode: 'relative' });
    assert.deepEqual(spec.query.where.filter(term => term.field === 'activity_at').map(term => [term.op, term.value]), [['>=', '-30d'], ['<', '+0s']]);
    assert.deepEqual(spec.query.where.find(term => term.any), query.where[2]);
    assert.deepEqual(spec.query.aggregations, query.aggregations);
    assert.equal(spec.query.offset, 0);
    for (const [key, value] of [['search', 'indexing'], ['case', '1'], ['fuzzy', '0'], ['heat_grain', 'week']]) assert.equal(url.searchParams.get(key), value);
    assert.equal(url.hash, '#library');
    assert.equal(categoryForQuery(spec.dataset, spec.query), 'cost');
  } finally { delete global.location; }
});

test('homepage search exposes the full search options and opens a clean Library route', async () => {
  const { browser, page, data } = await setup();
  try {
    assert.equal(await page.locator('#home').evaluate(element => element.classList.contains('active')), true);
    const trigger = page.getByRole('button', { name: 'Search your library', exact: true });
    assert.equal(await trigger.getAttribute('aria-expanded'), 'false');
    await trigger.click();
    await page.waitForFunction(() => document.activeElement?.getAttribute('aria-label') === 'Text for rule 1.1');
    const target = page.getByRole('combobox', { name: 'Search target', exact: true });
    await target.selectOption('file');
    await page.getByRole('textbox', { name: 'Search library', exact: true }).fill('parser.go');
    await target.selectOption('prose');
    await page.getByRole('textbox', { name: 'Text for rule 1.1', exact: true }).fill('query plan');
    await page.getByRole('button', { name: /^Match options/ }).click();
    await page.getByRole('checkbox', { name: 'Include word variations', exact: true }).uncheck();
    await page.getByRole('checkbox', { name: 'Match letter case', exact: true }).check();
    await page.getByRole('checkbox', { name: 'Keep phrase punctuation and spacing', exact: true }).check();
    await page.getByRole('button', { name: 'Search messages', exact: true }).click();
    await page.waitForURL('**/library?**');
    const params = new URL(page.url()).searchParams, draft = JSON.parse(params.get('text_builder'));
    assert.equal(draft.root.children[0].text, 'query plan');
    assert.equal(draft.fuzzy, false); assert.equal(draft.caseSensitive, true); assert.equal(draft.separators, true);
    assert.equal(params.has('kind'), false); assert.equal(params.has('search'), false);
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('collection unifies saved queries, edits metadata, reorders, deletes, and preserves empty bookmarks', async () => {
  const { browser, page, data } = await setup({ saved: true });
  try {
    await page.locator('.home-collection .query-card').waitFor();
    assert.equal(await page.locator('.home-collection .query-title').innerText(), 'My words');
    assert.equal(await page.locator('.home-collection .query-category').innerText(), 'Human Words');
    const originalURL = await page.locator('.home-collection .query-card a').getAttribute('href');
    await page.locator('.home-collection .query-card').hover();
    await page.getByRole('button', { name: 'Edit My words' }).click();
    await page.getByRole('textbox', { name: 'Title', exact: true }).fill('Writing practice');
    await page.getByRole('textbox', { name: 'Description', exact: true }).fill('The words I actually typed.');
    await page.getByRole('button', { name: 'Save changes' }).click();
    assert.equal(await page.locator('.home-collection .query-card a').getAttribute('href'), originalURL, 'editing metadata preserves a fixed saved range');
    const first = page.locator('.home-belt-row .query-card:not([data-xray])').first();
    const title = await first.locator('.query-title').innerText();
    const category = await first.getAttribute('data-category'), eyebrow = await first.locator('.query-category').innerText();
    await first.getByRole('button', { name: `Collect ${title}`, exact: true }).click();
    await page.waitForFunction(() => document.querySelectorAll('.home-collection .query-card').length === 2);
    const collected = page.locator('.home-collection .query-card').nth(1);
    assert.equal(await collected.getAttribute('data-category'), category);
    assert.equal(await collected.locator('.query-category').innerText(), eyebrow);
    await collected.locator('a').focus(); await page.keyboard.press('Alt+ArrowLeft');
    assert.equal(await page.locator('.home-collection .query-title').first().innerText(), title);
    await page.getByRole('button', { name: `Remove ${title}`, exact: true }).click();
    await page.getByRole('button', { name: 'Undo', exact: true }).click();
    await page.waitForTimeout(350);
    assert.equal(data.preferences['pharos-bookmarks-v1'].length, 2);
    assert.equal(data.preferences['pharos-bookmarks-v1'][1].savedQueryId, 'original-query');
    assert.equal(data.preferences['query-table:default:pharos_writing_messages'], 'original-query');
    data.empty = true;
    await page.reload();
    await page.waitForFunction(() => document.querySelector('.home-conveyor')?.getAttribute('aria-busy') === 'false' && document.querySelector('.home-discovery-empty'), null, {timeout:5000}).catch(async () => assert.fail(await page.locator('.home-conveyor').innerText()));
    assert.equal(await page.locator('.home-collection .query-card').count(), 2);
    await page.waitForFunction(() => [...document.querySelectorAll('.home-collection .query-card')].every(node => node.querySelector('.query-preview-status') && !node.querySelector('.query-preview-unavailable')), null, {timeout:5000}).catch(async () => assert.fail(await page.locator('.home-collection').innerText()));
    assert.equal(await page.locator('.home-belt-row .query-card:not([data-xray])').count(), 0);
    assert.equal(data.preferences['pharos-bookmarks-v1'].length, 2);
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('the card editor updates its rolling timescale, preview, saved query and navigation together', async () => {
  const { browser, page, data } = await setup({ saved: true });
  global.location = { origin: 'http://home.test' };
  try {
    await page.setViewportSize({ width: 2133, height: 1075 });
    await page.getByRole('button', { name: 'Pause suggestions' }).click();
    const card = page.locator('.home-collection .query-card').first();
    await page.waitForFunction(() => document.querySelector('.home-collection .query-result-summary')?.textContent === '938 words');
    const originalID = await card.getAttribute('data-card-id');
    await card.hover(); await card.getByRole('button', { name: 'Edit My words' }).click();
    const windowSelect = page.getByRole('combobox', { name: 'Time window' });
    assert.equal(await windowSelect.inputValue(), '');
    assert.deepEqual(await windowSelect.locator('option').evaluateAll(options => options.map(option => option.value)), ['', '1', '7', '30', '90', '180', '365', 'all']);
    await windowSelect.selectOption('1');
    assert.equal(await windowSelect.evaluate(element => getComputedStyle(element).backgroundRepeat), 'no-repeat');
    await page.screenshot({ path: path.join(root, '.context/homepage-design/home-editor-timescale.png') });
    await page.setViewportSize({ width: 390, height: 1050 });
    const dialog = await page.getByRole('dialog', { name: 'Make this view yours' }).boundingBox();
    assert.ok(dialog.x >= 0 && dialog.x + dialog.width <= 390, 'the editor stays within a phone viewport');
    await page.screenshot({ path: path.join(root, '.context/homepage-design/home-editor-timescale-narrow.png') });
    await page.setViewportSize({ width: 2133, height: 1075 });
    await page.getByRole('button', { name: 'Save changes' }).click();
    await page.waitForFunction(() => document.querySelector('.home-collection .query-result-summary')?.textContent === '122 words');
    assert.equal(await card.getAttribute('data-card-id'), originalID);
    assert.equal(await card.locator('.query-window').innerText(), 'Last 1d');
    assert.equal(await card.locator('.query-cover-columns > span').count(), 2, 'an elapsed one-day lookback spans yesterday and today');
    assert.equal(await card.locator('.query-category').innerText(), 'Human Words');
    await page.waitForTimeout(350);
    const saved = data.preferences['pharos-bookmarks-v1'].find(item => item.id === originalID);
    assert.equal(saved.savedQueryId, 'original-query');
    assert.equal(data.preferences['query-table:default:pharos_writing_messages'], 'original-query');
    const href = await card.locator('a').getAttribute('href');
    assert.equal(new URL(href, 'http://home.test').searchParams.get('window_days'), '1');
    assert.equal(categoryForQuery('writing_messages', core.decodeQuery(new URL(href, 'http://home.test').searchParams.get('q_writing_messages'))), 'human');
    await card.getByRole('button', { name: 'Edit My words' }).click();
    assert.equal(await windowSelect.inputValue(), '1');
    await windowSelect.selectOption('90');
    await page.getByRole('button', { name: 'Cancel', exact: true }).click();
    assert.equal(await card.locator('a').getAttribute('href'), href, 'cancel leaves the saved time window intact');
    await card.locator('a').click();
    await page.getByRole('button', { name: 'Records', exact: true }).first().waitFor();
    assert.deepEqual(links.queryFromURL(page.url()).query.grouping, links.queryFromURL(href).query.grouping, 'opening the card preserves its grouped destination');
    assert.equal(new URL(page.url()).pathname, '/usage');
    assert.equal(new URL(page.url()).searchParams.get('window_days'), '1');
    await page.goto('http://home.test/');
    await page.waitForFunction(() => document.querySelector('.home-collection .query-result-summary')?.textContent === '122 words');
    await card.hover(); await card.getByRole('button', { name: 'Edit My words' }).click();
    assert.equal(await windowSelect.inputValue(), '1', 'the updated window survives a reload');
    await page.getByRole('button', { name: 'Cancel', exact: true }).click();
    assert.deepEqual(data.errors, []);
  } finally { delete global.location; await browser.close(); }
});

test('the card editor saves extended and all-time windows with matching previews and navigation', async () => {
  const { browser, page, data } = await setup({ saved: true });
  try {
    for (const [days, words] of [[200, 1000], [400, 2000]]) {
      const date = new Date(Date.now() - days * 86400000);
      data.rows.writing_messages.push({ ...data.rows.writing_messages[0], id: `old-${days}`, sent_at: date.toISOString(), day: date.toISOString().slice(0, 10), typed_words: words });
    }
    const card = page.locator('.home-collection .query-card').first();
    const windowSelect = page.getByRole('combobox', { name: 'Time window' });
    for (const [value, label, summary] of [['180', 'Last 180d', '938 words'], ['365', 'Last year', '1,938 words'], ['all', 'All time', '3,938 words']]) {
      await card.hover(); await card.getByRole('button', { name: 'Edit My words' }).click();
      await windowSelect.selectOption({ label });
      await page.getByRole('button', { name: 'Save changes' }).click();
      await page.waitForFunction(expected => document.querySelector('.home-collection .query-result-summary')?.textContent === expected, summary);
      assert.equal(await card.locator('.query-window').innerText(), label);
      const url = new URL(await card.locator('a').getAttribute('href'), 'http://home.test');
      assert.equal(url.searchParams.get('window_days'), value === 'all' ? null : value);
      await card.hover(); await card.getByRole('button', { name: 'Edit My words' }).click();
      assert.equal(await windowSelect.inputValue(), value);
      await page.getByRole('button', { name: 'Cancel', exact: true }).click();
    }
    await page.screenshot({ path: path.join(root, '.context/time-window-all-time.png') });
    await page.reload();
    await page.waitForFunction(() => document.querySelector('.home-collection .query-result-summary')?.textContent === '3,938 words');
    await card.hover(); await card.getByRole('button', { name: 'Edit My words' }).click();
    assert.equal(await windowSelect.inputValue(), 'all', 'all time survives a reload');
    await page.screenshot({ path: path.join(root, '.context/time-window-editor.png') });
    await page.getByRole('button', { name: 'Cancel', exact: true }).click();
    await card.locator('a').click();
    await waitForQuery(page, 'writing_messages', query => !(query.grouping?.source ?? query).where.some(term => term.field === 'sent_at'));
    assert.equal(new URL(page.url()).searchParams.has('window_days'), false);
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('carousel advances a page every thirty seconds and pauses during interaction or reduced motion', async () => {
  const { browser, page, data } = await setup({ clock: true });
  try {
    const initial = await page.locator('.home-belt-row .query-card:not([data-xray])').first().getAttribute('data-card-id');
    assert.match(await page.locator('.home-countdown').innerText(), /More in \d+s/);
    await page.clock.fastForward(30_250);
    await page.locator('.home-belt-row.sliding').waitFor();
    assert.equal(await page.locator('.home-belt-row.sliding').evaluate(row => getComputedStyle(row).transitionDuration), '2.04s');
    await page.clock.runFor(2200);
    assert.notEqual(await page.locator('.home-belt-row .query-card:not([data-xray])').first().getAttribute('data-card-id'), initial);
    await page.getByRole('button', { name: 'Pause suggestions' }).click();
    const paused = await page.locator('.home-belt-row .query-card:not([data-xray])').first().getAttribute('data-card-id');
    await page.clock.fastForward(60_000);
    assert.equal(await page.locator('.home-belt-row .query-card:not([data-xray])').first().getAttribute('data-card-id'), paused);
    await page.getByRole('button', { name: 'Play suggestions' }).click();
    await page.locator('.home-belt-row .query-card:not([data-xray])').first().hover();
    await page.clock.fastForward(60_000);
    assert.equal(await page.locator('.home-belt-row .query-card:not([data-xray])').first().getAttribute('data-card-id'), paused);
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
  const reduced = await setup({ reducedMotion: true });
  try { assert.equal(await reduced.page.getByRole('button', { name: 'Play suggestions' }).count(), 1); }
  finally { await reduced.browser.close(); }
});

test('the loading belt retains its fixed middle while ready batches grow in pairs and introduce x-rays', async () => {
  const { browser, page, data } = await setup();
  const real = page.locator('.home-belt-row .query-card:not([data-xray])');
  const releaseOne = async () => {
    for (let attempt = 0; !data.pending.length && attempt < 50; attempt++) await page.waitForTimeout(20);
    assert.ok(data.pending.length, 'a preview batch should be waiting for its response');
    data.pending.shift().release();
    await page.waitForTimeout(400);
  };
  try {
    await page.setViewportSize({ width: 2133, height: 1075 });
    await page.getByRole('button', { name: 'Pause suggestions' }).click();
    data.holdPreviews = true;
    await page.getByRole('button', { name: 'Refresh suggestions' }).click();
    await page.mouse.move(5, 5);
    await page.waitForFunction(() => document.querySelectorAll('.home-belt-row .query-card').length === 0);
    for (let attempt = 0; !await real.count() && attempt < 24; attempt++) await releaseOne();
    assert.ok(await real.count(), `No preview became ready after releasing batches: ${await page.locator('.home-conveyor').innerText()}`);
    const seedCard = page.locator('[data-belt-position="0"] .query-card');
    const seed = await seedCard.getAttribute('data-card-id');
    const lean = await seedCard.evaluate(card => card.style.getPropertyValue('--query-lean'));
    for (let attempt = 0; await page.locator('.home-belt-row .query-card').count() < 7 && attempt < 30; attempt++) {
      assert.equal(await page.locator('.home-belt-row .query-card').count() % 2, 1, 'incomplete ready batches join the belt in pairs');
      const origin = await seedCard.boundingBox();
      assert.ok(Math.abs(origin.x + origin.width / 2 - 2133 / 2) < 1, 'new batches keep the seed in the middle');
      assert.equal(await seedCard.getAttribute('data-card-id'), seed);
      assert.equal(await seedCard.evaluate(card => card.style.getPropertyValue('--query-lean')), lean);
      await releaseOne();
    }
    assert.equal(await real.count(), 5, 'a wide screen reads five cards');
    assert.ok(await page.locator('.home-belt-row .query-xray-open').count() >= 2);
    await page.screenshot({ path: path.join(root, '.context/homepage-design/home-belt-loading.png') });
    data.holdPreviews = false;
    for (const pending of data.pending.splice(0)) pending.release();
    await page.waitForFunction(() => document.querySelector('.home-conveyor').getAttribute('aria-busy') === 'false');
    assert.deepEqual(data.errors, []);
  } finally {
    data.holdPreviews = false; for (const pending of data.pending.splice(0)) pending.release();
    await browser.close();
  }
});

test('clicking or keyboard-activating either x-ray brings that card into the middle', async () => {
  const { browser, page, data } = await setup();
  try {
    await page.setViewportSize({ width: 2133, height: 1075 });
    await page.getByRole('button', { name: 'Pause suggestions' }).click();
    for (const [position, keyboard] of [[-3, false], [3, true]]) {
      const side = page.locator(`.home-belt-slot[data-belt-position="${position}"]`);
      const id = await side.locator('.query-card').getAttribute('data-card-id');
      const reveal = side.getByRole('button', { name: /^Bring .* into view$/ });
      if (keyboard) { await reveal.focus(); await page.keyboard.press('Enter'); } else await reveal.click();
      await page.locator('.home-belt-row.sliding').waitFor();
      await page.waitForFunction(() => !document.querySelector('.home-belt-row.sliding'));
      const middle = page.locator('.home-belt-slot[data-belt-position="0"] .query-card');
      assert.equal(await middle.getAttribute('data-card-id'), id);
      assert.equal(await middle.getAttribute('data-xray'), null);
      assert.equal(await middle.locator('a').evaluate(link => document.activeElement === link), true);
      assert.equal(new URL(page.url()).pathname, '/', 'revealing a side card keeps you on the homepage');
    }
    await page.setViewportSize({ width: 390, height: 1050 });
    await page.waitForFunction(() => document.querySelectorAll('.home-belt-row .query-card:not([data-xray])').length === 1);
    const side = page.locator('.home-belt-slot[data-belt-position="1"]'), id = await side.locator('.query-card').getAttribute('data-card-id');
    await side.getByRole('button', { name: /^Bring .* into view$/ }).click();
    await page.waitForFunction(() => !document.querySelector('.home-belt-row.sliding'));
    assert.equal(await page.locator('.home-belt-slot[data-belt-position="0"] .query-card').getAttribute('data-card-id'), id);
    assert.equal(await page.evaluate(() => document.querySelector('main').scrollWidth <= document.querySelector('main').clientWidth), true);
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('conveyor slides in both directions and wraps while side cards show only evidence', async () => {
  const { browser, page, data } = await setup();
  try {
    await page.setViewportSize({ width: 2133, height: 1075 });
    await page.getByRole('button', { name: 'Pause suggestions' }).click();
    const cards = page.locator('.home-belt-row .query-card:not([data-xray])');
    const initial = await cards.evaluateAll(elements => elements.map(element => element.dataset.cardId));
    assert.equal(initial.length, 5, 'a wide screen reads five cards');
    const arriving = page.locator('.home-belt-slot[data-belt-position="3"] .query-card');
    const next = await arriving.getAttribute('data-card-id');
    const before = await arriving.boundingBox();
    await page.getByRole('button', { name: 'Next five suggestions' }).click();
    await page.locator('.home-belt-row.sliding').waitFor();
    await page.waitForTimeout(90);
    const during = await arriving.boundingBox();
    assert.ok(during.x < before.x - 10 && during.x > before.x - 5 * (before.width + 18), 'the same card should travel continuously into the reading area');
    await page.waitForFunction(() => !document.querySelector('.home-belt-row.sliding'));
    assert.equal(await cards.first().getAttribute('data-card-id'), next);
    await page.locator('.home-conveyor').focus(); await page.keyboard.press('ArrowLeft');
    await page.locator('.home-belt-row.sliding').waitFor();
    await page.waitForFunction(() => !document.querySelector('.home-belt-row.sliding'));
    assert.equal(await cards.first().getAttribute('data-card-id'), initial[0]);
    await page.getByRole('button', { name: 'Previous five suggestions' }).click();
    await page.waitForFunction(() => !document.querySelector('.home-belt-row.sliding'));
    await page.getByRole('button', { name: 'Next five suggestions' }).click();
    await page.waitForFunction(() => !document.querySelector('.home-belt-row.sliding'));
    assert.equal(await cards.first().getAttribute('data-card-id'), initial[0], 'wrapping back and forward should restore the original cards');
    const belt = await page.locator('.home-belt-viewport').boundingBox();
    assert.ok(belt.x < 1 && Math.abs(belt.width - 2133) < 2, 'the conveyor must span the screen');
    const ghosts = await page.locator('.home-belt-row .query-card[data-xray]').evaluateAll(elements => elements.map(element => ({
      x: element.getBoundingClientRect().x, opacity: getComputedStyle(element.querySelector('.query-title')).opacity,
      hidden: element.getAttribute('aria-hidden'), focusable: element.querySelector('a').tabIndex, hasMarks: Boolean(element.querySelector('i,.qt-metric-mark,.qt-metric-bar-fill,.qt-metric-bar-track,.query-scalar-value,.query-headline-value,.human-study-headline>strong,.human-study-sparkline path,td')), revealable: Boolean(element.querySelector('.query-xray-open')),
    })));
    assert.ok(ghosts.some(card => card.x > 0 && card.x < 468 && card.hasMarks));
    assert.ok(ghosts.some(card => card.x > 1648 && card.x < 2133 && card.hasMarks));
    assert.ok(ghosts.every(card => card.opacity === '0' && card.focusable === -1 && (card.revealable ? card.hidden === null : card.hidden === 'true')));
    assert.equal(await page.locator('.home-conveyor-foot').count(), 0);
    assert.equal(await page.locator('.home-conveyor-head .home-countdown').count(), 1);
    assert.equal(await page.evaluate(() => document.querySelector('main').scrollWidth <= document.querySelector('main').clientWidth), true);
    await page.mouse.move(5, 5); await page.evaluate(() => document.querySelector('main').scrollTop = 0);
    await page.screenshot({ path: path.join(root, '.context/homepage-design/home-feedback-wide.png'), fullPage: true });
    await page.setViewportSize({ width: 390, height: 1050 });
    await page.waitForFunction(() => document.querySelectorAll('.home-belt-row .query-card:not([data-xray])').length === 1);
    assert.equal(await cards.count(), 1, 'narrow screens retain one fully readable card between the side previews');
    assert.equal(await page.evaluate(() => document.querySelector('main').scrollWidth <= document.querySelector('main').clientWidth), true);
    await page.screenshot({ path: path.join(root, '.context/homepage-design/home-feedback-narrow.png'), fullPage: true });
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('the sliding belt stays usable: arriving cards open, moves retarget, and asked-for moves run at twice the timed pace', async () => {
  const { browser, page, data } = await setup();
  const sliding = page.locator('.home-belt-row.sliding');
  const middle = () => page.locator('.home-belt-slot[data-belt-position="0"] .query-card').getAttribute('data-card-id');
  const settled = () => page.waitForFunction(() => !document.querySelector('.home-belt-row.sliding'));
  try {
    await page.getByRole('button', { name: 'Pause suggestions' }).click();
    assert.equal(await page.locator('.home-belt-row .query-card:not([data-xray])').count(), 3);
    const initial = await middle();
    await page.getByRole('button', { name: 'Next three suggestions' }).click();
    await sliding.waitFor();
    assert.equal(await sliding.evaluate(row => getComputedStyle(row).transitionDuration), '1.02s');
    // The first card to cross into the reading area opens before the belt stops.
    const arriving = page.locator('.home-belt-slot[data-belt-position="2"] .query-card:not([data-xray])');
    await arriving.waitFor({ timeout: 900 });
    assert.equal(await sliding.count(), 1, 'the card should join the reading area while the belt still moves');
    const hit = await arriving.evaluate(card => { const box = card.getBoundingClientRect(), target = document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2); return { card: card.contains(target), link: Boolean(target?.closest('a.query-card-open')), pointer: getComputedStyle(card).pointerEvents }; });
    // Hit-testing the moving card, as hover and clicks do, reaches its link.
    assert.deepEqual(hit, { card: true, link: true, pointer: 'auto' });
    // Asking to go back mid-slide reverses from wherever the belt has reached.
    await page.getByRole('button', { name: 'Previous three suggestions' }).click();
    await settled();
    assert.equal(await middle(), initial);
    // Asking again mid-slide carries the belt on past the first destination.
    await page.getByRole('button', { name: 'Next three suggestions' }).click();
    await sliding.waitFor();
    await page.getByRole('button', { name: 'Next three suggestions' }).click();
    const destination = await page.locator('.home-belt-slot[data-belt-position="6"] .query-card').getAttribute('data-card-id');
    await settled();
    assert.equal(await middle(), destination);
    // An x-ray clicked mid-slide, once it comes onscreen, becomes the new destination.
    await page.getByRole('button', { name: 'Next three suggestions' }).click();
    const ghost = page.locator('.home-belt-slot[data-belt-position="5"]'), revealed = await ghost.locator('.query-card').getAttribute('data-card-id');
    await ghost.locator('.query-xray-open').waitFor({ timeout: 900 });
    assert.equal(await sliding.count(), 1);
    await ghost.locator('.query-xray-open').evaluate(button => button.click());
    await settled();
    assert.equal(await middle(), revealed);
    // Five cards need room for a glimpse of their neighbors as well.
    for (const [width, count] of [[1440, 5], [1380, 3], [2133, 5], [1180, 3]]) {
      await page.setViewportSize({ width, height: 1000 });
      await page.waitForFunction(count => document.querySelectorAll('.home-belt-row .query-card:not([data-xray])').length === count, count);
    }
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('reversing or repeating moves mid-slide keeps the reading area filled and the belt bounded', async () => {
  const { browser, page, data } = await setup();
  const settled = () => page.waitForFunction(() => !document.querySelector('.home-belt-row.sliding'), null, { timeout: 15000 });
  // Samples the reading area every frame while the belt slides: each card spot
  // must have a card under it, and the belt must not mount runaway slots.
  const watch = () => page.evaluate(() => { window.__belt = { blank: 0, frames: 0, slots: 0 }; const row = document.querySelector('.home-belt-row'); const view = document.querySelector('.home-belt-viewport').getBoundingClientRect();
    (function sample() {
      const card = row.querySelector('.query-card:not([data-xray])')?.getBoundingClientRect(), stride = parseFloat(getComputedStyle(document.querySelector('.home-conveyor')).getPropertyValue('--query-card-stride'));
      // A spot is blank when no mounted card covers it; gaps between moving cards are not.
      const slots = [...row.querySelectorAll('.home-belt-slot')].map(slot => slot.getBoundingClientRect());
      if (card) for (const dx of [-stride, 0, stride]) { const x = view.x + view.width / 2 + dx; window.__belt.blank += slots.some(slot => slot.left - 20 <= x && x <= slot.right + 20) ? 0 : 1; }
      window.__belt.frames++; window.__belt.slots = Math.max(window.__belt.slots, row.querySelectorAll('.home-belt-slot').length);
      if (row.classList.contains('sliding')) window.__belt.started = true;
      if (window.__belt.frames < 900 && (row.classList.contains('sliding') || !window.__belt.started)) requestAnimationFrame(sample);
    })(); });
  try {
    await page.getByRole('button', { name: 'Pause suggestions' }).click();
    await page.mouse.move(5, 5);
    const resting = await page.locator('.home-belt-slot').count();
    // Out three pages, then back two before the belt arrives.
    await watch();
    const next = page.getByRole('button', { name: 'Next three suggestions' }), previous = page.getByRole('button', { name: 'Previous three suggestions' });
    for (const button of [next, next, next]) await button.click();
    await page.waitForTimeout(450);
    for (const button of [previous, previous]) await button.click();
    await settled();
    let belt = await page.evaluate(() => window.__belt);
    assert.ok(belt.started && belt.frames > 30);
    assert.equal(belt.blank, 0, `cards vanished from the reading area mid-slide: ${JSON.stringify(belt)}`);
    // Held arrow key: repeats queue at most a short lead over the belt.
    await page.locator('.home-conveyor').focus();
    await watch();
    for (let press = 0; press < 60; press++) { await page.keyboard.press('ArrowRight'); await page.waitForTimeout(25); }
    await settled();
    belt = await page.evaluate(() => window.__belt);
    assert.equal(belt.blank, 0, JSON.stringify(belt));
    assert.ok(belt.slots <= resting + 20, `a held key mounted ${belt.slots} slots (resting belt: ${resting})`);
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('a number in an x-ray leaves a block in its card tone', async () => {
  const { browser, page, data } = await setup({ viewport: { width: 2133, height: 1075 } });
  try {
    await page.getByRole('button', { name: 'Pause suggestions' }).click();
    const blocks = async () => page.locator('.home-belt-row :is(.query-headline-value,.query-scalar-value,.human-study-headline>strong)').evaluateAll(values => values.map(value => {
      const block = getComputedStyle(value, '::after'), card = value.closest('.query-card'), cover = value.getBoundingClientRect();
      return { xray: card.hasAttribute('data-xray'), opacity: Number(block.opacity), color: block.backgroundColor, tone: getComputedStyle(card).getPropertyValue('--query-tone'), width: cover.width };
    }));
    let found = await blocks();
    for (let attempt = 0; !found.some(item => item.xray) && attempt < 12; attempt++) {
      await page.getByRole('button', { name: 'Next five suggestions' }).click();
      await page.waitForFunction(() => !document.querySelector('.home-belt-row.sliding'));
      found = await blocks();
    }
    assert.ok(found.some(item => item.xray), 'the fixture belt should show a number in an x-ray');
    // Cards whose groups collapse to a bare number are kept apart like any other kind.
    const numbers = await page.locator('.home-belt-slot').evaluateAll(slots => slots.map(slot => Boolean(slot.querySelector('.query-compact-metric.is-scalar'))));
    assert.ok(numbers.filter(Boolean).length >= 2);
    assert.ok(numbers.every((number, index) => !number || !numbers[index + 1]), numbers.map(Number).join(''));
    for (const item of found) assert.ok(item.xray ? item.opacity > .4 && item.width > 10 : item.opacity === 0, JSON.stringify(item));
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('dragging saves the exact preview query and collection remains contained on narrow screens', async () => {
  const { browser, page, data } = await setup();
  global.location = { origin: 'http://home.test' };
  try {
    for (let i = 0; i < 4; i++) {
      const id = await page.locator('.home-belt-row .query-card:not([data-xray])').first().getAttribute('data-card-id');
      const card = page.locator(`.home-belt-row .query-card:not([data-xray])[data-card-id="${id}"]`);
      const href = await card.locator('a').getAttribute('href');
      await card.dragTo(page.locator('.home-collection'));
      await page.waitForFunction(count => document.querySelectorAll('.home-collection .query-card').length === count, i + 1);
      // Saving promotes the URL to an immutable reference; the query is exact.
      const saved = await page.evaluate(id => window.pharosPrefs.get('pharos-bookmarks-v1', []).filter(item => item.templateId === id), id);
      assert.equal(saved.length, 1);
      assert.equal(core.encodeQuery(saved[0].query), core.encodeQuery(links.queryFromURL(href).query));
      await page.waitForFunction(id => !document.querySelector(`.home-belt-row .query-card[data-card-id="${id}"]`), id);
    }
    const dir = path.join(root, '.context/homepage-design'); fs.mkdirSync(dir, { recursive: true });
    await page.mouse.move(5, 5); await page.evaluate(() => document.querySelector('main').scrollTop = 0); await page.screenshot({ path: path.join(dir, 'home-implemented.png'), fullPage: true });
    for (const width of [600, 390, 320]) {
      await page.setViewportSize({ width, height: 940 });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    }
    await page.evaluate(() => document.querySelector('main').scrollTop = 0);
    await page.screenshot({ path: path.join(dir, 'home-implemented-narrow.png'), fullPage: true });
    const href = await page.locator('.home-collection .query-card a').first().getAttribute('href');
    await page.locator('.home-collection .query-card a').first().click();
    assert.equal(new URL(page.url()).pathname, new URL(href, 'http://home.test').pathname);
    await page.waitForTimeout(250);
    assert.equal(new URL(page.url()).searchParams.get('window_days'), new URL(href, 'http://home.test').searchParams.get('window_days'));
    assert.deepEqual(data.errors, []);
  } finally { delete global.location; await browser.close(); }
});

test('compact daily charts retain visible marks in ninety-day windows at narrow card widths', async () => {
  const { browser, page, data } = await setup();
  try {
    const saved = [['human-words', 90], ['tool-failures', 7], ['usage-models', 30], ['correction-phrases', 30]].map(([id, days]) => {
      const item = instantiateRecipe(queryRecipes.find(recipe => recipe.id === id), days);
      return { id: item.id, title: item.title, description: item.description, url: item.url, dataset: item.dataset, savedQueryId: item.id, templateId: item.instanceId };
    });
    await page.evaluate(items => { window.pharosPrefs.set('pharos-bookmarks-v1', items); window.dispatchEvent(new Event('pharos:bookmarks')); }, saved);
    const chart = page.locator('.home-collection [data-card-id="human-words"] .query-cover-columns');
    await chart.locator('i').first().waitFor();
    // A rolling -90d window touches the partial starting date and today.
    assert.equal(await chart.locator(':scope > span').count(), 91);
    assert.deepEqual(await page.locator('.home-collection .query-category').allTextContents(), ['Human Words', 'Error Patterns', 'Model Choice', 'Steering']);
    const colors = await page.locator('.home-collection .query-category').evaluateAll(labels => labels.map(label => getComputedStyle(label).color));
    assert.equal(new Set(colors).size, 4, 'different subjects use distinct category colors');
    await page.setViewportSize({ width: 1180, height: 1120 });
    await page.mouse.move(5, 5); await page.evaluate(() => document.querySelector('main').scrollTop = 0);
    await page.screenshot({ path: path.join(root, '.context/homepage-design/home-implemented.png'), fullPage: true });
    for (const width of [390, 320]) {
      await page.setViewportSize({ width, height: 1120 });
      const bounds = await chart.locator('i').first().boundingBox();
      assert.ok(bounds.width > 0 && bounds.height > 0, 'matching observations must remain visible in the chart');
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
      if (width === 390) await page.screenshot({ path: path.join(root, '.context/homepage-design/home-implemented-narrow.png'), fullPage: true });
    }
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('homepage fits text search, centers wrapping collection rows, and contains the full-width belt', async () => {
  const { browser, page, data } = await setup({ viewport: { width: 2133, height: 1075 }, deviceScaleFactor: 1.8 });
  const bookmarks = queryRecipes.slice(0, 8).map(recipe => {
    const item = instantiateRecipe(recipe, 7);
    return { id: item.id, title: item.title, description: item.description, url: item.url, dataset: item.dataset, savedQueryId: item.id, templateId: item.instanceId };
  });
  const collect = async count => {
    await page.evaluate(items => { window.pharosPrefs.set('pharos-bookmarks-v1', items); window.dispatchEvent(new Event('pharos:bookmarks')); }, bookmarks.slice(0, count));
    await page.waitForFunction(count => document.querySelectorAll('.home-collection .query-card').length === count, count);
  };
  const geometry = () => page.evaluate(() => {
    const main = document.querySelector('main'), region = document.querySelector('.home-collection').getBoundingClientRect();
    const rows = [];
    for (const card of document.querySelectorAll('.home-collection .query-card')) {
      const box = card.getBoundingClientRect();
      let row = rows.find(row => Math.abs(row.top - box.top) < 1);
      if (!row) { row = { top: box.top, left: box.left, right: box.right, count: 0 }; rows.push(row); }
      row.right = box.right; row.count++;
    }
    return { center: region.left + region.width / 2, rows, clientWidth: main.clientWidth, scrollWidth: main.scrollWidth, clientHeight: main.clientHeight, scrollHeight: main.scrollHeight, overflowX: getComputedStyle(main).overflowX };
  });
  const centered = (layout, counts) => {
    assert.deepEqual(layout.rows.map(row => row.count), counts);
    for (const row of layout.rows) assert.ok(Math.abs((row.left + row.right) / 2 - layout.center) < 1, 'each row should be centered, including a partially filled row');
    assert.ok(layout.scrollWidth <= layout.clientWidth, 'the conveyor must not create horizontal overflow');
  };
  try {
    await page.getByRole('button', { name: 'Pause suggestions' }).click();
    await page.mouse.move(5, 5);
    await collect(4); centered(await geometry(), [4]);
    let collapsedHeight;
    for (const expanded of [false, true, false]) {
      const trigger = page.locator('.home-search-trigger');
      if ((await trigger.getAttribute('aria-expanded') === 'true') !== expanded) await trigger.click();
      await page.waitForTimeout(320);
      const layout = await geometry(); centered(layout, [4]);
      if (expanded) assert.ok(layout.scrollHeight > collapsedHeight, 'expanded search can scroll without compressing the cards');
      else if (collapsedHeight === undefined) collapsedHeight = layout.scrollHeight;
      else assert.equal(layout.scrollHeight, collapsedHeight, 'collapsing search restores the content height');
      assert.equal(layout.overflowX, 'hidden');
    }
    await page.mouse.move(5, 5);
    await page.screenshot({ path: path.join(root, '.context/homepage-design/home-layout-wide.png') });
    await collect(6); centered(await geometry(), [4, 2]);
    await collect(8);
    const wrapped = await geometry(); centered(wrapped, [4, 4]);
    assert.ok(wrapped.scrollHeight > wrapped.clientHeight, 'larger collections must still scroll vertically');
    // Exercise conventional scrollbars as well as macOS overlay scrollbars.
    await page.addStyleTag({ content: 'main { scrollbar-gutter:stable; } main::-webkit-scrollbar { width:15px; height:15px; } main::-webkit-scrollbar-thumb { background:var(--line); }' });
    await page.waitForTimeout(200);
    centered(await geometry(), [4, 4]);
    await page.evaluate(() => document.querySelector('main').scrollTop = 0);
    await page.getByRole('button', { name: 'Next five suggestions' }).click();
    await page.locator('.home-belt-row.sliding').waitFor();
    centered(await geometry(), [4, 4]);
    await page.waitForFunction(() => !document.querySelector('.home-belt-row.sliding'));
    await collect(3);
    for (const width of [600, 390, 320]) {
      await page.setViewportSize({ width, height: 1050 });
      await page.waitForTimeout(200);
      centered(await geometry(), width === 600 ? [2, 1] : [1, 1, 1]);
      if (width === 390) {
        await page.evaluate(() => document.querySelector('main').scrollTop = 0);
        await page.screenshot({ path: path.join(root, '.context/homepage-design/home-layout-narrow.png') });
      }
    }
    await page.goto('http://home.test/library');
    assert.equal(await page.locator('main').evaluate(main => getComputedStyle(main).overflowX), 'auto', 'other views retain their scrolling behavior');
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('wide cards inset translucent collection actions and keep metric covers linear', async () => {
  const { browser, page, data } = await setup({ url: '/home?cards=library', viewport: { width: 1470, height: 836 }, deviceScaleFactor: 2 });
  try {
    data.rows.usage = data.rows.usage.slice(0, 2).map((row, i) => ({ ...row, cache_read_input_tokens: i ? 100 : 10, total_tokens: i ? 100 : 10, model_family: 'test-model', price_status: 'priced' }));
    await page.reload();
    await page.waitForFunction(() => !document.querySelector('.card-library-count').textContent.includes('Loading charts'));
    const card = page.locator('#card-price-coverage .query-card');
    const layout = await card.evaluate(card => {
      const box = card.getBoundingClientRect(), button = card.querySelector('.query-keep'), fab = button.getBoundingClientRect();
      const caption = getComputedStyle(card.querySelector('.query-caption'));
      return { width: box.width, columns: getComputedStyle(document.querySelector('.card-library-grid')).gridTemplateColumns.split(' ').length,
        position: getComputedStyle(button).position, radius: getComputedStyle(button).borderRadius, background: getComputedStyle(button).backgroundColor, size: fab.width,
        centerX: fab.x + fab.width / 2 - box.right, centerY: fab.y + fab.height / 2 - box.bottom,
        leftPadding: caption.paddingLeft, rightPadding: caption.paddingRight };
    });
    assert.ok(layout.width >= 240);
    assert.equal(layout.columns, 4);
    assert.equal(layout.position, 'absolute');
    assert.equal(layout.radius, '7px');
    assert.match(layout.background, /(?:\/ 0\.25|, 0\.25)/, 'FAB background is translucent');
    assert.equal(layout.size, 34);
    assert.ok(layout.centerX <= -24 && layout.centerY <= -24, 'FAB is inset inside the card corner');
    assert.equal(layout.leftPadding, layout.rightPadding, 'collection action reserves no caption width');
    assert.equal(await card.locator('.query-card-open .query-keep').count(), 0, 'collection is independent from opening the query');
    const heights = await page.locator('#card-usage-models .query-cover-columns i').evaluateAll(marks => marks.map(mark => parseFloat(mark.style.height)).sort((a, b) => a - b));
    assert.deepEqual(heights, [10, 100], 'a tenfold difference retains its linear proportion');
    assert.equal(await page.locator('.query-timeline-scale,.query-cover-columns.broken').count(), 0);
    for (const width of [600, 390, 320]) {
      await page.setViewportSize({ width, height: 844 });
      assert.ok(await page.locator('.card-library').evaluate(el => el.scrollWidth <= el.clientWidth + 1));
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
      if (width < 600) {
        assert.equal(await page.locator('.card-library-grid').evaluate(el => getComputedStyle(el).gridTemplateColumns.split(' ').length), 1);
        assert.ok((await card.boundingBox()).width >= 240);
      }
    }
    await card.getByRole('button', { name: 'Collect Token pricing coverage', exact: true }).focus();
    await page.keyboard.press('Enter');
    await page.getByText('Collected “Token pricing coverage”.', { exact: true }).waitFor();
    assert.equal(new URL(page.url()).pathname, '/home');
    assert.equal(await page.evaluate(() => window.pharosPrefs.get('pharos-bookmarks-v1', []).length), 1);
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('annotated covers show scalar values, clean rankings and legible chart labels', async () => {
  const { browser, page, data } = await setup({ url: '/home?cards=library', viewport: { width: 1470, height: 836 }, deviceScaleFactor: 2 });
  global.location = { origin: 'http://home.test' };
  try {
    data.rows.usage.forEach((row, i) => { row.price_status = 'priced'; row.repository_name = `repository-${i % 6}`; row.input_tokens = 1000 + i * 100; row.root_conversation_id = `root-${i}`; row.root_repository_name = row.repository_name; row.root_title = row.title; row.root_provider = 'codex'; row.root_model_family = 'gpt-5.4'; });
    data.rows.tool_calls.forEach((row, i) => { row.result_tokens = (i + 1) * 400.12345; });
    await page.reload();
    await page.waitForFunction(() => !document.querySelector('.card-library-count').textContent.includes('Loading charts'));
    const price = page.locator('#card-price-coverage');
    assert.equal(await price.locator('.is-scalar .query-scalar-value').count(), 1);
    assert.match(await price.locator('.query-scalar-value').innerText(), /[KM]/, 'large scalar values use compact units');
    assert.equal(await price.locator('.query-result-summary').count(), 0, 'no meaningless one-group caption');
    assert.match(await price.locator('.query-scalar-group').innerText(), /priced/);
    const original = await price.locator('.query-card-open').getAttribute('href');
    assert.deepEqual(links.queryFromURL(original).query.grouping.keys, ['price_status']);

    const work = page.locator('#card-window-work-cost .query-cover');
    assert.equal(await work.locator('.query-result-summary,.query-result-evidence').count(), 0);
    assert.doesNotMatch(await work.getAttribute('aria-label'), /\b28\b|groups returned|Top:/);
    assert.equal(await work.locator('.query-compact-limit').count(), 0);
    assert.match(await work.locator('.qt-metric-bar-value').first().innerText(), /\$/, 'ranked costs retain currency units');

    const scatter = page.locator('#card-work-volume-cost');
    assert.equal(await scatter.locator('svg text[transform*="rotate"]').count(), 0);
    assert.equal(await scatter.locator('.qt-metric-axis-labels > span').first().innerText(), "↑ API equivalent (USD)");
    await page.waitForFunction(() => {
      const card = document.querySelector('#card-work-volume-cost'), svg = card.querySelector('.qt-metric-plot svg');
      const plot = svg.getBoundingClientRect(), title = card.querySelector('.qt-metric-axis-labels').getBoundingClientRect();
      return title.top >= plot.bottom - 1 && [...svg.querySelectorAll('text[text-anchor="end"]')].every(tick => {
        const box = tick.getBoundingClientRect();
        return box.left >= plot.left && box.right <= plot.right;
      });
    });

    const timeline = page.locator('#card-cost-repos');
    assert.equal(await timeline.locator('.query-timeline-dates > span').count(), 3);
    assert.equal(await timeline.locator('.query-timeline-legend > span').count(), 6);
    assert.match(await timeline.locator('.query-timeline-legend').innerText(), /Other \(1\)/);
    assert.match(await timeline.locator('.query-timeline-ticks').innerText(), /\$/);
    assert.match(await timeline.locator('.query-headline-value').innerText(), /\$/);
    assert.equal(await timeline.locator('.query-headline-label').innerText(), 'API equivalent');
    assert.ok(await timeline.locator('.query-headline-value').evaluate(el => parseFloat(getComputedStyle(el).fontSize) >= 28));
    const plot = await page.locator('#card-usage-models .query-timeline-plot').boundingBox();
    assert.ok(plot.height > 40, 'timeline fills the remaining cover area');
    for (const label of await timeline.locator('.query-timeline-dates, .query-timeline-legend').all()) {
      const box = await label.boundingBox(), cover = await timeline.locator('.query-cover').boundingBox();
      assert.ok(box.y >= cover.y && box.y + box.height <= cover.y + cover.height, 'labels fit inside the cover');
    }
    await page.screenshot({ path: path.join(root, '.context/review-card-covers.png') });
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok(await page.locator('.card-library').evaluate(el => el.scrollWidth <= el.clientWidth + 1));
    await price.locator('.query-card-open').click();
    assert.deepEqual(links.queryFromURL(page.url()).query.grouping.keys, ['price_status'], 'scalar cover retains grouped destination');
    assert.deepEqual(data.errors, []);
  } finally { delete global.location; await browser.close(); }
});

test('native covers retain distributions, paired axes and complete dashboards while cover selection persists', async () => {
  const ids = ['tool-failure-rates', 'duration-distribution', 'output-distribution', 'output-duration', 'price-comparison', 'skill-reach', 'tl1-advancement', 'expensive-work'];
  const bookmarks = ids.map(id => {
    const recipe = instantiateRecipe(queryRecipes.find(item => item.id === id), 7);
    // Existing bookmarks keep their original metrics when templates evolve.
    const query = recipe.query.grouping?.source ?? recipe.query;
    return { id, title: recipe.title, description: recipe.description, url: links.queryURL(recipe.dataset, query, recipe.window), templateId: recipe.instanceId };
  });
  const { browser, page, data } = await setup({ preferenceValues: { 'pharos-bookmarks-v1': bookmarks } });
  global.location = { origin: 'http://home.test' };
  try {
    await page.waitForFunction(() => document.querySelectorAll('.home-collection .query-compact-metric').length === 8);
    assert.equal(await page.locator('.home-collection .qt-metrics-error,.home-collection .qt-metric-message').count(), 0);
    assert.equal(await page.locator('.home-collection .query-compact-metric[inert]').count(), 0, 'charts remain hoverable inside saved card links');
    assert.ok((await page.locator('.home-collection .query-window').allTextContents()).every(label => label === "Last 7d"));
    for (const id of ['output-duration', 'price-comparison']) {
      const cover = page.locator(`.home-collection [data-card-id="${id}"]`);
      assert.ok(await cover.locator('svg .qt-metric-mark').count() > 0, `${id} should contain native marks`);
      const size = await cover.locator('.query-compact-metric').boundingBox();
      const bounds = await cover.locator('.query-cover').boundingBox();
      assert.ok(size.height > 40 && size.y + size.height <= bounds.y + bounds.height + 1, `${id} fits inside its saved cover`);
    }
    for (const id of ['duration-distribution', 'output-distribution']) {
      const box = page.locator(`.home-collection [data-card-id="${id}"]`);
      assert.ok(await box.locator('.query-box-marks').count() > 0, `${id} retains its shared box plot`);
      assert.equal(await box.locator('.query-box-axis').count(), 1);
    }
    const costCover = page.locator('.home-collection [data-card-id="expensive-work"] .query-compact-metric');
    const costSize = await costCover.boundingBox();
    const costRows = costCover.locator('.qt-metric-bar-row');
    assert.equal(await costRows.count(), 4);
    for (const row of await costRows.all()) {
      const size = await row.boundingBox();
      assert.ok(size.y >= costSize.y && size.y + size.height <= costSize.y + costSize.height + 1, 'findings fit inside the cover');
    }
    const card = page.locator('.home-collection [data-card-id="tool-failure-rates"]');
    assert.match(await card.locator('.query-compact-metric').innerText(), /%/);
    const original = bookmarks[0].url;
    await card.hover();
    await card.getByRole('button', { name: /^Edit / }).click();
    await page.getByLabel('Cover metric', { exact: true }).selectOption('calls');
    await page.getByRole('button', { name: 'Save changes', exact: true }).click();
    await page.waitForFunction(() => document.querySelector('.home-collection [data-card-id="tool-failure-rates"] .qt-metric-bar-row') && !document.querySelector('.home-collection [data-card-id="tool-failure-rates"] .query-compact-metric').textContent.includes('%'));
    const saved = await page.evaluate(() => window.pharosPrefs.get('pharos-bookmarks-v1', []).find(item => item.id === 'tool-failure-rates'));
    assert.equal(saved.coverMetricId, 'calls');
    assert.equal(saved.url, original);
    assert.equal(links.queryFromURL(saved.url).query.aggregations.length, 3);
    await page.mouse.move(5, 5);
    await page.evaluate(() => document.querySelector('main').scrollTop = 0);
    await page.screenshot({ path: path.join(root, '.context/homepage-design/home-v2-covers.png'), fullPage: true });
    await page.reload();
    await page.waitForFunction(() => document.querySelector('.home-collection [data-card-id="tool-failure-rates"] .qt-metric-bar-row') && !document.querySelector('.home-collection [data-card-id="tool-failure-rates"] .query-compact-metric').textContent.includes('%'));
    await page.locator('.home-collection [data-card-id="tool-failure-rates"] .query-card-open').click();
    const opened = links.queryFromURL(page.url());
    assert.equal(opened.query.aggregations.length, 3);
    assert.match(opened.query.aggregations[0].expression, /SUM\(\[error_count\]\)/);
    await page.goto('http://home.test/');
    const legacy = page.locator('.home-collection [data-card-id="tool-failure-rates"]');
    await legacy.hover(); await legacy.getByRole('button', { name: /^Edit / }).click();
    await page.getByRole('button', { name: 'Try grouped comparison' }).click();
    await page.getByRole('button', { name: 'Records', exact: true }).first().waitFor();
    assert.equal(links.queryFromURL(page.url()).query.grouping.keys[0], 'tool_name');
    assert.equal(await page.evaluate(() => window.pharosPrefs.get('pharos-bookmarks-v1', [])[0].url), original);
    assert.deepEqual(data.errors, []);
  } finally { delete global.location; await browser.close(); }
});

test('grouped cards keep row values, allow cover selection, and drill into the original records', async () => {
  const item = instantiateRecipe(queryRecipes.find(recipe => recipe.id === 'tool-failure-rates'), 7);
  const bookmark = { id: item.id, title: item.title, description: item.description, url: item.url, templateId: item.instanceId };
  const { browser, page, data } = await setup({ preferenceValues: { 'pharos-bookmarks-v1': [bookmark] } });
  try {
    const card = page.locator('.home-collection .query-card');
    await card.locator('.query-compact-metric').waitFor();
    assert.equal(await card.locator('.query-result-evidence,.query-compact-limit').count(), 0);
    assert.match(await card.locator('.query-compact-metric').innerText(), /%/);
    assert.equal(await card.locator('.query-destination').innerText(), 'Compare failure rates');
    await card.hover(); await card.getByRole('button', { name: /^Edit / }).click();
    await page.getByLabel('Cover metric', { exact: true }).selectOption('card_call_count');
    await page.getByRole('button', { name: 'Save changes', exact: true }).click();
    await page.waitForFunction(() => document.querySelector('.home-collection .query-compact-metric') && !document.querySelector('.home-collection .query-compact-metric').textContent.includes('%'));
    const saved = await page.evaluate(() => window.pharosPrefs.get('pharos-bookmarks-v1', [])[0]);
    assert.equal(saved.coverMetricId, 'card_call_count');
    assert.equal(saved.url, item.url);
    await card.locator('.query-card-open').click();
    await page.getByRole('button', { name: 'Records', exact: true }).first().waitFor();
    const groupedToken = new URL(page.url()).searchParams.get('q_tool_calls');
    await page.getByRole('button', { name: 'Records', exact: true }).first().click();
    await page.waitForFunction(token => new URL(location.href).searchParams.get('q_tool_calls') !== token, groupedToken);
    const query = core.decodeQueryStrict(new URL(page.url()).searchParams.get('q_tool_calls'));
    assert.equal(query.grouping, undefined);
    assert.ok(query.where.some(term => term.field === 'tool_name' && term.op === '='));
    assert.ok(!query.where.some(term => term.field?.startsWith('card_')));
    assert.equal(query.aggregations.length, 3);
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('Home reveals the card library by topic and the library link opens it with filters', async () => {
  const { browser, page, data } = await setup();
  const active = queryRecipes.filter(recipe => !recipe.retired);
  const toggle = page.locator('.home-library-toggle'), library = page.locator('#home-card-library');
  const entries = () => page.locator('.card-library-entry').count();
  try {
    assert.equal(await library.count(), 0, 'the library is hidden by default');
    assert.equal(await toggle.getAttribute('aria-expanded'), 'false');
    await page.getByRole('button', { name: 'Show all cards', exact: true }).click();
    await library.waitFor();
    assert.equal(await toggle.getAttribute('aria-expanded'), 'true');
    assert.equal(await page.getByRole('tab', { selected: true }).getAttribute('data-topic'), 'human');
    assert.equal(await entries(), active.filter(recipe => recipe.category === 'human').length);
    assert.equal(await page.locator('.card-library [role="tab"][data-topic="steering"]').count(), 0, 'retired topics have no tab');
    assert.equal(await page.getByLabel('Find a card', { exact: true }).count(), 0, 'filters start closed');
    assert.equal(await page.locator('.card-library-id').count(), 0, 'definitions stay out of the way until edit mode');
    assert.equal(new URL(page.url()).search, '', 'opening the section leaves the Home URL clean');
    await assertEventually(() => data.preferences['pharos-home-card-library-v1']?.shown === true);

    await page.getByRole('tab', { name: /^Error Patterns/ }).click();
    assert.equal(await entries(), active.filter(recipe => recipe.category === 'errors').length);
    assert.equal(new URL(page.url()).searchParams.get('card_topic'), 'errors');
    await page.keyboard.press('ArrowRight');
    const next = await page.getByRole('tab', { selected: true }).getAttribute('data-topic');
    assert.notEqual(next, 'errors');
    assert.equal(await page.evaluate(() => document.activeElement.getAttribute('role')), 'tab');
    await assertEventually(() => data.preferences['pharos-home-card-library-v1']?.topic === next);

    const first = page.locator('.card-library-entry').first(), title = await first.locator('.query-title').innerText();
    await first.getByRole('button', { name: `Collect ${title}`, exact: true }).click();
    await library.getByText(`Collected “${title}”.`, { exact: true }).waitFor();
    assert.equal(await page.locator('.home-feedback').innerText(), '', 'feedback stays beside the library');
    await first.locator('.card-library-collected').waitFor();

    await page.getByRole('button', { name: 'Filters', exact: true }).click();
    await page.getByLabel('Find a card', { exact: true }).fill('tool');
    assert.equal(await page.getByRole('tab', { selected: true }).getAttribute('data-topic'), 'all', 'a search looks across every topic');
    assert.ok(await entries() > 0);
    await page.getByRole('button', { name: 'Edit cards', exact: true }).click();
    assert.equal(await page.locator('.card-library-id').count(), await entries());
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok(await page.locator('.home-conveyor-head').evaluate(el => el.scrollWidth <= el.clientWidth + 1));
    assert.ok(await page.locator('.card-library').evaluate(el => el.scrollWidth <= el.clientWidth + 1));
    assert.equal(await page.evaluate(() => document.querySelector('main').scrollWidth <= document.querySelector('main').clientWidth), true);
    await page.setViewportSize({ width: 1180, height: 920 });

    await page.getByRole('button', { name: 'Hide all cards', exact: true }).click();
    assert.equal(await library.count(), 0);
    assert.equal(new URL(page.url()).search, '', 'hiding drops the library URL state');
    await assertEventually(() => data.preferences['pharos-home-card-library-v1'].shown === false);
    await page.reload();
    await page.locator('.home-library-toggle').waitFor();
    assert.equal(await library.count(), 0);

    // In-app library links open the section and replace its state.
    const selected = () => page.getByRole('tab', { selected: true }).getAttribute('data-topic');
    await page.evaluate(() => window.pharosNavigate('/home?card_topic=errors'));
    await library.waitFor();
    assert.equal(await selected(), 'errors');
    await page.evaluate(() => window.pharosNavigate('/home?card_topic=context'));
    await assertEventually(async () => await selected() === 'context');
    await page.getByRole('tab', { name: /^All/ }).click();
    assert.equal(new URL(page.url()).searchParams.get('card_topic'), 'all', 'the All tab keeps a library URL');
    await page.reload();
    await library.waitFor();
    assert.equal(await selected(), 'all', 'reloading reopens the library on All although it was last hidden');
    await page.getByRole('button', { name: 'Hide all cards', exact: true }).click();
    assert.equal(new URL(page.url()).search, '');

    await page.goto('http://home.test/home?cards=library');
    await page.locator('.card-library-entry').first().waitFor();
    assert.equal(await page.getByRole('tab', { selected: true }).getAttribute('data-topic'), 'all');
    assert.equal(await entries(), active.length);
    assert.equal(await page.getByLabel('Find a card', { exact: true }).count(), 1);
    assert.ok(await page.locator('.card-library-id').count() > 0);
    await assertEventually(() => page.evaluate(() => Math.abs(document.getElementById('home-card-library').getBoundingClientRect().top - document.querySelector('main').getBoundingClientRect().top) < 80));
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('card library shows empty cards, filters and sorts active definitions, and persists template edits', async () => {
  const { browser, page, data } = await setup({ url: '/home?cards=library' });
  const library = page.locator('.card-library');
  try {
    const activeRecipes = queryRecipes.filter(recipe => !recipe.retired);
    assert.equal(await page.locator('a[href*="-review"]').count(), 0);
    assert.equal(await page.locator('.card-library-entry').count(), activeRecipes.length);
    assert.deepEqual(data.metadataRequests, [], 'hidden table controls must not scan distinct values on Home');
    assert.ok(data.requests.every(({ path }) => !path.endsWith('/aggregations')), 'hidden table header charts must not compete with card previews');
    const identities = await page.locator('.card-library-entry').evaluateAll(entries => entries.map(entry => [entry.id, entry.dataset.cardDefinition, entry.querySelector('.card-library-id').textContent]));
    assert.equal(new Set(identities.map(([id]) => id)).size, activeRecipes.length);
    for (const [id, definition, label] of identities) { assert.equal(id, `card-${definition}`); assert.equal(label, `ID: ${definition}`); }
    await page.getByLabel('Data source', { exact: true }).selectOption('usage');
    assert.equal(await page.locator('.card-library-entry').count(), activeRecipes.filter(r => r.dataset === 'usage').length);
    await page.getByLabel('Sort by', { exact: true }).selectOption('title');
    const titles = await page.locator('.card-library-entry .query-title').allTextContents();
    assert.deepEqual(titles, [...titles].sort((a, b) => a.localeCompare(b)));
    await page.getByLabel('Find a card', { exact: true }).fill(queryRecipes.find(recipe => recipe.id === 'effort-cost').description);
    await page.waitForFunction(() => document.querySelectorAll('.card-library-entry').length === 1);
    await page.getByRole('button', { name: /^Edit card / }).click();
    await page.getByLabel('Question', { exact: true }).fill('Effort, at a glance');
    await page.getByLabel('Default lookback', { exact: true }).selectOption('30');
    await page.getByLabel('Chart', { exact: true }).selectOption('bar-vertical');
    const savedTemplate = page.waitForResponse(response => response.url().endsWith('/api/preferences') && response.request().postData()?.includes('pharos-card-templates-v1'));
    await page.getByRole('button', { name: 'Save template', exact: true }).click();
    await savedTemplate;
    await library.getByText('Effort, at a glance', { exact: true }).waitFor();
    assert.equal(await page.locator('.card-library-entry').getAttribute('id'), 'card-effort-cost');
    assert.equal(data.preferences['pharos-card-templates-v1']['effort-cost'].days, 30);
    assert.equal(data.preferences['pharos-card-templates-v1']['effort-cost'].cover.kind, 'bar-vertical');
    await page.reload();
    await library.getByText('Effort, at a glance', { exact: true }).waitFor();
    assert.equal(await page.getByLabel('Data source', { exact: true }).inputValue(), 'usage');
    await library.getByRole('button', { name: 'Collect Effort, at a glance', exact: true }).click();
    await page.getByText('Collected “Effort, at a glance”.', { exact: true }).waitFor();
    const collected = await page.evaluate(() => window.pharosPrefs.get('pharos-bookmarks-v1', [])[0]);
    assert.equal(collected.cover.kind, 'bar-vertical');
    assert.deepEqual(collected.query.grouping.keys, queryRecipes.find(recipe => recipe.id === 'effort-cost').exploration.keys);
    assert.deepEqual(collected.query.grouping.source.where.filter(term => term.field === 'first_usage_at').map(term => term.value), ['-30d', '+0s']);
    await page.getByRole('button', { name: /^Edit card / }).click();
    await page.getByRole('button', { name: 'Reset template', exact: true }).click();
    await library.getByText(queryRecipes.find(recipe => recipe.id === 'effort-cost').title, { exact: true }).waitFor();
    await page.getByLabel('Find a card', { exact: true }).fill('');
    await page.getByLabel('Data source', { exact: true }).selectOption('');
    assert.equal(await page.locator('.card-library-entry').count(), activeRecipes.length);
    data.empty = true;
    await page.reload();
    await page.waitForFunction(() => !document.querySelector('.card-library-count').textContent.includes('Loading charts'));
    assert.equal(await page.locator('.card-library-entry').count(), activeRecipes.length);
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok(await page.locator('.card-library').evaluate(el => el.scrollWidth <= el.clientWidth + 1));
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('provider cost share defaults to 90 days and keeps seven-day percentage smoothing when collected', async () => {
  const { browser, page, data } = await setup({ url: '/home?cards=library&card_search=provider-cost-share' });
  try {
    const base = data.rows.usage[0];
    data.rows.usage = Array.from({ length: 90 }, (_, i) => {
      const date = new Date(Date.now() - (89 - i) * 86400000 - 1000);
      const day = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`;
      const share = .65 - i / 180 + .1 * Math.sin(i / 8);
      return ['claude', 'codex'].map((provider, j) => ({ ...base, id: `provider-${i}-${j}`, provider, day, first_usage_at: date.toISOString(), last_usage_at: date.toISOString(), cost_usd: 10 * (j ? 1 - share : share) }));
    }).flat();
    await page.reload();
    const card = page.locator('#card-provider-cost-share');
    await card.locator('.query-cover-timeline').waitFor();
    assert.equal(await card.locator('.query-window').innerText(), 'Last 90d');
    assert.equal(await card.locator('.query-cover-columns > span').count(), 91);
    assert.match(await card.locator('.query-timeline-ticks').innerText(), /100%/);
    assert.match(await card.locator('.query-cover-columns > span').last().getAttribute('data-chart-tooltip'), /7d rolling average.*claude:.*%.*codex:.*%/s);
    assert.match(await card.locator('.query-chart-axis-labels').innerText(), /Cost share/);
    await card.screenshot({ path: path.join(root, '.context/provider-cost-share.png') });
    await card.getByRole('button', { name: 'Collect Provider cost share', exact: true }).click();
    await page.getByText('Collected “Provider cost share”.', { exact: true }).waitFor();
    const saved = await page.evaluate(() => window.pharosPrefs.get('pharos-bookmarks-v1', [])[0]);
    assert.equal(saved.templateId, 'provider-cost-share:90');
    assert.deepEqual(saved.cover.timeline, { rollingPeriods: 7, percent: true });
    assert.equal(new URL(saved.url, 'http://home.test').searchParams.get('window_days'), '90');
    await assertEventually(() => data.preferences['pharos-bookmarks-v1']?.some(item => item.id === saved.id));
    await page.goto('http://home.test/home');
    const collected = page.locator('.home-collection .query-card');
    await collected.locator('.query-cover-timeline').waitFor();
    assert.equal(await collected.locator('.query-window').innerText(), 'Last 90d');
    assert.match(await collected.locator('.query-cover-columns > span').last().getAttribute('data-chart-tooltip'), /7d rolling average/);
    assert.match(await collected.locator('.query-timeline-ticks').innerText(), /100%/);
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('model share timelines distinguish tokens from cost and retain 90-day smoothing when collected', async () => {
  const { browser, page, data } = await setup({ url: '/home?cards=library&card_search=share%20over%20time' });
  try {
    const base = data.rows.usage[0], models = ['opus-4-6', 'gpt-5.3-codex', 'gemini-3.8-flash'];
    data.rows.usage = Array.from({ length: 90 }, (_, i) => {
      const date = new Date(Date.now() - (89 - i) * 86400000 - 1000);
      const day = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`;
      const share = .65 - i / 180 + .1 * Math.sin(i / 8);
      return models.map((model_family, j) => {
        const total_tokens = Math.round(1e6 * [share, (1 - share) * .8, (1 - share) * .2][j]);
        return { ...base, id: `model-${i}-${j}`, provider: ['claude', 'codex', 'antigravity'][j], model_family, day, first_usage_at: date.toISOString(), last_usage_at: date.toISOString(), total_tokens, cost_usd: total_tokens / 1e6 * [4, 1, .2][j] };
      });
    }).flat();
    await page.reload();
    const tips = [];
    for (const metric of ['token', 'cost']) {
      const id = `model-${metric}-share-trend`, card = page.locator(`#card-${id}`);
      await card.locator('.query-cover-timeline').waitFor();
      assert.equal(await card.locator('.query-window').innerText(), 'Last 90d');
      assert.equal(await card.locator('.query-cover-columns > span').count(), 91);
      assert.match(await card.locator('.query-timeline-ticks').innerText(), /100%/);
      const tip = await card.locator('.query-cover-columns > span').last().getAttribute('data-chart-tooltip');
      for (const model of models) assert.ok(tip.includes(`${model}:`));
      assert.match(tip, /7d rolling average/);
      tips.push(tip);
      assert.match(await card.locator('.query-chart-axis-labels').innerText(), metric === 'token' ? /Token share/ : /Cost share/);
      await card.screenshot({ path: path.join(root, `.context/${id}.png`) });
      await card.getByRole('button', { name: /^Collect / }).click();
      await assertEventually(() => data.preferences['pharos-bookmarks-v1']?.some(item => item.templateId === `${id}:90`));
      const saved = data.preferences['pharos-bookmarks-v1'].find(item => item.templateId === `${id}:90`);
      assert.deepEqual(saved.cover.timeline, { rollingPeriods: 7, percent: true });
      assert.deepEqual(saved.query.grouping.keys, ['day', 'model_family']);
    }
    assert.notEqual(tips[0], tips[1], 'different model prices give different token and cost percentages');
    await page.goto('http://home.test/home');
    for (const metric of ['token', 'cost']) {
      const saved = data.preferences['pharos-bookmarks-v1'].find(item => item.templateId === `model-${metric}-share-trend:90`);
      const card = page.locator(`.home-collection [data-card-id="${saved.id}"]`);
      await card.locator('.query-cover-timeline').waitFor();
      assert.equal(await card.locator('.query-window').innerText(), 'Last 90d');
      assert.match(await card.locator('.query-cover-columns > span').last().getAttribute('data-chart-tooltip'), /7d rolling average/);
    }
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('every active catalog card preserves its displayed data after collection and dashboard reload', async () => {
  const { browser, page, data } = await setup({ url: '/home?cards=library', now: new Date('2026-10-16T18:00:00Z') });
  try {
    await page.waitForFunction(() => !document.querySelector('.card-library-count').textContent.includes('Loading charts'));
    const active = queryRecipes.filter(recipe => !recipe.retired);
    const signature = card => card.locator('.query-cover').evaluate(cover => ({
      label: cover.getAttribute('aria-label'),
      text: cover.textContent,
      study: cover.querySelector('.human-study-cover')?.className ?? null,
      tips: [...cover.querySelectorAll('[data-chart-tooltip]')].map(element => element.getAttribute('data-chart-tooltip')),
    }));
    const expected = new Map();
    for (const recipe of active) {
      const card = page.locator(`#card-${recipe.id}`);
      expected.set(recipe.id, await signature(card));
      await card.getByRole('button', { name: /^Collect / }).click();
      const days = recipe.period ?? (recipe.days?.length === 1 ? recipe.days[0] : 7);
      await assertEventually(async () => await page.evaluate(({ id, days }) => window.pharosPrefs.get('pharos-bookmarks-v1', []).some(card => card.templateId === `${id}:${days}`), { id: recipe.id, days })).catch(async error => assert.fail(`${recipe.id}: ${await page.locator('.card-library-feedback').innerText()} (${error.message})`));
    }
    await assertEventually(async () => data.preferences['pharos-bookmarks-v1']?.length === active.length);
    await page.goto('http://home.test/home');
    await page.waitForFunction(count => document.querySelectorAll('.home-collection .query-card').length === count
      && !document.querySelector('.home-collection [aria-busy="true"]'), active.length);
    for (const saved of data.preferences['pharos-bookmarks-v1']) {
      const id = saved.templateId.split(':')[0];
      assert.deepEqual(await signature(page.locator(`.home-collection [data-card-id="${saved.id}"]`)), expected.get(id), id);
    }
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('template edits preserve distribution layout, sample labels, and existing scatter limits', async () => {
  const { browser, page, data } = await setup({ url: '/home?cards=library&card_search=skill-body-sizes' });
  try {
    for (const id of ['skill-body-sizes', 'work-volume-cost']) {
      await page.getByLabel('Find a card', { exact: true }).fill(id);
      await page.getByRole('button', { name: /^Edit card / }).click();
      const title = await page.getByLabel('Question', { exact: true }).inputValue();
      await page.getByLabel('Question', { exact: true }).fill(`${title} customized`);
      await page.getByRole('button', { name: 'Save template', exact: true }).click();
      await page.locator('.card-template-editor').waitFor({ state: 'hidden', timeout: 5000 });
      const saved = await page.evaluate(id => window.pharosPrefs.get('pharos-card-templates-v1', {})[id], id);
      const original = queryRecipes.find(recipe => recipe.id === id).cover;
      assert.deepEqual(saved.cover, original, `${id}: title changes must retain the full cover`);
    }
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('retained distribution cards preserve native boxes and local-hour queries through editing and collection', async () => {
  const { browser, page, data } = await setup({ url: '/home?cards=library' });
  try {
    data.rows.tool_calls.forEach((row,i)=>{row.tool_category='web';row.search_query='Query plan';row.conversation_id=`search-${i}`;});
    await page.reload();
    await page.getByLabel('Find a card', { exact: true }).fill('research-conversation-searches');
    await page.locator('.query-box-row').first().waitFor();
    assert.ok(data.requests.some(({request})=>request.grouping?.keys.includes('conversation_id')&&request.metrics?.some(metric=>metric.distribution?.kind==='box')));
    await page.getByRole('button', { name: /^Edit card / }).click();
    assert.equal(await page.getByLabel('Chart', { exact: true }).inputValue(), 'box');
    await page.getByLabel('Question', { exact: true }).fill('My conversation search counts');
    await page.getByRole('button', { name: 'Save template', exact: true }).click();
    const library = page.locator('.card-library');
    await library.getByText('My conversation search counts', { exact: true }).waitFor();
    await library.getByRole('button', { name: 'Collect My conversation search counts', exact: true }).click();
    await page.getByText('Collected “My conversation search counts”.', { exact: true }).waitFor();
    let saved = await page.evaluate(() => window.pharosPrefs.get('pharos-bookmarks-v1', []).find(card => card.templateId?.startsWith('research-conversation-searches:')));
    assert.equal(saved.cover.kind, 'box');
    assert.deepEqual(saved.query.grouping.keys, ['repository_name', 'conversation_id']);
    assert.equal(saved.query.aggregations[0].distribution.input, queryRecipes.find(r=>r.id==='research-conversation-searches').exploration.coverMetric.distribution.input);

    await page.getByLabel('Find a card', { exact: true }).fill('writing-hours');
    await page.waitForFunction(() => document.querySelectorAll('.card-library-entry').length === 1 && !document.querySelector('.card-library-count').textContent.includes('Loading charts'));
    assert.equal(await library.locator('.human-study-hour-bars > span').count(), 24);
    await library.getByRole('button', { name: 'Collect When you work', exact: true }).click();
    await page.getByText('Collected “When you work”.', { exact: true }).waitFor();
    saved = await page.evaluate(() => window.pharosPrefs.get('pharos-bookmarks-v1', []).find(card => card.templateId?.startsWith('writing-hours:')));
    assert.equal(saved.query.aggregations[0].dateGrouping.timeZone, 'America/Denver');
    assert.equal(saved.query.aggregations[0].dateGrouping.interval, 'hour');
    assert.deepEqual(saved.query.where.filter(term => term.field === 'sent_at').map(term => term.value), ['-7d', '+0s']);
    await page.reload();
    await page.waitForFunction(() => !document.querySelector('.card-library-count').textContent.includes('Loading charts'));
    assert.equal(await page.locator('.query-preview-status').filter({ hasText: 'Preview unavailable' }).count(), 0);
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('collected writing-hours cards combine multiple days by local hour after reload and window edits', async () => {
  const { browser, page, data } = await setup({ url: '/home?cards=library&card_search=writing-hours' });
  try {
    data.rows.writing_messages = [
      { id: 'morning-one', sent_at: '2026-10-14T15:15:00Z', typed_words: 100 },
      { id: 'morning-two', sent_at: '2026-10-15T15:45:00Z', typed_words: 50 },
      { id: 'evening', sent_at: '2026-10-14T23:15:00Z', typed_words: 25 },
      { id: 'older-morning', sent_at: '2026-10-01T15:15:00Z', typed_words: 200 },
    ];
    await page.clock.setFixedTime(new Date('2026-10-16T18:00:00Z'));
    await page.reload();
    const catalog = page.locator('#card-writing-hours');
    await catalog.locator('.human-study-hour-bars').waitFor();
    const hourDetails = card => card.locator('.human-study-hour-bars > span').evaluateAll(bars => bars.map(bar => bar.getAttribute('data-chart-tooltip')));
    const expected = await hourDetails(catalog);
    assert.equal(expected.length, 24);
    assert.match(expected[9], /150 human-written words/);
    assert.match(expected[17], /25 human-written words/);
    await catalog.getByRole('button', { name: 'Collect When you work', exact: true }).click();
    await page.getByText('Collected “When you work”.', { exact: true }).waitFor();
    const saved = await page.evaluate(() => window.pharosPrefs.get('pharos-bookmarks-v1', []).find(card => card.templateId === 'writing-hours:7'));
    assert.equal(saved.cover.metricId, 'hourly-human-words');
    await assertEventually(async () => data.preferences['pharos-bookmarks-v1']?.some(card => card.id === saved.id));
    await page.goto('http://home.test/home');
    const card = page.locator(`.home-collection [data-card-id="${saved.id}"]`);
    await card.locator('.human-study-hour-bars').waitFor();
    assert.deepEqual(await hourDetails(card), expected, 'collecting preserves the catalog aggregation');
    await page.reload();
    await card.locator('.human-study-hour-bars').waitFor();
    assert.deepEqual(await hourDetails(card), expected, 'existing saved cards recover the same clock-hour cover');
    await card.screenshot({ path: path.join(root, '.context/writing-hours-collected.png') });
    await card.hover();
    await card.getByRole('button', { name: 'Edit When you work', exact: true }).click();
    await page.getByLabel(/^Time window/).selectOption('30');
    await page.getByRole('button', { name: 'Save changes', exact: true }).click();
    await assertEventually(async () => (await hourDetails(card))[9]?.includes('350 human-written words'));
    assert.equal((await hourDetails(card)).length, 24);
    assert.deepEqual(await card.locator('.human-study-axis > span').allTextContents(), ['00:00', '12:00', '23:00']);
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});


test('compact cover ticks and box rows stay readable with Last time badges', async () => {
  const { browser, page, data } = await setup({ url: '/home?cards=library&card_search=duration-distribution', viewport: { width: 1470, height: 836 }, deviceScaleFactor: 2 });
  try {
    data.rows.tool_calls.forEach((row, i) => {
      row.command_name = ['go test', 'npm test', 'pytest', 'cargo test'][i % 4];
      row.duration_ms = i < 4 ? 100000000 : (i + 1) * 1000;
    });
    await page.reload();
    const box = page.locator('#card-duration-distribution');
    await page.waitForFunction(() => document.querySelectorAll('#card-duration-distribution .query-box-row').length === 4);
    assert.equal(await box.locator('.query-window').innerText(), 'Last 7d');
    assert.equal(await box.locator('.query-compact-limit').count(), 0);
    assert.equal(await box.locator('.query-box-axis').count(), 1);
    assert.match(await box.locator('.query-box-scale-label').textContent(), /Shared log Recorded call duration axis/);
    assert.ok(await box.locator('.query-box-outlier').count() >= 4, 'actual extremes remain visible');
    const bounds = await box.locator('.query-cover').boundingBox();
    for (const row of await box.locator('.query-box-row').all()) {
      const title = await row.locator('.query-box-row-label').boundingBox();
      const glyph = await row.locator('.query-box-marks').boundingBox();
      assert.ok(title.y + title.height <= glyph.y, 'command label clears the glyph');
      assert.match(await row.locator('.query-box-row-label strong').textContent(), /^Median /);
      assert.ok(glyph.x >= bounds.x && glyph.x + glyph.width <= bounds.x + bounds.width + 1);
    }
    for (const tick of await box.locator('.query-box-axis span').all()) {
      const label = await tick.boundingBox();
      assert.ok(label.x >= bounds.x && label.x + label.width <= bounds.x + bounds.width + 1);
      assert.ok(label.y >= bounds.y && label.y + label.height <= bounds.y + bounds.height);
    }
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok(await page.locator('.card-library').evaluate(el => el.scrollWidth <= el.clientWidth + 1));
    assert.equal(await box.locator('.query-box-row').count(), 4);
    await page.setViewportSize({ width: 1470, height: 836 });
    await page.goto('http://home.test/home?cards=library&card_search=writing-hours');
    const hours = page.locator('#card-writing-hours');
    await hours.locator('.human-study-hour-bars').waitFor();
    assert.equal(await hours.locator('.query-window').innerText(), 'Last 7d');
    assert.equal(await hours.locator('.human-study-hour-bars>span').count(), 24);
    assert.deepEqual(await hours.locator('.human-study-axis>span').allTextContents(), ['00:00','12:00','23:00']);
    assert.ok(await hours.locator('.human-study-hour-bars>span').evaluateAll(bars => bars.some(bar => bar.querySelector('i').style.height !== '0%')));
    await hours.locator('.human-study-hour-bars>span').first().hover();
    assert.match(await page.getByRole('tooltip').innerText(), /human-written words/);
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('long card labels wrap over underline bars on desktop and phone', async () => {
  const { browser, page, data } = await setup({ url: '/home?cards=library&card_search=error-reach', viewport: { width: 1470, height: 836 }, deviceScaleFactor: 2 });
  try {
    data.rows.tool_calls.forEach((row, i) => { row.error_count = 1; row.error_signature = ['A long error signature about missing configuration settings', 'Another long error about a missing source file', 'An error when reading a very long repository path', 'Repeated decoding error with full diagnostic details'][i % 4]; });
    await page.reload();
    const card = page.locator('#card-error-reach');
    await card.locator('.qt-metric-bar-row--below').first().waitFor();
    for (const width of [1470, 390]) {
      await page.setViewportSize({ width, height: 844 });
      await waitForBarsInCover(card);
      const cover = await card.locator('.query-cover').boundingBox();
      assert.equal(await card.locator('.qt-metric-bar-row--below').count(), 4);
      for (const row of await card.locator('.qt-metric-bar-row--below').all()) {
        const label = await row.locator('.qt-metric-bar-label').boundingBox(), bar = await row.locator('.qt-metric-bar-track').boundingBox(), value = await row.locator('.qt-metric-bar-value').boundingBox();
        assert.ok(label.width > cover.width * .7, 'long labels get most of the row width');
        assert.equal(await row.locator('.qt-metric-bar-label').evaluate(el => getComputedStyle(el).whiteSpace), 'normal', 'labels may wrap instead of ellipsizing in a narrow column');
        assert.equal(await row.locator('.qt-metric-bar-label').evaluate(el => getComputedStyle(el).webkitLineClamp), '2', 'the card supplies its two-line limit through the metric options');
        assert.ok(bar.y >= label.y + label.height - 1 && bar.y >= value.y + value.height - 1);
        assert.ok(bar.width > cover.width * .8);
        assert.ok(bar.y + bar.height <= cover.y + cover.height, 'all four underline bars fit');
        assert.match(await row.locator('.qt-metric-bar-label').getAttribute('data-chart-title'), /long|Repeated/);
      }
      assert.ok(await page.locator('.card-library').evaluate(el => el.scrollWidth <= el.clientWidth + 1));
    }
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('the metric editor saves and clears underline label limits through the query URL', async () => {
  global.location = { origin: 'http://home.test' };
  const query = { ...core.EMPTY_QUERY, aggregations: [{ id: 'underline-demo', op: 'count', groupBy: ['error_signature'], display: { kind: 'list' } }] };
  const url = links.queryURL('tool_calls', query);
  const { browser, page, data } = await setup();
  try {
    data.rows.tool_calls.forEach(row => { row.error_signature = 'A very long diagnostic about missing configuration settings and repository paths that should wrap over several lines in a narrow metric'; });
    await page.goto(`http://home.test${url}`);
    await page.getByRole('button', { name: 'Edit metrics', exact: true }).click();
    const placement = page.getByLabel('List bar placement', { exact: true });
    assert.equal(await placement.inputValue(), 'inline');
    if (!await placement.isVisible()) await page.locator('.qt-metrics-editor summary').filter({ hasText: /^Display/ }).first().click();
    await placement.selectOption('below');
    const limit = page.getByLabel('Maximum label lines', { exact: true });
    assert.equal(await limit.inputValue(), '');
    await limit.fill('1');
    await limit.press('Tab');
    await page.getByRole('button', { name: 'Apply metrics', exact: true }).click();
    await page.locator('.qt-metric-bar-row--below').first().waitFor();
    const label = page.locator('.qt-metric-bar-label').first();
    assert.equal(await label.evaluate(el => getComputedStyle(el).webkitLineClamp), '1');
    assert.ok(await label.evaluate(el => el.getBoundingClientRect().height <= parseFloat(getComputedStyle(el).lineHeight) + 1));
    assert.match(await label.getAttribute('title'), /very long diagnostic/);
    const token = new URL(page.url()).searchParams.get('q_tool_calls');
    assert.equal(core.decodeQuery(token).aggregations[0].display.list.barPosition, 'below');
    assert.equal(core.decodeQuery(token).aggregations[0].display.list.maxLabelLines, 1);
    await page.reload();
    await page.locator('.qt-metric-bar-row--below').first().waitFor();
    await page.getByRole('button', { name: 'Edit metrics', exact: true }).click();
    assert.equal(await placement.inputValue(), 'below');
    assert.equal(await limit.inputValue(), '1');
    if (!await placement.isVisible()) await page.locator('.qt-metrics-editor summary').filter({ hasText: /^Display/ }).first().click();
    await limit.fill('3');
    await limit.press('Tab');
    await page.getByRole('button', { name: 'Apply metrics', exact: true }).click();
    assert.equal(await label.evaluate(el => getComputedStyle(el).webkitLineClamp), '3');
    await page.getByRole('button', { name: 'Edit metrics', exact: true }).click();
    if (!await placement.isVisible()) await page.locator('.qt-metrics-editor summary').filter({ hasText: /^Display/ }).first().click();
    await limit.fill('');
    await limit.press('Tab');
    await page.getByRole('button', { name: 'Apply metrics', exact: true }).click();
    assert.equal(core.decodeQuery(new URL(page.url()).searchParams.get('q_tool_calls')).aggregations[0].display.list.maxLabelLines, undefined);
    assert.equal(await label.evaluate(el => getComputedStyle(el).webkitLineClamp), 'none');
    await page.reload();
    await page.getByRole('button', { name: 'Edit metrics', exact: true }).click();
    assert.equal(await limit.inputValue(), '');
    assert.deepEqual(data.errors, []);
  } finally { delete global.location; await browser.close(); }
});

test('card keys, visible series and Other are configurable and survive collection', async () => {
  const { browser, page, data } = await setup({ url: '/home?cards=library&card_search=human-words' });
  try {
    const human = page.locator('#card-human-words');
    await human.locator('.query-cover-timeline').waitFor();
    assert.equal(await human.locator('.query-timeline-legend').count(), 0, 'a single series needs no redundant key');
    assert.deepEqual(await human.locator('.query-chart-axis-labels>span').allTextContents(), ['↑ words', '→ Day']);
    await human.getByRole('button', { name: /^Edit card / }).click();
    await page.getByLabel('Chart key', { exact: true }).selectOption('true');
    await page.getByLabel('Visible rows / series', { exact: true }).fill('2');
    await page.getByLabel('Other remainder', { exact: true }).selectOption('false');
    await page.getByRole('button', { name: 'Save template', exact: true }).click();
    await human.locator('.query-timeline-legend').waitFor();
    await human.getByRole('button', { name: /^Collect / }).click();
    await page.waitForFunction(() => window.pharosPrefs.get('pharos-bookmarks-v1', []).some(card => card.templateId?.startsWith('human-words:')));
    const saved = await page.evaluate(() => window.pharosPrefs.get('pharos-bookmarks-v1', []).find(card => card.templateId?.startsWith('human-words:')));
    assert.equal(saved.cover.maxGroups, 2);
    assert.equal(saved.cover.showLegend, true);
    assert.equal(saved.cover.showOther, false);
    data.rows.usage.forEach((row, i) => row.repository_name = `repository-${i % 5}`);
    await page.getByLabel('Find a card', { exact: true }).fill('cost-repos');
    await page.reload();
    const cost = page.locator('#card-cost-repos');
    await cost.locator('.query-cover-timeline').waitFor();
    await cost.getByRole('button', { name: /^Edit card / }).click();
    await page.getByLabel('Visible rows / series', { exact: true }).fill('2');
    await page.getByLabel('Chart key', { exact: true }).selectOption('true');
    await page.getByLabel('Other remainder', { exact: true }).selectOption('true');
    await page.getByRole('button', { name: 'Save template', exact: true }).click();
    await page.waitForFunction(() => document.querySelectorAll('#card-cost-repos .query-timeline-legend>span').length === 3);
    assert.match(await cost.locator('.query-timeline-legend').innerText(), /Other \(3\)/);
    await cost.getByRole('button', { name: /^Edit card / }).click();
    await page.getByLabel('Other remainder', { exact: true }).selectOption('false');
    await page.getByRole('button', { name: 'Save template', exact: true }).click();
    await page.waitForFunction(() => document.querySelectorAll('#card-cost-repos .query-timeline-legend>span').length === 2);
    assert.doesNotMatch(await cost.locator('.query-timeline-legend').innerText(), /Other/);
    await assertEventually(() => data.preferences['pharos-card-templates-v1']?.['cost-repos']?.cover?.showOther === false);
    await page.reload();
    await cost.locator('.query-cover-timeline').waitFor();
    assert.equal(await cost.locator('.query-timeline-legend>span').count(), 2);
    await cost.getByRole('button', { name: /^Edit card / }).click();
    await assertEventually(async () => await page.getByLabel('Visible rows / series', { exact: true }).inputValue() === '2');
    assert.equal(await page.getByLabel('Visible rows / series', { exact: true }).inputValue(), '2');
    assert.equal(await page.getByLabel('Other remainder', { exact: true }).inputValue(), 'false');
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('unavailable saved previews show geometric artwork and reload independently without navigation', async () => {
  const healthy = instantiateRecipe(queryRecipes.find(item => item.id === 'tool-failures'), 7);
  const { browser, page, data } = await setup({ saved: true, preferenceValues: { 'pharos-bookmarks-v1': [{ ...healthy, id: 'healthy', templateId: healthy.instanceId }] } });
  try {
    await page.getByRole('button', { name: 'Pause suggestions' }).click();
    const card = page.locator('.home-collection [data-card-id="query:writing_messages:original-query"]');
    await card.locator('.query-cover-timeline').waitFor();
    const height = (await card.locator('.query-cover').boundingBox()).height;
    data.previewFailures.add('writing_messages');
    await page.evaluate(() => window.dispatchEvent(new Event('pharos:data-refresh')));
    await card.locator('.query-preview-unavailable').waitFor();
    await page.waitForFunction(() => document.querySelector('.home-conveyor').getAttribute('aria-busy') === 'false');
    assert.equal(await card.locator('a button').count(), 0, 'reload is independent from opening the view');
    assert.equal(await card.locator('.query-unavailable-symbol').getAttribute('aria-hidden'), 'true');
    assert.ok(await card.locator('.query-unavailable-symbol rect').count() > 10);
    assert.ok(Math.abs((await card.locator('.query-cover').boundingBox()).height - height) < 1, 'failure keeps the loading cover dimensions');
    await card.screenshot({ path: path.join(root, '.context/preview-unavailable-dark.png') });
    await page.evaluate(() => document.documentElement.dataset.theme = 'light');
    await card.screenshot({ path: path.join(root, '.context/preview-unavailable-light.png') });
    await page.evaluate(() => document.documentElement.dataset.theme = 'dark');
    const url = page.url(), reload = card.getByRole('button', { name: 'Reload preview for My words' });
    await reload.click();
    await card.locator('.query-preview-unavailable').waitFor();
    assert.equal(page.url(), url, 'a failed retry stays on Home and remains retryable');
    await page.evaluate(() => { window.healthyPreview = document.querySelector('[data-card-id="healthy"] .query-compact-metric'); });
    data.previewFailures.clear(); data.holdPreviews = true; data.requests.length = 0;
    await reload.focus(); await page.keyboard.press('Enter');
    await card.locator('.query-preview-loading').waitFor();
    assert.equal(await card.locator('.query-cover').getAttribute('aria-busy'), 'true');
    assert.equal(await card.locator('.query-preview-unavailable').count(), 0);
    assert.ok(Math.abs((await card.locator('.query-cover').boundingBox()).height - height) < 1);
    await assertEventually(() => data.pending.length === 1);
    assert.deepEqual(data.requests.map(request => request.dataset), ['writing_messages'], 'only the failed card fetches again');
    assert.equal(await page.evaluate(() => window.healthyPreview === document.querySelector('[data-card-id="healthy"] .query-compact-metric')), true);
    data.holdPreviews = false; for (const pending of data.pending.splice(0)) pending.release();
    await card.locator('.query-cover-timeline').waitFor();
    assert.equal(await card.getByRole('button', { name: /^Reload preview/ }).count(), 0);
    assert.equal(page.url(), url);
    await card.locator('.query-card-open').click();
    await waitForQuery(page, 'writing_messages', query => Boolean(query.grouping));
    assert.deepEqual(data.errors, []);
  } finally { data.holdPreviews = false; for (const pending of data.pending.splice(0)) pending.release(); await browser.close(); }
});

test('a collected card refreshes on its own, and every card refreshes when a sync finishes or a hidden tab returns', async () => {
  const healthy = instantiateRecipe(queryRecipes.find(item => item.id === 'tool-failures'), 7);
  const { browser, page, data } = await setup({ saved: true, preferenceValues: { 'pharos-bookmarks-v1': [{ ...healthy, id: 'healthy', templateId: healthy.instanceId }] } });
  try {
    await page.getByRole('button', { name: 'Pause suggestions' }).click();
    const card = page.locator('.home-collection [data-card-id="query:writing_messages:original-query"]');
    await card.locator('.query-cover-timeline').waitFor();
    await page.locator('[data-card-id="healthy"] .query-compact-metric').waitFor();
    await page.waitForFunction(() => document.querySelector('.home-conveyor').getAttribute('aria-busy') === 'false');
    await card.hover();
    const tools = await card.locator('.query-card-tools button').evaluateAll(buttons => buttons.map(button => button.getAttribute('aria-label')));
    assert.deepEqual(tools, ['Refresh My words', 'Edit My words', 'Remove My words'], 'refresh sits beside edit and remove');
    await page.evaluate(() => { window.healthyPreview = document.querySelector('[data-card-id="healthy"] .query-compact-metric'); });
    const url = page.url();
    data.holdPreviews = true; data.requests.length = 0;
    await card.getByRole('button', { name: 'Refresh My words' }).click();
    await card.locator('.query-preview-loading').waitFor();
    await assertEventually(() => data.pending.length === 1);
    assert.deepEqual(data.requests.map(request => request.dataset), ['writing_messages'], 'only the refreshed card fetches');
    assert.equal(await page.evaluate(() => window.healthyPreview === document.querySelector('[data-card-id="healthy"] .query-compact-metric')), true);
    data.holdPreviews = false; for (const pending of data.pending.splice(0)) pending.release();
    await card.locator('.query-cover-timeline').waitFor();
    assert.equal(page.url(), url, 'refreshing stays on Home');

    const tally = () => data.requests.reduce((counts, { dataset }) => ({ ...counts, [dataset]: (counts[dataset] ?? 0) + 1 }), {});
    data.requests.length = 0;
    await page.evaluate(() => window.dispatchEvent(new CustomEvent('pharos:data-refresh', { detail: 'sync' })));
    await assertEventually(() => ['writing_messages', 'tool_calls'].every(dataset => data.requests.some(request => request.dataset === dataset)));
    await page.waitForFunction(() => document.querySelector('.home-conveyor').getAttribute('aria-busy') === 'false');
    await page.waitForTimeout(500);
    const once = tally();
    // A hidden tab waits until it is shown, then reruns once.
    const setHidden = hidden => page.evaluate(hidden => { Object.defineProperty(document, 'hidden', { value: hidden, configurable: true }); document.dispatchEvent(new Event('visibilitychange')); }, hidden);
    await setHidden(true);
    data.requests.length = 0;
    for (let i = 0; i < 2; i++) await page.evaluate(() => window.dispatchEvent(new CustomEvent('pharos:data-refresh', { detail: 'sync' })));
    await page.waitForTimeout(500);
    assert.deepEqual(data.requests, [], 'a hidden Home does not rerun its cards');
    await setHidden(false);
    await assertEventually(() => ['writing_messages', 'tool_calls'].every(dataset => data.requests.some(request => request.dataset === dataset)));
    await page.waitForFunction(() => document.querySelector('.home-conveyor').getAttribute('aria-busy') === 'false');
    await page.waitForTimeout(500);
    assert.deepEqual(tally(), once, 'showing the tab reruns the cards once');
    await card.locator('.query-cover-timeline').waitFor();
    assert.deepEqual(data.errors, []);
  } finally { data.holdPreviews = false; for (const pending of data.pending.splice(0)) pending.release(); await browser.close(); }
});

test('unavailable library previews reload and cancel retries when the time window changes', async () => {
  const { browser, page, data } = await setup({ url: '/home?cards=library&card_search=cost-repos' });
  try {
    const card = page.locator('#card-cost-repos');
    await card.locator('.query-cover-timeline').waitFor();
    data.previewFailures.add('usage');
    await clearPreviewCache(page);
    await page.getByLabel('Preview window', { exact: true }).selectOption('30');
    await card.locator('.query-preview-unavailable').waitFor();
    // Let the refreshed catalog finish so only this card's retry is held.
    await page.waitForFunction(() => document.querySelector('.home-conveyor')?.getAttribute('aria-busy') === 'false');
    await page.setViewportSize({ width: 390, height: 844 });
    await card.screenshot({ path: path.join(root, '.context/preview-unavailable-mobile.png') });
    const bounds = await card.getByRole('button', { name: /^Reload preview/ }).boundingBox();
    assert.ok(bounds.x >= 0 && bounds.x + bounds.width <= 390);
    data.previewFailures.clear(); data.holdPreviews = true;
    await card.getByRole('button', { name: /^Reload preview/ }).click();
    await assertEventually(() => data.pending.length === 1);
    const stale = data.pending.shift();
    await page.getByLabel('Preview window', { exact: true }).selectOption('90');
    await assertEventually(() => data.pending.length === 1);
    data.holdPreviews = false; for (const pending of data.pending.splice(0)) pending.release();
    await card.locator('.query-cover-timeline').waitFor();
    const current = await card.locator('.query-cover').innerText();
    stale.release();
    await page.waitForTimeout(100);
    assert.equal(await card.locator('.query-cover').innerText(), current, 'the older retry cannot replace the new window');
    assert.equal(await card.locator('.query-window').innerText(), 'Last 90d');
    assert.equal(await card.locator('.query-preview-unavailable').count(), 0);
    assert.deepEqual(data.errors, []);
  } finally { data.holdPreviews = false; for (const pending of data.pending.splice(0)) pending.release(); await browser.close(); }
});

test('card loading has a spinner and skeleton, respects reduced motion and clears when ready', async () => {
  const { browser, page, data } = await setup({ url: '/home?cards=library&card_search=human-words' });
  try {
    await page.locator('#card-human-words .query-cover-timeline').waitFor();
    data.holdPreviews = true;
    await clearPreviewCache(page);
    await page.getByLabel('Preview window', { exact: true }).selectOption('30');
    const card = page.locator('#card-human-words');
    await card.locator('.query-preview-spinner').waitFor();
    assert.equal(await card.locator('.query-cover').getAttribute('aria-busy'), 'true');
    assert.equal(await card.locator('.query-preview-loading-bars i').count(), 31);
    assert.equal(await card.locator('.query-preview-loading').getAttribute('data-loading-kind'), 'columns');
    assert.equal(await card.locator('.query-loading-headline').count(), 1);
    assert.equal(await card.locator('.query-preview-spinner').evaluate(el => getComputedStyle(el).animationName), 'query-preview-spin');
    await card.screenshot({ path: path.join(root, '.context/card-review/card-loading.png') });
    await page.emulateMedia({ reducedMotion: 'reduce' });
    assert.equal(await card.locator('.query-preview-spinner').evaluate(el => getComputedStyle(el).animationName), 'none');
    data.holdPreviews = false;
    for (const pending of data.pending.splice(0)) pending.release();
    await card.locator('.query-cover-timeline').waitFor();
    assert.equal(await card.locator('.query-preview-spinner').count(), 0);
    assert.equal(await card.locator('.query-cover').getAttribute('aria-busy'), null);
    assert.deepEqual(data.errors, []);
  } finally { data.holdPreviews = false; for (const pending of data.pending.splice(0)) pending.release(); await browser.close(); }
});

test('compact log axes use round ticks and a measured gutter on desktop and phone', async () => {
  const { browser, page, data } = await setup({ url: '/home?cards=library&card_search=session-token-cost' });
  try {
    data.rows.usage=data.rows.usage.map((row,i)=>({...row, total_tokens: 14400 * (1785000000 / 14400) ** (i / 27), cost_usd: .000451 * (1424.5 / .000451) ** (i / 27), root_conversation_id:`root-${i}`,root_provider:'codex',root_model_family:'gpt-5.4',root_effort:'high'}));
    await page.reload();
    const card = page.locator('#card-session-token-cost');
    await card.locator('.qt-metric-axis-labels').waitFor();
    for (const width of [1470, 390]) {
      await page.setViewportSize({ width, height: 844 });
      const labels = card.locator('.qt-metric-axis-labels>span'), first = await labels.nth(0).boundingBox(), second = await labels.nth(1).boundingBox();
      assert.ok(Math.abs(first.y - second.y) < 1, 'axis labels share one row');
      assert.match(await labels.nth(0).innerText(), /^↑ /);
      assert.match(await labels.nth(1).innerText(), /^→ /);
      assert.equal(await card.locator('svg text[transform]').count(), 0);
      const clip = card.locator('svg clipPath rect');
      const gutter = Number(await clip.getAttribute('x'));
      assert.ok(gutter < 35, `the gutter fits the short labels (${gutter}px)`);
      const svg = await card.locator('.qt-metric-plot svg').boundingBox();
      assert.ok(Number(await clip.getAttribute('width')) >= svg.width * .8, 'at least 80% of the width is available for points');
      assert.deepEqual(await card.locator('.query-scatter-x-ticks text').allTextContents(), ['100K', '10M', '1B']);
      assert.deepEqual(await card.locator('.query-scatter-y-ticks text').allTextContents(), ['0.001', '0.1', '10', '1K']);
      for (const tick of await card.locator('svg text').all()) {
        const bounds = await tick.boundingBox();
        assert.ok(bounds.x >= svg.x && bounds.x + bounds.width <= svg.x + svg.width + 1, 'ticks fit inside the card');
      }
      assert.equal(await card.locator('.qt-metric-mark').count(), 28, 'all original pairs remain drawn');
      assert.ok(first.y >= svg.y + svg.height - 1, 'axis labels sit below the plot');
      const cover = await card.locator('.query-cover').boundingBox();
      assert.ok(first.y + first.height <= cover.y + cover.height + 1);
      assert.ok(await page.locator('.card-library').evaluate(el => el.scrollWidth <= el.clientWidth + 1));
      await card.locator('.query-card').screenshot({ path: path.join(root, `.context/scatter-axis-${width}.png`) });
      await card.locator('.qt-metric-mark').last().hover();
      const tooltip = await page.getByRole('tooltip').innerText();
      assert.match(tooltip, /Recorded tokens.*14,400/);
      assert.match(tooltip, /API equivalent.*0\.000451/, 'hover preserves values below a cent');
      assert.equal(await page.getByRole('tooltip').locator('.chart-tooltip-swatch').count(), 1, 'model color survives in hover details');
      await page.keyboard.press('Escape');
    }
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('configured list and box row counts expand the cover to fit every selected row', async () => {
  const { browser, page, data } = await setup({ url: '/home?cards=library&card_search=error-reach' });
  try {
    data.rows.tool_calls.forEach((row, i) => { row.error_count = 1; row.error_signature = `Long error about missing configuration settings for feature ${i % 7}`; row.command_name = `command-${i % 7}`; });
    await page.reload();
    const list = page.locator('#card-error-reach');
    await list.locator('.qt-metric-bars').waitFor();
    await page.getByRole('button', { name: /^Edit card / }).click();
    await page.getByLabel('Visible rows / series', { exact: true }).fill('6');
    await page.getByRole('button', { name: 'Save template', exact: true }).click();
    await page.waitForFunction(() => document.querySelectorAll('#card-error-reach .qt-metric-bar-row').length === 6);
    for (const width of [1470, 390]) {
      await page.setViewportSize({ width, height: 844 });
      await waitForBarsInCover(list);
      const cover = await list.locator('.query-cover').boundingBox();
      assert.ok(cover.height > 170);
      for (const bar of await list.locator('.qt-metric-bar-track').all()) { const rect = await bar.boundingBox(); assert.ok(rect.y + rect.height <= cover.y + cover.height + 1); }
    }
    await page.getByLabel('Find a card', { exact: true }).fill('duration-distribution');
    const box = page.locator('#card-duration-distribution');
    await box.locator('.qt-metric-box-row').first().waitFor();
    await page.getByRole('button', { name: /^Edit card / }).click();
    await page.getByLabel('Visible rows / series', { exact: true }).fill('5');
    await page.getByRole('button', { name: 'Save template', exact: true }).click();
    try { await page.waitForFunction(() => document.querySelectorAll('#card-duration-distribution .qt-metric-box-row').length === 5); } catch (error) { console.log(await box.evaluate(el => ({text:el.innerText,geometry:[...el.querySelectorAll('.query-cover,.query-compact-metric,.qt-metric,.qt-metric-chart-layout,.qt-metric-plot,svg[role=group]')].map(e=>({cls:e.className.baseVal??e.className,height:e.getBoundingClientRect().height,min:getComputedStyle(e).minHeight,style:e.getAttribute('style'),box:e.getAttribute('viewBox')}))}))); await box.screenshot({path:path.join(root,'.context/card-review/box-row-limit-debug.png')}); throw error; }
    const cover = await box.locator('.query-cover').boundingBox();
    for (const row of await box.locator('.qt-metric-box-row').all()) { const rect = await row.boundingBox(); assert.ok(rect.y + rect.height <= cover.y + cover.height + 1); }
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('contextual card loaders match saved chart types and row limits before data arrives', async () => {
  const { browser, page, data } = await setup({ url: '/home?cards=library&card_search=cost-repos', viewport: { width: 1470, height: 836 }, deviceScaleFactor: 2 });
  const search = page.getByLabel('Find a card', { exact: true });
  try {
    await page.locator('#card-cost-repos .query-cover-timeline').waitFor();
    data.holdPreviews = true;
    await clearPreviewCache(page);
    await page.getByRole('button', { name: /^Edit card / }).click();
    await page.getByLabel('Chart', { exact: true }).selectOption('bar-vertical');
    await page.getByLabel('Visible rows / series', { exact: true }).fill('4');
    await page.getByRole('button', { name: 'Save template', exact: true }).click();
    const columns = page.locator('#card-cost-repos');
    await columns.locator('[data-loading-kind="columns"]').waitFor();
    assert.equal(await columns.locator('.query-preview-loading-bars i').count(), 4);
    assert.equal(await columns.locator('.query-preview-loading-bars i').first().evaluate(el => getComputedStyle(el).animationDuration), '1.5s');
    assert.equal(await columns.locator('.query-preview-spinner').evaluate(el => getComputedStyle(el).animationDuration), '0.8s');
    await columns.screenshot({ path: path.join(root, '.context/card-review/loading-four-columns.png') });
    await search.fill('session-input-output');
    const scatter = page.locator('#card-session-input-output');
    await scatter.locator('[data-loading-kind="scatter"]').waitFor();
    assert.equal(await scatter.locator('.query-loading-scatter i').count(), 100);
    const dot = scatter.locator('.query-loading-scatter i').first();
    assert.equal(await dot.evaluate(el => getComputedStyle(el).animationName), 'query-preview-drift');
    const position = await dot.evaluate(el => getComputedStyle(el).transform);
    await page.waitForTimeout(300);
    assert.notEqual(await dot.evaluate(el => getComputedStyle(el).transform), position, 'scatter dots gently drift while pulsing');
    await scatter.screenshot({ path: path.join(root, '.context/card-review/loading-scatter.png') });
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok(await page.locator('.card-library').evaluate(el => el.scrollWidth <= el.clientWidth + 1));
    await scatter.screenshot({ path: path.join(root, '.context/card-review/loading-scatter-phone.png') });
    await page.emulateMedia({ reducedMotion: 'reduce' });
    assert.equal(await dot.evaluate(el => getComputedStyle(el).animationName), 'none');
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    await page.setViewportSize({ width: 1470, height: 836 });
    await search.fill('price-coverage');
    const scalar = page.locator('#card-price-coverage');
    await scalar.locator('[data-loading-kind="donut"]').waitFor();
    assert.equal(await scalar.locator('.query-loading-circle').count(), 1);
    await page.getByRole('button', { name: /^Edit card / }).click();
    await page.getByLabel('Chart', { exact: true }).selectOption('value');
    await page.getByRole('button', { name: 'Save template', exact: true }).click();
    await scalar.locator('[data-loading-kind="value"]').waitFor();
    assert.equal(await scalar.locator('.query-loading-value').count(), 1);
    assert.equal(await scalar.locator('.query-preview-loading-bars').count(), 0);
    await scalar.screenshot({ path: path.join(root, '.context/card-review/loading-value.png') });
    await search.fill('duration-distribution');
    const boxes = page.locator('#card-duration-distribution');
    await boxes.locator('[data-loading-kind="box"]').waitFor();
    assert.equal(await boxes.locator('.query-loading-whisker').count(), 6);
    await boxes.screenshot({ path: path.join(root, '.context/card-review/loading-boxes.png') });
    await search.fill('error-reach');
    const list = page.locator('#card-error-reach');
    await list.locator('[data-loading-kind="list"]').waitFor();
    assert.equal(await list.locator('.query-loading-row').count(), 6);
    assert.equal(await list.locator('.query-preview-loading-shape.has-below-labels').count(), 1);
    await list.screenshot({ path: path.join(root, '.context/card-review/loading-list.png') });
    await search.fill('cost-repos');
    await page.getByRole('button', { name: /^Edit card / }).click();
    await page.getByLabel('Chart', { exact: true }).selectOption('line');
    await page.getByRole('button', { name: 'Save template', exact: true }).click();
    await columns.locator('[data-loading-kind="line"]').waitFor();
    assert.equal((await columns.locator('.query-loading-line polyline').getAttribute('points')).split(' ').length, 4);
    await columns.screenshot({ path: path.join(root, '.context/card-review/loading-line.png') });
    data.holdPreviews = false;
    for (const pending of data.pending.splice(0)) pending.release();
    await columns.locator('.qt-metric-plot svg').waitFor();
    assert.equal(await columns.locator('.query-preview-loading').count(), 0);
    assert.deepEqual(data.errors, []);
  } finally { data.holdPreviews = false; for (const pending of data.pending.splice(0)) pending.release(); await browser.close(); }
});

// Older preferences may retain a cover reference after an external saved-query
// update. The Home editor must let those users recover without a metric picker.
test('kept cards with a stale cover and no metrics can be renamed in Home', async () => {
  global.location = { origin: 'http://home.test' };
  const query = { ...core.EMPTY_QUERY, select: [{ field: 'id' }, { field: 'tool_name' }], limit: 10 };
  const bookmark = { id: 'stale-cover', savedQueryId: 'stale-cover', dataset: 'tool_calls', query, url: links.queryURL('tool_calls', query), title: 'Changed saved query', savedAt: 100, cover: { mode: 'source', metricId: 'removed-metric', maxGroups: 20 } };
  const { browser, page, data } = await setup({ preferenceValues: { 'pharos-bookmarks-v1': [bookmark] } });
  try {
    const card = page.locator('.home-collection [data-card-id="stale-cover"]');
    await card.hover();
    await card.getByRole('button', { name: /^Edit / }).click();
    assert.equal(await page.getByLabel('Cover metric', { exact: true }).count(), 0);
    await page.getByLabel('Title', { exact: true }).fill('Renamed after query change');
    await page.getByLabel('Description', { exact: true }).fill('The updated row view remains editable.');
    await page.getByRole('button', { name: 'Save changes', exact: true }).click();
    await page.locator('.home-editor').waitFor({ state: 'hidden' });
    await assertEventually(() => data.preferences['pharos-bookmarks-v1']?.[0]?.title === 'Renamed after query change');
    assert.equal(data.preferences['pharos-bookmarks-v1'][0].cover, undefined);
    assert.equal(await page.locator('.home-editor [role="alert"]').count(), 0);
    await page.reload();
    await card.getByText('Renamed after query change', { exact: true }).waitFor();
    assert.deepEqual(data.errors, []);
    await card.screenshot({ path: path.join(root, '.context/review2-renamed-card.png') });
  } finally { delete global.location; await browser.close(); }
});

test('card covers keep friendly work labels across twenty visible rows', async () => {
  const { browser, page, data } = await setup({ url: '/home?cards=library&card_search=usage-work', viewport: { width: 1470, height: 1100 } });
  try {
    data.rows.usage = data.rows.usage.slice(0, 25).map((row, i) => ({ ...row, workspace_id: `workspace-${i}`, root_conversation_id: `root-${i}`, root_repository_name: "repo", root_title: `Friendly work ${i + 1}`, agent_session_id: `session-${i}`, title: `Friendly work ${i + 1}`, total_tokens: 100000 - i * 1000 }));
    await page.reload();
    const card = page.locator('#card-usage-work');
    await card.locator('.qt-metric-bar-row').first().waitFor();
    await page.getByRole('button', { name: /^Edit card / }).click();
    await page.getByLabel('Visible rows / series', { exact: true }).fill('2');
    await page.getByLabel('Other remainder', { exact: true }).selectOption('true');
    await page.getByRole('button', { name: 'Save template', exact: true }).click();
    await page.waitForFunction(() => document.querySelectorAll('#card-usage-work .qt-metric-bar-row').length === 3);
    assert.match(await card.locator('.qt-metric-bar-row').last().innerText(), /Other[\s\S]*2M$/);
    await page.getByRole('button', { name: /^Edit card / }).click();
    await page.getByLabel('Visible rows / series', { exact: true }).fill('20');
    await page.getByLabel('Other remainder', { exact: true }).selectOption('false');
    await page.getByRole('button', { name: 'Save template', exact: true }).click();
    await page.waitForFunction(() => document.querySelectorAll('#card-usage-work .qt-metric-bar-row').length === 20);
    const labels = await card.locator('.qt-metric-bar-row').allTextContents();
    assert.ok(labels.every((label, i) => label.includes(`Friendly work ${i + 1}`)), labels.join('\n'));
    assert.doesNotMatch(labels.join('\n'), /workspace-/);
    assert.deepEqual(data.errors, []);
    await card.screenshot({ path: path.join(root, '.context/review2-friendly-labels.png') });
  } finally { await browser.close(); }
});

test('opening and immediately saving a restored template preserves all cover settings', async () => {
  const cover = { mode: 'result', metricId: 'card_cost_usd', kind: 'auto', maxGroups: 2, showLegend: true, showOther: false };
  const edit = { title: 'My repository cost', description: 'Saved presentation', days: 30, cover };
  const { browser, page, data } = await setup({ url: '/home?cards=library&card_search=cost-repos', preferenceValues: { 'pharos-card-templates-v1': { 'cost-repos': edit } } });
  try {
    await page.evaluate(() => {
      const showModal = HTMLDialogElement.prototype.showModal;
      HTMLDialogElement.prototype.showModal = function() {
        showModal.call(this);
        if (!this.classList.contains('card-template-editor')) return;
        window.initialTemplateFields = [...this.querySelectorAll('input,select,textarea')].map(el => [el.getAttribute('aria-label') ?? el.closest('label')?.firstChild?.textContent, el.value]);
        this.querySelector('button[type="submit"]').click();
      };
    });
    const savedPreferences = page.waitForResponse(response => new URL(response.url()).pathname === '/api/preferences'
      && response.request().postDataJSON()?.set?.['pharos-card-templates-v1']);
    await page.getByRole('button', { name: /^Edit card / }).click();
    const fields = Object.fromEntries(await page.evaluate(() => window.initialTemplateFields));
    assert.equal(fields['Visible rows / series'], '2', 'the first exposed form must contain the saved limit');
    assert.equal(fields['Other remainder'], 'false');
    assert.equal(fields['Chart key'], 'true');
    assert.equal(fields['Default lookback'], '30');
    await page.locator('.card-template-editor').waitFor({ state: 'hidden' });
    await savedPreferences;
    await assertEventually(() => data.preferences['pharos-card-templates-v1']?.['cost-repos']?.cover?.maxGroups === 2);
    assert.deepEqual(data.preferences['pharos-card-templates-v1']['cost-repos'], edit);
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('retired Steering category stays out of discovery and preserves old saved cards and feedback', async () => {
 const draft={overall:'Drop this category.',cards:{'positive-feedback':{verdict:'drop',note:'Retired.'}}};
 const {browser,page,data}=await setup({url:'/home?cards=library&card_topic=steering',localValues:{'pharos-steering-review-v1':draft}});
 try {
  await page.waitForFunction(()=>!document.querySelector('.card-library-count').textContent.includes('Loading charts'));
  assert.equal(await page.locator('.card-library [role="tab"][data-topic="steering"]').count(),0);
  assert.equal(await page.getByRole('tab',{selected:true}).getAttribute('data-topic'),'all');
  assert.equal(new URL(page.url()).searchParams.get('card_topic'),'all');
  assert.equal(await page.locator('a[href*="cards=steering-review"]').count(),0);
  for(const id of ['correction-phrases','correction-conversation-share','positive-feedback','positive-conversation-share','repeated-messages','within-conversation-repeats'])assert.equal(await page.locator(`[data-card-id="${id}"]`).count(),0);
  await page.goto('http://home.test/home?cards=steering-review');
  await page.waitForFunction(()=>document.querySelector('.home-conveyor')?.getAttribute('aria-busy')==='false');
  assert.equal(await page.locator('.human-review-entry,a[href*="cards=steering-review"]').count(),0);
  assert.equal(await page.locator('.home-belt-row .query-category').filter({hasText:'Steering'}).count(),0);
  assert.deepEqual(await page.evaluate(()=>JSON.parse(localStorage.getItem('pharos-steering-review-v1'))),draft);
  const item=instantiateRecipe(queryRecipes.find(r=>r.id==='correction-phrases'),90);
  const saved={id:item.id,title:item.title,description:item.description,url:item.url,dataset:item.dataset,savedQueryId:item.id,templateId:item.instanceId};
  await page.evaluate(item=>{window.pharosPrefs.set('pharos-bookmarks-v1',[item]);window.dispatchEvent(new Event('pharos:bookmarks'));},saved);
  const card=page.locator('.home-collection [data-card-id="correction-phrases"]');await card.waitFor();assert.equal(await card.locator('.query-category').innerText(),'Steering');
  await card.locator('.query-card-open').click();await waitForQuery(page,'writing_messages',q=>q.grouping?.source.where.some(t=>t.field==='correction_cue'&&t.value==='true'));assert.deepEqual(data.errors,[]);
 }finally{await browser.close();}
});

test('card token destinations ignore stale Machine view preferences and retain explicit URL scopes', async () => {
 const item=instantiateRecipe(queryRecipes.find(r=>r.id==='task-token-ranges'),30);
 const {browser,page,data}=await setup({url:item.url,waitForHome:false,preferenceValues:{'pharos-machine-view':'time','pharos-machine-scope':{days:7,repository:'stale-repo',provider:'claude',model:'stale-model'}}});
 try {
  await waitForQuery(page,'usage',q=>q.grouping?.source.where.some(t=>t.field==='first_usage_at'&&t.value==='-30d'));
  const q=await readQuery(page,'usage');assert.deepEqual(q.grouping.source.where,item.query.grouping.source.where);
  assert.equal(new URL(page.url()).searchParams.get('machine_view'),'tokens');assert.equal(await page.getByRole('button',{name:/^Machine Tokens/}).getAttribute('aria-pressed'),'true');
  const scoped=new URL(item.url,'http://home.test');scoped.searchParams.set('activity_repo','explicit-repo');await page.goto(scoped.href);
  await waitForQuery(page,'usage',q=>q.grouping?.source.where.some(t=>t.field==='repository_name'&&t.value==='explicit-repo'));
  const source=(await readQuery(page,'usage')).grouping.source.where;assert.ok(source.some(t=>t.field==='first_usage_at'&&t.value==='-30d'));assert.ok(!source.some(t=>t.value==='stale-repo'));assert.deepEqual(data.errors,[]);
 }finally{await browser.close();}
});
