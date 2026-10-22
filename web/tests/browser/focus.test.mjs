// Focus and placement that only a real browser can check: modal surfaces take
// focus, keep it while open and give it back on Escape; the transcript rails
// stay below the header; the annotation toolbar stays on screen.
import { test } from "node:test";
import assert from "node:assert/strict";
import { activateHeaderTool, open, skip } from "./harness.mjs";
import { workID } from "./fixtures.mjs";

test("summary value links retain keyboard focus when the summary rerenders", { skip }, async t => {
  const { page } = await open(t, { url: "/library", ready: "#queryTableLibrary .qt-row", preferences: { "pharos-usage-summary-links": true } });
  await activateHeaderTool(page, "usageSummaryToggle");
  const link = page.getByRole("link", { name: /^Tokens, 6h:/ });
  await link.focus();
  await page.evaluate(() => window.dispatchEvent(new Event("pharos:carbon-settings")));
  await page.waitForFunction(() => document.activeElement?.getAttribute("aria-label")?.startsWith("Tokens, 6h:"));
  await page.keyboard.press("Escape");
  await page.locator("#usageSummary").waitFor({ state: "hidden" });
  await page.waitForFunction(() => document.activeElement?.id === "usageSummaryToggle");
});

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

// Exercise portal focus and dismissal against both native and role-based modals.
async function useSearchMenus(page, dialog) {
  const options = dialog.getByRole("button", { name: /^Match options/ });
  await press(options);
  const optionsPopup = page.getByRole("dialog", { name: /^Match options/ });
  await page.waitForFunction(element => element.contains(document.activeElement), await optionsPopup.elementHandle());
  const matchCase = optionsPopup.getByRole("checkbox", { name: "Match letter case", exact: true });
  await matchCase.check();
  assert.equal(await matchCase.isChecked(), true);
  await optionsPopup.getByText("Match letter case", { exact: true }).click();
  assert.equal(await matchCase.isChecked(), false);
  await matchCase.focus();
  await page.keyboard.press("Space");
  assert.equal(await matchCase.isChecked(), true);
  await page.keyboard.press("Escape");
  await optionsPopup.waitFor({ state: "hidden" });
  assert.equal(await dialog.isVisible(), true);
  await page.waitForFunction(element => document.activeElement === element, await options.elementHandle());

  const add = dialog.getByRole("button", { name: "Add condition", exact: true });
  await press(add);
  const addPopup = page.getByRole("dialog", { name: "Add condition", exact: true });
  await page.waitForFunction(element => element.contains(document.activeElement), await addPopup.elementHandle());
  await page.keyboard.press("Tab");
  assert.ok(await addPopup.evaluate(element => element.contains(document.activeElement)), "keyboard focus left the condition menu");
  await page.keyboard.press("Escape");
  await addPopup.waitFor({ state: "hidden" });
  assert.equal(await dialog.isVisible(), true);
  await page.waitForFunction(element => document.activeElement === element, await add.elementHandle());
}

test("quick search takes focus from the header button and from the keyboard shortcut, and returns it", { skip }, async t => {
  const { page } = await open(t, { url: "/library", ready: "#queryTableLibrary .qt-row" });
  const dialog = page.locator("#searchDialog"), button = page.locator("#searchToggle");
  await press(button);
  await useSearchMenus(page, dialog);
  await holdsFocus(page, dialog, button);
  const tab = page.getByRole('navigation', { name: 'Primary navigation', exact: true }).getByRole('button', { name: 'Analyze', exact: true });
  await press(tab, "ControlOrMeta+k");
  await holdsFocus(page, dialog, tab);
});

for (const viewport of [{ width: 2133, height: 1075 }, { width: 1440, height: 900 }, { width: 1024, height: 768 }, { width: 700, height: 768 }]) {
  test(`header page buttons remain reachable and navigate at ${viewport.width}px`, { skip }, async t => {
    const { page } = await open(t, { url: '/analyze', viewport, ready: '.usage-dashboard .usage-chart' });
    const navigation = page.getByRole('navigation', { name: 'Primary navigation', exact: true });
    for (const button of await navigation.getByRole('button').all()) {
      await button.focus();
      assert.equal(await onScreen(button), true, `${await button.textContent()} is outside the viewport`);
    }
    await press(navigation.getByRole('button', { name: 'Search', exact: true }));
    await page.locator('#queryTableLibrary .qt-row').first().waitFor();
    assert.equal(new URL(page.url()).pathname, '/library');
    await press(navigation.getByRole('button', { name: 'Analyze', exact: true }));
    await page.locator('.usage-dashboard .usage-chart').first().waitFor();
    assert.equal(new URL(page.url()).pathname, '/analyze');
  });
}

