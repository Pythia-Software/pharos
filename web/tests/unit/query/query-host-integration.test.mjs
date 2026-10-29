import assert from 'node:assert/strict';
import test from 'node:test';
import { EMPTY_QUERY, encodeQuery, normalizeQueryState, createQueryLinkService, httpQuerySnapshotStore, QuerySnapshotHttpError } from '@pythia-software/query-table-core';
import * as storageModule from '../../../src/storage';
import * as bookmarksModule from '../../../src/bookmarks';
import * as queryLinksModule from '../../../src/query-links';
import { freshPageURL, tableRecoveryQuery } from '../../../src/query-recovery';
import { core, schemas, execution, capabilities as queryCapabilities } from '../helpers/recipes.mjs';
import { categoryForQuery } from '../../../src/query-categories';
import { instantiateRecipe, queryRecipes } from '../../../src/curiosity-data';
import { usageDatasets, usageWorkspaceURL } from '../../../src/usage-workspace-model';
import { createQueryMetadataLoader } from '../../../src/query-metadata';
import { matchingSelectionIDs } from '../../../src/query-selection';
const m={...storageModule,...bookmarksModule,...queryLinksModule,encodeQuery,normalizeQueryState,createQueryLinkService,httpQuerySnapshotStore,QuerySnapshotHttpError};
const prefs=new Map(),cache=new Map(),snapshots=new Map();
let fail=false,writes=0;
globalThis.location=new URL('http://localhost/tools');
globalThis.window={pharosPrefs:{get:(key,fallback)=>prefs.get(key)??fallback,set:(key,value)=>prefs.set(key,value),remove:key=>prefs.delete(key),keys:prefix=>[...prefs.keys()].filter(key=>key.startsWith(prefix))},dispatchEvent(){}};
globalThis.localStorage={getItem:key=>cache.get(key)??null,setItem:(key,value)=>cache.set(key,value),removeItem:key=>cache.delete(key)};
globalThis.fetch=async (input,options)=>{
 const url=new URL(input,location.origin),key=url.searchParams.get('dataset')+':'+url.searchParams.get('code');
 if(options?.method==='PUT'){
  writes++;
  if(fail)return new Response('query snapshot storage quota exceeded',{status:507,headers:{'Content-Type':'text/plain'}});
  const snapshot=JSON.parse(options.body);snapshots.set(key,snapshot);
  return Response.json(snapshot);
 }
 return snapshots.has(key)?Response.json(snapshots.get(key)):new Response('',{status:404});
};
const query=m.normalizeQueryState({select:[{field:'tool_name'}],where:[{field:'status',op:'=',value:'error'}],orderBy:[],limit:25,offset:0});

test('select all supports V2-only transports independently of the visible page', async () => {
 const schema=schemas.library,where=[{field:'source_kind',op:'=',value:'codex'}];
 const state={...EMPTY_QUERY,select:[{field:'title'}],where,limit:1,offset:2,aggregations:[{id:'n',op:'count',groupBy:[]}]};
 let sent;
 const ids=await matchingSelectionIDs(state,schema,execution,{fetchRowsV2:async request=>{
  sent=request;
  return {version:2,rows:[{id:'a'},{id:'b'}],total:2,computed:[],execution};
 }},200);
 assert.deepEqual(ids,['a','b']);
 assert.equal(sent.version,2);
 assert.deepEqual(sent.where,where);
 assert.ok(sent.select.includes(schema.idField));
 assert.equal(sent.limit,200);assert.equal(sent.offset,0);
 assert.equal(state.offset,2);assert.equal(state.select[0].field,'title');
});

test('select all rejects incomplete exports and row transport failures without replacing selection', async () => {
 const fetch=async result=>matchingSelectionIDs(EMPTY_QUERY,schemas.library,execution,{fetchRowsV2:async()=>({version:2,computed:[],execution,...result})},200);
 for(const result of [
  {total:201,rows:[{id:'a'}]}, {total:2,rows:[{id:'a'}]},
  {total:1,rows:[{}]}, {total:2,rows:[{id:'a'},{id:'a'}]},
 ]) await assert.rejects(fetch(result),/export|identit/);
 await assert.rejects(matchingSelectionIDs(EMPTY_QUERY,schemas.library,execution,{fetchRowsV2:async()=>{throw new Error('Offline');}},200),/Offline/);
});

