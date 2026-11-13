// Focus and placement that only a real browser can check: modal surfaces take
// focus, keep it while open and give it back on Escape; the transcript rails
// stay below the header; the annotation toolbar stays on screen.
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { activateHeaderTool, open, skip } from "./harness.mjs";
import { claudeImportFixture, claudeSettingsSourceFixture, claudeSettingsCoverageFixture, defaultApi, workID, rootConversation, eventStream, busyStatus, workBackgroundDocument, workInspection } from "./fixtures.mjs";
import { EMPTY_QUERY, encodeQuery, withGroupingKeys } from "@pythia-software/query-table-core";

test('the Lighthouse menu action shares its saved setting with App preferences', {skip}, async t => {
  const {page, settled} = await open(t, {url:'/settings/preferences', ready:'#lighthouseLoadingIndicator'});
  const indicator = page.getByRole('switch', {name:'Lighthouse loading indicator'});
  assert.equal(await indicator.getAttribute('aria-checked'), 'true');
  await page.evaluate(() => window.pharosPageAction('lighthouse-loading'));
  assert.equal(await indicator.getAttribute('aria-checked'), 'false');
  assert.equal(await page.evaluate(() => window.pharosLighthouseLoadingIndicator()), false);
  await page.evaluate(() => window.pharosPrefs.flush());
  await page.reload();
  await indicator.waitFor();
  await settled();
  assert.equal(await indicator.getAttribute('aria-checked'), 'false');
  await indicator.click();
  assert.equal(await page.evaluate(() => window.pharosLighthouseLoadingIndicator()), true);
});

test('Settings switches keep keyboard focus and their saved state across Settings and Analyze', {skip}, async t => {
  let background = structuredClone(defaultApi['GET /api/background']);
  const {page, settled, shot} = await open(t, {url:'/settings/sync',viewport:{width:1920,height:1050},ready:'#backgroundSchedule .bg-col', api: {
    'GET /api/background': () => background,
    'POST /api/labels/settings': ({body}) => {
      if ('enabled' in body) {
        background.labels.enabled = body.enabled;
        const step = background.view.items.find(item => item.id === 'labels');
        Object.assign(step, {state: body.enabled ? 'queued' : 'disabled', state_label: body.enabled ? 'Up next' : 'Off', cadence: body.enabled ? 'hourly' : 'manual'});
        background.view.unscheduled = background.view.items.filter(item => item.cadence === 'manual').map(item => item.id);
        background.view.cadences.find(row => row.id === 'hourly').items = background.view.items.filter(item => item.cadence === 'hourly').map(item => item.id);
      }
      if ('agent_events_threads' in body) background.agent_events.threads = body.agent_events_threads;
      return background;
    },
  }});
  const refreshFocused = async control => {
    await control.focus();
    await page.evaluate(() => window.pharosBackground.refresh(true));
    await settled();
    assert.ok(await control.evaluate(element => document.activeElement === element), 'refreshing an idle model switch keeps keyboard focus');
  };
  await page.locator('[data-view-key="background:labels"]').click();
  const labels = page.getByRole('switch', {name:'Message labels',exact:true});
  await refreshFocused(labels);
  for (const enabled of [true, false]) {
    const saved = page.waitForResponse(response => response.url().endsWith('/api/labels/settings') && response.request().method() === 'POST');
    await page.keyboard.press('Space');
    await saved;
    await page.waitForFunction(enabled => {
      const toggle = document.getElementById('labelsEnabled');
      return toggle?.getAttribute('aria-checked') === String(enabled) && !toggle.disabled && document.activeElement === toggle;
    }, enabled);
    assert.equal(await labels.evaluate(element => element.closest('[data-cadence]').dataset.cadence), enabled ? 'hourly' : 'manual');
    await refreshFocused(labels);
    if (enabled) await shot('enabled');
  }
  await page.locator('[data-view-key="background:agent_events"]').click();
  await refreshFocused(page.getByRole('switch', {name:'Agent events (preview)',exact:true}));
  const threads = page.getByRole('combobox', {name:'Agent events (preview) CPU threads', exact:true});
  await threads.focus();
  const savedThreads = page.waitForResponse(response => response.url().endsWith('/api/labels/settings') && response.request().method() === 'POST');
  await threads.selectOption('2');
  await savedThreads;
  await page.waitForFunction(() => {
    const select = document.querySelector('[data-focus-key="agentEvents:threads"]');
    return select?.value === '2' && document.activeElement === select;
  });
  await shot('labeling');
  await page.evaluate(() => window.pharosNavigate('/settings/mcp'));
  await page.locator('#mcpSettingsPage .mcp-setup-grid').waitFor();
  const mcp = page.getByRole('switch', { name: 'Enable MCP', exact: true });
  await page.waitForFunction(() => !document.querySelector('[aria-label="Enable MCP"]')?.disabled);
  await press(mcp);
  await page.waitForFunction(() => document.querySelector('[aria-label="Enable MCP"]').getAttribute('aria-checked') === 'false');
  await press(page.getByRole('link', { name: 'View MCP usage', exact: true }));
  await page.locator('.usage-mcp-dashboard .qt-metric').waitFor();
  await press(page.getByRole('link', { name: 'MCP settings', exact: true }));
  await mcp.waitFor();
  await settled();
  assert.equal(await mcp.getAttribute('aria-checked'), 'false');
});

test('Background CPU allowance and task Stop/Resume keep keyboard focus across their redraws', {skip}, async t => {
  const background = structuredClone(workBackgroundDocument), posts = [];
  let held = true, cpuUsed = 2, covered = 12, historyLive = false, finishHistory, delayInspection = false, finishPoll;
  const pollStarted = Promise.withResolvers();
  const {page, settled} = await open(t, {url:'/settings/sync', ready:'#backgroundSchedule [data-card=work] .bg-work-row', api: {
    'GET /api/background': () => background,
    'POST /api/work/settings': ({body}) => { background.work.cpu_slots = body.cpu_slots; return background; },
    'GET /api/work': () => {
      const value = workInspection();
      value.state.authority.cpu_cap = background.work.cpu_slots;
      value.state.authority.cpu_allocated = cpuUsed;
      value.state.requests[0].sealed = covered;
      if (historyLive) value.state.requests.push({id: 9, scope: 'backfill', explicit: true, state: 'running', kinds: ['analysis.labels'], members: 50, sealed: 0});
      if (!held) Object.assign(value.state.tasks.find(task => task.kind === 'analysis.labels'), {state: 'queued', blocker: null});
      if (delayInspection) {
        delayInspection = false;
        pollStarted.resolve();
        return new Promise(resolve => { finishPoll = () => resolve(value); });
      }
      return value;
    },
    'POST /api/work': ({body}) => {
      posts.push(body);
      if (body.action === 'backfill') {
        historyLive = true;
        return new Promise(resolve => { finishHistory = () => resolve({request_id: 9}); });
      }
      if (['stop', 'resume'].includes(body.action)) held = body.action === 'stop';
      return {changed: true};
    },
  }});
  const allowance = page.locator('[data-focus-key="work:cpu"]');
  await allowance.focus();
  const saved = page.waitForRequest(request => request.url().endsWith('/api/work/settings') && request.method() === 'POST');
  await allowance.selectOption('2');
  assert.deepEqual((await saved).postDataJSON(), {cpu_slots: 2});
  await page.waitForFunction(() => document.activeElement?.dataset.focusKey === 'work:cpu' && document.activeElement.value === '2');
  await page.waitForFunction(() => document.querySelector('[data-card="work"] .bg-state')?.textContent.includes('2 of 2'));
  cpuUsed = 1; covered = 18;
  // Polling must keep updating work and other cards while the select keeps
  // keyboard focus; it must not require the user to tab away first.
  background.cost.text = 'about 6 h';
  await page.evaluate(() => window.pharosBackground.refresh(true));
  await page.waitForFunction(() => document.querySelector('[data-card="work"] .bg-state')?.textContent.includes('1 of 2') &&
    document.querySelector('[data-request="7"]')?.textContent.includes('18 of 40'));
  assert.equal(await allowance.evaluate(element => element === document.activeElement), true);

  // Polling keeps an in-flight action disabled even while the CPU select
  // is focused; completion must retain the server model's disabled state.
  const history = page.locator('[data-focus-key="work:backfill"]');
  delayInspection = true;
  await page.evaluate(() => { window.backgroundPoll = window.pharosBackground.refresh(true); });
  await pollStarted.promise;
  const posting = page.waitForRequest(request => request.url().endsWith('/api/work') && request.method() === 'POST');
  await history.click();
  await posting;
  await allowance.focus();
  assert.equal(await history.isDisabled(), true);
  const posted = page.waitForResponse(response => response.url().endsWith('/api/work') && response.request().method() === 'POST');
  finishHistory();
  await posted;
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  assert.equal(await history.isDisabled(), true, 'an action stays pending while an earlier poll delays its reconciliation');
  finishPoll();
  await page.evaluate(() => window.backgroundPoll);
  await page.locator('[data-request="9"]').waitFor();
  assert.equal(await history.isDisabled(), true);
  assert.equal(await allowance.evaluate(element => element === document.activeElement), true);

  // A refused save restores the saved choice without moving keyboard focus.
  // Return the fetch refusal locally so no deliberate HTTP error pollutes
  // the browser harness's unexpected-network-error assertions.
  await page.evaluate(() => {
    const fetch = window.fetch;
    window.fetch = (url, options) => String(url).endsWith('/api/work/settings')
      ? Promise.resolve({ok: false, status: 409, json: async () => ({error: 'The CPU allowance is fixed by the environment.'})})
      : fetch(url, options);
  });
  await allowance.selectOption('3');
  await page.waitForFunction(() => document.activeElement?.dataset.focusKey === 'work:cpu' && document.activeElement.value === '2');
  // Resume and Stop replace each other on the same task row; focus follows.
  const hold = page.locator('[data-focus-key="work:hold:analysis.labels"]');
  for (const [action, next] of [['resume', 'stop'], ['stop', 'resume']]) {
    await hold.focus();
    await page.keyboard.press('Enter');
    await page.waitForFunction(next => {
      const element = window.document.activeElement;
      return element?.isConnected && element.dataset.focusKey === 'work:hold:analysis.labels' && element.dataset.action === next && !element.disabled;
    }, next);
    assert.deepEqual(posts.at(-1), {action, kind: 'analysis.labels'});
  }
  await page.evaluate(() => {
    const fetch = window.fetch;
    window.fetch = (url, options) => String(url) === '/api/work' && options?.method === 'POST'
      ? Promise.resolve({ok: false, status: 409, json: async () => ({error: 'The task is no longer running.'})})
      : fetch(url, options);
  });
  await hold.focus();
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => !document.querySelector('[data-focus-key="work:hold:analysis.labels"]')?.disabled && document.activeElement?.dataset.focusKey === 'work:hold:analysis.labels');
  await page.evaluate(() => document.activeElement?.blur());
  await page.evaluate(() => window.pharosBackground.refresh(true));
  assert.equal(await page.evaluate(() => document.activeElement === document.body), true, 'a rejected action cannot restore focus on a later poll');
  await settled();
});

test('Library setup keeps focus in its modal and returns to the choice after cancelling a flow', {skip}, async t => {
  const {page, calls, shot} = await open(t, {url:'/app',ready:'#application-setup[open]',api:{
    'GET /api/app/state': {application:'/Applications/Pharos.app',selected:'',mcp:'',busy:false,libraries:[]},
    'POST /api/app/create': {saved:true},
    'POST /api/app/choose-folder': {path:null},
  }});
  const dialog=page.locator('#application-setup');
  const focused=locator=>locator.evaluate(element=>element===document.activeElement);
  assert.ok(await dialog.evaluate(element=>element.contains(document.activeElement)));
  await page.locator('.brand').evaluate(element=>element.focus());
  assert.ok(await dialog.evaluate(element=>element.contains(document.activeElement)), 'background took focus during setup');
  for(let i=0;i<8;i++){
    await page.keyboard.press('Tab');
    assert.ok(await dialog.evaluate(element=>element.contains(document.activeElement)), 'Tab escaped the modal');
  }
  await shot('welcome');
  await page.locator('#meet-pharos').click();
  assert.ok(await focused(page.locator('#about-title')));
  await page.locator('#start-setup').click();
  assert.ok(await focused(page.locator('#flow-title')));
  await page.locator('.wizard-alternate summary').focus();
  await page.keyboard.press('Shift+Tab');
  assert.ok(await page.evaluate(()=>document.activeElement.matches('input[name=machines]')), 'reverse Tab skipped the machine choices');
  await page.locator('input[name=machines][value=many]').check();
  await page.locator('#submit').click();
  assert.ok(await focused(page.locator('#flow-title')));
  await page.locator('#submit').click();
  await page.locator('#submit').click();
  assert.ok(await focused(page.locator('#path')), 'missing SSD folder was not focused');
  await page.getByRole('button',{name:'Done editing library location',exact:true}).click();
  await page.getByRole('button',{name:'Edit library location',exact:true}).click();
  assert.ok(await focused(page.locator('#path')), 'editing the location did not focus its input');
  await page.locator('#choose-folder').click();
  await page.waitForFunction(() => document.activeElement === document.querySelector('#choose-folder') && !document.activeElement.disabled);
  await page.locator('#path').fill('/Volumes/Test SSD');
  await page.getByRole('button',{name:'Edit library name',exact:true}).click();
  assert.ok(await focused(page.locator('#name')), 'editing the library name did not focus its input');
  await page.locator('#name').fill('Research');
  await page.locator('#submit').click();
  assert.ok(await focused(page.locator('#flow-title')));
  await shot('review');
  await page.keyboard.press('Escape');
  assert.equal(await page.locator('#name').inputValue(),'Research');
  await page.keyboard.press('Escape');
  await page.keyboard.press('Escape');
  await page.keyboard.press('Escape');
  assert.ok(await focused(page.locator('#about-title')));
  assert.ok(calls.every(call=>['/api/app/state','/api/app/choose-folder'].includes(call.path)), 'setup wrote data before final review');
});

const settingsLocal={id:'local',name:'Pending history',config:'/Libraries/Pending/library.toml',available:true,connected:true};
const settingsArchive={id:'archive',name:'My archive',config:'/Volumes/Archive/library.toml',available:true};
const settingsState={selected:settingsLocal.id,mcp:settingsLocal.id,busy:false,libraries:[settingsLocal,settingsArchive],local_library_parent:'/Users/test/Libraries'};

for(const embedded of [false,true])test(`Obsolete folder pickers leave focus in the replacement source form (${embedded?'startup':'normal shell'})`,{skip},async t=>{
  let releaseBroker,brokerStarted;
  const brokerReady=new Promise(resolve=>{brokerStarted=resolve});
  const {page}=await open(t,{url:embedded?'/app':'/settings/sources',ready:embedded?'#application-setup[open]':'#pharosAddSource',api:{
    'POST /api/app/choose-folder':()=>new Promise(resolve=>{releaseBroker=resolve;brokerStarted()}),
  }});
  if(!embedded)await page.evaluate(()=>{
    window.webkit={messageHandlers:{pharosLibrary:{postMessage:()=>new Promise((resolve,reject)=>{window.finishFolder=(fail)=>fail?reject(Error('Obsolete chooser error')):resolve('/obsolete')})}}};
  });
  const guide=page.locator('#pharosOnboarding');
  for(const purpose of (embedded?['source']:['source','export'])){
    await page.evaluate(({purpose})=>window.pharosOnboarding.open({mode:purpose==='source'?'manual':'import',kind:'claude'}),{purpose});
    const field=guide.getByRole('textbox',{name:purpose==='source'?'Claude Code folder':'Export',exact:true});
    await field.waitFor();
    const picker=guide.getByRole('button',{name:purpose==='source'?'Choose folder… Claude Code folder':'Choose file… Export',exact:true});
    await picker.evaluate(button=>{window.originalFolderButton=button});
    await picker.click();
    await page.waitForFunction(()=>window.originalFolderButton.disabled);
    if(embedded)await brokerReady;
    await guide.getByRole('button',{name:'Back',exact:true}).click();
    if(!embedded&&purpose==='source'){
      await guide.getByRole('button',{name:'Cancel',exact:true}).click();
      await page.evaluate(()=>window.pharosOnboarding.open({mode:'manual',kind:'claude'}));
    }else if(purpose==='source'){
      await guide.getByRole('radio',{name:/I’ll tell Pharos where to look/}).check();
      await guide.getByRole('button',{name:'Continue',exact:true}).click();
    }else await guide.getByRole('button',{name:'Import an export',exact:true}).click();
    const replacement=guide.getByRole('textbox',{name:purpose==='source'?'Claude Code folder':'Export',exact:true});
    await replacement.fill('/replacement');
    assert.ok(await replacement.evaluate(element=>document.activeElement===element));
    if(embedded){
      assert.ok(releaseBroker,'the startup broker chooser was not requested');
      releaseBroker({path:'/obsolete'});releaseBroker=null;
    }else await page.evaluate(fail=>window.finishFolder(fail),purpose==='export');
    await page.waitForFunction(()=>!window.originalFolderButton.disabled);
    assert.ok(await replacement.evaluate(element=>document.activeElement===element),'an obsolete picker moved focus out of the replacement form');
    // Return to the start screen for the next purpose.
    await guide.getByRole('button',{name:'Back',exact:true}).click();
    if(!embedded)await guide.getByRole('button',{name:'Cancel',exact:true}).click();
  }
});

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

