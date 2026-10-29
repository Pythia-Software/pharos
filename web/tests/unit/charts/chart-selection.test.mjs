import { test } from 'node:test';
import assert from 'node:assert/strict';
import { EMPTY_QUERY, applyQuery, loadSchema } from '@pythia-software/query-table-core';
import usageSchema from '../../../../schemas/usage.schema.json';
import writingSchema from '../../../../schemas/writing.schema.json';
import { isSliceTerm, readSlice, updateSlice, withSlice } from '../../../src/chart-selection';
import { chartGesture, chartInspectionRange } from '../../../src/chart-inspection';
const all = { values: [], excluded: false };
const field = 'model_family';

test('one displayed period inspects, while two or more periods filter in either direction', () => {
  assert.deepEqual(chartGesture(3, 3), { first: 3, last: 3, count: 1, inspect: true });
  assert.deepEqual(chartGesture(1, 4), { first: 1, last: 4, count: 4, inspect: false });
  assert.deepEqual(chartGesture(4, 1), chartGesture(1, 4));
  const endpoints = [2, 3, 5, 2].map(current => chartGesture(2, current));
  assert.equal(endpoints[2].inspect, false);
  assert.equal(endpoints.at(-1).inspect, true, 'dragging out and back still inspects');
});

test('inspection covers whole displayed buckets, cumulative history, and rolling windows across DST', () => {
  const previous = process.env.TZ;
  process.env.TZ = 'America/Denver';
  try {
    const bucket = { key: '2026-11-02', date: new Date(2026, 10, 2), values: {} };
    const daily = chartInspectionRange(bucket, 'day', 1, 'interval');
    assert.equal(daily.from.getTime(), bucket.date.getTime());
    assert.equal(daily.to.getTime(), new Date(2026, 10, 3).getTime());
    assert.equal(chartInspectionRange(bucket, 'days', 3, 'interval').to.getTime(), new Date(2026, 10, 5).getTime());
    const rolling = chartInspectionRange({ ...bucket, windowSize: 2 }, 'day', 1, 'interval');
    assert.equal(rolling.from.getTime(), new Date(2026, 10, 1).getTime());
    assert.equal(rolling.to - rolling.from, 49 * 3_600_000);
    assert.equal(rolling.rolling, true);
    const start = new Date(2026, 9, 15, 12);
    const cumulative = chartInspectionRange({ ...bucket, windowSize: 99 }, 'week', 1, 'cumulative', start);
    assert.equal(cumulative.from.getTime(), start.getTime());
    assert.equal(cumulative.to.getTime(), new Date(2026, 10, 9).getTime());
    assert.equal(cumulative.rolling, false);
  } finally {
    if (previous === undefined) delete process.env.TZ; else process.env.TZ = previous;
  }
});

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

test('all except a model includes missing and future models, not just visible legend values', () => {
  const rows = ['opus-5-5', 'gpt-6-sol', 'model-hidden-in-other', 'future-model', null, ''].map(model_family => ({ model_family }));
  const where = withSlice([], field, { values: ['opus-5-5'], excluded: true });
  assert.deepEqual(applyQuery(rows, { ...EMPTY_QUERY, where }, loadSchema(usageSchema)).rows, rows.slice(1));
  assert.ok(where.every(term => isSliceTerm(term, field)));
});

test('excluding missing values and multiple exclusions round-trip and match the query engine', () => {
  const rows = ['opus-5-5', 'gpt-6-sol', 'gemini-3.8-flash', null, ''].map(model_family => ({ model_family }));
  for (const values of [[null], ['opus-5-5', 'gpt-6-sol'], ['opus-5-5', null]]) {
    const selection = { values, excluded: true };
    const where = withSlice([], field, selection);
    assert.deepEqual(readSlice(where, field), selection);
    const expected = rows.filter(row => !values.includes(row.model_family === '' ? null : row.model_family));
    assert.deepEqual(applyQuery(rows, { ...EMPTY_QUERY, where }, loadSchema(usageSchema)).rows, expected);
  }
});

test('an OR of conjunctions is never one field\'s slice and survives slice edits', () => {
  const pairs = { any: [{ all: [{ field, op: '=', value: 'opus-5-5' }, { field: 'provider', op: '=', value: 'claude' }] }, { field, op: '=', value: 'gpt-6-sol' }] };
  assert.equal(isSliceTerm(pairs, field), false);
  assert.deepEqual(readSlice([pairs], field), all);
  assert.deepEqual(withSlice([pairs], field, { values: ['opus-5-5'], excluded: false }), [pairs, { field, op: '=', value: 'opus-5-5' }]);
});

test('inclusions with an unknown value remain a single OR selection', () => {
  const selection = { values: ['opus-5-5', null], excluded: false };
  const where = withSlice([], field, selection);
  assert.equal(where.length, 1);
  assert.deepEqual(readSlice(where, field), selection);
});

test('plain not-equal filters preserve missing repositories through legend actions', () => {
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
    for (const document of [usageSchema, writingSchema]) {
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
