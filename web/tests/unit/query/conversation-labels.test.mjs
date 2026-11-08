import assert from 'node:assert/strict';
import {messageLabelFingerprintCache,messageLabelRibbonNotes} from '../../../src/message-label-results-model';
import {test} from 'node:test';
import {EMPTY_QUERY,loadSchema,readFieldValue,toServerQuery,toQueryRequestV3,encodeQuery,decodeQueryStrict} from '@pythia-software/query-table-core';
import {withFingerprintColumn} from '../../../src/fingerprint-columns';
import {schemaDocuments} from '../../../src/schema-documents';

test('fingerprint columns request stable entity identities even as the only selected column',()=>{
 for(const [dataset,identity,renderer] of [['library','id','workspace_fingerprint'],['writing','id','workspace_fingerprint'],['usage','conversation_id','label_fingerprint'],['writing_messages','conversation_id','label_fingerprint'],['tool_calls','conversation_id','label_fingerprint'],['tl1_attempts','workspace_id','workspace_fingerprint']]){
  const original=loadSchema(schemaDocuments[dataset]),schema=withFingerprintColumn(original,dataset),field=schema.fields.find(f=>f.name==='fingerprint');
  assert.equal(field.render,renderer,dataset);
  assert.equal(readFieldValue(field,{id:'row',conversation_id:'conversation',workspace_id:'workspace'}),identity==='id'?'row':identity==='conversation_id'?'conversation':'workspace');
  assert.equal(readFieldValue(field,{id:'unrelated-row'}),identity==='id'?'unrelated-row':undefined,'never use an event ID as a conversation ID');
  const query={...EMPTY_QUERY,select:[{field:'fingerprint',width:180}],orderBy:[]};
  const projected=toServerQuery(query,schema).select;
  assert.ok(projected.includes(identity),dataset);
  assert.ok(!projected.includes('fingerprint'),'render-only field never reaches SQL');
  const request=toQueryRequestV3(query,schema,{version:3,profile:'qt-sqlite-v1',groupedRows:true,computedSourceFilters:true});
  assert.deepEqual(request.diagnostics??[],[],dataset);
  assert.deepEqual(decodeQueryStrict(encodeQuery(query)).select,query.select);
  assert.equal(withFingerprintColumn(schema,dataset),schema);
  assert.deepEqual(schema.defaultSelect,original.defaultSelect);
  const workspace=schema.fields.find(field=>field.name==='workspace_fingerprint');
  if(identity==='conversation_id'&&original.fields.some(field=>field.name==='workspace_id')){
   assert.equal(readFieldValue(workspace,{id:'event',workspace_id:'work'}),'work');
   const workspaceQuery={...query,select:[{field:'workspace_fingerprint'}]};
   assert.ok(toServerQuery(workspaceQuery,schema).select.includes('workspace_id'));
  }
 }
 const labels=loadSchema(schemaDocuments.message_labels);
 const enriched=withFingerprintColumn(labels,'message_labels');
 assert.equal(enriched.fields.find(field=>field.name==='fingerprint'),labels.fields.find(field=>field.name==='fingerprint'),'keep the existing label grouping key');
 assert.ok(enriched.fields.some(field=>field.name==='workspace_fingerprint'));
 for(const dataset of ['tools','mcp_calls']){
  const schema=loadSchema(schemaDocuments[dataset]);
  assert.equal(withFingerprintColumn(schema,dataset),schema,'aggregate or caller records have no source fingerprint');
 }
});

test('fingerprint requests share work, retain recently visited pages and reload evicted conversations',async()=>{
 const calls=[],cache=messageLabelFingerprintCache(async id=>{calls.push(id);return id;},2);
 const a=cache.read('a');assert.equal(cache.read('a'),a);await a;
 await cache.read('b');await cache.read('a');await cache.read('c');
 await cache.read('a');await cache.read('b');
 assert.deepEqual(calls,['a','b','c','b']);
});

test('fingerprint refresh invalidates off-screen entries and failed reads can retry',async()=>{
 let version=0,failed=true;
 const cache=messageLabelFingerprintCache(async id=>{if(id==='retry'&&failed)throw new Error('Unavailable');return version;});
 assert.equal(await cache.read('offscreen'),0);version=1;cache.clear();
 assert.equal(await cache.read('offscreen'),1);
 await assert.rejects(cache.read('retry'),/Unavailable/);failed=false;
 assert.equal(await cache.read('retry'),1);
});

test('a pre-refresh fingerprint failure leaves the replacement request cached',async()=>{
 let rejectFirst,requests=0;
 const cache=messageLabelFingerprintCache(()=>++requests===1?new Promise((resolve,reject)=>{rejectFirst=reject;}):Promise.resolve('current'));
 const old=cache.read('same');await Promise.resolve();cache.clear();
 const current=cache.read('same');assert.equal(await current,'current');
 rejectFirst(new Error('Old request failed'));await assert.rejects(old);
 assert.equal(cache.read('same'),current);assert.equal(requests,2);
});

import {timelinePreviewText,trimTimelineSnippet} from '../../../src/timeline-preview.ts';

import {nativeTimelineLabels} from '../../../src/label-timeline-overlay.tsx';

import {timelineModeGroups,timelineLegendWindow} from '../../../src/timeline-legend.tsx';
import {timelineValueVisible,timelineSectionVisible,timelineEventVisible,updateTimelineVisibility,visibleTimelineTree} from '../../../src/timeline-visibility';

import {workflowSessions} from '../../../src/conversation-labels.ts';

import {conversationRibbon,fingerprintMessageAt} from '../../../src/conversation-fingerprint-model.ts';

import {agentEventClock,textTime,sessionRibbonTree,ribbonCounts,ribbonEventCount,ribbonHits,ribbonLayers,ribbonToolPlacement,ribbonPointerHits,ribbonSourceURL} from '../../../src/label-session-ribbon-model.ts';
import {overlapping} from '../../../src/ribbon-marks';

import {labelIntervals,recordedToolMarks,activityCategory} from '../../../src/label-session-ribbon-model.ts';

import {stateColor} from '../../../src/conversation-labels.ts';

import {ribbonSegment} from '../../../src/conversation-fingerprint-model.ts';

import {ribbonMessageTitle,parallelEnvelopes,messageAllocation} from '../../../src/conversation-fingerprint-model.ts';
import {eventLinkURL} from '../../../src/event-link-url';
import {ribbonInspectorPlacement} from '../../../src/label-ribbon-inspector';

import {splitTimelineCallers,authorshipMix,authorshipSegments,authorshipExcerpt} from '../../../src/timeline-caller-model.ts';

import {normalizeTimelineColumns,timelineColumnWidth,timelineColumnLayout} from '../../../src/timeline-columns.tsx';

test('native timeline lanes preserve session ownership, empty classifications and pending coverage',()=>{
 const taxonomy=[{id:'C.report.finding',name:'Finding',definition:'A finding'}];
 const native={preview:'Readable preview',preview_truncated:false,id:'message-1',conversation_id:'source-transcript',role:'assistant',kind:'reasoning',created_at:'2026-01-01T00:00:00Z',text:'Observed',model:'test-model',labels:{ids:['C','C.report','C.report.finding'],probs:{'C.report.finding':.9},segment:'AR'}};
 const source=nativeTimelineLabels({taxonomy,lanes:[{session:'main',messages:[native,{...native,id:'no-match',labels:{ids:[],probs:{},segment:'AR'}}]},{session:'child',labels_pending:true,messages:[{...native,id:'pending'}]},{session:'tools-only'}]});
 assert.deepEqual(source.taxonomy,taxonomy);
 assert.deepEqual(source.messages.get('main')[0].labels,[['C',0],['C.report',0],['C.report.finding',.9]]);
 assert.equal(source.messages.get('main')[0].session_id,'main');
 assert.equal(source.messages.get('main')[0].preview,'Readable preview');assert.equal(source.messages.get('main')[0].preview_truncated,false);
 assert.equal(source.messages.get('main')[0].conversation_id,'source-transcript');
 assert.equal(source.messages.get('main')[0].kind,'reasoning');
 assert.equal(source.messages.get('main')[1].chunks,1);
 assert.equal(source.messages.get('child')[0].chunks,0);
 assert.deepEqual(source.messages.get('child')[0].labels,[]);
 assert.deepEqual(source.messages.get('tools-only'),[]);
 assert.equal(nativeTimelineLabels(null).messages.size,0);
});

