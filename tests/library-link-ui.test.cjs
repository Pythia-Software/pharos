const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');

const root = path.resolve(__dirname, '..');
const { chromium } = createRequire(path.join(root, '.context/browser-tests/package.json'))('playwright');
const source = fs.readFileSync(path.join(root, 'internal/archive/assets/ui.py'), 'utf8');
const html = source.slice(source.indexOf("r'''") + 4, source.lastIndexOf("'''"));

for (const start of ['/', '/usage?usage=tokens', '/work/work-1']) {
  for (const link of ['repository', 'model']) {
    test(`work ${link} link filters Library on its first visit from ${start}`, async () => {
      const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
      const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
      try {
        const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
        page.setDefaultTimeout(10_000);
        const queries = [], errors = [];
        page.on('pageerror', error => errors.push(error.message));
        const row = { id: 'work-1', title: 'Matching work', repository_name: 'example/repo', models: 'model-special', source_kind: 'codex' };
        await page.route('http://library-link.test/**', route => {
          const url = new URL(route.request().url());
          const json = (body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
          if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/assets/')) return route.fulfill({ contentType: 'text/html', body: html });
          if (url.pathname.startsWith('/assets/query-tables.')) return route.fulfill({ path: path.join(root, 'internal/archive', url.pathname) });
          if (url.pathname === '/api/work/work-1') return json({ ...row, conversations: [{ id: 'conversation-1', provider: 'codex' }] });
          if (url.pathname === '/api/work/work-1/conversations/conversation-1') return json({ id: 'conversation-1', provider: 'codex', title: 'Conversation', model: 'model-special', messages: [{ native_id: 'u1', role: 'user', kind: 'message', text: 'Review this work' }] });
          if (url.pathname.endsWith('/distinct')) return json({ values: [], hasMore: false });
          if (url.pathname.endsWith('/field-stats')) return json({});
          if (url.pathname.endsWith('/aggregations')) return json({ metrics: [] });
          if (url.pathname === '/api/query/library' && route.request().method() === 'POST') {
            const query = route.request().postDataJSON();
            queries.push(query);
            return json({ rows: [row], total: 1 });
          }
          if (url.pathname === '/api/query/usage') return json({ rows: [], total: 0 });
          return json({ error: 'not stubbed' }, 404);
        });
        await page.goto(`http://library-link.test${start}`);
        await page.waitForFunction(() => Boolean(window.pharosQueryTables && window.pharosNavigate));
        if (!start.startsWith('/work/')) await page.evaluate(() => window.pharosNavigate('/work/work-1'));
        await page.locator('#detail .model-search').waitFor();
        assert.equal(await page.locator('#queryTableLibrary > *').count(), 0);
        assert.equal(queries.length, 0);

        const expected = link === 'repository'
          ? { field: 'repository_name', op: '=', value: 'example/repo' }
          : { field: 'models', op: 'contains', value: 'model-special' };
        await page.locator(`#detail .${link === 'repository' ? 'work-repository-link' : 'model-search'}`).click();
        await page.locator('#queryTableLibrary .qt-row', { hasText: 'Matching work' }).waitFor();
        await page.waitForFunction(() => new URL(location.href).pathname === '/library' && new URL(location.href).searchParams.has('q_library'));
        // Restoration must not issue an unfiltered request before applying the link.
        assert.ok(queries.length > 0);
        for (const query of queries) {
          assert.deepEqual(query.where, [expected]);
          assert.equal(query.offset, 0);
        }

        // The same links must also replace the filter after Library has mounted.
        await page.evaluate(() => window.pharosNavigate('/work/work-1'));
        await page.locator('#detail .model-search').waitFor();
        const previousCount = queries.length;
        const response = page.waitForResponse(response => response.url().endsWith('/api/query/library') && response.request().method() === 'POST');
        await page.locator(`#detail .${link === 'repository' ? 'model-search' : 'work-repository-link'}`).click();
        await response;
        const replacement = link === 'repository'
          ? { field: 'models', op: 'contains', value: 'model-special' }
          : { field: 'repository_name', op: '=', value: 'example/repo' };
        assert.ok(queries.length > previousCount);
        assert.deepEqual(queries.at(-1).where, [replacement]);
        assert.deepEqual(errors, []);
      } finally { await browser.close(); }
    });
  }
}
