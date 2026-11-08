import assert from 'node:assert/strict';
import {test} from 'node:test';
import {readFileSync} from 'node:fs';
import {join} from 'node:path';
import vm from 'node:vm';
import {assets, loadClassicScript} from '../helpers/classic-script.mjs';

const source=readFileSync(join(assets,'application.html'),'utf8').match(/<script id="setup-wizard-model">([\s\S]*?)<\/script>/)[1];
const model=vm.runInNewContext(source+';({createPharosSetupWizard,nextPharosLibraryName,pharosLibraryHome,pharosLibraryReopen,pharosLibraryHeading,pharosLibraryConnect})',{TextEncoder,URLSearchParams});
const create=model.createPharosSetupWizard;
const plain=value=>structuredClone(value);

test('setup recommends storage from the number of Macs without creating anything',()=>{
  for(const [machines,storage]of [['one','mac'],['many','ssd']]){
    const wizard=create();wizard.setMachines(machines);
    assert.equal(wizard.step,'machines');assert.equal(wizard.storage,storage);
    assert.throws(()=>wizard.payload(),/Review/);
    wizard.next();assert.equal(wizard.step,'storage');
    assert.throws(()=>wizard.payload(),/Review/);
    wizard.next();assert.equal(wizard.step,'details');
  }
});

test('setup keeps deliberate overrides when going back, and clears dependent folders when the route changes',()=>{
  const wizard=create();wizard.setMachines('many');wizard.next();
  wizard.setStorage('mac');wizard.next();wizard.name='Research';wizard.parent='/Users/me/History';
  assert.ok(wizard.back());assert.equal(wizard.step,'storage');assert.equal(wizard.parent,'/Users/me/History');
  wizard.back();wizard.setMachines('many');wizard.next();
  assert.equal(wizard.storage,'mac');assert.equal(wizard.parent,'/Users/me/History');
  wizard.back();wizard.setMachines('one');wizard.next();
  assert.equal(wizard.storage,'mac');assert.equal(wizard.parent,'');assert.equal(wizard.name,'Research');
  wizard.setStorage('ssd');assert.equal(wizard.parent,'');
});

test('external setup requires a folder and review, and constructs one named library inside its parent',()=>{
  const wizard=create();wizard.setMachines('many');wizard.next();wizard.next();
  assert.equal(wizard.folder('/Users/me/Libraries'),'');
  assert.equal(wizard.next().field,'path');assert.equal(wizard.step,'details');
  wizard.parent='/Volumes/My SSD/';wizard.name=' Research ';
  assert.equal(wizard.folder(),'/Volumes/My SSD/Research');
  assert.throws(()=>wizard.payload(),/Review/);
  assert.equal(wizard.next(),null);
  assert.deepEqual(plain(wizard.payload()),{name:'Research',path:'/Volumes/My SSD/Research',location:'ssd'});
  wizard.back();wizard.name='New name';wizard.next();
  assert.equal(wizard.payload().path,'/Volumes/My SSD/New name');
});

test('local setup explicitly requests Mac storage, including when the app itself is portable',()=>{
  const wizard=create();wizard.next();wizard.next();wizard.next();
  assert.deepEqual(plain(wizard.payload()),{name:'My Library',path:'',location:'mac'});
  assert.equal(wizard.folder('/isolated/support/Libraries'),'/isolated/support/Libraries/My Library');
  wizard.parent='/';assert.equal(wizard.payload().path,'/My Library');
});

test('setup keeps invalid names and paths out of review',()=>{
  for(const name of ['', '.', '..','a/b','a\\b','a\0b','é'.repeat(81)]){
    const wizard=create();wizard.next();wizard.next();wizard.name=name;
    assert.equal(wizard.next().field,'name');assert.equal(wizard.step,'details');
  }
  const wizard=create();wizard.next();wizard.next();wizard.parent='relative/folder';
  assert.equal(wizard.next().field,'path');wizard.parent='~/History';wizard.next();
  assert.equal(wizard.payload().path,'~/History/My Library');
  assert.throws(()=>wizard.setMachines('unknown'));assert.throws(()=>wizard.setStorage('network'));
});