const message=(id,family,extra={})=>({id,role:'assistant',kind:'message',segment:'AR',labels:family?[[family,.9]]:[],chunks:family?1:0,text:'Test fixture',text_source:'Retained message excerpt',truncated:false,at:null,...extra});

const conversation=(id,messages)=>({id,work_id:'workspace-test',title:'Test fixture',repository:'fixture',provider:'codex',started_at:null,messages});

test('workspace fingerprint event links open the source conversation and preserve the event selection',()=>{
 const event={id:'e7',message_id:'reply'},workspace=conversation('workspace-test',[message('reply','C.report',{conversation_id:'source-conversation'})]);
 const url=new URL(eventLinkURL(workspace,event),'http://pharos.invalid');
 assert.equal(url.pathname,'/work/workspace-test');
 assert.equal(url.searchParams.get('conversation'),'source-conversation');
 assert.equal(url.searchParams.get('message'),'reply');
 assert.equal(url.searchParams.get('reader_events'),'1');
 assert.equal(url.searchParams.get('reader_event_link'),'e7');
 const direct=new URL(eventLinkURL({id:'direct-conversation',work_id:'workspace-test'},event),'http://pharos.invalid');
 assert.equal(direct.pathname,'/work/workspace-test');
 assert.equal(direct.searchParams.get('conversation'),'direct-conversation');
 const inherited=conversation('container',[message('reply','C.report',{conversation_id:'elsewhere'})]);
 const resolved=new URL(eventLinkURL(inherited,event),'http://pharos.invalid');
 assert.equal(resolved.pathname,'/conversation/elsewhere');assert.equal(resolved.searchParams.get('message'),'reply');
 inherited.messages[0].work_id='source-work';
 assert.equal(new URL(eventLinkURL(inherited,event),'http://pharos.invalid').pathname,'/work/source-work');
});

test('timeline legend keeps specific labels with their parent modes without duplicating ancestors',()=>{
 const messages=[message('a','C.report.finding',{labels:[['C',1],['C.report',.95],['C.report.finding',.9]]}),message('b','C.report.progress'),message('duplicate','C.report.finding'),message('request','P.request.work.implement'),message('parent','P.request'),message('generic','C'),message('future','P.custom.detail')];
 const groups=timelineModeGroups(messages,[{id:'P.custom',name:'Custom',definition:'A future mode'}]),byID=id=>groups.find(group=>group.id===id);
 assert.deepEqual(byID('C.report').labels,['C.report.finding','C.report.progress']);
 assert.deepEqual(byID('P.request').labels,['P.request.work.implement']);
 assert.deepEqual(byID('C').labels,[]);
 assert.deepEqual(byID('P.custom').labels,['P.custom.detail']);
 assert.equal(byID('P.custom').name,'Custom');
 assert.ok(groups.indexOf(byID('P.request'))<groups.indexOf(byID('C.report')));
 assert.equal(new Set(groups.flatMap(group=>group.labels)).size,4);
 assert.equal(groups.length,4,'absent families do not enter the legend');
 assert.equal(byID('P.request').direct,true);assert.equal(byID('C.report').direct,false);
});

test('timeline Show and Hide preserve independent hidden values and Show All restores the Key',()=>{
 let state=updateTimelineVisibility({},'events',['choices'],'hide');
 state=updateTimelineVisibility(state,'tools',['error'],'hide');
 assert.equal(timelineValueVisible(state,'events','choices'),false);
 assert.equal(timelineValueVisible(state,'tools','error'),false);
 state=updateTimelineVisibility(state,'events',['choices'],'show');
 assert.equal(timelineValueVisible(state,'events','choices'),true);
 assert.equal(timelineValueVisible(state,'tools','error'),false);
 state=updateTimelineVisibility(state,'events',['learning'],'only');
 assert.equal(timelineSectionVisible(state,'tools'),false);
 assert.equal(timelineValueVisible(state,'events','learning'),true);
 state=updateTimelineVisibility(state,'events',['learning'],'hide');
 assert.equal(timelineValueVisible(state,'events','learning'),false);
 assert.equal(timelineValueVisible(state,'events','future'),false);
 state=updateTimelineVisibility(state,'tools',['read'],'show');
 assert.equal(timelineSectionVisible(state,'tools'),true);
 assert.equal(timelineValueVisible(state,'tools','read'),true);
 assert.equal(timelineValueVisible(state,'tools','error'),false,'showing one value in a hidden section leaves its siblings hidden');
 assert.deepEqual(updateTimelineVisibility(state,'tools',[],'clear'),{});
});

test('timeline section headings isolate whole layers, fill partial selections and restore hidden values',()=>{
 let state={tools:{values:['error'],excluded:true},origins:{values:['typed'],excluded:false}};
 state=updateTimelineVisibility(state,'sections',['tools'],'only');
 assert.deepEqual(state.sections,{values:['tools'],excluded:false});
 assert.equal(state.tools,undefined,'showing a whole section clears its value filter');
 assert.equal(timelineSectionVisible(state,'modes'),false);
 assert.equal(timelineValueVisible(state,'tools','future-tool'),true);
 state=updateTimelineVisibility(state,'sections',['events'],'toggle');
 assert.equal(timelineSectionVisible(state,'tools'),true);
 assert.equal(timelineSectionVisible(state,'events'),true);
 state=updateTimelineVisibility(state,'sections',['events'],'toggle');
 assert.equal(timelineSectionVisible(state,'events'),false);
 state=updateTimelineVisibility(state,'events',['learning'],'show');
 assert.equal(timelineSectionVisible(state,'events'),true,'a hidden label can be selected directly');
 state=updateTimelineVisibility(state,'sections',['events'],'toggle');
 assert.equal(state.events,undefined,'adding a mixed section fills all values');
 assert.equal(timelineSectionVisible(state,'tools'),true,'filling a section retains other selected layers');
 state=updateTimelineVisibility(state,'sections',['events'],'exclude');
 assert.equal(timelineSectionVisible(state,'events'),false);
 assert.equal(timelineSectionVisible(state,'modes'),true);
 state=updateTimelineVisibility(state,'tools',['error'],'only');
 assert.equal(timelineValueVisible(state,'tools','read'),false);
 assert.deepEqual(updateTimelineVisibility(state,'sections',['events'],'clear'),{});
});

