// Focus and placement that only a real browser can check: modal surfaces take
// focus, keep it while open and give it back on Escape; the transcript rails
// stay below the header; the annotation toolbar stays on screen.
import { test } from "node:test";
import assert from "node:assert/strict";
import { activateHeaderTool, open, skip } from "./harness.mjs";
import { defaultApi, workID } from "./fixtures.mjs";
import { EMPTY_QUERY, encodeQuery } from "@pythia-software/query-table-core";

for (const mode of ['drive', 'activity']) test(`Activity ${mode} refresh preserves scrolling, focus and animation phase`, {skip}, async t => {
  const main = {id: 'refreshing-index', kind: 'sync', label: 'Capture & index', stoppable: true,
    started_at: new Date(Date.now() - 120_000).toISOString(),
    work: {phase: 'Discovering inputs', source: 'Codex', done: 0, total: 0},
    forecast: {state: 'partial', remaining_steps: 4, predicted_steps: 3, basis: 'Learning the remaining analyses'}};
  let activities = [main, ...Array.from({length: 5}, (_, index) => ({id: `task-${index}`, kind: 'maintenance', label: `Background task ${index}`}))];
  let stopped = 0;
  const {page, settled, shot} = await open(t, {url: '/library', ready: '#queryTableLibrary .qt-row', viewport: {width: 390, height: 550}, api: {
    'GET /api/library/status': () => ({...defaultApi['GET /api/library/status'], idle: false, activities}),
    'POST /api/library/refresh/stop': () => {stopped++; return {stopped: true};},
  }});
  await page.emulateMedia({reducedMotion: 'no-preference'});
  if (mode === 'drive') await activateHeaderTool(page, 'pharosDrive');
  else await page.evaluate(() => window.pharosLibrary.toggleActivityPanel());
  await settled();
  const panel = page.getByRole('dialog', {name: mode === 'drive' ? 'Library drive' : 'Running activity', exact: true});
  const summary = panel.locator('summary').first();
  await summary.click();
  await summary.focus();
  await page.evaluate(async () => {
    const panel = document.getElementById('pharosDrivePanel');
    panel.scrollTop = 160;
    const animations = [...panel.querySelector('[role="progressbar"]').getAnimations({subtree: true}),
      ...document.getElementById('pharosDrive').getAnimations({subtree: true})];
    if (!animations.length) throw new Error('No loading animations to exercise');
    for (const animation of animations) {animation.pause(); await animation.ready; animation.currentTime = 750;}
    window.activityRefreshTest = {animations, focused: document.activeElement, scrollTop: panel.scrollTop};
    if (!panel.scrollTop) throw new Error('Activity fixture must scroll');
  });
  activities = [{...main, stop_path: '/api/library/refresh/stop', detail: 'Updated discovery counts'}, ...activities.slice(1)];
  await page.evaluate(() => window.pharosLibrary.refresh());
  assert.ok(await page.evaluate(() => {
    const panel = document.getElementById('pharosDrivePanel'), before = window.activityRefreshTest;
    const animations = [...panel.getAnimations({subtree: true}), ...document.getElementById('pharosDrive').getAnimations({subtree: true})];
    return document.activeElement === before.focused && panel.scrollTop === before.scrollTop
      && panel.querySelector('details').open
      && before.animations.every(animation => animations.includes(animation) && animation.currentTime === 750);
  }), 'refresh interrupted scrolling, focus, disclosure state or an in-progress animation');
  // Removing a preceding task also keeps a later task's animation alive.
  await page.evaluate(async () => {
    const bar = [...document.querySelectorAll('#pharosDrivePanel [role="progressbar"]')].find(el => el.getAttribute('aria-label').startsWith('Background task 4: overall'));
    window.laterActivityAnimation = bar.getAnimations({subtree: true})[0];
    window.laterActivityAnimation.pause();
    await window.laterActivityAnimation.ready;
    window.laterActivityAnimation.currentTime = 900;
  });
  activities = [activities[0], ...activities.slice(2)];
  await page.evaluate(() => window.pharosLibrary.refresh());
  assert.ok(await panel.evaluate(element => element.getAnimations({subtree: true}).includes(window.laterActivityAnimation) && window.laterActivityAnimation.currentTime === 900));
  // Exercise the state-preserving move fallback used by older browsers.
  await page.evaluate(() => {Element.prototype.moveBefore = undefined;});
  activities = [activities[0], ...activities.slice(1).reverse()];
  await page.evaluate(() => window.pharosLibrary.refresh());
  assert.ok(await panel.evaluate(element => {
    const bar = [...element.querySelectorAll('[role="progressbar"]')].find(el => el.getAttribute('aria-label').startsWith('Background task 4: overall'));
    return bar.getAnimations({subtree: true})[0].currentTime === 900;
  }), 'reordering tasks restarted an in-progress animation');
  const stop = panel.getByRole('button', {name: 'Stop', exact: true});
  await stop.click();
  await settled();
  assert.equal(stopped, 1, 'retained Stop button did not use the latest stop target');
  // A measured step can replace discovery without closing its details.
  activities = [{...activities[0], work: {phase: 'Indexing', source: 'Codex', done: 42, total: 100},
    forecast: {state: 'provisional', progress: .63, low_seconds: 120, high_seconds: 180}}, ...activities.slice(1)];
  await page.evaluate(() => window.pharosLibrary.refresh());
  assert.ok(await panel.locator('details').first().evaluate(element => element.open));
  activities = [activities[0]];
  await page.evaluate(() => window.pharosLibrary.refresh());
  await panel.locator('summary').first().click();
  await panel.evaluate(element => {element.scrollTop = 0;});
  await shot('refreshed');
});

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