for (const viewport of [{ width: 2133, height: 1075 }, { width: 1440, height: 900 }, { width: 1280, height: 720 }, { width: 1024, height: 768 }]) {
  test(`section sidebar collapses, restores space and navigates at ${viewport.width}px`, { skip }, async t => {
    const { page } = await open(t, { url: '/analyze', viewport, ready: '.usage-dashboard .usage-chart' });
    const sidebar = page.getByRole('complementary', { name: 'Analyze navigation', exact: true });
    const heading = page.getByRole('heading', { name: 'Tokens and Cost', exact: true });
    await sidebar.waitFor();
    const sidebarBox = await sidebar.boundingBox(), expanded = await heading.boundingBox();
    assert.ok(expanded.x >= sidebarBox.x + sidebarBox.width, 'sidebar overlaps the page');
    const pageBox = await page.locator('main>.view.active').boundingBox();
    assert.ok(Math.abs(sidebarBox.x + sidebarBox.width - pageBox.x) < 1, 'sidebar stands apart from the content');
    await press(page.getByRole('button', { name: 'Collapse sidebar', exact: true }));
    await sidebar.waitFor({ state: 'hidden' });
    await page.waitForFunction(left => document.querySelector('main .view.active h1').getBoundingClientRect().left < left, expanded.x);
    const expand = page.getByRole('button', { name: 'Expand sidebar', exact: true });
    await page.waitForFunction(element => document.activeElement === element, await expand.elementHandle());
    const expandBox = await expand.boundingBox(), collapsedHeading = await heading.boundingBox();
    assert.ok(expandBox.x + expandBox.width <= collapsedHeading.x, 'sidebar control overlaps the page heading');
    if (await page.evaluate(() => { const main = document.querySelector('main'); return main.scrollHeight > main.clientHeight; })) {
      await page.evaluate(() => document.querySelector('main').scrollTo(0, 1200));
      await page.waitForFunction(() => document.querySelector('main').scrollTop > 0);
    }
    assert.equal(await onScreen(expand), true, 'sidebar control scrolls out of reach');
    assert.ok(await expand.evaluate(element => {
      const box = element.getBoundingClientRect();
      return element.contains(document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2));
    }), 'sidebar control is covered by page content');
    await press(expand);
    await sidebar.waitFor();
    await press(sidebar.getByRole('link', { name: /^Human words$/i }));
    await page.locator('.usage-dashboard .writing-panel .usage-chart').waitFor();
    await page.waitForFunction(() => document.activeElement?.tagName === 'H1');
    assert.equal(await sidebar.getByRole('link', { name: /^Human words$/i }).getAttribute('aria-current'), 'page');
  });
}

test('navigation keeps focus reachable when resizing between a sidebar and phone switcher', { skip }, async t => {
  const { page } = await open(t, { url: '/analyze', viewport: { width: 1280, height: 720 }, ready: '.usage-dashboard .usage-chart' });
  const sidebar = page.getByRole('complementary', { name: 'Analyze navigation', exact: true });
  await sidebar.getByRole('link', { name: /^Human words$/i }).focus();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForFunction(() => document.activeElement === document.getElementById('appNavigationToggle'));
  await press(page.locator('#appNavigationToggle'));
  await page.getByRole('dialog', { name: 'Navigate Pharos' }).waitFor();
  await page.setViewportSize({ width: 1024, height: 768 });
  await sidebar.waitFor();
  await page.waitForFunction(() => document.activeElement === document.getElementById('appSidebarToggle'));
  await press(page.getByRole('button', { name: 'Collapse sidebar', exact: true }));
  await page.setViewportSize({ width: 390, height: 844 });
  await page.locator('#appNavigationToggle').waitFor();
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.getByRole('button', { name: 'Expand sidebar', exact: true }).waitFor();
  await press(page.getByRole('button', { name: 'Expand sidebar', exact: true }));
  await sidebar.waitFor();
});

for (const width of [1440, 390, 320]) test(`Search content search takes focus and returns it to the query builder @ ${width}`, { skip }, async t => {
  const { page } = await open(t, { url: "/library", viewport: { width, height: 844 }, ready: "#queryTableLibrary .qt-row" });
  const button = page.getByRole("button", { name: "Messages", exact: true });
  await press(button);
  const dialog = page.getByRole("dialog", { name: "Search within work", exact: true });
  await useSearchMenus(page, dialog);
  await holdsFocus(page, dialog, button);
});

