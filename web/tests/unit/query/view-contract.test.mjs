import {dataEntities,dataEntityGroups,dataSources,describeDataSource,resolveGuideEntity} from '../../../src/data-taxonomy-model';
import searchCases from '../../../../testdata/search-draft-cases.json';
import timelineVisibilityCases from '../../../../testdata/timeline-visibility-cases.json';
import inspectionCases from '../../../../testdata/json-inspection-cases.json';
import {parseDetailTarget,detailReference,revealQueryDetail} from '../../../src/query-attention';
import {findingsRank} from '../../../src/findings-route';
import {createDetailLanding,nativeDetailsReady,attentionMutationMatters,attentionReferenceAt,attentionTargetNoun,toggleAttentionTarget} from '../../../src/view-attention-model';
import {createAttentionRegistry,parseJsonInspections} from '@pythia-software/query-table-core';
import test from 'node:test';
import assert from 'node:assert/strict';
import {EMPTY_QUERY,encodeQuery,decodeQueryStrict} from '@pythia-software/query-table-core';
import {viewFields,fieldsForPage,encodeViewValue,parseViewValue,retainPageView,viewPageForURL} from '../../../src/view-contract';
import {portableQueryLinks} from '../../../src/portable-query-link';
import {viewRequestURL} from '../../../src/query-request';
import {withSearchDateWindow,emptyTextSearch,refreshSearchToday,readTextSearch} from '../../../src/text-search-model';
import {restoreRollingDefinition} from '../../../src/query-links';
import {readRollingWindow, resolveRollingQuery, updateQueryWindowURL, queryURL, queryFromURL} from '../../../src/query-links';
import {todayBounds} from '../../../src/day-window';
import {readDashboardLink} from '../../../src/usage-dashboard-model';

const query={...EMPTY_QUERY,where:Array.from({length:70},(_,i)=>({field:'model',op:'!=',value:`${i}-${Array.from({length:60},(_,n)=>String.fromCharCode(33+(i*31+n*17+i*n)%90)).join('')}`}))};
const address=q=>'https://pharos.test/analyze?'+new URLSearchParams({analyze_mode:'query',analyze_data:'usage',q_usage:encodeQuery(q)});

test('JSON inspection definitions use the shared TypeScript and Go link fixtures',()=>{
 for(const item of inspectionCases){
  const query={...EMPTY_QUERY,select:[{field:'id'},{field:'tool_name'}],itemDetails:{modes:['expand'],jsonInspections:item.inspections}};
  if(item.valid){assert.deepEqual(decodeQueryStrict(encodeQuery(query)),item.query,item.name);assert.deepEqual(decodeQueryStrict(item.token),item.query,item.name);assert.deepEqual(decodeQueryStrict(item.goToken),item.query,`${item.name}: published Go encoder`);}
  else assert.throws(()=>encodeQuery(query),undefined,item.name);
 }
 const entry=inspectionCases[0].inspections[0];
 assert.deepEqual(parseJsonInspections([{...entry,state:{...entry.state,hover:true,focus:'transient'}}]),[entry]);
});

test('semantic detail targets preserve typed identities, reject ambiguous paths, and cancel navigation',async()=>{
 for(const target of [{rowId:'0'},{rowId:0,field:'arguments_json'},{rowId:'a:b',field:'arguments_json',path:'$["quote\\"key"][21]'}])assert.deepEqual(parseDetailTarget(detailReference(target)),target);
 for(const target of [{},{rowId:null},{rowId:false},{rowId:'a',path:'$'},{rowId:'a',field:'arguments_json',path:'$.events'},{rowId:'a',field:''},{rowId:'a',unknown:true}])assert.throws(()=>detailReference(target));
 const registry=createAttentionRegistry(),target={rowId:0,field:'arguments_json',path:'$["events"][21]'},calls=[];
 registry.register({dataset:'pharos_mcp_calls',rowId:0},{reveal:(value,signal)=>{calls.push(value);return !signal.aborted}});
 const navigation=new AbortController();
 assert.equal(await revealQueryDetail(registry,'pharos_mcp_calls',detailReference(target),navigation.signal),'revealed');
 assert.deepEqual(calls,[{...target,dataset:'pharos_mcp_calls'}]);
 navigation.abort();assert.equal(await revealQueryDetail(registry,'pharos_mcp_calls',detailReference(target),navigation.signal),'cancelled');assert.equal(calls.length,1);
});

