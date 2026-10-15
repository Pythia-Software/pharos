const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');
const { pathToFileURL } = require('node:url');
const root = path.resolve(__dirname, '..');
const { chromium } = createRequire(path.join(root, '.context/browser-tests/package.json'))('playwright');
const source = fs.readFileSync(path.join(root, 'internal/archive/assets/ui.py'), 'utf8');
const html = source.slice(source.indexOf("r'''") + 4, source.lastIndexOf("'''"));

async function setup({ unassigned = false } = {}) {
  const { applyQuery, applyAggregations, loadSchema, EMPTY_QUERY } = await import(pathToFileURL(path.join(root, 'web/node_modules/@pythia-software/query-table-core/dist/index.js')).href);
  const schema = loadSchema(JSON.parse(fs.readFileSync(path.join(root, 'schemas/tool_calls.schema.json'))));
  const operation = JSON.stringify(['command', 'npm', 'run test:unit']);
  const at = days => { const date = new Date(); date.setDate(date.getDate() - days); date.setHours(12, 0, 0, 0); return date.getTime(); };
  const rows = [120000, 60000, null, 60000, 60000].map((duration_ms, index) => ({ id: `call-${index + 1}`, command: `${index % 2 ? 'CI=1 timeout 600 ' : ''}npm run test:unit -- --runInBand`, operation_key: operation, operation_name: 'npm run test:unit', activity: 'test', tool_name: 'exec_command', tool_category: 'command', command_category: 'test', mcp_server: null, program: 'npm', subcommand: 'run test:unit', provider: 'codex', model: 'model-a', duration_ms, duration_source: 'reported', started_at: at(index < 3 ? 2 : 9), day: new Date(at(index < 3 ? 2 : 9)).toLocaleDateString('en-CA'), month: new Date().toISOString().slice(0, 7), title: 'Verify repository changes', repository_name: 'repo-a', workspace_id: 'work-1', conversation_id: 'conversation-1', status: index === 2 ? 'error' : 'ok' }));
  if (unassigned) rows.push({ ...rows[0], id: 'unassigned-null', repository_name: null }, { ...rows[1], id: 'unassigned-empty', repository_name: '' });
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1250 } });
  const errors = [], requests = [];
  const state = { fail: false };
  page.on('pageerror', error => { errors.push(error.message); console.error('Browser error:', error.message); });
  await page.route('http://machine-activity.test/**', route => {
    const url = new URL(route.request().url());
    const json = (body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
    if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/assets/')) return route.fulfill({ contentType: 'text/html', body: html });
    if (url.pathname.startsWith('/assets/query-tables.')) return route.fulfill({ path: path.join(root, 'internal/archive', url.pathname) });
    if (url.pathname.endsWith('/distinct')) return json({ values: url.searchParams.get('field') === 'repository_name' ? ['repo-a', 'repo-b'] : url.searchParams.get('field') === 'provider' ? ['codex'] : ['model-a'], hasMore: false });
    if (url.pathname === '/api/query/tool_calls/aggregations') {
      const body = route.request().postDataJSON(); requests.push({ kind: 'metrics', ...body });
      if (state.fail) return json({ error: 'Timing is temporarily unavailable' }, 503);
      return json(applyAggregations(rows, { ...EMPTY_QUERY, ...body }, schema));
    }
    if (url.pathname === '/api/query/tool_calls') {
      const body = route.request().postDataJSON(); requests.push({ kind: 'calls', ...body });
      return json(applyQuery(rows, { ...EMPTY_QUERY, ...body }, schema));
    }
    if (url.pathname.startsWith('/api/tool-calls/')) return json({ ...rows.find(row => row.id === path.basename(url.pathname)), started_at: new Date(at(2)).toISOString(), input_text: 'npm run test:unit -- --runInBand', result_text: 'Tests passed' });
    if (url.pathname === '/api/pricing') return json({ unpriced_models: [], unpriced_tokens: 0, confirmed_changes: 0, proposed_changes: 0 });
    if (url.pathname === '/api/tools/status') return json({ conversations: 0, pending_conversations: 0, backfill: { running: false, done: 0, total: 0, error: null } });
    if (url.pathname.endsWith('/aggregations')) return json({ metrics: [] });
    if (url.pathname.endsWith('/field-stats')) return json({});
    if (url.pathname === '/api/query/usage') { requests.push({ kind: 'tokens', ...route.request().postDataJSON() }); return json({ rows: [], total: 0 }); }
    return json({ error: 'not stubbed' }, 404);
  });
  return { browser, page, errors, requests, state, operation };
}
const stat = (page, index) => page.locator('.machine-stats > div').nth(index);
const count = async page => Number((await stat(page, 1).locator('strong').innerText()).replaceAll(',', ''));