for (const width of [1440, 390, 320]) test(`Search long query can scroll to submit @ ${width}`, { skip }, async t => {
  const draft = {
    editor: "visual", match: "rules", text: "", mode: "advanced", fuzzy: false, caseSensitive: false, separators: false,
    scope: { surface: "prose", roles: [], kinds: [], repository: "", provider: "", source: "", from: "", to: "", conversation_id: "", workspace_id: "" },
    root: { id: "long-query", kind: "group", join: "all", exclude: false,
      children: Array.from({ length: 12 }, (_, index) => ({ id: `rule-${index}`, kind: "words", text: `condition ${index + 1}`, other: "", distance: "8", exclude: false })) },
  };
  const app = await open(t, { url: `/library?${new URLSearchParams({ text_builder: JSON.stringify(draft) })}`, viewport: { width, height: 600 }, ready: "#queryTableLibrary .qt-row",
    api: { "POST /api/search/text/validate": ({ body }) => ({ where: body.where }) } });
  const { page } = app;
  await press(page.getByRole("button", { name: "Edit messages search", exact: true }));
  const dialog = page.getByRole("dialog", { name: "Search within work", exact: true });
  const heading = dialog.getByRole("heading", { name: "Search within work", exact: true });
  const inViewport = element => { const rect = element.getBoundingClientRect(); return rect.top >= 0 && rect.bottom <= innerHeight; };
  assert.ok(await heading.evaluate(inViewport), "the modal heading is outside the viewport");
  const bounds = await dialog.boundingBox();
  await page.mouse.move(bounds.x + bounds.width / 2, bounds.y + bounds.height / 2);
  await page.mouse.wheel(0, 2400);
  const submit = dialog.getByRole("button", { name: "Search messages", exact: true });
  await page.waitForFunction(inViewport, await submit.elementHandle());
  assert.ok(await heading.evaluate(inViewport), "scrolling hid the modal heading");
  await app.shot("scrolled-to-submit");
  await submit.click();
  await dialog.waitFor({ state: "hidden" });
  await app.settled();
});

for (const route of [
  { name: "Analyze", url: "/analyze", ready: ".usage-dashboard .usage-chart", link: /^Human words$/i, destination: ".usage-dashboard .writing-panel .usage-chart" },
  { name: "Settings", url: "/settings", ready: "#healthCards .panel:not([aria-busy])", link: /^Sources$/, destination: "#sourceGrid .source-card" },
  { name: "TL1", url: "/tl1", ready: ".tl1-tiles", link: /^Configurations$/, destination: "#tl1-matrix" },
]) test(`${route.name} navigation is usable from the keyboard on a phone`, { skip }, async t => {
  const { page } = await open(t, { url: route.url, viewport: { width: 390, height: 844 }, ready: route.ready });
  const menu = page.getByRole("dialog", { name: "Navigate Pharos" });
  await press(page.locator("#appNavigationToggle"));
  const target = menu.getByRole("link", { name: route.link });
  await target.waitFor();
  await press(target);
  await page.locator(route.destination).first().waitFor();
  await page.waitForFunction(() => document.activeElement?.tagName === "H1");
  assert.equal(await menu.isVisible(), false);
  await page.goBack();
  await page.locator(route.ready).first().waitFor();
});

for (const viewport of [{ width: 390, height: 844 }, { width: 320, height: 640 }]) {
  test(`app navigation takes and returns focus, and reaches other app sections at ${viewport.width}px`, { skip }, async t => {
    const { page } = await open(t, { url: '/analyze', viewport, ready: '.usage-dashboard .usage-chart' });
    const opener = page.locator('#appNavigationToggle'), dialog = page.getByRole('dialog', { name: 'Navigate Pharos' });
    await press(opener);
    await holdsFocus(page, dialog, opener);
    await press(opener);
    await press(dialog.getByRole('button', { name: 'All pages', exact: true }));
    await press(dialog.getByRole('button', { name: 'Settings', exact: true }));
    await press(dialog.getByRole('link', { name: 'Sources', exact: true }));
    await page.locator('#sourceGrid .source-card').first().waitFor();
    await page.waitForFunction(() => document.activeElement?.tagName === 'H1');
    assert.equal(new URL(page.url()).pathname, '/settings/sources');
    await page.goBack();
    await page.locator('.usage-dashboard .usage-chart').waitFor();
    assert.equal(await opener.getAttribute('aria-expanded'), 'false');
  });
}

