// The status store (docs/background-work.md §7): one client copy of the
// status snapshot, for the unified activity panel, the header chip and the
// header's Index changes button. It runs only while the
// pharos-unified-activity-v1 preference is on; off, it never connects and
// the page behaves exactly as before.
//
// It reads /api/status/stream (in the app, the broker's /api/app/status/stream,
// which adds every other library and the app's own operations) and falls back
// to polling the snapshot every 5 s when the stream fails, trying the stream
// again now and then. A hidden page holds no stream and polls nothing; it
// reconnects when shown again.
//
// The logic is in createStore and normalize, which take their browser APIs as
// arguments, so web/tests/unit/page/status-store.test.mjs can drive them.
(() => {
  'use strict';
  const FLAG = 'pharos-unified-activity-v1';
  const POLL_MS = 5000;
  // After this long on polling, the store tries the stream again.
  const RETRY_STREAM_MS = 30000;

  const enabled = () => window.pharosPrefs?.get(FLAG, false) === true;

  // The endpoints: the broker's in the app, the service's otherwise.
  const endpoints = managed => managed
    ? {stream: '/api/app/status/stream', poll: '/api/app/status'}
    : {stream: '/api/status/stream', poll: '/api/status'};

  const TERMINAL = new Set(['succeeded', 'failed', 'cancelled', 'interrupted', 'skipped']);
  const touches = (operation, id) => Array.isArray(operation.libraries) && operation.libraries.includes(id);

  // One shape for both documents. `library` is the selected library's
  // service snapshot, with its eject verdict replaced by the broker's when
  // the broker recomputed it with its own operations (§4.4); `appOperations`
  // are the broker's running operations that touch the selected library
  // (shown with its own); `others` are the other registered libraries, each
  // with the broker's operations that touch it, plus app work that touches
  // no library; `attention` is the whole list.
  function normalize(document, {managed = false} = {}) {
    if (!document || typeof document !== 'object') return null;
    if (!managed || !Array.isArray(document.libraries)) {
      return {library: document, others: [], app: null, appOperations: [], attention: Array.isArray(document.attention) ? document.attention : [], service: null, resources: null};
    }
    const selected = document.libraries.find(entry => entry.selected) || null;
    const library = selected?.snapshot ? {...selected.snapshot, eject: selected.eject || selected.snapshot.eject} : null;
    const appRunning = (document.app?.operations || []).filter(operation => !TERMINAL.has(operation.state));
    const others = document.libraries.filter(entry => entry !== selected).map(entry => ({
      id: entry.id, name: entry.name, connected: Boolean(entry.connected), eject: entry.eject || null, stateLabel: entry.state_label || null,
      operations: [...appRunning.filter(operation => touches(operation, entry.id)), ...(entry.snapshot?.operations || [])], service: entry.service || null,
    }));
    for (const operation of appRunning.filter(operation => !document.libraries.some(entry => touches(operation, entry.id)))) {
      others.push({id: `app:${operation.id}`, name: operation.label, connected: true, eject: null, stateLabel: null, operations: [operation], service: null});
    }
    return {library, others, app: document.app || null, appOperations: selected ? appRunning.filter(operation => touches(operation, selected.id)) : [],
      attention: Array.isArray(document.attention) ? document.attention : [],
      // Every Pharos process on this Mac, for the Load row.
      resources: document.resources || null,
      service: selected ? {id: selected.id, name: selected.name, state: selected.service?.state || null, stateLabel: selected.state_label || null, connected: Boolean(selected.connected), error: selected.error || null, eject: selected.eject || null} : null};
  }

  // The fresh verdict in the app: the broker's, for the selected library,
  // checked at this moment. Anything else refuses: the panel never ejects on
  // the library's own verdict alone, which cannot see the app's operations.
  function brokerEjectVerdict(document, selectedId) {
    const refuse = summary => ({verdict: 'unsafe', headline: summary, summary, blockers: [], checked: 'fresh'});
    if (!document || !Array.isArray(document.libraries)) return refuse('Pharos could not check with the app whether ejecting is safe, so it will not eject. Try again.');
    const entry = document.libraries.find(item => item.id === selectedId && item.selected);
    if (!entry?.eject?.verdict) return refuse('The app did not report on this library, so Pharos will not eject. Try again.');
    if (entry.eject.checked !== 'fresh') return refuse('The app could not run a full check of this library, so Pharos will not eject. Try again.');
    return entry.eject;
  }

  // createStore owns one connection. Options are the browser's APIs, so a
  // test can pass fakes: EventSource, fetch, setTimeout, clearTimeout, and
  // hidden() (document.hidden).
  function createStore({managed = false, selectedId = null, EventSource: Source = globalThis.EventSource, fetch: get = (...args) => globalThis.fetch(...args),
    setTimeout: later = (...args) => globalThis.setTimeout(...args), clearTimeout: cancel = id => globalThis.clearTimeout(id),
    hidden = () => Boolean(globalThis.document?.hidden), now = () => Date.now()} = {}) {
    const paths = endpoints(managed);
    const listeners = new Set();
    let state = {document: null, view: null, error: null, mode: 'stopped', receivedAt: 0};
    // generation changes whenever the feed is opened, closed or paused, so a
    // poll that was in flight then is dropped when it returns.
    let source = null, timer = null, running = false, tag = null, pollingSince = 0, generation = 0;
    // streamed counts the stream's accepted events, so a refresh that was in
    // flight when a newer event arrived does not overwrite it.
    let streamed = 0;

    const notify = () => { for (const listener of [...listeners]) { try { listener(state); } catch (error) { console.error(error); } } };
    const set = changes => { state = {...state, ...changes}; notify(); };
    const accept = document => {
      const view = normalize(document, {managed});
      if (!view) return;
      set({document, view, error: null, receivedAt: now()});
    };
    const clearTimer = () => { if (timer !== null) cancel(timer); timer = null; };
    const closeStream = () => { if (source) { source.onerror = null; source.close(); } source = null; };

    function openStream() {
      generation++;
      clearTimer();
      closeStream();
      if (typeof Source !== 'function') return poll();
      let stream;
      try { stream = new Source(paths.stream); } catch { return poll(); }
      source = stream;
      set({mode: 'stream'});
      stream.addEventListener('snapshot', event => {
        if (source !== stream) return;
        // The event's id is the document's ETag: a later poll must compare
        // with what the stream last showed, not what an earlier poll did.
        tag = event.lastEventId || null;
        streamed++;
        try { accept(JSON.parse(event.data)); } catch (error) { set({error}); }
      });
      // A stream that fails, whatever the reason, becomes polling: the
      // browser's own reconnects would hide that the data is not live.
      stream.onerror = () => {
        if (source !== stream) return;
        closeStream();
        pollingSince = now();
        set({mode: 'polling'});
        poll();
      };
    }

    async function poll() {
      clearTimer();
      if (!running) return;
      const mine = ++generation;
      if (state.mode !== 'polling') { pollingSince = now(); set({mode: 'polling'}); }
      try {
        const response = await get(paths.poll, {headers: tag ? {'If-None-Match': tag} : {}});
        if (!running || mine !== generation) return;
        if (response.status === 304) set({error: null});
        else if (!response.ok) throw Object.assign(new Error(`HTTP ${response.status}`), {status: response.status});
        else {
          const document = await response.json();
          if (!running || mine !== generation) return;
          tag = response.headers?.get?.('ETag') || null;
          accept(document);
        }
      } catch (error) {
        if (running && mine === generation) set({error});
      }
      if (!running || mine !== generation) return;
      if (hidden()) { set({mode: 'paused'}); return; }
      if (now() - pollingSince >= RETRY_STREAM_MS && typeof Source === 'function') { timer = later(openStream, 0); return; }
      timer = later(poll, POLL_MS);
    }

    function start() {
      if (running) return;
      running = true;
      if (hidden()) { set({mode: 'paused'}); return; }
      openStream();
    }
    function stop() {
      running = false;
      generation++;
      clearTimer();
      closeStream();
      set({mode: 'stopped'});
    }
    // A hidden page keeps nothing busy; showing it reconnects.
    function visibility() {
      if (!running) return;
      if (hidden()) { generation++; clearTimer(); closeStream(); set({mode: 'paused'}); }
      else if (!source && timer === null) openStream();
    }
    // The eject check (§4.2): a fresh verdict, never the polled one. In the
    // app it is the broker's fresh verdict for the selected library, which
    // adds the app's own operations; without one, eject is refused.
    async function fresh() {
      if (managed) {
        let document = null;
        try {
          const response = await get(`${paths.poll}?fresh=1`, {});
          document = response.ok ? await response.json() : null;
        } catch { document = null; }
        return {eject: brokerEjectVerdict(document, selectedId)};
      }
      const response = await get('/api/status?fresh=1', {});
      if (!response.ok) throw Object.assign(new Error(`HTTP ${response.status}`), {status: response.status});
      return await response.json();
    }
    // refresh after an action (Stop): with the stream open, one fetch that
    // changes no mode and schedules nothing; otherwise the next poll now.
    async function refresh() {
      if (!running) return;
      if (!source) return poll();
      const mine = generation, seen = streamed;
      const current = () => running && mine === generation && source && seen === streamed;
      try {
        const response = await get(paths.poll, {});
        if (!current() || !response.ok) return;
        const document = await response.json();
        if (current()) accept(document);
      } catch { /* the stream carries on */ }
    }
    return {
      start, stop, visibility, fresh, refresh,
      current: () => state,
      subscribe(listener) { listeners.add(listener); return () => listeners.delete(listener); },
    };
  }

  // The page's one store, created when the flag is first on.
  let store = null;
  const instance = () => store ??= createStore({managed: Boolean(window.pharosManagedLibrary), selectedId: window.pharosManagedLibrary?.id || null});
  function startIfEnabled() {
    if (enabled()) instance().start();
    else store?.stop();
  }
  document.addEventListener('visibilitychange', () => store?.visibility());
  window.addEventListener('pharos:unified-activity', startIfEnabled);

  window.pharosStatusStore = {
    enabled, start: startIfEnabled,
    current: () => store?.current() || null,
    subscribe: listener => instance().subscribe(listener),
    fresh: () => instance().fresh(),
    refresh: () => store?.refresh(),
    // Pure helpers, for web/tests/unit/page/status-store.test.mjs.
    createStore, normalize, endpoints, brokerEjectVerdict, POLL_MS,
  };
  startIfEnabled();
})();
