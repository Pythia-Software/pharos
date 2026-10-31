import {test} from 'node:test';
import assert from 'node:assert/strict';
import {open,skip,activateHeaderTool} from './harness.mjs';
import {workID,rootConversation,defaultApi,cloudScheduledStatus,busyStatus,eventStream} from './fixtures.mjs';
import {EMPTY_QUERY,encodeQuery,decodeQueryStrict,loadSchema} from '@pythia-software/query-table-core';
import toolCallsDocument from '../../../schemas/tool_calls.schema.json' with {type:'json'};
import mcpCallsDocument from '../../../schemas/mcp_calls.schema.json' with {type:'json'};
import {answerQuery} from '../support/query-server.mjs';
const path=page=>new URL(page.url()).pathname+new URL(page.url()).search;

function jsonInspectorFixture() {
 const schema=loadSchema(mcpCallsDocument),now=Date.now(),definitions=new Map();
 const payload={'key.with.dots':{'quote"key':'needle'},events:Array.from({length:45},(_,i)=>({text:`needle ${i}`})),...Object.fromEntries(Array.from({length:240},(_,i)=>['tail'+i,i]))};
 const rows=[{id:'inspect-1',tool_name:'query',arguments_json:JSON.stringify(payload),called_at:new Date(now).toISOString(),status:'success'},
  {id:'inspect-2',tool_name:'query',arguments_json:JSON.stringify({other:'needle'}),called_at:new Date(now).toISOString(),status:'success'}];
 const api={
  '* /api/query/mcp_calls/:operation':({params,body,query})=>answerQuery(params.operation,body,rows,schema,{now,id:query.get('id')}),
  'PUT /api/query-snapshots':({body})=>{definitions.set(body.code,body);return body},
  'GET /api/query-snapshots':({query})=>definitions.get(query.get('code')),
 };
 const query={...EMPTY_QUERY,where:[{field:'tool_name',op:'=',value:'query'}],select:[{field:'id'},{field:'tool_name'},{field:'__qt_item_details__'}],fitToScreen:false,limit:5,itemDetails:{modes:['expand'],active:{id:'inspect-1',mode:'expand'}}};
 const route=(state=query,reveal)=>'/analyze?'+new URLSearchParams({analyze_mode:'query',analyze_data:'mcp_calls',q_mcp_calls:encodeQuery(state),...(reveal?{reveal}:{})});
 const read=page=>{const token=new URL(page.url()).searchParams.get('q_mcp_calls');return decodeQueryStrict(token.startsWith('@')?definitions.get(token.slice(1)).token:token)};
 return {api,query,route,read};
}

test('native JSON property choices restore in independent browsers and through Back/Forward',{skip},async t=>{
 const fixture=jsonInspectorFixture(),app=await open(t,{url:fixture.route(),api:fixture.api,ready:'.qt-property-json-open'}),{page}=app;
 const copies=[];
 const change=async action=>{const before=new URL(page.url()).searchParams.get('q_mcp_calls');await action();await page.waitForFunction(before=>new URLSearchParams(location.search).get('q_mcp_calls')!==before,before);await app.settled();};
 const capture=()=>copies.push({url:path(page),inspection:fixture.read(page).itemDetails.jsonInspections[0].state});
 await change(()=>page.getByRole('button',{name:'Open Arguments (sanitized) in JSON Viewer',exact:true}).click());
 const dialog=page.getByRole('dialog',{name:'Arguments (sanitized) · JSON',exact:true});await dialog.waitFor();
 await change(()=>dialog.getByRole('button',{name:'Expand $',exact:true}).click());
 await change(()=>dialog.getByRole('button',{name:'Expand $["events"]',exact:true}).click());
 await change(()=>dialog.getByRole('button',{name:'Expand $["events"] items 20–39',exact:true}).click());
 await change(()=>dialog.getByRole('button',{name:'Show more',exact:true}).click());capture();
 await change(()=>dialog.getByRole('searchbox',{name:'Search Arguments (sanitized) JSON',exact:true}).fill('needle'));
 await change(()=>dialog.getByRole('combobox',{name:'JSON search mode',exact:true}).selectOption('find'));
 await change(()=>dialog.getByRole('button',{name:'Next JSON match',exact:true}).click());capture();
 await change(()=>dialog.getByRole('button',{name:'Collapse $["events"]',exact:true}).click());capture();
 await change(()=>dialog.getByRole('button',{name:'Show original',exact:true}).click());capture();
 await change(()=>dialog.getByRole('button',{name:'Show JSON',exact:true}).click());
 await change(()=>dialog.getByRole('searchbox',{name:'Search Arguments (sanitized) JSON',exact:true}).fill(''));capture();
 await change(()=>dialog.getByRole('button',{name:'Close Arguments (sanitized) · JSON',exact:true}).click());capture();
 for(const copy of copies){
  const fresh=await open(t,{url:copy.url,api:fixture.api,ready:'.qt-property-json-open'}),restored=fresh.page.getByRole('dialog',{name:'Arguments (sanitized) · JSON',exact:true});
  assert.equal(await restored.isVisible(),copy.inspection.open);
  if(copy.inspection.open){
   assert.equal(await restored.getByRole('searchbox',{name:'Search Arguments (sanitized) JSON',exact:true}).inputValue(),copy.inspection.search);
   assert.equal(await restored.getByRole('combobox',{name:'JSON search mode',exact:true}).inputValue(),copy.inspection.searchMode);
   assert.equal(await restored.getByRole('button',{name:copy.inspection.raw?'Show JSON':'Show original',exact:true}).getAttribute('aria-pressed'),String(copy.inspection.raw));
  }
  assert.deepEqual(fixture.read(fresh.page).itemDetails.jsonInspections[0].state,copy.inspection);
 }
 await page.evaluate(url=>window.pharosNavigate(url),copies[1].url);await dialog.waitFor();
 await page.goBack();await dialog.waitFor({state:'hidden'});
 await page.goForward();await dialog.waitFor();
 assert.equal(await dialog.getByRole('searchbox',{name:'Search Arguments (sanitized) JSON',exact:true}).inputValue(),'needle');
});

test('semantic JSON-path reveal materializes exact details, preserves focus, and never replays on refresh',{skip},async t=>{
 const fixture=jsonInspectorFixture(),target='detail:'+JSON.stringify({rowId:'inspect-1',field:'arguments_json',path:'$["events"][41]["text"]'});
 const app=await open(t,{url:fixture.route({...fixture.query,itemDetails:{modes:['expand']}},target),api:fixture.api,ready:'.qt-attention-target'}),{page}=app;
 await page.waitForFunction(()=>document.documentElement.dataset.pharosAttentionMissing==='');
 const marker=page.locator('.qt-attention-target').filter({hasText:'needle 41'});await marker.first().waitFor();
 assert.equal(await page.evaluate(()=>document.activeElement===document.body),true);
 let scrolls=await page.evaluate(()=>{window.__attentionScrolls=0;const original=Element.prototype.scrollIntoView;Element.prototype.scrollIntoView=function(...args){window.__attentionScrolls++;return original.apply(this,args)};return window.__attentionScrolls});
 const refreshed=page.waitForResponse(response=>new URL(response.url()).pathname==='/api/query/mcp_calls/rows-v2');
 await page.evaluate(()=>window.pharosQueryTables.refresh('mcp_calls'));await refreshed;await app.settled();
 assert.equal(await page.evaluate(()=>window.__attentionScrolls),scrolls);
 const state=fixture.read(page);assert.equal(state.offset,fixture.query.offset);assert.deepEqual(state.where,fixture.query.where);
 await page.evaluate(()=>window.pharosNavigate('/cards'));
 await page.locator('.card-library-page').waitFor();assert.equal(await page.locator('.qt-attention-target:visible').count(),0);
});

test('semantic reveal waits for the initial result page instead of landing in a temporary fallback',{skip},async t=>{
 const fixture=jsonInspectorFixture();let release,requested,itemCalls=0;
 const started=new Promise(resolve=>requested=resolve),rowsReady=new Promise(resolve=>release=resolve);
 const respond=fixture.api['* /api/query/mcp_calls/:operation'];
 const api={
  'POST /api/query/mcp_calls/rows-v2':async request=>{requested();await rowsReady;return respond({...request,params:{operation:'rows-v2'}})},
  'GET /api/query/mcp_calls/item':request=>{itemCalls++;return respond({...request,params:{operation:'item'}})},
  ...fixture.api,
 };
 const app=await open(t,{url:'/cards',api,ready:'.card-library-page'}),target='detail:'+JSON.stringify({rowId:'inspect-1',field:'arguments_json',path:'$["events"][41]["text"]'});
 await app.page.evaluate(url=>window.pharosNavigate(url),fixture.route({...fixture.query,itemDetails:{modes:['expand']}},target));await started;
 await app.page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
 assert.equal(itemCalls,0,'no native landing before its result surface is ready');
 release();await app.page.locator('.qt-attention-target').filter({hasText:'needle 41'}).first().waitFor();await app.settled();
 assert.equal(itemCalls>0,true);
});

test('missing JSON paths report unavailable without changing the query population',{skip},async t=>{
 const fixture=jsonInspectorFixture(),target='detail:'+JSON.stringify({rowId:'inspect-1',field:'arguments_json',path:'$["deleted"]'});
 const app=await open(t,{url:fixture.route(fixture.query,target),api:fixture.api,ready:'.qt-property-json-open'});await app.settled();
 await app.page.waitForFunction(target=>document.documentElement.dataset.pharosAttentionMissing===target&&document.documentElement.dataset.pharosAttentionPending==='',target);
 assert.equal(await app.page.locator('.qt-attention-target').count(),0);
 assert.deepEqual(fixture.read(app.page).where,fixture.query.where);
});

test('native modal and restored nested JSON reveal preserve keyboard focus',{skip},async t=>{
 const fixture=jsonInspectorFixture(),target='detail:'+JSON.stringify({rowId:'inspect-1',field:'arguments_json',path:'$["events"][21]["text"]'});
 const state={version:1,open:true,search:'needle',searchMode:'find',raw:false,collapsedPaths:[],expandedBuckets:[],visibleEntryLimit:200,searchOverrides:[]};
 const query={...fixture.query,itemDetails:{modes:['modal'],jsonInspections:[{dataset:'pharos_mcp_calls',rowId:'inspect-1',field:'arguments_json',state}]}};
 const app=await open(t,{url:fixture.route(query,target),api:fixture.api,ready:'.qt-json-inspector .qt-attention-target'});
 await app.page.waitForFunction(()=>document.documentElement.dataset.pharosAttentionMissing==='');
 assert.equal(await app.page.evaluate(()=>document.activeElement===document.body),true);
 assert.equal(fixture.read(app.page).itemDetails.active.mode,'modal');
});

test('native detail reveal loads a record outside the result page without broadening it',{skip},async t=>{
 const fixture=jsonInspectorFixture(),target='detail:'+JSON.stringify({rowId:'inspect-2',field:'arguments_json',path:'$["other"]'});
 const query={...fixture.query,limit:1,itemDetails:{modes:['expand','modal']}};
 const app=await open(t,{url:fixture.route(query,target),api:fixture.api,ready:'.qt-attention-target'});
 await app.page.waitForFunction(()=>document.documentElement.dataset.pharosAttentionMissing==='');
 const restored=fixture.read(app.page);
 assert.equal(restored.itemDetails.active.id,'inspect-2');assert.equal(restored.limit,1);assert.equal(restored.offset,0);assert.deepEqual(restored.where,query.where);
 assert.equal(await app.page.evaluate(()=>document.activeElement===document.body),true);
});

