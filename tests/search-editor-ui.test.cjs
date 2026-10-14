const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');
const { buildSync } = require('../web/node_modules/esbuild');
const root = path.resolve(__dirname, '..');
const { chromium } = createRequire(path.join(root, '.context/browser-tests/package.json'))('playwright');
const shell = fs.readFileSync(path.join(root, 'internal/archive/assets/ui.py'), 'utf8');
const style = shell.match(/<style>([\s\S]*?)<\/style>/)[1];
const sprite = shell.match(/<svg class="app-icon-sprite"[\s\S]*?<\/svg>/)[0];
const bundle = buildSync({
  stdin: { contents: `
    import React from 'react';
    import { createRoot } from 'react-dom/client';
    import { loadSchema } from '@pythia-software/query-table-core';
    import { HomeSearch } from './search-composer';
    import schema from '../../schemas/library.schema.json';
    window.pharosNavigate = url => { window.submitted = url; };
    createRoot(document.getElementById('fixture')).render(<HomeSearch schema={loadSchema(schema)} />);
  `, resolveDir: path.join(root, 'web/src'), loader: 'tsx' },
  bundle: true, preserveSymlinks: true, write: false, format: 'iife', outfile: 'fixture.js',
});
const html = `<!doctype html><html data-theme="dark"><head><style>${style}
  body { display: block; overflow: auto; padding: 30px 16px; }
  .home-intro { margin: 0 auto; }
</style><link rel="stylesheet" href="/fixture.css"></head><body>${sprite}
  <div class="home-intro"><h1>What will you discover?</h1><div id="fixture"></div></div>
  <button id="after">After search</button><script src="/fixture.js"></script></body></html>`;

