#!/usr/bin/env node
// Time transcript reader interactions on a real conversation page.
// Uses web/'s locked Playwright installation. The Mac app renders with WebKit
// (`cd web && npx playwright-core install webkit`).
//
//   node tools/transcript-bench.cjs --url 'http://127.0.0.1:8799/work/<workspace>/conversation/<conversation>?token=…' \
//     [--engine webkit|chromium] [--steps open,scroll,top,scroll,close,find]
//
// A dev-ui URL with ?token= signs in. Steps avoid the Show filter, which is a
// library preference: changing it here would change it for the user.
const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');
const root = path.resolve(__dirname, '..');
const playwright = process.env.PLAYWRIGHT_MODULE ? require(process.env.PLAYWRIGHT_MODULE)
  : createRequire(path.join(root, 'web/package.json'))('playwright-core');
const args = process.argv.slice(2);
const option = (name, fallback) => args.includes(name) ? args[args.indexOf(name) + 1] : fallback;
const url = option('--url', '');
const engine = option('--engine', 'webkit');
const steps = option('--steps', 'open,scroll,top,scroll,close,find').split(',').filter(Boolean);

async function main() {
  if (!url) throw new Error('--url is required');
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await playwright[engine].launch({ headless: true, ...(engine === 'chromium' && fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
  const context = await browser.newContext({ viewport: { width: 1600, height: 1000 },
    ...(process.env.PHAROS_BENCH_TOKEN ? { extraHTTPHeaders: { Authorization: `Bearer ${process.env.PHAROS_BENCH_TOKEN}` } } : {}) });
  const page = await context.newPage();
  page.setDefaultTimeout(60_000);
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  // Main-thread stalls: gaps over 50 ms between animation frames.
  await page.addInitScript(() => {
    window.benchStalls = []; let last = performance.now();
    const frame = time => { if (time - last > 50) window.benchStalls.push(Math.round(time - last)); last = time; requestAnimationFrame(frame); };
    requestAnimationFrame(frame);
  });
  const started = Date.now();
  await page.goto(url);
  await page.locator('.conversation-panel').first().waitFor();
  await page.waitForLoadState('networkidle').catch(() => {});
  console.log(JSON.stringify({ engine, step: 'load', ms: Date.now() - started }));
  const painted = () => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  const actions = {
    open: () => page.getByRole('button', { name: 'Open turns', exact: true }).click(),
    close: () => page.getByRole('button', { name: 'Close turns', exact: true }).click(),
    scroll: async () => { await page.mouse.move(900, 600); for (let i = 0; i < 20; i++) { await page.mouse.wheel(0, 2000); await painted(); } },
    top: () => page.evaluate(() => { (document.querySelector('#detail .workspace-reader-content') || document.scrollingElement).scrollTop = 0; }),
    find: async () => { await page.getByRole('button', { name: 'Find', exact: true }).click(); await page.keyboard.type('the'); await page.waitForLoadState('networkidle'); await page.keyboard.press('Escape'); },
  };
  await painted();
  for (const step of steps) {
    if (!actions[step]) throw new Error(`unknown step ${step}`);
    await page.evaluate(() => { window.benchStalls.length = 0; });
    const start = Date.now();
    await actions[step]();
    await painted();
    const ms = Date.now() - start;
    const state = await page.evaluate(() => {
      const scroller = document.querySelector('#detail .workspace-reader-content') || document.scrollingElement;
      return { elements: document.getElementsByTagName('*').length, stalls: window.benchStalls.splice(0), scrollTop: Math.round(scroller.scrollTop), scrollHeight: scroller.scrollHeight };
    });
    console.log(JSON.stringify({ engine, step, ms, ...state }));
  }
  if (errors.length) throw new Error(errors.join('\n'));
  } finally { await browser.close(); }
}
main().catch(error => { console.error(error); process.exit(1); });