for (const width of [1440, 390, 320]) test(`Data guide takes focus, keeps controls reachable and returns focus @ ${width}`, { skip }, async t => {
  const app = await open(t, { url: "/analyze?analyze_mode=query&analyze_data=tool_calls&analyze_topic=tools", viewport: { width, height: 900 }, ready: ".usage-workspace-table .qt-row" });
  const { page } = app;
  const opener = page.locator(".usage-workspace-table .data-source-link");
  await app.shot("from-statement");
  await press(opener);
  const dialog = page.getByRole("dialog", { name: "Data Guide", exact: true });
  await dialog.waitFor();
  await app.shot("tool-call-guide");
  await dialog.getByRole("button", { name: "Review", exact: true }).click();
  await press(dialog.getByRole("button", { name: /Agent sessions/ }));
  const source = dialog.getByRole("region", { name: "Dataset row definitions", exact: true });
  await source.waitFor({ state: "hidden" });
  await press(dialog.getByRole("navigation", { name: "Data layers", exact: true }).getByRole("button", { name: "Tool calls", exact: true }));
  await source.waitFor({ state: "visible" });
  await holdsFocus(page, dialog, opener);
  await press(opener);
  await dialog.getByRole("button", { name: "Close data guide", exact: true }).click();
  await page.waitForFunction(element => document.activeElement === element, await opener.elementHandle());
  await press(page.getByRole("button", { name: "Collapse query builder", exact: true }));
  await press(opener);
  await holdsFocus(page, dialog, opener);
});

test("Data guide is available outside Analyze in the light theme", { skip }, async t => {
  const app = await open(t, { url: "/library", preferences: { "pharos-theme": "light" }, ready: "#queryTableLibrary .qt-row" });
  await press(app.page.locator("#queryTableLibrary .data-source-link"));
  await app.page.getByRole("dialog", { name: "Data Guide", exact: true }).waitFor();
  await app.shot("workspace-guide-light");
});