for (const viewport of [{ width: 850, height: 720 }, { width: 390, height: 844 }]) {
  test(`overflowed header tools remain live and restore focus at ${viewport.width}px`, { skip }, async t => {
    const { page } = await open(t, { url: '/library', viewport, ready: '#queryTableLibrary .qt-row',
      preferences: { 'pharos-header-tools-v1': ['summary', 'theme', 'history', 'library', 'search', 'settings'] },
    });
    const more = page.getByRole('button', { name: 'More tools', exact: true });
    await press(more);
    const popup = page.getByRole('dialog', { name: 'Header tools' });
    await popup.waitFor();
    await page.keyboard.press('Escape');
    await popup.waitFor({ state: 'hidden' });
    await page.waitForFunction(element => document.activeElement === element, await more.elementHandle());
    const theme = await page.locator('html').getAttribute('data-theme');
    await activateHeaderTool(page, 'themeToggle');
    assert.notEqual(await page.locator('html').getAttribute('data-theme'), theme);
    await activateHeaderTool(page, 'usageSummaryToggle');
    await page.locator('#usageSummary').waitFor();
    await page.keyboard.press('Escape');
    await page.locator('#usageSummary').waitFor({ state: 'hidden' });
    await page.waitForFunction(element => document.activeElement === element, await more.elementHandle());
  });
}

for (const tools of [['settings'], ['back', 'sync', 'settings']]) {
  test(`header respects saved tool visibility with ${tools.join(', ')}`, { skip }, async t => {
    const { page, settled } = await open(t, {
      url: '/library', ready: '#queryTableLibrary .qt-row',
      preferences: { 'pharos-header-tools-v1': tools },
    });
    // Give history a previous page so an accidentally exposed Back is enabled.
    await press(page.getByRole('navigation', { name: 'Primary navigation', exact: true }).getByRole('button', { name: 'Analyze', exact: true }));
    await page.locator('.usage-dashboard .usage-chart').first().waitFor();
    for (const width of [1440, 1280, 390]) {
      await page.setViewportSize({ width, height: 900 });
      await settled();
      const more = page.getByRole('button', { name: 'More tools', exact: true }), overflow = await more.isVisible();
      if (overflow) await press(more);
      for (const [tool, id] of [['history', 'headerHistory'], ['back', 'headerBackOnly'], ['library', 'headerLibrary'], ['sync', 'headerSyncOnly']]) {
        assert.equal(await page.locator(`#${id}`).isVisible(), tools.includes(tool), `${tool} visibility differs from saved choices at ${width}px`);
      }
      const settings = page.locator('#settingsToggle');
      await settings.focus();
      await page.locator('#navBack').evaluate(element => element.focus());
      await page.waitForFunction(element => document.activeElement === element, await settings.elementHandle());
      if (overflow) await page.keyboard.press('Escape');
    }
  });
}

test('header navigation hides unavailable TL1 and skips it in keyboard navigation', { skip }, async t => {
  const { page, settled } = await open(t, {
    url: '/library', ready: '#queryTableLibrary .qt-row', api: { 'GET /api/tl1': { projects: [] } },
  });
  for (const width of [1440, 1024]) {
    await page.setViewportSize({ width, height: 900 });
    await settled();
    assert.equal(await page.locator('#tl1Tab').isVisible(), false);
    const navigation = page.getByRole('navigation', { name: 'Primary navigation', exact: true });
    const library = navigation.getByRole('button', { name: 'Search', exact: true });
    await library.focus();
    await page.locator('#tl1Tab').evaluate(element => element.focus());
    await page.waitForFunction(element => document.activeElement === element, await library.elementHandle());
    // Safari's next tab stop depends on its keyboard navigation preference.
    await page.keyboard.press('Tab');
    assert.equal(await page.locator('#tl1Tab').evaluate(element => document.activeElement === element), false);
  }
});

test('header focus survives resizing while its tool remains inline', { skip }, async t => {
  const { page, settled } = await open(t, { url: '/library', ready: '#queryTableLibrary .qt-row' });
  const settings = page.locator('#settingsToggle');
  await settings.focus();
  await page.evaluate(() => dispatchEvent(new Event('resize')));
  await page.waitForFunction(element => document.activeElement === element, await settings.elementHandle());
  await page.setViewportSize({ width: 1600, height: 900 });
  await settled();
  await page.waitForFunction(element => document.activeElement === element, await settings.elementHandle());
  const theme = page.locator('#themeToggle');
  await theme.focus();
  for (const width of [1280, 390, 1440]) {
    await page.setViewportSize({ width, height: 900 });
    await settled();
    await page.waitForFunction(element => document.activeElement === element, await theme.elementHandle());
  }
});