test('JSON inspection scope follows record and property identities across layouts',{skip},async t=>{
 const fixture=jsonInspectorFixture(),state={version:1,open:true,search:'needle',searchMode:'find',raw:false,collapsedPaths:[],expandedBuckets:[],visibleEntryLimit:400,searchOverrides:[]};
 const query={...fixture.query,itemDetails:{...fixture.query.itemDetails,jsonInspections:[{dataset:'pharos_mcp_calls',rowId:'inspect-1',field:'arguments_json',state}]}};
 const other=await open(t,{url:fixture.route({...query,itemDetails:{...query.itemDetails,active:{id:'inspect-2',mode:'expand'}}}),api:fixture.api,ready:'.qt-property-json-open'});
 assert.equal(await other.page.getByRole('dialog',{name:'Arguments (sanitized) · JSON',exact:true}).isVisible(),false);
 await other.page.getByRole('button',{name:'Open Arguments (sanitized) in JSON Viewer',exact:true}).click();
 assert.equal(await other.page.getByRole('searchbox',{name:'Search Arguments (sanitized) JSON',exact:true}).inputValue(),'');
 const reordered={...query,itemDetails:{...query.itemDetails,views:{expand:{version:1,properties:[{id:'renamed-instance',field:'tool_name'},{id:'new-property-id',field:'arguments_json',label:'Renamed payload'}]}}}};
 const fresh=await open(t,{url:fixture.route(reordered),api:fixture.api,ready:'.qt-json-inspector'});
 assert.equal(await fresh.page.getByRole('searchbox',{name:'Search Renamed payload JSON',exact:true}).inputValue(),'needle');
 assert.equal(await fresh.page.getByRole('combobox',{name:'JSON search mode',exact:true}).inputValue(),'find');
});

test('JSON inspection state survives automatic durable snapshots without a browser cache',{skip},async t=>{
 const fixture=jsonInspectorFixture(),state={version:1,open:true,search:'needle',searchMode:'find',raw:false,collapsedPaths:[],expandedBuckets:['$["events"]:bucket:20'],visibleEntryLimit:400,searchOverrides:[]};
 const terms=Array.from({length:70},(_,i)=>({field:'tool_name',op:'!=',value:`unused-${i}-${Array.from({length:48},(_,n)=>String.fromCharCode(33+(i*31+n*17+i*n)%90)).join('')}`}));
 const query={...fixture.query,where:[...fixture.query.where,...terms],itemDetails:{...fixture.query.itemDetails,jsonInspections:[{dataset:'pharos_mcp_calls',rowId:'inspect-1',field:'arguments_json',state}]}};
 const first=await open(t,{url:fixture.route(query),api:fixture.api,ready:'.qt-json-inspector'});
 await first.page.waitForFunction(()=>new URLSearchParams(location.search).get('q_mcp_calls')?.startsWith('@'));
 const fresh=await open(t,{url:path(first.page),api:fixture.api,ready:'.qt-json-inspector'});
 assert.equal(await fresh.page.getByRole('searchbox',{name:'Search Arguments (sanitized) JSON',exact:true}).inputValue(),'needle');
 assert.deepEqual(fixture.read(fresh.page).itemDetails.jsonInspections[0].state,state);
});

test('a pending native detail reveal is cancelled when navigation leaves its page',{skip},async t=>{
 const fixture=jsonInspectorFixture();let release,requested;
 const started=new Promise(resolve=>requested=resolve),pending=new Promise(resolve=>release=resolve);
 const api={'GET /api/query/mcp_calls/item':async({query})=>{if(query.get('id')==='inspect-2'){requested();await pending;}return {row:{id:'inspect-2',tool_name:'query',arguments_json:'{"other":"needle"}'}}},...fixture.api};
 const app=await open(t,{url:fixture.route({...fixture.query,itemDetails:{modes:['expand']}}),api,ready:'.usage-workspace-table .qt-row'});
 const target='detail:'+JSON.stringify({rowId:'inspect-2',field:'arguments_json',path:'$["other"]'});
 await app.page.evaluate(url=>window.pharosNavigate(url),fixture.route({...fixture.query,itemDetails:{modes:['expand']}},target));
 await started;
 await app.page.evaluate(()=>window.pharosNavigate('/cards'));
 await app.page.locator('.card-library-page').waitFor();release();await app.settled();
 assert.equal(new URL(app.page.url()).pathname,'/cards');
 assert.equal(await app.page.locator('.qt-attention-target:visible').count(),0);
});

test('unavailable detail links retry when custom results switch to a native table',{skip},async t=>{
 const target='detail:'+JSON.stringify({rowId:'row-0',field:'title'});
 const app=await open(t,{url:'/library?'+new URLSearchParams({view:'conversation',reveal:target}),ready:'.conversation-result-card'});
 await app.page.waitForFunction(target=>document.documentElement.dataset.pharosAttentionMissing===target&&document.documentElement.dataset.pharosAttentionPending==='',target);
 assert.equal(new URL(app.page.url()).searchParams.get('view'),'conversation');
 assert.equal(await app.page.locator('.qt-attention-target').count(),0);
 await app.page.getByRole('group',{name:'Display results as',exact:true}).getByRole('button',{name:'Table',exact:true}).click();
 await app.page.locator('[data-property-field="title"].qt-attention-target').first().waitFor();
 await app.page.waitForFunction(()=>document.documentElement.dataset.pharosAttentionMissing==='');
 assert.equal(new URL(app.page.url()).searchParams.get('reveal'),target,'the same linked target becomes available without navigation');
});

test('conversation result links restore their session and inline agent without taking focus',{skip},async t=>{
 const message=(id,role,text)=>({id,native_id:id,role,kind:'message',text});
 const api={
  'POST /api/query/library/:operation':request=>{
   const result=defaultApi['* /api/query/:dataset/:operation']({...request,params:{dataset:'library',operation:request.params.operation}});
   return !result?.rows?result:{...result,total:1,rows:result.rows.slice(0,1).map(row=>({...row,id:workID}))};
  },
  'GET /api/work/:id':request=>({...defaultApi['GET /api/work/:id'](request),conversations:[{id:'first',provider:'codex'},{id:'second',provider:'codex'}]}),
  'GET /api/work/:id/conversations/:conversation':({params})=>({id:params.conversation,workspace_id:workID,provider:'codex',messages:params.conversation==='first'?[message('first-prompt','user','First session prompt')]:[
   message('second-prompt','user','Second session prompt'),
   message('shared-envelope','assistant',JSON.stringify({content:[
    {type:'tool_use',id:'configuration',name:'Task',input:{subagent_type:'Configuration auditor',prompt:'Check configuration'}},
    {type:'tool_use',id:'timing',name:'Agent',input:{subagent_type:'Timing auditor',prompt:'Check timing'}},
   ]})),
   {...message('configuration-result','tool',JSON.stringify({content:'Configuration checked.'})),kind:'delegation_result',call_id:'configuration'},
   {...message('timing-result','tool',JSON.stringify({content:'Timing checked.'})),kind:'delegation_result',call_id:'timing'},
  ]}),
 };
 const state={[`${workID}/browse`]:true,[`${workID}/turn`]:'second-prompt'};
 const disclosures={[`search:${workID}:conversation:first`]:true,[`search:${workID}:conversation:second`]:true};
 const app=await open(t,{url:'/library?'+new URLSearchParams({view:'conversation',search_cards:JSON.stringify(state),disclosures:JSON.stringify(disclosures)}),api,ready:'.conversation-result-card'});
 const preview=app.page.getByRole('region',{name:'Selected turn preview',exact:true});
 await preview.getByText('Second session prompt',{exact:true}).waitFor();
 await app.page.getByRole('navigation',{name:'Conversation outline',exact:true}).getByRole('button',{name:/First session prompt/}).waitFor();
 assert.equal(await preview.evaluate(element=>element.contains(document.activeElement)),false);
 await app.page.getByRole('navigation',{name:'Conversation outline',exact:true}).getByRole('button',{name:/Sub-agent · Timing auditor/}).click();
 await preview.getByText('Timing checked.',{exact:true}).waitFor();
 const fresh=await open(t,{url:path(app.page),api,ready:'.conversation-result-card'});
 const restored=fresh.page.getByRole('region',{name:'Selected turn preview',exact:true});
 await restored.getByText('Check timing',{exact:true}).waitFor();
 await restored.getByText('Timing checked.',{exact:true}).waitFor();
 await fresh.page.getByRole('navigation',{name:'Conversation outline',exact:true}).getByRole('button',{name:/First session prompt/}).waitFor();
 assert.equal(await restored.evaluate(element=>element.contains(document.activeElement)),false);
});

test('JSON display cells restore their inspector choices in a fresh browser',{skip,todo:'Query Table 0.9.8 DataTable does not wire cell display JsonCell to scoped state; docs/query-table-url-state-handoff.md'},async t=>{
 const fixture=jsonInspectorFixture(),query={...fixture.query,select:[{field:'id'},{field:'arguments_json',display:{kind:'json'}}],itemDetails:{modes:['expand']}};
 const first=await open(t,{url:fixture.route(query),api:fixture.api,ready:'.usage-workspace-table .qt-row'});
 await first.page.getByRole('button',{name:'Inspect Arguments (sanitized)',exact:true}).first().click();
 await first.page.getByRole('searchbox',{name:'Search Arguments (sanitized) JSON',exact:true}).fill('needle');
 const fresh=await open(t,{url:path(first.page),api:fixture.api,ready:'.usage-workspace-table .qt-row'});
 assert.equal(await fresh.page.getByRole('dialog',{name:'Arguments (sanitized) · JSON',exact:true}).isVisible(),true);
});

test('copied Cards view overrides preferences in a fresh context and follows Back/Forward', {skip}, async t=>{
 const first=await open(t,{url:'/cards?card_topic=all',ready:'.card-library-page'});
 await first.page.getByRole('button',{name:/^Filters/}).click();
 const preview=first.page.getByRole('combobox',{name:'Preview window',exact:true});
 await preview.selectOption('30');
 await first.page.waitForFunction(()=>new URLSearchParams(location.search).get('card_window')==='30');
 const copied=path(first.page);
 const fresh=await open(t,{url:copied,ready:'.card-library-page',preferences:{'pharos-home-card-library-v1':{filters:false,topic:'other'}}});
 assert.equal(await fresh.page.getByRole('combobox',{name:'Preview window',exact:true}).inputValue(),'30');
 await fresh.page.evaluate(()=>window.pharosNavigate('/cards?card_topic=all&card_filters=1&card_window=7'));
 await fresh.page.waitForFunction(()=>document.querySelector('select[aria-label="Preview window"]')?.value==='7');
 await fresh.page.goBack();
 await fresh.page.waitForFunction(()=>document.querySelector('select[aria-label="Preview window"]')?.value==='30');
});

