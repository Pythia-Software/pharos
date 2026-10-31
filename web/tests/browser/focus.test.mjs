// Focus and placement that only a real browser can check: modal surfaces take
// focus, keep it while open and give it back on Escape; the transcript rails
// stay below the header; the annotation toolbar stays on screen.
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { activateHeaderTool, open, skip } from "./harness.mjs";
import { defaultApi, workID, eventStream, busyStatus } from "./fixtures.mjs";
import { EMPTY_QUERY, encodeQuery, withGroupingKeys } from "@pythia-software/query-table-core";

test('Library setup keeps focus in its modal and returns to the choice after cancelling a flow', {skip}, async t => {
  let folderSelections=0;
  const {page, calls, shot} = await open(t, {url:'/app',ready:'#application-setup[open]',api:{
    'GET /api/app/state': {application:'/Applications/Pharos.app',selected:'',mcp:'',busy:false,libraries:[]},
    'POST /api/app/create': {saved:true},
    'POST /api/app/choose-folder': ()=>({path:folderSelections++===0?'/Volumes/Test SSD':''}),
  }});
  const dialog=page.locator('#application-setup');
  assert.ok(await dialog.evaluate(element=>element.contains(document.activeElement)));
  await page.locator('.brand').evaluate(element=>element.focus());
  assert.ok(await dialog.evaluate(element=>element.contains(document.activeElement)), 'background took focus during setup');
  for(let i=0;i<8;i++){
    await page.keyboard.press('Tab');
    assert.ok(await dialog.evaluate(element=>element.contains(document.activeElement)), 'Tab escaped the modal');
  }
  await shot('welcome');
  await page.locator('#meet-pharos').click();
  assert.ok(await page.locator('#about-title').evaluate(element=>element===document.activeElement));
  await shot('about-pharos');
  await page.locator('#back-to-welcome').click();
  assert.ok(await page.locator('#welcome-title').evaluate(element=>element===document.activeElement));
  await page.locator('#meet-pharos').click();
  await page.locator('#start-setup').click();
  assert.ok(await page.locator('#flow-title').evaluate(element=>element===document.activeElement));
  await shot('machines');
  await page.locator('.wizard-alternate summary').focus();
  await page.keyboard.press('Shift+Tab');
  assert.ok(await page.evaluate(()=>document.activeElement.matches('input[name=machines]')), 'reverse Tab skipped the machine choices');
  await page.locator('input[name=machines][value=many]').check();
  await page.locator('#submit').click();
  assert.ok(await page.locator('#flow-title').evaluate(element=>element===document.activeElement));
  await shot('storage');
  await page.locator('#submit').click();
  assert.ok(await page.locator('#name').isHidden());
  assert.ok(await page.locator('#path').isHidden());
  await page.locator('#submit').click();
  assert.ok(await page.locator('#path').evaluate(element=>element===document.activeElement), 'missing SSD folder was not focused');
  await page.getByRole('button',{name:'Edit library name',exact:true}).click();
  assert.ok(await page.locator('#name').evaluate(element=>element===document.activeElement), 'editing the library name did not focus its input');
  await page.locator('#name').fill('Research');
  await page.getByRole('button',{name:'Done editing library name',exact:true}).click();
  assert.ok(await page.locator('#name').isHidden());
  await page.locator('#choose-folder').click();
  await page.waitForFunction(()=>document.querySelector('#path').value==='/Volumes/Test SSD'&&!document.querySelector('#choose-folder').disabled);
  assert.ok(await page.locator('#choose-folder').evaluate(element=>element===document.activeElement), 'folder selection lost keyboard focus');
  await page.locator('#choose-folder').click();
  await page.waitForFunction(()=>!document.querySelector('#choose-folder').disabled);
  assert.equal(await page.locator('#path').inputValue(),'/Volumes/Test SSD', 'cancelling cleared the selected folder');
  await shot('details');
  await page.locator('#submit').click();
  assert.ok(await page.locator('#flow-title').evaluate(element=>element===document.activeElement));
  await shot('review');
  await page.keyboard.press('Escape');
  assert.equal(await page.locator('#name').inputValue(),'Research');
  assert.ok(await page.locator('#name').isHidden(), 'returning from review exposed the editor');
  await page.keyboard.press('Escape');
  await page.keyboard.press('Escape');
  await page.keyboard.press('Escape');
  assert.ok(await page.locator('#about-title').evaluate(element=>element===document.activeElement));
  await page.locator('#start-setup').click();
  await page.locator('.wizard-alternate summary').click();
  await page.locator('#wizard-machines [data-flow=open]').click();
  await page.locator('#submit').click();
  assert.ok(await page.locator('#existing-path').evaluate(element=>element===document.activeElement));
  await page.keyboard.press('Escape');
  assert.ok(await page.locator('#flow-title').evaluate(element=>element===document.activeElement));
  await page.locator('#wizard-machines [data-flow=recover]').click();
  await page.locator('#submit').click();
  assert.ok(await page.locator('#backup').evaluate(element=>element===document.activeElement));
  await page.locator('#backup').fill('/Backups/My Library');
  await page.locator('#submit').click();
  await page.locator('#submit').click();
  assert.ok(await page.locator('#recover-path').evaluate(element=>element===document.activeElement));
  await page.locator('#recover-path').fill('/Libraries/Restored');
  await page.locator('#submit').click();
  await shot('restore-review');
  await page.keyboard.press('Escape');
  await page.keyboard.press('Escape');
  await page.keyboard.press('Escape');
  assert.ok(await dialog.isVisible(), 'initial setup dismissed before choosing a library');
  assert.ok(calls.every(call=>['/api/app/state','/api/app/choose-folder'].includes(call.path)), 'setup wrote data before final review');
  await page.locator('#application-theme').click();
  await shot('machines-light');
  await page.locator('#submit').click();
  await page.locator('#submit').click();
  await page.locator('#submit').click();
  await page.locator('#submit').click();
  await page.locator('#next').waitFor({state:'visible'});
  assert.ok(await page.locator('#next').evaluate(element=>element===document.activeElement), 'completion did not focus source approval');
  await shot('ready');
});

const settingsLocal={id:'local',name:'Pending history',config:'/Libraries/Pending/library.toml',available:true,connected:true};
const settingsArchive={id:'archive',name:'My archive',config:'/Volumes/Archive/library.toml',available:true};
const settingsState={selected:settingsLocal.id,mcp:settingsLocal.id,busy:false,libraries:[settingsLocal,settingsArchive],local_library_parent:'/Users/test/Libraries'};

test('Library settings opens inline forms and returns focus when cancelled', {skip}, async t => {
  const {page,shot}=await open(t,{url:'/settings',ready:'#healthCards .panel',managedLibrary:settingsLocal,api:{
    'GET /api/app/state':settingsState,
    'POST /api/app/choose-folder':{path:'/Volumes/Research SSD'},
  }});
  await page.getByRole('link',{name:'Libraries',exact:true}).click();
  await page.locator('#library-settings-current h3').waitFor();
  await shot('libraries');
  for(const kind of ['open','create','restore']){
    const button=page.locator({open:'#library-settings-open',create:'#library-settings-new',restore:'#library-settings-restore'}[kind]);
    await button.click();
    const input=page.locator({open:'#library-settings-existing',create:'#library-settings-name',restore:'#library-settings-backup'}[kind]);
    assert.ok(await input.evaluate(el=>el===document.activeElement));
    const form=page.locator('#library-settings-'+kind+'-form');
    if(kind==='create'){
      await page.locator('#library-settings-storage').selectOption('ssd');
      await form.getByRole('button',{name:'Choose folder…',exact:true}).click();
      await page.waitForFunction(()=>document.querySelector('#library-settings-parent').value==='/Volumes/Research SSD'&&document.activeElement?.dataset.libraryPick==='library-settings-parent');
      assert.ok(await form.getByRole('button',{name:'Choose folder…',exact:true}).evaluate(el=>el===document.activeElement));
    }
    await shot(kind);
    await form.getByRole('button',{name:'Cancel',exact:true}).click();
    assert.ok(await button.evaluate(el=>el===document.activeElement));
  }
});

test('Library settings starts transfers and releases, then the startup window returns there', {skip}, async t => {
  await t.test('transfer', async t => {
    const {page,calls,shot}=await open(t,{url:'/settings/libraries',ready:'#library-settings-current h3',managedLibrary:settingsLocal,api:{
      'GET /api/app/state':({state})=>state.transferred?{...settingsState,libraries:[{...settingsLocal,connected:false},settingsArchive],job:{state:'complete',phase:'Library operation completed and verified',updated_at:new Date().toISOString()}}:settingsState,
      'POST /api/app/transfer':({state})=>{state.transferred=true;return {started:true}},
      'POST /api/app/connect':{saved:true},
    }});
    await page.getByRole('heading',{name:'Copy or move history',exact:true}).click();await shot('transfer');
    await page.getByRole('radio',{name:/^Move/}).check();
    await page.getByRole('button',{name:'Move history',exact:true}).click();
    await page.waitForURL(url=>url.pathname==='/settings/libraries'&&calls.some(call=>call.path==='/api/app/connect'));
    assert.deepEqual(calls.filter(call=>call.method==='POST').map(call=>[call.path,call.body]),[['/api/app/transfer',{source:'local',target:'archive',move:true}],['/api/app/connect',{id:'local'}]]);
  });
  await t.test('release', async t => {
    const {page,calls}=await open(t,{url:'/settings/libraries',ready:'#library-settings-current h3',managedLibrary:settingsArchive,api:{
      'GET /api/app/state':({state})=>({...settingsState,selected:'archive',libraries:[settingsLocal,{...settingsArchive,connected:!state.released}]}),
      'POST /api/app/disconnect':({state})=>{state.released=true;return {saved:true}},
    }});
    await page.getByRole('button',{name:'Release for eject',exact:true}).click();
    await page.locator('#notice').getByText('Library released',{exact:false}).waitFor();
    assert.deepEqual(calls.find(call=>call.path==='/api/app/disconnect').body,{id:'archive'});
    assert.ok(await page.getByRole('button',{name:'Reconnect to My archive',exact:true}).isVisible());
  });
});

test('Library settings renames in place and opens delivery from its switch, keeping focus', {skip}, async t => {
  const {page,calls}=await open(t,{url:'/settings/libraries',ready:'#library-settings-current h3',managedLibrary:settingsLocal,api:{
    'GET /api/app/state':({state})=>({...settingsState,libraries:[settingsLocal,{...settingsArchive,name:state.renamed||settingsArchive.name}]}),
    'POST /api/app/rename':({body,state})=>{state.renamed=body.name;return {saved:true}},
  }});
  const rename=page.getByRole('button',{name:'Rename My archive',exact:true});
  await rename.click();
  const input=page.getByRole('textbox',{name:'Library name',exact:true});
  assert.ok(await input.evaluate(el=>el===document.activeElement),'rename did not focus the name');
  await page.keyboard.press('Escape');
  assert.ok(await rename.evaluate(el=>el===document.activeElement),'cancelling rename lost focus');
  await rename.click();
  await input.fill('Archive drive');
  await page.keyboard.press('Enter');
  const renamed=page.getByRole('button',{name:'Rename Archive drive',exact:true});
  await renamed.waitFor();
  assert.ok(await renamed.evaluate(el=>el===document.activeElement),'saving a name lost focus');
  assert.deepEqual(calls.find(call=>call.path==='/api/app/rename').body,{id:'archive',name:'Archive drive'});
  const delivery=page.getByRole('switch',{name:'Capture here, deliver to a drive',exact:true});
  assert.ok(!await page.locator('#library-settings-pending').isVisible(),'delivery starts expanded while off');
  await delivery.click();
  assert.equal(await delivery.getAttribute('aria-checked'),'true');
  assert.ok(await page.locator('#library-settings-pending').evaluate(el=>el===document.activeElement),'opening delivery did not focus its first choice');
  assert.ok(!calls.some(call=>call.path==='/api/app/delivery'),'opening delivery turned it on');
});

test('Library settings switches within Settings and opens the selected library', {skip}, async t => {
  const {page,calls}=await open(t,{url:'/settings/libraries',ready:'#library-settings-current h3',managedLibrary:settingsLocal,api:{
    'GET /api/app/state':({state})=>({...settingsState,selected:state.managedLibrary.id}),
    'POST /api/app/connect':({body,state})=>{assert.equal(body.id,settingsArchive.id);state.managedLibrary=settingsArchive;return {saved:true}},
  }});
  await page.getByRole('button',{name:'Switch to My archive',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('#library-settings-current h3')?.textContent==='My archive');
  assert.equal(new URL(page.url()).pathname,'/settings/libraries');
  assert.deepEqual(calls.filter(call=>call.path==='/api/app/connect').map(call=>call.body),[{id:settingsArchive.id}]);
});

test('Library settings creates and opens libraries from its inline forms', {skip}, async t => {
  for(const kind of ['create','open'])await t.test(kind,async t=>{
    const {page,calls}=await open(t,{url:'/settings/libraries',ready:'#library-settings-current h3',managedLibrary:settingsLocal,api:{
      'GET /api/app/state':settingsState,
      ['POST /api/app/'+kind]:({state})=>{state.managedLibrary=settingsArchive;return {saved:true}},
    }});
    await page.locator(kind==='open'?'#library-settings-open':'#library-settings-new').click();
    if(kind==='open')await page.locator('#library-settings-existing').fill('/Volumes/Archive/library.toml');
    else {await page.locator('#library-settings-name').fill('Research');await page.locator('#library-settings-parent').fill('/Volumes/Archive')}
    const saved=page.waitForResponse(response=>response.url().endsWith('/api/app/'+kind)&&response.request().method()==='POST');
    await page.locator('#library-settings-'+kind+'-form button[type=submit]').click();
    await saved;
    await page.waitForFunction(()=>window.pharosManagedLibrary?.id==='archive');
    assert.equal(new URL(page.url()).pathname,kind==='open'?'/settings/libraries':'/settings/sources');
    assert.deepEqual(calls.find(call=>call.path==='/api/app/'+kind).body,kind==='open'?{path:'/Volumes/Archive/library.toml'}:{name:'Research',path:'/Volumes/Archive/Research',location:'mac'});
  });
});

test('Library proposal allows default setup and deliberate inline overrides', {skip}, async t => {
  const parent='/Users/test/Library/Application Support/Pharos/Libraries';
  const {page,calls}=await open(t,{url:'/app',ready:'#application-setup[open]',api:{
    'GET /api/app/state':{application:'/Applications/Pharos.app',selected:'',mcp:'',busy:false,libraries:[],local_library_parent:parent},
    'POST /api/app/create':{saved:true},
  }});
  await page.locator('#meet-pharos').click();await page.locator('#start-setup').click();
  await page.locator('#submit').click();await page.locator('#submit').click();
  assert.ok(await page.locator('#name').isHidden());assert.ok(await page.locator('#path').isHidden());
  assert.ok((await page.locator('#location-path').textContent()).includes(parent));
  await page.locator('#submit').click();
  assert.ok((await page.locator('#review-folder').textContent()).startsWith(parent+'/'));
  await page.locator('#cancel').click();
  await page.getByRole('button',{name:'Edit library location',exact:true}).click();
  assert.equal(await page.locator('#path').inputValue(),parent);
  assert.ok(await page.locator('#path').evaluate(element=>element===document.activeElement));
  await page.locator('#path').fill('/Users/test/Research');
  await page.getByRole('button',{name:'Done editing library location',exact:true}).click();
  await page.getByRole('button',{name:'Edit library name',exact:true}).click();
  await page.locator('#name').fill('');await page.locator('#submit').click();
  assert.ok(await page.locator('#name').evaluate(element=>element===document.activeElement));
  await page.locator('#name').fill('Research');
  await page.locator('#submit').click();await page.locator('#cancel').click();
  assert.ok(await page.locator('#name').isHidden());assert.ok(await page.locator('#path').isHidden());
  await page.locator('#submit').click();
  assert.ok(!calls.some(call=>call.path==='/api/app/create'), 'editing the proposal created a library');
  await page.locator('#submit').click();await page.locator('#next').waitFor({state:'visible'});
  assert.deepEqual(calls.find(call=>call.path==='/api/app/create').body,{name:'Research',path:'/Users/test/Research/Research',location:'mac'});
});

test('Source setup keeps tool choices and focus while reviewing folders before approval', {skip}, async t => {
  const {page,calls,shot}=await open(t,{url:'/app',ready:'#application-setup[open]',api:{
    'GET /api/app/state':{application:'/Applications/Pharos.app',selected:'source-review-library',mcp:'',busy:false,libraries:[]},
    'POST /api/app/create':{saved:true},
    'POST /api/probe/accept':{added:['claude'],enabled:[],declined:['claude-work'],skipped:[]},
    'POST /api/capture':{run:{id:'capture-test',state:'complete',sources:['claude'],bytes_copied:2400000,results:[]}},
    'POST /api/index':{run:{id:'index-test',state:'complete',sources:['claude'],conversations:24,messages:100,results:[]}},
  }});
  const libraryScopes=[];
  page.on('request',request=>{if(/^\/api\/(probe|capture|index)/.test(new URL(request.url()).pathname))libraryScopes.push(request.headers()['x-pharos-library'])});
  await page.locator('#meet-pharos').click();await page.locator('#start-setup').click();
  for(let step=0;step<4;step++)await page.locator('#submit').click();
  await page.locator('#configure-sources').click();
  const guide=page.locator('#pharosOnboarding');
  await guide.getByRole('group',{name:'Detected tools',exact:true}).waitFor();
  assert.ok(await guide.locator('h2').evaluate(element=>element===document.activeElement));
  assert.ok(await guide.getByRole('checkbox',{name:/Claude Code/}).isChecked());
  assert.ok(!await guide.getByRole('checkbox',{name:/Codex/}).isVisible());
  assert.ok(!calls.some(call=>call.path==='/api/probe'),'presence detection parsed source history');
  await shot('source-tools');
  await guide.getByText('Other tools',{exact:true}).click();
  await guide.getByRole('checkbox',{name:/Codex/}).check();
  await guide.getByRole('checkbox',{name:/Codex/}).uncheck();
  await guide.getByRole('checkbox',{name:/Claude Code/}).check();
  await guide.getByRole('button',{name:'Continue',exact:true}).click();
  await guide.locator('.pharos-candidate').first().waitFor();
  assert.ok(await guide.locator('h2').evaluate(element=>element===document.activeElement));
  await guide.getByRole('checkbox',{name:/claude-work\/projects/}).uncheck();
  await shot('source-locations');
  await guide.getByRole('button',{name:'Continue',exact:true}).click();
  assert.ok(await guide.locator('h2').evaluate(element=>element===document.activeElement));
  await shot('source-review');
  await page.keyboard.press('Escape');
  assert.ok(await guide.getByRole('button',{name:'Continue',exact:true}).isVisible());
  await guide.getByRole('button',{name:'Back',exact:true}).click();
  assert.ok(await guide.getByRole('checkbox',{name:/Claude Code/}).isChecked());
  await guide.getByRole('button',{name:'Continue',exact:true}).click();
  assert.ok(!await guide.getByRole('checkbox',{name:/claude-work\/projects/}).isChecked());
  await guide.getByRole('button',{name:'Continue',exact:true}).click();
  assert.ok(!calls.some(call=>['/api/probe/accept','/api/capture','/api/index'].includes(call.path)),'history was read or capture approved before final consent');
  await guide.getByRole('button',{name:'Add and capture',exact:true}).click();
  await guide.getByRole('button',{name:'Index now',exact:true}).waitFor();
  await guide.getByRole('button',{name:'Index now',exact:true}).click();
  await guide.getByRole('button',{name:'Done',exact:true}).waitFor();
  assert.ok(await guide.getByRole('button',{name:'Done',exact:true}).evaluate(element=>element===document.activeElement));
  assert.deepEqual(calls.find(call=>call.path==='/api/probe/accept').body,{kinds:['claude'],locations:[],accept:['claude'],decline:['claude-work'],names:{}});
  assert.ok(libraryScopes.length>0&&libraryScopes.every(scope=>scope==='source-review-library'),'source setup requests lost their library binding');
});

for (const topic of ['tokens', 'human', 'skills', 'time', 'carbon']) for (const display of ['interval', 'cumulative']) test(`Dashboard ${topic} ${display} inspection focuses its links and dismisses on Escape or outside click`, { skip }, async t => {
  const { page, settled, shot } = await open(t, { url: `/analyze?${new URLSearchParams({ analyze_topic: topic, analyze_chart: JSON.stringify({ display }) })}`, ready: '.usage-chart-bars:not(.loading) > button' });
  const chart = page.locator('.usage-dashboard .usage-chart').first();
  const bar = display === 'cumulative' ? chart.locator('.usage-chart-bars > button').last() : chart.locator('.usage-chart-bars > button').filter({ has: page.locator('i') }).first();
  await bar.focus();
  await page.keyboard.press('Enter');
  const details = chart.getByRole('group', { name: /^Details for / });
  await details.getByRole('link').first().waitFor();
  await shot('pinned-key');
  assert.ok(await details.evaluate(element => element.contains(document.activeElement)), 'keyboard inspection focuses a reachable detail link');
  await page.keyboard.press('Escape');
  await details.waitFor({ state: 'hidden' });
  assert.ok(await bar.evaluate(element => element === document.activeElement), 'Escape returns focus to the inspected period');
  await bar.click();
  await details.waitFor();
  await chart.locator('h3').click();
  await details.waitFor({ state: 'hidden' });
  await settled();
});

test('Dashboard chart pointer capture snaps to columns and treats a drag back to its anchor as inspection', { skip }, async t => {
  const { page, settled } = await open(t, { url: '/analyze?analyze_topic=tokens', ready: '.usage-chart-bars:not(.loading) > button' });
  const chart = page.locator('.usage-dashboard .usage-chart').first();
  const bars = chart.locator('.usage-chart-bars > button');
  const before = page.url();
  const start = await bars.nth(1).boundingBox(), end = await bars.nth(4).boundingBox();
  await page.mouse.move(start.x + start.width / 2, start.y + 30);
  await page.mouse.down();
  await page.mouse.move(end.x + end.width / 2, end.y + 30);
  await chart.getByRole('status').filter({ hasText: /4 periods.*Release to filter/ }).waitFor();
  await page.mouse.move(start.x + start.width / 2, start.y + 30);
  await chart.getByRole('status').filter({ hasText: /1 period.*Release to inspect/ }).waitFor();
  await page.mouse.up();
  await chart.getByRole('group', { name: /^Details for / }).waitFor();
  assert.equal(new URL(page.url()).searchParams.get('analyze_filters'),new URL(before).searchParams.get('analyze_filters'),'one-period inspection leaves date filters alone');
  assert.ok(Object.keys(JSON.parse(new URL(page.url()).searchParams.get('chart_pins'))).length,'a deliberate pinned bucket is represented in the URL');
  await chart.locator('h3').click();
  await page.mouse.move(end.x + end.width / 2, end.y + 30);
  await page.mouse.down();
  await page.mouse.move(start.x + start.width / 2, start.y + 30);
  await page.mouse.up();
  await page.waitForFunction(previous => location.href !== previous, before);
  await settled();
});

test('Cloud backup setup focuses invalid fields and retains storage-class keyboard focus', {skip}, async t => {
  const {page} = await open(t, {url:'/settings/backups',ready:'#cloud-explore'});
  await page.getByRole('button',{name:'Explore cloud backup options',exact:true}).click();
  await page.getByLabel('Cloud provider',{exact:true}).selectOption('gcp');
  await page.getByRole('button',{name:'Configure this backup',exact:true}).click();
  assert.ok(await page.locator('#cloud-step-1').evaluate(panel=>panel===document.activeElement));
  await page.getByRole('button',{name:'Review configuration & commands',exact:true}).click();
  assert.ok(await page.getByLabel('Google Cloud project ID',{exact:true}).evaluate(input=>input===document.activeElement));
  await page.getByLabel('Google Cloud project ID',{exact:true}).fill('pharos-example');
  const standard=page.getByLabel('Google Cloud Storage · Standard',{exact:true});
  await standard.focus();
  await page.keyboard.press('ArrowDown');
  assert.ok(await page.getByLabel('Google Cloud Storage · Nearline',{exact:true}).evaluate(input=>input===document.activeElement));
  await page.keyboard.press('ArrowDown');
  assert.ok(await page.getByLabel('Google Cloud Storage · Coldline',{exact:true}).evaluate(input=>input===document.activeElement));
  await page.getByLabel('Google Cloud project',{exact:true}).selectOption('new');
  await page.getByRole('button',{name:'Review configuration & commands',exact:true}).click();
  assert.ok(await page.getByLabel('Billing account ID',{exact:true}).evaluate(input=>input===document.activeElement));
});

test('Cloud backup price forecast supports keyboard exploration without losing focus', {skip}, async t => {
  const {page}=await open(t,{url:'/settings/backups',ready:'#cloud-explore'});
  await page.getByRole('button',{name:'Explore cloud backup options',exact:true}).click();
  for(const chart of await page.locator('.cloud-results svg').all()){
    await chart.focus();
    const readout=chart.locator('..').locator('output'),first=await readout.textContent();
    await page.keyboard.press('ArrowRight');
    assert.ok(await chart.evaluate(input=>input===document.activeElement));
    assert.notEqual(await readout.textContent(),first);
    await page.keyboard.press('End');
    assert.ok(await chart.evaluate(input=>input===document.activeElement));
    await page.keyboard.press('Home');
    assert.equal(await readout.textContent(),first);
  }
});

test('Cloud backup password stages keep keyboard focus while copying each independent block', {skip}, async t => {
  const {page}=await open(t,{url:'/settings/backups',ready:'#cloud-explore'});
  await page.getByRole('button',{name:'Explore cloud backup options',exact:true}).click();
  await page.getByRole('button',{name:'Configure this backup',exact:true}).click();
  await page.getByLabel('Google Cloud project ID',{exact:true}).fill('pharos-example');
  await page.getByLabel('Require a password to restore',{exact:true}).selectOption('true');
  await page.getByRole('button',{name:'Review configuration & commands',exact:true}).click();
  await page.evaluate(()=>{window.cloudCopies=0;Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:async()=>{window.cloudCopies++;}}});});
  for(const [index,label]of ['Copy preparation commands','Copy password setup command','Copy final commands'].entries()){
    const button=page.getByRole('button',{name:label,exact:true});await button.focus();await page.keyboard.press('Enter');
    await page.waitForFunction(count=>window.cloudCopies===count,index+1);
    assert.ok(await button.evaluate(element=>element===document.activeElement));
  }
});

