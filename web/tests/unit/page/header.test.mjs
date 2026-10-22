import { test } from 'node:test';
import assert from 'node:assert/strict';
import { loadClassicScript, plain } from '../helpers/classic-script.mjs';

test('headers overflow only tools that do not fit and retain saved order', () => {
  const header = loadClassicScript('header.js');
  const saved = ['theme', 'library', 'history', 'settings', 'search'];
  const widths = { theme: 36, library: 120, history: 71, settings: 36, search: 36, sync: 36 };
  const place = (tools, available) => plain(header.headerToolPlacement(tools, available, widths, 6, 36));
  assert.deepEqual(place(saved, 323), { inline: saved, overflow: [] }, 'all tools stay inline at the exact fit');
  assert.deepEqual(place(saved, 322), { inline: ['history', 'settings', 'search'], overflow: ['theme', 'library'] }, 'More replaces the leftmost actions and keeps its own space');
  assert.deepEqual(place(saved, 197), { inline: ['history', 'settings', 'search'], overflow: ['theme', 'library'] });
  assert.deepEqual(place(saved, 196), { inline: ['settings', 'search'], overflow: ['theme', 'library', 'history'] });
  assert.deepEqual(place(['settings', 'sync'], 78), { inline: ['settings', 'sync'], overflow: [] });
  assert.deepEqual(place(['settings'], 36), { inline: ['settings'], overflow: [] }, 'a minimal layout does not need More');
  assert.deepEqual(place(saved, 78), { inline: ['search'], overflow: ['theme', 'library', 'history', 'settings'] });
  assert.deepEqual(place(saved, 77), { inline: [], overflow: saved });
  assert.deepEqual(place(saved, 40), { inline: [], overflow: saved });
  assert.deepEqual(place([], 40), { inline: [], overflow: [] });
  assert.deepEqual(place(saved, 323), { inline: saved, overflow: [] }, 'tools return as soon as there is room');
  assert.deepEqual(saved, ['theme', 'library', 'history', 'settings', 'search']);
});

test('navigation switches only after all actions collect, then restores actions above its dropdown row', () => {
  const header = loadClassicScript('header.js');
  const tools = ['history', 'search', 'settings', 'theme'];
  const dimensions = { brandWidth: 120, tabsWidth: 300, widths: { history: 71, search: 36, settings: 36, theme: 36 }, gap: 6, moreWidth: 36, barGap: 16 };
  const place = (width, changes = {}) => plain(header.headerNavigationPlacement(tools, { ...dimensions, width, ...changes }));
  assert.deepEqual(place(649), { phone: false, inline: tools, overflow: [] });
  assert.deepEqual(place(488), { phone: false, inline: [], overflow: tools }, 'tabs and More fit exactly');
  assert.deepEqual(place(487), { phone: true, inline: tools, overflow: [] }, 'the second row frees room for every action');
  assert.deepEqual(place(256), { phone: true, inline: ['settings', 'theme'], overflow: ['history', 'search'] });
  assert.deepEqual(place(172), { phone: true, inline: [], overflow: tools });
  assert.deepEqual(place(488), { phone: false, inline: [], overflow: tools }, 'growing the window restores the tabs without hysteresis');
  assert.equal(place(488, { tabsWidth: 301 }).phone, true, 'renaming a tab changes the measured transition');
  assert.equal(place(488, { brandWidth: 121 }).phone, true, 'brand size is included');
  assert.equal(place(488, { barGap: 17 }).phone, true, 'header spacing is included');
  assert.equal(place(488, { moreWidth: 37 }).phone, true, 'More uses its measured size');
  assert.deepEqual(place(256, { widths: { ...dimensions.widths, theme: 120 } }), { phone: true, inline: [], overflow: tools }, 'wider action labels change what fits');
});