test('host view codecs preserve explicit false, empty values, ordered IDs and relative intent',()=>{
 const values={card_window:'today',card_filters:false,search_cards:{},delegation_columns:['cost','tokens'],reader_expanded:['message:m'],reader_navigator:['timeline','context','turns'],disclosures:{'row:r:input':true},findings_rank:'tokens',timeline_visibility:{modes:{values:['C.report.finding','?'],excluded:false},events:{values:['friction'],excluded:true},tools:{values:['error'],excluded:true},origins:{values:['typed'],excluded:false}}};
 for(const [key,value]of Object.entries(values))assert.deepEqual(parseViewValue(viewFields[key],encodeViewValue(viewFields[key],value)),value,key);
 assert.equal(fieldsForPage('findings/settings').findings_rank,undefined);
 assert.equal(fieldsForPage('analyze/query/tool_calls','tool_calls').record_keyword.param,'record_tool_calls');
 for(const [key,raw]of [['card_filters','false'],['card_window','forever'],['delegation_columns','[1]'],['search_state','{}']])assert.throws(()=>parseViewValue(viewFields[key],raw),key);
 for(const sample of timelineVisibilityCases){
  if(sample.valid)assert.deepEqual(parseViewValue(viewFields.timeline_visibility,encodeViewValue(viewFields.timeline_visibility,sample.state)),sample.state,sample.name);
  else assert.throws(()=>encodeViewValue(viewFields.timeline_visibility,sample.state),undefined,sample.name);
 }
 for(const count of [1000,1001]){
  const state={modes:{values:Array(count).fill('?'),excluded:false}};
  if(count===1000)assert.doesNotThrow(()=>encodeViewValue(viewFields.timeline_visibility,state));
  else assert.throws(()=>encodeViewValue(viewFields.timeline_visibility,state));
 }
 assert.deepEqual(parseViewValue(viewFields.timeline_visibility,'{}'),{});
 assert.deepEqual(parseViewValue(viewFields.reader_navigator,'[]'),[]);
 for(const raw of ['[\"unknown\"]','[\"turns\",\"turns\"]','{}'])assert.throws(()=>parseViewValue(viewFields.reader_navigator,raw));
});

test('Safety links restore their own section, lifecycle, case and evidence disclosures',()=>{
 const url=new URL('https://pharos.test/findings?optimize_area=safety');
 const values={optimize_area:'safety',safety_list:'handed_off',safety_place:'machine:host-1',safety_rank:'calls',safety_case:'safety_stable',disclosures:{'safety:safety_stable:evidence:source-1':true}};
 for(const [name,value]of Object.entries(values)){
  const field=viewFields[name];url.searchParams.set(field.param,encodeViewValue(field,value));
  assert.deepEqual(parseViewValue(field,url.searchParams.get(field.param)),value);
 }
 assert.equal(viewPageForURL(url),'findings/safety');
 const fields=fieldsForPage('findings/safety');
 for(const name of Object.keys(values))assert.ok(fields[name],name);
 assert.equal(fields.findings_rank,undefined);
 assert.equal(fieldsForPage('findings').safety_case,undefined);
 assert.throws(()=>parseViewValue(viewFields.safety_list,'won'));
 assert.equal(viewPageForURL(new URL('https://pharos.test/findings?optimize_area=safety&view=table')),'findings/safety/table');
 assert.equal(viewPageForURL(new URL('https://pharos.test/findings?optimize_area=safety&view=settings')),'findings/safety/settings');
 assert.ok(fieldsForPage('findings/safety/table').safety_case);
 assert.equal(fieldsForPage('findings/safety/settings').safety_case,undefined);
 assert.equal(fieldsForPage('findings/safety/table').findings_rank,undefined);
 assert.equal(fieldsForPage('findings/safety/table').safety_rank,undefined);
 assert.equal(fieldsForPage('findings/safety/settings').safety_rank,undefined);
 assert.equal(fieldsForPage('findings').safety_rank,undefined);
 for(const rank of ['recent','calls','evidence'])assert.equal(parseViewValue(viewFields.safety_rank,encodeViewValue(viewFields.safety_rank,rank)),rank);
 assert.throws(()=>parseViewValue(viewFields.safety_rank,'usd'));
});

test('large links remain self-contained until immutable library persistence succeeds',async()=>{
 let release,received,replaced=[];let current=address(query);
 const original=current;
 const save=new Promise(resolve=>{release=resolve;});
 const promote=portableQueryLinks('pharos_usage',{save:async(_key,snapshot)=>{received=snapshot;await save;return snapshot;},resolve:async()=>null});
 const pending=promote('q_usage',()=>current,url=>{current=url;replaced.push(url);});
 assert.equal(current,original,'copying while Save is pending carries the full definition');
 release();await pending;
 assert.equal(replaced.length,1);
 assert.equal(new URL(current).searchParams.get('q_usage'),'@'+received.code);
 assert.deepEqual(decodeQueryStrict(received.token),decodeQueryStrict(encodeQuery(query)));
});

test('a save failure or stale completion cannot replace the current view',async()=>{
 let current=address(query),writes=0;
 const broken=portableQueryLinks('pharos_usage',{save:async()=>{throw Error('quota')},resolve:async()=>null});
 await assert.rejects(broken('q_usage',()=>current,()=>writes++),/quota/);
 assert.equal(writes,0);assert.ok(!new URL(current).searchParams.get('q_usage').startsWith('@'));
 let release;
 const saving=portableQueryLinks('pharos_usage',{save:async(_key,snapshot)=>{await new Promise(resolve=>release=resolve);return snapshot;},resolve:async()=>null});
 const pending=saving('q_usage',()=>current,()=>writes++);
 current=address({...EMPTY_QUERY,limit:5});release();await pending;assert.equal(writes,0);
});