test('Cloud backup wizard focuses the calculator after Reset and returns to Explore when closed', {skip}, async t => {
  const {page}=await open(t,{url:'/settings/backups',ready:'#cloud-explore'});
  const explore=page.getByRole('button',{name:'Explore cloud backup options',exact:true});
  await explore.click();
  await page.getByRole('button',{name:'Configure this backup',exact:true}).click();
  await page.getByRole('button',{name:'Review configuration & commands',exact:true}).click();
  await page.getByRole('button',{name:'Reset cloud backup options',exact:true}).click();
  assert.ok(await page.locator('#cloud-step-0').evaluate(panel=>panel===document.activeElement));
  await page.keyboard.press('Tab');
  assert.ok(await page.getByLabel('Cloud provider',{exact:true}).evaluate(input=>input===document.activeElement));
  await page.getByRole('button',{name:'Close cloud backup options',exact:true}).click();
  assert.ok(await explore.evaluate(input=>input===document.activeElement));
});

// The drive panel, opened from the header chip, is where running work is
// shown. Maintenance folds into the index's card, so the later tasks here are
// backups, which keep cards of their own.
test('Drive panel refresh preserves scrolling, focus and animation phase', {skip}, async t => {
  const main = {id: 'refreshing-index', kind: 'sync', label: 'Capture & index', stoppable: true,
    started_at: new Date(Date.now() - 120_000).toISOString(),
    work: {phase: 'Discovering inputs', source: 'Codex', done: 0, total: 0},
    forecast: {state: 'partial', remaining_steps: 4, predicted_steps: 3, basis: 'Learning the remaining analyses'}};
  let activities = [main, ...Array.from({length: 5}, (_, index) => ({id: `task-${index}`, kind: 'backup', label: `Background task ${index}`}))];
  let stopped = 0;
  const {page, settled, shot} = await open(t, {url: '/library', ready: '#queryTableLibrary .qt-row', viewport: {width: 390, height: 550}, api: {
    'GET /api/library/status': () => ({...defaultApi['GET /api/library/status'], idle: false, activities}),
    'POST /api/library/refresh/stop': () => {stopped++; return {stopped: true};},
  }});
  await page.emulateMedia({reducedMotion: 'no-preference'});
  await activateHeaderTool(page, 'pharosDrive');
  await settled();
  const panel = page.getByRole('dialog', {name: 'Library drive', exact: true});
  const summary = panel.locator('summary').first();
  await summary.click();
  await summary.focus();
  await page.evaluate(async () => {
    const panel = document.getElementById('pharosDrivePanel');
    panel.scrollTop = 160;
    const animations = [...panel.querySelector('[role="progressbar"]').getAnimations({subtree: true}),
      ...document.getElementById('pharosDrive').getAnimations({subtree: true})];
    if (!animations.length) throw new Error('No loading animations to exercise');
    for (const animation of animations) {animation.pause(); await animation.ready; animation.currentTime = 750;}
    window.activityRefreshTest = {animations, focused: document.activeElement, scrollTop: panel.scrollTop};
    if (!panel.scrollTop) throw new Error('Activity fixture must scroll');
  });
  activities = [{...main, stop_path: '/api/library/refresh/stop', detail: 'Updated discovery counts'}, ...activities.slice(1)];
  await page.evaluate(() => window.pharosLibrary.refresh());
  assert.ok(await page.evaluate(() => {
    const panel = document.getElementById('pharosDrivePanel'), before = window.activityRefreshTest;
    const animations = [...panel.getAnimations({subtree: true}), ...document.getElementById('pharosDrive').getAnimations({subtree: true})];
    return document.activeElement === before.focused && panel.scrollTop === before.scrollTop
      && panel.querySelector('details').open
      && before.animations.every(animation => animations.includes(animation) && animation.currentTime === 750);
  }), 'refresh interrupted scrolling, focus, disclosure state or an in-progress animation');
  // Removing a preceding task also keeps a later task's animation alive.
  await page.evaluate(async () => {
    const bar = [...document.querySelectorAll('#pharosDrivePanel [role="progressbar"]')].find(el => el.getAttribute('aria-label').startsWith('Background task 4: overall'));
    window.laterActivityAnimation = bar.getAnimations({subtree: true})[0];
    window.laterActivityAnimation.pause();
    await window.laterActivityAnimation.ready;
    window.laterActivityAnimation.currentTime = 900;
  });
  activities = [activities[0], ...activities.slice(2)];
  await page.evaluate(() => window.pharosLibrary.refresh());
  assert.ok(await panel.evaluate(element => element.getAnimations({subtree: true}).includes(window.laterActivityAnimation) && window.laterActivityAnimation.currentTime === 900));
  // Exercise the state-preserving move fallback used by older browsers.
  await page.evaluate(() => {Element.prototype.moveBefore = undefined;});
  activities = [activities[0], ...activities.slice(1).reverse()];
  await page.evaluate(() => window.pharosLibrary.refresh());
  assert.ok(await panel.evaluate(element => {
    const bar = [...element.querySelectorAll('[role="progressbar"]')].find(el => el.getAttribute('aria-label').startsWith('Background task 4: overall'));
    return bar.getAnimations({subtree: true})[0].currentTime === 900;
  }), 'reordering tasks restarted an in-progress animation');
  const stop = panel.getByRole('button', {name: 'Stop', exact: true});
  await stop.click();
  await settled();
  assert.equal(stopped, 1, 'retained Stop button did not use the latest stop target');
  // A measured step can replace discovery without closing its details.
  activities = [{...activities[0], work: {phase: 'Indexing', source: 'Codex', done: 42, total: 100},
    forecast: {state: 'provisional', progress: .63, low_seconds: 120, high_seconds: 180}}, ...activities.slice(1)];
  await page.evaluate(() => window.pharosLibrary.refresh());
  assert.ok(await panel.locator('details').first().evaluate(element => element.open));
  activities = [activities[0]];
  await page.evaluate(() => window.pharosLibrary.refresh());
  await panel.locator('summary').first().click();
  await panel.evaluate(element => {element.scrollTop = 0;});
  await shot('refreshed');
});

test("summary value links retain keyboard focus when the summary rerenders", { skip }, async t => {
  const { page } = await open(t, { url: "/library", ready: "#queryTableLibrary .qt-row", preferences: { "pharos-usage-summary-links": true, "pharos-quick-stats-v1": {metrics: ["cost_usd", "tokens"], windows: ["today", "6h"]} } });
  await activateHeaderTool(page, "usageSummaryToggle");
  const link = page.getByRole("link", { name: /^Tokens, 6h:/ });
  await link.focus();
  await page.evaluate(() => window.dispatchEvent(new Event("pharos:carbon-settings")));
  await page.waitForFunction(() => document.activeElement?.getAttribute("aria-label")?.startsWith("Tokens, 6h:"));
  await page.keyboard.press("Escape");
  await page.locator("#usageSummary").waitFor({ state: "hidden" });
  await page.waitForFunction(() => document.activeElement?.id === "usageSummaryToggle");
});

// Focus is in `surface`, or with the browser itself (a native modal dialog
// lets Tab leave the page past its last control), never on the page behind.
const kept = surface => surface.evaluate(element => { const active = document.activeElement; return !active || active === document.body || element.contains(active); });

// Opens a surface from the keyboard, as a keyboard user would (WebKit, like
// Safari, does not focus a button that is clicked).
async function press(opener, key = "Enter") {
  await opener.focus();
  await opener.page().keyboard.press(key);
}

// Focus moves into `surface` and stays there while Tab runs past its end and
// Shift+Tab past its start; Escape closes it and focus returns to `opener`.
async function holdsFocus(page, surface, opener) {
  await page.waitForFunction(element => element.contains(document.activeElement), await surface.elementHandle());
  for (const key of [...Array(12).fill("Tab"), ...Array(4).fill("Shift+Tab")]) {
    await page.keyboard.press(key);
    assert.ok(await kept(surface), `focus moved behind the surface after ${key}`);
  }
  await page.keyboard.press("Escape");
  await surface.waitFor({ state: "hidden" });
  await page.waitForFunction(element => document.activeElement === element, await opener.elementHandle());
}

// The unified activity panel (flag on) takes focus from the chip, keeps it
// while open, gives it back on Escape, and keeps focus and its disclosure
// across a pushed snapshot. Stop reaches every stoppable operation through
// its declared stop; Eject acts only on a fresh verdict.
test("Unified activity panel holds focus across refreshes, stops each operation and ejects only on a fresh verdict", { skip }, async t => {
  let fresh = { verdict: "busy", summary: "Busy", blockers: [{ label: "L", on_eject: "O" }], checked: "fresh" };
  const app = await open(t, { url: "/library", ready: "#queryTableLibrary .qt-row", preferences: { "pharos-unified-activity-v1": true }, api: {
    "GET /api/status/stream": eventStream(busyStatus),
    "GET /api/status": ({ query }) => query.get("fresh") === "1" ? { ...busyStatus, eject: fresh } : busyStatus,
  } });
  const { page, settled, calls, push } = app;
  await page.evaluate(() => { window.ejects = 0; window.webkit = { messageHandlers: { pharosLibrary: { postMessage: async () => { window.ejects++; } } } }; });
  const chip = page.locator("#pharosDrive");
  await page.waitForFunction(() => window.pharosStatusStore.current()?.view?.library?.revision === 2);
  await press(chip);
  const panel = page.getByRole("dialog", { name: "Library drive", exact: true });
  await panel.waitFor();
  await holdsFocus(page, panel, chip);
  assert.equal(await page.locator("#pharosDrivePanel").count(), 0, "the old drive panel stays closed while the flag is on");

  await press(chip);
  await panel.waitFor();
  // Daily is closed in this snapshot: open it.
  const daily = panel.locator('[data-key="cadence:daily"]');
  await daily.locator("summary").click();
  const stop = panel.getByRole("button", { name: "Stop", exact: true });
  await stop.focus();
  push("/api/status/stream", { ...busyStatus, revision: 3, operations: busyStatus.operations.map(operation => operation.id === "op-index" ? { ...operation, step: { ...operation.step, done: 900e6, fraction: 0.45 } } : operation) });
  await page.waitForFunction(() => window.pharosStatusStore.current()?.view?.library?.revision === 3);
  assert.ok(await stop.evaluate(element => document.activeElement === element), "a refresh moved focus");
  assert.ok(await daily.evaluate(element => element.open), "a refresh closed the cadence row the user opened");
  await app.shot("refreshed");

  // A busy fresh verdict refuses; a safe one ejects.
  await panel.getByRole("button", { name: /^Eject/ }).click();
  await panel.getByRole("button", { name: "Check again", exact: true }).waitFor();
  assert.equal(await page.evaluate(() => window.ejects), 0, "ejected on a busy verdict");
  assert.ok(calls.some(call => call.path === "/api/status" && call.query.get("fresh") === "1"));
  fresh = { verdict: "safe", summary: "Safe", blockers: [], checked: "fresh" };
  await panel.getByRole("button", { name: "Check again", exact: true }).click();
  await page.waitForFunction(() => window.ejects === 1);

  // A stop that fails leaves Stop enabled to try again; one that works
  // keeps it disabled while the operations wind down.
  // (The refusal is answered in the page, as a 409 from the service would
  // be, so the harness does not count it as a failed request.)
  await page.evaluate(() => {
    const real = window.fetch;
    window.fetch = (input, options) => String(input).includes("/api/operations/stop") && window.stopFails
      ? Promise.resolve(new Response(JSON.stringify({ error: "try again in a moment" }), { status: 409 })) : real(input, options);
    window.stopFails = true;
  });
  await stop.click();
  await page.waitForFunction(() => !document.querySelector('#pharosStatusPanel [data-key="stop"]').disabled);
  await page.evaluate(() => { window.stopFails = false; });
  await stop.click();
  await settled();
  const stopped = calls.filter(call => call.method === "POST" && call.path === "/api/operations/stop").map(call => call.body.id).sort();
  assert.deepEqual(stopped, ["op-backup", "op-index", "op-view"]);
  assert.ok(await page.evaluate(() => document.querySelector('#pharosStatusPanel [data-key="stop"]').disabled), "Stop enabled again after it worked");
});

// With the flag off (the default) the old drive panel runs and nothing reads
// the status snapshot. The Settings switch takes over the chip at once, and
// turning it off gives it back.
test("Unified activity switch: off leaves the old drive panel alone; on and off again switch the chip's panel", { skip }, async t => {
  const { page, settled, calls } = await open(t, { url: "/settings/preferences", ready: "#unifiedActivity" });
  const chip = page.locator("#pharosDrive");
  const statusCalls = () => calls.filter(call => call.path.startsWith("/api/status") || call.path.startsWith("/api/app/status")).length;
  await chip.click();
  await page.locator("#pharosDrivePanel").waitFor();
  await chip.click();
  await settled();
  assert.equal(statusCalls(), 0, "the flag is off, yet the page read the status snapshot");
  assert.equal(await page.locator("#pharosStatusPanel").count(), 0);

  await page.locator("#unifiedActivity").click();
  await page.waitForFunction(() => window.pharosStatusStore.current()?.view?.library);
  assert.ok(calls.some(call => call.method === "POST" && call.path === "/api/preferences" && call.body?.set?.["pharos-unified-activity-v1"] === true));
  await chip.click();
  await page.locator("#pharosStatusPanel").waitFor();
  assert.equal(await page.locator("#pharosDrivePanel").count(), 0);
  await page.keyboard.press("Escape");

  await page.locator("#unifiedActivity").click();
  await settled();
  await chip.click();
  await page.locator("#pharosDrivePanel").waitFor();
  assert.equal(await page.locator("#pharosStatusPanel").count(), 0);
});