test('whole timeline layers filter counts, inspection items and descendants while preserving their clock',()=>{
 const at=n=>new Date(n).toISOString(),messages=[message('caller','P.request',{role:'user',at:at(100),authorship:[{category:'typed',bytes:5,text:'typed'},{category:'pasted',bytes:3,text:'123'}]})];
 const marks=[{start:300,end:450,end_known:true,category:'read',tool_id:'ok',name:'Read',error:false}];
 const events=[{id:'event',message_id:'caller',group:'Friction',label:'Obstacle',source:'reply',target:null}];
 const tree=sessionRibbonTree(messages,marks,[{start:600,end:700}],[],[0,1000],true,true,messages,marks,events),original=structuredClone(tree),walk=node=>[node,...node.children.flatMap(walk)];
 for(const section of ['modes','origins','events','tools']){
  const only=updateTimelineVisibility({},'sections',[section],'only'),shown=visibleTimelineTree(tree,only,true);
  assert.ok(shown.items.length,section);
  const belongs=item=>section==='events'?!!item.event:section==='tools'?!!(item.mark||item.inferred):section==='origins'?!!item.authorship:!!item.label&&!item.authorship;
  for(const node of walk(shown))for(const item of node.items){
   assert.ok(belongs(item),`${section}: ${item.id}`);
   const original=tree.items.find(before=>before.id===item.id);
   assert.equal(item.start,original.start);assert.equal(item.end,original.end);
  }
  const excluded=visibleTimelineTree(tree,updateTimelineVisibility({},'sections',[section],'exclude'),true);
  assert.ok(excluded.items.length);
  for(const node of walk(excluded))assert.ok(node.items.every(item=>!belongs(item)));
  assert.equal(timelineEventVisible(only,events[0]),section==='events','overlay and tree use the same layer selection');
 }
 assert.deepEqual(tree,original,'original observations remain intact');
});

test('timeline visibility filters every ribbon level and event type without reallocating time',()=>{
 const at=n=>new Date(n).toISOString(),messages=[message('a','C.report.finding',{at:at(100)}),message('b','C.report.progress',{at:at(500)}),message('unlabeled',null,{at:at(900)})];
 const marks=[{start:300,end:450,end_known:true,category:'read',tool_id:'ok',name:'Read',error:false},{start:600,end:700,end_known:true,category:'read',tool_id:'failed',name:'Read',error:true}];
 const events=[{id:'choice',message_id:'a',group:'Choices you may not see',label:'Assumption',source:'reply',target:{message_id:'b',method:'quote'}},{id:'friction',message_id:'a',group:'Friction',label:'Obstacle',source:'reply',target:null}];
 const tree=sessionRibbonTree(messages,marks,[],[],[0,1000],true,false,messages,marks,events);
 assert.equal(visibleTimelineTree(tree,{}),tree);
 const state={modes:{values:['C.report.finding'],excluded:true},tools:{values:['error'],excluded:true},events:{values:['friction'],excluded:true}};
 const shown=visibleTimelineTree(tree,state),walk=node=>[node,...node.children.flatMap(walk)];
 for(const node of walk(shown))for(const item of node.items){
  assert.notEqual(item.label,'C.report.finding');assert.notEqual(item.mark?.tool_id,'failed');assert.notEqual(item.event?.id,'friction');
  const original=tree.items.find(before=>before.id===item.id);assert.equal(item.start,original.start);assert.equal(item.end,original.end);
 }
 assert.ok(shown.items.some(item=>item.label==='?'));
 assert.ok(shown.items.some(item=>item.mark?.tool_id==='ok'),'error visibility does not hide successful calls of the same category');
 assert.ok(shown.items.some(item=>item.event?.id==='choice'),'events remain independent of hidden message modes');
 assert.ok(!walk(shown).some(node=>node!==shown&&!node.items.length));
 assert.ok(tree.items.some(item=>item.event?.id==='friction'),'source data remains intact');
 const none=visibleTimelineTree(tree,{modes:{values:['missing'],excluded:false},events:{values:['missing'],excluded:false},tools:{values:['missing'],excluded:false}});
 assert.equal(none.items.length,0);assert.equal(none.children.length,0);
});

test('caller visibility cuts original text runs and intersects mode selections at every level',()=>{
 const caller=message('caller','P.request',{at:new Date(0).toISOString(),authorship:[{category:'typed',bytes:5,text:'typed'},{category:'pasted',bytes:3,text:'123'},{category:'harness',bytes:2,text:'ab'}]});
 const boundary={start:1000,end:1000,end_known:false,category:'other',name:'Boundary',error:false};
 const tree=sessionRibbonTree([caller],[],[],[],[0,1000],true,true,[caller],[boundary]);
 const state={origins:{values:['pasted'],excluded:false}},shown=visibleTimelineTree(tree,state,true),walk=node=>[node,...node.children.flatMap(walk)];
 for(const node of walk(shown)){
  assert.equal(new Set(node.items.map(item=>item.id)).size,node.items.length,'caller tracks are not duplicated');
  for(const item of node.items){assert.equal(item.authorship,'pasted');assert.equal(item.start,500);assert.equal(item.end,800);assert.equal(item.excerpt.text,'123');}
 }
 assert.ok(shown.items.length>0);
 assert.equal(visibleTimelineTree(tree,{...state,modes:{values:['P.request'],excluded:true}},true).items.length,0);
 const agent=sessionRibbonTree([caller],[],[],[],[0,1000],true,false,[caller],[boundary]);
 assert.deepEqual(visibleTimelineTree(agent,state).items,agent.items,'caller source selections leave agent text unchanged');
});

test('recorded descendants stay beside their parent while independent and unresolved branches survive',()=>{
 const c=conversation('tree',[]);c.main_session='main';
 c.sessions=[{id:'main',parent_id:null},{id:'child',parent_id:'main'},{id:'sibling',parent_id:'main'},{id:'grandchild',parent_id:'child'},{id:'orphan',parent_id:'unavailable'},{id:'cycle',parent_id:'cycle'}];
 assert.deepEqual(workflowSessions(c).map(s=>s.id),['main','child','grandchild','sibling','orphan','cycle']);
});

const discoveryTaxonomy=['P','P.request','P.request.work','P.request.work.review','P.request.work.verify','C','C.report','C.report.progress'].map(id=>({id,name:id,definition:id}));

test('session ribbons partition thinking before function roles and keep every coexisting label down to a single observation',()=>{
 const at=n=>new Date(n).toISOString(),normal=message('plain','C.report.product',{at:at(10)}),thinking=message('thought','C.report',{kind:'reasoning',at:at(20),labels:[['C.report.progress',.9],['P.request.work.review',.8]]}),unknown=message('unknown',null,{at:at(30)}),untimed=message('untimed','C.report');
 const tree=sessionRibbonTree([normal,thinking,unknown,untimed],[],[],discoveryTaxonomy,[0,40]);
 assert.deepEqual(ribbonCounts(tree.items),{events:0,messages:3,thinking:1,activity:0,inferred:0});
 assert.equal(ribbonEventCount(tree.items),3);
 const sharedTool={...tree.items[0],id:'shared-tool',message:undefined,mark:{message_id:'plain',count:1}};
 const distinctTool={...sharedTool,id:'new-tool',mark:{message_id:'tool-message',count:1}};
 assert.equal(ribbonEventCount([...tree.items,sharedTool,distinctTool]),4,'labels, origin runs and tools sharing a message do not inflate the event total');
 const labels=tree.children.find(n=>n.id==='labels'),thoughts=labels.children.find(n=>n.id==='labels:thinking');
 assert.ok(thoughts);assert.equal(ribbonCounts(thoughts.items).messages,1);
 assert.deepEqual(thoughts.children.map(n=>n.title),['Sender','Receiver']);
 assert.ok(labels.children.filter(n=>n!==thoughts).every(n=>n.items.every(item=>item.message.id!=='thought')));
 const visit=node=>{if(!node.children.length){assert.equal(node.items.length,1);return;}assert.deepEqual(node.children.flatMap(n=>n.items).map(i=>i.id).sort(),node.items.map(i=>i.id).sort());node.children.forEach(visit);};visit(tree);
 const singleRole=sessionRibbonTree([{...thinking,labels:[['C.report.progress',.9]]}],[],[],discoveryTaxonomy,[0,40]).children[0].children[0];assert.equal(singleRole.title,'Thinking · Receiver');assert.ok(singleRole.children.every(n=>n.title!=='Receiver'));visit(singleRole);
 const generic=sessionRibbonTree([message('generic','P',{at:at(10)}),message('parent','P.request',{at:at(20)})],[],[],discoveryTaxonomy,[0,40]);visit(generic);
});