test('Machine Time distinguishes per-run cost and volume, and native source tables follow every drill-down', async () => {
  const { browser, page, errors } = await setup();
  try {
    await page.goto('http://machine-activity.test/usage?machine_view=time&machine_demo=1');
    await page.locator('.machine-analysis .qt-row').first().waitFor();
    assert.equal(await page.getByRole('group', { name: 'Usage view', exact: true }).getByRole('button', { name: 'Machine Activity', exact: true }).getAttribute('aria-pressed'), 'true');
    assert.equal(await page.getByText('Other tools', { exact: true }).count(), 0);
    const families = page.getByRole('combobox', { name: 'Trend command family', exact: true });
    assert.ok((await families.locator('option').allTextContents()).includes('github / search_issues'));
    assert.ok((await families.locator('option').allTextContents()).includes('pharos / query_statistics'));
    await page.getByRole('combobox', { name: 'Analytics grouping', exact: true }).selectOption('tool');
    await page.locator('.machine-analysis').getByRole('button', { name: 'exec_command', exact: true }).waitFor();
    await page.getByRole('combobox', { name: 'Analytics grouping', exact: true }).selectOption('operation');
    await page.locator('.machine-analysis').getByRole('button', { name: 'npm run build', exact: true }).waitFor();
    await page.setViewportSize({ width: 1440, height: 1500 });
    await page.screenshot({ path: path.join(root, '.context/machine-activity-v2.png') });

    await page.getByRole('combobox', { name: 'Activity repository', exact: true }).selectOption('pharos');
    await page.locator('.machine-analysis .qt-row').first().waitFor();
    const fullCount = await count(page);
    const back = page.getByRole('button', { name: 'Back one step', exact: true });
    const reset = page.getByRole('button', { name: 'Reset zoom', exact: true });
    assert.equal(await back.isDisabled(), true);
    const build = JSON.stringify(['command', 'npm', 'run build']);
    await families.selectOption(build);
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) small')?.textContent.includes('0% calls'));
    assert.match(await stat(page, 2).locator('small').innerText(), /\+\d+% per run/);
    const familyCount = await count(page);
    await page.getByRole('img', { name: /^Daily time per timed run/ }).waitFor();
    await page.screenshot({ path: path.join(root, '.context/machine-activity-v2-build.png') });
    const measure = page.getByRole('combobox', { name: 'Trend measure', exact: true });
    await measure.selectOption('calls');
    await page.getByRole('img', { name: /^Daily call count/ }).waitFor();
    await measure.selectOption('total');
    await page.getByRole('img', { name: /^Daily total tool time/ }).waitFor();
    await measure.selectOption('average');
    await page.locator('.machine-trend-point').first().click();
    await page.waitForFunction(() => document.querySelector('#machineRawCalls p')?.textContent.startsWith('6 matching'));
    assert.equal(await count(page), 6);
    const raw = page.locator('.machine-call-surface > .qt-table-wrap');
    assert.equal(await raw.locator('.qt-row').count(), 6);
    assert.ok((await raw.locator('td[data-qt-field="started_at"]').allTextContents()).every(date => !/^\d{13}$/.test(date)), 'Source timestamps should be readable dates');
    assert.ok((await raw.locator('td[data-qt-field="command"]').allTextContents()).some(command => command.includes('CI=1 timeout 600')));
    await raw.getByRole('button', { name: 'Details', exact: true }).first().click();
    await page.getByRole('dialog', { name: 'Tool call details' }).getByText('Synthetic example result;', { exact: false }).waitFor();
    await page.getByRole('dialog').getByRole('button', { name: 'Close', exact: true }).click();

    // Both controls remain reachable after the day drill-down scrolls to calls.
    const navigationBox = await page.getByRole('navigation', { name: 'Exploration navigation' }).boundingBox();
    assert.ok(navigationBox.y >= 0 && navigationBox.y < 100, 'Exploration controls should stick above the raw calls');
    await back.click();
    await page.waitForFunction(expected => Number(document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent.replaceAll(',', '')) === expected, familyCount);
    assert.equal(await families.inputValue(), build);
    await back.click();
    await page.waitForFunction(expected => Number(document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent.replaceAll(',', '')) === expected, fullCount);
    assert.equal(await back.isDisabled(), true, 'Undo must not add another history entry');
    assert.equal(await reset.isDisabled(), true);
    assert.equal(await page.getByRole('combobox', { name: 'Activity repository', exact: true }).inputValue(), 'pharos');
    await families.selectOption(build);
    await page.locator('.machine-trend-point').first().click();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '6');
    await reset.click();
    await page.waitForFunction(expected => Number(document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent.replaceAll(',', '')) === expected, fullCount);
    await back.click();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '6');
    await page.locator('.machine-breadcrumb').getByRole('button', { name: 'All activity', exact: true }).click();
    await page.locator('#machineRawCalls').getByRole('button', { name: 'Expand query builder', exact: true }).click();
    const firstPage = await raw.locator('.qt-row').first().textContent();
    const before = await count(page);
    const addOffset = page.locator('#machineRawCalls').getByRole('button', { name: 'Add offset', exact: true });
    if (await addOffset.count()) await addOffset.click();
    await page.locator('#machineRawCalls').getByRole('button', { name: 'Next 50 rows', exact: true }).click();
    await page.waitForFunction(first => { const row = document.querySelector('.machine-call-surface > .qt-table-wrap .qt-row'); return row && row.textContent !== first; }, firstPage);
    assert.equal(await count(page), before, 'Paging must not change the aggregate population');
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok(await page.locator('main').evaluate(main => main.scrollWidth <= main.clientWidth), 'Native tables should scroll within the page');
    await page.screenshot({ path: path.join(root, '.context/machine-activity-v2-mobile.png') });
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

