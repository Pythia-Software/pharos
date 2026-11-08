import {test} from 'node:test';
import assert from 'node:assert/strict';
import {open,skip,activateHeaderTool} from './harness.mjs';
import {workID,rootConversation,defaultApi,queryFixtureDatasets,busyStatus,eventStream,conversationLabelsFixture,pendingEvidenceApi} from './fixtures.mjs';
import {EMPTY_QUERY,encodeQuery,decodeQueryStrict,loadSchema} from '@pythia-software/query-table-core';
import writingMessagesDocument from '../../../schemas/writing_messages.schema.json' with {type:'json'};
import usageDocument from '../../../schemas/usage.schema.json' with {type:'json'};
import toolCallsDocument from '../../../schemas/tool_calls.schema.json' with {type:'json'};
import mcpCallsDocument from '../../../schemas/mcp_calls.schema.json' with {type:'json'};
import safetyDocument from '../../../schemas/safety.schema.json' with {type:'json'};
import messageLabelsDocument from '../../../schemas/message_labels.schema.json' with {type:'json'};
import {answerQuery} from '../support/query-server.mjs';
const path=page=>new URL(page.url()).pathname+new URL(page.url()).search;

test('workspace fingerprints restore across views and respect the global event display setting',{skip},async t=>{
 const requests=[];
 const api={'POST /api/fingerprints':request=>{
  requests.push(request.body);
  const result=defaultApi['POST /api/fingerprints'](request);
  for(const item of result.items){
   const height=item.profile==='wide'?128:256;
   // Synthetic image with marks near both edges makes clipping visible in screenshots.
   const svg=`<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="${height}"><rect width="1024" height="${height}" fill="#ddd4bf"/><path d="M0 ${height*.35}h170v${height*.3}h-170z M240 ${height*.2}h180v${height*.6}h-180z M490 ${height*.3}h190v${height*.4}h-190z M750 ${height*.1}h274v${height*.8}H750z" fill="#557974"/><path d="M80 ${height*.9}Q400 ${height*.02} 850 ${height*.9}" fill="none" stroke="#a05437" stroke-width="8"/><circle cx="850" cy="${height*.9}" r="9" fill="#a05437"/></svg>`;
   const src='data:image/svg+xml,'+encodeURIComponent(svg);item.images={light:src,dark:src};
  }
  return result;
 }};
 const app=await open(t,{api,url:'/library?view=table',ready:'.message-label-fingerprint img'}),{page}=app;
 assert.ok(await page.getByRole('img',{name:'Workspace fingerprint',exact:true}).count());
 await page.getByRole('group',{name:'Display results as'}).getByRole('button',{name:'Conversations',exact:true}).click();
 await page.locator('.conversation-result-facts .message-label-fingerprint img').first().waitFor();
 assert.ok(requests.some(body=>body.items.some(item=>item.entity==='workspace'&&item.profile==='wide')));
 const fingerprint=page.locator('.conversation-result-facts .fingerprint-image').first();
 const geometry=await fingerprint.evaluate(node=>{const image=node.querySelector('img').getBoundingClientRect(),box=node.getBoundingClientRect();return {image:{width:image.width,height:image.height,top:image.top,bottom:image.bottom},box:{width:box.width,height:box.height,top:box.top,bottom:box.bottom}};});
 assert.ok(Math.abs(geometry.image.width/geometry.image.height-8)<.05);
 assert.ok(geometry.image.top>=geometry.box.top-.5&&geometry.image.bottom<=geometry.box.bottom+.5,'the full fingerprint fits in its container');
 await app.shot('search-fingerprint');
 await page.setViewportSize({width:320,height:900});
 const narrow=await fingerprint.evaluate(node=>{const image=node.querySelector('img').getBoundingClientRect(),box=node.getBoundingClientRect();return {imageWidth:image.width,imageHeight:image.height,boxWidth:box.width};});
 assert.ok(Math.abs(narrow.imageWidth/narrow.imageHeight-8)<.05&&narrow.imageWidth<=narrow.boxWidth+.5,'the fingerprint fits on a narrow screen');
 await page.setViewportSize({width:1440,height:900});
 await app.settled();await page.reload();
 await page.locator('.conversation-result-facts .message-label-fingerprint img').first().waitFor();
 assert.equal(await page.getByRole('button',{name:'Conversations',exact:true}).getAttribute('aria-pressed'),'true');
 const settings=async()=>{await activateHeaderTool(page,'settingsToggle');await page.getByRole('link',{name:'App preferences',exact:true}).click();await page.getByRole('switch',{name:'Agent events in fingerprints',exact:true}).waitFor();await app.settled();};
 await settings();
 const toggle=()=>page.getByRole('switch',{name:'Agent events in fingerprints',exact:true});
 assert.equal(await toggle().getAttribute('aria-checked'),'true');
 const settingsURL=path(page),saved=page.waitForResponse(response=>response.url().endsWith('/api/preferences')&&response.request().method()==='POST'&&response.request().postDataJSON()?.set?.['pharos-fingerprint-events']===false);
 await toggle().click();await saved;
 assert.equal(await toggle().getAttribute('aria-checked'),'false');assert.equal(path(page),settingsURL);
 requests.length=0;
 await page.goBack();await page.goBack();
 await page.locator('.conversation-result-facts .message-label-fingerprint img').first().waitFor();
 assert.ok(requests.some(body=>body.events===false&&body.items.some(item=>item.entity==='workspace')));
 requests.length=0;await page.reload();
 await page.locator('.conversation-result-facts .message-label-fingerprint img').first().waitFor();
 assert.ok(requests.length&&requests.every(body=>body.events===false));
 // The persisted preference also selects the conversation image variant.
 requests.length=0;
 await page.goto(new URL('/analyze?analyze_mode=query&analyze_data=message_labels&label_results=cards',page.url()).href);
 await page.locator('.message-label-result-card .message-label-fingerprint img').first().waitFor();
 assert.ok(requests.some(body=>body.events===false&&body.items.some(item=>item.entity==='conversation')));
 await settings();assert.equal(await toggle().getAttribute('aria-checked'),'false');await toggle().click();
 requests.length=0;await page.goBack();await page.goBack();
 await page.locator('.message-label-result-card .message-label-fingerprint img').first().waitFor();
 assert.ok(requests.some(body=>body.events===true));
});

test('Background Processing table queries and editor layout restore independently in a fresh browser',{skip},async t=>{
 const query={...EMPTY_QUERY,where:[{field:'kind',op:'=',value:'backup.local'}],orderBy:[{field:'seconds',dir:'asc'}]};
 const params=new URLSearchParams({q_sync_operations:encodeQuery(query),query_layout:JSON.stringify({sync_operations:true,sync_sources:false})});
 const first=await open(t,{url:'/settings/sync?'+params,ready:'#syncHistoryTables .qt-row'});
 const history=page=>page.getByRole('region',{name:'Recent run history',exact:true});
 await first.page.waitForFunction(()=>document.querySelectorAll('#syncHistoryTables .qt-row').length===1);
 assert.ok(await history(first.page).getByText('Back up',{exact:true}).isVisible());
 assert.ok(await history(first.page).getByRole('button',{name:'Expand query builder',exact:true}).isVisible());
 assert.ok(await first.page.getByRole('region',{name:'Source status',exact:true}).getByRole('button',{name:'Add filter',exact:true}).isVisible());
 await history(first.page).getByRole('button',{name:'Expand query builder',exact:true}).click();
 await first.page.waitForFunction(()=>JSON.parse(new URLSearchParams(location.search).get('query_layout')).sync_operations===false);
 const linked=path(first.page);
 const fresh=await open(t,{url:linked,ready:'#syncHistoryTables .qt-row'});
 await fresh.page.waitForFunction(()=>document.querySelectorAll('#syncHistoryTables .qt-row').length===1);
 assert.ok(await history(fresh.page).getByRole('button',{name:'Remove Kind filter',exact:true}).isVisible());
 assert.ok(await history(fresh.page).getByRole('button',{name:'Collapse query builder',exact:true}).isVisible());
 const restored=decodeQueryStrict(new URL(fresh.page.url()).searchParams.get('q_sync_operations'));
 assert.deepEqual(restored.where,query.where);
 assert.deepEqual(restored.orderBy,query.orderBy);
 // Back/Forward must restore the query on the mounted page.
 const all=new URL(fresh.page.url());all.searchParams.set('q_sync_operations',encodeQuery(EMPTY_QUERY));
 await fresh.page.evaluate(url=>{history.pushState(null,'',url);window.dispatchEvent(new PopStateEvent('popstate'));},all.href);
 await fresh.page.waitForFunction(()=>document.querySelectorAll('#syncHistoryTables .qt-row').length===3);
 await fresh.page.goBack();
 await fresh.page.waitForFunction(()=>document.querySelectorAll('#syncHistoryTables .qt-row').length===1);
 assert.ok(await history(fresh.page).getByText('Back up',{exact:true}).isVisible());
});

test('saved Parallel activity links open the native Timeline and retain the selected range and session',{skip},async t=>{
 const analysis=defaultApi['GET /api/work/:id/delegation']({query:new URLSearchParams()});
 const range=[analysis.start+10000,analysis.end-10000],params=new URLSearchParams({explorer:'parallel',delegation_start:String(range[0]),delegation_end:String(range[1]),delegation_session:'s-root',delegation_labels:'off'});
 const app=await open(t,{url:`/work/${workID}?${params}`,ready:'.lt-session-root'}),{page}=app;
 await app.settled();
 const url=new URL(page.url());
 assert.equal(url.searchParams.get('explorer'),'timeline');
 assert.equal(url.searchParams.get('delegation_start'),String(range[0]));
 assert.equal(url.searchParams.get('delegation_end'),String(range[1]));
 assert.equal(url.searchParams.get('delegation_session'),'s-root');
 assert.equal(await page.getByRole('tab',{name:'Timeline',exact:true}).getAttribute('aria-selected'),'true');
 assert.ok(app.calls.some(call=>call.path.endsWith('/parallel')&&call.query.get('labels')==='1'));
 const copy=await open(t,{url:path(page),ready:'.lt-session-root'});
 assert.equal(await copy.page.getByRole('tab',{name:'Timeline',exact:true}).getAttribute('aria-selected'),'true');
});

test('legacy delegation session IDs restore to canonical sessions once and keep unrelated state',{skip},async t=>{
 // s-legacy is an old row ID of s-child; s-split no longer names one session.
 const aliases={'s-legacy':'s-child','s-split':null};
 const api={'GET /api/work/:id/delegation':({query})=>{
  const data=defaultApi['GET /api/work/:id/delegation']({query}),asked=query.getAll('resolve').filter(id=>id in aliases);
  return asked.length?{...data,aliases:Object.fromEntries(asked.map(id=>[id,aliases[id]]))}:data;
 }};
 const disclosures={'delegation-labels:s-legacy:session':true,'delegation-labels:s-split:session':true,'unrelated-section':true};
 const params=new URLSearchParams({delegation_session:'s-legacy',delegation_collapsed:'s-legacy,s-split',disclosures:JSON.stringify(disclosures)});
 const app=await open(t,{url:`/work/${workID}?${params}`,api,ready:'.delegation-table tbody tr'}),{page}=app;
 await page.waitForFunction(()=>new URLSearchParams(location.search).get('delegation_session')==='s-child');await app.settled();
 const url=new URL(page.url());
 assert.equal(url.searchParams.get('delegation_collapsed'),'s-child,s-split');
 assert.deepEqual(JSON.parse(url.searchParams.get('disclosures')),{'delegation-labels:s-child:session':true,'delegation-labels:s-split:session':true,'unrelated-section':true});
 const resolving=()=>app.calls.filter(call=>call.query.getAll('resolve').length).length,once=resolving();
 await page.reload();await page.locator('.delegation-table tbody tr').first().waitFor();await app.settled();
 assert.equal(new URL(page.url()).searchParams.get('delegation_session'),'s-child','a canonical URL stays canonical');
 assert.ok(resolving()<=once+1,'restoration does not loop');
});

test('relatives left out at the family bound load from the URL in a fresh browser',{skip},async t=>{
 const loaded=page=>page.locator('.delegation-table [data-session-id="s-relative-1"]').first();
 const app=await open(t,{url:`/work/${workID}`,api:pendingEvidenceApi,ready:'.delegation-table .delegation-relatives'}),{page}=app;
 await page.getByRole('button',{name:/^Show 2 more related sessions of /}).click();
 await page.waitForFunction(()=>new URLSearchParams(location.search).get('delegation_relatives')==='["s-child"]');
 await loaded(page).waitFor();await app.settled();
 assert.ok(app.calls.some(call=>call.path.endsWith('/delegation')&&call.query.get('relatives')==='s-child'));
 const copy=await open(t,{url:path(page),api:pendingEvidenceApi,ready:'.delegation-table tbody tr'});
 await loaded(copy.page).waitFor();
 assert.ok(copy.calls.some(call=>call.path.endsWith('/delegation')&&call.query.get('relatives')==='s-child'));
});

test('Timeline pending events stay announced for a collapsed session, after restoration and without taking focus',{skip},async t=>{
 const status=page=>page.locator('[role=status].delegation-truncation');
 const caption=page=>page.locator('.lt-recursive-lane[data-session-id="s-nested"] .delegation-truncation');
 const app=await open(t,{url:`/work/${workID}?explorer=timeline`,api:pendingEvidenceApi,ready:'.lt-recursive-lane[data-session-id="s-nested"] .delegation-truncation'}),{page}=app;
 await status(page).first().waitFor();const drawn=await status(page).first().textContent();
 assert.equal(await page.evaluate(()=>document.activeElement===document.body),true,'the status does not take focus');
 // Collapsing the parent hides the pending lane and its caption; the Timeline still says so.
 await page.getByRole('button',{name:/^Collapse child sessions of Review implementation/}).click();
 await caption(page).waitFor({state:'detached'});
 await page.waitForFunction(text=>[...document.querySelectorAll('[role=status].delegation-truncation')].some(el=>el.textContent!==text),drawn);
 await app.settled();
 const copy=await open(t,{url:path(page),api:pendingEvidenceApi,ready:'.lt-session-root'});
 await status(copy.page).first().waitFor();
 await copy.page.getByRole('button',{name:/^Expand child sessions of Review implementation/}).waitFor();
 assert.notEqual(await status(copy.page).first().textContent(),drawn,'the restored status still counts the unshown session');
});

