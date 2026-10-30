import assert from 'node:assert/strict';
import test from 'node:test';
import { EMPTY_QUERY, decodeQueryStrict, encodeQuery, loadSchema, normalizeQueryState, parseHostQueryState, toMetricQuery } from '@pythia-software/query-table-core';
import toolCalls from '../../../../schemas/tool_calls.schema.json';
import { refreshTodayHost, canonicalDraft, emptyHostState, libraryFind, libraryHostQuery, libraryHostState, messageFind, messageHostQuery, readLibraryHost, readMessageSearchHost, readRecordKeyword, recordKeywordFind, recordKeywordHostQuery, sameHostState, withMessageSearch, withRecordKeyword, withoutLibrarySearch } from '../../../src/host-query-state';
import { hostQueryV3Transport } from '../../../src/host-query-transport';
import { execution, queryCapabilities, answerQuery } from '../../support/query-server.mjs';
import { freshRecordKeywordSearch } from '../../../src/record-keyword-model';
import { withSearchDateWindow, chooseScope, emptyTextSearch, newGroup, newRule } from '../../../src/text-search-model';
import { queryFromURL, queryURL } from '../../../src/query-links';

globalThis.location = new URL('http://localhost/analyze');
const schema = loadSchema(toolCalls), fields = schema.fields;
const host = recordKeywordHostQuery('tool_calls', fields);
// Query Table applies normalize to every restored, loaded and committed state.
const normalized = (options, state) => parseHostQueryState(options.normalize(parseHostQueryState(state)));
const syntaxKeyword = text => { const search = freshRecordKeywordSearch('command'); search.draft.editor = 'syntax'; search.draft.text = text; return search; };

test('a saved Tool calls analysis restores its keyword with the grouped, filtered table query', () => {
  // Grouped by Tool, Status = error, HAVING Calls > 2, Command keyword.
  const source = { ...EMPTY_QUERY, where: [{ field: 'status', op: '=', value: 'error' }] };
  const table = normalizeQueryState({ ...EMPTY_QUERY, grouping: { source, keys: ['tool_name'], summaries: [{ id: 'calls', op: 'count', label: 'Calls' }], representative: null }, where: [{ field: 'calls', op: '>', value: '2' }] });
  const saved = { ...table, hostState: normalized(host, withRecordKeyword('tool_calls', syntaxKeyword('(cache OR buffer) NOT obsolete'))) };
  // The save travels as a link (and durable snapshot) of the whole definition.
  const restored = decodeQueryStrict(encodeQuery(saved));
  assert.deepEqual(restored, saved);
  const state = normalized(host, restored.hostState);
  const keyword = readRecordKeyword(state, 'tool_calls', fields);
  assert.equal(keyword.field, 'command');
  assert.equal(keyword.draft.text, '(cache OR buffer) NOT obsolete');
  assert.deepEqual(JSON.parse(new URLSearchParams(recordKeywordFind(state, 'tool_calls', fields)).get('record_search')),
    { field: 'command', query: '(cache OR buffer) NOT obsolete', mode: 'advanced', case_sensitive: false, separators: false });
  assert.deepEqual(restored.grouping.source.where, source.where);
  assert.deepEqual(restored.where, [{ field: 'calls', op: '>', value: '2' }]);
  // Removing the keyword is a host edit to the default state; an older save
  // with no host state takes the same defaults, which clear an active keyword.
  assert.deepEqual(normalized(host, withRecordKeyword('tool_calls', null)), emptyHostState());
  assert.deepEqual(normalized(host, host.defaults), emptyHostState());
  assert.equal(readRecordKeyword(host.defaults, 'tool_calls', fields), null);
  assert.equal(recordKeywordFind(host.defaults, 'tool_calls', fields), '');
});