test('linked assumptions follow rows, metrics and details and never replace explicit request assumptions',()=>{
 const view=new URL('https://pharos.test/analyze?'+new URLSearchParams({analyze_carbon:JSON.stringify({grid:'custom',customGrid:123}),analyze_parallelism:JSON.stringify({gap:15,counting:'agent'})}));
 for(const operation of ['rows-v2','query-v3','metrics','item?id=r']){
  const url=new URL(viewRequestURL('/api/query/parallelism/'+operation,view),view.origin);
  assert.deepEqual(JSON.parse(url.searchParams.get('parallelism_settings')),{gap:15,counting:'agent'});
 }
 const partial=new URL('https://pharos.test/analyze?'+new URLSearchParams({analyze_parallelism:JSON.stringify({range:'7d'})}));
 assert.deepEqual(JSON.parse(new URL(viewRequestURL('/api/query/parallelism/metrics',partial),partial.origin).searchParams.get('parallelism_settings')),{gap:60,counting:'workspace'});
 assert.match(viewRequestURL('/api/query/usage/metrics',view),/carbon_settings=/);
 assert.equal(new URL(viewRequestURL('/api/query/usage/metrics?carbon_settings=explicit',view),view.origin).searchParams.get('carbon_settings'),'explicit');
 assert.equal(viewRequestURL('https://elsewhere.test/api/query/usage/metrics',view),'https://elsewhere.test/api/query/usage/metrics');
});

test('elapsed lookbacks renew on a later execution while fixed custom dates stay fixed across DST',()=>{
 const opened=new Date('2026-10-29T18:00:00Z'),reopened=new Date('2026-11-03T18:00:00Z');
 const draft=withSearchDateWindow({...emptyTextSearch(),text:'failure',match:'literal'},'7d',opened);
 assert.equal(refreshSearchToday(draft,reopened).scope.from,'2026-10-27T18:00:00.000Z');
 const fixed={...draft,dateWindow:undefined};assert.equal(refreshSearchToday(fixed,reopened),fixed);
 const read=clock=>readDashboardLink(new URLSearchParams({analyze_days:'7'}),{},()=>true,{first:'first_usage_at',last:'last_usage_at'},undefined,clock);
 assert.deepEqual(read(opened).where,read(reopened).where);
 assert.deepEqual(read(reopened).where.map(term=>term.value),['-7d','+0s']);
});

test('the same contract is exported to agents, and each field has a runtime owner',async()=>{
 const {readFileSync}=await import('node:fs');
 const {join}=await import('node:path');
 const {assets}=await import('../helpers/classic-script.mjs');
 const {viewContract,delegatedViewFields}=await import('../../../src/view-contract');
 const root=join(assets,'../../..');
 const map=JSON.parse(readFileSync(join(assets,'site-map.json'),'utf8'));
 assert.deepEqual(map.view_contract.fields,JSON.parse(JSON.stringify(viewContract.fields)));
 assert.deepEqual(map.view_contract.targets,viewContract.targets);
 const ledger=JSON.parse(readFileSync(join(root,'docs/view-controls.json'),'utf8'));
 const packages=JSON.parse(readFileSync(join(root,'web/package.json'),'utf8'));
 for(const name of ['core','react','ui','codegen']){
  const key='@pythia-software/query-table-'+name,version=packages.dependencies[key]??packages.devDependencies[key];
  assert.equal(version,map.query_types.version);
  assert.equal(ledger.dependencies[key].version,version);
 }
 assert.ok(readFileSync(join(root,'go.mod'),'utf8').includes('github.com/Pythia-Software/query-table/backends/go v'+map.query_types.version));
 for(const name of ['JsonInspectionState','JsonInspectionScope','JsonMatchIdentity','ScopedJsonInspectionState'])assert.ok(map.query_types.types[name],`${name} is discoverable`);
 const runtimeBindings=new Set(ledger.bindings.map(binding=>binding.field));
 for(const [name] of Object.entries(viewFields)){
  if(delegatedViewFields[name]){const owner=delegatedViewFields[name];assert.ok(readFileSync(join(root,owner.file),'utf8').includes(owner.symbol),name);}
  else assert.ok(runtimeBindings.has(name),`${name} has no discovered runtime binding`);
 }
 for(const control of ledger.controls){if(['details','summary'].includes(control.tag)||control.disclosure)assert.notEqual(control.contract,'settings',`Unbound accordion: ${control.file} ${control.owner}`);assert.notEqual(control.contract,'unclassified',control.key);assert.ok(control.reason,control.key);if(control.contract.startsWith('view.'))assert.ok(viewFields[control.contract.slice(5)],control.contract);}
});