test('select all uses negotiated V3 for derived canonical computed filters and validates the response', async () => {
 const name='@computed/active',schema={...schemas.library,fields:[...schemas.library.fields,{name,type:'number',source:{kind:'derived',computedId:'active'},filter:{enabled:false},sort:{enabled:false}}]};
 const handshake={...execution,planToken:'current',resolvedRevisions:{active:'2'},fields:{[name]:{type:'number',select:true,sort:true,filter:true,measure:true,group:true}}};
 const state={...EMPTY_QUERY,where:[{any:[{field:name,op:'>',value:'0'},{field:'source_kind',op:'=',value:'codex'}]}],orderBy:[{field:name,dir:'desc'}],limit:1,offset:1};
 let sent,v2Calls=0;
 const v2=async()=>{v2Calls++;throw new Error('filter_disabled: computed filtering requires host policy');};
 const fetch=async changes=>matchingSelectionIDs(state,schema,handshake,{
  queryCapabilities,fetchRowsV2:v2,
  fetchQueryV3:async request=>{
   sent=request;
   return {version:3,now:request.now,sourceTotal:3,rows:[{id:'a'},{id:'b'}],total:2,metrics:[],schema:{name:schema.name,idField:schema.idField,fields:[{name:schema.idField,type:'text'}]},execution:handshake,...changes};
  },
 },200);
 assert.deepEqual(await fetch(),['a','b']);
 assert.equal(sent.version,3);assert.equal(sent.rows,true);assert.deepEqual(sent.diagnostics,[]);
 assert.deepEqual(sent.result.where,state.where);assert.deepEqual(sent.result.orderBy,state.orderBy);
 assert.deepEqual(sent.result.select,[schema.idField]);assert.equal(sent.result.limit,200);assert.equal(sent.result.offset,0);
 assert.deepEqual({...sent.expectedRevisions},{active:'2'});assert.equal(sent.planToken,'current');
 assert.equal(state.offset,1);assert.equal(schema.fields.at(-1).source.kind,'derived');
 await assert.rejects(fetch({execution:{...handshake,resolvedRevisions:{active:'3'}}}),/definitions changed/i);
 await assert.rejects(fetch({execution:{...handshake,planToken:'obsolete'}}),/mismatched/);
 await assert.rejects(fetch({total:201}),/export/);
 await assert.rejects(fetch({total:3}),/export/);
 await assert.rejects(fetch({rows:[{id:'a'},{id:'a'}]}),/identit/);
 await assert.rejects(fetch({schema:{name:schema.name,idField:schema.idField,fields:[]}}),/schema/);
 assert.equal(v2Calls,0);
 await assert.rejects(matchingSelectionIDs(state,schema,handshake,{fetchRowsV2:v2},200),/residual/);
 assert.equal(v2Calls,0,'a V2-only server must not receive a query with dropped predicates');
});
test('collecting a dashboard card preserves its destination and durable editable query',async t=>{
 const before=[...m.readBookmarks()];
 t.after(()=>m.writeBookmarks(before));
 const recipe=queryRecipes.find(recipe=>recipe.id==='model-token-share-trend'),item=instantiateRecipe(recipe,90);
 const old=m.collectQuery(item.dataset,item.query,item.title,{window:item.window,templateId:item.instanceId,cover:item.cover});
 const saved=await m.collectDurableQuery(item.dataset,item.query,item.title,{url:item.url,window:item.window,templateId:item.instanceId,cover:item.cover});
 assert.notEqual(saved.id,old.id,'an older query bookmark must not replace a newly collected dashboard');
 const duplicate=await m.collectDurableQuery(item.dataset,item.query,item.title,{url:item.url,window:item.window,templateId:item.instanceId,cover:item.cover});
 assert.equal(duplicate.id,saved.id,'identical dashboard saves still deduplicate');
 const url=new URL(saved.url,location.origin),original=new URL(item.url,location.origin);
 assert.equal(url.searchParams.get('analyze_mode'),'dashboards');
 assert.equal(url.searchParams.get('analyze_chart'),original.searchParams.get('analyze_chart'));
 assert.equal(url.searchParams.get('analyze_filters'),original.searchParams.get('analyze_filters'));
 assert.match(url.searchParams.get('q_usage'),/^@[A-Za-z0-9_-]{22}$/);
 cache.clear();
 const loaded=await m.createQueryLinkService('pharos_usage',m.httpQuerySnapshotStore('/api/query-snapshots')).resolve(url.href,'q_usage');
 assert.deepEqual(loaded,normalizeQueryState(item.query));
 assert.deepEqual(m.queryFromURL(saved.url,saved.query).query,normalizeQueryState(item.query));
});
test('metadata shares stable results, queues scans and isolates subscriber cancellation', async()=>{
 let now=0,reads=0,finish;
 const load=createQueryMetadataLoader(()=>now,5),controller=new AbortController();
 const first=load('first',()=>{reads++;return new Promise(resolve=>{finish=resolve;});});
 const cancelled=load('first',()=>{throw Error('duplicate');},controller.signal);
 const second=load('second',async()=>{reads++;return {values:['second']};});
 await Promise.resolve();await Promise.resolve();
 assert.equal(reads,1);
 controller.abort();await assert.rejects(cancelled,{name:'AbortError'});
 const value={values:['first']};finish(value);
 assert.equal(await first,value);assert.deepEqual(await second,{values:['second']});
 assert.equal(await load('first',()=>{throw Error('cached');}),value);
 assert.equal(reads,2);
 now=6;assert.deepEqual(await load('first',async()=>({values:['updated']})),{values:['updated']});
 await assert.rejects(load('failed',async()=>{throw Error('retry');}),/retry/);
 assert.deepEqual(await load('failed',async()=>({values:[]})),{values:[]});
});
test('named saves create durable links, resolve on another Mac and keep defaults during updates',async()=>{
 const storage=m.libraryStorageAdapter();
 const saved=await storage.saveNamed('pharos_tool_calls','Errors',query,10);
 await storage.setDefaultSaved('pharos_tool_calls',saved.id);
 const bookmark=m.readBookmarks()[0];
 assert.match(new URL(bookmark.url,location.origin).searchParams.get('q_tool_calls'),/^@[A-Za-z0-9_-]{22}$/);
 cache.clear();
 assert.deepEqual((await storage.loadDefaultSaved('pharos_tool_calls')).query,query);
 const links=m.createQueryLinkService('pharos_tool_calls',m.httpQuerySnapshotStore('/api/query-snapshots'));
 assert.deepEqual(await links.resolve(new URL(bookmark.url,location.origin).href,'q_tool_calls'),query);
 const changed={...query,limit:50};
 const updated=await storage.updateNamed('pharos_tool_calls',saved.id,changed,20);
 assert.equal(updated.id,saved.id);
 assert.equal((await storage.loadDefaultSaved('pharos_tool_calls')).id,saved.id);
 assert.deepEqual((await storage.loadDefaultSaved('pharos_tool_calls')).query,changed);
 const oldURL=bookmark.url;
 cache.clear();
 assert.deepEqual(await links.resolve(new URL(oldURL,location.origin).href,'q_tool_calls'),query);
});
test('failed snapshot saves leave the named query and collection unchanged',async()=>{
 const storage=m.libraryStorageAdapter(),before=JSON.stringify(m.readBookmarks());
 fail=true;
 const quotaError=error=>{
  assert.ok(error instanceof m.QuerySnapshotHttpError);
  assert.equal(error.status,507);assert.equal(error.code,'quota');
  assert.equal(error.detail,'query snapshot storage quota exceeded');
  assert.match(error.message,/Existing links still work/);
  return true;
 };
 await assert.rejects(storage.updateNamed('pharos_tool_calls',m.readBookmarks()[0].savedQueryId,{...query,limit:75},30),quotaError);
 await assert.rejects(storage.saveNamed('pharos_tool_calls','Another',query,30),quotaError);
 fail=false;
 assert.equal(JSON.stringify(m.readBookmarks()),before);
});
test('editing overflow links is local and strict navigation rejects missing or duplicate state',async()=>{
 const storage=m.libraryStorageAdapter(),links=m.createQueryLinkService('pharos_tool_calls',storage.snapshots,{maxUrlBytes:100});
 const before=writes,local=links.local(query,location.href,'q_tool_calls');
 assert.equal(local.kind,'local');assert.equal(writes,before);
 assert.deepEqual(await links.resolve(local.url,'q_tool_calls'),query);
 await assert.rejects(links.resolve(`${location.href}?q_tool_calls=@aaaaaaaaaaaaaaaaaaaaaa`,'q_tool_calls'),/unavailable|missing|expired/i);
 assert.equal(m.queryFromURL('/tools?q_tool_calls=invalid'),undefined);
 assert.equal(m.queryFromURL(`/tools?q_tool_calls=${m.encodeQuery(query)}&q_tool_calls=${m.encodeQuery(query)}`),undefined);
});

