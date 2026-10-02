const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');

const root = path.resolve(__dirname, '..');
const { chromium } = createRequire(path.join(root, '.context/browser-tests/package.json'))('playwright');
const source = fs.readFileSync(path.join(root, 'internal/archive/assets/ui.py'), 'utf8');
const html = source.slice(source.indexOf("r'''") + 4, source.lastIndexOf("'''"));

test('Sync now runs the incremental sync, shares busy state with a Stop in the drive panel, and exposes cadence, source selection, resume and exhaustive verification', async () => {
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1100 } });
    const errors = [];
    const requests = [];
    let active = true;
    let settings = { enabled: true, interval_seconds: 300, sources: [], next_at: '2026-10-01T19:00:00Z' };
    page.on('pageerror', error => { if ((error.stack || '').includes('/assets/sync.js')) errors.push(error.message); });
    await page.route('http://sync.test/**', route => {
      const url = new URL(route.request().url());
      const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
      if (url.pathname.startsWith('/assets/')) {
        const file = path.join(root, 'internal/archive/assets', path.basename(url.pathname));
        return route.fulfill({ contentType: file.endsWith('.css') ? 'text/css' : 'application/javascript', body: fs.existsSync(file) ? fs.readFileSync(file) : '' });
      }
      if (!url.pathname.startsWith('/api/')) return route.fulfill({ contentType: 'text/html', body: html });
      if (route.request().method() === 'POST') {
        const body = JSON.parse(route.request().postData() || '{}');
        requests.push({ path: url.pathname, body });
        if (url.pathname === '/api/sync/stop' || url.pathname === '/api/index/cancel') active = false;
        if (url.pathname === '/api/sync/settings') settings = { ...settings, ...body };
        return json({ accepted: true, stopped: true, run_id: 'manual', ...settings });
      }
      if (url.pathname === '/api/activity') return json({ runs: active ? [{ id: 'auto', kind: 'automatic-sync', state: 'running', phase: 'verifying', conversations: 2 }] : [] });
      if (url.pathname === '/api/sync/status') return json({
        settings, busy: active, pause_reason: active ? 'Sync operation running' : '',
        available_sources: [{ name: 'codex' }, { name: 'claude' }], sources: [{ name: 'codex' }, { name: 'claude' }],
        source_states: [{ source_name: 'codex', pending_preservation: true }], history: [], statistics: {},
        coverage: [{ source_name: 'codex', outcome: 'passed', units: 2 }], deferred: [{ name: 'tools' }],
        recovery_jobs: [{ id: 'repair', mode: 'full', state: 'interrupted' }], issues: [], measurement_limits: 'CPU is process-wide.'
      });
      if (url.pathname === '/api/sync/issues') return json({ issues: [] });
      if (url.pathname === '/api/upgrade') return json({ running: false, needed: false, steps: [], repository_renames: {} });
      if (url.pathname === '/api/library/status') return json({ portable: true, idle: !active, drive: { name: 'Replica SSD', mounted: true, ejectable: false }, activities: active ? [{ kind: 'sync', label: 'Automatic sync', stoppable: true }] : [] });
      if (url.pathname === '/api/sources') return json({ enabled: 2, configured: 2, items: [] });
      if (url.pathname === '/api/index' || url.pathname === '/api/capture') return json({ runs: [], hosts: [] });
      return json({ items: [], rows: [], metrics: [], counts: {}, setup: {}, usage: { running: false } });
    });
    await page.goto('http://sync.test/settings/sync');
    const dashboard = page.locator('#syncDashboard');
    await dashboard.getByText(/Live updates are searchable/).waitFor();
    const navigation = page.getByRole('navigation', { name: 'Settings pages' });
    assert.equal(await navigation.getByRole('link', { name: 'Sync', exact: true }).getAttribute('aria-current'), 'page');
    assert.equal(await page.locator('#health').isHidden(), true);
    assert.equal(await page.locator('#sources').isHidden(), true);
    await navigation.getByRole('link', { name: 'App preferences' }).click();
    await page.waitForURL('**/settings/preferences');
    assert.equal(await dashboard.isHidden(), true);
    await navigation.getByRole('link', { name: 'Sync', exact: true }).click();
    await page.waitForURL('**/settings/sync');
    await dashboard.getByText(/Live updates are searchable/).waitFor();
    await page.reload();
    await dashboard.getByText(/Live updates are searchable/).waitFor();
    assert.equal(await page.locator('#headerSync').isDisabled(), true);
    assert.equal(await page.locator('#headerSync').getAttribute('aria-busy'), 'true');
    assert.equal(await dashboard.getByRole('button', { name: 'Update library', exact: true }).isDisabled(), true);
    assert.match(await page.locator('#headerSync').getAttribute('aria-label'), /verifying a conversation/);
    assert.equal(await dashboard.getByRole('button', { name: 'Resume repair' }).isDisabled(), true);
    // Stop is in the drive panel; the header's combo button keeps only the
    // drive and Sync now, so Sync now keeps its rounded right side.
    const group = page.locator('body>header .header-icons > .combo-button');
    assert.deepEqual(await group.locator('> *').evaluateAll(items => items.map(item => item.id)), ['pharosDrive', 'headerSync']);
    assert.deepEqual(await page.evaluate(() => { const style = getComputedStyle(document.getElementById('headerSync')); return [style.borderTopRightRadius, style.borderBottomRightRadius]; }), ['4px', '4px']);
    await page.locator('#pharosDrive').click();
    await page.getByRole('dialog', { name: 'Library drive' }).getByRole('button', { name: 'Stop' }).click();
    await page.waitForFunction(() => !document.getElementById('headerSync').disabled);
    assert.equal(await page.locator('#headerSyncStop').count(), 0);
    const drivePanel = page.getByRole('dialog', { name: 'Library drive' });
    assert.equal(await drivePanel.locator('.sync-menu').count(), 0);
    assert.equal(await drivePanel.getByRole('button', { name: 'Full recapture & re-index…' }).count(), 0);
    assert.equal(await drivePanel.getByRole('button', { name: 'Rebuild indexes from retained messages…' }).count(), 0);
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('#headerSync').getAttribute('aria-label'), 'Sync now');
    await page.locator('#headerSync').click();
    await page.waitForFunction(() => !document.getElementById('headerSync').disabled);
    assert.deepEqual(requests.filter(request => ['/api/sync/check', '/api/library/update'].includes(request.path)).map(request => request.path), ['/api/sync/check']);
    await dashboard.getByRole('button', { name: 'Resume repair' }).click();
    assert.deepEqual(requests.find(request => request.path === '/api/sync/recovery').body, { resume: 'repair' });
    await dashboard.getByRole('button', { name: 'Verify all retained inputs' }).click();
    assert.deepEqual(requests.find(request => request.path === '/api/sync/verify').body, { all: true });
    const cadence = dashboard.getByLabel('Automatic refresh cadence');
    await cadence.selectOption('60');
    await page.waitForFunction(() => document.querySelector('#syncDashboard select').value === '60');
    const offSaved = page.waitForResponse(response => response.url().endsWith('/api/sync/settings') && response.request().method() === 'POST' && response.request().postDataJSON().enabled === false);
    await cadence.selectOption('0');
    await offSaved;
    assert.ok(requests.some(request => request.path === '/api/sync/settings' && request.body.enabled === false));
    const sourcesSaved = page.waitForResponse(response => response.url().endsWith('/api/sync/settings') && response.request().method() === 'POST' && response.request().postDataJSON().sources);
    await dashboard.getByLabel('Automatically refresh claude').uncheck();
    await sourcesSaved;
    assert.ok(requests.some(request => request.path === '/api/sync/settings' && JSON.stringify(request.body.sources) === '["codex"]'));
    for (const name of ['Library health', 'Sources', 'Optimization', 'App preferences']) {
      await navigation.getByRole('link', { name, exact: true }).click();
      assert.equal(await dashboard.isHidden(), true);
    }
    await navigation.getByRole('link', { name: 'Sync', exact: true }).click();
    await dashboard.getByText(/Live updates are searchable/).waitFor();
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