for (const width of [1440, 390, 320]) test(`Data guide takes focus, keeps controls reachable and returns focus @ ${width}`, { skip }, async t => {
  const app = await open(t, { url: "/analyze?analyze_mode=query&analyze_data=tool_calls&analyze_topic=tools", viewport: { width, height: 900 }, ready: ".usage-workspace-table .qt-row" });
  const { page } = app;
  const opener = page.locator(".usage-workspace-table .data-source-link");
  await app.shot("from-statement");
  await press(opener);
  const dialog = page.getByRole("dialog", { name: "Data Guide", exact: true });
  await dialog.waitFor();
  await app.shot("tool-call-guide");
  await dialog.getByRole("button", { name: "Review", exact: true }).click();
  await press(dialog.getByRole("button", { name: /Agent sessions/ }));
  const source = dialog.getByRole("region", { name: "Dataset row definitions", exact: true });
  await source.waitFor({ state: "hidden" });
  await press(dialog.getByRole("navigation", { name: "Data layers", exact: true }).getByRole("button", { name: "Tool calls", exact: true }));
  await source.waitFor({ state: "visible" });
  await holdsFocus(page, dialog, opener);
  await press(opener);
  await dialog.getByRole("button", { name: "Close data guide", exact: true }).click();
  await page.waitForFunction(element => document.activeElement === element, await opener.elementHandle());
  await press(page.getByRole("button", { name: "Collapse query builder", exact: true }));
  await app.shot("collapsed-query");
  await press(page.getByRole("button", { name: "Expand query builder", exact: true }));
  await press(opener);
  await holdsFocus(page, dialog, opener);
});

test("Data guide is available outside Analyze in the light theme", { skip }, async t => {
  const app = await open(t, { url: "/library", preferences: { "pharos-theme": "light" }, ready: "#queryTableLibrary .qt-row" });
  await press(app.page.locator("#queryTableLibrary .data-source-link"));
  await app.page.getByRole("dialog", { name: "Data Guide", exact: true }).waitFor();
  await app.shot("workspace-guide-light");
});

// Exercise the guide alongside native grouping, filter and aggregate editors.
for (const [dataset, path, numeric] of [["library", "/library", "conversation_count"], ["tool_calls", "/analyze?analyze_mode=query&analyze_data=tool_calls", "duration_ms"]]) {
  test(`Data guide preserves native grouping editors for ${dataset}`, { skip }, async t => {
    const query = {
      ...EMPTY_QUERY,
      select: [{ field: "repository_name" }, { field: "total" }],
      grouping: { source: EMPTY_QUERY, keys: ["repository_name"], summaries: [{ id: "total", op: "sum", field: numeric, label: "Total" }], representative: null },
    };
    const url = new URL(path, "http://localhost");
    url.searchParams.set(`q_${dataset}`, encodeQuery(query));
    const app = await open(t, { url: url.pathname + url.search, ready: ".qt-table .qt-row" });
    const { page } = app;
    const aggregate = page.getByRole("combobox", { name: /^Aggregation for/ }).first();
    await aggregate.selectOption("avg");
    assert.equal(await aggregate.inputValue(), "avg");
    for (const name of ["Add filter", "Add HAVING filter", "Customize columns"]) {
      const opener = page.getByRole("button", { name, exact: true });
      await press(opener);
      const editor = page.getByRole("dialog");
      await page.waitForFunction(element => element.contains(document.activeElement), await editor.elementHandle());
      await page.keyboard.press("Escape");
      await editor.waitFor({ state: "hidden" });
    }
    const guideLink = page.locator(".data-source-link");
    await press(guideLink);
    await holdsFocus(page, page.getByRole("dialog", { name: "Data Guide", exact: true }), guideLink);
    await app.shot(`${dataset}-grouping-with-source`);
    await press(page.getByRole("button", { name: "Collapse query builder", exact: true }));
    await app.shot(`${dataset}-grouping-collapsed`);
    await press(page.getByRole("button", { name: "Expand query builder", exact: true }));
    await press(guideLink);
    await holdsFocus(page, page.getByRole("dialog", { name: "Data Guide", exact: true }), guideLink);
    await press(page.getByRole("button", { name: "Remove Repository grouping", exact: true }));
    const addGroup = page.getByRole("button", { name: "Add group by", exact: true });
    await press(addGroup);
    const picker = page.getByRole("dialog", { name: "Group rows by", exact: true });
    await page.waitForFunction(element => element.contains(document.activeElement), await picker.elementHandle());
    await page.keyboard.press("Escape");
    await picker.waitFor({ state: "hidden" });
    await page.waitForFunction(element => document.activeElement === element, await addGroup.elementHandle());
  });
}

// Exercise portal focus and dismissal against both native and role-based modals.
async function useSearchMenus(page, dialog) {
  const options = dialog.getByRole("button", { name: /^Match options/ });
  await press(options);
  const optionsPopup = page.getByRole("dialog", { name: /^Match options/ });
  await page.waitForFunction(element => element.contains(document.activeElement), await optionsPopup.elementHandle());
  const matchCase = optionsPopup.getByRole("checkbox", { name: "Match letter case", exact: true });
  await matchCase.check();
  assert.equal(await matchCase.isChecked(), true);
  await optionsPopup.getByText("Match letter case", { exact: true }).click();
  assert.equal(await matchCase.isChecked(), false);
  await matchCase.focus();
  await page.keyboard.press("Space");
  assert.equal(await matchCase.isChecked(), true);
  await page.keyboard.press("Escape");
  await optionsPopup.waitFor({ state: "hidden" });
  assert.equal(await dialog.isVisible(), true);
  await page.waitForFunction(element => document.activeElement === element, await options.elementHandle());

  const add = dialog.getByRole("button", { name: "Add condition", exact: true });
  await press(add);
  const addPopup = page.getByRole("dialog", { name: "Add condition", exact: true });
  await page.waitForFunction(element => element.contains(document.activeElement), await addPopup.elementHandle());
  await page.keyboard.press("Tab");
  assert.ok(await addPopup.evaluate(element => element.contains(document.activeElement)), "keyboard focus left the condition menu");
  await page.keyboard.press("Escape");
  await addPopup.waitFor({ state: "hidden" });
  assert.equal(await dialog.isVisible(), true);
  await page.waitForFunction(element => document.activeElement === element, await add.elementHandle());
}

test("quick search takes focus from the header button and from the keyboard shortcut, and returns it", { skip }, async t => {
  const { page } = await open(t, { url: "/library", ready: "#queryTableLibrary .qt-row" });
  const dialog = page.locator("#searchDialog"), button = page.locator("#searchToggle");
  await press(button);
  await useSearchMenus(page, dialog);
  await holdsFocus(page, dialog, button);
  const tab = page.getByRole('navigation', { name: 'Primary navigation', exact: true }).getByRole('button', { name: 'Analyze', exact: true });
  await press(tab, "ControlOrMeta+k");
  await holdsFocus(page, dialog, tab);
});

for (const viewport of [{ width: 2133, height: 1075 }, { width: 1440, height: 900 }, { width: 1024, height: 768 }, { width: 700, height: 768 }]) {
  test(`header page buttons remain reachable and navigate at ${viewport.width}px`, { skip }, async t => {
    const { page } = await open(t, { url: '/analyze', viewport, ready: '.usage-dashboard .usage-chart' });
    const navigation = page.getByRole('navigation', { name: 'Primary navigation', exact: true });
    for (const button of await navigation.getByRole('button').all()) {
      await button.focus();
      assert.equal(await onScreen(button), true, `${await button.textContent()} is outside the viewport`);
    }
    await press(navigation.getByRole('button', { name: 'Search', exact: true }));
    await page.locator('#queryTableLibrary .qt-row').first().waitFor();
    assert.equal(new URL(page.url()).pathname, '/library');
    await press(navigation.getByRole('button', { name: 'Analyze', exact: true }));
    await page.locator('.usage-dashboard .usage-chart').first().waitFor();
    assert.equal(new URL(page.url()).pathname, '/analyze');
  });
}

for (const viewport of [{ width: 2133, height: 1075 }, { width: 1440, height: 900 }, { width: 1280, height: 720 }, { width: 1024, height: 768 }]) {
  test(`section sidebar collapses, restores space and navigates at ${viewport.width}px`, { skip }, async t => {
    const { page } = await open(t, { url: '/analyze', viewport, ready: '.usage-dashboard .usage-chart' });
    const sidebar = page.getByRole('complementary', { name: 'Analyze navigation', exact: true });
    const heading = page.getByRole('heading', { name: 'Tokens and Cost', exact: true });
    await sidebar.waitFor();
    const sidebarBox = await sidebar.boundingBox(), expanded = await heading.boundingBox();
    assert.ok(expanded.x >= sidebarBox.x + sidebarBox.width, 'sidebar overlaps the page');
    const pageBox = await page.locator('main>.view.active').boundingBox();
    assert.ok(Math.abs(sidebarBox.x + sidebarBox.width - pageBox.x) < 1, 'sidebar stands apart from the content');
    await press(page.getByRole('button', { name: 'Collapse sidebar', exact: true }));
    await sidebar.waitFor({ state: 'hidden' });
    await page.waitForFunction(left => document.querySelector('main .view.active h1').getBoundingClientRect().left < left, expanded.x);
    const expand = page.getByRole('button', { name: 'Expand sidebar', exact: true });
    await page.waitForFunction(element => document.activeElement === element, await expand.elementHandle());
    const expandBox = await expand.boundingBox(), collapsedHeading = await heading.boundingBox();
    assert.ok(expandBox.x + expandBox.width <= collapsedHeading.x, 'sidebar control overlaps the page heading');
    if (await page.evaluate(() => { const main = document.querySelector('main'); return main.scrollHeight > main.clientHeight; })) {
      await page.evaluate(() => document.querySelector('main').scrollTo(0, 1200));
      await page.waitForFunction(() => document.querySelector('main').scrollTop > 0);
    }
    assert.equal(await onScreen(expand), true, 'sidebar control scrolls out of reach');
    assert.ok(await expand.evaluate(element => {
      const box = element.getBoundingClientRect();
      return element.contains(document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2));
    }), 'sidebar control is covered by page content');
    await press(expand);
    await sidebar.waitFor();
    await press(sidebar.getByRole('link', { name: /^Human words$/i }));
    await page.locator('.usage-human-dashboard .usage-chart').waitFor();
    await page.waitForFunction(() => document.activeElement?.tagName === 'MAIN');
    assert.equal(await sidebar.getByRole('link', { name: /^Human words$/i }).getAttribute('aria-current'), 'page');
  });
}

test('navigation keeps focus reachable when resizing between a sidebar and phone switcher', { skip }, async t => {
  const { page } = await open(t, { url: '/analyze', viewport: { width: 1280, height: 720 }, ready: '.usage-dashboard .usage-chart' });
  const sidebar = page.getByRole('complementary', { name: 'Analyze navigation', exact: true });
  await sidebar.getByRole('link', { name: /^Human words$/i }).focus();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForFunction(() => document.activeElement === document.getElementById('appNavigationToggle'));
  await press(page.locator('#appNavigationToggle'));
  await page.getByRole('dialog', { name: 'Navigate Pharos' }).waitFor();
  await page.setViewportSize({ width: 1024, height: 768 });
  await sidebar.waitFor();
  await page.waitForFunction(() => document.activeElement === document.getElementById('appSidebarToggle'));
  await press(page.getByRole('button', { name: 'Collapse sidebar', exact: true }));
  await page.setViewportSize({ width: 390, height: 844 });
  await page.locator('#appNavigationToggle').waitFor();
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.getByRole('button', { name: 'Expand sidebar', exact: true }).waitFor();
  await press(page.getByRole('button', { name: 'Expand sidebar', exact: true }));
  await sidebar.waitFor();
});

for (const width of [1440, 390, 320]) test(`Search content search takes focus and returns it to the query builder @ ${width}`, { skip }, async t => {
  const { page } = await open(t, { url: "/library", viewport: { width, height: 844 }, ready: "#queryTableLibrary .qt-row" });
  const button = page.getByRole("button", { name: "Messages", exact: true });
  await press(button);
  const dialog = page.getByRole("dialog", { name: "Search within work", exact: true });
  await useSearchMenus(page, dialog);
  await holdsFocus(page, dialog, button);
});

for (const width of [1440, 390, 320]) test(`Search long query can scroll to submit @ ${width}`, { skip }, async t => {
  const draft = {
    editor: "visual", match: "rules", text: "", mode: "advanced", fuzzy: false, caseSensitive: false, separators: false,
    scope: { surface: "prose", roles: [], kinds: [], repository: "", provider: "", source: "", from: "", to: "", conversation_id: "", workspace_id: "" },
    root: { id: "long-query", kind: "group", join: "all", exclude: false,
      children: Array.from({ length: 12 }, (_, index) => ({ id: `rule-${index}`, kind: "words", text: `condition ${index + 1}`, other: "", distance: "8", exclude: false })) },
  };
  const app = await open(t, { url: `/library?${new URLSearchParams({ text_builder: JSON.stringify(draft) })}`, viewport: { width, height: 600 }, ready: "#queryTableLibrary .qt-row",
    api: { "POST /api/search/text/validate": ({ body }) => ({ where: body.where }) } });
  const { page } = app;
  await press(page.getByRole("button", { name: "Edit messages search", exact: true }));
  const dialog = page.getByRole("dialog", { name: "Search within work", exact: true });
  const heading = dialog.getByRole("heading", { name: "Search within work", exact: true });
  const inViewport = element => { const rect = element.getBoundingClientRect(); return rect.top >= 0 && rect.bottom <= innerHeight; };
  assert.ok(await heading.evaluate(inViewport), "the modal heading is outside the viewport");
  const bounds = await dialog.boundingBox();
  await page.mouse.move(bounds.x + bounds.width / 2, bounds.y + bounds.height / 2);
  await page.mouse.wheel(0, 2400);
  const submit = dialog.getByRole("button", { name: "Search messages", exact: true });
  await page.waitForFunction(inViewport, await submit.elementHandle());
  assert.ok(await heading.evaluate(inViewport), "scrolling hid the modal heading");
  await app.shot("scrolled-to-submit");
  await submit.click();
  await dialog.waitFor({ state: "hidden" });
  await app.settled();
});

for (const route of [
  { name: "Analyze", url: "/analyze", ready: ".usage-dashboard .usage-chart", link: /^Human words$/i, destination: ".usage-human-dashboard .usage-chart" },
  { name: "Settings", url: "/settings", ready: "#healthCards .panel:not([aria-busy])", link: /^Sources$/, destination: "#sourceGrid .source-card" },
]) test(`${route.name} navigation is usable from the keyboard on a phone`, { skip }, async t => {
  const { page } = await open(t, { url: route.url, viewport: { width: 390, height: 844 }, ready: route.ready });
  const menu = page.getByRole("dialog", { name: "Navigate Pharos" });
  await press(page.locator("#appNavigationToggle"));
  const target = menu.getByRole("link", { name: route.link });
  await target.waitFor();
  await press(target);
  await page.locator(route.destination).first().waitFor();
  await page.waitForFunction(() => document.activeElement?.tagName === "MAIN");
  assert.equal(await menu.isVisible(), false);
  await page.goBack();
  await page.locator(route.ready).first().waitFor();
});

for (const viewport of [{ width: 390, height: 844 }, { width: 320, height: 640 }]) {
  test(`app navigation takes and returns focus, and reaches other app sections at ${viewport.width}px`, { skip }, async t => {
    const { page } = await open(t, { url: '/analyze', viewport, ready: '.usage-dashboard .usage-chart' });
    const opener = page.locator('#appNavigationToggle'), dialog = page.getByRole('dialog', { name: 'Navigate Pharos' });
    await press(opener);
    await holdsFocus(page, dialog, opener);
    await press(opener);
    await press(dialog.getByRole('button', { name: 'All pages', exact: true }));
    await press(dialog.getByRole('button', { name: 'Settings', exact: true }));
    await press(dialog.getByRole('link', { name: 'Sources', exact: true }));
    await page.locator('#sourceGrid .source-card').first().waitFor();
    await page.waitForFunction(() => document.activeElement?.tagName === 'MAIN');
    assert.equal(new URL(page.url()).pathname, '/settings/sources');
    await page.goBack();
    await page.locator('.usage-dashboard .usage-chart').waitFor();
    assert.equal(await opener.getAttribute('aria-expanded'), 'false');
    await press(opener);
    await press(dialog.getByRole('button', { name: 'All pages', exact: true }));
    await press(dialog.getByRole('link', { name: 'Home', exact: true }));
    await page.locator('.home-intro h1').waitFor();
    await page.waitForFunction(() => document.activeElement?.tagName === 'MAIN');
    assert.equal(await page.locator('.home-intro h1').evaluate(element => { element.focus(); return document.activeElement === element; }), false, 'navigation keeps the welcome text unfocusable');
  });
}

for (const viewport of [{ width: 850, height: 720 }, { width: 390, height: 844 }]) {
  test(`overflowed header tools remain live and restore focus at ${viewport.width}px`, { skip }, async t => {
    const { page } = await open(t, { url: '/library', viewport, ready: '#queryTableLibrary .qt-row',
      preferences: { 'pharos-header-tools-v1': ['summary', 'theme', 'history', 'library', 'search', 'settings'] },
    });
    const more = page.getByRole('button', { name: 'More tools', exact: true });
    await press(more);
    const popup = page.getByRole('dialog', { name: 'Header tools' });
    await popup.waitFor();
    await page.keyboard.press('Escape');
    await popup.waitFor({ state: 'hidden' });
    await page.waitForFunction(element => document.activeElement === element, await more.elementHandle());
    const theme = await page.locator('html').getAttribute('data-theme');
    await activateHeaderTool(page, 'themeToggle');
    assert.notEqual(await page.locator('html').getAttribute('data-theme'), theme);
    await activateHeaderTool(page, 'usageSummaryToggle');
    await page.locator('#usageSummary').waitFor();
    await page.keyboard.press('Escape');
    await page.locator('#usageSummary').waitFor({ state: 'hidden' });
    await page.waitForFunction(element => document.activeElement === element, await more.elementHandle());
  });
}

for (const tools of [['settings'], ['back', 'sync', 'settings']]) {
  test(`header respects saved tool visibility with ${tools.join(', ')}`, { skip }, async t => {
    const { page, settled } = await open(t, {
      url: '/library', ready: '#queryTableLibrary .qt-row',
      preferences: { 'pharos-header-tools-v1': tools },
    });
    // Give history a previous page so an accidentally exposed Back is enabled.
    await press(page.getByRole('navigation', { name: 'Primary navigation', exact: true }).getByRole('button', { name: 'Analyze', exact: true }));
    await page.locator('.usage-dashboard .usage-chart').first().waitFor();
    for (const width of [1440, 1280, 390]) {
      await page.setViewportSize({ width, height: 900 });
      await settled();
      const more = page.getByRole('button', { name: 'More tools', exact: true }), overflow = await more.isVisible();
      if (overflow) await press(more);
      for (const [tool, id] of [['history', 'headerHistory'], ['back', 'headerBackOnly'], ['library', 'headerLibrary'], ['sync', 'headerSyncOnly']]) {
        assert.equal(await page.locator(`#${id}`).isVisible(), tools.includes(tool), `${tool} visibility differs from saved choices at ${width}px`);
      }
      const settings = page.locator('#settingsToggle');
      await settings.focus();
      await page.locator('#navBack').evaluate(element => element.focus());
      await page.waitForFunction(element => document.activeElement === element, await settings.elementHandle());
      if (overflow) await page.keyboard.press('Escape');
    }
  });
}

for (const width of [1440, 390]) test(`Optimize pages move keyboard focus to their content at ${width}px`, { skip }, async t => {
  const { page, settled, shot } = await open(t, { url: '/findings', viewport: { width, height: 900 }, ready: '.findings-card' });
  const phone = width === 390;
  const menu = page.getByRole('dialog', { name: 'Navigate Pharos' });
  const navigation = phone ? menu : page.getByRole('complementary', { name: 'Optimize navigation', exact: true });
  async function choose(name, ready) {
    if (phone) await press(page.locator('#appNavigationToggle'));
    await press(navigation.getByRole('link', { name, exact: true }));
    await page.locator(ready).first().waitFor();
    await page.waitForFunction(() => document.activeElement?.tagName === 'MAIN');
    if (phone) assert.equal(await menu.isVisible(), false);
  }
  await choose('Settings', '.findings-settings .findings-panel');
  await choose('Table', '.findings-table .qt-row');
  // Settings bypasses the empty-library state without remounting the page,
  // so the retained findings table survives the round trip.
  const table = await page.locator('.findings-table').elementHandle();
  await choose('Settings', '.findings-settings .findings-panel');
  await choose('Table', '.findings-table .qt-row');
  assert.ok(await table.evaluate(element => element.isConnected), 'Findings Settings keeps the findings table mounted');
  await choose('Dashboard', '.findings-card');
  await settled();
  await shot('optimize-findings');
});