test('header defaults start with navigation and preserve previous visibility choices', () => {
  const header = loadClassicScript('header.js');
  const defaults = plain(header.defaultHeaderTools());
  assert.equal(defaults[0], 'history');
  assert.equal(defaults[1], 'library');
  assert.ok(defaults.includes('theme'));
  assert.ok(defaults.includes('summary'));
  assert.ok(!defaults.includes('navigator'));
  const legacy = new Map([
    ['pharos-show-nav-button', true], ['pharos-show-agent-annotation-button', true],
    ['pharos-show-theme-button', false], ['pharos-show-usage-summary-button', false],
  ]);
  const migrated = plain(header.normalizeHeaderTools(null, (key, fallback) => legacy.get(key) ?? fallback));
  assert.ok(migrated.includes('navigator'));
  assert.ok(migrated.includes('annotations'));
  assert.ok(!migrated.includes('theme'));
  assert.ok(!migrated.includes('summary'));
  assert.deepEqual(plain(header.defaultHeaderTools()), defaults, 'reset ignores legacy visibility choices');
});

test('saved header layouts keep their order and omissions, reject unknowns, and retain Settings', () => {
  const header = loadClassicScript('header.js');
  assert.deepEqual(plain(header.normalizeHeaderTools(['theme', 'history', 'theme', 'retired', null, {}])), ['theme', 'history', 'settings']);
  assert.deepEqual(plain(header.normalizeHeaderTools([])), ['settings']);
  assert.deepEqual(plain(header.normalizeHeaderTools(['settings', 'search'])), ['settings', 'search']);
  for (const invalid of [null, false, 'search', {}]) {
    assert.deepEqual(plain(header.normalizeHeaderTools(invalid)), plain(header.defaultHeaderTools()));
  }
});

test('header tools move in either direction, add once, and return to the toolbox', () => {
  const { editHeaderTools: edit } = loadClassicScript('header.js');
  const start = ['history', 'library', 'settings', 'search'];
  assert.deepEqual(plain(edit(start, 'search', 0)), ['search', 'history', 'library', 'settings']);
  assert.deepEqual(plain(edit(start, 'history', 4)), ['library', 'settings', 'search', 'history']);
  assert.deepEqual(plain(edit(start, 'library', 1)), start);
  assert.deepEqual(plain(edit(start, 'library', 2)), start);
  assert.deepEqual(plain(edit(start, 'theme', 1)), ['history', 'theme', 'library', 'settings', 'search']);
  assert.deepEqual(plain(edit(start, 'history', null)), ['library', 'settings', 'search']);
  assert.deepEqual(plain(edit(start, 'settings', null)), start);
  assert.deepEqual(plain(edit(start, 'settings', 0)), ['settings', 'history', 'library', 'search'], 'Settings can still be repositioned');
  assert.deepEqual(plain(edit(start, 'unknown', 0)), start);
  assert.deepEqual(start, ['history', 'library', 'settings', 'search'], 'edits do not mutate saved input');
  const all = plain(edit(edit(start, 'theme', 0), 'theme', 99));
  assert.equal(all.filter(id => id === 'theme').length, 1);
});

test('Settings survives removing every optional tool and restoring an incomplete saved layout', () => {
  const header = loadClassicScript('header.js');
  let tools = plain(header.defaultHeaderTools());
  for (const tool of header.HEADER_TOOLS) {
    tools = plain(header.editHeaderTools(tools, tool.id, null));
    assert.ok(tools.includes('settings'), `Settings remains accessible after removing ${tool.id}`);
  }
  assert.deepEqual(tools, ['settings']);
  assert.deepEqual(plain(header.normalizeHeaderTools(['back', 'sync'])), ['back', 'sync', 'settings']);
});

