const { test } = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { buildSync } = require('../web/node_modules/esbuild');
const usageSchema = require('../schemas/usage.schema.json');
const { outputFiles } = buildSync({ entryPoints: [path.join(__dirname, '../web/src/chart-selection.ts')], bundle: true, platform: 'node', format: 'cjs', write: false });
const loaded = { exports: {} };
new Function('module', 'exports', outputFiles[0].text)(loaded, loaded.exports);
const { isSliceTerm, readSlice, updateSlice, withSlice } = loaded.exports;
const all = { values: [], excluded: false };
const field = 'model_family';
const core = import(pathToFileURL(path.join(__dirname, '../web/node_modules/@pythia-software/query-table-core/dist/index.js')).href);

test('only, additive toggle, exclusion, and reset have explicit selection semantics', () => {
  const only = updateSlice(all, 'opus-5-5', 'only');
  assert.deepEqual(only, { values: ['opus-5-5'], excluded: false });
  assert.deepEqual(updateSlice(only, 'opus-5-5', 'only'), only);
  const multiple = updateSlice(only, 'gpt-6-sol', 'toggle');
  assert.deepEqual(multiple, { values: ['opus-5-5', 'gpt-6-sol'], excluded: false });
  assert.deepEqual(updateSlice(multiple, 'opus-5-5', 'toggle'), { values: ['gpt-6-sol'], excluded: false });
  const except = updateSlice(multiple, 'gemini-3.8-flash', 'exclude');
  assert.deepEqual(except, { values: ['gemini-3.8-flash'], excluded: true });
  const hidden = updateSlice(except, 'opus-5-5', 'toggle');
  assert.deepEqual(hidden, { values: ['gemini-3.8-flash', 'opus-5-5'], excluded: true });
  assert.deepEqual(updateSlice(hidden, 'opus-5-5', 'toggle'), except);
  assert.deepEqual(updateSlice(except, 'gemini-3.8-flash', 'toggle'), all);
  assert.deepEqual(updateSlice(hidden, '', 'clear'), all);
});

test('selection writes preserve date, repository, and unrelated model predicates', () => {
  const preserved = [{ field: 'last_usage_at', op: '>=', value: '2026-09-01' }, { field: 'repository_name', op: '=', value: 'pharos' }, { field, op: 'contains', value: '5' }];
  const original = [...preserved, { field, op: '=', value: 'opus-5-5' }];
  const changed = withSlice(original, field, { values: ['gpt-6-sol'], excluded: true });
  assert.deepEqual(changed.slice(0, 3), preserved);
  assert.deepEqual(readSlice(changed, field), { values: ['gpt-6-sol'], excluded: true });
  assert.deepEqual(withSlice(changed, field, all), preserved);
  assert.deepEqual(original, [...preserved, { field, op: '=', value: 'opus-5-5' }]);
});

test('all except a model includes missing and future models, not just visible legend values', async () => {
  const { EMPTY_QUERY, applyQuery, loadSchema } = await core;
  const rows = ['opus-5-5', 'gpt-6-sol', 'model-hidden-in-other', 'future-model', null, ''].map(model_family => ({ model_family }));
  const where = withSlice([], field, { values: ['opus-5-5'], excluded: true });
  assert.deepEqual(applyQuery(rows, { ...EMPTY_QUERY, where }, loadSchema(usageSchema)).rows, rows.slice(1));
  assert.ok(where.every(term => isSliceTerm(term, field)));
});

test('excluding missing values and multiple exclusions round-trip and match the query engine', async () => {
  const { EMPTY_QUERY, applyQuery, loadSchema } = await core;
  const rows = ['opus-5-5', 'gpt-6-sol', 'gemini-3.8-flash', null, ''].map(model_family => ({ model_family }));
  for (const values of [[null], ['opus-5-5', 'gpt-6-sol'], ['opus-5-5', null]]) {
    const selection = { values, excluded: true };
    const where = withSlice([], field, selection);
    assert.deepEqual(readSlice(where, field), selection);
    const expected = rows.filter(row => !values.includes(row.model_family === '' ? null : row.model_family));
    assert.deepEqual(applyQuery(rows, { ...EMPTY_QUERY, where }, loadSchema(usageSchema)).rows, expected);
  }
});

test('inclusions with an unknown value remain a single OR selection', () => {
  const selection = { values: ['opus-5-5', null], excluded: false };
  const where = withSlice([], field, selection);
  assert.equal(where.length, 1);
  assert.deepEqual(readSlice(where, field), selection);
});

test('plain not-equal filters preserve missing repositories through legend actions', async () => {
  const { EMPTY_QUERY, applyQuery, loadSchema } = await core;
  const repositoryField = 'repository_name';
  const time = { field: 'last_usage_at', op: '>=', value: '2026-09-01' };
  const where = [time, { field: repositoryField, op: '!=', value: 'foo' }];
  const selection = readSlice(where, repositoryField);
  assert.deepEqual(selection, { values: ['foo'], excluded: true });
  assert.equal(selection.values.includes(null), false);
  const rows = ['foo', 'bar', 'baz', null, ''].map(repository_name => ({ repository_name, last_usage_at: '2026-10-01' }));
  rows.push({ last_usage_at: '2026-10-01' });
  for (const [next, expected] of [[selection, rows.slice(1)], [updateSlice(selection, 'bar', 'toggle'), rows.slice(2)]]) {
    const rewritten = withSlice(where, repositoryField, next);
    assert.deepEqual(rewritten[0], time);
    assert.ok(!rewritten.some(term => term.op === 'is_not_null'));
    assert.deepEqual(readSlice(rewritten, repositoryField), next);
    for (const document of [usageSchema, require('../schemas/writing.schema.json')]) {
      assert.deepEqual(applyQuery(rows, { ...EMPTY_QUERY, where: rewritten }, loadSchema(document)).rows, expected);
    }
  }
});

test('negated equality and explicit null filters retain their null-excluding semantics', () => {
  assert.deepEqual(readSlice([{ field, op: '=', value: 'opus-5-5', negated: true }], field), { values: ['opus-5-5', null], excluded: true });
  assert.deepEqual(readSlice([{ field, op: 'is_not_null', value: '' }], field), { values: [null], excluded: true });
  assert.deepEqual(readSlice([{ field, op: 'is_null', value: '', negated: true }], field), { values: [null], excluded: true });
  assert.equal(isSliceTerm({ any: [{ field, op: '=', value: 'opus-5-5' }, { field: 'provider', op: '=', value: 'codex' }] }, field), false);
});