test('new libraries get an unused proposed name',()=>{
  assert.equal(model.nextPharosLibraryName([]),'My Library');
  assert.equal(model.nextPharosLibraryName([{name:'My Library'},{name:'My Library 2'},{name:'Research'}]),'My Library 3');
});

test('the startup window reopens the selected library after a verified operation and returns to where it was sent from',()=>{
  const arrival=query=>new URLSearchParams(query);
  const local={id:'local',name:'Local',available:true},archive={id:'archive',name:'Archive',available:true};
  const state=(job,more={})=>({selected:'local',busy:false,libraries:[local,archive],job:job&&{state:job,phase:'Done'},...more});
  const reopen=(query,value)=>{const result=model.pharosLibraryReopen(arrival(query),value);return result&&[result.library.id,result.home]};
  assert.equal(model.pharosLibraryHome(arrival('')),'/library');
  for(const query of ['after=operation','released=1'])assert.equal(model.pharosLibraryHome(arrival(query)),'/settings/libraries');
  assert.deepEqual(reopen('after=operation',state('complete')),['local','/settings/libraries']);
  for(const job of [null,'running','failed'])assert.equal(reopen('after=operation',state(job)),null,'reopened after '+job);
  assert.equal(reopen('after=operation',state('complete',{busy:true})),null);
  assert.equal(reopen('after=operation',state('complete',{libraries:[{...local,available:false},archive]})),null);
  assert.equal(reopen('',state('complete')),null);
  assert.equal(reopen('released=1',state('complete')),null);
  assert.deepEqual(reopen('after=compact',state('complete')),['local','/settings']);
  assert.deepEqual(reopen('after=compact',state('failed')),['local','/settings/libraries']);
});

test('after a release the startup window offers to reconnect the released library',()=>{
  const local={id:'local',name:'Local'},archive={id:'archive',name:'My archive'};
  assert.equal(model.pharosLibraryHeading({selected:'',libraries:[]}),'Let’s set up your library.');
  const connected={selected:'archive',libraries:[local,{...archive,connected:true}]},released={...connected,libraries:[local,archive]};
  assert.equal(model.pharosLibraryHeading(connected),'Your libraries.');
  assert.equal(model.pharosLibraryHeading(released),'Choose a library.');
  assert.deepEqual(plain(model.pharosLibraryConnect(connected.libraries[1],connected)),{active:true,text:'Continue browsing',label:'Browse My archive'});
  assert.deepEqual(plain(model.pharosLibraryConnect(archive,released)),{active:false,text:'Reconnect',label:'Reconnect to My archive'});
  assert.deepEqual(plain(model.pharosLibraryConnect(local,released)),{active:false,text:'Switch to library',label:'Switch to Local'});
});

test('library changes save before writing and keep navigation guarded until the old page unloads',async()=>{
  for(const kind of ['connect','open','create'])for(const failing of ['', 'save', 'write', 'navigate']){
    const globals={pharosLibrarySwitching:false};
    const model=loadClassicScript('managed-library.js',globals);
    const events=[],destination=kind==='create'?'/settings/sources':'/settings/libraries';
    let finishWrite;
    const writing=new Promise(resolve=>{finishWrite=resolve});
    const operation=model.changePharosLibrary(async()=>{
      events.push(kind);await writing;if(failing==='write')throw Error('write failed');
    },{
      flush:async()=>{events.push('save');if(failing==='save')throw Error('save failed')},
      navigate:path=>{events.push(path);if(failing==='navigate')throw Error('navigate failed')},
      destination,
    });
    assert.equal(model.pharosLibrarySwitching,true,'polls must be suppressed while saving or writing');
    finishWrite();
    if(failing)await assert.rejects(operation,new RegExp(failing+' failed'));else await operation;
    assert.equal(model.pharosLibrarySwitching,!failing,'successful navigation must retain the guard in the old document');
    assert.deepEqual(events,failing==='save'?['save']:failing==='write'?['save',kind]:['save',kind,destination]);
  }
});