test('header focus survives resizing while its tool remains inline', { skip }, async t => {
  const { page, settled } = await open(t, { url: '/library', ready: '#queryTableLibrary .qt-row' });
  const settings = page.locator('#settingsToggle');
  await settings.focus();
  await page.evaluate(() => dispatchEvent(new Event('resize')));
  await page.waitForFunction(element => document.activeElement === element, await settings.elementHandle());
  await page.setViewportSize({ width: 1600, height: 900 });
  await settled();
  await page.waitForFunction(element => document.activeElement === element, await settings.elementHandle());
  const theme = page.locator('#themeToggle');
  await theme.focus();
  for (const width of [1280, 390, 1440]) {
    await page.setViewportSize({ width, height: 900 });
    await settled();
    await page.waitForFunction(element => document.activeElement === element, await theme.elementHandle());
  }
});

test('header focus stays reachable when its tool moves between inline and overflow', { skip }, async t => {
  const { page } = await open(t, { url: '/library', viewport: { width: 1440, height: 900 }, ready: '#queryTableLibrary .qt-row' });
  await page.locator('#searchToggle').focus();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForFunction(() => document.activeElement === document.getElementById('headerMore'));
  await press(page.locator('#headerMore'));
  await page.locator('#searchToggle').focus();
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.waitForFunction(() => document.activeElement === document.getElementById('searchToggle'));
});

test("MCP call details take focus, keep it and return it on Escape", { skip }, async t => {
  const { page } = await open(t, { url: "/analyze?analyze_mode=query&analyze_data=mcp_calls&analyze_topic=mcp", ready: ".usage-workspace-table .qt-row" });
  const opener = page.getByRole("button", { name: /^Open item .+ in modal$/ }).first();
  await press(opener);
  const dialog = page.getByRole("dialog");
  await dialog.getByText("Arguments (sanitized)", { exact: true }).waitFor();
  await holdsFocus(page, dialog, opener);
});

test("grouped results' Detail takes focus, keeps it and returns it on Escape", { skip }, async t => {
  const grouped = withGroupingKeys({ ...EMPTY_QUERY, select: [{ field: "tool_name" }] }, ["tool_name"]);
  const { page } = await open(t, { url: `/analyze?${new URLSearchParams({ analyze_mode: "query", analyze_data: "tool_calls", q_tool_calls: encodeQuery(grouped) })}`, ready: ".usage-workspace-table .qt-row" });
  const opener = page.locator(".usage-workspace-table").getByRole("button", { name: /^Open .+ in modal$/ }).first();
  await press(opener);
  const dialog = page.getByRole("dialog", { name: "Group details" });
  await dialog.waitFor();
  await holdsFocus(page, dialog, opener);
});

test("MCP settings switch remains usable when moving between Settings and Analyze", { skip }, async t => {
  const { page, settled } = await open(t, { url: "/settings/mcp", ready: "#mcpSettingsPage .mcp-setup-grid" });
  const toggle = page.getByRole("switch", { name: "Enable MCP", exact: true });
  await page.waitForFunction(() => !document.querySelector('[aria-label="Enable MCP"]').disabled);
  await press(toggle);
  await page.waitForFunction(() => document.querySelector('[aria-label="Enable MCP"]').getAttribute("aria-checked") === "false");
  await press(page.getByRole("link", { name: "View MCP usage", exact: true }));
  await page.locator(".usage-mcp-dashboard .qt-metric").waitFor();
  await press(page.getByRole("link", { name: "MCP settings", exact: true }));
  await toggle.waitFor();
  await settled();
  assert.equal(await toggle.getAttribute("aria-checked"), "false");
});

test('header editing keeps keyboard focus on the handle being changed', { skip }, async t => {
  const { page } = await open(t, { url: '/settings/header', ready: '#headerActive .header-tool' });
  const add = page.getByRole('button', { name: 'Add Navigation bar', exact: true });
  const handle = page.getByRole('button', { name: 'Arrange Navigation bar', exact: true });
  await press(add);
  await page.waitForFunction(element => document.activeElement === element, await handle.elementHandle());
  await press(handle);
  await page.locator('#headerToolMenu:popover-open').waitFor();
  await page.keyboard.press('Escape');
  await page.waitForFunction(element => document.activeElement === element, await handle.elementHandle());
  await press(handle);
  await press(page.getByRole('button', { name: 'Move left Navigation bar', exact: true }));
  await page.waitForFunction(element => document.activeElement === element, await handle.elementHandle());
  await press(handle);
  await press(page.getByRole('button', { name: 'Remove Navigation bar', exact: true }));
  await page.waitForFunction(element => document.activeElement === element, await add.elementHandle());
  await press(add);
  await press(handle, 'Alt+ArrowLeft');
  await page.waitForFunction(element => document.activeElement === element, await handle.elementHandle());
  await page.keyboard.press('Delete');
  await page.waitForFunction(element => document.activeElement === element, await add.elementHandle());
  const settings = page.getByRole('button', { name: 'Arrange Settings', exact: true });
  await press(settings, 'Delete');
  await page.waitForFunction(element => document.activeElement === element, await settings.elementHandle());
});

for (const [url, ready] of [
  ["/cards", ".card-library-entry .query-card"],
  ["/library", "#queryTableLibrary .qt-row"],
  ["/analyze", ".usage-dashboard .usage-chart"],
  ["/findings", ".findings-card"],
  ["/settings", "#healthCards .panel:not([aria-busy])"],
  ["/settings/sources", "#sourceGrid .source-card"],
  ["/settings/preferences", "#optionalBehaviors"],
  [`/work/${workID}`, ".work-heading h1"],
  [`/work/${workID}/conversation/conv-root`, ".conversation-panel .turn"],
]) test(`Opening ${url} leaves focus alone and its title is ordinary text`, { skip }, async t => {
  const { page } = await open(t, { url, ready });
  assert.ok(await page.evaluate(() => document.activeElement === document.body), "loading a page should not choose a focused element");
  for (const title of await page.locator(".view.active h1:visible").all()) {
    assert.equal(await title.evaluate(element => { element.focus(); return document.activeElement === element; }), false, "page titles should not accept focus");
    await title.click();
    assert.equal(await title.evaluate(element => document.activeElement === element), false, "clicking a title should leave it unfocused");
  }
});

for (const [drawer, toggle, close] of [
  ["History", "historyToggle", "historyClose"],
  ["Bookmarks", "bookmarksToggle", "bookmarksClose"],
]) for (const width of [1440, 390]) test(`${drawer} close restores reachable focus at ${width}px`, { skip }, async t => {
  const { page } = await open(t, { url: "/library", viewport: { width, height: 900 }, ready: "#queryTableLibrary .qt-row", preferences: { "pharos-header-tools-v1": ["navigator", "settings"] } });
  await activateHeaderTool(page, "navigatorToggle");
  await press(page.locator(`#${toggle}`));
  await press(page.locator(`#${close}`));
  const target = await page.evaluate(id => {
    const opener = document.getElementById(id);
    const anchor = window.headerToolAnchor?.(opener) ?? opener;
    return anchor === document.activeElement && !!anchor.getClientRects().length;
  }, toggle);
  assert.ok(target, "closing the drawer returns focus to its visible opener");
  await press(page.locator(`#${toggle}`));
  const other = page.locator("#navigatorURI");
  await other.focus();
  await page.keyboard.press("Escape");
  assert.equal(await page.locator(`#${close}`).isVisible(), false);
  assert.ok(await other.evaluate(element => element === document.activeElement), "dismissing a drawer should preserve focus chosen outside it");
});

for (const url of ["/", "/home"]) test(`Home at ${url} leaves initial focus to the user`, { skip }, async t => {
  const { page } = await open(t, { url, ready: ".home-belt-row .query-card" });
  assert.ok(await page.evaluate(() => document.activeElement === document.body), "opening Home should not focus a page element");
  const welcome = page.locator(".home-intro h1");
  assert.equal(await welcome.evaluate(element => { element.focus(); return document.activeElement === element; }), false, "the welcome text should not accept programmatic focus");
  await welcome.click();
  assert.equal(await welcome.evaluate(element => document.activeElement === element), false, "clicking the welcome text should not focus it");
  await press(page.getByRole("button", { name: "Search past work", exact: true }));
  await page.waitForFunction(() => document.activeElement?.matches(".home-search-content textarea, .home-search-content input"));
});

test("All Cards moves focus to its content and browser history returns Home", { skip }, async t => {
  const { page, settled, shot } = await open(t, { ready: ".home-belt-row .query-card", preferences: { "pharos-home-card-library-v1": { shown: true } } });
  await press(page.getByRole("button", { name: "All Cards", exact: true }));
  const heading = page.getByRole("heading", { name: "Card Library", exact: true });
  await heading.waitFor();
  await page.waitForFunction(() => document.activeElement?.tagName === "MAIN");
  await settled();
  await shot("card-library");
  await page.goBack();
  const welcome = page.locator(".home-intro h1");
  await welcome.waitFor();
  await settled();
  assert.equal(await welcome.evaluate(element => { element.focus(); return document.activeElement === element; }), false, "browser history leaves the welcome text unfocusable");
  await page.goForward();
  await heading.waitFor();
  await press(page.getByRole("link", { name: "Back to Home", exact: true }));
  await welcome.waitFor();
  await settled();
  assert.equal(await welcome.evaluate(element => { element.focus(); return document.activeElement === element; }), false, "returning Home leaves the welcome text unfocusable");
});

test("the card template editor takes focus, keeps it and returns it on Escape", { skip }, async t => {
  const { page } = await open(t, { url: "/home?cards=library", ready: ".card-library-entry .query-card" });
  const edit = page.locator(".card-library-entry button[aria-label^='Edit card']").first();
  await press(edit);
  await holdsFocus(page, page.locator("dialog.card-template-editor"), edit);
});

test("the findings prompt review takes focus, keeps it and returns it on Escape", { skip }, async t => {
  const { page, settled } = await open(t, { url: "/findings", ready: ".findings-card" });
  const review = page.locator(".findings-cart button.primary").first();
  await press(review);
  const dialog = page.locator('.findings-dialog[role="dialog"]');
  await dialog.waitFor();
  await settled();
  await holdsFocus(page, dialog, review);
});

test("the delegation session sheet on a phone takes focus, keeps it and returns it on Escape", { skip }, async t => {
  const { page } = await open(t, { url: `/work/${workID}/conversation/conv-child`, viewport: { width: 390, height: 844 }, ready: ".conversation-panel .turn" });
  const show = page.locator('button[aria-label="Show workspace sessions"]');
  await press(show);
  const sheet = page.locator('[role="dialog"][aria-label="Workspace sessions"]');
  await sheet.waitFor();
  await holdsFocus(page, sheet, show);
});

for (const moveFocus of [false, true]) test(`conversation navigation ${moveFocus ? "preserves a newer focus choice" : "focuses the requested reader"}`, { skip }, async t => {
  let release;
  const pending = new Promise(resolve => { release = resolve; });
  t.after(() => release());
  const { page, settled } = await open(t, { url: `/work/${workID}/conversation/conv-root`, ready: ".conversation-panel .turn", api: {
    "GET /api/work/:id/conversations/:conversation": async call => {
      if (call.params.conversation === "conv-child") await pending;
      return defaultApi["GET /api/work/:id/conversations/:conversation"](call);
    },
  } });
  const loading = page.waitForRequest(request => new URL(request.url()).pathname.endsWith("/conversations/conv-child"));
  await page.evaluate(url => window.pharosNavigate(url), `/work/${workID}/conversation/conv-child`);
  await loading;
  const control = page.locator("#settingsToggle");
  if (moveFocus) { await control.focus(); assert.ok(await control.evaluate(element => element === document.activeElement)); }
  release();
  await page.getByRole("article", { name: "User message", exact: true }).getByText("Review the change (conv-child)", { exact: true }).waitFor();
  await settled();
  const target = moveFocus ? control : page.locator(".conversation-panel");
  await page.waitForFunction(element => document.activeElement === element, await target.elementHandle());
  await press(page.locator(".workspace-reader-workspace"));
  await page.waitForFunction(() => document.activeElement?.tagName === "MAIN");
  assert.equal(await page.locator(".work-heading h1").evaluate(element => { element.focus(); return element === document.activeElement; }), false, "returning to the overview leaves its title unfocusable");
});

for (const merged of [false, true]) test(`transcript rails follow layout changes in ${merged ? "merged" : "split"} workspaces`, { skip }, async t => {
  const api = merged ? {
    "GET /api/work/:id": call => {
      const work = defaultApi["GET /api/work/:id"](call);
      return { ...work, conversations: work.conversations.slice(0, 1), attempts: [{ attempt_no: 1, result: "Retained archive evidence\n".repeat(40) }] };
    },
    "GET /api/work/:id/delegation": call => {
      const data = defaultApi["GET /api/work/:id/delegation"](call);
      return { ...data, rows: data.rows.slice(0, 1), focus: data.focus.slice(0, 1), session_count: 1, total: 1 };
    },
  } : {};
  const { page, settled } = await open(t, { url: `/work/${workID}/conversation/conv-root`, ready: ".conversation-panel .turn", api, preferences: { "pharos-show-nav-button": true } });
  await activateHeaderTool(page, "navigatorToggle");
  await page.locator("#navigator.open").waitFor();
  await page.evaluate(() => document.querySelector("main").scrollTo(0, 1200));
  await page.locator(".conversation-map.visible").waitFor();
  await settled();
  const tops = await page.evaluate(() => {
    const header = document.querySelector("body > header").getBoundingClientRect().bottom;
    return { header, rails: [...document.querySelectorAll(".conversation-map, .token-map, .conversation-find-rail, .conversation-map-window")].filter(rail => rail.getClientRects().length).map(rail => [rail.className, rail.getBoundingClientRect().top]) };
  });
  assert.ok(tops.rails.length, "the rails are shown");
  for (const [name, top] of tops.rails) assert.ok(top >= tops.header - 1, `${name} starts at ${top}, above the header's bottom at ${tops.header}`);

  // Rail geometry must stay useful after disclosures and a viewport change.
  // A context jump must still reach its event, including a deferred turn.
  await page.getByRole("button", { name: "Close turns", exact: true }).click();
  await page.getByRole("button", { name: "Open turns", exact: true }).click();
  await page.setViewportSize({ width: 1100, height: 800 });
  await settled();
  await page.locator(".token-marker").last().evaluate(marker => marker.click());
  await page.waitForFunction(() => {
    const target = document.querySelector(".token-highlight");
    if (!target) return false;
    const bounds = target.getBoundingClientRect();
    return bounds.height > 0 && bounds.bottom > document.querySelector("body > header").getBoundingClientRect().bottom && bounds.top < innerHeight;
  });
  if (merged) {
    // The overview grows without resizing the transcript or its scrollport.
    // The selected turn and the progress band must still describe the viewport.
    for (const expanded of [true, false]) {
      await page.getByText("Archive details", { exact: true }).click();
      assert.equal(await page.locator(".archive-details").evaluate(details => details.open), expanded);
      await settled();
      await page.locator(".turn").nth(6).evaluate(turn => turn.scrollIntoView({ block: "center", behavior: "instant" }));
      await page.waitForFunction(() => {
        const turns = [...document.querySelectorAll(".conversation-map-turn")];
        const marker = turns[6], band = document.querySelector(".conversation-map-viewport");
        if (!marker.classList.contains("current")) return false;
        const bounds = marker.getBoundingClientRect(), viewport = band.getBoundingClientRect(), middle = (bounds.top + bounds.bottom) / 2;
        return middle >= viewport.top && middle <= viewport.bottom;
      });
    }
  }
});

for (const trigger of ["context jump", "scroll"]) test(`a ${trigger} reaches a paginated event and its child conversation link`, { skip }, async t => {
  const { page, settled, calls } = await open(t, {
    url: `/work/${workID}/conversation/conv-root`, ready: ".conversation-panel .turn",
    api: { "GET /api/work/:id/conversations/:conversation": call => {
      const body = defaultApi["GET /api/work/:id/conversations/:conversation"](call);
      if (call.params.conversation !== "conv-root") return body;
      const event = (id, kind, text, extra = {}) => ({ id, native_id: id, kind, role: "assistant", text: typeof text === "string" ? text : JSON.stringify(text), created_at: "2026-10-01T00:00:00Z", ...extra });
      return { ...body, messages: [
        event("prompt", "message", "Review the changes", { role: "user" }),
        ...Array.from({ length: 52 }, (_, i) => event(`call-${i}`, "tool_call", { name: "Bash", input: { command: `check ${i}` } }, { call_id: `call-${i}` })),
        event("m-15", "delegation", { name: "Task", input: { description: "Review implementation", subagent_type: "Reviewer" } }, { call_id: "child" }),
        event("context", "metadata", { type: "token_count", info: { total_token_usage: { total_tokens: 40000 }, last_token_usage: { input_tokens: 30000, output_tokens: 500 } } }),
      ] };
    } },
  });
  assert.equal(calls.filter(call => call.path.endsWith("/delegation")).length, 1, "the deep link loads delegation analysis once for the selected conversation");
  if (trigger === "scroll") {
    await page.evaluate(() => { const scroller = document.querySelector(".workspace-reader-content"); scroller.scrollTop = scroller.scrollHeight; });
    await page.getByRole("button", { name: /codex.*Open child/ }).waitFor();
  }
  await page.locator(".token-marker").last().evaluate(marker => marker.click());
  await page.waitForFunction(() => {
    const target = document.querySelector(".token-highlight");
    const bounds = target?.getBoundingClientRect();
    return bounds?.height > 0 && bounds.bottom > 0 && bounds.top < innerHeight;
  });
  const child = page.getByRole("button", { name: /codex.*Open child/ });
  await child.click();
  await settled();
  assert.ok(await page.getByRole("article", { name: "User message", exact: true }).getByText("Review the change (conv-child)", { exact: true }).isVisible());
});

// The toolbar repositions itself after a resize; waits for it to settle and
// returns true, or the box that stayed off screen.
const onScreen = locator => locator.evaluate(element => new Promise(resolve => {
  const end = performance.now() + 2000;
  const check = () => {
    const box = element.getBoundingClientRect(), fits = box.left >= 0 && box.top >= 0 && box.right <= innerWidth && box.bottom <= innerHeight;
    if (fits || performance.now() > end) resolve(fits || [box.left, box.top, box.right, box.bottom].map(Math.round).join(", "));
    else requestAnimationFrame(check);
  };
  check();
}));

async function annotate(t) {
  const app = await open(t, { url: "/settings", ready: "#healthCards .panel", preferences: { "pharos-show-agent-annotation-button": true } });
  await activateHeaderTool(app.page, "feedbackToggle");
  await app.page.locator("#feedbackTarget").click();
  await app.page.getByRole("heading", { level: 1 }).click();
  await app.page.locator(".feedback-composer").waitFor();
  return app.page;
}

test("the annotation toolbar and composer stay inside the viewport after a resize", { skip }, async t => {
  const page = await annotate(t);
  await page.setViewportSize({ width: 580, height: 460 });
  for (const surface of [".feedback-toolbar", ".feedback-composer"]) assert.equal(await onScreen(page.locator(surface)), true, `${surface} off screen at 580×460`);
});

test("the expanded feedback panel stays inside a 360px viewport", { skip }, async t => {
  const page = await annotate(t);
  await page.locator("#feedbackNote").fill("Annotation");
  // Adding an annotation expands the toolbar into the feedback panel.
  await page.locator("#feedbackAdd").click();
  await page.locator("#feedbackPanel.open").waitFor();
  await page.setViewportSize({ width: 360, height: 420 });
  const box = await onScreen(page.locator(".feedback-toolbar"));
  assert.equal(box, true, `expanded toolbar at [${box}] in 360×420`);
});

