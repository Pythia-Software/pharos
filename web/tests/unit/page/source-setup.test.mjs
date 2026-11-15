import assert from 'node:assert/strict';
import {test} from 'node:test';
import {readFileSync} from 'node:fs';
import {join} from 'node:path';
import vm from 'node:vm';
import {assets} from '../helpers/classic-script.mjs';
import registry from '../../../../testdata/providers.json';
const source=readFileSync(join(assets,'onboarding.js'),'utf8').split('// Source setup UI.')[0];
const [makeWizard,capabilityRows,captureSettings]=vm.runInNewContext(source+';[createPharosSourceWizard,pharosCapabilityRows,captureClaudeSettings]');
const create=()=>makeWizard(registry.providers);
const plain=value=>structuredClone(value);
const candidate=(kind,path=kind)=>({name:kind,kind,path,acceptable:true,recommended:true,status:'found'});

test('source setup begins without tool assumptions or implicit agent dependencies',()=>{
  const wizard=create();assert.deepEqual(plain(wizard.request()),{kinds:[],locations:[]});
  wizard.choose('conductor',true);assert.deepEqual(plain(wizard.request().kinds),['conductor']);
  wizard.choose('tl1',true);assert.deepEqual(plain(wizard.request().kinds),['conductor','tl1']);
  assert.throws(()=>wizard.choose('unsupported',true));
});

test('setup lists found tools, then the common ones, and keeps niche tools out of the way',()=>{
  const wizard=create();wizard.detect([{kind:'codex'},{kind:'tl1'},{kind:'unknown'},{kind:'codex'}]);
  const groups=plain(wizard.toolGroups());
  assert.deepEqual(groups.found,['codex','tl1'],'a niche tool found here is listed with the others found');
  assert.deepEqual(groups.more,['conductor','claude-ai-settings'],'a niche tool not found here is only under More tools');
  assert.ok(!groups.offered.includes('tl1')&&!groups.offered.includes('conductor'));
  assert.equal(groups.offered[0],'claude','the most used tools come first');
  assert.deepEqual(plain(wizard.request().kinds),['codex','tl1']);
  const empty=create();empty.detect([]);
  assert.deepEqual(plain(empty.request().kinds),[]);
  assert.deepEqual(plain(empty.toolGroups().more),['conductor','tl1','claude-ai-settings']);
  assert.ok(!empty.toolGroups().offered.includes('chatgpt'),'exports are imported, not found');
  assert.deepEqual(plain(empty.exports().map(item=>item.kind)),['chatgpt','claude-ai']);
  assert.ok(empty.exports().every(item=>item.how),'each export says how to download it');
});

test('a tool with two faces is one choice that reads both',()=>{
  const wizard=create();
  assert.ok(!wizard.catalog.some(tool=>tool.kind==='cursor-cli'),'Cursor CLI is shown under Cursor');
  assert.deepEqual(plain(wizard.tool('cursor-cli').kinds),['cursor','cursor-cli']);
  wizard.detect([{kind:'cursor-cli'}]);
  assert.deepEqual(plain(wizard.toolGroups().found),['cursor'],'either face finds the tool');
  wizard.scan();assert.deepEqual(plain(wizard.request().kinds),['cursor','cursor-cli']);
  wizard.choose('cursor',false);assert.deepEqual(plain(wizard.request().kinds),[]);
});

test('looking in the usual places reads every found tool, and only those',()=>{
  const wizard=create();wizard.detect([{kind:'claude'},{kind:'zed'}]);
  wizard.choose('claude',false);wizard.locations.push({kind:'zed',path:'/old'});
  wizard.scan();
  assert.deepEqual(plain(wizard.request()),{kinds:['claude','zed'],locations:[]});
});

