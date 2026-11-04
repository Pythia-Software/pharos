import {test} from 'node:test';
import assert from 'node:assert/strict';
import {open,skip,activateHeaderTool} from './harness.mjs';
import {workID,rootConversation,defaultApi,busyStatus,eventStream} from './fixtures.mjs';
import {EMPTY_QUERY,encodeQuery,decodeQueryStrict,loadSchema} from '@pythia-software/query-table-core';
import toolCallsDocument from '../../../schemas/tool_calls.schema.json' with {type:'json'};
import mcpCallsDocument from '../../../schemas/mcp_calls.schema.json' with {type:'json'};
import safetyDocument from '../../../schemas/safety.schema.json' with {type:'json'};
import {answerQuery} from '../support/query-server.mjs';
const path=page=>new URL(page.url()).pathname+new URL(page.url()).search;

test('nested navigation branches restore independently in a fresh browser',{skip},async t=>{
 const viewport={width:1024,height:900};
 const app=await open(t,{url:'/analyze',viewport,ready:'.usage-dashboard .usage-chart'}),{page}=app;
 const navigation=page.getByRole('complementary',{name:'Analyze navigation',exact:true});
 const human=navigation.getByRole('link',{name:'Human Words',exact:true});
 const tokens=navigation.getByRole('link',{name:'Tokens and Cost',exact:true});
 const toggle=async label=>{
  const control=navigation.getByText(label,{exact:true});
  await control.click();await app.settled();
 };
 assert.equal(await human.isVisible(),false);
 await toggle('People');await human.waitFor();
 await toggle('People');await human.waitFor({state:'hidden'});
 assert.equal(await tokens.isVisible(),true);
 await toggle('Dashboards');await tokens.waitFor({state:'hidden'});
 await toggle('Dashboards');await tokens.waitFor();
 assert.equal(await human.isVisible(),false);
 const fresh=await open(t,{url:path(page),viewport,ready:'.usage-dashboard .usage-chart'});
 const restored=fresh.page.getByRole('complementary',{name:'Analyze navigation',exact:true});
 assert.equal(await restored.getByRole('link',{name:'Human Words',exact:true}).isVisible(),false);
 assert.equal(await restored.getByRole('link',{name:'Tokens and Cost',exact:true}).isVisible(),true);
 await restored.getByText('People',{exact:true}).click();
 await restored.getByRole('link',{name:'Human Words',exact:true}).waitFor();
 await fresh.shot('nested-dashboards');
});

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
 await change(()=>dialog.getByRole('button',{name:'Show more',exact:true}).click());
 await change(()=>dialog.getByRole('searchbox',{name:'Search Arguments (sanitized) JSON',exact:true}).fill('needle'));
 await change(()=>dialog.getByRole('combobox',{name:'JSON search mode',exact:true}).selectOption('find'));
 await change(()=>dialog.getByRole('button',{name:'Next JSON match',exact:true}).click());
 await change(()=>dialog.getByRole('button',{name:'Show original',exact:true}).click());capture();
 await change(()=>dialog.getByRole('button',{name:'Show JSON',exact:true}).click());
 await change(()=>dialog.getByRole('searchbox',{name:'Search Arguments (sanitized) JSON',exact:true}).fill(''));
 await change(()=>dialog.getByRole('button',{name:'Close Arguments (sanitized) · JSON',exact:true}).click());capture();
 for(const copy of copies){
  const fresh=await open(t,{url:copy.url,api:fixture.api,ready:'.qt-property-json-open'}),restored=fresh.page.getByRole('dialog',{name:'Arguments (sanitized) · JSON',exact:true});
  assert.equal(await restored.isVisible(),copy.inspection.open);
  assert.deepEqual(fixture.read(fresh.page).itemDetails.jsonInspections[0].state,copy.inspection);
  if(copy.inspection.open){
   assert.equal(await restored.getByRole('searchbox',{name:'Search Arguments (sanitized) JSON',exact:true}).inputValue(),copy.inspection.search);
   assert.equal(await restored.getByRole('combobox',{name:'JSON search mode',exact:true}).inputValue(),copy.inspection.searchMode);
   assert.equal(await restored.getByRole('button',{name:copy.inspection.raw?'Show JSON':'Show original',exact:true}).getAttribute('aria-pressed'),String(copy.inspection.raw));
   // Inspection state is scoped to its record: another row's property starts clean.
   const state=fixture.read(fresh.page);
   await fresh.page.evaluate(url=>window.pharosNavigate(url),fixture.route({...state,itemDetails:{...state.itemDetails,active:{id:'inspect-2',mode:'expand'}}}));
   await restored.waitFor({state:'hidden'});
   await fresh.page.getByRole('button',{name:'Open Arguments (sanitized) in JSON Viewer',exact:true}).click();
   assert.equal(await restored.getByRole('searchbox',{name:'Search Arguments (sanitized) JSON',exact:true}).inputValue(),'');
  }
 }
 await page.evaluate(url=>window.pharosNavigate(url),copies[0].url);await dialog.waitFor();
 await page.goBack();await dialog.waitFor({state:'hidden'});
 await page.goForward();await dialog.waitFor();
 assert.equal(await dialog.getByRole('searchbox',{name:'Search Arguments (sanitized) JSON',exact:true}).inputValue(),'needle');
});

