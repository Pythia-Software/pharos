// Reports what goes wrong in the page to Settings → Logs (Page errors):
// uncaught errors and promise rejections, scripts and styles that fail to
// load, requests that fail or stall, a page that has not finished loading,
// and, through pharosReportEvents, what the app saw (a web process that
// stopped). The shell loads it first, before anything that can fail.
const PAGE_ERROR_LIMIT = 30;
// The most events POST /api/logs/page records from one request.
const PAGE_ERROR_BATCH = 20;
const PAGE_REQUEST_STALL_MS = 15000;
const PAGE_LOAD_STALL_MS = 20000;

function describePageError(value) {
  if (value && typeof value === 'object' && 'message' in value) {
    const name = typeof value.name === 'string' && value.name !== 'Error' ? value.name + ': ' : '';
    return {message: name + String(value.message), detail: typeof value.stack === 'string' ? value.stack : ''};
  }
  try {
    return {message: typeof value === 'string' ? value : JSON.stringify(value) ?? String(value), detail: ''};
  } catch {
    return {message: String(value), detail: ''};
  }
}

// A same-origin URL as its path and query; another origin without its query.
function pageRequestPath(input, base) {
  const raw = typeof input === 'string' ? input : input?.url ?? String(input);
  try {
    const url = new URL(raw, base);
    return url.origin === new URL(base).origin ? url.pathname + url.search : url.origin + url.pathname;
  } catch {
    return raw;
  }
}

function pageRequestMethod(input, init) {
  return String(init?.method || (typeof input === 'object' ? input?.method : '') || 'GET').toUpperCase();
}

// One report per distinct event per page load, and at most PAGE_ERROR_LIMIT:
// a failing render can throw on every frame.
function createPageErrorQueue(send, schedule, limit = PAGE_ERROR_LIMIT) {
  const seen = new Set(), pending = [];
  let accepted = 0, scheduled = false;
  const flush = () => {
    scheduled = false;
    while (pending.length) send(pending.splice(0, PAGE_ERROR_BATCH));
  };
  return {
    flush,
    report(event) {
      const key = [event.kind, event.message, event.path ?? ''].join('\n');
      if (!event.message || seen.has(key) || accepted >= limit) return false;
      seen.add(key);
      accepted++;
      pending.push(event);
      if (!scheduled) { scheduled = true; schedule(flush); }
      return true;
    },
  };
}

(() => {
  'use strict';
  if (typeof window.addEventListener !== 'function' || window.pharosReportEvents) return;
  const originalFetch = typeof window.fetch === 'function' ? window.fetch.bind(window) : null;
  const page = () => location.pathname + location.search;
  const queue = createPageErrorQueue(events => {
    try {
      originalFetch?.('/api/logs/page', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({events}), keepalive: true}).catch(() => {});
    } catch {}
  }, flush => setTimeout(flush, 1000));
  const report = event => queue.report({page: page(), ...event});
  window.pharosReportEvents = events => { for (const event of Array.isArray(events) ? events : []) report(event); };
  window.pharosReportError = (error, kind = 'render', extra = '') => {
    const described = describePageError(error);
    report({kind, message: described.message, detail: [described.detail, extra].filter(Boolean).join('\n\n')});
  };
  addEventListener('error', event => {
    // An element's load error has the element as its target; an uncaught
    // error, the window.
    const target = event.target;
    if (target?.tagName) {
      // Images and media fail for ordinary reasons; a script or style
      // that does not load can leave the page blank.
      const source = target.tagName === 'SCRIPT' ? target.src : target.tagName === 'LINK' && target.rel === 'stylesheet' ? target.href : '';
      if (source) report({kind: 'resource', message: `Could not load ${pageRequestPath(source, location.href)}`, path: pageRequestPath(source, location.href)});
      return;
    }
    const described = describePageError(event.error ?? event.message);
    report({kind: 'error', message: described.message || String(event.message), detail: described.detail || (event.filename ? `${event.filename}:${event.lineno}:${event.colno}` : '')});
  }, true);
  addEventListener('unhandledrejection', event => {
    if (event.reason?.name === 'AbortError') return;
    report({kind: 'rejection', ...describePageError(event.reason)});
  });
  addEventListener('pagehide', queue.flush);
  setTimeout(() => {
    if (document.readyState !== 'complete') report({kind: 'stalled', message: `The page had not finished loading after ${PAGE_LOAD_STALL_MS / 1000} s (${document.readyState})`});
  }, PAGE_LOAD_STALL_MS);
  if (!originalFetch) return;
  window.fetch = function(input, init) {
    const path = pageRequestPath(input, location.href), method = pageRequestMethod(input, init), started = Date.now();
    if (path.startsWith('/api/logs/')) return originalFetch(input, init);
    // Actions (a sync, an export) may run long by design; a read that has
    // not answered is what leaves a page blank.
    const stalled = method === 'GET' ? setTimeout(() => report({kind: 'stalled', message: `${method} ${path} had not answered after ${PAGE_REQUEST_STALL_MS / 1000} s`, path}), PAGE_REQUEST_STALL_MS) : 0;
    return originalFetch(input, init).then(response => {
      clearTimeout(stalled);
      if (response.status >= 500) report({kind: 'request', message: `${method} ${path} returned ${response.status}`, path, status: response.status, duration_ms: Date.now() - started});
      return response;
    }, error => {
      clearTimeout(stalled);
      if (error?.name !== 'AbortError') report({kind: 'request', message: `${method} ${path} failed: ${describePageError(error).message}`, path, duration_ms: Date.now() - started});
      throw error;
    });
  };
})();