test('an explicit query sizing choice survives native restoration', {skip},async t=>{
 const query={...EMPTY_QUERY,fitToScreen:false,select:[{field:'model',width:240}],limit:5};
 const opened=await open(t,{url:'/analyze?'+new URLSearchParams({analyze_mode:'query',analyze_data:'usage',q_usage:encodeQuery(query)}),ready:'.usage-workspace-table .qt-table'});
 await opened.settled();
 assert.equal(Boolean(decodeQueryStrict(new URL(opened.page.url()).searchParams.get('q_usage')).fitToScreen),false);
 const defaults=await open(t,{url:'/analyze?analyze_mode=query&analyze_data=usage',ready:'.usage-workspace-table .qt-table'});
 await defaults.settled();
 const effective=decodeQueryStrict(new URL(defaults.page.url()).searchParams.get('q_usage'));
 assert.equal(effective.limit,50,'a new fitted table retains the dataset page size');
 assert.equal(effective.fitToScreen,true);
});

test('reader category and disclosure state survive a fresh browser', {skip},async t=>{
 const first=await open(t,{url:`/work/${workID}?conversation=${rootConversation}`,ready:'.transcript-filter'});
 await first.page.getByRole('group',{name:'Show in transcript',exact:true}).getByRole('button',{name:'Primary only',exact:true}).click();
 await first.page.waitForFunction(()=>new URLSearchParams(location.search).get('reader_filter_level')==='primary');
 const turn=first.page.locator('.turn').first();
 await turn.locator(':scope > summary').click();
 await first.page.waitForFunction(()=>new URLSearchParams(location.search).has('reader_collapsed'));
 const wasOpen=await turn.evaluate(element=>element.open);
 const summary=turn.getByRole('button',{name:'Expand turn ask',exact:true});
 await summary.click();
 await first.page.waitForFunction(()=>Object.keys(JSON.parse(new URLSearchParams(location.search).get('reader_content')||'{}')).some(key=>key.endsWith(':turn-summary-prompt')));
 const fresh=await open(t,{url:path(first.page),ready:'.transcript-filter'});
 assert.equal(await fresh.page.getByRole('group',{name:'Show in transcript',exact:true}).getByRole('button',{name:'Primary only',exact:true}).getAttribute('aria-pressed'),'true');
 assert.equal(await fresh.page.locator('.turn').first().evaluate(element=>element.open),wasOpen);
 assert.equal(await fresh.page.locator('.turn').first().getByRole('button',{name:'Collapse turn ask',exact:true}).getAttribute('aria-expanded'),'true');
});

test('dynamic message reveal materializes its target without taking keyboard focus', {skip},async t=>{
 const messages=Array.from({length:45},(_,turn)=>['user','assistant'].map(role=>({id:`lazy-${turn}-${role}`,native_id:`lazy-${turn}-${role}`,role,kind:'message',text:`Turn ${turn} ${role}`,created_at:new Date(1760000000000+turn*1000).toISOString()}))).flat();
 const api={'GET /api/work/:id/conversations/:conversation':request=>({...defaultApi['GET /api/work/:id/conversations/:conversation'](request),messages})};
 const opened=await open(t,{api,url:`/work/${workID}?conversation=${rootConversation}&reveal=message:lazy-44-assistant&highlight=message:lazy-0-user`,preferences:{"pharos-workspace-sidebar-width":465.7210937499999},ready:'.conversation-panel'});
 await opened.page.waitForFunction(()=>document.querySelector('[data-message-id="lazy-44-assistant"]')?.classList.contains('pharos-reveal'));
 assert.equal(await opened.page.locator('[data-message-id="lazy-44-assistant"]').first().evaluate(element=>element===document.activeElement),false);
 const width=Number(new URL(opened.page.url()).searchParams.get('sidebar_width'));
 assert.ok(Number.isInteger(width)&&width>=300);
 await opened.page.waitForFunction(()=>!!document.querySelector('[data-message-id="lazy-0-user"].pharos-highlight'));
 await opened.page.evaluate(()=>window.dispatchEvent(new Event('pharos:usage-refresh')));
 await opened.settled();
 assert.equal(await opened.page.locator('[data-message-id="lazy-44-assistant"]').first().evaluate(element=>element===document.activeElement),false);
});

test('automatic oversized query references resolve in a fresh browser with no local cache', {skip},async t=>{
 const definitions=new Map();
 const api={
  'PUT /api/query-snapshots':({body})=>{definitions.set(body.code,body);return body;},
  'GET /api/query-snapshots':({query})=>definitions.get(query.get('code')),
 };
 // Distinct nonrepeating values avoid a highly compressible artificial query.
 const terms=Array.from({length:70},(_,i)=>({field:'model',op:'!=',value:`model-${i}-${Array.from({length:48},(_,n)=>String.fromCharCode(33+(i*31+n*17+i*n)%90)).join('')}`}));
 const query={...EMPTY_QUERY,where:terms,select:[{field:'model'}]};
 const first=await open(t,{url:'/analyze?'+new URLSearchParams({analyze_mode:'query',analyze_data:'usage',q_usage:encodeQuery(query)}),api,ready:'.usage-workspace-table .qt-table'});
 await first.page.waitForFunction(()=>new URLSearchParams(location.search).get('q_usage')?.startsWith('@'));
 assert.ok(definitions.size>0);
 const copied=path(first.page);
 const fresh=await open(t,{url:copied,api,ready:'.usage-workspace-table .qt-table'});
 await fresh.settled();
 assert.equal(await fresh.page.locator('.query-table-error').count(),0);
 assert.ok(fresh.calls.some(call=>call.path==='/api/query-snapshots'&&call.method==='GET'));
});

test('literal text reveal opens the reader Find UI and reveals a match without focusing its input', {skip},async t=>{
 const api={'POST /api/search/text':({body})=>({items:[{message_id:'m-1',start_byte:0,end_byte:4,snippet:'Turn 0'}],complete:true,stop_reason:'complete',elapsed_ms:1,coverage:'Synthetic fixture'})};
 const opened=await open(t,{url:`/work/${workID}?conversation=${rootConversation}&reveal=text%3ATurn`,api,ready:'.conversation-search-composer'});
 await opened.page.waitForFunction(()=>!!document.querySelector('[data-message-id="m-1"].pharos-reveal'));
 const search=JSON.parse(new URL(opened.page.url()).searchParams.get('reader_search'));
 assert.equal(search.text,'Turn');assert.equal(search.match,'literal');
 assert.equal(await opened.page.locator('.conversation-search-composer').evaluate(element=>element.contains(document.activeElement)),false);
 await opened.page.getByRole('button',{name:'Clear highlights',exact:true}).click();
 await opened.page.waitForFunction(()=>!document.querySelector('[data-message-id="m-1"].pharos-reveal'));
 await opened.page.getByRole('button',{name:'Close conversation search',exact:true}).click();
 assert.equal(new URL(opened.page.url()).searchParams.has('reveal'),false);
});

test('a row identity can highlight its actual rendered row without opening a modal', {skip},async t=>{
 const query={...EMPTY_QUERY,select:[{field:'id'},{field:'model'}],limit:5};
 const opened=await open(t,{url:'/analyze?'+new URLSearchParams({analyze_mode:'query',analyze_data:'usage',q_usage:encodeQuery(query)}),ready:'.usage-workspace-table .qt-row'});
 const id=await opened.page.locator('.usage-workspace-table .qt-row td').first().textContent();
 await opened.page.evaluate(id=>{const url=new URL(location.href);url.searchParams.set('reveal',`row:${id}`);history.replaceState(history.state,'',url);window.dispatchEvent(new Event('pharos:uri-changed'));},id.trim());
 await opened.page.waitForFunction(()=>!!document.querySelector('tr.pharos-reveal'));
 assert.equal(await opened.page.locator('[role="dialog"]:visible').count(),0);
});


test('section reveal opens content ancestors and clearing an empty Find removes attention', {skip},async t=>{
 const opened=await open(t,{url:'/cards?card_topic=all',ready:'.card-library-page'});
 await opened.page.evaluate(()=>{
  const details=document.createElement('details'),summary=document.createElement('summary'),target=document.createElement('p');
  summary.textContent='Synthetic nested content';target.id='synthetic-section';target.textContent='Attention fixture';details.append(summary,target);document.querySelector('.view.active').append(details);
  const url=new URL(location.href);url.searchParams.set('reveal','section:synthetic-section');history.replaceState(history.state,'',url);window.dispatchEvent(new Event('pharos:uri-changed'));
 });
 await opened.page.waitForFunction(()=>document.querySelector('#synthetic-section')?.classList.contains('pharos-reveal')&&document.querySelector('#synthetic-section')?.closest('details').open);
 assert.equal(await opened.page.evaluate(()=>document.activeElement===document.querySelector('#synthetic-section')),false);
 await opened.page.evaluate(()=>{
  const url=new URL(location.href);url.searchParams.set('reveal','text:Attention fixture');history.replaceState(history.state,'',url);window.dispatchEvent(new Event('pharos:uri-changed'));
 });
 await opened.page.locator('.pharos-attention-find input').fill('');
 await opened.page.locator('.pharos-attention-find input').blur();
 await opened.page.waitForFunction(()=>!new URLSearchParams(location.search).has('reveal'));
});


test('multiple literal attention targets are highlighted with a single reveal landing', {skip},async t=>{
 const opened=await open(t,{url:'/cards?card_topic=all',ready:'.card-library-page'});
 await opened.page.evaluate(()=>{
  const content=document.createElement('p');content.textContent='Alpha fixture — Beta fixture';document.querySelector('.view.active').append(content);
  const url=new URL(location.href);url.searchParams.append('highlight','text:Alpha fixture');url.searchParams.append('highlight','text:Beta fixture');url.searchParams.set('reveal','text:Beta fixture');history.replaceState(history.state,'',url);window.dispatchEvent(new Event('pharos:uri-changed'));
 });
 await opened.page.waitForFunction(()=>{
  const marks=[...(CSS.highlights.get('pharos-matches')||[])].map(range=>range.toString());
  return marks.includes('Alpha fixture')&&marks.includes('Beta fixture')&&[...(CSS.highlights.get('pharos-landing')||[])][0]?.toString()==='Beta fixture';
 });
 assert.equal(await opened.page.locator('.pharos-attention-find input').evaluate(input=>input===document.activeElement),false);
});


test('conversation Find restores a later batch with fresh cursors and renews relative dates', {skip},async t=>{
 let execution=0;
 const api={'POST /api/search/text':({body})=>{
  if(!body.cursor){execution++;return {items:[{message_id:'m-1',snippet:'First',start_byte:0,end_byte:4}],next_cursor:'execution-'+execution,complete:false,stop_reason:'limit',elapsed_ms:1,coverage:'Synthetic fixture'};}
  return {items:[{message_id:'m-15',snippet:'Later',start_byte:0,end_byte:4}],complete:true,stop_reason:'complete',elapsed_ms:1,coverage:'Synthetic fixture'};
 }};
 const opened=await open(t,{url:`/work/${workID}?conversation=${rootConversation}&reveal=text%3ATurn`,api,ready:'.conversation-search-composer'});
 await opened.page.getByRole('button',{name:'Next search batch',exact:true}).click();
 await opened.page.waitForFunction(()=>new URLSearchParams(location.search).get('reader_page')==='1');
 await opened.page.getByRole('button',{name:'Later',exact:true}).waitFor();
 const url=new URL(opened.page.url()),saved=JSON.parse(url.searchParams.get('reader_search'));
 saved.scope.from='2020-01-01T00:00:00.000Z';saved.scope.to='';saved.dateWindow={duration:'7d',from:saved.scope.from};
 url.searchParams.set('reader_search',JSON.stringify(saved));url.searchParams.delete('reveal');
 const fresh=await open(t,{url:url.pathname+url.search,api,ready:'.conversation-search-composer'});
 await fresh.page.getByRole('button',{name:'Later',exact:true}).waitFor();
 assert.equal(new URL(fresh.page.url()).searchParams.get('reader_page'),'1');
 const requests=fresh.calls.filter(call=>call.path==='/api/search/text');
 assert.equal(requests[0].body.cursor,'');assert.ok(requests.some(call=>call.body.cursor==='execution-'+execution));
 assert.ok(Math.abs(Date.parse(requests[0].body.from)-(Date.now()-7*86400000))<60000);
});