test('accordion validation rejects ledger-only bindings and accepts the shared React and classic APIs',async()=>{
 const {join}=await import('node:path');
 const {pathToFileURL}=await import('node:url');
 const {assets}=await import('../helpers/classic-script.mjs');
 const {discoverSourceControls,discoverHTMLControls,validateDisclosureControls}=await import(pathToFileURL(join(assets,'../../../web/view-controls.mjs')).href);
 const reviewed=controls=>controls.map(control=>({...control,contract:'view.disclosures',reason:'Durable section.'}));
 const jsx=source=>reviewed(discoverSourceControls('web/src/example.tsx',source).controls);
 const classic=source=>reviewed(discoverSourceControls('internal/archive/assets/example.js',source).controls);
 const html=source=>reviewed(discoverHTMLControls('internal/archive/assets/example.html',source,{fragment:true}));
 assert.doesNotThrow(()=>validateDisclosureControls(jsx('<ViewDisclosure viewKey="sample:help" summary="Help">Body</ViewDisclosure>')));
 for(const source of ['<ViewDisclosure summary="Help" />','<ViewDisclosure viewKey="" summary="Help" />'])assert.throws(()=>validateDisclosureControls(jsx(source)),/nonempty viewKey/);
 assert.throws(()=>validateDisclosureControls(jsx('<details data-view-field="disclosures"><summary>Help</summary>Body</details>')),/Missing shared accordion binding/);
 assert.throws(()=>validateDisclosureControls(jsx('<FutureAccordion aria-expanded={open} data-view-field="disclosures" />')),/Missing shared accordion binding/);
 assert.doesNotThrow(()=>validateDisclosureControls(jsx('function Preview(){ const [open,setOpen]=useViewEntry("disclosures","message:stable-id",false); return <button data-view-field="disclosures" aria-expanded={open} onClick={()=>setOpen(!open)}>Read more</button>; }')));
 assert.throws(()=>validateDisclosureControls(classic('function render(){ const help=node("details"); }')),/Missing shared accordion binding/);
 assert.throws(()=>validateDisclosureControls(classic('function render(){ const help=node("details"); const other=node("div"); other.dataset.viewKey="other"; }')),/Missing shared accordion binding/);
 assert.doesNotThrow(()=>validateDisclosureControls(classic('function render(){ const help=node("details"), summary=node("summary"); help.dataset.viewKey="sample:help"; help.append(summary); }')));
 assert.doesNotThrow(()=>validateDisclosureControls(classic('function render(){ const toggle=node("button"); toggle.dataset.viewKey="sample:help"; toggle.setAttribute("aria-controls","help"); toggle.setAttribute("aria-expanded","false"); }')));
 assert.throws(()=>validateDisclosureControls(classic('function render(){ const toggle=node("button"); toggle.dataset.viewKey="sample:help"; toggle.setAttribute("aria-expanded","false"); }')),/Missing shared accordion binding/);
 assert.doesNotThrow(()=>validateDisclosureControls(html('<details data-view-key="sample:help"><summary>Help</summary>Body</details>')));
 assert.doesNotThrow(()=>validateDisclosureControls(html("<details data-view-key='sample:help'><summary>Help</summary></details>")));
 for(const source of ['<details data-view-field="disclosures"><summary>Help</summary></details>','<button data-view-key="sample:help" aria-expanded="false">Help</button>'])assert.throws(()=>validateDisclosureControls(html(source)),/Missing shared accordion binding/);
 const raw=jsx('<details><summary>Help</summary>Body</details>');
 for(const contract of ['settings','legacy'])assert.throws(()=>validateDisclosureControls(raw.map(control=>({...control,contract}))),/Accordion openness is view state/);
 for(const contract of ['editing','transient','view.parallelism','view.nav_expanded'])assert.doesNotThrow(()=>validateDisclosureControls(raw.map(control=>({...control,contract}))));
});


test('Today follows the viewer unless pinned, and native datetime edits release its intent',()=>{
 const old={...EMPTY_QUERY,where:[{field:'started_at',op:'>=',value:'2026-10-29T15:00:00Z'},{field:'started_at',op:'<',value:'+0s'}]},window={mode:'today',days:1,field:'started_at'},known=new Map(),clock=new Date('2026-11-02T18:00:00Z');
 const restored=restoreRollingDefinition(old,old,window,known,clock);
 assert.equal(restored.released,false);assert.equal(restored.query.where[0].value,todayBounds(clock)[0]);
 // A subsequent native URL write and host rerender do not reinterpret the
 // still-retained original definition as a manual datetime edit.
 assert.equal(restoreRollingDefinition(old,restored.query,window,known,clock).released,false);
 for(const timestamp of ['2026-10-26T06:00:00Z','2026-10-26T12:34:56Z']){
  const edited={...restored.query,where:[{...restored.query.where[0],value:timestamp},restored.query.where[1]]};
  const result=restoreRollingDefinition(edited,restored.query,window,known,clock);assert.equal(result.released,true);assert.equal(result.query,edited);
 }
 const pinned=restoreRollingDefinition(old,old,{...window,timezone:'Asia/Tokyo'},new Map(),clock);
 assert.equal(pinned.query.where[0].value,todayBounds(clock,'Asia/Tokyo')[0]);
});


