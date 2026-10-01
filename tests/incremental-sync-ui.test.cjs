const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');

const root = path.resolve(__dirname, '..');
const { chromium } = createRequire(path.join(root, '.context/browser-tests/package.json'))('playwright');
const source = fs.readFileSync(path.join(root, 'internal/archive/assets/ui.py'), 'utf8');
const html = source.slice(source.indexOf("r'''") + 4, source.lastIndexOf("'''"));

test('Sync shares busy/Stop state and exposes cadence, source selection, resume and exhaustive verification', async () => {
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
        if (url.pathname === '/api/sync/stop') active = false;
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
    await dashboard.getByText(/Indexed after last capture/).waitFor();
    const navigation = page.getByRole('navigation', { name: 'Settings pages' });
    assert.equal(await navigation.getByRole('link', { name: 'Sync', exact: true }).getAttribute('aria-current'), 'page');
    assert.equal(await page.locator('#health').isHidden(), true);
    assert.equal(await page.locator('#sources').isHidden(), true);
    await navigation.getByRole('link', { name: 'App preferences' }).click();
    await page.waitForURL('**/settings/preferences');
    assert.equal(await dashboard.isHidden(), true);
    await navigation.getByRole('link', { name: 'Sync', exact: true }).click();
    await page.waitForURL('**/settings/sync');
    await dashboard.getByText(/Indexed after last capture/).waitFor();
    await page.reload();
    await dashboard.getByText(/Indexed after last capture/).waitFor();
    assert.equal(await page.locator('#headerSync').isDisabled(), true);
    assert.equal(await page.locator('#headerSync').getAttribute('aria-busy'), 'true');
    assert.match(await page.locator('#headerSync').getAttribute('aria-label'), /verifying a conversation/);
    assert.equal(await dashboard.getByRole('button', { name: 'Resume repair' }).isDisabled(), true);
    await page.locator('#headerSyncStop').click();
    await page.waitForFunction(() => !document.getElementById('headerSync').disabled);
    assert.equal(await page.locator('#headerSyncStop').isHidden(), true);
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
    await dashboard.getByText(/Indexed after last capture/).waitFor();
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});