test('Carbon factor disclosures survive copying into a fresh browser', {skip},async t=>{
 const first=await open(t,{url:'/analyze?analyze_topic=carbon',ready:'#carbonCard .carbon-calculation > summary'});
 await first.page.locator('#carbonCard .carbon-calculation > summary').click();
 await first.page.locator('#carbonCard .carbon-method > summary').click();
 await first.page.waitForFunction(()=>JSON.parse(new URLSearchParams(location.search).get('disclosures')||'{}')['carbon:reference']===true);
 const fresh=await open(t,{url:path(first.page),ready:'#carbonCard .carbon-calculation > summary'});
 await fresh.page.waitForFunction(()=>document.querySelector('#carbonCard .carbon-method')?.open);
});


for(const empty of ['absent','empty'])test(`Carbon disclosure history restores ${empty} overrides after rerender`,{skip},async t=>{
 const params=new URLSearchParams({analyze_topic:'carbon',disclosures:JSON.stringify({'carbon:calculation':true,'carbon:reference':true})});
 const app=await open(t,{url:'/analyze?'+params,ready:'#carbonCard .carbon-method > summary'}),{page}=app;
 await page.waitForFunction(()=>document.querySelector('#carbonCard .carbon-calculation')?.open&&document.querySelector('#carbonCard .carbon-method')?.open);
 await app.settled();
 // A route refresh replaces Carbon's DOM while keeping the current URL state.
 await page.evaluate(()=>window.dispatchEvent(new Event('pharos:route')));await app.settled();
 assert.equal(await page.locator('#carbonCard .carbon-method').evaluate(element=>element.open),true);
 await page.evaluate(empty=>{const url=new URL(location.href);if(empty==='empty')url.searchParams.set('disclosures','{}');else url.searchParams.delete('disclosures');window.pharosNavigate(url.pathname+url.search);},empty);
 const closed=()=>page.waitForFunction(()=>!document.querySelector('#carbonCard .carbon-calculation')?.open&&!document.querySelector('#carbonCard .carbon-method')?.open);
 await closed();await app.settled();
 assert.equal(new URL(page.url()).searchParams.get('disclosures'),empty==='empty'?'{}':null);
 await page.goBack();
 await page.waitForFunction(()=>document.querySelector('#carbonCard .carbon-calculation')?.open&&document.querySelector('#carbonCard .carbon-method')?.open);
 await page.goForward();await closed();await app.settled();
 assert.equal(new URL(page.url()).searchParams.get('disclosures'),empty==='empty'?'{}':null);
});


test('copied disclosure defaults override a different browser preference without a toggle', {skip},async t=>{
 const first=await open(t,{url:'/analyze?analyze_mode=query&analyze_data=usage',ready:'.usage-query-ideas',preferences:{'pharos-query-ideas:usage':false}});
 await first.page.waitForFunction(()=>JSON.parse(new URLSearchParams(location.search).get('disclosures')||'{}')['query:usage:ideas']===false);
 const fresh=await open(t,{url:path(first.page),ready:'.usage-query-ideas',preferences:{'pharos-query-ideas:usage':true}});
 assert.equal(await fresh.page.locator('.usage-query-ideas').evaluate(element=>element.open),false);
});


test('a transcript preset URL overrides preferences without a category list', {skip},async t=>{
 const opened=await open(t,{url:`/work/${workID}?conversation=${rootConversation}&reader_filter_level=primary`,ready:'.transcript-filter',preferences:{'pharos-transcript-filter':{level:'all'}}});
 const primary=opened.page.getByRole('group',{name:'Show in transcript',exact:true}).getByRole('button',{name:'Primary only',exact:true});
 assert.equal(await primary.getAttribute('aria-pressed'),'true');
 assert.equal(new URL(opened.page.url()).searchParams.get('reader_filter_level'),'primary');
 assert.deepEqual(JSON.parse(new URL(opened.page.url()).searchParams.get('reader_filter')),['prompts','response']);
});

for(const empty of ['absent','empty'])test(`Find history restores its closed ${empty} state and cancels a pending request`,{skip},async t=>{
 let hold=false,release;
 const pending=new Promise(resolve=>release=resolve);t.after(()=>release());
 const api={'POST /api/search/text':async()=>{if(hold)await pending;return {items:[{message_id:'m-1',snippet:'Retained search result',start_byte:0,end_byte:4}],complete:true,stop_reason:'complete',elapsed_ms:1,coverage:'Synthetic fixture'};}};
 const initial=`/work/${workID}?conversation=${rootConversation}${empty==='empty'?'&reader_search=%7B%7D':''}`;
 const app=await open(t,{url:initial,api,ready:'.conversation-panel'}),{page}=app;
 await page.evaluate(()=>{
  const url=new URL(location.href),draft=structuredClone(window.pharosView.contract.fields.reader_search.details.literal_example);
  url.searchParams.set('reader_search',JSON.stringify(draft));window.pharosNavigate(url.pathname+url.search);
 });
 await page.getByRole('button',{name:'Retained search result',exact:true}).waitFor();
 await page.goBack();
 await page.locator('.conversation-search-composer').waitFor({state:'hidden'});
 assert.equal(new URL(page.url()).searchParams.get('reader_search'),empty==='empty'?'{}':null);
 hold=true;
 const started=page.waitForRequest(request=>new URL(request.url()).pathname==='/api/search/text'&&request.method()==='POST');
 await page.goForward();await started;
 await page.locator('.conversation-search-composer').waitFor();
 await page.goBack();await page.locator('.conversation-search-composer').waitFor({state:'hidden'});
 release();await app.settled();
 assert.equal(await page.locator('.conversation-search-composer').count(),0);
 assert.equal(new URL(page.url()).searchParams.get('reader_search'),empty==='empty'?'{}':null);
});

test('session Next and Previous use one live request while URL restoration replays the ordinal',{skip},async t=>{
 let execution=0;
 const cursors=[];
 const api={'GET /api/work/:id/delegation':({query})=>{
  const cursor=query.get('cursor')||'';cursors.push(cursor);
  if(!cursor)execution++;
  const page=cursor?Number(cursor.split(':')[1]):0;
  const base=defaultApi['GET /api/work/:id/delegation']({query});
  return {...base,offset:page*40,total:120,next_cursor:page<2?`${execution}:${page+1}`:''};
 }};
 const app=await open(t,{url:`/work/${workID}?conversation=${rootConversation}&explorer=table`,api,ready:'.delegation-pagination'}),{page}=app;
 const pagination=page.locator('.delegation-pagination');
 assert.deepEqual(cursors,['']);
 for(const [direction,ordinal]of [['Next',1],['Next',2],['Previous',1],['Previous',0]]){
  const before=cursors.length;
  await pagination.getByRole('button',{name:direction,exact:true}).click();
  await page.waitForFunction(ordinal=>new URLSearchParams(location.search).get('delegation_page')===String(ordinal),ordinal);
  await pagination.getByRole('button',{name:ordinal===2?'Previous':'Next',exact:true}).waitFor({state:'visible'});
  await app.settled();
  assert.equal(cursors.length-before,1,`${direction} to ${ordinal}`);
 }
 const before=cursors.length;
 await page.evaluate(()=>{const url=new URL(location.href);url.searchParams.set('delegation_page','2');window.pharosNavigate(url.pathname+url.search);});
 await pagination.getByRole('button',{name:'Previous',exact:true,disabled:false}).waitFor();
 await pagination.getByRole('button',{name:'Next',exact:true,disabled:true}).waitFor();
 await app.settled();
 assert.deepEqual(cursors.slice(before),['',`${execution}:1`,`${execution}:2`]);
});

test('reader-owned text attention becomes idle and ignores its own status UI mutations',{skip},async t=>{
 const api={'POST /api/search/text':()=>({items:[{message_id:'m-1',snippet:'Turn 0',start_byte:0,end_byte:4}],complete:true,stop_reason:'complete',elapsed_ms:1,coverage:'Synthetic fixture'})};
 const app=await open(t,{url:'/cards?reveal=text:Card',api,ready:'.pharos-attention-find'}),{page}=app;
 await page.evaluate(url=>window.pharosNavigate(url),`/work/${workID}?conversation=${rootConversation}&reveal=text:Turn`);
 await page.locator('.conversation-search-composer').waitFor();
 await page.waitForFunction(()=>!!document.querySelector('.pharos-attention-find[hidden]'));
 await app.settled();
 await page.evaluate(()=>{
  const walk=document.createTreeWalker.bind(document);let walks=0;
  document.createTreeWalker=(...args)=>{walks++;return walk(...args)};
  window.attentionWalks=()=>walks;window.attentionPrevious=-1;window.attentionStable=0;
  document.querySelector('.pharos-attention-status span').textContent='Synthetic status update';
 });
 await page.waitForFunction(()=>{
  const current=window.attentionWalks();window.attentionStable=current===window.attentionPrevious?window.attentionStable+1:0;window.attentionPrevious=current;
  return window.attentionStable>=4;
 });
});

test('linked message attention opens once and lets the user collapse its disclosure',{skip},async t=>{
 const app=await open(t,{url:`/work/${workID}?conversation=${rootConversation}&highlight=message:m-1`,ready:'[data-message-id="m-1"].pharos-highlight'}),{page}=app;
 await page.evaluate(()=>{
  const details=document.querySelector('[data-message-id="m-1"]').closest('details');
  if(!details?.open)throw Error('Attention did not open its content');
  window.linkedDisclosure=details;details.querySelector('summary').click();
 });
 await app.settled();
 await page.evaluate(()=>window.pharosView.refresh());await app.settled();
 assert.equal(await page.evaluate(()=>window.linkedDisclosure.open),false);
 assert.ok(JSON.parse(new URL(page.url()).searchParams.get('reader_collapsed')||'[]').length>0);
});

