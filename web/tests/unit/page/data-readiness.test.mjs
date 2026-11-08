import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readinessDomains,readinessModel,readinessPreview,readinessTone,readinessSettingsURL,readinessStageLabel,readinessStageDescription,readinessStageState,readinessProgressText,readinessGroups,readinessSubjects,readinessNextText,readinessHasProgress} from '../../../src/data-readiness-model.ts';
import {readinessDial} from '../../../src/data-readiness-icon.ts';
const now=Date.parse('2026-11-07T17:24:00Z'),iso=minutes=>new Date(now-minutes*60000).toISOString();
const scope=(dataset='writing_messages',extra={})=>({id:dataset,title:dataset,dataset,...extra});
const row=(domain,changes={})=>({domain,label:domain,as_of:iso(2),behind:{state:'current'},...changes});
const snapshot=(rows=[])=>({freshness:[row('conv:codex'),...rows],background:{mode:'on',items:[{id:'writing',label:'Writing estimates',state:'current',cadence:'frequent',cadence_label:'Frequently',next_at:iso(-3)}]},operations:[]});

test('the displayed population, not unrelated backlog, determines the summary',()=>{
 const data=snapshot([row('an.authorship'),row('an.ledger'),row('backup.local',{behind:{state:'behind'}})]);
 assert.equal(readinessModel(data,[scope()],now).kind,'current');
 data.freshness.find(row=>row.domain==='an.authorship').behind.state='behind';
 assert.equal(readinessModel(data,[scope()],now).kind,'behind');
 assert.equal(readinessModel(data,[scope('tool_calls')],now).kind,'current');
 assert.equal(readinessModel(data,[],now).kind,'unknown');
 assert.equal(readinessModel(data,[scope('future_dataset')],now).kind,'unknown');
});
test('source results include pending and unreadable captured history when present',()=>{
 const data=snapshot([row('view'),row('an.authorship')]);
 const populations=[scope('library'),scope('usage'),scope('writing_messages')];
 for(const population of populations)assert.equal(readinessModel(data,[population],now).kind,'current','captures are optional');
 data.freshness.push(row('capt:other-mac/claude',{as_of:null,behind:{state:'behind'},reason:{code:'not-indexed',text:'Captured data is not indexed yet'}}));
 for(const population of populations){
  const result=readinessModel(data,[population],now);
  assert.equal(result.kind,'behind');assert.equal(result.lagMs,null,'capture age was not measured');
  assert(result.stages.some(stage=>stage.id==='capt:other-mac/claude'));
 }
 data.freshness.at(-1).behind.state='current';
 assert.equal(readinessModel(data,populations,now).kind,'current');
 data.freshness.at(-1).behind.state='unknown';
 data.freshness.at(-1).reason={code:'unreadable',text:'The capture folder cannot be read'};
 assert.equal(readinessModel(data,populations,now).kind,'unknown');
 data.freshness.at(-1).domain='capt:*';
 assert.equal(readinessModel(data,populations,now).kind,'unknown','an unreadable root without cached captures is also evidence');
 assert.equal(readinessModel(data,[scope('mcp_calls')],now).stages.some(stage=>stage.id.startsWith('capt:')),false);
});
test('Fixes depends on agent events even when conversations and Findings are current',()=>{
 const data=snapshot([row('findings')]);
 assert(readinessDomains(scope('fixes')).includes('@agent_events'));
 for(const state of ['disabled','running','current']){
  data.background.items=[{id:'agent_events',label:'Agent events',state}];
  const result=readinessModel(data,[scope('fixes')],now);
  assert.equal(result.kind,'unknown','a task state cannot replace missing event ledger evidence');
  assert.equal(result.stages.find(stage=>stage.id==='@agent_events').item.state,state);
 }
 data.freshness.push(row('@agent_events',{behind:{state:'behind',count:3,unit:'message'}}));
 assert.equal(readinessModel(data,[scope('fixes')],now).kind,'behind');
 data.freshness.at(-1).behind.state='current';
 assert.equal(readinessModel(data,[scope('fixes')],now).kind,'current');
});
test('source schedule and Settings task come from the ledger rather than source names',()=>{
 const data=snapshot([row('conv:custom',{task_id:'other-sources',behind:{state:'unknown'},reason:{code:'manual-only',text:'Set to update only when you ask'},next:'updates with the next manual index'})]);
 data.background.items=[{id:'conversations',label:'New conversations',state:'current',next_at:iso(-3)},{id:'other-sources',label:'Other sources',state:'manual',cadence:'manual',next_at:null}];
 const source=()=>readinessModel(data,[scope('usage')],now).stages.find(stage=>stage.id==='conv:custom');
 assert.equal(source().item.id,'other-sources');assert.equal(source().nextAt,null);
 assert.match(readinessStageDescription(source()),/request an update in settings/);
 data.background.items[1]={...data.background.items[1],state:'scheduled',cadence:'daily',next_at:iso(-360)};
 data.freshness[1].reason={code:'overdue',text:'Not checked recently'};
 assert.equal(source().nextAt,iso(-360));assert.equal(source().item.id,'other-sources');
 // Older snapshots or catalog-only evidence may not identify the owner.
 delete data.freshness[1].task_id;
 assert.equal(source().item,undefined);assert.equal(source().nextAt,undefined);
 data.freshness[1].task_id='conversations';
 assert.equal(source().item.id,'conversations');assert.equal(source().nextAt,iso(-3));
});
test('Waiting includes pending conversation-ledger upgrades and uses their actual task',()=>{
 const data=snapshot([row('an.ledger',{behind:{state:'behind',count:8,unit:'conversation'},reason:{code:'version',text:'This version of Pharos rebuilds it'},next:'updates when you run the library upgrade'}),row('an.tools')]);
 data.background.items=[{id:'tools',label:'Tools & usage',state:'current',cadence:'frequent',next_at:iso(-3)},{id:'upgrade',label:'Library upgrade',state:'scheduled',cadence:'daily',next_at:iso(-360)}];
 const result=()=>readinessModel(data,[scope('model_requests')],now);
 assert(readinessDomains(scope('model_requests')).includes('an.ledger'));
 assert.equal(result().kind,'behind');
 const ledger=result().stages.find(stage=>stage.id==='an.ledger');
 assert.equal(ledger.item.id,'upgrade');assert.equal(ledger.nextAt,iso(-360));
 const settings=model=>new URL(readinessSettingsURL(model),'http://localhost');
 assert.equal(settings(result()).searchParams.get('highlight'),'section:background-upgrade');
 assert.deepEqual(JSON.parse(settings(result()).searchParams.get('disclosures')),{'background:upgrade':true});
 const tools=readinessModel(data,[scope('tools')],now).stages.find(stage=>stage.id==='an.tools');
 assert.equal(tools.item.id,'tools');assert.equal(tools.nextAt,iso(-3));
 data.freshness[2].behind.state='behind';
 assert.equal(settings(readinessModel(data,[scope('tools')],now)).searchParams.get('highlight'),'section:background-upgrade','a rollup backlog cannot redirect away from its required upgrade');
 data.freshness[2].behind.state='current';
 data.background.items[1]={...data.background.items[1],state:'manual',cadence:'manual',next_at:null};
 assert.equal(result().stages.find(stage=>stage.id==='an.ledger').nextAt,null);
 data.freshness[1].behind.state='current';
 assert.equal(result().kind,'current');
 assert.equal(readinessSettingsURL(result()),'/settings/sync');
 data.freshness.splice(1,1);
 assert.equal(result().kind,'unknown','missing ledger evidence cannot imply current timing data');
});
test('query fields, filters, grouped source queries and expressions add labeling dependencies',()=>{
 for(const query of [{select:[{field:'message_label'}]},{where:[{field:'message_label',op:'=',value:'planning'}]},{grouping:{source:{select:[{field:'typed_words'}]},keys:['message_label']}},{aggregations:[{expression:'COUNT_DISTINCT([message_label])'}]}]){
  assert(readinessDomains(scope('writing_messages',{query})).includes('@labels'));
 }
 assert(!readinessDomains(scope()).includes('@labels'));
 assert(readinessDomains(scope('people_message_labels')).includes('@labels'));
});
test('a planned pass stays distinct from known staleness',()=>{
 const data=snapshot([row('an.authorship',{behind:{state:'scheduled'},reason:{code:'scheduled-frequent',text:'Waiting for the next frequent pass'}})]);
 const result=readinessModel(data,[scope()],now);
 assert.equal(result.kind,'scheduled');assert.equal(result.tone,'current');assert.equal(result.label,'Update scheduled');
});
test('an old publication is not evidence of a stale interval; observed queued input is',()=>{
 const data=snapshot([row('an.authorship',{as_of:iso(4000),behind:{state:'behind',count:128,unit:'message'}})]);
 const unknownLag=readinessModel(data,[scope()],now);assert.equal(unknownLag.label,'Update needed');assert.equal(unknownLag.lagMs,null);
 for(const [minutes,tone] of [[3,'minutes'],[45,'recent'],[187,'hours'],[540,'orange'],[2963,'days']]){
  data.freshness[1].pending_since=iso(minutes);
  const model=readinessModel(data,[scope()],now);
  assert.equal(model.tone,tone);assert.equal(model.lagMs,minutes*60000);
 }
 data.freshness[1].pending_since='not-a-time';assert.equal(readinessModel(data,[scope()],now).label,'Update needed');
});
test('the database dial measures actual hours, saturates at twelve, and never treats unknown lag as current',()=>{
 const data=snapshot([row('an.authorship',{behind:{state:'behind'},pending_since:iso(180)})]);
 const three=readinessModel(data,[scope()],now);
 assert.equal(readinessDial(three).fraction,.25);
 // Three and six hours share a severity color, but have different pie areas.
 for(const [minutes,fraction] of [[0,0],[3,1/240],[45,1/16],[180,.25],[360,.5],[540,.75],[720,1],[721,1],[2880,1]]){
  data.freshness[1].pending_since=iso(minutes);
  const dial=readinessDial(readinessModel(data,[scope()],now));
  assert.equal(dial.kind,'lag');assert.equal(dial.fraction,fraction);
  if(fraction>0&&fraction<1)assert(dial.path&&!/NaN|Infinity/.test(dial.path));else assert.equal(dial.path,null);
 }
 data.freshness[1].pending_since=iso(180);
 assert.equal(readinessDial(readinessModel(data,[scope()],now+60*60000)).fraction,1/3);
 for(const lagMs of [null,NaN,Infinity,-1])assert.equal(readinessDial({...three,lagMs}).kind,'unknown');
 for(const kind of ['unknown','saved','scheduled'])assert.equal(readinessDial({...three,kind}).kind,'unknown');
 assert.equal(readinessDial({...three,kind:'missing'}).kind,'missing');
 assert.equal(readinessDial(readinessModel(snapshot([row('an.authorship')]),[scope()],now)).kind,'check');
 assert.equal(readinessDial(readinessPreview(three,'clear',now)).kind,'check');
 assert.equal(readinessDial(readinessPreview(three,'hours',now)).fraction,187/720);
 assert.equal(readinessDial(readinessPreview(three,'unknown',now)).kind,'unknown');
 assert.equal(readinessDial(readinessPreview(three,'missing',now)).kind,'missing');
});
test('no known staleness keeps the source observation gap, and disconnects never report green',()=>{
 const data=snapshot([row('an.authorship')]);
 assert.equal(readinessModel(data,[scope()],now).checkedAt,now-120000);
 assert.match(readinessModel(data,[scope()],now).sentence,/Activity since then/);
 assert.equal(readinessModel(data,[scope()],now,true).kind,'unknown');
 assert.equal(readinessModel(null,[scope()],now).kind,'unknown');
 assert.equal(readinessModel(snapshot(),[scope()],now).kind,'unknown');
});
test('cached results cannot become current merely because a new analysis finished',()=>{
 const data=snapshot([row('an.authorship')]);
 assert.equal(readinessModel(data,[scope('writing_messages',{updatedAt:now-3600000})],now).kind,'saved');
 assert.equal(readinessModel(data,[scope('writing_messages',{refreshError:'offline'})],now).kind,'saved');
});
test('first-build output absence survives real queued and running reason changes',()=>{
 const data=snapshot([row('an.authorship',{as_of:null,output_missing:true,behind:{state:'behind',count:128,unit:'message'},reason:{code:'never-built',text:'Not built'}})]);
 const empty=scope('writing_messages',{empty:true});
 assert.equal(readinessModel(data,[empty],now).kind,'missing');
 // Real ledger responses replace never-built with running even while queued.
 for(const state of ['queued','running']){
  data.freshness[1].reason={code:'running',text:'Updating now'};
  data.freshness[1].catching_up='op-writing';
  data.operations=[{id:'root',label:'Background update',state:'running',children:[{id:'op-writing',label:'Writing',state,step:{done:58,total:null,unit:'message'}}]}];
  const model=readinessModel(data,[empty],now);
  assert.equal(model.kind,'missing');assert.equal(model.stages[1].done,58);assert.equal(model.stages[1].total,null);
  assert.equal(readinessModel(data,[scope('writing_messages',{empty:false})],now).kind,'behind');
  assert.equal(readinessModel(data,[scope()],now).kind,'behind');
  assert.equal(readinessModel(data,[empty,scope('usage',{empty:false})],now).kind,'behind');
 }
 data.operations=[];Object.assign(data.freshness[1],{output_missing:false,reason:null,catching_up:null,behind:{state:'current'},as_of:iso(0)});
 assert.equal(readinessModel(data,[empty],now).kind,'current','a finished empty query remains a valid empty result');
});
test('one unindexed source cannot turn available or filtered results into a missing output',()=>{
 const data=snapshot([row('view'),row('conv:new',{as_of:null,output_missing:true,behind:{state:'behind'},reason:{code:'indexing',text:'Indexing now'}})]);
 for(const empty of [false,true,undefined]){
  const population=scope('library',{empty,query:{where:[{field:'source_kind',op:'=',value:'new',negated:true}]}});
  assert.equal(readinessModel(data,[population],now).kind,'behind');
 }
 data.freshness[0]={...data.freshness[0],as_of:null,output_missing:true,behind:{state:'behind'},reason:{code:'indexing',text:'Indexing now'}};
 assert.equal(readinessModel(data,[scope('library',{empty:true})],now).kind,'missing');
 assert.equal(readinessModel(data,[scope('library',{empty:false})],now).kind,'behind');
});
test('page saved-result checks use each result’s dependencies before reducing scopes',()=>{
 const data=snapshot([row('an.authorship',{as_of:iso(1)})]);
 const usage=scope('usage',{updatedAt:now-60*60000,empty:false});
 const writing=scope('writing_messages',{updatedAt:now,empty:false});
 for(const scopes of [[usage],[writing],[usage,writing],[writing,usage]])assert.equal(readinessModel(data,scopes,now).kind,'current');
 assert.equal(readinessModel(data,[usage,{...writing,updatedAt:now-60*60000}],now).kind,'saved');
});
test('preview switching retains scope and never mutates the processing snapshot or registrations',()=>{
 const data=snapshot([row('an.authorship')]),scopes=[scope()],before=structuredClone({data,scopes});
 const live=readinessModel(data,scopes,now);
 for(const state of ['minutes','recent','hours','orange','days','clear','missing','running','paused','failed','unknown']){
  const preview=readinessPreview(live,state,now);assert(preview.preview);assert.deepEqual(preview.scopes,scopes);if(['paused','unknown'].includes(state)){assert.equal(preview.nextAt,null);assert(preview.stages.every(stage=>stage.nextAt===null));}
 }
 assert.deepEqual({data,scopes},before);assert.equal(readinessPreview(live),live);
});
test('severity follows observed age at each boundary, independently of processing causes',()=>{
 for(const [minutes,tone] of [[0,'minutes'],[14.99,'minutes'],[15,'recent'],[120,'recent'],[120.01,'hours'],[480,'hours'],[480.01,'orange'],[1440,'orange'],[1440.01,'days']])assert.equal(readinessTone(minutes*60000),tone);
 assert.equal(readinessTone(null),'unknown');
 for(const state of ['running','queued','blocked','paused','failed']){
  const data=snapshot([row('an.authorship',{pending_since:iso(540),behind:{state:'behind'},reason:{code:'cpu',text:'CPU busy'},catching_up:'op-writing'})]);
  data.operations=[{id:'op-writing',label:'Writing',state}];
  const result=readinessModel(data,[scope()],now);
  assert.equal(result.tone,'orange');assert.equal(result.label,'9 hours behind');
  assert(!/CPU|busy|running|queued|blocked|paused|failed/i.test(result.label+' '+result.sentence));
  assert.equal(result.stages[1].reason,'CPU busy');
 }
});

