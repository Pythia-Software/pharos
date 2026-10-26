// A managed web view is temporary, but its working state must survive closing
// the app. Keep only small session values in the library's preferences, scoped
// to this Mac and application. Covers and other caches stay in the web view.
(() => {
  'use strict';
  const scope = window.pharosManagedSession;
  if (!scope) return;
  const stored = window.pharosPreferenceValues;
  if (!stored) throw new Error('Could not load this library’s saved UI session. Reconnect before editing.');
  const raw = window.localStorage;
  const names = new Set(['pharos-ui-feedback-v1', 'pharos-ui-feedback-draft-v1',
    'pharos-ui-feedback-layout-v1', 'pharos-feedback-enabled', 'pharos-history-v1', 'pharos-nav-open']);
  const durable = key => names.has(key) || /^(query-table:last:|pharos-query-rejected:)[A-Za-z0-9:_.-]+$/.test(key);
  const values = new Map(Object.entries(stored).flatMap(([key, value]) =>
    key.startsWith(scope.preferences) && durable(key.slice(scope.preferences.length)) && typeof value === 'string'
      ? [[key.slice(scope.preferences.length), value]] : []));
  let pending = new Map(), saving = null, timer;
  const warn = error => {
    console.warn('Could not save this library’s UI session:', error);
    window.dispatchEvent(new CustomEvent('pharos:session-save-error', {detail: String(error)}));
  };
  async function flush() {
    clearTimeout(timer);
    if (saving) { await saving; return flush(); }
    if (!pending.size) return;
    const batch = pending;
    pending = new Map();
    const set = {}, remove = [];
    for (const [key, value] of batch) {
      if (value === null) remove.push(scope.preferences + key);
      else set[scope.preferences + key] = value;
    }
    saving = (async () => {
      const body = JSON.stringify({set, delete: remove});
      const response = await fetch('/api/preferences', {method: 'POST',
        headers: {'Content-Type': 'application/json'}, body, keepalive: body.length < 60000});
      if (!response.ok) throw new Error('Session save failed (HTTP ' + response.status + '). Keep Pharos open and retry.');
      for (const key of batch.keys()) raw.removeItem(scope.legacy + key);
    })();
    try { await saving; }
    catch (error) {
      for (const [key, value] of batch) if (!pending.has(key)) pending.set(key, value);
      throw error;
    } finally { saving = null; }
    if (pending.size) return flush();
  }
  function change(key, value) {
    if (value === null) values.delete(key); else values.set(key, value);
    pending.set(key, value);
    clearTimeout(timer);
    timer = setTimeout(() => { void flush().catch(warn); }, 150);
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
  window.pharosSession = {flush};
  addEventListener('pagehide', () => { void flush().catch(warn); });
  addEventListener('click', event => {
    const link = event.target?.closest?.('a[href]');
    if (!link || event.button || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || link.target) return;
    const url = new URL(link.href, location.href);
    if (url.origin !== location.origin || url.pathname !== '/app' || (!pending.size && !saving)) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    void flush().then(() => location.assign(url.href)).catch(warn);
  }, true);
})();