test('ribbon hit testing includes smaller covered symbols and activity observations without inventing durations',()=>{
 const marks=[{start:0,end:100,end_known:true,name:'Read files',category:'read',count:3,error:false},{start:50,end:90,end_known:false,name:'Recorded delegation',category:'delegate',count:1,error:false}];
 const tree=sessionRibbonTree([message('point','P.request',{at:new Date(50).toISOString()})],marks,[{start:0,end:110}],[],[0,100]);
 const hits=ribbonHits(tree.items,50,1);assert.equal(hits.length,4);assert.equal(hits.find(i=>i.mark?.category==='delegate').end,50);
 const layers=ribbonLayers(hits);assert.equal(layers[0].item.message.id,'point');assert.ok(layers.slice(1).every(layer=>!layer.item.message));assert.deepEqual(new Set(layers.map(layer=>layer.item.id)),new Set(hits.map(item=>item.id)));
 assert.equal(ribbonCounts(tree.items).activity,4);assert.equal(ribbonHits(tree.items,95,1).length,3);
 assert.equal(sessionRibbonTree([],marks,[],[],[101,200]).items.length,0);
});

test('tool dots take free lanes for overlapping calls, hit-test by position and preserve point observations',()=>{
 const marks=[{start:0,end:10,end_known:true,name:'long',category:'run',count:1,error:false,tool_id:'long',preview:'npm test'},{start:1,end:4,end_known:true,name:'first',category:'read',count:1,error:false,tool_id:'first'},{start:2,end:3,end_known:true,name:'second',category:'read',count:1,error:false,tool_id:'second'},{start:4,end:7,end_known:true,name:'reuse',category:'read',count:1,error:false,tool_id:'reuse'},{start:11,end:12,end_known:true,name:'solo',category:'read',count:1,error:false,tool_id:'solo'}];
 const items=sessionRibbonTree([],marks,[],[],[0,20]).items,px=at=>at*50,placed=ribbonToolPlacement(items,px,32),byName=name=>placed.get(items.find(item=>item.title===name).id);
 assert.equal(new Set(['long','first','second'].map(name=>byName(name).y)).size,3,'simultaneous calls sit in separate lanes');
 assert.notEqual(byName('reuse').y,byName('long').y);
 assert.equal(byName('solo').y,16,'a call with room stays on the axis');
 assert.deepEqual(overlapping([...placed.values()]),[]);
 assert.deepEqual(ribbonToolPlacement([...items].reverse(),px,32),placed);
 const targets=[...placed.values()].map(m=>({...m,ids:[m.id]}));
 for(const name of ['long','first','second','reuse','solo']){
  const m=byName(name),{items:hits,target}=ribbonPointerHits(items,m.x/50,.01,{x:m.x+1,y:m.y+1},targets);
  assert.deepEqual(hits.map(item=>item.title),[name]);assert.equal(target.id,m.id);
 }
 const long=byName('long');assert.equal(ribbonPointerHits(items,8,.01,{x:400,y:long.y},targets).items[0].mark.preview,'npm test','the duration is part of the dot');
 assert.deepEqual(ribbonPointerHits(items,15,.01,{x:750,y:16},targets).items,[],'nothing under the pointer');
 const points=sessionRibbonTree([],['a','b','c'].map(tool_id=>({...marks[0],tool_id,start:5,end:99,end_known:false})),[],[],[0,10]).items;
 assert.ok(points.every(item=>item.end===item.start));
 assert.equal(new Set([...ribbonToolPlacement(points,at=>at*100,32).values()].map(m=>m.y)).size,3);
 assert.equal(ribbonHits(points,6,.01).length,0);
 assert.equal(sessionRibbonTree([],points.map(item=>item.mark),[],[],[6,10]).items.length,0);
});

test('compressed fingerprint segments preserve every original message for informative inspection',()=>{
 const events=Array.from({length:30},(_,i)=>message(String(i),i%2?'P.request.work.review':'C.report.progress',{at:i===29?null:new Date(i*1000).toISOString(),kind:i%3?'message':'reasoning'})),c=conversation('inspect',events);
 const tokens=conversationRibbon(c,4);
 assert.deepEqual(tokens.flatMap(t=>t.messages),events);assert.ok(tokens.every(t=>t.count===t.messages.length));
 assert.equal(tokens.flatMap(t=>t.messages).filter(m=>m.kind==='reasoning').length,10);
});

test('fingerprint source selection follows the clicked message within a compressed column',()=>{
 const messages=[message('first','P.request',{conversation_id:'earlier-conversation'}),message('second','C.report',{conversation_id:'source /conversation',session_id:'source /session'}),message('third','P.request')];
 const [token]=conversationRibbon(conversation('workspace-ribbon',messages),1);
 assert.equal(fingerprintMessageAt(token,.05),messages[0]);
 assert.equal(fingerprintMessageAt(token,.5),messages[1]);
 assert.equal(fingerprintMessageAt(token,.95),messages[2]);
 assert.equal(fingerprintMessageAt(token,0),messages[0]);assert.equal(fingerprintMessageAt(token,1),messages[2]);
 assert.equal(fingerprintMessageAt({messages:[]},.5),undefined);
 const selected=fingerprintMessageAt(token,.5),url=new URL(ribbonSourceURL({message:selected,start:0},{workspace:'work /space',conversation:'workspace-ribbon',session:'main'},'/analyze?analyze_data=message_labels'),'http://pharos.invalid');
 assert.equal(url.pathname,'/conversation/source%20%2Fconversation','the source workspace is not recorded on this message');
 assert.equal(url.searchParams.get('conversation'),'source /conversation');
 assert.equal(url.searchParams.get('delegation_session'),'source /session');
 assert.equal(url.searchParams.get('message'),'second');assert.equal(url.searchParams.get('reveal'),'message:second');
 assert.equal(url.searchParams.get('reader_view'),'conversation');assert.equal(url.searchParams.has('analyze_data'),false);
});

test('ribbon keys fit on screen without covering their inspected bar',()=>{
 for(const [anchor,height,viewport] of [[{top:400,bottom:440},320,480],[{top:10,bottom:54},200,480],[{top:210,bottom:254},600,480],[{top:100,bottom:144},300,900]]){
  const position=ribbonInspectorPlacement(anchor,height,viewport),bottom=position.top+Math.min(height,position.maxHeight);
  assert.ok(position.top>=0&&bottom<=viewport);
  assert.ok(bottom<=anchor.top||position.top>=anchor.bottom,'a tall key must leave the ribbon clickable');
  assert.ok(position.maxHeight>0);
 }
});

test('label intervals reach between observations, clip at tool boundaries and leave idle gaps blank',()=>{
 const at=n=>new Date(n).toISOString(),messages=[message('a','P.request',{at:at(0)}),message('thought','C.deliberation',{kind:'thinking',at:at(5000)}),message('last','C.report',{at:at(9000)}),message('idle','C.report',{at:at(3600000)})];
 const tools=recordedToolMarks([{id:'t',session_id:'s',message_id:'call',name:'exec_command',category:'shell',status:'ok',start:at(1000),end:at(2000)}]);
 const spans=labelIntervals(messages,tools);assert.deepEqual(spans.slice(0,3).map(s=>[s.start,s.end,s.attribution]),[[0,1000,'following'],[2000,5000,'preceding'],[9000,9000,'instant']]);
 const visible=sessionRibbonTree(messages,tools,[],[],[2500,4500]);assert.ok(visible.items.some(i=>i.message?.id==='thought'));
 assert.equal(tools[0].end_known,true);assert.equal(activityCategory({name:'wait_agent',category:'agent'}),'wait');assert.equal(activityCategory({name:'write_stdin',category:'shell'}),'poll');assert.equal(activityCategory({name:'exec_command',category:'shell',command_category:'git'}),'git');
});

