// The browser and e2e tiers' global setup (`--test-global-setup`, see
// web/tests/run.mjs): one browser for the whole run, reached through
// PHAROS_BROWSER_WS by every test process, each test in its own context.
// Without an installed engine there is no browser and the tests skip with an
// install hint.
import { engine, launchPlan, startBrowser } from "./engine.mjs";

let browser = null;

export async function globalSetup() {
  const { skip } = launchPlan();
  if (skip) return console.log(`# ${engine} tests skip: ${skip}`);
  browser = await startBrowser();
  process.env.PHAROS_BROWSER_WS = browser.wsEndpoint();
}

export async function globalTeardown() {
  await browser?.close();
}