test('Sources links to Sync, where coordinated updates and separate capture/index share progress and busy state', async () => {
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1100 } });
    const errors = [];
    const requests = [];
    let captureRun = null;
    let indexRun = null;
    let updateRun = null;
    let automatic = false;
    let enabled = 2;
    let needsIndex = true;
    const running = () => automatic || [captureRun, indexRun, updateRun].some(run => run?.state === 'running');
    const run = (id, kind) => ({ id, kind, state: 'running', phase: 'indexing', current_source: 'codex', total_sources: 1, completed_sources: 0, conversations: 3, messages: 50, bytes_copied: 200 * 1048576, started_at: new Date().toISOString() });
    page.on('pageerror', error => { if (/\/assets\/(sync|library|onboarding)\.js/.test(error.stack || '')) errors.push(error.message); });
    await page.route('http://sync.test/**', route => {
      const url = new URL(route.request().url());
      const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
      if (url.pathname.startsWith('/assets/')) {
        const file = path.join(root, 'internal/archive/assets', path.basename(url.pathname));
        return route.fulfill({ contentType: file.endsWith('.css') ? 'text/css' : 'application/javascript', body: fs.existsSync(file) ? fs.readFileSync(file) : '' });
      }
      if (!url.pathname.startsWith('/api/')) return route.fulfill({ contentType: 'text/html', body: html });
      if (route.request().method() === 'POST') {
        const body = JSON.parse(route.request().postData() || '{}');
        requests.push({ path: url.pathname, body });
        if (url.pathname === '/api/library/update') { updateRun = run('update-1', 'index'); return json({ run_id: updateRun.id }); }
        if (url.pathname === '/api/capture') { captureRun = run('capture-1', 'capture'); return json({ run: captureRun }); }
        if (url.pathname === '/api/index') { indexRun = run('index-1', 'index'); return json({ run: indexRun }); }
        if (url.pathname === '/api/index/cancel') { indexRun = { ...indexRun, state: 'interrupted', stop_requested: true, completed_at: new Date().toISOString() }; return json({ stopped: true }); }
        return json({ accepted: true });
      }
      if (url.pathname === '/api/sources') return json({ enabled, configured: 2, items: [] });
      if (url.pathname === '/api/probe/status') return json({ library: true, needs_onboarding: false, host: { id: 'test-mac', label: 'This Mac' } });
      if (url.pathname === '/api/index') return json({ active: indexRun?.state === 'running', run: indexRun, hosts: [{ id: 'test-mac', current: true, label: 'This Mac', sources: [{ name: 'codex', needs_index: needsIndex }] }] });
      if (url.pathname === '/api/capture') return json({ active: captureRun?.state === 'running', run: captureRun, runs: captureRun ? [captureRun] : [], host: { label: 'This Mac' } });
      if (url.pathname === '/api/activity') return json({ runs: [...[indexRun, updateRun].filter(Boolean), ...(automatic ? [{ id: 'auto', kind: 'automatic-sync', state: 'running', phase: 'verifying' }] : [])] });
      if (url.pathname === '/api/library/status') return json({ portable: true, idle: !running(), drive: { name: 'Replica SSD', mounted: true, ejectable: false }, activities: running() ? [{ kind: captureRun?.state === 'running' ? 'capture' : 'index', label: 'Syncing the library', stoppable: true }] : [] });
      if (url.pathname === '/api/sync/status') return json({ settings: { enabled: false, interval_seconds: 300, sources: [] }, busy: running(), available_sources: [{ name: 'codex' }, { name: 'claude' }], history: [], statistics: {}, source_states: [], coverage: [], deferred: [], recovery_jobs: [], issues: [] });
      if (url.pathname === '/api/sync/issues') return json({ issues: [] });
      if (url.pathname === '/api/upgrade') return json({ running: false, needed: false, steps: [], repository_renames: {} });
      return json({ items: [], rows: [], metrics: [], counts: {}, setup: {}, usage: { running: false } });
    });
    await page.goto('http://sync.test/settings/sources');
    const sources = page.locator('#sources');
    await sources.getByRole('button', { name: 'Find sources on this Mac…' }).waitFor();
    assert.equal(await sources.getByRole('button', { name: 'Capture this Mac' }).count(), 0);
    assert.equal(await sources.getByRole('button', { name: 'Index captured files' }).count(), 0);
    assert.equal(await sources.getByRole('button', { name: 'Update library', exact: true }).count(), 0);
    await sources.getByRole('link', { name: 'Open Sync →' }).click();
    await page.waitForURL('**/settings/sync');
    const operations = page.getByRole('region', { name: 'One-off sync', exact: true });
    const update = operations.getByRole('button', { name: 'Update library', exact: true });
    await update.waitFor();
    const capture = operations.locator('#pharosCaptureHost');
    const index = operations.getByRole('button', { name: 'Index captured files' });
    assert.equal(await capture.isVisible(), false);
    assert.equal(await index.isVisible(), false);
    await operations.locator('summary').click();
    assert.equal(await capture.isEnabled(), true);
    assert.equal(await index.isEnabled(), true);
    await update.focus();
    await page.evaluate(() => window.pharosSync.refresh());
    assert.equal(await operations.locator('details').evaluate(element => element.open), true);
    assert.equal(await update.evaluate(element => element === document.activeElement), true);
    await operations.screenshot({ path: path.join(root, '.context/sync-one-off.png') });

    await update.click();
    await page.waitForFunction(() => document.getElementById('pharosUpdateLibrary').getAttribute('aria-busy') === 'true');
    await operations.getByText('Updating library', { exact: true }).waitFor();
    assert.equal(await operations.getByText('Indexing conversations', { exact: true }).isVisible(), true);
    assert.equal(await capture.isDisabled(), true);
    assert.equal(await index.isDisabled(), true);
    assert.equal(await page.locator('#headerSync').isDisabled(), true);
    assert.deepEqual(requests.find(request => request.path === '/api/library/update').body, {});
    updateRun = { ...updateRun, state: 'complete', completed_at: new Date().toISOString() };
    await page.waitForFunction(() => !document.getElementById('pharosUpdateLibrary').disabled);

    await capture.click();
    await operations.getByText('Capturing This Mac', { exact: true }).waitFor();
    assert.equal(await update.isDisabled(), true);
    assert.equal(await index.isDisabled(), true);
    assert.deepEqual(requests.find(request => request.path === '/api/capture').body, {});
    captureRun = { ...captureRun, state: 'complete', completed_sources: 1, completed_at: new Date().toISOString() };
    await page.waitForFunction(() => !document.getElementById('pharosCaptureHost').disabled);

    await index.click();
    await operations.getByText('Indexing captures', { exact: true }).waitFor();
    assert.equal(await update.isDisabled(), true);
    assert.equal(await capture.isDisabled(), true);
    assert.deepEqual(requests.find(request => request.path === '/api/index').body, { all_hosts: true, only_needed: true });
    await operations.getByRole('button', { name: 'Stop', exact: true }).click();
    await operations.getByText(/You stopped the last index/).waitFor();
    await page.waitForFunction(() => !document.getElementById('pharosUpdateLibrary').disabled);
    assert.ok(requests.some(request => request.path === '/api/index/cancel'));

    automatic = true;
    await page.evaluate(() => window.pharosSync.refresh());
    for (const action of [update, capture, index]) assert.equal(await action.isDisabled(), true);
    automatic = false;
    enabled = 0;
    await page.evaluate(() => window.pharosLibrary.refreshSettings());
    await page.evaluate(() => window.pharosSync.refresh());
    assert.equal(await update.isDisabled(), true);
    assert.equal(await capture.isDisabled(), true);
    assert.equal(await index.isEnabled(), true);
    needsIndex = false;
    await page.evaluate(() => window.pharosLibrary.refreshSettings());
    assert.equal(await index.isDisabled(), true);
    await operations.getByRole('link', { name: 'Manage sources →' }).click();
    await page.waitForURL('**/settings/sources');
    assert.equal(await operations.isHidden(), true);
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

