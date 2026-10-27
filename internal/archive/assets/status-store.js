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

  // One shape for both documents. `library` is the selected library's
  // service snapshot, with its eject verdict replaced by the broker's when
  // the broker recomputed it with its own operations (§4.4); `others` are
  // the other registered libraries; `attention` is the whole list.
  function normalize(document, {managed = false} = {}) {
    if (!document || typeof document !== 'object') return null;
    if (!managed || !Array.isArray(document.libraries)) {
      return {library: document, others: [], app: null, attention: Array.isArray(document.attention) ? document.attention : [], service: null};
    }
    const selected = document.libraries.find(entry => entry.selected) || null;
    const library = selected?.snapshot ? {...selected.snapshot, eject: selected.eject || selected.snapshot.eject} : null;
    const others = document.libraries.filter(entry => entry !== selected).map(entry => ({
      id: entry.id, name: entry.name, connected: Boolean(entry.connected), eject: entry.eject || null,
      operations: entry.snapshot?.operations || [], service: entry.service || null,
    }));
    return {library, others, app: document.app || null, attention: Array.isArray(document.attention) ? document.attention : [],
      service: selected ? {name: selected.name, state: selected.service?.state || null, connected: Boolean(selected.connected), error: selected.error || null, eject: selected.eject || null} : null};
  }

  // createStore owns one connection. Options are the browser's APIs, so a
  // test can pass fakes: EventSource, fetch, setTimeout, clearTimeout, and
  // hidden() (document.hidden).
  function createStore({managed = false, EventSource: Source = globalThis.EventSource, fetch: get = (...args) => globalThis.fetch(...args),
    setTimeout: later = (...args) => globalThis.setTimeout(...args), clearTimeout: cancel = id => globalThis.clearTimeout(id),
    hidden = () => Boolean(globalThis.document?.hidden), now = () => Date.now()} = {}) {
    const paths = endpoints(managed);
    const listeners = new Set();
    let state = {document: null, view: null, error: null, mode: 'stopped', receivedAt: 0};
    let source = null, timer = null, running = false, tag = null, pollingSince = 0;

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
      closeStream();
      if (typeof Source !== 'function') return poll();
      let stream;
      try { stream = new Source(paths.stream); } catch { return poll(); }
      source = stream;
      set({mode: 'stream'});
      stream.addEventListener('snapshot', event => {
        if (source !== stream) return;
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
      if (state.mode !== 'polling') { pollingSince = now(); set({mode: 'polling'}); }
      try {
        const response = await get(paths.poll, {headers: tag ? {'If-None-Match': tag} : {}});
        if (!running) return;
        if (response.status === 304) set({error: null});
        else if (!response.ok) throw Object.assign(new Error(`HTTP ${response.status}`), {status: response.status});
        else {
          tag = response.headers?.get?.('ETag') || null;
          accept(await response.json());
        }
      } catch (error) {
        if (running) set({error});
      }
      if (!running) return;
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
      clearTimer();
      closeStream();
      set({mode: 'stopped'});
    }
    // A hidden page keeps nothing busy; showing it reconnects.
    function visibility() {
      if (!running) return;
      if (hidden()) { clearTimer(); closeStream(); set({mode: 'paused'}); }
      else if (!source && timer === null) openStream();
    }
    // The eject check (§4.2): a fresh verdict, never the polled one. In the
    // app the broker's polled verdict for the library adds its own
    // operations; the worse of the two wins.
    async function fresh() {
      const response = await get('/api/status?fresh=1', {});
      if (!response.ok) throw Object.assign(new Error(`HTTP ${response.status}`), {status: response.status});
      const document = await response.json();
      let eject = document.eject;
      if (managed) {
        try {
          const brokerResponse = await get(paths.poll, {});
          const broker = brokerResponse.ok ? normalize(await brokerResponse.json(), {managed}) : null;
          eject = worseVerdict(eject, broker?.service?.eject);
        } catch { eject = worseVerdict(eject, {verdict: 'unsafe', summary: 'Pharos could not ask the app whether another library operation is running.', blockers: []}); }
      }
      return {...document, eject};
    }
    return {
      start, stop, visibility, fresh, refresh: poll,
      current: () => state,
      subscribe(listener) { listeners.add(listener); return () => listeners.delete(listener); },
    };
  }

  const RANK = {safe: 0, 'safe-in': 1, busy: 2, unsafe: 3};
  // The worse of two verdicts; a missing one never improves the other.
  function worseVerdict(first, second) {
    if (!second?.verdict) return first;
    if (!first?.verdict) return second;
    return (RANK[second.verdict] ?? 3) > (RANK[first.verdict] ?? 3) ? second : first;
  }

  // The page's one store, created when the flag is first on.
  let store = null;
  const instance = () => store ??= createStore({managed: Boolean(window.pharosManagedLibrary)});
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
    createStore, normalize, endpoints, worseVerdict, POLL_MS,
  };
  startIfEnabled();
})();
