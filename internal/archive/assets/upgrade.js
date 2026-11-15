// Pharos library upgrade. A library indexed by an older Pharos needs its
// derived data brought up to date once (GET /api/upgrade lists what is
// pending). The service runs it as background work; this script follows its
// status for the rest of the page, says when it finishes, and points saved
// table filters at repository names a merge retired.
(() => {
  'use strict';
  const call = async (path, options = {}) => {
    const response = await fetch(path, {...options, headers: {'Content-Type': 'application/json'}});
    const body = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body.error || response.statusText);
    return body;
  };
  const toast = message => { if (typeof window.feedbackToast === 'function') window.feedbackToast(message); };
  let status = null, timer = 0, wasRunning = false;

  // Saved table queries live in the library's preferences and last-used ones
  // in this web view's storage, so a repository merge that retires a display
  // name cannot update them. Point filters on a retired name at the surviving
  // one, once per set of renames on each Mac.
  const RENAMED = 'pharos-repository-renames-applied';
  const REPOSITORY_FIELDS = new Set(['repository_name', 'path_repository']);
  function applyRenames(renames) {
    const names = Object.keys(renames || {});
    if (!names.length) return;
    const version = JSON.stringify(renames);
    try {
      if (localStorage.getItem(RENAMED) === version) return;
      const rename = value => typeof value === 'string' && renames[value] ? renames[value]
        : Array.isArray(value) ? value.map(rename) : value;
      const walk = value => {
        if (Array.isArray(value)) return value.map(walk);
        if (!value || typeof value !== 'object') return value;
        const out = {};
        for (const [key, item] of Object.entries(value)) out[key] = walk(item);
        if (REPOSITORY_FIELDS.has(out.field) && 'value' in out) out.value = rename(out.value);
        return out;
      };
      for (let index = 0; index < localStorage.length; index++) {
        const key = localStorage.key(index);
        if (!key || !key.startsWith('query-table:')) continue;
        const raw = localStorage.getItem(key);
        let parsed;
        try { parsed = JSON.parse(raw); } catch { continue; }
        const next = JSON.stringify(walk(parsed));
        if (next !== raw) localStorage.setItem(key, next);
      }
      const prefs = window.pharosPrefs;
      for (const key of prefs.keys('query-table:')) {
        const value = prefs.get(key);
        if (!value || typeof value !== 'object') continue;
        const next = walk(value);
        if (JSON.stringify(next) !== JSON.stringify(value)) prefs.set(key, next);
      }
      localStorage.setItem(RENAMED, version);
    } catch { /* Storage may be disabled. */ }
  }

  async function refresh() {
    try {
      status = await call('/api/upgrade');
    } catch {
      return;
    }
    applyRenames(status.repository_renames);
    if (wasRunning && !status.running) {
      toast(status.error ? 'The library upgrade stopped before finishing.' : 'The library upgrade finished.');
      // Cards follow; query tables rerun only through their own Run Now and auto-refresh.
      for (const dataset of ['library', 'usage', 'tools', 'tool_calls']) window.pharosQueryTables?.refreshDashboards(dataset);
    }
    wasRunning = status.running;
    window.dispatchEvent(new CustomEvent('pharos:upgrade-status', {detail: status}));
    clearTimeout(timer);
    if (status.running) timer = setTimeout(refresh, 2000);
  }

  document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', refresh);
  else refresh();
})();