test('Source setup keeps tool choices and focus while reviewing folders before approval', {skip}, async t => {
  const {page,calls,shot}=await open(t,{url:'/app',ready:'#application-setup[open]',api:{
    'GET /api/app/state':{application:'/Applications/Pharos.app',selected:'source-review-library',mcp:'',busy:false,libraries:[]},
    'POST /api/app/create':{saved:true},
    // A folder added on the found screen fails its check: the reply cannot be
    // read. (Error statuses count as test failures here.)
    'POST /api/probe':({body})=>body.locations.length?{checked:[]}:undefined,
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
  await guide.getByRole('radio',{name:/Look in the usual places/}).waitFor();
  assert.ok(await guide.locator('h2').evaluate(element=>element===document.activeElement));
  assert.ok(!calls.some(call=>call.path==='/api/probe'),'presence detection parsed source history');
  await shot('source-tools');
  await guide.getByRole('button',{name:'Continue',exact:true}).click();
  await guide.locator('.pharos-candidate').first().waitFor();
  assert.ok(await guide.locator('h2').evaluate(element=>element===document.activeElement));
  await guide.getByRole('checkbox',{name:/claude-work\/projects/}).uncheck();
  const guideButton=guide.getByRole('button',{name:'Data Guide',exact:true});
  const callsBeforeGuide=calls.length;
  await guideButton.click();
  const dictionary=page.getByRole('dialog',{name:'Data Guide',exact:true});
  await dictionary.waitFor();
  await dictionary.getByRole('button',{name:'Explain sources from Cursor',exact:true}).waitFor();
  const guideURL=new URL(page.url());
  const restored=await open(t,{url:guideURL.pathname+guideURL.search,ready:'.data-guide[open]'});
  assert.equal(await restored.page.getByRole('button',{name:'Sources',exact:true}).getAttribute('aria-current'),'page','first-run setup did not restore the selected guide term');
  assert.ok(restored.calls.every(call=>call.path.startsWith('/api/app/')),'restoring the setup guide requested library data');
  await shot('source-guide');
  await page.keyboard.press('Escape');
  assert.ok(await guideButton.evaluate(element=>element===document.activeElement),'closing the setup guide lost focus');
  assert.equal(await guide.getByRole('checkbox',{name:/claude-work\/projects/}).isChecked(),false,'opening the guide discarded source choices');
  assert.equal(calls.length,callsBeforeGuide,'the standalone guide requested library data');
  await guideButton.click();
  await dictionary.waitFor();
  assert.equal(await page.locator('#dataGuideHost').count(),1,'reopening duplicated the guide');
  await page.keyboard.press('Escape');
  await shot('source-locations');
  // A folder for another tool that fails its check leaves the list as it was.
  await guide.getByText('Add a folder Pharos didn’t find',{exact:true}).click();
  await guide.getByRole('combobox',{name:'Tool for this folder'}).selectOption('codex');
  await guide.getByRole('textbox',{name:'Folder'}).fill('/missing');
  await guide.getByRole('button',{name:'Check folder',exact:true}).click();
  await guide.locator('.pharos-error').waitFor();
  assert.ok(await guide.getByRole('button',{name:'Continue',exact:true}).isVisible(),'a failed folder check left no way on');
  assert.ok(!await guide.getByRole('checkbox',{name:/claude-work\/projects/}).isChecked(),'a failed folder check forgot a folder left out');
  await guide.getByRole('button',{name:'Continue',exact:true}).click();
  assert.ok(await guide.locator('h2').evaluate(element=>element===document.activeElement));
  await shot('source-review');
  await page.keyboard.press('Escape');
  assert.ok(await guide.getByRole('button',{name:'Continue',exact:true}).isVisible(),'Escape left the review');
  assert.ok(!await guide.getByRole('checkbox',{name:/claude-work\/projects/}).isChecked(),'going back forgot a folder left out');
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

test('Look for sources finds Downloads exports without installed tools and opens import for review', {skip}, async t => {
  const {page,calls,shot}=await open(t,{url:'/settings/sources',ready:'#pharosFindSources',api:{
    'GET /api/probe/tools':{tools:[]},
    'POST /api/probe':({body})=>({candidates:[],checked:[],downloads:body.downloads?{exports:[
      {kind:'chatgpt',path:'~/Downloads/chat.zip'},
      {kind:'claude-ai',path:'~/Downloads/manifest.json'},
      {path:'~/Downloads/unknown.zip'},
    ]}:undefined}),
  }});
  await page.locator('#pharosFindSources').click();
  const guide=page.locator('#pharosOnboarding');
  await guide.getByRole('heading',{name:'Exports in Downloads'}).waitFor();
  assert.equal(calls.find(call=>call.path==='/api/probe').body.downloads,true);
  assert.equal(await guide.getByRole('button',{name:'Review export'}).count(),3);
  assert.ok(!calls.some(call=>['/api/probe/accept','/api/chatgpt-imports','/api/claude-imports'].includes(call.path)&&call.method==='POST'));
  await shot('sources-downloads');
  await page.setViewportSize({width:390,height:900});
  assert.ok(await guide.evaluate(el=>el.scrollWidth<=el.clientWidth),'download results overflow on a narrow screen');
  for(const row of await guide.locator('.pharos-download-export').all())assert.ok(await row.evaluate(el=>{const info=el.querySelector('.pharos-download-export-info').getBoundingClientRect(),button=el.querySelector('button').getBoundingClientRect();return info.right<=button.left&&button.right<=el.getBoundingClientRect().right+1}),'export details overlap their action');
  await shot('sources-downloads-narrow');
  await page.setViewportSize({width:1440,height:900});
  await guide.locator('.pharos-download-export').filter({hasText:'~/Downloads/chat.zip'}).getByRole('button',{name:'Review export'}).click();
  assert.equal(await guide.getByLabel('Export',{exact:true}).inputValue(),'~/Downloads/chat.zip');
  assert.ok(await guide.getByRole('radio',{name:'ChatGPT',exact:true}).isChecked());
  await guide.getByRole('button',{name:'Back',exact:true}).click();
  await guide.locator('.pharos-download-export').filter({hasText:'~/Downloads/manifest.json'}).getByRole('button',{name:'Review export'}).click();
  assert.equal(await guide.getByLabel('Export',{exact:true}).inputValue(),'~/Downloads/manifest.json');
  assert.ok(await guide.getByRole('radio',{name:'Claude.ai',exact:true}).isChecked());
  await guide.getByRole('button',{name:'Back',exact:true}).click();
  await guide.locator('.pharos-download-export').filter({hasText:'~/Downloads/unknown.zip'}).getByRole('button',{name:'Review export'}).click();
  assert.ok(await guide.getByRole('button',{name:'Import',exact:true}).isDisabled());
  await guide.getByRole('radio',{name:'Claude.ai',exact:true}).check();
  assert.ok(await guide.getByRole('button',{name:'Import',exact:true}).isEnabled());
});

// Work Hours uses the shared chart; exercise inspection at both widths.
for (const [topic, display, width] of [['tokens', 'interval', 1440], ['tokens', 'cumulative', 1440], ['work_hours', 'interval', 1440], ['work_hours', 'interval', 390]]) test(`Dashboard ${topic} ${display} inspection at ${width}px focuses its links and dismisses on Escape or outside click${display === 'interval' ? '; pointer capture snaps to columns' : ''}`, { skip }, async t => {
  const { page, settled, shot } = await open(t, { url: `/analyze?${new URLSearchParams({ analyze_topic: topic, analyze_chart: JSON.stringify({ display }), highlight: 'page' })}`, viewport: { width, height: 900 }, ready: '.usage-chart-bars:not(.loading) > button' });
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
  assert.equal(new URL(page.url()).searchParams.get('highlight'), 'page', 'closing the chart details preserves linked highlights');
  assert.ok(await bar.evaluate(element => element === document.activeElement), 'Escape returns focus to the inspected period');
  await bar.click();
  await details.waitFor();
  assert.equal(await bar.getAttribute('aria-pressed'), 'true', 'pointer inspection selects the period under the pointer');
  await page.locator('.usage-chart-headline > h2').click();
  await details.waitFor({ state: 'hidden' });
  await settled();
  if (display !== 'interval') return;

  // Pointer capture snaps to columns and treats a drag back to its anchor as inspection.
  const bars = chart.locator('.usage-chart-bars > button');
  await bars.nth(1).scrollIntoViewIfNeeded();
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
  await page.locator('.usage-chart-headline > h2').click();
  await bars.nth(1).scrollIntoViewIfNeeded();
  const dragStart = await bars.nth(4).boundingBox(), dragEnd = await bars.nth(1).boundingBox();
  await page.mouse.move(dragStart.x + dragStart.width / 2, dragStart.y + 30);
  await page.mouse.down();
  await page.mouse.move(dragEnd.x + dragEnd.width / 2, dragEnd.y + 30);
  await page.mouse.up();
  await page.waitForFunction(previous => new URLSearchParams(location.search).get('analyze_filters') !== previous, new URL(before).searchParams.get('analyze_filters'));
  await settled();
});

test('Cloud backup forecast, setup, password stages and Reset keep keyboard focus', {skip}, async t => {
  const {page}=await open(t,{url:'/settings/backups',ready:'#cloud-explore'});
  const explore=page.getByRole('button',{name:'Explore cloud backup options',exact:true});
  await explore.click();
  // The price forecast keeps focus while the keyboard moves its readout (months: unit/page/cloud-backup).
  for(const chart of await page.locator('.cloud-results svg').all()){
    await chart.focus();
    for(const key of ['ArrowRight','End','Home']){
      await page.keyboard.press(key);
      assert.ok(await chart.evaluate(input=>input===document.activeElement));
    }
  }
  // Setup focuses each invalid field.
  await page.getByLabel('Cloud provider',{exact:true}).selectOption('gcp');
  await page.getByRole('button',{name:'Configure this backup',exact:true}).click();
  assert.ok(await page.locator('#cloud-step-1').evaluate(panel=>panel===document.activeElement));
  await page.getByRole('button',{name:'Review configuration & commands',exact:true}).click();
  assert.ok(await page.getByLabel('Google Cloud project ID',{exact:true}).evaluate(input=>input===document.activeElement));
  await page.getByLabel('Google Cloud project ID',{exact:true}).fill('pharos-example');
  const project=page.getByLabel('Google Cloud project',{exact:true});
  await project.selectOption('new');
  await page.getByRole('button',{name:'Review configuration & commands',exact:true}).click();
  assert.ok(await page.getByLabel('Billing account ID',{exact:true}).evaluate(input=>input===document.activeElement));
  // Password stages keep focus while copying each independent block.
  await project.selectOption('existing');
  await page.getByLabel('Google Cloud project ID',{exact:true}).fill('pharos-example');
  await page.getByLabel('Require a password to restore',{exact:true}).selectOption('true');
  await page.getByRole('button',{name:'Review configuration & commands',exact:true}).click();
  for(const [index,label]of ['Copy preparation commands','Copy password setup command','Copy final commands'].entries()){
    const button=page.getByRole('button',{name:label,exact:true});
    await deferredCopyKeepsFocus(page,button,{fail:index===1});
    if(index===1)await deferredCopyKeepsFocus(page,button);
  }
  await deferredCopyKeepsFocus(page,page.getByRole('button',{name:'Copy final commands',exact:true}),{moveFocusTo:page.getByRole('button',{name:'Reset cloud backup options',exact:true})});
  // Reset focuses the calculator; closing returns to Explore.
  await page.getByRole('button',{name:'Reset cloud backup options',exact:true}).click();
  assert.ok(await page.locator('#cloud-step-0').evaluate(panel=>panel===document.activeElement));
  await page.keyboard.press('Tab');
  assert.ok(await page.getByLabel('Cloud provider',{exact:true}).evaluate(input=>input===document.activeElement));
  await page.getByRole('button',{name:'Close cloud backup options',exact:true}).click();
  assert.ok(await explore.evaluate(input=>input===document.activeElement));
});

async function deferredCopyKeepsFocus(page,button,{fail=false,moveFocusTo=null}={}){
  await page.evaluate(()=>{
    delete window.finishClipboardWrite;
    Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:()=>new Promise((resolve,reject)=>{
      window.finishClipboardWrite=fail=>fail?reject(new Error('Clipboard denied')):resolve();
    })}});
  });
  await button.focus();await page.keyboard.press('Enter');
  await page.waitForFunction(()=>typeof window.finishClipboardWrite==='function');
  // Cross a rendering frame while the clipboard promise is still pending.
  await page.evaluate(()=>new Promise(requestAnimationFrame));
  assert.ok(await button.evaluate(element=>element===document.activeElement),'a pending clipboard write keeps keyboard focus');
  if(moveFocusTo)await moveFocusTo.focus();
  await page.evaluate(async fail=>{window.finishClipboardWrite(fail);await new Promise(requestAnimationFrame);},fail);
  assert.ok(await (moveFocusTo??button).evaluate(element=>element===document.activeElement),'clipboard completion preserves the current keyboard focus');
}

test('Reader file copying keeps keyboard focus through delayed success, failure and retry', {skip},async t=>{
  const {page}=await open(t,{url:`/work/${workID}/conversation/conv-root`,ready:'.file-copy',api:{
    'GET /api/work/:id/conversations/:conversation':call=>{
      const body=defaultApi['GET /api/work/:id/conversations/:conversation'](call);
      const message=(id,role,kind,text,extra={})=>({id,native_id:id,role,kind,text:JSON.stringify(text),created_at:'2026-10-01T00:00:00Z',...extra});
      return {...body,messages:[message('prompt','user','message','Read this file'),
        message('read','assistant','tool_call',{name:'Read',input:{file_path:'/repo/notes.txt'}},{call_id:'read'}),
        message('result','tool','tool_result',{content:'Notes'},{call_id:'read'})]};
    },
  }});
  const button=page.getByRole('button',{name:'Copy path /repo/notes.txt',exact:true});
  await deferredCopyKeepsFocus(page,button);
  await deferredCopyKeepsFocus(page,button,{fail:true});
  await deferredCopyKeepsFocus(page,button);
  await deferredCopyKeepsFocus(page,button,{moveFocusTo:page.locator('#settingsToggle')});
});

test('Sync diagnostics copying keeps keyboard focus through delayed success, failure and retry', {skip}, async t=>{
  const issue={id:'mismatch',source_name:'codex',unit:'/sessions/rollout.jsonl'};
  const {page}=await open(t,{url:'/settings/sync',ready:'.sync-warning',api:{
    'GET /api/sync/status':{...defaultApi['GET /api/sync/status'],busy:true,maintenance_busy:true,issues:[issue]},
    'GET /api/sync/issues':{issues:[issue]},
  }});
  await page.getByRole('button',{name:'View details / copy diagnostics',exact:true}).click();
  const dialog=page.locator('dialog.sync-dialog');
  const button=dialog.getByRole('button',{name:'Copy diagnostics',exact:true});
  await deferredCopyKeepsFocus(page,button);
  await deferredCopyKeepsFocus(page,button,{fail:true});
  await deferredCopyKeepsFocus(page,button);
  await deferredCopyKeepsFocus(page,button,{moveFocusTo:dialog.getByRole('button',{name:'Close',exact:true})});
});

test('Feedback inbox report copying keeps keyboard focus through delayed success, failure and retry', {skip}, async t=>{
  const id='00000000-0000-4000-8000-000000000001',received_at='2026-10-01T00:00:00Z';
  const application={name:'Example app',version:'1.0'};
  const {page}=await open(t,{inbox:true,url:`/?submission=${id}`,ready:'.report-detail',api:{
    'GET /api/submissions':{submissions:[{id,application,kind:'bug',comment:'Synthetic report',received_at,annotation_count:0,screenshot_count:0,has_diagnostics:false,tags:[],revision:'1'}]},
    [`GET /api/submissions/${id}`]:{version:1,id,application,kind:'bug',comment:'Synthetic report',annotations:null,received_at,tags:[],revision:'1'},
  }});
  const button=page.getByRole('button',{name:'Copy report for an agent',exact:true});
  await deferredCopyKeepsFocus(page,button);
  await deferredCopyKeepsFocus(page,button,{fail:true});
  await deferredCopyKeepsFocus(page,button);
  await deferredCopyKeepsFocus(page,button,{moveFocusTo:page.getByRole('button',{name:'Download report',exact:true})});
});

test("summary value links retain keyboard focus when the summary rerenders", { skip }, async t => {
  const { page, settled, shot } = await open(t, { url: "/library?view=table", ready: "#queryTableLibrary .qt-row", preferences: { "pharos-usage-summary-links": true, "pharos-quick-stats-v1": {metrics: ["cost_usd", "tokens"], windows: ["today", "6h"]} } });
  await activateHeaderTool(page, "usageSummaryToggle");
  await settled();
  const link = page.getByRole("link", { name: /^Tokens, 6h:/ });
  await link.focus();
  await page.evaluate(() => window.dispatchEvent(new Event("pharos:carbon-settings")));
  await page.waitForFunction(() => document.activeElement?.getAttribute("aria-label")?.startsWith("Tokens, 6h:"));
  await page.keyboard.press("Escape");
  await page.locator("#usageSummary").waitFor({ state: "hidden" });
  await page.waitForFunction(() => document.activeElement?.id === "usageSummaryToggle");
  // A full reload restores the saved card while its fresh request is pending.
  let finish;
  const pending = new Promise(resolve => {finish = resolve;});
  t.after(() => finish());
  await page.route('**/api/usage/summary*', async route => {await pending; await route.continue();});
  await page.reload();
  await activateHeaderTool(page, 'usageSummaryToggle');
  await link.waitFor();
  await link.focus();
  await shot('quick-stats-cached-refresh');
  finish(); await settled();
  await page.waitForFunction(() => document.activeElement?.getAttribute('aria-label')?.startsWith('Tokens, 6h:'));
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
// across a pushed snapshot. Stop and Eject decisions are unit tested in
// web/tests/unit/page/status-panel.test.mjs.
test("Unified activity panel holds focus and its disclosure across refreshes", { skip }, async t => {
  const snapshot = {...busyStatus, operations: busyStatus.operations.map(operation => operation.id === 'op-index'
    ? {...operation, children: [], children_count: 0} : operation)};
  const app = await open(t, { url: "/library?view=table", ready: "#queryTableLibrary .qt-row", api: {
    "GET /api/status/stream": eventStream(snapshot),
    "GET /api/status": snapshot,
  } });
  const { page, push } = app;
  const chip = page.locator("#pharosDrive");
  await page.waitForFunction(() => window.pharosStatusStore.current()?.view?.library?.revision === 2);
  await press(chip);
  const panel = page.getByRole("dialog", { name: "Library drive", exact: true });
  await panel.waitFor();
  await holdsFocus(page, panel, chip);
  assert.equal(await page.locator("#pharosDrivePanel").count(), 0, "the old drive panel stays closed");

  await press(chip);
  await panel.waitFor();
  // Daily is closed in this snapshot: open it.
  const daily = panel.locator('[data-key="cadence:daily"]');
  await daily.locator("summary").click();
  const stop = panel.getByRole("button", { name: "Stop", exact: true });
  await stop.focus();
  push("/api/status/stream", { ...snapshot, revision: 3, operations: snapshot.operations.map(operation => operation.id === "op-index" ? { ...operation, step: { ...operation.step, done: 900e6, fraction: 0.45 } } : operation) });
  await page.waitForFunction(() => window.pharosStatusStore.current()?.view?.library?.revision === 3);
  assert.ok(await stop.evaluate(element => document.activeElement === element), "a refresh moved focus");
  assert.ok(await daily.evaluate(element => element.open), "a refresh closed the cadence row the user opened");
  const progress = panel.getByRole('progressbar', { name: 'Index changes progress', exact: true });
  assert.notEqual(await progress.getAttribute('aria-valuenow'), null);
  push('/api/status/stream', {...snapshot, revision: 4, operations: snapshot.operations.map(operation => operation.id === 'op-index'
    ? {...operation, step: {...operation.step, total: null, fraction: null}} : operation)});
  await page.waitForFunction(() => window.pharosStatusStore.current()?.view?.library?.revision === 4);
  assert.equal(await progress.getAttribute('aria-valuenow'), null, 'a new unknown-total phase must remove the old percentage');
  assert.ok(await stop.evaluate(element => document.activeElement === element), 'changing progress mode moved focus');
  push('/api/status/stream', {...snapshot, revision: 5});
  await page.waitForFunction(() => window.pharosStatusStore.current()?.view?.library?.revision === 5);
  assert.notEqual(await progress.getAttribute('aria-valuenow'), null, 'measured progress returns when inventory is known');
  const detail = 'Waiting for the catalog writer';
  const child = {id: 'git', kind: 'analysis.git', label: 'Check repositories', state: 'running', state_label: 'Checking repositories',
    step: {name: 'Checking Git history', unit: 'repository', done: 2, total: 5, fraction: .4}, children: []};
  const waiting = {state: 'blocked', state_label: 'Waiting', detail};
  for (const [index, ancestor] of [
    {...snapshot.operations[0], ...waiting, children: [child]},
    {...snapshot.operations[0], children: [{id: 'chain', kind: 'analysis.chain', label: 'Update analyses', ...waiting, children: [child]}]},
  ].entries()) {
    const revision = 6 + index;
    push('/api/status/stream', {...snapshot, revision, operations: snapshot.operations.map(operation => operation.id === 'op-index' ? ancestor : operation)});
    await page.waitForFunction(revision => window.pharosStatusStore.current()?.view?.library?.revision === revision, revision);
    await panel.getByText(detail).waitFor();
    await panel.getByRole('progressbar', {name: `${child.label} progress`, exact: true}).waitFor();
    assert.ok(await stop.evaluate(element => document.activeElement === element), 'an ancestor wait moved focus');
  }
  await app.shot("refreshed");
  // The resource history: keys inspect and pin a time, and only keyboard
  // inspection is announced; Escape first clears the inspection, keeping
  // focus and the panel, and only then closes the panel.
  const strip = panel.locator('[data-history]');
  const announced = () => strip.getByRole('status').textContent();
  await strip.focus();
  await page.keyboard.press('ArrowLeft');
  await page.waitForFunction(() => document.querySelector('#pharosStatusPanel [data-history] [role=status]')?.textContent);
  const reading = await announced();
  await page.keyboard.press('Enter');
  await page.waitForFunction(text => document.querySelector('#pharosStatusPanel [data-history] [role=status]')?.textContent !== text, reading);
  const pinned = await announced();
  const box = await strip.locator('svg').first().boundingBox();
  await page.mouse.move(box.x + box.width * 0.2, box.y + box.height / 2);
  await page.mouse.move(box.x + box.width * 0.6, box.y + box.height / 2);
  assert.equal(await announced(), pinned, 'moving the pointer announced a reading');
  await strip.focus();
  await page.keyboard.press('Escape');
  await page.waitForFunction(text => document.querySelector('#pharosStatusPanel [data-history] [role=status]')?.textContent !== text, pinned);
  assert.ok(await panel.isVisible(), 'Escape on an inspected strip closed the panel');
  assert.ok(await strip.evaluate(element => document.activeElement === element), 'clearing the inspection moved focus');
  await page.keyboard.press('Escape');
  await panel.waitFor({ state: 'detached' });
  assert.ok(await chip.evaluate(element => document.activeElement === element), 'closing did not return focus to the chip');
});

// Eject when safe keeps checking while the window is hidden: a hidden page
// holds no stream, so no snapshot says the blocker ended, and the wait
// checks the fresh verdict itself once snapshots have been quiet.
test("Eject when safe ejects once safe while the page is hidden", { skip }, async t => {
  const unsafe = { verdict: "unsafe", summary: "Not safe to eject: indexing sources.", headline: "Not safe to eject: indexing sources",
    blockers: [{ label: "Indexing sources", on_eject: "Not registered.", verdict: "unsafe" }], checked: "polled" };
  let fresh = { ...unsafe, checked: "fresh" };
  const snapshot = { ...busyStatus, eject: unsafe };
  const { page } = await open(t, { url: "/library?view=table", ready: "#queryTableLibrary .qt-row", api: {
    "GET /api/status/stream": eventStream(snapshot),
    "GET /api/status": ({ query }) => query.get("fresh") === "1" ? { ...snapshot, eject: fresh } : snapshot,
  } });
  await page.evaluate(() => { window.ejects = 0; window.webkit = { messageHandlers: { pharosLibrary: { postMessage: async () => { window.ejects++; return {released: true}; } } } }; });
  await page.waitForFunction(() => window.pharosStatusStore.current()?.view?.library);
  await press(page.locator("#pharosDrive"));
  const panel = page.getByRole("dialog", { name: "Library drive", exact: true });
  await panel.getByRole("button", { name: /Eject when safe/ }).click();
  await panel.getByRole("button", { name: /Waiting to eject/ }).waitFor();
  await page.evaluate(() => {
    Object.defineProperty(document, "hidden", { configurable: true, get: () => true });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await page.waitForFunction(() => window.pharosStatusStore.current()?.mode === "paused");
  fresh = { verdict: "safe", summary: "Safe", blockers: [], checked: "fresh" };
  await page.waitForFunction(() => window.ejects === 1, null, { timeout: 20000 });
});

for (const width of [1440, 320]) test(`Data guide takes focus, keeps controls reachable and returns focus @ ${width}`, { skip }, async t => {
  const app = await open(t, { url: "/analyze?analyze_mode=query&analyze_data=tool_calls&analyze_topic=tools", viewport: { width, height: 900 }, ready: ".usage-workspace-table .qt-row" });
  const { page } = app;
  const opener = page.locator(".usage-workspace-table .data-source-link");
  await app.shot("from-statement");
  await press(opener);
  const dialog = page.getByRole("dialog", { name: "Data Guide", exact: true });
  await dialog.waitFor();
  await app.shot("tool-call-guide");
  const review = dialog.getByRole("button", { name: "Conversation Review", exact: true });
  await press(review);
  await page.waitForFunction(() => new URLSearchParams(location.search).get('guide_example') === 'review');
  assert.equal(await review.evaluate(element => element === document.activeElement), true, 'a surviving conversation control keeps focus');
  const terms = dialog.getByRole("combobox", { name: "Guide term", exact: true });
  const choose = async (value, label) => {
    if (width < 760) { await terms.focus(); await terms.selectOption(value); }
    else await dialog.getByRole('navigation', { name: 'Data Guide terms' }).getByRole('button', { name: label, exact: true }).click();
    await page.waitForFunction(value => new URLSearchParams(location.search).get('guide_entity') === value, value);
  };
  const follow = async (link, entity) => {
    const previous = await link.elementHandle();
    await press(link);
    await page.waitForFunction(value => new URLSearchParams(location.search).get('guide_entity') === value, entity);
    await page.waitForFunction(element => !element.isConnected, previous);
    assert.equal(await dialog.evaluate(element => element.contains(document.activeElement)), true, 'replacing the focused illustration keeps focus in the guide');
    assert.equal(await page.evaluate(() => document.activeElement.getClientRects().length > 0), true, 'the replacement focus target is visible');
    await page.keyboard.press('Tab');
    assert.equal(await dialog.evaluate(element => element.contains(document.activeElement)), true, 'Tab continues through guide controls');
  };
  await choose('work_items', 'Work items');
  await follow(dialog.getByRole('button', { name: 'Explain workspaces', exact: true }), 'workspaces');
  await follow(dialog.getByRole('button', { name: 'Conversation Review', exact: true }), 'conversations');
  await choose('work_items', 'Work items');
  await follow(dialog.getByRole('button', { name: 'Explain workspaces', exact: true }), 'workspaces');
  await follow(dialog.getByRole('button', { name: 'Conversation Implementation', exact: true }), 'conversations');
  if (width >= 760) {
    const conversationGroup = dialog.locator('summary').filter({hasText: /^Conversations$/});
    await press(conversationGroup);
    await choose('repositories', 'Repositories');
    await follow(dialog.getByRole('button', {name: 'Conversation Review', exact: true}), 'conversations');
    // Keep later selections available, whether focus recovery used the closed
    // summary or revealed the destination term.
    if (!await conversationGroup.evaluate(element => element.parentElement.open)) await press(conversationGroup);
  }
  await choose('libraries', 'Libraries');
  const finder = await dialog.getByLabel('Finder window', {exact:true}).elementHandle();
  const applications = dialog.getByRole('button', {name:'Show Applications',exact:true});
  await press(applications);
  await dialog.getByRole('button', {name:'Explain sources from Cursor',exact:true}).waitFor();
  assert.equal(await applications.evaluate(element=>element===document.activeElement),true,'changing Finder locations lost focus');
  assert.equal(await finder.evaluate(element=>element.isConnected),true,'changing locations replaced the Finder window');
  await press(dialog.getByRole('button', {name:'Show Pharos library',exact:true}));
  await dialog.getByRole('button', {name:'Explain libraries',exact:true}).waitFor();
  await follow(dialog.getByRole('button', { name: 'Explain sources', exact: true }).first(), 'sources');
  await choose('agents', 'Agent sessions');
  const helper = dialog.getByRole('button', { name: 'Explain agent sessions', exact: true }).first();
  assert.equal(await helper.evaluate(element => {
    const bounds = element.getBoundingClientRect(), history = element.closest('.guide-chat-history').getBoundingClientRect(), dialog = element.closest('dialog').getBoundingClientRect();
    const picker = element.closest('dialog').querySelector('.data-guide-picker');
    const top = Math.max(history.top, picker?.getClientRects().length ? picker.getBoundingClientRect().bottom : dialog.top);
    return bounds.top >= top && bounds.bottom <= Math.min(history.bottom, dialog.bottom);
  }), true, 'explaining sessions scrolls a delegated session into view');
  await press(helper);
  assert.equal(await helper.evaluate(element => element === document.activeElement), true, 'explaining an inline helper retains focus');
  for (const [term, label] of [['skills', 'skills and instructions'], ['compactions', 'context compactions'], ['tokens', 'tokens and api cost'], ['models', 'models and effort'], ['channels', 'channels'], ['agent_apps', 'agent apps']]) {
    const link = dialog.getByRole('button', { name: `Explain ${label}`, exact: true }).first();
    await press(link);
    await page.waitForFunction(value => new URLSearchParams(location.search).get('guide_entity') === value, term);
    assert.equal(await link.evaluate(element => element === document.activeElement), true, 'switching between parts of the same illustration retains keyboard focus');
  }
  const laterUsage = dialog.getByRole('button', { name: 'Explain tokens and api cost', exact: true }).last();
  await press(laterUsage);
  await page.waitForFunction(() => new URLSearchParams(location.search).get('guide_entity') === 'tokens');
  assert.equal(await laterUsage.evaluate(element => element === document.activeElement), true, 'a link later in the conversation keeps focus when the guide highlights repeated usage');
  assert.equal(await laterUsage.evaluate(element => {
    const bounds = element.getBoundingClientRect(), viewport = element.closest('.guide-chat-history').getBoundingClientRect();
    return bounds.top >= viewport.top && bounds.bottom <= viewport.bottom;
  }), true, 'the focused usage link stays in view rather than scrolling to an earlier response');
  await choose('tool_calls', 'Tool calls');
  await holdsFocus(page, dialog, opener);
  await press(opener);
  await dialog.getByRole("button", { name: "Close data guide", exact: true }).click();
  await page.waitForFunction(element => document.activeElement === element, await opener.elementHandle());
});

// Exercise the guide alongside native grouping, filter and aggregate editors.
{
  const [dataset, path, numeric] = ["tool_calls", "/analyze?analyze_mode=query&analyze_data=tool_calls", "duration_ms"];
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
    const refreshed = page.waitForResponse(response => {
      if (new URL(response.url()).pathname !== `/api/query/${dataset}/query-v3` || response.request().method() !== 'POST') return false;
      const request = response.request().postDataJSON();
      return request?.rows && request.grouping?.summaries?.some(summary => summary.id === 'total' && summary.op === 'avg');
    });
    await aggregate.selectOption("avg");
    assert.equal(await aggregate.inputValue(), "avg");
    // Wait for the scheduled refresh as well as in-flight requests. Opening a
    // popover before the aggregation refresh starts can race its focus return.
    await refreshed;
    await app.settled();
    for (const name of ["Add filter", "Add HAVING filter", "Customize columns"]) {
      const opener = page.getByRole("button", { name, exact: true });
      await press(opener);
      const editor = page.getByRole("dialog");
      await page.waitForFunction(element => element.contains(document.activeElement), await editor.elementHandle());
      await page.keyboard.press("Escape");
      await editor.waitFor({ state: "hidden" });
      await page.waitForFunction(element => document.activeElement === element, await opener.elementHandle());
    }
    const guideLink = page.locator(".data-source-link");
    await press(guideLink);
    await holdsFocus(page, page.getByRole("dialog", { name: "Data Guide", exact: true }), guideLink);
    await app.shot(`${dataset}-grouping-with-source`);
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
  const { page } = await open(t, { url: "/library?view=table", ready: "#queryTableLibrary .qt-row" });
  const dialog = page.locator("#searchDialog"), button = page.locator("#searchToggle");
  await press(button);
  await useSearchMenus(page, dialog);
  await holdsFocus(page, dialog, button);
  const tab = page.getByRole('navigation', { name: 'Primary navigation', exact: true }).getByRole('button', { name: 'Analyze', exact: true });
  await press(tab, "ControlOrMeta+k");
  await holdsFocus(page, dialog, tab);
});

{
  // 1024 px is the narrowest width that keeps the sidebar beside the page.
  const viewport = { width: 1024, height: 768 };
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
    await press(sidebar.locator('summary').filter({ hasText: /^People$/ }));
    await press(sidebar.getByRole('link', { name: /^Human words$/i }));
    await page.locator('.usage-human-dashboard .usage-chart').waitFor();
    await page.waitForFunction(() => document.activeElement?.tagName === 'MAIN');
    assert.equal(await sidebar.getByRole('link', { name: /^Human words$/i }).getAttribute('aria-current'), 'page');
  });
}

test('navigation and header focus stay reachable when resizing between layouts', { skip }, async t => {
  const { page, settled } = await open(t, { url: '/analyze', viewport: { width: 1280, height: 720 }, ready: '.usage-dashboard .usage-chart' });
  // The section sidebar hands focus to the phone switcher and back.
  const sidebar = page.getByRole('complementary', { name: 'Analyze navigation', exact: true });
  await sidebar.getByRole('link', { name: 'Tokens and Cost', exact: true }).focus();
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

  // A header tool that stays inline keeps focus through resizes.
  await page.setViewportSize({ width: 1440, height: 900 });
  await settled();
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

  // A header tool that moves into the overflow menu hands focus to it and back.
  await page.locator('#searchToggle').focus();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForFunction(() => document.activeElement === document.getElementById('headerMore'));
  await press(page.locator('#headerMore'));
  await page.locator('#searchToggle').focus();
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.waitForFunction(() => document.activeElement === document.getElementById('searchToggle'));
});

test("Search content search takes focus and returns it to the query builder @ 390", { skip }, async t => {
  const { page } = await open(t, { url: "/library?view=table", viewport: { width: 390, height: 844 }, ready: "#queryTableLibrary .qt-row" });
  const button = page.getByRole("button", { name: "Messages", exact: true });
  await press(button);
  const dialog = page.getByRole("dialog", { name: "Search within this workspace", exact: true });
  await useSearchMenus(page, dialog);
  await holdsFocus(page, dialog, button);
});

test("Search long query can scroll to submit @ 320", { skip }, async t => {
  const width = 320;
  const draft = {
    editor: "visual", match: "rules", text: "", mode: "advanced", fuzzy: false, caseSensitive: false, separators: false,
    scope: { surface: "prose", roles: [], kinds: [], repository: "", provider: "", source: "", from: "", to: "", conversation_id: "", workspace_id: "" },
    root: { id: "long-query", kind: "group", join: "all", exclude: false,
      children: Array.from({ length: 12 }, (_, index) => ({ id: `rule-${index}`, kind: "words", text: `condition ${index + 1}`, other: "", distance: "8", exclude: false })) },
  };
  const app = await open(t, { url: `/library?view=table&${new URLSearchParams({ text_builder: JSON.stringify(draft) })}`, viewport: { width, height: 600 }, ready: "#queryTableLibrary .qt-row",
    api: { "POST /api/search/text/validate": ({ body }) => ({ where: body.where }) } });
  const { page } = app;
  await press(page.getByRole("button", { name: "Edit messages search", exact: true }));
  const dialog = page.getByRole("dialog", { name: "Search within this workspace", exact: true });
  const heading = dialog.getByRole("heading", { name: "Search within this workspace", exact: true });
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

{
  const viewport = { width: 390, height: 844 };
  test(`app navigation takes and returns focus, and reaches other app sections at ${viewport.width}px`, { skip }, async t => {
    const { page, shot } = await open(t, { url: '/analyze', viewport, ready: '.usage-dashboard .usage-chart' });
    const opener = page.locator('#appNavigationToggle'), dialog = page.getByRole('dialog', { name: 'Navigate Pharos' });
    await press(opener);
    await holdsFocus(page, dialog, opener);
    await press(opener);
    const outcomes = dialog.locator('summary').filter({ hasText: /^Outcomes$/ });
    await press(outcomes);
    assert.equal(await dialog.getByRole('link', { name: 'Tokens and Cost', exact: true }).isVisible(), false);
    await page.keyboard.press('Escape');
    await dialog.waitFor({ state: 'hidden' });
    await page.waitForFunction(element => document.activeElement === element, await opener.elementHandle());
    await press(opener);
    await page.waitForFunction(element => document.activeElement === element, await outcomes.elementHandle());
    await press(outcomes);
    const people = dialog.locator('summary').filter({ hasText: /^People$/ });
    await press(people);
    await page.waitForFunction(element => {
      const rect = element.getBoundingClientRect();
      return element.contains(document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2));
    }, await people.elementHandle());
    await shot('nested-dashboards');
    await dialog.getByRole('link', { name: 'TL1 attempts', exact: true }).scrollIntoViewIfNeeded();
    assert.equal(await onScreen(dialog.getByRole('button', { name: 'All pages', exact: true })), true, 'scrolling keeps the way back reachable');
    assert.equal(await onScreen(dialog.getByRole('button', { name: 'Close navigation', exact: true })), true, 'scrolling keeps the close control reachable');
    await shot('scrolled-navigation');
    await press(dialog.getByRole('link', { name: 'Human Words', exact: true }));
    await page.locator('.usage-human-dashboard .usage-chart').waitFor();
    await page.waitForFunction(() => document.activeElement?.tagName === 'MAIN');
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

{
  const viewport = { width: 390, height: 844 };
  test(`overflowed header tools remain live and restore focus at ${viewport.width}px`, { skip }, async t => {
    const { page } = await open(t, { url: '/library?view=table', viewport, ready: '#queryTableLibrary .qt-row',
      preferences: { 'pharos-header-tools-v1': ['guide', 'summary', 'theme', 'history', 'library', 'search', 'settings'] },
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
    const guideButton = page.locator('#dataGuideToggle');
    assert.equal(await guideButton.getAttribute('aria-expanded'), 'false');
    await activateHeaderTool(page, 'dataGuideToggle');
    const guide = page.getByRole('dialog', {name:'Data Guide',exact:true});
    await guide.waitFor();
    await page.waitForFunction(() => document.getElementById('dataGuideToggle').getAttribute('aria-expanded') === 'true');
    await holdsFocus(page, guide, more);
    await page.waitForFunction(() => document.getElementById('dataGuideToggle').getAttribute('aria-expanded') === 'false');
  });
}

test('Optimize pages move keyboard focus to their content at 1440px', { skip }, async t => {
  const { page, settled, shot } = await open(t, { url: '/findings', viewport: { width: 1440, height: 900 }, ready: '.findings-card' });
  const navigation = page.getByRole('complementary', { name: 'Optimize navigation', exact: true });
  async function choose(name, ready) {
    // The Safety area has its own Table and Settings links.
    await press(navigation.getByRole('link', { name, exact: true }).and(page.locator(':not([href*="optimize_area"])')));
    await page.locator(ready).first().waitFor();
    await page.waitForFunction(() => document.activeElement?.tagName === 'MAIN');
  }
  await choose('Settings', '.findings-settings .findings-panel');
  await choose('Table', '.findings-table .qt-row');
  // Settings bypasses the empty-library state without remounting the page,
  // so the retained findings table survives the round trip.
  const table = await page.locator('.findings-table').elementHandle();
  await choose('Settings', '.findings-settings .findings-panel');
  await choose('Table', '.findings-table .qt-row');
  assert.ok(await table.evaluate(element => element.isConnected), 'Findings Settings keeps the findings table mounted');
  // Pressing non-focusable content focuses its nearest focusable ancestor
  // (every press on a button, in WebKit). Once focus has left the content
  // landmark, a press inside a popover must not move focus out and close it.
  await press(page.getByRole('button', { name: 'Add group by', exact: true }));
  const picker = page.getByRole('dialog', { name: 'Group rows by', exact: true });
  await picker.locator('.qt-picker-heading').click();
  assert.ok(await picker.isVisible(), 'A press inside the picker keeps it open after navigation focused the content');
  await page.getByRole('heading', { level: 1 }).click();
  await picker.waitFor({ state: 'hidden' });
  await choose('Dashboard', '.findings-card');
  await settled();
  await shot('optimize-findings');
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

test('header editing keeps keyboard focus on the handle being changed', { skip }, async t => {
  const { page, settled, shot } = await open(t, { url: '/settings/header?highlight=page', ready: '#headerActive .header-tool' });
  const guideButton=page.locator('#dataGuideToggle');
  assert.equal(await guideButton.isVisible(),false,'the Data Guide shortcut starts in the toolbox');
  const add = page.getByRole('button', { name: 'Add Navigation bar', exact: true });
  const handle = page.getByRole('button', { name: 'Arrange Navigation bar', exact: true });
  await press(add);
  await page.waitForFunction(element => document.activeElement === element, await handle.elementHandle());
  await press(handle);
  await page.locator('#headerToolMenu:popover-open').waitFor();
  await page.getByRole('button', { name: 'Move left Navigation bar', exact: true }).click({ button: 'right' });
  await page.locator('.pharos-attention-menu button').click();
  assert.equal(new URL(page.url()).searchParams.has('highlight'), false, 'clear highlights is clickable above the native popover');
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
  await press(page.getByRole('button',{name:'Add Data Guide',exact:true}));
  const guideHandle=page.getByRole('button',{name:'Arrange Data Guide',exact:true});
  await page.waitForFunction(element=>document.activeElement===element,await guideHandle.elementHandle());
  await settled();
  await page.reload();
  await guideHandle.waitFor();
  await activateHeaderTool(page,'dataGuideToggle');
  await holdsFocus(page,page.getByRole('dialog',{name:'Data Guide',exact:true}),guideButton);
  await shot('data-guide-shortcut');
  await press(guideHandle,'Delete');
  await page.getByRole('button',{name:'Add Data Guide',exact:true}).waitFor();
  assert.equal(await guideButton.isVisible(),false,'removing the shortcut returns it to the toolbox');
});

// Both drawers share setDrawer; History stands for them, inline and in More.
const [toggle, close] = ["historyToggle", "historyClose"];
for (const width of [1440, 390]) test(`History close restores reachable focus at ${width}px`, { skip }, async t => {
  const { page } = await open(t, { url: "/library?view=table", viewport: { width, height: 900 }, ready: "#queryTableLibrary .qt-row", preferences: { "pharos-header-tools-v1": ["navigator", "settings"] } });
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

test("Home search button focuses its input", { skip }, async t => {
  const { page } = await open(t, { url: "/", ready: ".home-belt-row .query-card" });
  await press(page.getByRole("button", { name: "Search past work", exact: true }));
  await page.waitForFunction(() => document.activeElement?.matches(".home-search-content textarea, .home-search-content input"));
  const composer = page.getByRole("region", { name: "Search composer", exact: true });
  await composer.getByRole("combobox", { name: "Date comparison", exact: true }).selectOption("all");
  const add = composer.locator(".text-query-tools").getByRole("button", { name: "Add condition", exact: true });
  await page.waitForFunction(element => document.activeElement === element, await add.elementHandle());
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

test("the findings prompt review takes focus, keeps it and returns it on Escape", { skip }, async t => {
  const { page, settled } = await open(t, { url: "/findings?highlight=page", ready: ".findings-card" });
  const review = page.locator(".findings-cart button.primary").first();
  await press(review);
  const dialog = page.locator('.findings-dialog[role="dialog"]');
  await dialog.waitFor();
  await settled();
  await dialog.getByRole('heading').first().click({ button: 'right' });
  await page.locator('.pharos-attention-menu button').waitFor();
  await page.keyboard.press('Escape');
  assert.equal(await dialog.isVisible(), true, 'Escape dismisses only the context menu');
  assert.equal(await page.locator('.pharos-attention-menu').count(), 0);
  assert.equal(new URL(page.url()).searchParams.get('highlight'), 'page');
  await holdsFocus(page, dialog, review);
});

for (const scenario of [
  { name: "Risky action", url: "/findings?optimize_area=safety&view=table&safety_case=safety_sample", api: "/api/findings/safety", id: "safety_sample", collection: "cases", detail: ".safety-detail", remove: "In prompt · Remove", add: "Add to prompt" },
  { name: "Fix", url: "/findings?optimize_area=fixes&view=table&fixes_item=fix_sample", api: "/api/findings/fixes", id: "fix_sample", collection: "items", detail: ".fixes-detail", remove: "In prompt · Remove", add: "Add to prompt" },
  { name: "Efficiency finding", url: "/findings?finding=failure%3Arepo%3Arepo_explo%3Adef", api: "/api/findings", id: "failure:repo:repo_explo:def", collection: "findings", detail: ".findings-detail", remove: "Remove from prompt", add: "Add to Prompt Cart" },
]) {
  test(`${scenario.name} detail keeps its prompt cart usable`, { skip }, async t => {
    const overview = structuredClone(defaultApi[`GET ${scenario.api}`]);
    const card = overview[scenario.collection].find(item => item.id === scenario.id);
    const membership = structuredClone(card.cart);
    const cart = structuredClone(overview.cart);
    const initialDetail = defaultApi[`GET ${scenario.api}/:id`]({ params: { id: scenario.id } });
    const api = {
      [`GET ${scenario.api}`]: () => overview,
      [`GET ${scenario.api}/:id`]: () => ({ ...initialDetail, cart: card.cart }),
      [`POST ${scenario.api}/:id/action`]: ({ body }) => {
        if (body.action === "cart_remove") { card.cart = null; overview.cart = []; }
        if (body.action === "cart_add") { card.cart = membership; overview.cart = structuredClone(cart); }
        return { ok: true };
      },
    };
    const { page, shot } = await open(t, { url: scenario.url, viewport: { width: 1920, height: 1050 }, api, ready: scenario.detail });
    const cartPanel = page.locator(".findings-cart:visible");
    await cartPanel.locator("li").waitFor();
    assert.equal(await cartPanel.count(), 1, "the detail has one visible prompt cart");
    assert.equal(await page.locator("#findingsCart").count(), 1, "the hidden table does not duplicate the cart");
    await shot("cart");
    await cartPanel.locator("button.primary").click();
    const dialog = page.locator('.findings-dialog[role="dialog"]');
    await dialog.waitFor();
    await dialog.getByRole("button", { name: "Keep in cart" }).click();
    await page.locator(scenario.detail).getByRole("button", { name: scenario.remove, exact: true }).click();
    await cartPanel.locator(".findings-cart-empty").waitFor();
    await page.locator(scenario.detail).getByRole("button", { name: scenario.add, exact: true }).click();
    await cartPanel.locator("li").waitFor();
    await page.setViewportSize({ width: 320, height: 800 });
    await cartPanel.scrollIntoViewIfNeeded();
    assert.equal(await cartPanel.isVisible(), true, "the cart remains accessible on a narrow screen");
    await shot("phone-cart");
  });
}

test("page Find inside an ARIA modal searches only the modal and Escape closes Find before the modal", { skip }, async t => {
  const { page, settled } = await open(t, { url: "/findings", ready: ".findings-card" });
  await press(page.locator(".findings-cart button.primary").first());
  const dialog = page.locator('.findings-dialog[role="dialog"]');
  await dialog.waitFor();
  await settled();
  await page.keyboard.press(process.platform === "darwin" ? "Meta+f" : "Control+f");
  await dialog.locator(".pharos-find input").waitFor();
  await page.keyboard.type("the");
  await page.waitForFunction(() => (CSS.highlights.get("pharos-matches")?.size ?? 0) > 0);
  assert.ok(await page.evaluate(() => [...(CSS.highlights.get("pharos-matches") ?? [])].every(range => range.startContainer.parentElement.closest(".findings-dialog"))), "matches come only from the modal");
  await page.keyboard.press("Tab");
  assert.ok(await page.evaluate(() => !!document.activeElement.closest(".pharos-find")), "Tab stays in Find");
  await page.keyboard.press("Escape");
  await page.locator(".pharos-find").waitFor({ state: "detached" });
  assert.equal(await dialog.isVisible(), true, "Escape closed Find, not the modal");
});

test("the delegation session sheet on a phone takes focus, keeps it and returns it on Escape", { skip }, async t => {
  const { page } = await open(t, { url: `/work/${workID}/conversation/conv-child`, viewport: { width: 390, height: 844 }, ready: ".conversation-panel .turn" });
  const show = page.locator('button[aria-label="Show agent sessions"]');
  await press(show);
  const sheet = page.locator('[role="dialog"][aria-label="Agent sessions"]');
  await sheet.waitFor();
  await holdsFocus(page, sheet, show);
  await press(show);
  await press(sheet.getByRole('button', { name: 'Hide agent sessions', exact: true }));
  await page.waitForFunction(() => document.activeElement?.getAttribute('aria-label') === 'Show agent sessions');
});

test("the workspace sidebar returns focus when collapsed and preserves the reader when reopened", { skip }, async t => {
  const { page, settled } = await open(t, { url: `/work/${workID}/conversation/conv-root`, ready: ".conversation-panel .turn" });
  const reader = page.getByRole('region', { name: 'Conversation viewer', exact: true });
  const hide = page.getByRole('button', { name: 'Hide agent sessions', exact: true });
  const show = page.getByRole('button', { name: 'Show agent sessions', exact: true });
  await page.locator('.turn').nth(6).evaluate(turn => turn.scrollIntoView({ block: 'center', behavior: 'instant' }));
  await settled();
  const scroll = await page.locator('.workspace-reader-content').evaluate(el => el.scrollTop);
  await press(hide);
  await page.waitForFunction(() => document.activeElement?.getAttribute('aria-label') === 'Show agent sessions');
  assert.equal(await reader.isVisible(), true);
  assert.equal(await hide.evaluate(el => { el.focus(); return el === document.activeElement; }), false, 'collapsed sessions cannot receive keyboard focus');
  await press(show);
  await page.waitForFunction(() => document.activeElement?.closest('.workspace-overview'));
  assert.equal(await page.getByRole('region', { name: 'Workspace overview', exact: true }).isVisible(), true);
  assert.equal(await page.locator('.workspace-reader-content').evaluate(el => el.scrollTop), scroll);
  assert.equal(new URL(page.url()).searchParams.get('conversation'), 'conv-root');
});

test("conversation navigation focuses the requested reader unless focus moved while it loaded", { skip }, async t => {
  let gate = null;
  t.after(() => gate?.release());
  const deferred = () => { let release; const promise = new Promise(resolve => { release = resolve; }); return { promise, release }; };
  const { page, settled } = await open(t, { url: `/work/${workID}/conversation/conv-root`, ready: ".conversation-panel .turn", api: {
    "GET /api/work/:id/conversations/:conversation": async call => {
      if (call.params.conversation !== "conv-root") await gate?.promise;
      return defaultApi["GET /api/work/:id/conversations/:conversation"](call);
    },
  } });
  const control = page.locator("#settingsToggle");
  for (const [conversation, moveFocus] of [["conv-child", false], ["conv-nested", true]]) {
    gate = deferred();
    const loading = page.waitForRequest(request => new URL(request.url()).pathname.endsWith(`/conversations/${conversation}`));
    await page.evaluate(url => window.pharosNavigate(url), `/work/${workID}/conversation/${conversation}`);
    await loading;
    if (moveFocus) { await control.focus(); assert.ok(await control.evaluate(element => element === document.activeElement)); }
    gate.release();
    await page.getByRole("article", { name: "User message", exact: true }).getByText(`Review the change (${conversation})`, { exact: true }).waitFor();
    await settled();
    const target = moveFocus ? control : page.locator(".conversation-panel");
    await page.waitForFunction(element => document.activeElement === element, await target.elementHandle());
  }
  await press(page.getByRole('button', { name: 'Open workspace overview', exact: true }));
  await page.waitForFunction(() => document.activeElement?.tagName === "MAIN");
  assert.equal(await page.locator(".work-heading h1").evaluate(element => { element.focus(); return element === document.activeElement; }), false, "returning to the overview leaves its title unfocusable");
});

for (const merged of [false, true]) test(`transcript navigators follow layout changes in ${merged ? "merged" : "split"} workspaces`, { skip }, async t => {
  let sourceOnly = false;
  const api = merged ? {
    "GET /api/work/:id": call => {
      const work = defaultApi["GET /api/work/:id"](call);
      return { ...work, title:sourceOnly ? 'Financial outlook' : work.title, source_kind: sourceOnly ? 'chatgpt' : 'claude', repository_name: sourceOnly ? null : work.repository_name, location: sourceOnly ? null : work.location, branch: sourceOnly ? null : work.branch,
        conversations: sourceOnly ? [{...work.conversations[0],provider:'chatgpt',model:null,first_prompt:'Summarize the outlook'}] : work.conversations.slice(0, 1), attempts: sourceOnly ? [] : [{ attempt_no: 1, result: "Retained archive evidence\n".repeat(40) }],
        handoffs: sourceOnly ? [] : [{ candidate_id:'candidate-1', custody_holder:'agent', custody_version:'1' }],
        identity_links: sourceOnly ? [] : [{ related_provider:'codex', relationship:'related', confidence:'high' }],
        receipt: sourceOnly ? null : {outcome:'complete',package_bytes:1024,package_hash:'test-hash'}, preservation_completeness: sourceOnly ? 'partial' : 'complete' };
    },
    "GET /api/work/:id/conversations/:conversation": call => {
      const conversation=defaultApi["GET /api/work/:id/conversations/:conversation"](call);
      if(!sourceOnly)return conversation;
      return {...conversation,provider:'chatgpt',model:null,title:'Financial outlook',messages:[
        {id:'chat-prompt',native_id:'chat-prompt',kind:'message',role:'user',text:'Summarize the outlook',created_at:'2026-11-10T18:00:00Z'},
        {id:'chat-reply',native_id:'chat-reply',kind:'message',role:'assistant',text:'Here is a summary of the outlook.',created_at:'2026-11-10T18:00:10Z'},
      ]};
    },
    "GET /api/work/:id/delegation": call => {
      const data = defaultApi["GET /api/work/:id/delegation"](call);
      return { ...data, rows: data.rows.slice(0, 1), focus: data.focus.slice(0, 1), session_count: 1, total: 1 };
    },
  } : {};
  const { page, settled, shot } = await open(t, { url: `/work/${workID}/conversation/conv-root`, ready: ".conversation-panel .turn", api, preferences: { "pharos-show-nav-button": true } });
  await activateHeaderTool(page, "navigatorToggle");
  await page.locator("#navigator.open").waitFor();
  await page.evaluate(() => document.querySelector("main").scrollTo(0, 1200));
  await page.locator('.reader-navigator').waitFor();
  for(const name of ['Context','Turns'])assert.equal(await page.getByRole('checkbox',{name,exact:true}).isChecked(),true,'available navigators are shown by default');
  assert.equal(await page.getByRole('checkbox',{name:'Timeline',exact:true}).count(),0);
  const timelineKey=()=>page.getByRole('group',{name:'Timeline key',exact:true}).getByRole('button',{name:'Timeline',exact:true});
  await page.getByRole('checkbox', {name:'Turns',exact:true}).check();
  await page.getByRole('checkbox', {name:'Context',exact:true}).check();
  await page.locator('.turn').nth(6).evaluate(turn=>turn.scrollIntoView({block:'center',behavior:'instant'}));
  await settled();
  const tops = await page.evaluate(() => {
    const header = document.querySelector("body > header").getBoundingClientRect().bottom;
    return { header, rails: [...document.querySelectorAll(".reader-navigator-host")].filter(rail => rail.getClientRects().length).map(rail => [rail.className, rail.getBoundingClientRect().top]) };
  });
  assert.ok(tops.rails.length, "the rails are shown");
  for (const [name, top] of tops.rails) assert.ok(top >= tops.header - 1, `${name} starts at ${top}, above the header's bottom at ${tops.header}`);

  await shot('session-navigator');
  const navigator = page.locator('.reader-navigator');
  await timelineKey().click();
  assert.equal(await navigator.locator('.reader-navigator-timeline').count(),0);
  await page.getByRole('checkbox', {name:'Context',exact:true}).uncheck();
  await page.getByRole('checkbox', {name:'Turns',exact:true}).uncheck();
  assert.equal(await navigator.locator('.reader-navigator-track').count(),0);
  await page.waitForFunction(()=>!document.querySelector('.workspace-reader-content').style.getPropertyValue('--reader-navigator-height'));
  await page.reload();
  await navigator.waitFor();
  for(const name of ['Context','Turns'])assert.equal(await navigator.getByRole('checkbox',{name,exact:true}).isChecked(),false,'hidden navigators stay hidden after reload');
  assert.equal(await timelineKey().getAttribute('aria-pressed'),'false');
  assert.equal(await navigator.locator('.reader-navigator-timeline').count(),0);
  await navigator.scrollIntoViewIfNeeded();
  await shot('all-navigators-hidden');
  const hiddenRail=await navigator.evaluate(element=>{
    const rail=element.closest('.reader-navigator-host'),scroller=element.closest('.workspace-reader-content');
    scroller.scrollTop=scroller.scrollHeight;
    return {bottom:rail.getBoundingClientRect().bottom,top:scroller.getBoundingClientRect().top};
  });
  assert.ok(hiddenRail.bottom<=hiddenRail.top,'the hidden navigator scrolls away with the transcript');
  await shot('hidden-navigators-scrolled');
  await timelineKey().click();
  const shownRail=await navigator.evaluate(element=>{
    const rail=element.closest('.reader-navigator-host'),scroller=element.closest('.workspace-reader-content');
    scroller.scrollTop+=rail.getBoundingClientRect().top-scroller.getBoundingClientRect().top+100;
    return {top:rail.getBoundingClientRect().top,viewportTop:scroller.getBoundingClientRect().top};
  });
  assert.ok(Math.abs(shownRail.top-shownRail.viewportTop)<2,'showing a navigator restores the sticky rail');
  await page.getByRole('checkbox', {name:'Turns',exact:true}).check();
  await navigator.getByRole('button', {name:'Browse session timeline'}).focus();
  await page.keyboard.press('End');
  await settled();
  assert.ok(await navigator.getByRole('button', {name:'Browse session timeline'}).evaluate(button=>button===document.activeElement),'revealing a message keeps keyboard focus in the navigator');
  const saved = page.url();
  await page.reload();
  await page.locator('.reader-navigator-timeline').waitFor();
  await settled();
  assert.equal(await page.getByRole('checkbox', {name:'Turns',exact:true}).isChecked(),true);
  assert.equal(await page.getByRole('checkbox', {name:'Context',exact:true}).isChecked(),false);
  assert.equal(new URL(page.url()).searchParams.get('reader_navigator'),new URL(saved).searchParams.get('reader_navigator'));

  if (merged) {
    assert.match(await page.locator('.work-heading > .meta').innerText(), /Source: claude/);
    // The overview grows without resizing the transcript or its scrollport.
    // The selected turn and the progress band must still describe the viewport.
    for (const expanded of [true, false]) {
      await page.getByText("Archive details", { exact: true }).click();
      assert.equal(await page.locator(".archive-details").evaluate(details => details.open), expanded);
      if(expanded){
        const details=page.locator('.archive-body');
        for(const text of ['Retained archive evidence','candidate-1','Related source identities','Preservation receipt','test-hash'])assert.ok(await details.getByText(text,{exact:false}).count(),`${text} remains in Archive details`);
        assert.doesNotMatch(await details.innerText(),/Source · claude/);
      }
      await settled();
      await page.locator(".turn").nth(6).evaluate(turn => turn.scrollIntoView({ block: "center", behavior: "instant" }));
      await page.waitForFunction(() => {
        const turns = [...document.querySelectorAll(".reader-navigator-turn")];
        const marker = turns[6], band = document.querySelector(".reader-navigator-window");
        const bounds = marker.getBoundingClientRect(), viewport = band.getBoundingClientRect(), middle = (bounds.left + bounds.right) / 2;
        return middle >= viewport.left && middle <= viewport.right;
      });
    }
    sourceOnly=true;
    await page.reload();
    await page.locator('.reader-navigator').waitFor();
    const context=await page.locator('.work-heading > .meta').innerText();
    assert.match(context,/Repository not identified/);
    assert.match(context,/Source: chatgpt/);
    assert.equal(await page.locator('.archive-details').isVisible(),false,'source alone does not need Archive details');
    assert.doesNotMatch(await page.locator('.coverage-warning').innerText(),/See Archive details/);
    await shot('source-only-heading');
  } else {
    // The child fixture is a single exchange without context measurements.
    // Keep unavailable choices in the URL so they return on another session.
    const all = encodeURIComponent(JSON.stringify(['timeline','context','turns']));
    await page.evaluate(url=>window.pharosNavigate(url), `/work/${workID}/conversation/conv-child?reader_navigator=${all}`);
    await page.getByRole('article',{name:'User message',exact:true}).getByText('Review the change (conv-child)',{exact:true}).waitFor();
    await page.locator('.reader-navigator-timeline').waitFor();
    for(const name of ['Context','Turns'])assert.equal(await page.getByRole('checkbox',{name,exact:true}).count(),0,`${name} is unavailable`);
    await shot('session-navigator-unmeasured');
    await timelineKey().click();
    assert.equal(await page.locator('.reader-navigator-timeline').count(),0);
    const state=JSON.stringify(['timeline','context','turns']);
    await page.evaluate(url=>window.pharosNavigate(url), `/work/${workID}/conversation/conv-root?reader_navigator=${encodeURIComponent(state)}`);
    await page.locator('.reader-navigator-context .token-marker').first().waitFor();
    for(const name of ['Context','Turns'])assert.equal(await page.getByRole('checkbox',{name,exact:true}).isChecked(),true,`${name} returns when available`);
    await shot('session-navigator-measured');
  }
});

test('overlapping context profiles reveal the stream actually clicked', {skip}, async t=>{
  const {page,settled}=await open(t,{url:`/work/${workID}/conversation/conv-root`,ready:'.reader-navigator-context .token-marker'});
  const entries=await page.evaluate(()=>document.querySelector('.conversation-panel').navigatorData.contexts.map(entry=>({id:entry.message.id,size:entry.sample.size,title:entry.title})));
  const parent=entries.find(entry=>entry.size===150200),child=entries.find(entry=>entry.id==='m-17');
  assert.ok(parent&&child,'the fixture has overlapping main and subagent profiles');
  const parentButton=page.getByRole('button',{name:parent.title,exact:true});
  const childButton=page.getByRole('button',{name:child.title,exact:true});
  const parentBounds=await parentButton.boundingBox(),childBounds=await childButton.boundingBox();
  const clickX=(Math.max(parentBounds.x,childBounds.x)+Math.min(parentBounds.x+parentBounds.width,childBounds.x+childBounds.width))/2;
  assert.ok(clickX>Math.max(parentBounds.x,childBounds.x)&&clickX<Math.min(parentBounds.x+parentBounds.width,childBounds.x+childBounds.width));
  // This point is inside both buttons' rectangles, above the smaller profile.
  await page.mouse.click(clickX,parentBounds.y+parentBounds.height*.9);
  await page.waitForFunction(id=>[...document.querySelectorAll('.token-highlight')].some(target=>target.closest('[data-message-id]')?.dataset.messageId===id),parent.id);
  await settled();
  // Keyboard activation still reaches the small profile.
  await childButton.focus();await page.keyboard.press('Enter');
  await page.waitForFunction(id=>[...document.querySelectorAll('.token-highlight')].some(target=>target.closest('[data-message-id]')?.dataset.messageId===id),child.id);
});

test('untimed session navigator draws calls and failures and reveals by message order', {skip}, async t=>{
  let timed=false,partial=false,singleSession=false;
  const longQuery='select:'+Array.from({length:40},(_,index)=>`mcp__service__search_resource_${index}`).join(',');
  const {page,settled,shot}=await open(t,{url:`/work/${workID}/conversation/conv-root`,ready:'.reader-navigator-timeline',viewport:{width:1920,height:1050},api:{
    'GET /api/work/:id':call=>{const work=defaultApi['GET /api/work/:id'](call);return singleSession?{...work,conversations:work.conversations.slice(0,1)}:work;},
    'GET /api/work/:id/delegation':call=>{const data=defaultApi['GET /api/work/:id/delegation'](call);return singleSession?{...data,rows:data.rows.slice(0,1),focus:data.focus.slice(0,1),session_count:1,total:1}:data;},
    'GET /api/work/:id/conversations/:conversation':call=>{
      const body=defaultApi['GET /api/work/:id/conversations/:conversation'](call);
      if(call.params.conversation!=='conv-root')return body;
      let tick=0;
      const event=(id,kind,text,extra={})=>({id,native_id:id,kind,role:'assistant',created_at:timed||partial&&id==='prompt'?new Date(Date.UTC(2026,10,1)+tick++*10000).toISOString():null,text:typeof text==='string'?text:JSON.stringify(text),...extra});
      return {...body,messages:[event('prompt','message','Check the files',{role:'user',labels:{ids:['P.request.work.implement'],probs:{'P.request.work.implement':.95}},authorship:{provisional:true,typed_words:3,spans:[{start:0,end:15,category:'typed',words:3}]}}),
        event('command','tool_call',{name:'Bash',input:{command:'check'}},{call_id:'cmd'}),
        event('result','tool_result',{exit_code:1,content:'Process exited with code 1'},{call_id:'cmd'}),
        event('delegate','delegation',{name:'Task',input:{description:'Review files'}},{call_id:'agent'}),
        event('done','delegation_result',{content:'Reviewed'},{call_id:'agent'}),
        event('search','tool_call',{name:'Search',input:{query:'climate outlook'}},{call_id:'search-call'}),
        event('search-result','tool_result',{content:'Search results'},{call_id:'search-call'}),
        event('web','tool_call',{name:'Browser',input:{url:'https://example.com/report'}},{call_id:'web-call'}),
        event('long-search','tool_call',{name:'Search',input:{query:longQuery}},{call_id:'long-search-call'}),
        event('long-result','tool_result',{content:'Search results'},{call_id:'long-search-call'}),
        event('reply','message','Finished checking the files',{agent_events:[{id:'obstacle',kind:'obstacle',label:'Obstacle',group:'Friction',probability:.9,sentence:[0,27],subject:[],response:[]}]})]};
    },
  }});
  const navigator=page.locator('.reader-navigator'),key=page.getByRole('group',{name:'Timeline key',exact:true});
  for(const name of ['Delegate / message','Error','Network'])await key.getByRole('button',{name,exact:true}).waitFor();
  await shot('ungrounded-legend');
  assert.ok(await navigator.getByText('Message order · timestamps unavailable',{exact:true}).isVisible());
  assert.equal(await key.getByText('End time unknown',{exact:true}).count(),0,'ordinal timelines have no seconds-based guide');
  assert.equal(await navigator.locator('.reader-navigator-timeline [data-tool-id="web-call"] line').count(),0);
  assert.equal(await page.locator('.timeline-entry[data-message-id="search"] .timeline-time').innerText(),'');
  const eventMark=navigator.locator('.reader-navigator-timeline [data-agent-event="obstacle"]');
  const eventBounds=await eventMark.boundingBox(),timelineBounds=await navigator.locator('.reader-navigator-timeline').boundingBox();
  await page.mouse.click(Math.min(eventBounds.x+eventBounds.width/2,timelineBounds.x+timelineBounds.width-1),eventBounds.y+eventBounds.height/2);
  await page.locator('[data-message-id="reply"].authorship-target').waitFor({state:'visible'});
  const prompt=page.locator('.timeline-entry[data-message-id="prompt"]'),marker=prompt.locator('.writing-estimate-trigger');
  const checkEstimatePlacement=async()=>{
    const time=prompt.locator('.timeline-time > span');
    assert.match(await prompt.locator('.timeline-time').innerText(),/^\*\s*\+0s/);
    const star=await marker.boundingBox(),stamp=await time.boundingBox();
    assert.ok(star&&stamp&&star.x+star.width<=stamp.x,'the estimate precedes the timestamp');
    assert.ok(star.y<stamp.y+stamp.height&&stamp.y<star.y+star.height,'the estimate and timestamp share a line');
    assert.ok(star.x>=0,'the estimate remains within the viewport');
  };
  const actionRow=page.locator('.timeline-entry[data-message-id="search"]');
  const contentInset=async()=>actionRow.evaluate(row=>row.querySelector('.timeline-content').getBoundingClientRect().left-row.getBoundingClientRect().left);
  assert.ok(Math.abs(await contentInset())<1,'untimed actions use the full row width');
  const sections=navigator.locator('.reader-section-node').first();
  const assertCaretFits=async(label)=>{
    const placement=await label.evaluate(element=>{const text=element.querySelector('strong').getBoundingClientRect(),caret=element.querySelector('.reader-section-chevron').getBoundingClientRect(),bounds=element.getBoundingClientRect();return {right:caret.left>=text.right,inside:caret.left>=bounds.left&&caret.right<=bounds.right&&caret.top>=bounds.top&&caret.bottom<=bounds.bottom};});
    assert.ok(placement.right&&placement.inside,'the caret stays to the right of its label and inside the row');
  };
  await assertCaretFits(sections.locator(':scope > summary .reader-section-label'));
  await shot('multi-session-caret');
  await sections.locator(':scope > summary .reader-section-label').click();
  await navigator.getByText('Message modes',{exact:true}).waitFor();
  await navigator.getByText('Agent events',{exact:true}).waitFor();
  const events=navigator.locator('.reader-section-node').filter({has:page.getByText('Agent events',{exact:true})}).last();
  await events.locator(':scope > summary .reader-section-label').click();
  await events.getByText('Friction',{exact:true}).waitFor();
  const tools=navigator.locator('.reader-section-node').filter({has:page.getByText('Tool actions',{exact:true})}).last();
  await tools.locator(':scope > summary .reader-section-label').click();
  await navigator.locator('.reader-section-node').getByText('Run',{exact:true}).waitFor();
  await key.getByRole('button',{name:'Error',exact:true}).click();
  await page.getByRole('menuitem',{name:'Hide',exact:true}).click();
  await page.waitForFunction(()=>!document.querySelector('.reader-navigator [data-tool-id="cmd"]'));
  await key.getByRole('button',{name:'Error',exact:true}).click();
  await page.getByRole('menuitem',{name:'Show',exact:true}).click();
  await navigator.locator('[data-tool-id="cmd"]').first().waitFor({state:'attached'});
  const search=page.locator('.query-chip').filter({hasText:'climate outlook'});
  await search.click();assert.equal(await search.getAttribute('aria-expanded'),'true');
  await search.click();assert.equal(await search.getAttribute('aria-expanded'),'false');
  const longSearch=page.locator('.timeline-entry[data-message-id="long-search"] .query-chip');
  const checkLongSearch=async()=>{
    const shortHeight=(await search.boundingBox()).height;
    const result=await longSearch.evaluate(button=>{const text=button.querySelector('.query-chip-label');return {height:button.getBoundingClientRect().height,clipped:text.scrollWidth>text.clientWidth,title:button.title}});
    assert.ok(result.height<=shortHeight+1,'long activity chips stay on one line');
    assert.ok(result.clipped,'long activity chip text is truncated');
    assert.equal(result.title,longQuery,'the full query remains available');
  };
  await checkLongSearch();
  await longSearch.click();assert.equal(await longSearch.getAttribute('aria-expanded'),'true');
  await longSearch.click();assert.equal(await longSearch.getAttribute('aria-expanded'),'false');
  const web=page.locator('.action-web-link');assert.equal(await web.getAttribute('href'),'https://example.com/report');
  assert.ok(await web.locator('.markdown-link-icon :is(img,svg)').count());
  assert.ok(await web.locator('.external-indicator').isVisible());
  const browse=page.locator('.action-row').filter({has:web}).getByRole('button',{name:'Browse',exact:true});
  await browse.scrollIntoViewIfNeeded();await settled();await browse.click();
  await page.getByRole('menu',{name:'Filter Browse',exact:true}).getByRole('menuitem',{name:'Hide',exact:true}).waitFor();
  await page.keyboard.press('Escape');
  await shot('reader-timeline-sections');
  await page.setViewportSize({width:320,height:900});
  await assertCaretFits(sections.locator(':scope > summary .reader-section-label'));
  await checkLongSearch();
  assert.ok(Math.abs(await contentInset())<1,'untimed actions use the full row width on a narrow screen');
  assert.ok(await navigator.evaluate(element=>element.scrollWidth<=element.clientWidth),'expanded timeline fits on a narrow screen');
  await shot('reader-timeline-mobile');
  await page.setViewportSize({width:1920,height:1050});
  await page.reload();await navigator.locator('.reader-navigator-timeline').waitFor();
  await navigator.getByText('Agent events',{exact:true}).waitFor();
  await navigator.locator('.reader-section-node').getByText('Run',{exact:true}).waitFor();
  await navigator.getByRole('button',{name:'Browse session timeline'}).focus();
  await page.keyboard.press('End');await settled();
  await page.waitForFunction(()=>{
    const target=document.querySelector('[data-message-id="reply"].authorship-target'),bounds=target?.getBoundingClientRect();
    return bounds?.height>0&&bounds.top<innerHeight&&bounds.bottom>document.querySelector('.reader-navigator-host').getBoundingClientRect().bottom;
  });
  timed=true;
  await page.reload();await navigator.locator('.reader-navigator-timeline').waitFor();
  await key.getByText('End time unknown',{exact:true}).waitFor();
  assert.match(await page.locator('.timeline-entry[data-message-id="search"] .timeline-time').innerText(),/^\+\d/,'available timestamps remain visible');
  await checkEstimatePlacement();
  await shot('writing-estimate-timestamp');
  await page.setViewportSize({width:320,height:900});
  await checkEstimatePlacement();
  await page.setViewportSize({width:1920,height:1050});
  assert.ok(await marker.isVisible());await marker.click();await page.getByRole('dialog',{name:'How estimates work'}).waitFor();await page.keyboard.press('Escape');
  assert.ok(await contentInset()>0,'timed actions retain their time column');
  assert.ok(await navigator.locator('.reader-navigator-timeline [data-tool-id="web-call"] line').count(),'timed calls with an unknown end show a guide');
  await key.getByRole('button',{name:'Network',exact:true}).click();
  await page.getByRole('menuitem',{name:'Hide',exact:true}).click();
  await key.getByText('End time unknown',{exact:true}).waitFor({state:'detached'});
  assert.equal(await navigator.locator('[data-tool-id="web-call"]').count(),0);
  timed=false;partial=true;
  await page.reload();await marker.waitFor();
  await checkEstimatePlacement();
  await page.setViewportSize({width:320,height:900});
  await settled();
  await checkEstimatePlacement();
  singleSession=true;timed=true;partial=false;
  await page.setViewportSize({width:1920,height:929});
  await page.reload();
  await page.waitForFunction(()=>document.querySelector('.conversation-host')?.dataset.readerSingleSession==='true');
  const timelineDisclosure=navigator.locator('.reader-navigator-timeline-disclosure');
  await timelineDisclosure.waitFor();
  assert.equal(await navigator.locator('.reader-section-label').getByText('Session',{exact:true}).count(),0,'a single session has no duplicate Session row');
  assert.equal(await timelineDisclosure.evaluate(details=>details.open),true,'the original session disclosure state is preserved');
  await navigator.getByText('Message modes',{exact:true}).waitFor();
  await assertCaretFits(timelineDisclosure.locator(':scope > summary .reader-section-label'));
  await shot('single-session-timeline');
  await timelineDisclosure.locator(':scope > summary .reader-section-label').click();
  assert.equal(await timelineDisclosure.evaluate(details=>details.open),false);
  await page.waitForFunction(()=>window.pharosView.read('disclosures',{})['reader-timeline:conv-root:session']===false);
  await page.reload();await timelineDisclosure.waitFor();
  assert.equal(await timelineDisclosure.evaluate(details=>details.open),false,'the timeline disclosure restores after reload');
  await timelineDisclosure.locator(':scope > summary .reader-section-label').click();
  await navigator.getByText('Message modes',{exact:true}).waitFor();
  const modes=timelineDisclosure.locator('.reader-section-node').filter({has:page.getByText('Message modes',{exact:true})}).first();
  await modes.locator(':scope > summary .reader-section-label').click();
  const sender=timelineDisclosure.locator('.reader-section-node').filter({has:page.getByText('Sender',{exact:true})}).last();
  await sender.waitFor();
  await assertCaretFits(sender.locator(':scope > summary .reader-section-label'));
  await shot('reader-timeline-counts-icons');
  const singleTrack=timelineDisclosure.locator('.reader-navigator-timeline'),singleBounds=await singleTrack.boundingBox();
  await page.mouse.click(singleBounds.x+singleBounds.width/2,singleBounds.y+singleBounds.height/2);
  assert.equal(await timelineDisclosure.evaluate(details=>details.open),true,'clicking the timeline still reveals without toggling its disclosure');
  const browseTimeline=navigator.getByRole('button',{name:'Browse session timeline'});
  await browseTimeline.focus();await page.keyboard.press('End');
  assert.equal(await timelineDisclosure.evaluate(details=>details.open),true,'keyboard browsing leaves the disclosure open');
  assert.ok(await browseTimeline.evaluate(button=>button===document.activeElement),'keyboard browsing keeps focus');
});

test('session navigator skips idle time and keeps scrolling and navigation aligned', {skip}, async t=>{
  const base=Date.UTC(2026,9,1);let anotherActive=false;
  const {page,settled,shot}=await open(t,{url:`/work/${workID}/conversation/conv-root`,ready:'.reader-navigator-turn',viewport:{width:1920,height:929},api:{
    'GET /api/work/:id/conversations/:conversation':call=>{
      const body=defaultApi['GET /api/work/:id/conversations/:conversation'](call);
      if(call.params.conversation!=='conv-root')return body;
      const message=(id,role,at)=>({id,native_id:id,role,kind:'message',text:`Message ${id}. `+'Retained conversation text. '.repeat(30),created_at:new Date(base+at).toISOString()});
      return {...body,messages:[message('first','user',0),...Array.from({length:18},(_,i)=>message(`before-${i}`,'assistant',(i+1)*3000)),
        message('resume','user',60*60000),{id:'timeline-tool',native_id:'timeline-tool',role:'assistant',kind:'tool_call',call_id:'timeline-tool-call',text:JSON.stringify({name:'Bash',input:{command:'inspect'}}),created_at:new Date(base+60*60000+15000).toISOString()},
        message('last','assistant',61*60000),...Array.from({length:18},(_,i)=>message(`after-${i}`,'assistant',61*60000+(i+1)*1000))]};
    },
    'GET /api/work/:id/delegation':call=>({...defaultApi['GET /api/work/:id/delegation'](call),start:base,end:base+61*60000}),
    'GET /api/work/:id/parallel':call=>{
      const activity=defaultApi['GET /api/work/:id/parallel'](call);
      return {...activity,lanes:activity.lanes.map(lane=>({...lane,model_inferred:[],marks:anotherActive&&lane.session==='s-child'?[{start:base+60000,end:base+60*60000,end_known:true}]:[],messages:lane.session==='s-root'?[['first','user',0],['first-reply','assistant',1],['resume','user',60],['last','assistant',61]].map(([id,role,minute])=>({id,role,kind:'message',created_at:new Date(base+minute*60000).toISOString(),text:id})):[]}))};
    },
  }});
  const navigator=page.locator('.reader-navigator'),track=navigator.locator('.reader-navigator-timeline');
  await page.waitForFunction(()=>Array.isArray(document.querySelector('.conversation-panel').pharosReaderIdleGaps));
  await navigator.locator('.lt-idle-cut-line').first().waitFor({state:'attached'});
  const turn=navigator.locator('.reader-navigator-turn').nth(1);
  const relativeLeft=()=>turn.evaluate(element=>{const track=element.parentElement.getBoundingClientRect(),mark=element.getBoundingClientRect();return (mark.left-track.left)/track.width;});
  assert.ok(await relativeLeft()<.6,'the resumed turn follows the compressed gap');
  await navigator.locator('[data-tool-id="timeline-tool-call"]').first().waitFor({state:'attached'});
  const toolX=await navigator.locator('[data-tool-id="timeline-tool-call"]').first().evaluate(mark=>mark.getBoundingClientRect().x);
  const trackBounds=await track.boundingBox();
  await page.mouse.click(toolX,trackBounds.y+trackBounds.height/2);
  const target=page.locator('.event-body.authorship-target[data-message-id="timeline-tool"]');
  await target.waitFor({state:'visible'});
  await page.waitForFunction(()=>{
    const target=document.querySelector('.event-body.authorship-target[data-message-id="timeline-tool"]'),scroller=document.querySelector('.workspace-reader-content'),rail=document.querySelector('.reader-navigator-host');
    if(!target)return false;
    const box=target.getBoundingClientRect(),view=scroller.getBoundingClientRect(),top=Math.max(view.top,rail.getBoundingClientRect().bottom);
    return Math.abs((box.top+box.bottom)/2-(top+view.bottom)/2)<20;
  });
  assert.notEqual(await target.evaluate(element=>getComputedStyle(element).outlineStyle),'none','the selected tool event has a visible highlight');
  await page.waitForFunction(()=>!document.querySelector('.reader-navigator-window').hidden);
  const indicator=await navigator.evaluate(element=>{
    const timeline=element.querySelector('.reader-navigator-timeline').getBoundingClientRect(),turn=element.querySelectorAll('.reader-navigator-turn')[1],last=turn.parentElement.getBoundingClientRect(),band=element.querySelector('.reader-navigator-window').getBoundingClientRect(),text=document.createRange();
    text.selectNodeContents(turn.querySelector('span'));
    return {extendsAbove:band.top<timeline.top,extendsBelow:band.bottom>last.bottom,labelClear:text.getBoundingClientRect().left>turn.getBoundingClientRect().left+2};
  });
  assert.ok(indicator.extendsAbove&&indicator.extendsBelow,'the viewport outline surrounds the navigator bars');
  assert.ok(indicator.labelClear,'the turn label clears its indicator');
  await shot('timeline-event-reveal');
  const bounds=await track.boundingBox();
  await page.mouse.click(bounds.x+bounds.width*.75,bounds.y+bounds.height/2);
  await page.waitForFunction(()=>document.querySelector('[data-message-id="resume"].authorship-target, [data-message-id="last"].authorship-target'));
  await settled();
  await page.locator('.turn').nth(1).evaluate(element=>element.scrollIntoView({block:'center',behavior:'instant'}));
  await page.waitForFunction(()=>{
    const turn=document.querySelectorAll('.reader-navigator-turn')[1].getBoundingClientRect(),band=document.querySelector('.reader-navigator-window').getBoundingClientRect();
    return !document.querySelector('.reader-navigator-window').hidden&&turn.left<=band.right&&turn.right>=band.left;
  });
  await page.evaluate(()=>window.pharosView.write('timeline_idle',false));
  await page.waitForFunction(()=>!document.querySelector('.reader-navigator .lt-idle-cut-line'));
  assert.ok(await relativeLeft()>.9,'the shared setting restores linear time');
  anotherActive=true;
  await page.evaluate(()=>window.pharosView.write('timeline_idle',true));
  await page.reload();
  await page.waitForFunction(()=>Array.isArray(document.querySelector('.conversation-panel')?.pharosReaderIdleGaps));
  assert.equal(await navigator.locator('.lt-idle-cut-line').count(),0,'activity in another session keeps the gap visible');
});

test('session navigator scrolling reuses transcript geometry on a long session', {skip}, async t=>{
  const {page,settled}=await open(t,{url:`/work/${workID}/conversation/conv-root?reader_navigator=${encodeURIComponent(JSON.stringify(['timeline','context','turns']))}`,ready:'.reader-navigator-turn',api:{
    'GET /api/work/:id/conversations/:conversation':call=>{
      const body=defaultApi['GET /api/work/:id/conversations/:conversation'](call);
      if(call.params.conversation!=='conv-root')return body;
      return {...body,messages:Array.from({length:160},(_,turn)=>Array.from({length:12},(_,index)=>{
        const context=index%2===1;
        return {id:`nav-${turn}-${index}`,native_id:`nav-${turn}-${index}`,role:index===0?'user':context?'system':'assistant',kind:context?'metadata':'message',
          text:context?JSON.stringify({type:'token_count',info:{total_token_usage:{total_tokens:(turn*12+index)*20000},last_token_usage:{input_tokens:12000+turn*100+index*20,output_tokens:500}}}):`Turn ${turn+1}, message ${index+1}: `+'Retained transcript text. '.repeat(5),
          created_at:new Date(Date.UTC(2026,9,1)+turn*60000+index*1000).toISOString()};
      })).flat()};
    },
  }});
  assert.equal(await page.locator('.token-marker').count(),960,'the fixture exercises hundreds of context profiles');
  await page.getByRole('button',{name:/^Close (All )?Turns$/i}).click();
  await settled();
  // Warm the sticky transition before instrumenting the scroll path.
  await page.locator('.turn').nth(2).evaluate(turn=>turn.scrollIntoView({block:'start',behavior:'instant'}));
  await settled();
  const measureScroll=()=>page.evaluate(async()=>{
    const scroller=document.querySelector('.workspace-reader-content'),list=document.querySelector('.turn-list'),maximum=scroller.scrollHeight-scroller.clientHeight;
    const original=Element.prototype.getBoundingClientRect;let transcriptReads=0;const gaps=[];
    Element.prototype.getBoundingClientRect=function(){if(this===list||list.contains(this))transcriptReads++;return original.call(this);};
    const frame=()=>new Promise(resolve=>requestAnimationFrame(resolve));
    try{for(let i=1;i<=20;i++){const start=performance.now();scroller.scrollTop=maximum*i/20;await frame();await frame();gaps.push(performance.now()-start);}}
    finally{Element.prototype.getBoundingClientRect=original;}
    return{transcriptReads,maxFramePair:Math.round(Math.max(...gaps)),slowFramePairs:gaps.filter(gap=>gap>50).length};
  });
  const measurements=await measureScroll();
  assert.equal(measurements.transcriptReads,0,'scrolling must not remeasure transcript targets');
  t.diagnostic(JSON.stringify({fixture:'160 turns / 1,920 messages / 960 context samples, collapsed',...measurements}));
  await page.getByRole('button',{name:/^Open (All )?Turns$/i}).click();
  await settled();
  const expanded=await measureScroll();
  assert.equal(expanded.transcriptReads,0,'expanded history also reuses cached geometry');
  t.diagnostic(JSON.stringify({fixture:'160 turns / 1,920 messages / 960 context samples, expanded',...expanded}));
  t.diagnostic(JSON.stringify({fixture:'same expanded transcript, all navigators, repeat traversal',...await measureScroll()}));
  await page.getByRole('checkbox',{name:'Context',exact:true}).uncheck();
  await settled();
  t.diagnostic(JSON.stringify({fixture:'same expanded transcript, timeline and turns',...await measureScroll()}));
  await page.getByRole('checkbox',{name:'Turns',exact:true}).uncheck();
  await settled();
  t.diagnostic(JSON.stringify({fixture:'same expanded transcript, timeline only',...await measureScroll()}));
  const timelineToggle=page.getByRole('group',{name:'Timeline key',exact:true}).getByRole('button',{name:'Timeline',exact:true});
  await timelineToggle.click();
  await settled();
  t.diagnostic(JSON.stringify({fixture:'same expanded transcript, all navigators hidden',...await measureScroll()}));
  await timelineToggle.click();
  await settled();
  await page.locator('.turn').nth(80).evaluate(turn=>turn.scrollIntoView({block:'center',behavior:'instant'}));
  await page.waitForFunction(()=>!document.querySelector('.reader-navigator-window').hidden);
  // Opening and closing content must refresh the layout without relocating focus.
  await page.locator('.turn').nth(80).locator(':scope > summary').click();
  await settled();
  assert.ok(await page.locator('.reader-navigator-window').evaluate(band=>!band.hidden),'the visible window survives the relayout');
  await page.setViewportSize({width:390,height:844});
  await settled();
  assert.ok(await page.locator('.reader-navigator').isVisible());
  assert.ok(await page.locator('.workspace-reader-content').evaluate(element=>element.scrollWidth<=element.clientWidth+1),'navigator fits a narrow reader');
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
        event("multiblock", "message", {message:{role:'assistant',content:[{type:'text',text:'First retained block.'},{type:'text',text:'Second retained block.'},{type:'tool_use',id:'multi-read-1',name:'Read',input:{file_path:'a.go'}},{type:'tool_use',id:'multi-read-2',name:'Read',input:{file_path:'b.go'}}]}}),
        ...Array.from({ length: 52 }, (_, i) => event(`call-${i}`, "tool_call", { name: "Bash", input: { command: `check ${i}` } }, { call_id: `call-${i}` })),
        event("m-15", "delegation", { name: "Task", input: { description: "Review implementation", subagent_type: "Reviewer" } }, { call_id: "child" }),
        event("context", "metadata", { type: "token_count", info: { total_token_usage: { total_tokens: 40000 }, last_token_usage: { input_tokens: 30000, output_tokens: 500 } } }),
      ] };
    } },
  });
  await page.locator(".delegation-inbox-heading").waitFor();
  assert.equal(calls.filter(call => call.path.endsWith("/delegation")).length, 1, "the deep link loads delegation analysis once for the selected conversation");
  if (trigger === "scroll") {
    await page.evaluate(() => { const scroller = document.querySelector(".workspace-reader-content"); scroller.scrollTop = scroller.scrollHeight; });
    await page.getByRole("button", { name: /Open conversation/ }).waitFor();
  }
  await page.getByRole('checkbox', {name:'Context',exact:true}).check();
  await page.locator(".token-marker").last().click();
  await page.waitForFunction(() => {
    const target = document.querySelector(".token-highlight");
    const bounds = target?.getBoundingClientRect();
    return bounds?.height > 0 && bounds.bottom > 0 && bounds.top < innerHeight;
  });
  const child = page.getByRole("button", { name: /Open conversation/ });
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

test("Ask Pharos feedback requires explicit approval of the reviewed draft", { skip, timeout:60000 }, async t => {
  const selection = {provider:"Codex",model:"test-model",effort:"Low"};
  const feedback = {kind:"feature",comment:"Add Markdown export of selected conversations."};
  const session = {id:"feedback-chat",createdAt:Date.now(),updatedAt:Date.now(),selection,draft:"",conversation:[{id:"feature-1",prompt:"Request Markdown export",cli:"Codex",model:"test-model",effort:"Low",status:"feedback",reply:"I prepared a feature request for review.",cards:[],feedback}]};
  const submissions = [], questions = [];
  const other = {...session,id:"another-chat",conversation:[{...session.conversation[0],id:"another-turn",status:"multiple",feedback:undefined,prompt:"Another chat",reply:"An unrelated conversation."}]};
  const delivered = Promise.withResolvers();
  const { page, settled, shot } = await open(t, {url:"/?ask_chat=feedback-chat",viewport:{width:390,height:844},ready:".pharos-feedback .uifeedback-form",api:{
    "GET /api/ask/status":{settings:{enabled:true,showInvitation:true,selection},providers:[{id:"Codex"}]},
    "GET /api/ask/models":{id:"Codex",models:[{id:"test-model",label:"Test Model",efforts:["Default","Low"],default:true}]},
    "GET /api/ask/sessions":{sessions:[session,other]},"GET /api/ask/session":({query})=>query.get("id")==="another-chat"?other:session,
    "POST /api/ask/session":({body})=>({...session,...body}),
    "POST /api/ask/send":({body})=>{questions.push(body);return session;},
    "POST /api/feedback/submit":async({body})=>{submissions.push(body);if(submissions.length===1)return {id:"wrong-receipt",received:true};await delivered.promise;return {id:body.id,received:true};},
  }});
  await page.evaluate(() => localStorage.setItem("uifeedback.v1.draft", JSON.stringify({kind:"feedback",comment:"An unrelated manual report"})));
  const review = page.getByRole("button", {name:"Review feedback",exact:true});
  assert.equal(submissions.length, 0);
  assert.equal(await page.locator(".uifeedback-dialog").count(), 0, "a proposal does not open a dialog automatically");
  const inline = page.locator(".pharos-feedback .uifeedback-inline"), comment = inline.getByRole("textbox", {name:"What would you like changed?"});
  const approve = inline.getByRole("button", {name:"Approve and send feedback",exact:true});
  await page.waitForFunction(() => !document.querySelector(".uifeedback-actions .primary")?.disabled);
  assert.ok(await inline.evaluate(element => !element.contains(document.activeElement)), "restoring an inline review leaves focus alone");
  const question = page.getByRole("textbox", {name:"Ask a question",exact:true});
  await question.fill("An unrelated next question");
  assert.equal(await comment.inputValue(), feedback.comment);
  assert.ok(!await inline.getByRole("checkbox", {name:/Include recent browser errors/}).isChecked());
  await comment.fill("Add Markdown export so I can share selected conversations in a review.");
  await shot("ask-feedback-inline-phone");
  await page.setViewportSize({width:1440,height:1050});await shot("ask-feedback-inline-desktop");
  await page.setViewportSize({width:390,height:844});
  await page.keyboard.press("Escape");await review.waitFor();
  assert.ok(await review.evaluate(element => element === document.activeElement), "cancel returns focus to Review feedback");
  assert.equal(submissions.length, 0, "dismissing does not consent");
  await review.click();await page.waitForFunction(() => !document.querySelector(".uifeedback-actions .primary")?.disabled);
  assert.match(await comment.inputValue(), /so I can share/);
  assert.ok(await comment.evaluate(element => element === document.activeElement), "deliberately reopening the draft focuses its editor");
  const email = inline.getByRole("textbox", {name:"Contact email (optional)"});
  await email.fill("reviewer@example.test");await email.press("Enter");await settled();
  assert.equal(submissions.length, 0, "Enter in the email field is not approval");
  assert.equal(questions.length, 0, "feedback fields cannot submit the surrounding Ask composer");
  assert.equal(await question.evaluate(element => element.value), "An unrelated next question");
  await comment.focus();await comment.press("ControlOrMeta+A");await comment.press("ArrowRight");await comment.press("Enter");await page.keyboard.insertText("Keep message order.");
  assert.match(await comment.inputValue(), /\nKeep message order\.$/);
  assert.equal(questions.length, 0, "Enter in the report inserts a line break without asking a question");
  await approve.focus();await page.keyboard.press("Enter");
  await inline.getByRole("alert").waitFor();
  assert.equal(submissions.length, 1);
  assert.equal(submissions[0].comment, (await comment.inputValue()).trim(), "only the edited report is sent");
  assert.deepEqual(submissions[0].annotations, []);
  assert.equal(submissions[0].diagnostics, undefined);
  assert.equal(submissions[0].email, "reviewer@example.test");
  await comment.focus();await page.keyboard.press("Escape");await review.waitFor();
  await page.reload();await settled();
  await page.waitForFunction(() => !document.querySelector(".uifeedback-actions .primary")?.disabled);
  assert.equal(submissions.length, 1, "reopening an uncertain submission does not send it");
  const retryStarted = page.waitForRequest(request=>request.url().endsWith("/api/feedback/submit"));
  await approve.click();await retryStarted;
  await page.evaluate(()=>window.pharosNavigate('/ask?ask_chat=another-chat'));
  await page.locator('.pharos-user-question').filter({hasText:"Another chat"}).waitFor();
  await page.evaluate(()=>window.pharosNavigate('/ask?ask_chat=feedback-chat'));
  const sending = inline.getByRole("button", {name:"Sending…",exact:true});
  await sending.waitFor();
  assert.ok(await sending.isDisabled(), "a replacement editor follows the submission already in flight");
  // A second tab has no shared in-memory operation. Its editor can become
  // stale while the original tab commits a receipt to their shared outbox.
  const stalePage = await page.context().newPage();
  await stalePage.goto(page.url());
  const staleInline = stalePage.locator('.pharos-feedback .uifeedback-inline');
  const staleApprove = staleInline.getByRole("button", {name:"Approve and send feedback",exact:true});
  await staleApprove.waitFor();
  await stalePage.waitForFunction(() => !document.querySelector('.uifeedback-inline .primary')?.disabled);
  await staleInline.getByRole("textbox", {name:"What would you like changed?"}).fill("A stale editor must not create a replacement report.");
  delivered.resolve();
  await page.getByRole("region", {name:"Feedback sent",exact:true}).waitFor();
  assert.equal(submissions.length, 2);
  assert.equal(submissions[0].id, submissions[1].id, "an unchanged retry confirms the same report");
  await staleApprove.click();
  await stalePage.getByRole("region", {name:"Feedback sent",exact:true}).waitFor();
  assert.equal(submissions.length,2,"approval in a stale editor restores the saved receipt without another submission");
  await stalePage.close();
  assert.ok(await inline.evaluate(element => !element.contains(document.activeElement)), "the replacement editor receives the receipt without stealing focus");
  // Completion retains a receipt, while restoring it does not move focus.
  await page.reload();await settled();await page.getByRole("region", {name:"Feedback sent",exact:true}).waitFor();
  assert.equal(submissions.length, 2, "reopening a confirmed proposal after reload shows its receipt");
  assert.ok(await inline.evaluate(element => !element.contains(document.activeElement)), "restoring a receipt leaves focus alone");
  const sentSummary = inline.getByRole("status");
  await sentSummary.focus();await sentSummary.press("Escape");
  await inline.getByRole("button", {name:"View receipt",exact:true}).waitFor();
  assert.equal(await inline.locator('.uifeedback-draft-text').textContent(),submissions[1].comment,"a restored sent summary shows the submitted edits");
  await inline.getByRole("button", {name:"View receipt",exact:true}).click();
  await activateHeaderTool(page, "feedbackSubmitToggle");
  await page.getByRole("dialog", {name:"Send feedback",exact:true}).waitFor();
  assert.equal(await page.locator(".uifeedback-dialog").getByRole("textbox", {name:"What would you like changed?"}).inputValue(), "An unrelated manual report", "Ask preserves the manual feedback draft");
  await page.locator(".uifeedback-dialog").getByRole("button", {name:"Close feedback",exact:true}).click();
  session.conversation.push({...session.conversation[0],id:"discard-2",feedback:{kind:"feature",comment:"A second proposed feature request."}});
  await page.reload();await settled();
  const discardReview = page.locator('[data-turn-id="discard-2"] .uifeedback-inline');
  await discardReview.getByRole("textbox", {name:"What would you like changed?"}).fill("Discard these edits, without sharing them.");
  await discardReview.getByRole("button", {name:"Keep draft",exact:true}).click();
  await discardReview.getByRole("button", {name:"Discard draft",exact:true}).click();
  await page.getByRole("region", {name:"Feedback draft discarded",exact:true}).waitFor();
  const discardedStatus = await discardReview.getByRole("status").elementHandle();
  await page.waitForFunction(element => element === document.activeElement, discardedStatus);
  assert.equal(submissions.length, 2, "discarding a draft does not submit it");
  await page.reload();await settled();
  await page.getByRole("region", {name:"Feedback draft discarded",exact:true}).waitFor();
  assert.ok(await discardReview.evaluate(element => !element.contains(document.activeElement)), "restoring a discard leaves focus alone");
  assert.equal(submissions.length, 2, "discard survives restoring the original proposal from the chat");
  await page.getByRole("region", {name:"Feedback sent",exact:true}).waitFor();
});

test("feedback review keeps focus, fits a phone, and returns focus to its opener", { skip }, async t => {
  const { page, shot } = await open(t, { url: "/settings", ready: "#healthCards .panel" });
  const feedbackHeader = page.locator("#feedbackSubmitToggle");
  assert.equal(await feedbackHeader.getAttribute("aria-expanded"), "false");
  await activateHeaderTool(page, "feedbackSubmitToggle");
  const dialog = page.getByRole("dialog", { name: "Send feedback", exact: true });
  await dialog.waitFor();
  assert.equal(await feedbackHeader.getAttribute("aria-expanded"), "true");
  await page.waitForFunction(() => document.querySelector(".uifeedback-dialog")?.contains(document.activeElement));
  // A bug report is the default and includes recent logs; the contact email is an app preference.
  assert.equal(await dialog.getByRole("combobox", { name: "Type" }).inputValue(), "bug");
  await dialog.getByRole("checkbox", { name: /Include recent browser errors/ }).waitFor({ state: "attached" });
  await page.waitForFunction(() => document.querySelector(".uifeedback-check input")?.checked);
  await dialog.getByRole("textbox", { name: "Contact email (optional)" }).fill("person@example.com");
  assert.equal(await page.evaluate(() => window.pharosPrefs.get("pharos-feedback-contact-email", "")), "person@example.com");
  await page.setViewportSize({ width: 360, height: 640 });
  assert.equal(await onScreen(dialog), true);
  await shot("feedback-review");
  await page.keyboard.press("Escape");
  await dialog.waitFor({ state: "hidden" });
  assert.equal(await feedbackHeader.getAttribute("aria-expanded"), "false");
  assert.equal(await page.evaluate(() => headerToolAnchor(document.getElementById("feedbackSubmitToggle")) === document.activeElement), true);
});

test("annotated element screenshots render, the annotation surfaces stay on screen and the reviewed report sends without leaving the page", { skip }, async t => {
  const page = await annotate(t);
  // The toolbar and composer stay inside the viewport after a resize.
  await page.setViewportSize({ width: 580, height: 460 });
  for (const surface of [".feedback-toolbar", ".feedback-composer"]) assert.equal(await onScreen(page.locator(surface)), true, `${surface} off screen at 580×460`);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.locator("#feedbackNote").fill("Make this heading clearer");
  await page.locator("#feedbackIncludeURL").check();
  await page.getByRole("button", { name: "Take a screenshot of this element", exact: true }).click();
  await page.waitForFunction(() => {
    const img = document.getElementById("feedbackScreenshotPreview");
    return !img.hidden && img.complete && img.naturalWidth > 0;
  });
  if (process.env.PHAROS_UI_SCREENSHOTS) { mkdirSync(process.env.PHAROS_UI_SCREENSHOTS, { recursive: true }); await page.locator("#feedbackScreenshotPreview").screenshot({ path: `${process.env.PHAROS_UI_SCREENSHOTS}/feedback-element-capture.png` }); }
  // Adding an annotation expands the toolbar into the feedback panel, which fits 360 px.
  await page.locator("#feedbackAdd").click();
  await page.locator("#feedbackPanel.open").waitFor();
  await page.setViewportSize({ width: 360, height: 420 });
  const box = await onScreen(page.locator(".feedback-toolbar"));
  assert.equal(box, true, `expanded toolbar at [${box}] in 360×420`);
  // The browser adapter may ask for clipboard permission; use its acknowledged
  // transfer boundary while exercising real capture, IndexedDB and export.
  await page.evaluate(() => { window.pharosHost.clipboard.writeText = async () => {}; });
  const exported = page.waitForResponse(response => response.url().endsWith('/api/feedback/screenshot'));
  await page.locator('#feedbackCopy').click();
  await exported;
  await page.waitForFunction(() => document.getElementById('feedbackToastMessage').textContent.startsWith('Copied 1 annotation'));
  await page.locator("#feedbackSend").click();
  const dialog = page.getByRole("dialog", { name: "Send feedback", exact: true });
  await dialog.waitFor();
  assert.equal(await page.locator("#feedbackSubmitToggle").getAttribute("aria-expanded"), "true");
  await page.waitForFunction(() => document.querySelector(".uifeedback-dialog img")?.naturalWidth > 0);
  await dialog.getByRole("button", { name: "Send feedback", exact: true }).click();
  await page.getByRole("dialog", { name: "Feedback sent", exact: true }).waitFor();
  assert.equal(await page.locator("#feedbackSubmitToggle").getAttribute("aria-expanded"), "true");
  await page.getByRole("button", { name: "Close feedback", exact: true }).click();
  assert.equal(await page.locator("#feedbackSubmitToggle").getAttribute("aria-expanded"), "false");
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
  await page.waitForFunction(element=>element.contains(document.activeElement),await dialog.elementHandle());
  await page.keyboard.press('Escape');
  await dialog.waitFor({state:'hidden'});
  await page.waitForFunction(element=>element===document.activeElement,await edit.elementHandle());
});

test('Capture & index keeps run details focus, table scrolling and query edits through live refreshes @ 390', { skip }, async t => {
  const { page, settled } = await open(t, { url: '/settings/sync', viewport: { width: 390, height: 900 }, ready: '#syncHistoryTables .qt-row' });
  const history = page.getByRole('region', { name: 'Recent run history', exact: true });
  // Run details keep focus through a refresh and return it on close.
  const opener = history.getByRole('button', { name: 'Details', exact: true }).first();
  await press(opener);
  const dialog = page.getByRole('dialog', { name: 'Index run details', exact: true });
  await dialog.waitFor();
  await page.evaluate(() => window.pharosSync.refresh());
  await settled();
  await holdsFocus(page, dialog, opener);

  // Tables keep their horizontal scroll through a status refresh.
  await page.getByText('Performance & verification details', { exact: true }).click();
  const labels = ['Recent run history', 'Source status', 'Measured indexing cost'];
  const positions = [];
  for (const label of labels) {
    const wrap = page.getByRole('region', { name: label, exact: true }).locator('.qt-table-wrap');
    await page.waitForFunction(element => element.scrollWidth > element.clientWidth, await wrap.elementHandle());
    positions.push(await wrap.evaluate(element => { element.scrollLeft = 300; return element.scrollLeft; }));
  }
  await page.evaluate(() => window.pharosSync.refresh());
  await settled();
  for (const [index, label] of labels.entries()) {
    assert.equal(await page.getByRole('region', { name: label, exact: true }).locator('.qt-table-wrap').evaluate(element => element.scrollLeft), positions[index], `${label} scroll after a refresh`);
  }

  // The query editor keeps focus and its edit across refreshes.
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
  const focused = async locator => page.waitForFunction(element => document.activeElement === element, await locator.elementHandle());
  await press(visibility);
  await disclosure.waitFor();
  assert.ok(await visibility.evaluate(element => document.activeElement === element), 'enabling the menu keeps focus on its toggle');
  await press(disclosure);
  await options.waitFor();
  assert.ok(await disclosure.evaluate(element => document.activeElement === element), 'opening the card keeps keyboard focus on the disclosure');
  await press(disclosure, 'Space');
  await options.waitFor({ state: 'hidden' });
  await page.keyboard.press('Tab');
  assert.ok(await options.evaluate(element => !element.contains(document.activeElement)), 'Tab skips collapsed menu controls');
  await press(disclosure);
  await options.waitFor();
  const add = page.getByRole('button', { name: 'Add Estimated API cost', exact: true });
  const handle = page.getByRole('button', { name: 'Arrange Estimated API cost', exact: true });
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
  await press(addSeparator);
  await page.waitForFunction(() => document.activeElement?.getAttribute('aria-label')?.startsWith('Arrange Separator '));
  const label = await page.evaluate(() => document.activeElement.getAttribute('aria-label'));
  await page.keyboard.press('Alt+ArrowUp');
  await page.waitForFunction(label => document.activeElement?.getAttribute('aria-label') === label, label);
  await page.keyboard.press('Delete');
  await focused(addSeparator);
  await press(visibility);
  await options.waitFor({ state: 'hidden' });
  assert.ok(await visibility.evaluate(element => document.activeElement === element), 'disabling the menu keeps its toggle reachable');
});

test('Ask Pharos attachments restore focus and preserve the draft through full-screen element selection', {skip,timeout:60000}, async t => {
  const selection={provider:'Codex',model:'test-model',effort:'Low'},sessions=new Map(),submitted=[];
  const {page,settled,shot}=await open(t,{url:'/analyze?analyze_topic=thinking-overview',ready:'h1',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/sessions':()=>({sessions:[...sessions.values()]}),
    'GET /api/ask/session':({query})=>sessions.get(query.get('id')),
    'POST /api/ask/attachments':{id:'file-sample',kind:'file',name:'notes.csv',path:'/tmp/pharos-sample/notes.csv',size:42,mime:'text/csv'},
    'POST /api/ask/session':({body})=>{const session={conversation:[],createdAt:Date.now(),updatedAt:Date.now(),...sessions.get(body.id),...body};sessions.set(body.id,session);return session;},
    'POST /api/ask/send':({body})=>{submitted.push(body);const session={...sessions.get(body.id),...body,conversation:[{id:body.turnID,prompt:body.prompt,attachments:body.attachments,cli:'Codex',model:'test-model',modelLabel:'Test Model',effort:'Low',status:'multiple',reply:'I received the attached context.',cards:[]}]};sessions.set(body.id,session);return session;},
  }});
  await activateHeaderTool(page,'askPharosToggle');
  const question=page.getByRole('textbox',{name:'Ask a question',exact:true});
  await question.fill('Explain this chart using the attached notes.');
  const plus=page.getByRole('button',{name:'Attach context',exact:true});
  await plus.click();await page.getByRole('button',{name:'Attach current URL',exact:false}).click();
  const chips=page.locator('#pharos-question');
  await chips.getByRole('button',{name:/Inspect attachment:/}).click();
  await page.getByRole('dialog',{name:'Attached context Pharos receives',exact:true}).waitFor();
  await page.keyboard.press('Escape');
  assert.ok(await chips.getByRole('button',{name:/Inspect attachment:/}).evaluate(node=>document.activeElement===node));
  await page.locator('.pharos-context-files').setInputFiles({name:'notes.csv',mimeType:'text/csv',buffer:Buffer.from('model,comment\nexample,Investigate this week')});
  await chips.getByRole('button',{name:'Inspect attachment: notes.csv',exact:true}).waitFor();
  await page.getByRole('button',{name:'Full screen',exact:true}).click();
  await page.waitForURL(url=>url.pathname==='/ask');
  await plus.click();await page.getByRole('button',{name:'Select a page element',exact:false}).click();
  const panel=page.locator('.pharos-floating');await panel.getByText('Selecting an element',{exact:true}).waitFor();
  await page.waitForURL(url=>url.pathname==='/analyze');
  const heading=page.getByRole('heading',{level:1}).first();await heading.hover();
  await page.locator('#feedbackHighlight.visible').waitFor();
  await page.keyboard.press('ArrowUp');await page.keyboard.press('ArrowDown');
  await shot('ask-context-picker');await heading.click();
  await page.waitForURL(url=>url.pathname==='/ask');await question.waitFor();
  assert.ok(await question.evaluate(node=>node.value.includes('attached notes')));
  await plus.click();await page.getByRole('button',{name:'Select a page element',exact:false}).click();
  await panel.getByText('Selecting an element',{exact:true}).waitFor();await page.keyboard.press('Escape');
  await page.waitForURL(url=>url.pathname==='/ask');
  await shot('ask-context-attachments');
  await page.getByRole('button',{name:'Send question',exact:true}).click();
  await page.getByRole('status').filter({hasText:'I received the attached context.'}).waitFor();
  assert.equal(submitted.length,1);
  assert.ok(!submitted[0].currentURI,'context is supplied only through explicit attachments');
  assert.equal(submitted[0].attachments.length,3);
  await page.locator('.pharos-user-question').getByRole('button',{name:'Inspect attachment: notes.csv',exact:true}).click();
  await page.getByRole('dialog',{name:'Attached context Pharos receives',exact:true}).waitFor();
  await page.keyboard.press('Escape');await settled();
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

for(const [modal,width] of [[false,1440],[true,320]]) test(`Ask Pharos model picker overlays content without expanding ${modal?'the dialog':'Home'} @ ${width}`,{skip},async t=>{
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

test('Ask Pharos keeps long chats scrollable and the editor reachable @ 390', {skip}, async t => {
  const width = 390;
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  const turn=index=>({id:'turn-'+index,prompt:'Question '+index,cli:'Codex',model:'test-model',modelLabel:'Test Model',effort:'Low',status:'multiple',message:'',elapsedMs:1000,reply:index===5?'The newest reply.':'Earlier context. '.repeat(100),cards:[]});
  let session={id:'long-chat',createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',conversation:Array.from({length:6},(_,index)=>turn(index))};
  let finish;
  let reply=new Promise(resolve=>{finish=resolve;});t.after(()=>finish());
  const {page,settled,shot,calls}=await open(t,{url:'/?ask_chat=long-chat',viewport:{width,height:900},ready:'#pharos-ask .pharos-turn',api:{
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
  const clear=page.locator('.pharos-ai').getByRole('button',{name:'New Chat',exact:true});
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

test('Ask Pharos retry with another model focuses the picker @ 320',{skip},async t=>{
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  const file={id:'file-original',kind:'file',name:'notes.pdf',path:'/tmp/private/notes.pdf',size:42,offset:6};
  const url={id:'url-original',kind:'url',name:'Tokens',url:'/analyze?analyze_topic=tokens',offset:12};
  const element={id:'element-original',kind:'element',name:'Selected chart',url:url.url,selector:'#chart',offset:0};
  const draftFile={id:'file-draft',kind:'file',name:'draft.csv',path:'/tmp/private/draft.csv',size:12,offset:5};
  const turn={id:'first-turn',prompt:'Which commands keep failing?',attachments:[file,url],cli:'Codex',model:'test-model',modelLabel:'Test Model',effort:'Low',status:'clarification',reply:'',clarification:{question:'Which project?',options:['Pharos','All projects']},cards:[],elapsedMs:1000};
  const original={id:'failed-chat',createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'Keep the time range to this week.',draftAttachments:[draftFile],conversation:[turn,{...turn,id:'failed-turn',prompt:'Pharos',attachments:[element],status:'error',clarification:null,reply:'The agent returned an empty response; try again'}]};
  const sessions=new Map([[original.id,original]]);
  const {page,settled,shot,calls}=await open(t,{url:'/?ask_chat=failed-chat',viewport:{width:320,height:900},ready:'.pharos-recovery:not([hidden])',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true},{id:'second-model',label:'Second Model',efforts:['Default','Low'],fast:true}]},
    'GET /api/ask/session':({query})=>sessions.get(query.get('id')),
    'GET /api/ask/sessions':()=>({sessions:[...sessions.values()]}),
    'POST /api/ask/session':({body})=>{
      const session={...(sessions.get(body.id)||{id:body.id,createdAt:Date.now(),updatedAt:Date.now(),conversation:[]}),draft:body.draft,draftAttachments:body.draftAttachments,selection:body.selection};
      sessions.set(body.id,session);return session;
    },
    'POST /api/ask/send':({body})=>{
      const session={...sessions.get(body.id),draft:'',draftAttachments:[],conversation:[{...turn,id:body.turnID,prompt:body.prompt,attachments:body.attachments,status:'multiple',clarification:null,reply:'Your retry completed.'}]};
      sessions.set(body.id,session);return session;
    },
  }});
  await shot('failed-chat');
  const question=page.getByRole('textbox',{name:'Ask a question',exact:true});
  const clear=page.locator('.pharos-recovery').getByRole('button',{name:'New Chat',exact:true});
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
  // The transcript's exact form is unit tested in ask-pharos.test.mjs.
  const expected=await question.evaluate(element=>element.value);
  assert.ok(expected.includes(original.draft),'the retry draft carries the unsent draft');
  const newID=new URL(page.url()).searchParams.get('ask_chat');
  assert.ok(newID&&newID!==original.id,'retry has its own draft chat');
  assert.equal(calls.filter(call=>call.path==='/api/ask/send').length,0,'opening the picker must not send a request');
  const fits=element=>{const box=element.getBoundingClientRect();return box.left>=0&&box.right<=innerWidth&&box.top>=0&&box.bottom<=innerHeight;};
  assert.ok(await picker.evaluate(fits),'the picker stays on screen');
  assert.ok(await question.evaluate(fits),'restored messages remain reachable');
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'recovery actions must wrap on narrow screens');
  await shot('restored-draft-and-picker');
  const saved=page.waitForResponse(response=>response.url().includes('/api/ask/session')&&response.request().method()==='POST'&&response.request().postDataJSON().selection.model==='second-model'&&response.request().postDataJSON().selection.fast===true);
  const fast=picker.getByRole('switch',{name:'Fast',exact:true});
  assert.ok(await fast.isHidden(),'Fast is offered only for a model that supports it');
  await picker.getByRole('button',{name:/^Model /}).click();
  await picker.getByRole('button',{name:'Second Model',exact:true}).click();
  assert.equal(await fast.getAttribute('aria-checked'),'false','Fast starts off');
  await fast.focus();await page.keyboard.press('Space');
  assert.equal(await fast.getAttribute('aria-checked'),'true');
  await shot('fast-on');
  await page.keyboard.press('Escape');await saved;await settled();
  assert.ok(await page.locator('.pharos-model-trigger').evaluate(element=>element===document.activeElement),'Escape returns focus to the model control');
  assert.equal(sessions.get(newID).draft,expected);
  assert.deepEqual(sessions.get(newID).draftAttachments.map(item=>item.id),[file.id,url.id,element.id,draftFile.id]);
  assert.deepEqual(sessions.get(original.id).conversation,original.conversation,'the prior chat remains in history');
  await page.reload();await settled();
  await page.waitForFunction(text=>document.querySelector('#pharos-question')?.value===text,expected);
  await question.getByRole('button',{name:'Inspect attachment: notes.pdf',exact:true}).click();
  await page.getByRole('dialog',{name:'Attached context Pharos receives',exact:true}).waitFor();
  await page.keyboard.press('Escape');
  assert.ok(await page.getByRole('button',{name:'Send question',exact:true}).isEnabled());
  await page.getByRole('button',{name:'Send question',exact:true}).click();
  await page.getByRole('status').filter({hasText:'Your retry completed.'}).waitFor();
  await settled();
  assert.equal(sessions.get(newID).conversation[0].prompt,expected,'the restored transcript can be sent after reload');
  assert.equal(calls.find(call=>call.path==='/api/ask/send').body.selection.fast,true,'Fast survives reload into the request');
  const sentAttachments=calls.find(call=>call.path==='/api/ask/send').body.attachments;
  assert.deepEqual(sentAttachments.map(item=>item.id),[file.id,url.id,element.id,draftFile.id]);
  assert.equal(sentAttachments[0].path,file.path);assert.equal(sentAttachments[1].url,url.url);assert.equal(sentAttachments[2].selector,element.selector);
});

// Whether focus moved while the chat loaded is unit tested (askFocusAfterLoad);
// this checks the composer takes focus once the chat opened from History arrives.
test("the History drawer takes focus, returns it when dismissed, and its chat focuses the composer", { skip }, async t => {
  const selection = { provider: 'Codex', model: 'test-model', effort: 'Low' };
  const prompts = ['A prior question', 'What kinds of errors in access control or permissions are my agents consistently facing?', 'An older question 2', 'An older question 3'];
  const sessions = [0, 3, 15, 45].map((days, index) => ({ id: index ? 'older-chat-'+index : 'focus-chat', createdAt: Date.now()-days*86400000, updatedAt: Date.now()-days*86400000, selection, draft: '', conversation: [{ id: 'focus-chat-turn-'+index, prompt: prompts[index], cli: 'Codex', model: 'test-model', modelLabel: 'Test Model', effort: 'Low', status: 'multiple', message: '', elapsedMs: 1000, reply: 'A saved reply', cards: [] }] }));
  const { page, settled, shot } = await open(t, { ready: '#pharos-ask .pharos-ai:not([hidden])', api: {
    'GET /api/ask/status': { settings: { enabled: true, showInvitation: true, selection }, providers: [{ id: 'Codex' }] },
    'GET /api/ask/models': { id: 'Codex', models: [{ id: 'test-model', label: 'Test Model', efforts: ['Default', 'Low'], default: true }] },
    'GET /api/ask/sessions': { sessions },
    'GET /api/ask/session': ({ query }) => sessions.find(session => session.id === query.get('id')),
    'POST /api/ask/session': ({ body }) => body,
  } });
  const trigger = page.getByRole('button', { name: 'History', exact: true }), drawer = page.getByRole('dialog', { name: 'Chat history', exact: true });
  const question = page.getByRole('textbox', { name: 'Ask a question', exact: true });
  const focused = selector => page.waitForFunction(selector => document.activeElement?.matches(selector), selector);
  await press(trigger);
  const belowHeader = () => page.waitForFunction(() => {
    const header = document.querySelector('body>header').getBoundingClientRect();
    const drawer = document.querySelector('#pharos-chat-history').getBoundingClientRect();
    return Math.abs(drawer.top - header.bottom) <= 1 && drawer.bottom <= innerHeight + 1;
  });
  await belowHeader();
  const originalSize = page.viewportSize();
  await page.setViewportSize({width: 750, height: originalSize.height});
  await belowHeader();
  await page.setViewportSize(originalSize);
  await belowHeader();
  await settled();
  await shot('chat-history');
  await holdsFocus(page, drawer, trigger);
  // The drawer is modal: a click beside it dismisses it.
  await press(trigger);await focused('.pharos-history-close');
  const size = page.viewportSize();
  await page.mouse.click(size.width - 20, size.height / 2);
  await focused('.pharos-history-trigger');
  // Leaving full screen with Back moves the composer; the drawer must not stay open without its backdrop.
  await page.locator('#homeAskSearch').getByRole('button', { name: 'Full screen', exact: true }).click();
  await page.waitForURL(url => url.pathname === '/ask');await settled();
  await drawer.waitFor();
  assert.equal(await drawer.evaluate(element=>element.matches(':modal')),false,'fullscreen history leaves the conversation interactive');
  await shot('ask-fullscreen-inbox');
  await page.goBack();await page.waitForURL(url => url.pathname === '/');
  await activateHeaderTool(page, 'askPharosToggle');
  await question.waitFor();
  assert.equal(await page.locator('#pharos-chat-history').evaluate(element => element.open), false, 'moving the composer closes History');
  await press(trigger);
  assert.ok(await drawer.evaluate(element => element.matches(':modal')), 'History reopens as a modal drawer');
  await page.keyboard.press('Escape');await focused('.pharos-history-trigger');
  await press(trigger);
  // Let the history list finish refreshing before choosing from it.
  await settled();
  await press(page.getByRole('link', { name: 'Open chat: A prior question', exact: true }));
  await page.getByRole('status').filter({ hasText: 'A saved reply' }).waitFor();
  await settled();
  await page.waitForFunction(element => document.activeElement === element, await question.elementHandle());
});

test('Ask Pharos chat links survive reload and browser Back', {skip,timeout:60000}, async t => {
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  const saved={id:'saved-chat',createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',conversation:[{id:'saved-turn',prompt:'A prior question',cli:'Codex',model:'test-model',modelLabel:'Test Model',effort:'Low',status:'multiple',message:'',elapsedMs:1000,reply:'An earlier reply',cards:[]}]};
  const sessions=new Map([[saved.id,saved]]);
  let delaySession=false,releaseSession;
  const delayedSession=new Promise(resolve=>{releaseSession=resolve;});
  t.after(()=>releaseSession());
  const {page,settled}=await open(t,{url:'/',ready:'#pharos-ask .pharos-ai:not([hidden])',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/session':async({query})=>{if(delaySession&&query.get('id')==='saved-chat')await delayedSession;return sessions.get(query.get('id'));},
    'GET /api/ask/sessions':()=>({sessions:[...sessions.values()]}),
    'POST /api/ask/session':({body})=>{
      const session={...(sessions.get(body.id)||{...saved,id:body.id,conversation:[]}),draft:body.draft,selection:body.selection};
      sessions.set(body.id,session);return session;
    },
    'POST /api/ask/send':({body})=>{
      const session={...sessions.get(body.id),draft:'',conversation:[{...saved.conversation[0],id:body.turnID,prompt:body.prompt,reply:'Your latest reply',cli:body.selection.provider,model:body.selection.model,effort:body.selection.effort}]};
      sessions.set(body.id,session);return session;
    },
  }});
  // Visit payloads, selection defaults and history search are unit tested in ask-pharos.test.mjs.
  const question=page.getByRole('textbox',{name:'Ask a question',exact:true});
  const draftSaved=page.waitForResponse(response=>response.url().includes('/api/ask/session')&&response.request().method()==='POST');
  await question.fill('Keep this question');await draftSaved;
  const id=new URL(page.url()).searchParams.get('ask_chat');
  assert.ok(id,'a new draft gets a conversation URL');
  // This checks persisted chat restoration, so finish background card loads
  // before the deliberate reload cancels the old document's requests.
  await settled();
  await page.reload();await settled();
  await page.waitForFunction(()=>document.querySelector('#pharos-question')?.value==='Keep this question');
  assert.ok(await page.evaluate(() => document.activeElement === document.body), 'restoring a chat should leave initial focus alone');
  await page.getByRole('button',{name:'Send question',exact:true}).click();
  await page.getByRole('status').filter({hasText:'Your latest reply'}).waitFor();
  assert.equal(new URL(page.url()).searchParams.get('ask_chat'),id);
  await page.getByRole('button',{name:'New Chat',exact:true}).click();
  assert.equal(new URL(page.url()).searchParams.get('ask_chat'),null);
  await page.goBack();await page.getByRole('status').filter({hasText:'Your latest reply'}).waitFor();
  assert.equal(new URL(page.url()).searchParams.get('ask_chat'),id);
  // Back can arrive before a slow chat load. Its late response must not
  // replace the page the user has already returned to.
  await page.getByRole('button',{name:'New Chat',exact:true}).click();await settled();
  delaySession=true;
  const loading=page.waitForRequest(request=>new URL(request.url()).pathname==='/api/ask/session'&&request.method()==='GET');
  await page.evaluate(()=>window.pharosNavigate('/?ask_chat=saved-chat'));await loading;
  await page.goBack();
  await page.waitForFunction(()=>!document.querySelector('#pharos-question')?.disabled);
  releaseSession();await settled();
  assert.equal(new URL(page.url()).searchParams.get('ask_chat'),null);
  assert.equal(await page.getByRole('status').filter({hasText:'An earlier reply'}).count(),0);
  // A history link opened from another page keeps the chat in the overlay.
  delaySession=false;
  await page.evaluate(()=>window.pharosNavigate('/analyze?analyze_topic=tokens'));await settled();
  const contextURL=page.url();
  await activateHeaderTool(page,'askPharosToggle');
  await page.getByRole('button',{name:'History',exact:true}).click();
  await page.getByRole('link',{name:'Open chat: A prior question',exact:true}).click();
  await page.getByRole('dialog',{name:'Ask Pharos',exact:true}).getByRole('status').filter({hasText:'An earlier reply'}).waitFor();
  assert.equal(page.url(),contextURL,'opening a chat in the overlay must preserve the page URI');
});

// Explicit URL attachments survive reload even when the page URI is long.
for(const url of ['/analyze?analyze_topic=tokens', '/analyze?analyze_topic=tokens&history_search='+'x'.repeat(2100)]) test(`Ask Pharos restores its open and floating surfaces after reload (${url.length>2000?'Analyze, long URI':'Analyze'})`,{skip},async t=>{
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
  const saved=page.waitForResponse(response=>response.url().includes('/api/ask/session')&&response.request().method()==='POST');
  await question.evaluate(element=>{element.value='Keep this open question';element.dispatchEvent(new Event('input',{bubbles:true}));});await saved;await settled();
  await page.getByRole('button',{name:'Attach context',exact:true}).click();
  const attached=page.waitForResponse(response=>response.url().includes('/api/ask/session')&&response.request().method()==='POST'&&response.request().postDataJSON().draftAttachments?.length===1);
  await page.getByRole('button',{name:'Attach current URL',exact:false}).click();await attached;
  const chip=page.locator('#pharos-question').getByRole('button',{name:/Inspect attachment:/});
  const inlineDraft=await question.innerText();await question.focus();
  await reload();
  await modal.waitFor();
  await page.waitForFunction(()=>document.querySelector('#pharos-question')?.value==='Keep this open question');
  assert.ok(await question.evaluate(element=>element===document.activeElement),'the restored modal focuses its editor');
  assert.equal(await question.innerText(),inlineDraft,'reload preserves the draft alongside its explicit attachment');
  assert.equal(await chip.count(),1,'reload keeps the page context');
  assert.equal(page.url(),pageURL,'restoring the overlay keeps the underlying page URI');
  await page.getByRole('button',{name:'Float Ask Pharos',exact:true}).click();
  const handle=panel.getByRole('button',{name:'Move Ask Pharos progress'});
  await handle.focus();await page.keyboard.press('ArrowLeft');
  await reload();
  await panel.waitFor();
  assert.equal(page.url(),pageURL,'restoring floating progress keeps the underlying page URI');
  // Which surface a reload restores (including dismissed) is unit tested by askRestorePresentation.
  await panel.getByRole('button',{name:'Dismiss Ask Pharos progress',exact:true}).click();
  assert.equal(await panel.isVisible(),false,'dismissing hides the progress panel');
  await activateHeaderTool(page,'askPharosToggle');
  assert.equal(await question.evaluate(element=>element.value),'Keep this open question');
  await page.getByRole('button',{name:'Float Ask Pharos',exact:true}).click();
  await panel.getByRole('button',{name:/^Ask Pharos/}).click();
  assert.equal(await question.evaluate(element=>element.value),'Keep this open question');
  await page.getByRole('button',{name:'Close Ask Pharos',exact:true}).click();
  await page.locator('body>header .brand').click();
  await page.locator('#homeAskSearch').getByRole('textbox',{name:'Ask a question',exact:true}).waitFor();
  assert.equal(await question.evaluate(element=>element.value),'Keep this open question','returning Home reattaches the restored composer');
});

for(const mode of ['choice','other']) test(`Ask Pharos clarification supports ${mode} replies and keeps focus usable`,{skip},async t=>{
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  const clarification={question:'By “use”, do you mean model tokens, API cost equivalent, or something else?',options:['Model tokens','API cost equivalent']};
  let session={id:'clarification-chat',createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',clarificationDraft:'',conversation:[{id:'clarify-turn',prompt:'Show me my use',cli:'Codex',model:'test-model',modelLabel:'Test Model',effort:'Low',status:'clarification',reply:'',message:'',elapsedMs:1000,cards:[],clarification}]};
  const submitted=[];
  const {page,settled,shot}=await open(t,{url:'/?ask_chat=clarification-chat',viewport:{width:mode==='choice'?1440:390,height:900},ready:'.pharos-clarification:not([hidden])',api:{
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
  }else{
    await question.fill('An unrelated next question');
    const saved=page.waitForResponse(response=>response.url().includes('/api/ask/session')&&response.request().method()==='POST'&&response.request().postDataJSON().clarificationDraft==='Workspace counts');
    await other.fill('Workspace counts');await saved;
    await settled();
    await page.reload();await settled();
    await page.waitForFunction(()=>document.querySelector('.pharos-clarification-other textarea')?.value==='Workspace counts');
    await other.focus();await page.keyboard.press('Shift+Enter');await page.keyboard.insertText('across all projects');
    expected='Workspace counts\nacross all projects';
    await page.keyboard.press('Enter');
  }
  await page.getByRole('link',{name:/Usage details/}).waitFor();await settled();
  assert.equal(submitted.length,1,'an answer should submit exactly one request');
  assert.equal(submitted[0].prompt,expected);
  assert.equal(new URL(page.url()).searchParams.get('ask_chat'),'clarification-chat');
  assert.equal(await question.evaluate(element=>element.value),'An unrelated next question');
  assert.ok(await question.evaluate(element=>element===document.activeElement),'answering should return keyboard focus to the composer');
  assert.ok(await page.getByRole('button',{name:'A. Model tokens',exact:true}).isDisabled(),'older clarification options must not be submitted again');
  const footer=page.getByRole('button',{name:'Send question',exact:true});
  assert.ok(await footer.evaluate(element=>{const box=element.getBoundingClientRect();return box.top>=0&&box.bottom<=innerHeight;}));
  await question.focus();
  assert.ok(await question.evaluate(element=>element===document.activeElement));
  await shot('answered');
});

test('Ask Pharos inline attachments drag between words and preserve keyboard editing @ 1440',{skip},async t=>{
  const selection={provider:'Codex',model:'test-model',effort:'Low'};let sent,draft;
  const {page,shot}=await open(t,{viewport:{width:1440,height:800},url:'/analyze?analyze_topic=tokens',ready:'#askPharosToggle:not([disabled])',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/sessions':{sessions:[]},'POST /api/ask/session':({body})=>{draft=body;return body;},
    'POST /api/ask/send':({body})=>{sent=body;return {id:body.id,createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',conversation:[{id:body.turnID,prompt:body.prompt,attachments:body.attachments,cli:'Codex',model:'test-model',effort:'Low',status:'multiple',reply:'Here is the breakdown.',cards:[]}]};},
  }});
  await activateHeaderTool(page,'askPharosToggle');
  const editor=page.getByRole('textbox',{name:'Ask a question',exact:true});
  await editor.fill('Find usage by model');
  await page.getByRole('button',{name:'Attach context',exact:true}).click();await page.getByRole('button',{name:'Attach current URL',exact:false}).click();
  const inspect=page.locator('#pharos-question').getByRole('button',{name:/Inspect attachment:/});
  await inspect.focus();await page.keyboard.press('Enter');
  await page.getByRole('dialog',{name:'Attached context Pharos receives',exact:true}).waitFor();
  await page.keyboard.press('Escape');assert.ok(await inspect.evaluate(node=>node===document.activeElement));
  const chip=editor.getByRole('group',{name:/Page URL context/});
  const target=await editor.evaluate(element=>{
    const walker=document.createTreeWalker(element,NodeFilter.SHOW_TEXT);let text;
    while(walker.nextNode())if(walker.currentNode.data.startsWith('Find usage')){text=walker.currentNode;break;}
    const range=document.createRange();range.setStart(text,5);range.setEnd(text,6);const rect=range.getBoundingClientRect();
    return {x:rect.left+1,y:rect.top+rect.height/2};
  });
  const source=await chip.boundingBox();
  await page.mouse.move(source.x+source.width/2,source.y+source.height/2);await page.mouse.down();
  await page.mouse.move(target.x,target.y,{steps:20});await page.mouse.up();
  await page.waitForFunction(()=>document.querySelector('#pharos-question').firstChild?.textContent==='Find ');
  const savedDraft=page.waitForResponse(response=>response.url().includes('/api/ask/session')&&response.request().method()==='POST'&&JSON.parse(response.request().postData()).draftAttachments?.[0]?.offset===6);
  await chip.focus();await page.keyboard.press('Alt+ArrowRight');
  await page.waitForFunction(()=>document.querySelector('#pharos-question').firstChild?.textContent==='Find u');
  await savedDraft;
  assert.equal(draft.draft,'Find usage by model');assert.equal(draft.draftAttachments[0].offset,6);
  await shot('attachment-preview');
  await page.locator('#pharos-question').getByRole('button',{name:/Remove attachment:/}).click();
  assert.ok(await editor.evaluate(node=>node===document.activeElement));
  assert.equal(await editor.evaluate(node=>node.value),'Find usage by model');
  await editor.press('Meta+ArrowDown');await page.keyboard.press('Shift+Enter');await page.keyboard.insertText('this week');
  await page.getByRole('button',{name:'Send question',exact:true}).click();
  await page.getByRole('status').filter({hasText:'Here is the breakdown.'}).waitFor();
  assert.equal(sent.prompt,'Find usage by model\nthis week');assert.ok(!sent.currentURI);assert.deepEqual(sent.attachments,[]);
});

for(const [width,height,motion] of [[2133,1070,'no-preference'],[320,480,'reduce']])test(`Ask Pharos dialog grows within the viewport and settles back after Clear (${motion}) @ ${width}`,{skip},async t=>{
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
  const editor=page.getByRole('textbox',{name:'Ask a question',exact:true});
  const hintVisible=()=>editor.evaluate(element=>{
    const hint=getComputedStyle(element,'::after');
    return !['none','normal','""'].includes(hint.content)&&hint.display!=='none'&&hint.visibility==='visible';
  });
  assert.ok(await hintVisible(),'the empty, unfocused composer offers an invitation');
  await shot('empty-invitation');
  await editor.focus();
  assert.equal(await hintVisible(),false,'focus hides the invitation before any typing');
  await shot('focused-empty');
  await editor.fill('An unsent draft');
  await page.locator('.pharos-model-trigger').focus();
  assert.equal(await hintVisible(),false,'a blurred draft must not regain a placeholder');
  await editor.fill('');
  assert.equal(await hintVisible(),false,'deleting the draft while focused keeps the invitation hidden');
  await page.locator('.pharos-model-trigger').focus();
  assert.ok(await hintVisible(),'leaving an empty composer restores the invitation');
  await activateHeaderTool(page,'askPharosToggle');
  const modal=page.getByRole('dialog',{name:'Ask Pharos',exact:true});
  assert.equal(await hintVisible(),false,'opening the modal focuses a clear editor');
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
  const emptyFrame=await modal.locator('.pharos-ai-frame').boundingBox();
  assert.ok(empty.y>0&&empty.y+empty.height<=height,'the empty overlay floats inside the viewport');
  await shot('empty-position');
  await modal.getByRole('button',{name:'History',exact:true}).click();await finishMotion();
  assert.ok(await modal.getByRole('button',{name:'Close chat history',exact:true}).evaluate(element=>{const box=element.getBoundingClientRect(),hit=document.elementFromPoint(box.x+box.width/2,box.y+box.height/2);return element===hit||element.contains(hit);}),'history controls remain reachable as the dialog makes room for the menu');
  await modal.getByRole('button',{name:'Close chat history',exact:true}).click();await finishMotion();
  const clear=modal.locator('.pharos-clear-chat'),close=modal.getByRole('button',{name:'Close Ask Pharos',exact:true});
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
  await editor.fill('An unsent question');assert.equal(await clear.getAttribute('aria-label'),'Clear');await checkWindowControls();await shot('draft-clear');
  await clear.click();
  assert.equal(await editor.evaluate(element=>element.value),'');
  assert.ok(await editor.evaluate(element=>element===document.activeElement),'clearing a draft returns focus to the composer');
  assert.ok(await clear.isHidden(),'Clear hides when the composer is empty');
  await modal.evaluate(element=>{element.observedTransitions=[];element.addEventListener('transitionrun',event=>{if(event.target===element)element.observedTransitions.push(event.propertyName);});});
  await editor.fill('Give me a detailed overview of my recent work.');
  await page.getByRole('button',{name:'Send question',exact:true}).click();
  await page.getByRole('region',{name:'AI request in progress',exact:true}).waitFor();
  assert.equal(await clear.getAttribute('aria-label'),'New Chat');
  await checkWindowControls();
  const activeFrame=await modal.locator('.pharos-ai-frame').boundingBox();
  if(width>1000)assert.ok(activeFrame.width>emptyFrame.width,'the active conversation uses the available width');
  else assert.ok(activeFrame.x>=0&&activeFrame.x+activeFrame.width<=width,'the conversation fits a narrow viewport');
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
  assert.ok(Math.abs((await modal.locator('.pharos-ai-frame').boundingBox()).width-emptyFrame.width)<1,'Clear restores the empty composer width');
  await page.setViewportSize({width:320,height:350});await settled();await finishMotion();
  assert.ok(await editor.evaluate(fits),'the empty composer fits after shrinking the window');
  assert.ok(await page.getByRole('button',{name:'Close Ask Pharos',exact:true}).evaluate(fits),'Close stays reachable after shrinking the window');
});

// Opens Analyze with Ask enabled; the send stub resolves `received`, then waits for `finish`.
async function openAskOverlay(t, width) {
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  const state={sent:null};
  const response=new Promise(resolve=>{state.finish=resolve;});
  state.received=new Promise(resolve=>{state.receive=resolve;});
  t.after(()=>state.finish());
  const app=await open(t,{url:'/analyze?analyze_topic=tokens',viewport:{width,height:650},ready:'body>header',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/sessions':{sessions:[]},
    'POST /api/ask/session':({body})=>body,
    'POST /api/ask/send':async({body})=>{
      state.sent=body;state.receive();await response;
      return {id:body.id,createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',conversation:[{id:body.turnID,prompt:body.prompt,currentURI:body.currentURI,cli:'Codex',model:'test-model',effort:'Low',status:'single',message:'',elapsedMs:1000,reply:'Ready to explore.',cards:[{id:'library',title:'Search',kind:'Page',description:'Your past work',href:'/library'}]}]};
    },
  }});
  await app.page.waitForFunction(()=>document.querySelector('.pharos-current-settings')?.textContent.includes('Test Model'));
  await activateHeaderTool(app.page,'askPharosToggle');
  await app.page.getByRole('button',{name:'Attach context',exact:true}).click();await app.page.getByRole('button',{name:'Attach current URL',exact:false}).click();
  return {...app,state,modal:app.page.getByRole('dialog',{name:'Ask Pharos',exact:true}),editor:app.page.getByRole('textbox',{name:'Ask a question',exact:true})};
}

test('Ask Pharos overlay keeps focus and its controls reachable @ 1440', {skip}, async t => {
  const {page,shot,modal,editor}=await openAskOverlay(t,1440);
  const askHeader=page.locator('#askPharosToggle');
  assert.equal(await askHeader.getAttribute('aria-expanded'),'true');
  assert.equal(await askHeader.getAttribute('aria-current'),null);
  await page.evaluate(()=>{
    const original=window.fetch;window.askDraftAttempts=0;
    window.fetch=(input,options)=>{
      if(String(input)==='/api/ask/session'&&options?.method==='POST'&&++window.askDraftAttempts===1)
        return Promise.resolve(new Response(JSON.stringify({error:'Pharos is busy'}),{status:503,headers:{'Content-Type':'application/json'}}));
      return original(input,options);
    };
  });
  assert.ok(await editor.evaluate(element=>element===document.activeElement));
  const fullscreen = modal.getByRole('button',{name:'Full screen',exact:true});
  await fullscreen.hover();
  await page.waitForFunction(() => {
    const button = document.querySelector('.pharos-fullscreen');
    const tooltip = getComputedStyle(button, '::after');
    return tooltip.content === '"Full screen"' && tooltip.visibility === 'visible';
  }, null, {timeout: 3000});
  await page.locator('body>header .brand').evaluate(element=>element.focus());
  assert.ok(await modal.evaluate(element=>element.contains(document.activeElement)),'the overlay must keep focus');
  await editor.evaluate(element=>{element.value='What if we broke it down by model?';element.dispatchEvent(new Event('input',{bubbles:true}));});
  await page.waitForFunction(()=>window.askDraftAttempts>=1);
  assert.ok(await modal.locator('.pharos-notice').isHidden(),'a retrying draft save does not show an error');
  await page.waitForFunction(()=>window.askDraftAttempts>=2);
  assert.ok(await modal.locator('.pharos-notice').isHidden(),'the recovered save leaves no error');
  const reachable=element=>{const box=element.getBoundingClientRect(),target=document.elementFromPoint(box.x+box.width/2,box.y+box.height/2);return box.left>=0&&box.right<=innerWidth&&(element===target||element.contains(target));};
  const removeContext=modal.getByRole('button',{name:/^Remove attachment:/});
  for(const button of [modal.getByRole('button',{name:'Clear',exact:true}),modal.getByRole('button',{name:'Close Ask Pharos',exact:true}),removeContext])assert.ok(await button.evaluate(reachable),'the control must stay reachable');
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
  await removeContext.click();
  assert.ok(await editor.evaluate(element=>element===document.activeElement),'removing context returns focus to the request');
  assert.equal(await editor.evaluate(element=>element.value),'What if we broke it down by model?','removing context preserves the draft');
  await modal.getByRole('button',{name:'Close Ask Pharos',exact:true}).click();
  await modal.waitFor({state:'hidden'});
  assert.equal(await askHeader.getAttribute('aria-expanded'),'false');
  assert.equal(await askHeader.getAttribute('aria-current'),null);
  assert.ok(await page.locator('#askPharosToggle').evaluate(element=>element===document.activeElement)||await page.locator('#headerMore').evaluate(element=>element===document.activeElement),'Close returns focus to the header');
  await activateHeaderTool(page,'askPharosToggle');
  await page.getByRole('button',{name:'Switch to non-AI search',exact:true}).click();
  await modal.getByRole('button',{name:'Search past work',exact:true}).click();
  await modal.getByRole('textbox',{name:'Text for rule 1.1',exact:true}).fill('cache');
  await modal.getByRole('button',{name:'Search messages',exact:true}).click();
  await page.waitForURL(url=>url.pathname==='/library');
  await modal.waitFor({state:'hidden'});
  const brand=page.locator('body>header .brand');await brand.focus();
  assert.ok(await brand.evaluate(element=>element===document.activeElement),'ordinary search results must allow background interaction');
});

test('Ask Pharos errors float over full screen and dismiss without moving it @ 390', {skip}, async t => {
  // A saved CLI that is no longer installed reports without a failed request.
  const {page,shot}=await open(t,{url:'/ask',viewport:{width:390,height:700},ready:'#askPage .pharos-notice:not([hidden])',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection:{provider:'Removed',model:'',effort:'Default'}},providers:[{id:'Codex'}]},
  }});
  const notice=page.locator('#askPage .pharos-notice');
  assert.equal(await page.locator('#askPharosToggle').getAttribute('aria-current'),'page');
  assert.equal(await page.locator('#askPharosToggle').getAttribute('aria-expanded'),'false');
  assert.match(await notice.getByRole('alert').textContent(),/Removed is unavailable/);
  const surface=page.locator('#askPage .pharos-product > :not(.pharos-notice-anchor):not([hidden])').first();
  const shown=await surface.boundingBox(),box=await notice.boundingBox(),ask=await page.locator('#askPage .pharos-product').boundingBox();
  assert.ok(box.x>=ask.x&&box.x+box.width<=ask.x+ask.width&&box.y>=ask.y,'the message stays on screen over Ask');
  await shot('ask-error-notice');
  await notice.getByRole('button',{name:'Dismiss message',exact:true}).focus();
  await page.keyboard.press('Enter');
  await notice.waitFor({state:'hidden'});
  assert.deepEqual(await surface.boundingBox(),shown,'showing the message must not move Ask');
  assert.ok(await page.evaluate(()=>!!document.activeElement?.closest('#askPage .pharos-product')),'dismissing keeps focus in Ask');
});

test('Ask Pharos full screen moves the chat to its own page and back @ 390', {skip}, async t => {
  const {page,shot,modal,editor,state}=await openAskOverlay(t,390);
  const askHeader=page.locator('#askPharosToggle');
  assert.equal(await askHeader.getAttribute('aria-expanded'),'true');
  await page.getByRole('button',{name:'Full screen',exact:true}).click();
  await page.waitForURL(url=>url.pathname==='/ask');
  await modal.waitFor({state:'hidden'});
  assert.equal(await askHeader.getAttribute('aria-current'),'page');
  assert.equal(await askHeader.getAttribute('aria-expanded'),'false');
  assert.ok(await editor.evaluate(element=>!!element.closest('#askPage')&&element===document.activeElement),'full screen keeps the composer focused on its page');
  // Suggestions sit inside the composer until the user types.
  const suggestions=page.getByRole('region',{name:'Prompt suggestions',exact:true});
  await suggestions.waitFor();
  assert.ok(await suggestions.evaluate(element=>!!element.closest('.pharos-ai')));
  await shot('ask-full-screen-empty');
  await editor.pressSequentially('Which tools fail most?');
  await suggestions.waitFor({state:'hidden'});
  await page.getByRole('button',{name:'Send question',exact:true}).click();
  await page.getByRole('region',{name:'AI request in progress',exact:true}).waitFor();
  await state.received;
  assert.equal(new URL(page.url()).searchParams.get('ask_chat'),state.sent?.id,'the page names the chat it is showing');
  assert.ok(state.sent.attachments[0].url.startsWith('/analyze?'),'the question keeps the explicitly attached page');
  // The transcript fills the page, keeping the composer at the bottom.
  await page.waitForFunction(()=>{const box=document.querySelector('.pharos-send').getBoundingClientRect();return box.bottom<=innerHeight&&box.bottom>innerHeight-120;});
  await shot('ask-full-screen');
  await page.getByRole('button',{name:'Exit full screen',exact:true}).click();
  await page.waitForURL(url=>url.pathname==='/analyze');
  await modal.waitFor();
  assert.equal(await askHeader.getAttribute('aria-current'),null);
  assert.equal(await askHeader.getAttribute('aria-expanded'),'true');
  assert.ok(await modal.evaluate(element=>element.contains(document.activeElement)),'leaving full screen reopens the dialog it came from');
  await page.getByRole('button',{name:'Full screen',exact:true}).click();
  await page.waitForURL(url=>url.pathname==='/ask');
  await page.goBack();
  await page.waitForURL(url=>url.pathname==='/analyze');
  const panel=page.getByRole('complementary',{name:'Ask Pharos progress'});
  await panel.waitFor();
  assert.ok(await modal.isHidden(),'Back leaves full screen, floating a pending question');
  assert.equal(await askHeader.getAttribute('aria-current'),null);
  assert.equal(await askHeader.getAttribute('aria-expanded'),'false');
  // Header tabs switch views without a route event; they still leave full screen.
  await panel.locator('.pharos-floating-open').click();await modal.waitFor();
  await page.getByRole('button',{name:'Full screen',exact:true}).click();
  await page.waitForURL(url=>url.pathname==='/ask');
  await activateHeaderTool(page,'settingsToggle');
  await page.waitForURL(url=>url.pathname==='/settings');
  await panel.waitFor();
  await activateHeaderTool(page,'askPharosToggle');
  await modal.waitFor();
  assert.ok(await editor.evaluate(element=>element===document.activeElement),'Ask reopens after a header tab leaves full screen');
  // Picture in picture from full screen opened on Home floats there instead of returning inline.
  await page.keyboard.press('Escape');await panel.waitFor();
  await page.locator('body>header .brand').click();
  await page.waitForURL(url=>url.pathname==='/');
  await page.locator('#homeAskSearch').getByRole('button',{name:'Full screen',exact:true}).click();
  await page.waitForURL(url=>url.pathname==='/ask');
  await page.getByRole('button',{name:'Float Ask Pharos',exact:true}).click();
  await page.waitForURL(url=>url.pathname==='/');
  await panel.waitFor();
  assert.equal(await page.locator('#homeAskSearch .pharos-product').evaluate(element=>element.inert),true,'Home is disabled while the chat floats');
  await page.locator('#homeAskSearch .pharos-home-floating-indicator').waitFor();
  assert.ok(await panel.locator('.pharos-floating-open').evaluate(element=>element===document.activeElement),'focus moves to the floating panel');
  await panel.getByRole('button',{name:'Dismiss Ask Pharos progress'}).click();
  assert.equal(await page.locator('#homeAskSearch .pharos-product').evaluate(element=>element.inert),false,'dismissing restores Home');
  await page.locator('#homeAskSearch').getByRole('textbox',{name:'Ask a question',exact:true}).waitFor();
});

test('Ask Pharos full screen keeps a chat that is still loading', {skip}, async t => {
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  const chat=(id,prompt)=>({id,createdAt:1,updatedAt:id==='slow'?2:1,selection,draft:'',conversation:[{id:id+'-turn',prompt,cli:'Codex',model:'test-model',modelLabel:'Test Model',effort:'Low',status:'multiple',message:'',elapsedMs:1000,reply:'Done.',cards:[]}]});
  const chats={slow:chat('slow','Slow question'),other:chat('other','Other question')};
  const gates={};const release=id=>gates[id]?.();
  const held=id=>new Promise(resolve=>{gates[id]=resolve;});
  let waiting={};
  t.after(()=>Object.values(gates).forEach(open=>open()));
  const {page,settled,shot}=await open(t,{url:'/',viewport:{width:1920,height:1050},ready:'#pharos-ask .pharos-ai:not([hidden])',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/sessions':()=>({sessions:Object.values(chats)}),
    'GET /api/ask/session':async({query})=>{const id=query.get('id');await waiting[id];return chats[id];},
    'POST /api/ask/session':({body})=>({...chats[body.id],...body}),
  }});
  const prompts=()=>page.locator('.pharos-user-question').allTextContents();
  // Entering full screen while a chat from History loads opens that chat.
  waiting={slow:held('slow')};
  await page.getByRole('button',{name:'History',exact:true}).click();
  await page.getByRole('link',{name:'Open chat: Slow question',exact:true}).click();
  await page.locator('#homeAskSearch').getByRole('button',{name:'Full screen',exact:true}).click();
  await page.waitForURL(url=>url.pathname==='/ask'&&url.searchParams.get('ask_chat')==='slow');
  release('slow');waiting={};
  await page.locator('#askPage .pharos-user-question').filter({hasText:'Slow question'}).waitFor();
  await shot('fullscreen-conversation');
  // Leaving full screen for the dialog while another chat loads keeps that chat.
  await activateHeaderTool(page,'settingsToggle');await page.waitForURL(url=>url.pathname==='/settings');
  await activateHeaderTool(page,'askPharosToggle');
  await page.getByRole('button',{name:'Full screen',exact:true}).click();
  await page.waitForURL(url=>url.pathname==='/ask');
  if(!await page.getByRole('dialog',{name:'Chat history',exact:true}).isVisible())await page.getByRole('button',{name:'History',exact:true}).click();
  await page.getByRole('dialog',{name:'Chat history',exact:true}).getByRole('button',{name:'New chat',exact:true}).click();
  waiting={other:held('other')};
  await page.getByRole('link',{name:'Open chat: Other question',exact:true}).click();
  await page.getByRole('button',{name:'Exit full screen',exact:true}).click();
  await page.waitForURL(url=>url.pathname==='/settings');
  const modal=page.getByRole('dialog',{name:'Ask Pharos',exact:true});await modal.waitFor();
  release('other');waiting={};
  await modal.locator('.pharos-user-question').filter({hasText:'Other question'}).waitFor();
  await settled();
  assert.deepEqual(await prompts(),['Other question']);
});

// Placement of dragged and resized progress is unit tested in ask-pharos.test.mjs.
test('Ask Pharos floating progress keeps focus and a ready response leaves the page alone @ 390', {skip}, async t => {
  const {page,shot,modal,editor,state}=await openAskOverlay(t,390);
  await modal.getByRole('button',{name:/^Remove attachment:/}).click();
  await editor.evaluate(element=>{element.value='What if we broke it down by model?';element.dispatchEvent(new Event('input',{bubbles:true}));});
  await page.getByRole('button',{name:'Send question',exact:true}).click();
  const browseHint=page.locator('.pharos-browse-hint');await browseHint.waitFor();
  await page.waitForFunction(()=>{const hint=document.getElementById('pharos-browse-hint'),rect=hint.getBoundingClientRect();return rect.left>=0&&rect.right<=innerWidth&&rect.top>=0&&rect.bottom<=innerHeight;});
  await page.waitForFunction(element=>new Promise(resolve=>{const observer=new IntersectionObserver(([entry])=>{observer.disconnect();resolve(entry.intersectionRatio===1);});observer.observe(element);}),await browseHint.elementHandle());
  assert.ok(await page.getByRole('button',{name:'Float Ask Pharos',exact:true}).evaluate(element=>{const rect=element.getBoundingClientRect(),target=document.elementFromPoint(rect.x+rect.width/2,rect.y+rect.height/2);return element===target||element.contains(target);}), 'the hint leaves picture in picture clickable');
  await shot('ask-browse-hint');
  await page.getByRole('button',{name:'Dismiss browse hint',exact:true}).click();
  await browseHint.waitFor({state:'hidden'});
  assert.equal(await page.evaluate(()=>document.activeElement?.getAttribute('aria-label')),'Float Ask Pharos','dismissing the hint leaves focus on picture in picture');
  await page.getByRole('button',{name:'Float Ask Pharos',exact:true}).click();
  await modal.waitFor({state:'hidden'});
  const panel=page.getByRole('complementary',{name:'Ask Pharos progress'});
  await panel.waitFor();
  await panel.locator('.pharos-floating-open').click();
  await modal.waitFor();
  assert.ok(await editor.evaluate(element=>element===document.activeElement));
  await page.getByRole('button',{name:'Float Ask Pharos',exact:true}).click();await panel.waitFor();
  await activateHeaderTool(page,'settingsToggle');
  await page.waitForURL('**/settings*');
  await panel.getByRole('button',{name:'Dismiss Ask Pharos progress'}).click();
  await panel.waitFor({state:'hidden'});
  // Reopen while pending, then close with Escape: this restores the floating panel.
  await activateHeaderTool(page,'askPharosToggle');
  assert.ok(await editor.evaluate(element=>element===document.activeElement));
  await page.keyboard.press('Escape');await modal.waitFor({state:'hidden'});await panel.waitFor();
  state.finish();
  await panel.getByRole('button',{name:/Your response is ready/}).waitFor();
  assert.equal(new URL(page.url()).pathname,'/settings','a ready response must leave the current page alone');
  assert.ok(!state.sent.currentURI,'the request respects the removed page context');
  await shot('ask-floating-ready');
  await panel.getByRole('button',{name:/Your response is ready/}).click();
  await page.locator('.pharos-destination:not([hidden])').click();
  await modal.waitFor({state:'hidden'});
  await page.waitForURL(url=>url.pathname==='/library');
  // Opening the answer floats the chat; focus returns to the picture in picture panel that reopened it.
  await panel.getByRole('button',{name:/Ask a follow-up/}).waitFor();
  assert.ok(await panel.locator('.pharos-floating-open').evaluate(element=>element===document.activeElement));
});

test('Ask Pharos on Home can keep browsing without losing the active chat', {skip}, async t => {
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  let finish;
  const response=new Promise(resolve=>{finish=resolve;});t.after(()=>finish());
  const {page,shot}=await open(t,{url:'/',viewport:{width:1920,height:1050},ready:'#pharos-ask .pharos-ai:not([hidden])',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/sessions':{sessions:[]},
    'POST /api/ask/session':({body})=>body,
    'POST /api/ask/send':async({body})=>{await response;return {id:body.id,createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',conversation:[{id:body.turnID,prompt:body.prompt,cli:'Codex',model:'test-model',effort:'Low',status:'clarification',message:'',elapsedMs:1000,reply:'',cards:[],clarification:{question:'Which metric?',options:['Tokens','Cost']}}]};},
  }});
  await page.waitForFunction(()=>document.querySelector('.pharos-current-settings')?.textContent.includes('Test Model'));
  const homeFrame=page.locator('#homeAskSearch .pharos-ai-frame');
  const emptyHomeWidth=(await homeFrame.boundingBox()).width;
  await page.getByRole('textbox',{name:'Ask a question',exact:true}).fill('Break it down');
  await page.getByRole('button',{name:'Clear',exact:true}).click();
  assert.equal(await page.getByRole('textbox',{name:'Ask a question',exact:true}).evaluate(element=>element.value),'');
  assert.ok(await page.getByRole('textbox',{name:'Ask a question',exact:true}).evaluate(element=>element===document.activeElement),'clearing a Home draft returns focus to the composer');
  await page.getByRole('textbox',{name:'Ask a question',exact:true}).fill('Break it down');
  await page.getByRole('button',{name:'Float Ask Pharos',exact:true}).click();
  const panel=page.getByRole('complementary',{name:'Ask Pharos progress'});await panel.waitFor();
  const homeProduct=page.locator('#homeAskSearch .pharos-product');
  assert.equal(await homeProduct.evaluate(element=>element.inert),true);
  await page.locator('#homeAskSearch .pharos-home-floating-indicator').waitFor();
  await shot('ask-home-floating');
  assert.equal(await page.locator('#homeAskSearch #pharos-question').evaluate(element=>{element.focus();return element===document.activeElement;}),false,'the disabled composer cannot receive focus');
  await panel.locator('.pharos-floating-open').click();
  assert.equal(await page.locator('#askPharosDialog .pharos-product').evaluate(element=>element.inert),false,'reopening enables the composer');
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
  await page.emulateMedia({reducedMotion:'no-preference'});
  await page.getByRole('button',{name:'Send question',exact:true}).click();
  const puzzle=page.locator('.pharos-waiting:not([hidden]) .pharos-puzzle');
  await puzzle.waitFor();
  await page.waitForFunction(()=>document.querySelectorAll('.pharos-puzzle span').length===8&&[...document.querySelectorAll('.pharos-puzzle span')].every(tile=>tile.getAnimations().length===1));
  const centered=()=>puzzle.evaluate(element=>{
    const icon=element.getBoundingClientRect(),text=element.nextElementSibling.getBoundingClientRect();
    return Math.abs((icon.top+icon.bottom-text.top-text.bottom)/2)<1;
  });
  assert.ok(await centered(),'the puzzle is centered beside the initial message');
  await page.waitForFunction(()=>typeof window.pharosAskProgress==='function');
  await page.evaluate(()=>window.pharosAskProgress({message:'Browsing available views…',elapsedMs:1000}));
  await page.locator('.pharos-elapsed').filter({hasText:'elapsed'}).waitFor();
  assert.ok(await centered(),'the puzzle is centered beside the message and elapsed time');
  await page.getByRole('button',{name:'Float Ask Pharos',exact:true}).click();
  await panel.waitFor();
  assert.equal(await puzzle.evaluate(element=>[...element.children].reduce((count,tile)=>count+tile.getAnimations().length,0)),0,'hidden puzzles stop animating');
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
  await shot('home-answer');
  assert.ok((await homeFrame.boundingBox()).width>emptyHomeWidth,'a restored conversation uses more of the available Home width');
  await page.locator('#homeAskSearch').getByRole('button',{name:'New Chat',exact:true}).click();
  assert.ok(Math.abs((await homeFrame.boundingBox()).width-emptyHomeWidth)<1,'clearing the conversation restores the Home composer width');
});

test('Ask Pharos blocking and reopened browsing overlays handle a ready destination', {skip}, async t => {
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  let finish,response,answers=0;
  const hold=()=>{response=new Promise(resolve=>{finish=resolve;});};
  t.after(()=>finish?.());
  const {page}=await open(t,{url:'/library',ready:'body>header',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/sessions':{sessions:[]},
    'POST /api/ask/session':({body})=>body,
    'POST /api/ask/send':async({body})=>{
      // The first answer opens Settings; the second, asked from Settings, opens Search.
      const href=answers++?'/library':'/settings';
      await response;
      return {id:body.id,createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',conversation:[{id:body.turnID,prompt:body.prompt,cli:'Codex',model:'test-model',effort:'Low',status:'single',message:'',elapsedMs:1000,reply:'Your destination.',cards:[{id:href,title:'Destination',kind:'Page',description:'A destination',href}]}]};
    },
  }});
  await page.waitForFunction(()=>document.querySelector('.pharos-current-settings')?.textContent.includes('Test Model'));
  const modal=page.getByRole('dialog',{name:'Ask Pharos',exact:true});
  const ask=async prompt=>{
    hold();
    await activateHeaderTool(page,'askPharosToggle');
    await page.getByRole('textbox',{name:'Ask a question',exact:true}).fill(prompt);
    await page.getByRole('button',{name:'Send question',exact:true}).click();
  };
  // Blocking: the ready answer opens its destination.
  await ask('Open settings');
  finish();
  await page.waitForURL(url=>url.pathname==='/settings');await modal.waitFor({state:'hidden'});
  // The chat pops out into picture in picture, ready for a follow-up about the opened page.
  const panel=page.getByRole('complementary',{name:'Ask Pharos progress',exact:true});
  await panel.waitFor();
  await page.locator('.pharos-pip-ghost').waitFor({state:'detached'});
  await panel.locator('.pharos-floating-open').click();await modal.waitFor();
  assert.ok(await modal.evaluate(element=>element.contains(document.activeElement)));
  await page.getByRole('button',{name:'Close Ask Pharos',exact:true}).click();await modal.waitFor({state:'hidden'});
  // Browsing, then reopened: the ready answer waits for a click.
  await ask('Open search');
  await page.getByRole('button',{name:'Float Ask Pharos',exact:true}).click();
  await activateHeaderTool(page,'askPharosToggle');
  finish();
  const search=page.locator('.pharos-destination[href="/library"]:not([hidden])');
  await search.waitFor();
  assert.equal(new URL(page.url()).pathname,'/settings');
  assert.ok(await modal.evaluate(element=>element.contains(document.activeElement)));
  await search.click();
  await page.waitForURL(url=>url.pathname==='/library');await modal.waitFor({state:'hidden'});
});

test('Quick Stats disclosure and editing keep keyboard focus on the control being used', { skip }, async t => {
  const { page, settled, shot } = await open(t, { url: '/settings/layout', ready: '#quickStatsExpand' });
  let finish;
  const pending = new Promise(resolve => {finish = resolve;});
  t.after(() => finish());
  await page.route('**/api/usage/summary*', async route => {await pending; await route.continue();});
  const disclosure = page.locator('#quickStatsExpand');
  await press(disclosure);
  await page.locator('#quickStatsOptions').waitFor();
  await page.locator('#quickStatsPreview td.usage-summary-pending').first().waitFor();
  assert.ok(await disclosure.evaluate(element => document.activeElement === element));
  const handle = page.getByRole('button', { name: 'Arrange Tokens', exact: true });
  const focused = async locator => page.waitForFunction(element => document.activeElement === element, await locator.elementHandle());
  await handle.focus();
  finish(); await settled();
  // Loaded preview and toolbox values follow the numeric column headings,
  // even when their existing cells were first rendered as placeholders.
  await page.waitForFunction(() => {
    const heading = document.querySelector('#quickStatsPreview th[data-quick-stats-window]');
    return [...document.querySelectorAll('#quickStatsEditor td')].every(cell => getComputedStyle(cell).textAlign === getComputedStyle(heading).textAlign);
  });
  await focused(handle);
  await shot('quick-stats-values-loaded');
  await press(handle, 'Alt+ArrowUp');
  await focused(handle);
  await page.keyboard.press('Delete');
  const add = page.getByRole('button', { name: 'Add Tokens', exact: true });
  await focused(add);
  await press(add);
  await focused(handle);
  await page.evaluate(() => dispatchEvent(new Event('pharos:carbon-settings')));
  await focused(handle);
  await shot('quick-stats-editor');
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
  for(const [width,height] of [[1440,900],[320,568]]) {
    await page.setViewportSize({width,height});
    assert.ok(await dialog.evaluate(el=>{const r=el.getBoundingClientRect();return r.left>=0 && r.right<=innerWidth && r.top>=0 && r.bottom<=innerHeight && el.scrollWidth<=el.clientWidth;}),'default card copy fits the viewport');
  }
  await dialog.getByRole('textbox',{name:'Title',exact:true}).fill('My provider tokens');
  await dialog.getByRole('textbox',{name:'Description',exact:true}).fill('A moving view of my token usage.');
  await dialog.getByRole('combobox',{name:'Category',exact:true}).selectOption('cost');
  await dialog.getByRole('textbox',{name:'Call to action',exact:true}).fill('See where my tokens go');
  for(const [width,height] of [[1440,900],[320,568]]) {
    await page.setViewportSize({width,height});
    assert.ok(await dialog.evaluate(el=>{const r=el.getBoundingClientRect();return r.left>=0 && r.right<=innerWidth && r.top>=0 && r.bottom<=innerHeight && el.scrollWidth<=el.clientWidth;}),'dialog fits the viewport');
  }
  // Its actions stay reachable by scrolling the dialog on a short screen.
  await dialog.evaluate(el=>{el.scrollTop=el.scrollHeight;});
  assert.ok(await dialog.getByRole('button',{name:'Create card',exact:true}).evaluate(el=>el.getBoundingClientRect().bottom<=innerHeight),'Create card can be scrolled into view');
  await shot('create-dashboard-card');
  await dialog.getByRole('button',{name:'Create card',exact:true}).click();
  await page.locator('.dashboard-card-actions').getByRole('link',{name:'Home',exact:true}).click();
  const card = page.locator('.home-collection .query-card').filter({hasText:'My provider tokens'});
  await card.locator('.query-cover[aria-busy]').waitFor({state:'detached'});
  await shot('created-dashboard-card-home');
  // Home's editor changes the same labels.
  await card.getByRole('button',{name:'Edit My provider tokens',exact:true}).click();
  const editor = page.getByRole('dialog',{name:'Make this view yours'});
  await editor.getByRole('combobox',{name:'Category',exact:true}).selectOption('usage');
  await editor.getByRole('textbox',{name:'Call to action',exact:true}).fill('Open my tokens');
  await shot('edit-dashboard-card-home');
  await editor.getByRole('button',{name:'Save changes',exact:true}).click();
  await editor.waitFor({state:'hidden'});
  await card.getByRole('link').click();
  await page.locator('.usage-dashboard .usage-chart').waitFor();
});

test('Ask Pharos loads chart previews behind the floating panel so a ready answer opens with its charts', {skip}, async t => {
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  const cards=['provider','model'].map(split=>({id:split,title:'Tokens by '+split,description:'Explore the selected token usage.',href:'/analyze?'+new URLSearchParams({analyze_mode:'dashboards',analyze_topic:'tokens',analyze_chart:JSON.stringify({v:2,view:'compare',metric:'tokens',split,unit:'conversation',minSamples:0}),analyze_filters:encodeQuery({...EMPTY_QUERY,where:[]})})}));
  let finish;
  const response=new Promise(resolve=>{finish=resolve;});
  t.after(()=>finish());
  const {page,calls}=await open(t,{url:'/library',ready:'body>header',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/sessions':{sessions:[]},
    'POST /api/ask/session':({body})=>body,
    'POST /api/ask/send':async({body})=>{await response;return {id:body.id,createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',conversation:[{id:body.turnID,prompt:body.prompt,cli:'Codex',model:'test-model',effort:'Low',status:'multiple',message:'',elapsedMs:1000,reply:'Your token usage.',cards}]};},
  }});
  await page.waitForFunction(()=>document.querySelector('.pharos-current-settings')?.textContent.includes('Test Model'));
  await activateHeaderTool(page,'askPharosToggle');
  const modal=page.getByRole('dialog',{name:'Ask Pharos',exact:true});
  const question=page.getByRole('textbox',{name:'Ask a question',exact:true});
  await question.fill('Show my token usage');await question.press('Enter');
  await page.getByRole('button',{name:'Float Ask Pharos',exact:true}).click();await modal.waitFor({state:'hidden'});
  const panel=page.getByRole('complementary',{name:'Ask Pharos progress'});
  finish();
  await panel.getByRole('button',{name:/Your response is ready/}).waitFor();
  await page.waitForFunction(count=>{const charts=[...document.querySelectorAll('figure[aria-label^="Chart preview:"]')];return charts.length===count&&charts.every(chart=>chart.getAttribute('aria-busy')==='false');},cards.length);
  const queries=()=>calls.filter(call=>/^\/api\/query\/usage\/(metrics|query-v3)$/.test(call.path)).length;
  const loaded=queries();
  assert.ok(loaded>0,'floating answers query their charts before they are opened');
  await panel.getByRole('button',{name:/Your response is ready/}).click();await modal.waitFor();
  for (const card of cards) assert.equal(await page.getByRole('figure',{name:'Chart preview: '+card.title,exact:true}).getAttribute('aria-busy'),'false','opening the answer shows its loaded chart');
  assert.equal(queries(),loaded,'opening a just-loaded answer reuses its charts');
});

for (const width of [1920,320]) test(`Ask Pharos work previews fit beside their links @ ${width}`, {skip}, async t => {
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  const cards=[{id:'work',title:'Long work',description:'The whole workspace.',href:'/work/'+workID},{id:'conversation',title:'Root conversation',description:'One conversation in it.',href:'/work/'+workID+'?conversation=conv-root'},{id:'child',title:'Child conversation',description:'A second conversation.',href:'/work/'+workID+'/conversation/conv-child'}];
  // Synthetic ribbon used only to exercise image loading and preview layout.
  const colors=['#399bed','#20bfae','#b36df1','#f4b632','#ef9535','#2bc48e','#f16b52','#eace32'];
  const ribbon='data:image/svg+xml,'+encodeURIComponent('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 320 40">'+colors.map((color,index)=>`<rect x="${index*40}" y="${8+index%3*4}" width="38" height="${24-index%3*8}" fill="${color}"/>`).join('')+'</svg>');
  let session;
  const home=width===1920;
  const {page,shot,calls}=await open(t,{url:home?'/':'/library',ready:home?'#pharos-ask .pharos-ai:not([hidden])':'.conversation-result-card',viewport:{width,height:home?1050:900},api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/sessions':()=>({sessions:session?[session]:[]}),
    'GET /api/ask/session':()=>session,
    'POST /api/ask/session':({body})=>(session={...session,...body}),
    'POST /api/ask/send':({body})=>(session={id:body.id,createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',conversation:[{id:body.turnID,prompt:body.prompt,cli:'Codex',model:'test-model',effort:'Low',status:'multiple',message:'',elapsedMs:1000,reply:'Your work.',cards}]}),
    'POST /api/fingerprints':({body})=>({items:body.items.map(item=>({...item,state:'ready',href:'/work/'+workID+'/conversation/'+item.id,images:{light:ribbon,dark:ribbon}}))}),
  }});
  await page.waitForFunction(()=>document.querySelector('.pharos-current-settings')?.textContent.includes('Test Model'));
  if(!home)await activateHeaderTool(page,'askPharosToggle');
  const surface=home?page.locator('#homeAskSearch'):page.getByRole('dialog',{name:'Ask Pharos',exact:true});
  const question=page.getByRole('textbox',{name:'Ask a question',exact:true});
  await question.fill('What did I do in the long work?');
  const before=page.url();
  await question.press('Enter');
  for (const card of cards) {
    const figure=page.getByRole('figure',{name:'Work preview: '+card.title,exact:true});
    await figure.waitFor();await figure.scrollIntoViewIfNeeded();
    await page.waitForFunction(title=>document.querySelector(`figure[aria-label="Work preview: ${title}"]`)?.getAttribute('aria-busy')==='false',card.title);
    assert.equal(await page.getByRole('link',{name:new RegExp(card.title)}).getAttribute('href'),card.href);
    if(card.id!=='work')await figure.getByRole('img',{name:'Conversation fingerprint',exact:true}).waitFor();
  }
  const requested=calls.filter(call=>call.path==='/api/fingerprints').flatMap(call=>call.body.items);
  for(const id of ['conv-root','conv-child'])assert.ok(requested.some(item=>item.entity==='conversation'&&item.id===id),'the conversation ID selects its fingerprint');
  for (const card of cards) assert.equal(await page.getByRole('figure',{name:'Work preview: '+card.title,exact:true}).locator('p').count(),2,'where/when and who/how much render');
  const after=new URL(page.url());if(home)after.searchParams.delete('ask_chat');
  assert.equal(after.href,before,'previews leave the underlying page URL alone');
  assert.ok(await surface.evaluate(element=>element.contains(document.activeElement)),'loading previews must not move focus out of Ask');
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'work preview overflowed the viewport');
  assert.ok(await page.evaluate(()=>[...document.querySelectorAll('.pharos-work-preview')].every(el=>el.scrollWidth<=el.clientWidth)),'work preview content overflowed its card');
  await shot('ask-work-previews');
});

// Preview resolution, jobs and unavailable charts are unit tested in ask-pharos.test.mjs.
for (const [width, count, view] of [[390,2,'compare'],[1440,1,'trend']]) test(`Ask Pharos chart previews keep links, page state and focus usable (${view}, ${count} charts) @ ${width}`, {skip}, async t => {
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  const cards=Array.from({length:count},(_,index)=>({id:'chart-'+index,title:index?'Tokens by model':'Tokens by provider',description:'Explore the selected token usage.',href:'/analyze?'+new URLSearchParams({analyze_mode:'dashboards',analyze_topic:'tokens',...view==='trend'?{analyze_window:'today',analyze_timezone:'America/Denver'}:{},analyze_chart:JSON.stringify({v:2,view,metric:'tokens',split:index?'model':'provider',unit:'conversation',minSamples:0,...view==='trend'?{rollingPeriods:3,norm:true}:{}}),analyze_filters:encodeQuery({...EMPTY_QUERY,where:[]})})}));
  let session;
  const {page,shot,settled,calls}=await open(t,{url:'/library',ready:'.conversation-result-card',viewport:{width,height:900},api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/sessions':()=>({sessions:session?[session]:[]}),
    'GET /api/ask/session':()=>session,
    'POST /api/ask/session':({body})=>(session={...session,...body}),
    'POST /api/ask/send':({body})=>(session={id:body.id,createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',conversation:[{id:body.turnID,prompt:body.prompt,cli:'Codex',model:'test-model',effort:'Low',status:count===1?'single':'multiple',message:'',elapsedMs:1000,reply:'Your token usage.',cards}]}),
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
  // One click collects the chart's own dashboard address, as in the Card Library.
  const collect=modal.getByRole('button',{name:'Collect '+cards[0].title,exact:true});
  await collect.click();
  await collect.waitFor({state:'detached'});
  await page.waitForFunction(()=>window.pharosPrefs.get('pharos-bookmarks-v1',[]).length===1);
  const saved=await page.evaluate(()=>window.pharosPrefs.get('pharos-bookmarks-v1',[])[0]);
  assert.deepEqual([saved.url,saved.title,saved.dashboard],[cards[0].href,cards[0].title,true]);
  await page.waitForFunction(()=>document.activeElement?.matches('.pharos-chart-preview .query-card-open'));
  assert.ok(await modal.evaluate(element=>element.contains(document.activeElement)),'collecting keeps focus on the chart in Ask');
  assert.equal(page.url(),before,'collecting a chart leaves the underlying page alone');
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
    // Reopening itself revalidates a preview older than a minute, even
    // without a calendar or dataset event.
    await page.getByRole('button',{name:'Close Ask Pharos',exact:true}).click();await modal.waitFor({state:'hidden'});
    await page.clock.setFixedTime(new Date(await page.evaluate(()=>Date.now()+61000)));
    refreshed=page.waitForResponse(metricRequest);
    await activateHeaderTool(page,'askPharosToggle');await refreshed;
    await page.waitForFunction(()=>document.querySelector('figure[aria-label^="Chart preview:"]')?.getAttribute('aria-busy')==='false');
    assert.equal(page.url(),before,'revalidation preserves the surrounding page state');
    assert.ok(await modal.evaluate(element=>element.contains(document.activeElement)),'revalidation preserves focus in Ask');
    return;
  }
  await shot('ask-chart-previews');
  await page.getByRole('button',{name:'New Chat',exact:true}).click();
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
  const link=modal.getByRole('link',{name:new RegExp(cards[0].title)});
  await link.click();
  await page.waitForURL(url=>url.pathname==='/analyze'&&url.searchParams.get('analyze_topic')==='tokens');
  await modal.waitFor({state:'hidden'});
});

for(const width of [1440,390])test(`Data freshness follows the native modal and Background Processing at ${width}px`,{skip},async t=>{
  const {idleStatus}=await import('./fixtures.mjs');
  const status={...idleStatus,freshness:[...idleStatus.freshness,{domain:'an.ledger',label:'Tool ledger',behind:{state:'current'}},{domain:'an.authorship',group:'analyses',label:'Writing estimates',as_of:new Date(Date.now()-3600000).toISOString(),pending_since:new Date(Date.now()-187*60000).toISOString(),behind:{state:'behind',count:128,unit:'message'},reason:{code:'changed',text:'New messages are waiting'},next:'Next frequent update'}]};
  const {page,calls,push,shot,settled}=await open(t,{url:'/analyze?analyze_topic=human&readiness_preview=hours',viewport:{width,height:1000},ready:'.usage-analysis-trend[aria-busy="false"]',api:{'GET /api/status':status,'GET /api/status/stream':eventStream(status)}});
  assert.equal(await page.locator('.data-freshness-icon use').evaluateAll(elements=>elements.filter(element=>!document.getElementById(element.getAttribute('href').slice(1))).length),0,'freshness symbols resolve in the served page shell');
  await shot('lag-clock');
  await activateHeaderTool(page,'dataReadinessToggle');
  const dialog=page.locator('#dataReadinessDialog');await dialog.waitFor();
  assert.ok(await dialog.evaluate(element=>element.contains(document.activeElement)));
  await page.locator('body>header .brand').evaluate(element=>element.focus());assert.ok(await dialog.evaluate(element=>element.contains(document.activeElement)));
  const picker=dialog.getByRole('combobox',{name:'Preview example'});
  await picker.selectOption('days');await page.waitForFunction(()=>document.getElementById('dataReadinessTitle')?.textContent.includes('2 days'));
  await picker.selectOption('missing');await page.locator('.data-readiness-empty').first().waitFor();
  assert(await page.locator('.data-readiness-empty [role="progressbar"]').count()>0);
  await page.setViewportSize({width,height:320});
  const constrained=await dialog.locator('.data-guide-body').evaluate(element=>({client:element.clientHeight,scroll:element.scrollHeight}));
  assert(constrained.scroll>constrained.client,'long update details scroll inside a short viewport');
  assert(await dialog.getByRole('link',{name:'Update settings'}).isVisible());
  await page.setViewportSize({width,height:1000});
  await picker.selectOption('clear');await page.waitForFunction(()=>document.getElementById('dataReadinessTitle')?.textContent==='Up to date as of 6 minutes ago');
  const compact=await dialog.boundingBox();
  assert(compact.height<900,'short update details do not fill the viewport');
  await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'});
  await shot('current-check');
  await page.evaluate(()=>window.pharosNavigate('/analyze?analyze_topic=human'));
  await activateHeaderTool(page,'dataReadinessToggle');await dialog.waitFor();
  await shot('update-details');
  const updated={...status,freshness:status.freshness.map(row=>row.domain==='an.authorship'?{...row,pending_since:null,behind:{state:'current'},reason:null,as_of:new Date().toISOString()}:row)};
  push('/api/status/stream',updated);await page.waitForFunction(()=>/Up to date|No updates waiting|Showing saved results/.test(document.getElementById('dataReadinessTitle')?.textContent));
  await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'});
  assert.ok(await page.evaluate(()=>['dataReadinessToggle','headerMore'].includes(document.activeElement?.id)));
  push('/api/status/stream',status);
  await page.evaluate(()=>window.pharosNavigate('/analyze?analyze_topic=tools'));
  await page.locator('.usage-tools-dashboard [aria-busy="false"]').first().waitFor();
  await page.waitForFunction(()=>document.getElementById('dataReadinessToggle')?.getAttribute('aria-label').includes('Up to date'));
  await page.evaluate(()=>window.pharosNavigate('/analyze?analyze_topic=human'));
  await page.locator('[data-readiness-result="chart:writing_messages"]').click();await dialog.waitFor();
  assert.notEqual(await dialog.locator('header small').innerText(),'This page');
  await dialog.getByRole('link',{name:'Update settings',exact:true}).click();await page.locator('#backgroundSchedule .bg-col').first().waitFor();await settled();
  assert(await page.locator('.data-readiness-workload').isVisible());
  assert.equal(calls.filter(call=>call.method==='POST'&&/^\/api\/(sync|background|labels)/.test(call.path)).length,0,'reviewing status never enqueues work or changes Settings');
  assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
  await shot('settings');
});

for(const width of [1440,390])test(`Freshness is reachable on collapsed tables and bespoke dashboard panels at ${width}px`,{skip},async t=>{
 const queryLayout=encodeURIComponent(JSON.stringify({library:true}));
 const {idleStatus}=await import('./fixtures.mjs');
 const background=structuredClone(defaultApi['GET /api/background']);
 const upgrade={...background.view.items.find(item=>item.id==='writing-refine'),id:'upgrade',label:'Library upgrade',verb:'Upgrading the library',fixed:true};
 background.view.items.push(upgrade);background.view.cadences.find(cadence=>cadence.id==='daily').items.push('upgrade');
 const status={...idleStatus,background:background.view};
 const {page,settled,push,shot}=await open(t,{url:'/library?query_layout='+queryLayout,viewport:{width,height:1000},ready:'[data-readiness-result="query:library"]',api:{'GET /api/background':background,'GET /api/status':status,'GET /api/status/stream':eventStream(status)}});
 const dialog=page.locator('#dataReadinessDialog');
 const check=async (selector)=>{
  const button=page.locator(selector).first();await button.waitFor();
  if(selector.includes('query:')){
   // The readiness action precedes request timing in the rendered run bar,
   // including the narrow layout where the controls may wrap onto a new row.
   const placement=await button.evaluate(element=>{
    const activity=element.closest('.data-source-query')?.querySelector('button[aria-label="Request activity"]');
    if(!activity)return null;
    const a=element.getBoundingClientRect(),b=activity.getBoundingClientRect();
    return {before:a.top<b.top || (a.top===b.top && a.left<b.left), sameRow:a.top===b.top};
   });
   assert(placement?.before,'readiness appears before request timing');
   if(width===1440)assert(placement.sameRow,'desktop run actions share a row');
  }
  await button.focus();assert(await button.evaluate(element=>element===document.activeElement));
  await button.click();await dialog.waitFor();assert(await dialog.evaluate(element=>element.contains(document.activeElement)));
  await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'});
  assert(await button.evaluate(element=>element===document.activeElement));
  await settled();assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
 };
 await check('[data-readiness-result="query:library"]');
 for(const [url,identity] of [
  ['/findings?view=table','query:findings'],
  ['/findings?optimize_area=safety&view=table&query_layout='+encodeURIComponent(JSON.stringify({safety:true})),'query:safety'],
  ['/findings?optimize_area=fixes&view=table','query:fixes'],
  ['/analyze?analyze_topic=habits','chart:habits-calendar'],
  ['/analyze?analyze_topic=thinking-overview','chart:thinking-waits'],
  ['/analyze?analyze_topic=thinking-detail','chart:thinking-distributions'],
  ['/analyze?analyze_topic=parallelism','chart:ribbon'],
  ['/analyze?analyze_topic=carbon','carbon:estimate'],
  ['/analyze?analyze_topic=agent_experience','events:examples'],
 ]){
  await page.evaluate(url=>window.pharosNavigate(url),url);
  await check(`[data-readiness-result="${identity}"]`);
  if(identity==='events:examples'&&process.env.PHAROS_UI_SCREENSHOTS){
   await activateHeaderTool(page,'dataReadinessToggle');await dialog.waitFor();
   await shot('agent-experience-update-details');
   await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'});
  }
  if(identity==='query:safety'){
   const table=page.locator('.safety-table .data-source-query');
   await table.getByTitle('Configure auto-update').click();
   await page.locator('.qt-auto-refresh-popover').getByRole('button',{name:'Start',exact:true}).click();
   await table.getByTitle('View or clear auto-update').waitFor();
   await check(`[data-readiness-result="${identity}"]`);
   await table.getByTitle('View or clear auto-update').click();
   await page.locator('.qt-auto-refresh-popover').getByRole('button',{name:'Clear',exact:true}).click();
  }
 }
 // Follow a bespoke chart's prerequisite through the real Settings disclosure.
 // The domain/task reduction itself is covered by the readiness unit tests.
 push('/api/status/stream',{...status,freshness:[...status.freshness,{domain:'an.ledger',label:'Tools ledger',behind:{state:'behind',count:8,unit:'conversation'},reason:{code:'version',text:'This version of Pharos rebuilds it'},next:'updates when you run the library upgrade'}]});
 await page.waitForFunction(()=>window.pharosStatusStore?.current()?.view?.library?.freshness?.some(row=>row.domain==='an.ledger'));
 await page.evaluate(()=>window.pharosNavigate('/analyze?analyze_topic=thinking-overview'));
 await page.locator('[data-readiness-result="chart:thinking-waits"]').click();await dialog.waitFor();
 await dialog.getByRole('link',{name:'Update settings',exact:true}).click();
 await page.locator('#background-detail-upgrade').waitFor();
});

// These are live ledger snapshots: no preview parameter, and the running
// snapshot has the same reason replacement the Go ledger actually emits.
test('Live data updates keep real backlog, existing results and first-build progress', {skip}, async t=>{
 const {idleStatus}=await import('./fixtures.mjs');
 const iso=minutes=>new Date(Date.now()-minutes*60000).toISOString();
 const currentSource={domain:'conv:claude',label:'Conversations (claude)',task_id:'conversations',as_of:iso(2),behind:{state:'current'}};
 const view={domain:'view',label:'Search view',behind:{state:'current'}};
 const status={...idleStatus,freshness:[{...currentSource,pending_since:iso(540),behind:{state:'behind',count:128,unit:'conversation'},reason:{code:'needs-capture',text:'New conversations are waiting'}},view,{domain:'conv:new',label:'Conversations (new)',as_of:null,output_missing:true,behind:{state:'behind'},reason:{code:'never-indexed',text:'Not indexed yet'}}]};
 const excluded={...EMPTY_QUERY,where:[{field:'source_kind',op:'=',value:'new',negated:true}]};
 const bookmark={id:'durable-readiness',title:'Saved conversations',url:'/library?q_library=@aaaaaaaaaaaaaaaaaaaaaa',query:excluded};
 const {page,push,settled,shot}=await open(t,{url:'/library?view=conversation&q_library='+encodeURIComponent(encodeQuery(excluded)),ready:'.conversation-result-card',preferences:{'pharos-bookmarks-v1':[bookmark]},api:{
  'GET /api/status':status,'GET /api/status/stream':eventStream(status),
  '* /api/query/:dataset/:operation':call=>{
   const reply=defaultApi['* /api/query/:dataset/:operation'](call);
   if(call.params.dataset==='writing_messages'&&call.body?.metrics?.some(metric=>metric.dateGrouping||metric.groupBy?.length)&&reply.metrics)return {...reply,metrics:reply.metrics.map(metric=>({...metric,buckets:[]}))};
   return call.params.dataset==='writing_messages'&&['rows','rows-v2','query-v3'].includes(call.params.operation)?{...reply,rows:[],total:0,sourceTotal:0}:reply;
  },
 }});
 // Settle the capture frame as well as fetches, then check an ordinary reload.
 for(let attempt=0;attempt<2;attempt++){
  await settled();
  assert.equal(new URL(page.url()).searchParams.has('readiness_preview'),false);
  assert.equal(await page.locator('.conversation-result-card').count(),48);
  await activateHeaderTool(page,'dataReadinessToggle');
  const dialog=page.locator('#dataReadinessDialog');await dialog.waitFor();
  assert.match(await dialog.getByRole('heading').innerText(),/9 hours behind/);
  await page.keyboard.press('Escape');
  await dialog.waitFor({state:'hidden'});
  // Native dismissal precedes the close event that persists URL state.
  await page.waitForFunction(()=>new URL(location.href).searchParams.get('readiness_open')==='0');
  if(!attempt){await page.reload();await page.locator('.conversation-result-card').first().waitFor();}
 }
 const first={...status,freshness:[currentSource,view,{domain:'an.authorship',label:'Writing estimates',as_of:null,output_missing:true,behind:{state:'behind',count:128,unit:'message'},reason:{code:'never-built',text:'Not built yet'}}]};
 push('/api/status/stream',first);
 await page.evaluate(()=>window.pharosNavigate('/analyze?analyze_mode=query&analyze_data=writing_messages'));
 const empty=page.locator('.data-readiness-empty');await empty.waitFor();
 await empty.getByRole('progressbar').waitFor();
 for(const state of ['queued','running']){
  push('/api/status/stream',{...first,freshness:first.freshness.map(row=>row.domain==='an.authorship'?{...row,reason:{code:'running',text:'Updating now'},catching_up:'op-writing'}:row),operations:[{id:'op-writing',kind:'analysis.authorship',label:'Writing estimates',state,step:{done:58,total:128,unit:'message'}}]});
  await page.waitForFunction(state=>window.pharosStatusStore?.current()?.view?.library?.operations?.some(operation=>operation.id==='op-writing'&&operation.state===state),state);
  await empty.locator('[role=progressbar][aria-valuenow]').waitFor();
  assert(await empty.isVisible());
  assert.equal(new URL(page.url()).searchParams.has('readiness_preview'),false);
 }
 // The chart's first-build surface must not hide independently loaded tiles.
 await page.evaluate(()=>window.pharosNavigate('/analyze?analyze_topic=human'));
 await page.locator('.usage-analysis-trend[aria-busy="false"]').waitFor();
 await empty.waitFor();
 await page.locator('[aria-label="Human Words summary"][aria-busy="false"]').waitFor();
 assert(await empty.isVisible());
 push('/api/status/stream',{...first,freshness:first.freshness.map(row=>row.domain==='an.authorship'?{...row,output_missing:false,reason:null,as_of:iso(0),behind:{state:'current'}}:row),operations:[]});
 await empty.waitFor({state:'hidden'});
 await page.locator('[data-readiness-result="chart:writing_messages"]').waitFor();
 // A saved query still renders with a durable URL and no local snapshot. Its
 // update details must use the same stored definition as its loaded preview.
 push('/api/status/stream',status);
 await page.evaluate(()=>window.pharosNavigate('/home'));
 const saved=page.locator('[data-card-id="durable-readiness"]');
 await saved.locator('.query-cover:not([aria-busy="true"])').waitFor();
 const readiness=saved.locator('[data-readiness-result="card:durable-readiness"]');
 await saved.hover();
 const refresh=await saved.getByRole('button',{name:'Refresh Saved conversations'}).boundingBox();
 const updates=await readiness.boundingBox();
 const edit=await saved.getByRole('button',{name:'Edit Saved conversations'}).boundingBox();
 assert(refresh.x+refresh.width<=updates.x && updates.x+updates.width<=edit.x,'update details sit between Refresh and Edit');
 await shot('card-readiness-hover');
 await readiness.click();
 const details=page.locator('#dataReadinessDialog');await details.waitFor();
 await details.getByRole('progressbar').first().waitFor();
 await page.keyboard.press('Escape');
 await details.waitFor({state:'hidden'});
 await page.waitForFunction(()=>document.activeElement?.dataset.readinessResult==='card:durable-readiness');
 await page.keyboard.press('Enter');
 await details.waitFor();
 await details.getByRole('link',{name:'Update settings',exact:true}).click();
 await page.locator('#background-detail-conversations').waitFor();
 // Suggested cards also expose their update details on hover.
 await page.evaluate(()=>window.pharosNavigate('/cards'));
 const suggestion=page.locator('.card-library-entry .query-card').first();
 await suggestion.waitFor();
 await suggestion.hover();
 const suggestedReadiness=suggestion.locator('[data-readiness-result]');
 await suggestedReadiness.click();
 await details.waitFor();
 await page.keyboard.press('Escape');
 await details.waitFor({state:'hidden'});
 await page.waitForFunction(()=>document.activeElement?.matches('.card-library-entry [data-readiness-result]'));
});

for (const startup of [false, true]) test(`Host clipboard fallback restores focus and selection in the ${startup ? 'startup' : 'library'} shell`, {skip}, async t => {
  const {page} = await open(t, startup ? {url:'/app', ready:'#application-setup[open]', api:{
    'GET /api/app/state':{selected:'',libraries:[],busy:false},
  }} : {url:'/settings', ready:'#healthCards .panel'});
  // Exercise the actual textarea/execCommand path inside each rendered shell.
  // A selected input in the modal also catches accidental focus escape.
  const result = await page.evaluate(async startup => {
    const input = document.createElement('input');
    input.value = 'keep this selection';
    document.querySelector(startup ? '#application-setup' : 'main').append(input);
    input.focus();
    input.setSelectionRange(2, 8, 'backward');
    let copied;
    document.addEventListener('copy', event => {
      copied = {text:event.target.value, start:event.target.selectionStart, end:event.target.selectionEnd};
    }, {once:true});
    Object.defineProperty(navigator, 'clipboard', {configurable:true, value:undefined});
    await (startup ? window.pharosHost.clipboard.writeText('copied') : window.pharosCopyText('copied'));
    const result = {focused:document.activeElement === input, start:input.selectionStart, end:input.selectionEnd, direction:input.selectionDirection, copied};
    input.remove();
    return result;
  }, startup);
  assert.deepEqual(result, {focused:true, start:2, end:8, direction:'backward', copied:{text:'copied', start:0, end:6}});
});

test('Browser clipboard denial displays copy failure and leaves focus on the copy button', {skip}, async t => {
  const {page} = await open(t, {url:'/settings', ready:'#healthCards .panel'});
  await page.evaluate(() => {
    Object.defineProperty(navigator, 'clipboard', {configurable:true, value:{writeText:async () => {throw new Error('Clipboard permission denied');}}});
    document.querySelector('#navigator').classList.add('open');
  });
  await press(page.locator('#navigatorCopy'));
  await page.locator('#feedbackToast.visible').waitFor();
  assert.ok(await page.locator('#navigatorCopy').evaluate(element => element === document.activeElement));
});

for(const surface of ['import','Downloads'])test(`Claude daily export prompt copying keeps focus through failure and retry in ${surface}`,{skip},async t=>{
  const {page,shot}=await open(t,{url:'/settings/sources',ready:'#pharosImportSource'});
  let parent=page.locator('#claudeDownloadsWatchSection');
  if(surface==='import'){
    await page.locator('#pharosImportSource').click();
    parent=page.locator('#pharosOnboarding');
    await parent.getByRole('radio',{name:'Claude.ai',exact:true}).check();
  }
  const routine=parent.locator('.pharos-claude-routine'),copy=routine.getByRole('button',{name:'Copy daily export prompt',exact:true});
  await copy.waitFor();
  await page.evaluate(()=>{
    window.exportPromptCopies=[];
    Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:text=>{
      window.exportPromptCopies.push(text);window.exportPromptClipboard=Promise.withResolvers();return window.exportPromptClipboard.promise;
    }}});
  });
  await copy.click();
  await page.waitForFunction(()=>!!window.exportPromptClipboard);
  assert.ok(await copy.evaluate(el=>el===document.activeElement),'copying keeps keyboard focus');
  await page.evaluate(()=>window.exportPromptClipboard.reject(Error('Clipboard permission denied')));
  await routine.getByRole('status').filter({hasText:'Could not copy'}).waitFor();
  assert.ok(await copy.evaluate(el=>el===document.activeElement),'failure keeps focus for retry');
  await copy.click();
  await page.waitForFunction(()=>window.exportPromptCopies.length===2);
  await page.evaluate(()=>window.exportPromptClipboard.resolve());
  await routine.getByRole('status').filter({hasText:'Copied.'}).waitFor();
  assert.ok(await copy.evaluate(el=>el===document.activeElement),'success keeps focus');
  const prompt=await routine.locator('pre').innerText();
  assert.deepEqual(await page.evaluate(()=>window.exportPromptCopies),[prompt,prompt],'each attempt copies the visible starter prompt');
  await shot('daily-export-prompt');
});

test('Claude export reminder can be acknowledged and completed history stays usable', {skip}, async t=>{
  const watch={enabled:true,available:true,browser:'',browsers:[],import_history_available:true,last_import_at:'2026-01-01T00:00:00Z',exports:[
    {id:'completed',state:'imported',exported_at:'2026-01-01T00:00:00Z',collected:6,expected:6},
  ],library:{workspaces:1,conversations:1,recent:[{id:workID,title:'Imported conversation',url:`/work/${workID}`}]}};
  const {page,settled,shot}=await open(t,{url:'/settings/sources',viewport:{width:1920,height:1050},ready:'#claudeDownloadsWatchSection .claude-watch-history tbody tr',api:{
    'GET /api/claude-imports':()=>({jobs:[],run:{running:false},watch}),
    'POST /api/claude-imports/watch':({body})=>{if(body.action==='routine_done')watch.routine_done_at=new Date().toISOString();return watch},
  }});
  const root=page.locator('#claudeDownloadsWatchSection'),routine=root.locator('.pharos-claude-routine');
  await routine.waitFor();
  await root.locator('.claude-watch-history').scrollIntoViewIfNeeded();
  await shot('claude-completed-exports');
  assert.equal(await root.getByRole('link',{name:'View conversations',exact:true}).count(),1);
  await routine.getByRole('button',{name:'I’ve set this up',exact:true}).click();
  await routine.waitFor({state:'hidden'});
  assert.ok(await root.locator('#claudeDownloadsWatch').evaluate(element=>element===document.activeElement),'dismissal keeps focus on a visible control');
  await page.reload();await root.locator('.claude-watch-history tbody tr').waitFor();await settled();
  assert.equal(await routine.isVisible(),false,'acknowledgement survives reload');
  watch.routine_done_at='2026-01-01T00:00:00Z';watch.last_import_at=new Date().toISOString();
  await page.evaluate(()=>window.pharosClaudeDownloads.render(document.getElementById('claudeDownloadsWatchSection')));
  await settled();assert.equal(await routine.isVisible(),false,'a recent import hides the reminder');
});

test('Ask fullscreen inbox keeps concurrent chats running and reads only visible answers', {skip,timeout:60000}, async t=>{
  const selection={provider:'Codex',model:'test-model',effort:'Low'},sessions=new Map(),finish=new Map(),attempts=new Map();
  let recordFirst,releaseReceipt,receiptStarted,secondAttempt;
  const firstRecording=new Promise(resolve=>{recordFirst=resolve;}),oldReceipt=new Promise(resolve=>{releaseReceipt=resolve;});
  const readingError=new Promise(resolve=>{receiptStarted=resolve;});
  const heldSearch=Promise.withResolvers(),searchStarted=Promise.withResolvers();
  t.after(()=>{recordFirst();releaseReceipt();heldSearch.resolve();});
  t.after(()=>{for(const resolve of finish.values())resolve();});
  const {page,settled,shot,calls}=await open(t,{url:'/ask',viewport:{width:1920,height:1050},ready:'.pharos-new-chat',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/sessions':async({query})=>{
      const term=query.get('q')||'';
      if(term==='First'){searchStarted.resolve();await heldSearch.promise;}
      return {sessions:[...sessions.values()].filter(session=>!term||session.conversation.some(turn=>turn.prompt.includes(term)))};
    },
    'GET /api/ask/session':({query})=>sessions.get(query.get('id')),
    'POST /api/ask/session':({body})=>{const saved={createdAt:Date.now(),updatedAt:Date.now(),conversation:[],...sessions.get(body.id),...body};sessions.set(body.id,saved);return saved;},
    'POST /api/ask/read':async({body})=>{
      if(body.startedAt===secondAttempt){receiptStarted();await oldReceipt;}
      const turn=sessions.get(body.id)?.conversation.find(turn=>turn.id===body.turnID&&turn.startedAt===body.startedAt);
      if(turn)turn.seenAt=Date.now();return {ok:true};
    },
    'POST /api/ask/send':async({body})=>{
      const attempt=(attempts.get(body.prompt)||0)+1;attempts.set(body.prompt,attempt);
      if(body.prompt==='First question')await firstRecording;
      const session={createdAt:Date.now(),updatedAt:Date.now(),...sessions.get(body.id),...body,conversation:[{id:body.turnID,prompt:body.prompt,cli:'Codex',model:'test-model',status:'loading',startedAt:(body.prompt==='First question'?10:20)+attempt,cards:[],reply:''}]};
      sessions.set(body.id,session);
      if(body.prompt==='Second question'&&attempt===1)secondAttempt=session.conversation[0].startedAt;
      if(attempt===1)await new Promise(resolve=>finish.set(body.prompt,resolve));
      session.conversation[0].status=body.prompt==='First question'?'single':attempt===1?'error':'multiple';
      if(body.prompt==='First question')session.conversation[0].cards=[{id:'library',title:'Recorded work',description:'Explore your recorded work.',kind:'library',href:'/library'}];
      session.conversation[0].reply=body.prompt==='First question'?'The answer to First question':attempt===1?'Original failure':'Recovered answer';session.updatedAt=Date.now();return session;
    },
  }});
  const question=page.getByRole('textbox',{name:'Ask a question',exact:true}),drawer=page.getByRole('dialog',{name:'Chat history',exact:true});
  const newChat=page.getByRole('button',{name:'New chat',exact:true});
  await question.fill('First question');await page.getByRole('button',{name:'Send question',exact:true}).click();
  await page.getByRole('region',{name:'AI request in progress',exact:true}).waitFor();
  await newChat.click();
  // The first send has not reached persistence: reopening must keep its local
  // prompt mounted so both streaming progress and its final answer can render.
  await page.getByRole('link',{name:'Open chat: First question',exact:true}).click();
  await page.locator('.pharos-user-question').filter({hasText:'First question'}).waitFor();
  await page.getByRole('button',{name:'Stop',exact:true}).waitFor();
  recordFirst();
  await newChat.click();await question.fill('Second question');await page.getByRole('button',{name:'Send question',exact:true}).click();
  await page.getByRole('region',{name:'AI request in progress',exact:true}).waitFor();
  await page.waitForFunction(()=>document.querySelectorAll('.pharos-history-entry[data-running="true"]').length===2);
  assert.equal(calls.filter(call=>call.path==='/api/ask/cancel').length,0);
  assert.equal(calls.filter(call=>call.path==='/api/ask/send').length,2);
  await question.focus();
  finish.get('First question')();
  const first=page.getByRole('link',{name:'Open chat: First question',exact:true});
  await first.locator('.pharos-history-state').filter({hasText:'unseen answer'}).waitFor();
  assert.equal(calls.filter(call=>call.path==='/api/ask/read').length,0,'a background answer stays unseen');
  assert.equal(new URL(page.url()).pathname,'/ask','a background destination leaves the inbox open');
  assert.ok(await question.evaluate(element=>element===document.activeElement),'background completion preserves focus');
  await shot('ask-inbox-background-answer');
  await first.click();await page.getByRole('status').filter({hasText:'The answer to First question'}).waitFor();
  await first.locator('.pharos-history-state').filter({hasText:'Seen'}).waitFor();
  finish.get('Second question')();await settled();
  assert.ok(calls.some(call=>call.path==='/api/ask/read'),'viewing an answer saves a receipt');
  const transcript=page.getByRole('region',{name:'Chat messages, scroll for earlier replies'});
  assert.ok(await transcript.evaluate(element=>element.getBoundingClientRect().left>=document.querySelector('#pharos-chat-history').getBoundingClientRect().right),'history sits beside the conversation');
  assert.equal(await drawer.evaluate(element=>element.matches(':modal')),false);

  // Delay the old error's Seen response across reopening and a successful
  // retry. The response must never paint the captured error over that answer.
  const second=page.getByRole('link',{name:'Open chat: Second question',exact:true});
  await second.click();await page.getByRole('status').filter({hasText:'Original failure'}).waitFor();await readingError;
  await first.click();await page.getByRole('status').filter({hasText:'The answer to First question'}).waitFor();
  await second.click();await page.getByRole('button',{name:'Try again',exact:true}).click();
  await page.getByRole('status').filter({hasText:'Recovered answer'}).waitFor();
  await second.locator('.pharos-history-state').filter({hasText:'Seen'}).waitFor();
  const acknowledged=page.waitForResponse(response=>response.url().endsWith('/api/ask/read')&&response.request().postDataJSON()?.startedAt===secondAttempt);
  releaseReceipt();const response=await acknowledged;await response.finished();await settled();
  await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(resolve)));
  assert.ok(await page.getByRole('status').filter({hasText:'Recovered answer'}).isVisible(),'a delayed receipt preserves the retried answer');

  // Both answers are already seen now, so no new receipt can mask a stale
  // sidebar selection after opening either chat or starting a new one.
  await first.click();await page.getByRole('status').filter({hasText:'The answer to First question'}).waitFor();
  assert.equal(await first.getAttribute('aria-current'),'true');assert.equal(await second.getAttribute('aria-current'),'false');
  assert.match(await first.getAttribute('aria-describedby').then(id=>page.locator('#'+id).textContent()),/Current chat/);
  await second.click();await page.getByRole('status').filter({hasText:'Recovered answer'}).waitFor();
  assert.equal(await second.getAttribute('aria-current'),'true');assert.equal(await first.getAttribute('aria-current'),'false');
  await shot('ask-inbox-selected');
  const sendsBeforeSearch=calls.filter(call=>call.path==='/api/ask/send').length;
  const filter=drawer.getByRole('searchbox',{name:'Find a prior chat',exact:true}),search=drawer.getByRole('button',{name:'Search',exact:true});
  await filter.fill('First');await search.click();await searchStarted.promise;
  await drawer.getByRole('status').filter({hasText:'Searching…'}).waitFor();
  await shot('ask-inbox-searching');
  await filter.fill('Second');await filter.press('Enter');
  await second.waitFor();await drawer.getByRole('status').filter({hasText:'Searching…'}).waitFor({state:'hidden'});
  const stale=page.waitForResponse(response=>response.url().includes('/api/ask/sessions?')&&new URL(response.url()).searchParams.get('q')==='First');
  heldSearch.resolve();await stale;await settled();
  assert.equal(await first.count(),0,'an older search must not replace the latest results');
  assert.ok(await filter.evaluate(element=>element===document.activeElement),'search keeps keyboard focus');
  await filter.fill('');await search.click();await first.waitFor();await second.waitFor();
  assert.equal(calls.filter(call=>call.path==='/api/ask/send').length,sendsBeforeSearch,'searching does not send a question');
  await newChat.click();
  assert.equal(await first.getAttribute('aria-current'),'false');assert.equal(await second.getAttribute('aria-current'),'false');
  await page.setViewportSize({width:390,height:844});
  await page.getByRole('button',{name:'History',exact:true}).click();
  assert.ok(await drawer.evaluate(element=>element.matches(':modal')),'narrow windows use the history drawer');
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'narrow layout fits the window');
  await page.keyboard.press('Escape');await question.click();
  await shot('ask-inbox-phone');
});

test('Claude export inspection preserves the replacement form when an older request completes', {skip},async t=>{
  const pending=Promise.withResolvers(),creation=Promise.withResolvers();
  const {page}=await open(t,{url:'/settings/sources',ready:'#pharosAddSource',api:{
    'POST /api/claude-imports':()=>creation.promise,
    'GET /api/claude-imports':({query})=>query.get('id')?pending.promise:{jobs:[{...claudeImportFixture,collected:1,expected:2}],run:{running:false}},
  }});
  await page.evaluate(()=>window.pharosOnboarding.open({mode:'import'}));
  const guide=page.locator('#pharosOnboarding');
  await guide.getByRole('radio',{name:'Claude.ai',exact:true}).check();
  const requested=page.waitForRequest(request=>request.url().includes('/api/claude-imports?id='));
  await guide.getByRole('button',{name:/Resume export/}).click();
  await requested;
  await page.keyboard.press('Escape');
  const replacement=guide.getByRole('textbox',{name:'Export',exact:true});
  await replacement.fill('/replacement-export');
  const response=page.waitForResponse(response=>response.url().includes('/api/claude-imports?id='));
  pending.resolve(claudeImportFixture);
  await response;
  assert.equal(await replacement.inputValue(),'/replacement-export');
  assert.ok(await replacement.evaluate(element=>document.activeElement===element));
  await guide.getByRole('radio',{name:'Claude.ai',exact:true}).check();
  const posted=page.waitForRequest(request=>request.method()==='POST'&&new URL(request.url()).pathname==='/api/claude-imports');
  await guide.getByRole('button',{name:'Import',exact:true}).click();
  await posted;
  await page.keyboard.press('Escape');
  await guide.getByRole('button',{name:'Import an export',exact:true}).click();
  const newer=guide.getByRole('textbox',{name:'Export',exact:true});
  await newer.fill('/new-export-draft');
  const completed=page.waitForResponse(response=>response.request().method()==='POST'&&new URL(response.url()).pathname==='/api/claude-imports');
  creation.resolve({started:true});
  await completed;
  assert.equal(await newer.inputValue(),'/new-export-draft');
  assert.ok(await newer.evaluate(element=>document.activeElement===element));
});


test('Claude settings capture preserves focus through failures, progress and completion', {skip}, async t => {
  for(const reader of [false,true])await t.test(reader?'conversation reader':'Sources',async t=>{
    let captured=false,failOnce=true;
    const refresh=Promise.withResolvers();
    let selectedSettings={observations:[{organization_uuid:'example-org',observed_at:'2026-11-12T12:00:00Z',time_basis:'capture',model:'claude-opus-4-6',effort_level:'high',thinking_mode:'enabled'}]};
    const captureRun={id:'settings-capture',kind:'capture',state:'complete',sources:['claude-chat-settings'],results:[]};
    const indexRun={id:'settings-index',kind:'index',state:'complete',sources:['claude-chat-settings'],results:[]};
    const {page}=await open(t,{url:reader?`/work/${workID}/conversation/${rootConversation}`:'/settings/sources',api:{
      'GET /api/sources':{configured:1,enabled:0,items:[{...claudeSettingsSourceFixture,enabled:false}]},
      'GET /api/work/:id/conversations/:conversation':async()=>{
        if(captured)await refresh.promise;
        return {id:rootConversation,workspace_id:workID,provider:'claude',harness:'claude-ai-export',selected_settings:captured?selectedSettings:undefined,messages:[
          {id:'backfill-question',role:'user',kind:'message',text:'Compare these reports.'},
          {id:'backfill-answer',role:'assistant',kind:'message',text:'The reports use different assumptions.'},
        ]};
      },
      'GET /api/work/:id/delegation':request=>{
        const data=defaultApi['GET /api/work/:id/delegation'](request);
        const updated=row=>row.conversation_id===rootConversation&&row.depth===0
          ? {...row,model:captured?selectedSettings?.observations[0].model:null,effort:captured?selectedSettings?.observations[0].effort_level:null} : row;
        return {...data,rows:data.rows.map(updated),focus:data.focus.map(updated)};
      },
      'GET /api/claude-chat-settings/coverage':()=>({accounts:[{...claudeSettingsCoverageFixture,missing_models:captured?0:2}]}),
      'POST /api/claude-chat-settings/setup':claudeSettingsCoverageFixture.setup,
      'POST /api/capture':()=>({run:{...captureRun,state:failOnce?'failed':'complete'}}),
      'GET /api/capture':()=>({active:false,runs:[{...captureRun,state:failOnce?'failed':'complete'}]}),
      'POST /api/index':()=>{captured=true;return {run:indexRun}},
      'GET /api/activity':{active:0,runs:[indexRun]},
    }});
    const notice=page.locator(reader?'.conversation-panel .claude-settings-backfill':'#claudeSettingsBackfill');
    const button=notice.getByRole('button');
    await button.waitFor();
    let source,turn;
    if(reader){
      await page.locator('.source-details > summary').click();
      await page.locator('.turn > summary').click();
      source=await page.locator('.source-details').elementHandle();
      turn=await page.locator('.turn').elementHandle();
      assert.ok(await source.evaluate(el=>el.open));
      assert.ok(await turn.evaluate(el=>!el.open));
    }
    await button.click();
    await notice.getByRole('status').filter({hasText:'Settings capture did not complete'}).waitFor();
    assert.ok(await button.evaluate(el=>document.activeElement===el),'a failed capture keeps its action focused');
    failOnce=false;
    await button.click();
    if(reader){
      await notice.getByRole('status').filter({hasText:'Refreshing Source details'}).waitFor();
      assert.ok(await button.evaluate(el=>document.activeElement===el),'refreshing metadata keeps the action focused');
      refresh.resolve();
      await page.locator('.source-details dd').filter({hasText:'claude-opus-4-6'}).waitFor();
      await page.locator('.delegation-session-heading').filter({hasText:'claude-opus-4-6'}).waitFor();
    }
    await notice.getByRole('status').filter({hasText:'Settings captured.'}).waitFor();
    assert.ok(await button.evaluate(el=>document.activeElement===el),'completion leaves focus on the same action');
    if(reader){
      assert.ok(await source.evaluate(el=>el.isConnected&&el.open),'refresh keeps the existing Source details open');
      assert.ok(await turn.evaluate(el=>el.isConnected&&!el.open),'refresh keeps the existing turn closed');
      // A later capture can remove a selection; old facts must not linger.
      selectedSettings=undefined;
      await button.click();
      await page.locator('.source-details dd').filter({hasText:'claude-opus-4-6'}).waitFor({state:'detached'});
      await page.locator('.delegation-session-heading').filter({hasText:'claude-opus-4-6'}).waitFor({state:'detached'});
      await notice.getByRole('status').filter({hasText:'Settings captured.'}).waitFor();
      assert.ok(await button.evaluate(el=>document.activeElement===el));
    }
  });
});

test('Claude import offers one-time model backfill after indexing without moving focus', {skip},async t=>{
  const job={...claudeImportFixture,source:{added:['claude-ai-imports'],enabled:[],declined:[],skipped:[]}};
  let indexed=false;
  const captureRun={id:'import-capture',kind:'capture',state:'complete',sources:['claude-ai-imports'],results:[]};
  const indexRun={id:'import-index',kind:'index',state:'complete',sources:['claude-ai-imports'],results:[]};
  const {page}=await open(t,{url:'/settings/sources',api:{
    'GET /api/claude-imports':({query})=>query.get('id')?job:{jobs:[job],run:{running:false,id:job.id,name:job.name}},
    'POST /api/claude-imports':{started:true},
    'GET /api/claude-chat-settings/setup':{enabled:false},
    'GET /api/claude-chat-settings/coverage':()=>({accounts:indexed?[claudeSettingsCoverageFixture]:[]}),
    'POST /api/capture':{run:captureRun},
    'GET /api/capture':{active:false,runs:[captureRun]},
    'POST /api/index':()=>{indexed=true;return {run:indexRun}},
    'GET /api/activity':{active:0,runs:[indexRun]},
  }});
  const guide=page.locator('#pharosOnboarding');
  await page.evaluate(()=>window.pharosOnboarding.open({mode:'import'}));
  await guide.getByRole('radio',{name:'Claude.ai',exact:true}).check();
  await guide.getByRole('textbox',{name:'Export',exact:true}).fill('/example/claude-export.json');
  await guide.getByRole('button',{name:'Import',exact:true}).click();
  await guide.getByRole('button',{name:'Capture and index conversations',exact:true}).click();
  await guide.getByRole('button',{name:'Index now',exact:true}).waitFor();
  assert.ok(await guide.getByRole('button',{name:'Index now',exact:true}).evaluate(el=>el===document.activeElement),'capture continues in the same modal');
  await guide.getByRole('button',{name:'Index now',exact:true}).click();
  await guide.getByRole('button',{name:'Capture settings',exact:true}).waitFor();
  assert.ok(await guide.getByRole('button',{name:'Done',exact:true}).evaluate(el=>el===document.activeElement),'missing-model guidance does not take focus after indexing');
});

test('a late Claude settings check leaves a replacement import form and its focus alone', {skip},async t=>{
  const pending=Promise.withResolvers(),job={...claudeImportFixture,source:{added:['claude-ai-imports'],enabled:[],declined:[],skipped:[]}};
  const {page}=await open(t,{url:'/settings/sources',api:{
    'GET /api/claude-imports':({query})=>query.get('id')?job:{jobs:[job],run:{running:false}},
    'GET /api/claude-chat-settings/setup':()=>pending.promise,
  }});
  await page.evaluate(()=>window.pharosOnboarding.open({mode:'import'}));
  const guide=page.locator('#pharosOnboarding');
  await guide.getByRole('radio',{name:'Claude.ai',exact:true}).check();
  const requested=page.waitForRequest(request=>new URL(request.url()).pathname==='/api/claude-chat-settings/setup');
  await guide.getByRole('button',{name:/Resume export/}).click();await requested;
  await page.keyboard.press('Escape');
  const field=guide.getByRole('textbox',{name:'Export',exact:true});await field.fill('/replacement-import');
  const completed=page.waitForResponse(response=>new URL(response.url()).pathname==='/api/claude-chat-settings/setup');
  pending.resolve({enabled:false,path:'/example/Claude',account:'local'});await completed;
  assert.equal(await field.inputValue(),'/replacement-import');
  assert.ok(await field.evaluate(el=>el===document.activeElement));
});


test('source limitations dialog returns focus and stays usable on a narrow screen', {skip}, async t=>{
  const index=structuredClone(defaultApi['GET /api/index']);index.hosts[0].sources[0].kind='claude';
  const {page,settled,shot}=await open(t,{url:'/settings/sources',viewport:{width:1920,height:1050},ready:'#sourceGrid .source-card',api:{'GET /api/index':index}});
  await settled();
  const info=page.getByRole('button',{name:'Ingestion limitations for claude',exact:true});
  await info.scrollIntoViewIfNeeded();await shot('source-cards');
  await info.click();
  const dialog=page.getByRole('dialog',{name:'claude · ingestion limitations'});
  await dialog.waitFor();
  assert.ok(await dialog.getByRole('button',{name:'Close',exact:true}).evaluate(el=>document.activeElement===el));
  await shot('source-limitations-desktop');
  await page.setViewportSize({width:390,height:844});await shot('source-limitations-mobile');
  await page.keyboard.press('Escape');
  await dialog.waitFor({state:'hidden'});
  await page.waitForFunction(element=>document.activeElement===element,await info.elementHandle());
  const remote=page.getByRole('button',{name:'Ingestion limitations for Claude',exact:true});
  await remote.click();
  await page.getByRole('dialog',{name:'Claude · ingestion limitations'}).waitFor();
  await page.keyboard.press('Escape');
  await page.waitForFunction(element=>document.activeElement===element,await remote.elementHandle());
});
