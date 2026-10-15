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

test('Usage presets follow the current query across clicks, edits, reloads and routes', async () => {
  const { decodeQuery, encodeQuery, EMPTY_QUERY, applyQuery, loadSchema, prepareGrouping, evaluateMetrics } = await import(pathToFileURL(path.join(root, 'web/node_modules/@pythia-software/query-table-core/dist/index.js')).href);
  const usageSchema = loadSchema(require('../schemas/usage.schema.json'));
  const usageRows = [1, 2].map(day => ({ id: `u${day}`, agent_session_id: 'same-session', workspace_id: 'w1', title: 'Deep work', repository_name: 'example/repo',
    provider: 'codex', model_family: 'gpt-6', effort: 'high', day: `2026-10-0${day}`, total_tokens: day * 100, cost_usd: day, output_tokens: day * 10,
    uncached_input_tokens: 10, cache_read_input_tokens: 90, input_tokens: 100 }));
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1920, height: 963 } });
    page.setDefaultTimeout(10000);
    const errors = [];
    const serverQueries = [];
    let populationRequests = 0;
    page.on('pageerror', error => errors.push(error.message));
    await page.route('http://usage-presets.test/**', route => {
      const url = new URL(route.request().url());
      if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/assets/')) return route.fulfill({ contentType: 'text/html', body: html });
      if (url.pathname === '/assets/query-tables.js' || url.pathname === '/assets/query-tables.css') return route.fulfill({ contentType: url.pathname.endsWith('.js') ? 'application/javascript' : 'text/css', body: fs.readFileSync(path.join(root, 'internal/archive', url.pathname)) });
      const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
      if (url.pathname === '/api/authorship') return json({ built_at: '2026-10-09T12:00:00Z', running: false, stale: false, error: null, totals: {}, last_30_days: {} });
      if (url.pathname === '/api/query/writing/series') return json({ works: 1, daily: [{ day: '2026-10-09', typed_words: 100, typed_chars: 600, typed_messages: 5 }], totals: { typed_words: 100, typed_chars: 600, typed_messages: 5, messages: 5 } });
      if (url.pathname.endsWith('/aggregations')) {
        const { aggregations = [] } = route.request().postDataJSON();
        return json({ metrics: aggregations.map(aggregation => ({ id: aggregation.id, buckets: [{ keys: aggregation.groupBy.map(field => field === 'repository_name' ? 'example/repo' : field === 'provider' ? 'codex' : '2026-10-05'), value: 100, count: 1 }] })) });
      }
      if (url.pathname === '/api/query/writing') return json({ rows: [{ id: 'w1', workspace_id: 'w1', title: 'Deep work', typed_words: 100, typed_turns: 5, repository_name: 'example/repo' }], total: 1 });
      if (url.pathname === '/api/query-computed-columns') {
        if (url.searchParams.get('dataset')?.includes(':groups')) return route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ error: 'unknown dataset' }) });
        return json([]);
      }
      const execution = { profile: 'qt-sqlite-v1', planToken: '', fields: {}, resolvedRevisions: {} };
      if (url.pathname.endsWith('/capabilities')) return json({ metricCapabilities: { version: 2, expressions: true, distributions: true }, computedExecution: execution,
        ...(url.pathname === '/api/query/usage/capabilities' ? { queryCapabilities: { version: 3, profile: execution.profile, groupedRows: true, calendarGrouping: true, computedSourceFilters: true, regex: false, maxRows: 10000, maxGroups: 10000, maxPopulation: 250000 } } : {}) });
      if (url.pathname === '/api/query/usage/query-v3') {
        const request = route.request().postDataJSON();
        serverQueries.push(request);
        const state = { ...EMPTY_QUERY, ...request.result, select: request.result.select.map(field => ({ field })), aggregations: request.metrics,
          ...(request.grouping ? { grouping: { ...request.grouping, source: { ...EMPTY_QUERY, ...request.source, select: request.source.select.map(field => ({ field })) } } } : {}) };
        const prepared = state.grouping ? prepareGrouping(usageRows, usageSchema, state.grouping, { now: request.now }) : undefined;
        const schema = prepared?.schema ?? usageSchema;
        const page = applyQuery(usageRows, state, usageSchema, { now: request.now });
        const metrics = evaluateMetrics(usageRows, state, usageSchema, { now: request.now, shownRows: page.rows });
        return json({ version: 3, now: request.now, rows: request.rows ? page.rows : [], total: page.total, sourceTotal: usageRows.length, metrics: metrics.metrics, execution,
          schema: { name: schema.name, idField: schema.idField, fields: schema.fields.map(({ name, type }) => ({ name, type })) } });
      }
      if (url.pathname.endsWith('/population')) { populationRequests++; return route.fulfill({ status: 500 }); }
      if (url.pathname === '/api/query/writing_messages' || url.pathname === '/api/query/usage') return json({ rows: [], total: 0 });
      if (url.pathname.endsWith('/distinct')) return json({ values: [], hasMore: false });
      if (url.pathname.endsWith('/field-stats')) return json({});
      if (url.pathname === '/api/pricing') return json({ unpriced_models: [], unpriced_tokens: 0, confirmed_changes: 0, proposed_changes: 0 });
      return route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: 'not stubbed' }) });
    });
    const sorts = page.getByRole('group', { name: 'Sort presets', exact: true });
    const metrics = page.getByRole('group', { name: 'Metrics presets', exact: true });
    const active = group => group.locator('.query-chip[aria-pressed="true"]').allTextContents();
    const waitPressed = async (button, pressed) => {
      await page.waitForFunction(({ name, pressed }) => [...document.querySelectorAll('#usage .query-chip')].some(button => button.textContent === name && button.getAttribute('aria-pressed') === String(pressed)), { name: await button.textContent(), pressed });
    };
    const navigate = async (dataset, query, extra = '') => {
      const url = `/usage?usage=${dataset === 'usage' ? 'tokens' : 'writing'}${extra}&q_${dataset}=${encodeURIComponent(encodeQuery(query))}`;
      await page.evaluate(url => window.pharosNavigate(url), url);
    };

    const initial = { ...EMPTY_QUERY, orderBy: [{ field: 'typed_words', dir: 'desc' }] };
    await page.goto(`http://usage-presets.test/usage?usage=writing&q_writing=${encodeURIComponent(encodeQuery(initial))}`);
    const deepest = sorts.getByRole('button', { name: 'Deepest conversations', exact: true });
    await waitPressed(deepest, true);
    assert.deepEqual(await active(sorts), ['Deepest conversations']);
    assert.deepEqual(await active(metrics), []);

    const handsOn = sorts.getByRole('button', { name: 'Most hands-on', exact: true });
    await handsOn.click();
    await waitPressed(handsOn, true);
    assert.deepEqual(await active(sorts), ['Most hands-on']);
    const byRepository = metrics.getByRole('button', { name: 'By repository', exact: true });
    await byRepository.click();
    await waitPressed(byRepository, true);
    assert.deepEqual(await active(sorts), ['Most hands-on']);
    assert.deepEqual(await active(metrics), ['By repository']);
    const selectedStyle = await handsOn.evaluate(button => ({ background: getComputedStyle(button).backgroundColor, border: getComputedStyle(button).borderColor, shadow: getComputedStyle(button).boxShadow }));
    const idleStyle = await deepest.evaluate(button => ({ background: getComputedStyle(button).backgroundColor, border: getComputedStyle(button).borderColor, shadow: getComputedStyle(button).boxShadow }));
    assert.notDeepEqual(selectedStyle, idleStyle, 'The matching preset is visibly highlighted');
    await page.reload();
    await waitPressed(handsOn, true);
    await waitPressed(byRepository, true);

    // A manually changed sort removes stale highlighting; restoring it through
    // a route with filters and different metric IDs still finds both matches.
    const query = decodeQuery(new URL(page.url()).searchParams.get('q_writing'));
    await page.locator('#usage .qt-chip--sort').getByRole('button', { name: 'desc', exact: true }).click();
    await waitPressed(handsOn, false);
    await waitPressed(byRepository, true);
    await navigate('writing', { ...query, where: [{ field: 'repository_name', op: '=', value: 'example/repo' }], offset: 50, aggregations: query.aggregations.map((aggregation, index) => ({ ...aggregation, id: `manual-${index}`, label: 'My metric' })) });
    await waitPressed(handsOn, true);
    await waitPressed(byRepository, true);
    await page.screenshot({ path: path.join(root, '.context/usage-presets-highlighted.png') });
    await metrics.getByRole('button', { name: 'Clear metrics', exact: true }).click();
    await waitPressed(byRepository, false);
    assert.deepEqual(await active(sorts), ['Most hands-on']);

    // Matching uses the full sort priority list, including secondary sorts.
    await navigate('writing_messages', { ...EMPTY_QUERY, orderBy: [{ field: 'typed_share', dir: 'asc', nulls: 'last' }, { field: 'words', dir: 'desc' }] }, '&writing=messages');
    const leastTyped = sorts.getByRole('button', { name: 'Least typed', exact: true });
    await waitPressed(leastTyped, true);
    await navigate('writing_messages', { ...EMPTY_QUERY, orderBy: [{ field: 'typed_share', dir: 'asc' }] }, '&writing=messages');
    await waitPressed(leastTyped, false);
    assert.deepEqual(await active(sorts), []);
    await sorts.getByRole('button', { name: 'Longest typed', exact: true }).click();
    await waitPressed(sorts.getByRole('button', { name: 'Longest typed', exact: true }), true);
    await metrics.getByRole('button', { name: 'Per week', exact: true }).click();
    await waitPressed(metrics.getByRole('button', { name: 'Per week', exact: true }), true);

    // Chart shortcuts follow chart controls independently of the table queries.
    await navigate('usage', EMPTY_QUERY);
    const chart = page.getByRole('group', { name: 'Chart presets', exact: true });
    await waitPressed(chart.getByRole('button', { name: 'Weekly cache mix', exact: true }), true);
    await chart.getByRole('button', { name: 'Daily cost', exact: true }).click();
    await waitPressed(chart.getByRole('button', { name: 'Daily cost', exact: true }), true);
    await waitPressed(chart.getByRole('button', { name: 'Weekly cache mix', exact: true }), false);
    await page.getByRole('combobox', { name: 'Chart interval', exact: true }).selectOption('month');
    await waitPressed(chart.getByRole('button', { name: 'Daily cost', exact: true }), false);

    // Grouping is available directly from the server row view, fetches a complete
    // source, and stays navigable while editing groups or restoring a saved URL.
    const groups = page.getByRole('group', { name: 'Group by presets', exact: true });
    const repositories = groups.getByRole('button', { name: 'Repositories', exact: true });
    await repositories.click();
    await waitPressed(repositories, true);
    await page.locator('#usage .qt-table tbody tr', { hasText: 'example/repo' }).waitFor();
    assert.deepEqual(serverQueries.at(-1).grouping.keys, ['repository_name']);
    assert.deepEqual(serverQueries.at(-1).source.where, []);
    const cells = await page.locator('#usage .qt-table tbody tr').first().locator('td').allTextContents();
    assert.deepEqual(cells.slice(0, 5), ['example/repo', '$3.00', '300', '30', '1']);
    assert.equal(cells[5], '90%');
    const grouped = decodeQuery(new URL(page.url()).searchParams.get('q_usage'));
    await page.reload();
    await waitPressed(repositories, true);
    await page.locator('#usage .qt-table tbody tr', { hasText: 'example/repo' }).waitFor();
    await groups.getByRole('button', { name: 'Models', exact: true }).click();
    await waitPressed(groups.getByRole('button', { name: 'Models', exact: true }), true);
    await waitPressed(repositories, false);
    assert.deepEqual(decodeQuery(new URL(page.url()).searchParams.get('q_usage')).grouping.source, grouped.grouping.source);
    // Changing representative columns and formulas must leave React's controls intact.
    await groups.getByRole('button', { name: 'Work', exact: true }).click();
    const workRow = page.locator('#usage .qt-table tbody tr', { hasText: 'Deep work' });
    await workRow.waitFor();
    assert.equal(await workRow.getByRole('button', { name: 'Records', exact: true }).isEnabled(), true);
    await workRow.getByRole('button', { name: 'Records', exact: true }).click();
    await page.locator('#usage .qt-table th[data-qt-field="day"]').waitFor();
    const drill = decodeQuery(new URL(page.url()).searchParams.get('q_usage'));
    assert.equal(drill.grouping, undefined);
    assert.ok(drill.where.some(term => term.field === 'workspace_id' && term.value === 'w1'));
    await groups.getByRole('button', { name: 'Work', exact: true }).click();
    await workRow.waitFor();
    await page.getByRole('group', { name: 'Compare presets', exact: true }).getByRole('button', { name: 'Cache efficiency', exact: true }).click();
    await page.locator('#usage th[data-qt-field="usage_uncached_input_tokens"]').waitFor();
    await repositories.click();
    await page.locator('#usage .qt-table tbody tr', { hasText: 'example/repo' }).waitFor();
    await groups.getByRole('button', { name: 'Rows', exact: true }).click();
    await waitPressed(groups.getByRole('button', { name: 'Rows', exact: true }), true);
    assert.deepEqual(decodeQuery(new URL(page.url()).searchParams.get('q_usage')), drill, 'switching presets retains the Records drill-in filter');
    await chart.getByRole('button', { name: 'Daily cost', exact: true }).waitFor();
    assert.equal(populationRequests, 0, 'Server grouping never downloads a complete source into the browser');
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});