test('workspace sidebar visibility and width restore without leaving the selected conversation',{skip},async t=>{
 const api={'GET /api/work/:id/conversations/:conversation':request=>{
  const conversation=defaultApi['GET /api/work/:id/conversations/:conversation'](request);
  if(request.params.conversation!=='conv-child')return {...conversation,machine:'Unknown',execution_machine:'Unknown'};
  return {...conversation,machine:'Laptop',execution_machine:'Worker',effort:'medium',messages:conversation.messages.filter(message=>message.role!=='user')};
 }};
 const app=await open(t,{api,url:`/work/${workID}/conversation/${rootConversation}?sidebar_width=460`,ready:'.conversation-panel .turn'}),{page}=app;
 const show=page.getByRole('button',{name:'Show agent sessions',exact:true}),hide=page.getByRole('button',{name:'Hide agent sessions',exact:true});
 const divider=page.getByRole('separator',{name:'Resize agent sessions sidebar',exact:true});
 assert.equal(await divider.getAttribute('aria-valuenow'),'460');
 await hide.click();await page.reload();await show.waitFor();await app.settled();
 assert.equal(new URL(page.url()).searchParams.get('sidebar_visible'),'0');
 await show.click();await divider.waitFor();assert.equal(await divider.getAttribute('aria-valuenow'),'460');
 await page.evaluate(()=>{const url=new URL(location.href);url.searchParams.set('sidebar_visible','0');url.searchParams.set('sidebar_width','340');history.pushState(null,'',url);dispatchEvent(new PopStateEvent('popstate'));});
 await show.waitFor();await page.goBack();await hide.waitFor();await app.settled();
 assert.equal(await divider.getAttribute('aria-valuenow'),'460');
 await page.goForward();await show.waitFor();await show.click();await app.settled();
 assert.equal(await divider.getAttribute('aria-valuenow'),'340');
 assert.equal(new URL(page.url()).searchParams.get('conversation'),rootConversation);
 const context=()=>page.locator('.conversation-panel .delegation-context');
 const children=()=>context().getByRole('button',{name:'1 child session',exact:true});
 await page.locator('.delegation-session-heading').waitFor();
 assert.equal(await children().isVisible(),false);
 const chips=()=>page.locator('.conversation-panel .conversation-title-content > .chips');
 assert.equal(await chips().getByText(/^(Machine|Execution machine):/).count(),0);
 assert.equal(await chips().getByText(/^Effort:/).count(),0);
 assert.equal(await chips().getByText('claude-opus-5-5',{exact:true}).count(),0);
 assert.equal(await context().getByText(/This agent session/).count(),0);
 await hide.click();await children().waitFor();await app.shot('collapsed-root');
 const rootURL=new URL(page.url());await children().click();await hide.waitFor();
 assert.equal(new URL(page.url()).searchParams.get('conversation'),rootURL.searchParams.get('conversation'));
 await page.getByRole('table',{name:'Agent session hierarchy and usage'}).getByRole('button',{name:'Open session Review implementation',exact:true}).click();
 await page.locator('.conversation-panel [data-message-id="conv-child-reply"]').first().waitFor();
 assert.ok(await chips().getByText('Machine: Laptop',{exact:true}).isVisible());
 assert.ok(await chips().getByText('Execution machine: Worker',{exact:true}).isVisible());
 assert.ok(await chips().getByText('1 turn',{exact:true}).isVisible());
 assert.equal(await page.locator('.turn-list > .turn').count(),1);
 await hide.click();const parent=()=>context().getByRole('button',{name:'Parent session',exact:true});
 await parent().waitFor();await children().waitFor();await app.shot('collapsed-child');
 const childURL=new URL(page.url());await parent().click();await hide.waitFor();
 assert.equal(new URL(page.url()).searchParams.get('conversation'),childURL.searchParams.get('conversation'));
 assert.equal(new URL(page.url()).searchParams.get('delegation_session'),childURL.searchParams.get('delegation_session'));
 assert.equal(await parent().isVisible(),false);assert.equal(await children().isVisible(),false);
 await page.setViewportSize({width:390,height:900});await children().waitFor();
 await children().click();await page.getByRole('dialog',{name:'Agent sessions'}).waitFor();
 assert.equal(new URL(page.url()).searchParams.get('conversation'),childURL.searchParams.get('conversation'));
});

