import assert from 'node:assert/strict';
import {test} from 'node:test';
import {readFileSync} from 'node:fs';
import {join} from 'node:path';
import vm from 'node:vm';
import {transformSync} from 'esbuild';
import {assets, loadClassicScript, plain} from '../helpers/classic-script.mjs';

const load = globals => loadClassicScript('host-services.js', globals).window;
const rejected = (code, message) => error => {
  assert.equal(error.code, code);
  if (message !== undefined) assert.equal(error.message, message);
  return true;
};

test('host adapter preserves legacy payloads, reply objects and one-way dispatch semantics', async () => {
  const sent = [];
  const update = {version: '1.2.3', phase: 'available', progress: 25};
  const native = (name, reply) => ({postMessage(payload) {sent.push([name, plain(payload)]); return reply;}});
  const window = load({webkit: {messageHandlers: {
    pharosClipboard: native('clipboard', Promise.resolve(true)),
    pharosLibrary: {postMessage(payload) {sent.push(['library', plain(payload)]); return Promise.resolve(payload.action === 'eject' ? {released: true} : '/Volumes/Archive');}},
    pharosUpdates: native('updates', Promise.resolve(update)),
    pharosMenuBar: native('menu', undefined),
  }}});
  const host = window.pharosHost;
  assert.equal(host.protocolVersion, 1);
  assert.equal(host.kind, 'legacy-webkit');
  for (const value of Object.values(host.capabilities)) assert.deepEqual(plain(value), {supported: true, available: true});
  assert.equal(await host.clipboard.writeText('hello'), undefined);
  assert.equal(await host.folders.chooseLibraryFolder(), '/Volumes/Archive');
  assert.equal(await host.updates.status(), update);
  assert.equal(await host.updates.install(), update);
  assert.deepEqual(plain(await host.lifecycle.requestEject()), {released: true});
  assert.deepEqual(plain(host.menu.applyPreferences({enabled: true, order: ['open', 'quit']})), {dispatched: true});
  assert.deepEqual(plain(host.menu.refreshUsage()), {dispatched: true});
  assert.deepEqual(sent, [
    ['clipboard', 'hello'], ['library', {action: 'chooseFolder'}],
    ['updates', {action: 'status'}], ['updates', {action: 'install'}],
    ['library', {action: 'eject'}], ['menu', {enabled: true, order: ['open', 'quit']}], ['menu', {refreshUsage: true}],
  ]);
});

test('host adapter resolves capabilities and missing handlers at call time without using the user agent', async () => {
  const window = load({navigator: {userAgent: 'Macintosh AppleWebKit'}}), host = window.pharosHost;
  assert.equal(host.kind, 'browser');
  for (const name of ['folders', 'updates', 'menu', 'eject']) {
    assert.equal(host.capabilities[name].supported, false);
    assert.equal(host.capabilities[name].available, false);
    assert.ok(host.capabilities[name].reason);
  }
  const requests = [() => host.folders.chooseLibraryFolder(), () => host.updates.status(), () => host.updates.install(), () => host.lifecycle.requestEject()];
  for (const request of requests) await assert.rejects(request(), rejected('unsupported'));
  assert.throws(() => host.menu.refreshUsage(), rejected('unsupported'));
  window.webkit = {messageHandlers: {pharosLibrary: {postMessage: async () => null}}};
  assert.equal(host.kind, 'legacy-webkit');
  assert.equal(host.capabilities.folders.available, true);
  assert.equal(await host.folders.chooseLibraryFolder(), null);
  assert.equal(host.capabilities.updates.supported, true);
  assert.equal(host.capabilities.updates.available, false);
  await assert.rejects(host.clipboard.writeText('missing native clipboard'), rejected('unsupported'));
  delete window.webkit.messageHandlers.pharosLibrary;
  await assert.rejects(host.folders.chooseLibraryFolder(), rejected('unsupported'));
  window.webkit.messageHandlers.pharosLibrary = {postMessage: 'not callable'};
  assert.equal(host.capabilities.folders.available, false);
});

