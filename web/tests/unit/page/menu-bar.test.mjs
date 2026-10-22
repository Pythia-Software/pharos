import { test } from 'node:test';
import assert from 'node:assert/strict';
import fixtures from '../../../../testdata/menu-bar-layout.json';
import { loadClassicScript, plain } from '../helpers/classic-script.mjs';

for (const fixture of fixtures) test(`Mac menu preferences: ${fixture.name}`, () => {
  const menu = loadClassicScript('menu-bar.js');
  const settings = plain(menu.normalizeMenuBarSettings(fixture.saved));
  assert.deepEqual(settings.order, fixture.order);
  assert.equal(settings.enabled, fixture.enabled);
  for (const tool of menu.MENU_BAR_TOOLS) assert.equal(settings[tool.id], fixture.order.includes(tool.id));
  assert.deepEqual(plain(menu.normalizeMenuBarSettings(settings)), settings, 'normalized preferences survive reload');
});

test('Mac menu edits move either way, add once, remove, and preserve essential actions', () => {
  const menu = loadClassicScript('menu-bar.js');
  const edit = (settings, id, position) => plain(menu.editMenuBarSettings(settings, id, position));
  const start = plain(menu.normalizeMenuBarSettings({layoutVersion: 2}));
  const up = edit(start, 'tokens', 2);
  assert.deepEqual(up.order, ['open', 'status', 'tokens', 'indexed', 'words', 'carbon', 'settings', 'quit']);
  const down = edit(up, 'tokens', 5);
  assert.deepEqual(down, start);
  const added = edit(start, 'cost', 1);
  assert.equal(added.order[1], 'cost');
  assert.equal(added.cost, true);
  assert.deepEqual(edit(added, 'cost', 1), added);
  const removed = edit(added, 'cost', null);
  assert.deepEqual(removed, start);
  for (const tool of menu.MENU_BAR_TOOLS.filter(tool => tool.required)) assert.deepEqual(edit(start, tool.id, null), start);
  assert.deepEqual(edit(start, 'retired', 0), start);
  assert.deepEqual(edit({...start, enabled: false}, 'words', 0).enabled, false);
});


test('Mac menu separators can be added repeatedly, moved independently, and removed completely', () => {
  const menu = loadClassicScript('menu-bar.js');
  const edit = (settings, id, position) => plain(menu.editMenuBarSettings(settings, id, position));
  const start = plain(menu.normalizeMenuBarSettings({layoutVersion: 2}));
  let settings = edit(start, 'separator', 1);
  const first = settings.order[1];
  settings = edit(settings, 'separator', 4);
  const second = settings.order[4];
  assert.notEqual(first, second);
  assert.ok(menu.isMenuBarSeparator(first) && menu.isMenuBarSeparator(second));
  settings = edit(settings, first, settings.order.length);
  assert.equal(settings.order.at(-1), first);
  assert.equal(settings.order.indexOf(second), 3, 'moving one separator leaves the other in place');
  settings = edit(settings, second, null);
  assert.ok(settings.order.includes(first));
  assert.ok(!settings.order.includes(second));
  assert.deepEqual(edit(settings, first, null), start, 'the last divider stays removed after normalization');
  assert.deepEqual(edit(start, 'separator', null), start, 'returning the toolbox factory does nothing');
  settings = plain(menu.normalizeMenuBarSettings());
  for (const id of settings.order.filter(menu.isMenuBarSeparator)) settings = edit(settings, id, null);
  assert.ok(!plain(menu.normalizeMenuBarSettings(settings)).order.some(menu.isMenuBarSeparator), 'migrated dividers can all be removed');
});


test('new Mac menu shortcuts remain opt in and leave default dividers unchanged', () => {
  const menu = loadClassicScript('menu-bar.js');
  const defaults = plain(menu.normalizeMenuBarSettings());
  const shortcuts = ['search', 'analyze', 'optimize', 'capture', 'health', 'sources', 'finder'];
  for (const id of shortcuts) {
    assert.equal(defaults[id], false);
    assert.ok(!defaults.order.includes(id));
    const added = plain(menu.editMenuBarSettings(defaults, id, 1));
    assert.equal(added[id], true);
    assert.equal(added.order[1], id);
    assert.deepEqual(plain(menu.normalizeMenuBarSettings(added)), added);
    assert.deepEqual(plain(menu.editMenuBarSettings(added, id, null)), defaults);
  }
});


test('Mac menu preview includes the current stage and remaining estimate in status text', () => {
  const { menuBarActivityDetail: detail } = loadClassicScript('menu-bar.js');
  const now = Date.parse('2026-10-22T13:22:00Z');
  assert.equal(detail({label: 'Manual sync: evaluating findings', progress: .22, estimated_completion_at: '2026-10-22T13:23:00Z'}, now),
    'Manual sync: evaluating findings · stage 22% · about 1 min for this stage');
  assert.equal(detail({label: 'Indexing', estimated_completion_at: '2026-10-22T13:21:00Z'}, now), 'Indexing');
  assert.equal(detail({estimated_completion_at: 'invalid'}, now), 'Working');
});