for(const width of [1440,390])test(`workspace Timeline beside a reader expands before editing at ${width}px`,{skip},async t=>{
 const root=`delegation-labels:caller:${workID}:`,disclosures={'timeline-key':true,...Object.fromEntries(['session','authorship','authorship:m-1'].map(id=>[root+id,true]))};
 const params=new URLSearchParams({explorer:'timeline',conversation:rootConversation,message:'m-1',disclosures:JSON.stringify(disclosures),timeline_columns:JSON.stringify({execution:205,events:185,time:480})});
 const app=await open(t,{url:`/work/${workID}?${params}`,viewport:{width,height:900},ready:'.lt-preview-activate'}),{page}=app;
 await page.locator('.conversation-panel:not(.conversation-loading)').waitFor();await app.settled();
 const reveal=async()=>{if(width<960){await page.getByRole('button',{name:'Show agent sessions',exact:true}).click();}};
 const settings=()=>page.evaluate(()=>{const p=new URLSearchParams(location.search);return Object.fromEntries(['delegation_start','delegation_end','timeline_columns','disclosures','timeline_visibility','delegation_collapsed','delegation_event'].map(key=>[key,p.get(key)]));});
 await reveal();
 const before=await settings(),saved=path(page),activate=page.getByRole('button',{name:'Open full-width timeline',exact:true});
 const key=page.locator('.lt-timeline-legend'),friction=key.getByRole('button',{name:'Friction',exact:true});
 assert.equal(await friction.innerText(),'','the sidebar key stays compact even when the full Key is expanded');
 assert.equal(await friction.getAttribute('aria-haspopup'),null,'preview icons never offer a visibility menu');
 await friction.hover();await page.getByRole('tooltip').getByText('Friction',{exact:true}).waitFor();
 assert.deepEqual(await settings(),before,'hovering the preview leaves view settings unchanged');
 await app.shot('sidebar-key-hover');
 await page.mouse.move(0,0);

 assert.equal(await page.getByRole('slider',{name:'Timeline window start',exact:true}).evaluate(el=>{el.focus();return document.activeElement===el;}),false,'preview controls cannot receive keyboard focus');
 assert.equal(await page.locator('.delegation-parallel').evaluate(el=>el.scrollWidth<=el.clientWidth+1),true,'static overview fits the sidebar');
 if(width===1440){
  await page.getByRole('separator',{name:'Resize agent sessions sidebar',exact:true}).press('End');
  await page.locator('.workspace-inbox:not(.compact)').waitFor();
  assert.equal(await activate.isVisible(),true,'a wide sidebar still uses the read-only overview');
  // Even a zoom-like drag is consumed by the overview activation.
  const ribbon=await page.locator('.lt-caller-row .lt-session-ribbon').boundingBox();
  await page.mouse.move(ribbon.x+5,ribbon.y+ribbon.height/2);await page.mouse.down();await page.mouse.move(ribbon.x+ribbon.width-5,ribbon.y+ribbon.height/2);await page.mouse.up();
 }else{await activate.focus();await activate.press('Space');}
 await page.waitForFunction(()=>!document.querySelector('.workspace-inbox').classList.contains('viewing'));await app.settled();
 assert.deepEqual(await settings(),before,'activation preserves saved timeline settings');
 assert.equal(new URL(page.url()).searchParams.get('conversation'),null);
 assert.equal(await key.locator('.lt-key-disclosure').evaluate(el=>el.open),true,'the full Timeline keeps its own expanded Key');
 assert.equal(await page.locator(`[data-pharos-target="section:${root}authorship:m-1"]`).evaluate(el=>el.open),true);
 const start=page.getByRole('slider',{name:'Timeline window start',exact:true});assert.equal(await start.isVisible(),true);
 await page.goBack();await page.locator('.lt-preview-activate').waitFor();await app.settled();
 assert.equal(new URL(page.url()).searchParams.get('conversation'),rootConversation);
 await reveal();await page.getByRole('button',{name:'Open full-width timeline',exact:true}).press('Enter');
 await page.waitForFunction(()=>!document.querySelector('.workspace-inbox').classList.contains('viewing'));await app.settled();assert.deepEqual(await settings(),before);
 await page.goBack();await page.locator('.lt-preview-activate').waitFor();await app.settled();
 await page.goForward();await page.waitForFunction(()=>!document.querySelector('.workspace-inbox').classList.contains('viewing'));await app.settled();
 await start.focus();await start.press('ArrowRight');await app.settled();assert.ok(Number(new URL(page.url()).searchParams.get('delegation_start'))>Number(before.delegation_start));
 await page.getByRole('button',{name:'Reset',exact:true}).click();await app.settled();
 // Choose only Friction in the full Timeline, then enter a conversation.
 await friction.click();await page.getByRole('menuitem',{name:'Hide others',exact:true}).click();await app.settled();
 await page.locator('[data-agent-event="e-s-root-open"]').first().waitFor({state:'attached'});
 const chosen=new URL(page.url()).searchParams.get('timeline_visibility');
 await key.locator('.lt-key-disclosure > summary').click();await app.settled();
 await page.getByRole('button',{name:/^Open session /}).first().click();await page.locator('.conversation-panel:not(.conversation-loading)').waitFor();await page.locator('.lt-preview-activate').waitFor();await app.settled();
 await reveal();
 assert.equal(new URL(page.url()).searchParams.get('timeline_visibility'),chosen,'conversation navigation retains visibility selections');
 await page.locator('[data-agent-event="e-s-root-open"]').first().waitFor({state:'attached'});
 assert.equal(await page.locator('[data-agent-event="e-s-root"]').count(),0,'the preview also draws only Friction');
 assert.equal(await page.locator('.lt-session-preview [data-tool-id]').count(),0,'hidden tools stay hidden in the conversation preview');
 await friction.hover();await page.getByRole('tooltip').getByText('Friction',{exact:true}).waitFor();
 await app.shot('sidebar-key-only-friction');
 await friction.click();await page.waitForFunction(()=>!document.querySelector('.workspace-inbox').classList.contains('viewing'));await app.settled();
 assert.equal(await page.getByRole('menu').count(),0,'clicking a preview icon opens the full Timeline without editing visibility');
 assert.equal(new URL(page.url()).searchParams.get('timeline_visibility'),chosen);
 assert.equal(await key.locator('.lt-key-disclosure').evaluate(el=>el.open),false,'returning retains the full Key’s deliberately collapsed state');
 const copy=await open(t,{url:saved,viewport:{width,height:900},ready:'.lt-preview-activate'});await copy.settled();
 assert.equal(await copy.page.locator('.delegation-parallel [inert]').count(),1);
 assert.equal(await copy.page.locator('.delegation-parallel details[open]').count(),0);
 assert.equal(JSON.parse(new URL(copy.page.url()).searchParams.get('disclosures'))[root+'authorship:m-1'],true);
 assert.ok(await copy.page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
});

for(const width of [1440,390])test(`hierarchy metric scope and session timeline navigation restore from copied links at ${width}px`, {skip}, async t => {
 const base=defaultApi['GET /api/work/:id/delegation']({query:new URLSearchParams()});
 const child=base.rows.find(row=>row.id==='s-child');
 const api={'GET /api/work/:id/delegation':({query})=>{
  const data=defaultApi['GET /api/work/:id/delegation']({query});
  return {...data,rows:data.rows.map(row=>row.id===child.id?{...row,start:base.start+10000,end:base.end-10000}:row)};
 }};
 const app=await open(t,{url:`/work/${workID}`,api,viewport:{width,height:900},ready:'.delegation-table tbody tr'}),{page}=app;
 await app.settled();
 const scope=page.getByRole('combobox',{name:'Session metric scope',exact:true});
 await scope.selectOption('self');await app.settled();
 await page.getByRole('button',{name:'Tool calls',exact:true}).click();await app.settled();
 assert.equal(await scope.inputValue(),'self');
 await scope.selectOption('all');await app.settled();
 await page.getByRole('button',{name:'Session',exact:true}).click();await app.settled();
 assert.equal(await scope.inputValue(),'all');
 await scope.selectOption('self');await app.settled();
 const tableLink=path(page);
 await app.shot('hierarchy-usage-table');
 await page.locator('.delegation-table [data-session-id="s-child"] .delegation-timeline-button').click();
 await page.locator('[data-pharos-target="section:delegation-labels:s-child:session"][open]').waitFor();await app.settled();
 await page.getByRole('tab',{name:'Hierarchy & usage',exact:true}).click();await page.locator('.delegation-table tbody tr').first().waitFor();await app.settled();
 const row=page.locator('.delegation-table [data-session-id="s-child"]');
 await row.getByRole('button',{name:/^Open session /}).click();await page.waitForFunction(()=>new URLSearchParams(location.search).get('conversation')==='conv-child');await app.settled();
 assert.equal(await page.locator('.workspace-reader').isVisible(),true);
 if(width<960)await page.getByRole('button',{name:'Show agent sessions',exact:true}).click();
 await page.getByRole('button',{name:'Open workspace overview',exact:true}).first().click();await app.settled();
 await page.getByRole('button',{name:`View timeline for ${child.label}`,exact:true}).click();
 const expanded=page.locator('[data-pharos-target="section:delegation-labels:s-child:session"]');
 await expanded.waitFor();await page.waitForFunction(()=>document.querySelector('[data-pharos-target="section:delegation-labels:s-child:session"]').open);await app.settled();
 assert.equal(new URL(page.url()).searchParams.get('conversation'),null);
 assert.equal(await page.getByRole('tab',{name:'Timeline',exact:true}).getAttribute('aria-selected'),'true');
 assert.equal(await page.locator('.delegation-parallel [inert]').count(),0);
 assert.equal(await expanded.locator('details[open]').count(),0);
 const timelineLink=path(page);
 await app.shot('hierarchy-session-timeline');
 const copy=await open(t,{url:timelineLink,api,ready:'[data-pharos-target="section:delegation-labels:s-child:session"][open]'});await copy.settled();
 assert.equal(await copy.page.locator('[data-pharos-target="section:delegation-labels:s-child:session"] details[open]').count(),0);
 await page.evaluate(url=>window.pharosNavigate(url),tableLink);await page.locator('.delegation-table tbody tr').first().waitFor();await app.settled();
 assert.equal(await scope.inputValue(),'self');
});

for(const width of [1280,390])test(`Timeline session navigation and event expansion remain independent at ${width}px`,{skip},async t=>{
 const app=await open(t,{url:`/work/${workID}?explorer=timeline`,viewport:{width,height:900},ready:'.lt-session-root'}),{page}=app;
 const session=id=>page.locator(`[data-session-id="${id}"]`).getByRole('button',{name:/^Open session /});
 const settings=()=>page.evaluate(()=>{const p=new URLSearchParams(location.search);return Object.fromEntries(['delegation_start','delegation_end','disclosures','timeline_columns'].map(key=>[key,p.get(key)]));});
 await app.settled();const before=await settings();
 await session('s-root').click();await page.waitForFunction(()=>new URLSearchParams(location.search).get('conversation')==='conv-root');await app.settled();
 assert.equal(new URL(page.url()).searchParams.get('delegation_session'),'s-root');assert.deepEqual(await settings(),before);
 const show=async()=>{if(width<960)await page.getByRole('button',{name:'Show agent sessions',exact:true}).click();};
 await show();await session('s-child').click();await page.waitForFunction(()=>new URLSearchParams(location.search).get('conversation')==='conv-child');await app.settled();
 assert.equal(new URL(page.url()).searchParams.get('delegation_session'),'s-child');assert.equal(await page.locator('.workspace-reader').isVisible(),true);assert.deepEqual(await settings(),before);
 await show();
 const current=page.locator('[data-session-id="s-child"] [aria-current="true"]');
 await current.click();await app.settled();assert.equal(new URL(page.url()).searchParams.get('conversation'),'conv-child','the current identity leaves the reader and timeline overview in place');
 await app.shot('timeline-sidebar');
 const clockBox=await page.locator('[data-session-id="s-child"] .lt-session-ribbon').boundingBox();await page.mouse.click(clockBox.x+clockBox.width/2,clockBox.y+clockBox.height/2);
 await page.waitForFunction(()=>!document.querySelector('.workspace-inbox').classList.contains('viewing'));await app.settled();
 assert.equal(new URL(page.url()).searchParams.get('conversation'),null);assert.equal(await page.getByRole('tab',{name:'Timeline',exact:true}).getAttribute('aria-selected'),'true');assert.deepEqual(await settings(),before);
 const compactWidth=(await page.locator('.lt-column-heading').nth(1).boundingBox()).width,root=page.locator('[data-pharos-target="section:delegation-labels:s-root:session"] > summary');
 await root.locator('.lt-session-row-label').click();await page.waitForFunction(width=>document.querySelector('.lt-column-heading:nth-child(2)').getBoundingClientRect().width>width,compactWidth);await app.settled();const expandedWidth=(await page.locator('.lt-column-heading').nth(1).boundingBox()).width;assert.ok(expandedWidth>compactWidth);
 await root.locator('.lt-session-row-label').click();await page.waitForFunction(width=>document.querySelector('.lt-column-heading:nth-child(2)').getBoundingClientRect().width<width,expandedWidth);await app.settled();assert.ok((await page.locator('.lt-column-heading').nth(1).boundingBox()).width<expandedWidth);
 await session('s-child').focus();await session('s-child').press('Enter');await page.waitForFunction(()=>new URLSearchParams(location.search).get('conversation')==='conv-child');await app.settled();
 await show();await session('s-root').press('Enter');await page.waitForFunction(()=>new URLSearchParams(location.search).get('conversation')==='conv-root');await app.settled();
 assert.equal(new URL(page.url()).searchParams.get('delegation_session'),'s-root');
 await page.goBack();await page.waitForFunction(()=>new URLSearchParams(location.search).get('conversation')==='conv-child');await app.settled();assert.equal(new URL(page.url()).searchParams.get('delegation_session'),'s-child');
});

for(const width of [1280,390])test(`session hierarchy sidebar retains navigation and fits usage columns at ${width}px`,{skip},async t=>{
 const params=new URLSearchParams({conversation:rootConversation,delegation_mode:'rank',delegation_sort:'cost',delegation_direction:'asc',delegation_scope:'all'});
 const app=await open(t,{url:`/work/${workID}?${params}`,viewport:{width,height:900},ready:'.delegation-inbox-row'}),{page}=app;
 const show=async()=>{if(width<960)await page.getByRole('button',{name:'Show agent sessions',exact:true}).click();};
 await show();await app.settled();
 assert.equal(app.calls.filter(call=>call.path.endsWith('/delegation')).at(-1).query.get('mode'),'hierarchy','the compact table loads the hierarchy even for a saved ranking');
 const table=page.getByRole('table',{name:'Agent session hierarchy and usage'});
 assert.equal(await table.evaluate(el=>el.scrollWidth<=el.clientWidth+1),true,'session names leave space for both usage columns');
 const current=table.locator('[aria-current="true"]');await current.click();await app.settled();
 assert.equal(new URL(page.url()).searchParams.get('conversation'),rootConversation);
 await table.getByRole('button',{name:'Open session Review implementation',exact:true}).click();
 await page.waitForFunction(()=>new URLSearchParams(location.search).get('conversation')==='conv-child');await app.settled();
 await show();await app.shot('hierarchy-sidebar');
 await table.getByRole('button',{name:'Collapse Review implementation',exact:true}).click();await app.settled();
 await table.getByRole('button',{name:'Expand Review implementation',exact:true}).waitFor();
 assert.equal(new URL(page.url()).searchParams.get('conversation'),'conv-child','folding the selected session leaves its reader open');
 await table.getByRole('button',{name:'Expand Review implementation',exact:true}).click();await app.settled();
 await table.getByRole('button',{name:'Open session Review timing',exact:true}).waitFor();
 if(width>=960){
  await page.getByRole('separator',{name:'Resize agent sessions sidebar',exact:true}).press('End');
  await page.locator('.workspace-inbox:not(.compact)').waitFor();await app.settled();
  await page.locator('.delegation-table').getByRole('button',{name:'Open session Review timing',exact:true}).click();
  await page.waitForFunction(()=>new URLSearchParams(location.search).get('conversation')==='conv-nested');await app.settled();
  await app.shot('hierarchy-wide-sidebar');
 }
 await page.getByRole('button',{name:'Open workspace overview',exact:true}).first().click();await app.settled();
 assert.equal(new URL(page.url()).searchParams.get('delegation_mode'),'rank','the fixed sidebar presentation preserves the full-width ranking');
 await app.shot('hierarchy-full-width');
});

test('workspace Timeline message links preserve source transcripts across canonical session aliases',{skip},async t=>{
 const keys=['session','labels','labels:P','labels:P:P.request','labels:P:P.request:P.request.work','labels:P:P.request:P.request.work:P.request.work.implement'];
 const disclosures=Object.fromEntries(keys.map(id=>['delegation-labels:caller:'+workID+':'+id,true]));
 const api={'GET /api/work/:id/parallel':({query})=>{
  const data=defaultApi['GET /api/work/:id/parallel']({query});
  return {...data,lanes:data.lanes.map(lane=>lane.session==='s-root'?{...lane,messages:lane.messages?.map((m,i)=>i===0?{...m,id:'conv-child-reply',conversation_id:'conv-child'}:m)}:lane)};
 }};
 const app=await open(t,{url:`/work/${workID}?`+new URLSearchParams({explorer:'timeline',disclosures:JSON.stringify(disclosures)}),api,ready:'.lt-session-leaf button'});
 await app.page.locator('.lt-session-leaf button').first().click();
 await app.page.getByRole('group',{name:'Ribbon segment details',exact:true}).getByRole('link').first().click();await app.settled();
 assert.equal(new URL(app.page.url()).searchParams.get('conversation'),'conv-child');
 assert.equal(new URL(app.page.url()).searchParams.get('message'),'conv-child-reply');
 await app.page.locator('.conversation-panel [data-message-id="conv-child-reply"]').first().waitFor();
});

for(const width of [1440,390])test(`single-agent Conversation and Timeline share the actual reader at ${width}px`,{skip},async t=>{
 const work=defaultApi['GET /api/work/:id']({params:{id:workID}}),base=defaultApi['GET /api/work/:id/delegation']({query:new URLSearchParams()});
 const analysis={...base,rows:[{...base.rows[0],child_count:0}],focus:[{...base.rows[0],child_count:0}],session_count:1,total:1};
 const api={'GET /api/work/:id':{...work,conversations:[work.conversations[0]]},'GET /api/work/:id/delegation':analysis};
 const app=await open(t,{url:`/work/${workID}`,api,viewport:{width,height:900},ready:'.delegation-single'}),{page}=app;
 await page.locator('.conversation-panel:not(.conversation-loading)').waitFor();
 assert.equal(await page.getByRole('tab',{name:'Conversation',exact:true}).getAttribute('aria-selected'),'true');
 await page.getByRole('tab',{name:'Timeline',exact:true}).click();await page.locator('.lt-session-root').first().waitFor();await app.settled();
 assert.equal(await page.locator('.conversation-panel').isVisible(),false);
 assert.equal(new URL(page.url()).searchParams.get('reader_view'),'timeline');
 const copy=await open(t,{url:path(page),api,viewport:{width,height:900},ready:'.lt-session-root'});await copy.settled();
 assert.equal(await copy.page.getByRole('tab',{name:'Timeline',exact:true}).getAttribute('aria-selected'),'true');
 for(const id of ['session','labels','labels:P','labels:P:P.request','labels:P:P.request:P.request.work','labels:P:P.request:P.request.work:P.request.work.implement'])await page.locator(`[data-pharos-target="section:delegation-labels:caller:${workID}:${id}"] > summary`).click();
 await page.locator('.lt-session-leaf button').first().click();
 await page.getByRole('group',{name:'Ribbon segment details',exact:true}).getByRole('link').first().click();await app.settled();
 assert.equal(new URL(page.url()).searchParams.get('reader_view'),'conversation');
 assert.equal(new URL(page.url()).searchParams.get('message'),'m-1');
 assert.equal(await page.locator('.conversation-panel').isVisible(),true);
 assert.equal(await page.getByRole('tab',{name:'Conversation',exact:true}).getAttribute('aria-selected'),'true');
 assert.ok(await copy.page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'page overflow');
 await page.evaluate(url=>window.pharosNavigate(url),`/work/${workID}?reader_view=timeline`);await app.settled();
 assert.equal(await page.getByRole('tab',{name:'Timeline',exact:true}).getAttribute('aria-selected'),'true');
 await page.goBack();await app.settled();
 assert.equal(await page.locator('.conversation-panel').isVisible(),true);
 await page.goForward();await app.settled();
 assert.equal(await page.locator('.conversation-panel').isVisible(),false);
 await app.shot(`single-timeline-${width}`);
});

test('Timeline resizing, window controls and Caller disclosures restore without changing their source identities',{skip},async t=>{
 const app=await open(t,{url:`/work/${workID}?explorer=timeline&timeline_labels=off&timeline_columns=${encodeURIComponent(JSON.stringify({execution:175,events:165,time:480}))}`,ready:'.lt-caller-row .lt-session-root'}),{page}=app;
 const before=new URL(page.url()).searchParams;
 const windowBefore=[before.get('delegation_start'),before.get('delegation_end')];
 const cutoffs=page.getByRole('status',{name:'Selected timeline window',exact:true}).locator('time');
 const readCutoffs=()=>cutoffs.evaluateAll(elements=>elements.map(element=>Date.parse(element.dateTime)));
 const readWindow=async(p=page)=>Promise.all(['start','end'].map(async edge=>Math.trunc(Number(await p.getByRole('slider',{name:`Timeline window ${edge}`,exact:true}).getAttribute('aria-valuenow')))));
 const whole=await readWindow();
 const root=`section:delegation-labels:caller:${workID}:`;
 const summary=page.locator(`[data-pharos-target="${root}session"] > summary`);
 const compactWidth=(await page.locator('.lt-column-heading').nth(1).boundingBox()).width;
 await summary.locator('.lt-session-row-label').click();
 await page.waitForFunction(width=>document.querySelector('.lt-column-heading:nth-child(2)').getBoundingClientRect().width>width,compactWidth);
 assert.ok((await page.locator('.lt-column-heading').nth(1).boundingBox()).width>compactWidth,'event details have room once expanded');
 for(const label of ['session','events']){
  const resize=page.getByRole('separator',{name:`Resize ${label} column`,exact:true});
  await resize.focus();await resize.press('ArrowRight');
 }
 const events=page.getByRole('separator',{name:'Resize events column',exact:true}),box=await events.boundingBox();
 await page.mouse.move(box.x+box.width/2,box.y+box.height/2);await page.mouse.down();await page.mouse.move(box.x+box.width/2+35,box.y+box.height/2);await page.mouse.up();
 await app.settled();
 const params=new URL(page.url()).searchParams,widths=JSON.parse(params.get('timeline_columns'));
 assert.ok(widths.execution>175&&widths.events>165);
 const edge=await page.locator('.delegation-lane-scroll').boundingBox(),clock=await page.locator('.lt-clock-heading').boundingBox(),ribbons=await page.locator('.lt-session-root > summary .lt-session-ribbon').evaluateAll(elements=>elements.map(element=>element.getBoundingClientRect().right));
 assert.ok(Math.abs(clock.x+clock.width-edge.x-edge.width)<1,'the clock fills the remaining table width');
 assert.ok(ribbons.every(right=>Math.abs(right-clock.x-clock.width)<1),'every session ribbon reaches the clock endpoint');
 assert.deepEqual([params.get('delegation_start'),params.get('delegation_end')],windowBefore);
 await page.locator(`[data-pharos-target="${root}authorship"] > summary`).click();
 await page.locator(`[data-pharos-target="${root}authorship:m-1"] > summary`).click();await app.settled();
 const spans=page.locator(`[data-pharos-target="${root}authorship:m-1"] .lt-session-leaf button`);
 await spans.first().focus();
 assert.equal(await page.getByRole('tooltip').locator('blockquote').textContent(),'Implement th');
 await spans.last().focus();
 assert.equal(await page.getByRole('tooltip').locator('blockquote').textContent(),'is change');
 const copy=await open(t,{url:path(page),ready:'.lt-caller-row .lt-session-root'});await copy.settled();
 assert.deepEqual(JSON.parse(new URL(copy.page.url()).searchParams.get('timeline_columns')),widths);
 assert.equal(await copy.page.locator(`[data-pharos-target="${root}authorship"]`).evaluate(e=>e.open),true);
 assert.equal(await copy.page.locator(`[data-pharos-target="${root}authorship:m-1"]`).evaluate(e=>e.open),true);
 await copy.page.locator(`[data-pharos-target="${root}authorship:m-1"] .lt-session-leaf button`).last().click();
 await copy.page.getByRole('group',{name:'Ribbon segment details',exact:true}).getByRole('link').first().click();await copy.settled();
 assert.equal(new URL(copy.page.url()).searchParams.get('message'),'m-1');
 assert.equal(new URL(copy.page.url()).searchParams.get('conversation'),rootConversation);
 const rangeStart=Number(new URL(page.url()).searchParams.get('delegation_start'));
 await page.getByRole('slider',{name:'Timeline window start',exact:true}).focus();await page.getByRole('slider',{name:'Timeline window start',exact:true}).press('ArrowRight');await app.settled();
 assert.ok(Number(new URL(page.url()).searchParams.get('delegation_start'))>rangeStart);
 const saved=await open(t,{url:path(page),ready:'.lt-caller-row'});await saved.settled();
 assert.equal(new URL(saved.page.url()).searchParams.get('delegation_start'),new URL(page.url()).searchParams.get('delegation_start'));
 // The drag's displayed endpoints update before pointer-up commits the URL.
 const end=page.getByRole('slider',{name:'Timeline window end',exact:true}),endBox=await end.boundingBox(),shown=await readCutoffs(),urlBeforeDrag=path(page);
 await page.mouse.move(endBox.x+endBox.width/2,endBox.y+endBox.height/2);await page.mouse.down();
 await page.mouse.move(endBox.x+endBox.width/2-45,endBox.y+endBox.height/2);
 await page.waitForFunction(before=>Date.parse(document.querySelector('.delegation-overview-cutoffs time:last-child').dateTime)!==before,shown[1]);
 assert.equal(path(page),urlBeforeDrag);
 const draft=await readCutoffs();assert.equal(draft[0],shown[0]);assert.ok(draft[1]<shown[1]);
 await page.mouse.up();await app.settled();
 assert.equal(Math.trunc(Number(new URL(page.url()).searchParams.get('delegation_end'))),draft[1]);
 // Pointer capture keeps resize active above/below the overview. Passing its
 // edges clamps the moved end, without shifting the end we did not drag.
 const overview=await page.locator('.delegation-overview').boundingBox(),heldStart=(await readWindow())[0],endAgain=await end.boundingBox();
 await page.mouse.move(endAgain.x+endAgain.width/2,endAgain.y+endAgain.height/2);await page.mouse.down();await page.mouse.move(overview.x+overview.width+40,overview.y-40);await page.mouse.up();await app.settled();
 assert.deepEqual(await readWindow(),[heldStart,whole[1]]);
 const startAgain=await page.getByRole('slider',{name:'Timeline window start',exact:true}).boundingBox();
 await page.mouse.move(startAgain.x+startAgain.width/2,startAgain.y+startAgain.height/2);await page.mouse.down();await page.mouse.move(Math.max(0,overview.x-20),overview.y+overview.height+40);await page.mouse.up();await app.settled();
 assert.deepEqual(await readWindow(),whole);
 // A release may carry a newer X coordinate than the last move event.
 await page.evaluate(()=>window.addEventListener('pointerdown',event=>{window.__timelinePointerId=event.pointerId;},{once:true}));
 const terminalStart=await page.getByRole('slider',{name:'Timeline window start',exact:true}).boundingBox();
 await page.mouse.move(terminalStart.x+terminalStart.width/2,terminalStart.y+terminalStart.height/2);await page.mouse.down();
 await page.locator('.delegation-overview').evaluate((element,point)=>element.dispatchEvent(new PointerEvent('pointerup',{bubbles:true,pointerId:window.__timelinePointerId,clientX:point.x,clientY:point.y})),{x:overview.x+overview.width*.25,y:overview.y+overview.height+40});
 await page.mouse.up();await app.settled();assert.ok((await readWindow())[0]>whole[0]);assert.equal((await readWindow())[1],whole[1]);
 const beforeCancel=path(page),cancelStart=await page.getByRole('slider',{name:'Timeline window start',exact:true}).boundingBox();
 await page.mouse.move(cancelStart.x+cancelStart.width/2,cancelStart.y+cancelStart.height/2);await page.mouse.down();await page.mouse.move(cancelStart.x+35,cancelStart.y+60);await page.keyboard.press('Escape');await page.mouse.up();await app.settled();
 assert.equal(path(page),beforeCancel,'cancelling a captured drag leaves its saved window alone');
 await page.getByRole('button',{name:'Reset',exact:true}).click();await app.settled();
 assert.deepEqual(await readWindow(),whole);
 assert.equal(await page.getByRole('status',{name:'Selected timeline window',exact:true}).isVisible(),false);
 const reset=await open(t,{url:path(page),ready:'.lt-caller-row'});await reset.settled();
 assert.deepEqual(await readWindow(reset.page),whole);
});

test('Timeline grows with its rows and scrolls in the page instead of a nested vertical viewport',{skip},async t=>{
 const base=defaultApi['GET /api/work/:id/delegation']({query:new URLSearchParams()});
 const rows=Array.from({length:35},(_,i)=>({...base.rows[0],id:i?'many-'+i:'s-root',outline:String(i+1),order:i,depth:0,child_count:0}));
 const api={'GET /api/work/:id/delegation':{...base,rows,focus:rows,session_count:rows.length,total:rows.length}};
 const app=await open(t,{url:`/work/${workID}?explorer=timeline`,api,ready:'.lt-caller-row'});await app.settled();
 assert.ok(await app.page.locator('.delegation-lane-scroll').evaluate(element=>element.scrollHeight<=element.clientHeight+1),'the chart must not create its own vertical scroll viewport');
 await app.page.locator('[data-session-id="many-34"]').scrollIntoViewIfNeeded();
 assert.equal(await app.page.locator('[data-session-id="many-34"]').isVisible(),true);
});

test('timeline zoom retains sessions and blanks out-of-window usage',{skip},async t=>{
 const base=defaultApi['GET /api/work/:id/delegation']({query:new URLSearchParams()}), start=base.start;
 const rows=base.rows.map(row=>row.id==='s-root'?row:{...row,end:start+30000,subtree_end:start+30000});
 const api={
  'GET /api/work/:id/delegation':()=>({...base,rows,focus:rows}),
  'GET /api/work/:id/parallel':({query})=>{
   const data=defaultApi['GET /api/work/:id/parallel']({query});
   return {...data,lanes:data.lanes.map(lane=>lane.session==='s-root'?{...lane,marks:[{...lane.marks[0],start:start+160000,end:start+180000}]}:Number(query.get('start'))>start+30000?{...lane,messages:[],marks:[],model_inferred:[],agent_events:[]}:lane)};
  },
 };
 const params=new URLSearchParams({explorer:'timeline',delegation_start:String(start+150000),delegation_end:String(start+200000)});
 const app=await open(t,{url:`/work/${workID}?${params}`,api,viewport:{width:1440,height:900},ready:'.lt-caller-row'}),{page}=app;
 await page.locator('.delegation-lane-scroll[aria-busy="false"]').waitFor();await app.settled();
 for(const id of ['s-root','s-child','s-nested'])assert.equal(await page.locator(`.lt-recursive-lane[data-session-id="${id}"]`).isVisible(),true);
 assert.equal(await page.locator('.lt-recursive-lane[data-session-id="s-child"] .lt-session-empty-root').count(),1);
 assert.equal(await page.locator('.lt-recursive-lane[data-session-id="s-root"] [data-tool-id="tool-read"]').count(),1);
 await app.shot('timeline-empty-sessions');
 await page.getByRole('tab',{name:'Hierarchy & usage',exact:true}).click();await page.locator('.delegation-table tbody tr').first().waitFor();await app.settled();
 const table=page.locator('.delegation-table');
 assert.equal(await table.locator('[data-session-id="s-child"]').count(),1);
 assert.ok((await table.locator('[data-session-id="s-root"] td').nth(1).textContent()).trim());
 assert.deepEqual(await table.locator('[data-session-id="s-child"] td[title="Outside the selected timeline window"]').allTextContents(),Array(await table.locator('thead th').count()-2).fill(''));
 await app.shot('hierarchy-empty-sessions');
 await table.getByRole('button',{name:'Open session Review implementation',exact:true}).click();await page.locator('.delegation-inbox-row[data-session-id="s-child"]').waitFor();await app.settled();
 const sidebar=page.locator('.delegation-inbox-row[data-session-id="s-child"]');
 assert.deepEqual(await sidebar.locator('.delegation-inbox-metric').allTextContents(),['','']);
 await page.getByRole('button',{name:'Open workspace overview',exact:true}).first().click();
 await page.getByRole('tab',{name:'Timeline',exact:true}).click();await page.getByRole('button',{name:'Reset',exact:true}).click();
 await page.locator('.lt-recursive-lane[data-session-id="s-child"] [data-tool-id="tool-read"]').first().waitFor({state:'attached'});
 await page.getByRole('tab',{name:'Hierarchy & usage',exact:true}).click();await app.settled();
 assert.ok((await page.locator('.delegation-table [data-session-id="s-child"] td').nth(1).textContent()).trim(),'reset restores usage');
});

test('Timeline tool hover selects the recorded call on each overlapping track',{skip},async t=>{
 const api={'GET /api/work/:id/parallel':({query})=>{
  const data=defaultApi['GET /api/work/:id/parallel']({query});
  return {...data,lanes:data.lanes.map(lane=>lane.session!=='s-root'?lane:{...lane,marks:[lane.marks[0],{...lane.marks[1],start:lane.marks[0].start+20000,end:lane.marks[0].end-10000}]})};
 }};
 const app=await open(t,{url:`/work/${workID}?explorer=timeline`,api,ready:'.lt-session-root'}),{page}=app;
 const root=page.locator('[data-session-id="s-root"] .lt-session-root > summary');
 await root.scrollIntoViewIfNeeded();
 for(const [id,preview] of [['tool-read','/repo/README.md'],['tool-error','npm test']]){
  const point=await root.locator(`[data-tool-id="${id}"] line`).evaluate(line=>{
   const transform=line.getScreenCTM(),point=new DOMPoint((Number(line.getAttribute('x1'))+Number(line.getAttribute('x2')))/2,Number(line.getAttribute('y1'))).matrixTransform(transform);
   return {x:point.x,y:point.y};
  });
  await page.mouse.move(point.x,point.y);
  const content=page.getByRole('tooltip').locator('pre');await content.waitFor();
  assert.equal(await content.textContent(),preview);
 }
});

test('Timeline Key menus filter ribbons and restore selections with keyboard access',{skip},async t=>{
 const app=await open(t,{url:`/work/${workID}?explorer=timeline`,ready:'.lt-timeline-legend .usage-legend-item'}),{page}=app;
 await app.settled();
 const key=page.locator('.lt-timeline-legend'),button=name=>key.getByRole('button',{name,exact:true});
 const choices=button('Choices'),menu=page.getByRole('menu',{name:'Filter Choices',exact:true});
 const choose=async(name,label)=>{await button(name).click();await page.getByRole('menuitem',{name:label,exact:true}).click();await app.settled();};
 const isolate=async name=>{await button(name).click();await page.getByRole('menuitem',{name:/^(Hide others|Show and Hide Others)$/}).click();await app.settled();};
 await choices.focus();await choices.press('ArrowDown');
 assert.equal(await menu.getByRole('menuitem',{name:'Hide',exact:true}).evaluate(el=>el===document.activeElement),true);
 await app.shot('legend-menu-visible');
 await menu.getByRole('menuitem',{name:'Hide',exact:true}).click();await app.settled();
 await page.locator('.lt-session-root [data-agent-event="e-s-root"]').waitFor({state:'detached'});
 assert.ok(await page.locator('.lt-session-root [data-agent-event="e-s-root-open"]').count());
 await choose('Read / search','Hide');
 await page.locator('.lt-session-root [data-tool-id="tool-read"]').waitFor({state:'detached'});
 assert.ok(await page.locator('.lt-session-root [data-tool-id="tool-error"]').count());
 await choices.click();await app.shot('legend-menu-hidden');
 await menu.getByRole('menuitem',{name:'Show',exact:true}).click();await app.settled();
 await page.locator('.lt-session-root [data-agent-event="e-s-root"]').first().waitFor();
 assert.equal(await page.locator('.lt-session-root [data-tool-id="tool-read"]').count(),0,'Show retains other hidden values');
 await isolate('Sender category');
 await button('Message modes category').click();await app.shot('legend-menu-mixed');
 await page.getByRole('menuitem',{name:'Show 1 value',exact:true}).click();await app.settled();
 await choose('Plan','Hide');
 await choose('Message modes category','Hide 1 value');
 await page.waitForFunction(()=>!document.querySelector('.lt-session-root [data-agent-event],.lt-session-root [data-tool-id]'));
 await choose('Choices','Show All');
 await choose('Choices','Hide');await choose('Read / search','Hide');await choose('Human typed','Hide');await choose('Sender category','Hide');
 const saved=path(page),fresh=await open(t,{url:saved,ready:'.lt-timeline-legend .usage-legend-item'});await fresh.settled();
 const restored=fresh.page.locator('.lt-timeline-legend');
 for(const [name,pressed] of [['Choices','false'],['Friction','true'],['Read / search','false'],['Error','true'],['Human typed','false'],['Likely pasted','true'],['Sender category','false'],['Receiver category','true']]){
  assert.equal(await restored.getByRole('button',{name,exact:true}).getAttribute('aria-pressed'),pressed,name);
 }
 assert.deepEqual(JSON.parse(new URL(fresh.page.url()).searchParams.get('timeline_visibility')),JSON.parse(new URL(page.url()).searchParams.get('timeline_visibility')));
 await page.setViewportSize({width:390,height:900});await choices.click();
 const bounds=await menu.boundingBox();assert.ok(bounds.x>=0&&bounds.x+bounds.width<=390&&bounds.y>=0&&bounds.y+bounds.height<=900);
 await page.keyboard.press('Escape');assert.equal(await choices.evaluate(el=>el===document.activeElement),true);
 await choose('Choices','Show All');
 await page.locator('.lt-session-root [data-tool-id="tool-read"]').first().waitFor({state:'attached'});
 await page.locator('.lt-session-root [data-agent-event="e-s-root"]').first().waitFor();
 await isolate('Tool actions category');
 await page.waitForFunction(()=>!document.querySelector('.lt-session-root [data-agent-event]'));
 assert.ok(await page.locator('.lt-session-root [data-tool-id="tool-error"]').count());
 await isolate('Agent events category');
 await page.waitForFunction(()=>!document.querySelector('.lt-session-root [data-tool-id]'));
 await page.locator('.lt-session-root [data-agent-event="e-s-root"]').first().waitFor();
 for(const name of ['Message modes category','Caller text category'])await isolate(name);
 await choose('Caller text category','Hide');
 await choose('Tool actions category','Show');await choose('Agent events category','Show');
 const layers=await open(t,{url:path(page),ready:'.lt-timeline-legend .usage-legend-item'});await layers.settled();
 for(const [name,pressed] of [['Agent events category','true'],['Tool actions category','true'],['Message modes category','false'],['Caller text category','false']]){
  assert.equal(await layers.page.getByRole('button',{name,exact:true}).getAttribute('aria-pressed'),pressed,name);
 }
 await choose('Tool actions category','Show All');
 await app.shot('timeline-key-visibility');
 // Compact keeps every visual key entry, its menu and immediate hover name.
 const toggle=()=>key.locator('.lt-key-disclosure > summary');
 await toggle().click();await app.settled();
 assert.equal(await key.locator('.lt-key-disclosure').evaluate(el=>el.open),false);
 for(const name of ['Implement','Plan','Friction','Choices','Read / search','Error','Human typed','Likely pasted']){
  const icon=button(name);assert.ok(await icon.isVisible(),name);
  assert.equal(await icon.innerText(),'','compact icons retain accessible names without visible labels');
 }
 await choices.hover();await page.getByRole('tooltip').getByText('Choices',{exact:true}).waitFor();
 await choices.focus();await choices.press('ArrowDown');await menu.waitFor();
 assert.equal(await page.getByRole('tooltip').count(),0,'keyboard menus also dismiss the hover name');
 await page.keyboard.press('Escape');
 await choices.hover();await page.getByRole('tooltip').getByText('Choices',{exact:true}).waitFor();
 await choices.click();await menu.waitFor();
 assert.equal(await page.getByRole('tooltip').count(),0,'opening a menu dismisses the hover name');
 await page.getByRole('menuitem',{name:'Hide',exact:true}).click();await app.settled();
 await page.locator('.lt-session-root [data-agent-event="e-s-root"]').waitFor({state:'detached'});
 await choices.focus();await choices.press('ArrowDown');await menu.waitFor();
 await page.keyboard.press('Escape');assert.equal(await choices.evaluate(el=>el===document.activeElement),true);
 const eventHints=button('Agent events category');
 await eventHints.first().hover();await page.getByRole('tooltip').getByText('Arc back to the earlier text it is about',{exact:true}).waitFor();
 await eventHints.nth(1).hover();await page.getByRole('tooltip').getByText('On the axis: no target found',{exact:true}).waitFor();
 await eventHints.first().click();await page.getByRole('menu',{name:'Filter Agent events category',exact:true}).waitFor();
 await page.keyboard.press('Escape');
 await app.shot('timeline-key-compact');
 const compact=await open(t,{url:path(page),ready:'.lt-timeline-legend'});await compact.settled();
 const compactKey=compact.page.locator('.lt-timeline-legend');
 assert.equal(await compactKey.locator('.lt-key-disclosure').evaluate(el=>el.open),false);
 assert.equal(await compactKey.getByRole('button',{name:'Choices',exact:true}).getAttribute('aria-pressed'),'false');
 const selected=new URL(compact.page.url()).searchParams.get('timeline_visibility');
 await compactKey.locator('.lt-key-disclosure > summary').click();await compact.settled();
 await compactKey.getByRole('button',{name:'Sender category',exact:true}).waitFor();
 assert.equal(new URL(compact.page.url()).searchParams.get('timeline_visibility'),selected,'expanding preserves visibility choices');
 await compact.page.reload();await compact.settled();
 await compactKey.getByRole('button',{name:'Sender category',exact:true}).waitFor();
 // Restoring compact on the mounted page and returning through history agrees with reloads.
 const compactURL=new URL(compact.page.url());
 const disclosures=JSON.parse(compactURL.searchParams.get('disclosures'));disclosures['timeline-key']=false;
 compactURL.searchParams.set('disclosures',JSON.stringify(disclosures));
 await compact.page.evaluate(url=>{history.pushState(null,'',url);window.dispatchEvent(new PopStateEvent('popstate'));},compactURL.href);
 await compact.page.waitForFunction(()=>document.querySelector('.lt-key-disclosure')?.open===false);
 await compact.page.goBack();await compact.page.waitForFunction(()=>document.querySelector('.lt-key-disclosure')?.open===true);
 await compact.page.goForward();await compact.page.waitForFunction(()=>document.querySelector('.lt-key-disclosure')?.open===false);
});

test('Timeline segments pin exact content before revealing their conversation source',{skip},async t=>{
 const app=await open(t,{url:`/work/${workID}?explorer=timeline`,ready:'.lt-session-root'}),{page}=app;
 const root=page.locator('[data-session-id="s-root"] .lt-session-root > summary'),pinned=page.getByRole('group',{name:'Ribbon segment details',exact:true});
 await root.scrollIntoViewIfNeeded();await app.settled();
 const point=await root.locator('[data-tool-id="tool-read"] line').evaluate(line=>{const p=new DOMPoint((Number(line.getAttribute('x1'))+Number(line.getAttribute('x2')))/2,Number(line.getAttribute('y1'))).matrixTransform(line.getScreenCTM());return {x:p.x,y:p.y};});
 const before=path(page);await page.mouse.move(point.x,point.y);await page.getByRole('tooltip').locator('pre').waitFor();
 await page.mouse.click(point.x,point.y);await pinned.waitFor();
 assert.equal(path(page),before,'pinning does not navigate or toggle disclosures');
 assert.equal(await pinned.locator('pre').textContent(),'/repo/README.md');
 await page.mouse.move(20,80);assert.equal(await pinned.isVisible(),true);
 const link=pinned.getByRole('link'),url=new URL(await link.getAttribute('href'),'http://localhost');
 assert.equal(url.searchParams.get('conversation'),rootConversation);assert.equal(url.searchParams.get('message'),'m-1');assert.equal(url.searchParams.get('call'),'tool-read');
 await page.keyboard.press('Escape');await pinned.waitFor({state:'hidden'});
 // Agent event marks share that hit test and pin: a click on the mark itself.
 const mark=await root.locator('[data-agent-event="e-s-root"]').boundingBox();
 await page.mouse.click(mark.x+mark.width/2,mark.y+mark.height/2);await pinned.waitFor();
 assert.equal(path(page),before,'pinning an event does not navigate or toggle disclosures');
 const eventLink=pinned.getByRole('link',{name:/Show both in conversation/});
 assert.equal(new URL(await eventLink.getAttribute('href'),'http://localhost').searchParams.get('reader_event_link'),'e-s-root');
 await page.keyboard.press('Escape');await pinned.waitFor({state:'hidden'});
 // Keyboard inspection pins the focused message and makes the source link reachable.
 const caller=page.locator('.lt-caller-row').getByRole('button',{name:/Inspect Events ribbon/});await caller.focus();await caller.press('Enter');await pinned.waitFor();
 assert.equal(await pinned.getByRole('link').first().evaluate(el=>el===document.activeElement),true);
 assert.match(await pinned.locator('blockquote').first().textContent(),/Implement/);
 await page.keyboard.press('Escape');await pinned.waitFor({state:'hidden'});
 assert.equal(await caller.evaluate(el=>el===document.activeElement),true);
 // A brush still changes the time window instead of pinning a card.
 const fullEnd=await page.getByRole('slider',{name:'Timeline window end',exact:true}).getAttribute('aria-valuenow');
 const ribbon=await root.locator('.lt-session-ribbon').boundingBox();await page.mouse.move(ribbon.x+ribbon.width*.1,ribbon.y+2);await page.mouse.down();await page.mouse.move(ribbon.x+ribbon.width+20,ribbon.y+ribbon.height+50);await page.mouse.up();await app.settled();
 assert.notEqual(path(page),before);assert.equal(await pinned.isVisible(),false);
 assert.equal(Number(new URL(page.url()).searchParams.get('delegation_end')),Number(fullEnd),'off-canvas releases still finish at the clock endpoint');
 // Selecting a segment's source reveals it, retaining the timeline in its sidebar preview.
 await page.getByRole('button',{name:'Reset',exact:true}).click();await app.settled();
 await page.mouse.click(point.x,point.y);await pinned.waitFor();
 await page.getByRole('tab',{name:'Timeline',exact:true}).click();await pinned.waitFor({state:'hidden'});
 await page.mouse.click(point.x,point.y);await pinned.waitFor();await pinned.getByRole('link').first().click();
 await page.locator('.conversation-panel [data-message-id="m-1"]').first().waitFor();await app.settled();
 assert.equal(new URL(page.url()).searchParams.get('reveal'),'message:m-1');
 assert.equal(await page.getByRole('button',{name:'Open full-width timeline',exact:true}).isVisible(),true);
 assert.equal(await pinned.isVisible(),false);
});

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
 const messages=navigation.getByRole('link',{name:'Messages',exact:true});
 assert.equal(await messages.isVisible(),false);
 await toggle('Conversations & agent sessions');await messages.waitFor();
 const fresh=await open(t,{url:path(page),viewport,ready:'.usage-dashboard .usage-chart'});
 const restored=fresh.page.getByRole('complementary',{name:'Analyze navigation',exact:true});
 assert.equal(await restored.getByRole('link',{name:'Human Words',exact:true}).isVisible(),false);
 assert.equal(await restored.getByRole('link',{name:'Tokens and Cost',exact:true}).isVisible(),true);
 assert.equal(await restored.getByRole('link',{name:'Messages',exact:true}).isVisible(),true);
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
 const refreshed=page.waitForResponse(response=>new URL(response.url()).pathname==='/api/query/mcp_calls/query-v3'&&response.request().postDataJSON()?.rows);
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
 await fresh.page.getByRole('button',{name:'Close All Turns',exact:true}).click();
 assert.ok(await fresh.page.locator('.turn').evaluateAll(turns=>turns.length>1&&turns.every(turn=>!turn.open)));
 await fresh.page.getByRole('button',{name:'Open All Turns',exact:true}).click();
 assert.ok(await fresh.page.locator('.turn').evaluateAll(turns=>turns.length>1&&turns.every(turn=>turn.open)));
});