import {dataSources} from '../../../src/data-taxonomy-model.ts';
test('every app query dataset declares dependencies, including non-chart tables',()=>{
 for(const dataset of Object.keys(dataSources))assert(!readinessDomains(scope(dataset)).some(domain=>domain.startsWith('@unknown:')),dataset);
});

import {cardReadinessSource} from '../../../src/data-readiness-card.ts';
import {usageTopics} from '../../../src/usage-workspace-model.ts';
import {encodeQuery,EMPTY_QUERY} from '@pythia-software/query-table-core';
test('durable saved cards retain their resolved source with an empty local query cache',()=>{
 const previous={location:globalThis.location,storage:globalThis.localStorage};
 globalThis.location={origin:'http://localhost'};
 globalThis.localStorage={getItem:()=>null};
 try{
  const data=snapshot([row('view'),row('an.authorship'),row('an.ledger'),row('@labels')]);
  Object.assign(data.freshness[0],{task_id:'conversations',pending_since:iso(540),behind:{state:'behind',count:8,unit:'conversation'}});
  data.background.items.push({id:'conversations',label:'New conversations',state:'scheduled',next_at:iso(-3)});
  for(const [dataset,query] of [
   ['library',{...EMPTY_QUERY,where:[{field:'source_kind',op:'=',value:'codex'}]}],
   ['writing_messages',{...EMPTY_QUERY,where:[{field:'message_label',op:'=',value:'planning'}]}],
   ['model_requests',{...EMPTY_QUERY,where:[{field:'harness',op:'=',value:'codex'}]}],
  ]){
   const address=(dataset==='library'?'/library?':'/analyze?analyze_mode=query&analyze_data='+dataset+'&')+'q_'+dataset+'=@aaaaaaaaaaaaaaaaaaaaaa';
   assert.equal(cardReadinessSource(address).dataset,'unknown','the durable definition is not in the local cache');
   const resolved=cardReadinessSource(address,{dataset,query});
   assert.deepEqual(resolved,{dataset,query});
   const result=readinessModel(data,[scope(resolved.dataset,{query:resolved.query})],now);
   assert.equal(result.kind,'behind');assert.equal(result.lagMs,540*60000);
   assert(result.stages.some(stage=>stage.id==='conv:codex'));
   if(dataset==='writing_messages')assert(result.stages.some(stage=>stage.id==='@labels'),'stored filters retain their dependencies');
   assert.equal(new URL(readinessSettingsURL(result),'http://localhost').searchParams.get('highlight'),'section:background-conversations');
  }
  // A resolved population belongs to the displayed result even if an older
  // query remains encoded in its navigation URL.
  const stored={dataset:'writing_messages',query:{...EMPTY_QUERY,where:[{field:'message_label',op:'=',value:'planning'}]}};
  assert.deepEqual(cardReadinessSource('/library?q_library='+encodeURIComponent(encodeQuery(EMPTY_QUERY)),stored),stored);
 }finally{
  globalThis.location=previous.location;
  if(previous.storage===undefined)delete globalThis.localStorage;else globalThis.localStorage=previous.storage;
 }
});
test('bespoke chart sources use dataset IDs and retain their analysis backlog',()=>{
 const previous=globalThis.location;globalThis.location={origin:'http://localhost'};
 try{
  const data=snapshot([row('an.authorship',{behind:{state:'behind'}}),row('an.ledger',{behind:{state:'behind'}})]);
  for(const [topic,dataset,dependency] of [['habits','rhythm','an.authorship'],['parallelism','parallelism','conv:codex'],['thinking-overview','model_requests','an.ledger'],['thinking-detail','model_requests','an.ledger']]){
   const source=cardReadinessSource('/analyze?analyze_mode=dashboards&analyze_topic='+topic);
   assert.equal(source.dataset,dataset,topic);
   const model=readinessModel(data,[scope(source.dataset,{query:source.query})],now);
   assert(model.stages.some(stage=>stage.id===dependency),topic);
   assert.equal(model.kind,topic==='parallelism'?'current':'behind',topic);
  }
 }finally{globalThis.location=previous;}
});
test('all dashboard card sources resolve without an optional saved-query token',()=>{
 const previous=globalThis.location;globalThis.location={origin:'http://localhost'};
 try{
  for(const topic of Object.keys(usageTopics)){
   const source=cardReadinessSource('/analyze?analyze_mode=dashboards&analyze_topic='+topic);
   assert.notEqual(source.dataset,'unknown',topic);
  }
  const query={...EMPTY_QUERY,where:[{field:'message_label',op:'=',value:'planning'}]};
  const source=cardReadinessSource('/analyze?analyze_mode=query&analyze_data=writing_messages&q_writing_messages='+encodeURIComponent(encodeQuery(query)));
  assert.equal(source.dataset,'writing_messages');assert(readinessDomains(source).includes('@labels'));
  assert.equal(cardReadinessSource('/unrecognized').dataset,'unknown');
 }finally{globalThis.location=previous;}
});

