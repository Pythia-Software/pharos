const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');

const root = path.resolve(__dirname, '..');
const requireLocal = createRequire(path.join(root, '.context/browser-tests/package.json'));
const { chromium } = requireLocal('playwright');
const source = fs.readFileSync(path.join(root, 'internal/archive/assets/ui.py'), 'utf8');
const html = source.slice(source.indexOf("r'''") + 4, source.lastIndexOf("'''"));
const factors = JSON.parse(fs.readFileSync(path.join(root, 'carbon/co2_factors.json'), 'utf8'));

test('Usage Carbon Impact estimates CO2e and recalculates from its controls', async () => {
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
    const page = await browser.newPage();
    const errors = [];
    page.on('pageerror', error => { if ((error.stack || '').includes('/assets/carbon.js')) errors.push(error.message); });
    // 1M mid-size output tokens all time; 1M cache reads in the last 30 days.
    const body = {
      factors,
      windows: [
        { key: 'all', tokens: { medium: { uncached_input: 0, cache_write: 0, cache_read: 0, output: 1e6 } } },
        { key: '30d', tokens: { medium: { uncached_input: 0, cache_write: 0, cache_read: 1e6, output: 0 } } },
      ],
      default_tier_models: [{ model: 'mystery-model', tokens: 1234 }],
    };
    await page.route('http://carbon.test/**', route => {
      const url = new URL(route.request().url());
      if (url.pathname === '/usage' || url.pathname === '/settings') return route.fulfill({ contentType: 'text/html', body: html });
      if (url.pathname.startsWith('/assets/')) {
        const file = path.join(root, 'internal/archive/assets', url.pathname.slice('/assets/'.length));
        if (fs.existsSync(file)) return route.fulfill({ contentType: file.endsWith('.css') ? 'text/css' : 'application/javascript', body: fs.readFileSync(file) });
      }
      if (url.pathname === '/api/query/usage/carbon') {
        const request = route.request().postDataJSON();
        const period = body.windows[request.where?.length ? 1 : 0];
        const day = new Date(); day.setDate(day.getDate() - (request.where?.length ? 1 : 40));
        return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ ...body, windows: [period], series: [{ day: day.toLocaleDateString('en-CA'), value: '', tokens: period.tokens }], choices: { provider: [], repository_name: [] } }) });
      }
      if (url.pathname.startsWith('/api/query/') && route.request().method() === 'POST') return route.fulfill({ contentType: 'application/json', body: JSON.stringify(url.pathname.endsWith('/aggregations') ? { metrics: [] } : { rows: [], total: 0 }) });
      if (url.pathname.startsWith('/api/')) return route.fulfill({ contentType: 'application/json', body: '{}' });
      return route.fulfill({ status: 404 });
    });
    await page.goto('http://carbon.test/usage?usage=carbon');
    assert.equal(await page.getByRole('group', { name: 'Usage view' }).getByRole('button', { name: 'Carbon Impact' }).getAttribute('aria-pressed'), 'true');
    const card = page.locator('#carbonCard');
    const assumptions = page.getByRole('region', { name: 'Estimate assumptions', exact: true });
    const summary = page.locator('#carbonSummary');
    const big = summary.locator('.carbon-hero .big');
    // 1M output × 400 Wh × PUE 1.15 × 350 g/kWh = 161 g.
    await big.filter({ hasText: '161 g CO₂e' }).waitFor();
    assert.equal(await summary.locator('.carbon-electricity .big').textContent(), '460 Wh');
    assert.equal(await card.locator('.carbon-calculation').getAttribute('open'), null);
    assert.ok((await assumptions.boundingBox()).y < (await page.locator('.carbon-filters').boundingBox()).y);
    await card.locator('.carbon-calculation > summary').click();
    assert.match(await card.textContent(), /Assigned to Mid-size models by default; size unknown/);
    const ledger = card.locator('.carbon-ledger').first();
    const contribution = ledger.locator('.carbon-term-row').first();
    assert.deepEqual(await contribution.locator('.carbon-operator').allTextContents(), ['×', '=', '×', '=', '×', '×', '×', '=']);
    assert.equal(await contribution.locator('td').first().textContent(), '1M tok');
    assert.equal(await contribution.getByRole('math').first().getAttribute('aria-label'), '1 OE tokens divided by 1 output token');
    assert.equal(await contribution.getByRole('math').nth(1).getAttribute('aria-label'), '400 server Wh divided by 1M OE tokens');
    assert.deepEqual(await contribution.locator('.carbon-quantity strong').allTextContents(), ['1M', '1M', '400', '161 g']);
    assert.match(await contribution.textContent(), /400.*1.15 fWh.*1 sWh.*1 kWh.*1k fWh.*350 g.*1 kWh.*161 g/s);
    await contribution.locator('.carbon-row-trigger').click();
    await card.getByRole('heading', { name: 'Substituted arithmetic', exact: true }).waitFor();
    assert.match(await ledger.locator('.carbon-row-evidence').first().textContent(), /1,000,000 output tokens.*400 Wh.*400 server Wh/);
    await contribution.getByRole('button', { name: 'Explain medium model-tier energy', exact: true }).click();
    const factor = page.getByRole('dialog', { name: 'Mid-size models', exact: true });
    assert.match(await factor.textContent(), /Assumed.*400 Wh per 1M output tokens/);
    assert.ok(await factor.locator('a[href*="arxiv.org"]').count());
    await factor.getByRole('button', { name: 'Close information' }).click();
    const vehicle = summary.locator('[data-carbon-control="vehicle"]');
    const from = summary.locator('[data-carbon-control="flightFrom"]');
    const to = summary.locator('[data-carbon-control="flightTo"]');
    assert.equal(await vehicle.inputValue(), 'car_miles');
    assert.equal(await from.inputValue(), 'jfk');
    assert.equal(await to.inputValue(), 'den');
    assert.equal(await summary.locator('.carbon-facts > .carbon-comparison').count(), 2);
    assert.equal(await from.locator('option:checked').textContent(), 'NYC');
    assert.equal(await to.locator('option:checked').textContent(), 'DEN');
    assert.ok((await from.boundingBox()).width <= 52);
    const vehicleMiles = await summary.locator('.carbon-comparison').first().locator('b').textContent();
    await vehicle.selectOption('car_hybrid');
    assert.notEqual(await summary.locator('.carbon-comparison').first().locator('b').textContent(), vehicleMiles);
    await vehicle.selectOption('car_pickup');
    assert.notEqual(await summary.locator('.carbon-comparison').first().locator('b').textContent(), vehicleMiles);
    await vehicle.selectOption('car_electric');
    assert.notEqual(await summary.locator('.carbon-comparison').first().locator('b').textContent(), vehicleMiles);
    const flightCount = await summary.locator('.carbon-comparison').nth(1).locator('b').textContent();
    await to.selectOption('sfo');
    const shortFlightCount = await summary.locator('.carbon-comparison').nth(1).locator('b').textContent();
    assert.notEqual(shortFlightCount, flightCount);
    await from.selectOption('den');
    assert.equal(await from.inputValue(), 'den');
    assert.notEqual(await summary.locator('.carbon-comparison').nth(1).locator('b').textContent(), shortFlightCount);
    assert.equal(await page.locator('#usage #carbonCard').count(), 1);
    assert.equal(await page.locator('#settings #carbonCard').count(), 0);

    await assumptions.locator('[data-carbon-control="grid"]').selectOption('world');
    await big.filter({ hasText: '211 g CO₂e' }).waitFor();
    await assumptions.locator('[data-carbon-control="grid"]').selectOption('custom');
    const custom = assumptions.locator('input[data-carbon-control="customGrid"]');
    assert.equal(await page.locator('.carbon-info-button').count(), 7);
    await custom.fill('100');
    await custom.dispatchEvent('change');
    await big.filter({ hasText: '46 g CO₂e' }).waitFor();
    const pue = assumptions.getByRole('group', { name: 'PUE' });
    const estimate = assumptions.getByRole('group', { name: 'Estimate' });
    const prefillRatio = assumptions.getByRole('group', { name: 'Input sensitivity' });
    assert.equal(await pue.getByRole('button', { name: 'Central' }).getAttribute('aria-pressed'), 'true');
    await pue.getByRole('button', { name: 'High' }).click();
    await big.filter({ hasText: '52 g CO₂e' }).waitFor();
    assert.equal(await pue.getByRole('button', { name: 'High' }).evaluate(button => button === document.activeElement), true);
    // Estimate changes model energy and ratios while PUE remains high.
    await estimate.getByRole('button', { name: 'High' }).click();
    await big.filter({ hasText: '156 g CO₂e' }).waitFor();
    assert.equal(await pue.getByRole('button', { name: 'High' }).getAttribute('aria-pressed'), 'true');
    await prefillRatio.getByRole('button', { name: 'Watershed' }).click();
    // Output is unchanged by the prefill ratio.
    await big.filter({ hasText: '156 g CO₂e' }).waitFor();

    const help = assumptions.getByRole('button', { name: 'About Input sensitivity' });
    await help.click();
    const dialog = page.getByRole('dialog', { name: 'Input sensitivity' });
    assert.match(await dialog.textContent(), /Anthropic’s published input-to-output API price ratio/);
    assert.match(await dialog.textContent(), /0\.32 Wh per 1,000 input tokens and 0\.96 Wh per 1,000 output tokens/);
    assert.match(await dialog.textContent(), /Why both are useful/);
    assert.equal(await dialog.locator('a[href*="sanity.io"]').count(), 1);
    await page.keyboard.press('Escape');
    await dialog.waitFor({ state: 'detached' });
    assert.equal(await help.evaluate(button => button === document.activeElement), true);
    await assumptions.getByRole('button', { name: 'About Electricity grid' }).click();
    await page.getByRole('dialog', { name: 'Electricity grid' }).getByRole('button', { name: 'Close information' }).click();
    assert.equal(await page.getByRole('dialog').count(), 0);

    // Last 30 days: 1M cache reads × 1,200 Wh × 0.08 × (5/3) × 1.3 × 100 g/kWh = 20.8 g.
    await page.getByRole('combobox', { name: 'Range', exact: true }).selectOption('30d');
    await big.filter({ hasText: '20.8 g CO₂e' }).waitFor();

    // Choices persist across reloads.
    await page.reload();
    await big.filter({ hasText: '20.8 g CO₂e' }).waitFor();
    assert.equal(await pue.getByRole('button', { name: 'High' }).getAttribute('aria-pressed'), 'true');
    assert.equal(await prefillRatio.getByRole('button', { name: 'Watershed' }).getAttribute('aria-pressed'), 'true');
    assert.equal(await vehicle.inputValue(), 'car_electric');
    assert.equal(await from.inputValue(), 'den');
    assert.equal(await to.inputValue(), 'sfo');
    assert.equal(await card.locator('.carbon-calculation').getAttribute('open'), null, 'Calculation starts collapsed after reload');
    await card.locator('.carbon-calculation > summary').click();
    assert.match(await card.locator('.carbon-ledger').first().textContent(), /Cache reads.*1M.*0.1333.*1.2k.*160.*20.8 g/s);
    await card.locator('.carbon-method > summary').click();
    assert.equal(await card.locator('.carbon-source-index > li').count(), factors.sources.length);
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