test("feedback review keeps focus, fits a phone, and returns focus to its opener", { skip }, async t => {
  const { page, shot } = await open(t, { url: "/settings", ready: "#healthCards .panel" });
  await activateHeaderTool(page, "feedbackSubmitToggle");
  const dialog = page.getByRole("dialog", { name: "Send feedback", exact: true });
  await dialog.waitFor();
  await page.waitForFunction(() => document.querySelector(".uifeedback-dialog")?.contains(document.activeElement));
  await page.setViewportSize({ width: 360, height: 640 });
  assert.equal(await onScreen(dialog), true);
  await shot("feedback-review");
  await page.keyboard.press("Escape");
  await dialog.waitFor({ state: "hidden" });
  assert.equal(await page.evaluate(() => headerToolAnchor(document.getElementById("feedbackSubmitToggle")) === document.activeElement), true);
});

test("annotated element screenshots render and the reviewed report sends without leaving the page", { skip }, async t => {
  const page = await annotate(t);
  await page.locator("#feedbackNote").fill("Make this heading clearer");
  await page.locator("#feedbackIncludeURL").check();
  await page.getByRole("button", { name: "Take a screenshot of this element", exact: true }).click();
  await page.waitForFunction(() => {
    const img = document.getElementById("feedbackScreenshotPreview");
    return !img.hidden && img.complete && img.naturalWidth > 0;
  });
  if (process.env.PHAROS_UI_SCREENSHOTS) { mkdirSync(process.env.PHAROS_UI_SCREENSHOTS, { recursive: true }); await page.locator("#feedbackScreenshotPreview").screenshot({ path: `${process.env.PHAROS_UI_SCREENSHOTS}/feedback-element-capture.png` }); }
  await page.locator("#feedbackAdd").click();
  await page.locator("#feedbackSend").click();
  const dialog = page.getByRole("dialog", { name: "Send feedback", exact: true });
  await dialog.waitFor();
  await page.waitForFunction(() => document.querySelector(".uifeedback-dialog img")?.naturalWidth > 0);
  await dialog.getByRole("button", { name: "Send feedback", exact: true }).click();
  await page.getByRole("dialog", { name: "Feedback sent", exact: true }).waitFor();
  assert.equal(new URL(page.url()).pathname, "/settings");
});

test('Analyze record keyword editor keeps focus through apply, edit and cancel', { skip }, async t => {
  const { page, settled, shot } = await open(t, { url:'/analyze?analyze_mode=query&analyze_data=tool_calls',ready:'.usage-workspace-table .qt-qb' });
  const opener=page.getByRole('button',{name:/^Add keyword filter for /});
  await press(opener);
  const dialog=page.locator('.explore-search-modal');
  await dialog.waitFor();
  await page.waitForFunction(element=>element.contains(document.activeElement),await dialog.elementHandle());
  await dialog.getByRole('textbox',{name:'Text for rule 1.1',exact:true}).fill('target');
  await shot('keyword-draft');
  await dialog.getByRole('button',{name:'Apply keyword filter',exact:true}).click();
  await dialog.waitFor({state:'hidden'});
  const edit=page.getByRole('button',{name:/^Edit keyword filter for /});
  await edit.waitFor();
  await page.waitForFunction(element=>element===document.activeElement,await edit.elementHandle());
  await settled();
  await press(edit);
  await dialog.waitFor();
  await holdsFocus(page,dialog,edit);
  await shot('keyword-applied');
});

test("Query ideas disclosure supports keyboard toggles and restores the saved preference", { skip }, async t => {
  const app = await open(t, { url: "/analyze?analyze_mode=query&analyze_data=tool_calls", ready: ".usage-workspace-table .qt-row" });
  const { page } = app;
  const summary = page.locator(".usage-query-ideas > summary");
  const chip = page.locator(".usage-query-ideas .query-chip").first();
  await chip.waitFor();
  const saved = page.waitForResponse(response => response.url().endsWith("/api/preferences") && response.request().method() === "POST");
  await summary.focus();
  await page.keyboard.press("Enter");
  await chip.waitFor({ state: "hidden" });
  await saved;
  assert.equal(await summary.evaluate(element => element === document.activeElement), true);
  await page.reload();
  await page.locator(".usage-workspace-table .qt-row").first().waitFor();
  await chip.waitFor({ state: "hidden" });
  await summary.focus();
  await page.keyboard.press("Enter");
  await chip.waitFor();
  assert.equal(await summary.evaluate(element => element === document.activeElement), true);
  await app.settled();
  await app.shot("query-ideas-keyboard");
});

for (const width of [1440, 390, 320]) test(`Capture & index run details keep focus through live refresh and return it on close @ ${width}`, { skip }, async t => {
  const { page, settled } = await open(t, { url: '/settings/sync', viewport: { width, height: 900 }, ready: '#syncHistoryTables .qt-row' });
  const opener = page.getByRole('region', { name: 'Recent run history', exact: true }).getByRole('button', { name: 'Details', exact: true }).first();
  await press(opener);
  const dialog = page.getByRole('dialog', { name: 'Index run details', exact: true });
  await dialog.waitFor();
  await page.evaluate(() => window.pharosSync.refresh());
  await settled();
  await holdsFocus(page, dialog, opener);
});

test('Capture & index query editor retains focus and edits across live refreshes', { skip }, async t => {
  const { page } = await open(t, { url: '/settings/sync', ready: '#syncHistoryTables .qt-row' });
  const history = page.getByRole('region', { name: 'Recent run history', exact: true });
  await history.getByRole('button', { name: 'Expand query builder', exact: true }).click();
  const limit = history.getByRole('spinbutton');
  await limit.fill('2');
  await page.evaluate(() => window.pharosSync.refresh());
  await page.waitForFunction(element => document.activeElement === element, await limit.elementHandle());
  assert.equal(await limit.inputValue(), '2');
  await limit.press('Tab');
  await page.evaluate(() => window.pharosSync.refresh());
  await limit.focus();
  assert.equal(await limit.inputValue(), '2');
});

test('Mac menu editing preserves focus when adding, moving, and removing items', { skip }, async t => {
  const { page, shot } = await open(t, { url: '/settings/layout', ready: '#showMacMenuBar', preferences: { 'pharos-menu-bar': { enabled: false } } });
  const disclosure = page.locator('#menuBarExpand');
  const visibility = page.getByRole('switch', { name: 'macOS menu bar', exact: true });
  const options = page.locator('#menuBarOptions');
  assert.equal(await visibility.getAttribute('aria-checked'), 'false');
  assert.equal(await disclosure.isVisible(), false, 'the off state only shows the visibility toggle');
  await shot('off');
  await press(visibility);
  await disclosure.waitFor();
  assert.ok(await visibility.evaluate(element => document.activeElement === element), 'enabling the menu keeps focus on its toggle');
  assert.equal(await disclosure.getAttribute('aria-expanded'), 'false', 'enabled menu settings start collapsed');
  assert.equal(await options.isVisible(), false);
  await shot('collapsed');
  await press(disclosure);
  await options.waitFor();
  assert.ok(await disclosure.evaluate(element => document.activeElement === element), 'opening the card keeps keyboard focus on the disclosure');
  await shot('expanded');
  await press(disclosure, 'Space');
  await options.waitFor({ state: 'hidden' });
  assert.equal(await visibility.getAttribute('aria-checked'), 'true', 'collapsing the editor keeps the menu enabled');
  await page.keyboard.press('Tab');
  assert.ok(await options.evaluate(element => !element.contains(document.activeElement)), 'Tab skips collapsed menu controls');
  await press(disclosure);
  await options.waitFor();
  const add = page.getByRole('button', { name: 'Add Estimated API cost', exact: true });
  const handle = page.getByRole('button', { name: 'Arrange Estimated API cost', exact: true });
  const focused = async locator => page.waitForFunction(element => document.activeElement === element, await locator.elementHandle());
  await press(add);
  await focused(handle);
  await press(handle);
  await page.locator('#menuBarToolMenu:popover-open').waitFor();
  await page.keyboard.press('Escape');
  await focused(handle);
  await press(handle);
  await press(page.getByRole('button', { name: 'Move up', exact: true }));
  await focused(handle);
  await press(handle, 'Alt+ArrowUp');
  await focused(handle);
  await shot('arranged');
  await page.keyboard.press('Delete');
  await focused(add);
  const status = page.getByRole('button', { name: 'Arrange Pharos status', exact: true });
  await press(status, 'Delete');
  await focused(status);
  const addSeparator = page.getByRole('button', { name: 'Add Separator', exact: true });
  const separators = page.getByRole('button', { name: /^Arrange Separator / });
  const before = await separators.count();
  await press(addSeparator);
  await page.waitForFunction(() => document.activeElement?.getAttribute('aria-label')?.startsWith('Arrange Separator '));
  const added = page.locator('#menuBarActive button:focus');
  const label = await added.getAttribute('aria-label');
  await page.keyboard.press('Alt+ArrowUp');
  await page.waitForFunction(label => document.activeElement?.getAttribute('aria-label') === label, label);
  assert.equal(await separators.count(), before + 1);
  await page.keyboard.press('Delete');
  await focused(addSeparator);
  assert.equal(await separators.count(), before);
  await press(visibility);
  await options.waitFor({ state: 'hidden' });
  assert.equal(await disclosure.isVisible(), false, 'disabling the menu hides its editor control');
  assert.ok(await visibility.evaluate(element => document.activeElement === element), 'disabling the menu keeps its toggle reachable');
  await press(visibility);
  await disclosure.waitFor();
  assert.equal(await disclosure.getAttribute('aria-expanded'), 'false', 'reenabling the menu starts collapsed');
});

for (const width of [390, 320]) test(`Capture & index tables retain horizontal scrolling through status refreshes @ ${width}`, { skip }, async t => {
  const { page, settled } = await open(t, { url: '/settings/sync', viewport: { width, height: 900 }, ready: '#syncHistoryTables .qt-row' });
  await page.getByText('Performance & verification details', { exact: true }).click();
  const labels = ['Recent run history', 'Source status', 'Measured indexing cost'];
  const positions = [];
  for (const label of labels) {
    const wrap = page.getByRole('region', { name: label, exact: true }).locator('.qt-table-wrap');
    await page.waitForFunction(element => element.scrollWidth > element.clientWidth, await wrap.elementHandle());
    positions.push(await wrap.evaluate(element => { element.scrollLeft = 300; return element.scrollLeft; }));
  }
  for (let poll = 0; poll < 2; poll++) {
    await page.evaluate(() => window.pharosSync.refresh());
    await settled();
    for (const [index, label] of labels.entries()) {
      assert.equal(await page.getByRole('region', { name: label, exact: true }).locator('.qt-table-wrap').evaluate(element => element.scrollLeft), positions[index], `${label} scroll after poll ${poll + 1}`);
    }
  }
});

for (const width of [1440, 390]) test(`Capture & index saved query remains available after a reload @ ${width}`, { skip }, async t => {
  const { page } = await open(t, { url: '/settings/sync', viewport: { width, height: 900 }, ready: '#syncHistoryTables .qt-row' });
  const history = page.getByRole('region', { name: 'Recent run history', exact: true });
  await history.getByRole('button', { name: 'Save query', exact: true }).click();
  const name = history.getByRole('textbox');
  await name.fill('Run history to revisit');
  const persisted = page.waitForResponse(response => response.url().endsWith('/api/preferences') && response.request().method() === 'POST' && response.request().postDataJSON()?.set?.['query-table:saved:sync_history']);
  await name.press('Enter');
  await persisted;
  await history.getByRole('button', { name: 'Run history to revisit', exact: true }).waitFor();
  await page.reload();
  await page.locator('#syncHistoryTables .qt-row').first().waitFor();
  await history.getByRole('button', { name: /^Saved/ }).click();
  const saved = page.getByRole('dialog', { name: 'Saved queries', exact: true });
  await saved.getByRole('button', { name: /^Run history to revisit/ }).click();
  await saved.waitFor({ state: 'hidden' });
  await history.getByRole('button', { name: 'Run history to revisit', exact: true }).waitFor();
});

test('Ask Pharos invitation gives focus back to existing search when dismissed', {skip}, async t => {
  const {page, settled} = await open(t, {url:'/', ready:'#pharos-ask .pharos-invitation:not([hidden])', api:{
    'GET /api/ask/status':{settings:{enabled:false,showInvitation:true},providers:[{id:'Codex',models:[]}]},
  }});
  await page.getByRole('button',{name:'Try Ask Pharos',exact:true}).click();
  const modal=page.getByRole('dialog',{name:'Ask in your own words',exact:true});
  assert.ok(await modal.evaluate(element=>element.contains(document.activeElement)));
  await page.keyboard.press('Escape');await settled();
  assert.ok(await page.getByRole('button',{name:'Search past work',exact:true}).evaluate(element=>element===document.activeElement));
});

for(const modal of [false,true]) for(const width of [1440,390,320]) test(`Ask Pharos model picker overlays content without expanding ${modal?'the dialog':'Home'} @ ${width}`,{skip},async t=>{
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  const {page,settled,shot}=await open(t,{url:'/',viewport:{width,height:900},ready:'#pharos-ask .pharos-ai:not([hidden])',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true},...Array.from({length:30},(_,index)=>({id:'model-'+index,label:'Model '+index,efforts:['Default']}))]},
    'GET /api/ask/sessions':{sessions:[]},
  }});
  if(modal)await activateHeaderTool(page,'askPharosToggle');
  const trigger=page.locator('.pharos-model-trigger'),product=page.locator('.pharos-product');
  await trigger.scrollIntoViewIfNeeded();await settled();
  const height=(await product.boundingBox()).height;
  await trigger.click();
  const picker=page.getByRole('dialog',{name:'Model selection',exact:true});
  const check=async()=>{
    assert.equal((await product.boundingBox()).height,height,'opening or resizing the picker must not expand its container');
    assert.ok(await picker.evaluate(element=>{
      const box=element.getBoundingClientRect();
      return box.left>=0&&box.right<=innerWidth&&box.top>=0&&box.bottom<=innerHeight&&element.contains(document.elementFromPoint(box.x+box.width/2,box.y+box.height/2));
    }),'the picker must stay on screen above other content');
  };
  await check();await shot('model-picker-overlay');
  await picker.getByRole('button',{name:/^Model /}).click();
  await check();
  if(!modal){
    await page.locator('main').evaluate(element=>{element.scrollTop+=innerHeight;});
    await settled();await check();
  }
  await picker.getByRole('button',{name:'Model 29',exact:true}).click();
  await check();
  await page.keyboard.press('Escape');
  assert.ok(await picker.isHidden());
  assert.ok(await trigger.evaluate(element=>element===document.activeElement),'Escape returns focus to the model control');
  await trigger.click();
  await page.getByRole('textbox',{name:'Ask a question',exact:true}).click();
  assert.ok(await picker.isHidden(),'clicking outside dismisses the picker');
  await trigger.click();
  await page.setViewportSize({width,height:400});await settled();
  await check();await shot('model-picker-short-viewport');
  // At this height the popover can cover the editor; Escape must still dismiss it.
  await page.keyboard.press('Escape');
  assert.ok(await picker.isHidden());
});

for (const theme of ['dark','light']) for (const width of [1440,390,320]) test(`Ask Pharos keeps long chats scrollable and the editor reachable (${theme}) @ ${width}`, {skip}, async t => {
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  const turn=index=>({id:'turn-'+index,prompt:'Question '+index,cli:'Codex',model:'test-model',modelLabel:'Test Model',effort:'Low',status:'multiple',message:'',elapsedMs:1000,reply:index===5?'The newest reply.':'Earlier context. '.repeat(100),cards:[]});
  let session={id:'long-chat',createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',conversation:Array.from({length:6},(_,index)=>turn(index))};
  let finish;
  let reply=new Promise(resolve=>{finish=resolve;});t.after(()=>finish());
  const {page,settled,shot,calls}=await open(t,{url:'/?ask_chat=long-chat',viewport:{width,height:900},preferences:{'pharos-theme':theme},ready:'#pharos-ask .pharos-turn',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/session':()=>session,
    'GET /api/ask/sessions':()=>({sessions:[session]}),
    'POST /api/ask/session':({body})=>(session={...session,draft:body.draft}),
    'POST /api/ask/cancel':{ok:true},
    'POST /api/ask/send':async({body})=>{
      await reply;
      return session={...session,draft:'',conversation:[...session.conversation,{...turn(6),id:body.turnID,prompt:body.prompt,reply:'A completed reply while you read the earlier messages.'}]};
    },
  }});
  const viewport=page.getByRole('region',{name:'Chat messages, scroll for earlier replies'});
  const atBottom=element=>element.scrollHeight-element.clientHeight-element.scrollTop<2;
  await page.waitForFunction(atBottom,await viewport.elementHandle());
  assert.ok(await viewport.evaluate(element=>element.scrollHeight>element.clientHeight),'earlier messages must be scrollable');
  const fits=element=>{const box=element.getBoundingClientRect();return box.top>=0&&box.bottom<=innerHeight;};
  assert.ok(await page.getByRole('textbox',{name:'Ask a question',exact:true}).evaluate(fits),'editor must stay on screen');
  assert.ok(await page.locator('.pharos-user-question').last().evaluate(fits),'latest prompt should be visible with a short reply');
  assert.ok(await page.getByRole('button',{name:'Send question',exact:true}).evaluate(fits),'send must stay on screen');
  const clear=page.locator('.pharos-ai').getByRole('button',{name:'Clear',exact:true});
  assert.ok(await clear.evaluate(button=>{
    const transcript=document.querySelector('.pharos-thread-viewport'),before=transcript.getBoundingClientRect();
    button.hidden=true;
    const after=transcript.getBoundingClientRect();
    button.hidden=false;
    return before.top===after.top&&before.height===after.height;
  }),'Clear overlays the conversation without taking up vertical space');
  const reachable=element=>{const box=element.getBoundingClientRect(),target=document.elementFromPoint(box.x+box.width/2,box.y+box.height/2);return element===target||element.contains(target);};
  assert.ok(await clear.evaluate(reachable),'Clear must be clickable above the conversation');
  await shot('latest-exchange');

  await page.getByRole('textbox',{name:'Ask a question',exact:true}).fill('A follow-up question');
  await page.getByRole('button',{name:'Send question',exact:true}).click();
  await page.getByRole('region',{name:'AI request in progress',exact:true}).waitFor();
  const stop=page.getByRole('button',{name:'Stop',exact:true});
  assert.ok(await stop.isEnabled(),'an empty draft must still allow stopping the request');
  assert.ok(await stop.evaluate(fits),'Stop must stay on screen while the conversation scrolls');
  await shot('processing-stop');
  const editor=page.getByRole('textbox',{name:'Ask a question',exact:true});
  await editor.fill('A preserved follow-up');
  await editor.press('Enter');
  assert.equal(calls.filter(call=>call.path==='/api/ask/cancel').length,0,'typing Enter in a follow-up must not stop the current request');
  await stop.focus();await page.keyboard.press('Enter');
  const send=page.getByRole('button',{name:'Send question',exact:true});await send.waitFor();
  await page.getByRole('button',{name:'Try again',exact:true}).waitFor();
  await page.waitForFunction(element=>element===document.activeElement,await send.elementHandle());
  assert.ok(await send.isEnabled(),'a preserved follow-up can be sent after stopping');
  assert.equal(await editor.evaluate(element=>element.value),'A preserved follow-up');
  await shot('stopped-preserving-draft');
  finish();await settled();
  assert.equal(calls.filter(call=>call.path==='/api/ask/cancel').length,1,'Stop sends one cancellation');
  reply=new Promise(resolve=>{finish=resolve;});
  await send.click();
  await page.getByRole('region',{name:'AI request in progress',exact:true}).waitFor();
  await viewport.evaluate(element=>{element.scrollTop=0;});
  await page.getByRole('button',{name:'Latest reply',exact:true}).waitFor();
  assert.ok(await clear.evaluate(reachable),'scrolling earlier replies must not cover Clear');
  finish();
  await settled();
  await page.getByRole('status').filter({hasText:'A completed reply while you read the earlier messages.'}).waitFor({state:'attached'});
  assert.equal(await viewport.evaluate(element=>element.scrollTop),0,'a reply must not pull the reader away from earlier messages');
  await page.getByRole('button',{name:'Latest reply',exact:true}).click();
  await page.waitForFunction(atBottom,await viewport.elementHandle());
  await shot('returned-to-latest');

  await page.setViewportSize({width,height:550});
  await settled();
  assert.ok(await page.getByRole('button',{name:'Send question',exact:true}).evaluate(fits),'send must remain reachable after resizing');
  await viewport.focus();
  await page.keyboard.press('Home');
  await page.keyboard.press('Tab');
  assert.ok(await page.getByRole('textbox',{name:'Ask a question',exact:true}).evaluate(element=>element===document.activeElement)||await page.getByRole('button',{name:'Latest reply',exact:true}).evaluate(element=>element===document.activeElement),'keyboard focus can leave the scrollable conversation');
  await editor.fill('An unsent follow-up');
  await clear.click();
  assert.equal(await editor.evaluate(element=>element.value),'');
  assert.ok(await editor.evaluate(element=>element===document.activeElement),'Clear returns focus to the fresh composer');
  assert.ok(await page.locator('.pharos-model-trigger').isEnabled(),'Clear starts a new chat with an unlocked model selection');
});