test('semantic JSON-path reveal materializes exact details, reports missing paths, loads records outside the page, and never replays on refresh',{skip},async t=>{
 const fixture=jsonInspectorFixture(),target='detail:'+JSON.stringify({rowId:'inspect-1',field:'arguments_json',path:'$["events"][41]["text"]'});
 const app=await open(t,{url:fixture.route({...fixture.query,itemDetails:{modes:['expand']}},target),api:fixture.api,ready:'.qt-attention-target'}),{page}=app;
 await page.waitForFunction(()=>document.documentElement.dataset.pharosAttentionMissing==='');
 const marker=page.locator('.qt-attention-target').filter({hasText:'needle 41'});await marker.first().waitFor();
 assert.equal(await page.evaluate(()=>document.activeElement===document.body),true);
 let scrolls=await page.evaluate(()=>{window.__attentionScrolls=0;const original=Element.prototype.scrollIntoView;Element.prototype.scrollIntoView=function(...args){window.__attentionScrolls++;return original.apply(this,args)};return window.__attentionScrolls});
 const refreshed=page.waitForResponse(response=>new URL(response.url()).pathname==='/api/query/mcp_calls/rows-v2');
 await page.getByRole('button',{name:'Run Now'}).first().click();await refreshed;await app.settled();
 assert.equal(await page.evaluate(()=>window.__attentionScrolls),scrolls);
 const state=fixture.read(page);assert.equal(state.offset,fixture.query.offset);assert.deepEqual(state.where,fixture.query.where);
 // A missing JSON path reports unavailable without changing the query population.
 const missing='detail:'+JSON.stringify({rowId:'inspect-1',field:'arguments_json',path:'$["deleted"]'});
 await page.evaluate(url=>window.pharosNavigate(url),fixture.route(fixture.query,missing));
 await page.waitForFunction(target=>document.documentElement.dataset.pharosAttentionMissing===target&&document.documentElement.dataset.pharosAttentionPending==='',missing);
 await app.settled();
 assert.equal(await page.locator('.qt-attention-target').count(),0);
 assert.deepEqual(fixture.read(page).where,fixture.query.where);
 // A record outside the result page loads without broadening the page.
 const outside='detail:'+JSON.stringify({rowId:'inspect-2',field:'arguments_json',path:'$["other"]'});
 const narrow={...fixture.query,limit:1,itemDetails:{modes:['expand','modal']}};
 await page.evaluate(url=>window.pharosNavigate(url),fixture.route(narrow,outside));
 await page.waitForFunction(()=>document.documentElement.dataset.pharosAttentionMissing==='');
 await page.locator('.qt-attention-target').first().waitFor();
 const restored=fixture.read(page);
 assert.equal(restored.itemDetails.active.id,'inspect-2');assert.equal(restored.limit,1);assert.equal(restored.offset,0);assert.deepEqual(restored.where,narrow.where);
 assert.equal(await page.evaluate(()=>document.activeElement===document.body),true);
 await page.evaluate(()=>window.pharosNavigate('/cards'));
 await page.locator('.card-library-page').waitFor();assert.equal(await page.locator('.qt-attention-target:visible').count(),0);
});

test('automatic oversized query references resolve in a fresh browser with no local cache, including JSON inspection state',{skip},async t=>{
 const fixture=jsonInspectorFixture(),state={version:1,open:true,search:'needle',searchMode:'find',raw:false,collapsedPaths:[],expandedBuckets:['$["events"]:bucket:20'],visibleEntryLimit:400,searchOverrides:[]};
 // Distinct nonrepeating values avoid a highly compressible artificial query.
 const terms=Array.from({length:70},(_,i)=>({field:'tool_name',op:'!=',value:`unused-${i}-${Array.from({length:48},(_,n)=>String.fromCharCode(33+(i*31+n*17+i*n)%90)).join('')}`}));
 const query={...fixture.query,where:[...fixture.query.where,...terms],itemDetails:{...fixture.query.itemDetails,jsonInspections:[{dataset:'pharos_mcp_calls',rowId:'inspect-1',field:'arguments_json',state}]}};
 const first=await open(t,{url:fixture.route(query),api:fixture.api,ready:'.qt-json-inspector'});
 await first.page.waitForFunction(()=>new URLSearchParams(location.search).get('q_mcp_calls')?.startsWith('@'));
 const fresh=await open(t,{url:path(first.page),api:fixture.api,ready:'.qt-json-inspector'});
 await fresh.settled();
 assert.equal(await fresh.page.locator('.query-table-error').count(),0);
 assert.ok(fresh.calls.some(call=>call.path==='/api/query-snapshots'&&call.method==='GET'));
 assert.equal(await fresh.page.getByRole('searchbox',{name:'Search Arguments (sanitized) JSON',exact:true}).inputValue(),'needle');
 assert.deepEqual(fixture.read(fresh.page).itemDetails.jsonInspections[0].state,state);
});