test('fingerprint crests count overlapping captured branches separately from nesting depth',()=>{
 const at=n=>new Date(n).toISOString(),c=conversation('parallel',[message('main-a','C.report',{session_id:'main',at:at(0)}),message('main-b','C.report',{session_id:'main',at:at(90)}),...['a','b','c'].flatMap((sid,j)=>[message(sid+'-start','C.report',{session_id:sid,at:at(10+j*10)}),message(sid+'-end','C.report',{session_id:sid,at:at(60)})])]);
 c.main_session='main';c.sessions=[{id:'main',depth:0},...['a','b','c'].map(id=>({id,parent_id:'main',depth:1}))];
 const tokens=conversationRibbon(c,40);assert.deepEqual([...new Set(tokens.map(t=>t.parallelism))].sort(),[0,1,2,3]);assert.equal(Math.max(...tokens.map(t=>t.depth)),1);
});

test('specific report labels retain meaning and unknown descendants inherit their closest chromatic parent',()=>{
 for(const id of ['C.report.finding','C.report.progress','C.report.product'])assert.notEqual(stateColor(id),stateColor('?'));
 assert.notEqual(stateColor('C.report.finding'),stateColor('C.report.progress'));assert.equal(stateColor('C.report.finding.future'),stateColor('C.report.finding'));
 const c=conversation('specific',[message('finding','C.report.finding'),message('product','C.report.product')]);assert.deepEqual(conversationRibbon(c).flatMap(t=>t.ids),['C.report.finding','C.report.product']);
});

test('rarity layering preserves every observation and hit while drawing common modes beneath rare modes and tools',()=>{
 const at=n=>new Date(n).toISOString(),messages=[message('a','C.report',{at:at(0)}),message('b','C.report',{at:at(10),labels:[['C.report',.9],['C.difficulty.blocker',.8]]})],marks=[{start:0,end:20,end_known:true,name:'Read',category:'read',count:1,error:false}];
 const tree=sessionRibbonTree(messages,marks,[],[],[0,20]),layers=ribbonLayers(tree.items),common=layers.filter(l=>l.item.label==='C.report'),rare=layers.find(l=>l.item.label==='C.difficulty.blocker');
 assert.ok(common.every(l=>l.height>rare.height));assert.ok(layers.indexOf(rare)>layers.indexOf(common.at(-1)));assert.ok(layers.at(-1).item.mark);assert.equal(layers.length,tree.items.length);assert.ok(ribbonHits(tree.items,10,1).some(i=>i.label==='C.difficulty.blocker'));
});

test('ribbon stripes retain every deepest accepted label including reports below the previous top-five cutoff',()=>{
 const labels=['P.request.work.implement','P.frame.scope','P.control.redirect','C.input.answer','C.feedback.accept','C.deliberation.plan','C.report.finding'],messages=labels.flatMap((id,i)=>Array.from({length:7-i},(_,j)=>message(`${i}:${j}`,id))),token=conversationRibbon(conversation('all-labels',messages),1)[0],segment=ribbonSegment(token.messages,[]);
 assert.deepEqual(new Set(segment.slices.map(s=>s.id)),new Set(labels));assert.ok(segment.slices.find(s=>s.id==='C.report.finding').share>0);assert.ok(Math.abs(segment.slices.reduce((n,s)=>n+s.share,0)-1)<1e-12);
 const deepest=ribbonSegment([message('ancestor','C.report',{labels:[['C.report',1],['C.report.finding',.3]]})],[]);assert.deepEqual(deepest.slices.map(s=>s.id),['C.report.finding']);
});

test('fingerprint captions distinguish a single message from an inclusive message range',()=>{
 assert.equal(ribbonMessageTitle(8,8,9),'Message 8 / 9');assert.equal(ribbonMessageTitle(1,1,1),'Message 1 / 1');assert.equal(ribbonMessageTitle(8,9,20),'Messages 8–9 / 20');
});

test('timeline Caller separates human and supplied input without reclassifying subagent assignments',()=>{
 const human=message('human','P.request',{role:'user',session_id:'main',authorship:[{category:'typed',bytes:40},{category:'pasted',bytes:60}]}),unknown=message('unknown','P.request',{role:'user',session_id:'main'}),automated=message('assignment','P.request',{role:'user',session_id:'child',segment:'H-typed',authorship:[{category:'automated',bytes:100}]}),reply=message('reply','C.report',{session_id:'main'}),childHuman=message('child-human','P.control',{role:'user',session_id:'child',authorship:[{category:'typed',bytes:10}]});
 const externalAutomation={...automated,id:'external-automation',session_id:'main'};
 const source=new Map([['main',[human,unknown,reply,externalAutomation]],['child',[automated,childHuman,human]]]);
 const split=splitTimelineCallers(source,[{id:'main',depth:0},{id:'child',depth:1}]);
 assert.deepEqual(new Set(split.caller.map(m=>m.id)),new Set(['human','unknown','child-human','external-automation']));
 assert.deepEqual(split.messages.get('main').map(m=>m.id),['reply']);
 assert.deepEqual(split.messages.get('child').map(m=>m.id),['assignment']);
 assert.equal(source.get('main').length,4);
 assert.equal(authorshipMix(split.caller.find(m=>m.id==='external-automation'))[0].category,'automated');
 assert.deepEqual(authorshipMix(unknown),[{category:'unknown',bytes:1}]);
 assert.equal(split.caller.find(m=>m.id==='human').session_id,'main');
});

test('Caller origin shares preserve mixed text and use recipient observations as clock boundaries',()=>{
 const prompt=message('prompt','P.request',{role:'user',at:new Date(1000).toISOString(),authorship:[{category:'typed',bytes:20},{category:'quoted',bytes:60},{category:'typed',bytes:20}]}),response=message('response','C.report',{at:new Date(5000).toISOString()});
 const tree=sessionRibbonTree([prompt],[],[],[],[0,6000],true,true,[prompt,response]);
 assert.equal(ribbonCounts(tree.items).messages,1);
 assert.equal(tree.items.find(item=>item.label).end,5000);
 const origins=tree.items.filter(item=>item.authorship);
 assert.deepEqual(origins.map(item=>[item.authorship,item.start,item.end]),[['typed',1000,1800],['quoted',1800,4200],['typed',4200,5000]]);
 assert.ok(origins.every(item=>item.color===tree.items.find(item=>item.label).color));
 assert.equal(new Set(origins.map(item=>item.id)).size,3);
 const origin=tree.children.find(node=>node.id==='authorship');
 assert.equal(origin.children.length,1);
 assert.equal(origin.children[0].children.length,3);
 assert.deepEqual(ribbonHits(origins,3000,0).map(item=>item.authorship),['quoted']);
 assert.deepEqual(ribbonHits(origins,1800,50).map(item=>item.authorship),['quoted']);
 assert.deepEqual(ribbonHits(origins,4199,50).map(item=>item.authorship),['quoted']);
 assert.deepEqual(ribbonHits(origins,4200,50).map(item=>item.authorship),['typed']);
 const zoomed=sessionRibbonTree([prompt],[],[],[],[2000,4000],true,true,[prompt,response]);
 assert.deepEqual(zoomed.items.filter(item=>item.authorship).map(item=>[item.start,item.end]),origins.map(item=>[item.start,item.end]));
 assert.deepEqual(authorshipMix(prompt),[{category:'typed',bytes:40},{category:'quoted',bytes:60}]);
});