test('naming folders checks only those folders, for the tools chosen',()=>{
  const wizard=create();wizard.detect([{kind:'claude'}]);wizard.manual();
  assert.deepEqual(plain(wizard.request()),{kinds:['claude'],locations:[{kind:'claude',path:'~/.claude'}],only_locations:true},'a chosen tool starts with its usual folder');
  wizard.choose('cursor',true);
  assert.deepEqual(plain(wizard.request().locations.map(location=>location.kind)),['claude','cursor'],'a second face (Cursor CLI) starts empty');
  wizard.name('cursor','');wizard.name('cursor-cli','  ~/.cursor ');
  assert.deepEqual(plain(wizard.request()),{kinds:['claude','cursor-cli'],locations:[{kind:'claude',path:'~/.claude'},{kind:'cursor-cli',path:'~/.cursor'}],only_locations:true});
  wizard.name('claude','');wizard.name('cursor-cli','/elsewhere');wizard.choose('cursor',true);
  assert.deepEqual(plain(wizard.request().locations),[{kind:'cursor-cli',path:'/elsewhere'}],'a cleared folder stays clear; a changed one is replaced');
  wizard.choose('cursor',false);
  assert.deepEqual(plain(wizard.request()),{kinds:[],locations:[],only_locations:true},'a tool unchosen takes its folders with it');
  wizard.choose('claude',false);wizard.choose('claude',true);
  assert.deepEqual(plain(wizard.request().locations),[{kind:'claude',path:'~/.claude'}],'chosen again, it starts again from its usual folder');
  wizard.scan();wizard.choose('codex',true);assert.deepEqual(plain(wizard.request().locations),[],'looking in the usual places names no folders');
  const accepted=create();accepted.manual();accepted.choose('codex',true);accepted.name('codex','/codex');
  assert.deepEqual(plain(accepted.request().locations),[{kind:'codex',path:'/codex'}]);
  accepted.discovered({candidates:[candidate('codex','/codex')]});
  assert.equal(accepted.selection().only_locations,true,'approval repeats the same narrow check');
});

test('source consent includes only selected tools and preserves excluded folders across rediscovery',()=>{
  const wizard=create();wizard.choose('claude',true);
  const personal=candidate('claude','/personal'),work={...candidate('claude','/work'),name:'work'};
  wizard.discovered({candidates:[personal,work,candidate('codex')]});
  assert.equal(wizard.selected().length,2);wizard.set(work,false);
  wizard.discovered({candidates:[personal,work,candidate('codex')]});
  assert.deepEqual(plain(wizard.selection()),{kinds:['claude'],locations:[],accept:['claude'],decline:['work'],names:{}});
  wizard.choose('claude',false);wizard.discovered({candidates:[personal]});
  assert.deepEqual(plain(wizard.selection().accept),[]);
  // A folder added for another tool, whose check fails, leaves the list as it was.
  const report=plain(wizard.report),restore=wizard.checkpoint();
  wizard.choose('codex',true);wizard.locations.push({kind:'codex',path:'/missing'});
  assert.equal(wizard.report,null);
  restore();
  assert.deepEqual(plain(wizard.report),report);
  assert.deepEqual(plain(wizard.request()),{kinds:[],locations:[]},'the failed folder and its tool are dropped');
});

test('source changes retain folder provenance and cannot enable already configured shared sources',()=>{
  const wizard=create();wizard.choose('codex',true);wizard.locations.push({kind:'codex',path:'/custom'});
  const shared={...candidate('codex'),configured:{scope:'library',enabled:false}};
  wizard.discovered({candidates:[shared]});assert.equal(wizard.selected().length,0);
  wizard.choose('codex',false);assert.deepEqual(plain(wizard.request().locations),[]);
  wizard.choose('claude',true);
  const first=candidate('claude','/first'),other=candidate('claude','/different');
  wizard.discovered({candidates:[first]});wizard.set(first,false);
  wizard.discovered({candidates:[other]});assert.equal(wizard.selected().length,1);
});

test('setup describes each tool from the registry and says what each records',()=>{
  assert.deepEqual(plain(makeWizard([]).toolGroups()),{found:[],offered:[],more:[]});
  const wizard=create();
  for(const tool of wizard.catalog)assert.ok(tool.summary&&tool.locations.length,`${tool.kind} says what it reads and where`);
  assert.ok(wizard.catalog.find(tool=>tool.kind==='claude').locations.includes('~/.claude*'),'alternate profiles are listed');
  assert.deepEqual(plain(wizard.capabilities()),[]);
  assert.ok(wizard.notRecorded('cursor-cli').includes('Token usage'));
  assert.deepEqual(plain(wizard.notRecorded('unknown')),[]);
  wizard.choose('cursor',true);wizard.choose('claude',true);
  const rows=plain(wizard.capabilities());
  assert.deepEqual(rows.map(row=>row.key),registry.capabilities.map(capability=>capability.key));
  const tokens=rows.find(row=>row.key==='message_tokens');
  assert.deepEqual(tokens.cells.map(cell=>[cell.kind,cell.support]),[['claude','supported'],['cursor','varies'],['cursor-cli','unsupported']]);
  assert.ok(tokens.cells[2].note,'a missing capability says why when it can');
  assert.equal(tokens.cells[0].name,'Claude Code');
  assert.deepEqual(plain(capabilityRows(registry.providers,['unknown'])),[]);
});

