// A managed web view is temporary, but its working state must survive closing
// the app. Keep only small session values in the library's preferences, scoped
// to this Mac and application. Covers and other caches stay in the web view.
function serializePageHistory(entries, {limit = 1000, maxBytes = 512 << 10} = {}) {
  // preferences.go limits the JSON-encoded string, including Go's HTML and
  // line-separator escaping. A count alone cannot bound full view-state URLs.
  const encoder = new TextEncoder(), parts = [];
  let bytes = 4; // The outer JSON quotes and the history array's brackets.
  for (const entry of entries.slice(0, limit)) {
    const part = JSON.stringify(entry);
    const encoded = JSON.stringify(part).replace(/[<>&\u2028\u2029]/g,
      character => '\\u' + character.charCodeAt(0).toString(16).padStart(4, '0'));
    const size = encoder.encode(encoded).length - 2;
    if (size + 4 > maxBytes) continue; // Never truncate a saved URL.
    const next = bytes + size + (parts.length ? 1 : 0);
    if (next > maxBytes) break;
    parts.push(part);
    bytes = next;
  }
  return '[' + parts.join(',') + ']';
}

(() => {
  'use strict';
  const scope = window.pharosManagedSession;
  if (!scope) return;
  const stored = window.pharosPreferenceValues;
  if (!stored) throw new Error('Could not load this library’s saved view settings. Reconnect before editing.');
  const raw = window.localStorage;
  const names = new Set(['pharos-ui-feedback-v1', 'pharos-ui-feedback-draft-v1',
    'pharos-ui-feedback-layout-v1', 'pharos-feedback-enabled', 'pharos-history-v1', 'pharos-nav-open']);
  const durable = key => names.has(key) || /^(query-table:last:|pharos-query-rejected:)[A-Za-z0-9:_.-]+$/.test(key);
  // Navigation and derived view state can be reconstructed. Annotations and
  // feedback drafts are user-authored and still require a durable save.
  const transient = key => key !== 'pharos-ui-feedback-v1' && key !== 'pharos-ui-feedback-draft-v1';
  const values = new Map(Object.entries(stored).flatMap(([key, value]) =>
    key.startsWith(scope.preferences) && durable(key.slice(scope.preferences.length)) && typeof value === 'string'
      ? [[key.slice(scope.preferences.length), value]] : []));
  const critical = {pending: new Map(), saving: null, timer: null, delay: 150};
  const background = {pending: new Map(), saving: null, timer: null, delay: 150};
  let preservation = Promise.resolve(), preserving = 0;
  const warn = error => {
    console.warn('Could not save this library’s feedback:', error);
    window.dispatchEvent(new CustomEvent('pharos:session-save-error', {detail: String(error)}));
  };
  function schedule(lane, delay) {
    clearTimeout(lane.timer);
    lane.timer = setTimeout(() => {
      void flushLane(lane).catch(error => {
        if (lane === critical) warn(error);
        else { lane.delay = Math.min(lane.delay * 2, 30000); schedule(lane, lane.delay); }
      });
    }, delay);
  }
  async function flushLane(lane) {
    clearTimeout(lane.timer);
    if (lane.saving) { await lane.saving; return flushLane(lane); }
    if (!lane.pending.size) return;
    const batch = lane.pending;
    lane.pending = new Map();
    const set = {}, remove = [];
    for (const [key, value] of batch) {
      if (value === null) remove.push(scope.preferences + key);
      else set[scope.preferences + key] = value;
    }
    lane.saving = (async () => {
      const body = JSON.stringify({set, delete: remove});
      const response = await fetch(lane === background ? '/api/session-preferences' : '/api/preferences', {method: 'POST',
        headers: {'Content-Type': 'application/json'}, body, keepalive: body.length < 60000});
      if (!response.ok) {
        let reason = '';
        try { const body = await response.json(); if (typeof body.error === 'string') reason = ' ' + body.error; } catch {}
        throw new Error('Saving session state failed (HTTP ' + response.status + ').' + reason);
      }
      for (const key of batch.keys()) raw.removeItem(scope.legacy + key);
    })();
    try { await lane.saving; lane.delay = 150; }
    catch (error) {
      for (const [key, value] of batch) if (!lane.pending.has(key)) lane.pending.set(key, value);
      throw error;
    } finally { lane.saving = null; }
    if (lane.pending.size) return flushLane(lane);
  }
  function flushBackground() {
    void flushLane(background).catch(() => { background.delay = Math.min(background.delay * 2, 30000); schedule(background, background.delay); });
  }
  function flush() {
    flushBackground();
    return Promise.all([preservation, flushLane(critical)]).then(() => {});
  }
  function change(key, value) {
    if (value === null) values.delete(key); else values.set(key, value);
    const lane = transient(key) ? background : critical;
    lane.pending.set(key, value);
    schedule(lane, 150);
  }
  // Explicit history removal is durable, unlike automatic visits. Wait for
  // any older background request before saving it, then requeue newer visits.
  function preserve(key) {
    const value = values.get(key) ?? null;
    preserving++;
    const work = preservation.catch(() => {}).then(async () => {
      if (background.saving) { try { await background.saving; } catch {} }
      background.pending.delete(key);
      critical.pending.set(key, value);
      try { await flushLane(critical); }
      catch (error) {
        if (critical.pending.get(key) === value) critical.pending.delete(key);
        change(key, values.get(key) ?? null);
        throw error;
      }
      if ((values.get(key) ?? null) !== value) change(key, values.get(key) ?? null);
    });
    const result = work.finally(() => { preserving--; });
    preservation = result.catch(() => {});
    return result;
  }
  // A tab from the first managed build may still contain ID-scoped values.
  // Import only this library's known session keys, never another library's.
  for (let index = 0; index < raw.length; index++) {
    const name = raw.key(index);
    if (name?.startsWith(scope.legacy)) {
      const key = name.slice(scope.legacy.length);
      if (durable(key) && !values.has(key)) change(key, raw.getItem(name));
    }
  }
  function keys() {
    const found = new Set(values.keys());
    for (let index = 0; index < raw.length; index++) {
      const key = raw.key(index);
      if (key?.startsWith(scope.storage)) found.add(key.slice(scope.storage.length));
    }
    return [...found];
  }
  const storage = {
    getItem(key) { key = String(key); return durable(key) ? values.get(key) ?? null : raw.getItem(scope.storage + key); },
    setItem(key, value) { key = String(key); value = String(value); if (durable(key)) change(key, value); else raw.setItem(scope.storage + key, value); },
    removeItem(key) { key = String(key); if (durable(key)) change(key, null); else raw.removeItem(scope.storage + key); },
    clear() { for (const key of keys()) storage.removeItem(key); },
    key(index) { return keys()[index] ?? null; },
    get length() { return keys().length; },
  };
  Object.defineProperty(window, 'localStorage', {value: storage});
  window.pharosSession = {flush, preserve};
  addEventListener('pagehide', () => { void flush().catch(warn); });
  addEventListener('click', event => {
    const link = event.target?.closest?.('a[href]');
    if (!link || event.button || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || link.target) return;
    const url = new URL(link.href, location.href);
    if (url.origin !== location.origin || url.pathname !== '/app' || (!critical.pending.size && !critical.saving && !preserving)) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    void flush().then(() => location.assign(url.href)).catch(warn);
  }, true);
})();
