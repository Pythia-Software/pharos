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

test('The header joins the drive indicator and Sync now into one combo button', async () => {
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

test('Running now shows historical completion estimates without disconnect controls', async () => {
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const started = new Date('2026-10-01T16:00:00Z');
    await page.clock.install({ time: new Date(started.getTime() + 120_000) });
    let activities = [{ kind: 'index', label: 'Indexing captures', detail: '120 of 600 conversations', progress: 0.2,
      started_at: started.toISOString(), estimated_completion_at: new Date(started.getTime() + 420_000).toISOString() }];
    await page.route('http://drive-estimate.test/**', route => {
      const url = new URL(route.request().url());
      const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
      if (url.pathname === '/assets/library.js') return route.fulfill({ contentType: 'application/javascript', body: fs.readFileSync(path.join(root, 'internal/archive/assets/library.js')) });
      if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/assets/')) return route.fulfill({ contentType: 'text/html', body: html });
      if (url.pathname === '/api/library/status') return json({ idle: !activities.length, portable: true, library_dir: '/Volumes/Pharos SSD/Pharos',
        drive: { name: 'Pharos SSD', location: 'external', ejectable: true }, activities,
        unplug: { action: 'eject', summary: 'Disconnecting advice must not appear.' } });
      if (url.pathname === '/api/health/drive') return json({ free_bytes: 400_000_000_000, sizes: { library_bytes: 12_000_000_000 } });
      if (url.pathname === '/api/activity') return json({ active: 1, runs: [] });
      if (url.pathname.startsWith('/api/')) return json({ items: [] });
      return route.fulfill({ contentType: 'application/javascript', body: '' });
    });
    await page.goto('http://drive-estimate.test/library');
    await page.locator('#pharosDrive').click();
    const panel = page.getByRole('dialog', { name: 'Library drive' });
    await panel.getByText('Started 2m ago', { exact: true }).waitFor();
    await panel.getByText('Estimated complete in 5m', { exact: true }).waitFor();
    const info = await panel.locator('.pharos-activity-info').boundingBox();
    const timing = await panel.locator('.pharos-activity-timing').boundingBox();
    assert.ok(timing.x >= info.x + info.width, 'timing sits to the right of the activity');
    assert.ok(Math.abs(timing.y - info.y) < 4, 'timing aligns with the activity title');
    assert.doesNotMatch(await panel.textContent(), /Disconnecting|Eject|diskutil/);
    assert.equal(await panel.locator('#pharosEject').count(), 0);
    if (process.env.PHAROS_UI_SCREENSHOTS) {
      fs.mkdirSync(process.env.PHAROS_UI_SCREENSHOTS, { recursive: true });
      await panel.screenshot({ path: path.join(process.env.PHAROS_UI_SCREENSHOTS, 'drive-popover-estimate.png') });
    }
    await page.setViewportSize({ width: 320, height: 900 });
    assert.equal(await panel.evaluate(element => element.scrollWidth <= element.clientWidth), true, 'timing fits a narrow popover');
    await page.setViewportSize({ width: 1280, height: 900 });
    activities = [{ kind: 'backup', label: 'Backing up', started_at: started.toISOString(), estimated_completion_at: new Date(started.getTime() + 420_000).toISOString() }];
    await page.clock.runFor(61_000);
    await panel.getByText('Started 3m ago', { exact: true }).waitFor();
    await panel.getByText('Estimated complete in 4m', { exact: true }).waitFor();
    activities = [{ kind: 'maintenance', label: 'Updating findings' }];
    await page.clock.runFor(2100);
    await panel.getByText('Started unknown', { exact: true }).waitFor();
    await panel.getByText('Estimated complete unknown', { exact: true }).waitFor();
    activities = [{ kind: 'index', label: 'Indexing captures', estimated_completion_at: started.toISOString() }];
    await page.clock.runFor(2100);
    await panel.getByText('Estimated complete overdue', { exact: true }).waitFor();
    activities = [{ kind: 'index', label: 'Indexing captures', estimated_completion_at: 'invalid' }];
    await page.clock.runFor(2100);
    await panel.getByText('Estimated complete unknown', { exact: true }).waitFor();
    activities = [];
    await page.clock.runFor(2100);
    await panel.getByText('Nothing is running.').waitFor();
    assert.doesNotMatch(await panel.textContent(), /Estimated complete|Started|Disconnecting|Eject/);
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

test('Update library starts a pending upgrade through the shared action', async () => {
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 720 } });
    const errors = [];
    let running = false;
    let starts = 0;
    let statusReads = 0;
    page.on('pageerror', error => errors.push(error.message));
    await page.clock.install();
    await page.route('http://shared-update.test/**', route => {
      const url = new URL(route.request().url());
      const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
      if (url.pathname === '/assets/library.js' || url.pathname === '/assets/upgrade.js') {
        return route.fulfill({ contentType: 'application/javascript', body: fs.readFileSync(path.join(root, 'internal/archive/assets', path.basename(url.pathname))) });
      }
      if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/assets/')) return route.fulfill({ contentType: 'text/html', body: html });
      if (url.pathname === '/api/upgrade') {
        if (route.request().method() === 'POST') { running = true; starts++; return json({ started: true }); }
        statusReads++;
        return json({ needed: true, running, step: running ? 'usage' : null, done: 2, total: 10, overall_progress: running ? 0.1 : null,
          steps: [{ id: 'usage', label: 'Correct token attribution', detail: 'Recounting', unit: 'workspace', pending: 10 }], repository_renames: {} });
      }
      if (url.pathname === '/api/library/status') return json({ idle: !running, portable: true, drive: { name: 'Pharos SSD', ejectable: false },
        activities: running ? [{ kind: 'maintenance', label: 'Upgrading the library', detail: 'Step: usage', progress: 0.2, writes: true }] : [] });
      if (url.pathname === '/api/activity') return json({ active: 0, runs: [] });
      if (url.pathname === '/api/sources') return json({ enabled: 0, items: [] });
      if (url.pathname.startsWith('/api/')) return json({ items: [] });
      return route.fulfill({ contentType: 'application/javascript', body: '' });
    });
    await page.goto('http://shared-update.test/library');
    await page.locator('#headerSync').waitFor();
    await page.clock.runFor(31_000);
    assert.equal(statusReads, 1, 'a pending upgrade does not keep polling every open tab');
    const upgradeStarted = page.waitForResponse(response => response.url().endsWith('/api/upgrade') && response.request().method() === 'POST');
    await page.locator('#pharosDrive').click();
    const panel = page.getByRole('dialog', { name: 'Library drive' });
    await panel.getByText('Use Update library to run it').waitFor();
    await panel.getByRole('button', { name: 'Update library' }).click();
    await upgradeStarted;
    await page.waitForFunction(() => document.querySelector('#headerSync')?.disabled);
    assert.equal(starts, 1);
    assert.equal(await page.locator('#upgradeToggle').count(), 0);
    await panel.getByText('Correct token attribution').waitFor();
    assert.equal(await panel.locator('.pharos-activity').count(), 1);
    assert.equal(await panel.locator('.pharos-bar').count(), 1);
    await panel.getByRole('button', { name: 'Update details' }).click();
    const details = page.getByRole('dialog', { name: 'Library update details' });
    await details.getByText('Correct token attribution').waitFor();
    assert.equal(await details.getByRole('button', { name: 'Upgrade now' }).count(), 0);
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

test('standalone maintenance stays separate from a library update', async () => {
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 720 } });
    const errors = [];
    let recentRun = null;
    let activities = [
      { kind: 'maintenance', label: 'Building substring search', detail: '50 of 100 conversations', progress: 0.5 },
      { kind: 'maintenance', label: 'Building the Tools ledger', detail: '10 of 20 conversations', progress: 0.5 },
      { kind: 'maintenance', label: 'Updating the Library view', detail: '5 workspaces to refresh', progress: null },
      { kind: 'git', label: 'Looking up merges in Git', detail: 'first scan of this catalog', progress: null },
    ];
    page.on('pageerror', error => errors.push(error.message));
    await page.route('http://activity-grouping.test/**', route => {
      const url = new URL(route.request().url());
      const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
      if (url.pathname === '/assets/library.js' || url.pathname === '/assets/upgrade.js') {
        return route.fulfill({ contentType: 'application/javascript', body: fs.readFileSync(path.join(root, 'internal/archive/assets', path.basename(url.pathname))) });
      }
      if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/assets/')) return route.fulfill({ contentType: 'text/html', body: html });
      if (url.pathname === '/api/library/status') return json({ idle: false, portable: true, drive: { name: 'Pharos SSD', ejectable: false }, activities });
      if (url.pathname === '/api/activity') return json({ active: 0, runs: recentRun ? [recentRun] : [] });
      if (url.pathname === '/api/upgrade') return json({ needed: false, running: false, steps: [], repository_renames: {} });
      if (url.pathname.startsWith('/api/')) return json({ items: [] });
      return route.fulfill({ contentType: 'application/javascript', body: '' });
    });
    await page.goto('http://activity-grouping.test/library');
    await page.locator('#pharosDrive').click();
    const panel = page.getByRole('dialog', { name: 'Library drive' });
    await panel.getByText('Building substring search').waitFor();
    assert.equal(await panel.locator('.pharos-activity').count(), 4);
    assert.equal(await panel.getByText('Updating library', { exact: true }).count(), 0);
    assert.equal(await panel.locator('.pharos-activity', { hasText: 'Building substring search' }).locator('.pharos-bar').count(), 1);
    assert.match(await page.locator('#pharosDrive').getAttribute('title'), /Building substring search 50%/);

    const started = new Date(Date.now() - 10_000).toISOString();
    recentRun = { id: 'recent-index', kind: 'capture-index', state: 'complete', started_at: started, completed_at: new Date().toISOString() };
    activities = [
      { kind: 'git', label: 'Looking up merges in Git', detail: 'Main-branch merges of indexed work, after the last index', started_at: started, progress: null },
      { kind: 'maintenance', label: 'Building substring search', detail: '50 of 100 conversations', progress: 0.5 },
    ];
    await page.evaluate(() => window.pharosLibrary.refresh());
    await panel.getByText('Finishing the library update').waitFor();
    assert.equal(await panel.locator('.pharos-activity').count(), 2);
    assert.equal(await panel.getByText('Building substring search').count(), 1);
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

test('the update action stays busy while a completed index hands off to upgrade', async () => {
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1280, height: 720 } });
    const errors = [];
    let upgradeRunning = false;
    let indexStarts = 0;
    const startedAt = new Date().toISOString();
    page.on('pageerror', error => errors.push(error.message));
    await page.route('http://update-handoff.test/**', route => {
      const url = new URL(route.request().url());
      const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
      if (url.pathname === '/assets/library.js' || url.pathname === '/assets/upgrade.js') {
        return route.fulfill({ contentType: 'application/javascript', body: fs.readFileSync(path.join(root, 'internal/archive/assets', path.basename(url.pathname))) });
      }
      if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/assets/')) return route.fulfill({ contentType: 'text/html', body: html });
      if (url.pathname === '/api/upgrade') {
        if (route.request().method() === 'POST') setTimeout(() => { upgradeRunning = true; }, 900);
        return json({ needed: true, running: upgradeRunning, step: upgradeRunning ? 'usage' : null,
        done: 1, total: 10, overall_progress: upgradeRunning ? 0.05 : null,
        steps: [{ id: 'usage', label: 'Correct token attribution', detail: 'Recounting', unit: 'workspace', pending: 10 }], repository_renames: {} });
      }
      if (url.pathname === '/api/library/status') return json({ idle: !upgradeRunning, portable: true, drive: { name: 'Pharos SSD', ejectable: false },
        activities: upgradeRunning ? [{ kind: 'maintenance', label: 'Upgrading the library', detail: 'Step: usage', progress: 0.1 }] : [] });
      if (url.pathname === '/api/sources') return json({ enabled: 1, items: [] });
      if (url.pathname === '/api/library/update') {
        indexStarts++;
        return json({ run_id: 'index' });
      }
      if (url.pathname === '/api/capture') return json(route.request().method() === 'POST'
        ? { run: { id: 'capture', state: 'running' } } : { runs: [{ id: 'capture', state: 'complete' }] });
      if (url.pathname === '/api/index') {
        if (route.request().method() === 'POST') {
          indexStarts++;
          setTimeout(() => { upgradeRunning = true; }, 900);
          return json({ run: { id: 'index', state: 'running' } });
        }
        return json({ active: false, runs: [], hosts: [] });
      }
      if (url.pathname === '/api/activity') return json({ active: 0, runs: indexStarts
        ? [{ id: 'index', kind: 'capture-index', state: 'complete', started_at: startedAt, completed_at: new Date().toISOString(), results: [] }] : [] });
      if (url.pathname.startsWith('/api/')) return json({ items: [] });
      return route.fulfill({ contentType: 'application/javascript', body: '' });
    });
    await page.goto('http://update-handoff.test/library');
    const indexStarted = page.waitForResponse(response => response.url().endsWith('/api/library/update') && response.request().method() === 'POST');
    await page.locator('#pharosDrive').click();
    const panel = page.getByRole('dialog', { name: 'Library drive' });
    await panel.getByRole('button', { name: 'Update library' }).click();
    await indexStarted;
    await panel.getByText('Preparing the library upgrade…').waitFor();
    assert.equal(await page.locator('#headerSync').isDisabled(), true);
    await panel.getByText('Correct token attribution').waitFor({ timeout: 5000 });
    assert.equal(indexStarts, 1);
    assert.equal(await page.locator('#headerSync').isDisabled(), true);
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});