// Exercise the guide alongside native grouping, filter and aggregate editors.
for (const [dataset, path, numeric] of [["library", "/library", "conversation_count"], ["tool_calls", "/analyze?analyze_mode=query&analyze_data=tool_calls", "duration_ms"]]) {
  test(`Data guide preserves native grouping editors for ${dataset}`, { skip }, async t => {
    const query = {
      ...EMPTY_QUERY,
      select: [{ field: "repository_name" }, { field: "total" }],
      grouping: { source: EMPTY_QUERY, keys: ["repository_name"], summaries: [{ id: "total", op: "sum", field: numeric, label: "Total" }], representative: null },
    };
    const url = new URL(path, "http://localhost");
    url.searchParams.set(`q_${dataset}`, encodeQuery(query));
    const app = await open(t, { url: url.pathname + url.search, ready: ".qt-table .qt-row" });
    const { page } = app;
    const aggregate = page.getByRole("combobox", { name: /^Aggregation for/ }).first();
    await aggregate.selectOption("avg");
    assert.equal(await aggregate.inputValue(), "avg");
    for (const name of ["Add filter", "Add HAVING filter", "Customize columns"]) {
      const opener = page.getByRole("button", { name, exact: true });
      await press(opener);
      const editor = page.getByRole("dialog");
      await page.waitForFunction(element => element.contains(document.activeElement), await editor.elementHandle());
      await page.keyboard.press("Escape");
      await editor.waitFor({ state: "hidden" });
    }
    const guideLink = page.locator(".data-source-link");
    await press(guideLink);
    await holdsFocus(page, page.getByRole("dialog", { name: "Data Guide", exact: true }), guideLink);
    await app.shot(`${dataset}-grouping-with-source`);
    await press(page.getByRole("button", { name: "Collapse query builder", exact: true }));
    await press(guideLink);
    await holdsFocus(page, page.getByRole("dialog", { name: "Data Guide", exact: true }), guideLink);
    await press(page.getByRole("button", { name: "Expand query builder", exact: true }));
    await press(page.getByRole("button", { name: "Remove Repository grouping", exact: true }));
    const addGroup = page.getByRole("button", { name: "Add group by", exact: true });
    await press(addGroup);
    const picker = page.getByRole("dialog", { name: "Group rows by", exact: true });
    await page.waitForFunction(element => element.contains(document.activeElement), await picker.elementHandle());
    await page.keyboard.press("Escape");
    await picker.waitFor({ state: "hidden" });
    await page.waitForFunction(element => document.activeElement === element, await addGroup.elementHandle());
  });
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

for (const width of [1440, 390]) test(`Optimize pages move keyboard focus to their content at ${width}px`, { skip }, async t => {
  const { page, settled, shot } = await open(t, { url: '/findings', viewport: { width, height: 900 }, ready: '.findings-card' });
  const phone = width === 390;
  const menu = page.getByRole('dialog', { name: 'Navigate Pharos' });
  const navigation = phone ? menu : page.getByRole('complementary', { name: 'Optimize navigation', exact: true });
  async function choose(name, ready) {
    if (phone) await press(page.locator('#appNavigationToggle'));
    await press(navigation.getByRole('link', { name, exact: true }));
    await page.locator(ready).first().waitFor();
    await page.waitForFunction(() => document.activeElement?.tagName === 'H1');
    if (phone) assert.equal(await menu.isVisible(), false);
  }
  await choose('Settings', '.findings-settings .findings-panel');
  await choose('Table', '.findings-table .qt-row');
  await choose('Dashboard', '.findings-card');
  if (phone) await press(page.locator('#appNavigationToggle'));
  await press(navigation.getByRole('link', { name: 'Configurations', exact: true }));
  await page.locator('#tl1-matrix').waitFor();
  await page.waitForFunction(() => document.activeElement?.tagName === 'H1');
  await settled();
  await shot('optimize-tl1');
  await page.goBack();
  await page.locator('.findings-card').first().waitFor();
  await settled();
  await shot('optimize-findings');
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

test("All Cards opens a page with focus on its heading and browser history returns Home", { skip }, async t => {
  const { page, settled, shot } = await open(t, { ready: ".home-belt-row .query-card", preferences: { "pharos-home-card-library-v1": { shown: true } } });
  await press(page.getByRole("button", { name: "All Cards", exact: true }));
  const heading = page.getByRole("heading", { name: "Card Library", exact: true });
  await heading.waitFor();
  await page.waitForFunction(element => document.activeElement === element, await heading.elementHandle());
  await settled();
  await shot("card-library");
  await page.goBack();
  const welcome = page.locator(".home-intro h1");
  await welcome.waitFor();
  await page.waitForFunction(element => document.activeElement === element, await welcome.elementHandle());
  await page.goForward();
  await heading.waitFor();
  await press(page.getByRole("link", { name: "Back to Home", exact: true }));
  await welcome.waitFor();
  await page.waitForFunction(element => document.activeElement === element, await welcome.elementHandle());
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

for (const merged of [false, true]) test(`transcript rails follow layout changes in ${merged ? "merged" : "split"} workspaces`, { skip }, async t => {
  const api = merged ? {
    "GET /api/work/:id": call => {
      const work = defaultApi["GET /api/work/:id"](call);
      return { ...work, conversations: work.conversations.slice(0, 1), attempts: [{ attempt_no: 1, result: "Retained archive evidence\n".repeat(40) }] };
    },
    "GET /api/work/:id/delegation": call => {
      const data = defaultApi["GET /api/work/:id/delegation"](call);
      return { ...data, rows: data.rows.slice(0, 1), focus: data.focus.slice(0, 1), session_count: 1, total: 1 };
    },
  } : {};
  const { page, settled } = await open(t, { url: `/work/${workID}/conversation/conv-root`, ready: ".conversation-panel .turn", api, preferences: { "pharos-show-nav-button": true } });
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

  // Rail geometry must stay useful after disclosures and a viewport change.
  // A context jump must still reach its event, including a deferred turn.
  await page.getByRole("button", { name: "Close turns", exact: true }).click();
  await page.getByRole("button", { name: "Open turns", exact: true }).click();
  await page.setViewportSize({ width: 1100, height: 800 });
  await settled();
  await page.locator(".token-marker").last().evaluate(marker => marker.click());
  await page.waitForFunction(() => {
    const target = document.querySelector(".token-highlight");
    if (!target) return false;
    const bounds = target.getBoundingClientRect();
    return bounds.height > 0 && bounds.bottom > document.querySelector("body > header").getBoundingClientRect().bottom && bounds.top < innerHeight;
  });
  if (merged) {
    // The overview grows without resizing the transcript or its scrollport.
    // The selected turn and the progress band must still describe the viewport.
    for (const expanded of [true, false]) {
      await page.getByText("Archive details", { exact: true }).click();
      assert.equal(await page.locator(".archive-details").evaluate(details => details.open), expanded);
      await settled();
      await page.locator(".turn").nth(6).evaluate(turn => turn.scrollIntoView({ block: "center", behavior: "instant" }));
      await page.waitForFunction(() => {
        const turns = [...document.querySelectorAll(".conversation-map-turn")];
        const marker = turns[6], band = document.querySelector(".conversation-map-viewport");
        if (!marker.classList.contains("current")) return false;
        const bounds = marker.getBoundingClientRect(), viewport = band.getBoundingClientRect(), middle = (bounds.top + bounds.bottom) / 2;
        return middle >= viewport.top && middle <= viewport.bottom;
      });
    }
  }
});

for (const trigger of ["context jump", "scroll"]) test(`a ${trigger} reaches a paginated event and its child conversation link`, { skip }, async t => {
  const { page, settled, calls } = await open(t, {
    url: `/work/${workID}/conversation/conv-root`, ready: ".conversation-panel .turn",
    api: { "GET /api/work/:id/conversations/:conversation": call => {
      const body = defaultApi["GET /api/work/:id/conversations/:conversation"](call);
      if (call.params.conversation !== "conv-root") return body;
      const event = (id, kind, text, extra = {}) => ({ id, native_id: id, kind, role: "assistant", text: typeof text === "string" ? text : JSON.stringify(text), created_at: "2026-10-01T00:00:00Z", ...extra });
      return { ...body, messages: [
        event("prompt", "message", "Review the changes", { role: "user" }),
        ...Array.from({ length: 52 }, (_, i) => event(`call-${i}`, "tool_call", { name: "Bash", input: { command: `check ${i}` } }, { call_id: `call-${i}` })),
        event("m-15", "delegation", { name: "Task", input: { description: "Review implementation", subagent_type: "Reviewer" } }, { call_id: "child" }),
        event("context", "metadata", { type: "token_count", info: { total_token_usage: { total_tokens: 40000 }, last_token_usage: { input_tokens: 30000, output_tokens: 500 } } }),
      ] };
    } },
  });
  assert.equal(calls.filter(call => call.path.endsWith("/delegation")).length, 1, "the deep link loads delegation analysis once for the selected conversation");
  if (trigger === "scroll") {
    await page.evaluate(() => { const scroller = document.querySelector(".workspace-reader-content"); scroller.scrollTop = scroller.scrollHeight; });
    await page.getByRole("button", { name: /codex.*Open child/ }).waitFor();
  }
  await page.locator(".token-marker").last().evaluate(marker => marker.click());
  await page.waitForFunction(() => {
    const target = document.querySelector(".token-highlight");
    const bounds = target?.getBoundingClientRect();
    return bounds?.height > 0 && bounds.bottom > 0 && bounds.top < innerHeight;
  });
  const child = page.getByRole("button", { name: /codex.*Open child/ });
  await child.click();
  await settled();
  assert.ok(await page.getByRole("article", { name: "User message", exact: true }).getByText("Review the change (conv-child)", { exact: true }).isVisible());
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