for(const [status,width,theme,longTranscript] of [['error',1440,'dark'],['error',390,'light'],['error',320,'dark'],['canceled',390,'dark'],['error',1440,'light',true]]) test(`Ask Pharos retry with another model focuses the picker (${status}, ${theme}${longTranscript?', long transcript':''}) @ ${width}`,{skip},async t=>{
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  const turn={id:'first-turn',prompt:'Which commands keep failing?',cli:'Codex',model:'test-model',modelLabel:'Test Model',effort:'Low',status:'clarification',reply:'',clarification:{question:'Which project?',options:['Pharos','All projects']},cards:[],elapsedMs:1000};
  const original={id:'failed-chat',createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'Keep the time range to this week.',conversation:[turn,{...turn,id:'failed-turn',prompt:'Pharos',status,clarification:null,reply:status==='error'?'The agent returned an empty response; try again':'Request canceled.'}]};
  if(longTranscript)original.conversation.splice(0,1,{...turn,id:'older-turn',prompt:'Older question',status:'multiple',clarification:null,reply:'A'.repeat(8000)},{...turn,status:'multiple',clarification:null,reply:'B'.repeat(8000)});
  const sessions=new Map([[original.id,original]]);
  const {page,settled,shot,calls}=await open(t,{url:'/?ask_chat=failed-chat',viewport:{width,height:900},preferences:{'pharos-theme':theme},ready:'.pharos-recovery:not([hidden])',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true},{id:'second-model',label:'Second Model',efforts:['Default','Low']}]},
    'GET /api/ask/session':({query})=>sessions.get(query.get('id')),
    'GET /api/ask/sessions':()=>({sessions:[...sessions.values()]}),
    'POST /api/ask/session':({body})=>{
      assert.ok(Buffer.byteLength(body.draft||'','utf8')<=12000,'saved drafts must satisfy the server byte limit');
      const session={...(sessions.get(body.id)||{id:body.id,createdAt:Date.now(),updatedAt:Date.now(),conversation:[]}),draft:body.draft,selection:body.selection};
      sessions.set(body.id,session);return session;
    },
    'POST /api/ask/send':({body})=>{
      assert.ok(Buffer.byteLength(body.prompt,'utf8')<=12000,'sent questions must satisfy the server byte limit');
      const session={...sessions.get(body.id),draft:'',conversation:[{...turn,id:body.turnID,prompt:body.prompt,status:'multiple',clarification:null,reply:'Your retry completed.'}]};
      sessions.set(body.id,session);return session;
    },
  }});
  await shot('failed-chat');
  const question=page.getByRole('textbox',{name:'Ask a question',exact:true});
  const clear=page.locator('.pharos-recovery').getByRole('button',{name:'Clear',exact:true});
  await clear.focus();await page.keyboard.press('Enter');
  assert.ok(await question.evaluate(element=>element===document.activeElement),'clearing a failure returns focus to the composer');
  assert.equal(await question.evaluate(element=>element.value),'');
  assert.ok(await page.getByRole('log',{name:'Conversation',exact:true}).isHidden(),'clearing hides the prior conversation');
  assert.ok(await page.locator('.pharos-model-trigger').isEnabled(),'a cleared chat allows choosing another model');
  assert.equal(new URL(page.url()).searchParams.get('ask_chat'),null);
  await shot('cleared-chat');
  await page.goBack();await clear.waitFor();await settled();
  assert.equal(await question.evaluate(element=>element.value),original.draft,'the failed chat remains available with its prior draft');
  const retry=page.getByRole('button',{name:'Try Again with a different model',exact:true});
  await retry.focus();await page.keyboard.press('Enter');
  const picker=page.getByRole('dialog',{name:'Model selection',exact:true});
  assert.ok(await picker.evaluate(element=>element.contains(document.activeElement)),'retry moves keyboard focus into the model picker');
  const prior=longTranscript?'[Earlier conversation omitted or shortened to fit.]\n\nYou:\nWhich commands keep failing?\n\nPharos:\n'+'B'.repeat(8000):'You:\nWhich commands keep failing?\n\nPharos:\nWhich project?\n\n- Pharos\n\n- All projects';
  const expected=prior+'\n\nYou:\nPharos\n\nPharos ('+status+'):\n'+original.conversation.at(-1).reply+'\n\nYou (unsent draft):\nKeep the time range to this week.';
  assert.equal(await question.evaluate(element=>element.value),expected);
  const newID=new URL(page.url()).searchParams.get('ask_chat');
  assert.ok(newID&&newID!==original.id,'retry has its own draft chat');
  assert.equal(calls.filter(call=>call.path==='/api/ask/send').length,0,'opening the picker must not send a request');
  const fits=element=>{const box=element.getBoundingClientRect();return box.left>=0&&box.right<=innerWidth&&box.top>=0&&box.bottom<=innerHeight;};
  assert.ok(await picker.evaluate(fits),'the picker stays on screen');
  assert.ok(await question.evaluate(fits),'restored messages remain reachable');
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'recovery actions must wrap on narrow screens');
  await shot('restored-draft-and-picker');
  const saved=page.waitForResponse(response=>response.url().includes('/api/ask/session')&&response.request().method()==='POST'&&response.request().postDataJSON().selection.model==='second-model');
  await picker.getByRole('button',{name:/^Model /}).click();
  await picker.getByRole('button',{name:'Second Model',exact:true}).click();
  await page.keyboard.press('Escape');await saved;await settled();
  assert.ok(await page.locator('.pharos-model-trigger').evaluate(element=>element===document.activeElement),'Escape returns focus to the model control');
  assert.deepEqual(sessions.get(newID).selection,{...selection,model:'second-model'});
  assert.equal(sessions.get(newID).draft,expected);
  assert.deepEqual(sessions.get(original.id).conversation,original.conversation,'the prior chat remains in history');
  await page.reload();await settled();
  await page.waitForFunction(text=>document.querySelector('#pharos-question')?.value===text,expected);
  assert.ok(await page.getByRole('button',{name:'Send question',exact:true}).isEnabled());
  if(longTranscript){
    await page.getByRole('button',{name:'Send question',exact:true}).click();
    await page.getByRole('status').filter({hasText:'Your retry completed.'}).waitFor();
    await settled();
    assert.equal(sessions.get(newID).conversation[0].prompt,expected,'the restored transcript can be sent after reload');
    await shot('sent-trimmed-transcript');
  }
});

for (const moveFocus of [false, true]) test(`opening a saved chat ${moveFocus ? "preserves a newer focus choice" : "focuses its composer"}`, { skip }, async t => {
  const selection = { provider: 'Codex', model: 'test-model', effort: 'Low' };
  const session = { id: 'focus-chat', createdAt: Date.now(), updatedAt: Date.now(), selection, draft: '', conversation: [{ id: 'prior-turn', prompt: 'A prior question', cli: 'Codex', model: 'test-model', modelLabel: 'Test Model', effort: 'Low', status: 'multiple', message: '', elapsedMs: 1000, reply: 'A saved reply', cards: [] }] };
  let release;
  const pending = new Promise(resolve => { release = resolve; });
  t.after(() => release());
  const { page, settled } = await open(t, { ready: '#pharos-ask .pharos-ai:not([hidden])', api: {
    'GET /api/ask/status': { settings: { enabled: true, showInvitation: true, selection }, providers: [{ id: 'Codex' }] },
    'GET /api/ask/models': { id: 'Codex', models: [{ id: 'test-model', label: 'Test Model', efforts: ['Default', 'Low'], default: true }] },
    'GET /api/ask/sessions': { sessions: [session] },
    'GET /api/ask/session': async () => { await pending; return session; },
  } });
  await press(page.getByRole('button', { name: 'History', exact: true }));
  const loading = page.waitForRequest(request => new URL(request.url()).pathname === '/api/ask/session');
  await press(page.getByRole('link', { name: 'Open chat: A prior question', exact: true }));
  await loading;
  const control = page.locator('#settingsToggle');
  if (moveFocus) { await control.focus(); assert.ok(await control.evaluate(element => element === document.activeElement)); }
  release();
  await page.getByRole('status').filter({ hasText: 'A saved reply' }).waitFor();
  await settled();
  const target = moveFocus ? control : page.getByRole('textbox', { name: 'Ask a question', exact: true });
  await page.waitForFunction(element => document.activeElement === element, await target.elementHandle());
});

test('Ask Pharos chat links survive reload, new tabs and browser Back/Forward', {skip,timeout:60000}, async t => {
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  const saved={id:'saved-chat',createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',conversation:[{id:'saved-turn',prompt:'A prior question',cli:'Codex',model:'test-model',modelLabel:'Test Model',effort:'Low',status:'multiple',message:'',elapsedMs:1000,reply:'An earlier reply',cards:[]}]};
  saved.conversation.push({...saved.conversation[0],id:'later-turn',prompt:'A later topic',reply:'A later reply'});
  const sessions=new Map([[saved.id,saved]]);
  let delaySession=false,releaseSession;
  const delayedSession=new Promise(resolve=>{releaseSession=resolve;});
  t.after(()=>releaseSession());
  const {page,settled,calls}=await open(t,{url:'/',ready:'#pharos-ask .pharos-ai:not([hidden])',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'},{id:'Claude'}]},
    'GET /api/ask/models':({query})=>query.get('provider')==='Claude'
      ? {id:'Claude',models:[{id:'claude-first',label:'Claude First',efforts:['Default','Medium'],default:true},{id:'claude-second',label:'Claude Second',efforts:['Default','Low']}],defaultSelection:{provider:'Claude',model:'claude-first',effort:'Medium'}}
      : {id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true},{id:'second-model',label:'Second Model',efforts:['Default','Low']}]},
    'GET /api/ask/session':async({query})=>{if(delaySession&&query.get('id')==='saved-chat')await delayedSession;return sessions.get(query.get('id'));},
    'GET /api/ask/sessions':({query})=>({sessions:[...sessions.values()].filter(session=>JSON.stringify(session).toLowerCase().includes((query.get('q')||'').toLowerCase())).map(session=>({...session,conversation:session.conversation.slice(0,1)}))}),
    'POST /api/ask/session':({body})=>{
      const session={...(sessions.get(body.id)||{...saved,id:body.id,conversation:[]}),draft:body.draft,selection:body.selection};
      sessions.set(body.id,session);return session;
    },
    'POST /api/ask/send':({body})=>{
      const session={...sessions.get(body.id),draft:'',conversation:[{...saved.conversation[0],id:body.turnID,prompt:body.prompt,reply:'Your latest reply',cli:body.selection.provider,model:body.selection.model,effort:body.selection.effort}]};
      sessions.set(body.id,session);return session;
    },
  }});
  assert.ok(calls.some(call=>call.path==='/api/ask/visits'&&call.body.reconcile),'opening Ask reconciles the authoritative browser history');
  const previousVisit=await page.evaluate(()=>JSON.parse(localStorage.getItem('pharos-history-v1'))[0]);
  const replaced=page.waitForResponse(response=>response.url().endsWith('/api/ask/visits')&&response.request().method()==='POST'&&response.request().postDataJSON().replaces?.at===previousVisit.at);
  await page.evaluate(()=>window.pharosNameVisit('Updated page title'));
  const replacement=(await replaced).request().postDataJSON();
  assert.equal(replacement.replaces.url,previousVisit.url,'the private store receives the identity removed from browser history');
  assert.equal(replacement.visits.length,1);
  await settled();
  const question=page.getByRole('textbox',{name:'Ask a question',exact:true});
  const draftSaved=page.waitForResponse(response=>response.url().includes('/api/ask/session')&&response.request().method()==='POST');
  await question.fill('Keep this question');await draftSaved;
  // This checks persisted chat restoration, so finish background card loads
  // before the deliberate reload cancels the old document's requests.
  await settled();
  const chatURL=page.url(),id=new URL(chatURL).searchParams.get('ask_chat');
  assert.ok(id,'a new draft gets a conversation URL');
  await page.reload();await settled();
  await page.waitForFunction(()=>document.querySelector('#pharos-question')?.value==='Keep this question');
  assert.equal(await question.evaluate(element=>element.value),'Keep this question');
  assert.ok(await page.evaluate(() => document.activeElement === document.body), 'restoring a chat should leave initial focus alone');
  const choose=async(kind,value,expected)=>{
    const persisted=page.waitForResponse(response=>response.url().includes('/api/ask/session')&&response.request().method()==='POST'&&Object.entries(expected).every(([key,value])=>response.request().postDataJSON().selection[key]===value));
    await page.locator('.pharos-model-trigger').click();
    const menu=page.getByRole('dialog',{name:'Model selection',exact:true});
    await menu.getByRole('button',{name:new RegExp('^'+kind+' ')}).click();
    await menu.getByRole('button',{name:value,exact:true}).click();
    await page.keyboard.press('Escape');await persisted;await settled();
    await page.reload();await settled();
    await page.waitForFunction(()=>document.querySelector('#pharos-question')?.value==='Keep this question');
    assert.deepEqual(sessions.get(id).selection,expected);
    assert.ok((await page.locator('.pharos-model-trigger').textContent()).includes(value),'the saved draft should restore the chosen picker value');
  };
  await choose('Model','Second Model',{...selection,model:'second-model'});
  await choose('Effort','Default',{...selection,model:'second-model',effort:'Default'});
  await choose('CLI','Claude',{provider:'Claude',model:'claude-first',effort:'Medium'});
  await choose('Model','Claude Second',{provider:'Claude',model:'claude-second',effort:'Default'});
  await choose('Effort','Low',{provider:'Claude',model:'claude-second',effort:'Low'});
  await page.getByRole('button',{name:'Send question',exact:true}).click();
  await page.getByRole('status').filter({hasText:'Your latest reply'}).waitFor();
  assert.equal(new URL(page.url()).searchParams.get('ask_chat'),id);
  await page.getByRole('button',{name:'Clear',exact:true}).click();
  assert.equal(new URL(page.url()).searchParams.get('ask_chat'),null);
  await page.goBack();await page.getByRole('status').filter({hasText:'Your latest reply'}).waitFor();
  assert.equal(new URL(page.url()).searchParams.get('ask_chat'),id);
  await page.goForward();await settled();
  await page.getByRole('button',{name:'History',exact:true}).click();
  const search=page.getByRole('searchbox',{name:'Find a prior chat',exact:true});
  const matches=page.waitForResponse(response=>response.url().includes('/api/ask/sessions?')&&new URL(response.url()).searchParams.get('q')==='later topic');
  await search.fill('later topic');await matches;await settled();
  const link=page.getByRole('link',{name:'Open chat: A prior question',exact:true});
  await link.waitFor();
  const savedURL=new URL(await link.getAttribute('href'),page.url()).href;
  const anotherTab=await page.context().newPage();
  await anotherTab.goto(savedURL);await anotherTab.getByRole('status').filter({hasText:'An earlier reply'}).waitFor();
  assert.equal(new URL(anotherTab.url()).searchParams.get('ask_chat'),'saved-chat');
  await anotherTab.close();
  await link.click();await page.getByRole('status').filter({hasText:'An earlier reply'}).waitFor();
  assert.equal(new URL(page.url()).searchParams.get('ask_chat'),'saved-chat');
  // Back can arrive before a slow chat load. Its late response must not
  // replace the page the user has already returned to.
  await page.getByRole('button',{name:'Clear',exact:true}).click();
  delaySession=true;
  const loading=page.waitForRequest(request=>new URL(request.url()).pathname==='/api/ask/session'&&request.method()==='GET');
  await page.evaluate(()=>window.pharosNavigate('/?ask_chat=saved-chat'));await loading;
  await page.goBack();
  await page.waitForFunction(()=>!document.querySelector('#pharos-question')?.disabled);
  releaseSession();await settled();
  assert.equal(new URL(page.url()).searchParams.get('ask_chat'),null);
  assert.equal(await page.getByRole('status').filter({hasText:'An earlier reply'}).count(),0);
  // A history link from another page must also restore the chat when copied
  // into a new tab, while an ordinary click keeps the chat in the overlay.
  await page.evaluate(()=>window.pharosNavigate('/analyze?analyze_topic=tokens'));await settled();
  const contextURL=page.url();
  await activateHeaderTool(page,'askPharosToggle');
  await page.getByRole('button',{name:'History',exact:true}).click();
  await link.waitFor();
  const modalChatURL=new URL(await link.getAttribute('href'),page.url()).href;
  const modalTab=await page.context().newPage();
  try{
    await modalTab.goto(modalChatURL);
    await modalTab.getByRole('status').filter({hasText:'An earlier reply'}).waitFor();
    assert.equal(new URL(modalTab.url()).searchParams.get('ask_chat'),'saved-chat');
  }finally{await modalTab.close();}
  await link.click();
  await page.getByRole('dialog',{name:'Ask Pharos',exact:true}).getByRole('status').filter({hasText:'An earlier reply'}).waitFor();
  assert.equal(page.url(),contextURL,'opening a chat in the overlay must preserve the page URI');
});

for(const url of ['/', '/analyze?analyze_topic=tokens']) test(`Ask Pharos restores its open and floating surfaces after reload (${url==='/'?'Home':'Analyze'})`,{skip},async t=>{
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  let session=null;
  const {page,settled}=await open(t,{url,ready:'#askPharosToggle:not([disabled])',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/sessions':()=>({sessions:session?[session]:[]}),
    'GET /api/ask/session':()=>session,
    'POST /api/ask/session':({body})=>session={createdAt:Date.now(),updatedAt:Date.now(),conversation:[],...body},
  }});
  const modal=page.getByRole('dialog',{name:'Ask Pharos',exact:true});
  const question=page.getByRole('textbox',{name:'Ask a question',exact:true});
  const panel=page.getByRole('complementary',{name:'Ask Pharos progress'});
  const pageURL=page.url();
  const reload=async()=>{await settled();await page.waitForLoadState('networkidle');await page.reload();await settled();};
  await activateHeaderTool(page,'askPharosToggle');
  await reload();
  await modal.waitFor();
  assert.ok(await question.evaluate(element=>element===document.activeElement),'the restored modal focuses its editor');
  const saved=page.waitForResponse(response=>response.url().includes('/api/ask/session')&&response.request().method()==='POST');
  await question.evaluate(element=>{element.value='Keep this open question';element.dispatchEvent(new Event('input',{bubbles:true}));});await saved;await settled();
  await question.getByRole('group',{name:/^About/}).focus();await page.keyboard.press('Alt+ArrowRight');
  const inlineDraft=await question.innerText();await question.focus();
  await reload();
  await modal.waitFor();
  await page.waitForFunction(()=>document.querySelector('#pharos-question')?.value==='Keep this open question');
  assert.equal(await question.evaluate(element=>element.value),'Keep this open question');
  assert.equal(await question.innerText(),inlineDraft,'reload preserves the context chip position within the draft');
  assert.equal(page.url(),pageURL,'restoring the overlay keeps the underlying page URI');
  await page.getByRole('button',{name:'Float Ask Pharos',exact:true}).click();
  const handle=panel.getByRole('button',{name:'Move Ask Pharos progress'});
  await handle.focus();await page.keyboard.press('ArrowLeft');
  await reload();
  await panel.waitFor();
  assert.equal(page.url(),pageURL,'restoring floating progress keeps the underlying page URI');
  await panel.getByRole('button',{name:'Dismiss Ask Pharos progress',exact:true}).click();
  await reload();
  assert.equal(await panel.isVisible(),false,'dismissed progress stays dismissed');
  assert.equal(await modal.isVisible(),false);
  await activateHeaderTool(page,'askPharosToggle');
  assert.equal(await question.evaluate(element=>element.value),'Keep this open question');
  await page.getByRole('button',{name:'Float Ask Pharos',exact:true}).click();
  await panel.getByRole('button',{name:/^Ask Pharos/}).click();
  assert.equal(await question.evaluate(element=>element.value),'Keep this open question');
  await page.getByRole('button',{name:'Close Ask Pharos',exact:true}).click();
  await reload();
  assert.equal(await modal.isVisible(),false,'a closed overlay stays closed');
  await page.waitForFunction(()=>document.querySelector('#pharos-question')?.value==='Keep this open question');
  if(url==='/'){
    await page.locator('#homeAskSearch').getByRole('textbox',{name:'Ask a question',exact:true}).waitFor();
    assert.equal(await question.evaluate(element=>element.value),'Keep this open question','Home still owns the composer after reload');
  }else{
    await page.locator('body>header .brand').click();
    await page.locator('#homeAskSearch').getByRole('textbox',{name:'Ask a question',exact:true}).waitFor();
    assert.equal(await question.evaluate(element=>element.value),'Keep this open question','returning Home reattaches the restored composer');
  }
});

