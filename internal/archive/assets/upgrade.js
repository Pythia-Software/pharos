// Pharos library upgrade. A library indexed by an older Pharos needs its
// derived data brought up to date once: repository identities, token
// attribution, harness versions, and the tool ledger (GET /api/upgrade lists
// what is pending). This offers the upgrade once per launch, keeps an
// "Upgrade library" button in the header until it is done, previews the
// repository merges (GET /api/upgrade/preview, which always asks GitHub about
// renamed repositories, through the gh tool, when it is available), and runs every step as one
// background job (POST /api/upgrade). The job survives closing this panel and
// resumes after an eject; the drive badge shows its progress too.
(() => {
  'use strict';
  const OFFERED = 'pharos-upgrade-offered';
  const INTRO = 'This version of Pharos counts tokens more accurately and records more about each tool call and conversation. Your existing catalog needs a one-time pass to catch up. Nothing leaves this Mac except a question to GitHub about repository names, and your transcripts are not changed.';
  const CSS = `
.pharos-upgrade-backdrop{position:fixed;inset:0;z-index:9000;display:grid;place-items:center;padding:24px;background:color-mix(in srgb,#000 42%,transparent)}
.pharos-upgrade{width:min(720px,100%);max-height:calc(100vh - 48px);display:flex;flex-direction:column;background:var(--panel,#fff);color:var(--ink,#222);border:1px solid var(--line,#ccc);border-radius:16px;box-shadow:0 24px 70px #0006;overflow:hidden}
.pharos-upgrade>h2{margin:0;padding:22px 24px 6px;font:700 24px/1.2 var(--serif,serif);letter-spacing:-.02em}
.pharos-upgrade-intro{margin:0;padding:0 24px 16px;color:var(--muted,#666);font-size:14px;border-bottom:1px solid var(--line,#ccc)}
.pharos-upgrade-body{overflow:auto;padding:4px 24px 14px}
.pharos-upgrade-step{display:grid;grid-template-columns:1fr auto;gap:3px 14px;padding:12px 0;border-bottom:1px solid var(--line,#ccc);font-size:13px}
.pharos-upgrade-step strong{font-size:14px}
.pharos-upgrade-step .count{color:var(--muted,#666);white-space:nowrap}
.pharos-upgrade-step p{grid-column:1/-1;margin:0;color:var(--muted,#666)}
.pharos-upgrade-step.done strong::after{content:' ✓';color:var(--accent,#315845)}
.pharos-upgrade-meter{grid-column:1/-1;height:6px;overflow:hidden;border-radius:6px;background:var(--line,#ccc)}
.pharos-upgrade-meter>span{display:block;height:100%;min-width:6px;background:var(--accent,#315845);transition:width .25s}
.pharos-upgrade-merges{margin:12px 0 0;font-size:13px}
.pharos-upgrade-merges p{margin:8px 0;color:var(--muted,#666)}
.pharos-upgrade-merges p.error{color:var(--bad,#9c3d36)}
.pharos-upgrade-merges ul{margin:8px 0;padding-left:18px;max-height:200px;overflow:auto}
.pharos-upgrade-merges li{margin:3px 0}
.pharos-upgrade-merges code{font:12px ui-monospace,SFMono-Regular,monospace;color:var(--muted,#666)}
.pharos-upgrade-actions{display:flex;justify-content:flex-end;align-items:center;gap:10px;padding:14px 24px;border-top:1px solid var(--line,#ccc)}
.pharos-upgrade-actions .note{margin-right:auto;font-size:13px;color:var(--muted,#666)}
.pharos-upgrade-actions .error{margin-right:auto;font-size:13px;color:var(--bad,#9c3d36)}
.pharos-upgrade-actions button,.pharos-upgrade-merges button{border:1px solid var(--line,#ccc);background:var(--panel,#fff);color:var(--ink,#222)}
.pharos-upgrade-actions button.primary{background:var(--accent,#315845);border-color:var(--accent,#315845);color:var(--panel,#fff)}
.pharos-upgrade-actions button:disabled{opacity:.55;cursor:default}
.pharos-upgrade-actions button{flex:none;white-space:nowrap}
.upgrade-toggle{width:auto!important;padding:0 10px!important;font-size:12px;white-space:nowrap}`;

  const node = (tag, cls, text) => {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = text;
    return n;
  };
  const call = async (path, options = {}) => {
    const response = await fetch(path, {...options, headers: {'Content-Type': 'application/json'}});
    const body = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body.error || response.statusText);
    return body;
  };
  const plural = (count, unit) => `${Number(count).toLocaleString()} ${count === 1 ? unit : unit.endsWith('y') ? unit.slice(0, -1) + 'ies' : unit + 's'}`;
  const toast = message => { if (typeof window.feedbackToast === 'function') window.feedbackToast(message); };
  let status = null, timer = 0, wasRunning = false;

  function injectStyle() {
    if (document.getElementById('pharosUpgradeStyle')) return;
    const style = node('style');
    style.id = 'pharosUpgradeStyle';
    style.textContent = CSS;
    document.head.append(style);
  }

  function headerButton() {
    let button = document.getElementById('upgradeToggle');
    const show = status && (status.needed || status.running);
    if (!show) { button?.remove(); return; }
    if (!button) {
      button = node('button', 'header-icon upgrade-toggle');
      button.id = 'upgradeToggle';
      button.type = 'button';
      button.addEventListener('click', open);
      document.querySelector('.header-icons')?.prepend(button);
    }
    button.textContent = status.running ? 'Upgrading…' : 'Upgrade library';
    button.title = status.running ? 'The library upgrade is running' : 'This library needs a one-time upgrade';
  }

  function close() {
    document.getElementById('pharosUpgrade')?.remove();
  }

  function stepRow(step) {
    const running = status.running && status.step === step.id;
    const row = node('div', 'pharos-upgrade-step' + (step.pending === 0 && !running ? ' done' : ''));
    row.append(node('strong', '', step.label));
    let count = step.pending === 0 ? 'Done' : plural(step.pending, step.unit);
    if (running && status.total > 0) count = `${Number(status.done).toLocaleString()} of ${Number(status.total).toLocaleString()}`;
    else if (running) count = 'Running…';
    row.append(node('span', 'count', count));
    row.append(node('p', '', step.detail));
    if (running && status.total > 0) {
      const meter = node('div', 'pharos-upgrade-meter'), fill = node('span');
      fill.style.width = `${Math.round(status.done / status.total * 100)}%`;
      meter.append(fill);
      row.append(meter);
    }
    return row;
  }

  // What GitHub can tell about renamed repositories depends on the gh tool.
  function githubNote(github) {
    if (github === 'missing') return 'Renamed or moved repositories may stay separate: GitHub\u2019s gh command-line tool is not installed. Install it and run gh auth login in Terminal; Pharos checks again every few minutes and merges them then.';
    if (github === 'signed_out') return 'Renamed or moved repositories may stay separate: the gh command-line tool is not signed in to GitHub. Run gh auth login in Terminal; Pharos checks again every few minutes and merges them then.';
    return 'Pharos checks with GitHub, through the gh command-line tool, which repositories were renamed or moved. Nothing else contacts the network.';
  }

  function mergeSection(repositories) {
    const section = node('div', 'pharos-upgrade-merges');
    const note = node('p', 'note', githubNote(status.github));
    const button = node('button', '', 'Preview repository merges');
    const list = node('div');
    button.addEventListener('click', async () => {
      button.disabled = true;
      list.replaceChildren(node('p', 'note', 'Checking repositories…'));
      try {
        const result = await call('/api/upgrade/preview');
        note.textContent = githubNote(result.github);
        const merges = result.repository_merges || [];
        if (!merges.length) { list.replaceChildren(node('p', '', 'No repositories need merging.')); return; }
        const items = node('ul');
        for (const merge of merges) {
          const item = node('li');
          item.append(node('strong', '', merge.name), document.createTextNode(` (${plural(merge.workspaces, 'workspace')}) takes in `), node('code', '', merge.merged.join(', ')));
          items.append(item);
        }
        list.replaceChildren(node('p', '', `${plural(merges.length, 'repository')} will absorb duplicate rows:`), items);
      } catch (error) {
        list.replaceChildren(node('p', 'error', error.message));
      } finally {
        button.disabled = false;
      }
    });
    if (repositories.pending > 0) section.append(note, button, list);
    return section;
  }

  function render() {
    const panel = document.querySelector('#pharosUpgrade .pharos-upgrade');
    if (!panel || !status) return;
    const body = node('div', 'pharos-upgrade-body');
    for (const step of status.steps) body.append(stepRow(step));
    const repositories = status.steps.find(step => step.id === 'repositories') || {pending: 0};
    if (!status.running) body.append(mergeSection(repositories));
    // A library that only needs the repository step (it upgraded before) takes about a minute.
    const onlyRepositories = status.needed && status.steps.every(step => step.id === 'repositories' || step.pending === 0);
    const intro = panel.querySelector('.pharos-upgrade-intro');
    if (intro) intro.textContent = onlyRepositories
      ? 'Some repositories are still split across several entries: worktrees without a remote, and repositories that moved between owners or were renamed. This pass merges them so each repository\u2019s work is counted together. Nothing leaves this Mac except a question to GitHub about repository names, and your transcripts are not changed.'
      : INTRO;
    const actions = node('div', 'pharos-upgrade-actions');
    if (status.error) actions.append(node('span', 'error', `The last attempt stopped: ${status.error}`));
    else actions.append(node('span', 'note', status.running
      ? 'You can close this; the upgrade keeps running, and the drive badge shows its progress. Ejecting stops it safely, and starting it again resumes.'
      : status.needed ? (onlyRepositories ? 'This takes about a minute. You can keep using Pharos meanwhile.' : 'It re-reads the catalog and can take an hour or more on a large library. You can keep using Pharos meanwhile.')
        : 'The library is up to date.'));
    const later = node('button', '', status.running || !status.needed ? 'Close' : 'Later');
    later.type = 'button';
    later.addEventListener('click', close);
    actions.append(later);
    if (!status.running && status.needed) {
      const start = node('button', 'primary', 'Upgrade now');
      start.type = 'button';
      start.addEventListener('click', async () => {
        start.disabled = true;
        try {
          await call('/api/upgrade', {method: 'POST', body: '{}'});
          await refresh();
        } catch (error) {
          start.disabled = false;
          actions.querySelector('.note, .error')?.replaceWith(node('span', 'error', error.message));
        }
      });
      actions.append(start);
    }
    panel.querySelector('.pharos-upgrade-body')?.remove();
    panel.querySelector('.pharos-upgrade-actions')?.remove();
    panel.append(body, actions);
  }

  function open() {
    if (!status) return;
    injectStyle();
    if (!document.getElementById('pharosUpgrade')) {
      const backdrop = node('div', 'pharos-upgrade-backdrop');
      backdrop.id = 'pharosUpgrade';
      const panel = node('div', 'pharos-upgrade');
      panel.setAttribute('role', 'dialog');
      panel.setAttribute('aria-label', 'Upgrade this library');
      panel.append(node('h2', '', 'Upgrade this library'),
        node('p', 'pharos-upgrade-intro', INTRO));
      backdrop.append(panel);
      backdrop.addEventListener('click', event => { if (event.target === backdrop) close(); });
      document.body.append(backdrop);
    }
    render();
  }

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
      for (const dataset of ['library', 'usage', 'tools', 'tool_calls']) window.pharosQueryTables?.refresh(dataset);
    }
    wasRunning = status.running;
    headerButton();
    render();
    clearTimeout(timer);
    if (status.running) timer = setTimeout(refresh, 2000);
  }

  // Offer the upgrade once per session. Onboarding a new Mac comes first:
  // while its panel is open, wait for it to close before offering.
  function offer() {
    if (!status || !status.needed || status.running) return;
    try {
      if (sessionStorage.getItem(OFFERED)) return;
    } catch { /* Storage may be disabled; offer every load. */ }
    if (document.getElementById('pharosOnboarding')) {
      const watcher = new MutationObserver(() => {
        if (document.getElementById('pharosOnboarding')) return;
        watcher.disconnect();
        offer();
      });
      watcher.observe(document.body, {childList: true});
      return;
    }
    try { sessionStorage.setItem(OFFERED, '1'); } catch { /* See above. */ }
    open();
  }

  async function start() {
    await refresh();
    offer();
    // Onboarding can open after this offer (it waits on its own request);
    // step aside for it, and offer again once it closes.
    new MutationObserver(() => {
      if (!document.getElementById('pharosOnboarding') || !document.getElementById('pharosUpgrade') || status?.running) return;
      close();
      try { sessionStorage.removeItem(OFFERED); } catch { /* Storage may be disabled. */ }
      offer();
    }).observe(document.body, {childList: true});
  }

  window.pharosUpgrade = {open, refresh};
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start);
  else start();
})();
