(() => {
  if (document.getElementById('pharosShareData')) return;
  let status = null;
  let busy = false;
  let timer = null;
  let activeRun = null;
  let runningIds = new Set();
  let openIssues = [];
  const openPanels = new Set();
  const initializedPanels = new Set();
  const node = (tag, text, className = '') => {
    const element = document.createElement(tag);
    element.textContent = text;
    element.className = className;
    return element;
  };
  const call = async (path, body) => {
    const response = await fetch(path, body === undefined ? {} : {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body)});
    const value = await response.json();
    if (!response.ok) throw new Error(value.error || `HTTP ${response.status}`);
    return value;
  };
  const button = (text, action) => {
    const element = node('button', text);
    element.type = 'button';
    element.onclick = async () => {
      element.disabled = true;
      try { await action(); }
      catch (error) { window.pharosLibrary?.toast?.(error.message); showError(error.message); }
      finally { element.disabled = false; await refresh(); }
    };
    return element;
  };
  const number = value => value == null || !Number.isFinite(Number(value)) ? '—' : Number(value).toLocaleString(undefined, {maximumFractionDigits: 0});
  const seconds = value => {
    if (value == null || !Number.isFinite(Number(value))) return '—';
    const duration = Math.max(0, Number(value));
    if (duration === 0) return '0s';
    if (duration < 1) return '<1s';
    const rounded = Math.round(duration);
    if (rounded < 60) return `${rounded}s`;
    if (rounded < 3600) return `${Math.floor(rounded / 60)}m${rounded % 60 ? ` ${rounded % 60}s` : ''}`;
    return `${Math.floor(rounded / 3600)}h${Math.floor(rounded % 3600 / 60) ? ` ${Math.floor(rounded % 3600 / 60)}m` : ''}`;
  };
  const preservationState = value => {
    if (value?.pending_newer_inputs) return 'pending';
    if (value?.status === 'indexed') return 'indexed';
    return 'untracked';
  };
  const memory = value => value == null || !Number.isFinite(Number(value)) ? '—' : Number(value) >= 1073741824
    ? `${(Number(value) / 1073741824).toFixed(1)} GiB` : `${number(Number(value) / 1048576)} MiB`;
  const percent = value => value == null || !Number.isFinite(Number(value)) ? '—' : `${Number(value).toLocaleString(undefined, {maximumFractionDigits: 1})}%`;
  const when = value => {
    if (!value || !Number.isFinite(new Date(value).getTime())) return 'Not yet';
    const delta = (Date.now() - new Date(value).getTime()) / 1000;
    const distance = Math.abs(delta);
    if (distance < 60) return delta < 0 ? 'In <1m' : 'Just now';
    const label = distance < 3600 ? `${Math.floor(distance / 60)}m` : distance < 86400 ? `${Math.floor(distance / 3600)}h` : `${Math.floor(distance / 86400)}d`;
    return delta < 0 ? `In ${label}` : `${label} ago`;
  };
  const time = value => {
    const element = node('time', when(value));
    if (value && Number.isFinite(new Date(value).getTime())) {
      element.dateTime = new Date(value).toISOString();
      element.title = new Date(value).toLocaleString();
    }
    return element;
  };
  const panel = (title, key, initiallyOpen = false) => {
    if (!initializedPanels.has(key)) {
      initializedPanels.add(key);
      if (initiallyOpen) openPanels.add(key);
    }
    const element = node('details', '', 'sync-panel sync-disclosure');
    element.open = openPanels.has(key);
    const summary = node('summary', title);
    summary.dataset.syncFocus = key;
    element.append(summary);
    element.ontoggle = () => {
      if (!element.isConnected) return;
      if (element.open) openPanels.add(key); else openPanels.delete(key);
    };
    return element;
  };
  const style = node('style', `
    #syncDashboard{display:grid;grid-template-columns:minmax(0,1fr);gap:18px;min-width:0}
    #syncDashboard h3{margin:0;font-size:var(--fs-lg)}
    #syncDashboard p{margin:8px 0;font-size:var(--fs-sm)}
    #syncDashboard :is(.sync-controls,.sync-actions,.sync-warning)>button,#syncDashboard .sync-disclosure>p>button,.sync-dialog button{padding:7px 14px;border:1px solid var(--line);border-radius:6px;background:var(--panel);color:var(--ink);font:inherit;font-size:var(--fs-sm);font-weight:600;cursor:pointer}
    #syncDashboard .sync-detail-button:hover:not(:disabled),.sync-dialog button:hover:not(:disabled){border-color:var(--accent)}
    #syncDashboard button:disabled,.sync-dialog button:disabled{opacity:.55;cursor:default}
    .sync-panel{border:1px solid var(--line);border-radius:8px;padding:18px;background:var(--panel);min-width:0}
    .sync-section-head,.sync-controls,.sync-actions{display:flex;align-items:center;gap:12px;flex-wrap:wrap}
    .sync-section-head{justify-content:space-between;margin-bottom:16px}
    .sync-section-head p{margin:4px 0 0!important}
    .sync-controls label{display:inline-flex;align-items:center;gap:8px}
    .sync-controls select,.sync-controls input[type=number]{font:inherit;border:1px solid var(--line);border-radius:5px;padding:5px 8px;background-color:var(--bg);color:var(--ink)}
    .sync-controls select{padding-right:30px}
    .sync-controls>button{border:1px solid var(--line);padding:6px 12px;background:var(--bg)}
    .sync-summary{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:1px;background:var(--line);border:1px solid var(--line);border-radius:6px;overflow:hidden;margin-bottom:18px}
    .sync-metric{background:var(--panel);padding:14px 16px;min-width:0}
    .sync-metric span{display:block;color:var(--muted);font-size:var(--fs-sm)}
    .sync-metric strong{display:block;font-size:var(--fs-2xl);font-weight:600;margin-top:4px;font-variant-numeric:tabular-nums}
    .sync-result{display:inline-block;max-width:100%;box-sizing:border-box;white-space:normal;overflow-wrap:anywhere;padding:2px 7px;border:1px solid var(--line);border-radius:4px;font-size:var(--fs-xs)}
    .sync-result.attention{color:var(--warn);border-color:var(--warn)}
    .sync-disclosure>summary{cursor:pointer;font-weight:600;font-size:var(--fs-md)}
    .sync-disclosure[open]>summary{margin-bottom:16px}
    .sync-separate{margin-top:14px;border-top:1px solid var(--line);padding-top:12px}
    .sync-separate>summary{cursor:pointer;font-size:var(--fs-sm);color:var(--muted)}
    #syncDashboard fieldset{border:0;padding:0;margin:14px 0 0}
    #syncDashboard legend{font-size:var(--fs-sm);color:var(--muted);margin-bottom:8px}
    .sync-warning{border:1px solid var(--warn);padding:14px;border-radius:6px}
    .sync-dialog{max-width:620px;background:var(--panel);color:var(--ink);border:1px solid var(--line);border-radius:10px;padding:24px}
    .sync-dialog::backdrop{background:#0008}
    .header-sync[aria-busy=true] svg{animation:sync-spin 1.5s linear infinite}
    #syncDashboard [hidden]{display:none}
    @media(max-width:760px){.sync-panel{padding:14px}.sync-summary{grid-template-columns:repeat(2,minmax(0,1fr))}.sync-metric{padding:12px}}
    @keyframes sync-spin{to{transform:rotate(360deg)}}
  `);
  document.head.append(style);

  function showError(message) {
    const dashboard = document.getElementById('syncDashboard');
    if (dashboard) dashboard.prepend(node('p', message, 'badtext'));
  }

  async function recovery(mode, sources, resume) {
    if (resume) { await call('/api/sync/recovery', {resume}); return; }
    const dialog = node('dialog', '', 'sync-dialog');
    dialog.append(node('h2', mode === 'full' ? 'Full recapture & reindex' : 'Reindex from stored messages'));
    if (mode === 'full') {
      const estimate = node('p', 'Measuring retained scope…', 'muted');
      dialog.append(estimate);
      const query = new URLSearchParams({estimate: 'true'});
      for (const source of sources || []) query.append('source', source);
      call(`/api/sync/recovery?${query}`).then(value => {
        estimate.textContent = `${value.files} retained source parts (${memory(value.bytes)}), about ${value.groups} indexed groups. Copy estimate: ${value.capture_seconds == null ? 'unknown' : seconds(value.capture_seconds)}; committed-ingest estimate: ${value.index_seconds == null ? 'unknown' : seconds(value.index_seconds)}. ${value.note}`;
      }).catch(() => { estimate.textContent = 'No complete throughput measurements yet. Capture, indexing, verification and library analysis can take a long time.'; });
    }
    dialog.append(node('p', mode === 'full'
      ? 'Freshly copy available source evidence on this Mac, then reindex selected captures from every Mac. This can take a long time. It is stoppable and resumable. Historical evidence and catalog-only user records are preserved. Other Macs are re-indexed from retained captures, not recaptured remotely.'
      : 'Reindex search, conversation documents, sessions, usage and tool ledgers from the messages the library already stores. No original source is required. This cannot recreate missing transcripts or source-only metadata. Your findings, preferences, identity links and retained history are preserved.'));
    if (sources?.length) dialog.append(node('p', `Scope: ${sources.join(', ')}`));
    const actions = node('div', '', 'sync-actions');
    actions.append(button('Cancel', () => dialog.close()), button('Begin repair', async () => {
      await call('/api/sync/recovery', {mode, sources});
      dialog.close();
    }));
    dialog.append(actions);
    dialog.addEventListener('close', () => dialog.remove());
    document.body.append(dialog);
    dialog.showModal();
  }

  // Whether a run among `followed` (the ids running at the last poll) has
  // since finished having indexed something. A run that has left the list
  // can't say what it changed, so it counts.
  const finishedRunsChanged = (followed, runs) => [...followed].map(id => runs.find(run => run.id === id))
    .filter(run => run?.state !== 'running').some(run => !run || run.workspaces > 0 || run.conversations > 0 || run.changed === true);
  // The header's Index changes is busy while a run is running.
  // A background pass is not one: Index changes stops it, so it stays free.
  const runningRun = activity => (activity.runs || []).find(run => run.state === 'running' && run.kind !== 'background-update');

  // Home and the card pages rerun their queries when a run finishes having
  // indexed something. Runs that found no changes leave their results as they
  // were. Every run is followed, so one finishing beside another still counts.
  function announceChanges(activity) {
    const runs = activity.runs || [];
    const changed = finishedRunsChanged(runningIds, runs);
    runningIds = new Set(runs.filter(run => run.state === 'running').map(run => run.id));
    if (changed) {
      window.dispatchEvent(new CustomEvent('pharos:data-refresh', {detail: 'sync'}));
    }
  }

  function renderHeader(activity) {
    const action = document.getElementById('headerSync');
    if (!action) return;
    const active = runningRun(activity);
    const wasActive = Boolean(activeRun);
    activeRun = active || null;
    // With the unified activity preference on, status-panel.js owns the
    // header's Index changes state.
    if (window.pharosStatusStore?.enabled() === true) {
      if (wasActive && !active) window.pharosLibrary?.refresh?.();
      window.pharosLibrary?.refreshSyncControls?.();
      return;
    }
    // A running sync is stopped from the drive panel, not the header.
    if (active) {
      action.disabled = true;
      action.setAttribute('aria-busy', 'true');
      const automatic = active.kind === 'automatic-sync';
      action.title = automatic ? `Indexing changes: ${active.phase === 'verifying' ? 'verifying a conversation' : active.phase === 'checking' ? 'checking for changes' : `${active.conversations} conversations`}` : active.kind === 'manual-sync' ? `Manual sync: ${active.phase}` : `${active.kind}: ${active.phase}`;
      action.setAttribute('aria-label', action.title);
    } else {
      action.setAttribute('aria-busy', 'false');
      action.title = 'Index changes';
      action.setAttribute('aria-label', 'Index changes');
      if (wasActive) window.pharosLibrary?.refresh?.();
    }
    window.pharosLibrary?.refreshSyncControls?.();
  }

  function renderDashboard() {
    const dashboard = document.getElementById('syncDashboard');
    if (!dashboard || !status?.settings || (dashboard.contains(document.activeElement) && document.activeElement.matches('input,select'))) return;
    const focused = dashboard.contains(document.activeElement) ? document.activeElement.dataset.syncFocus : null;
    // Keep the React tables and activity panel attached. Detaching a host
    // resets its scroll containers even when the React root is retained.
    const retained = new Set(['backgroundSchedule', 'syncHistoryTables', 'syncSourceTables', 'syncDiagnosticTables', 'syncOperations'].map(id => document.getElementById(id)));
    for (const child of [...dashboard.children]) if (!retained.has(child)) child.remove();
    // The schedule (Background updates) is background.js's, at the top.
    const repairs = panel('Repair & maintenance', 'repairs', status.recovery_jobs?.some(job => ['interrupted', 'failed'].includes(job.state)));
    repairs.append(node('p', 'For missing data or damaged indexes. These operations can take a long time; everyday capturing and indexing do not need them.', 'muted'));
    const actions = node('div', '', 'sync-actions');
    actions.append(button('Full recapture & reindex…', () => recovery('full')), button('Reindex from stored messages…', () => recovery('retained')), button('Verify all retained inputs', () => call('/api/sync/verify', {all: true})));
    repairs.append(actions);
    for (const job of status.recovery_jobs || []) {
      const line = node('p', `${job.mode} repair: ${job.state} · last progress ${when(job.updated_at)}${job.error ? ` · ${job.error}` : ''}`);
      const resume = button('Resume repair', () => recovery(job.mode, undefined, job.id));
      resume.disabled = Boolean(status.busy);
      line.append(resume); repairs.append(line);
    }
    for (const source of status.source_states || []) {
      const preservation = source.preservation || {};
      const state = preservationState(preservation);
      if (state === 'untracked') continue;
      const labels = {pending: preservation.error ? 'Acquisition failed; inputs remain pending' : 'Newer inputs pending', indexed: 'Latest retained acquisition indexed'};
      const line = node('p', `${source.source_name} · ${labels[state]} `);
      line.append(button('Repair this source…', () => recovery('full', [source.source_name])));
      repairs.append(line);
    }
    dashboard.append(repairs);
    for (const issue of status.issues || []) {
      const warning = node('div', '', 'sync-warning');
      warning.append(node('strong', 'Verifying indexed changes found a mismatch.'), node('p', `${issue.source_name} · ${issue.unit}`), button('View details / copy diagnostics', async () => {
        const data = await call('/api/sync/issues');
        const details = data.issues.find(item => item.id === issue.id);
        const dialog = node('dialog', '', 'sync-dialog');
        dialog.append(node('h2', 'Index integrity issue'), node('pre', JSON.stringify(details, null, 2)), button('Copy diagnostics', () => navigator.clipboard.writeText(JSON.stringify(details, null, 2))), button('Close', () => dialog.close()));
        dialog.addEventListener('close', () => dialog.remove()); document.body.append(dialog); dialog.showModal();
      }), button('Full recapture & reindex…', () => recovery('full', [issue.source_name])));
      dashboard.prepend(warning);
    }
    if (focused) [...dashboard.querySelectorAll('[data-sync-focus]')].find(element => element.dataset.syncFocus === focused)?.focus({preventScroll: true});
  }

  async function refresh({automatic = false} = {}) {
    if (busy) return;
    busy = true;
    try {
      const activity = await call('/api/activity');
      renderHeader(activity);
      announceChanges(activity);
      if (document.getElementById('settings')?.classList.contains('active') || !status || status.library_empty) {
        const latest = await call('/api/sync/status');
        // Keep history still while it is being reviewed. Page visits, Refresh
        // and actions replace it; activity, source status and alerts stay live.
        status = automatic && status?.settings ? {...latest, history: status.history} : latest;
        renderDashboard();
        window.dispatchEvent(new Event('pharos:sync-status'));
      }
      // Open mismatches turn the drive chip's dot red; the drive panel lists them.
      const issues = await call('/api/sync/issues');
      const open = issues.issues?.filter(issue => issue.state === 'open') || [];
      if (JSON.stringify(open.map(issue => issue.id)) !== JSON.stringify(openIssues.map(issue => issue.id))) {
        openIssues = open;
        window.pharosLibrary?.renderAlerts?.();
      }
    } catch (error) { if (!status) showError(error.message); }
    finally { busy = false; clearTimeout(timer); timer = setTimeout(() => refresh({automatic: true}), 3000); }
  }
  window.addEventListener('pharos:route', () => refresh());
  window.pharosSync = {status: () => status, refresh, recovery, isBusy: () => Boolean(activeRun), issues: () => openIssues,
    // Pure helpers, for web/tests/unit/page/sync.test.mjs.
    finishedRunsChanged, runningRun, preservationState, format: {seconds, memory, when, number, percent}};
  refresh();
})();