test('Library chart selections, raw call filters, and previous-period queries stay aligned', async () => {
  const { browser, page, errors, requests, state, operation } = await setup();
  try {
    await page.goto('http://machine-activity.test/usage?machine_view=time&activity_days=7&activity_repo=repo-a');
    await page.locator('.machine-analysis').getByRole('button', { name: 'npm run test:unit', exact: true }).waitFor();
    assert.equal(await count(page), 3);
    assert.match(await stat(page, 3).innerText(), /67%/);
    await page.locator('.machine-analysis').getByRole('button', { name: /^Inspect calls/ }).click();
    await page.waitForFunction(() => new URLSearchParams(location.search).has('q_machine_calls'));
    await page.locator('#machineRawCalls').getByRole('button', { name: 'Expand query builder', exact: true }).click();
    const raw = page.locator('.machine-call-surface > .qt-table-wrap');
    // Finish the intentional drill-down scroll before opening a menu that
    // dismisses itself on scroll, matching a person pausing to inspect a row.
    await page.locator('#machineRawCalls').evaluate(element => element.scrollIntoView({ behavior: 'instant', block: 'start' }));
    const statusCell = raw.locator('td[data-qt-field="status"]').filter({ hasText: /^ok$/ }).first();
    await statusCell.scrollIntoViewIfNeeded();
    // A wide source table also scrolls horizontally to the target cell.
    // Let that scroll event settle before opening its point-anchored menu.
    await page.evaluate(() => new Promise(resolve => {
      let timer;
      const finished = () => { document.removeEventListener('scroll', settling, true); resolve(); };
      const settling = () => { clearTimeout(timer); timer = setTimeout(finished, 200); };
      document.addEventListener('scroll', settling, true);
      settling();
    }));
    await statusCell.click({ button: 'right' });
    await page.locator('.qt-cell-menu .qt-cm-filter:not(.qt-cm-filter--neg)').first().click();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '2');
    assert.ok(requests.some(request => request.kind === 'metrics' && request.where.some(term => term.field === 'status' && term.value === 'ok') && request.where.some(term => term.field === 'operation_key' && term.value === operation)));
    const metricQueries = requests.filter(request => request.kind === 'metrics' && request.where.some(term => term.field === 'status'));
    assert.ok(metricQueries.every(query => query.requireComplete === true));
    assert.ok(metricQueries.some(query => query.where.some(term => term.any?.every(clause => clause.field === 'day' && clause.op === '=' && new Date(clause.value).getTime() < new Date().setHours(0,0,0,0) - 6 * 86400000))));
    await raw.getByRole('button', { name: 'Details', exact: true }).first().click();
    await page.getByRole('dialog', { name: 'Tool call details' }).waitFor();
    await page.getByRole('dialog').getByRole('button', { name: 'Close', exact: true }).click();
    assert.match(await raw.getByRole('link', { name: 'Conversation ↗' }).first().getAttribute('href'), /work\/work-1\?conversation=conversation-1/);
    await page.getByRole('button', { name: 'Back one step', exact: true }).click();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '3');
    assert.ok(requests.filter(request => request.kind === 'metrics').slice(-2).every(request => !request.where.some(term => term.field === 'status')));
    // Filter again through the native call table, then check a saved deep link.
    await statusCell.scrollIntoViewIfNeeded();
    await page.evaluate(() => new Promise(resolve => {
      let timer;
      const finished = () => { document.removeEventListener('scroll', settling, true); resolve(); };
      const settling = () => { clearTimeout(timer); timer = setTimeout(finished, 200); };
      document.addEventListener('scroll', settling, true);
      settling();
    }));
    await statusCell.click({ button: 'right' });
    await page.locator('.qt-cell-menu .qt-cm-filter:not(.qt-cm-filter--neg)').first().click();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '2');
    await page.reload();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '2');
    assert.equal(await page.getByRole('button', { name: 'Back one step', exact: true }).isDisabled(), true);
    assert.equal(await page.getByRole('button', { name: 'Reset zoom', exact: true }).isEnabled(), true);
    const tokenRequest = page.waitForRequest(request => new URL(request.url()).pathname === '/api/query/usage' && request.postDataJSON()?.where?.some(term => term.field === 'repository_name' && term.value === 'repo-a'));
    await page.getByRole('button', { name: /Machine Tokens/ }).click();
    const tokenQuery = (await tokenRequest).postDataJSON();
    assert.ok(!tokenQuery.where.some(term => term.field === 'operation_key'));
    await page.getByRole('button', { name: /Machine Time/ }).click();
    await page.locator('.machine-stats').waitFor();
    state.fail = true;
    await page.getByRole('button', { name: 'Refresh', exact: true }).click();
    await page.getByRole('alert').getByText('Timing is temporarily unavailable').waitFor();
    assert.equal(await page.locator('.machine-stats').count(), 0);
    await raw.locator('.qt-row').first().waitFor();
    assert.ok(await raw.locator('.qt-row').count(), 'The supporting calls remain inspectable when analytics fail');
    await page.getByRole('button', { name: 'Reset zoom', exact: true }).click();
    assert.equal(await page.getByRole('combobox', { name: 'Activity repository', exact: true }).inputValue(), 'repo-a');
    await page.getByRole('alert').getByText('Timing is temporarily unavailable').waitFor();
    state.fail = false;
    await page.getByRole('button', { name: 'Try again', exact: true }).click();
    await page.locator('.machine-stats').waitFor();
    assert.equal(await count(page), 3, 'Reset clears native status and family filters while preserving the selected period and repository');
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});