test('retained work and Find components cannot pollute another page URL',{skip},async t=>{
 const api={"POST /api/search/text":()=>({items:[],complete:true,stop_reason:"complete",elapsed_ms:1,coverage:"Synthetic fixture"})};
 const app=await open(t,{url:`/work/${workID}?conversation=${rootConversation}&explorer=parallel`,api,ready:'.conversation-panel'}),{page}=app;
 await page.evaluate(()=>{const url=new URL(location.href);url.searchParams.set('reveal','text:Turn');window.pharosNavigate(url.pathname+url.search)});
 await page.locator('.conversation-search-composer').waitFor();
 await page.evaluate(()=>window.pharosNavigate('/analyze?analyze_topic=tokens&reveal=text:Tokens'));
 await page.locator('.usage-workspace').waitFor();await app.settled();
 const params=new URL(page.url()).searchParams;
 assert.deepEqual([...params.keys()].filter(key=>/^(delegation_|reader_)/.test(key)),[]);
 assert.equal(params.get('reveal'),'text:Tokens','closing the old reader must preserve the new page attention');
 await page.goBack();await page.locator('.conversation-panel').waitFor();await app.settled();
 assert.equal(new URL(page.url()).pathname.includes('/work/'),true);
});

test('row disclosures record deliberate choices without capturing every rendered default',{skip},async t=>{
 const schema=loadSchema(toolCallsDocument),now=Date.now();
 const rows=Array.from({length:12},(_,i)=>({id:`payload-${i}`,workspace_id:workID,conversation_id:rootConversation,input_text:'Synthetic retained input '.repeat(50),tool_name:'shell',status:'success',started_at:new Date(now).toISOString()}));
 const api={'* /api/query/tool_calls/:operation':({params,body,query})=>answerQuery(params.operation,body,rows,schema,{now,id:query.get('id')})};
 const route=offset=>'/analyze?'+new URLSearchParams({analyze_mode:'query',analyze_data:'tool_calls',q_tool_calls:encodeQuery({...EMPTY_QUERY,select:[{field:'id'},{field:'tool_name'},{field:'__qt_item_details__'}],itemDetails:{modes:['expand']},limit:4,offset,fitToScreen:false})});
 const app=await open(t,{url:route(0),api,ready:'.usage-workspace-table .qt-row'}),{page}=app;
 await page.locator('.usage-workspace-table .qt-row').first().getByRole('button',{name:/^Expand /}).click();
 await page.locator('.usage-payload').first().waitFor();
 const entries=()=>Object.keys(JSON.parse(new URL(page.url()).searchParams.get('disclosures')||'{}')).filter(key=>key.startsWith('row:'));
 assert.deepEqual(entries(),[]);
 await page.locator('.usage-payload summary').first().click();await app.settled();
 const chosen=entries();assert.equal(chosen.length,1);
 await page.evaluate(next=>{const url=new URL(location.href);url.searchParams.set('q_tool_calls',new URL(next,location.origin).searchParams.get('q_tool_calls'));window.pharosNavigate(url.pathname+url.search)},route(4));await app.settled();
 await page.locator('.usage-workspace-table .qt-row').first().getByRole('button',{name:/^Expand /}).click();
 await page.locator('.usage-payload').first().waitFor();
 assert.deepEqual(entries(),chosen);
 await page.goBack();await page.locator('.usage-payload').first().waitFor();await app.settled();
 assert.equal(await page.locator('.usage-payload').first().evaluate(element=>element.open),true);
});

test('manual assumption changes save chosen defaults while restoring a link stays read-only',{skip},async t=>{
 const storedCarbon={scenario:'high',grid:'world',pueScenario:'high'},storedParallel={gap:60,counting:'workspace',repository:'saved',range:'7d'};
 const preferences={'pharos-carbon-v1':storedCarbon,'pharos-concurrency-v1':storedParallel};
 const carbon=await open(t,{url:'/analyze?'+new URLSearchParams({analyze_topic:'carbon',analyze_carbon:JSON.stringify({scenario:'low',grid:'world'})}),preferences,ready:'#carbonCard .carbon-calculation > summary'});
 assert.equal(await carbon.page.evaluate(()=>window.pharosPrefs.get('pharos-carbon-v1').scenario),'high');
 await carbon.page.locator('#carbonCard .carbon-calculation > summary').click();
 await carbon.page.getByRole('combobox',{name:'Electricity grid',exact:true}).selectOption('custom');
 await carbon.page.waitForFunction(()=>window.pharosPrefs.get('pharos-carbon-v1').grid==='custom');
 await carbon.page.evaluate(()=>window.pharosPrefs.flush());
 assert.equal(await carbon.page.evaluate(()=>window.pharosPrefs.get('pharos-carbon-v1').scenario),'high');
 assert.equal(JSON.parse(new URL(carbon.page.url()).searchParams.get('analyze_carbon')).scenario,'low');
 const parallel=await open(t,{url:'/analyze?'+new URLSearchParams({analyze_topic:'parallelism',analyze_parallelism:JSON.stringify({gap:0,counting:'workspace',range:'30d'})}),preferences,ready:'.concurrency-panel'});
 assert.equal(await parallel.page.evaluate(()=>window.pharosPrefs.get('pharos-concurrency-v1').gap),60);
 await parallel.page.getByRole('combobox',{name:'Count by',exact:true}).selectOption('agent');
 await parallel.page.waitForFunction(()=>window.pharosPrefs.get('pharos-concurrency-v1').counting==='agent');
 await parallel.page.evaluate(()=>window.pharosPrefs.flush());
 assert.equal(await parallel.page.evaluate(()=>window.pharosPrefs.get('pharos-concurrency-v1').gap),60);
 assert.equal(JSON.parse(new URL(parallel.page.url()).searchParams.get('analyze_parallelism')).gap,0);
});

test('manual Findings ranking saves the default and a new unlinked view uses it',{skip},async t=>{
 let rank='usd';const saves=[];
 const api={'GET /api/findings':()=>({...defaultApi['GET /api/findings'],settings:{...defaultApi['GET /api/findings'].settings,rank}}),'POST /api/findings/settings':({body})=>{rank=body.rank??rank;saves.push(body);return {ok:true}}};
 const first=await open(t,{url:'/findings?findings_rank=minutes',api,ready:'.findings-rank'});
 assert.equal(saves.length,0);
 const saved=first.page.waitForResponse(response=>new URL(response.url()).pathname==='/api/findings/settings'&&response.request().method()==='POST');
 await first.page.locator('.findings-rank').getByRole('button',{name:'Tokens',exact:true}).click();await saved;
 await first.settled();assert.equal(rank,'tokens');
 assert.equal(new URL(first.page.url()).searchParams.get('findings_rank'),'tokens');
 const fresh=await open(t,{url:'/findings',api,ready:'.findings-rank'});
 assert.equal(await fresh.page.locator('.findings-rank').getByRole('button',{name:'Tokens',exact:true}).getAttribute('aria-pressed'),'true');
});


for(const dataset of ['library','usage'])test(`native ${dataset} query preserves its composed host search in a fresh browser`,{skip},async t=>{
 const draft={editor:'visual',match:'literal',mode:'advanced',text:'failure',root:{kind:'group',join:'all',children:[],exclude:false},scope:{surface:'prose',roles:[],kinds:[],repository:'',provider:'',source:'',from:'',to:'',conversation_id:'',workspace_id:''},fuzzy:false,caseSensitive:false,separators:false};
 const parameter=dataset==='library'?'text_builder':'record_usage',search=JSON.stringify(dataset==='library'?draft:{field:'model',draft});
 // site_link composes view search into an explicitly empty native host state.
 const query={...EMPTY_QUERY,select:[{field:dataset==='library'?'title':'model'}],limit:5,fitToScreen:false,hostState:{version:1,params:{[parameter]:[search]}},aggregations:[{id:'calls',op:'count',groupBy:[]}]};
 const params=new URLSearchParams({[`q_${dataset}`]:encodeQuery(query),[parameter]:search});
 if(dataset!=='library'){params.set('analyze_mode','query');params.set('analyze_data',dataset);}
 const app=await open(t,{url:`/${dataset==='library'?'library':'analyze'}?${params}`,ready:dataset==='library'?'#queryTableLibrary .qt-row':'.usage-workspace-table .qt-row'});
 await app.settled();
 const normalized=new URL(app.page.url()).searchParams;
 const restored=decodeQueryStrict(normalized.get(`q_${dataset}`));
 assert.deepEqual(JSON.parse(normalized.get(parameter)),JSON.parse(restored.hostState.params[parameter][0]));
 const applied=JSON.parse(restored.hostState.params[parameter][0]);
 assert.equal((dataset==='library'?applied:applied.draft).text,'failure');
 const requests=app.calls.filter(call=>call.method==='POST'&&call.path.startsWith(`/api/query/${dataset}/`));
 assert.ok(requests.length>0);
 for(const request of requests){
  assert.ok(dataset==='library'?request.query.has('find')||request.query.has('find_where'):request.query.has('record_search'),`${request.path} dropped the applied search`);
  if(dataset==='usage')assert.equal(request.path,'/api/query/usage/query-v3');
 }
 if(dataset==='usage')assert.ok(requests.some(call=>call.body.rows===false),'ordinary metrics use V3 too');
});


test('same-conversation links restore transcript controls without reloading', {skip},async t=>{
 const app=await open(t,{url:`/work/${workID}?conversation=${rootConversation}&reader_filter_level=all`,ready:'.transcript-filter'}),{page}=app;
 const turn=page.locator('.turn').first(),id=await turn.getAttribute('data-pharos-disclosure');
 assert.ok(id);
 await page.evaluate(id=>{const url=new URL(location.href);url.searchParams.delete('reader_filter');url.searchParams.set('reader_filter_level','primary');url.searchParams.set('reader_collapsed',JSON.stringify([id]));window.pharosNavigate(url.pathname+url.search);},id);
 await page.waitForFunction(()=>document.querySelector('.transcript-levels button[aria-pressed="true"]')?.textContent==='Primary only'&&!document.querySelector('.turn').open);
 await app.settled();
 assert.equal(await page.getByRole('group',{name:'Show in transcript',exact:true}).getByRole('button',{name:'Primary only',exact:true}).getAttribute('aria-pressed'),'true');
 assert.deepEqual(JSON.parse(new URL(page.url()).searchParams.get('reader_collapsed')),[id]);
});

for(const empty of ['absent','empty'])test(`reader disclosure history restores ${empty} overrides to native defaults`,{skip},async t=>{
 const initial=`/work/${workID}?conversation=${rootConversation}${empty==='empty'?'&reader_expanded=%5B%5D&reader_collapsed=%5B%5D':''}`;
 const app=await open(t,{url:initial,ready:'.transcript-filter'}),{page}=app;
 const details=page.locator('details[data-pharos-disclosure]'),baseline=await details.evaluateAll(items=>items.map(item=>({id:item.dataset.pharosDisclosure,open:item.open})));
 const opened=baseline.find(item=>item.open),closed=baseline.find(item=>!item.open);assert.ok(opened&&closed);
 await page.evaluate(({opened,closed})=>{const url=new URL(location.href);url.searchParams.set('reader_expanded',JSON.stringify([closed.id]));url.searchParams.set('reader_collapsed',JSON.stringify([opened.id]));window.pharosNavigate(url.pathname+url.search);},{opened,closed});
 await page.waitForFunction(({opened,closed})=>{const details=[...document.querySelectorAll('details[data-pharos-disclosure]')];return details.find(item=>item.dataset.pharosDisclosure===opened.id)?.open===false&&details.find(item=>item.dataset.pharosDisclosure===closed.id)?.open===true;},{opened,closed});
 await page.goBack();
 await page.waitForFunction(baseline=>baseline.every(expected=>[...document.querySelectorAll('details[data-pharos-disclosure]')].find(item=>item.dataset.pharosDisclosure===expected.id)?.open===expected.open),baseline);
 await app.settled();
 const params=new URL(page.url()).searchParams;
 for(const name of ['reader_expanded','reader_collapsed'])assert.equal(params.get(name),empty==='empty'?'[]':null);
 await page.goForward();
 await page.waitForFunction(({opened,closed})=>{const details=[...document.querySelectorAll('details[data-pharos-disclosure]')];return details.find(item=>item.dataset.pharosDisclosure===opened.id)?.open===false&&details.find(item=>item.dataset.pharosDisclosure===closed.id)?.open===true;},{opened,closed});
});