test('source and export pickers use host capabilities, retaining service fallback and manual path input', async () => {
  const source=readFileSync(join(assets,'onboarding.js'),'utf8');
  const picker=source.slice(source.indexOf('  const pickerAvailable ='),source.indexOf('\n  // A tool as a chip:'));
  const pathField=source.slice(source.indexOf('  function pathField('),source.indexOf('\n  // mode: start'));
  const element=()=>({children:[],append(...children){this.children.push(...children);},setAttribute(){}});
  for(const native of [false,true]) for(const managed of [false,true]) for(const purpose of ['source','export']) {
    const calls=[],window={pharosHost:{capabilities:{folders:{available:native}},folders:{chooseLibraryFolder:async value=>{calls.push(['host',value]);return '/chosen';}}},pharosManagedSession:managed};
    const model=vm.runInNewContext(picker+pathField+';({pickerAvailable,choosePath,pathField})',{
      window,active:null,document:{getElementById:()=>null},node:element,button:()=>element(),
      call:async(path,options)=>{calls.push([path,JSON.parse(options.body)]);return {path:'/service-machine'};},
    });
    assert.equal(model.pickerAvailable(),native||managed);
    assert.equal(await model.choosePath(purpose),native?'/chosen':'/service-machine');
    assert.deepEqual(calls,native?[['host',purpose]]:[['/api/app/choose-folder',{purpose}]]);
    let changed;
    const {row,input}=model.pathField('Folder','/manual','path',purpose,value=>{changed=value;},()=>{});
    input.value='/typed/path';input.oninput();
    assert.equal(changed,'/typed/path');
    assert.equal(row.children.length,native||managed?2:1,'manual entry is always available; unsupported pickers stay hidden');
    window.pharosHost.folders.chooseLibraryFolder=async()=>null;
    if(native)assert.equal(await model.choosePath(purpose),null);
  }
});

test('source and export picker completions belong to their connected field and current onboarding view',async()=>{
  const source=readFileSync(join(assets,'onboarding.js'),'utf8');
  const pathField=source.slice(source.indexOf('  function pathField('),source.indexOf('\n  // mode: start'));
  for(const purpose of ['source','export'])for(const result of ['/chosen',null,new Error('Picker failed')])for(const change of ['current','field replaced','closed','view replaced']){
    let settle;
    const pending=new Promise((resolve,reject)=>{settle=()=>result instanceof Error?reject(result):resolve(result)});
    const changes=[],errors=[],focused=[];
    const node=()=>({isConnected:true,disabled:false,children:[],append(...children){this.children.push(...children)},setAttribute(){},focus(){focused.push(this)}});
    const model=vm.runInNewContext('let active={};'+pathField+`;({pathField,close(){active=null},replace(){active={}}})`,{
      node,button:(text,primary,onclick)=>Object.assign(node(),{textContent:text,onclick}),
      pickerAvailable:()=>true,choosePath:()=>pending,
    });
    const original=model.pathField('Folder','/original','',purpose,value=>changes.push(value),error=>errors.push(error));
    const pick=original.row.children[1],operation=pick.onclick();
    assert.equal(pick.disabled,true);
    let replacement;
    if(change==='field replaced')original.input.isConnected=false;
    if(change==='closed')model.close();
    if(change==='view replaced')model.replace();
    if(change!=='current'){
      replacement=model.pathField('Folder','/replacement','',purpose,()=>assert.fail('obsolete chooser changed the replacement'),()=>assert.fail('obsolete chooser failed in the replacement'));
      replacement.input.focus();
    }
    settle();await operation;
    const current=change==='current';
    assert.equal(original.input.value,current&&typeof result==='string'?result:'/original');
    assert.deepEqual(changes,current&&typeof result==='string'?[result]:[]);
    assert.deepEqual(errors,current&&result instanceof Error?[result]:[]);
    assert.equal(pick.disabled,false,'the original control is cleaned up');
    assert.equal(focused.at(-1),current?pick:replacement.input,'obsolete finally must leave focus in the replacement');
    if(replacement){
      assert.equal(replacement.input.value,'/replacement');
      assert.equal(replacement.row.children[1].disabled,false);
    }
  }
});

