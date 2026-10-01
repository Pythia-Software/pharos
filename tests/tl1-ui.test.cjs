const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');

const root = path.resolve(__dirname, '..');
const { chromium } = createRequire(path.join(root, '.context/browser-tests/package.json'))('playwright');
const source = fs.readFileSync(path.join(root, 'internal/archive/assets/ui.py'), 'utf8');
const html = source.slice(source.indexOf("r'''") + 4, source.lastIndexOf("'''"));

test('TL1 pages show follow-up outcomes and omit redundant configuration chips', async () => {
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    const followup = { from_flavor: 'implement', from_configuration: 'codex-high', from_model: 'gpt-6', next_flavor: 'agent-review', next_configuration: 'claude-medium', next_model: 'claude-sonnet', next_class: 'llm', next_outcome: 'findings', paths: 4, with_findings: 3, findings: 5 };
    const overview = {
      project: 'example', installations: [{ host_label: 'Mac', synced_at: '2026-09-30T12:00:00Z' }], enqueues: [], window: { tasks: 14, total_tasks: 14, label: 'all time' },
      coverage: { transcript_share: 1, cost_share: 1 }, totals: { cost_usd: 42, llm_attempts: 14, procedural_attempts: 0, candidates: 4, finished_candidates: 3, llm_error_rate: 0, agent_error_rate: 0, human_tasks: 4, pending_human: 0, median_steps: 3, median_llm_duration_ms: 12000, cache_read_share: 0.5, tokens: { total_tokens: 1000 } },
      matrix: [{ flavor: 'implement', configuration: 'codex-high', attempts: 14, advanced: 12, advance_rate: 0.86, escalation_rate: 0.14, agent_error_rate: 0, median_cost_usd: 2, cost_per_advance_usd: 3, median_duration_ms: 12000, median_tokens: 1000, cache_read_share: 0.5, default: true, low_sample: true, flag: 'worst_value' }, { flavor: 'review', configuration: 'claude-medium', attempts: 14, advanced: 14, advance_rate: 1, escalation_rate: 0, agent_error_rate: 0, median_cost_usd: 1, cost_per_advance_usd: 1, median_duration_ms: 10000, median_tokens: 500, cache_read_share: 0.4, flag: 'best_value' }],
      flavors: [], graph: { nodes: [], edges: [] }, errors: [], human: {}, reviews: {}, candidates: {},
      followups: { next: [followup], after: [{ ...followup, after_flavor: 'human-review', after_class: 'human', after_outcome: 'remanded', paths: 2, with_findings: 2, findings: 3 }] },
    };
    await page.route('http://tl1-ui.test/**', route => {
      const url = new URL(route.request().url());
      if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/assets/')) return route.fulfill({ contentType: 'text/html', body: html });
      if (url.pathname === '/assets/query-tables.js') return route.fulfill({ contentType: 'application/javascript', body: fs.readFileSync(path.join(root, 'internal/archive/assets/query-tables.js')) });
      if (url.pathname === '/assets/query-tables.css') return route.fulfill({ contentType: 'text/css', body: fs.readFileSync(path.join(root, 'internal/archive/assets/query-tables.css')) });
      const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
      if (url.pathname === '/api/tl1') return json({ projects: [{ project: 'example', tasks: 14, installations: [{ installation_id: 'mac', host_label: 'Mac', tasks: 14 }] }] });
      if (url.pathname === '/api/tl1/overview') return json(overview);
      if (url.pathname === '/api/tools/status') return json({ backfill: { running: false, done: 0, total: 0 }, pending_conversations: 0, conversations: 0 });
      if (url.pathname.startsWith('/api/query/')) return json({ rows: [], total: 0, metrics: [] });
      if (url.pathname.startsWith('/api/')) return json({});
      return route.fulfill({ status: 404 });
    });

    await page.goto('http://tl1-ui.test/tl1/configurations');
    await page.locator('#tl1-matrix .tl1-table tbody tr').first().waitFor();
    assert.equal(await page.locator('#tl1-matrix .tl1-chip').count(), 0);
    assert.equal(await page.locator('.tl1-nav a[aria-current="page"]').innerText(), 'Configurations');
    await page.screenshot({ path: path.join(root, '.context/tl1-configurations.png'), fullPage: true });
    await page.getByRole('link', { name: 'Follow-up outcomes' }).click();
    await page.locator('#tl1-followup-next tbody tr').waitFor();
    assert.equal(new URL(page.url()).pathname, '/tl1/followups');
    assert.match(await page.locator('#tl1-followup-next').innerText(), /claude-sonnet[\s\S]*findings[\s\S]*3 \/ 4/);
    assert.match(await page.locator('#tl1-followup-after').innerText(), /human-review[\s\S]*remanded/);
    assert.equal(await page.locator('.tl1-nav a[aria-current="page"]').innerText(), 'Follow-up outcomes');
    await page.screenshot({ path: path.join(root, '.context/tl1-followups.png'), fullPage: true });
    await page.setViewportSize({ width: 760, height: 900 });
    await page.screenshot({ path: path.join(root, '.context/tl1-followups-mobile.png'), fullPage: true });
    assert.equal(await page.locator('.tl1-nav a[aria-current="page"]').isVisible(), true);
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.reload();
    await page.locator('#tl1-followup-next tbody tr').waitFor();
    assert.equal(await page.locator('.tl1-nav a[aria-current="page"]').innerText(), 'Follow-up outcomes');
    await page.getByRole('link', { name: 'Runs', exact: true }).click();
    assert.equal(new URL(page.url()).pathname, '/tl1/runs');
    await page.locator('#tl1-runs').waitFor();
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});
