const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createRequire } = require('node:module');

const root = path.resolve(__dirname, '..');
const { chromium } = createRequire(path.join(root, '.context/browser-tests/package.json'))('playwright');
const shell = fs.readFileSync(path.join(root, 'internal/archive/assets/ui.py'), 'utf8');
const styles = [...shell.matchAll(/<style>([\s\S]*?)<\/style>/g)].map(match => match[0]).join('\n');
const bundleCSS = fs.readFileSync(path.join(root, 'internal/archive/assets/query-tables.css'), 'utf8');
const select = (id, attributes = '') => `<select id="${id}" ${attributes}><option>Selected label</option><option>Other label</option></select>`;
// Exercise the shipped component rules after the shell, including background
// shorthands that reset the caret's repeat, size, and position independently.
const fixture = `<!doctype html><html><head>${styles}<style>${bundleCSS}</style></head><body>
  ${select('plain')}
  <div class="qt-metrics-editor">
    <div class="qt-metrics-editor-header">${select('workbench')}</div>
    <div class="qt-metric-selection-tools">${select('metric')}</div>
    <div class="qt-metric-basic">${select('aggregation')}${select('measure')}</div>
    <div class="qt-metric-reference">${select('reference')}</div>
  </div>
  <div class="qt-property-controls"><div class="qt-property-view-picker">${select('properties')}</div></div>
  <div class="usage-chart-controls"><div class="usage-chart-select">${select('chart')}<svg class="usage-chart-caret"></svg></div></div>
  <div class="text-search-editor"><span class="text-select">${select('search')}<svg class="app-icon"></svg></span></div>
  <div class="text-condition-popup"><span class="text-select">${select('condition')}<svg class="app-icon"></svg></span></div>
  <div class="card-library"><div class="card-library-controls">${select('library')}</div></div>
  <div class="home-editor card-template-editor">${select('template')}</div>
  ${select('multiple', 'multiple')}
</body></html>`;

test('select carets stay single and aligned across explicit and system themes', async () => {
  const chrome = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  const browser = await chromium.launch({ headless: true, ...(fs.existsSync(chrome) ? { executablePath: chrome } : {}) });
  try {
    const page = await browser.newPage();
    await page.setContent(fixture);
    for (const colorScheme of ['light', 'dark']) {
      await page.emulateMedia({ colorScheme });
      for (const theme of ['light', 'dark', null]) {
        await page.evaluate(theme => {
          if (theme) document.documentElement.dataset.theme = theme;
          else document.documentElement.removeAttribute('data-theme');
        }, theme);
        const controls = await page.locator('select').evaluateAll(elements => elements.map(element => {
          const style = getComputedStyle(element);
          return { id: element.id, image: style.backgroundImage, repeat: style.backgroundRepeat,
            size: style.backgroundSize, x: style.backgroundPositionX, y: style.backgroundPositionY,
            padding: style.paddingRight, appearance: style.appearance };
        }));
        for (const control of controls) {
          const context = `${control.id}, theme=${theme ?? 'system'}, OS=${colorScheme}`;
          if (['chart', 'search', 'condition', 'library', 'template', 'multiple'].includes(control.id)) {
            assert.equal(control.image, 'none', context);
            assert.equal(control.appearance, ['library', 'template', 'multiple'].includes(control.id) ? 'auto' : 'none', context);
            continue;
          }
          assert.equal(control.repeat, 'no-repeat', context);
          assert.equal(control.size, '14px 14px', context);
          assert.equal(control.x, 'calc(100% - 9px)', context);
          assert.equal(control.y, '50%', context);
          assert.equal(control.padding, '28px', context);
          assert.equal(control.appearance, 'none', context);
          assert.match(control.image, (theme === 'dark' || (!theme && colorScheme === 'dark')) ? /%23b5ab93/ : /%23756c5f/, context);
        }
      }
    }
  } finally { await browser.close(); }
});