test('keyword state is canonical: editor IDs are dropped and rebuilt, and normalizing is stable', () => {
  const search = freshRecordKeywordSearch('command');
  search.draft.root.children = [newRule('go test'), { ...newGroup(), exclude: true, join: 'any', children: [newRule('obsolete'), { ...newRule('generated file'), kind: 'phrase' }] }];
  const state = normalized(host, withRecordKeyword('tool_calls', search));
  const token = state.params.record_tool_calls[0];
  assert.doesNotMatch(token, /"id"/, 'ephemeral rule and group IDs never enter host state');
  assert.deepEqual(normalized(host, state), state);
  assert.ok(sameHostState(state, withRecordKeyword('tool_calls', readRecordKeyword(state, 'tool_calls', fields))));
  // Reading rebuilds unique editable IDs.
  const first = readRecordKeyword(state, 'tool_calls', fields), second = readRecordKeyword(state, 'tool_calls', fields);
  const ids = draft => [draft.root.id, ...draft.root.children.flatMap(child => [child.id, ...(child.children ?? []).map(grand => grand.id)])];
  assert.equal(new Set(ids(first.draft)).size, 5);
  assert.notDeepEqual(ids(first.draft), ids(second.draft));
  assert.equal(canonicalDraft(first.draft), canonicalDraft(second.draft));
  // An editor's draft with its own IDs normalizes to the same state.
  assert.deepEqual(normalized(host, { version: 1, params: { record_tool_calls: [JSON.stringify(search)] } }), state);
});

test('keyword state outside the allowlists, or unreadable, rejects the whole restore', () => {
  const valid = syntaxKeyword('cache');
  const reject = (state, pattern) => assert.throws(() => normalized(host, state), pattern);
  reject({ version: 2, params: {} }, /version/);
  reject({ version: 1, params: { record_mcp_calls: [JSON.stringify(valid)] } }, /unknown setting/, 'another dataset\'s clause');
  reject({ version: 1, params: { record_search: ['{}'] } }, /unknown setting/, 'the compiled request is never host state');
  reject({ version: 1, params: { record_tool_calls: [JSON.stringify(valid), JSON.stringify(valid)] } }, /repeats/);
  for (const value of ['invalid', JSON.stringify({ ...valid, field: 'duration_ms' }), JSON.stringify({ ...valid, draft: { ...valid.draft, fuzzy: true } }),
    JSON.stringify({ ...valid, draft: { ...valid.draft, scope: { ...valid.draft.scope, surface: 'tools' } } })]) reject({ version: 1, params: { record_tool_calls: [value] } }, /could not be read/);
  // Query Table keeps each host value within 4,000 characters; edits past it
  // fail with a message before they reach the table.
  const large = syntaxKeyword(`cache ${'x'.repeat(4000)}`);
  assert.throws(() => withRecordKeyword('tool_calls', large), /too large/);
  assert.throws(() => host.normalize({ version: 1, params: { record_tool_calls: [JSON.stringify(large)] } }), /too large/);
});

test('a Search save keeps its phrase, user-message scope and Conversations view', () => {
  const draft = emptyTextSearch();
  draft.root.children = [{ ...newRule('lock this to another object'), kind: 'phrase' }];
  draft.scope = chooseScope(draft.scope, 'user');
  const state = normalized(libraryHostQuery, libraryHostState({ kind: 'text', text: draft, file: '', fileCase: false, view: 'conversation' }));
  assert.deepEqual(Object.keys(state.params), ['text_builder', 'view']);
  assert.deepEqual(normalized(libraryHostQuery, state), state);
  const saved = decodeQueryStrict(encodeQuery({ ...EMPTY_QUERY, hostState: state }));
  const restored = readLibraryHost(normalized(libraryHostQuery, saved.hostState));
  assert.equal(restored.view, 'conversation');
  assert.equal(restored.text.root.children[0].kind, 'phrase');
  assert.equal(restored.text.root.children[0].text, 'lock this to another object');
  assert.equal(restored.text.scope.surface, 'user');
  const find = new URLSearchParams(libraryFind(state));
  assert.equal(find.get('find_surface'), 'user');
  assert.deepEqual(JSON.parse(find.get('find_where')), { op: 'phrase', text: 'lock this to another object' });
  // Clearing the search keeps the view; the default state has neither.
  assert.deepEqual(readLibraryHost(withoutLibrarySearch(state)), { kind: 'text', text: null, file: '', fileCase: false, view: 'conversation' });
  assert.deepEqual(readLibraryHost(libraryHostQuery.defaults), { kind: 'text', text: null, file: '', fileCase: false, view: 'table' });
  assert.equal(libraryFind(libraryHostQuery.defaults), '');
});