test('Parallelism supports stepping back through nested chart zooms and conversation selections', async () => {
  const { browser, page, errors } = await setup();
  try {
    await page.goto('http://machine-activity.test/usage?machine_view=parallelism&machine_demo=1');
    const chart = page.locator('.concurrency-chart svg');
    await chart.waitFor();
    const back = page.getByRole('button', { name: 'Back one step', exact: true });
    const reset = page.getByRole('button', { name: 'Reset zoom', exact: true });
    assert.equal(await back.isDisabled(), true);
    assert.equal(await reset.isDisabled(), true);
    const saved = () => page.evaluate(() => JSON.parse(localStorage.getItem('pharos-concurrency-v1')));
    async function zoom() {
      await chart.scrollIntoViewIfNeeded();
      const box = await chart.boundingBox();
      await page.mouse.move(box.x + box.width * .25, box.y + 100);
      await page.mouse.down();
      await page.mouse.move(box.x + box.width * .75, box.y + 100, { steps: 10 });
      await page.mouse.up();
    }
    await zoom();
    await page.waitForFunction(() => Boolean(JSON.parse(localStorage.getItem('pharos-concurrency-v1'))?.zoom));
    const first = (await saved()).zoom;
    await zoom();
    const second = (await saved()).zoom;
    assert.ok(second[1] - second[0] < first[1] - first[0]);
    await back.click();
    assert.deepEqual((await saved()).zoom, first);
    await back.click();
    assert.equal((await saved()).zoom, null);
    assert.equal(await back.isDisabled(), true);
    await page.locator('.concurrency-conversations summary').click();
    await page.locator('.concurrency-thread-button').first().click();
    assert.equal(await reset.isEnabled(), true);
    await zoom();
    await reset.click();
    const restored = await saved();
    assert.equal(restored.zoom, null);
    assert.equal(restored.workspace, '');
    assert.equal(restored.agent, '');
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

test('Browser Back and Forward restore machine filters, view changes, and nested parallelism zooms', async () => {
  const { browser, page, errors } = await setup();
  try {
    await page.goto('http://machine-activity.test/usage?machine_view=time&machine_demo=1');
    await page.locator('.machine-analysis .qt-row').first().waitFor();
    const initialCount = await count(page);
    const initialHistory = await page.evaluate(() => history.length);
    const repository = page.getByRole('combobox', { name: 'Activity repository', exact: true });
    const family = page.getByRole('combobox', { name: 'Trend command family', exact: true });
    const build = JSON.stringify(['command', 'npm', 'run build']);
    await repository.selectOption('pharos');
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent && !document.querySelector('.machine-loading'));
    const repoCount = await count(page);
    await family.selectOption(build);
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) small')?.textContent.includes('0% calls'));
    const familyCount = await count(page);
    await page.locator('.machine-trend-point').first().click();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '6');
    await page.getByRole('button', { name: /Machine Parallelism/ }).click();
    const chart = page.locator('.concurrency-chart svg');
    await chart.waitFor();
    const zoomValue = () => page.evaluate(() => JSON.parse(new URLSearchParams(location.search).get('q_machine_parallelism')).zoom);
    async function zoom() {
      await chart.scrollIntoViewIfNeeded();
      const box = await chart.boundingBox();
      await page.mouse.move(box.x + box.width * .25, box.y + 100);
      await page.mouse.down();
      await page.mouse.move(box.x + box.width * .75, box.y + 100, { steps: 10 });
      await page.mouse.up();
    }
    await zoom();
    const firstZoom = await zoomValue();
    await zoom();
    const secondZoom = await zoomValue();
    assert.equal(await page.evaluate(() => history.length), initialHistory + 6, 'Each filter, tab, and completed drag gets one entry');
    await page.goBack();
    assert.deepEqual(await zoomValue(), firstZoom);
    await page.goBack();
    assert.equal(await zoomValue(), null);
    await page.goBack();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '6');
    assert.equal(await family.inputValue(), build);
    await page.goBack();
    await page.waitForFunction(expected => Number(document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent.replaceAll(',', '')) === expected, familyCount);
    await page.goBack();
    await page.waitForFunction(expected => Number(document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent.replaceAll(',', '')) === expected, repoCount);
    assert.equal(await family.inputValue(), '');
    await page.goBack();
    await page.waitForFunction(expected => Number(document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent.replaceAll(',', '')) === expected, initialCount);
    assert.equal(await repository.inputValue(), '', 'An explicit All repositories selection must beat the latest saved preference');
    await page.goForward();
    await page.waitForFunction(expected => Number(document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent.replaceAll(',', '')) === expected, repoCount);
    assert.equal(await repository.inputValue(), 'pharos');
    await page.goForward();
    await page.waitForFunction(expected => Number(document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent.replaceAll(',', '')) === expected, familyCount);
    await page.goForward();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '6');
    await page.goForward();
    await chart.waitFor();
    await page.goForward();
    assert.deepEqual(await zoomValue(), firstZoom);
    await page.goForward();
    assert.deepEqual(await zoomValue(), secondZoom);
    await page.reload();
    await chart.waitFor();
    assert.deepEqual(await zoomValue(), secondZoom, 'Reload must keep the captured zoom');
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

test('Browser navigation restores native call filters and shared scopes across the Tokens view', async () => {
  const { browser, page, requests, errors, operation } = await setup();
  try {
    await page.goto('http://machine-activity.test/usage?machine_view=time&activity_days=7&activity_repo=repo-a');
    await page.locator('.machine-analysis .qt-row').first().waitFor();
    await page.getByRole('combobox', { name: 'Trend command family', exact: true }).selectOption(operation);
    const raw = page.locator('.machine-call-surface > .qt-table-wrap');
    const status = raw.locator('td[data-qt-field="status"]').filter({ hasText: /^ok$/ }).first();
    await status.scrollIntoViewIfNeeded();
    await page.evaluate(() => new Promise(resolve => {
      let timer;
      const finished = () => { document.removeEventListener('scroll', settling, true); resolve(); };
      const settling = () => { clearTimeout(timer); timer = setTimeout(finished, 200); };
      document.addEventListener('scroll', settling, true); settling();
    }));
    await status.click({ button: 'right' });
    await page.locator('.qt-cell-menu .qt-cm-filter:not(.qt-cm-filter--neg)').first().click();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '2');
    await page.goBack();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '3');
    assert.equal(await page.getByRole('combobox', { name: 'Trend command family', exact: true }).inputValue(), operation);
    await page.goForward();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '2');
    await page.getByRole('button', { name: /Machine Tokens/ }).click();
    await page.getByRole('combobox', { name: 'Activity repository', exact: true }).selectOption('');
    await page.getByRole('button', { name: /Machine Time/ }).click();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '2');
    assert.ok(requests.filter(request => request.kind === 'metrics').slice(-2).every(request => !request.where.some(term => term.field === 'repository_name')), 'Changing scope while the call table is unmounted must replace its old scope');
    await page.goBack();
    await page.getByRole('button', { name: /Machine Tokens/, pressed: true }).waitFor();
    assert.equal(await page.getByRole('button', { name: /Machine Tokens/ }).getAttribute('aria-pressed'), 'true');
    assert.equal(await page.getByRole('combobox', { name: 'Activity repository', exact: true }).inputValue(), '');
    await page.goBack();
    await page.waitForFunction(() => document.querySelector('[aria-label="Activity repository"]')?.value === 'repo-a');
    await page.goBack();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '2');
    assert.equal(await page.getByRole('combobox', { name: 'Activity repository', exact: true }).inputValue(), 'repo-a');
    await page.getByRole('button', { name: 'Reset zoom', exact: true }).click();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '3');
    await page.getByRole('combobox', { name: 'Activity period', exact: true }).selectOption('14');
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '5');
    await page.getByRole('combobox', { name: 'Trend command family', exact: true }).selectOption(operation);
    await page.locator('.machine-analysis .qt-row').first().waitFor();
    assert.equal(await count(page), 5, 'A local edit must not resurrect the previous seven-day scope');
    await page.goBack();
    await page.waitForFunction(() => document.querySelector('[aria-label="Trend command family"]')?.value === '');
    await page.goBack();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '3');
    assert.equal(await page.getByRole('combobox', { name: 'Activity period', exact: true }).inputValue(), '7');
    await page.goForward();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '5');
    assert.equal(await page.getByRole('combobox', { name: 'Activity period', exact: true }).inputValue(), '14');
    await page.getByRole('button', { name: 'Human Words', exact: true }).click();
    assert.equal(new URL(page.url()).searchParams.get('usage'), 'writing');
    await page.goBack();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '5');
    assert.equal(await page.getByRole('button', { name: 'Machine Activity', exact: true }).getAttribute('aria-pressed'), 'true');
    await page.goForward();
    assert.equal(new URL(page.url()).searchParams.get('usage'), 'writing');
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});