test('rolling grouped views renew source bounds without rewriting HAVING or metrics',()=>{
 const window={field:'started_at',days:7},clock=new Date('2026-10-13T12:00:00Z');
 const grouped=m.normalizeQueryState({grouping:{source:{...query,where:[{field:'status',op:'=',value:'error'}]},keys:['tool_name'],summaries:[{id:'group_rows',op:'count'}]},select:[{field:'tool_name'},{field:'group_rows'}],where:[{field:'group_rows',op:'>=',value:'5'}],orderBy:[],limit:25,offset:0});
 const resolved=m.resolveRollingQuery(grouped,window,clock);
 assert.deepEqual(resolved.where,grouped.where);
 assert.deepEqual(resolved.grouping.source.where.slice(-2).map(term=>term.value),m.rollingBounds(7,clock));
 assert.ok(m.rollingQueryMatches(resolved,window));
 const updated=m.queryFromURL(m.updateQueryWindowURL(m.queryURL('tool_calls',resolved,window),1));
 assert.deepEqual(updated.query.where,grouped.where);
 assert.deepEqual(updated.window,{field:'started_at',days:1,mode:'relative'});
 assert.ok(m.rollingQueryMatches(updated.query,updated.window));
 assert.deepEqual(updated.query.grouping.source.where.slice(-2).map(term=>term.value),['-1d','+0s']);
});