const summarizeClaude=vm.runInNewContext(source+';pharosClaudeImportSummary');
test('Claude collection counts only verified export records and keeps full coverage distinct',()=>{
  const parts=[{state:'collected',conversations:2,messages:7},{state:'missing',conversations:90,messages:900}];
  assert.deepEqual(plain(summarizeClaude({parts,completeness:'manifest'})),{expected:2,collected:1,conversations:2,messages:7,complete:false,knownCoverage:true});
  assert.equal(summarizeClaude({parts:[parts[0]],completeness:'unknown'}).knownCoverage,false);
  assert.equal(summarizeClaude({parts:[parts[0]],completeness:'unknown'}).complete,true);
  assert.equal(summarizeClaude({parts:[],completeness:'manifest'}).complete,false);
  const excluded={state:'excluded',error:'Not kept'};
  assert.deepEqual(plain(summarizeClaude({parts:[parts[0],excluded],completeness:'manifest'})),{expected:1,collected:1,conversations:2,messages:7,complete:true,knownCoverage:true});
});

const watchLine=vm.runInNewContext(source+';pharosClaudeWatchLine');
const routineDue=vm.runInNewContext(source+';pharosClaudeRoutineDue');
test('Claude export reminder waits a week after an import or an acknowledgement',()=>{
  const now=Date.parse('2026-11-12T12:00:00Z'),week=7*24*60*60*1000;
  const date=age=>new Date(now-age).toISOString();
  assert.equal(routineDue({import_history_available:true},now),true);
  assert.equal(routineDue({last_import_at:date(week)},now),false);
  assert.equal(routineDue({last_import_at:date(week+1)},now),true);
  assert.equal(routineDue({last_import_at:date(week+1),routine_done_at:date(1000)},now),false);
  assert.equal(routineDue({last_import_at:date(week+1),routine_done_at:date(week+1)},now),true);
  assert.equal(routineDue({import_history_available:false},now),false);
  assert.equal(routineDue({last_import_at:'invalid'},now),true);
});
test('a watched Claude export offers retrying only once Pharos has stopped opening links',()=>{
  const waiting=watchLine({state:'fetching',waiting_file:'frames-000.zip',browser_label:'Google Chrome — Work',collected:3,expected:7});
  assert.match(waiting.text,/frames-000\.zip/);assert.match(waiting.text,/Google Chrome — Work/);assert.match(waiting.files,/3 of 7 files/);
  assert.deepEqual(plain(waiting.actions),['stop']);assert.equal(waiting.attention,false);
  const queued=watchLine({state:'fetching',phase:'queued',collected:0,expected:6,message:'Waiting for another capture or index to finish.'});
  assert.match(queued.text,/Waiting for the library/);assert.match(queued.files,/0 of 6 files/);
  assert.deepEqual(plain(queued.actions),['stop']);assert.match(queued.detail,/another capture or index/);
  const indexing=watchLine({state:'fetching',phase:'indexing',collected:6,expected:6,ingestion:{stage:'indexing',processed:8,workspaces:6,conversations:6,messages:40,skipped_current:2}});
  assert.match(indexing.text,/Adding conversations/);assert.match(indexing.files,/6 of 6 files/);
  assert.match(indexing.progress,/8 workspaces processed/);assert.match(indexing.progress,/6 conversations written/);assert.match(indexing.progress,/40 messages written/);assert.match(indexing.progress,/2 already current/);
  assert.deepEqual(plain(indexing.actions),['stop']);
  assert.match(watchLine({state:'fetching',phase:'indexing',ingestion:{stage:'capturing'}}).text,/Preserving/);
  assert.match(watchLine({state:'fetching',phase:'indexing',ingestion:{stage:'waiting for catalog'}}).text,/Waiting to save/);
  assert.equal(watchLine({state:'paused',phase:'indexing',ingestion:{stage:'failed'}}).text,'Ingestion paused');
  assert.equal(watchLine({state:'imported'}).text,'Ready in your library');
  const paused=watchLine({state:'paused',message:'No file arrived.',collected:3,expected:7});
  assert.deepEqual(plain(paused.actions),['retry','stop']);assert.equal(paused.attention,true);assert.equal(paused.detail,'No file arrived.');
  assert.deepEqual(plain(watchLine({state:'stopped'}).actions),['retry']);
  assert.deepEqual(plain(watchLine({state:'expired'}).actions),[]);
  assert.deepEqual(plain(watchLine({state:'imported',collected:7,expected:7}).actions),[]);
});

