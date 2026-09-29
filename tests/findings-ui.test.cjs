// Drives the Findings tab (web/src/findings.tsx in the query-tables bundle and
// the shell in internal/archive/assets/ui.py) against mocked /api/findings
// responses: routing to /findings and back, the tab badge and the header's
// cart button, a finding's detail with its chart, the prompt cart's copy
// flow, and the empty state.
//
//   node --test tests/findings-ui.test.cjs
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');

const root = path.resolve(__dirname, '..');
const { chromium } = createRequire(path.join(root, '.context/browser-tests/package.json'))('playwright');
const source = fs.readFileSync(path.join(root, 'internal/archive/assets/ui.py'), 'utf8');
const html = source.slice(source.indexOf("r'''") + 4, source.lastIndexOf("'''"));

const repo = 'repo:repo_explo';
const openCard = {
  id: 'drift:repo:repo_explo:abc', state: 'open', title: "Codex can't find explo's instructions",
  explanation: 'explo keeps its agent instructions in `CLAUDE.md`, which only Claude reads.',
  impact: { usd: 41.2, tokens: 150e6, minutes: 12, failures: 330, window_days: 28 }, impact_note: '330 searches', rate: '1 in 3',
  change: 'Make one instructions file that both Claude and Codex read.', change_label: 'One shared instructions file', attempt: 1,
  target: repo, target_label: 'explo', scope_label: 'explo', repository_id: 'repo_explo', first_seen_at: '2026-09-20T10:00:00Z', last_seen: '2026-09-27T10:00:00Z', active: true, new: true,
};
const cartCard = {
  ...openCard, id: 'failure:repo:repo_explo:def', title: 'Gzipped files are read as text in explo', explanation: 'Agents read gzipped files as text.',
  impact: { usd: 3, tokens: 1e6, minutes: 1, failures: 36 }, impact_note: '34 failed commands', new: false,
  cart: { target: repo, target_label: 'explo', ticked: true },
};
const watchingCard = {
  ...openCard, id: 'drift:repo:repo_tl1:ghi', state: 'watching', title: "Codex can't find tl1's instructions", new: false, target: 'repo:repo_tl1', target_label: 'tl1', scope_label: 'tl1', repository_id: 'repo_tl1',
  change_label: 'A shared instructions file', change: 'A shared instructions file',
  watching: { before: '1 in 3', change: 'A shared instructions file', copied_at: '2026-09-16T10:00:00Z', day: 12, of_days: 28, result_on: '2026-10-14', saved_usd: 3.2, target_label: 'tl1' },
};
function overview({ findings = [openCard, cartCard, watchingCard], fresh = 2 } = {}) {
  return {
    status: { built_at: '2026-09-28T06:02:00Z', took_ms: 11000, measured_at: null, conversations_28d: 4482, weekly_conversations: 1120, generation: 'g', running: false, phase: null, error: null, current: true },
    settings: { threshold: 0, rank: 'usd', handoff: 'auto', repository_handoff: {}, seen_at: '' },
    threshold: 5, recommended: 5,
    checkpoints: [3, 5, 10, 15, 20].map(threshold => ({ threshold, shown: 60 - threshold * 2, typical_wait_days: 28, clear_share: 0.8 })),
    summary: { saved: { usd: 38.4, tokens: 5.2e6, minutes: 74, failures: 12, added_usd: 1.1, added_tokens: 9e5 }, open: findings.filter(card => card.state === 'open').length, watching: findings.filter(card => card.state === 'watching').length, won: 0, dismissed: 0, snoozed: 0, regressed: 0, at_stake_usd: 44.2, new: fresh, next_result_days: 16 },
    findings,
    cart_count: findings.filter(card => card.cart).length,
    cart: findings.some(card => card.cart) ? [{ target: repo, label: 'explo', handoff: 'pr', handoff_reason: "explo's work ends in a pull request 86% of the time, so Pharos suggests a PR.", ticked: 1, items: [{ id: cartCard.id, title: cartCard.title, ticked: true, change_label: 'A line in the instructions', suggested_target: repo, undo: false }] }] : [],
    targets: [{ target: repo, label: 'explo' }, { target: 'repo:repo_tl1', label: 'tl1' }, { target: 'global:host:codex', label: 'All repositories · Codex on this Mac' }],
    repositories: [{ id: 'repo_explo', name: 'explo', work: 700, pr_share: 0.86, handoff: 'pr', override: null }],
    near: [{ id: 'n1', title: '`python`: command not found', affected: 3 }, { id: 'n2', title: 'A missing AGENTS.md in one repository', affected: 4 }],
    wins: [],
  };
}
const weeks = Array.from({ length: 14 }, (_, index) => {
  const start = new Date(Date.UTC(2026, 5, 24 + index * 7)).toISOString().slice(0, 10);
  return { start, events: index < 12 ? 5 : 1, exposure: index === 3 ? 1 : 12, rate: index < 12 ? 5 / 12 : 1 / 12, hollow: index === 3, after: index >= 12, cost_usd: 1 };
});
const detail = { ...watchingCard, chart: { title: 'Codex conversations that looked for instructions', kind: 'rate', weeks, anchor: '2026-09-16', copies: ['2026-09-16'], baseline: 0.4, baseline_phrase: '2 in 5' },
  evidence: [{ at: '2026-09-27T12:00:00Z', workspace_id: 'workspace_1', conversation_id: 'conversation_1', message_id: 'message_1', where: 'tl1 · Codex', did: 'searched for `AGENTS.md`', happened: 'found nothing' }],
  attempts: [{ attempt: 1, change: 'A shared instructions file', copied_at: '2026-09-16T10:00:00Z', status: 'watching', target_label: 'tl1', savings: { usd: 3.2, added_usd: 0 } }],
  steps: [{ label: 'A shared instructions file', change: '' }, { label: 'A fallback setting', change: '' }], metric: { kind: 'rate', unit: 'conversations' }, prompt: 'Pharos finding drift:repo:repo_tl1:ghi …' };

