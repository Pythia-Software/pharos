import assert from 'node:assert/strict';
import test from 'node:test';
import { loadSchema } from '@pythia-software/query-table-core';
import toolCalls from '../../../../schemas/tool_calls.schema.json';
import { freshRecordKeywordSearch, readRecordKeywordSearch, recordKeywordFields, recordKeywordParameter, recordKeywordRequest } from '../../../src/record-keyword-model';
import { newRule, newGroup } from '../../../src/text-search-model';
const fields = loadSchema(toolCalls).fields;

test('record keyword filters round-trip separately from table state without inheriting conversation scope', () => {
  const value = freshRecordKeywordSearch('command');
  value.draft.root.children = [newRule('go test'), { ...newGroup(), exclude: true, join: 'any', children: [newRule('obsolete'), { ...newRule('generated file'), kind: 'phrase' }] }];
  const token = JSON.stringify(value);
  const restored = readRecordKeywordSearch(token, fields);
  assert.equal(restored.error, '');
  assert.deepEqual(JSON.parse(recordKeywordRequest(restored.value)), JSON.parse(recordKeywordRequest(value)));
  const request = JSON.parse(recordKeywordRequest(value));
  assert.equal(request.field, 'command');
  assert.equal(request.where.op, 'not');
  for (const key of ['surface','workspace_id','conversation_id','from','fuzzy']) assert.equal(Object.hasOwn(request,key),false);
  assert.equal(value.draft.scope.from, '');
  assert.equal(recordKeywordParameter('tool_calls'), 'record_tool_calls');
  assert.equal(recordKeywordParameter('writing_messages'), 'record_writing_messages');
});

test('record keyword fields and saved definitions reject unavailable fields and message scopes', () => {
  assert.ok(recordKeywordFields(fields).some(field => field.name === 'command'));
  assert.ok(!recordKeywordFields(fields).some(field => field.name === 'duration_ms'));
  assert.equal(recordKeywordFields([{ name:'computed',label:'Computed',type:'text',source:{kind:'derived',computedId:'x'} }]).length, 0);
  const valid = freshRecordKeywordSearch('command');
  valid.draft.root.children[0].text = 'cache';
  for (const value of [
    { ...valid, field:'missing' }, { ...valid, field:'duration_ms' },
    { ...valid, draft:{...valid.draft, fuzzy:true} },
    { ...valid, draft:{...valid.draft, scope:{...valid.draft.scope, from:'2026-01-01'}} },
    { ...valid, draft:{...valid.draft, scope:{...valid.draft.scope, surface:'tools'}} },
  ]) {
    const restored = readRecordKeywordSearch(JSON.stringify(value), fields);
    assert.equal(restored.value, null);
    assert.ok(restored.error);
  }
  for (const token of ['invalid', '{}', '[]']) assert.ok(readRecordKeywordSearch(token, fields).error);
  assert.deepEqual(readRecordKeywordSearch(null, fields), { value:null, error:'' });
});

test('record syntax and literal options preserve their matching definition', () => {
  const search = freshRecordKeywordSearch('command');
  search.draft.editor='syntax'; search.draft.text='(cache OR buffer) NOT obsolete';
  search.draft.caseSensitive=true; search.draft.separators=true;
  const result=readRecordKeywordSearch(JSON.stringify(search),fields);
  assert.equal(result.error,'');
  assert.deepEqual(JSON.parse(recordKeywordRequest(result.value)), {field:'command',query:search.draft.text,mode:'advanced',case_sensitive:true,separators:true});
  assert.throws(()=>recordKeywordRequest(freshRecordKeywordSearch('command')),/Invalid/);
});