test('native bookmarks created before dataset metadata can still be updated',async()=>{
 const items=m.readBookmarks();
 prefs.set(m.BOOKMARKS_KEY,[...items,{id:'old-native',title:'Legacy native bookmark',url:m.queryURL('tool_calls',query)}]);
 const updated=await m.libraryStorageAdapter().updateNamed('pharos_tool_calls','old-native',{...query,limit:75},30);
 assert.equal(updated.id,'old-native');assert.equal(updated.query.limit,75);
});

test('page parameters choose the dataset, and every card URL opens its own dataset',()=>{
 assert.equal(m.queryFromURL('/analyze?analyze_view=tokens&q_library=&q_usage=').dataset,'usage');
 assert.equal(m.queryFromURL('/tools?tools_view=skills&q_library=&q_skill_usages=').dataset,'skill_usages');
 for(const dataset of Object.keys(usageDatasets)) for(const mode of ['query','search']) {
  const url=usageWorkspaceURL({mode,dataset,query:EMPTY_QUERY});
  assert.equal(m.queryFromURL(url)?.dataset,dataset,`${mode} bookmarks keep their record type`);
 }
 for(const dataset of Object.keys(usageDatasets)) {
  const url=new URL(m.queryURL(dataset,query),location.origin);
  assert.equal(url.pathname,'/analyze');
  assert.equal(url.searchParams.get('analyze_mode'),'query');
  assert.equal(url.searchParams.get('analyze_data'),dataset);
  assert.ok(![...url.searchParams.keys()].some(key=>key.startsWith('usage_')));
  assert.deepEqual(m.queryFromURL(url.href),{dataset,query,window:undefined});
 }
 assert.equal(m.queryFromURL('/analyze?analyze_workspace=1&q_usage='),undefined,'A dashboard is not a token-table bookmark');
 const clock=new Date(2026,9,8,15);
 for(const recipe of queryRecipes)assert.equal(m.queryFromURL(instantiateRecipe(recipe,7,clock).url)?.dataset,recipe.dataset,recipe.id);
 const writing=new URL(instantiateRecipe(queryRecipes.find(item=>item.id==='human-words'),7,clock).url,location.origin);
 assert.equal(writing.pathname,'/analyze');
 assert.equal(writing.searchParams.get('analyze_data'),'writing_messages');
 assert.equal(writing.searchParams.get('card_query'),'writing_messages');
});

