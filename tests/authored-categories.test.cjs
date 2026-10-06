const { test } = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { buildSync } = require('../web/node_modules/esbuild');
const { outputFiles } = buildSync({ entryPoints: [path.join(__dirname, '../web/src/authored.tsx')], bundle: true, platform: 'node', format: 'cjs', write: false, nodePaths: [path.join(__dirname, '../web/node_modules')] });
const loaded = { exports: {} };
new Function('module', 'exports', 'require', outputFiles[0].text)(loaded, loaded.exports, require);
const { toggleCategory } = loaded.exports;
const core = import(pathToFileURL(path.join(__dirname, '../web/node_modules/@pythia-software/query-table-core/dist/index.js')).href);

test('legend categories collect in one ALL set filter the query builder can edit', async () => {
  const { loadSchema, readSetFilter, toServerQuery } = await core;
  const schema = loadSchema(require('../schemas/writing_messages.schema.json'));
  const repository = { field: 'repository_name', op: '=', value: 'example/repo' };
  const typed = toggleCategory([repository], 'typed');
  assert.deepEqual(typed[0], repository);
  const both = toggleCategory(typed, 'pasted');
  const set = readSetFilter(both, 1);
  assert.equal(set.field, 'categories');
  assert.deepEqual({ mode: set.metadata.mode, values: set.metadata.values, count: set.count }, { mode: 'all', values: ['typed', 'pasted'], count: 2 });
  assert.equal(set.metadata.id, readSetFilter(typed, 1).metadata.id);
  // Servers see only the existing predicates.
  assert.deepEqual(toServerQuery({ select: [], where: both, orderBy: [], limit: 50, offset: 0 }, schema).where,
    [repository, { field: 'categories', op: 'includes', value: 'typed' }, { field: 'categories', op: 'includes', value: 'pasted' }]);
  // Removing one keeps the rest as an intact set; removing the last removes it.
  const pasted = toggleCategory(both, 'typed');
  assert.deepEqual(readSetFilter(pasted, 1).metadata.values, ['pasted']);
  assert.deepEqual(toggleCategory(pasted, 'pasted'), [repository]);
});

test('legend toggles leave other set modes intact and still clear plain category filters', async () => {
  const { createSetFilter, readSetFilter } = await core;
  const none = createSetFilter('categories', 'none', ['harness'], 'editor-1');
  const added = toggleCategory(none, 'typed');
  assert.deepEqual(added.slice(0, none.length), none);
  assert.deepEqual(readSetFilter(added, none.length).metadata.values, ['typed']);
  assert.deepEqual(toggleCategory([{ field: 'categories', op: 'includes', value: 'typed' }], 'typed'), []);
});