test("partial linked parallelism views never inherit another viewer's saved counting assumptions",async()=>{
 const {readConcurrencySettings}=await import('../../../src/concurrency-settings');
 const savedWindow=globalThis.window,savedLocation=globalThis.location;
 try{
  globalThis.window={pharosPrefs:{get:()=>({gap:15,counting:'agent',repository:'saved-repo'}),set(){throw Error('must not save linked assumptions');}}};
  globalThis.location=new URL('https://pharos.test/analyze?'+new URLSearchParams({analyze_parallelism:JSON.stringify({range:'7d'})}));
  const settings=readConcurrencySettings();assert.equal(settings.gap,60);assert.equal(settings.counting,'workspace');assert.equal(settings.repository,'');assert.equal(settings.range,'7d');
 }finally{if(savedWindow===undefined)delete globalThis.window;else globalThis.window=savedWindow;if(savedLocation===undefined)delete globalThis.location;else globalThis.location=savedLocation;}
});

test('malformed linked parallelism uses the same product defaults for every query request',()=>{
 const view=new URL('https://pharos.test/analyze?analyze_mode=query&analyze_data=parallelism&analyze_parallelism=%7Bbad');
 for(const operation of ['rows-v2','query-v3','metrics','item?id=r']){
  const request=new URL(viewRequestURL('/api/query/parallelism/'+operation,view),view.origin);
  assert.deepEqual(JSON.parse(request.searchParams.get('parallelism_settings')),{gap:60,counting:'workspace'});
 }
});

test('retained page bindings cannot write foreign URL fields after navigation',async t=>{
 const {writeViewState}=await import('../../../src/view-state');
 const originals=new Map(['location','history','window'].map(key=>[key,Object.getOwnPropertyDescriptor(globalThis,key)]));
 t.after(()=>{for(const[key,descriptor]of originals){if(descriptor)Object.defineProperty(globalThis,key,descriptor);else delete globalThis[key]}});
 globalThis.location=new URL('https://pharos.test/work/w');
 globalThis.history={state:null,replaceState(_state,_title,url){globalThis.location=new URL(url);}};
 globalThis.window={dispatchEvent(){}};
 writeViewState('delegation_page','2');assert.equal(location.searchParams.get('delegation_page'),'2');
 globalThis.location=new URL('https://pharos.test/analyze?analyze_topic=tokens');
 const before=location.href;
 for(const [name,value]of Object.entries({delegation_page:'0',delegation_event:'',delegation_expanded:[],card_window:'default',findings_rank:'usd'}))writeViewState(name,value);
 assert.equal(location.href,before);
 writeViewState('nav_collapsed',false);assert.equal(location.searchParams.get('nav_collapsed'),'0');
 // Fields written together replace the history entry once, and not at all when unchanged.
 const {writeViewStates}=await import('../../../src/view-state');
 globalThis.location=new URL('https://pharos.test/work/w');
 let replaced=0;globalThis.history={state:null,replaceState(_state,_title,url){replaced++;globalThis.location=new URL(url);}};
 writeViewStates({reader_expanded:['message:a'],reader_collapsed:['message:b'],card_window:'default'});
 assert.equal(replaced,1);
 assert.deepEqual([location.searchParams.get('reader_expanded'),location.searchParams.get('reader_collapsed'),location.searchParams.has('card_window')],['["message:a"]','["message:b"]',false]);
 writeViewStates({reader_expanded:['message:a'],reader_collapsed:['message:b']});
 assert.equal(replaced,1);
});

test('viewer-dependent entry captures batch writes and respect explicit changes, cancellation and navigation',async t=>{
 const {captureViewEntry,writeViewState}=await import('../../../src/view-state');
 const originals=new Map(['location','history','window','requestAnimationFrame'].map(key=>[key,Object.getOwnPropertyDescriptor(globalThis,key)]));
 t.after(()=>{for(const[key,descriptor]of originals){if(descriptor)Object.defineProperty(globalThis,key,descriptor);else delete globalThis[key]}});
 globalThis.location=new URL('https://pharos.test/cards');
 const frames=[],writes=[];
 globalThis.history={state:null,replaceState(_state,_title,url){globalThis.location=new URL(url);writes.push(location.href);}};
 globalThis.window={dispatchEvent(){}};globalThis.requestAnimationFrame=fn=>frames.push(fn);
 for(let i=0;i<150;i++)captureViewEntry('disclosures','preference:'+i,false);
 const cancel=captureViewEntry('disclosures','unmounted',false);cancel();
 writeViewState('disclosures',{'preference:0':true});writes.length=0;
 assert.equal(frames.length,1);frames.shift()();
 assert.equal(writes.length,1);
 const saved=JSON.parse(location.searchParams.get('disclosures'));
 assert.equal(Object.keys(saved).length,150);assert.equal(saved['preference:0'],true);assert.equal(saved.unmounted,undefined);
 captureViewEntry('query_layout','usage',true);
 globalThis.location=new URL('https://pharos.test/work/w');writes.length=0;
 frames.shift()();assert.equal(writes.length,0);assert.equal(location.searchParams.has('query_layout'),false);
});


