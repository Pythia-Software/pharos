#!/usr/bin/env node
// Exercise the real Analyze controls against an isolated development service.
// Uses web/'s playwright-core (run `npm --prefix web ci` once) with the
// installed Chrome, like the browser tests.
const fs = require('node:fs');
const path = require('node:path');
const { createHash } = require('node:crypto');
const { createRequire } = require('node:module');
const root = path.resolve(__dirname, '..');
const { chromium } = process.env.PLAYWRIGHT_MODULE ? require(process.env.PLAYWRIGHT_MODULE)
  : createRequire(path.join(root, 'web/package.json'))('playwright-core');
const args = process.argv.slice(2);
const option = (name, fallback) => args.includes(name) ? args[args.indexOf(name) + 1] : fallback;
const origin = option('--url', 'http://127.0.0.1:8850');
const output = path.resolve(option('--output', '.context/usage-benchmark.json'));
const section = option('--section', 'all');

async function main() {
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  const context = await browser.newContext({ viewport: { width: 1600, height: 1200 },
    ...(process.env.PHAROS_BENCH_TOKEN ? { extraHTTPHeaders: { Authorization: `Bearer ${process.env.PHAROS_BENCH_TOKEN}` } } : {}) });
  const page = await context.newPage();
  page.setDefaultTimeout(15_000);
  const pending = new Set(), requests = [], actions = [], errors = [];
  let action = '';
  const relevant = url => /\/api\/query\/(usage|writing|writing_messages|tool_calls)\//.test(url) || url.includes('/api/usage/concurrency-prototype');
  const save = () => {
    fs.mkdirSync(path.dirname(output), { recursive: true });
    fs.writeFileSync(output, JSON.stringify({ actions, requests, errors }, null, 2) + '\n');
  };
  page.on('pageerror', error => errors.push({ action, error: error.message }));
  page.on('request', request => { if (relevant(request.url())) pending.add(request); });
  page.on('requestfailed', request => pending.delete(request));
  page.on('response', async response => {
    const request = response.request();
    if (!relevant(request.url())) return;
    const startedBy = action;
    try {
      await response.finished();
      const result = await response.json().catch(() => null);
      const url = new URL(request.url());
      const body = request.postDataJSON();
      // Record request fixtures and digests, never message text or result rows.
      if (result) { delete result.execution; delete result.now; }
      requests.push({ action: startedBy, path: url.pathname + url.search, body,
        ms: Math.round(request.timing().responseEnd), status: response.status(),
        error: result?.error, digest: createHash('sha256').update(JSON.stringify(result)).digest('hex') });
    } finally { pending.delete(request); }
  });
  async function run(name, change) {
    action = name;
    const start = Date.now(), first = requests.length;
    try {
      await change();
      // The table intentionally debounces edits for 100 ms. Include that time
      // and two paint frames, along with metadata, in the action measurement.
      await page.waitForTimeout(180);
      while (pending.size && Date.now() - start < 45_000) await page.waitForTimeout(25);
      if (pending.size) throw new Error('Analyze requests did not complete within 45 seconds');
      await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
      actions.push({ name, ms: Date.now() - start, requests: requests.length - first,
        heapMiB: await page.evaluate(() => Math.round((performance.memory?.usedJSHeapSize ?? 0) / 1048576)),
        errors: await page.locator('#analyze .query-table-error,#analyze .usage-chart-error,#analyze .machine-error').allTextContents() });
    } catch (error) { actions.push({ name, ms: Date.now() - start, error: error.message }); }
    save();
    console.log(`${name}: ${actions.at(-1).ms} ms${actions.at(-1).error ? ' FAILED' : ''}`);
  }
  async function selectOptions(prefix, selector, labels) {
    for (const label of labels) {
      const select = page.locator(selector).first().locator(`select[aria-label="${label}"]`);
      if (!await select.count() || await select.isDisabled()) continue;
      const choices = await select.locator('option').evaluateAll(options => options
        .filter(option => !option.disabled && option.value !== 'custom')
        .map(option => ({ value: option.value, label: option.textContent })));
      for (const choice of choices) await run(`${prefix}/${label}/${choice.label}`, () => select.selectOption(choice.value));
    }
  }
  const chart = '#analyze .usage-chart';
  const chartOptions = prefix => selectOptions(`${prefix}/chart`, chart, ['Metric', 'Breakdown', 'Display', 'Range', 'Scale', 'Time Bucket', 'Smoothing']);
  async function presets(prefix, selector) {
    const count = await page.locator(selector).count();
    for (let i = 0; i < count; i++) {
      const chip = page.locator(selector).nth(i);
      await run(`${prefix}/preset/${await chip.innerText()}`, () => chip.click());
    }
  }
  const open = (name, query) => run(`${name}/open`, () => page.goto(new URL(`/analyze?${query}`, origin).href));
  try {
    if (section !== 'time') {
    await open('tokens', 'analyze_view=tokens&machine_view=tokens');
    await presets('tokens', '#analyze [aria-label="Chart presets"] .query-chip');
    await chartOptions('tokens');
    await run('tokens/restore-metric', () => page.locator(chart).first().locator('select[aria-label="Metric"]').selectOption('tokens'));
    await selectOptions('tokens/chart', chart, ['Scale', 'Smoothing']);
    for (const days of ['7', '14', '30', '90', '0']) await run(`tokens/period/${days}`,
      () => page.getByRole('combobox', { name: 'Activity period', exact: true }).selectOption(days));
    for (const label of ['Activity repository', 'Activity provider', 'Activity model']) {
      const select = page.getByRole('combobox', { name: label, exact: true });
      const value = await select.locator('option').evaluateAll(options => options.find(option => !option.disabled && option.value)?.value);
      if (!value) continue;
      await run(`tokens/filter/${label}`, () => select.selectOption(value));
      await run(`tokens/clear/${label}`, () => select.selectOption(''));
    }
    await presets('tokens', '#analyze .usage-table-queries .query-chip');
    await open('writing', 'analyze_view=writing');
    await presets('writing', '#analyze .usage-presets .query-chip');
    await chartOptions('writing');
    await run('messages/open', () => page.getByRole('group', { name: 'List', exact: true }).getByRole('button', { name: 'Messages', exact: true }).click());
    await presets('messages', '#analyze .usage-presets .query-chip');
    await open('carbon', 'analyze_view=carbon');
    await chartOptions('carbon');
    await selectOptions('carbon/filters', '#analyze .carbon-filters', ['Repository', 'Provider']);
    await selectOptions('carbon/assumptions', '#carbonAssumptions', ['Electricity grid']);
    for (const label of ['PUE', 'Estimate', 'Input sensitivity']) {
      const group = page.getByRole('group', { name: label, exact: true });
      const names = await group.getByRole('button').allTextContents();
      for (const name of names) await run(`carbon/${label}/${name}`, () => group.getByRole('button', { name, exact: true }).click());
    }
    }
    await open('time', 'analyze_view=tokens&machine_view=time');
    for (const days of ['7', '14', '30', '90', '0']) await run(`time/period/${days}`,
      () => page.getByRole('combobox', { name: 'Activity period', exact: true }).selectOption(days));
    await selectOptions('time', '#analyze', ['Analytics grouping', 'Trend measure']);
    for (const measure of ['Hours', 'Share']) await run(`time/repository-measure/${measure}`, () =>
      page.getByRole('group', { name: 'Repository chart measure', exact: true }).getByRole('button', { name: measure, exact: true }).click());
    const family = page.getByRole('combobox', { name: 'Trend command family', exact: true });
    const families = await family.locator('option').evaluateAll(options => options.filter(option => option.value).slice(0, 3).map(option => ({ value: option.value, label: option.textContent })));
    for (const choice of families) {
      await run(`time/command-family/${choice.label}`, () => family.selectOption(choice.value));
      await run('time/clear-command-family', () => family.selectOption(''));
    }
    const allKindNames = [...new Set(await page.locator('.machine-activity-legend button').allTextContents())];
    // Every built-in category plus representative recorded command names.
    // A real catalog can contain thousands of arbitrary program identities.
    const builtInKinds = new Set(['Tests & checks', 'Builds', 'Reading & search', 'Editing', 'Dependencies', 'Git', 'Delegation', 'Planning', 'Human input', 'Tool discovery', 'Network operations', 'Process management']);
    const kindNames = [...new Set([...allKindNames.filter(name => builtInKinds.has(name)), ...allKindNames.filter(name => !builtInKinds.has(name)).slice(0, 3)])];
    for (const name of kindNames) {
      await run(`time/activity/${name}`, () => page.locator('.machine-activity-legend').getByRole('button', { name, exact: true }).first().click());
      await run('time/all-activity', () => page.locator('.machine-breadcrumb').getByRole('button', { name: 'All activity', exact: true }).click());
    }
    await run('time/next-raw-call-page', async () => {
      const button = page.locator('.machine-call-surface').getByRole('button', { name: 'Next page', exact: true });
      if (await button.count() && await button.isEnabled()) await button.click();
    });
    await run('time/example-data', () => page.getByRole('combobox', { name: 'Activity data source', exact: true }).selectOption('example'));
    await run('time/library-data', () => page.getByRole('combobox', { name: 'Activity data source', exact: true }).selectOption('library'));
    if (await page.locator('html[data-concurrency-prototype]').count()) {
      await run('parallelism/open', async () => {
        await page.goto(new URL('/analyze?analyze_view=tokens&machine_view=parallelism&activity_days=0', origin).href);
        await page.locator('.concurrency-chart svg').waitFor();
      });
      await run('parallelism/threaded-view', () => page.getByRole('combobox', { name: 'View', exact: true }).selectOption('user'));
      await selectOptions('parallelism', '#analyze', ['Turn gap']);
      for (const counting of ['workspace', 'agent']) {
        await run(`parallelism/count/${counting}`, () => page.getByRole('combobox', { name: 'Count by', exact: true }).selectOption(counting));
        await selectOptions(`parallelism/${counting}`, '#analyze', ['View']);
      }
    }
    await run('parallelism/example-data', () => page.getByRole('combobox', { name: 'Activity data source', exact: true }).selectOption('example'));
    await run('parallelism/library-data', () => page.getByRole('combobox', { name: 'Activity data source', exact: true }).selectOption('library'));
    if (actions.some(action => action.error || action.errors?.length) || requests.some(request => request.status >= 400) || errors.length) process.exitCode = 1;
  } finally { await browser.close(); save(); }
}

main().catch(error => { console.error(error.message); process.exitCode = 1; });
