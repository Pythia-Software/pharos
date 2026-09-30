const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');

const root = path.resolve(__dirname, '..');
const { chromium } = createRequire(path.join(root, '.context/browser-tests/package.json'))('playwright');
const source = fs.readFileSync(path.join(root, 'internal/archive/assets/ui.py'), 'utf8');
const html = source.slice(source.indexOf("r'''") + 4, source.lastIndexOf("'''"));

const harness = `<system_instruction>\n${'You are working inside Conductor. '.repeat(12)}\n</system_instruction>\n\n`;
const message = {
  id: 'm1', workspace_id: 'w1', work_id: 'w1', conversation_id: 'c1', title: 'Parser fixes', repository_name: 'example/repo', source_kind: 'conductor', provider: 'claude',
  sent_at: '2026-09-29T12:00:00Z', words: 60, typed_words: 7, other_words: 53, pasted_words: 9, harness_words: 44, typed_share: 11.7, main_category: 'harness',
  categories: ['harness', 'pasted', 'typed'], rules: ['Conductor system instruction', 'Code block', 'No other rule matched'],
  segments: [
    { text: harness, category: 'harness', rule: 'Conductor system instruction', reason: 'Conductor system instruction', words: 44 },
    { text: 'Why does this fail?\n', category: 'typed', rule: 'No other rule matched', words: 4 },
    { text: '```\npanic: runtime error: index out of range\n```', category: 'pasted', rule: 'Code block', reason: 'Code block', words: 9,
      source: { workspace_id: 'w2', conversation_id: 'c2', message_id: 'm9', title: 'Earlier run' } },
    { text: '\nPlease fix it.', category: 'typed', rule: 'No other rule matched', words: 3 },
  ],
};

test('Human Words lists single messages as a table or as text highlighted by where it came from', async () => {
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
    const page = await (await browser.newContext({ viewport: { width: 1280, height: 900 } })).newPage();
    const errors = [], queries = [], mixes = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.route('http://writing-ui.test/**', route => {
      const url = new URL(route.request().url());
      if (!url.pathname.startsWith('/api/') && !url.pathname.startsWith('/assets/')) return route.fulfill({ contentType: 'text/html', body: html });
      if (url.pathname === '/assets/query-tables.js') return route.fulfill({ contentType: 'application/javascript', body: fs.readFileSync(path.join(root, 'internal/archive/assets/query-tables.js')) });
      if (url.pathname === '/assets/query-tables.css') return route.fulfill({ contentType: 'text/css', body: fs.readFileSync(path.join(root, 'internal/archive/assets/query-tables.css')) });
      const json = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
      if (url.pathname === '/api/authorship') return json({ built_at: '2026-09-29T12:00:00Z', running: false, stale: false, error: null, totals: {}, last_30_days: {} });
      if (url.pathname === '/api/query/writing/series') return json({ works: 1, daily: [], totals: { messages: 1 } });
      if (url.pathname === '/api/query/writing') return json({ rows: [{ id: 'w1', workspace_id: 'w1', title: 'Parser fixes', typed_words: 7, user_words: 60 }], total: 1 });
      if (url.pathname === '/api/query/writing_messages') { queries.push(route.request().postDataJSON()); return json({ rows: [message], total: 1 }); }
      if (url.pathname === '/api/query/writing_messages/aggregations') {
        const body = route.request().postDataJSON();
        if (body.aggregations.some(aggregation => aggregation.id === 'messages')) mixes.push(body);
        return json({ metrics: body.aggregations.map(aggregation => ({ id: aggregation.id, buckets: [{ keys: [], value: aggregation.id === 'messages' ? 1 : Number(message[`${aggregation.id}_words`]) || 0, count: 1 }] })) });
      }
      if (url.pathname.endsWith('/distinct')) return json({ values: [], hasMore: false });
      if (url.pathname.endsWith('/field-stats')) return json({});
      if (url.pathname.endsWith('/aggregations')) return json({ metrics: [] });
      return route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: 'not stubbed' }) });
    });

    // A conversation's Messages button lists the messages counted toward it.
    await page.goto('http://writing-ui.test/usage?usage=writing');
    await page.locator('#usage .qt-row', { hasText: 'Parser fixes' }).getByRole('button', { name: 'Messages' }).click();
    await page.locator('#usage .qt-row .authored-inline').waitFor();
    assert.deepEqual(queries.at(-1).where, [{ field: 'work_id', op: '=', value: 'w1' }]);
    assert.equal(new URL(page.url()).searchParams.get('writing'), 'messages');
    // The table's preview shrinks the harness block so the typed text shows,
    // and every span says what it counted as.
    const inline = page.locator('#usage .qt-row .authored-inline');
    assert.match(await inline.innerText(), /^harness\s+Why does this fail\?/);
    assert.equal(await inline.locator('mark[data-category="pasted"]').getAttribute('title'), 'Likely pasted · Code block · 9 words');
    assert.equal(await page.locator('#usage .qt-row .authored-pill').innerText(), 'Harness');

    // The key totals words per category and filters to a category.
    await page.locator('.authored-mix h3', { hasText: '1 message · 60 words' }).waitFor();
    await page.locator('.authored-legend').getByRole('button', { name: /Likely pasted/ }).click();
    await page.waitForFunction(() => document.querySelector('.authored-legend [data-category="pasted"]')?.getAttribute('aria-pressed') === 'true');
    assert.deepEqual(mixes.at(-1).where.at(-1), { field: 'categories', op: 'includes', value: 'pasted' });

    // Highlighted text shows the whole message, long harness text folded.
    await page.getByRole('group', { name: 'Message result view' }).getByRole('button', { name: 'Highlighted text' }).click();
    const card = page.locator('.authored-card');
    await card.waitFor();
    assert.equal(new URL(page.url()).searchParams.get('messages'), 'text');
    assert.match(await card.locator('.authored-facts').innerText(), /7 words typed[\s\S]*53 from elsewhere[\s\S]*Mostly harness/);
    const folded = card.locator('mark[data-category="harness"]');
    assert.ok((await folded.innerText()).length < harness.length);
    await folded.getByRole('button', { name: 'show all 44 words' }).click();
    assert.equal((await folded.innerText()).trim(), harness.trim());

    // Hovering a span explains the rule; a click keeps it open for its link.
    const pasted = card.locator('mark[data-category="pasted"]');
    await pasted.hover();
    const tip = page.locator('.authored-tip');
    await tip.waitFor();
    assert.match(await tip.innerText(), /Likely pasted[\s\S]*9 words[\s\S]*Code block[\s\S]*Text inside ``` fences/);
    await pasted.click();
    await page.locator('.authored-tip.pinned').waitFor();
    await tip.getByRole('button', { name: 'Open the matching text in Earlier run' }).click();
    await page.waitForURL(url => url.pathname === '/work/w2' && url.searchParams.get('message') === 'm9');
    assert.deepEqual(errors, []);
  } finally {
    await browser.close();
  }
});