test('every schema timestamp window accepted by the agent renews in the browser', async () => {
 const {default:map} = await import('../../../../internal/archive/assets/site-map.json');
 for (const [dataset, schema] of Object.entries(map.datasets)) for (const field of schema.columns.filter(field=>field.type==='datetime')) {
  const params=new URLSearchParams({window_dataset:dataset,window_days:'7',window_mode:'relative',window_field:field.name,window_until_field:field.name});
  assert.ok(readRollingWindow(params,dataset),`${dataset}.${field.name}`);
 }
 const old={...EMPTY_QUERY,where:[{field:'ended_at',op:'>=',value:'2026-10-28T06:00:00Z'},{field:'started_at',op:'<',value:'+0s'}]};
 const params=new URLSearchParams({window_dataset:'tool_calls',window_days:'1',window_mode:'today',window_field:'ended_at',window_until_field:'started_at'});
 const window=readRollingWindow(params,'tool_calls'),clock=new Date('2026-11-03T18:00:00Z');
 assert.ok(window);
 for(const timezone of [undefined,'America/Denver']) {
  const renewed=resolveRollingQuery(old,{...window,timezone},clock);
  assert.deepEqual(renewed.where,[{field:'ended_at',op:'>=',value:todayBounds(clock,timezone)[0]},{field:'started_at',op:'<',value:'+0s'}]);
 }
 params.set('window_field','id');assert.equal(readRollingWindow(params,'tool_calls'),undefined);
});


test('applied search codecs agree with shared cross-language validation cases',()=>{
 for(const sample of searchCases){
  const restored=readTextSearch(new URLSearchParams({text_builder:JSON.stringify(sample.draft)}));
  assert.equal(!restored.error,sample.valid,sample.name);
  for(const owner of ['search_state','record_keyword']){
   const value=owner==='record_keyword'?{field:'model',draft:sample.draft}:sample.draft;
   if(owner==='record_keyword'?(sample.recordValid??sample.valid):sample.valid)assert.doesNotThrow(()=>encodeViewValue(viewFields[owner],value),`${owner}: ${sample.name}`);
   else assert.throws(()=>encodeViewValue(viewFields[owner],value),undefined,`${owner}: ${sample.name}`);
  }
 }
 const large={...searchCases.find(item=>item.valid).draft,editor:'syntax',text:'x'.repeat(24001)};
 assert.throws(()=>encodeViewValue(viewFields.search_state,large));
 for(const [name,value]of [['delegation_page','+5'],['delegation_page','-0'],['find_case','true'],['sidebar_width','+400'],['reader_expanded',['m',null]],['window_timezone','Local'],['window_timezone','']])assert.throws(()=>encodeViewValue(viewFields[name],value));
});

test('classic controls recover from invalid state while strict query readers still reject it',async()=>{
 const {readClassicViewState,readViewState}=await import('../../../src/view-state');
 const oldWindow=globalThis.window,oldEvent=globalThis.CustomEvent;const warnings=[];
 globalThis.CustomEvent=class{constructor(type,props){this.type=type;Object.assign(this,props);}};
 globalThis.window={dispatchEvent:event=>warnings.push(event)};
 try{
  for(const [name,raw,fallback]of [['reader_content','{"a":1}',{}],['reader_content','{',{}],['history_collapsed','[null]',[]],['overlay','bad','none']]){
   const params=new URLSearchParams({[viewFields[name].param]:raw});
   assert.throws(()=>readViewState(name,fallback,params));
   assert.deepEqual(readClassicViewState(name,fallback,params),fallback);
  }
  assert.equal(warnings.length,4);
 }finally{globalThis.window=oldWindow;globalThis.CustomEvent=oldEvent;}
});


test('changing a window preserves unrelated datetime constraints and pinned Today intent',()=>{
 globalThis.location??=new URL('https://pharos.test');
 for(const [dataset,field,other]of [['tool_calls','started_at','ended_at'],['tl1_attempts','started_at','task_created_at']]){
  const manual={field:other,op:'>=',value:'2026-01-01T00:00:00Z'};
  const query={...EMPTY_QUERY,where:[manual,{field,op:'>=',value:'-7d'},{field,op:'<',value:'+0s'}]};
  const url=queryURL(dataset,query,{mode:'relative',days:7,field});
  for(const days of [30,null])assert.ok(queryFromURL(updateQueryWindowURL(url,days)).query.where.some(term=>JSON.stringify(term)===JSON.stringify(manual)));
 }
 const url=queryURL('tool_calls',EMPTY_QUERY,{mode:'today',days:1,field:'started_at',timezone:'Asia/Tokyo'});
 assert.equal(queryFromURL(updateQueryWindowURL(url,'today')).window.timezone,'Asia/Tokyo');
});


