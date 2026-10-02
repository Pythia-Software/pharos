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

test('Usage table badges follow the active graph and preserve raw sorting and cell filters', async () => {
  const { applyQuery, applyAggregations, loadSchema, encodeQuery, EMPTY_QUERY } = await import(pathToFileURL(path.join(root, 'web/node_modules/@pythia-software/query-table-core/dist/index.js')).href);
  const schema = loadSchema(JSON.parse(fs.readFileSync(path.join(root, 'schemas/usage.schema.json'), 'utf8')));
  const writingSchema = loadSchema(JSON.parse(fs.readFileSync(path.join(root, 'schemas/writing.schema.json'), 'utf8')));
  const now = new Date();
  const day = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`;
  const monday = new Date(now);
  monday.setDate(monday.getDate() - ((monday.getDay() + 6) % 7));
  const week = `${monday.getFullYear()}-${String(monday.getMonth() + 1).padStart(2, '0')}-${String(monday.getDate()).padStart(2, '0')}`;
  const models = ['opus-5-5', 'sonnet-5-5', 'haiku-5', 'gpt-6-sol', 'gemini-3-pro', 'gemini-3-flash', 'gpt-5-codex'];
  const rows = models.map((model, index) => ({
    id: `usage-${index}`, day, week, month: day.slice(0, 7), last_usage_at: now.toISOString(),
    repository_name: `example/repo-${index}`, provider: index < 3 ? 'claude' : index === 4 || index === 5 ? 'antigravity' : 'codex',
    model_family: model, model: `claude-${model}-20260928[1m]`, session_kind: 'root', title: 'Implement dashboard improvements',
    total_tokens: 70000 - index * 8000, uncached_input_tokens: 60000 - index * 8000, output_tokens: 10000, cost_usd: 7 - index * 0.8,
  }));
  const requests = [], errors = [];
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1100 } });
    page.on('pageerror', error => errors.push(error.message));
    await page.route('http://usage-colors.test/**', route => {
      const url = new URL(route.request().url());
      const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
      if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/assets/')) return route.fulfill({ contentType: 'text/html', body: html });
      if (url.pathname.startsWith('/assets/query-tables.')) return route.fulfill({ contentType: url.pathname.endsWith('.js') ? 'application/javascript' : 'text/css', body: fs.readFileSync(path.join(root, 'internal/archive', url.pathname)) });
      if (url.pathname === '/api/query/usage') {
        const query = { ...EMPTY_QUERY, ...route.request().postDataJSON() };
        requests.push(query);
        return json(applyQuery(rows, query, schema));
      }
      if (url.pathname === '/api/query/usage/aggregations') return json(applyAggregations(rows, { ...EMPTY_QUERY, ...route.request().postDataJSON() }, schema));
      if (url.pathname === '/api/query/writing') return json({ rows: [{ id: 'writing-1', repository_name: rows[0].repository_name, providers: 'claude,codex', typed_words: 200, typed_turns: 5 }], total: 1 });
      if (url.pathname === '/api/query/writing/series') {
        const facet = route.request().postDataJSON().split;
        return json({ works: 1, daily: [{ day, typed_words: 200, words: 300 }], split: [{ day, value: facet === 'repository_name' ? rows[0].repository_name : 'claude,codex', typed_words: 200, words: 300 }], totals: { typed_words: 200, words: 300 } });
      }
      if (url.pathname.endsWith('/distinct')) return json({ values: [], hasMore: false });
      if (url.pathname.endsWith('/field-stats')) return json({});
      if (url.pathname.endsWith('/aggregations')) return json({ metrics: [] });
      if (url.pathname === '/api/authorship') return json({ built_at: now.toISOString(), totals: {}, last_30_days: {} });
      if (url.pathname === '/api/health/pricing') return json({ priced_tokens: 100, total_tokens: 100, cost_usd: 7 });
      if (url.pathname === '/api/pricing') return json({ unpriced_models: [], priced_tokens: 100, unpriced_tokens: 0 });
      return route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"not stubbed"}' });
    });
    const query = { ...EMPTY_QUERY, select: [...schema.defaultSelect, { field: 'model', width: 250 }] };
    const writingQuery = { ...EMPTY_QUERY, select: [...writingSchema.defaultSelect, { field: 'providers', width: 180 }] };
    await page.goto(`http://usage-colors.test/usage?q_usage=${encodeURIComponent(encodeQuery(query))}&q_writing=${encodeURIComponent(encodeQuery(writingQuery))}`);
    await page.locator('#usage .qt-row').first().waitFor();
    assert.equal(await page.locator('.usage-facet-badge').count(), 0);
    const split = page.getByRole('group', { name: 'Split', exact: true });
    const checkFacet = async (field, label, value, tableField = field) => {
      const swatch = page.locator('.usage-chart-legend').getByRole('button', { name: label, exact: true }).locator('.usage-swatch');
      await swatch.waitFor();
      const color = await swatch.evaluate(element => getComputedStyle(element).backgroundColor);
      const cell = page.locator(`#usage td[data-qt-field="${tableField}"]`).filter({ hasText: value }).first();
      await cell.locator('.usage-facet-badge').waitFor();
      assert.equal(await cell.locator('.usage-facet-badge').evaluate(element => getComputedStyle(element).backgroundColor), color);
      assert.equal(await page.locator('.usage-facet-badge').evaluateAll(badges => badges.every(badge => badge.dataset.chartFacet === badge.closest('td').dataset.qtField || badge.dataset.chartFacet === 'model_family' && badge.closest('td').dataset.qtField === 'model')), true);
      for (const inactive of ['repository_name', 'provider', 'model_family', 'model'].filter(name => name !== field && !(field === 'model_family' && name === 'model'))) assert.equal(await page.locator(`td[data-qt-field="${inactive}"] .usage-facet-badge`).count(), 0);
    };
    await split.getByRole('button', { name: 'Provider', exact: true }).click();
    await checkFacet('provider', 'Claude', 'Claude');
    await split.getByRole('button', { name: 'Repository', exact: true }).click();
    await checkFacet('repository_name', rows[0].repository_name, rows[0].repository_name);
    const folded = page.locator('td[data-qt-field="repository_name"]').filter({ hasText: rows[6].repository_name });
    const otherColor = await page.locator('.usage-chart-legend').getByRole('button', { name: 'Other (2)', exact: true }).locator('.usage-swatch').evaluate(element => getComputedStyle(element).backgroundColor);
    assert.equal(await folded.locator('.usage-facet-badge').evaluate(element => getComputedStyle(element).backgroundColor), otherColor);
    await page.locator('.usage-chart-legend').getByRole('button', { name: 'Other (2)', exact: true }).click();
    await checkFacet('repository_name', rows[6].repository_name, rows[6].repository_name);
    assert.notEqual(await folded.locator('.usage-facet-badge').evaluate(element => getComputedStyle(element).backgroundColor), otherColor);
    await split.getByRole('button', { name: 'Model', exact: true }).click();
    for (const metric of ['Tokens', 'Cost', '%']) {
      await page.locator('.usage-chart').getByRole('button', { name: metric, exact: true }).click();
      await checkFacet('model_family', rows[0].model_family, rows[0].model_family);
      await checkFacet('model_family', rows[0].model_family, rows[0].model, 'model');
    }
    await page.getByRole('spinbutton', { name: 'Smoothing periods' }).fill('4');
    await checkFacet('model_family', rows[0].model_family, rows[0].model_family);
    fs.mkdirSync(path.join(root, '.context'), { recursive: true });
    await page.locator('#usage').getByRole('button', { name: 'Collapse query builder', exact: true }).click();
    await page.screenshot({ path: path.join(root, '.context/usage-table-colors.png'), fullPage: true });
    await page.locator('#usage th[data-qt-field="model_family"] .qt-th-label').click();
    await page.getByRole('menuitem', { name: 'Set Sort (Asc)', exact: true }).click();
    await page.waitForFunction(() => [...document.querySelectorAll('#usage td[data-qt-field="model_family"]')].map(cell => cell.textContent).join('|') === [...document.querySelectorAll('#usage td[data-qt-field="model_family"]')].map(cell => cell.textContent).sort().join('|'));
    assert.ok(requests.at(-1).orderBy.some(term => term.field === 'model_family' && term.dir === 'asc'));
    await page.locator('#usage td[data-qt-field="model_family"]').filter({ hasText: rows[0].model_family }).click({ button: 'right' });
    await page.locator('.qt-cell-menu .qt-cm-filter:not(.qt-cm-filter--neg)').first().click();
    await page.waitForFunction(() => document.querySelectorAll('#usage .qt-row').length === 1);
    assert.ok(requests.at(-1).where.some(term => term.field === 'model_family' && term.op === '=' && term.value === rows[0].model_family));
    await checkFacet('model_family', rows[0].model_family, rows[0].model_family);
    await split.getByRole('button', { name: 'Agent', exact: true }).click();
    await page.waitForFunction(() => document.querySelectorAll('.usage-facet-badge').length === 0);
    await page.locator('.usage-chart').getByRole('button', { name: 'Tokens', exact: true }).click();
    await split.getByRole('button', { name: 'Token type', exact: true }).click();
    assert.equal(await page.locator('.usage-facet-badge').count(), 0);
    await page.locator('.usage-chart').getByRole('button', { name: 'Cost', exact: true }).click();
    await checkFacet('provider', 'Claude', 'claude');
    await page.getByRole('group', { name: 'Usage view' }).getByRole('button', { name: 'Human Words' }).click();
    await split.getByRole('button', { name: 'Repository', exact: true }).click();
    await checkFacet('repository_name', rows[0].repository_name, rows[0].repository_name);
    await split.getByRole('button', { name: 'Provider', exact: true }).click();
    const providerSwatch = page.locator('.usage-chart-legend .usage-swatch').first();
    await providerSwatch.waitFor();
    await page.locator('td[data-qt-field="providers"] .usage-facet-badge').waitFor();
    assert.equal(await page.locator('td[data-qt-field="providers"] .usage-facet-badge').evaluate(element => getComputedStyle(element).backgroundColor), await providerSwatch.evaluate(element => getComputedStyle(element).backgroundColor));
    assert.equal(await page.locator('td[data-qt-field="repository_name"] .usage-facet-badge').count(), 0);
    await page.getByRole('group', { name: 'Show', exact: true }).getByRole('button', { name: 'All input', exact: true }).click();
    await checkFacet('providers', 'Claude + Codex', 'claude,codex');
    await page.getByRole('group', { name: 'List', exact: true }).getByRole('button', { name: 'Messages', exact: true }).click();
    await page.waitForFunction(() => document.querySelectorAll('.usage-facet-badge').length === 0);
    assert.deepEqual(errors, []);
  } finally {
    await browser.close();
  }
});