test('Search file and URL searches, and older keyword links, normalize to their canonical parameters', () => {
  const file = normalized(libraryHostQuery, { version: 1, params: { kind: ['file'], search: [' go.mod '], case: ['1'], view: ['table'] } });
  assert.deepEqual(file.params, { case: ['1'], kind: ['file'], search: ['go.mod'] });
  assert.deepEqual(Object.fromEntries(new URLSearchParams(libraryFind(file))), { find: 'go.mod', find_kind: 'file', find_case: '1' });
  assert.deepEqual(normalized(libraryHostQuery, { version: 1, params: { kind: ['url'], search: [''] } }), emptyHostState(), 'an empty file search is no search');
  // The live state (names sorted by Query Table) and a freshly built one
  // compare equal, so re-applying a case-sensitive search runs it again.
  const built = libraryHostState({ kind: 'file', text: null, file: 'go.mod', fileCase: true, view: 'conversation' });
  assert.deepEqual(Object.keys(libraryHostQuery.normalize(built).params), ['case', 'kind', 'search', 'view']);
  assert.ok(sameHostState(libraryHostQuery.normalize(built), parseHostQueryState(built)));
  assert.ok(sameHostState({ version: 1, params: { search: ['a'], case: ['1'] } }, { version: 1, params: { case: ['1'], search: ['a'] } }));
  assert.ok(!sameHostState(built, emptyHostState()));
  // An older keyword link (search, case, text_mode) becomes a text_builder.
  const legacy = normalized(libraryHostQuery, { version: 1, params: { search: ['cache'], case: ['1'], fuzzy: ['0'] } });
  assert.deepEqual(Object.keys(legacy.params), ['text_builder']);
  const text = readLibraryHost(legacy).text;
  assert.equal(text.caseSensitive, true);
  assert.equal(text.root.children[0].text, 'cache');
  for (const params of [{ view: ['grid'] }, { kind: ['folder'] }, { case: ['yes'] }, { text_builder: ['{'] }, { q_library: ['x'] }])
    assert.throws(() => normalized(libraryHostQuery, { version: 1, params }));
});

test('the older Human messages search keeps its definition and leaves its cursor out', () => {
  const draft = emptyTextSearch();
  draft.scope.surface = 'user';
  draft.root.children = [newRule('deploy')];
  const state = normalized(messageHostQuery, withMessageSearch(draft));
  assert.deepEqual(Object.keys(state.params), ['message_query']);
  assert.equal(readMessageSearchHost(state).root.children[0].text, 'deploy');
  const find = new URLSearchParams(messageFind(state));
  assert.equal(find.get('find_surface'), 'user');
  assert.equal(find.has('find_cursor'), false);
  assert.deepEqual(normalized(messageHostQuery, withMessageSearch(null)), emptyHostState());
  assert.throws(() => normalized(messageHostQuery, { version: 1, params: { message_query: ['nope'] } }));
  assert.throws(() => normalized(messageHostQuery, { version: 1, params: { message_cursor: ['abc'] } }), /unknown setting/);
});

test('query links mirror host state in its own parameters and keep it in the encoded query', () => {
  const hostState = normalized(host, withRecordKeyword('tool_calls', syntaxKeyword('cache')));
  const url = new URL(queryURL('tool_calls', { ...EMPTY_QUERY, hostState }), location.origin);
  assert.deepEqual(url.searchParams.getAll('record_tool_calls'), hostState.params.record_tool_calls);
  assert.deepEqual(queryFromURL(url.pathname + url.search).query.hostState, hostState);
  assert.equal(new URL(queryURL('tool_calls', EMPTY_QUERY), location.origin).searchParams.has('record_tool_calls'), false);
});


