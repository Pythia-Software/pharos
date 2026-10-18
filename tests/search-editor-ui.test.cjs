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
    import { decodeQueryStrict, loadSchema } from '@pythia-software/query-table-core';
    import { HomeSearch } from './search-composer';
    import { RecordSearchEditor } from './record-search-editor';
    import schema from '../../schemas/library.schema.json';
    import toolSchema from '../../schemas/tool_calls.schema.json';
    window.pharosNavigate = url => { window.submitted = url; };
    window.decodeQuery = decodeQueryStrict;
    // The Tools record search, with a table controller that records applied queries.
    function Records() {
      const fields = React.useMemo(() => loadSchema(toolSchema), []);
      const [query, setQuery] = React.useState({ where: [], orderBy: fields.defaultSort ?? [] });
      const api = { computed: { catalogue: fields.fields }, query, defaults: { orderBy: fields.defaultSort ?? [] }, setQuery: update => setQuery(previous => (window.recordQuery = typeof update === 'function' ? update(previous) : update)) };
      return <RecordSearchEditor api={api} dataset="tool_calls" layout={<p>Layout</p>} />;
    }
    const view = new URLSearchParams(location.search).get('view');
    createRoot(document.getElementById('fixture')).render(view === 'record' ? <Records /> : <HomeSearch schema={loadSchema(schema)} />);
  `, resolveDir: path.join(root, 'web/src'), loader: 'tsx' },
  bundle: true, preserveSymlinks: true, write: false, format: 'iife', outfile: 'fixture.js',
});
const html = `<!doctype html><html data-theme="dark"><head><style>${style}
  body { display: block; overflow: auto; padding: 30px 16px; }
  .home-intro { margin: 0 auto; }
</style><link rel="stylesheet" href="/fixture.css"></head><body>${sprite}
  <div class="home-intro"><h1>What will you discover?</h1><div id="fixture"></div></div>
  <button id="after">After search</button><script src="/fixture.js"></script></body></html>`;

async function setup({ width = 1180, reducedMotion = 'no-preference', view = '' } = {}) {
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
  await page.goto(`http://search.test/${view ? `?view=${view}` : ''}`);
  await (view ? page.getByRole('button', { name: 'Add condition', exact: true }) : page.getByRole('button', { name: 'Search your library', exact: true })).waitFor();
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
    await page.getByRole('button', { name: 'Add condition', exact: true }).click();
    await page.getByRole('button', { name: 'Words or phrase', exact: true }).click();
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
    await page.getByRole('button', { name: 'Add condition', exact: true }).click();
    await page.getByRole('button', { name: 'Date on or after', exact: true }).click();
    await page.getByLabel('Date on or after', { exact: true }).fill('2026-10-13');
    await page.getByRole('button', { name: 'Add condition', exact: true }).click();
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