async function open(state) {
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.addInitScript(() => {
    sessionStorage.setItem('pharos-upgrade-offered', '1');
    window.webkit = { messageHandlers: { pharosClipboard: { postMessage: async text => { window.__copiedText = text; } } } };
  });
  await page.route('http://findings-ui.test/**', route => {
    const url = new URL(route.request().url());
    const respond = body => route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
    if (['/library', '/findings', '/'].includes(url.pathname)) return route.fulfill({ contentType: 'text/html', body: html });
    if (url.pathname === '/assets/query-tables.js' || url.pathname === '/assets/query-tables.css') {
      const name = path.basename(url.pathname);
      return route.fulfill({ contentType: name.endsWith('.js') ? 'application/javascript' : 'text/css', body: fs.readFileSync(path.join(root, 'internal/archive/assets', name)) });
    }
    if (url.pathname.startsWith('/api/findings')) {
      const method = route.request().method();
      state.requests.push({ method, path: url.pathname + url.search, body: method === 'POST' ? route.request().postDataJSON() : null });
      if (url.pathname === '/api/findings') return respond(state.overview);
      if (url.pathname === '/api/findings/seen') { state.overview = { ...state.overview, summary: { ...state.overview.summary, new: 0 } }; return respond({ ok: true }); }
      if (url.pathname === '/api/findings/cart/prompt') return respond({ target: repo, label: 'explo', handoff: route.request().postDataJSON().handoff ?? 'pr', prompt: `PROMPT for ${route.request().postDataJSON().ids.join(',')}`, count: 1 });
      if (url.pathname === '/api/findings/cart/copy') { state.copied = route.request().postDataJSON(); return respond({ copied: true }); }
      if (url.pathname.endsWith('/action')) return respond({ ok: true });
      if (url.pathname === `/api/findings/${encodeURIComponent(detail.id)}`) return respond(detail);
      return route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"not found"}' });
    }
    if (url.pathname === '/api/tools/status') return respond({ conversations: 0, current_conversations: 0, pending_conversations: 0, tool_calls: 0, backfill: { running: false, done: 0, total: 0, error: null } });
    if (url.pathname.startsWith('/api/query/')) return respond(url.pathname.endsWith('/distinct') ? { values: [], hasMore: false } : url.pathname.endsWith('/aggregations') ? { metrics: [] } : { rows: [], total: 0 });
    return respond({});
  });
  return { browser, page, errors };
}