test('No repository includes null and empty calls, toggles, avoids duplicate segments, and restores with browser history', async () => {
  const { browser, page, errors, requests } = await setup({ unassigned: true });
  try {
    await page.goto('http://machine-activity.test/usage?usage=tokens&machine_view=time&activity_days=7&activity_repo=&activity_provider=&activity_model=');
    await page.locator('.machine-analysis .qt-row').first().waitFor();
    assert.equal(await count(page), 5);
    const missing = page.locator('.machine-repository-heading').getByRole('button', { name: '(No repository)', exact: true });
    const selected = () => page.locator('.machine-breadcrumb').getByText('(No repository)', { exact: true });
    await missing.click();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '2');
    assert.equal(await missing.getAttribute('aria-pressed'), 'true');
    await selected().waitFor();
    const raw = page.locator('.machine-call-surface > .qt-table-wrap');
    await page.waitForFunction(() => document.querySelectorAll('.machine-call-surface > .qt-table-wrap .qt-row').length === 2);
    assert.equal(await raw.locator('.qt-row').count(), 2, 'Both null and empty repository values must be included');
    await missing.click();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '5');
    assert.equal(await selected().count(), 0);
    const segment = page.getByRole('group', { name: '(No repository) activity breakdown', exact: true }).getByRole('button').first();
    for (let click = 0; click < 3; click++) {
      await segment.click();
      await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '2');
    }
    const latest = requests.filter(request => request.kind === 'metrics').at(-2);
    const repoTerms = latest.where.filter(term => 'any' in term ? term.any.some(clause => clause.field === 'repository_name') : term.field === 'repository_name');
    assert.equal(repoTerms.length, 1, 'Repeated segment clicks must not accumulate repository conditions');
    await selected().waitFor();
    await page.goBack();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '5');
    await page.goForward();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '2');
    await selected().waitFor();
    await page.reload();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '2');
    await selected().waitFor();
    await missing.click();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '5');
    await page.locator('.machine-repository-heading').getByRole('button', { name: 'repo-a', exact: true }).click();
    await page.waitForFunction(() => document.querySelector('.machine-stats > div:nth-child(2) strong')?.textContent === '3');
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});