for(const theme of ['dark','light']) for(const mode of ['choice','other','composer']) test(`Ask Pharos clarification supports ${mode} replies and keeps focus usable (${theme})`,{skip},async t=>{
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  const clarification={question:'By “use”, do you mean model tokens, API cost equivalent, or something else?',options:['Model tokens','API cost equivalent']};
  let session={id:'clarification-chat',createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',clarificationDraft:'',conversation:[{id:'clarify-turn',prompt:'Show me my use',cli:'Codex',model:'test-model',modelLabel:'Test Model',effort:'Low',status:'clarification',reply:'',message:'',elapsedMs:1000,cards:[],clarification}]};
  const submitted=[];
  const {page,settled,shot}=await open(t,{url:'/?ask_chat=clarification-chat',viewport:{width:mode==='choice'?1440:mode==='other'?390:320,height:900},preferences:{'pharos-theme':theme},ready:'.pharos-clarification:not([hidden])',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/session':()=>session,
    'GET /api/ask/sessions':()=>({sessions:[session]}),
    'POST /api/ask/session':({body})=>(session={...session,draft:body.draft,clarificationDraft:body.clarificationDraft||''}),
    'POST /api/ask/send':({body})=>{
      submitted.push(body);
      return session={...session,draft:body.draft||'',clarificationDraft:'',conversation:[...session.conversation,{id:body.turnID,prompt:body.prompt,cli:'Codex',model:'test-model',modelLabel:'Test Model',effort:'Low',status:'multiple',message:'',elapsedMs:1000,reply:'Here are the relevant views.',cards:[{title:'Usage details',kind:'Analyze',description:'Explore the requested measure.',href:'/analyze'},{title:'Matching work',kind:'Search',description:'See the underlying conversations.',href:'/library'}]}]};
    },
  }});
  const question=page.getByRole('textbox',{name:'Ask a question',exact:true}),other=page.getByRole('textbox',{name:'Other answer',exact:true});
  const sendOther=page.getByRole('button',{name:'Send other answer',exact:true});
  assert.ok(await sendOther.isDisabled());
  assert.ok(await page.getByRole('button',{name:'A. Model tokens',exact:true}).isEnabled());
  await shot('clarification');
  let expected='API cost equivalent';
  if(mode==='choice'){
    await question.fill('An unrelated next question');
    await page.getByRole('button',{name:'B. API cost equivalent',exact:true}).focus();
    await page.keyboard.press('Enter');
  }else if(mode==='other'){
    await question.fill('An unrelated next question');
    const saved=page.waitForResponse(response=>response.url().includes('/api/ask/session')&&response.request().method()==='POST'&&response.request().postDataJSON().clarificationDraft==='Workspace counts');
    await other.fill('Workspace counts');await saved;
    await settled();
    await page.reload();await settled();
    await page.waitForFunction(()=>document.querySelector('.pharos-clarification-other textarea')?.value==='Workspace counts');
    await other.focus();await page.keyboard.press('Shift+Enter');await page.keyboard.insertText('across all projects');
    expected='Workspace counts\nacross all projects';
    await page.keyboard.press('Enter');
  }else{
    expected='Workspace counts';await question.fill(expected);await page.getByRole('button',{name:'Send question',exact:true}).click();
  }
  await page.getByRole('link',{name:/Usage details/}).waitFor();await settled();
  assert.equal(submitted.length,1,'an answer should submit exactly one request');
  assert.equal(submitted[0].prompt,expected);
  assert.equal(new URL(page.url()).searchParams.get('ask_chat'),'clarification-chat');
  if(mode!=='composer')assert.equal(await question.evaluate(element=>element.value),'An unrelated next question');
  if(mode!=='composer')assert.ok(await question.evaluate(element=>element===document.activeElement),'answering should return keyboard focus to the composer');
  assert.ok(await page.getByRole('button',{name:'A. Model tokens',exact:true}).isDisabled(),'older clarification options must not be submitted again');
  const footer=page.getByRole('button',{name:'Send question',exact:true});
  assert.ok(await footer.evaluate(element=>{const box=element.getBoundingClientRect();return box.top>=0&&box.bottom<=innerHeight;}));
  await question.focus();
  assert.ok(await question.evaluate(element=>element===document.activeElement));
  await shot('answered');
});