test('Sync prioritizes readable history and source counts, with persistent optional diagnostics', async () => {
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1100 } });
    const errors = [];
    const reference = new Date('2026-10-01T18:00:00Z').getTime();
    await page.addInitScript(value => { Date.now = () => value; }, reference);
    page.on('pageerror', error => { if ((error.stack || '').includes('/assets/sync.js')) errors.push(error.message); });
    let history = Array.from({ length: 12 }, (_, index) => ({
      run_id: `run-${index}`, finished_at: new Date(reference - (index + 1) * 120000).toISOString(),
      state: 'complete', class: 'changes', changed_groups: 2, conversations: 12, messages_in_changed_groups: 340,
      seconds: 22.123456789, cpu_seconds: 8.7123456789, cpu_percent_one_core: 39.38123456789,
      rss_baseline_bytes: 100 * 1048576, rss_observed_peak_bytes: 129 * 1048576, allocated_bytes: 50 * 1048576,
      sources: ['codex', 'claude'], source_results: [
        { source: 'codex', workspaces: 1, conversations: 8, messages: 130 },
        { source: 'claude', workspaces: 1, conversations: 4, messages: 210 }
      ],
      phases: [{ name: 'codex', seconds: 10.7123456789, cpu_seconds: 4.9123456789 }, { name: 'claude', seconds: 9.0123456789, cpu_seconds: 3.0123456789 }, { name: 'audit', seconds: 2.4, cpu_seconds: 0.8 }],
      audit: { outcome: 'passed', discovered_units: 10, deferred_units: 1 }
    }));
    history[1] = { ...history[1], class: 'no_changes', changed_groups: 0, conversations: 0, messages_in_changed_groups: 0, seconds: 0.23123456789, cpu_seconds: 0, source_results: [{ source: 'codex', conversations: 0, messages: 0 }, { source: 'claude', conversations: 0, messages: 0 }] };
    history[2] = { ...history[2], state: 'failed', seconds: 61.8123456789, conversations: 2, messages_in_changed_groups: 14, sources: ['codex'], source_results: [{ source: 'codex', conversations: 2, messages: 14, error: 'Source unavailable' }] };
    history[3] = { ...history[3], sources: undefined, source_results: undefined, seconds: 3661.2, phases: [{ name: 'codex', seconds: 3650, cpu_seconds: 4 }, { name: 'projections', seconds: 10, cpu_seconds: 1 }, { name: 'audit', seconds: 1, cpu_seconds: 0 }] };
    history[4] = { ...history[4], cpu_seconds: undefined, rss_observed_peak_bytes: undefined, audit: undefined };
    history[5] = { ...history[5], state: 'interrupted', audit: { outcome: 'deferred' } };
    const settings = { enabled: true, interval_seconds: 300, sources: [], next_at: new Date(reference + 180000).toISOString() };
    await page.route('http://sync.test/**', route => {
      const url = new URL(route.request().url());
      const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
      if (url.pathname.startsWith('/assets/')) {
        const file = path.join(root, 'internal/archive/assets', path.basename(url.pathname));
        return route.fulfill({ contentType: file.endsWith('.css') ? 'text/css' : 'application/javascript', body: fs.existsSync(file) ? fs.readFileSync(file) : '' });
      }
      if (!url.pathname.startsWith('/api/')) return route.fulfill({ contentType: 'text/html', body: html });
      if (url.pathname === '/api/activity') return json({ runs: [] });
      if (url.pathname === '/api/sync/status') return json({
        settings, busy: false, available_sources: [{ name: 'codex' }, { name: 'claude' }], history,
        source_states: [
          { source_name: 'codex', attempted_at: history[0]?.finished_at, succeeded_at: history[0]?.finished_at, pending_preservation: true, last_capture_at: '2026-10-01T15:00:00Z' },
          { source_name: 'claude', attempted_at: history[0]?.finished_at, succeeded_at: history[0]?.finished_at, last_capture_at: '2026-10-01T17:00:00Z' }
        ],
        statistics: { changes: { samples: 10, elapsed_seconds: { median: 22.12345678, p95: 61.8 }, cpu_seconds: { median: 8.712345678, p95: 11 }, rss_observed_peak_bytes: { last: 129 * 1048576 }, estimated_cpu_duty_percent_one_core: 2.9123456789 } },
        coverage: [{ source_name: 'codex', outcome: 'passed', units: 2, last_verification: history[0]?.finished_at }],
        deferred: [{ name: 'tools', changed_at: history[0]?.finished_at }], recovery_jobs: [], issues: [], measurement_limits: 'CPU is process-wide.'
      });
      if (url.pathname === '/api/sync/issues') return json({ issues: [] });
      if (url.pathname === '/api/upgrade') return json({ running: false, needed: false, steps: [], repository_renames: {} });
      if (url.pathname === '/api/library/status') return json({ portable: true, idle: true, drive: { name: 'Replica SSD', mounted: true, ejectable: false }, activities: [] });
      if (url.pathname === '/api/sources') return json({ enabled: 2, configured: 2, items: [] });
      if (url.pathname === '/api/index' || url.pathname === '/api/capture') return json({ runs: [], hosts: [] });
      return json({ items: [], rows: [], metrics: [], counts: {}, setup: {}, usage: { running: false } });
    });
    await page.goto('http://sync.test/settings/sync');
    const dashboard = page.locator('#syncDashboard');
    const recent = dashboard.getByRole('region', { name: 'Recent syncs', exact: true });
    await recent.getByRole('heading', { name: 'Recent syncs' }).waitFor();
    assert.equal(await dashboard.locator('section').first().getAttribute('aria-label'), 'Recent syncs');
    assert.deepEqual(await recent.locator('.sync-metric strong').allTextContents(), ['12', '22s', '9s', '129 MiB']);
    const rows = recent.getByRole('table', { name: 'Recent sync history' }).locator('tbody > tr:not([hidden])');
    assert.equal(await rows.count(), 5);
    const first = rows.first();
    assert.match(await first.innerText(), /2m ago/);
    assert.equal(await first.locator('time').getAttribute('title'), new Date(history[0].finished_at).toLocaleString());
    assert.match(await first.innerText(), /codex · 8 conv\./);
    assert.match(await first.innerText(), /claude · 4 conv\./);
    assert.match(await rows.nth(1).innerText(), /No changes/);
    assert.match(await rows.nth(1).innerText(), /<1s/);
    assert.match(await rows.nth(2).innerText(), /Failed/);
    assert.match(await rows.nth(2).innerText(), /1m 2s/);
    assert.match(await rows.nth(3).innerText(), /1h 1m/);
    assert.equal(await rows.nth(3).locator('.sync-source-list').innerText(), 'codex');
    assert.match(await rows.nth(4).innerText(), /—/);
    assert.ok(!(await recent.innerText()).includes('12345678'));
    assert.equal(await dashboard.getByRole('table', { name: 'Measured refresh cost' }).isVisible(), false);
    assert.equal(await dashboard.getByRole('button', { name: 'Verify all retained inputs' }).isVisible(), false);
    assert.match(await dashboard.innerText(), /In 3m/);
    await page.screenshot({ path: path.join(root, '.context/sync-desktop.png') });

    await first.getByRole('button', { name: 'Details for sync 2m ago' }).click();
    await recent.getByRole('table', { name: 'Source breakdown' }).waitFor();
    assert.match(await recent.getByRole('table', { name: 'Source breakdown' }).innerText(), /130/);
    assert.match(await recent.getByRole('table', { name: 'Source breakdown' }).innerText(), /210/);
    await dashboard.locator('summary').filter({ hasText: 'Performance & verification details' }).click();
    await page.evaluate(() => window.pharosSync.refresh());
    assert.equal(await recent.getByRole('table', { name: 'Source breakdown' }).isVisible(), true);
    assert.equal(await dashboard.getByRole('table', { name: 'Measured refresh cost' }).isVisible(), true);
    await dashboard.locator('summary').filter({ hasText: 'Performance & verification details' }).click();
    await recent.getByRole('button', { name: 'Details for sync 2m ago' }).click();
    await rows.nth(3).getByRole('button', { name: 'Details for sync 8m ago' }).click();
    assert.equal(await recent.getByText('Per-source counts were not recorded for this older run.').isVisible(), true);
    await recent.getByRole('button', { name: 'Details for sync 8m ago' }).click();
    await recent.getByRole('button', { name: 'Show all 12 runs' }).click();
    assert.equal(await rows.count(), 12);
    assert.match(await rows.nth(5).innerText(), /Stopped/);
    await recent.getByRole('button', { name: 'Show latest 5' }).click();
    assert.equal(await rows.count(), 5);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.locator('main').evaluate(element => { element.scrollTop = 0; });
    await page.screenshot({ path: path.join(root, '.context/sync-mobile.png') });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true);
    const region = recent.getByRole('region', { name: 'Recent sync history', exact: true });
    assert.equal(await region.evaluate(element => element.scrollWidth > element.clientWidth), true);
    await region.focus();
    const scrollPosition = await region.evaluate(element => { element.scrollLeft = 250; return element.scrollLeft; });
    await page.evaluate(() => window.pharosSync.refresh());
    assert.equal(await region.evaluate(element => element.scrollLeft), scrollPosition);
    assert.equal(await region.evaluate(element => element === document.activeElement), true);
    history = [];
    await page.evaluate(() => { document.activeElement.blur(); return window.pharosSync.refresh(); });
    await recent.getByText('No automatic refreshes yet. Use Check now to refresh your selected sources.').waitFor();
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});