test('Settings sends each library change and keeps its old document guarded and controls locked after it navigates',async()=>{
  const forms={open:'open',create:'create',recover:'restore',transfer:'transfer'};
  const destinations={create:'/settings/sources',recover:'/app?after=operation',transfer:'/app?after=operation',disconnect:'/app?released=1'};
  const bodies={connect:{id:'archive'},open:{path:'/Libraries/Archive'},create:{name:'Research',path:'/Libraries/Research',location:'mac'},
    recover:{backup:'/Backups/Library',path:'/Libraries/Restored'},transfer:{source:'local',target:'archive',move:true},disconnect:{id:'local'}};
  for(const kind of ['connect','open','create','recover','transfer','disconnect']){
    const created=[],ids=new Map(),calls=[];
    const node=(tag='div')=>{
      const value={tagName:tag.toUpperCase(),children:[],dataset:{},value:'',textContent:'',isConnected:true,
        classList:{toggle(){}},setAttribute(){},focus(){},querySelectorAll(){return []},
        append(...children){this.children.push(...children)},replaceChildren(...children){this.children=children}};
      created.push(value);return value;
    };
    const get=id=>{if(!ids.has(id))ids.set(id,node());return ids.get(id)};
    const body=node();
    let ready,arrived;
    const rendered=new Promise(resolve=>{ready=resolve}),navigated=new Promise(resolve=>{arrived=resolve});
    get('library-settings-current').replaceChildren=function(...children){this.children=children;ready()};
    get('librarySettings').querySelectorAll=selector=>selector==='input[name=library-settings-mode]'?[{value:'copy',checked:false},{value:'move',checked:true}]:[];
    // Release is offered for the open library when it is on a drive.
    const local={id:'local',name:'Local',available:true,config:kind==='disconnect'?'/Volumes/Drive/Local/library.toml':'/Libraries/Local/library.toml'},archive={id:'archive',name:'Archive',available:true};
    const model=loadClassicScript('managed-library.js',{
      pharosManagedLibrary:local,
      document:{body,activeElement:body,getElementById:get,createElement:node,createElementNS:(_,tag)=>node(tag)},
      location:{assign:path=>arrived(path)},
      fetch:async(path,options)=>{
        if(options?.method==='POST')calls.push([path,JSON.parse(options.body)]);
        return {ok:true,json:async()=>path==='/api/app/state'?{libraries:[local,archive],mcp:'local'}:{saved:true}};
      },
    });
    await rendered;
    get('library-settings-existing').value='/Libraries/Archive';
    get('library-settings-name').value='Research';
    get('library-settings-parent').value='/Libraries';
    get('library-settings-storage').value='mac';
    get('library-settings-backup').value='/Backups/Library';
    get('library-settings-recover-path').value='/Libraries/Restored';
    get('library-settings-from').value='local';
    get('library-settings-to').value='archive';
    const activate=kind==='connect'?()=>created.find(value=>value.dataset.libraryID==='archive').onclick():
      kind==='disconnect'?()=>created.find(value=>value.textContent==='Release for eject').onclick():
      ()=>get('library-settings-'+forms[kind]+'-form').onsubmit({preventDefault(){}});
    activate();
    assert.equal(await navigated,destinations[kind]||'/settings/libraries');
    // Simulate the old document staying alive after location.assign returns.
    await new Promise(resolve=>setImmediate(resolve));
    assert.equal(model.pharosLibrarySwitching,true);
    activate();await new Promise(resolve=>setImmediate(resolve));
    assert.deepEqual(calls,[['/api/app/'+kind,bodies[kind]]],'the old page must send one '+kind+' and accept no other action during navigation');
  }
});