for(const empty of ['absent','empty'])test(`reader content history restores ${empty} overrides to collapsed defaults`,{skip},async t=>{
 const messages=[{id:'long-prose',role:'assistant',kind:'message',text:'Long synthetic prose. '.repeat(60),created_at:new Date().toISOString()}];
 const api={'GET /api/work/:id/conversations/:conversation':request=>({...defaultApi['GET /api/work/:id/conversations/:conversation'](request),messages})};
 const params=new URLSearchParams({conversation:rootConversation,reader_content:JSON.stringify({'long-prose':true,['summary:'+rootConversation]:true})});
 const app=await open(t,{api,url:`/work/${workID}?${params}`,ready:'.message-toggle'}),{page}=app;
 const expanded=()=>page.waitForFunction(()=>document.querySelector('.message-toggle')?.getAttribute('aria-expanded')==='true'&&!document.querySelector('.tool-summary')?.hidden);
 const collapsed=()=>page.waitForFunction(()=>document.querySelector('.message-toggle')?.getAttribute('aria-expanded')==='false'&&document.querySelector('.message-prose')?.classList.contains('prose-truncated')&&document.querySelector('.tool-summary')?.hidden);
 await expanded();await app.settled();
 await page.evaluate(empty=>{const url=new URL(location.href);if(empty==='empty')url.searchParams.set('reader_content','{}');else url.searchParams.delete('reader_content');window.pharosNavigate(url.pathname+url.search);},empty);
 await collapsed();await app.settled();
 const fresh=await open(t,{api,url:path(page),ready:'.message-toggle'});
 assert.equal(await fresh.page.locator('.message-toggle').getAttribute('aria-expanded'),'false');
 assert.equal(await fresh.page.locator('.tool-summary').evaluate(element=>element.hidden),true);
 await page.goBack();await expanded();await page.goForward();await collapsed();await app.settled();
 assert.equal(new URL(page.url()).searchParams.get('reader_content'),empty==='empty'?'{}':null);
});


test('header navigation clears source attention and explicit destination links retain their targets',{skip},async t=>{
 const params=new URLSearchParams({conversation:rootConversation,highlight:'text:Turn',reveal:'page',nav_collapsed:'0'});
 const api={'POST /api/search/text':()=>({items:[],complete:true,stop_reason:'complete',elapsed_ms:1,coverage:'Synthetic fixture'})};
 const app=await open(t,{url:`/work/${workID}?${params}`,api,ready:'.conversation-panel'}),{page}=app;
 await page.locator('header button[data-view="library"]').click();await page.locator('#library.active').waitFor();await app.settled();
 const destination=new URL(page.url()).searchParams;
 assert.deepEqual(destination.getAll('highlight'),[]);assert.equal(destination.has('reveal'),false);assert.equal(destination.get('nav_collapsed'),'0');
 assert.equal(await page.locator('.pharos-attention-find:visible').count(),0);
 await page.goBack();await page.waitForFunction(()=>new URLSearchParams(location.search).get('reveal')==='page');
 assert.deepEqual(new URL(page.url()).searchParams.getAll('highlight'),['text:Turn']);
 await page.evaluate(()=>window.pharosNavigate('/cards?highlight=page&reveal=page'));
 await page.locator('.card-library-page').waitFor();await app.settled();
 assert.deepEqual(new URL(page.url()).searchParams.getAll('highlight'),['page']);assert.equal(new URL(page.url()).searchParams.get('reveal'),'page');
});


test('Next failed action reveals retained failures without a script error',{skip},async t=>{
 const app=await open(t,{url:`/work/${workID}?conversation=${rootConversation}`,ready:'.conversation-panel'}),{page}=app;
 const next=page.getByRole('button',{name:'Next failed action',exact:true});
 await next.click();
 await page.waitForFunction(()=>[...document.querySelectorAll('.tool-event.error')].some(item=>item.open));
 await next.click();await app.settled();
 assert.ok(await page.locator('.tool-event.error').count());
});

test('opening work and returning to Search keeps the native Library search URL',{skip},async t=>{
 const draft={editor:'visual',match:'literal',mode:'advanced',text:'failure',root:{kind:'group',join:'all',children:[],exclude:false},scope:{surface:'prose',roles:[],kinds:[],repository:'',provider:'',source:'',from:'',to:'',conversation_id:'',workspace_id:''},fuzzy:false,caseSensitive:false,separators:false};
 const token=JSON.stringify(draft),query={...EMPTY_QUERY,select:[{field:'title'}],hostState:{version:1,params:{text_builder:[token]}},limit:5,fitToScreen:false};
 const app=await open(t,{url:'/library?'+new URLSearchParams({q_library:encodeQuery(query),text_builder:token}),ready:'#queryTableLibrary .qt-row'}),{page}=app;
 await app.settled();
 const original=decodeQueryStrict(new URL(page.url()).searchParams.get('q_library'));
 await page.evaluate(id=>window.pharosOpenDetail(id),workID);
 await page.waitForFunction(()=>location.pathname.startsWith('/work/')&&!!document.querySelector('#detail.active'));
 assert.equal(JSON.parse(new URL(page.url()).searchParams.get('text_builder')).text,'failure');
 await page.evaluate(()=>document.getElementById('back').click());
 await page.waitForFunction(()=>location.pathname==='/library');await app.settled();
 const restored=new URL(page.url()).searchParams;
 assert.deepEqual(decodeQueryStrict(restored.get('q_library')),original);
 assert.equal(JSON.parse(restored.get('text_builder')).text,'failure');
});

test('reader Find stepping keeps text highlights without rerunning the search',{skip},async t=>{
 const api={'POST /api/search/text':()=>({items:['m-1','m-5','m-9'].map(message_id=>({message_id,start_byte:0,end_byte:4,snippet:'Turn'})),complete:true,stop_reason:'complete',elapsed_ms:1,coverage:'Synthetic fixture'})};
 const app=await open(t,{url:`/work/${workID}?conversation=${rootConversation}&highlight=text:Turn`,api,ready:'.conversation-search-composer'}),{page}=app;
 await page.getByRole('button',{name:'Next matching message',exact:true}).waitFor();await app.settled();
 const requests=()=>app.calls.filter(call=>call.path==='/api/search/text').length,initial=requests();
 await page.getByRole('button',{name:'Next matching message',exact:true}).click();
 await page.getByRole('button',{name:'Next matching message',exact:true}).click();
 await page.waitForFunction(()=>new URLSearchParams(location.search).get('reveal')==='message:m-5');await app.settled();
 assert.equal(requests(),initial);
 await page.getByRole('button',{name:'Previous matching message',exact:true}).click();
 await page.waitForFunction(()=>new URLSearchParams(location.search).get('reveal')==='message:m-1');await app.settled();
 assert.equal(requests(),initial);
});

for(const invalid of ['{"a":1}','{'])test(`a malformed reader content link still opens the conversation (${invalid})`,{skip},async t=>{
 const app=await open(t,{url:`/work/${workID}?`+new URLSearchParams({conversation:rootConversation,reader_content:invalid,overlay:'unknown',history_collapsed:'[null]'}),ready:'.conversation-panel'});
 await app.settled();
 await app.page.evaluate(()=>document.getElementById('historyToggle').click());
 await app.page.locator('#historyDrawer.open').waitFor();
 assert.ok(await app.page.locator('#historyList a').count());
 await app.page.evaluate(()=>document.getElementById('historyClose').click());
});

test('bookmarks ignore drawer state while preserving the page view',{skip},async t=>{
 const app=await open(t,{url:'/cards?card_window=30',ready:'.card-library-page'}),{page}=app;
 await app.settled();
 await page.evaluate(()=>document.getElementById('bookmarksToggle').click());
 await page.locator('#bookmarksDrawer.open').waitFor();
 await page.locator('#bookmarkAdd').click();
 await page.waitForFunction(()=>window.pharosPrefs.get('pharos-bookmarks-v1',[]).length>0);
 const saved=await page.evaluate(()=>window.pharosPrefs.get('pharos-bookmarks-v1',[])[0].url);
 const params=new URL(saved,'https://pharos.test').searchParams;
 for(const key of ['overlay','history_search','history_collapsed'])assert.equal(params.has(key),false);
 assert.equal(params.get('card_window'),'30');
 await page.evaluate(()=>document.getElementById('bookmarksClose').click());
 await page.evaluate(()=>document.getElementById('historyToggle').click());
 await page.locator('#historyFilter').fill('Cards');
 await page.evaluate(()=>document.getElementById('historyClose').click());
 assert.equal(await page.locator('#bookmarkStar').getAttribute('aria-label'),'Edit bookmark');
 await page.evaluate(url=>window.pharosNavigate(url),saved);await app.settled();
 assert.equal(await page.locator('#bookmarksDrawer').evaluate(element=>element.classList.contains('open')),false);
});

test('restored action previews remain below their action summary',{skip},async t=>{
 const first=await open(t,{url:`/work/${workID}?conversation=${rootConversation}`,ready:'.conversation-panel'});
 await first.page.getByRole('button',{name:/^Show diff for /}).first().click();
 await first.page.locator('.chip-preview:visible').first().waitFor();await first.settled();
 const fresh=await open(t,{url:path(first.page),ready:'.chip-preview:visible'});
 assert.equal(await fresh.page.locator('.chip-preview:visible').first().evaluate(element=>element.getBoundingClientRect().top>=element.closest('.action-row').querySelector('summary').getBoundingClientRect().bottom),true);
});


