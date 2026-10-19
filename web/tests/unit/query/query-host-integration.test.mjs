import assert from 'node:assert/strict';
import test from 'node:test';
import { EMPTY_QUERY, encodeQuery, normalizeQueryState, createQueryLinkService, httpQuerySnapshotStore, QuerySnapshotHttpError } from '@pythia-software/query-table-core';
import * as storageModule from '../../../src/storage';
import * as bookmarksModule from '../../../src/bookmarks';
import * as queryLinksModule from '../../../src/query-links';
import { categoryForQuery } from '../../../src/query-categories';
import { instantiateRecipe, queryRecipes } from '../../../src/curiosity-data';
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
 assert.equal(m.queryFromURL('/usage?q_library=&q_usage=').dataset,'usage');
 assert.equal(m.queryFromURL('/tools?tools_view=skills&q_library=&q_skill_usages=').dataset,'skill_usages');
 const clock=new Date(2026,9,8,15);
 for(const recipe of queryRecipes)assert.equal(m.queryFromURL(instantiateRecipe(recipe,7,clock).url)?.dataset,recipe.dataset,recipe.id);
 const writing=new URL(instantiateRecipe(queryRecipes.find(item=>item.id==='human-words'),7,clock).url,location.origin);
 assert.equal(writing.searchParams.get('usage'),'writing');
 assert.equal(writing.searchParams.get('writing'),'messages');
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