test('header focus stays reachable when its tool moves between inline and overflow', { skip }, async t => {
  const { page } = await open(t, { url: '/library', viewport: { width: 1440, height: 900 }, ready: '#queryTableLibrary .qt-row' });
  await page.locator('#searchToggle').focus();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForFunction(() => document.activeElement === document.getElementById('headerMore'));
  await press(page.locator('#headerMore'));
  await page.locator('#searchToggle').focus();
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.waitForFunction(() => document.activeElement === document.getElementById('searchToggle'));
});

test("MCP call details take focus, keep it and return it on Escape", { skip }, async t => {
  const { page } = await open(t, { url: "/analyze?analyze_mode=query&analyze_data=mcp_calls&analyze_topic=mcp", ready: ".usage-workspace-table .qt-row" });
  const opener = page.getByRole("button", { name: /^Open item .+ in modal$/ }).first();
  await press(opener);
  const dialog = page.getByRole("dialog");
  await dialog.getByText("Arguments (sanitized)", { exact: true }).waitFor();
  await holdsFocus(page, dialog, opener);
});

test("MCP settings switch remains usable when moving between Settings and Analyze", { skip }, async t => {
  const { page, settled } = await open(t, { url: "/settings/mcp", ready: "#mcpSettingsPage .mcp-setup-grid" });
  const toggle = page.getByRole("switch", { name: "Enable MCP", exact: true });
  await page.waitForFunction(() => !document.querySelector('[aria-label="Enable MCP"]').disabled);
  await press(toggle);
  await page.waitForFunction(() => document.querySelector('[aria-label="Enable MCP"]').getAttribute("aria-checked") === "false");
  await press(page.getByRole("link", { name: "View MCP usage", exact: true }));
  await page.locator(".usage-mcp-dashboard .qt-metric").waitFor();
  await press(page.getByRole("link", { name: "MCP settings", exact: true }));
  await toggle.waitFor();
  await settled();
  assert.equal(await toggle.getAttribute("aria-checked"), "false");
});

test('header editing keeps keyboard focus on the handle being changed', { skip }, async t => {
  const { page } = await open(t, { url: '/settings/header', ready: '#headerActive .header-tool' });
  const add = page.getByRole('button', { name: 'Add Navigation bar', exact: true });
  const handle = page.getByRole('button', { name: 'Arrange Navigation bar', exact: true });
  await press(add);
  await page.waitForFunction(element => document.activeElement === element, await handle.elementHandle());
  await press(handle);
  await page.locator('#headerToolMenu:popover-open').waitFor();
  await page.keyboard.press('Escape');
  await page.waitForFunction(element => document.activeElement === element, await handle.elementHandle());
  await press(handle);
  await press(page.getByRole('button', { name: 'Move left Navigation bar', exact: true }));
  await page.waitForFunction(element => document.activeElement === element, await handle.elementHandle());
  await press(handle);
  await press(page.getByRole('button', { name: 'Remove Navigation bar', exact: true }));
  await page.waitForFunction(element => document.activeElement === element, await add.elementHandle());
  await press(add);
  await press(handle, 'Alt+ArrowLeft');
  await page.waitForFunction(element => document.activeElement === element, await handle.elementHandle());
  await page.keyboard.press('Delete');
  await page.waitForFunction(element => document.activeElement === element, await add.elementHandle());
  const settings = page.getByRole('button', { name: 'Arrange Settings', exact: true });
  await press(settings, 'Delete');
  await page.waitForFunction(element => document.activeElement === element, await settings.elementHandle());
});

test("the card template editor takes focus, keeps it and returns it on Escape", { skip }, async t => {
  const { page } = await open(t, { url: "/home?cards=library", ready: ".card-library-entry .query-card" });
  const edit = page.locator(".card-library-entry button[aria-label^='Edit card']").first();
  await press(edit);
  await holdsFocus(page, page.locator("dialog.card-template-editor"), edit);
});

test("the findings prompt review takes focus, keeps it and returns it on Escape", { skip }, async t => {
  const { page, settled } = await open(t, { url: "/findings", ready: ".findings-card" });
  const review = page.locator(".findings-cart button.primary").first();
  await press(review);
  const dialog = page.locator('.findings-dialog[role="dialog"]');
  await dialog.waitFor();
  await settled();
  await holdsFocus(page, dialog, review);
});