test('page navigation drops source attention while preserving global view controls',()=>{
 const params=new URLSearchParams({highlight:'text:quorum',reveal:'message:old',nav_collapsed:'1',reader_content:'{"old":true}'});
 params.append('highlight','card:old');
 retainPageView(params,'cards');
 assert.deepEqual(params.getAll('highlight'),[]);assert.equal(params.has('reveal'),false);
 assert.equal(params.get('nav_collapsed'),'1');assert.equal(params.has('reader_content'),false);
 const samePage=new URLSearchParams({highlight:'control:query_days',reveal:'page'});
 retainPageView(samePage,'analyze/query/usage',[],'analyze/query/usage');
 assert.equal(samePage.get('highlight'),'control:query_days');assert.equal(samePage.get('reveal'),'page');
 retainPageView(samePage,'analyze/query/tools',[],'analyze/query/usage');
 assert.equal(samePage.has('highlight'),false);assert.equal(samePage.has('reveal'),false);
});


// A provider whose answers the test releases one at a time.
function detailProvider(){
 const calls=[];
 const resolve=(reference,signal)=>new Promise(done=>calls.push({reference,signal,done}));
 return {calls,resolve};
}
const settled=()=>new Promise(resolve=>setTimeout(resolve,0));

test('native detail landings wait for a provider, retry when one registers, and cancel on navigation',async()=>{
 let passes=0;const landing=createDetailLanding(()=>passes++),target='detail:{"rowId":"a"}';
 assert.equal(landing.resolve(target),false,'without a provider the target waits');assert.equal(landing.pending,false);
 const custom=detailProvider(),removeCustom=landing.register(custom.resolve);
 assert.equal(landing.resolve(target),false);assert.equal(landing.pending,true);
 assert.equal(landing.resolve(target),false);assert.equal(custom.calls.length,1,'repeated passes share one request');
 custom.calls[0].done('unavailable');await settled();
 assert.equal(landing.pending,false);assert.equal(landing.resolve(target),false);assert.equal(custom.calls.length,1,'an unavailable target is not retried every pass');
 removeCustom();
 const table=detailProvider(),before=passes;landing.register(table.resolve);
 assert.ok(passes>before,'registration asks for another pass');
 assert.equal(landing.resolve(target),false);assert.equal(table.calls.length,1,'a new provider retries the unavailable target');
 table.calls[0].done('revealed');await settled();
 assert.equal(landing.resolve(target),true);
 const refreshed=detailProvider();landing.register(refreshed.resolve)();
 assert.equal(landing.resolve(target),true,'a revealed landing survives a provider refresh without replaying');
 assert.equal(refreshed.calls.length,0);

 const other='detail:{"rowId":"b"}';
 assert.equal(landing.resolve(other),false);const request=table.calls.at(-1);
 assert.equal(request.reference,other,'a new target replaces the landed one');
 landing.cancel();assert.equal(request.signal.aborted,true,'navigation aborts the pending request');
 request.done('revealed');await settled();
 assert.equal(landing.pending,false);
 assert.equal(landing.resolve(other),false,'a late answer to a cancelled request cannot land');
 assert.equal(table.calls.length,3);
 table.calls[2].done(Promise.reject(new Error('lost')));await settled();
 assert.equal(landing.pending,false,'a failed provider reports unavailable');
 assert.equal(landing.resolve(other),false);
});

test('native details wait for the first result page of a visible, restored table',()=>{
 const ready={visible:true,restored:true,loading:false,rows:'succeeded',schema:'pharos_mcp_calls'};
 assert.equal(nativeDetailsReady(ready),true);
 assert.equal(nativeDetailsReady({...ready,rows:'failed'}),true);
 for(const waiting of [{rows:'running'},{rows:undefined},{loading:true},{restored:false},{visible:false},{schema:undefined}])
  assert.equal(nativeDetailsReady({...ready,...waiting}),false,JSON.stringify(waiting));
});