test('Calculation shows live cache arithmetic, assumptions, and sources with progressive detail', async () => {
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 1100 } });
    const requests = [], errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const counts = { small: { output: 1000000 }, medium: { uncached_input: 1000000, cache_write: 2000000, cache_read: 20000000, output: 500000 }, large: { output: 250000 } };
    const today = new Date().toLocaleDateString('en-CA');
    const body = { factors, windows: [{ tokens: counts }], series: [{ day: today, value: '', tokens: counts }], choices: { provider: ['codex'], repository_name: ['math/repo'] },
      default_tier_models: [{ model: 'mystery-model', tokens: 500000 }], default_tier_unknown_tokens: 250000, unclassified_tokens: 100000, undated_tokens: 0 };
    await page.route('http://carbon-math.test/**', route => {
      const url = new URL(route.request().url());
      if (url.pathname === '/usage') return route.fulfill({ contentType: 'text/html', body: html });
      if (url.pathname.startsWith('/assets/')) {
        const file = path.join(root, 'internal/archive/assets', url.pathname.slice('/assets/'.length));
        if (fs.existsSync(file)) return route.fulfill({ contentType: file.endsWith('.css') ? 'text/css' : 'application/javascript', body: fs.readFileSync(file) });
      }
      const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
      if (url.pathname === '/api/query/usage/carbon') { requests.push(route.request().postDataJSON()); return json(body); }
      if (url.pathname === '/api/tools/status') return json({ conversations: 0, pending_conversations: 0, backfill: { running: false, done: 0, total: 0, error: null } });
      if (url.pathname.startsWith('/api/query/') && route.request().method() === 'POST') return json({ rows: [], metrics: [], total: 0 });
      if (url.pathname.startsWith('/api/')) return json({});
      return route.fulfill({ status: 404 });
    });
    await page.goto('http://carbon-math.test/usage?usage=carbon');
    const total = page.locator('#carbonSummary .carbon-title .big'), card = page.locator('#carbonCard');
    const assumptions = page.getByRole('region', { name: 'Estimate assumptions', exact: true });
    await total.filter({ hasText: '372 g CO₂e' }).waitFor();
    assert.equal(await card.locator('.carbon-calculation').getAttribute('open'), null);
    assert.match(await card.locator('.carbon-calculation > summary').textContent(), /750k tokens with unknown model size/);
    assert.match(await card.locator('.carbon-calculation-preview').textContent(), /24.8M tokens → ≈ 2.75M modeled output-equivalent tokens → 1.06 kWh → 372 g CO₂e/);
    await card.locator('.carbon-calculation > summary').click();
    const ledger = card.locator('.carbon-ledger').first(), cache = ledger.locator('.carbon-term-row[data-category="cache_read"][data-tier="medium"]');
    assert.equal(await ledger.locator('.carbon-term-row').count(), 6);
    assert.equal(await card.locator('.carbon-ledger').count(), 1);
    assert.deepEqual(await ledger.locator('.carbon-ledger-group th').allTextContents(), ['Uncached input', 'Cache writes', 'Cache reads', 'Output']);
    assert.equal(await ledger.locator('.carbon-zero-row, .carbon-subtotal, .carbon-factor-kind').count(), 0);
    for (const group of await ledger.locator('tbody').all()) {
      const names = await group.locator('.carbon-term-row th .carbon-row-trigger').allTextContents();
      assert.deepEqual(names.map(name => name.replace('Details', '')), await group.getAttribute('data-category') === 'output' ? ['Small', 'Mid', 'Large'] : ['Mid']);
    }
    assert.deepEqual(await cache.locator('.carbon-operator').allTextContents(), ['×', '=', '×', '=', '×', '×', '×', '=']);
    assert.equal(await cache.getByRole('math').first().getAttribute('aria-label'), '0.02 OE tokens divided by 1 cache-read token');
    assert.equal(await cache.getByRole('math').nth(1).getAttribute('aria-label'), '400 server Wh divided by 1M OE tokens');
    assert.deepEqual(await cache.locator('.carbon-quantity strong').allTextContents(), ['20M', '400k', '160', '64.4 g']);
    const emissions = cache;
    assert.deepEqual(await cache.getByRole('math').allTextContents(), ['0.02 OE / 1 tok', '400 sWh / 1M OE', '1.15 fWh / 1 sWh', '1 kWh / 1k fWh', '350 g / 1 kWh']);
    assert.equal(await cache.locator('.carbon-assumption-box').count(), 4);
    assert.equal(await ledger.locator('.carbon-emissions-row').count(), 0, 'Each tier keeps the full equation on one row');
    assert.equal(await cache.locator('.carbon-assumption-box').first().evaluate(el => getComputedStyle(el).borderTopStyle), 'dotted');
    assert.deepEqual(await ledger.locator('.carbon-grand-total .carbon-quantity strong').allTextContents(), ['24.8M', '2.75M', '925', '372 g']);
    await card.getByRole('button', { name: 'How assumption boxes work', exact: true }).click();
    const assumptionHelp = page.getByRole('dialog', { name: 'Calculation assumptions', exact: true });
    assert.match(await assumptionHelp.textContent(), /Dotted boxes contain assumptions.*Click any box/s);
    await assumptionHelp.getByRole('button', { name: 'Close information' }).click();
    await emissions.getByRole('button', { name: 'Explain facility overhead', exact: true }).click();
    const pueHelp = page.getByRole('dialog', { name: 'Data-center overhead (PUE)', exact: true });
    assert.match(await pueHelp.textContent(), /1.15× server electricity/);
    await pueHelp.getByRole('button', { name: 'Close information' }).click();
    assert.equal(await card.locator('.carbon-calculation-overview').evaluate(element => getComputedStyle(element).borderBottomWidth), '0px');
    assert.equal(await ledger.locator('.carbon-row-evidence').count(), 0, 'Evidence is built only when requested');
    await cache.locator('.carbon-row-trigger').click();
    const explanation = ledger.locator('.carbon-row-evidence');
    await explanation.getByRole('heading', { name: 'Substituted arithmetic', exact: true }).waitFor();
    assert.match(await explanation.textContent(), /20,000,000 cache-read tokens.*0.02.*400 Wh.*160 server Wh.*184 facility Wh.*64.4 g CO₂e/s);
    assert.ok(await explanation.locator('a[href*="2505.23970"]').count(), 'Cache evidence links to its own research');
    assert.match(await card.textContent(), /100,000 unclassified tokens are already included in uncached input/);
    assert.match(await card.textContent(), /Model not reported: 250,000 tokens/);
    await assumptions.getByRole('group', { name: 'Input sensitivity' }).getByRole('button', { name: 'Watershed' }).click();
    await total.filter({ hasText: '480 g CO₂e' }).waitFor();
    assert.match(await card.locator('.carbon-calculation-preview').textContent(), /≈ 3.42M modeled output-equivalent tokens/);
    assert.deepEqual(await ledger.locator('.carbon-grand-total .carbon-quantity strong').allTextContents(), ['24.8M', '3.42M', '1.19k', '480 g']);
    assert.equal(await cache.locator('.carbon-row-trigger').getAttribute('aria-expanded'), 'true', 'Open row survives recalculation');
    assert.match(await explanation.textContent(), /0.02 × 5\/3/);
    assert.match(await explanation.textContent(), /266.6666667 server Wh/);
    assert.equal(requests.length, 1, 'Assumption changes do not refetch token counts');
    await assumptions.getByRole('group', { name: 'PUE' }).getByRole('button', { name: 'High' }).click();
    await total.filter({ hasText: '542 g CO₂e' }).waitFor();
    await assumptions.getByRole('combobox', { name: 'Electricity grid' }).selectOption('custom');
    await assumptions.getByRole('spinbutton', { name: 'Custom grid intensity in g CO₂e per kWh' }).fill('100');
    await assumptions.getByRole('spinbutton', { name: 'Custom grid intensity in g CO₂e per kWh' }).dispatchEvent('change');
    await total.filter({ hasText: '155 g CO₂e' }).waitFor();
    await emissions.getByRole('button', { name: 'Explain electricity emissions factor', exact: true }).click();
    const grid = page.getByRole('dialog', { name: 'Electricity grid', exact: true });
    assert.match(await grid.textContent(), /User-supplied assumption/);
    assert.match(await grid.textContent(), /100 g CO₂e per kWh/);
    await grid.getByRole('button', { name: 'Close information' }).click();
    await assumptions.getByRole('group', { name: 'Estimate' }).getByRole('button', { name: 'High' }).click();
    await total.filter({ hasText: '926 g CO₂e' }).waitFor();
    assert.match(await cache.textContent(), /20M.*0.1333.*1.2k.*3.2k/s);
    assert.match(await emissions.textContent(), /3.2k.*416 g/s);
    // Restore the central example and capture the inspectable math, not only output tokens.
    await assumptions.getByRole('group', { name: 'Estimate' }).getByRole('button', { name: 'Central' }).click();
    await assumptions.getByRole('group', { name: 'PUE' }).getByRole('button', { name: 'Central' }).click();
    await assumptions.getByRole('group', { name: 'Input sensitivity' }).getByRole('button', { name: 'Labs' }).click();
    await assumptions.getByRole('combobox', { name: 'Electricity grid' }).selectOption('us_average');
    await total.filter({ hasText: '372 g CO₂e' }).waitFor();
    await cache.locator('.carbon-row-trigger').click();
    await page.evaluate(() => {
      const main = document.querySelector('main'), card = document.querySelector('#carbonCard');
      main.scrollTop += card.getBoundingClientRect().top - main.getBoundingClientRect().top;
    });
    await page.setViewportSize({ width: 1280, height: 1400 });
    await page.screenshot({ path: path.join(root, '.context/carbon-calculation-worked.png') });
    await cache.locator('.carbon-row-trigger').click();
    await cache.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(root, '.context/carbon-calculation-evidence.png') });
    await page.setViewportSize({ width: 390, height: 844 });
    await cache.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(root, '.context/carbon-calculation-evidence-mobile.png') });
    assert.ok(await page.locator('.carbon-ledger-scroll').first().evaluate(el => el.getBoundingClientRect().right <= innerWidth && el.scrollWidth > el.clientWidth), 'Mobile ledger scrolls locally while preserving a single equation row');
    // A real dev-library count exercises compact billions and the full desktop equation.
    counts.medium.cache_read = 174650085702;
    await page.setViewportSize({ width: 1280, height: 900 });
    await page.reload();
    await total.filter({ hasText: '563 kg CO₂e' }).waitFor();
    await card.locator('.carbon-calculation > summary').click();
    assert.equal(await cache.locator('.carbon-quantity strong').first().textContent(), '175B');
    assert.equal(await cache.locator('.carbon-quantity').first().getAttribute('title'), '174,650,085,702 cache-read tokens');
    assert.match(await card.locator('.carbon-ledger-key').first().textContent(), /tok = tokens.*OE = output-equivalent tokens.*sWh \/ fWh = server \/ facility watt-hours.*g \/ kg \/ t/s);
    assert.ok(await ledger.evaluate(table => table.parentElement.scrollWidth <= table.parentElement.clientWidth), 'Full desktop equation fits without horizontal scrolling');
    assert.ok(await ledger.locator('.carbon-term-row').evaluateAll(rows => rows.every(row => row.offsetHeight <= 55 && [...row.querySelectorAll('.carbon-math-cell')].every(cell => {
      const parts = [...cell.children];
      return cell.scrollWidth <= cell.clientWidth && (parts.length < 2 || parts[0].getBoundingClientRect().right <= parts[1].getBoundingClientRect().left);
    }))), 'Large counts fit a single row without operand collisions');
    await cache.locator('.carbon-row-trigger').click();
    assert.match(await ledger.locator('.carbon-row-evidence').textContent(), /174,650,085,702 cache-read tokens/, 'Details retain the precise logged count');
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