for(const width of [1440,390])test(`Ask Pharos prompt chips drag into text and support keyboard editing @ ${width}`,{skip},async t=>{
  const selection={provider:'Codex',model:'test-model',effort:'Low'};let sent;
  const {page,shot}=await open(t,{viewport:{width,height:800},ready:'.pharos-ai:not([hidden])',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/sessions':{sessions:[]},'POST /api/ask/session':({body})=>body,
    'POST /api/ask/send':({body})=>{sent=body;return {id:body.id,createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',conversation:[{id:body.turnID,prompt:body.prompt,currentURI:body.currentURI,cli:'Codex',model:'test-model',effort:'Low',status:'multiple',reply:'Here is the breakdown.',cards:[]}]};},
  }});
  await page.waitForFunction(()=>document.querySelector('.pharos-current-settings')?.textContent.includes('Test Model'));
  await activateHeaderTool(page,'askPharosToggle');
  const editor=page.getByRole('textbox',{name:'Ask a question',exact:true}),chip=page.getByRole('group',{name:/About Home context/});
  const contextURI=await page.evaluate(()=>askPageURI(location.href));
  await editor.press('End');await page.keyboard.insertText('Find usage by model');
  assert.equal(await editor.evaluate(element=>element.value),'Find usage by model');
  const beforeDrag=await editor.innerText();
  // Drop at the caret after “Find ”, using the rendered text's real location.
  const target=await editor.evaluate(element=>{
    const walker=document.createTreeWalker(element,NodeFilter.SHOW_TEXT);
    while(walker.nextNode())if(walker.currentNode.data.includes('Find usage by model')){
      const range=document.createRange();range.setStart(walker.currentNode,5);range.collapse(true);
      const caret=range.getBoundingClientRect(),box=element.getBoundingClientRect();return {x:caret.x-box.x+1,y:caret.y-box.y+caret.height/2};
    }
    throw new Error('The typed prompt is missing');
  });
  await chip.dragTo(editor,{sourcePosition:{x:12,y:12},targetPosition:target});
  assert.match(await editor.innerText(),/Find .*About Home.*usage by model/s,'the chip moves between the words at the drop caret');
  assert.equal(await editor.evaluate(element=>element.value),'Find usage by model','dragging preserves the question');
  await shot('chip-between-words');
  await editor.press('Meta+z');
  assert.equal(await editor.innerText(),beforeDrag,'undo restores the chip position');
  await editor.press('Meta+Shift+z');
  assert.match(await editor.innerText(),/Find .*About Home.*usage by model/s);
  await chip.focus();await page.keyboard.press('Alt+ArrowRight');
  assert.match(await editor.innerText(),/Find u.*About Home.*sage by model/s,'keyboard movement relocates the same chip');
  await page.keyboard.press('Backspace');
  assert.ok(await editor.evaluate(element=>element===document.activeElement),'removing a focused chip returns to the editor');
  assert.equal(await editor.evaluate(element=>element.value),'Find usage by model');
  await editor.press('Meta+z');await chip.waitFor();
  await editor.press('Meta+a');await page.keyboard.press('Backspace');
  assert.equal(await editor.evaluate(element=>element.value),'','select-all deletion clears text and context together');
  await editor.press('Meta+z');await chip.waitFor();
  assert.equal(await editor.evaluate(element=>element.value),'Find usage by model','undo restores the selected prompt and chip');
  await editor.press('Meta+ArrowDown');await page.keyboard.press('Shift+Enter');await page.keyboard.insertText('this week');
  assert.equal(await editor.evaluate(element=>element.value),'Find usage by model\nthis week');
  await page.getByRole('button',{name:'Send question',exact:true}).click();
  await page.getByRole('status').filter({hasText:'Here is the breakdown.'}).waitFor();
  assert.equal(sent.prompt,'Find usage by model\nthis week');assert.equal(sent.currentURI,contextURI);
});

for(const [width,height,motion] of [[2133,1070,'no-preference'],[390,650,'no-preference'],[320,480,'reduce']])test(`Ask Pharos dialog grows within the viewport and settles back after Clear (${motion}) @ ${width}`,{skip},async t=>{
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  let finish;const response=new Promise(resolve=>{finish=resolve;});t.after(()=>finish());
  const {page,settled,shot}=await open(t,{viewport:{width,height},ready:'.pharos-ai:not([hidden])',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default'],default:true}]},
    'GET /api/ask/sessions':{sessions:Array.from({length:8},(_,index)=>({id:'prior-'+index,selection,conversation:[{prompt:'Prior question '+index}],updatedAt:Date.now()}))},'POST /api/ask/session':({body})=>body,
    'POST /api/ask/send':async({body})=>{await response;return {id:body.id,createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',conversation:[{id:body.turnID,prompt:body.prompt,cli:'Codex',model:'test-model',effort:'Low',status:'multiple',reply:'Your activity overview. '+('Here is more detail about the work and the questions you explored. '.repeat(150)),cards:[]}]};},
  }});
  await page.emulateMedia({reducedMotion:motion});
  await page.waitForFunction(()=>document.querySelector('.pharos-current-settings')?.textContent.includes('Test Model'));
  await activateHeaderTool(page,'askPharosToggle');
  const modal=page.getByRole('dialog',{name:'Ask Pharos',exact:true}),editor=page.getByRole('textbox',{name:'Ask a question',exact:true});
  const finishMotion=()=>modal.evaluate(async element=>{
    for(;;){
      await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
      const animations=[element,...element.querySelectorAll('.pharos-ai-frame')].flatMap(surface=>surface.getAnimations()).filter(animation=>animation.playState==='running'&&animation.effect.getTiming().iterations!==Infinity);
      if(!animations.length)return;
      await Promise.all(animations.map(animation=>animation.finished.catch(()=>{})));
    }
  });
  await finishMotion();
  const empty=await modal.boundingBox();
  assert.ok(empty.y>0&&empty.y+empty.height<=height,'the empty overlay floats inside the viewport');
  if(height>900)assert.ok(empty.y>height/5&&empty.y<height/3,'the empty overlay starts around a quarter down a tall window');
  await shot('empty-position');
  await modal.getByRole('button',{name:'History',exact:true}).click();await finishMotion();
  assert.ok(await modal.getByRole('button',{name:'Close chat history',exact:true}).evaluate(element=>{const box=element.getBoundingClientRect(),hit=document.elementFromPoint(box.x+box.width/2,box.y+box.height/2);return element===hit||element.contains(hit);}),'history controls remain reachable as the dialog makes room for the menu');
  await modal.getByRole('button',{name:'Close chat history',exact:true}).click();await finishMotion();
  const clear=modal.getByRole('button',{name:'Clear',exact:true}),close=modal.getByRole('button',{name:'Close Ask Pharos',exact:true});
  const checkWindowControls=async()=>{
    await finishMotion();
    const clearBox=await clear.boundingBox(),closeBox=await close.boundingBox();
    assert.ok(clearBox.x+clearBox.width<=closeBox.x&&clearBox.y<closeBox.y+closeBox.height&&closeBox.y<clearBox.y+clearBox.height,'Clear sits to the left of Close on the same row');
    for(const button of [clear,close])assert.ok(await button.evaluate(element=>{const box=element.getBoundingClientRect(),hit=document.elementFromPoint(box.x+box.width/2,box.y+box.height/2);return element===hit||element.contains(hit);}), 'window controls remain clickable');
    const transcript=modal.getByRole('region',{name:'Chat messages, scroll for earlier replies'});
    if(await transcript.isVisible()){
      assert.ok(await clear.evaluate(button=>{
        const transcript=document.querySelector('.pharos-thread-viewport'),before=transcript.getBoundingClientRect();
        button.hidden=true;
        const after=transcript.getBoundingClientRect();
        button.hidden=false;
        return before.top===after.top&&before.height===after.height;
      }),'Clear adds no vertical space beside Close');
    }
  };
  await editor.fill('Summarize my recent work.\nHighlight recurring themes.\nCompare usage across models.\nShow changes over time.\nFind work worth revisiting.\nSuggest a useful next question.');
  await finishMotion();
  const draft=await modal.boundingBox();
  assert.ok(draft.y<empty.y&&draft.y+draft.height>empty.y+empty.height,'a growing prompt expands above and below the empty composer');
  await shot('draft-growth');
  await editor.fill('An unsent question');await checkWindowControls();await shot('draft-clear');
  await clear.click();
  assert.equal(await editor.evaluate(element=>element.value),'');
  assert.ok(await editor.evaluate(element=>element===document.activeElement),'clearing a draft returns focus to the composer');
  assert.ok(await clear.isHidden(),'Clear hides when the composer is empty');
  await modal.evaluate(element=>{element.observedTransitions=[];element.addEventListener('transitionrun',event=>{if(event.target===element)element.observedTransitions.push(event.propertyName);});});
  await editor.fill('Give me a detailed overview of my recent work.');
  await page.getByRole('button',{name:'Send question',exact:true}).click();
  await page.getByRole('region',{name:'AI request in progress',exact:true}).waitFor();
  await checkWindowControls();
  finish();await page.getByRole('status').filter({hasText:'Your activity overview.'}).waitFor();await settled();await finishMotion();
  const expanded=await modal.boundingBox();
  assert.ok(expanded.height>empty.height&&expanded.y<empty.y,'the overlay expands into the space above the starting position');
  assert.ok(expanded.y>=0&&expanded.y+expanded.height<=height,'the expanded overlay stays on screen');
  const fits=element=>{const box=element.getBoundingClientRect();return box.top>=0&&box.bottom<=innerHeight;};
  assert.ok(await editor.evaluate(fits),'the draft remains reachable with a long response');
  assert.ok(await page.getByRole('button',{name:'Send question',exact:true}).evaluate(fits),'Send remains reachable with a long response');
  assert.ok(await page.getByRole('region',{name:'Chat messages, scroll for earlier replies'}).evaluate(element=>element.scrollHeight>element.clientHeight),'a long response scrolls inside the composer');
  const transitions=await modal.evaluate(element=>element.observedTransitions);
  if(motion==='reduce')assert.deepEqual(transitions,[],'reduced motion disables dialog transitions');
  else assert.ok(transitions.includes('height')&&transitions.includes('top'),'growth and repositioning animate');
  await shot('expanded-position');
  await checkWindowControls();
  await clear.click();await settled();await finishMotion();
  assert.ok((await modal.boundingBox()).y>expanded.y,'Clear returns the empty composer to its starting region');
  await page.setViewportSize({width:320,height:350});await settled();await finishMotion();
  assert.ok(await editor.evaluate(fits),'the empty composer fits after shrinking the window');
  assert.ok(await page.getByRole('button',{name:'Close Ask Pharos',exact:true}).evaluate(fits),'Close stays reachable after shrinking the window');
});

for (const [width,url,theme] of [[1440,'/analyze?analyze_topic=tokens','dark'],[390,'/analyze?analyze_topic=tokens','dark'],[320,'/analyze?analyze_topic=tokens','dark'],[2133,'/','dark'],[1440,'/','light'],[390,'/','light']]) test(`Ask Pharos overlay and floating progress keep focus and stay reachable (${url==='/'?'Home':'Analyze'}, ${theme}) @ ${width}`, {skip}, async t => {
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  let finish, sent;
  const response=new Promise(resolve=>{finish=resolve;});
  t.after(()=>finish());
  const {page,shot}=await open(t,{url,preferences:{'pharos-theme':theme},viewport:{width,height:width===2133?1070:650},ready:'body>header',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/sessions':{sessions:[]},
    'POST /api/ask/session':({body})=>body,
    'POST /api/ask/send':async({body})=>{
      sent=body;await response;
      return {id:body.id,createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',conversation:[{id:body.turnID,prompt:body.prompt,currentURI:body.currentURI,cli:'Codex',model:'test-model',effort:'Low',status:'single',message:'',elapsedMs:1000,reply:'Ready to explore.',cards:[{id:'library',title:'Search',kind:'Page',description:'Your past work',href:'/library'}]}]};
    },
  }});
  await page.waitForFunction(()=>document.querySelector('.pharos-current-settings')?.textContent.includes('Test Model'));
  await activateHeaderTool(page,'askPharosToggle');
  const modal=page.getByRole('dialog',{name:'Ask Pharos',exact:true});
  const editor=page.getByRole('textbox',{name:'Ask a question',exact:true});
  assert.ok(await editor.evaluate(element=>element===document.activeElement));
  await page.locator('body>header .brand').evaluate(element=>element.focus());
  assert.ok(await modal.evaluate(element=>element.contains(document.activeElement)),'the overlay must keep focus');
  await editor.evaluate(element=>{element.value='What if we broke it down by model?';element.dispatchEvent(new Event('input',{bubbles:true}));});
  const reachable=element=>{const box=element.getBoundingClientRect(),target=document.elementFromPoint(box.x+box.width/2,box.y+box.height/2);return box.left>=0&&box.right<=innerWidth&&(element===target||element.contains(target));};
  for(const name of ['Clear','Close Ask Pharos','Remove page context'])assert.ok(await modal.getByRole('button',{name,exact:true}).evaluate(reachable),name+' must stay reachable');
  assert.ok(await editor.evaluate(element=>{
    const controls=[...element.closest('form').querySelectorAll('.pharos-window-controls button:not([hidden])')].map(button=>button.getBoundingClientRect());
    const walker=document.createTreeWalker(element,NodeFilter.SHOW_TEXT);
    while(walker.nextNode()){
      const range=document.createRange();range.selectNodeContents(walker.currentNode);
      for(const box of range.getClientRects())for(const control of controls)if(box.left<control.right&&box.right>control.left&&box.top<control.bottom&&box.bottom>control.top)return false;
    }
    return true;
  }),'floating controls must not obscure prompt text');
  await shot('ask-overlay');
  await modal.getByRole('button',{name:'Remove page context',exact:true}).click();
  assert.ok(await editor.evaluate(element=>element===document.activeElement),'removing context returns focus to the request');
  assert.equal(await editor.evaluate(element=>element.value),'What if we broke it down by model?','removing context preserves the draft');
  await modal.getByRole('button',{name:'Close Ask Pharos',exact:true}).click();
  await modal.waitFor({state:'hidden'});
  assert.ok(await page.locator('#askPharosToggle').evaluate(element=>element===document.activeElement)||await page.locator('#headerMore').evaluate(element=>element===document.activeElement),'Close returns focus to the header');
  await activateHeaderTool(page,'askPharosToggle');
  await modal.getByRole('button',{name:'Remove page context',exact:true}).waitFor();
  await page.getByRole('button',{name:'Switch to non-AI search',exact:true}).click();
  await modal.getByRole('button',{name:'Search past work',exact:true}).click();
  await modal.getByRole('textbox',{name:'Text for rule 1.1',exact:true}).fill('cache');
  await modal.getByRole('button',{name:'Search messages',exact:true}).click();
  await page.waitForURL(url=>url.pathname==='/library');
  await modal.waitFor({state:'hidden'});
  const brand=page.locator('body>header .brand');await brand.focus();
  assert.ok(await brand.evaluate(element=>element===document.activeElement),'ordinary search results must allow background interaction');
  await page.evaluate(()=>window.pharosNavigate('/analyze?analyze_topic=tokens'));
  await activateHeaderTool(page,'askPharosToggle');
  const originalURI=await page.evaluate(()=>location.pathname+location.search+location.hash);
  if(width===390)await modal.getByRole('button',{name:'Remove page context',exact:true}).click();
  await editor.evaluate(element=>{element.value='What if we broke it down by model?';element.dispatchEvent(new Event('input',{bubbles:true}));});
  await page.getByRole('button',{name:'Send question',exact:true}).click();
  const browseHint=page.getByRole('tooltip');await browseHint.waitFor();
  await page.waitForFunction(()=>{const hint=document.getElementById('pharos-browse-hint'),rect=hint.getBoundingClientRect();return rect.left>=0&&rect.right<=innerWidth&&rect.top>=0&&rect.bottom<=innerHeight;});
  await page.waitForFunction(element=>new Promise(resolve=>{const observer=new IntersectionObserver(([entry])=>{observer.disconnect();resolve(entry.intersectionRatio===1);});observer.observe(element);}),await browseHint.elementHandle());
  assert.ok(await page.getByRole('button',{name:'Float Ask Pharos',exact:true}).evaluate(element=>{const rect=element.getBoundingClientRect(),target=document.elementFromPoint(rect.x+rect.width/2,rect.y+rect.height/2);return element===target||element.contains(target);}), 'the hint leaves picture in picture clickable');
  await shot('ask-browse-hint');
  await page.getByRole('button',{name:'Float Ask Pharos',exact:true}).click();
  await modal.waitFor({state:'hidden'});
  const panel=page.getByRole('complementary',{name:'Ask Pharos progress'});
  await panel.waitFor();
  const handle=page.getByRole('button',{name:'Move Ask Pharos progress'});
  const bounds=await panel.locator('.pharos-floating-open').boundingBox();
  await page.mouse.move(bounds.x+bounds.width/2,bounds.y+bounds.height/2);
  await page.mouse.down();await page.mouse.move(width-15,620);await page.mouse.up();
  await handle.focus();await page.keyboard.press('ArrowDown');
  const fits=element=>{const rect=element.getBoundingClientRect();return rect.left>=0&&rect.right<=innerWidth&&rect.top>=0&&rect.bottom<=innerHeight;};
  assert.ok(await panel.evaluate(fits),'dragged progress must stay in the viewport');
  await panel.locator('.pharos-floating-open').click();
  await modal.waitFor();
  assert.ok(await editor.evaluate(element=>element===document.activeElement));
  await page.setViewportSize({width:320,height:500});
  await page.getByRole('button',{name:'Float Ask Pharos',exact:true}).click();
  await panel.waitFor();
  assert.ok(await panel.evaluate(fits),'progress restored after a hidden resize must stay in the viewport');
  await panel.locator('.pharos-floating-open').click();await modal.waitFor();
  await page.setViewportSize({width,height:650});
  await page.getByRole('button',{name:'Float Ask Pharos',exact:true}).click();await panel.waitFor();
  await activateHeaderTool(page,'settingsToggle');
  await page.waitForURL('**/settings*');
  await panel.getByRole('button',{name:'Dismiss Ask Pharos progress'}).click();
  await panel.waitFor({state:'hidden'});
  // Reopen while pending, then close with Escape: this restores the floating panel.
  await activateHeaderTool(page,'askPharosToggle');
  assert.ok(await editor.evaluate(element=>element===document.activeElement));
  await page.keyboard.press('Escape');await modal.waitFor({state:'hidden'});await panel.waitFor();
  finish();
  await panel.getByRole('button',{name:/Your response is ready/}).waitFor();
  assert.equal(new URL(page.url()).pathname,'/settings','a ready response must leave the current page alone');
  assert.equal(sent.currentURI,width===390?'':originalURI,'the request uses the attached context and respects its removal');
  await shot('ask-floating-ready');
  await panel.getByRole('button',{name:/Your response is ready/}).click();
  await page.locator('.pharos-destination:not([hidden])').click();
  await modal.waitFor({state:'hidden'});
  await page.waitForURL(url=>url.pathname==='/library');
  assert.ok(await page.locator('#askPharosToggle').evaluate(element=>element===document.activeElement)||await page.locator('#headerMore').evaluate(element=>element===document.activeElement));
});

test('Ask Pharos on Home can keep browsing without losing the active chat', {skip}, async t => {
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  let finish;
  const response=new Promise(resolve=>{finish=resolve;});t.after(()=>finish());
  const {page}=await open(t,{url:'/',ready:'#pharos-ask .pharos-ai:not([hidden])',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/sessions':{sessions:[]},
    'POST /api/ask/session':({body})=>body,
    'POST /api/ask/send':async({body})=>{await response;return {id:body.id,createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',conversation:[{id:body.turnID,prompt:body.prompt,cli:'Codex',model:'test-model',effort:'Low',status:'clarification',message:'',elapsedMs:1000,reply:'',cards:[],clarification:{question:'Which metric?',options:['Tokens','Cost']}}]};},
  }});
  await page.waitForFunction(()=>document.querySelector('.pharos-current-settings')?.textContent.includes('Test Model'));
  await page.getByRole('textbox',{name:'Ask a question',exact:true}).fill('Break it down');
  await page.getByRole('button',{name:'Clear',exact:true}).click();
  assert.equal(await page.getByRole('textbox',{name:'Ask a question',exact:true}).evaluate(element=>element.value),'');
  assert.ok(await page.getByRole('textbox',{name:'Ask a question',exact:true}).evaluate(element=>element===document.activeElement),'clearing a Home draft returns focus to the composer');
  await page.getByRole('textbox',{name:'Ask a question',exact:true}).fill('Break it down');
  await page.getByRole('button',{name:'Float Ask Pharos',exact:true}).click();
  const panel=page.getByRole('complementary',{name:'Ask Pharos progress'});await panel.waitFor();
  await panel.locator('.pharos-floating-open').click();
  assert.equal(await page.getByRole('textbox',{name:'Ask a question',exact:true}).evaluate(element=>element.value),'Break it down');
  // Keep the server's deferred answer, and deliver activity through the real
  // NDJSON reader so the floating surface is exercised before completion.
  await page.evaluate(()=>{
    const nativeFetch=window.fetch;
    window.fetch=(path,options)=>{
      const response=nativeFetch(path,options);
      if(path!=='/api/ask/send')return response;
      const encoder=new TextEncoder();
      return Promise.resolve(new Response(new ReadableStream({start(controller){
        const write=event=>controller.enqueue(encoder.encode(JSON.stringify(event)+'\n'));
        window.pharosAskProgress=activity=>write({type:'progress',activity});
        response.then(result=>result.json()).then(session=>{write({type:'result',session});controller.close();}).catch(error=>controller.error(error));
      }}),{headers:{'Content-Type':'application/x-ndjson'}}));
    };
  });
  await page.getByRole('button',{name:'Send question',exact:true}).click();
  await page.getByRole('button',{name:'Float Ask Pharos',exact:true}).click();
  await panel.waitFor();
  await page.waitForFunction(()=>typeof window.pharosAskProgress==='function');
  await page.evaluate(()=>window.pharosAskProgress({message:'Browsing available views…',elapsedMs:1000}));
  await panel.getByRole('button',{name:/Browsing available views/}).waitFor();
  await activateHeaderTool(page,'settingsToggle');
  await page.evaluate(()=>window.pharosAskProgress({message:'Searching archived work…',elapsedMs:2000}));
  await panel.getByRole('button',{name:/Searching archived work/}).waitFor();
  finish();await panel.getByRole('button',{name:/Pharos has a question/}).waitFor();
  await panel.getByRole('button',{name:/Pharos has a question/}).click();
  await page.getByRole('button',{name:'A. Tokens',exact:true}).waitFor();
  await page.keyboard.press('Escape');
  await page.locator('body>header .brand').click();
  await page.locator('#homeAskSearch').getByRole('button',{name:'A. Tokens',exact:true}).waitFor();
});

for (const browsing of [false,true]) test(`Ask Pharos ${browsing?'reopened browsing':'blocking'} overlay handles a ready destination`, {skip}, async t => {
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  let finish;
  const response=new Promise(resolve=>{finish=resolve;});t.after(()=>finish());
  const {page}=await open(t,{url:'/library',ready:'body>header',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/sessions':{sessions:[]},
    'POST /api/ask/session':({body})=>body,
    'POST /api/ask/send':async({body})=>{await response;return {id:body.id,createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',conversation:[{id:body.turnID,prompt:body.prompt,cli:'Codex',model:'test-model',effort:'Low',status:'single',message:'',elapsedMs:1000,reply:'Your preferences.',cards:[{id:'settings',title:'Settings',kind:'Page',description:'Your preferences',href:'/settings'}]}]};},
  }});
  await page.waitForFunction(()=>document.querySelector('.pharos-current-settings')?.textContent.includes('Test Model'));
  await activateHeaderTool(page,'askPharosToggle');
  await page.getByRole('textbox',{name:'Ask a question',exact:true}).fill('Open settings');
  await page.getByRole('button',{name:'Send question',exact:true}).click();
  const modal=page.getByRole('dialog',{name:'Ask Pharos',exact:true});
  if(browsing){
    await page.getByRole('button',{name:'Float Ask Pharos',exact:true}).click();
    await activateHeaderTool(page,'askPharosToggle');
  }
  finish();
  if(browsing){
    await page.locator('.pharos-destination:not([hidden])').waitFor();
    assert.equal(new URL(page.url()).pathname,'/library');
    assert.ok(await modal.evaluate(element=>element.contains(document.activeElement)));
    await page.locator('.pharos-destination:not([hidden])').click();
  }
  await page.waitForURL(url=>url.pathname==='/settings');await modal.waitFor({state:'hidden'});
});


test('Quick Stats disclosure and editing keep keyboard focus on the control being used', { skip }, async t => {
  const { page, shot } = await open(t, { url: '/settings/layout', ready: '#quickStatsExpand' });
  const disclosure = page.locator('#quickStatsExpand');
  await press(disclosure);
  await page.locator('#quickStatsOptions').waitFor();
  assert.ok(await disclosure.evaluate(element => document.activeElement === element));
  const handle = page.getByRole('button', { name: 'Arrange Tokens', exact: true });
  const focused = async locator => page.waitForFunction(element => document.activeElement === element, await locator.elementHandle());
  await handle.focus();
  await press(handle, 'Alt+ArrowUp');
  await focused(handle);
  await press(handle);
  await page.locator('#quickStatsToolMenu:popover-open').waitFor();
  await page.keyboard.press('Escape');
  await focused(handle);
  await press(handle);
  await press(page.getByRole('button', { name: 'Move down Tokens', exact: true }));
  await focused(handle);
  await page.keyboard.press('Delete');
  const add = page.getByRole('button', { name: 'Add Tokens', exact: true });
  await focused(add);
  await press(add);
  await focused(handle);
  const column = page.getByRole('button', { name: 'Arrange Today', exact: true });
  await press(column, 'Alt+ArrowLeft');
  await focused(column);
  await press(column);
  await press(page.getByRole('button', { name: 'Move right Today', exact: true }));
  await focused(column);
  await page.evaluate(() => dispatchEvent(new Event('pharos:carbon-settings')));
  await focused(column);
  await page.keyboard.press('Delete');
  const addColumn = page.getByRole('button', { name: 'Add Today', exact: true });
  await focused(addColumn);
  await press(addColumn);
  await focused(column);
  await shot('quick-stats-editor');
  await press(disclosure);
  await page.locator('#quickStatsOptions').waitFor({ state: 'hidden' });
  assert.ok(await disclosure.evaluate(element => document.activeElement === element));
});

test('dashboard card creation keeps focus in its dialog, restores it on cancel and opens the saved Home card', {skip}, async t => {
  const chart = {metric:'tokens',split:'provider',period:'day',rollingPeriods:3};
  const url = '/analyze?'+new URLSearchParams({analyze_topic:'tokens',analyze_chart:JSON.stringify(chart)});
  const {page,shot} = await open(t,{url,ready:'.usage-dashboard .usage-chart'});
  const create = page.getByRole('button',{name:'Create card',exact:true}).first();
  await press(create);
  const dialog = page.getByRole('dialog',{name:'Create dashboard card'});
  assert.ok(await dialog.getByRole('textbox',{name:'Title',exact:true}).evaluate(el=>el===document.activeElement));
  await holdsFocus(page,dialog,create);
  await create.click();
  await dialog.getByRole('textbox',{name:'Title',exact:true}).fill('My provider tokens');
  await dialog.getByRole('textbox',{name:'Description',exact:true}).fill('A moving view of my token usage.');
  for(const width of [1440,390,320]) {
    await page.setViewportSize({width,height:900});
    assert.ok(await dialog.evaluate(el=>{const r=el.getBoundingClientRect();return r.left>=0 && r.right<=innerWidth && el.scrollWidth<=el.clientWidth;}),'dialog fits the viewport');
  }
  await shot('create-dashboard-card');
  await dialog.getByRole('button',{name:'Create card',exact:true}).click();
  await page.getByRole('link',{name:'your Home dashboard',exact:true}).click();
  const card = page.locator('.home-collection .query-card').filter({hasText:'My provider tokens'});
  await card.locator('.query-cover[aria-busy]').waitFor({state:'detached'});
  await shot('created-dashboard-card-home');
  await card.getByRole('link').click();
  await page.locator('.usage-dashboard .usage-chart').waitFor();
});

for (const [width, theme, count, unavailable, view='compare'] of [[1440,'dark',2,false],[390,'light',1,false],[320,'dark',1,true],[1440,'dark',1,false,'trend'],[390,'light',1,false,'distribution']]) test(`Ask Pharos chart previews keep links, page state and focus usable (${theme}, ${view}, ${unavailable?'unavailable':count+' charts'}) @ ${width}`, {skip}, async t => {
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  const cards=Array.from({length:count},(_,index)=>({id:'chart-'+index,title:index?'Tokens by model':'Tokens by provider',description:'Explore the selected token usage.',href:'/analyze?'+new URLSearchParams({analyze_mode:'dashboards',analyze_topic:'tokens',...view==='trend'?{analyze_window:'today',analyze_timezone:'America/Denver'}:{},analyze_chart:JSON.stringify({v:2,view,metric:'tokens',split:index?'model':'provider',unit:'conversation',minSamples:0,...view==='trend'?{rollingPeriods:3,norm:true}:{}}),analyze_filters:encodeQuery({...EMPTY_QUERY,where:[]})})}));
  let session;
  const {page,shot,settled,calls}=await open(t,{url:'/library',ready:'body>header',viewport:{width,height:900},preferences:{'pharos-theme':theme},api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/sessions':()=>({sessions:session?[session]:[]}),
    'GET /api/ask/session':()=>session,
    'POST /api/ask/session':({body})=>(session={...session,...body}),
    'POST /api/ask/send':({body})=>(session={id:body.id,createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',conversation:[{id:body.turnID,prompt:body.prompt,cli:'Codex',model:'test-model',effort:'Low',status:count===1?'single':'multiple',message:'',elapsedMs:1000,reply:'Your token usage.',cards}]}),
    ...(unavailable?{'POST /api/query/usage/metrics':()=>({metrics:[]}), 'POST /api/query/usage/query-v3':()=>({metrics:[]})}:{}),
  }});
  await page.waitForFunction(()=>document.querySelector('.pharos-current-settings')?.textContent.includes('Test Model'));
  await activateHeaderTool(page,'askPharosToggle');
  const modal=page.getByRole('dialog',{name:'Ask Pharos',exact:true});
  const question=page.getByRole('textbox',{name:'Ask a question',exact:true});
  await question.fill('Show my token usage');
  const before=page.url();
  await question.press('Enter');
  for (const card of cards) {
    const chart=page.getByRole('figure',{name:'Chart preview: '+card.title,exact:true});
    await chart.waitFor();await chart.scrollIntoViewIfNeeded();
    await page.waitForFunction(title=>document.querySelector(`figure[aria-label="Chart preview: ${title}"]`)?.getAttribute('aria-busy')==='false',card.title);
    assert.equal(await page.getByRole('link',{name:new RegExp(card.title)}).getAttribute('href'),card.href);
  }
  assert.equal(page.url(),before,'rendering a chart must leave the underlying page URL alone');
  assert.ok(await modal.evaluate(element=>element.contains(document.activeElement)),'loading charts must not move focus out of Ask');
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'chart preview overflowed the viewport');
  if(view==='trend'){
    const metricRequest=response=>/\/api\/query\/usage\/(metrics|query-v3)(?:\?|$)/.test(response.url());
    const queries=()=>calls.filter(call=>/^\/api\/query\/usage\/(metrics|query-v3)$/.test(call.path)).length;
    const loaded=queries();
    await page.getByRole('button',{name:'Close Ask Pharos',exact:true}).click();await modal.waitFor({state:'hidden'});
    await page.clock.setFixedTime(new Date(await page.evaluate(()=>Date.now()+2*86400000)));
    // Let the calendar clock observe the new day as it does on app focus.
    await page.evaluate(()=>window.dispatchEvent(new Event('focus')));await settled();
    assert.equal(queries(),loaded,'hidden charts defer calendar refreshes until reopened');
    let refreshed=page.waitForResponse(metricRequest);
    await activateHeaderTool(page,'askPharosToggle');await refreshed;
    await page.waitForFunction(()=>document.querySelector('figure[aria-label^="Chart preview:"]')?.getAttribute('aria-busy')==='false');
    assert.ok(queries()>loaded,'reopening a retained Today chart must execute its new window');
    // A data refresh must also bypass a just-filled cache within the same day.
    refreshed=page.waitForResponse(metricRequest);
    await page.evaluate(()=>window.dispatchEvent(new CustomEvent('pharos:data-refresh',{detail:'usage'})));await refreshed;
    await page.waitForFunction(()=>document.querySelector('figure[aria-label^="Chart preview:"]')?.getAttribute('aria-busy')==='false');
    // Reopening itself revalidates, even without a calendar or dataset event.
    await page.getByRole('button',{name:'Close Ask Pharos',exact:true}).click();await modal.waitFor({state:'hidden'});
    refreshed=page.waitForResponse(metricRequest);
    await activateHeaderTool(page,'askPharosToggle');await refreshed;
    await page.waitForFunction(()=>document.querySelector('figure[aria-label^="Chart preview:"]')?.getAttribute('aria-busy')==='false');
    assert.equal(page.url(),before,'revalidation preserves the surrounding page state');
    assert.ok(await modal.evaluate(element=>element.contains(document.activeElement)),'revalidation preserves focus in Ask');
  }
  await shot('ask-chart-previews');
  await page.getByRole('button',{name:'Clear',exact:true}).click();
  await settled();
  assert.equal(await page.getByRole('figure',{name:/^Chart preview:/}).count(),0);
  assert.ok(await question.evaluate(element=>element===document.activeElement));
  // Reopening a saved answer recreates its previews without creating a card.
  session={...session,conversation:[{id:'restored',prompt:'Saved chart',cli:'Codex',model:'test-model',effort:'Low',status:'multiple',reply:'Saved result',cards}]};
  await page.goto(new URL('/?ask_chat='+encodeURIComponent(session.id),page.url()).href);await settled();
  await page.getByRole('figure',{name:'Chart preview: '+cards[0].title,exact:true}).waitFor();
  await activateHeaderTool(page,'askPharosToggle');
  await page.waitForFunction(()=>document.querySelector('figure[aria-label^="Chart preview:"]')?.getAttribute('aria-busy')==='false');
  await settled();
  const link=page.getByRole('link',{name:new RegExp(cards[0].title)});
  await link.click();
  await page.waitForURL(url=>url.pathname==='/analyze'&&url.searchParams.get('analyze_topic')==='tokens');
  await modal.waitFor({state:'hidden'});
});