test("attention ignores its own status UI and class changes but follows the page's",()=>{
 const element=(classes='',{owned=false,parent=null}={})=>({classes,parentElement:parent,closest:()=>owned?{}:null,getAttribute:()=>classes,matches(){return /pharos-(highlight|reveal)/.test(this.classes)}});
 const text=parentElement=>({parentElement});
 const status=element('',{owned:true}),page=element('card');
 assert.equal(attentionMutationMatters({type:'characterData',target:text(status)},new Set()),false,'status text');
 assert.equal(attentionMutationMatters({type:'childList',target:status,addedNodes:[text(status)],removedNodes:[]},new Set()),false,'status children');
 assert.equal(attentionMutationMatters({type:'childList',target:element(''),addedNodes:[status],removedNodes:[]},new Set()),false,'adding the status UI');
 assert.equal(attentionMutationMatters({type:'childList',target:element(''),addedNodes:[status,page],removedNodes:[]},new Set()),true,'page content arriving with it');
 assert.equal(attentionMutationMatters({type:'childList',target:page,addedNodes:[text(page)],removedNodes:[]},new Set()),true,'page text');
 assert.equal(attentionMutationMatters({type:'attributes',attributeName:'hidden',target:page},new Set()),true);
 const marked=element('card pharos-reveal');
 assert.equal(attentionMutationMatters({type:'attributes',attributeName:'class',oldValue:'card',target:marked},new Set([marked])),false,'marking an element');
 assert.equal(attentionMutationMatters({type:'attributes',attributeName:'class',oldValue:'card',target:element('card active pharos-reveal')},new Set()),true,'an ordinary class change');
 const cleared=element('card');
 assert.equal(attentionMutationMatters({type:'attributes',attributeName:'class',oldValue:'card pharos-highlight',target:cleared},new Set()),false,'unmarking an element');
 assert.equal(attentionMutationMatters({type:'attributes',attributeName:'class',oldValue:'card pharos-highlight',target:cleared},new Set([cleared])),true,'a re-render dropped the mark from a target');
});

test('a right-click names the innermost target that resolves back to it, and highlighting toggles the link',()=>{
 const node=(tagName,attributes={},parentElement=null,inner=null)=>({tagName,parentElement,getAttribute:name=>attributes[name]??null,querySelector:()=>inner});
 const card=node('ARTICLE',{'data-pharos-target':'card:tool-failures'});
 const disclosure=node('DETAILS',{'data-pharos-target':'section:help','data-view-field':'disclosures'},card);
 const text=node('P',{},disclosure);
 const owners=new Map([['card:tool-failures',card],['section:help',disclosure]]);
 const resolvesTo=(reference,element)=>owners.get(reference)===element;
 assert.equal(attentionReferenceAt(text,resolvesTo),'section:help');
 owners.delete('section:help');
 assert.equal(attentionReferenceAt(text,resolvesTo),'card:tool-failures','a reference naming another element is skipped');
 const row=node('TR',{},null,node('SPAN',{'data-pharos-target':'row:42'}));
 owners.set('row:42',row);
 assert.equal(attentionReferenceAt(node('TD',{},row),resolvesTo),'row:42','a cell names its row');
 assert.equal(attentionReferenceAt(node('DIV'),resolvesTo),null);
 assert.equal(attentionTargetNoun('call:toolu_1'),'Tool Call');
 assert.equal(attentionTargetNoun('unknown:x'),'Element');
 const step=(search,reference)=>'?'+toggleAttentionTarget(search,reference).toString();
 let search=step('?tab=1','card:a');
 assert.deepEqual([...new URLSearchParams(search)],[['tab','1'],['reveal','card:a']],'the first target is the landing');
 search=step(search,'card:b');
 assert.deepEqual(new URLSearchParams(search).getAll('highlight'),['card:b']);
 search=step(search,'card:a');
 assert.deepEqual([...new URLSearchParams(search)],[['tab','1'],['reveal','card:b']],'removing the landing promotes a highlight');
 assert.equal(step(search,'card:b'),'?tab=1');
});

test('a linked Findings rank applies without saving; a manual choice saves the default a new unlinked view uses',()=>{
 assert.deepEqual(findingsRank('minutes','usd'),{rank:'minutes'},'restoring a link is read-only');
 assert.deepEqual(findingsRank('','tokens'),{rank:'tokens'},'an unlinked view shows the saved default');
 assert.deepEqual(findingsRank('',undefined),{rank:'usd'});
 assert.deepEqual(findingsRank('minutes','usd','tokens'),{rank:'tokens',save:{rank:'tokens'}});
});


test('every guide term and dataset round-trips through the shared view contract',()=>{
 const grouped=dataEntityGroups.flatMap(group=>group.entities);
 assert.equal(new Set(grouped).size,grouped.length);
 assert.deepEqual([...grouped].sort(),Object.keys(dataEntities).sort());
 for(const term of grouped)assert.equal(parseViewValue(viewFields.guide_entity,encodeViewValue(viewFields.guide_entity,term)),term);
 for(const [dataset,source]of Object.entries(dataSources)){
  assert.ok(dataEntities[source.entity],dataset);
  assert.equal(parseViewValue(viewFields.guide_dataset,encodeViewValue(viewFields.guide_dataset,dataset)),dataset);
 }
 for(const [oldTerm,noun]of [['datasets','conversations'],['labels','messages'],['findings','conversations'],['model_requests','tokens'],['execution_machines','machines']]){
  const linked=parseViewValue(viewFields.guide_entity,oldTerm);
  assert.equal(resolveGuideEntity(linked),noun,'older guide links still open the related history');
 }
 assert.equal(describeDataSource('parallelism','workspace').entity,'workspaces');
 assert.equal(describeDataSource('parallelism','agent').entity,'conversations');
});