test('Carbon filters, dates, and chart controls stay distinct from assumptions and handle stale responses', async () => {
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 1100 } });
    await page.addInitScript(() => {
      if (!localStorage.getItem('pharos-carbon-chart')) localStorage.setItem('pharos-carbon-chart', JSON.stringify({ metric: 'co2', split: 'type', period: 'week', hours: 6, days: 3, scale: 'logarithmic', rollingPeriods: 10 }));
    });
    const requests = [], errors = [];
    page.on('pageerror', error => errors.push(error.stack || error.message));
    const date = offset => { const d = new Date(); d.setHours(12, 0, 0, 0); d.setDate(d.getDate() - offset); return d; };
    const day = d => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
    const rows = [
      { date: date(40), repository_name: 'alpha/repo', provider: 'claude', tier: 'medium', output: 1e6 },
      { date: date(1), repository_name: 'beta/repo', provider: 'codex', tier: 'small', output: 1e6 },
      { date: date(0), repository_name: 'alpha/repo', provider: 'codex', tier: 'large', output: 1e6 },
      { date: date(0), repository_name: null, provider: 'claude', tier: 'medium', output: 100000 },
    ];
    let fail = false, hold = false, held = null;
    await page.route('http://carbon-filters.test/**', async route => {
      const url = new URL(route.request().url());
      if (url.pathname === '/usage') return route.fulfill({ contentType: 'text/html', body: html });
      if (url.pathname.startsWith('/assets/')) {
        const file = path.join(root, 'internal/archive/assets', url.pathname.slice('/assets/'.length));
        if (fs.existsSync(file)) return route.fulfill({ contentType: file.endsWith('.css') ? 'text/css' : 'application/javascript', body: fs.readFileSync(file) });
      }
      const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
      if (url.pathname === '/api/query/usage/carbon') {
        const request = route.request().postDataJSON(); requests.push(request);
        if (fail) return route.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"Usage failed to load"}' });
        const selected = rows.filter(row => request.where.every(term => {
          if (term.field === 'first_usage_at') return row.date < new Date(term.value);
          if (term.field === 'last_usage_at') return row.date >= new Date(term.value);
          if (term.op === 'is_null') return row[term.field] === null;
          return row[term.field] === term.value;
        }));
        const total = {}, points = new Map();
        for (const row of selected) {
          (total[row.tier] ??= {}).output = (total[row.tier].output ?? 0) + row.output;
          const bucket = request.hourly ? row.date.toISOString() : day(row.date), value = request.split === 'tier' ? row.tier : row[request.split] ?? '';
          const key = `${bucket}|${value}`, point = points.get(key) ?? { day: bucket, value, tokens: {} };
          (point.tokens[row.tier] ??= {}).output = (point.tokens[row.tier].output ?? 0) + row.output;
          points.set(key, point);
        }
        const body = { factors, windows: [{ key: 'filtered', tokens: total }], series: [...points.values()], choices: { repository_name: ['', 'alpha/repo', 'beta/repo'], provider: ['claude', 'codex'] }, default_tier_models: [], undated_tokens: 0 };
        if (hold) {
          hold = false;
          held = () => json(body).catch(() => {});
          return;
        }
        return json(body);
      }
      if (url.pathname.startsWith('/api/query/') && route.request().method() === 'POST') return json(url.pathname.endsWith('/aggregations') ? { metrics: [] } : { rows: [], total: 0 });
      if (url.pathname === '/api/tools/status') return json({ conversations: 0, current_conversations: 0, pending_conversations: 0, tool_calls: 0, backfill: { running: false, done: 0, total: 0, error: null } });
      if (url.pathname.startsWith('/api/')) return json({});
      return route.fulfill({ status: 404 });
    });
    await page.goto('http://carbon-filters.test/usage?usage=carbon');
    const total = page.locator('#carbonSummary .carbon-title .big');
    const filters = page.getByRole('region', { name: 'Usage filters', exact: true });
    const assumptions = page.getByRole('region', { name: 'Estimate assumptions', exact: true });
    const repo = filters.getByRole('combobox', { name: 'Repository', exact: true });
    const provider = filters.getByRole('combobox', { name: 'Provider', exact: true });
    const range = filters.getByRole('combobox', { name: 'Range', exact: true });
    const interval = page.getByRole('combobox', { name: 'Chart interval', exact: true });
    await total.filter({ hasText: '580 g CO₂e' }).waitFor();
    const summaryLabel = page.locator('#carbonSummary .carbon-title .meta');
    assert.equal(await summaryLabel.textContent(), 'Estimated CO₂e · All repositories · All providers · All time');
    assert.equal(await assumptions.locator('p.muted').count(), 0);
    assert.equal(await filters.locator('p.muted').count(), 0);
    assert.equal(await page.getByRole('combobox', { name: 'Scale', exact: true }).count(), 0);
    assert.equal(await page.getByRole('combobox', { name: 'Smoothing', exact: true }).count(), 0);
    assert.match(await page.locator('.usage-chart-bars').getAttribute('aria-label'), /; linear scale$/);
    assert.equal(await page.locator('.usage-chart-log, .usage-chart-break, .usage-chart-average-label').count(), 0);
    const analysis = page.getByRole('region', { name: 'Carbon usage analysis', exact: true });
    assert.equal(await analysis.locator('.carbon-filters, .carbon-summary, .usage-chart').count(), 3);
    assert.equal(await assumptions.locator('table').count(), 0);
    assert.equal(await page.locator('#carbonCard .carbon-calculation').getAttribute('open'), null);
    assert.ok((await assumptions.boundingBox()).y < (await filters.boundingBox()).y);
    assert.equal(await filters.locator('[data-carbon-control]').count(), 0);
    assert.equal(await assumptions.getByRole('combobox', { name: 'Repository' }).count(), 0);
    assert.equal(await page.locator('.usage-chart').getByRole('combobox', { name: 'Electricity grid' }).count(), 0);
    await repo.selectOption('v:alpha/repo');
    await total.filter({ hasText: '523 g CO₂e' }).waitFor();
    assert.equal(await summaryLabel.textContent(), 'Estimated CO₂e · alpha/repo · All providers · All time');
    await provider.selectOption('v:codex');
    await total.filter({ hasText: '362 g CO₂e' }).waitFor();
    assert.equal(await summaryLabel.textContent(), 'Estimated CO₂e · alpha/repo · Codex · All time');
    assert.ok(requests.at(-1).where.some(term => term.field === 'repository_name' && term.value === 'alpha/repo'));
    assert.ok(requests.at(-1).where.some(term => term.field === 'provider' && term.value === 'codex'));
    await page.reload();
    await total.filter({ hasText: '362 g CO₂e' }).waitFor();
    assert.equal(await repo.inputValue(), 'v:alpha/repo');
    assert.equal(await provider.inputValue(), 'v:codex');
    await filters.getByRole('button', { name: 'Clear filters' }).click();
    await total.filter({ hasText: '580 g CO₂e' }).waitFor();
    await repo.selectOption('v:');
    await total.filter({ hasText: '16.1 g CO₂e' }).waitFor();
    assert.ok(requests.at(-1).where.some(term => term.op === 'is_null'));
    await provider.selectOption('v:codex');
    await page.getByRole('status').filter({ hasText: 'No reported token usage matches these filters.' }).waitFor();
    assert.equal(await total.textContent(), '0 g CO₂e');
    assert.equal(await page.locator('.usage-chart-bars i').count(), 0);
    await filters.getByRole('button', { name: 'Clear filters' }).click();
    await total.filter({ hasText: '580 g CO₂e' }).waitFor();

    // Presets filter dates without rewriting the independently chosen chart interval.
    assert.equal(await interval.inputValue(), 'week');
    await range.selectOption('30d');
    await total.filter({ hasText: '419 g CO₂e' }).waitFor();
    assert.equal(await interval.inputValue(), 'week');
    await range.selectOption('all');
    await total.filter({ hasText: '580 g CO₂e' }).waitFor();
    assert.equal(await interval.inputValue(), 'week');
    assert.equal(await page.evaluate(() => JSON.parse(localStorage.getItem('pharos-carbon-chart')).period), 'week');

    // A partially entered range keeps date inputs and focus, including From = today.
    for (const period of ['week', 'day']) {
      await interval.selectOption(period);
      await range.selectOption('custom');
      const fromDate = filters.getByLabel('From date', { exact: true });
      const throughDate = filters.getByLabel('Through date', { exact: true });
      await fromDate.fill(day(date(0)));
      await total.filter({ hasText: '378 g CO₂e' }).waitFor();
      assert.equal(await interval.inputValue(), period);
      assert.equal(await fromDate.getAttribute('type'), 'date');
      assert.equal(await throughDate.inputValue(), '');
      assert.equal(await fromDate.evaluate(input => input === document.activeElement), true, 'From keeps focus while Through is unset');
      await throughDate.fill(day(date(0)));
      assert.equal(await interval.inputValue(), period);
      assert.equal(await throughDate.getAttribute('type'), 'date');
      assert.equal(await throughDate.evaluate(input => input === document.activeElement), true, 'Through keeps focus after completing the range');
      await range.selectOption('all');
      await total.filter({ hasText: '580 g CO₂e' }).waitFor();
    }
    await interval.selectOption('week');

    await range.selectOption('custom');
    await filters.getByLabel('From date', { exact: true }).fill(day(date(1)));
    await filters.getByLabel('Through date', { exact: true }).fill(day(date(1)));
    await total.filter({ hasText: '40.2 g CO₂e' }).waitFor();
    assert.equal(await summaryLabel.textContent(), `Estimated CO₂e · All repositories · All providers · ${date(1).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })}`);
    assert.equal(new Date(requests.at(-1).where.find(term => term.field === 'first_usage_at').value).getDate(), date(0).getDate());
    await interval.selectOption('day');
    await page.locator('.usage-chart h3', { hasText: 'Estimated CO₂e per day by token type' }).waitFor();
    await page.locator('.usage-chart-bars > span[aria-label*="40.2 g"]').waitFor();
    await page.getByRole('combobox', { name: 'Breakdown', exact: true }).selectOption('provider');
    await page.locator('.usage-chart-bars > span[aria-label*="Codex 40.2 g"]').waitFor();
    assert.equal(requests.at(-1).split, 'provider');
    await page.getByRole('combobox', { name: 'Breakdown', exact: true }).selectOption('repository_name');
    await page.locator('.usage-chart-bars > span[aria-label*="beta/repo 40.2 g"]').waitFor();
    await page.getByRole('combobox', { name: 'Breakdown', exact: true }).selectOption('tier');
    await page.locator('.usage-chart-bars > span[aria-label*="Small models 40.2 g"]').waitFor();
    await interval.selectOption('hour');
    await page.locator('.usage-chart-bars > span[aria-label*="Small models 40.2 g"]').waitFor();
    assert.equal(requests.at(-1).hourly, true);
    await interval.selectOption('hours');
    await page.getByRole('spinbutton', { name: 'Number of hours' }).fill('6');
    await page.locator('.usage-chart h3', { hasText: 'Estimated CO₂e per 6 hours' }).waitFor();
    await interval.selectOption('day');
    await page.locator('.usage-chart-bars > span[aria-label*="Small models 40.2 g"]').waitFor();
    const beforeAssumptions = requests.length;
    await assumptions.getByRole('combobox', { name: 'Electricity grid' }).selectOption('world');
    await total.filter({ hasText: '52.7 g CO₂e' }).waitFor();
    await page.locator('.usage-chart-bars > span[aria-label*="52.7 g"]').waitFor();
    await page.getByRole('combobox', { name: 'Metric', exact: true }).selectOption('energy');
    await page.locator('.usage-chart-bars > span[aria-label*="115 Wh"]').waitFor();
    assert.match(await page.locator('.usage-chart-bars').getAttribute('aria-label'), /; linear scale$/);
    assert.equal(requests.length, beforeAssumptions);
    // Invalid custom dates remain visibly invalid and never become a query.
    await filters.getByLabel('From date', { exact: true }).fill(day(date(0)));
    await page.getByRole('alert', { name: '' }).filter({ hasText: 'Start date must be on or before end date.' }).waitFor();
    assert.equal(requests.length, beforeAssumptions);
    await range.selectOption('all');
    await total.filter({ hasText: '758 g CO₂e' }).waitFor();
    await page.getByRole('combobox', { name: 'Metric', exact: true }).selectOption('co2');
    await assumptions.getByRole('combobox', { name: 'Electricity grid' }).selectOption('us_average');
    await total.filter({ hasText: '580 g CO₂e' }).waitFor();
    // Keyboard date selection uses the same filter path as explicit dates.
    await page.locator('.usage-chart-bars > span').last().focus();
    await page.keyboard.press('Enter');
    await total.filter({ hasText: '378 g CO₂e' }).waitFor();
    await range.selectOption('all');
    await total.filter({ hasText: '580 g CO₂e' }).waitFor();

    // Pointer dragging custom multi-day columns retains repository/provider and zooms to days.
    await repo.selectOption('v:alpha/repo');
    await provider.selectOption('v:claude');
    await total.filter({ hasText: '161 g CO₂e' }).waitFor();
    await interval.selectOption('days');
    await page.getByRole('spinbutton', { name: 'Number of days' }).fill('14');
    await page.locator('.usage-chart h3', { hasText: 'Estimated CO₂e per 14 days' }).waitFor();
    const firstColumn = await page.locator('.usage-chart-bars > span').first().boundingBox();
    const secondColumn = await page.locator('.usage-chart-bars > span').nth(1).boundingBox();
    await page.mouse.move(firstColumn.x + firstColumn.width / 2, firstColumn.y + firstColumn.height / 2);
    await page.mouse.down();
    await page.mouse.move(secondColumn.x + secondColumn.width / 2, secondColumn.y + secondColumn.height / 2, { steps: 8 });
    await page.locator('.usage-chart-pick').waitFor();
    await page.mouse.up();
    await filters.getByLabel('Through date', { exact: true }).waitFor();
    await total.filter({ hasText: '161 g CO₂e' }).waitFor();
    assert.equal(await interval.inputValue(), 'day');
    assert.equal(await repo.inputValue(), 'v:alpha/repo');
    assert.equal(await provider.inputValue(), 'v:claude');
    assert.ok(requests.at(-1).where.some(term => term.field === 'last_usage_at'));
    assert.ok(requests.at(-1).where.some(term => term.field === 'first_usage_at'));
    assert.match(await summaryLabel.textContent(), /Estimated CO₂e · alpha\/repo · Claude · .+ – .+/);

    // Typed sub-day ranges retain precise local times and the chosen six-hour columns.
    await filters.getByRole('button', { name: 'Clear filters' }).click();
    await total.filter({ hasText: '580 g CO₂e' }).waitFor();
    await interval.selectOption('hours');
    await page.getByRole('spinbutton', { name: 'Number of hours' }).fill('6');
    await range.selectOption('custom');
    const fromTime = filters.getByLabel('From date and time', { exact: true });
    const untilTime = filters.getByLabel('Until date and time', { exact: true });
    await fromTime.fill(`${day(date(1))}T11:00`);
    await untilTime.fill(`${day(date(1))}T13:00`);
    await total.filter({ hasText: '40.2 g CO₂e' }).waitFor();
    assert.equal(await interval.inputValue(), 'hours');
    assert.equal(new Date(requests.at(-1).where.find(term => term.field === 'last_usage_at').value).getHours(), 11);
    assert.equal(new Date(requests.at(-1).where.find(term => term.field === 'first_usage_at').value).getHours(), 13);
    assert.match(await summaryLabel.textContent(), /11:00.*1:00.*\(end exclusive\)/);
    const beforeInvalidTime = requests.length;
    await untilTime.fill(`${day(date(1))}T11:00`);
    await page.getByRole('alert').filter({ hasText: 'Start must be before end.' }).waitFor();
    assert.equal(requests.length, beforeInvalidTime);
    await filters.getByRole('button', { name: 'Clear filters' }).click();
    await total.filter({ hasText: '580 g CO₂e' }).waitFor();
    await interval.selectOption('day');

    hold = true;
    await repo.selectOption('v:alpha/repo');
    await page.waitForFunction(() => document.querySelector('#carbonSummary').getAttribute('aria-busy') === 'true');
    for (let attempt = 0; !held && attempt < 100; attempt++) await new Promise(resolve => setTimeout(resolve, 10));
    assert.ok(held, "The old request should be waiting before selecting a new filter");
    await provider.selectOption('v:codex');
    await total.filter({ hasText: '362 g CO₂e' }).waitFor();
    await held();
    assert.equal(await total.textContent(), '362 g CO₂e');
    fail = true;
    await repo.selectOption('v:beta/repo');
    await total.filter({ hasText: 'Unavailable' }).waitFor();
    assert.equal(await page.locator('.usage-chart-bars i').count(), 0);
    fail = false;
    await page.getByRole('button', { name: 'Retry carbon usage' }).click();
    await total.filter({ hasText: '40.2 g CO₂e' }).waitFor();
    await filters.getByRole('button', { name: 'Clear filters' }).click();
    await total.filter({ hasText: '580 g CO₂e' }).waitFor();
    await page.getByRole('combobox', { name: 'Breakdown', exact: true }).selectOption('provider');
    await page.locator('.usage-chart-legend', { hasText: 'Codex' }).waitFor();
    // Other usage views retain their own chart preferences and filters.
    const views = page.getByRole('group', { name: 'Usage view' });
    await views.getByRole('button', { name: 'Machine Activity' }).click();
    assert.equal(await interval.inputValue(), 'week');
    assert.equal(await page.getByRole('combobox', { name: 'Scale', exact: true }).inputValue(), 'half');
    await views.getByRole('button', { name: 'Carbon Impact' }).click();
    await total.filter({ hasText: '580 g CO₂e' }).waitFor();
    assert.equal(await interval.inputValue(), 'day');
    assert.equal(await page.getByRole('combobox', { name: 'Scale', exact: true }).count(), 0);
    assert.equal(await page.getByRole('combobox', { name: 'Smoothing', exact: true }).count(), 0);
    assert.match(await page.locator('.usage-chart-bars').getAttribute('aria-label'), /; linear scale$/);
    assert.equal(new URL(page.url()).searchParams.get('usage'), 'carbon');
    await interval.selectOption('week');
    await page.setViewportSize({ width: 1280, height: 1100 });
    await page.screenshot({ path: path.join(root, '.context/carbon-usage-desktop.png'), fullPage: true });
    await page.setViewportSize({ width: 390, height: 844 });
    const providerBox = await provider.boundingBox(), rangeBox = await range.boundingBox();
    assert.ok(Math.abs(providerBox.y - rangeBox.y) < 1, 'Provider and Range should share a mobile row');
    const metricBox = await page.getByRole('combobox', { name: 'Metric', exact: true }).boundingBox();
    const breakdownBox = await page.getByRole('combobox', { name: 'Breakdown', exact: true }).boundingBox();
    assert.ok(Math.abs(metricBox.y - breakdownBox.y) < 1, 'Metric and Breakdown should share a mobile row');
    await page.screenshot({ path: path.join(root, '.context/carbon-usage-mobile.png'), fullPage: true });
    await page.locator('.usage-chart').scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(root, '.context/carbon-usage-mobile-chart.png') });
    await page.locator('#carbonAssumptions').scrollIntoViewIfNeeded();
    await page.evaluate(() => {
      const main = document.querySelector('main'), card = document.querySelector('#carbonAssumptions');
      main.scrollTop += card.getBoundingClientRect().top - main.getBoundingClientRect().top;
    });
    await page.screenshot({ path: path.join(root, '.context/carbon-usage-mobile-assumptions.png') });
    await page.locator('#carbonCard .carbon-calculation > summary').click();
    const calculation = page.locator('#carbonCard');
    await calculation.getByRole('heading', { name: '1 · Calculate each contribution', exact: true }).waitFor();
    assert.match(await calculation.textContent(), /1,440 server Wh.*1.15 PUE.*1,656 facility Wh/s);
    assert.match(await calculation.textContent(), /1.656 kWh.*350 g CO₂e\/kWh.*579.6 g CO₂e/s);
    await page.evaluate(() => {
      const main = document.querySelector('main'), card = document.querySelector('#carbonCard');
      main.scrollTop += card.getBoundingClientRect().top - main.getBoundingClientRect().top;
    });
    await page.screenshot({ path: path.join(root, '.context/carbon-calculation-mobile.png') });
    await page.setViewportSize({ width: 1280, height: 1400 });
    await page.screenshot({ path: path.join(root, '.context/carbon-calculation-desktop.png') });
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok(await page.locator('#carbonCard').evaluate(card => [...card.querySelectorAll('table')].every(table => (table.closest('.carbon-ledger-scroll') || table).getBoundingClientRect().right <= card.getBoundingClientRect().right)), 'Breakdown containers should fit inside the calculation card');
    assert.ok(await page.locator('main').evaluate(main => main.scrollWidth <= main.clientWidth), 'Carbon content should fit a narrow screen without page-level scrolling');
    assert.ok(await page.locator('.usage-heading').evaluate(heading => [...heading.querySelectorAll('button')].every(button => button.getBoundingClientRect().right <= innerWidth)), 'Usage view switches should remain visible');
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});