test('a periodic source check does not invalidate an unchanged displayed result',()=>{
 const data=snapshot([row('an.authorship',{as_of:iso(120)})]);
 assert.equal(readinessModel(data,[scope('writing_messages',{updatedAt:now-60*60000})],now).kind,'current');
});

// Hidden pages stay mounted in the real app, and warming a card is not a
// displayed result. Registration lifetime must therefore use its DOM boundary.
import {registerReadiness,displayedReadinessScopes} from '../../../src/data-readiness-store.ts';
test('unmounting and hidden retained pages remove their scopes from the header',()=>{
 const element=(state={})=>({isConnected:state.connected!==false,getClientRects:()=>state.visible===false?[]:[{}],closest:selector=>selector==='.view'?{classList:{contains:()=>state.active!==false}}:state.hidden?{}:null});
 const disposals=[registerReadiness(scope('writing_messages'),element()),registerReadiness(scope('tool_calls'),element({active:false})),registerReadiness(scope('findings'),element({visible:false})),registerReadiness(scope('usage'),element({hidden:true})),registerReadiness(scope('rhythm'),element({connected:false}))];
 try{assert.deepEqual(displayedReadinessScopes().map(scope=>scope.dataset),['writing_messages']);disposals[0]();assert.equal(displayedReadinessScopes().length,0);}finally{disposals.forEach(dispose=>dispose());}
});