test('conversation result links restore their session, matched message and inline agent without taking focus',{skip},async t=>{
 const message=(id,role,text)=>({id,native_id:id,role,kind:'message',text});
 const api={
  'POST /api/query/library/:operation':request=>{
   const result=defaultApi['* /api/query/:dataset/:operation']({...request,params:{dataset:'library',operation:request.params.operation}});
   return !result?.rows?result:{...result,total:1,rows:result.rows.slice(0,1).map(row=>({...row,id:workID}))};
  },
  'GET /api/work/:id':request=>({...defaultApi['GET /api/work/:id'](request),conversations:[{id:'first',provider:'codex'},{id:'second',provider:'codex'}]}),
  'GET /api/work/:id/conversations/:conversation':({params})=>({id:params.conversation,workspace_id:workID,provider:'codex',messages:params.conversation==='first'?[message('first-prompt','user','First session prompt')]:[
   message('second-prompt','user','Second session prompt'),
   message('second-note','assistant','Intermediate note'),
   message('shared-envelope','assistant',JSON.stringify({content:[
    {type:'tool_use',id:'configuration',name:'Task',input:{subagent_type:'Configuration auditor',prompt:'Check configuration'}},
    {type:'tool_use',id:'timing',name:'Agent',input:{subagent_type:'Timing auditor',prompt:'Check timing'}},
   ]})),
   {...message('configuration-result','tool',JSON.stringify({content:'Configuration checked.'})),kind:'delegation_result',call_id:'configuration'},
   {...message('timing-result','tool',JSON.stringify({content:'Timing checked.'})),kind:'delegation_result',call_id:'timing'},
   message('second-reply','assistant','Final reply'),
  ]}),
 };
 // A Search match's message within the turn (an intermediate reply) restores with it.
 const state={[`${workID}/browse`]:true,[`${workID}/turn`]:'second-prompt',[`${workID}/message`]:'second-note'};
 const disclosures={[`search:${workID}:conversation:first`]:true,[`search:${workID}:conversation:second`]:true};
 const app=await open(t,{url:'/library?'+new URLSearchParams({view:'conversation',search_cards:JSON.stringify(state),disclosures:JSON.stringify(disclosures)}),api,ready:'.conversation-result-card'});
 const preview=app.page.getByRole('region',{name:'Selected turn preview',exact:true});
 await preview.getByText('Second session prompt',{exact:true}).waitFor();
 await preview.getByText('Intermediate note',{exact:true}).waitFor();
 await preview.getByText('Final reply',{exact:true}).waitFor();
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

test('dynamic message reveal materializes its target without taking keyboard focus, and linked attention opens once', {skip},async t=>{
 const messages=Array.from({length:45},(_,turn)=>['user','assistant'].map(role=>({id:`lazy-${turn}-${role}`,native_id:`lazy-${turn}-${role}`,role,kind:'message',text:`Turn ${turn} ${role}`,created_at:new Date(1760000000000+turn*1000).toISOString()}))).flat();
 const api={'GET /api/work/:id/conversations/:conversation':request=>({...defaultApi['GET /api/work/:id/conversations/:conversation'](request),messages})};
 const opened=await open(t,{api,url:`/work/${workID}?conversation=${rootConversation}&reveal=message:lazy-44-assistant&highlight=message:lazy-0-user`,ready:'.conversation-panel'}),{page}=opened;
 await page.waitForFunction(()=>document.querySelector('[data-message-id="lazy-44-assistant"]')?.classList.contains('pharos-reveal'));
 assert.equal(await page.locator('[data-message-id="lazy-44-assistant"]').first().evaluate(element=>element===document.activeElement),false);
 await page.waitForFunction(()=>!!document.querySelector('[data-message-id="lazy-0-user"].pharos-highlight'));
 await page.evaluate(()=>window.dispatchEvent(new Event('pharos:usage-refresh')));
 await opened.settled();
 assert.equal(await page.locator('[data-message-id="lazy-44-assistant"]').first().evaluate(element=>element===document.activeElement),false);
 // Linked attention opens its disclosure once; the user can then collapse it.
 await page.evaluate(()=>{
  const details=document.querySelector('[data-message-id="lazy-0-user"]').closest('details');
  if(!details?.open)throw Error('Attention did not open its content');
  window.linkedDisclosure=details;details.querySelector('summary').click();
 });
 await opened.settled();
 await page.evaluate(()=>window.pharosView.refresh());await opened.settled();
 assert.equal(await page.evaluate(()=>window.linkedDisclosure.open),false);
 assert.ok(JSON.parse(new URL(page.url()).searchParams.get('reader_collapsed')||'[]').length>0);
});

test('literal text reveal opens page Find on the reader and reveals a match without focusing its input', {skip},async t=>{
 const opened=await open(t,{url:`/work/${workID}?conversation=${rootConversation}&reveal=text%3ATurn%201`,ready:'.pharos-find'});
 await opened.page.waitForFunction(()=>[...(CSS.highlights.get('pharos-landing')||[])][0]?.toString()==='Turn 1');
 const params=new URL(opened.page.url()).searchParams;
 assert.equal(params.get('find'),'Turn 1');assert.equal(params.has('reveal'),false);
 assert.equal(await opened.page.locator('.pharos-find').evaluate(element=>element.contains(document.activeElement)),false);
 assert.equal(opened.calls.filter(call=>call.path==='/api/search/text').length,0,'page Find searches the loaded transcript');
 await opened.page.getByRole('button',{name:'Close Find (Escape)',exact:true}).click();
 await opened.page.waitForFunction(()=>!document.querySelector('.pharos-find')&&!new URLSearchParams(location.search).has('find'));
});

test('a revealed literal inside unrendered content lands there even when another literal is shown', {skip},async t=>{
 const {page}=await open(t,{url:`/work/${workID}?conversation=${rootConversation}&highlight=text%3ATurn%201&reveal=text%3Acase_7_with`,ready:'.pharos-find'});
 await page.waitForFunction(()=>[...(CSS.highlights.get('pharos-landing')||[])][0]?.toString()==='case_7_with');
});

test('page Find counts matches in unrendered tool details, then opens and marks the path to one', {skip},async t=>{
 const app=await open(t,{url:`/work/${workID}?conversation=${rootConversation}&find=case_7_with`,ready:'[data-pharos-find="container"]'}),{page}=app;
 assert.equal(await page.evaluate(()=>CSS.highlights.get('pharos-matches')?.size??0),0,'no match is rendered yet');
 assert.match(await page.locator('.pharos-find-status').textContent(),/collapsed/);
 await page.keyboard.press(process.platform==='darwin'?'Meta+f':'Control+f');
 await page.waitForFunction(()=>document.activeElement===document.querySelector('.pharos-find input'));
 await page.keyboard.press('Enter');
 await page.waitForFunction(()=>[...(CSS.highlights.get('pharos-landing')||[])][0]?.toString()==='case_7_with');
 assert.equal(await page.evaluate(()=>{const range=[...CSS.highlights.get('pharos-landing')][0];return range.startContainer.parentElement.closest('details.tool-event')?.open}),true);
 assert.ok(await page.locator('[data-pharos-find-trail]').count()>0,'the opened path is marked');
 assert.equal(await page.evaluate(()=>document.activeElement===document.querySelector('.pharos-find input')),true,'stepping keeps focus in Find');
 assert.match(new URL(page.url()).searchParams.get('reader_content')??'',/loaded:/,'materialized content is recorded for copied URLs');
 // Escape still closes Find after a click moves focus to one of its buttons.
 await page.getByRole('button',{name:'Next match (Enter)',exact:true}).click();
 await page.keyboard.press('Escape');
 await page.waitForFunction(()=>!document.querySelector('[data-pharos-find-trail],[data-pharos-find]'));
});

test('section reveal opens content ancestors and closing Find removes its text attention', {skip},async t=>{
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
 await opened.page.waitForFunction(()=>new URLSearchParams(location.search).get('find')==='Attention fixture'&&!new URLSearchParams(location.search).has('reveal'));
 await opened.page.locator('.pharos-find input').press('Escape');
 await opened.page.waitForFunction(()=>!new URLSearchParams(location.search).has('find'));
});


test('multiple literal attention targets are highlighted with a single reveal landing', {skip},async t=>{
 const opened=await open(t,{url:'/cards?card_topic=all',ready:'.card-library-page'}),{page}=opened;
 await opened.page.evaluate(()=>{
  const content=document.createElement('p');content.textContent='Alpha fixture — Beta fixture';document.querySelector('.view.active').append(content);
  const url=new URL(location.href);url.searchParams.append('highlight','text:Alpha fixture');url.searchParams.append('highlight','text:Beta fixture');url.searchParams.set('reveal','text:Beta fixture');history.replaceState(history.state,'',url);window.dispatchEvent(new Event('pharos:uri-changed'));
 });
 await page.waitForFunction(()=>{
  const marks=[...(CSS.highlights.get('pharos-matches')||[])].map(range=>range.toString());
  return marks.includes('Alpha fixture')&&marks.includes('Beta fixture')&&[...(CSS.highlights.get('pharos-landing')||[])][0]?.toString()==='Beta fixture';
 });
 assert.equal(await opened.page.locator('.pharos-find input').evaluate(input=>input===document.activeElement),false);
});


test('page Find expands every collapsed match and can be dragged out of the way',{skip},async t=>{
 const app=await open(t,{url:`/work/${workID}?conversation=${rootConversation}&find=case_7_with`,ready:'[data-pharos-find="container"]'}),{page}=app;
 await page.getByRole('button',{name:'Open every collapsed section holding a match',exact:true}).click();
 await page.waitForFunction(()=>!document.querySelector('[data-pharos-find="container"]')&&(CSS.highlights.get('pharos-matches')?.size??0)===4);
 assert.equal(await page.locator('.pharos-find-expand').isHidden(),true);
 const grip=page.getByRole('button',{name:/^Move Find/});
 const before=await page.locator('.pharos-find').boundingBox(),handle=await grip.boundingBox();
 await page.mouse.move(handle.x+handle.width/2,handle.y+handle.height/2);await page.mouse.down();
 await page.mouse.move(handle.x-300,handle.y+200,{steps:4});await page.mouse.up();
 const after=await page.locator('.pharos-find').boundingBox();
 assert.ok(Math.abs(after.x-(before.x-300))<40&&after.y>before.y+150,'the bar follows the grip');
 await grip.press('Home');
 await page.waitForFunction(top=>Math.abs(document.querySelector('.pharos-find').getBoundingClientRect().top-top)<2,before.y);
});

test('page Find follows Back and Forward and closes on its own history entry',{skip},async t=>{
 const app=await open(t,{url:`/work/${workID}?conversation=${rootConversation}`,ready:'.conversation-panel'}),{page}=app;
 await page.evaluate(()=>{const url=new URL(location.href);url.searchParams.set('find','Turn');url.searchParams.set('find_case','1');window.pharosNavigate(url.pathname+url.search)});
 await page.locator('.pharos-find').waitFor();
 assert.equal(await page.locator('.pharos-find button[title="Match case"]').getAttribute('aria-pressed'),'true');
 await page.goBack();
 await page.locator('.pharos-find').waitFor({state:'detached'});
 await page.goForward();
 await page.waitForFunction(()=>document.querySelector('.pharos-find input')?.value==='Turn'&&(CSS.highlights.get('pharos-matches')?.size??0)>0);
 // A focused input still shows a term restored from the URL.
 await page.locator('.pharos-find input').focus();
 await page.evaluate(()=>{const url=new URL(location.href);url.searchParams.set('find','Session');window.pharosNavigate(url.pathname+url.search)});
 await page.waitForFunction(()=>document.querySelector('.pharos-find input')?.value==='Session');
});

test('reader disclosure history restores empty overrides to native defaults, and same-conversation links restore controls without reloading',{skip},async t=>{
 const app=await open(t,{url:`/work/${workID}?conversation=${rootConversation}&reader_expanded=%5B%5D&reader_collapsed=%5B%5D`,ready:'.transcript-filter'}),{page}=app;
 const details=page.locator('details[data-pharos-disclosure]'),baseline=await details.evaluateAll(items=>items.map(item=>({id:item.dataset.pharosDisclosure,open:item.open})));
 const opened=baseline.find(item=>item.open),closed=baseline.find(item=>!item.open);assert.ok(opened&&closed);
 await page.evaluate(({opened,closed})=>{const url=new URL(location.href);url.searchParams.set('reader_expanded',JSON.stringify([closed.id]));url.searchParams.set('reader_collapsed',JSON.stringify([opened.id]));window.pharosNavigate(url.pathname+url.search);},{opened,closed});
 const swapped=()=>page.waitForFunction(({opened,closed})=>{const details=[...document.querySelectorAll('details[data-pharos-disclosure]')];return details.find(item=>item.dataset.pharosDisclosure===opened.id)?.open===false&&details.find(item=>item.dataset.pharosDisclosure===closed.id)?.open===true;},{opened,closed});
 await swapped();
 await page.goBack();
 await page.waitForFunction(baseline=>baseline.every(expected=>[...document.querySelectorAll('details[data-pharos-disclosure]')].find(item=>item.dataset.pharosDisclosure===expected.id)?.open===expected.open),baseline);
 await app.settled();
 const params=new URL(page.url()).searchParams;
 for(const name of ['reader_expanded','reader_collapsed'])assert.equal(params.get(name),'[]');
 await page.goForward();await swapped();await app.settled();
 // A same-conversation link restores the category and disclosures in place.
 await page.evaluate(()=>{window.__sameDocument=true});
 const turn=page.locator('.turn').first(),id=await turn.getAttribute('data-pharos-disclosure');
 assert.ok(id);
 await page.evaluate(id=>{const url=new URL(location.href);url.searchParams.delete('reader_filter');url.searchParams.delete('reader_expanded');url.searchParams.set('reader_filter_level','primary');url.searchParams.set('reader_collapsed',JSON.stringify([id]));window.pharosNavigate(url.pathname+url.search);},id);
 await page.waitForFunction(()=>document.querySelector('.transcript-levels button[aria-pressed="true"]')?.textContent==='Primary only'&&!document.querySelector('.turn').open);
 await app.settled();
 assert.equal(await page.getByRole('group',{name:'Show in transcript',exact:true}).getByRole('button',{name:'Primary only',exact:true}).getAttribute('aria-pressed'),'true');
 assert.deepEqual(JSON.parse(new URL(page.url()).searchParams.get('reader_collapsed')),[id]);
 assert.equal(await page.evaluate(()=>window.__sameDocument),true,'the reader did not reload the document');
});

test('page Find becomes idle and ignores its own and the status UI mutations',{skip},async t=>{
 const app=await open(t,{url:'/cards?reveal=text:Card&highlight=page',ready:'.pharos-find'}),{page}=app;
 await app.settled();
 await page.evaluate(()=>{
  const style=window.getComputedStyle.bind(window);let walks=0;
  window.getComputedStyle=(...args)=>{walks++;return style(...args)};
  window.findWalks=()=>walks;window.findPrevious=-1;window.findStable=0;
  document.querySelector('.pharos-find-status').textContent='Synthetic status update';
  document.querySelector('.pharos-attention-status span').textContent='Synthetic status update';
 });
 await page.waitForFunction(()=>{
  const current=window.findWalks();window.findStable=current===window.findPrevious?window.findStable+1:0;window.findPrevious=current;
  return window.findStable>=4;
 });
});

test('header navigation clears source attention and explicit destination links retain their targets',{skip},async t=>{
 const params=new URLSearchParams({conversation:rootConversation,highlight:'text:Turn',reveal:'page',nav_collapsed:'0'});
 const app=await open(t,{url:`/work/${workID}?${params}`,ready:'.pharos-find'}),{page}=app;
 await page.locator('header button[data-view="library"]').click();await page.locator('#library.active').waitFor();await app.settled();
 const destination=new URL(page.url()).searchParams;
 assert.deepEqual(destination.getAll('highlight'),[]);assert.equal(destination.has('reveal'),false);assert.equal(destination.get('nav_collapsed'),'0');
 assert.equal(destination.has('find'),false);assert.equal(await page.locator('.pharos-find').count(),0);
 await page.goBack();await page.waitForFunction(()=>new URLSearchParams(location.search).get('reveal')==='page');
 assert.equal(new URL(page.url()).searchParams.get('find'),'Turn','the text target opened Find on its own page');
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

test('page Find stepping moves the landing highlight without a server search',{skip},async t=>{
 const app=await open(t,{url:`/work/${workID}?conversation=${rootConversation}&highlight=text:please look`,ready:'.pharos-find'}),{page}=app;
 await page.waitForFunction(()=>(CSS.highlights.get('pharos-matches')?.size??0)>2);
 const landing=()=>page.evaluate(()=>{const range=[...(CSS.highlights.get('pharos-landing')||[])][0];return range?range.startContainer.parentElement.closest('[data-message-id]')?.dataset.messageId:null});
 await page.getByRole('button',{name:'Next match (Enter)',exact:true}).click();
 await page.waitForFunction(()=>(CSS.highlights.get('pharos-landing')?.size??0)===1);
 const first=await landing();
 await page.getByRole('button',{name:'Next match (Enter)',exact:true}).click();
 await page.waitForFunction(first=>{const range=[...(CSS.highlights.get('pharos-landing')||[])][0];return range&&range.startContainer.parentElement.closest('[data-message-id]')?.dataset.messageId!==first},first);
 await page.getByRole('button',{name:'Previous match (Shift+Enter)',exact:true}).click();
 await page.waitForFunction(first=>{const range=[...(CSS.highlights.get('pharos-landing')||[])][0];return range?.startContainer.parentElement.closest('[data-message-id]')?.dataset.messageId===first},first);
 assert.equal(app.calls.filter(call=>call.path==='/api/search/text').length,0);
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

test('feedback inbox log accordions use the same URL syntax without the application shell',{skip},async t=>{
 const {readFileSync}=await import('node:fs');
 const {repo}=await import('./harness.mjs');
 const id='00000000-0000-4000-8000-000000000001';
 const entry={version:1,id,application:{name:'Fixture app'},kind:'feedback',comment:'A report',annotations:[],diagnostics:{browser:'Fixture log'},received_at:new Date().toISOString(),tags:[],revision:'1'};
 const api={'GET /api/submissions':{submissions:[{...entry,annotation_count:0,screenshot_count:0,has_diagnostics:true}]},'GET /api/submissions/:id':entry};
 const boot=async url=>{
  const app=await open(t,{url:'/library?view=table',api,ready:'#queryTableLibrary .qt-row'});
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

test('opening work and returning to Search keeps the native Library search URL',{skip},async t=>{
 const draft={editor:'visual',match:'literal',mode:'advanced',text:'failure',root:{kind:'group',join:'all',children:[],exclude:false},scope:{surface:'prose',roles:[],kinds:[],repository:'',provider:'',source:'',from:'',to:'',conversation_id:'',workspace_id:''},fuzzy:false,caseSensitive:false,separators:false};
 const token=JSON.stringify(draft),query={...EMPTY_QUERY,select:[{field:'title'}],hostState:{version:1,params:{text_builder:[token],view:['table']}},limit:5,fitToScreen:false};
 const app=await open(t,{url:'/library?'+new URLSearchParams({q_library:encodeQuery(query),text_builder:token}),ready:'#queryTableLibrary .qt-row'}),{page}=app;
 await app.settled();
 const original=decodeQueryStrict(new URL(page.url()).searchParams.get('q_library'));
 // site_link composes view search into the native host state; every request carries it.
 assert.deepEqual(JSON.parse(new URL(page.url()).searchParams.get('text_builder')),JSON.parse(original.hostState.params.text_builder[0]));
 const requests=app.calls.filter(call=>call.method==='POST'&&call.path.startsWith('/api/query/library/'));
 assert.ok(requests.length>0);
 for(const request of requests)assert.ok(request.query.has('find')||request.query.has('find_where'),`${request.path} dropped the applied search`);
 await page.evaluate(id=>window.pharosOpenDetail(id),workID);
 await page.waitForFunction(()=>location.pathname.startsWith('/work/')&&!!document.querySelector('#detail.active'));
 assert.equal(JSON.parse(new URL(page.url()).searchParams.get('text_builder')).text,'failure');
 await page.evaluate(()=>document.getElementById('back').click());
 await page.waitForFunction(()=>location.pathname==='/library');await app.settled();
 const restored=new URL(page.url()).searchParams;
 assert.deepEqual(decodeQueryStrict(restored.get('q_library')),original);
 assert.equal(JSON.parse(restored.get('text_builder')).text,'failure');
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
 // A row identity highlights its rendered row without opening a modal.
 await page.evaluate(()=>{const url=new URL(location.href);url.searchParams.set('reveal','row:payload-1');history.replaceState(history.state,'',url);window.dispatchEvent(new Event('pharos:uri-changed'));});
 await page.waitForFunction(()=>!!document.querySelector('tr.pharos-reveal'));
 assert.equal(await page.locator('[role="dialog"]:visible').count(),0);
});

test('accordion links restore capture/index diagnostics in fresh browsers and history, and reveal a closed accordion without opening it',{skip},async t=>{
 const keys=['sync:separate','sync:repairs','sync:diagnostics','sync:forecast:capture-forecast'],ready='#syncDiagnosticTables details';
 const route=entries=>'/settings/sync?'+new URLSearchParams({disclosures:JSON.stringify(entries)});
 const entries=Object.fromEntries(keys.map(key=>[key,true])),defaults=Object.fromEntries(keys.map(key=>[key,false]));
 const app=await open(t,{url:route(entries),ready}),{page}=app;
 const state=(browser,expected)=>browser.waitForFunction(expected=>Object.entries(expected).every(([key,open])=>{
  const element=[...document.querySelectorAll('[data-view-key],[data-pharos-target]')].find(element=>element.dataset.viewKey===key||element.dataset.pharosTarget==='section:'+key);
  return element&&(element.tagName==='DETAILS'?element.open:element.getAttribute('aria-expanded')==='true')===open;
 }),expected);
 await state(page,entries);
 const first=page.locator('[data-view-key="'+keys[0]+'"]');
 if(await first.evaluate(element=>element.tagName==='DETAILS'))await first.locator('summary').first().click();else await first.click();
 await state(page,{...entries,[keys[0]]:false});
 await page.waitForFunction(key=>JSON.parse(new URLSearchParams(location.search).get('disclosures')||'{}')[key]===false,keys[0]);await app.settled();
 const fresh=await open(t,{url:path(page),ready});
 await state(fresh.page,{...entries,[keys[0]]:false});
 await page.evaluate(url=>window.pharosNavigate(url),route({}));
 await state(page,defaults);
 await page.goBack();await state(page,{...entries,[keys[0]]:false});
 await page.goForward();await state(page,defaults);
 assert.equal(app.calls.some(call=>call.method!=='GET'&&/^\/api\/(background\/(settings|run)|sync\/|cloud-backup\/(schedule|start))/.test(call.path)),false,'restoring sections must not run work or save settings');
 // Revealing a closed accordion highlights its summary without overriding explicit false.
 const closed={'sync:separate':false,'sync:diagnostics':false};
 await page.evaluate(url=>window.pharosNavigate(url),route(closed)+'&'+new URLSearchParams({reveal:'section:sync:diagnostics'}));
 const target=page.locator('[data-pharos-target="section:sync:diagnostics"].pharos-reveal');await target.waitFor({state:'visible'});
 await page.waitForFunction(()=>document.documentElement.dataset.pharosAttentionMissing==='');
 assert.equal(await target.evaluate(element=>element.contains(document.activeElement)),false,'revealing a closed section does not move focus');
 assert.equal(await page.locator('[data-view-key="sync:diagnostics"],[data-pharos-target="section:sync:diagnostics"]').first().evaluate(element=>element.tagName==='DETAILS'?element.open:element.getAttribute('aria-expanded')==='true'),false);
});

test('drive-status accordion links restore the overlay without moving focus and survive polling',{skip},async t=>{
 const entries={'status:cadence:frequent':false,'status:cadence:hourly':true};
 const api={'GET /api/status':busyStatus,'GET /api/status/stream':eventStream(busyStatus)};
 const target='section:status:cadence:hourly';
 const app=await open(t,{url:'/library?'+new URLSearchParams({overlay:'drive',disclosures:JSON.stringify(entries),reveal:target}),api,ready:'#pharosStatusPanel'}),{page}=app;
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
});

test('Quick Stats keeps URL overlay restoration and reveals its configuration',{skip},async t=>{
 const app=await open(t,{url:'/settings',viewport:{width:1485,height:791},ready:'#settings.active'}),{page}=app;
 await activateHeaderTool(page,'usageSummaryToggle');
 await page.getByRole('dialog',{name:'Quick Stats',exact:true}).waitFor();
 assert.equal(new URL(page.url()).searchParams.get('overlay'),'usage');
 const fresh=await open(t,{url:path(page),ready:'#usageSummary:not([hidden])'});
 assert.equal(await fresh.page.getByRole('dialog',{name:'Quick Stats',exact:true}).isVisible(),true);
 await fresh.page.keyboard.press('Escape');
 await fresh.page.waitForFunction(()=>document.getElementById('usageSummary').hidden&&new URLSearchParams(location.search).get('overlay')==='none');
 await page.getByRole('button',{name:'Configure Quick Stats',exact:true}).click();
 await page.locator('#quickStatsOptions:not([hidden])').waitFor();await app.settled();
 await page.waitForFunction(()=>{const panel=document.getElementById('quickStatsSettings'),rect=panel.getBoundingClientRect();return panel.classList.contains('pharos-reveal')&&rect.top>=document.querySelector('body>header').getBoundingClientRect().bottom&&rect.top<innerHeight;});
 assert.equal(await page.locator('#usageSummary').isVisible(),false);
 assert.equal(await page.locator('#quickStatsSettings').evaluate(panel=>panel.contains(document.activeElement)),false,'reveal leaves keyboard focus alone');
});

test('Thinking shares filters, brushing and disclosures across pages and independent browsers',{skip},async t=>{
 const ready='.thinking-dashboard[aria-busy="false"] .thinking-population';
 const app=await open(t,{url:'/analyze?analyze_topic=thinking-detail&analyze_data=model_requests',ready}),{page}=app;
 const control=name=>page.locator('.thinking-controls').getByRole('combobox',{name,exact:true});
 const state=()=>JSON.parse(new URL(page.url()).searchParams.get('thinking'));
 const topic=()=>new URL(page.url()).searchParams.get('analyze_topic');
 await control('Model').selectOption('gpt-6.1-sol');await app.settled();
 const chart=page.locator('.thinking-brush').first();await chart.scrollIntoViewIfNeeded();const rect=await chart.boundingBox();
 await page.mouse.move(rect.x+rect.width*.3,rect.y+rect.height*.4);await page.mouse.down();await page.mouse.move(rect.x+rect.width*.65,rect.y+rect.height*.4,{steps:8});await page.mouse.up();
 await page.waitForFunction(()=>JSON.parse(new URLSearchParams(location.search).get('thinking')).range==='custom');await app.settled();
 const selected=state();assert.ok(selected.from&&selected.to);assert.ok(['custom','custom-times'].includes(await control('Range').inputValue()));
 const fresh=await open(t,{url:path(page),ready});
 assert.deepEqual(JSON.parse(new URL(fresh.page.url()).searchParams.get('thinking')),selected);
 const detailTopic=topic();
 await page.locator('.thinking-links').getByRole('link',{name:'Waiting and Compaction →',exact:true}).click();await page.locator(ready).waitFor();await app.settled();
 const overviewTopic=topic();assert.notEqual(overviewTopic,detailTopic);
 assert.equal(state().from,selected.from);
 await page.getByText('Methodology, exclusions and production limits',{exact:true}).click();await app.settled();
 // An already-open validation section must fetch its evidence on restoration,
 // even though shared accordions notify onOpenChange only for deliberate toggles.
 const overview=new URL(path(page),'http://localhost');
 const disclosures=JSON.parse(overview.searchParams.get('disclosures')||'{}');
 assert.equal(disclosures['thinking:methodology'],true);
 overview.searchParams.set('disclosures',JSON.stringify({...disclosures,'thinking:validation':true}));
 const copied=await open(t,{url:overview.pathname+overview.search,ready});
 assert.equal(await copied.page.locator('[data-pharos-target="section:thinking:methodology"]').getAttribute('open')!==null,true);
 const validationPanel=copied.page.locator('[data-pharos-target="section:thinking:validation"]');
 await validationPanel.locator('summary').scrollIntoViewIfNeeded();
 await validationPanel.locator('svg').first().waitFor();
 assert.ok(copied.calls.some(call=>call.path==='/api/thinking'&&call.query.get('evidence')==='1'));
 await page.goBack();await page.waitForFunction(topic=>new URLSearchParams(location.search).get('analyze_topic')===topic,detailTopic);await page.locator(ready).waitFor();
 assert.equal(state().model,'gpt-6.1-sol');assert.equal(state().from,selected.from);
 await page.goForward();await page.waitForFunction(topic=>new URLSearchParams(location.search).get('analyze_topic')===topic,overviewTopic);await page.locator(ready).waitFor();
 assert.equal(state().from,selected.from);
});

// Date arithmetic is covered in node; here verify the shared editor is wired to
// the viewer's local zone and keeps API/URL in sync.
test('Thinking custom date editor follows the viewer zone and writes absolute dates',{skip},async t=>{
 const ready='.thinking-dashboard[aria-busy="false"] .thinking-population';
 const thinking={timezone:'Asia/Tokyo',minimum:1000,range:'custom',from:'2026-10-20T06:00:00.000Z',to:'2026-10-21T06:00:00.000Z'};
 const app=await open(t,{url:'/analyze?'+new URLSearchParams({analyze_topic:'thinking-detail',analyze_data:'model_requests',thinking:JSON.stringify(thinking)}),ready}),{page}=app;
 const range=page.locator('.thinking-controls').getByRole('combobox',{name:'Range',exact:true});
 assert.equal(await range.inputValue(),'custom');
 assert.equal(await page.getByLabel('From date',{exact:true}).inputValue(),'2026-10-20');
 assert.equal(await page.getByLabel('Through date',{exact:true}).inputValue(),'2026-10-20');
 await page.getByLabel('Through date',{exact:true}).fill('2026-10-21');await app.settled();
 const state=JSON.parse(new URL(page.url()).searchParams.get('thinking'));
 assert.equal(state.from,'2026-10-20T06:00:00.000Z');assert.equal(state.to,'2026-10-22T06:00:00.000Z');
 assert.equal(state.timezone,'America/Denver');assert.equal(state.minimum,0);
 const calls=app.calls.filter(c=>c.path==='/api/thinking');
 assert.equal(calls.at(-1).query.get('from'),state.from);assert.equal(calls.at(-1).query.get('to'),state.to);
 assert.equal(calls.at(-1).query.get('timezone'),'America/Denver');assert.equal(calls.at(-1).query.get('minimum'),'0');
 await range.selectOption('custom-times');await app.settled();
 assert.equal(await page.getByLabel('From date and time',{exact:true}).inputValue(),'2026-10-20T00:00');
 await page.getByLabel('From date and time',{exact:true}).fill('2026-10-21T12:30');await app.settled();
 assert.equal(JSON.parse(new URL(page.url()).searchParams.get('thinking')).from,'2026-10-21T18:30:00.000Z');
});

test('Risky actions table edits are Back/Forward steps that restore the table',{skip},async t=>{
 const schema=loadSchema(safetyDocument),now=Date.now();
 const rows=['/srv/a','/srv/b','/srv/c'].map((subject,i)=>({id:`safety_${i}`,rule:'permissions',state:'open',title:'Permissions broadened',subject,command:`chmod 700 ${subject}`,target:'repo:r',target_label:'explo',calls:1,last_seen_at:new Date(now-i*86400000).toISOString()}));
 const api={'* /api/query/safety/:operation':({params,body,query})=>answerQuery(params.operation,body,rows,schema,{now,id:query.get('id')})};
 const query={...EMPTY_QUERY,where:[{field:'rule',op:'=',value:'permissions'}],orderBy:[{field:'subject',dir:'asc'}],select:[{field:'subject'},{field:'command'}]};
 const app=await open(t,{url:'/findings?'+new URLSearchParams({optimize_area:'safety',view:'table',q_safety:encodeQuery(query)}),api,ready:'.safety-table .qt-row'}),{page}=app;
 const read=()=>decodeQueryStrict(new URL(page.url()).searchParams.get('q_safety'));
 const first=()=>page.locator('.safety-table .qt-row').first().textContent();
 assert.match(await first(),/\/srv\/a/);
 const before=new URL(page.url()).searchParams.get('q_safety');
 await page.locator('.safety-table th[data-qt-field="subject"]').click();await page.getByRole('menuitem',{name:/desc/i}).first().click();
 await page.waitForFunction(before=>new URLSearchParams(location.search).get('q_safety')!==before,before);await app.settled();
 assert.deepEqual(read().orderBy,[{field:'subject',dir:'desc'}]);
 await page.waitForFunction(()=>/\/srv\/c/.test(document.querySelector('.safety-table .qt-row')?.textContent??''));
 await page.goBack();
 await page.waitForFunction(()=>/\/srv\/a/.test(document.querySelector('.safety-table .qt-row')?.textContent??''));
 assert.deepEqual(read().orderBy,[{field:'subject',dir:'asc'}]);
 assert.equal(new URL(page.url()).pathname,'/findings','Back stays on the table');
 await page.goForward();
 await page.waitForFunction(()=>/\/srv\/c/.test(document.querySelector('.safety-table .qt-row')?.textContent??''));
 assert.deepEqual(read().orderBy,[{field:'subject',dir:'desc'}]);
});