test('Quick Stats keeps URL overlay restoration and opens portable Layout accordions',{skip},async t=>{
 const app=await open(t,{url:'/settings',viewport:{width:1485,height:791},ready:'#settings.active'}),{page}=app;
 await activateHeaderTool(page,'usageSummaryToggle');
 await page.getByRole('dialog',{name:'Quick Stats',exact:true}).waitFor();
 assert.equal(new URL(page.url()).searchParams.get('overlay'),'usage');
 const fresh=await open(t,{url:path(page),ready:'#usageSummary:not([hidden])'});
 assert.equal(await fresh.page.getByRole('dialog',{name:'Quick Stats',exact:true}).isVisible(),true);
 await fresh.page.keyboard.press('Escape');
 await fresh.page.waitForFunction(()=>document.getElementById('usageSummary').hidden&&new URLSearchParams(location.search).get('overlay')==='none');
 await app.shot('overlay-gear');
 await page.getByRole('button',{name:'Configure Quick Stats',exact:true}).click();
 await page.locator('#quickStatsOptions:not([hidden])').waitFor();await app.settled();
 await page.waitForFunction(()=>{const panel=document.getElementById('quickStatsSettings'),rect=panel.getBoundingClientRect();return panel.classList.contains('pharos-reveal')&&rect.top>=document.querySelector('body>header').getBoundingClientRect().bottom&&rect.top<innerHeight;});
 assert.equal(await page.locator('#usageSummary').isVisible(),false);
 assert.equal(await page.locator('#quickStatsSettings').evaluate(panel=>panel.contains(document.activeElement)),false,'reveal leaves keyboard focus alone');
 await app.shot('configuration-open');
 const copied=await open(t,{url:path(page),ready:'#quickStatsOptions:not([hidden])'});
 await copied.page.reload();await copied.page.locator('#quickStatsOptions:not([hidden])').waitFor();await copied.settled();
 for(const button of ['headerExpand','menuBarExpand'])await copied.page.locator('#'+button).click();
 const all=await open(t,{url:path(copied.page),ready:'#headerOptions:not([hidden])'});
 for(const id of ['quickStatsOptions','headerOptions','menuBarOptions'])assert.equal(await all.page.locator('#'+id).isVisible(),true);
 await copied.page.evaluate(()=>{history.pushState(null,'','/settings/layout');dispatchEvent(new PopStateEvent('popstate'));});
 const collapsed=()=>copied.page.waitForFunction(()=>['quickStatsOptions','headerOptions','menuBarOptions'].every(id=>document.getElementById(id).hidden));
 await collapsed();
 await copied.page.goBack();
 for(const id of ['quickStatsOptions','headerOptions','menuBarOptions'])await copied.page.locator('#'+id+':not([hidden])').waitFor();
 await copied.page.goForward();await collapsed();
 const legacy=await open(t,{url:'/settings/header',ready:'#headerOptions:not([hidden])'});
 assert.equal(new URL(legacy.page.url()).pathname,'/settings/layout');
 const disabled=await open(t,{url:'/settings/layout?'+new URLSearchParams({disclosures:JSON.stringify({menuBarPreferences:true})}),preferences:{'pharos-menu-bar':{enabled:false}},ready:'#headerExpand'});
 assert.equal(await disabled.page.locator('#menuBarOptions').isVisible(),false,'a linked section cannot enable the native menu');
});


const accordionCases = [
 {name:'capture/index diagnostics', route:'/settings/sync', keys:['sync:separate','sync:repairs','sync:diagnostics','sync:forecast:capture-forecast'], ready:'#syncDiagnosticTables details'},
 {name:'background schedule rows', route:'/settings/sync', keys:['background:writing','background:conversations'], ready:'#backgroundSchedule .bg-item-head'},
 {name:'connected cloud schedules', route:'/settings/backups', keys:['cloud:schedule:cloud-browser'], api:{'GET /api/cloud-backup':cloudScheduledStatus}, ready:'.cloud-schedule-settings'},
 {name:'library health checks', route:'/settings', keys:['library:drive-health'], ready:'#pharosDriveHealth', defaults:{'library:drive-health':true}},
 {name:'library application location', route:'/settings/libraries', keys:['library:application'], managedLibrary:{id:'local',name:'Pending history',config:'/Users/test/Libraries/Pending/library.toml'}, ready:'.library-settings-about'},
];
for(const sample of accordionCases)test(`accordion links restore ${sample.name} in fresh browsers and history`,{skip},async t=>{
 const route=entries=>sample.route+'?'+new URLSearchParams({disclosures:JSON.stringify(entries)});
 const entries=Object.fromEntries(sample.keys.map(key=>[key,true]));
 const options={api:sample.api,managedLibrary:sample.managedLibrary,ready:sample.ready};
 const app=await open(t,{...options,url:route(entries)}),{page}=app;
 const state=(browser,expected)=>browser.waitForFunction(expected=>Object.entries(expected).every(([key,open])=>{
  const element=[...document.querySelectorAll('[data-view-key],[data-pharos-target]')].find(element=>element.dataset.viewKey===key||element.dataset.pharosTarget==='section:'+key);
  return element&&(element.tagName==='DETAILS'?element.open:element.getAttribute('aria-expanded')==='true')===open;
 }),expected);
 await state(page,entries);
 await page.locator('[data-pharos-target="section:'+sample.keys.at(-1)+'"]').scrollIntoViewIfNeeded();await app.settled();await app.shot('opened-'+sample.name);
 const first=page.locator('[data-view-key="'+sample.keys[0]+'"]');
 if(await first.evaluate(element=>element.tagName==='DETAILS'))await first.locator('summary').first().click();else await first.click();
 await state(page,{...entries,[sample.keys[0]]:false});
 await page.waitForFunction(key=>JSON.parse(new URLSearchParams(location.search).get('disclosures')||'{}')[key]===false,sample.keys[0]);await app.settled();
 const fresh=await open(t,{...options,url:await page.evaluate(()=>location.pathname+location.search)});
 await state(fresh.page,{...entries,[sample.keys[0]]:false});
 await fresh.page.reload();await fresh.page.locator(sample.ready).first().waitFor();await state(fresh.page,{...entries,[sample.keys[0]]:false});
 await page.evaluate(url=>window.pharosNavigate(url),route({}));
 await state(page,Object.fromEntries(sample.keys.map(key=>[key,sample.defaults?.[key]??false])));
 await page.goBack();await state(page,{...entries,[sample.keys[0]]:false});
 await page.goForward();await state(page,Object.fromEntries(sample.keys.map(key=>[key,sample.defaults?.[key]??false])));
 assert.equal(app.calls.some(call=>call.method!=='GET'&&/^\/api\/(background\/(settings|run)|sync\/|cloud-backup\/(schedule|start))/.test(call.path)),false,'restoring sections must not run work or save settings');
 await app.shot(sample.name);
});

test('classic disclosure restoration ignores detached nodes and restores same-page URL writes',{skip},async t=>{
 const app=await open(t,{url:'/settings',ready:'#pharosDriveHealthDetails'}),{page}=app;
 await page.evaluate(()=>{
  const section=document.createElement('section');section.id='disclosure-fixture';
  section.innerHTML='<details data-view-key="fixture:native"><summary>Native disclosure</summary><p>Content</p></details>';
  document.querySelector('main').append(section);
 });
 await page.evaluate(()=>window.pharosView.write('disclosures',{'fixture:native':true}));
 await page.waitForFunction(()=>document.querySelector('[data-view-key="fixture:native"]').open);
 await page.evaluate(()=>{
  const stale=document.querySelector('[data-view-key="fixture:native"]');stale.remove();stale.open=false;stale.dispatchEvent(new Event('toggle',{bubbles:true}));
  const replacement=document.createElement('details');replacement.dataset.viewKey='fixture:native';replacement.innerHTML='<summary>Replacement disclosure</summary><p>Content</p>';
  document.getElementById('disclosure-fixture').append(replacement);
 });
 await page.waitForFunction(()=>document.querySelector('[data-view-key="fixture:native"]').open);
 await page.evaluate(()=>window.pharosView.write('disclosures',{'fixture:native':false}));
 await page.waitForFunction(()=>!document.querySelector('[data-view-key="fixture:native"]').open);
});

test('drive-status accordion links restore the overlay without moving focus and survive polling',{skip},async t=>{
 const entries={'status:cadence:frequent':false,'status:cadence:hourly':true};
 const api={'GET /api/status':busyStatus,'GET /api/status/stream':eventStream(busyStatus)};
 const target='section:status:cadence:hourly';
 const app=await open(t,{url:'/library?'+new URLSearchParams({overlay:'drive',disclosures:JSON.stringify(entries),reveal:target}),preferences:{'pharos-unified-activity-v1':true},api,ready:'#pharosStatusPanel'}),{page}=app;
 const expanded=()=>page.waitForFunction(()=>document.querySelector('[data-view-key="status:cadence:hourly"]')?.open===true&&document.querySelector('[data-view-key="status:cadence:frequent"]')?.open===false);
 const highlighted=()=>page.waitForFunction(target=>document.documentElement.dataset.pharosAttentionMissing===''&&[...document.querySelectorAll('.pharos-reveal')].some(element=>element.dataset.pharosTarget===target),target);
 await expanded();await highlighted();await app.settled();
 assert.equal(await page.locator('#pharosStatusPanel').evaluate(panel=>panel.contains(document.activeElement)),false);
 await page.evaluate(()=>{window.__disclosureScrolls=0;const original=Element.prototype.scrollIntoView;Element.prototype.scrollIntoView=function(...args){window.__disclosureScrolls++;return original.apply(this,args);};});
 for(const revision of [busyStatus.revision+1,busyStatus.revision+2]){
  app.push('/api/status/stream',{...busyStatus,revision});
  await page.waitForFunction(revision=>window.pharosStatusStore.current()?.document?.revision===revision,revision);
  await app.settled();await expanded();await highlighted();
 }
 assert.equal(await page.evaluate(()=>window.__disclosureScrolls),0,'polling reapplies highlights without repeating reveal scrolling');
 const fresh=await open(t,{url:path(page),preferences:{'pharos-unified-activity-v1':true},api,ready:'#pharosStatusPanel'});
 await fresh.page.waitForFunction(()=>document.querySelector('[data-view-key="status:cadence:hourly"]')?.open===true);
 await app.shot('drive-status-portable');
});

test('Carbon evidence row disclosures restore independently of the calculation section',{skip},async t=>{
 const app=await open(t,{url:'/analyze?analyze_topic=carbon',ready:'#carbonCard .carbon-calculation'}),{page}=app;
 await page.locator('#carbonCard .carbon-calculation > summary').click();
 const evidence=page.locator('#carbonCard button.carbon-row-trigger').first();
 await evidence.click();await app.settled();
 const key=await evidence.getAttribute('data-view-key');
 const fresh=await open(t,{url:path(page),ready:'#carbonCard .carbon-calculation[open]'});
 const restored=fresh.page.locator('[data-view-key="'+key+'"]');
 await fresh.page.waitForFunction(key=>{const button=[...document.querySelectorAll('button[data-view-key]')].find(button=>button.dataset.viewKey===key);return button?.getAttribute('aria-expanded')==='true'&&document.getElementById(button.getAttribute('aria-controls'))?.hidden===false;},key);
 assert.equal(await restored.evaluate(button=>document.getElementById(button.getAttribute('aria-controls')).textContent.trim().length>0),true,'restored evidence materializes its content');
 await fresh.shot('carbon-evidence-portable');
});

test('workspace file-index accordion survives copying and reload by work identity',{skip},async t=>{
 const original=defaultApi['GET /api/work/:id']({params:{id:workID}});
 const api={'GET /api/work/:id':{...original,files:[{path:'src/app.ts',conversation_id:rootConversation}]}};
 const key=`work:${workID}:files`,url=`/work/${workID}?`+new URLSearchParams({disclosures:JSON.stringify({[key]:true})});
 const app=await open(t,{url,api,ready:'.file-index[open]'}),{page}=app;
 await page.locator('.file-index > summary').click();
 await page.waitForFunction(key=>JSON.parse(new URLSearchParams(location.search).get('disclosures')||'{}')[key]===false,key);
 const fresh=await open(t,{url:await page.evaluate(()=>location.pathname+location.search),api,ready:'.file-index'});
 assert.equal(await fresh.page.locator('.file-index').evaluate(element=>element.open),false);
 await fresh.page.reload();await fresh.page.locator('.file-index').waitFor();await fresh.settled();
 assert.equal(await fresh.page.locator('.file-index').evaluate(element=>element.open),false);
});