test('long text wraps in its box, the top box reads the search in English, and Enter submits', async () => {
  const { browser, page, errors } = await setup({ width: 700 });
  try {
    await page.getByRole('button', { name: 'Search your library', exact: true }).click();
    const reading = page.getByRole('button', { name: 'Collapse search', exact: true });
    assert.match(await reading.innerText(), /^Find conversation messages…/);
    const long = 'the migration to the new parser broke incremental sync for workspaces that were renamed while an agent was running';
    await rule(page).fill(`${long}\nwith a pasted line break`);
    assert.equal(await rule(page).inputValue(), `${long} with a pasted line break`);
    const box = await rule(page).evaluate(element => ({ height: element.clientHeight, lineHeight: parseFloat(getComputedStyle(element).lineHeight), scrolls: element.scrollWidth > element.clientWidth || element.scrollHeight > element.clientHeight }));
    assert.ok(box.height > box.lineHeight * 2, 'the text box grows to show wrapped lines'); assert.equal(box.scrolls, false);
    assert.match(await reading.innerText(), /^Find conversation messages with all of the words “the migration/);
    assert.ok((await reading.boundingBox()).height > 60, 'the reading wraps instead of truncating');
    await page.getByRole('button', { name: 'Add condition', exact: true }).click();
    await page.getByRole('button', { name: 'Provider', exact: true }).click();
    await page.getByLabel('Provider', { exact: true }).selectOption('codex');
    assert.match(await reading.innerText(), /^Find conversation messages from Codex, with all of the words/);
    await rule(page).press('Enter');
    await page.waitForFunction(() => window.submitted);
    const draft = JSON.parse(new URL(await page.evaluate(() => window.submitted), 'http://search.test').searchParams.get('text_builder'));
    assert.equal(draft.root.children[0].text, `${long} with a pasted line break`); assert.equal(draft.scope.provider, 'codex');
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

// The redesign moved controls into menus; every capability must still reach the search.
const submittedSearch = async page => {
  await page.waitForFunction(() => window.submitted);
  const url = new URL(await page.evaluate(() => window.submitted), 'http://search.test');
  await page.evaluate(() => { delete window.submitted; });
  const query = url.searchParams.get('q_library');
  return { url, draft: JSON.parse(url.searchParams.get('text_builder') ?? 'null'), query: query && await page.evaluate(token => window.decodeQuery(token), query) };
};
const addCondition = async (page, item, group) => {
  await page.getByRole('button', { name: group ? `Add condition to group ${group}` : 'Add condition', exact: true }).click();
  await page.getByRole('button', { name: item, exact: true }).click();
};
const ruleText = (page, path) => page.getByRole('textbox', { name: `Text for rule ${path}`, exact: true });

test('every text condition, property, option, and sort reaches the submitted search', async () => {
  const { browser, page, errors, requests } = await setup();
  try {
    await page.getByRole('button', { name: 'Search your library', exact: true }).click();
    await ruleText(page, '1.1').press('Enter');
    assert.equal(await page.evaluate(() => window.submitted), undefined); assert.deepEqual(requests, [], 'Enter waits for a searchable condition');
    const target = page.getByLabel('Search target', { exact: true });
    const targets = await target.locator('option').evaluateAll(options => options.map(option => option.value));
    for (const id of ['prose', 'user', 'assistant', 'reasoning', 'system', 'developer', 'tools', 'all', 'typed', 'pasted', 'quoted', 'resent', 'template', 'harness', 'automated', 'attachment', 'file', 'url']) assert.ok(targets.includes(id), `target ${id}`);
    await target.selectOption('user');

    await ruleText(page, '1.1').fill('parser migration');
    await addCondition(page, 'Words or phrase'); await ruleText(page, '1.2').fill('race condition');
    await page.getByLabel('Match type for rule 1.2', { exact: true }).selectOption('phrase');
    await addCondition(page, 'Words or phrase'); await ruleText(page, '1.3').fill('migrat');
    await page.getByLabel('Match type for rule 1.3', { exact: true }).selectOption('prefix');
    await addCondition(page, 'Words or phrase'); await page.getByLabel('Match type for rule 1.4', { exact: true }).selectOption('pre');
    await ruleText(page, '1.4').fill('lock');
    await page.getByLabel('Second phrase for rule 1.4', { exact: true }).fill('timeout');
    await page.getByLabel('Words between phrases in rule 1.4', { exact: true }).fill('5');
    await page.getByLabel('Match rules in group 1', { exact: true }).selectOption('any');
    await addCondition(page, 'Words to exclude'); await ruleText(page, '1.5').fill('flaky');
    await page.getByLabel('Match type for rule 1.5', { exact: true }).selectOption('anyWords');
    await page.getByRole('button', { name: 'Options for rule 1.3', exact: true }).click();
    await page.getByLabel('Include or exclude rule 1.3', { exact: true }).selectOption('exclude');
    await addCondition(page, 'Group of conditions'); await ruleText(page, '1.6.1').fill('deadlock');
    await addCondition(page, 'Words or phrase', '1.6'); await ruleText(page, '1.6.2').fill('mutex');
    await page.getByLabel('Match type for rule 1.6.2', { exact: true }).selectOption('near');
    await page.getByLabel('Second phrase for rule 1.6.2', { exact: true }).fill('wait');
    await page.getByLabel('Match rules in group 1.6', { exact: true }).selectOption('any');
    await addCondition(page, 'Excluded group', '1.6'); await ruleText(page, '1.6.3.1').fill('test');
    await page.getByRole('button', { name: 'Options for group 1.6.3', exact: true }).click();
    await page.getByLabel('Include or exclude group 1.6.3', { exact: true }).selectOption('include');
    await page.waitForFunction(() => document.activeElement?.getAttribute('aria-label') === 'Text for rule 1.6.3.1');
    await page.getByRole('button', { name: 'Options for group 1.6.3', exact: true }).click();
    await page.getByLabel('Include or exclude group 1.6.3', { exact: true }).selectOption('exclude');
    await addCondition(page, 'Group of conditions', '1.6.3'); await ruleText(page, '1.6.3.2.1').fill('deep');
    await page.getByRole('button', { name: 'Add condition to group 1.6.3.2', exact: true }).click();
    assert.equal(await page.getByRole('button', { name: 'Group of conditions', exact: true }).count(), 0, 'groups stop at the depth limit');
    await page.keyboard.press('Escape');
    await page.getByRole('button', { name: 'Remove group 1.6.3.2', exact: true }).click();
    await addCondition(page, 'Words or phrase'); await page.getByRole('button', { name: 'Remove rule 1.7', exact: true }).click();

    for (const item of ['Provider', 'Source', 'Repository', 'Date on or after', 'Date on or before', 'Conversation ID', 'Workspace ID']) await addCondition(page, item);
    await page.getByLabel('Provider', { exact: true }).selectOption('codex');
    await page.getByLabel('Source', { exact: true }).selectOption('conductor');
    await page.getByLabel('Repository', { exact: true }).fill('pharos');
    await page.getByLabel('Date on or after', { exact: true }).fill('2026-10-01');
    await page.getByLabel('Date on or before', { exact: true }).fill('2026-10-18');
    await page.getByLabel('Conversation ID', { exact: true }).fill('c1');
    await page.getByLabel('Workspace ID', { exact: true }).fill('w1');
    await page.getByRole('button', { name: 'Remove workspace id filter', exact: true }).click();

    await page.getByRole('button', { name: 'Add condition', exact: true }).click();
    await page.getByLabel('Workspace property', { exact: true }).selectOption('repository_name');
    await page.getByLabel('Filter operator 1', { exact: true }).selectOption('starts_with');
    await page.getByRole('textbox', { name: 'Filter value 1', exact: true }).fill('gbdubs/');
    await page.getByRole('button', { name: 'Options for filter 1', exact: true }).click();
    await page.getByRole('button', { name: 'Add OR alternative', exact: true }).click();
    await page.getByRole('textbox', { name: 'Filter value 1.2', exact: true }).fill('archive');
    await page.getByRole('button', { name: 'Options for filter 1.2', exact: true }).click();
    await page.getByRole('button', { name: 'Negate this condition', exact: true }).click();
    await page.getByRole('button', { name: 'Add condition', exact: true }).click();
    await page.getByLabel('Workspace property', { exact: true }).selectOption('title');
    await page.getByRole('button', { name: 'Remove filter 2', exact: true }).click();

    await page.getByRole('button', { name: /^Match options/ }).click();
    for (const name of ['Match letter case', 'Keep phrase punctuation and spacing', 'Include word variations']) await page.getByRole('checkbox', { name, exact: true }).check();
    await page.keyboard.press('Escape');
    assert.equal(await page.getByRole('button', { name: /^Match options/ }).getAttribute('aria-label'), 'Match options, 3 changed');

    await page.getByLabel('Sort field 1', { exact: true }).selectOption('turn_count');
    await page.getByLabel('Sort direction 1', { exact: true }).selectOption('asc');
    await page.getByLabel('Add sort field', { exact: true }).selectOption('activity_at');
    await page.getByLabel('Add sort field', { exact: true }).selectOption('title');
    await page.getByRole('button', { name: 'Remove sort 3', exact: true }).click();

    await page.getByRole('button', { name: 'Search messages', exact: true }).click();
    const { draft, query } = await submittedSearch(page);
    assert.equal(draft.scope.surface, 'user');
    const [words, phrase, prefix, ordered, excluded, group] = draft.root.children;
    assert.equal(draft.root.join, 'any');
    assert.deepEqual([words.kind, words.text, words.exclude], ['words', 'parser migration', false]);
    assert.deepEqual([phrase.kind, phrase.text], ['phrase', 'race condition']);
    assert.deepEqual([prefix.kind, prefix.text, prefix.exclude], ['prefix', 'migrat', true]);
    assert.deepEqual([ordered.kind, ordered.text, ordered.other, ordered.distance], ['pre', 'lock', 'timeout', '5']);
    assert.deepEqual([excluded.kind, excluded.text, excluded.exclude], ['anyWords', 'flaky', true]);
    assert.equal(draft.root.children.length, 6);
    assert.deepEqual([group.kind, group.join, group.children.length], ['group', 'any', 3]);
    assert.deepEqual([group.children[1].kind, group.children[1].other], ['near', 'wait']);
    assert.deepEqual([group.children[2].kind, group.children[2].exclude, group.children[2].children.map(child => child.text)], ['group', true, ['test']]);
    assert.deepEqual([draft.scope.provider, draft.scope.source, draft.scope.repository, draft.scope.from, draft.scope.to, draft.scope.conversation_id, draft.scope.workspace_id], ['codex', 'conductor', 'pharos', '2026-10-01', '2026-10-18', 'c1', '']);
    assert.deepEqual([draft.caseSensitive, draft.separators, draft.fuzzy], [true, true, true]);
    assert.equal(query.where.length, 1);
    const [first, second] = query.where[0].any;
    assert.deepEqual([first.field, first.op, first.value], ['repository_name', 'starts_with', 'gbdubs/']);
    assert.equal(second.value, 'archive'); assert.ok(second.negated || second.op !== 'starts_with', 'the alternative is negated');
    assert.deepEqual(query.orderBy.map(term => [term.field, term.dir]), [['turn_count', 'asc'], ['activity_at', 'asc']]);

    await page.getByRole('button', { name: 'Clear', exact: true }).click();
    assert.equal(await ruleText(page, '1.1').inputValue(), '');
    assert.equal(await page.getByLabel('Search target', { exact: true }).inputValue(), 'prose');
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

test('exact text, regular expressions, query formats, and file and URL targets remain available', async () => {
  const { browser, page, errors } = await setup();
  try {
    await page.getByRole('button', { name: 'Search your library', exact: true }).click();
    const options = page.getByRole('button', { name: /^Match options/ });
    await options.click(); await page.getByLabel('Match using', { exact: true }).selectOption('literal'); await page.keyboard.press('Escape');
    const exact = page.getByRole('textbox', { name: 'Text to find exactly', exact: true });
    await exact.fill('   ');
    assert.equal(await page.getByRole('button', { name: 'Search messages', exact: true }).isDisabled(), true, 'blank exact text is not a search');
    await exact.fill(' foo::bar ');
    assert.match(await page.getByRole('button', { name: 'Collapse search', exact: true }).innerText(), /the exact text “ foo::bar ”/);
    await page.getByRole('button', { name: 'Search messages', exact: true }).click();
    let { draft } = await submittedSearch(page);
    assert.deepEqual([draft.editor, draft.match, draft.text], ['visual', 'literal', ' foo::bar ']);
    await options.click(); await page.getByLabel('Match using', { exact: true }).selectOption('regex'); await page.keyboard.press('Escape');
    await page.getByRole('textbox', { name: 'Regular expression', exact: true }).fill('error[0-9]+');
    await page.getByRole('button', { name: 'Search messages', exact: true }).click();
    ({ draft } = await submittedSearch(page));
    assert.deepEqual([draft.match, draft.text], ['regex', 'error[0-9]+']);
    await options.click(); await page.getByLabel('Match using', { exact: true }).selectOption('rules');
    await page.getByRole('checkbox', { name: 'Include word variations', exact: true }).check(); await page.keyboard.press('Escape');
    await ruleText(page, '1.1').fill('cache');
    await page.getByRole('button', { name: 'Query syntax', exact: true }).click();
    await page.getByRole('alert').filter({ hasText: 'Turn off “Include word variations”' }).waitFor();
    await options.click(); await page.getByRole('checkbox', { name: 'Include word variations', exact: true }).uncheck(); await page.keyboard.press('Escape');
    await page.getByRole('button', { name: 'Query syntax', exact: true }).click();
    await page.getByRole('textbox', { name: 'Search query', exact: true }).fill('cache OR buffer');
    await options.click();
    const formats = await page.getByLabel('Query format', { exact: true }).locator('option').evaluateAll(list => list.map(option => option.value));
    assert.deepEqual(formats, ['advanced', 'terms', 'phrase', 'literal', 'regex']);
    await page.keyboard.press('Escape');
    await page.getByRole('button', { name: 'Search messages', exact: true }).click();
    ({ draft } = await submittedSearch(page));
    assert.deepEqual([draft.editor, draft.mode, draft.text], ['syntax', 'advanced', 'cache OR buffer']);
    for (const kind of ['file', 'url']) {
      await page.getByLabel('Search target', { exact: true }).selectOption(kind);
      assert.equal(await page.getByRole('button', { name: 'Query syntax', exact: true }).count(), 0, 'files and URLs have no query syntax');
      await page.getByRole('textbox', { name: 'Search library', exact: true }).fill(kind === 'file' ? 'parser.go' : 'example.com');
      await options.focus(); await page.keyboard.press('Enter');
      await page.waitForFunction(() => document.activeElement?.getAttribute('aria-label') === 'Match letter case');
      await page.keyboard.press('Space'); // on for files, then off again for URLs
      await page.keyboard.press('Escape');
      await page.getByRole('textbox', { name: 'Search library', exact: true }).press('Enter');
      const { url } = await submittedSearch(page);
      assert.deepEqual([url.searchParams.get('kind'), url.searchParams.get('search'), url.searchParams.get('case')], [kind, kind === 'file' ? 'parser.go' : 'example.com', kind === 'file' ? '1' : null]);
    }
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});

test('record search adds, alternates, negates, removes, sorts, clears, and applies conditions', async () => {
  const { browser, page, errors } = await setup({ view: 'record' });
  try {
    await page.getByRole('button', { name: 'Add condition', exact: true }).click();
    const fields = await page.locator('.text-add-popup button').allTextContents();
    assert.equal(fields[0], 'Command', 'the searched text field comes first'); assert.ok(fields.length > 5);
    await page.getByRole('button', { name: 'Command', exact: true }).click();
    await page.getByRole('textbox', { name: 'Filter value 1', exact: true }).fill('go test');
    await addCondition(page, 'Tool');
    await page.getByLabel('Filter operator 2', { exact: true }).selectOption('=');
    const tool = page.getByLabel('Filter value 2', { exact: true });
    await (await tool.evaluate(element => element.tagName) === 'SELECT' ? tool.selectOption({ index: 1 }) : tool.fill('Bash'));
    await page.getByRole('button', { name: 'Options for filter 2', exact: true }).click();
    await page.getByRole('button', { name: 'Add OR alternative', exact: true }).click();
    await page.getByRole('button', { name: 'Options for filter 1', exact: true }).click();
    await page.getByRole('button', { name: 'Negate this condition', exact: true }).click();
    await page.getByRole('button', { name: 'Remove filter 2.2', exact: true }).click();
    await page.getByLabel('Sort direction 1', { exact: true }).selectOption('asc');
    await page.getByRole('button', { name: 'Search tool calls', exact: true }).click();
    await page.waitForFunction(() => window.recordQuery);
    const query = await page.evaluate(() => window.recordQuery);
    assert.equal(query.where.length, 2);
    assert.equal(query.where[0].field, 'command'); assert.ok(query.where[0].negated || query.where[0].op !== 'contains', 'the command condition is negated');
    assert.equal(query.where[1].field ?? query.where[1].any?.[0].field, 'tool_name');
    assert.equal(query.orderBy[0].dir, 'asc');
    await page.getByRole('button', { name: 'Clear', exact: true }).click();
    await page.getByText('All tool calls. Add a condition to narrow them.').waitFor();
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});