test('Caller text positions retain repeated origins and safely normalize missing or invalid composition',()=>{
 const prompt=message('mixed','P.request',{authorship:[{category:'typed',bytes:2},{category:'quoted',bytes:6},{category:'typed',bytes:2},{category:'pasted',bytes:NaN},{category:'pasted',bytes:-1}]});
 assert.deepEqual(authorshipSegments(prompt).map(part=>[part.category,part.start,part.end]),[['typed',0,.2],['quoted',.2,.8],['typed',.8,1]]);
 assert.deepEqual(authorshipSegments(message('unknown','P.request')),[{category:'unknown',bytes:1,start:0,end:1}]);
 assert.equal(authorshipSegments({...prompt,authorship:[{category:'unrecognized',bytes:10}]})[0].category,'unknown');
 const simultaneous={...prompt,at:new Date(1000).toISOString(),labels:[['P.request',.9],['P.control',.8]]};
 const tree=sessionRibbonTree([simultaneous],[],[],[],[0,6000],true,true);
 const parts=tree.children.find(node=>node.id==='authorship').children[0].children;
 assert.equal(parts.length,3);
 assert.ok(parts.every(part=>part.items.length===2&&part.items.every(item=>item.start===1000&&item.end===1000)));
 assert.deepEqual(parts.map(part=>part.items[0].composition),[{start:0,end:.2},{start:.2,end:.8},{start:.8,end:1}]);
});

test('Caller span inspection uses the actual run excerpt even after the message preview ends',()=>{
 const prompt=message('text','P.request',{role:'user',at:new Date(1000).toISOString(),text:'A short preview',truncated:true,authorship:[{category:'harness',bytes:2000,text:'Retained harness context',truncated:true},{category:'typed',bytes:10,text:'Ship it!é',truncated:false},{category:'attachment',bytes:100,text:'[image]',truncated:false}]});
 const segments=authorshipSegments(prompt);
 assert.deepEqual(authorshipExcerpt(prompt,segments[1]),{text:'Ship it!é',truncated:false});
 const response=message('reply','C.report',{at:new Date(5000).toISOString()}),tree=sessionRibbonTree([prompt],[],[],[],[0,6000],true,true,[prompt,response]);
 const typed=tree.items.find(item=>item.authorship==='typed');
 assert.deepEqual(typed.excerpt,{text:'Ship it!é',truncated:false});
 const full=message('utf8','P.request',{text:'é😎done',authorship:[{category:'pasted',bytes:6},{category:'typed',bytes:4}]});
 assert.deepEqual(authorshipSegments(full).map(part=>authorshipExcerpt(full,part)),[{text:'é😎',truncated:false},{text:'done',truncated:false}]);
 const missing={...full,text:'é',truncated:true};
 assert.equal(authorshipExcerpt(missing,authorshipSegments(missing)[1]),null);
 const paddedText='\n'.repeat(300)+'Review this\n  Keep indentation\n\n',padded=message('padded','P.request',{text:paddedText,authorship:[{category:'template',bytes:Buffer.byteLength(paddedText)}]});
 assert.deepEqual(authorshipExcerpt(padded,authorshipSegments(padded)[0]),{text:'Review this\n  Keep indentation',truncated:false});
});

test('timeline column widths are bounded, portable and preserve automatic chart width',()=>{
 const columns=normalizeTimelineColumns({execution:260,events:200,time:880});
 const compact=timelineColumnLayout(columns,true);
 assert.ok(compact.events<columns.events);assert.equal(compact.execution,columns.execution);assert.equal(compact.time,columns.time);
 assert.deepEqual(timelineColumnLayout(columns,false),columns,'reopening event details restores the saved widths');
 assert.deepEqual(normalizeTimelineColumns(JSON.parse(JSON.stringify(columns))),columns);
 assert.equal(normalizeTimelineColumns({}).time,0);
 assert.equal(columns.time,0,'older fixed clock widths now fill the available space');
 const bounded=normalizeTimelineColumns({execution:-10,events:Infinity,time:99999});
 assert.ok(bounded.execution>0&&Number.isFinite(bounded.events)&&bounded.time<99999);
 assert.ok(timelineColumnWidth('events',120)<timelineColumnWidth('events',240));
});

test('ribbon source links retain aliases and timeline settings while revealing the original message or call',()=>{
 const source={workspace:'work /one',conversation:'canonical',session:'session'},item={id:'part',start:0,end:10,message:message('m:&=1','P.request',{work_id:'work /one',conversation_id:'original',session_id:'nested'})};
 const current='http://localhost/work/work%20%2Fone?explorer=timeline&reader_view=timeline&delegation_start=4&disclosures=%7B%7D&delegation_event=old&call=stale';
 const url=new URL(ribbonSourceURL(item,source,current),'http://localhost');
 assert.equal(url.searchParams.get('conversation'),'original');assert.equal(url.searchParams.get('message'),'m:&=1');assert.equal(url.searchParams.get('reveal'),'message:m:&=1');assert.equal(url.searchParams.get('delegation_session'),'nested');
 assert.equal(url.searchParams.get('explorer'),'timeline');assert.equal(url.searchParams.get('delegation_start'),'4');assert.equal(url.searchParams.get('disclosures'),'{}');
 assert.equal(url.searchParams.get('reader_view'),'conversation');for(const key of ['delegation_event','call'])assert.equal(url.searchParams.has(key),false);
 const tool=new URL(ribbonSourceURL({id:'tool',start:1000,end:2000,mark:{tool_id:'call /id'}},source,current),'http://localhost');
 assert.equal(tool.searchParams.get('call'),'call /id');assert.equal(tool.searchParams.get('at'),'1970-01-01T00:00:01.000Z');assert.equal(tool.searchParams.has('message'),false);assert.equal(tool.searchParams.get('reveal'),'call:call /id');
 const deep=new URL(ribbonSourceURL(item,source,current.replace('/work/work%20%2Fone?','/work/work%20%2Fone/conversation/original?')),'http://localhost');assert.equal(deep.searchParams.get('delegation_start'),'4');
 assert.equal(ribbonSourceURL({id:'inferred',start:0,end:10},source,current),null);
 const external=new URL(ribbonSourceURL({...item,message:{...item.message,work_id:'elsewhere'}},source,current),'http://localhost');assert.equal(external.pathname,'/work/elsewhere');assert.equal(external.searchParams.has('delegation_start'),false);
 const inherited=new URL(ribbonSourceURL({...item,message:{...item.message,work_id:undefined,session_id:undefined}},source,current),'http://localhost');
 assert.equal(inherited.pathname,'/conversation/original');assert.equal(inherited.searchParams.get('message'),'m:&=1');
 assert.equal(inherited.searchParams.has('delegation_start'),false);assert.equal(inherited.searchParams.has('delegation_session'),false);
});

