const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');

const root = path.resolve(__dirname, '..');
const { chromium } = createRequire(path.join(root, '.context/browser-tests/package.json'))('playwright');
const source = fs.readFileSync(['src/pharos/ui.py', 'internal/archive/assets/ui.py'].map(name => path.join(root, name)).find(file => fs.existsSync(file)), 'utf8');
const html = source.slice(source.indexOf("r'''") + 4, source.lastIndexOf("'''"));

test('Usage summary refreshes on demand, and auto update refetches every 30 seconds while open', async () => {
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 720 } });
    const errors = [];
    let fetches = 0;
    page.on('pageerror', error => errors.push(error.message));
    await page.clock.install();
    await page.route('http://usage-summary.test/**', route => {
      const url = new URL(route.request().url());
      if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/assets/')) return route.fulfill({ contentType: 'text/html', body: html });
      if (url.pathname === '/api/usage/summary') {
        fetches++;
        const windows = ['1h', '6h', '24h', '7d', '30d', 'all'].map((key, index) => ({ key, human_words: fetches * 100 * (index + 1), tokens: fetches * 1000, cost_usd: fetches }));
        return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ windows }) });
      }
      if (url.pathname.startsWith('/api/')) return route.fulfill({ contentType: 'application/json', body: '{"items":[]}' });
      return route.fulfill({ contentType: 'application/javascript', body: '' });
    });
    await page.goto('http://usage-summary.test/library');
    const summary = page.locator('#usageSummary');
    const firstHour = summary.locator('tbody tr').first().locator('td').first();

    await page.locator('#usageSummaryToggle').click();
    await firstHour.filter({ hasText: '100' }).waitFor();
    const refresh = summary.getByRole('button', { name: 'Refresh usage summary' });
    const auto = summary.getByRole('button', { name: 'Auto' });
    assert.equal(await summary.locator('thead th').first().locator('button').count(), 2, 'controls sit in the corner cell');
    assert.equal(await auto.getAttribute('aria-pressed'), 'false');

    // The two form one combo button: a single shared edge, square where they meet, rounded outside.
    const combo = summary.getByRole('group', { name: 'Usage summary updates' });
    assert.equal(await combo.getAttribute('class'), 'combo-button usage-summary-controls');
    const [left, right] = [await refresh.boundingBox(), await auto.boundingBox()];
    assert.equal(right.x, left.x + left.width - 1, 'segments overlap by one pixel to share an edge');
    const radii = await page.evaluate(() => ['#usageSummaryRefresh', '#usageSummaryAuto'].map(selector => {
      const style = getComputedStyle(document.querySelector(selector));
      return [style.borderTopLeftRadius, style.borderTopRightRadius, style.borderBottomRightRadius, style.borderBottomLeftRadius];
    }));
    assert.deepEqual(radii, [['5px', '0px', '0px', '5px'], ['0px', '5px', '5px', '0px']]);
    // Whichever segment is hovered draws the shared edge, in the accent color.
    const accent = await page.evaluate(() => { const probe = document.createElement('i'); probe.style.color = 'var(--accent)'; document.body.append(probe); const color = getComputedStyle(probe).color; probe.remove(); return color; });
    for (const [segment, side] of [[refresh, 'Right'], [auto, 'Left']]) {
      await segment.hover();
      const [id, color] = await page.evaluate(([x, y, side]) => { const button = document.elementFromPoint(x, y).closest('button'); return [button.id, getComputedStyle(button)[`border${side}Color`]]; }, [right.x + 0.5, right.y + right.height / 2, side]);
      assert.equal(id, await segment.getAttribute('id'), 'the hovered segment sits on top at the shared edge');
      assert.equal(color, accent);
    }
    await page.mouse.move(0, 0);

    // Auto update is off by default, so time alone does not refetch.
    await page.clock.runFor(60_000);
    assert.equal(fetches, 1);

    await refresh.click();
    await firstHour.filter({ hasText: '200' }).waitFor();
    assert.equal(fetches, 2);
    assert.equal(await page.evaluate(() => document.activeElement?.id), 'usageSummaryRefresh', 'refreshing keeps focus on the button');
    assert.match(await refresh.getAttribute('title'), /updated/);

    await auto.click();
    assert.equal(await auto.getAttribute('aria-pressed'), 'true');
    await firstHour.filter({ hasText: '300' }).waitFor();
    assert.equal(await page.evaluate(() => localStorage.getItem('pharos-usage-summary-auto')), 'true');
    await page.clock.runFor(29_000);
    assert.equal(fetches, 3);
    await page.clock.runFor(1_500);
    await firstHour.filter({ hasText: '400' }).waitFor();

    // Closing stops the timer; reopening fetches once and resumes it.
    await page.keyboard.press('Escape');
    await page.clock.runFor(90_000);
    assert.equal(fetches, 4);
    await page.locator('#usageSummaryToggle').click();
    await firstHour.filter({ hasText: '500' }).waitFor();
    await page.clock.runFor(30_500);
    await firstHour.filter({ hasText: '600' }).waitFor();

    await auto.click();
    assert.equal(await auto.getAttribute('aria-pressed'), 'false');
    await page.clock.runFor(90_000);
    assert.equal(fetches, 6);
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

