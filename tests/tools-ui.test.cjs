const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');

const root = path.resolve(__dirname, '..');
const { chromium } = createRequire(path.join(root, '.context/browser-tests/package.json'))('playwright');
const source = fs.readFileSync(path.join(root, 'internal/archive/assets/ui.py'), 'utf8');
const html = source.slice(source.indexOf("r'''") + 4, source.lastIndexOf("'''"));

test('Tools opens individual calls and aggregates their volume on demand', async () => {
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
    const page = await browser.newPage();
    const requests = [];
    await page.route('http://tools-ui.test/**', route => {
      const url = new URL(route.request().url());
      const respond = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
      if (url.pathname === '/tools') return route.fulfill({ contentType: 'text/html', body: html });
      if (url.pathname === '/assets/query-tables.js' || url.pathname === '/assets/query-tables.css') {
        const name = path.basename(url.pathname);
        return route.fulfill({ contentType: name.endsWith('.js') ? 'application/javascript' : 'text/css', body: fs.readFileSync(path.join(root, 'internal/archive/assets', name)) });
      }
      if (url.pathname === '/api/tools/status') return respond({ conversations: 2, current_conversations: 2, pending_conversations: 0, tool_calls: 2, backfill: { running: false, done: 0, total: 0, error: null } });
      if (url.pathname === '/api/query/tool_calls') return respond({ rows: [
        { id: 'call-1', started_at: '2026-09-24T12:00:00Z', tool_name: 'Read', status: 'ok', title: 'First work' },
        { id: 'call-2', started_at: '2026-09-24T12:01:00Z', tool_name: 'Bash', status: 'error', title: 'Second work' },
      ], total: 2 });
      if (url.pathname === '/api/query/tool_calls/aggregations') {
        requests.push(route.request().postDataJSON());
        return respond({ metrics: [] });
      }
      if (url.pathname === '/api/query/tools') return respond({ rows: [], total: 0 });
      if (url.pathname === '/api/query/skill_usages') return respond({ rows: [
        { id: 'skill-1', skill_name: 'dataviz', evidence_type: 'explicit_invocation', status: 'ok', skill_path: 'bundled:dataviz', tool_call_id: 'call-1', workspace_id: 'work-1', conversation_id: 'conversation-1', evidence_message_id: 'invoke-1', body_message_id: 'body-1', title: 'First work' },
      ], total: 1 });
      if (url.pathname === '/api/query/skill_usages/aggregations') {
        requests.push(route.request().postDataJSON());
        return respond({ metrics: [] });
      }
      if (url.pathname === '/api/tool-calls/call-1') return respond({ id: 'call-1', tool_name: 'Skill', skill_name: 'dataviz', status: 'ok', started_at: '2026-09-24T12:00:00Z', input_text: '{"skill":"dataviz"}', result_text: 'Launching skill: dataviz' });
      if (url.pathname.endsWith('/aggregations')) return respond({ metrics: [] });
      if (url.pathname.endsWith('/distinct')) return respond({ values: [], hasMore: false });
      if (url.pathname.endsWith('/field-stats')) return respond({});
      return respond({});
    });

    await page.goto('http://tools-ui.test/tools');
    await page.locator('#tools .qt-row', { hasText: 'First work' }).waitFor();
    assert.equal(await page.locator('#tools .qt-row').count(), 2);
    assert.equal(await page.getByRole('group', { name: 'Tool view' }).getByRole('button', { name: 'Calls' }).getAttribute('aria-pressed'), 'true');
    const aggregation = page.waitForResponse(response => new URL(response.url()).pathname === '/api/query/tool_calls/aggregations');
    await page.getByRole('button', { name: 'Calls by day' }).click();
    await aggregation;
    assert.deepEqual(requests.at(-1).aggregations.map(metric => [metric.op, metric.groupBy]), [['count', ['day']], ['sum', ['day']]]);
    assert.equal(await page.locator('#tools .qt-row').count(), 2);
    await page.getByRole('group', { name: 'Tool view' }).getByRole('button', { name: 'Summary' }).click();
    assert.equal(new URL(page.url()).searchParams.get('tools_view'), 'summary');
    await page.getByRole('group', { name: 'Tool view' }).getByRole('button', { name: 'Skills' }).click();
    assert.equal(new URL(page.url()).searchParams.get('tools_view'), 'skills');
    await page.locator('#tools .qt-row', { hasText: 'dataviz' }).waitFor();
    const skillAggregation = page.waitForResponse(response => new URL(response.url()).pathname === '/api/query/skill_usages/aggregations');
    await page.getByRole('button', { name: 'Evidence by skill' }).click();
    await skillAggregation;
    assert.deepEqual(requests.at(-1).aggregations.map(metric => [metric.id, metric.op, metric.groupBy]), [['count:skill_name,evidence_type', 'count', ['skill_name', 'evidence_type']]]);
    assert.equal(await page.getByRole('button', { name: 'Tool call', exact: true }).count(), 1);
    await page.getByRole('button', { name: 'Tool call', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'Tool call details' });
    await dialog.getByRole('heading', { name: 'Skill', exact: true }).waitFor();
    assert.equal(await dialog.locator('.tool-facts').getByText('dataviz', { exact: true }).count(), 1);
    await dialog.getByRole('button', { name: 'Close', exact: true }).click();
    await page.reload();
    assert.equal(await page.getByRole('group', { name: 'Tool view' }).getByRole('button', { name: 'Skills' }).getAttribute('aria-pressed'), 'true');
    await page.getByRole('button', { name: 'Evidence', exact: true }).click();
    assert.equal(new URL(page.url()).pathname, '/work/work-1');
    assert.equal(new URL(page.url()).searchParams.get('conversation'), 'conversation-1');
    assert.equal(new URL(page.url()).searchParams.get('message'), 'body-1');
  } finally {
    await browser.close();
  }
});