test('MCP query links use Analyze and older MCP bookmarks preserve query and window state',()=>{
 const window={field:'called_at',days:7,mode:'relative'};
 const url=new URL(m.queryURL('mcp_calls',query,window),location.origin);
 assert.equal(url.pathname,'/analyze');
 assert.equal(url.searchParams.get('analyze_topic'),'mcp');
 assert.equal(url.searchParams.get('analyze_data'),'mcp_calls');
 const parsed=m.queryFromURL(url.href);
 assert.equal(parsed.dataset,'mcp_calls');
 assert.deepEqual(parsed.window,window);
 const old=new URL(url);old.pathname='/mcp';
 for(const key of ['analyze_mode','analyze_topic','analyze_data'])old.searchParams.delete(key);
 old.searchParams.set('q_usage','stale');
 assert.deepEqual(m.queryFromURL(old.href),parsed);
});

test('rolling windows recognize whole local calendar spans across midnight and daylight saving',()=>{
 const previousTZ=process.env.TZ;
 process.env.TZ='America/Denver';
 try{
  const window={days:7,field:'started_at'};
  for(const date of [new Date(2026,9,8,23,59),new Date(2026,2,10),new Date(2026,10,3)]){
   const query=m.resolveRollingQuery(EMPTY_QUERY,window,date);
   assert.equal(m.rollingQueryMatches(query,window),true,'a saved window need not end on the current day');
   assert.equal(m.rollingQueryMatches({...query,offset:20,orderBy:[{field:'duration_ms',dir:'desc'}]},window),true);
   assert.equal(m.rollingQueryMatches(query,{...window,days:30}),false);
   assert.equal(m.rollingQueryMatches({...query,where:query.where.slice(1)},window),false);
   const partial={...query,where:query.where.map(term=>term.op==='>='?{...term,value:new Date(Date.parse(term.value)+3600000).toISOString()}:term)};
   assert.equal(m.rollingQueryMatches(partial,window),false,'manual partial-day bounds stop being rolling');
  }
 }finally{if(previousTZ===undefined)delete process.env.TZ;else process.env.TZ=previousTZ;}
});

test('changing a saved range preserves query structure, indexed search context, and unrelated URL controls',()=>{
 const query={...EMPTY_QUERY,offset:100,where:[
  {field:'activity_at',op:'>',value:'2025-01-01T00:00:00Z'},
  {field:'activity_at',op:'<=',value:'2025-12-31T00:00:00Z'},
  {any:[{field:'repository_name',op:'=',value:'pharos'},{field:'repository_name',op:'=',value:'query-table'}]},
 ],aggregations:[{id:'cost',op:'sum',field:'cost_usd',groupBy:['repository_name']}]};
 const original=`${m.queryURL('library',query)}&search=indexing&case=1&fuzzy=0&heat_grain=week#library`;
 const result=m.updateQueryWindowURL(original,30),spec=m.queryFromURL(result),url=new URL(result,location.origin);
 assert.deepEqual(spec.window,{days:30,field:'activity_at',mode:'relative'});
 assert.deepEqual(spec.query.where.filter(term=>term.field==='activity_at').map(term=>[term.op,term.value]),[['>=','-30d'],['<','+0s']]);
 assert.deepEqual(spec.query.where.find(term=>term.any),query.where[2]);
 assert.deepEqual(spec.query.aggregations,query.aggregations);
 assert.equal(spec.query.offset,0);
 for(const [key,value] of [['search','indexing'],['case','1'],['fuzzy','0'],['heat_grain','week']])assert.equal(url.searchParams.get(key),value);
 assert.equal(url.hash,'#library');
 assert.equal(categoryForQuery(spec.dataset,spec.query),'cost');
});


