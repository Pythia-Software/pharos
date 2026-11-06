// Focus and placement that only a real browser can check: modal surfaces take
// focus, keep it while open and give it back on Escape; the transcript rails
// stay below the header; the annotation toolbar stays on screen.
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdirSync } from "node:fs";
import { activateHeaderTool, open, skip } from "./harness.mjs";
import { defaultApi, workID, eventStream, busyStatus } from "./fixtures.mjs";
import { EMPTY_QUERY, encodeQuery, withGroupingKeys } from "@pythia-software/query-table-core";

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

test('Library setup keeps focus in its modal and returns to the choice after cancelling a flow', {skip}, async t => {
  const {page, calls, shot} = await open(t, {url:'/app',ready:'#application-setup[open]',api:{
    'GET /api/app/state': {application:'/Applications/Pharos.app',selected:'',mcp:'',busy:false,libraries:[]},
    'POST /api/app/create': {saved:true},
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
  assert.ok(calls.every(call=>call.path==='/api/app/state'), 'setup wrote data before final review');
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
  assert.ok(!calls.some(call=>call.path==='/api/probe'),'presence detection parsed source history');
  await shot('source-tools');
  await guide.getByRole('button',{name:'Continue',exact:true}).click();
  await guide.locator('.pharos-candidate').first().waitFor();
  assert.ok(await guide.locator('h2').evaluate(element=>element===document.activeElement));
  await guide.getByRole('checkbox',{name:/claude-work\/projects/}).uncheck();
  await shot('source-locations');
  await guide.getByRole('button',{name:'Continue',exact:true}).click();
  assert.ok(await guide.locator('h2').evaluate(element=>element===document.activeElement));
  await shot('source-review');
  await page.keyboard.press('Escape');
  assert.ok(await guide.getByRole('button',{name:'Continue',exact:true}).isVisible(),'Escape left the review');
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

// Habits surrounds the shared chart with its own panels; exercise that page
// at both widths so panel styles cannot break chart hit targets or gestures.
for (const [topic, display, width] of [['tokens', 'interval', 1440], ['tokens', 'cumulative', 1440], ['habits', 'interval', 1440], ['habits', 'interval', 390]]) test(`Dashboard ${topic} ${display} inspection at ${width}px focuses its links and dismisses on Escape or outside click${display === 'interval' ? '; pointer capture snaps to columns' : ''}`, { skip }, async t => {
  const { page, settled, shot } = await open(t, { url: `/analyze?${new URLSearchParams({ analyze_topic: topic, analyze_chart: JSON.stringify({ display }) })}`, viewport: { width, height: 900 }, ready: '.usage-chart-bars:not(.loading) > button' });
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
  await page.evaluate(()=>{window.cloudCopies=0;Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:async()=>{window.cloudCopies++;}}});});
  for(const [index,label]of ['Copy preparation commands','Copy password setup command','Copy final commands'].entries()){
    const button=page.getByRole('button',{name:label,exact:true});await button.focus();await page.keyboard.press('Enter');
    await page.waitForFunction(count=>window.cloudCopies===count,index+1);
    assert.ok(await button.evaluate(element=>element===document.activeElement));
  }
  // Reset focuses the calculator; closing returns to Explore.
  await page.getByRole('button',{name:'Reset cloud backup options',exact:true}).click();
  assert.ok(await page.locator('#cloud-step-0').evaluate(panel=>panel===document.activeElement));
  await page.keyboard.press('Tab');
  assert.ok(await page.getByLabel('Cloud provider',{exact:true}).evaluate(input=>input===document.activeElement));
  await page.getByRole('button',{name:'Close cloud backup options',exact:true}).click();
  assert.ok(await explore.evaluate(input=>input===document.activeElement));
});

test("summary value links retain keyboard focus when the summary rerenders", { skip }, async t => {
  const { page } = await open(t, { url: "/library?view=table", ready: "#queryTableLibrary .qt-row", preferences: { "pharos-usage-summary-links": true, "pharos-quick-stats-v1": {metrics: ["cost_usd", "tokens"], windows: ["today", "6h"]} } });
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
  await page.evaluate(() => { window.ejects = 0; window.webkit = { messageHandlers: { pharosLibrary: { postMessage: async () => { window.ejects++; } } } }; });
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

test("Data guide takes focus, keeps controls reachable and returns focus @ 320", { skip }, async t => {
  const app = await open(t, { url: "/analyze?analyze_mode=query&analyze_data=tool_calls&analyze_topic=tools", viewport: { width: 320, height: 900 }, ready: ".usage-workspace-table .qt-row" });
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
    await aggregate.selectOption("avg");
    assert.equal(await aggregate.inputValue(), "avg");
    // The new aggregation reruns the query; a popover opened mid-render closes.
    await app.settled();
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
  const dialog = page.getByRole("dialog", { name: "Search within work", exact: true });
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
  const { page, settled } = await open(t, { url: "/findings", ready: ".findings-card" });
  const review = page.locator(".findings-cart button.primary").first();
  await press(review);
  const dialog = page.locator('.findings-dialog[role="dialog"]');
  await dialog.waitFor();
  await settled();
  await holdsFocus(page, dialog, review);
});

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
  const show = page.locator('button[aria-label="Show workspace sessions"]');
  await press(show);
  const sheet = page.locator('[role="dialog"][aria-label="Workspace sessions"]');
  await sheet.waitFor();
  await holdsFocus(page, sheet, show);
  await press(show);
  await press(sheet.getByRole('button', { name: 'Hide workspace sessions', exact: true }));
  await page.waitForFunction(() => document.activeElement?.getAttribute('aria-label') === 'Show workspace sessions');
});

test("the workspace sidebar returns focus when collapsed and preserves the reader when reopened", { skip }, async t => {
  const { page, settled } = await open(t, { url: `/work/${workID}/conversation/conv-root`, ready: ".conversation-panel .turn" });
  const reader = page.getByRole('region', { name: 'Conversation viewer', exact: true });
  const hide = page.getByRole('button', { name: 'Hide workspace sessions', exact: true });
  const show = page.getByRole('button', { name: 'Show workspace sessions', exact: true });
  await page.locator('.turn').nth(6).evaluate(turn => turn.scrollIntoView({ block: 'center', behavior: 'instant' }));
  await settled();
  const scroll = await page.locator('.workspace-reader-content').evaluate(el => el.scrollTop);
  await press(hide);
  await page.waitForFunction(() => document.activeElement?.getAttribute('aria-label') === 'Show workspace sessions');
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
    return { header, rails: [...document.querySelectorAll(".conversation-map, .token-map, .conversation-map-window")].filter(rail => rail.getClientRects().length).map(rail => [rail.className, rail.getBoundingClientRect().top]) };
  });
  assert.ok(tops.rails.length, "the rails are shown");
  for (const [name, top] of tops.rails) assert.ok(top >= tops.header - 1, `${name} starts at ${top}, above the header's bottom at ${tops.header}`);

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

test("feedback review keeps focus, fits a phone, and returns focus to its opener", { skip }, async t => {
  const { page, shot } = await open(t, { url: "/settings", ready: "#healthCards .panel" });
  await activateHeaderTool(page, "feedbackSubmitToggle");
  const dialog = page.getByRole("dialog", { name: "Send feedback", exact: true });
  await dialog.waitFor();
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

test('Ask Pharos retry with another model focuses the picker @ 320',{skip},async t=>{
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  const turn={id:'first-turn',prompt:'Which commands keep failing?',cli:'Codex',model:'test-model',modelLabel:'Test Model',effort:'Low',status:'clarification',reply:'',clarification:{question:'Which project?',options:['Pharos','All projects']},cards:[],elapsedMs:1000};
  const original={id:'failed-chat',createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'Keep the time range to this week.',conversation:[turn,{...turn,id:'failed-turn',prompt:'Pharos',status:'error',clarification:null,reply:'The agent returned an empty response; try again'}]};
  const sessions=new Map([[original.id,original]]);
  const {page,settled,shot,calls}=await open(t,{url:'/?ask_chat=failed-chat',viewport:{width:320,height:900},ready:'.pharos-recovery:not([hidden])',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true},{id:'second-model',label:'Second Model',efforts:['Default','Low'],fast:true}]},
    'GET /api/ask/session':({query})=>sessions.get(query.get('id')),
    'GET /api/ask/sessions':()=>({sessions:[...sessions.values()]}),
    'POST /api/ask/session':({body})=>{
      const session={...(sessions.get(body.id)||{id:body.id,createdAt:Date.now(),updatedAt:Date.now(),conversation:[]}),draft:body.draft,selection:body.selection};
      sessions.set(body.id,session);return session;
    },
    'POST /api/ask/send':({body})=>{
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
  assert.deepEqual(sessions.get(original.id).conversation,original.conversation,'the prior chat remains in history');
  await page.reload();await settled();
  await page.waitForFunction(text=>document.querySelector('#pharos-question')?.value===text,expected);
  assert.ok(await page.getByRole('button',{name:'Send question',exact:true}).isEnabled());
  await page.getByRole('button',{name:'Send question',exact:true}).click();
  await page.getByRole('status').filter({hasText:'Your retry completed.'}).waitFor();
  await settled();
  assert.equal(sessions.get(newID).conversation[0].prompt,expected,'the restored transcript can be sent after reload');
  assert.equal(calls.find(call=>call.path==='/api/ask/send').body.selection.fast,true,'Fast survives reload into the request');
});

// Whether focus moved while the chat loaded is unit tested (askFocusAfterLoad);
// this checks the composer takes focus once the chat opened from History arrives.
test("opening a saved chat from History focuses its composer", { skip }, async t => {
  const selection = { provider: 'Codex', model: 'test-model', effort: 'Low' };
  const sessions = [{ id: 'focus-chat', createdAt: Date.now(), updatedAt: Date.now(), selection, draft: '', conversation: [{ id: 'focus-chat-turn', prompt: 'A prior question', cli: 'Codex', model: 'test-model', modelLabel: 'Test Model', effort: 'Low', status: 'multiple', message: '', elapsedMs: 1000, reply: 'A saved reply', cards: [] }] }];
  const { page, settled } = await open(t, { ready: '#pharos-ask .pharos-ai:not([hidden])', api: {
    'GET /api/ask/status': { settings: { enabled: true, showInvitation: true, selection }, providers: [{ id: 'Codex' }] },
    'GET /api/ask/models': { id: 'Codex', models: [{ id: 'test-model', label: 'Test Model', efforts: ['Default', 'Low'], default: true }] },
    'GET /api/ask/sessions': { sessions },
    'GET /api/ask/session': ({ query }) => sessions.find(session => session.id === query.get('id')),
    'POST /api/ask/session': ({ body }) => body,
  } });
  await press(page.getByRole('button', { name: 'History', exact: true }));
  // Let the history list finish refreshing before choosing from it.
  await settled();
  await press(page.getByRole('link', { name: 'Open chat: A prior question', exact: true }));
  await page.getByRole('status').filter({ hasText: 'A saved reply' }).waitFor();
  await settled();
  const question = page.getByRole('textbox', { name: 'Ask a question', exact: true });
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
  await page.getByRole('button',{name:'Clear',exact:true}).click();
  assert.equal(new URL(page.url()).searchParams.get('ask_chat'),null);
  await page.goBack();await page.getByRole('status').filter({hasText:'Your latest reply'}).waitFor();
  assert.equal(new URL(page.url()).searchParams.get('ask_chat'),id);
  // Back can arrive before a slow chat load. Its late response must not
  // replace the page the user has already returned to.
  await page.getByRole('button',{name:'Clear',exact:true}).click();await settled();
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

// The long URI exceeds what a saved prompt chip may hold, so reload restores the context alone.
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
  const chip=question.getByRole('group',{name:/^About/});
  if(url.length<=2000){await chip.focus();await page.keyboard.press('Alt+ArrowRight');}
  const inlineDraft=await question.innerText();await question.focus();
  await reload();
  await modal.waitFor();
  await page.waitForFunction(()=>document.querySelector('#pharos-question')?.value==='Keep this open question');
  assert.ok(await question.evaluate(element=>element===document.activeElement),'the restored modal focuses its editor');
  assert.equal(await question.innerText(),inlineDraft,'reload preserves the context chip position within the draft');
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

test('Ask Pharos prompt chips drag into text and support keyboard editing @ 1440',{skip},async t=>{
  const width=1440;
  const selection={provider:'Codex',model:'test-model',effort:'Low'};let sent;
  const {page,shot}=await open(t,{viewport:{width,height:800},ready:'.pharos-ai:not([hidden])',api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/sessions':{sessions:[]},'POST /api/ask/session':({body})=>body,
    'POST /api/ask/send':({body})=>{sent=body;return {id:body.id,createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',conversation:[{id:body.turnID,prompt:body.prompt,currentURI:body.currentURI,cli:'Codex',model:'test-model',effort:'Low',status:'multiple',reply:'Here is the breakdown.',cards:[]}]};},
  }});
  await page.waitForFunction(()=>document.querySelector('.pharos-current-settings')?.textContent.includes('Test Model'));
  const editor=page.getByRole('textbox',{name:'Ask a question',exact:true}),chip=page.getByRole('group',{name:/^About .+ context\./});
  await activateHeaderTool(page,'askPharosToggle');
  assert.equal(await chip.count(),0,'the home page is not attached as context');
  await page.keyboard.press('Escape');await page.getByRole('dialog',{name:'Ask Pharos',exact:true}).waitFor({state:'hidden'});
  await page.evaluate(()=>window.pharosNavigate('/analyze?analyze_topic=tokens'));
  await activateHeaderTool(page,'askPharosToggle');await chip.waitFor();
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
  assert.match(await editor.innerText(),/Find .*About .*usage by model/s,'the chip moves between the words at the drop caret');
  assert.equal(await editor.evaluate(element=>element.value),'Find usage by model','dragging preserves the question');
  await shot('chip-between-words');
  await editor.press('Meta+z');
  assert.equal(await editor.innerText(),beforeDrag,'undo restores the chip position');
  await editor.press('Meta+Shift+z');
  assert.match(await editor.innerText(),/Find .*About .*usage by model/s);
  await chip.focus();await page.keyboard.press('Alt+ArrowRight');
  assert.match(await editor.innerText(),/Find u.*About .*sage by model/s,'keyboard movement relocates the same chip');
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
  assert.ok(empty.y>0&&empty.y+empty.height<=height,'the empty overlay floats inside the viewport');
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
  return {...app,state,modal:app.page.getByRole('dialog',{name:'Ask Pharos',exact:true}),editor:app.page.getByRole('textbox',{name:'Ask a question',exact:true})};
}

test('Ask Pharos overlay keeps focus and its controls reachable @ 1440', {skip}, async t => {
  const {page,shot,modal,editor}=await openAskOverlay(t,1440);
  assert.ok(await editor.evaluate(element=>element===document.activeElement));
  await page.locator('body>header .brand').evaluate(element=>element.focus());
  assert.ok(await modal.evaluate(element=>element.contains(document.activeElement)),'the overlay must keep focus');
  await editor.evaluate(element=>{element.value='What if we broke it down by model?';element.dispatchEvent(new Event('input',{bubbles:true}));});
  const reachable=element=>{const box=element.getBoundingClientRect(),target=document.elementFromPoint(box.x+box.width/2,box.y+box.height/2);return box.left>=0&&box.right<=innerWidth&&(element===target||element.contains(target));};
  const removeContext=modal.getByRole('button',{name:'Remove page context',exact:true});
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
  await removeContext.click();
  assert.ok(await editor.evaluate(element=>element===document.activeElement),'removing context returns focus to the request');
  assert.equal(await editor.evaluate(element=>element.value),'What if we broke it down by model?','removing context preserves the draft');
  await modal.getByRole('button',{name:'Close Ask Pharos',exact:true}).click();
  await modal.waitFor({state:'hidden'});
  assert.ok(await page.locator('#askPharosToggle').evaluate(element=>element===document.activeElement)||await page.locator('#headerMore').evaluate(element=>element===document.activeElement),'Close returns focus to the header');
  await activateHeaderTool(page,'askPharosToggle');
  await removeContext.waitFor();
  await page.getByRole('button',{name:'Switch to non-AI search',exact:true}).click();
  await modal.getByRole('button',{name:'Search past work',exact:true}).click();
  await modal.getByRole('textbox',{name:'Text for rule 1.1',exact:true}).fill('cache');
  await modal.getByRole('button',{name:'Search messages',exact:true}).click();
  await page.waitForURL(url=>url.pathname==='/library');
  await modal.waitFor({state:'hidden'});
  const brand=page.locator('body>header .brand');await brand.focus();
  assert.ok(await brand.evaluate(element=>element===document.activeElement),'ordinary search results must allow background interaction');
});

test('Ask Pharos full screen moves the chat to its own page and back @ 390', {skip}, async t => {
  const {page,shot,modal,editor,state}=await openAskOverlay(t,390);
  await page.getByRole('button',{name:'Full screen',exact:true}).click();
  await page.waitForURL(url=>url.pathname==='/ask');
  await modal.waitFor({state:'hidden'});
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
  assert.ok(state.sent.currentURI.startsWith('/analyze?'),'the question keeps the page it was asked about');
  // The transcript fills the page, keeping the composer at the bottom.
  await page.waitForFunction(()=>{const box=document.querySelector('.pharos-send').getBoundingClientRect();return box.bottom<=innerHeight&&box.bottom>innerHeight-120;});
  await shot('ask-full-screen');
  await page.getByRole('button',{name:'Exit full screen',exact:true}).click();
  await page.waitForURL(url=>url.pathname==='/analyze');
  await modal.waitFor();
  assert.ok(await modal.evaluate(element=>element.contains(document.activeElement)),'leaving full screen reopens the dialog it came from');
  await page.getByRole('button',{name:'Full screen',exact:true}).click();
  await page.waitForURL(url=>url.pathname==='/ask');
  await page.goBack();
  await page.waitForURL(url=>url.pathname==='/analyze');
  const panel=page.getByRole('complementary',{name:'Ask Pharos progress'});
  await panel.waitFor();
  assert.ok(await modal.isHidden(),'Back leaves full screen, floating a pending question');
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
  assert.equal(await page.locator('#homeAskSearch .pharos-ai').count(),0,'the chat floats rather than returning inline');
  assert.ok(await panel.locator('.pharos-floating-open').evaluate(element=>element===document.activeElement),'focus moves to the floating panel');
});

test('Ask Pharos full screen keeps a chat that is still loading', {skip}, async t => {
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  const chat=(id,prompt)=>({id,createdAt:1,updatedAt:id==='slow'?2:1,selection,draft:'',conversation:[{id:id+'-turn',prompt,cli:'Codex',model:'test-model',modelLabel:'Test Model',effort:'Low',status:'multiple',message:'',elapsedMs:1000,reply:'Done.',cards:[]}]});
  const chats={slow:chat('slow','Slow question'),other:chat('other','Other question')};
  const gates={};const release=id=>gates[id]?.();
  const held=id=>new Promise(resolve=>{gates[id]=resolve;});
  let waiting={};
  t.after(()=>Object.values(gates).forEach(open=>open()));
  const {page,settled}=await open(t,{url:'/',ready:'#pharos-ask .pharos-ai:not([hidden])',api:{
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
  // Leaving full screen for the dialog while another chat loads keeps that chat.
  await activateHeaderTool(page,'settingsToggle');await page.waitForURL(url=>url.pathname==='/settings');
  await activateHeaderTool(page,'askPharosToggle');
  await page.getByRole('button',{name:'Full screen',exact:true}).click();
  await page.waitForURL(url=>url.pathname==='/ask');
  await page.locator('.pharos-ai').getByRole('button',{name:'Clear',exact:true}).click();
  await page.getByRole('button',{name:'History',exact:true}).click();
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
  await modal.getByRole('button',{name:'Remove page context',exact:true}).click();
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
  assert.equal(state.sent.currentURI,'','the request respects the removed page context');
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
  const { page, shot } = await open(t, { url: '/settings/layout', ready: '#quickStatsExpand' });
  const disclosure = page.locator('#quickStatsExpand');
  await press(disclosure);
  await page.locator('#quickStatsOptions').waitFor();
  assert.ok(await disclosure.evaluate(element => document.activeElement === element));
  const handle = page.getByRole('button', { name: 'Arrange Tokens', exact: true });
  const focused = async locator => page.waitForFunction(element => document.activeElement === element, await locator.elementHandle());
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

for (const width of [1440,320]) test(`Ask Pharos work previews fit beside their links @ ${width}`, {skip}, async t => {
  const selection={provider:'Codex',model:'test-model',effort:'Low'};
  const cards=[{id:'work',title:'Long work',description:'The whole workspace.',href:'/work/'+workID},{id:'conversation',title:'Root conversation',description:'One conversation in it.',href:'/work/'+workID+'?conversation=conv-root'}];
  let session;
  const {page,shot}=await open(t,{url:'/library',ready:'.conversation-result-card',viewport:{width,height:900},api:{
    'GET /api/ask/status':{settings:{enabled:true,showInvitation:true,selection},providers:[{id:'Codex'}]},
    'GET /api/ask/models':{id:'Codex',models:[{id:'test-model',label:'Test Model',efforts:['Default','Low'],default:true}]},
    'GET /api/ask/sessions':()=>({sessions:session?[session]:[]}),
    'GET /api/ask/session':()=>session,
    'POST /api/ask/session':({body})=>(session={...session,...body}),
    'POST /api/ask/send':({body})=>(session={id:body.id,createdAt:Date.now(),updatedAt:Date.now(),selection,draft:'',conversation:[{id:body.turnID,prompt:body.prompt,cli:'Codex',model:'test-model',effort:'Low',status:'multiple',message:'',elapsedMs:1000,reply:'Your work.',cards}]}),
  }});
  await page.waitForFunction(()=>document.querySelector('.pharos-current-settings')?.textContent.includes('Test Model'));
  await activateHeaderTool(page,'askPharosToggle');
  const modal=page.getByRole('dialog',{name:'Ask Pharos',exact:true});
  const question=page.getByRole('textbox',{name:'Ask a question',exact:true});
  await question.fill('What did I do in the long work?');
  const before=page.url();
  await question.press('Enter');
  for (const card of cards) {
    const figure=page.getByRole('figure',{name:'Work preview: '+card.title,exact:true});
    await figure.waitFor();await figure.scrollIntoViewIfNeeded();
    await page.waitForFunction(title=>document.querySelector(`figure[aria-label="Work preview: ${title}"]`)?.getAttribute('aria-busy')==='false',card.title);
    assert.equal(await page.getByRole('link',{name:new RegExp(card.title)}).getAttribute('href'),card.href);
  }
  for (const card of cards) assert.equal(await page.getByRole('figure',{name:'Work preview: '+card.title,exact:true}).locator('p').count(),2,'where/when and who/how much render');
  assert.equal(page.url(),before,'previews leave the underlying page URL alone');
  assert.ok(await modal.evaluate(element=>element.contains(document.activeElement)),'loading previews must not move focus out of Ask');
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
  const link=modal.getByRole('link',{name:new RegExp(cards[0].title)});
  await link.click();
  await page.waitForURL(url=>url.pathname==='/analyze'&&url.searchParams.get('analyze_topic')==='tokens');
  await modal.waitFor({state:'hidden'});
});