test('Findings is routed at /findings, badges new findings until visited, and the cart button opens it', async () => {
  const state = { overview: overview(), requests: [] };
  const { browser, page, errors } = await open(state);
  try {
    await page.goto('http://findings-ui.test/library');
    const tab = page.locator('#findingsTab');
    await page.locator('#findingsBadge:not([hidden])').waitFor();
    assert.equal(await page.locator('#findingsBadge').innerText(), '2');
    assert.equal(await tab.getAttribute('aria-label'), 'Findings, 2 new since your last visit');
    assert.equal(await page.locator('#findingsCartCount').innerText(), '1');
    assert.equal(await page.locator('#findingsCartButton').isVisible(), true);
    // Tabs sit in order, with Findings between Tools and MCP.
    assert.deepEqual(await page.locator('nav.tabs button').evaluateAll(buttons => buttons.filter(b => !b.hidden).map(b => b.dataset.view)), ['library', 'usage', 'tools', 'findings', 'mcp']);

    await tab.click();
    await page.getByRole('heading', { name: 'Findings', level: 1 }).waitFor();
    assert.equal(new URL(page.url()).pathname, '/findings');
    assert.equal(await page.locator('#findings').evaluate(view => view.classList.contains('active')), true);
    await page.locator('#findingsBadge[hidden]').waitFor({ state: 'attached' });
    assert.ok(state.requests.some(request => request.method === 'POST' && request.path === '/api/findings/seen'));
    // New since the last visit stays marked on the card during this visit.
    assert.equal(await page.locator('.findings-card', { hasText: "explo's instructions" }).locator('.findings-chip.new').count(), 1);
    // The ranked unit leads each card, then the note that makes the point.
    assert.match(await page.locator('.findings-card').first().locator('.findings-impact-lead').innerText(), /^\$41 a month · 330 searches$/);
    assert.match(await page.locator('.findings-card').first().locator('.findings-explanation').innerHTML(), /<code>CLAUDE\.md<\/code>/);

    await page.locator('button[data-view=library]').click();
    await page.locator('#findingsCartButton').click();
    await page.locator('#findingsCart').waitFor();
    assert.equal(new URL(page.url()).pathname, '/findings');
    await page.waitForFunction(() => document.activeElement?.id === 'findingsCart');

    // A finding's page has its own address; back and forward move between it and the list.
    await page.getByRole('tab', { name: /Watching/ }).click();
    assert.equal(new URL(page.url()).searchParams.get('list'), 'watching');
    await page.locator('.findings-card', { hasText: "tl1's instructions" }).locator('h3 a').click();
    await page.locator('.findings-detail h1', { hasText: "tl1's instructions" }).waitFor();
    assert.equal(new URL(page.url()).searchParams.get('finding'), detail.id);
    assert.match(await page.locator('.findings-chart > svg').getAttribute('aria-label'), /Prompt copied Sep 1[56]/);
    assert.equal(await page.locator('.findings-chart > svg .findings-chart-point.hollow').count(), 1);
    assert.match(await page.locator('.findings-detail-chips').innerText(), /Measuring · day 12 of about 28/);
    await page.getByRole('tab', { name: 'Evidence · 1' }).click();
    assert.equal(new URL(page.url()).searchParams.get('tab'), 'evidence');
    assert.match(await page.locator('.findings-evidence').innerText(), /searched for AGENTS\.md/);
    await page.goBack();
    await page.locator('.findings-tiles').waitFor();
    assert.equal(new URL(page.url()).searchParams.get('finding'), null);
    assert.equal(await page.getByRole('tab', { name: /Watching/ }).getAttribute('aria-selected'), 'true');
    await page.goForward();
    await page.locator('.findings-detail h1').waitFor();
    // Leaving Findings drops its own parameters from the address.
    await page.locator('button[data-view=library]').click();
    assert.equal(new URL(page.url()).searchParams.get('finding'), null);
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

test('Copying a cart prompt writes the clipboard first and then starts measuring the ticked findings', async () => {
  const state = { overview: overview(), requests: [] };
  const { browser, page, errors } = await open(state);
  try {
    await page.goto('http://findings-ui.test/findings');
    await page.getByRole('button', { name: 'Review prompt (1 ticked)' }).click();
    const dialog = page.getByRole('dialog', { name: 'Prompt for explo' });
    await dialog.getByText(`PROMPT for ${cartCard.id}`).waitFor();
    assert.match(await dialog.innerText(), /One finding\. Paste this into an agent working in explo\./);
    await dialog.getByLabel('Show me the diff').check();
    await page.waitForFunction(() => document.querySelector('.findings-prompt')?.textContent?.startsWith('PROMPT'));
    assert.equal(state.requests.filter(request => request.path === '/api/findings/cart/prompt').at(-1).body.handoff, 'diff');
    await dialog.getByRole('button', { name: 'Copy prompt and start measuring' }).click();
    await page.waitForFunction(() => window.__copiedText);
    await page.waitForFunction(() => document.querySelector('.findings-success'));
    assert.equal(await page.evaluate(() => window.__copiedText), `PROMPT for ${cartCard.id}`);
    assert.deepEqual(state.copied, { target: repo, ids: [cartCard.id], handoff: 'diff' });
    await dialog.waitFor({ state: 'detached' });

    // Escape closes menus and returns focus to their button.
    const card = page.locator('.findings-card').first();
    await card.getByRole('button', { name: 'Dismiss' }).click();
    await page.getByRole('menuitem', { name: /Not worth it/ }).waitFor();
    await page.keyboard.press('Escape');
    assert.equal(await page.getByRole('menu').count(), 0);
    await card.getByRole('button', { name: 'Dismiss' }).click();
    await page.getByRole('menuitem', { name: /Not worth it/ }).click();
    await page.waitForFunction(() => document.querySelector('.findings-toast.shown'));
    const action = state.requests.find(request => request.path.endsWith('/action'));
    assert.deepEqual(action.body, { action: 'dismiss', reason: 'not_worth_it' });
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

test('With nothing to show, Findings says what it looked at and which patterns are close', async () => {
  const state = { overview: overview({ findings: [], fresh: 0 }), requests: [] };
  const { browser, page, errors } = await open(state);
  try {
    await page.goto('http://findings-ui.test/findings');
    await page.getByText('Nothing qualifies yet.').waitFor();
    const text = await page.locator('.findings-empty').innerText();
    assert.match(text, /Pharos looked at 4,482 conversations from the last 28 days\./);
    assert.match(text, /shows up in 5 conversations \(the recommended threshold for a library this size\)/);
    assert.match(text, /Two patterns are close: python: command not found \(3 conversations\) and A missing AGENTS\.md in one repository \(4 conversations\)/);
    assert.equal(await page.locator('#findingsBadge').evaluate(badge => badge.hidden), true);
    assert.equal(await page.locator('#findingsCartButton').evaluate(button => button.hidden), true);
    if (process.env.PHAROS_UI_SCREENSHOTS) await page.screenshot({ path: path.join(process.env.PHAROS_UI_SCREENSHOTS, 'findings-empty-state.png') });
    await page.getByRole('link', { name: 'Change the threshold' }).click();
    await page.getByRole('heading', { name: 'Findings settings' }).waitFor();
    assert.equal(new URL(page.url()).searchParams.get('view'), 'settings');
    assert.equal(await page.getByRole('slider').inputValue(), '1');
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});