test('library cards say that agents search the connected library and which way delivery runs',()=>{
  const {pharosLibraryRoles}=loadClassicScript('managed-library.js');
  const state={delivery_source:'local',delivery_target:'archive',libraries:[{id:'local',name:'Local'},{id:'archive',name:'Archive'}]};
  assert.deepEqual(plain(pharosLibraryRoles({id:'local'},state,true)),['Agents using Pharos MCP search this library','Captures here and delivers to Archive']);
  assert.deepEqual(plain(pharosLibraryRoles({id:'archive'},state,false)),['Receives history captured in Local']);
  assert.deepEqual(plain(pharosLibraryRoles({id:'archive'},{...state,delivery_target:''},false)),[]);
  assert.deepEqual(plain(pharosLibraryRoles({id:'other',recovery_point:'2026-10-01'},state,false)),['Restored from 2026-10-01 · capture starts paused']);
});

test('library cards say when a closed library was last open and how many conversations it held',()=>{
  const {pharosLibraryAge,pharosLibraryFacts}=loadClassicScript('managed-library.js');
  const now=new Date(2026,9,28,15,0).getTime(),at=(...date)=>new Date(...date).toISOString();
  assert.equal(pharosLibraryAge(at(2026,9,28,14,59,40),now),'just now');
  assert.equal(pharosLibraryAge(at(2026,9,28,14,15),now),'45 min ago');
  assert.equal(pharosLibraryAge(at(2026,9,28,9,0),now),'6 hr ago');
  assert.equal(pharosLibraryAge(at(2026,9,27,9,0),now),'yesterday');
  assert.equal(pharosLibraryAge(at(2026,9,24,23,0),now),'4 days ago');
  assert.equal(pharosLibraryAge(at(2026,5,30,12,0),now),'Jun 30');
  assert.equal(pharosLibraryAge(at(2025,11,3,12,0),now),'Dec 3, 2025');
  assert.equal(pharosLibraryAge('',now),'');
  const library={last_opened:at(2026,9,27,9,0),conversations:18342};
  assert.equal(pharosLibraryFacts(library,false,now),'Last opened yesterday · 18,342 conversations');
  assert.equal(pharosLibraryFacts({...library,conversations:1},true,now),'1 conversation');
  assert.equal(pharosLibraryFacts({},false,now),'Not opened recently');
});

test('library settings reports the last operation until it is stale or no longer applies',()=>{
  const {pharosLibraryJob}=loadClassicScript('managed-library.js');
  const now=Date.parse('2026-10-28T12:00:00Z'),at=minutes=>new Date(now-minutes*60e3).toISOString();
  const delivering={delivery_source:'local',delivery_target:'archive'},off={delivery_source:'',delivery_target:''};
  const job=(state,minutes,phase='Copying')=>({state,phase,updated_at:at(minutes)});
  assert.deepEqual(plain(pharosLibraryJob(job('running',60),off,now)),{text:'Running: Copying',error:false});
  assert.deepEqual(plain(pharosLibraryJob(job('complete',2),off,now)),{text:'Done: Copying',error:false});
  assert.deepEqual(plain(pharosLibraryJob(job('failed',2),off,now)),{text:'Last library operation failed: Copying',error:true});
  // A pause delivery keeps reporting stays; one from launch, or after delivery is off, does not.
  assert.deepEqual(plain(pharosLibraryJob(job('paused',6,'Archive is away'),delivering,now)),{text:'Paused: Archive is away',error:true});
  assert.equal(pharosLibraryJob(job('paused',1,'Could not open the previous library'),off,now),null);
  for(const state of ['complete','failed','paused'])assert.equal(pharosLibraryJob(job(state,11),delivering,now),null,state+' never cleared');
  assert.equal(pharosLibraryJob(null,delivering,now),null);
});