test('sub-agent assignments and recorded output restore their disclosures', {skip},async t=>{
 const event=(id,kind,text,extra={})=>({id,native_id:id,role:'assistant',kind,text:typeof text==='string'?text:JSON.stringify(text),created_at:'2026-10-01T00:00:00Z',...extra});
 const messages=[event('prompt','message','Review the change',{role:'user'}),
  event('m-15','delegation',{name:'Task',input:{subagent_type:'Reviewer',description:'Review the cache',prompt:'Inspect the cache and report the result.'}},{call_id:'child'}),
  event('child-reply','message','The cache is safe.',{parent_native_id:'child'}),
  event('result','delegation_result',{content:'Async agent launched successfully. Internal launch metadata.'},{call_id:'child'})];
 const api={'GET /api/work/:id/conversations/:conversation':request=>({...defaultApi['GET /api/work/:id/conversations/:conversation'](request),messages})};
 const first=await open(t,{url:`/work/${workID}?conversation=${rootConversation}`,api,ready:'.subagent-run'}),{page}=first;
 const run=page.locator('.subagent-run').first(),summary=run.locator(':scope > summary');
 assert.match(await summary.innerText(),/Reviewer.*Review the cache/);
 assert.doesNotMatch(await summary.innerText(),/Async agent launched/);
 await summary.click();
 await run.locator('.subagent-assignment').getByText('Inspect the cache and report the result.',{exact:true}).waitFor();
 await run.getByText('The cache is safe.',{exact:true}).waitFor();
 assert.equal(await run.getByText('Async agent launched successfully. Internal launch metadata.',{exact:true}).isVisible(),false);
 await first.shot('subagent-expanded');
 await run.getByText('Recorded output and metadata',{exact:true}).click();
 await run.getByText('Async agent launched successfully. Internal launch metadata.',{exact:true}).waitFor();
 await page.waitForFunction(()=>JSON.parse(new URLSearchParams(location.search).get('reader_expanded')||'[]').length>=2);
 const fresh=await open(t,{url:path(page),api,ready:'.subagent-run'});
 await fresh.page.getByText('The cache is safe.',{exact:true}).waitFor();
 await fresh.page.getByText('Async agent launched successfully. Internal launch metadata.',{exact:true}).waitFor();
 assert.equal(await fresh.page.evaluate(()=>document.activeElement===document.body),true);
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

test('right-click and Escape clear linked highlights, with a saved shortcut preference', {skip},async t=>{
 const app=await open(t,{url:'/settings/preferences',ready:'#escapeClearsHighlights'}),{page}=app;
 const toggle=page.getByRole('switch',{name:'Escape clears highlights'});
 assert.equal(await toggle.getAttribute('aria-checked'),'true');
 await page.evaluate(()=>{
  const target=document.createElement('p');target.id='attention-fixture';target.textContent='Attention fixture';document.querySelector('#optionalBehaviors').append(target);
 });
 const mark=async()=>{
  await page.evaluate(()=>{const url=new URL(location.href);url.searchParams.set('reveal','section:attention-fixture');url.searchParams.append('highlight','page');history.replaceState(history.state,'',url);window.dispatchEvent(new Event('pharos:uri-changed'));});
  await page.waitForFunction(()=>document.querySelector('#attention-fixture')?.classList.contains('pharos-reveal'));
 };
 const cleared=async()=>page.waitForFunction(()=>!new URLSearchParams(location.search).has('highlight')&&!new URLSearchParams(location.search).has('reveal')&&!document.querySelector('.pharos-reveal,.pharos-highlight'));
 await mark();
 await page.locator('#attention-fixture').click({button:'right'});
 await page.getByRole('button',{name:'Clear highlights',exact:true}).last().click();await cleared();
 await mark();
 await page.locator('#settingsPageHeading').click({button:'right'});
 await page.getByRole('button',{name:'Clear highlights',exact:true}).last().click();await cleared();
 await mark();
 await page.locator('#feedbackContactEmail').focus();await page.keyboard.press('Escape');
 assert.equal(new URL(page.url()).searchParams.has('reveal'),true,'editing keeps its Escape behavior');
 await page.evaluate(()=>{document.activeElement.blur();const dialog=document.createElement('dialog');dialog.id='attention-dialog';document.body.append(dialog);dialog.showModal();});
 await page.keyboard.press('Escape');await page.waitForFunction(()=>!document.querySelector('#attention-dialog').open);
 assert.equal(new URL(page.url()).searchParams.has('reveal'),true,'closing a dialog does not also clear attention');
 await page.evaluate(()=>document.activeElement.blur());await page.keyboard.press('Escape');await cleared();
 await toggle.click();assert.equal(await toggle.getAttribute('aria-checked'),'false');
 await mark();await page.locator('#settingsPageHeading').click();await page.keyboard.press('Escape');
 assert.equal(new URL(page.url()).searchParams.has('reveal'),true,'disabled shortcut preserves attention');
 await page.reload();await page.locator('#escapeClearsHighlights').waitFor();
 assert.equal(await toggle.getAttribute('aria-checked'),'false','the preference survives reload');
 await page.keyboard.press('Escape');assert.equal(new URL(page.url()).searchParams.has('reveal'),true);
 await toggle.click();await page.locator('#settingsPageHeading').click();await page.keyboard.press('Escape');await cleared();
});

test('agent event links restore both ends from the URL, opening folded prose without taking focus', {skip},async t=>{
 const filler='Some context about the async work that the person wants reviewed. '.repeat(14),prompt=`${filler}Please keep the \`drawer only\` removal flow working on phones.`;
 // Both ends cross inline code: each must stay one highlight around it.
 const reply='I read `drawer only` as the bottom sheet. Done.',quote='`drawer only` removal',start=prompt.indexOf(quote);
 const messages=[
  {id:'link-prompt',native_id:'link-prompt',role:'user',kind:'message',text:prompt,created_at:new Date(1760000000000).toISOString()},
  {id:'link-reply',native_id:'link-reply',role:'assistant',kind:'message',text:reply,created_at:new Date(1760000001000).toISOString(),
   agent_events:[{id:'e-link',kind:'assumption',label:'Assumption',group:'Choices you may not see',probability:0.9,sentence:[0,reply.indexOf(' Done.')],subject:[],response:[],
    target:{method:'quote',message_id:'link-prompt',role:'user',kind:'message',start,end:start+quote.length,text:quote,strong:true}}]},
 ];
 const api={'GET /api/work/:id/conversations/:conversation':request=>({...defaultApi['GET /api/work/:id/conversations/:conversation'](request),messages,agent_events:{messages_read:2}})};
 const opened=await open(t,{api,url:`/work/${workID}?conversation=${rootConversation}&reader_events=1&reader_event_link=e-link`,ready:'.conversation-panel.show-events mark.event-target.link-selected'}),{page}=opened;
 await opened.settled();
 // The target sits past the fold of a long prompt; restoring the link opens it.
 assert.equal(await page.locator('mark.event-target.link-selected').first().evaluate(mark=>mark.closest('.message-prose').classList.contains('prose-truncated')),false);
 assert.ok(await page.locator('mark.agent-event.link-selected').count());
 for(const selector of ['mark.agent-event','mark.event-target'])assert.deepEqual(await page.locator(selector).evaluateAll(marks=>marks.map(mark=>mark.querySelectorAll('code').length)),[1],`${selector} is one element around the inline code`);
 assert.equal(await page.evaluate(()=>document.activeElement===document.body||!document.activeElement),true);
 assert.ok(await page.locator('.event-link-gutter path.is-selected').count(),'the gutter connects the two ends');
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
 // site_link composes view search into native host state; collection row requests carry it.
 // Initial metadata can precede restoration; identity reads are independent of the collection.
 assert.deepEqual(JSON.parse(new URL(page.url()).searchParams.get('text_builder')),JSON.parse(original.hostState.params.text_builder[0]));
 const requests=app.calls.filter(call=>call.method==='POST'&&call.path.startsWith('/api/query/library/')&&call.body?.rows&&!call.body?.source?.where?.some(filter=>filter.field==='id'&&filter.op==='='));
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
 const api={'* /api/query/tool_calls/:operation':({params,body,query})=>answerQuery(params.operation,body,rows,schema,{now,id:query.get('id')}),'GET /api/tool-calls/:id':({params})=>rows.find(row=>row.id===params.id)??{}};
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
 const keys=['sync:history','sync:sources','sync:repairs','sync:diagnostics','sync:forecast:capture-forecast','background:labels','background:agent_events'],ready='#syncDiagnosticTables details';
 const route=entries=>'/settings/sync?'+new URLSearchParams({disclosures:JSON.stringify(entries)});
 const entries=Object.fromEntries(keys.map(key=>[key,true])),defaults=Object.fromEntries(keys.map(key=>[key,['sync:history','sync:sources'].includes(key)]));
 const app=await open(t,{url:route(entries),ready}),{page}=app;
 const state=(browser,expected)=>browser.waitForFunction(expected=>Object.entries(expected).every(([key,open])=>{
  const element=[...document.querySelectorAll('[data-view-key],[data-pharos-target]')].find(element=>element.dataset.viewKey===key||element.dataset.pharosTarget==='section:'+key);
  return element&&(element.tagName==='DETAILS'?element.open:element.getAttribute('aria-expanded')==='true')===open;
 }),expected);
 await state(page,entries);
 const first=page.locator('[data-view-key="'+keys[0]+'"],[data-pharos-target="section:'+keys[0]+'"]');
 if(await first.evaluate(element=>element.tagName==='DETAILS'))await first.locator('summary').first().click();else await first.click();
 await state(page,{...entries,[keys[0]]:false});
 await page.waitForFunction(key=>JSON.parse(new URLSearchParams(location.search).get('disclosures')||'{}')[key]===false,keys[0]);await app.settled();
 const fresh=await open(t,{url:path(page),ready});
 await state(fresh.page,{...entries,[keys[0]]:false});
 await fresh.page.evaluate(()=>window.pharosSync.refresh());
 await state(fresh.page,{...entries,[keys[0]]:false});
 await page.evaluate(url=>window.pharosNavigate(url),route({}));
 await state(page,defaults);
 await page.goBack();await state(page,{...entries,[keys[0]]:false});
 await page.goForward();await state(page,defaults);
 assert.equal(app.calls.some(call=>call.method!=='GET'&&/^\/api\/(background\/(settings|run)|sync\/|cloud-backup\/(schedule|start))/.test(call.path)),false,'restoring sections must not run work or save settings');
 // Revealing a closed accordion highlights its summary without overriding explicit false.
 const closed={'sync:history':false,'sync:sources':false,'sync:diagnostics':false};
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
 await control('Model').selectOption('gpt-6.1-sol');await control('Breakdown').selectOption('model_speed');await app.settled();
 const chart=page.locator('.thinking-brush').first();await chart.scrollIntoViewIfNeeded();const rect=await chart.boundingBox();
 await page.mouse.move(rect.x+rect.width*.3,rect.y+rect.height*.4);await page.mouse.down();await page.mouse.move(rect.x+rect.width*.65,rect.y+rect.height*.4,{steps:8});await page.mouse.up();
 await page.waitForFunction(()=>JSON.parse(new URLSearchParams(location.search).get('thinking')).range==='custom');await app.settled();
 const selected=state();assert.ok(selected.from&&selected.to);assert.ok(['custom','custom-times'].includes(await control('Range').inputValue()));
 const fresh=await open(t,{url:path(page),ready});
 assert.deepEqual(JSON.parse(new URL(fresh.page.url()).searchParams.get('thinking')),selected);
 const detailTopic=topic();
 await page.locator('.thinking-links').getByRole('link',{name:'Waiting →',exact:true}).click();await page.locator(ready).waitFor();await app.settled();
 const overviewTopic=topic();assert.notEqual(overviewTopic,detailTopic);
 assert.equal(state().from,selected.from);
 assert.equal(await control('Breakdown').inputValue(),'model_speed');
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
 assert.equal(await control('Breakdown').inputValue(),'model_speed');
 await page.goForward();await page.waitForFunction(topic=>new URLSearchParams(location.search).get('analyze_topic')===topic,overviewTopic);await page.locator(ready).waitFor();
 assert.equal(state().from,selected.from);
 assert.equal(await control('Breakdown').inputValue(),'model_speed');
 await control('Breakdown').selectOption('model_effort_speed');await app.settled();
 assert.ok(app.calls.some(call=>call.path==='/api/thinking'&&call.query.get('overview')==='1'&&call.query.get('groupBy')==='model_effort_speed'));
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

test('Message Labels category menus apply to descendants and restore mixed selection with keyboard access',{skip},async t=>{
 const now=Date.now(),at=new Date(now-86400000).toISOString(),schema=loadSchema(messageLabelsDocument);
 const labels=['C.difficulty','C.difficulty.blocker','C.difficulty.ambiguity','C.report.finding','P.request.work.implement'];
 const rows=labels.map((label,i)=>({id:'category-'+i,message_id:'message-'+i,conversation_id:'conversation',occurred_at:at,day:at.slice(0,10),label,family:label.split('.').slice(0,2).join('.'),function_role:label.startsWith('P')?'Sender':'Receiver'}));
 const api={'* /api/query/message_labels/:operation':({params,body,query})=>answerQuery(params.operation,body,rows,schema,{now,id:query.get('id')})};
 const app=await open(t,{url:'/analyze?'+new URLSearchParams({analyze_topic:'labels',analyze_chart:JSON.stringify({view:'trend',metric:'messages',split:'label'})}),api,ready:'.usage-chart-legend'}),{page}=app;
 const category=page.getByRole('button',{name:'Difficulty category',exact:true});
 await category.focus();await category.press('ArrowDown');
 const menu=page.getByRole('menu',{name:'Filter Difficulty category',exact:true});
 assert.equal(await menu.getByRole('menuitem',{name:'Hide',exact:true}).evaluate(node=>node===document.activeElement),true);
 await menu.getByRole('menuitem',{name:'Hide others',exact:true}).click();await app.settled();
 assert.equal(await category.getAttribute('aria-pressed'),'true');
 assert.equal(await page.getByRole('button',{name:'Receiver category',exact:true}).getAttribute('aria-pressed'),'mixed');
 await page.getByRole('button',{name:'Missing context',exact:true}).click();
 await page.getByRole('menuitem',{name:'Hide',exact:true}).click();await app.settled();
 assert.equal(await category.getAttribute('aria-pressed'),'mixed');
 await category.click();await menu.getByRole('menuitem',{name:'Show 1 value',exact:true}).click();await app.settled();
 assert.equal(await category.getAttribute('aria-pressed'),'true');
 await category.click();await menu.getByRole('menuitem',{name:'Show All',exact:true}).click();await app.settled();
 await category.click();await menu.getByRole('menuitem',{name:'Hide',exact:true}).click();await app.settled();
 assert.equal(await category.getAttribute('aria-pressed'),'false');
 const saved=path(page),fresh=await open(t,{url:saved,api,ready:'.usage-chart-legend'});
 assert.equal(await fresh.page.getByRole('button',{name:'Difficulty category',exact:true}).getAttribute('aria-pressed'),'false');
 assert.equal(await fresh.page.getByRole('button',{name:'Receiver category',exact:true}).getAttribute('aria-pressed'),'mixed');
 await page.setViewportSize({width:390,height:900});await category.click();
 const bounds=await menu.boundingBox();assert.ok(bounds.x>=0&&bounds.x+bounds.width<=390&&bounds.y>=0&&bounds.y+bounds.height<=900);
 await page.keyboard.press('Escape');assert.equal(await category.evaluate(node=>node===document.activeElement),true);
});

test('Habits switches views and restores the selected chart and its controls', {skip}, async t => {
 const app=await open(t,{url:'/analyze?analyze_topic=habits',ready:'.habits-calendar'}),{page}=app;
 const view=page.getByRole('combobox',{name:'View',exact:true});
 await page.getByRole('combobox',{name:'Whose days',exact:true}).selectOption('agents');
 await page.getByRole('combobox',{name:'Calendar range',exact:true}).selectOption('13');
 await page.getByRole('combobox',{name:'Streaks count',exact:true}).selectOption('weekdays');
 await view.selectOption('punchcard');
 await page.locator('[data-pharos-target="section:habits-punchcard"]').waitFor();
 await page.getByRole('combobox',{name:'Punchcard time',exact:true}).selectOption('unattended');
 await page.getByRole('combobox',{name:'Punchcard range',exact:true}).selectOption('30');
 await view.selectOption('days');await page.locator('.habits-days-chart').waitFor();
 await page.getByRole('combobox',{name:'Your days range',exact:true}).selectOption('14');
 await page.locator('.habits-settings > summary').click();
 await page.waitForFunction(()=>JSON.parse(new URLSearchParams(location.search).get('disclosures')??'{}')['habits-settings']===true);await app.settled();
 const fresh=await open(t,{url:path(page),ready:'.habits-days-chart'});
 assert.equal(await fresh.page.getByRole('combobox',{name:'View',exact:true}).inputValue(),'days');
 assert.equal(await fresh.page.getByRole('combobox',{name:'Your days range',exact:true}).inputValue(),'14');
 assert.equal(await fresh.page.locator('.habits-settings').evaluate(element=>element.open),true);
 const restored=fresh.page.getByRole('combobox',{name:'View',exact:true});
 await restored.selectOption('punchcard');await fresh.page.locator('[data-pharos-target="section:habits-punchcard"]').waitFor();
 assert.equal(await fresh.page.getByRole('combobox',{name:'Punchcard time',exact:true}).inputValue(),'unattended');
 assert.equal(await fresh.page.getByRole('combobox',{name:'Punchcard range',exact:true}).inputValue(),'30');
 await restored.selectOption('calendar');await fresh.page.locator('.habits-calendar').waitFor();
 assert.equal(await fresh.page.getByRole('combobox',{name:'Whose days',exact:true}).inputValue(),'agents');
 assert.equal(await fresh.page.getByRole('combobox',{name:'Calendar range',exact:true}).inputValue(),'13');
 assert.equal(await fresh.page.getByRole('combobox',{name:'Streaks count',exact:true}).inputValue(),'weekdays');
 await fresh.shot('habits-restored');
});

test('Habits standalone charts preview, pin, restore and open their records',{skip},async t=>{
 const app=await open(t,{url:'/analyze?analyze_topic=habits&habits_view=days',ready:'.habits-days-chart [role="button"]'}),{page}=app;
 const tip=page.locator('.habits-chart-popover');
 const focus=()=>page.evaluate(()=>document.activeElement?.getAttribute('aria-label')??document.activeElement?.tagName);
 const charts=[
  '.habits-days-chart',
  '.habits-calendar',
  '[data-pharos-target="section:habits-punchcard"] .habits-chart-inspection',
  '.habits-automations',
 ];
 for(const selector of charts){
  await page.getByRole('combobox',{name:'View',exact:true}).selectOption(selector.includes('days-chart')?'days':selector.includes('calendar')?'calendar':'punchcard');
  const mark=page.locator(selector).getByRole('button').first();
  await mark.scrollIntoViewIfNeeded();
  const before=path(page),focused=await focus();
  await mark.hover();await tip.waitFor();
  assert.equal(path(page),before,'hover leaves the URL alone');
  assert.equal(await focus(),focused,'hover leaves focus alone');
  assert.equal(await tip.getByRole('link').count(),0);
  await mark.click();await tip.getByRole('link').first().waitFor();await app.settled();
  const url=path(page),name=await mark.getAttribute('aria-label');
  const fresh=await open(t,{url,ready:'.habits-chart-popover.pinned a'});
  const restoredMark=fresh.page.locator(selector).getByRole('button',{name,exact:true});
  assert.equal(await restoredMark.getAttribute('aria-pressed'),'true');
  for(const key of ['Enter','Space']){
   await restoredMark.focus();await restoredMark.press(key);await fresh.settled();
   assert.equal(await fresh.page.locator('.habits-chart-popover.pinned').getByRole('link').first().evaluate(node=>node===document.activeElement),true,`${selector}: ${key} reaches the detail link of a restored pin`);
  }
  await page.mouse.move(1,1);assert.ok(await tip.isVisible(),'a pin survives leaving the mark');
  await page.keyboard.press('Escape');await tip.waitFor({state:'hidden'});
 }
 await page.setViewportSize({width:320,height:800});
 await page.getByRole('combobox',{name:'View',exact:true}).selectOption('days');
 const mark=page.locator('.habits-days-chart').getByRole('button').first();
 await mark.scrollIntoViewIfNeeded();await mark.focus();await mark.press('Enter');
 await tip.getByRole('link').first().waitFor();
 assert.equal(await tip.getByRole('link').first().evaluate(node=>node===document.activeElement),true,'keyboard inspection reaches the detail link');
 const box=await tip.boundingBox();assert.ok(box.x>=0&&box.y>=0&&box.x+box.width<=320&&box.y+box.height<=800);
 await app.shot('daily-schedule-details');
 await page.keyboard.press('Escape');await tip.waitFor({state:'hidden'});
 assert.equal(await mark.evaluate(node=>node===document.activeElement),true,'Escape returns focus to the inspected mark');
 await mark.press('Space');await tip.getByRole('link').first().waitFor();
 await tip.getByRole('link').first().click();await page.locator('.usage-workspace-table').waitFor();await app.settled();
 assert.equal(new URL(page.url()).searchParams.get('analyze_data'),'rhythm');
 assert.equal(new URL(page.url()).searchParams.get('analyze_mode'),'query');
 await page.getByRole('link',{name:/^Back to dashboard/}).click();await page.locator('.habits-days-chart').waitFor();
 assert.equal(await page.getByRole('combobox',{name:'View',exact:true}).inputValue(),'days');
});

test('native Message Labels dashboards and conversation cards share standard navigation and saved query presentation',{skip},async t=>{
 const definitions=new Map(),api={'PUT /api/query-snapshots':({body})=>{definitions.set(body.code,body);return body},'GET /api/query-snapshots':({query})=>definitions.get(query.get('code'))};
 const read=page=>{const token=new URL(page.url()).searchParams.get('q_message_labels');return decodeQueryStrict(token?.startsWith('@')?definitions.get(token.slice(1)).token:token)};
 const app=await open(t,{api,url:'/analyze?label_lab=distribution&label_audience=thinking',ready:'.usage-labels-dashboard'}),{page}=app;
 assert.equal(new URL(page.url()).searchParams.get('analyze_topic'),'labels');assert.equal(new URL(page.url()).searchParams.has('label_lab'),false);
 const view=page.getByRole('combobox',{name:'View',exact:true});
 assert.equal(await view.inputValue(),'trend_line');
 await view.selectOption({label:'Over time - bar'});await app.settled();
 assert.equal(JSON.parse(new URL(page.url()).searchParams.get('analyze_chart')).view,'trend_bar');
 await page.locator('.usage-chart-legend').getByRole('button',{name:'Report',exact:true}).click();
 await page.getByRole('menuitem',{name:'Hide',exact:true}).click();await app.settled();
 assert.equal(JSON.parse(new URL(page.url()).searchParams.get('analyze_chart')).selection.excluded,true);
 await page.getByRole('button',{name:'Show all',exact:true}).click();await app.settled();
 const point=page.locator('.usage-chart-bars').getByRole('button',{name:/Report/}).first();await point.focus();await point.press('Enter');
 await page.locator('.usage-chart-tip.pinned').waitFor();assert.ok(await page.locator('.usage-chart-tip.pinned a').count());
 await page.keyboard.press('Escape');
 const plot=await page.locator('.usage-chart-bars').boundingBox();await page.mouse.move(plot.x+plot.width*.2,plot.y+plot.height/2);await page.mouse.down();await page.mouse.move(plot.x+plot.width*.7,plot.y+plot.height/2);await page.mouse.up();await app.settled();
 assert.ok(new URL(page.url()).searchParams.get('analyze_filters'));
 await page.getByRole('combobox',{name:'Breakdown',exact:true}).selectOption('label');
 await page.getByRole('combobox',{name:'Show as',exact:true}).selectOption('values');
 await page.getByRole('combobox',{name:'Scale',exact:true}).selectOption('logarithmic');await app.settled();
 const copy=await open(t,{api,url:path(page),ready:'.usage-labels-dashboard'});assert.equal(await copy.page.getByRole('combobox',{name:'Breakdown',exact:true}).inputValue(),'label');
 assert.equal(await copy.page.getByRole('combobox',{name:'View',exact:true}).inputValue(),'trend_bar');
 await copy.page.getByRole('combobox',{name:'View',exact:true}).selectOption({label:'Over time - line'});await copy.settled();
 const line=await open(t,{api,url:path(copy.page),ready:'.usage-labels-dashboard'});assert.equal(await line.page.getByRole('combobox',{name:'View',exact:true}).inputValue(),'trend_line');
 await page.getByRole('link',{name:/Matching conversations/}).click();await page.locator('.message-label-result-card').first().waitFor();await app.settled();
 const native=read(page);assert.ok(native.grouping);assert.ok(native.grouping.source.where.some(term=>term.field==='source'&&term.value==='thinking'));
 await page.getByRole('button',{name:'Table',exact:true}).click();await page.locator('.qt-row').first().waitFor();await app.settled();
 const rowsURL=path(page),fresh=await open(t,{api,url:rowsURL,ready:'.qt-row'});assert.equal(await fresh.page.getByRole('button',{name:'Table',exact:true}).getAttribute('aria-pressed'),'true');
 await page.getByRole('button',{name:'Cards',exact:true}).click();await page.locator('.message-label-result-card').first().waitFor();await app.settled();
 const cards=read(page);assert.deepEqual(cards.grouping.source.where,native.grouping.source.where);
 assert.ok(await page.locator('.message-label-result-card .message-label-fingerprint img').count());
 const link=await page.locator('.message-label-source').first().getAttribute('href');assert.ok(link.includes('/conversation/')||link.includes('/work/'));
});

test('compact fingerprint PNGs load and navigate to the detailed conversation with the keyboard',{skip},async t=>{
 const app=await open(t,{url:'/analyze?analyze_mode=query&analyze_data=message_labels&label_results=cards',ready:'.message-label-fingerprint img',viewport:{width:390,height:480}}),{page}=app;
 const image=page.locator('.message-label-result-card .fingerprint-image img').first();
 await image.scrollIntoViewIfNeeded();
 await page.waitForFunction(()=>{const img=document.querySelector('.message-label-result-card .fingerprint-image img');return img?.complete&&img.naturalWidth>0;});
 const link=page.locator('.message-label-result-card .fingerprint-image').first().getByRole('link',{name:'Open conversation',exact:true});
 await link.focus();await link.press('Enter');
 await page.locator('.conversation-panel').waitFor();await app.settled();
 assert.equal(new URL(page.url()).searchParams.get('conversation'),rootConversation);
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
 const questions=page.locator('details').filter({has:page.getByText('Questions for each notice',{exact:true})});
 await questions.locator('summary').click();
 await page.waitForFunction(()=>JSON.parse(new URLSearchParams(location.search).get('disclosures') || '{}')['safety-check:permissions:questions'] === true);
 await page.reload(); await page.waitForFunction(()=>document.querySelector('.optimize-table-questions')?.open);
});


test('Compaction chart controls and source query restore in a fresh browser', {skip}, async t => {
 const ready = '.usage-compaction-dashboard section[aria-label="Over time"][aria-busy="false"] .usage-query-destinations';
 const app = await open(t, {url:'/analyze?analyze_topic=compaction&analyze_data=compactions', ready}), {page} = app;
 const control = name => page.locator('.usage-compaction-dashboard').getByRole('combobox', {name, exact:true});
 await control('Breakdown').selectOption('trigger_provider'); await app.settled();
 await control('Range').selectOption('30d'); await app.settled();
 await control('View').selectOption('compare');
 await page.locator('.usage-analysis-dashboard[aria-busy="false"]').waitFor(); await app.settled();
 const saved = path(page);
 const fresh = await open(t, {url:saved, ready:'.usage-analysis-dashboard[aria-busy="false"] .usage-query-destinations'});
 assert.equal(await fresh.page.getByRole('combobox', {name:'Breakdown', exact:true}).inputValue(), 'trigger_provider');
 assert.equal(await fresh.page.getByRole('combobox', {name:'View', exact:true}).inputValue(), 'compare');
 const source = fresh.page.locator('.usage-query-destination').first();
 await source.click(); await fresh.page.locator('.usage-workspace-table .qt-row').first().waitFor(); await fresh.settled();
 assert.equal(new URL(fresh.page.url()).searchParams.get('analyze_data'), 'compactions');
 await app.shot('compaction-compare');
});


test('Data Guide terms and example restore and return focus',{skip},async t=>{
 const app=await open(t,{url:'/library',ready:'.pharos-query-page .view-heading'}),{page}=app;
 const trigger=page.getByRole('button',{name:'Data Guide',exact:true});
 await trigger.click();
 const guide=page.getByRole('dialog',{name:'Data Guide',exact:true});
 await guide.waitFor();
 const appsBranch=guide.getByText('Apps & machines',{exact:true});
 await appsBranch.click();
 await page.waitForFunction(()=>JSON.parse(new URLSearchParams(location.search).get('nav_expanded'))?.['data-guide']?.apps===false);
 await guide.getByRole('button',{name:'Work items',exact:true}).click();
 await page.waitForFunction(()=>new URLSearchParams(location.search).get('guide_entity')==='work_items');
 await page.reload();
 await guide.waitFor();
 assert.equal(await appsBranch.evaluate(element=>element.closest('details').open),false,'shared guide menu expansion did not restore');
 assert.equal(await guide.getByRole('button',{name:'Work items',exact:true}).getAttribute('aria-current'),'page');
 await guide.getByRole('button',{name:'Conversations',exact:true}).click();
 await guide.getByRole('button',{name:'Conversation Review',exact:true}).click();
 await page.waitForFunction(()=>new URLSearchParams(location.search).get('guide_example')==='review');
 await guide.getByRole('button',{name:'Repositories',exact:true}).click();
 await app.settled();
 await app.shot('data-guide');
 const linked=path(page);
 const fresh=await open(t,{url:linked,viewport:{width:320,height:820},ready:'.data-guide[open]'});
 const mobileGuide=fresh.page.getByRole('dialog',{name:'Data Guide',exact:true});
 assert.equal(await mobileGuide.getByRole('combobox',{name:'Guide term',exact:true}).inputValue(),'repositories');
 const picker=mobileGuide.getByRole('combobox',{name:'Guide term',exact:true});
 await picker.selectOption('machines');
 await fresh.page.waitForFunction(()=>new URLSearchParams(location.search).get('guide_entity')==='machines');
 assert.equal(await mobileGuide.evaluate(el=>el.scrollWidth<=el.clientWidth+1),true);
 await fresh.shot('data-guide-narrow');
 const olderLink=new URL(fresh.page.url());olderLink.searchParams.set('guide_entity','execution_machines');
 await fresh.page.goto(olderLink.href);
 await mobileGuide.waitFor();
 assert.equal(await picker.inputValue(),'machines','older execution-machine links open the combined Machines topic');
 // Switching illustration types retains the linked example conversation.
 await picker.selectOption('conversations');
 assert.equal(await mobileGuide.getByRole('button',{name:'Conversation Review',exact:true}).getAttribute('aria-pressed'),'true');
 for(const term of ['skills','compactions','tokens']){
  await picker.selectOption(term);
  await fresh.page.waitForFunction(value=>new URLSearchParams(location.search).get('guide_entity')===value,term);
  assert.equal(await mobileGuide.getByRole('button',{name:'Conversation Review',exact:true}).getAttribute('aria-pressed'),'true');
 }
 await picker.selectOption('agents');
 await fresh.page.waitForFunction(()=>new URLSearchParams(location.search).get('guide_entity')==='agents');
 await fresh.page.reload();
 await mobileGuide.waitFor();
 assert.equal(await picker.inputValue(),'agents');
 await mobileGuide.getByRole('button',{name:'Explain tool calls',exact:true}).first().click();
 await fresh.page.waitForFunction(()=>new URLSearchParams(location.search).get('guide_entity')==='tool_calls');
 await fresh.page.reload();
 await mobileGuide.waitFor();
 assert.equal(await picker.inputValue(),'tool_calls');
 await mobileGuide.getByRole('button',{name:'Conversation Implementation',exact:true}).click();
 await fresh.page.waitForFunction(()=>new URLSearchParams(location.search).get('guide_example')!=='review');
 await guide.getByRole('button',{name:'Close data guide',exact:true}).click();
 await trigger.click();
 await page.keyboard.press('Escape');
 await page.waitForFunction(()=>!document.querySelector('.data-guide').open);
 assert.equal(await trigger.evaluate(el=>document.activeElement===el),true);
});

test('message card labels and sentence events restore independently in the reader',{skip},async t=>{
 const at='2026-11-05T14:00:00Z',prompt='Please review the importer and keep the existing CSV format. Show the evidence before changing the parser.',thought='I should compare the parser with the saved fixtures first. The import fails when a quoted cell contains a newline. I will preserve that cell and add a focused check.'+' Additional context.'.repeat(30),reply='The parser now keeps quoted newlines inside their cells. The existing fixtures pass, and the added check covers the failing import.';
 const labeled=(id,role,kind,text,label,segment,seconds)=>({id,native_id:id,role,kind,text,created_at:new Date(Date.parse(at)+seconds*1000).toISOString(),labels:{ids:[label],probs:{[label]:.94},segment}});
 const messages=[labeled('tint-prompt','user','message',prompt,'P.request.work.implement','H-typed',0),labeled('tint-thinking','assistant','reasoning',thought,'C.deliberation.plan','TH',10),labeled('tint-reply','assistant','message',reply,'C.report.verification','AR',25)];
 const sentence='The import fails when a quoted cell contains a newline.';
 messages[1].agent_events=[{id:'e-tint',kind:'obstacle',label:'Obstacle or roadblock',group:'Friction',probability:.91,sentence:[thought.indexOf(sentence),thought.indexOf(sentence)+sentence.length],subject:[],response:[]}];
 const taxonomy=[{id:'P.request.work.implement',name:'Implement',definition:'Requests a concrete change.'},{id:'C.deliberation.plan',name:'Plan',definition:'Considers how to do the work.'},{id:'C.report.verification',name:'Verification',definition:'Reports checks and their evidence.'}];
 const api={'GET /api/work/:id':request=>({...defaultApi['GET /api/work/:id'](request),title:'Review the CSV importer'}),'GET /api/work/:id/conversations/:conversation':request=>({...defaultApi['GET /api/work/:id/conversations/:conversation'](request),title:'Review the CSV importer',first_prompt:prompt,agent_nickname:'Review the CSV importer',messages,labels:{labeled:3,pending:false,taxonomy},agent_events:{messages_read:2}})};
 api['GET /api/work/:id/delegation']=request=>{
  const data=defaultApi['GET /api/work/:id/delegation'](request);
  const named=row=>row.conversation_id===rootConversation?{...row,label:'Review the CSV importer'}:row;
  return {...data,rows:data.rows.map(named),focus:data.focus.map(named)};
 };
 const url=`/work/${workID}?conversation=${rootConversation}&reader_filter_level=all&reader_labels=1&reader_events=1&reader_writing=0`;
 const app=await open(t,{api,url,ready:'.conversation-panel.show-labels.show-events'}),{page}=app;
 const reasoning=page.locator('.action-row details').filter({has:page.locator('summary .activity-title',{hasText:'Reasoning'})}).first();
 await reasoning.locator('summary').click();
 await page.locator('.thinking-card mark.agent-event').waitFor();
 assert.equal(await page.getByText(sentence,{exact:false}).count()>0,true);
 const card=page.locator('.thinking-card'),tip=page.getByRole('tooltip');
 await card.hover({position:{x:3,y:3}});await tip.waitFor();
 assert.match(await tip.textContent(),/Plan/);
 await card.locator('.message-prose').hover();assert.equal(await tip.isVisible(),false,'message text does not show the card label');
 await card.locator('mark.agent-event').hover();assert.match(await tip.textContent(),/Obstacle/,'highlighted sentence still shows its event');
 await card.hover({position:{x:3,y:3}});await tip.waitFor();
 await page.mouse.down();assert.equal(await tip.isVisible(),false,'pressing to select hides the tooltip');
 await card.locator('.message-prose').hover();await card.hover({position:{x:3,y:3}});
 assert.equal(await tip.isVisible(),false,'dragging does not reopen the tooltip');await page.mouse.up();
 await card.locator('.message-toggle').hover();assert.equal(await tip.isVisible(),false,'card controls do not show the label');
 await card.locator('.message-toggle').click();assert.equal(await card.locator('.message-toggle').getAttribute('aria-expanded'),'true');
 const human=page.locator('.speaker-row.human[data-label-family]').first();
 await human.hover({position:{x:3,y:3}});await tip.waitFor();assert.match(await tip.textContent(),/Implement/);
 await human.locator('.message-prose').hover();assert.equal(await tip.isVisible(),false);
 await page.mouse.move(0,0);
 await app.shot('message-card-tints-dark');
 await page.getByRole('checkbox',{name:'Message labels',exact:true}).uncheck();await app.settled();
 assert.equal(new URL(page.url()).searchParams.get('reader_labels'),'0');
 await page.getByRole('checkbox',{name:'Message labels',exact:true}).check();
 await page.getByRole('checkbox',{name:'Agent events',exact:true}).uncheck();await app.settled();
 const copied=await open(t,{api,url:path(page),ready:'.conversation-panel.show-labels'});
 assert.equal(await copied.page.getByRole('checkbox',{name:'Agent events',exact:true}).isChecked(),false);
 assert.match(await copied.page.locator('.thinking-card').textContent(),/quoted cell/);
 await page.getByRole('checkbox',{name:'Agent events',exact:true}).check();
 await page.evaluate(()=>document.documentElement.dataset.theme='light');
 await app.shot('message-card-tints-light');
 await page.setViewportSize({width:390,height:844});
 await page.locator('.thinking-card').scrollIntoViewIfNeeded();
 await app.shot('message-card-tints-phone');
});

test('People message-label breakdowns keep their dashboard and saved URL',{skip},async t=>{
 const dashboard=topic=>'/analyze?'+new URLSearchParams({analyze_topic:topic,analyze_window:'30d',analyze_filters:encodeQuery({...EMPTY_QUERY,where:[{field:'repository_name',op:'is_not_null',value:''}]}),analyze_chart:JSON.stringify({metric:'messages',split:'message_family',period:'day',share:true})});
 const app=await open(t,{url:dashboard('human'),ready:'.usage-human-dashboard .usage-chart'}),{page}=app;
 await app.shot('people-message-label-breakdown');
 for(const topic of ['human','activity','work_hours']){
  if(topic!=='human')await page.goto(new URL(dashboard(topic),page.url()).href);
  await page.locator('.usage-'+topic.replaceAll('_','-')+'-dashboard .usage-chart').waitFor();await app.settled();
  await page.getByRole('combobox',{name:'Breakdown',exact:true}).selectOption('message_label');await app.settled();
  assert.equal(JSON.parse(new URL(page.url()).searchParams.get('analyze_chart')).split,'message_label');
  assert.ok(app.calls.some(call=>call.path.includes('/api/query/people_message_labels/')));
  await page.locator('.usage-query-destination').first().click();
  const back=page.getByRole('link',{name:'Back to dashboard',exact:true});await back.waitFor();await back.click();
  await page.locator('.usage-'+topic.replaceAll('_','-')+'-dashboard .usage-chart').waitFor();await app.settled();
  assert.equal(new URL(page.url()).searchParams.get('analyze_window'),'30d');
  assert.equal(await page.getByRole('combobox',{name:'Breakdown',exact:true}).inputValue(),'message_label');
 }
 await page.reload();await page.locator('.usage-work-hours-dashboard .usage-chart').waitFor();await app.settled();
 assert.equal(await page.getByRole('combobox',{name:'Breakdown',exact:true}).inputValue(),'message_label');
});


test('component share charts restore their controls across reloads and view switches',{skip},async t=>{
 const words=Object.fromEntries(Object.entries({typed:40,pasted:22,quoted:12,resent:8,template:6,attachment:2,harness:7,automated:3}).map(([key,value])=>[key+'_words',value]));
 const rows=queryFixtureDatasets.writing_messages.map(row=>({...row,...words,words:100,copied_words:20,prompt_words:8,machine_words:10}));
 const api={'POST /api/query/writing_messages/query-v3':({body})=>answerQuery('query-v3',body,rows,loadSchema(writingMessagesDocument))};
 const app=await open(t,{api,url:'/analyze?analyze_topic=human',viewport:{width:1920,height:1050},ready:'.usage-human-dashboard .usage-chart'}),{page}=app;
 const control=name=>page.locator('.usage-human-dashboard').getByRole('combobox',{name,exact:true});
 await control('View').selectOption('compare');
 await control('Metric').selectOption('words');
 await control('Breakdown').selectOption('category');
 await control('Show as').selectOption('share');
 await page.locator('.usage-analysis-dashboard[aria-busy="false"]').waitFor();await app.settled();
 const saved=path(page);
 await page.getByRole('button',{name:/^Typed:/}).waitFor();
 await app.shot('human-text-category-shares');
 const fresh=await open(t,{api,url:saved,ready:'.usage-analysis-dashboard[aria-busy="false"] .usage-query-destinations'});
 for(const [name,value] of [['View','compare'],['Metric','words'],['Breakdown','category'],['Show as','share']])assert.equal(await fresh.page.getByRole('combobox',{name,exact:true}).inputValue(),value);
 await fresh.page.getByRole('combobox',{name:'View',exact:true}).selectOption('trend');
 await fresh.page.locator('.usage-chart-bars:not(.loading) > button').first().waitFor();await fresh.settled();
 await fresh.page.getByRole('combobox',{name:'View',exact:true}).selectOption('compare');
 await fresh.page.locator('.usage-analysis-dashboard[aria-busy="false"]').waitFor();await fresh.settled();
 assert.equal(await fresh.page.getByRole('combobox',{name:'Breakdown',exact:true}).inputValue(),'category');
 assert.equal(await fresh.page.getByRole('combobox',{name:'Show as',exact:true}).inputValue(),'share');
 await fresh.page.getByRole('button',{name:/^Typed:/}).click();
 await fresh.page.waitForURL(url=>url.searchParams.get('analyze_mode')==='query');await fresh.settled();
 const tokenAPI={'POST /api/query/usage/query-v3':({body})=>answerQuery('query-v3',body,queryFixtureDatasets.usage.map(row=>({...row,input_tokens:100,uncached_input_tokens:20,cache_read_input_tokens:70,cache_creation_input_tokens:10})),loadSchema(usageDocument))};
 const tokens=await open(t,{api:tokenAPI,url:'/analyze?analyze_topic=tokens&analyze_chart='+encodeURIComponent(JSON.stringify({view:'compare',metric:'input',split:'input_type',compareAs:'share'})),viewport:{width:1920,height:1050},ready:'.usage-analysis-dashboard[aria-busy="false"] .usage-query-destinations'});
 await tokens.page.getByRole('button',{name:/^Cache reads:/}).waitFor();
 await tokens.shot('input-token-shares');
});


test('dataset query ideas restore their query and disclosure, and All records clears the population',{skip},async t=>{
 const app=await open(t,{url:'/analyze?analyze_mode=query&analyze_data=tool_calls',ready:'.usage-workspace-table .qt-row'});
 const {page}=app;
 const idea=page.getByRole('button',{name:/^Command families/,includeHidden:true});
 const section=page.locator('.usage-ideas-section').filter({has:idea});
 if(await section.getAttribute('open')===null)await section.locator('summary').click();
 await idea.click();
 const selected=await page.waitForFunction(()=>{
  const token=new URLSearchParams(location.search).get('q_tool_calls');
  return token&&document.querySelector('.usage-idea[aria-pressed="true"]')?token:null;
 });
 const query=decodeQueryStrict(await selected.jsonValue());
 assert.ok(query.grouping);
 assert.deepEqual(query.grouping.keys,['operation_key','operation_name']);
 await section.locator('summary').click();
 const disclosureKey=(await section.getAttribute('data-pharos-target')).slice('section:'.length);
 await page.waitForFunction(key=>JSON.parse(new URLSearchParams(location.search).get('disclosures')||'{}')[key]===false,disclosureKey);
 const saved=path(page);
 await page.reload();
 await page.waitForSelector('.usage-workspace-table .qt-row');
 assert.equal(await section.getAttribute('open'),null);
 assert.equal(decodeQueryStrict(new URL(page.url()).searchParams.get('q_tool_calls')).grouping.keys[0],'operation_key');
 const groupedToken=new URL(page.url()).searchParams.get('q_tool_calls');
 await page.getByRole('button',{name:'All records',exact:true}).click();
 await page.waitForFunction(token=>new URLSearchParams(location.search).get('q_tool_calls')!==token,groupedToken);
 const reset=decodeQueryStrict(new URL(page.url()).searchParams.get('q_tool_calls'));
 assert.equal(reset.grouping,undefined);
 assert.deepEqual(reset.where,[]);
 assert.deepEqual(reset.hostState?.params??{},{});
 assert.notEqual(path(page),saved);
 const record=page.getByRole('button',{name:/^Failed calls/,includeHidden:true});
 const recordsSection=page.locator('.usage-ideas-section').filter({has:record});
 if(await recordsSection.getAttribute('open')===null)await recordsSection.locator('summary').click();
 await record.click();
 await page.waitForFunction(()=>[...document.querySelectorAll('.usage-idea')].some(button=>button.querySelector('strong')?.textContent==='Failed calls'&&button.getAttribute('aria-pressed')==='true'));
 await app.settled();
 await page.reload();
 await page.waitForSelector('.usage-workspace-table .qt-row');
 assert.equal(await record.getAttribute('aria-pressed'),'true','a record idea stays selected after native evidence and Detail columns are restored');
});

test('scatter axes search by dashboard, retain keyboard focus and restore their committed choices',{skip},async t=>{
 const app=await open(t,{url:'/analyze?analyze_topic=scatter&analyze_data=scatter',ready:'.usage-scatter-dashboard'}),{page}=app;
 const x=page.getByRole('combobox',{name:'X axis',exact:true});
 await x.fill('cache write');
 await page.getByRole('listbox',{name:'X axis metrics'}).getByRole('option').first().waitFor();
 await x.press('Enter');
 assert.equal(await x.evaluate(node=>node===document.activeElement),true);
 await page.waitForFunction(()=>JSON.parse(new URLSearchParams(location.search).get('analyze_chart')).metric==='usage_cache_creation_input_tokens');
 const saved=path(page);
 await page.reload();
 await page.getByRole('combobox',{name:'X axis',exact:true}).waitFor();
 assert.equal(await page.getByRole('combobox',{name:'X axis',exact:true}).inputValue(),'Cache write tokens');
 const y=page.getByRole('combobox',{name:'Y axis',exact:true});
 await y.fill('session shape');
 await page.getByRole('listbox',{name:'Y axis metrics'}).getByRole('group',{name:'Session Shape',exact:true}).waitFor();
 await y.press('Escape');
 assert.equal(path(page),saved,'typing and cancelling a search leaves the chart URL alone');
 await page.getByLabel('Count each',{exact:true}).selectOption('agent_session');
 await page.waitForFunction(()=>JSON.parse(new URLSearchParams(location.search).get('analyze_chart')).unit==='agent_session');
 const fresh=await open(t,{url:path(page),ready:'.usage-scatter-dashboard'});
 assert.equal(await fresh.page.getByLabel('Count each',{exact:true}).inputValue(),'agent_session');
 await fresh.page.getByLabel('Count each',{exact:true}).selectOption('message');
 await fresh.page.waitForFunction(()=>JSON.parse(new URLSearchParams(location.search).get('analyze_chart')).unit==='message');
 await app.settled();await fresh.settled();
});