test('current summaries use the source check time, and never invent a missing time',()=>{
 const data=snapshot([row('an.authorship')]);
 data.freshness[0].as_of=iso(6);
 assert.equal(readinessModel(data,[scope()],now).label,'Up to date as of 6 minutes ago');
 const preview=readinessPreview(readinessModel(data,[scope()],now),'clear',now);
 assert.equal(preview.label,'Up to date as of 6 minutes ago');
 data.freshness[0].as_of=null;
 assert.equal(readinessModel(data,[scope()],now).label,'No updates waiting');
 assert.match(readinessModel(data,[scope()],now).sentence,/check time is unavailable/);
 data.freshness[1].behind.state='scheduled';
 assert.equal(readinessModel(data,[scope()],now).label,'Update scheduled');
});
test('stage copy explains progress without exposing processing identifiers or diagnostics',()=>{
 assert.equal(readinessStageLabel('an.ledger','Tools ledger'),'Read tool calls');
 assert.equal(readinessStageLabel('an.tools','Tools rollup'),'Summarize tool use');
 assert.equal(readinessStageLabel('@unknown:writing_messages'),'Check for updates');
 const stage={id:'an.tools',label:'Tools',state:'blocked',reason:'internal exception: join generation mismatch',row:row('an.tools',{reason:{code:'blocked',text:'raw diagnostic'}})};
 assert.match(readinessStageDescription(stage),/earlier step couldn’t finish/);
 assert.equal(readinessStageState(stage),'Waiting');
 assert.equal(readinessStageDescription({...stage,state:'behind',next:'updates with the next manual index',row:row('an.tools',{reason:{code:'changed'}})}),'Runs when you request an update in settings.');
 assert.equal(readinessStageState({...stage,state:'future-internal-state'}),'Status unavailable');
 assert.equal(readinessProgressText({...stage,done:58,total:128,unit:'message'}),'58 of 128 messages');
 assert.equal(readinessProgressText({...stage,done:1,unit:'message'}),'1 message done');
 assert.equal(readinessProgressText({...stage,count:1,unit:'message'}),'1 message waiting');
 assert.equal(readinessProgressText(stage),'No progress details yet');
 assert(!readinessStageDescription({...stage,row:undefined}).includes('internal exception'));
});
test('per-source stages collapse into one row per step, state and next run, with Done rows folded together',()=>{
 const capture=(host,source,state)=>row(`capt:${host}/${source}`,{label:`Captures (${source} on ${host==='studio'?'Studio':'Air'})`,task_id:'preserve',
  ...state==='behind'?{behind:{state:'behind'},reason:{code:'not-indexed',text:'Captured data is not indexed yet'},next:'updates with the next daily update'}:{}});
 const data=snapshot([...['claude','tl1'].map(name=>row('conv:'+name,{label:`Conversations (${name})`,task_id:'conversations'})),
  capture('studio','claude','behind'),capture('studio','codex','behind'),capture('studio','tl1'),capture('air','claude','behind'),capture('air','tl1'),row('an.authorship')]);
 data.freshness.shift();
 data.background.items.push({id:'preserve',label:'Preserve originals',state:'scheduled',cadence:'daily',next_at:iso(-600)},{id:'conversations',label:'New conversations',state:'current',next_at:iso(-3)});
 const model=readinessModel(data,[scope()],now),groups=readinessGroups(model.stages);
 assert.equal(model.kind,'behind');
 assert.deepEqual(groups.map(group=>group.label),['Read saved conversations','Done']);
 const [waiting,done]=groups;
 assert.deepEqual(waiting.subjects,['claude, codex on Studio','claude on Air']);
 assert.equal(waiting.stages.length,3);assert.equal(waiting.nextAt,Date.parse(iso(-600)));
 assert.equal(waiting.progress,null,'waiting without counts draws no empty progress');
 assert.equal(waiting.detail,'Runs with the next daily update.','captured data names the schedule that reads it');
 assert.deepEqual(done.subjects,['Read new conversations (2 sources)','Read saved conversations (2 sources on 2 Macs)','Estimate your writing']);
 assert.equal(model.nextAt,Date.parse(iso(-600)),'the summary’s next check is the one that reads what is waiting');
 // A different next run, or an owner without a schedule, is a separate row
 // that says how it will run.
 const manual=data.freshness.find(row=>row.domain==='capt:air/claude');
 manual.task_id='other-sources';manual.next='updates with the next manual index';
 data.background.items.push({id:'other-sources',label:'Other sources',state:'manual',cadence:'manual',next_at:null});
 const split=readinessGroups(readinessModel(data,[scope()],now).stages);
 assert.deepEqual(split.map(group=>group.subjects.join(' · ')).slice(0,2),['claude, codex on Studio','claude on Air']);
 assert.match(split[1].detail,/request an update in settings/);assert.equal(split[1].nextAt,null);
 // Failures come before waiting work; an operation's progress shows once.
 data.operations=[{id:'op-ic',state:'running',label:'Index captures',step:{done:1,total:3,unit:'source'}}];
 for(const row of data.freshness.filter(row=>row.domain.startsWith('capt:studio')&&row.behind.state==='behind'))row.catching_up='op-ic';
 data.freshness.find(row=>row.domain==='an.authorship').behind.state='failed';
 const ordered=readinessGroups(readinessModel(data,[scope()],now).stages);
 assert.deepEqual(ordered.map(group=>group.section),['attention','running','waiting','done']);
 assert.equal(ordered[1].progress?.operation?.id,'op-ic');assert.deepEqual(ordered[1].subjects,['claude, codex on Studio']);
});
test('next-run copy states the time and how long until it, or that it is overdue',()=>{
 assert.equal(readinessNextText(null,now),'');
 assert.match(readinessNextText(now+12*60000,now),/^Next update .+, in 12 minutes$/);
 assert.match(readinessNextText(now+(10*60-1)*60000,now),/, in 10 hours$/);
 assert.match(readinessNextText(now-60000,now),/^Due since /);
 assert.deepEqual(readinessSubjects(['claude','codex','']),['claude, codex']);
 assert.equal(readinessHasProgress({id:'x',label:'x',state:'behind',reason:'',next:''}),false);
 assert.equal(readinessHasProgress({id:'x',label:'x',state:'behind',reason:'',next:'',count:4}),true);
});
test('a row read by several tasks uses whichever is scheduled first',()=>{
 const tl1=row('capt:studio/tl1',{label:'Captures (tl1 on Studio)',task_id:'preserve',task_ids:['preserve','other-sources'],behind:{state:'behind'},reason:{code:'not-indexed',text:'Captured data is not indexed yet'},next:'updates with the next daily update'});
 const data=snapshot([tl1]);
 const at=(hours,minutes)=>new Date(Date.UTC(2026,10,8,hours,minutes)).toISOString();
 data.background.items.push({id:'preserve',label:'Preserve originals',state:'scheduled',cadence:'daily',next_at:at(3,0)},{id:'other-sources',label:'Other sources',state:'scheduled',cadence:'hourly',next_at:at(3,54)});
 const stage=()=>readinessModel(data,[scope('usage')],now).stages.find(stage=>stage.id==='capt:studio/tl1');
 assert.equal(stage().item.id,'preserve');assert.equal(stage().nextAt,at(3,0),'a daily task due sooner beats an hourly one due later');
 assert.equal(readinessStageDescription(stage()),'Runs with the next daily update.');
 data.background.items.find(item=>item.id==='other-sources').next_at=at(2,54);
 assert.equal(stage().item.id,'other-sources');assert.equal(stage().nextAt,at(2,54));
 assert.equal(readinessStageDescription(stage()),'Runs with the next hourly update.');
 // A task with no planned run never hides one that has a time.
 data.background.items.find(item=>item.id==='preserve').next_at=null;data.background.items.find(item=>item.id==='other-sources').next_at=at(3,54);
 assert.equal(stage().item.id,'other-sources');
 data.background.items.find(item=>item.id==='other-sources').next_at=null;
 assert.equal(stage().item.id,'preserve');assert.equal(stage().nextAt,null);
});
