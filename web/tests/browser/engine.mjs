// Which browser the browser and e2e tiers drive, and how to reach it.
//
// PHAROS_BROWSER picks the engine: `chromium` (the default) uses CHROME_PATH,
// else Google Chrome, else Playwright's own Chromium; `webkit` uses
// Playwright's WebKit. The runner's global setup (./setup.mjs) starts one
// browser per run with `startBrowser` and passes its endpoint in
// PHAROS_BROWSER_WS; a test file run without it launches one for its process.
import { existsSync } from "node:fs";
import { chromium, webkit } from "playwright-core";

export const engine = process.env.PHAROS_BROWSER || "chromium";
const types = { chromium, webkit };

// How to launch the engine, or `{ skip }` with how to install it.
export function launchPlan() {
  if (engine === "webkit") {
    return existsSync(webkit.executablePath()) ? { type: webkit, options: {} }
      : { skip: "WebKit is not installed. Install it with `npx playwright-core install webkit` in web/." };
  }
  if (engine !== "chromium") return { skip: `Unknown PHAROS_BROWSER "${engine}"; use chromium or webkit.` };
  const chrome = [process.env.CHROME_PATH, "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"].find(path => path && existsSync(path));
  if (chrome) return { type: chromium, options: { executablePath: chrome } };
  if (existsSync(chromium.executablePath())) return { type: chromium, options: {} };
  return { skip: "No Chromium found. Install Google Chrome, set CHROME_PATH, or run `npx playwright-core install chromium` in web/." };
}

// A browser server for a whole run; its wsEndpoint() goes in PHAROS_BROWSER_WS.
export function startBrowser() {
  const plan = launchPlan();
  return plan.skip ? Promise.resolve(null) : plan.type.launchServer({ headless: true, ...plan.options });
}

// The run's browser, or one launched for this process.
export function connectBrowser() {
  const plan = launchPlan();
  if (plan.skip) throw new Error(plan.skip);
  return process.env.PHAROS_BROWSER_WS ? types[engine].connect(process.env.PHAROS_BROWSER_WS) : plan.type.launch({ headless: true, ...plan.options });
}