test('feedback inbox log accordions use the same URL syntax without the application shell',{skip},async t=>{
 const {readFileSync}=await import('node:fs');
 const {repo}=await import('./harness.mjs');
 const id='00000000-0000-4000-8000-000000000001';
 const entry={version:1,id,application:{name:'Fixture app'},kind:'feedback',comment:'A report',annotations:[],diagnostics:{browser:'Fixture log'},received_at:new Date().toISOString(),tags:[],revision:'1'};
 const api={'GET /api/submissions':{submissions:[{...entry,annotation_count:0,screenshot_count:0,has_diagnostics:true}]},'GET /api/submissions/:id':entry};
 const boot=async url=>{
  const app=await open(t,{url:'/library',api,ready:'#queryTableLibrary .qt-row'});
  await app.page.route('**/feedback-inbox*',route=>route.fulfill({contentType:'text/html',body:readFileSync(repo+'/internal/uifeedback/assets/index.html')}));
  await app.page.route('**/assets/inbox.*',route=>{const name=new URL(route.request().url()).pathname.split('/').pop();return route.fulfill({contentType:name.endsWith('.js')?'text/javascript':'text/css',body:readFileSync(repo+'/internal/uifeedback/assets/'+name)});});
  await app.page.goto(new URL(url,app.page.url()).href);await app.page.locator('.report-detail details').waitFor();await app.settled();return app;
 };
 const key='feedback:logs:'+id;
 const app=await boot('/feedback-inbox?'+new URLSearchParams({submission:id,disclosures:JSON.stringify({[key]:true})}));
 assert.equal(await app.page.locator('.report-detail details').evaluate(element=>element.open),true);
 await app.page.locator('.report-detail summary').click();
 await app.page.waitForFunction(key=>JSON.parse(new URLSearchParams(location.search).get('disclosures')||'{}')[key]===false,key);
 const fresh=await boot(await app.page.evaluate(()=>location.pathname+location.search));
 assert.equal(await fresh.page.locator('.report-detail details').evaluate(element=>element.open),false);
});


test('revealing a closed accordion highlights its summary without overriding explicit false',{skip},async t=>{
 const entries={'sync:separate':false,'sync:diagnostics':false,'background:writing':false,'library:drive-health':false};
 const url='/settings/sync?'+new URLSearchParams({disclosures:JSON.stringify(entries),reveal:'section:sync:separate'});
 const app=await open(t,{url,ready:'[data-view-key="sync:separate"].pharos-reveal'}),{page}=app;
 assert.equal(await page.locator('[data-view-key="sync:separate"]').evaluate(element=>element.open),false);
 for(const [key,route]of [['sync:diagnostics','/settings/sync'],['background:writing','/settings/sync'],['library:drive-health','/settings']]){
  await page.evaluate(({key,route,entries})=>window.pharosNavigate(route+'?'+new URLSearchParams({disclosures:JSON.stringify(entries),reveal:'section:'+key})),{key,route,entries});
  const target=page.locator('[data-pharos-target="section:'+key+'"].pharos-reveal');await target.waitFor({state:'visible'});
  assert.equal(await target.evaluate(element=>element.contains(document.activeElement)),false,'revealing a closed section does not move focus');
  assert.equal(await page.locator('[data-view-key="'+key+'"],[data-pharos-target="section:'+key+'"]').first().evaluate(element=>element.tagName==='DETAILS'?element.open:element.getAttribute('aria-expanded')==='true'),false);
  await page.waitForFunction(()=>document.documentElement.dataset.pharosAttentionMissing==='');
  await app.shot('closed-'+key);
 }
});


test('Thinking shares filters, brushing and disclosures across pages and independent browsers',{skip},async t=>{
 const ready='.thinking-dashboard[aria-busy="false"] .thinking-population';
 const app=await open(t,{url:'/analyze?analyze_topic=thinking-detail&analyze_data=model_requests',ready}),{page}=app;
 const control=name=>page.locator('.thinking-controls').getByRole('combobox',{name,exact:true});
 const state=()=>JSON.parse(new URL(page.url()).searchParams.get('thinking'));
 for(const [range,period] of [['90d','day'],['7d','hours'],['1y','week'],['30d','day']]){
  await control('Range').selectOption(range);await app.settled();
  assert.equal(await control('Time Bucket').inputValue(),period);
  assert.equal(app.calls.filter(c=>c.path==='/api/thinking').at(-1).query.get('period'),period);
 }
 const bucketCopy=await open(t,{url:path(page),ready});
 assert.equal(await bucketCopy.page.getByRole('combobox',{name:'Time Bucket',exact:true}).inputValue(),'day');
 await control('Breakdown').selectOption('model_effort');await app.settled();
 // Wheel input over a tall, faceted plot must move the page scroller.
 const wheelChart=page.locator('.thinking-brush').first();await wheelChart.scrollIntoViewIfNeeded();
 const wheelRect=await wheelChart.boundingBox(),beforeWheel=await page.locator('main').evaluate(el=>el.scrollTop);
 await page.mouse.move(wheelRect.x+wheelRect.width*.5,wheelRect.y+wheelRect.height*.5);await page.mouse.wheel(0,240);
 await page.waitForFunction(before=>document.querySelector('main').scrollTop>before,beforeWheel);
 await control('Model').selectOption('gpt-6.1-sol');await app.settled();
 const chart=page.locator('.thinking-brush').first();await chart.scrollIntoViewIfNeeded();const rect=await chart.boundingBox();
 await page.mouse.move(rect.x+rect.width*.3,rect.y+rect.height*.4);await page.mouse.down();await page.mouse.move(rect.x+rect.width*.65,rect.y+rect.height*.4,{steps:8});await page.mouse.up();
 await page.waitForFunction(()=>JSON.parse(new URLSearchParams(location.search).get('thinking')).range==='custom');await app.settled();
 const selected=state();assert.ok(selected.from&&selected.to);assert.ok(['custom','custom-times'].includes(await control('Range').inputValue()));
 for(const view of ['heatmap','histogram','reasoning','wait-box','calendar']){await control('View').selectOption(view);await app.settled();assert.equal(state().from,selected.from);assert.equal(state().to,selected.to);await app.shot(view)}
 const linked=path(page),fresh=await open(t,{url:linked,ready});
 assert.deepEqual(JSON.parse(new URL(fresh.page.url()).searchParams.get('thinking')),state());
 await page.locator('.thinking-links').getByRole('link',{name:'Waiting and Compaction →',exact:true}).click();await page.locator(ready).waitFor();await app.settled();
 assert.equal(state().from,selected.from);
 await page.getByText('Methodology, exclusions and production limits',{exact:true}).click();await app.settled();
 const overview=path(page),copied=await open(t,{url:overview,ready});
 assert.equal(await copied.page.locator('[data-pharos-target="section:thinking:methodology"]').getAttribute('open')!==null,true);
 // An already-open validation section must fetch its evidence on restoration,
 // even though shared accordions notify onOpenChange only for deliberate toggles.
 const validationURL=new URL(overview,'http://localhost');
 validationURL.searchParams.set('disclosures',JSON.stringify({'thinking:validation':true}));
 const validationCopy=await open(t,{url:validationURL.pathname+validationURL.search,ready});
 const validationPanel=validationCopy.page.locator('[data-pharos-target="section:thinking:validation"]');
 await validationPanel.locator('summary').scrollIntoViewIfNeeded();
 await validationPanel.locator('svg').first().waitFor();
 assert.ok(validationCopy.calls.some(call=>call.path==='/api/thinking'&&call.query.get('evidence')==='1'));

 await app.shot('overview');
 const evidenceLoaded=page.waitForResponse(r=>new URL(r.url()).pathname==='/api/query/model_requests/rows-v2');
 await page.locator('.thinking-links').getByRole('link',{name:'Inspect model-request evidence →',exact:true}).click();await evidenceLoaded;await page.locator('.usage-workspace-table').waitFor();await app.settled();
 await page.getByRole('link',{name:'Back to dashboard',exact:true}).click();await page.locator(ready).waitFor();await app.settled();assert.equal(state().model,'gpt-6.1-sol');assert.equal(state().from,selected.from);
 await page.goBack();await page.locator('.usage-workspace-table').waitFor();
 await page.goBack();await page.locator(ready).waitFor();
 await page.goBack();await page.locator(ready).waitFor();assert.equal(state().view,'calendar');
 await page.goForward();await page.locator(ready).waitFor();assert.equal(state().model,'gpt-6.1-sol');
});

// Date arithmetic is covered in node; here verify the shared editor is wired to
// the viewer's local zone, restores in a fresh browser, and keeps API/URL in sync.
test('Thinking custom date editor follows the viewer zone and restores absolute dates',{skip},async t=>{
 const ready='.thinking-dashboard[aria-busy="false"] .thinking-population';
 const thinking={timezone:'Asia/Tokyo',minimum:1000,range:'custom',from:'2026-10-20T06:00:00.000Z',to:'2026-10-21T06:00:00.000Z'};
 const app=await open(t,{url:'/analyze?'+new URLSearchParams({analyze_topic:'thinking-detail',analyze_data:'model_requests',thinking:JSON.stringify(thinking)}),ready}),{page}=app;
 const range=page.locator('.thinking-controls').getByRole('combobox',{name:'Range',exact:true});
 assert.equal(await range.inputValue(),'custom');
 assert.equal(await page.getByLabel('From date',{exact:true}).inputValue(),'2026-10-20');
 assert.equal(await page.getByLabel('Through date',{exact:true}).inputValue(),'2026-10-20');
 await page.getByLabel('Through date',{exact:true}).fill('2026-10-21');await app.settled();
 await page.getByLabel('From date',{exact:true}).fill('2026-10-21');await app.settled();
 const state=JSON.parse(new URL(page.url()).searchParams.get('thinking'));
 assert.equal(state.from,'2026-10-21T06:00:00.000Z');assert.equal(state.to,'2026-10-22T06:00:00.000Z');
 assert.equal(state.timezone,'America/Denver');assert.equal(state.minimum,0);
 const calls=app.calls.filter(c=>c.path==='/api/thinking');
 assert.equal(calls.at(-1).query.get('from'),state.from);assert.equal(calls.at(-1).query.get('to'),state.to);
 assert.equal(calls.at(-1).query.get('timezone'),'America/Denver');assert.equal(calls.at(-1).query.get('minimum'),'0');
 const fresh=await open(t,{url:path(page),ready});
 assert.equal(await fresh.page.getByLabel('From date',{exact:true}).inputValue(),'2026-10-21');
 assert.equal(await fresh.page.getByLabel('Through date',{exact:true}).inputValue(),'2026-10-21');
 await range.selectOption('custom-times');await app.settled();
 assert.equal(await page.getByLabel('From date and time',{exact:true}).inputValue(),'2026-10-21T00:00');
 await page.getByLabel('From date and time',{exact:true}).fill('2026-10-21T12:30');await app.settled();
 assert.equal(JSON.parse(new URL(page.url()).searchParams.get('thinking')).from,'2026-10-21T18:30:00.000Z');
 await app.shot('thinking-local-dates');
});
