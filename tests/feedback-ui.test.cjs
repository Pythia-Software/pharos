// Run: npm install --prefix .context/browser-tests playwright --no-audit --no-fund
//      node --test tests/feedback-ui.test.cjs
// Use FEEDBACK_BROWSER=webkit to check the Mac app's browser engine.
const { test, before, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');
const root = path.resolve(__dirname, '..');
const { chromium, webkit } = createRequire(path.join(root, '.context/browser-tests/package.json'))('playwright');
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
  const panelPosition = await drag(page, panel, page.getByRole('button', { name: 'Move feedback panel' }), -600, -300);
  await page.locator('#feedbackClose').click();
  await page.locator('#feedbackOpen').click();
  samePosition(await panel.boundingBox(), panelPosition);

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
  samePosition(await panel.boundingBox(), panelPosition);
  samePosition(await pin.boundingBox(), pinPosition);
  samePosition(await toast.boundingBox(), toastPosition);
  await page.locator('.feedback-item button').click();
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
  await drag(page, page.locator('#feedbackPanel'), page.getByRole('button', { name: 'Move feedback panel' }), -600, -300);
  await modal.getByRole('button', { name: 'Close review' }).click();
  await page.waitForFunction(() => document.querySelector('.feedback-toolbar').parentElement === document.body);
  samePosition(await toolbar.boundingBox(), toolbarPosition);
  assert.equal(await page.locator('.feedback-pin').count(), 0);
  await page.evaluate(() => openFeedbackComposer(feedbackAnnotations[0], 'Another note'));
  samePosition(await page.locator('.feedback-composer').boundingBox(), composerPosition);
});