test('host failures preserve native messages without text-derived error codes or retries', async () => {
  for (const nativeError of [new Error('Pharos is closing.'), 'This update is no longer ready.', {message: 'Library actions must come from the Pharos window.'}]) {
    for (const synchronous of [false, true]) {
      let calls = 0;
      const handler = {postMessage() {calls++; if (synchronous) throw nativeError; return Promise.reject(nativeError);}};
      const host = load({webkit: {messageHandlers: {pharosClipboard: handler, pharosLibrary: handler, pharosUpdates: handler, pharosMenuBar: handler}}}).pharosHost;
      for (const request of [() => host.clipboard.writeText('text'), () => host.folders.chooseLibraryFolder(), () => host.updates.status(), () => host.updates.install(), () => host.lifecycle.requestEject()]) {
        await assert.rejects(request(), rejected('native-failure', nativeError.message ?? nativeError));
      }
      assert.equal(calls, 5);
      if (synchronous) assert.throws(() => host.menu.refreshUsage(), rejected('native-failure', nativeError.message ?? nativeError));
    }
  }
});

test('host adapter validates requests and native acknowledgements', async () => {
  let calls = 0;
  const handler = {postMessage() {calls++; return Promise.resolve(undefined);}};
  const host = load({webkit: {messageHandlers: {pharosClipboard: handler, pharosLibrary: handler, pharosUpdates: handler}}}).pharosHost;
  for (const text of [null, 3, {}, undefined]) await assert.rejects(host.clipboard.writeText(text), rejected('invalid-request'));
  for (const settings of [null, [], 'enabled']) assert.throws(() => host.menu.applyPreferences(settings), rejected('invalid-request'));
  assert.equal(calls, 0);
  for (const request of [() => host.clipboard.writeText('text'), () => host.folders.chooseLibraryFolder(), () => host.updates.status(), () => host.updates.install(), () => host.lifecycle.requestEject()]) {
    await assert.rejects(request(), rejected('native-failure'));
  }
});

test('browser clipboard succeeds directly, and rejection remains a visible failure', async () => {
  const texts = [];
  const host = load({navigator: {clipboard: {writeText: async text => {texts.push(text);}}}}).pharosHost;
  assert.equal(host.capabilities.clipboard.available, true);
  await host.clipboard.writeText('browser text');
  assert.deepEqual(texts, ['browser text']);
  const denied = load({navigator: {clipboard: {writeText: async () => {throw new Error('Clipboard permission denied');}}}}).pharosHost;
  await assert.rejects(denied.clipboard.writeText('text'), rejected('native-failure', 'Clipboard permission denied'));
  const missing = load({navigator: {}}).pharosHost;
  assert.equal(missing.capabilities.clipboard.available, false);
  await assert.rejects(missing.clipboard.writeText('text'), rejected('unsupported'));
});

test('textarea copying restores focus and both kinds of selection on success and failure', async () => {
  for (const outcome of [true, false, new Error('Copy command failed')]) {
    const events = [], range = {cloneRange: () => range};
    const active = {selectionStart: 2, selectionEnd: 5, selectionDirection: 'backward', focus: () => events.push('restore-focus'), setSelectionRange: (...args) => events.push(args)};
    const selection = {rangeCount: 1, getRangeAt: () => range, removeAllRanges: () => events.push('clear-ranges'), addRange: value => {assert.equal(value, range); events.push('restore-range');}};
    const field = {style: {}, setAttribute() {}, focus() {}, select() {}, setSelectionRange() {}, remove: () => events.push('remove')};
    const host = load({navigator: {}, getSelection: () => selection, document: {
      activeElement: active, body: {append() {}}, createElement: () => field,
      execCommand: command => {assert.equal(command, 'copy'); if (outcome instanceof Error) throw outcome; return outcome;},
    }}).pharosHost;
    if (outcome === true) await host.clipboard.writeText('some text');
    else await assert.rejects(host.clipboard.writeText('some text'), rejected('native-failure', outcome.message ?? 'Could not copy text.'));
    assert.equal(field.value, 'some text');
    assert.deepEqual(events, ['remove', 'restore-focus', 'clear-ranges', 'restore-range', [2, 5, 'backward']]);
  }
});