test('The header joins the drive indicator and Capture and Index into one combo button', async () => {
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 720 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.route('http://header-combo.test/**', route => {
      const url = new URL(route.request().url());
      const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
      if (url.pathname === '/assets/library.js') return route.fulfill({ contentType: 'application/javascript', body: fs.readFileSync(path.join(root, 'internal/archive/assets/library.js')) });
      if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/assets/')) return route.fulfill({ contentType: 'text/html', body: html });
      if (url.pathname === '/api/library/status') return json({ idle: true, portable: true, drive: { name: 'Pharos SSD', ejectable: true }, activities: [] });
      if (url.pathname === '/api/activity') return json({ active: 0, runs: [] });
      if (url.pathname.startsWith('/api/')) return json({ items: [] });
      return route.fulfill({ contentType: 'application/javascript', body: '' });
    });
    await page.goto('http://header-combo.test/library');
    const group = page.locator('body>header .header-icons > .combo-button');
    const drive = page.locator('#pharosDrive'), sync = page.locator('#headerSync');
    await sync.waitFor();
    assert.equal(await group.getAttribute('aria-label'), 'Library');
    assert.deepEqual(await group.locator('> button').evaluateAll(buttons => buttons.map(button => button.id)), ['pharosDrive', 'headerSync']);
    assert.equal(await sync.getAttribute('aria-busy'), 'false');

    // Header size: as tall as the other header icons, with the sync segment square.
    const [left, right, icon] = [await drive.boundingBox(), await sync.boundingBox(), await page.locator('#usageSummaryToggle').boundingBox()];
    assert.equal(left.height, icon.height);
    assert.equal(right.width, icon.width);
    assert.equal(right.x, left.x + left.width - 1, 'segments overlap by one pixel to share an edge');
    const radii = await page.evaluate(() => ['#pharosDrive', '#headerSync'].map(selector => {
      const style = getComputedStyle(document.querySelector(selector));
      return [style.borderTopLeftRadius, style.borderTopRightRadius, style.borderBottomRightRadius, style.borderBottomLeftRadius];
    }));
    assert.deepEqual(radii, [['4px', '0px', '0px', '4px'], ['0px', '4px', '4px', '0px']]);

    // Either segment, hovered, draws the shared edge in the masthead brass.
    const brass = await page.evaluate(() => { const probe = document.createElement('i'); probe.style.color = 'var(--mast-brass)'; document.body.append(probe); const color = getComputedStyle(probe).color; probe.remove(); return color; });
    for (const [segment, side] of [[drive, 'Right'], [sync, 'Left']]) {
      await segment.hover();
      const [id, color] = await page.evaluate(([x, y, side]) => { const button = document.elementFromPoint(x, y).closest('button'); return [button.id, getComputedStyle(button)[`border${side}Color`]]; }, [right.x + 0.5, right.y + right.height / 2, side]);
      assert.equal(id, await segment.getAttribute('id'));
      assert.equal(color, brass);
    }
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});
