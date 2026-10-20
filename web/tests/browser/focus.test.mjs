// Focus and placement that only a real browser can check: modal surfaces take
// focus, keep it while open and give it back on Escape; the transcript rails
// stay below the header; the annotation toolbar stays on screen.
import { test } from "node:test";
import assert from "node:assert/strict";
import { limits, open } from "./harness.mjs";
import { workID } from "./fixtures.mjs";

// Focus is in `surface`, or with the browser itself (a native modal dialog
// lets Tab leave the page past its last control), never on the page behind.
const kept = surface => surface.evaluate(element => { const active = document.activeElement; return !active || active === document.body || element.contains(active); });

// Opens a surface from the keyboard, as a keyboard user would (WebKit, like
// Safari, does not focus a button that is clicked).
async function press(opener, key = "Enter") {
  await opener.focus();
  await opener.page().keyboard.press(key);
}

// Focus moves into `surface` and stays there while Tab runs past its end and
// Shift+Tab past its start; Escape closes it and focus returns to `opener`.
async function holdsFocus(page, surface, opener) {
  await page.waitForFunction(element => element.contains(document.activeElement), await surface.elementHandle());
  for (const key of [...Array(12).fill("Tab"), ...Array(4).fill("Shift+Tab")]) {
    await page.keyboard.press(key);
    assert.ok(await kept(surface), `focus moved behind the surface after ${key}`);
  }
  await page.keyboard.press("Escape");
  await surface.waitFor({ state: "hidden" });
  await page.waitForFunction(element => document.activeElement === element, await opener.elementHandle());
}

test("quick search takes focus from the header button and from the keyboard shortcut, and returns it", limits, async t => {
  const { page } = await open(t, { url: "/library", ready: "#queryTableLibrary .qt-row" });
  const dialog = page.locator("#searchDialog"), button = page.locator("#searchToggle");
  await press(button);
  await holdsFocus(page, dialog, button);
  const tab = page.locator('nav.tabs button[data-view="usage"]');
  await press(tab, "ControlOrMeta+k");
  await holdsFocus(page, dialog, tab);
});

test("the card template editor takes focus, keeps it and returns it on Escape", limits, async t => {
  const { page } = await open(t, { url: "/home?cards=library", ready: ".card-library-entry .query-card" });
  const edit = page.locator(".card-library-entry button[aria-label^='Edit card']").first();
  await press(edit);
  await holdsFocus(page, page.locator("dialog.card-template-editor"), edit);
});

test("the findings prompt review takes focus, keeps it and returns it on Escape", limits, async t => {
  const { page, settled } = await open(t, { url: "/findings", ready: ".findings-card" });
  const review = page.locator(".findings-cart button.primary").first();
  await press(review);
  const dialog = page.locator('.findings-dialog[role="dialog"]');
  await dialog.waitFor();
  await settled();
  await holdsFocus(page, dialog, review);
});

test("the delegation session sheet on a phone takes focus, keeps it and returns it on Escape", limits, async t => {
  const { page } = await open(t, { url: `/work/${workID}/conversation/conv-child`, viewport: { width: 390, height: 844 }, ready: ".conversation-panel .turn" });
  const show = page.locator('button[aria-label="Show workspace sessions"]');
  await press(show);
  const sheet = page.locator('[role="dialog"][aria-label="Workspace sessions"]');
  await sheet.waitFor();
  await holdsFocus(page, sheet, show);
});

test("transcript rails start below the header, with the navigation bar open, after main scrolls", limits, async t => {
  const { page, settled } = await open(t, { url: `/work/${workID}/conversation/conv-root`, ready: ".conversation-panel .turn", preferences: { "pharos-show-nav-button": true } });
  await page.locator("#navigatorToggle").click();
  await page.locator("#navigator.open").waitFor();
  await page.evaluate(() => document.querySelector("main").scrollTo(0, 1200));
  await page.locator(".conversation-map.visible").waitFor();
  await settled();
  const tops = await page.evaluate(() => {
    const header = document.querySelector("body > header").getBoundingClientRect().bottom;
    return { header, rails: [...document.querySelectorAll(".conversation-map, .token-map, .conversation-find-rail, .conversation-map-window")].filter(rail => rail.getClientRects().length).map(rail => [rail.className, rail.getBoundingClientRect().top]) };
  });
  assert.ok(tops.rails.length, "the rails are shown");
  for (const [name, top] of tops.rails) assert.ok(top >= tops.header - 1, `${name} starts at ${top}, above the header's bottom at ${tops.header}`);
});

// The toolbar repositions itself after a resize; waits for it to settle and
// returns true, or the box that stayed off screen.
const onScreen = locator => locator.evaluate(element => new Promise(resolve => {
  const end = performance.now() + 2000;
  const check = () => {
    const box = element.getBoundingClientRect(), fits = box.left >= 0 && box.top >= 0 && box.right <= innerWidth && box.bottom <= innerHeight;
    if (fits || performance.now() > end) resolve(fits || [box.left, box.top, box.right, box.bottom].map(Math.round).join(", "));
    else requestAnimationFrame(check);
  };
  check();
}));

async function annotate(t) {
  const app = await open(t, { url: "/settings", ready: "#healthCards .panel", preferences: { "pharos-show-agent-annotation-button": true } });
  await app.page.locator("#feedbackToggle").click();
  await app.page.locator("#feedbackTarget").click();
  await app.page.locator("#healthHeading").click();
  await app.page.locator(".feedback-composer").waitFor();
  return app.page;
}

test("the annotation toolbar and composer stay inside the viewport after a resize", limits, async t => {
  const page = await annotate(t);
  await page.setViewportSize({ width: 580, height: 460 });
  for (const surface of [".feedback-toolbar", ".feedback-composer"]) assert.equal(await onScreen(page.locator(surface)), true, `${surface} off screen at 580×460`);
});

test("the expanded feedback panel stays inside a 360px viewport", limits, async t => {
  const page = await annotate(t);
  await page.locator("#feedbackNote").fill("Annotation");
  // Adding an annotation expands the toolbar into the feedback panel.
  await page.locator("#feedbackAdd").click();
  await page.locator("#feedbackPanel.open").waitFor();
  await page.setViewportSize({ width: 360, height: 420 });
  const box = await onScreen(page.locator(".feedback-toolbar"));
  assert.equal(box, true, `expanded toolbar at [${box}] in 360×420`);
});
