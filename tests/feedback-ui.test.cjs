// Run: npm install --prefix .context/browser-tests playwright --no-audit --no-fund
//      node --test tests/feedback-ui.test.cjs
// Use FEEDBACK_BROWSER=webkit to check the Mac app's browser engine.
const { test, before, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');
const root = path.resolve(__dirname, '..');
const { chromium, webkit } = process.env.PLAYWRIGHT_MODULE ? require(process.env.PLAYWRIGHT_MODULE) : createRequire(path.join(root, '.context/browser-tests/package.json'))('playwright');
const source = fs.readFileSync(path.join(root, 'internal/archive/assets/ui.py'), 'utf8');
const html = source.slice(source.indexOf("r'''") + 4, source.lastIndexOf("'''"));
let browser;
before(async () => {
  const chrome = process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const engine = process.env.FEEDBACK_BROWSER === 'webkit' ? webkit : chromium;
  browser = await engine.launch({ headless: true, ...(engine === chromium ? { args: ['--disable-features=MacAppCodeSignClone'] } : {}), ...(engine === chromium && fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
});
after(async () => { await browser?.close(); });

async function fixture(t) {
  const page = await browser.newPage({ viewport: { width: 1200, height: 900 } });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  t.after(async () => { await page.close(); assert.deepEqual(errors, [], 'No browser exceptions'); });
  await page.route('http://feedback.test/**', route => {
    const url = new URL(route.request().url());
    if (url.pathname === '/library') return route.fulfill({ contentType: 'text/html', body: html });
    if (url.pathname.startsWith('/api/')) return route.fulfill({ contentType: 'application/json', body: '{"items":[]}' });
    return route.fulfill({ contentType: 'application/javascript', body: '' });
  });
  await page.goto('http://feedback.test/library');
  await page.evaluate(() => {
    pharosPrefs.set('pharos-show-agent-annotation-button', true);
    setFeedbackEnabled(true);
    document.querySelector('#library').innerHTML = '<div style="height:1600px;padding:200px 0 0 400px"><button id="annotationFixture" data-feedback-label="Example control">Example control</button></div>';
  });
  return page;
}

async function drag(page, surface, handle, dx, dy) {
  const before = await surface.boundingBox(), grip = await handle.boundingBox();
  assert.ok(before && grip, 'Surface and handle are visible');
  const x = grip.x + grip.width / 2, y = grip.y + grip.height / 2;
  await page.mouse.move(x, y);
  await page.mouse.down();
  await page.mouse.move(x + dx, y + dy, { steps: 8 });
  await page.mouse.up();
  const after = await surface.boundingBox();
  assert.ok(Math.abs(after.x - before.x) + Math.abs(after.y - before.y) > 20, 'Surface moved');
  return after;
}

function samePosition(actual, expected) {
  assert.ok(Math.abs(actual.x - expected.x) < 1, `x: ${actual.x} ≠ ${expected.x}`);
  assert.ok(Math.abs(actual.y - expected.y) < 1, `y: ${actual.y} ≠ ${expected.y}`);
}

test('toolbar, target label and composer drag without changing targeting or losing a draft', async t => {
  const page = await fixture(t);
  await page.locator('#feedbackTarget').click();
  const toolbar = page.locator('.feedback-toolbar');
  const toolbarPosition = await drag(page, toolbar, page.getByRole('button', { name: 'Move annotation toolbar' }), -650, -450);
  assert.equal(await page.locator('#feedbackTarget').getAttribute('aria-pressed'), 'true');
  assert.equal(await page.locator('#feedbackComposer').isVisible(), false);

  const target = page.locator('#annotationFixture');
  await target.hover();
  const bounds = await target.boundingBox(), highlight = page.locator('#feedbackHighlight');
  const label = highlight.locator('span');
  await drag(page, label, label, -120, -100);
  samePosition(await highlight.boundingBox(), bounds);
  assert.equal(await page.evaluate(() => feedbackHoverElement.id), 'annotationFixture');
  await target.click();

  const composer = page.locator('.feedback-composer');
  const composerPosition = await drag(page, composer, page.getByRole('button', { name: 'Move annotation composer' }), 220, -200);
  await page.locator('#feedbackNote').fill('Keep this unfinished note');
  assert.equal(await page.evaluate(() => feedbackTarget.selector), '#annotationFixture');
  await page.reload();
  samePosition(await toolbar.boundingBox(), toolbarPosition);
  samePosition(await composer.boundingBox(), composerPosition);
  assert.equal(await page.locator('#feedbackNote').inputValue(), 'Keep this unfinished note');
  await page.locator('#feedbackAdd').click();
  assert.match(await page.locator('#feedbackList').innerText(), /Keep this unfinished note/);
});

test('panel, pins and status messages retain positions and keep original annotation evidence', async t => {
  const page = await fixture(t);
  await page.locator('#feedbackTarget').click();
  await page.locator('#annotationFixture').click();
  await page.locator('#feedbackNote').fill('Improve the example control');
  await page.locator('#feedbackAdd').click();
  const record = await page.evaluate(() => JSON.stringify(feedbackAnnotations));

  const panel = page.locator('#feedbackPanel');
  const toolbar = page.locator('.feedback-toolbar');
  assert.equal(await panel.evaluate(element => element.closest('.feedback-toolbar') !== null), true);
  const panelPosition = await drag(page, toolbar, page.getByRole('button', { name: 'Move annotation toolbar' }), -600, -300);
  await page.locator('#feedbackOpen').click();
  await page.locator('#feedbackOpen').click();
  samePosition(await toolbar.boundingBox(), panelPosition);

  const pin = page.locator('.feedback-pin');
  const pinPosition = await drag(page, pin, pin, 100, 70);
  await page.evaluate(() => renderFeedbackPins());
  samePosition(await pin.boundingBox(), pinPosition);
  await page.evaluate(() => { document.querySelector('main').scrollTop += 40; });
  await page.waitForFunction(y => Math.abs(document.querySelector('.feedback-pin').getBoundingClientRect().top - y) < 1, pinPosition.y - 40);
  assert.equal(await page.evaluate(() => JSON.stringify(feedbackAnnotations)), record);

  await page.evaluate(() => feedbackToast('Move this status message'));
  const toast = page.locator('#feedbackToast');
  const toastPosition = await drag(page, toast, page.getByRole('button', { name: 'Move feedback status message' }), -200, -150);
  await page.reload();
  // Restore the test target, just as the app materializes a view after loading.
  await page.evaluate(() => {
    document.querySelector('#library').innerHTML = '<div style="height:1600px;padding:200px 0 0 400px"><button id="annotationFixture">Example control</button></div>';
    renderFeedbackPins();feedbackToast('Move this status message');
  });
  await page.locator('#feedbackOpen').click();
  samePosition(await toolbar.boundingBox(), panelPosition);
  samePosition(await pin.boundingBox(), pinPosition);
  samePosition(await toast.boundingBox(), toastPosition);
  await page.getByRole('button', { name: 'Remove annotation 1' }).click();
  assert.equal(await pin.count(), 0);
  assert.equal(await page.evaluate(() => Object.keys(feedbackLayout).some(key => key.startsWith('pin:'))), false);
});

test('dragged controls stay reachable after resizing and keyboard movement works during targeting', async t => {
  const page = await fixture(t);
  const toolbar = page.locator('.feedback-toolbar'), grip = page.getByRole('button', { name: 'Move annotation toolbar' });
  await drag(page, toolbar, grip, -100, -100);
  await page.locator('#feedbackTarget').click();
  await grip.focus();
  const start = await toolbar.boundingBox();
  await page.keyboard.press('ArrowLeft');
  await page.keyboard.press('Shift+ArrowUp');
  samePosition(await toolbar.boundingBox(), { x: start.x - 10, y: start.y - 1 });
  assert.equal(await page.locator('#feedbackTarget').getAttribute('aria-pressed'), 'true');
  await page.locator('#annotationFixture').click();
  const composer = page.locator('.feedback-composer');
  await drag(page, composer, page.getByRole('button', { name: 'Move annotation composer' }), 250, 220);
  await page.setViewportSize({ width: 580, height: 460 });
  for (const surface of [toolbar, composer]) {
    await surface.evaluate(element => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    const bounds = await surface.boundingBox();
    assert.ok(bounds.x >= 0 && bounds.y >= 0 && bounds.x + bounds.width <= 580 && bounds.y + bounds.height <= 460, JSON.stringify(bounds));
  }
  // Resizing the editable field must also keep the composer's title reachable.
  await page.locator('#feedbackNote').evaluate(element => { element.style.height = '800px'; });
  await page.locator('#feedbackCancel').click();
  assert.equal(await composer.isVisible(), false);
});

test('dragging above a native modal preserves the modal and the moved positions after it closes', async t => {
  const page = await fixture(t);
  await page.evaluate(() => {
    const dialog = document.createElement('dialog');
    dialog.setAttribute('aria-label', 'Review modal');
    dialog.innerHTML = '<h2>Modal target</h2><button id="closeReview">Close review</button>';
    dialog.querySelector('button').onclick = () => { dialog.close();dialog.remove(); };
    // Simulates a document-level handler that dismisses on outside presses.
    document.addEventListener('pointerdown', event => { if (!dialog.contains(event.target)) dialog.close(); });
    document.body.append(dialog);dialog.showModal();
  });
  const modal = page.getByRole('dialog', { name: 'Review modal' }), toolbar = page.locator('.feedback-toolbar');
  await page.waitForFunction(() => document.querySelector('.feedback-toolbar').closest('dialog')?.open);
  const toolbarPosition = await drag(page, toolbar, page.getByRole('button', { name: 'Move annotation toolbar' }), -650, -550);
  assert.equal(await modal.isVisible(), true);
  await page.locator('#feedbackTarget').click();
  await modal.getByRole('heading').click();
  const composerPosition = await drag(page, page.locator('.feedback-composer'), page.getByRole('button', { name: 'Move annotation composer' }), 200, -180);
  assert.equal(await modal.isVisible(), true);
  await page.locator('#feedbackNote').fill('Improve this modal');
  await page.locator('#feedbackAdd').click();
  assert.equal(await page.locator('#feedbackPanel').evaluate(element => element.parentElement.classList.contains('feedback-toolbar')), true);
  await page.locator('#feedbackOpen').click();
  samePosition(await toolbar.boundingBox(), toolbarPosition);
  await modal.getByRole('button', { name: 'Close review' }).click();
  await page.waitForFunction(() => document.querySelector('.feedback-toolbar').parentElement === document.body);
  samePosition(await toolbar.boundingBox(), toolbarPosition);
  assert.equal(await page.locator('.feedback-pin').count(), 0);
  await page.evaluate(() => openFeedbackComposer(feedbackAnnotations[0], 'Another note'));
  samePosition(await page.locator('.feedback-composer').boundingBox(), composerPosition);
});

async function annotate(page, note = 'Improve the example control') {
  await page.locator('#feedbackTarget').click();
  await page.locator('#annotationFixture').click();
  await page.locator('#feedbackNote').fill(note);
  await page.locator('#feedbackAdd').click();
}

async function clipboardBridge(page, fail = false) {
  await page.evaluate(fail => {
    window.feedbackClipboard = [];
    Object.defineProperty(window, 'webkit', { configurable: true, value: { messageHandlers: { pharosClipboard: {
      postMessage: async text => { if (fail) throw new Error('denied'); window.feedbackClipboard.push(text); },
    } } } });
  }, fail);
}

test('feedback expands within the toolbar and both feedback controls disappear when empty', async t => {
  const page = await fixture(t), toolbar = page.locator('.feedback-toolbar'), panel = page.locator('#feedbackPanel');
  assert.equal(await page.locator('#feedbackOpen').isVisible(), false);
  assert.equal(await page.locator('#feedbackQuickCopy').isVisible(), false);
  const collapsed = await toolbar.boundingBox();
  await annotate(page);
  assert.equal(await page.locator('#feedbackOpen').getAttribute('aria-expanded'), 'true');
  assert.equal(await page.locator('#feedbackQuickCopy').isVisible(), true);
  assert.equal(await panel.evaluate(element => element.closest('.feedback-toolbar') !== null), true);
  assert.equal(await panel.locator('header').count(), 0);
  assert.ok((await toolbar.boundingBox()).height > collapsed.height + 100);
  assert.equal(await page.getByRole('button', { name: 'Move annotation toolbar' }).count(), 1);
  await page.locator('#feedbackOpen').click();
  assert.equal(await panel.isVisible(), false);
  assert.equal(await page.locator('#feedbackOpen').getAttribute('aria-expanded'), 'false');
  await page.locator('#feedbackOpen').click();
  await page.getByRole('button', { name: 'Remove annotation 1' }).click();
  assert.equal(await panel.isVisible(), false);
  assert.equal(await page.locator('#feedbackOpen').isVisible(), false);
  assert.equal(await page.locator('#feedbackQuickCopy').isVisible(), false);
  assert.equal(await page.locator('#feedbackTarget').isVisible(), true);
});

test('quick copy works with details closed and honors clear-on-copy', async t => {
  const page = await fixture(t);
  await clipboardBridge(page);
  await annotate(page, 'Copy me while collapsed');
  const copyColors = () => page.locator('#feedbackCopy, #feedbackQuickCopy').evaluateAll(buttons => buttons.map(button => ({
    background: getComputedStyle(button).backgroundColor, color: getComputedStyle(button).color, title: button.title,
  })));
  const yellow = await copyColors();
  assert.equal(yellow[0].background, yellow[1].background);
  assert.equal(yellow[0].background, 'rgb(234, 176, 74)');
  await page.locator('#feedbackOpen').click();
  await page.locator('#feedbackQuickCopy').click();
  await page.waitForFunction(() => window.feedbackClipboard.length === 1);
  assert.match(await page.evaluate(() => window.feedbackClipboard[0]), /Copy me while collapsed/);
  assert.equal(await page.locator('#feedbackPanel').isVisible(), false);
  assert.equal(await page.locator('#feedbackCount').innerText(), '1');
  await page.locator('#feedbackOpen').click();
  await page.locator('#feedbackClearOnCopy').check();
  const red = await copyColors();
  assert.equal(red[0].background, red[1].background);
  assert.notEqual(red[0].background, yellow[0].background);
  assert.equal(red[0].title, 'Copy and clear feedback');
  await page.locator('#feedbackClearOnCopy').uncheck();
  assert.deepEqual(await copyColors(), yellow);
  await page.locator('#feedbackClearOnCopy').check();
  await page.locator('#feedbackOpen').click();
  await page.reload();
  assert.deepEqual(await copyColors(), red);
  assert.equal(await page.locator('#feedbackPanel').isVisible(), false);
  await page.locator('#feedbackQuickCopy').click();
  await page.waitForFunction(() => feedbackAnnotations.length === 0);
  assert.equal(await page.locator('#feedbackOpen').isVisible(), false);
  assert.equal(await page.locator('#feedbackQuickCopy').isVisible(), false);
});

test('failed quick copy keeps feedback and restores both copy buttons', async t => {
  const page = await fixture(t);
  await clipboardBridge(page, true);
  await annotate(page, 'Keep this after clipboard failure');
  await page.locator('#feedbackClearOnCopy').check();
  await page.locator('#feedbackOpen').click();
  await page.locator('#feedbackQuickCopy').click();
  await page.waitForFunction(() => !feedbackCopying);
  assert.equal(await page.locator('#feedbackCount').innerText(), '1');
  assert.equal(await page.locator('#feedbackQuickCopy').isEnabled(), true);
  assert.equal(await page.locator('#feedbackCopy').isEnabled(), true);
  assert.match(await page.locator('#feedbackToast').innerText(), /Could not copy feedback/);
});

test('inline editing saves only the note, supports cancel, and survives reload', async t => {
  const page = await fixture(t);
  await annotate(page, 'Original feedback');
  const original = await page.evaluate(() => ({ ...feedbackAnnotations[0] }));
  await page.getByRole('button', { name: 'Edit annotation 1', exact: true }).click();
  const field = page.getByRole('textbox', { name: 'Edit annotation 1 feedback' });
  await field.fill('  ');
  assert.equal(await page.getByRole('button', { name: 'Save', exact: true }).isEnabled(), false);
  await field.fill('Discard this draft');
  await page.getByRole('button', { name: 'Cancel', exact: true }).click();
  assert.match(await page.locator('#feedbackList').innerText(), /Original feedback/);
  await page.getByRole('button', { name: 'Edit annotation 1', exact: true }).click();
  await field.fill('Escape this draft');
  await field.press('Escape');
  assert.equal(await page.locator('#feedbackPanel').isVisible(), true);
  assert.equal(await field.count(), 0);
  await page.getByRole('button', { name: 'Edit annotation 1', exact: true }).click();
  await field.fill('Updated feedback');
  await field.press('Control+Enter');
  assert.deepEqual(await page.evaluate(() => feedbackAnnotations), [{ ...original, note: 'Updated feedback' }]);
  await clipboardBridge(page);
  await page.locator('#feedbackOpen').click();
  await page.locator('#feedbackQuickCopy').click();
  assert.match(await page.evaluate(() => window.feedbackClipboard[0]), /Updated feedback/);
  await page.reload();
  await page.locator('#feedbackOpen').click();
  assert.match(await page.locator('#feedbackList').innerText(), /Updated feedback/);
  assert.equal(await page.locator('#feedbackCount').innerText(), '1');
});

test('list numbers and marker clicks reveal targets while marker dragging only moves the marker', async t => {
  const page = await fixture(t);
  await annotate(page);
  await page.evaluate(() => { document.querySelector('main').scrollTop = 600; });
  await page.locator('.feedback-item').getByRole('button', { name: 'Go to annotation 1' }).click();
  await page.waitForFunction(() => document.querySelector('main').scrollTop === 0);
  await page.locator('#feedbackOpen').click();
  const pin = page.locator('.feedback-pin');
  await drag(page, pin, pin, 80, 50);
  assert.equal(await page.locator('#feedbackPanel').isVisible(), false);
  await pin.click();
  assert.equal(await page.locator('#feedbackPanel').isVisible(), true);
  await page.evaluate(() => document.querySelector('#annotationFixture').remove());
  await page.locator('.feedback-item').getByRole('button', { name: 'Go to annotation 1' }).click();
  assert.match(await page.locator('#feedbackToast').innerText(), /unavailable/);
});

test('expanded feedback stays reachable near viewport edges and on a narrow screen', async t => {
  const page = await fixture(t), toolbar = page.locator('.feedback-toolbar');
  await annotate(page);
  await page.locator('#feedbackOpen').click();
  await drag(page, toolbar, page.getByRole('button', { name: 'Move annotation toolbar' }), -20, -20);
  await page.locator('#feedbackOpen').click();
  await page.getByRole('button', { name: 'Edit annotation 1', exact: true }).click();
  await page.setViewportSize({ width: 360, height: 420 });
  await page.waitForFunction(() => {
    const bounds = document.querySelector('.feedback-toolbar').getBoundingClientRect();
    return bounds.left >= 0 && bounds.top >= 0 && bounds.right <= innerWidth && bounds.bottom <= innerHeight;
  });
  await page.screenshot({ path: path.join(root, '.context/feedback-unified-narrow.png') });
  await page.getByRole('textbox', { name: 'Edit annotation 1 feedback' }).fill('Edited on a narrow screen');
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await page.setViewportSize({ width: 1200, height: 900 });
  await page.locator('#feedbackOpen').click();
  await page.locator('#feedbackOpen').click();
  await page.screenshot({ path: path.join(root, '.context/feedback-unified-expanded.png') });
  await toolbar.screenshot({ path: path.join(root, '.context/feedback-unified-panel.png') });
});


test('clicking a marker scrolls the expanded feedback list to its matching entry', async t => {
  const page = await fixture(t);
  await annotate(page);
  await page.evaluate(() => {
    const original = feedbackAnnotations[0];
    feedbackAnnotations = Array.from({ length: 18 }, (_, index) => ({ ...original, id: `annotation-${index}`, note: `Feedback entry ${index + 1}` }));
    saveFeedback();setFeedbackExpanded(false);
  });
  await page.locator('.feedback-pin').getByText('18', { exact: true }).click();
  assert.equal(await page.locator('#feedbackPanel').isVisible(), true);
  assert.ok(await page.locator('#feedbackList').evaluate(element => element.scrollTop > 0));
  const list = await page.locator('#feedbackList').boundingBox(), entry = await page.locator('.feedback-item').last().boundingBox();
  assert.ok(entry.y >= list.y && entry.y + entry.height <= list.y + list.height + 1);
  await page.getByRole('button', { name: 'Edit annotation 18', exact: true }).click();
  await page.getByRole('textbox', { name: 'Edit annotation 18 feedback' }).fill('Draft kept while collapsed');
  await page.locator('#feedbackOpen').click();
  await page.locator('#feedbackOpen').click();
  assert.equal(await page.getByRole('textbox', { name: 'Edit annotation 18 feedback' }).inputValue(), 'Draft kept while collapsed');
});
