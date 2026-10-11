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
  const { decodeQuery, encodeQuery, EMPTY_QUERY } = await import(pathToFileURL(path.join(root, 'web/node_modules/@pythia-software/query-table-core/dist/index.js')).href);
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1920, height: 963 } });
    page.setDefaultTimeout(10000);
    const errors = [];
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

    // Chart presets reflect chart controls independently of metric presets.
    await navigate('usage', EMPTY_QUERY);
    const tokens = page.getByRole('group', { name: 'Tokens presets', exact: true });
    const cost = page.getByRole('group', { name: 'Cost presets', exact: true });
    await waitPressed(tokens.getByRole('button', { name: 'Weekly cache mix', exact: true }), true);
    await cost.getByRole('button', { name: 'Daily cost', exact: true }).click();
    await waitPressed(cost.getByRole('button', { name: 'Daily cost', exact: true }), true);
    await waitPressed(tokens.getByRole('button', { name: 'Weekly cache mix', exact: true }), false);
    await tokens.getByRole('button', { name: 'Models', exact: true }).click();
    await waitPressed(tokens.getByRole('button', { name: 'Models', exact: true }), true);
    await waitPressed(cost.getByRole('button', { name: 'Daily cost', exact: true }), true);
    await page.getByRole('combobox', { name: 'Chart interval', exact: true }).selectOption('month');
    await waitPressed(cost.getByRole('button', { name: 'Daily cost', exact: true }), false);
    await waitPressed(tokens.getByRole('button', { name: 'Models', exact: true }), true);
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});
