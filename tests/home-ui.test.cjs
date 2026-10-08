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
  new Function('module', 'exports', buildSync({ entryPoints: [path.join(root, entry)], bundle: true, platform: 'node', format: 'cjs', write: false }).outputFiles[0].text)(loaded, loaded.exports);
  return loaded.exports;
}
const core = moduleOf('web/node_modules/@pythia-software/query-table-core/dist/index.js');
const links = moduleOf('web/src/query-links.ts');
const { queryRecipes, instantiateRecipe } = moduleOf('web/src/curiosity-data.ts');
const { categoryForQuery } = moduleOf('web/src/query-categories.ts');
const schemas = Object.fromEntries(links.queryDatasets.map(dataset => [dataset, core.loadSchema(JSON.parse(fs.readFileSync(path.join(root, `schemas/${dataset}.schema.json`))))]));

function fixtures() {
  const calls = [], usage = [], writing = [], skills = [];
  for (let i = 0; i < 28; i++) {
    const date = new Date(); date.setDate(date.getDate() - i % 7); date.setHours(12, 0, 0, 0);
    const day = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`;
    const repository_name = ['pharos', 'query-table', 'workbench'][i % 3];
    calls.push({ id: `call-${i}`, day, started_at: date.toISOString(), tool_name: ['Edit', 'Bash', 'Read'][i % 3], tool_category: ['edit', 'command', 'read'][i % 3], status: i % 2 ? 'ok' : 'error', error_count: i % 2 ? 0 : 1, call_count: 1, error_signature: 'file_not_read', duration_ms: (i + 1) * 5000, command_name: 'go test', repo_path: `src/module-${i % 3}.go`, test_failure: i % 2 === 0, carried_tokens: (i + 1) * 1200, result_tokens: 300 + i * 40, truncated: i % 4 === 0, search_query: 'SQLite indexing', host: 'go.dev', mcp_method: 'search_work', repository_name, title: 'Archive indexing' });
    usage.push({ id: `usage-${i}`, day, first_usage_at: date.toISOString(), last_usage_at: date.toISOString(), repository_name, total_tokens: 50000 + i * 7000, model_family: ['sonnet-4-6', 'gpt-5.4'][i % 2], cost_usd: i / 2 + 1, cache_read_input_tokens: i * 400, session_kind: i % 3 ? 'root' : 'subagent' });
    writing.push({ id: `writing-${i}`, day, sent_at: date.toISOString(), text: i % 2 ? 'Please inspect how indexes affect the query plan.' : 'I meant the timestamp index, not the title index.', typed_message: true, typed_words: i + 20, repository_name, title: 'Archive indexing' });
    skills.push({ id: `skill-${i}`, day, created_at: date.toISOString(), skill_name: ['review', 'debug', 'visualize'][i % 3], status: i % 3 ? 'ok' : 'error', repository_name });
  }
  return { tool_calls: calls, usage, writing_messages: writing, skill_usages: skills };
}
async function setup({ saved = false, reducedMotion = false, clock = false, viewport = { width: 1180, height: 920 }, deviceScaleFactor = 1, url = '/', waitForHome = url === '/', preferenceValues = {}, localValues = {}, now } = {}) {
  const browser = await chromium.launch({ headless: true, executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
  const page = await browser.newPage({ viewport, deviceScaleFactor, timezoneId: 'America/Denver', reducedMotion: reducedMotion ? 'reduce' : 'no-preference' });
  if (clock) await page.clock.install();
  if (now) await page.clock.setFixedTime(now);
  await page.addInitScript(values => { for (const [key, value] of Object.entries(values)) localStorage.setItem(key, JSON.stringify(value)); }, localValues);
  const data = { rows: fixtures(), empty: false, requests: [], errors: [], pending: [], holdPreviews: false, preferences: { 'pharos-theme': 'dark', ...preferenceValues } };
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
    if (url.pathname === '/api/preferences') {
      const body = route.request().postDataJSON(); Object.assign(data.preferences, body.set); for (const key of body.delete ?? []) delete data.preferences[key]; return json({});
    }
    if (['/assets/query-tables.js', '/assets/query-tables.css'].includes(url.pathname)) return route.fulfill({ contentType: url.pathname.endsWith('.css') ? 'text/css' : 'application/javascript', body: fs.readFileSync(path.join(root, 'internal/archive/assets', path.basename(url.pathname))) });
    if (url.pathname === '/api/query/writing/series') return json({ works: 0, daily: [], totals: {} });
    if (url.pathname.startsWith('/api/query/') && route.request().method() === 'POST') {
      const dataset = url.pathname.split('/')[3], request = route.request().postDataJSON(); data.requests.push({ dataset, request, path: url.pathname });
      const rows = (data.empty ? [] : data.rows[dataset] ?? []).map(row => Object.fromEntries(Object.entries(row).map(([key, value]) => [key, schemas[dataset].fields.find(field => field.name === key)?.type === 'datetime' ? Date.parse(value) : value])));
      if (data.holdPreviews) await new Promise(resolve => data.pending.push({ release: resolve }));
      if (url.pathname.endsWith('/aggregations')) return json(core.applyAggregations(rows, { ...core.EMPTY_QUERY, ...request }, schemas[dataset]));
      return json(core.applyQuery(rows, { ...core.EMPTY_QUERY, ...request }, schemas[dataset]));
    }
    if (url.pathname.endsWith('/distinct')) return json({ values: [], hasMore: false });
    if (url.pathname.endsWith('/field-stats')) return json({});
    if (url.pathname === '/api/tools/status') return json({ pending_conversations: 0, backfill: { running: false, error: null } });
    if (url.pathname === '/api/findings') return json({ status: { running: false }, items: [] });
    if (url.pathname.startsWith('/api/')) return json({ items: [], metrics: [], totals: {}, last_30_days: {}, sources: [], unpriced_models: [] });
    return route.fulfill({ status: 404 });
  });
  await page.goto(`http://home.test${url}`);
  if (clock) await page.clock.runFor(5000);
  if (waitForHome) await page.waitForFunction(() => document.querySelector('.home-conveyor')?.getAttribute('aria-busy') === 'false' && document.querySelector('.home-belt-row .query-card'));
  else await page.waitForFunction(() => document.querySelector('#queryTableLibrary .qt-qb'));
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
  assert.deepEqual(resolved.where.filter(term => term.field === 'started_at').map(term => term.value), links.rollingBounds(7, next));
  assert.deepEqual(resolved.aggregations, recipe.query.aggregations);
  assert.equal(resolved.where.filter(term => term.field === 'error_count').length, 1);
  const writing = instantiateRecipe(queryRecipes.find(item => item.id === 'human-words'), 7, clock);
  assert.match(writing.url, /usage=writing/); assert.match(writing.url, /writing=messages/);
  for (const item of queryRecipes) core.toServerQuery(instantiateRecipe(item, 7, clock).query, schemas[item.dataset]);
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

test('Home nav and logo activate previews and leaving Home suspends scans and autoplay', async () => {
  const { browser, page, data } = await setup({ url: '/library', saved: true, clock: true });
  try {
    for (const target of ['.tabs [data-view="home"]', '.brand']) {
      await page.locator(target).click();
      assert.equal(new URL(page.url()).pathname, '/');
      assert.equal(new URL(page.url()).searchParams.has('q_library'), false, 'Home URLs must not resemble old Library links');
      await page.clock.runFor(5000);
      await page.waitForFunction(() => document.querySelector('.home-conveyor')?.getAttribute('aria-busy') === 'false' && document.querySelector('.home-collection .query-result-summary'), undefined, { timeout: 5000 });
      const ids = await page.locator('.home-belt-row .query-card').evaluateAll(cards => cards.map(card => card.dataset.cardId));
      await page.mouse.move(5, 5);
      await page.locator('.tabs [data-view="library"]').click();
      await page.clock.runFor(1000);
      const requests = data.requests.length;
      await page.evaluate(() => window.dispatchEvent(new Event('pharos:data-refresh')));
      await page.clock.runFor(65000);
      assert.equal(data.requests.length, requests, 'hidden Home must not scan on a data refresh');
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
    assert.equal(await page.locator('#library').getByRole('searchbox', { name: 'Search library', exact: true }).inputValue(), 'indexing');
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
    assert.equal(await page.locator('#library').getByRole('searchbox', { name: 'Search library', exact: true }).inputValue(), 'indexing');
    assert.equal(new URL(page.url()).searchParams.get('kind'), 'file');
    assert.equal(new URL(page.url()).searchParams.get('case'), '1');
    await page.goBack();
    assert.equal(new URL(page.url()).pathname, '/');
    await page.locator('.tabs [data-view="library"]').click();
    await page.waitForFunction(() => new URL(location.href).searchParams.get('search') === 'indexing');
    assert.equal(new URL(page.url()).searchParams.get('kind'), 'file', 'visiting Home through history preserves the hidden Library search');
    await page.goBack();
    await page.goBack();
    assert.equal(new URL(page.url()).pathname, '/library');
    await page.goForward();
    assert.equal(new URL(page.url()).pathname, '/');
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
    await page.waitForFunction(({ dataset, value }) => {
      const token = new URL(location.href).searchParams.get(`q_${dataset}`);
      if (!token) return false;
      const query = JSON.parse(atob(token.replace(/-/g, '+').replace(/_/g, '/')));
      return query.w?.some(term => term.value === value);
    }, { dataset, value: `stored-${dataset}` }, { timeout: 5000 });
    assert.deepEqual(core.decodeQuery(new URL(page.url()).searchParams.get(`q_${dataset}`)).where, queries[dataset].where);
  };
  try {
    await page.locator('.tabs [data-view="library"]').click(); await check('library');
    const libraryURL = page.url();
    await page.getByRole('button', { name: 'Navigation bar', exact: true }).click();
    await page.locator('#bookmarkStar').click();
    await page.waitForFunction(() => window.pharosPrefs.get('pharos-bookmarks-v1', []).some(item => item.dataset === 'library' || item.url.startsWith('/library')));
    const libraryBookmark = await page.evaluate(() => window.pharosPrefs.get('pharos-bookmarks-v1', []).find(item => item.url.startsWith('/library')));
    assert.equal(new URL(libraryBookmark.url, page.url()).searchParams.get('q_library'), new URL(libraryURL).searchParams.get('q_library'));
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
    await page.waitForFunction(() => [...document.querySelectorAll('#library .qt-qb input')].some(input => input.value === 'incoming-library'));
    assert.deepEqual(core.decodeQuery(new URL(page.url()).searchParams.get('q_library')).where, incoming.where, 'route restoration must win over visibility synchronization');
    await page.goBack();
    assert.equal(await toolView.getByRole('button', { name: 'Skills', exact: true }).getAttribute('aria-pressed'), 'true');
    await check('skill_usages');
    await page.goForward();
    await page.waitForFunction(() => [...document.querySelectorAll('#library .qt-qb input')].some(input => input.value === 'incoming-library'));
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
  const { browser, page, data } = await setup({ url: item.url, now: before });
  try {
    await page.clock.setFixedTime(after);
    await page.getByRole('button', { name: 'Slowest calls', exact: true }).click();
    await page.waitForFunction(() => {
      const token = new URL(location.href).searchParams.get('q_tool_calls');
      return token && JSON.parse(atob(token.replace(/-/g, '+').replace(/_/g, '/'))).o?.some(sort => sort.field === 'duration_ms');
    }, undefined, { timeout: 5000 });
    assert.equal(new URL(page.url()).searchParams.get('window_days'), '7');
    assert.equal(new URL(page.url()).searchParams.get('window_field'), 'started_at');
    assert.equal(new URL(page.url()).searchParams.get('window_dataset'), 'tool_calls');
    await page.locator('#tools .qt-qb').filter({ visible: true }).getByRole('button', { name: 'Save query', exact: true }).click();
    const name = page.locator('#tools .qt-qb-saved-input').filter({ visible: true });
    await name.fill('Failures after midnight'); await name.press('Enter');
    await page.waitForFunction(() => window.pharosPrefs.get('pharos-bookmarks-v1', []).some(item => item.title === 'Failures after midnight'));
    const saved = await page.evaluate(() => window.pharosPrefs.get('pharos-bookmarks-v1', []).find(item => item.title === 'Failures after midnight'));
    assert.equal(new URL(saved.url, page.url()).searchParams.get('window_days'), '7');
    await page.evaluate(url => window.pharosNavigate(url), saved.url);
    await page.waitForFunction(expected => {
      const token = new URL(location.href).searchParams.get('q_tool_calls');
      return token && JSON.parse(atob(token.replace(/-/g, '+').replace(/_/g, '/'))).w?.some(term => term.field === 'started_at' && term.op === '>=' && term.value === expected);
    }, links.rollingBounds(7, after)[0]);
    const query = core.decodeQuery(new URL(page.url()).searchParams.get('q_tool_calls'));
    assert.deepEqual(query.where.filter(term => term.field === 'started_at').map(term => term.value), links.rollingBounds(7, after), 'reopening rolls the dates forward');
    const partialDay = new Date(Date.parse(links.rollingBounds(7, after)[0]) + 3600000).toISOString();
    await page.locator('#tools .qt-qb').filter({ visible: true }).locator('input[aria-label$="for Started"]').first().fill(partialDay);
    await page.waitForFunction(() => !new URL(location.href).searchParams.has('window_days'));
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
    assert.deepEqual(spec.window, { days: 30, field: 'activity_at' });
    assert.deepEqual(spec.query.where.filter(term => term.field === 'activity_at').map(term => [term.op, term.value]), [['>=', links.rollingBounds(30)[0]], ['<', links.rollingBounds(30)[1]]]);
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
    assert.equal(await page.getByRole('searchbox', { name: 'Search your library' }).evaluate(element => element === document.activeElement), true);
    const types = page.getByRole('group', { name: 'Search type', exact: true });
    await types.getByRole('button', { name: 'Modified file' }).click();
    assert.equal(await types.getByRole('button', { name: 'Modified file' }).getAttribute('aria-pressed'), 'true');
    await page.waitForTimeout(260);
    const selected = await types.getByRole('button', { name: 'Modified file' }).boundingBox(), thumb = await types.locator('.home-search-thumb').boundingBox();
    assert.ok(Math.abs(thumb.x - selected.x) < 1 && Math.abs(thumb.width - selected.width) < 1);
    await types.getByRole('button', { name: 'Conversation text' }).click();
    await page.getByRole('searchbox', { name: 'Search your library' }).fill('query plan');
    await page.locator('.home-search-options').getByLabel('Fuzzy words').uncheck();
    await page.locator('.home-search-options').getByLabel('Case sensitive').check();
    await page.locator('.home-search-options').getByLabel('Match separators').check();
    await page.locator('.home-search').getByRole('button').click();
    assert.equal(new URL(page.url()).pathname, '/library');
    for (const [key, value] of [['search', 'query plan'], ['fuzzy', '0'], ['case', '1'], ['separators', '1']]) assert.equal(new URL(page.url()).searchParams.get(key), value);
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
    await page.waitForFunction(() => document.querySelector('.home-conveyor')?.getAttribute('aria-busy') === 'false' && document.querySelector('.home-discovery-empty'));
    assert.equal(await page.locator('.home-collection .query-card').count(), 2);
    await page.waitForFunction(() => [...document.querySelectorAll('.home-collection .query-preview-status')].every(node => node.textContent === 'No recent matches'));
    assert.equal(await page.locator('.home-belt-row .query-card:not([data-xray])').count(), 0);
    assert.equal(data.preferences['pharos-bookmarks-v1'].length, 2);
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('the card editor updates its rolling timescale, preview, saved query and navigation together', async () => {
  const { browser, page, data } = await setup({ saved: true });
  try {
    await page.setViewportSize({ width: 2133, height: 1075 });
    await page.getByRole('button', { name: 'Pause suggestions' }).click();
    const card = page.locator('.home-collection .query-card').first();
    await page.waitForFunction(() => document.querySelector('.home-collection .query-result-summary')?.textContent === '938 words');
    const originalID = await card.getAttribute('data-card-id');
    await card.hover(); await card.getByRole('button', { name: 'Edit My words' }).click();
    const windowSelect = page.getByRole('combobox', { name: 'Time window' });
    assert.equal(await windowSelect.inputValue(), '');
    assert.deepEqual(await windowSelect.locator('option').evaluateAll(options => options.map(option => option.value)), ['', '1', '7', '30', '90']);
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
    assert.equal(await card.locator('.query-window').innerText(), '1d');
    assert.equal(await card.locator('.query-cover-columns > span').count(), 1);
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
    await page.getByRole('group', { name: 'Message result view' }).waitFor();
    assert.equal(new URL(page.url()).pathname, '/usage');
    assert.equal(new URL(page.url()).searchParams.get('window_days'), '1');
    await page.goto('http://home.test/');
    await page.waitForFunction(() => document.querySelector('.home-collection .query-result-summary')?.textContent === '122 words');
    await card.hover(); await card.getByRole('button', { name: 'Edit My words' }).click();
    assert.equal(await windowSelect.inputValue(), '1', 'the updated window survives a reload');
    await page.getByRole('button', { name: 'Cancel', exact: true }).click();
    assert.deepEqual(data.errors, []);
  } finally { await browser.close(); }
});

test('carousel advances three every thirty seconds and pauses during interaction or reduced motion', async () => {
  const { browser, page, data } = await setup({ clock: true });
  try {
    const initial = await page.locator('.home-belt-row .query-card:not([data-xray])').first().getAttribute('data-card-id');
    assert.match(await page.locator('.home-countdown').innerText(), /More in \d+s/);
    await page.clock.fastForward(30_250);
    await page.locator('.home-belt-row.sliding').waitFor();
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

test('the loading belt grows from a fixed middle in ready pairs before introducing x-rays', async () => {
  const { browser, page, data } = await setup();
  const releaseOne = async () => {
    for (let attempt = 0; !data.pending.length && attempt < 50; attempt++) await page.waitForTimeout(20);
    assert.ok(data.pending.length, 'a preview should be waiting for its response');
    data.pending.shift().release();
  };
  const real = page.locator('.home-belt-row .query-card:not([data-xray])');
  try {
    await page.setViewportSize({ width: 2133, height: 1075 });
    await page.getByRole('button', { name: 'Pause suggestions' }).click();
    data.holdPreviews = true;
    await page.getByRole('button', { name: 'Refresh suggestions' }).click();
    await page.mouse.move(5, 5);
    await page.waitForFunction(() => document.querySelectorAll('.home-belt-row .query-card').length === 0);
    await releaseOne();
    await page.waitForFunction(() => document.querySelectorAll('.home-belt-row .query-card').length === 1);
    const seed = await real.first().getAttribute('data-card-id'), seedCard = page.locator(`.home-belt-row [data-card-id="${seed}"]`);
    const lean = await seedCard.evaluate(card => card.style.getPropertyValue('--query-lean'));
    const origin = await seedCard.boundingBox();
    assert.ok(Math.abs(origin.x + origin.width / 2 - 2133 / 2) < 1, 'the first ready card starts in the middle');
    assert.equal(await page.locator('.home-belt-row [data-xray]').count(), 0);
    await releaseOne(); await page.waitForTimeout(400);
    assert.equal(await real.count(), 1, 'one waiting neighbor must not move or duplicate the middle card');
    await releaseOne();
    await page.waitForFunction(() => document.querySelectorAll('.home-belt-row .query-card').length === 3);
    await page.waitForTimeout(320);
    const bounds = await real.evaluateAll(cards => cards.map(card => { const box = card.getBoundingClientRect(); return { id: card.dataset.cardId, center: box.x + box.width / 2 }; }));
    assert.equal(bounds[1].id, seed);
    assert.equal(await seedCard.evaluate(card => card.style.getPropertyValue('--query-lean')), lean, 'new neighbors must not retip the existing card');
    assert.ok(Math.abs(bounds[1].center - 2133 / 2) < 1);
    assert.ok(Math.abs(bounds[1].center - bounds[0].center - 190) < 1 && Math.abs(bounds[2].center - bounds[1].center - 190) < 1);
    assert.equal(await page.locator('.home-belt-row [data-xray]').count(), 0);
    await page.screenshot({ path: path.join(root, '.context/homepage-design/home-belt-loading.png') });
    await releaseOne(); await page.waitForTimeout(400);
    assert.equal(await real.count(), 3);
    await releaseOne();
    await page.waitForFunction(() => document.querySelectorAll('.home-belt-row .query-card').length === 5);
    assert.equal(await page.locator('.home-belt-row [data-xray]').count(), 0);
    await releaseOne(); await releaseOne();
    await page.waitForFunction(() => document.querySelectorAll('.home-belt-row .query-card').length === 7);
    assert.equal(await real.count(), 5);
    assert.equal(await page.locator('.home-belt-row .query-xray-open').count(), 2);
    assert.equal(await page.locator('[data-belt-position="0"] .query-card').getAttribute('data-card-id'), seed);
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
    const arriving = page.locator(`.home-belt-row .query-card:not([data-xray])[data-card-id="${initial[3]}"]`);
    const before = await arriving.boundingBox();
    await page.getByRole('button', { name: 'Next three suggestions' }).click();
    await page.locator('.home-belt-row.sliding').waitFor();
    await page.waitForTimeout(180);
    const during = await arriving.boundingBox();
    assert.ok(during.x < before.x - 10 && during.x > before.x - 570, 'the same card should travel continuously into the reading area');
    await page.waitForFunction(() => !document.querySelector('.home-belt-row.sliding'));
    assert.equal(await cards.first().getAttribute('data-card-id'), initial[3]);
    await page.locator('.home-conveyor').focus(); await page.keyboard.press('ArrowLeft');
    await page.locator('.home-belt-row.sliding').waitFor();
    await page.waitForFunction(() => !document.querySelector('.home-belt-row.sliding'));
    assert.equal(await cards.first().getAttribute('data-card-id'), initial[0]);
    await page.getByRole('button', { name: 'Previous three suggestions' }).click();
    await page.waitForFunction(() => !document.querySelector('.home-belt-row.sliding'));
    await page.getByRole('button', { name: 'Next three suggestions' }).click();
    await page.waitForFunction(() => !document.querySelector('.home-belt-row.sliding'));
    assert.equal(await cards.first().getAttribute('data-card-id'), initial[0], 'wrapping back and forward should restore the original cards');
    const belt = await page.locator('.home-belt-viewport').boundingBox();
    assert.ok(belt.x < 1 && Math.abs(belt.width - 2133) < 2, 'the conveyor must span the screen');
    const ghosts = await page.locator('.home-belt-row .query-card[data-xray]').evaluateAll(elements => elements.map(element => ({
      x: element.getBoundingClientRect().x, opacity: getComputedStyle(element.querySelector('.query-title')).opacity,
      hidden: element.getAttribute('aria-hidden'), focusable: element.querySelector('a').tabIndex, hasMarks: Boolean(element.querySelector('i')), revealable: Boolean(element.querySelector('.query-xray-open')),
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

test('dragging saves the exact preview query and collection remains contained on narrow screens', async () => {
  const { browser, page, data } = await setup();
  try {
    for (let i = 0; i < 4; i++) {
      const id = await page.locator('.home-belt-row .query-card:not([data-xray])').first().getAttribute('data-card-id');
      const card = page.locator(`.home-belt-row .query-card:not([data-xray])[data-card-id="${id}"]`);
      const href = await card.locator('a').getAttribute('href');
      await card.dragTo(page.locator('.home-collection'));
      await page.waitForFunction(count => document.querySelectorAll('.home-collection .query-card').length === count, i + 1);
      // Dropping over a collected card can insert before it, rather than append.
      assert.equal(await page.locator('.home-collection .query-card a').evaluateAll((links, href) => links.filter(link => link.getAttribute('href') === href).length, href), 1);
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
  } finally { await browser.close(); }
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
    assert.equal(await chart.locator(':scope > span').count(), 90);
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
    await collect(6);
    for (const mode of ['Modified file', 'Tool URL', 'Conversation text']) {
      await page.getByRole('group', { name: 'Search type', exact: true }).getByRole('button', { name: mode }).click();
      await page.waitForTimeout(260);
      const layout = await geometry(); centered(layout, [6]);
      assert.equal(layout.scrollHeight, layout.clientHeight, `${mode} should fit without unnecessary vertical scrolling`);
      assert.equal(layout.overflowX, 'hidden');
    }
    await page.mouse.move(5, 5);
    await page.screenshot({ path: path.join(root, '.context/homepage-design/home-layout-wide.png') });
    await collect(8);
    const wrapped = await geometry(); centered(wrapped, [6, 2]);
    assert.ok(wrapped.scrollHeight > wrapped.clientHeight, 'larger collections must still scroll vertically');
    // Exercise conventional scrollbars as well as macOS overlay scrollbars.
    await page.addStyleTag({ content: 'main { scrollbar-gutter:stable; } main::-webkit-scrollbar { width:15px; height:15px; } main::-webkit-scrollbar-thumb { background:var(--line); }' });
    await page.waitForTimeout(200);
    centered(await geometry(), [6, 2]);
    await page.evaluate(() => document.querySelector('main').scrollTop = 0);
    await page.getByRole('button', { name: 'Next three suggestions' }).click();
    await page.locator('.home-belt-row.sliding').waitFor();
    centered(await geometry(), [6, 2]);
    await page.waitForFunction(() => !document.querySelector('.home-belt-row.sliding'));
    await collect(3);
    for (const width of [600, 390, 320]) {
      await page.setViewportSize({ width, height: 1050 });
      await page.waitForTimeout(200);
      centered(await geometry(), [2, 1]);
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
