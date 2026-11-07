// The status store (docs/background-work.md §7): one client copy of the
// status snapshot, for the unified activity panel, the header chip and the
// header's Index changes button.
//
// It reads /api/status/stream (in the app, the broker's /api/app/status/stream,
// which adds every other library and the app's own operations) and falls back
// to polling the snapshot every 5 s when the stream fails, trying the stream
// again now and then. A hidden page holds no stream and polls nothing; it
// reconnects when shown again.
//
// While the panel is open the store also keeps the resource history
// (docs/work-resource-accounting.md): one fetch when it opens, then a
// since= fetch whenever a snapshot carries newer resources. Without a
// collector (classic execution) the document says it is unavailable and the
// panel draws no chart.
//
// The logic is in createStore, normalize and mergeHistory, which take their
// browser APIs as arguments, so web/tests/unit/page tests can drive them.
(() => {
  'use strict';
  const POLL_MS = 5000;
  // After this long on polling, the store tries the stream again.
  const RETRY_STREAM_MS = 30000;

  // The unified activity panel is the only one now. library.js still asks,
  // until its old drive panel is removed.
  const enabled = () => true;

  // The endpoints: the broker's in the app, the service's otherwise.
  const endpoints = managed => managed
    ? {stream: '/api/app/status/stream', poll: '/api/app/status'}
    : {stream: '/api/status/stream', poll: '/api/status'};
  // History follows the Load row: in the app the covered Pharos processes,
  // otherwise this library service.
  const historyEndpoint = managed => managed ? '/api/app/resources/history' : '/api/resources/history';
  // The least time between two history fetches while the panel is open.
  const HISTORY_MS = 2000;

  // mergeHistory appends a since= document to the history the store holds.
  // A full document (no since), an unavailable one, or one for another
  // scope replaces it. Samples are kept in time order without repeating a
  // time, and everything older than the window is dropped.
  function mergeHistory(previous, next) {
    if (!next || typeof next !== 'object') return previous || null;
    if (!next.available || !previous?.available || next.since == null || (previous.library || '') !== (next.library || '')) return next;
    const end = Number(next.now) || 0;
    const start = end - (Number(next.window_seconds) || 900) * 1000;
    const columns = (old, fresh, keys) => {
      const merged = Object.fromEntries(keys.map(key => [key, []]));
      for (const source of [old, fresh]) {
        const times = source?.t || [];
        for (let index = 0; index < times.length; index++) {
          const time = times[index];
          if (time < start || (merged.t.length && time <= merged.t.at(-1))) continue;
          for (const key of keys) merged[key].push(key in (source || {}) ? source[key][index] ?? null : null);
        }
      }
      return merged;
    };
    const events = (old = [], fresh = [], key, keepLast) => {
      const seen = new Set(), merged = [];
      for (const event of [...old, ...fresh].sort((a, b) => a.t - b.t)) {
        const id = key(event);
        if (!seen.has(id)) { seen.add(id); merged.push(event); }
      }
      // Keep the entry in effect when the window starts.
      const first = merged.findIndex(event => event.t >= start);
      const from = first < 0 ? (keepLast ? merged.length - 1 : merged.length) : keepLast ? Math.max(0, first - 1) : first;
      return merged.slice(Math.max(0, from));
    };
    return {...next, since: previous.since ?? null,
      samples: columns(previous.samples, next.samples, ['t', 'cpu', 'memory', 'disk', 'cpu_partial', 'disk_partial', 'processes']),
      gpu: next.gpu || previous.gpu ? columns(previous.gpu, next.gpu, ['t', 'value', 'partial']) : null,
      segments: events(previous.segments, next.segments, event => `${event.t}:${event.reason}`, true),
      coverage: events(previous.coverage, next.coverage, event => String(event.t), true),
      markers: events(previous.markers, next.markers, event => `${event.t}:${event.event}:${event.id}`, false)};
  }
  const lastSample = history => history?.samples?.t?.length ? history.samples.t.at(-1) : null;
  // The newest resources reading a snapshot carries, as a time.
  const sampledAt = view => Date.parse(view?.resources?.sampled_at || view?.service?.resources?.sampled_at || view?.library?.resources?.sampled_at || '') || 0;

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
      id: entry.id, name: entry.name, drive: entry.drive || '', connected: Boolean(entry.connected), eject: entry.eject || null, stateLabel: entry.state_label || null,
      operations: [...appRunning.filter(operation => touches(operation, entry.id)), ...(entry.snapshot?.operations || [])], service: entry.service || null,
    }));
    for (const operation of appRunning.filter(operation => !document.libraries.some(entry => touches(operation, entry.id)))) {
      others.push({id: `app:${operation.id}`, name: operation.label, connected: true, eject: null, stateLabel: null, operations: [operation], service: null});
    }
    return {library, others, app: document.app || null, appOperations: selected ? appRunning.filter(operation => touches(operation, selected.id)) : [],
      attention: Array.isArray(document.attention) ? document.attention : [],
      // Every Pharos process on this Mac, for the Load row.
      resources: document.resources || null,
      // position and count: "Library Pending history (1/3)".
      service: selected ? {id: selected.id, name: selected.name, position: document.libraries.indexOf(selected) + 1, count: document.libraries.length, state: selected.service?.state || null, stateLabel: selected.state_label || null, connected: Boolean(selected.connected), error: selected.error || null, eject: selected.eject || null,
        resources: selected.resources || null} : null};
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
    const paths = endpoints(managed), historyPath = historyEndpoint(managed);
    const listeners = new Set();
    let state = {document: null, view: null, error: null, mode: 'stopped', receivedAt: 0, history: null};
    // generation changes whenever the feed is opened, closed or paused, so a
    // poll that was in flight then is dropped when it returns.
    let source = null, timer = null, running = false, tag = null, pollingSince = 0, generation = 0;
    // streamed counts the stream's accepted events, so a refresh that was in
    // flight when a newer event arrived does not overwrite it.
    let streamed = 0;
    let lastLibrarySizes = null;
    // History: kept only while the panel shows it. historyNone is set when
    // there is no collector (classic execution, an older service): no more
    // requests until the panel is opened again.
    let historyOpen = false, historyBusy = false, historyAt = 0, historySeen = 0, historyGeneration = 0, historyNone = false;

    const notify = () => { for (const listener of [...listeners]) { try { listener(state); } catch (error) { console.error(error); } } };
    const set = changes => { state = {...state, ...changes}; notify(); };
    const accept = document => {
      const view = normalize(document, {managed});
      if (!view) return;
      if (managed) {
        if (view.library && view.service?.id) {
          lastLibrarySizes = {id: view.service.id, storage: view.library.library?.storage_bytes ?? null, wal: view.library.library?.wal_bytes ?? null};
        } else if (view.service?.state === 'running' && lastLibrarySizes?.id === view.service.id) {
          // The broker can still sample processes when the service's status
          // request times out. Keep the last sizes, explicitly marked old.
          view.lastLibrarySizes = lastLibrarySizes;
        } else {
          lastLibrarySizes = null;
        }
      }
      set({document, view, error: null, receivedAt: now()});
      if (historyOpen && sampledAt(view) > historySeen) loadHistory();
    };

    // One history request at a time, at most every HISTORY_MS; a snapshot
    // arriving meanwhile is caught by the next one.
    async function loadHistory(full = false) {
      if (!historyOpen || historyNone || historyBusy || (!full && now() - historyAt < HISTORY_MS)) return;
      const mine = historyGeneration;
      historyBusy = true;
      historyAt = now();
      historySeen = Math.max(historySeen, sampledAt(state.view));
      const since = full ? null : lastSample(state.history);
      try {
        const response = await get(since == null ? historyPath : `${historyPath}?since=${since}`, {});
        if (!historyOpen || mine !== historyGeneration) return;
        // An older service has no route: no history, and no chart.
        const document = response.ok ? await response.json() : {available: false, reason: `HTTP ${response.status}`};
        if (!historyOpen || mine !== historyGeneration) return;
        // Unavailable stops asking, unless the collector has just not read
        // this scope yet (pending).
        if (document?.available === false && !document.pending) historyNone = true;
        set({history: since == null ? document : mergeHistory(state.history, document)});
      } catch { /* the next snapshot tries again */ } finally {
        if (mine === historyGeneration) historyBusy = false;
      }
    }
    function openHistory() {
      if (historyOpen) return;
      historyOpen = true;
      historyGeneration++;
      historyBusy = false;
      historyNone = false;
      historySeen = 0;
      loadHistory(true);
    }
    function closeHistory() {
      historyOpen = false;
      historyGeneration++;
      historyBusy = false;
      if (state.history) set({history: null});
    }
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
      start, stop, visibility, fresh, refresh, openHistory, closeHistory,
      current: () => state,
      subscribe(listener) { listeners.add(listener); return () => listeners.delete(listener); },
    };
  }

  // The page's one store.
  let store = null;
  const instance = () => store ??= createStore({managed: Boolean(window.pharosManagedLibrary), selectedId: window.pharosManagedLibrary?.id || null});
  const start = () => instance().start();
  document.addEventListener('visibilitychange', () => store?.visibility());

  window.pharosStatusStore = {
    enabled, start,
    current: () => store?.current() || null,
    subscribe: listener => instance().subscribe(listener),
    fresh: () => instance().fresh(),
    refresh: () => store?.refresh(),
    openHistory: () => instance().openHistory(),
    closeHistory: () => store?.closeHistory(),
    // Pure helpers, for web/tests/unit/page/status-store.test.mjs.
    createStore, normalize, endpoints, historyEndpoint, brokerEjectVerdict, mergeHistory, POLL_MS, HISTORY_MS,
  };
  start();
})();
