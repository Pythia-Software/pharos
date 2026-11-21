// The legacy WKWebView transport has no version handshake. Capabilities are
// presentation hints only; every action resolves its handler again at call time.
// protocolVersion describes this JavaScript API, not a verified native version.
(() => {
  'use strict';
  const names = {clipboard: 'pharosClipboard', folders: 'pharosLibrary', updates: 'pharosUpdates', menu: 'pharosMenuBar', eject: 'pharosLibrary', dictation: 'pharosDictation'};
  const handler = name => {
    const value = window.webkit?.messageHandlers?.[names[name]];
    return typeof value?.postMessage === 'function' ? value : null;
  };
  const kind = () => window.webkit?.messageHandlers ? 'legacy-webkit' : 'browser';
  const failure = (code, message) => Object.assign(new Error(message), {code});
  const nativeFailure = error => failure('native-failure', error?.message ?? String(error));
  // What each host action does, for the messages people may see.
  const actions = {clipboard: 'copy', folders: 'choose a folder', updates: 'check for updates', menu: 'update the menu bar', eject: 'eject', dictation: 'start dictation'};
  const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
  function dispatch(name, payload) {
    const target = handler(name);
    if (!target) throw failure('unsupported', `Could not ${actions[name]}: this needs the Pharos Mac app.`);
    try { return target.postMessage(payload); }
    catch (error) { throw nativeFailure(error); }
  }
  async function request(name, payload, valid) {
    // Keep missing-handler errors separate from native rejections. Native error
    // messages (including strings) are preserved without interpreting their text.
    const reply = dispatch(name, payload);
    let value;
    try { value = await reply; } catch (error) { throw nativeFailure(error); }
    if (!valid(value)) throw failure('native-failure', `Could not ${actions[name]}: the Pharos Mac app sent an unexpected reply.`);
    return value;
  }
  function textareaCopy(text) {
    const active = document.activeElement, selection = window.getSelection?.();
    const ranges = selection ? Array.from({length: selection.rangeCount}, (_, i) => selection.getRangeAt(i).cloneRange()) : [];
    const inputSelection = active && typeof active.selectionStart === 'number'
      ? [active.selectionStart, active.selectionEnd, active.selectionDirection] : null;
    const field = document.createElement('textarea');
    field.value = text;
    field.setAttribute('readonly', '');
    field.style.position = 'fixed';
    field.style.opacity = '0';
    try {
      // A modal makes the rest of the document inert, including a textarea
      // appended to body. Keep the transfer field in the active dialog.
      (active?.closest?.('dialog[open]') || document.body).append(field);
      field.focus({preventScroll: true});
      field.select();
      field.setSelectionRange(0, text.length);
      if (!document.execCommand('copy')) throw new Error('Could not copy text.');
    } finally {
      field.remove();
      active?.focus({preventScroll: true});
      if (selection) { selection.removeAllRanges(); for (const range of ranges) selection.addRange(range); }
      // Restoring DOM ranges can reset a text input's selection in Chromium.
      if (inputSelection) active.setSelectionRange(...inputSelection);
    }
  }
  // Native dictation reports progress as window events. Each session's handlers
  // stay registered until it ends, and onEnd runs exactly once per session:
  // from the native end event or, if that never comes, the failed start.
  const dictationSessions = new Map();
  window.addEventListener('pharos:dictation', event => {
    const detail = event.detail, session = object(detail) ? dictationSessions.get(detail.session) : undefined;
    if (!session) return;
    if (detail.type === 'text' && typeof detail.text === 'string') session.onText?.(detail.text, detail.final === true);
    else if (detail.type === 'state' && typeof detail.state === 'string') session.onState?.(detail.state);
    else if (detail.type === 'end') endDictation(detail.session, typeof detail.error === 'string' ? detail.error : null);
  });
  function endDictation(id, error) {
    const session = dictationSessions.get(id);
    if (!session) return;
    dictationSessions.delete(id);
    session.onEnd?.(error);
  }
  const capabilities = {};
  for (const name of Object.keys(names)) Object.defineProperty(capabilities, name, {enumerable: true, get() {
    const supported = name === 'clipboard' || kind() === 'legacy-webkit';
    const available = Boolean(handler(name)) || name === 'clipboard' && kind() === 'browser'
      && (typeof navigator.clipboard?.writeText === 'function' || typeof document.execCommand === 'function');
    return {supported, available, ...(!available ? {reason: supported ? `Could not ${actions[name]}: this version of the Pharos app does not offer it.` : `Could not ${actions[name]} in a browser: this needs the Pharos Mac app.`} : {})};
  }});
  window.pharosHost = {
    protocolVersion: 1,
    get kind() { return kind(); },
    capabilities,
    clipboard: {async writeText(text) {
      if (typeof text !== 'string') throw failure('invalid-request', 'Clipboard content must be text.');
      if (kind() === 'legacy-webkit') {
        await request('clipboard', text, value => value === true);
        return;
      }
      const clipboard = navigator.clipboard;
      if (typeof clipboard?.writeText !== 'function' && typeof document.execCommand !== 'function') throw failure('unsupported', 'Clipboard access is unavailable.');
      try {
        if (typeof clipboard?.writeText === 'function') await clipboard.writeText(text);
        else textareaCopy(text);
      } catch (error) { throw nativeFailure(error); }
    }},
    folders: {async chooseLibraryFolder(purpose) {
      // Onboarding already uses source/export to select NSOpenPanel's mode.
      // The default library action retains its exact no-purpose payload.
      if (purpose !== undefined && !['library', 'source', 'export', 'backup', 'restore'].includes(purpose)) throw failure('invalid-request', 'Unknown folder picker purpose.');
      return request('folders', {action: 'chooseFolder', ...(purpose === undefined ? {} : {purpose})}, value => value === null || typeof value === 'string' && value.length > 0);
    }},
    updates: (() => {
      const state = action => () => request('updates', {action}, value => object(value) && typeof value.version === 'string' && typeof value.phase === 'string');
      // installWhenIdle downloads now and installs once this Mac is idle;
      // stopWaiting keeps the downloaded update offered without waiting.
      return {status: state('status'), install: state('install'), installWhenIdle: state('installWhenIdle'), stopWaiting: state('stopWaiting')};
    })(),
    menu: {
      applyPreferences(settings) {
        if (!object(settings)) throw failure('invalid-request', 'Menu preferences must be an object.');
        dispatch('menu', settings);
        return {dispatched: true};
      },
      refreshUsage() { dispatch('menu', {refreshUsage: true}); return {dispatched: true}; },
    },
    dictation: {
      status: () => request('dictation', {action: 'status'}, value => object(value) && typeof value.available === 'boolean'),
      // Returns at once so a caller can cancel while the speech model loads;
      // `started` settles when audio is flowing.
      start(handlers = {}) {
        if (!object(handlers)) throw failure('invalid-request', 'Dictation handlers must be an object.');
        const session = crypto.randomUUID();
        dictationSessions.set(session, handlers);
        const started = request('dictation', {action: 'start', session}, value => object(value) && value.started === true)
          .then(() => undefined, error => { endDictation(session, error?.message || String(error)); throw error; });
        const finish = action => request('dictation', {action, session}, value => value === true);
        return {started, stop: () => finish('stop'), cancel: () => finish('cancel')};
      },
    },
    // Released confirms native orchestration accepted the Go release, not that
    // the OS has physically ejected the drive. Never retry this action here.
    lifecycle: {requestEject: () => request('eject', {action: 'eject'}, value => object(value) && value.released === true)},
  };
})();