test('one-time Claude settings capture indexes only its account source and refreshes coverage',async()=>{
  const requests=[],jobs=[],progress=[];
  const result=await captureSettings({account:'work',setup:{path:'/cache'}},{
    call:async(path,options)=>{requests.push({path,body:options?JSON.parse(options.body):null});return options?{source:'work-settings'}:{accounts:[{account:'work',missing_models:0}]}},
    runs:{capture:async names=>{jobs.push(['capture',plain(names)]);return {state:'complete'}},index:async options=>{jobs.push(['index',plain(options)]);return {state:'complete'}}},
    conversation:'chat/id',progress:text=>progress.push(text),
  });
  assert.equal(result.missing_models,0);
  assert.deepEqual(requests[0].body,{account:'work',path:'/cache',capture_only:true});
  assert.deepEqual(jobs,[['capture',['work-settings']],['index',{sources:['work-settings'],only_needed:true}]]);
  assert.equal(requests[1].path,'/api/claude-chat-settings/coverage?account=work&conversation=chat%2Fid');
  assert.equal(progress.length,3);
});

test('one-time settings capture keeps failures retryable and never indexes a failed capture',async()=>{
  for(const failed of ['capture','index','coverage']){
    const jobs=[];
    await assert.rejects(()=>captureSettings({account:'local',setup:{path:'/cache'}},{
      call:async(path,options)=>options?{source:'settings'}:{accounts:[]},runs:{
        capture:async()=>{jobs.push('capture');return {state:failed==='capture'?'failed':'complete'}},
        index:async()=>{jobs.push('index');return {state:failed==='index'?'interrupted':'complete'}},
      },
    }),/did not complete|could not be confirmed/);
    assert.deepEqual(jobs,failed==='capture'?['capture']:['capture','index']);
  }
});

test('ChatGPT collection and indexing progress describe a verified snapshot separately',()=>{
  const watchLine=vm.runInNewContext(source+';pharosChatGPTWatchLine');
  assert.match(watchLine({state:'fetching',phase:'collecting'}).text,/verifying/);
  const indexing=watchLine({state:'fetching',phase:'indexing',collected:1,expected:1,ingestion:{stage:'indexing',processed:2,conversations:2,messages:10}});
  assert.match(indexing.files,/verified/);assert.match(indexing.progress,/2 conversations/);assert.deepEqual(plain(indexing.actions),['stop']);
  assert.deepEqual(plain(watchLine({state:'paused'}).actions),['retry','stop']);
});


test('source limitations use provider notes and fall back to the source lists',()=>{
  const html=readFileSync(join(assets,'ui.py'),'utf8');
  const start=html.indexOf('function sourceLimitGroups('),end=html.indexOf('\nwindow.pharosSourceInfoButton=',start);
  const limits=vm.runInNewContext(html.slice(start,end)+';sourceLimitGroups');
  const grouped=plain(limits({kind:'codex'},registry.providers));
  assert.ok(grouped.missing.some(item=>item.label==='Reported cost'));
  assert.ok(grouped.varying.find(item=>item.label==='Thinking').note.includes('encrypted'));
  assert.ok(!grouped.missing.some(item=>item.support==='not-applicable'));
  const fallback=plain(limits({kind:'unknown',not_recorded:['Model'],sometimes_recorded:['Title']},[]));
  assert.deepEqual(fallback.missing.map(item=>item.label),['Model']);
  assert.deepEqual(fallback.varying.map(item=>item.label),['Title']);
  assert.equal(limits({kind:'unknown'},[]).missing.length,0);
});
