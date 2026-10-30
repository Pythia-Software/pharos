import {test} from 'node:test';
import assert from 'node:assert/strict';
import {open,skip} from './harness.mjs';
import {workID,rootConversation,defaultApi} from './fixtures.mjs';
import {EMPTY_QUERY,encodeQuery,decodeQueryStrict,loadSchema} from '@pythia-software/query-table-core';
import toolCallsDocument from '../../../schemas/tool_calls.schema.json' with {type:'json'};
import {answerQuery} from '../support/query-server.mjs';
const path=page=>new URL(page.url()).pathname+new URL(page.url()).search;

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