test('host query state survives last-used, named saves, updates, defaults and durable links in a fresh browser',async()=>{
 const key='pharos_tool_calls',storage=m.libraryStorageAdapter();
 assert.equal(storage.storesHostState,true,'Query Table refuses to save host state through adapters that drop it');
 assert.equal(m.clientTableStorageAdapter().storesHostState,true);
 const keyword={version:1,params:{record_tool_calls:['{"field":"command","draft":{"editor":"syntax","text":"(cache OR buffer) NOT obsolete"}}']}};
 const withKeyword={...query,hostState:keyword};
 await storage.saveLast(key,withKeyword);
 assert.deepEqual((await storage.loadLast(key)).hostState,keyword);
 const saved=await storage.saveNamed(key,'Keyword errors',withKeyword,40);
 await storage.setDefaultSaved(key,saved.id);
 const bookmark=m.readBookmarks().find(item=>item.savedQueryId===saved.id);
 const address=new URL(bookmark.url,location.origin);
 assert.match(address.searchParams.get('q_tool_calls'),/^@/);
 assert.deepEqual(address.searchParams.getAll('record_tool_calls'),keyword.params.record_tool_calls,'the address mirrors host state for previews');
 cache.clear(); // Another browser: no local snapshot cache.
 const links=m.createQueryLinkService(key,m.httpQuerySnapshotStore('/api/query-snapshots'));
 assert.deepEqual((await links.resolve(address.href,'q_tool_calls')).hostState,keyword);
 assert.deepEqual((await storage.loadDefaultSaved(key)).query.hostState,keyword);
 assert.deepEqual(m.savedQueries('tool_calls').find(item=>item.id===saved.id).query.hostState,keyword);
 // Updating the save replaces host state with the table query.
 const cleared={...query,limit:30,hostState:{version:1,params:{}}};
 await storage.updateNamed(key,saved.id,cleared,50);
 assert.deepEqual((await storage.loadDefaultSaved(key)).query,cleared);
 assert.equal(new URL(m.readBookmarks().find(item=>item.savedQueryId===saved.id).url,location.origin).searchParams.has('record_tool_calls'),false);
 cache.clear();
 assert.deepEqual((await links.resolve(address.href,'q_tool_calls')).hostState,keyword,'the earlier durable link still opens its own definition');
 await storage.deleteSaved(key,saved.id);
});

test('reset page bypasses the last-used query, and a failing query never needs a skip list',async()=>{
 // Query Table persists only succeeded queries (persistLast: "succeeded"),
 // so the adapter stores and restores what it is given.
 const key='pharos_tool_calls',storage=m.libraryStorageAdapter();
 await storage.saveLast(key,{...query,limit:27});
 assert.deepEqual(await storage.loadLast(key),{...query,limit:27});
 assert.equal(await m.libraryStorageAdapter({fresh:true}).loadLast(key),null);
});

test('fresh page links bypass cached queries and named defaults, preserving the saved library',async()=>{
 const storage=m.libraryStorageAdapter({fresh:true}),before=JSON.stringify(m.readBookmarks());
 assert.equal(await storage.loadLast('pharos_tool_calls'),null);
 assert.equal(await storage.loadDefaultSaved('pharos_tool_calls'),null);
 const url=new URL(freshPageURL('http://localhost/analyze?q_usage=invalid&analyze_topic=human&activity_repo=gone#chart'),location.origin);
 assert.equal(url.pathname,'/analyze');assert.equal(url.searchParams.get('fresh'),'1');
 assert.equal(url.searchParams.get('analyze_topic'),'tokens');assert.equal(url.searchParams.get('analyze_mode'),'dashboards');
 assert.equal(url.searchParams.has('activity_repo'),false);assert.equal(url.searchParams.has('q_usage'),false);assert.equal(url.hash,'');
 assert.equal(JSON.stringify(m.readBookmarks()),before);
 assert.equal(freshPageURL('http://localhost/tools?tools_view=skills&q_skill_usages=invalid'),'/tools?fresh=1');
 // Table reset starts from source-schema defaults even when the restored
 // baseline contains removed columns, metrics, grouping or invalid filters.
 for(const dataset of ['usage','tool_calls','mcp_calls']){
  const scope=dataset==='mcp_calls'?[]:[{field:'provider',op:'=',value:'codex'}];
  const schema=schemas[dataset],reset=tableRecoveryQuery(schema,scope);
  const request=core.toServerQueryV2(reset,schema,execution);
  assert.deepEqual(request.diagnostics,[]);
  assert.deepEqual(reset.where,scope);
  assert.equal(reset.offset,0);
  assert.equal(reset.limit,schema.defaultLimit);
  assert.equal(reset.grouping,undefined);
  assert.ok(!reset.aggregations?.length);
  assert.deepEqual(core.selectedFields(schema,reset).map(field=>field.name),schema.defaultSelect.map(column=>column.field));
  assert.deepEqual(request.orderBy,schema.defaultSort);
 }
});