test("the delegation session sheet on a phone takes focus, keeps it and returns it on Escape", { skip }, async t => {
  const { page } = await open(t, { url: `/work/${workID}/conversation/conv-child`, viewport: { width: 390, height: 844 }, ready: ".conversation-panel .turn" });
  const show = page.locator('button[aria-label="Show workspace sessions"]');
  await press(show);
  const sheet = page.locator('[role="dialog"][aria-label="Workspace sessions"]');
  await sheet.waitFor();
  await holdsFocus(page, sheet, show);
});

test("transcript rails start below the header, with the navigation bar open, after main scrolls", { skip }, async t => {
  const { page, settled } = await open(t, { url: `/work/${workID}/conversation/conv-root`, ready: ".conversation-panel .turn", preferences: { "pharos-show-nav-button": true } });
  await activateHeaderTool(page, "navigatorToggle");
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
  await activateHeaderTool(app.page, "feedbackToggle");
  await app.page.locator("#feedbackTarget").click();
  await app.page.getByRole("heading", { level: 1 }).click();
  await app.page.locator(".feedback-composer").waitFor();
  return app.page;
}

test("the annotation toolbar and composer stay inside the viewport after a resize", { skip }, async t => {
  const page = await annotate(t);
  await page.setViewportSize({ width: 580, height: 460 });
  for (const surface of [".feedback-toolbar", ".feedback-composer"]) assert.equal(await onScreen(page.locator(surface)), true, `${surface} off screen at 580×460`);
});

test("the expanded feedback panel stays inside a 360px viewport", { skip }, async t => {
  const page = await annotate(t);
  await page.locator("#feedbackNote").fill("Annotation");
  // Adding an annotation expands the toolbar into the feedback panel.
  await page.locator("#feedbackAdd").click();
  await page.locator("#feedbackPanel.open").waitFor();
  await page.setViewportSize({ width: 360, height: 420 });
  const box = await onScreen(page.locator(".feedback-toolbar"));
  assert.equal(box, true, `expanded toolbar at [${box}] in 360×420`);
});

test('Analyze record keyword editor keeps focus through apply, edit and cancel', { skip }, async t => {
  const { page, settled, shot } = await open(t, { url:'/analyze?analyze_mode=query&analyze_data=tool_calls',ready:'.usage-workspace-table .qt-qb' });
  const opener=page.getByRole('button',{name:/^Add keyword filter for /});
  await press(opener);
  const dialog=page.locator('.explore-search-modal');
  await dialog.waitFor();
  await page.waitForFunction(element=>element.contains(document.activeElement),await dialog.elementHandle());
  await dialog.getByRole('textbox',{name:'Text for rule 1.1',exact:true}).fill('target');
  await shot('keyword-draft');
  await dialog.getByRole('button',{name:'Apply keyword filter',exact:true}).click();
  await dialog.waitFor({state:'hidden'});
  const edit=page.getByRole('button',{name:/^Edit keyword filter for /});
  await edit.waitFor();
  await page.waitForFunction(element=>element===document.activeElement,await edit.elementHandle());
  await settled();
  await press(edit);
  await dialog.waitFor();
  await holdsFocus(page,dialog,edit);
  await shot('keyword-applied');
});

test("Query ideas disclosure supports keyboard toggles and restores the saved preference", { skip }, async t => {
  const app = await open(t, { url: "/analyze?analyze_mode=query&analyze_data=tool_calls", ready: ".usage-workspace-table .qt-row" });
  const { page } = app;
  const summary = page.locator(".usage-query-ideas > summary");
  const chip = page.locator(".usage-query-ideas .query-chip").first();
  await chip.waitFor();
  const saved = page.waitForResponse(response => response.url().endsWith("/api/preferences") && response.request().method() === "POST");
  await summary.focus();
  await page.keyboard.press("Enter");
  await chip.waitFor({ state: "hidden" });
  await saved;
  assert.equal(await summary.evaluate(element => element === document.activeElement), true);
  await page.reload();
  await page.locator(".usage-workspace-table .qt-row").first().waitFor();
  await chip.waitFor({ state: "hidden" });
  await summary.focus();
  await page.keyboard.press("Enter");
  await chip.waitFor();
  assert.equal(await summary.evaluate(element => element === document.activeElement), true);
  await app.settled();
  await app.shot("query-ideas-keyboard");
});