test('library settings resolves local and external destinations and rejects invalid proposals',()=>{
  const {pharosLibraryProposal,pharosLibraryName}=loadClassicScript('managed-library.js');
  assert.equal(pharosLibraryName([{name:'My Library'},{name:'My Library 2'}]),'My Library 3');
  assert.deepEqual(plain(pharosLibraryProposal(' Research ','','mac')),{name:'Research',path:'',location:'mac'});
  assert.deepEqual(plain(pharosLibraryProposal(' Research ',' /Volumes/SSD/ ','ssd')),{name:'Research',path:'/Volumes/SSD/Research',location:'ssd'});
  assert.equal(pharosLibraryProposal('Research','/','mac').path,'/Research');
  assert.equal(pharosLibraryProposal('Research','~/Libraries','mac').path,'~/Libraries/Research');
  for(const name of ['', '.', '..', 'a/b', 'a\\b', 'é'.repeat(81)])assert.throws(()=>pharosLibraryProposal(name,'','mac'),/name/);
  assert.throws(()=>pharosLibraryProposal('Research','','ssd'),/parent folder/);
  assert.throws(()=>pharosLibraryProposal('Research','relative/path','mac'),/absolute/);
  assert.throws(()=>pharosLibraryProposal('Research','','network'),/store/);
});

test('startup folder choices restore controls and affect only their initiating wizard view', async () => {
  const page=readFileSync(join(assets,'application.html'),'utf8');
  const picker=page.slice(page.indexOf('async function pickFolder('),page.indexOf("\n$('choose-folder').onclick"));
  for(const native of [true,false])for(const id of ['path','existing-path','backup','recover-path'])for(const result of ['/Volumes/Archive',null,new Error('Picker unavailable')])for(const change of ['current','field replaced','closed','view replaced','source setup']) {
    let settle;
    const pending=new Promise((resolve,reject)=>{settle=()=>result instanceof Error?reject(result):resolve(result)});
    const field={value:'/manual/path',isConnected:true,closest:()=>null},button={isConnected:true,disabled:false,focus(){this.focused=true;}},busy={disabled:true};
    let currentField=field,updated=0,sourceSetup=false;
    const setup={hidden:false},dialog={open:true,classList:{contains:()=>sourceSetup}},notices=[],calls=[];
    const window={pharosHost:{capabilities:{folders:{available:native}},folders:{chooseLibraryFolder:()=>{calls.push('host');return pending}}}};
    const model=vm.runInNewContext('let wizardView=0;'+picker+';({pickFolder,replaceView(){wizardView++}})',{
      window,dialog,$:key=>key==='library-form'?{querySelectorAll:()=>[button,busy]}:key==='setup'?setup:currentField,
      notice:(text,error)=>{if(error)notices.push(text)},updateLibraryProposal:()=>updated++,call:async(action,body)=>{calls.push([action,plain(body)]);return {path:await pending}},
    });
    const operation=model.pickFolder(id,button);
    assert.equal(button.disabled,true);assert.equal(busy.disabled,true);
    if(change==='field replaced'){field.isConnected=false;currentField={...field,isConnected:true,value:'/replacement'}}
    if(change==='closed'){setup.hidden=true;dialog.open=false}
    if(change==='view replaced'){model.replaceView();currentField.value='/replacement'}
    if(change==='source setup')sourceSetup=true;
    settle();await operation;
    const current=change==='current',selected=current&&typeof result==='string';
    assert.equal(currentField.value,selected?result+(id==='recover-path'?'/Recovered Library':''):['field replaced','view replaced'].includes(change)?'/replacement':'/manual/path');
    assert.equal(updated,selected&&id==='path'?1:0);
    assert.deepEqual(notices,current&&result instanceof Error?['Picker unavailable']:[]);
    assert.deepEqual(calls,native?['host']:[['choose-folder',{}]]);
    assert.equal(button.disabled,false);assert.equal(busy.disabled,true);assert.equal(!!button.focused,current);
  }
});