test('timeline legends follow drawn intervals, errors and the visible portion of caller composition',()=>{
 const at=n=>new Date(n).toISOString(),marks=[{start:200,end:800,end_known:true,category:'run',name:'Run',error:false},{start:90,end:600,end_known:true,category:'read',name:'Read',error:true},{start:900,end:950,end_known:true,category:'git',name:'Git',error:false}];
 const early=message('early','C.report.finding',{at:at(0)}),late=message('late','P.request',{at:at(2000)});
 const visible=timelineLegendWindow([{messages:[early,late],marks}],[100,250]);
 assert.deepEqual(visible.messages.map(m=>m.id),[],'the preceding report ends at the tool boundary before this window');
 assert.deepEqual(visible.tools,['run']);assert.equal(visible.error,true);
 const thought=message('thought','C.deliberation.plan',{at:at(800),kind:'reasoning'}),unmatched=message('unmatched',null,{at:at(1000)});
 const thinking=timelineLegendWindow([{messages:[thought,unmatched],marks:[],neighbors:[early,thought,unmatched]}],[300,500]);
 assert.deepEqual(thinking.messages.map(m=>m.id),['thought'],'thinking is visible before its recorded endpoint');
 const caller=message('human','P.request',{at:at(0),authorship:[{category:'typed',bytes:5},{category:'pasted',bytes:3},{category:'harness',bytes:2}]}),end={start:1000,end:1000,end_known:false,category:'other',name:'Boundary',error:false};
 const slice=range=>timelineLegendWindow([{caller:true,messages:[caller],marks:[end]}],range);
 assert.deepEqual(slice([50,450]).origins,['typed']);assert.deepEqual(slice([550,750]).origins,['pasted']);assert.deepEqual(slice([850,950]).origins,['harness']);assert.deepEqual(slice([1200,1400]).messages,[]);
 assert.equal(slice([50,450]).tools.length,0,'caller boundary tools are not themselves drawn in the caller ribbon');
});

test('timeline previews unwrap content and nested arguments without losing domain JSON or literal caller text',()=>{
 assert.equal(trimTimelineSnippet('\n  Review this\n  Keep indentation\n\n'),'Review this\n  Keep indentation');
 for(const [body,expected,options] of [
  [JSON.stringify({content:[{text:'Async agent launched successfully.\nWorking in the background.'}],details:{agentId:'opaque'},tool:'Task'}),'Async agent launched successfully.\nWorking in the background.'],
  [JSON.stringify({message:{content:[{type:'output_text',text:'First'},{type:'text',text:'Second'}]},usage:{tokens:9}}),'First\n\nSecond'],
  [JSON.stringify(JSON.stringify({content:[{type:'text',text:'Decoded twice'}]})),'Decoded twice'],
  [JSON.stringify({tool:'Task',input:{description:'Check',prompt:'Read the source\nThen check tests',subagent_type:'Reviewer'}}),'Read the source\nThen check tests',{input:true}],
  [JSON.stringify({tool:'exec_command',input:JSON.stringify({cmd:'npm test',timeout_ms:1000})}),'npm test',{input:true}],
  [JSON.stringify({input:{description:'Review the patch',subagent_type:'Reviewer'}}),'Review the patch',{input:true}],
  [JSON.stringify({cmd:'"quoted command"'}),'"quoted command"',{input:true}],
  ['"literal code"','"literal code"',{input:true}],
  [JSON.stringify({file_path:'/repo/README.md',offset:10,limit:20}),'/repo/README.md',{input:true}],
  [JSON.stringify({stdout:'Partial output',stderr:'Something failed',exit_code:2}),'Partial output\n\nstderr:\nSomething failed'],
  [JSON.stringify({content:[{type:'image',source:{data:'opaque'}},{text:'Caption'}]}),'[Image]\n\nCaption'],
  ['printf "hello\nworld"','printf "hello\nworld"'],
  ['{"content":[{"text":"unfinished','{"content":[{"text":"unfinished'],
  ['{"content":"literal user JSON"}','{"content":"literal user JSON"}',{unwrap:false}],
 ])assert.equal(timelinePreviewText(body,options),expected);
 const structured={text:'Keep the body',records:[{count:2}]};assert.deepEqual(JSON.parse(timelinePreviewText(JSON.stringify(structured))),structured);
 const edit={file_path:'/repo/a.go',old_string:'before',new_string:'after'};assert.deepEqual(JSON.parse(timelinePreviewText(JSON.stringify(edit),{input:true})),edit);
});


test('fingerprint allocations conserve message weight across roots, leaves, parent-only and coverage states',()=>{
 const taxonomy=['P','P.request','P.request.work','P.request.work.review','P.request.work.verify','C','C.report','C.report.finding'].map(id=>({id,name:id,definition:''}));
 const mixed=message('mixed','P.request',{labels:[['P.request.work.review',.9],['P.request.work.verify',.8],['C.report.finding',.7]]});
 const allocated=messageAllocation(mixed,taxonomy);assert.deepEqual(allocated.map(a=>a.weight),[.25,.25,.5]);assert.equal(allocated.reduce((sum,a)=>sum+a.weight,0),1);
 const segment=ribbonSegment([mixed,message('parent','C.report'),message('unlabeled',null),message('no-match',null,{chunks:1})],taxonomy),rows=new Map(segment.rows.map(row=>[row.id,row]));
 assert.equal(rows.get('all').share,1);assert.equal(rows.get('P').share,.125);assert.equal(rows.get('C').share,.375);assert.equal(rows.get('uncategorized').share,.5);assert.equal(rows.get('C.report~other').share,.25);assert.equal(rows.get('C.report~other').colorID,'C.report');
 assert.equal(rows.get('C.report.finding').parent,'C.report');assert.equal(rows.get('P.request.work.review').parent,'P.request.work');
 assert.equal(stateColor(rows.get('no-match').colorID),'transparent');
 assert.equal(stateColor(rows.get('unlabeled').colorID),stateColor('?'));
 assert.notEqual(stateColor(rows.get('unlabeled').colorID),'transparent');
 for(const row of segment.rows){const children=segment.rows.filter(child=>child.parent===row.id);if(children.length)assert.ok(Math.abs(children.reduce((sum,child)=>sum+child.weight,0)-row.weight)<1e-12);}
 assert.deepEqual(ribbonSegment([],taxonomy),{rows:[],slices:[]});
});

test('fingerprints keep no-match runs separate from unprocessed messages, including after compression',()=>{
 const messages=[message('pending',null),message('no-match-a',null,{chunks:1}),message('no-match-b',null,{chunks:2}),message('missing',null,{chunks:null}),message('matched','C.report')];
 const tokens=conversationRibbon(conversation('coverage',messages));
 assert.deepEqual(tokens.map(token=>token.messages.map(m=>m.id)),[['pending'],['no-match-a','no-match-b'],['missing'],['matched']]);
 assert.deepEqual(tokens.map(token=>ribbonSegment(token.messages).slices.map(slice=>[slice.id,slice.share])),[[['unlabeled',1]],[['no-match',1]],[['unlabeled',1]],[['C.report',1]]]);
 const compressed=conversationRibbon(conversation('coverage',messages),1);
 assert.deepEqual(compressed.flatMap(token=>token.messages),messages);
 const slices=new Map(ribbonSegment(compressed[0].messages).slices.map(slice=>[slice.id,slice]));
 assert.equal(slices.get('no-match').share,2/5);
 assert.equal(slices.get('unlabeled').share,2/5);
 assert.equal(stateColor(slices.get('no-match').colorID),'transparent');
 assert.notEqual(stateColor(slices.get('unlabeled').colorID),'transparent');
});

test('compact fingerprints bound their size while preserving message volume and recorded nesting',()=>{
 const c=conversation('ribbon',Array.from({length:201},(_,i)=>message(String(i),i%2?'P.request':'C.report',{session_id:i%3?'main':'child',at:new Date(i*1000).toISOString()})));c.main_session='main';c.sessions=[{id:'main',depth:0},{id:'child',depth:2}];
 const tokens=conversationRibbon(c,12);assert.ok(tokens.length<=12);assert.equal(tokens.reduce((sum,token)=>sum+token.count,0),201);assert.equal(Math.max(...tokens.map(token=>token.depth)),2);
});