async function setup({ width = 1180, reducedMotion = 'no-preference' } = {}) {
  const browser = await chromium.launch({ headless: true, executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
  const page = await browser.newPage({ viewport: { width, height: 900 }, reducedMotion });
  const requests = [], errors = [];
  page.on('pageerror', error => errors.push(String(error)));
  await page.route('http://search.test/**', route => {
    const url = new URL(route.request().url());
    const json = (body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
    if (url.pathname === '/') return route.fulfill({ contentType: 'text/html', body: html });
    if (url.pathname === '/fixture.js') return route.fulfill({ contentType: 'application/javascript', body: bundle.outputFiles.find(file => file.path.endsWith('.js')).text });
    if (url.pathname === '/fixture.css') return route.fulfill({ contentType: 'text/css', body: bundle.outputFiles.find(file => file.path.endsWith('.css')).text });
    if (url.pathname === '/api/search/text/options') return json({ providers: ['codex'], sources: ['conductor'] });
    if (url.pathname === '/api/search/text/validate') {
      const request = route.request().postDataJSON(); requests.push(request);
      if (request.query === 'cache OR buffer') return json({ where: { op: 'or', children: [{ op: 'term', text: 'cache' }, { op: 'term', text: 'buffer' }] } });
      if (request.where) return json({ where: request.where });
      return json({ error: 'Unclosed query group' }, 400);
    }
    return route.fulfill({ status: 404 });
  });
  await page.goto('http://search.test/');
  await page.getByRole('button', { name: 'Search your library', exact: true }).waitFor();
  return { browser, page, requests, errors };
}
const rule = page => page.getByRole('textbox', { name: 'Text for rule 1.1', exact: true });
const rules = page => page.getByRole('button', { name: 'Visual editor', exact: true });
const syntax = page => page.getByRole('button', { name: 'Query syntax', exact: true });
const query = page => page.getByRole('textbox', { name: 'Search query', exact: true });

test('Home starts collapsed, animates open, and preserves drafts through collapse and view changes', async () => {
  const { browser, page, errors } = await setup();
  try {
    const trigger = page.getByRole('button', { name: 'Search your library', exact: true });
    assert.equal(await trigger.getAttribute('aria-expanded'), 'false');
    assert.equal(await page.locator('.home-search-content').evaluate(element => element.inert), true);
    assert.equal(await page.locator('.text-editor-tabs').isVisible(), false);
    assert.ok((await trigger.boundingBox()).height <= 60);
    await page.keyboard.press('Tab'); assert.equal(await trigger.evaluate(element => element === document.activeElement), true);
    await page.keyboard.press('Tab'); assert.equal(await page.locator('#after').evaluate(element => element === document.activeElement), true);
    await trigger.click();
    await rule(page).waitFor();
    assert.equal(await page.locator('.home-search-expansion').evaluate(element => getComputedStyle(element).transitionDuration.includes('0.28s')), true);
    await page.waitForFunction(() => document.activeElement?.getAttribute('aria-label') === 'Text for rule 1.1');
    await rule(page).fill('parser migration');
    await page.keyboard.press('Escape');
    assert.equal(await trigger.getAttribute('aria-expanded'), 'false');
    assert.match(await trigger.innerText(), /parser migration/);
    await trigger.click(); assert.equal(await rule(page).inputValue(), 'parser migration');
    await page.evaluate(() => window.dispatchEvent(new Event('pharos:view')));
    assert.equal(await trigger.getAttribute('aria-expanded'), 'false');
    await trigger.click(); assert.equal(await rule(page).inputValue(), 'parser migration');
    await page.getByRole('button', { name: 'Collapse search', exact: true }).click();
    assert.equal(await page.locator('.home-search-content').evaluate(element => element.inert), true);
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

test('Rules and Syntax round trip empty and unfinished drafts without submitting or validating', async () => {
  const { browser, page, requests, errors } = await setup();
  try {
    await page.getByRole('button', { name: 'Search your library', exact: true }).click();
    await syntax(page).click(); await rules(page).click();
    assert.equal(await rules(page).getAttribute('aria-pressed'), 'true');
    await rule(page).fill('cache buffer');
    await page.getByRole('button', { name: 'Add clause to group 1', exact: true }).click();
    await page.getByRole('button', { name: 'Matching condition', exact: true }).click();
    await syntax(page).click(); await rules(page).click();
    assert.equal(await rule(page).inputValue(), 'cache buffer');
    assert.equal(await page.getByRole('textbox', { name: 'Text for rule 1.2', exact: true }).inputValue(), '');
    await page.getByRole('textbox', { name: 'Text for rule 1.2', exact: true }).fill('migration');
    await syntax(page).click(); assert.match(await query(page).inputValue(), /cache AND buffer/);
    await rules(page).click(); assert.equal(await rule(page).inputValue(), 'cache buffer');
    assert.deepEqual(requests, []); assert.equal(await page.evaluate(() => window.submitted), undefined);
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

test('edited syntax returns to Rules despite unfinished filters, and invalid syntax retains the draft', async () => {
  const { browser, page, requests, errors } = await setup();
  try {
    await page.getByRole('button', { name: 'Search your library', exact: true }).click();
    await page.getByRole('button', { name: 'Add filter to query', exact: true }).click();
    await page.getByRole('button', { name: 'Date on or after', exact: true }).click();
    await page.getByLabel('Date on or after', { exact: true }).fill('2026-10-13');
    await page.getByRole('button', { name: 'Add filter to query', exact: true }).click();
    await page.getByRole('button', { name: 'Date on or before', exact: true }).click();
    await page.getByLabel('Date on or before', { exact: true }).fill('2026-10-01');
    await syntax(page).click(); await query(page).fill('cache OR buffer'); await rules(page).click();
    await rule(page).waitFor(); assert.equal(await rule(page).inputValue(), 'cache');
    assert.equal(await page.getByLabel('Match rules in group 1', { exact: true }).inputValue(), 'any');
    assert.equal(await page.getByLabel('Date on or after', { exact: true }).inputValue(), '2026-10-13');
    assert.equal(requests.length, 1); assert.equal(requests[0].from, undefined); assert.equal(requests[0].to, undefined);
    await syntax(page).click(); await query(page).fill('(cache'); await rules(page).click();
    await page.getByRole('alert').filter({ hasText: 'Unclosed query group' }).waitFor();
    assert.equal(await query(page).inputValue(), '(cache');
    assert.equal(await syntax(page).getAttribute('aria-pressed'), 'true');
    assert.equal(await page.evaluate(() => window.submitted), undefined);
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

test('mobile supports user/agent targets, reduced motion, and submitting a converted query', async () => {
  const { browser, page, errors } = await setup({ width: 390, reducedMotion: 'reduce' });
  try {
    await page.getByRole('button', { name: 'Search your library', exact: true }).click();
    const target = page.getByLabel('Search target', { exact: true });
    const labels = await target.locator('option').allTextContents();
    assert.ok(labels.includes('User messages')); assert.ok(labels.includes('Agent replies'));
    assert.equal(labels.some(label => /people|person/i.test(label)), false);
    await target.selectOption('user'); await syntax(page).click(); await query(page).fill('cache OR buffer'); await rules(page).click();
    await rule(page).waitFor();
    assert.equal(await target.inputValue(), 'user');
    assert.equal(await page.locator('.home-search-expansion').evaluate(element => getComputedStyle(element).transitionDuration), '0s');
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    await page.getByRole('button', { name: 'Search messages', exact: true }).click();
    await page.waitForFunction(() => window.submitted);
    const url = new URL(await page.evaluate(() => window.submitted), 'http://search.test');
    const draft = JSON.parse(url.searchParams.get('text_builder'));
    assert.equal(draft.editor, 'visual'); assert.equal(draft.root.join, 'any'); assert.equal(draft.scope.surface, 'user');
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});