test('computed source links use native conversation redirects and internal query links stay in the app', async t => {
  const { navigateLink } = await import('../../../src/query-link-targets');
  const previous = globalThis.window, opened = [];
  globalThis.window = { location: new URL('http://localhost/analyze'), pharosNavigate: href => opened.push(href) };
  t.after(() => { globalThis.window = previous; });
  assert.equal(navigateLink('/conversation/conv%20one?message=message%20one'), false);
  assert.equal(navigateLink('https://example.test/explore'), false);
  assert.deepEqual(opened, []);
  assert.equal(navigateLink('/analyze?analyze_mode=query&analyze_data=writing_messages'), true);
  assert.equal(opened.length, 1);
});

test('computed query links enable automatic fitting for every target without changing the source layout', async () => {
  const { linkTargets } = await import('../../../src/query-link-targets');
  const source = { ...EMPTY_QUERY, fitToScreen: false, select: [{ field: 'id', width: 900 }], where: [{ field: 'id', op: '=', value: 'one' }] };
  for (const [dataset, target] of Object.entries(linkTargets)) {
    const url = new URL(target(source, {}), 'http://localhost');
    const restored = core.decodeQueryStrict(url.searchParams.get(`q_${dataset}`));
    assert.equal(restored.fitToScreen, true, dataset);
    assert.deepEqual(restored.where, source.where, dataset);
    assert.deepEqual(restored.select, source.select, dataset);
  }
  assert.equal(source.fitToScreen, false);
});

test('client table named queries and defaults survive remounting independently for each schema', async () => {
 const history='sync_history',sources='sync_sources',storage=m.clientTableStorageAdapter();
 const saved=await storage.saveNamed(history,' Slow runs ',query,10);
 const sourceSaved=await storage.saveNamed(sources,'Slow runs',{...query,limit:5},20);
 await storage.setDefaultSaved(history,saved.id);
 await storage.saveLast(history,{...query,offset:25});
 // Preferences are loaded as JSON from the library on the next page load.
 for(const [key,value] of prefs)if(key.includes('sync_'))prefs.set(key,JSON.parse(JSON.stringify(value)));
 const reloaded=m.clientTableStorageAdapter();
 assert.deepEqual(await reloaded.listSaved(history),[saved]);
 assert.deepEqual(await reloaded.listSaved(sources),[sourceSaved]);
 assert.deepEqual(await reloaded.loadDefaultSaved(history),saved);
 assert.equal(await reloaded.loadDefaultSaved(sources),null);
 assert.equal((await reloaded.loadLast(history)).offset,25);
 const updated=await reloaded.updateNamed(history,saved.id,{...query,limit:75},30);
 assert.equal(updated.id,saved.id);assert.equal(updated.name,'Slow runs');
 assert.equal((await m.clientTableStorageAdapter().loadDefaultSaved(history)).query.limit,75);
 await reloaded.deleteSaved(history,saved.id);
 assert.deepEqual(await m.clientTableStorageAdapter().listSaved(history),[]);
 assert.equal(await m.clientTableStorageAdapter().loadDefaultSaved(history),null);
 assert.deepEqual(await reloaded.listSaved(sources),[sourceSaved]);
 await reloaded.deleteSaved(sources,sourceSaved.id);
});

test('client table rejected saves preserve the existing named query', async () => {
 const key='sync_phases',storage=m.clientTableStorageAdapter();
 const saved=await storage.saveNamed(key,'By phase',query,1);
 await assert.rejects(storage.saveNamed(key,' By phase ',{...query,limit:5},2),/already exists/);
 await assert.rejects(storage.saveNamed(key,' ',query,2),/between 1 and/);
 await assert.rejects(storage.updateNamed(key,'missing',query,2),/does not exist/);
 await assert.rejects(storage.setDefaultSaved(key,'missing'),/does not exist/);
 assert.deepEqual(await m.clientTableStorageAdapter().listSaved(key),[saved]);
 await storage.deleteSaved(key,saved.id);
});