test('compact variants replace the paired control in place or at a chosen drop boundary', () => {
  const header = loadClassicScript('header.js');
  const start = ['history', 'library', 'search', 'settings'];
  const add = (tools, id) => plain(header.editHeaderTools(tools, id, header.headerToolAddPosition(tools, id)));
  const compact = add(add(start, 'back'), 'sync');
  assert.deepEqual(compact, ['back', 'sync', 'search', 'settings']);
  assert.deepEqual(add(add(compact, 'history'), 'library'), start);
  assert.deepEqual(plain(header.editHeaderTools(start, 'back', start.length)), ['library', 'search', 'settings', 'back']);
  assert.deepEqual(plain(header.editHeaderTools(start, 'sync', 0)), ['sync', 'history', 'search', 'settings']);
  assert.deepEqual(plain(header.editHeaderTools(start, 'back', null)), start, 'removing an unused alternative leaves the active control alone');
  assert.deepEqual(plain(header.normalizeHeaderTools(['sync', 'library', 'back', 'history', 'settings'])), ['sync', 'back', 'settings']);
  assert.deepEqual(add(['settings'], 'sync'), ['settings', 'sync']);
  assert.ok(!header.defaultHeaderTools().includes('back'));
  assert.ok(!header.defaultHeaderTools().includes('sync'));
});

function historyHarness() {
  const entries = [null], values = new Map(), buttons = { navBack: {}, navForward: {}, headerBackOnly: {} };
  let index = 0, changed = 0;
  const history = {
    get state() { return entries[index]; },
    pushState(state) { entries.splice(++index, entries.length, state); },
    replaceState(state) { entries[index] = state; },
  };
  const globals = {
    history,
    sessionStorage: { getItem: key => values.get(key) ?? null, setItem: (key, value) => values.set(key, value) },
    document: { getElementById: id => buttons[id] },
  };
  let header = loadClassicScript('header.js', globals);
  const install = () => header.initHeaderHistory(() => changed++);
  const sync = install();
  return {
    history, buttons, sync,
    pop(to) { index = to; header.window.dispatchEvent(new header.window.Event('popstate')); },
    reload() {
      // A new page gets native history methods and the same session storage.
      history.pushState = state => { entries.splice(++index, entries.length, state); };
      history.replaceState = state => { entries[index] = state; };
      header = loadClassicScript('header.js', globals);
      install();
    },
    get changed() { return changed; },
  };
}

test('history arrows track push, replace, traversal, reload and a new forward branch', () => {
  const h = historyHarness();
  const enabled = (back, forward) => {
    assert.equal(h.buttons.navBack.disabled, !back);
    assert.equal(h.buttons.headerBackOnly.disabled, !back);
    assert.equal(h.buttons.navForward.disabled, !forward);
  };
  enabled(false, false);
  h.history.pushState({ query: 'saved' }, '', '/library');
  enabled(true, false);
  h.history.replaceState({ query: 'edited' }, '', '/library?q=edited');
  assert.equal(h.history.state.pharosIndex, 1);
  assert.equal(h.history.state.query, 'edited');
  h.history.pushState(null, '', '/settings');
  h.pop(0);
  enabled(false, true);
  h.reload();
  enabled(false, true);
  h.pop(1);
  enabled(true, true);
  h.pop(2);
  enabled(true, false);
  h.pop(0);
  h.history.pushState(null, '', '/tools');
  enabled(true, false);
  h.pop(0);
  enabled(false, true);
  h.pop(1);
  enabled(true, false);
  assert.ok(h.changed > 0);
});

test('history still supports a forward branch when session storage is unavailable', () => {
  let state = null;
  const buttons = { navBack: {}, navForward: {} };
  const header = loadClassicScript('header.js', {
    document: { getElementById: id => buttons[id] },
    sessionStorage: { getItem() { throw new Error('storage disabled'); }, setItem() { throw new Error('storage disabled'); } },
    history: { get state() { return state; }, pushState(value) { state = value; }, replaceState(value) { state = value; } },
  });
  const sync = header.initHeaderHistory(() => {});
  header.window.history.pushState(null, '', '/library');
  assert.equal(buttons.navBack.disabled, false);
  state = { pharosIndex: 0 };
  sync();
  assert.equal(buttons.navForward.disabled, false);
});
