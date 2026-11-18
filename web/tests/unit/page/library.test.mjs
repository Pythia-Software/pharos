// library.js: the header's app update button and the Settings copy action
// (window.pharosLibrary).
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import vm from 'node:vm';
import { assets, loadClassicScript, plain } from "../helpers/classic-script.mjs";

const script = loadClassicScript("library.js");
const library = script.window.pharosLibrary;

test('each backup location keeps its partial outcome after other backups and restarts', () => {
  const missing = '/Volumes/Archive/preserved';
  const partial = {path: '/Volumes/First/Backup', last_backup: {partial: true, missing: [missing]}, last_run: {state: 'partial', missing: [missing]}};
  assert.deepEqual(plain(library.backupLocationOutcome(partial)), {partial: true, missing: [missing]});
  assert.deepEqual(plain(library.backupLocationOutcome({...partial, last_run: null})), {partial: true, missing: [missing]});
  assert.deepEqual(plain(library.backupLocationOutcome({last_backup: {}, last_run: {state: 'complete'}})), {partial: false, missing: []});
  assert.deepEqual(plain(library.backupLocationOutcome({last_run: {state: 'partial'}})), {partial: true, missing: []});
});

test('app updates distinguish available releases from download and install progress', () => {
  const present = state => plain(library.appUpdatePresentation(state));
  for (const state of [null, {}, {version: '0.8.0'}, {phase: 'available'}, {version: '0.8.0', phase: 'failed'}]) {
    assert.equal(present(state), null);
  }
  const available = present({version: '12.123.456-beta.123', phase: 'available'});
  assert.equal(available.busy, false);
  assert.equal(available.icon, 'download');
  assert.equal(available.progress, null);
  assert.ok(available.title.includes('12.123.456-beta.123'));
  assert.match(available.title, /restart/);
  for (const phase of ['downloading', 'preparing', 'installing']) {
    assert.equal(present({version: '0.8.0', phase}).busy, true);
  }
  const downloading = present({version: '0.8.0', phase: 'downloading', progress: 42.9});
  assert.equal(downloading.progress, 42);
  assert.match(downloading.title, /Downloading.*0\.8\.0.*42%/);
  for (const [progress, expected] of [[200, 100], [-10, 0], [0, 0], [NaN, null], [undefined, null]]) {
    assert.equal(present({version: '0.8.0', phase: 'downloading', progress}).progress, expected);
  }
  const preparing = present({version: '0.8.0', phase: 'preparing', progress: 42});
  assert.equal(preparing.icon, 'package');
  assert.equal(preparing.progress, null, 'the download percentage does not carry into preparation');
  assert.match(preparing.title, /Verifying and preparing.*0\.8\.0/);
  assert.equal(present({version: '0.8.0', phase: 'installing'}).icon, 'restart');
  assert.match(present({version: '0.8.0', phase: 'installing'}).title, /Installing.*0\.8\.0.*restarting/);
});

test('library copy actions use host services and keep copy failures visible', async () => {
  const source=readFileSync(join(assets,'library.js'),'utf8');
  const copy=source.slice(source.indexOf('  const copy = async text => {'),source.indexOf('\n  const codeLine ='));
  for(const fail of [false,true]) {
    const writes=[],toasts=[];
    const window={pharosHost:{clipboard:{writeText:async text=>{writes.push(text);if(fail)throw new Error('Copy denied');}}}};
    const run=vm.runInNewContext(copy+';copy',{window,toast:message=>toasts.push(message)});
    await run('folder path');
    assert.deepEqual(writes,['folder path']);
    assert.deepEqual(toasts,[fail?'Could not copy':'Copied']);
  }
});