test('public copy compatibility wrappers delegate once without recursion', async () => {
  const window = load();
  const page = readFileSync(join(assets, 'ui.py'), 'utf8');
  const wrapper = page.slice(page.indexOf('async function copyFeedbackText(text)'), page.indexOf('\nfunction clearFeedback'));
  vm.runInNewContext(wrapper + ';window.pharosCopyText=copyFeedbackText;', {window});
  await window.pharosCopyText('public wrapper');
  window.navigator.clipboard.writeText = async () => {throw new Error('Copy failed');};
  await assert.rejects(window.pharosCopyText('failure'), rejected('native-failure', 'Copy failed'));
});

test('query table copies use their shell owner and propagate its failure', async () => {
  const source=readFileSync(join(assets,'../../../web/src/index.tsx'),'utf8');
  const helper=source.slice(source.indexOf('async function copyText('),source.indexOf('\nfunction compactNumber('));
  const code=transformSync(helper+';copyText;', {loader:'ts'}).code;
  for(const fail of [false,true]){
    const transfer=Promise.withResolvers(),writes=[];
    const copy=vm.runInNewContext(code,{window:{pharosCopyText:text=>{writes.push(text);return transfer.promise;}},
      navigator:{clipboard:{writeText:()=>assert.fail('the bundle must use its shell clipboard owner')}}});
    const operation=copy('reference');
    if(fail)transfer.reject(Error('Copy failed'));else transfer.resolve();
    if(fail)await assert.rejects(operation,/Copy failed/);else await operation;
    assert.deepEqual(writes,['reference']);
  }
});

test('textarea fallback copies inside an active modal instead of its inert background', async () => {
  let target;
  const modal = {append: field => {target = field;}};
  const field = {style: {}, setAttribute() {}, focus() {}, select() {}, setSelectionRange() {}, remove() {}};
  const host = load({navigator: {}, document: {
    activeElement: {closest: selector => {assert.equal(selector, 'dialog[open]'); return modal;}, focus() {}},
    body: {append() {assert.fail('the modal makes body inert');}}, createElement: () => field, execCommand: () => true,
  }}).pharosHost;
  await host.clipboard.writeText('modal copy');
  assert.equal(target, field);
  assert.equal(target.value, 'modal copy');
});

test('feature scripts keep host transport access inside the adapter', () => {
  for (const name of ['library.js', 'status-panel.js', 'menu-bar.js', 'onboarding.js', 'application.html', 'ui.py']) {
    assert.doesNotMatch(readFileSync(join(assets, name), 'utf8'), /\bmessageHandlers\b|\bpostMessage\s*\(/, name);
  }
});

test('onboarding picker purposes preserve legacy source and export panel modes', async () => {
  const sent=[];
  const host=load({webkit:{messageHandlers:{pharosLibrary:{postMessage:async payload=>{sent.push(plain(payload));return null;}}}}}).pharosHost;
  for(const purpose of ['source','export']) assert.equal(await host.folders.chooseLibraryFolder(purpose),null);
  assert.deepEqual(sent,[{action:'chooseFolder',purpose:'source'},{action:'chooseFolder',purpose:'export'}]);
  await assert.rejects(host.folders.chooseLibraryFolder('arbitrary'),rejected('invalid-request'));
  assert.equal(sent.length,2);
  const missing=load({webkit:{messageHandlers:{}},navigator:{clipboard:{writeText:async()=>{throw new Error('Must not use browser clipboard in native host');}}}}).pharosHost;
  assert.equal(missing.kind,'legacy-webkit');
  await assert.rejects(missing.clipboard.writeText('text'),rejected('unsupported'));
});