test('Today host searches normalize stably and renew only at execution boundaries', () => {
  const clock = new Date(2026, 10, 1, 9), next = new Date(2026, 10, 2, 10);
  const draft = withSearchDateWindow(emptyTextSearch(), 'today', clock); draft.root.children[0].text = 'migration';
  for (const [host, initial, read] of [
    [libraryHostQuery, libraryHostState({ kind: 'text', text: draft, file: '', fileCase: false, view: 'table' }), state => readLibraryHost(state).text],
    [messageHostQuery, withMessageSearch(draft), readMessageSearchHost],
  ]) {
    const state = normalized(host, initial);
    assert.deepEqual(normalized(host, state), state);
    assert.equal(refreshTodayHost(state, new Date(+clock+1000), true), state);
    const renewed = refreshTodayHost(state, next, true);
    assert.notEqual(read(renewed).scope.from, draft.scope.from);
    assert.equal(read(renewed).scope.to, next.toISOString());
    assert.deepEqual(normalized(host, renewed), renewed);
    assert.equal(read(state).scope.to, clock.toISOString());
  }
});


test('record host transport runs standalone metrics through the same V3 source as rows', async () => {
 const state = normalized(host, withRecordKeyword('tool_calls', syntaxKeyword('cache')));
 const where = [{field:'status',op:'=',value:'error'}], calls=[];
 const rows=[{id:'a',status:'error',duration_ms:10},{id:'b',status:'ok',duration_ms:20}];
 const transport=hostQueryV3Transport({queryCapabilities,fetchRowsV2(){throw Error('wrong protocol');},fetchMetrics(){throw Error('wrong protocol');},async fetchQueryV3(request,signal){calls.push({request,signal});return answerQuery("query-v3",request,rows,schema);}},schema,execution);
 const query={...EMPTY_QUERY,where,hostState:state,aggregations:[{id:'calls',op:'count',groupBy:[]}]};
 const request={...toMetricQuery(query,schema,execution),hostState:state},signal=new AbortController().signal;
 request.snapshot='frozen';
 const result=await transport.fetchMetrics(request,signal);
 assert.equal(result.metrics[0].buckets[0].value,1);
 assert.equal(transport.fetchRowsV2,undefined,'record hosts must negotiate V3 even for an ungrouped query');
 assert.equal(calls[0].request.rows,false);
 assert.deepEqual(calls[0].request.hostState,state);
 assert.deepEqual(calls[0].request.result.where,where);
 assert.equal(calls[0].request.snapshot,'frozen');assert.equal(calls[0].signal,signal);
 const unsupported=hostQueryV3Transport({},schema,execution);
 await assert.rejects(unsupported.fetchMetrics(request),/cannot run record keyword/);
 request.diagnostics=[{message:'unavailable field'}];
 await assert.rejects(transport.fetchMetrics(request),/unavailable field/);
 assert.equal(calls.length,1,'unsupported predicates must never be dropped');
});


test('clearing a record keyword restores ordinary indexed execution', async () => {
 let current=emptyHostState();const sent=[];
 const ordinary={queryCapabilities,fetchRowsV2:async request=>{sent.push('rows-v2');return {rows:[],total:0};},fetchMetrics:async request=>{sent.push('metrics');return {metrics:[]};}};
 const transport=hostQueryV3Transport(ordinary,schema,execution,state=>Boolean((state??current).params.record_tool_calls));
 await transport.fetchRowsV2({hostState:current});
 await transport.fetchMetrics({hostState:current});
 current=withRecordKeyword('tool_calls',syntaxKeyword('cache'));
 assert.equal(transport.fetchRowsV2,undefined);
 current=emptyHostState();
 await transport.fetchRowsV2({hostState:current});
 assert.deepEqual(sent,['rows-v2','metrics','rows-v2']);
});