test('overlap envelopes remain symmetric, grow beyond three branches and stop at zero overlap',()=>{
 const segments=levels=>levels.map((parallelism,i)=>({x:i*10,width:10,token:{parallelism}}));
 assert.deepEqual(parallelEnvelopes(segments([0,0])),[]);
 const shapes=parallelEnvelopes(segments([0,1,3,16,0,64]));assert.deepEqual(shapes.map(s=>s.peak),[16,64]);
 const heights=shapes[0].points.map(p=>p.lower-p.upper);assert.ok(heights[0]<heights[1]&&heights[1]<heights[2]);assert.ok(shapes[1].points[0].lower-shapes[1].points[0].upper>heights[2]);
 for(const envelope of shapes){assert.ok(envelope.path.endsWith('Z')&&!envelope.path.includes('NaN'));for(const point of envelope.points){assert.ok(point.upper>0&&point.lower<38);assert.ok(Math.abs((point.upper+point.lower)/2-19)<1e-12);}}
});

test('session ribbons decompose agent events by group and kind beside tool actions, counting them once',()=>{
 const at=n=>new Date(n).toISOString(),reply=message('reply','C.report',{at:at(10)}),later=message('later','C.report',{at:at(30)});
 const event=(id,kind,label,group,messageID)=>({id,message_id:messageID,kind,label,group,probability:.9,source:'reply',sentence:`${label} sentence`,target:null});
 const events=[event('e1','obstacle','Obstacle','Friction','reply'),event('e2','obstacle','Obstacle','Friction','later'),event('e3','discovery','Discovery','Reactions and learning','reply'),event('gone','obstacle','Obstacle','Friction','elsewhere')];
 const tree=sessionRibbonTree([reply,later],[],[],discoveryTaxonomy,[0,40],true,false,undefined,undefined,events);
 const node=tree.children.find(child=>child.id==='events');
 assert.equal(node.title,'Agent events');
 const ordered=sessionRibbonTree([reply,later],[{start:12,end:13,end_known:true,name:'Read',category:'read',count:1,error:false}],[],discoveryTaxonomy,[0,40],true,false,undefined,undefined,events).children.map(child=>child.title);
 assert.deepEqual(ordered,['Message modes','Agent events','Tool actions'],'Agent events come before Tool actions');
 assert.deepEqual(node.children.map(child=>[child.title,ribbonEventCount(child.items,'events')]),[['Friction',2],['Learning',1]]);
 assert.deepEqual(node.children[0].children.map(child=>[child.title,child.children.length]),[['Obstacle',2]]);
 // The whole session still counts its two messages, not the events said in them.
 assert.equal(ribbonEventCount(tree.items),2);
 assert.equal(ribbonCounts(node.items).events,3);
 assert.equal(ribbonEventCount(node.items),2,'event-only session summaries still count distinct source messages');
 assert.equal(ribbonEventCount(node.items,'events'),3,'classification rows retain individual event counts');
 const marks=[{start:12,end:13,end_known:true,name:'Read',category:'read',count:1,error:false,tool_id:'read',message_id:'tool'},
  ...['one','two'].map(tool_id=>({start:14,end:15,end_known:true,name:'Run',category:'run',count:1,error:false,tool_id,message_id:'reply'}))];
 const withTools=sessionRibbonTree([reply,later],marks,[],discoveryTaxonomy,[0,40],true,false,undefined,undefined,events);
 const hiddenModes={modes:{values:['missing'],excluded:false}};
 const mixed=visibleTimelineTree(withTools,hiddenModes);
 assert.equal(ribbonEventCount(withTools.items),3);
 assert.equal(ribbonEventCount(mixed.items),3,'surviving events count when their message modes are hidden; tools on the same message deduplicate');
 const withoutRead=visibleTimelineTree(withTools,{...hiddenModes,tools:{values:['read'],excluded:true}});
 assert.equal(ribbonEventCount(withoutRead.items),2,'hiding an independent tool decreases the summary');
 const onlyEvents=visibleTimelineTree(withTools,{...hiddenModes,tools:{values:['missing'],excluded:false}});
 assert.equal(ribbonEventCount(onlyEvents.items),2,'removing the last tool does not change how events are counted');
 assert.equal(ribbonEventCount(onlyEvents.children.find(child=>child.id==='events').items,'events'),3);
 const onlyLearning=visibleTimelineTree(withTools,{...hiddenModes,tools:{values:['missing'],excluded:false},events:{values:['learning'],excluded:false}});
 assert.equal(ribbonEventCount(onlyLearning.items),1,'hiding events also removes their unrepresented source messages');
 const aggregated={id:'aggregate',start:12,end:13,title:'Read',color:'',thinking:false,mark:{...marks[0],count:3}};
 assert.equal(ribbonEventCount([...node.items,aggregated]),5,'aggregated tool counts retain their additional observations');
 // Outside the window an event is not shown.
 assert.equal(sessionRibbonTree([reply,later],[],[],discoveryTaxonomy,[20,40],true,false,undefined,undefined,events).children.find(child=>child.id==='events').items.length,1);
});

test('agent events sit at their sentence\'s share of the message span, as message-mode text parts do',()=>{
 const at=n=>new Date(n).toISOString();
 // Thinking takes the time before it ([0, 100]); a reply the time after it ([100, 300]).
 const prompt=message('prompt','P.request',{role:'user',at:at(0)}),thinking=message('think','C.deliberation',{kind:'thinking',at:at(100)}),reply=message('reply','C.report',{at:at(100)}),next=message('next','P.request',{role:'user',at:at(300)});
 const event=(id,messageID,position,target=null)=>({id,message_id:messageID,kind:'assumption',label:'Assumption',group:'Choices you may not see',probability:.9,source:'thinking',sentence:'s',target,position});
 const clock=agentEventClock([prompt,thinking,reply,next],[]);
 assert.equal(clock.at(event('a','think',.25)),25,'a quarter into the thinking');
 assert.equal(clock.at(event('b','think',1)),100);
 assert.equal(clock.at(event('c','reply',.5)),200,'half way through the reply');
 assert.equal(clock.at(event('d','think')),100,'no recorded share stays at the message time');
 assert.equal(clock.at(event('e','next',.9)),300,'a message with no duration stays at its time');
 assert.equal(clock.at(event('f','elsewhere',.5)),null);
 assert.equal(clock.targetAt(event('g','reply',.5,{method:'quote',message_id:'think',position:.5})),50,'the arc target uses the quoted text\'s share too');
 assert.equal(clock.targetAt(event('h','reply',.5,{method:'quote',message_id:'other-session',at:at(42)})),42,'a target in another session uses its own time');
 assert.equal(textTime({start:0,end:100,at:100},2),100,'shares clamp to the span');
 // The tree's event rows and the overlay share the clock.
 const tree=sessionRibbonTree([prompt,thinking,reply,next],[],[],discoveryTaxonomy,[0,400],true,false,undefined,undefined,[event('a','think',.25),event('c','reply',.5)]);
 assert.deepEqual(tree.children.find(child=>child.id==='events').items.map(item=>item.start).sort((x,y)=>x-y),[25,200]);
});

test('a ribbon reports events left out for changed evidence apart from labels still being produced',()=>{
 assert.deepEqual(messageLabelRibbonNotes({truncated:false,pending:false,events_pending:0}),[]);
 assert.deepEqual(messageLabelRibbonNotes({events_pending:2}).map(n=>n.badge),['Events pending']);
 assert.match(messageLabelRibbonNotes({events_pending:1})[0].note,/^Events for 1 conversation are left out until they are rebuilt/);
 assert.deepEqual(messageLabelRibbonNotes({pending:true}).map(n=>n.badge),['Labeling']);
 assert.deepEqual(messageLabelRibbonNotes({truncated:true,pending:true,events_pending:3}).map(n=>n.badge),['Sample','Labeling','Events pending']);
});
