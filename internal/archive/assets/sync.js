(() => {
  if (document.getElementById('pharosShareData')) return;
  let status = null;
  let busy = false;
  let timer = null;
  let activeRun = null;
  let openIssues = [];
  let showAllRuns = false;
  const recentRunLimit = 5;
  const openPanels = new Set();
  const initializedPanels = new Set();
  const openRuns = new Set();
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
  const table = (label, headings, key = label) => {
    const wrapper = node('div', '', 'sync-table-wrap');
    wrapper.tabIndex = 0;
    wrapper.dataset.syncScroll = key;
    wrapper.dataset.syncFocus = `table-${key}`;
    wrapper.setAttribute('role', 'region');
    wrapper.setAttribute('aria-label', label);
    const element = node('table', '', 'sync-table');
    element.append(node('caption', label, 'sync-sr-only'));
    const head = node('thead', '');
    const row = node('tr', '');
    for (const heading of headings) {
      const cell = node('th', heading);
      cell.scope = 'col';
      row.append(cell);
    }
    head.append(row);
    const body = node('tbody', '');
    element.append(head, body);
    wrapper.append(element);
    return {wrapper, body};
  };
  const cells = (body, values) => {
    const row = node('tr', '');
    for (const value of values) {
      const cell = node('td', '');
      cell.append(value instanceof Node ? value : document.createTextNode(String(value)));
      row.append(cell);
    }
    body.append(row);
    return row;
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
  const sourceNames = run => run.sources?.length ? run.sources : [...new Set([
    ...(run.source_results || []).map(result => result.source),
    ...(run.phases || []).filter(phase => !['audit', 'projections', 'github', 'identities', 'git', 'tools', 'authorship', 'findings'].includes(phase.name)).map(phase => phase.name)
  ])];
  const resultLabel = run => run.state === 'failed' ? 'Failed' : run.state === 'interrupted' ? 'Stopped'
    : run.audit?.outcome === 'mismatch' ? 'Needs attention' : run.state && run.state !== 'complete' ? run.state
    : run.audit?.outcome && run.audit.outcome !== 'passed' ? 'Check deferred' : run.changed_groups > 0 || run.conversations > 0 ? 'Indexed' : 'No changes';
  const style = node('style', `
    #syncDashboard{display:grid;gap:18px;min-width:0}
    #syncDashboard h3{margin:0;font-size:var(--fs-lg)}
    #syncDashboard p{margin:8px 0;font-size:var(--fs-sm)}
    #syncDashboard button:not(.pharos-button):not(.sync-run-toggle),.sync-dialog button{padding:7px 14px;border:1px solid var(--line);border-radius:6px;background:var(--panel);color:var(--ink);font:inherit;font-size:var(--fs-sm);font-weight:600;cursor:pointer}
    #syncDashboard button:hover:not(:disabled),.sync-dialog button:hover:not(:disabled){border-color:var(--accent)}
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
    .sync-table-wrap{overflow-x:auto;max-width:100%}
    .sync-table{border-collapse:collapse;width:100%;font-size:var(--fs-sm);font-variant-numeric:tabular-nums}
    .sync-table th,.sync-table td{text-align:left;padding:12px 10px;border-bottom:1px solid var(--line);vertical-align:top}
    .sync-table th{color:var(--muted);font-weight:500;white-space:nowrap}
    .sync-table td{white-space:nowrap}
    .sync-history>.sync-table th:nth-child(n+4),.sync-history>.sync-table>tbody>tr>td:nth-child(n+4){text-align:right}
    .sync-table tbody tr:last-child td{border-bottom:0}
    .sync-table tbody tr:hover{background:color-mix(in srgb,var(--bg) 45%,transparent)}
    .sync-table .sync-source-list{white-space:normal;min-width:150px;overflow-wrap:anywhere}
    .sync-source-list span{display:block;margin-bottom:3px}
    .sync-table .sync-run-detail{white-space:normal;padding:16px;background:var(--bg)}
    .sync-run-detail h4{margin:0 0 8px}
    .sync-run-detail .sync-table{margin-bottom:16px}
    .sync-run-toggle{display:block;margin-top:5px;padding:0;border:0;background:transparent;color:var(--accent);font-size:var(--fs-xs);text-decoration:underline}
    .sync-result{display:inline-block;padding:2px 7px;border:1px solid var(--line);border-radius:4px;font-size:var(--fs-xs)}
    .sync-result.attention{color:var(--warn);border-color:var(--warn)}
    .sync-disclosure>summary{cursor:pointer;font-weight:600;font-size:var(--fs-md)}
    .sync-disclosure[open]>summary{margin-bottom:16px}
    .sync-separate{margin-top:14px;border-top:1px solid var(--line);padding-top:12px}
    .sync-separate>summary{cursor:pointer;font-size:var(--fs-sm);color:var(--muted)}
    #syncDashboard fieldset{border:0;padding:0;margin:14px 0 0}
    #syncDashboard legend{font-size:var(--fs-sm);color:var(--muted);margin-bottom:8px}
    .sync-source-note{display:block;white-space:normal;max-width:320px;margin-top:5px;font-size:var(--fs-xs)}
    .sync-warning{border:1px solid var(--warn);padding:14px;border-radius:6px}
    .sync-empty{text-align:center;padding:24px;color:var(--muted)}
    .sync-dialog{max-width:620px;background:var(--panel);color:var(--ink);border:1px solid var(--line);border-radius:10px;padding:24px}
    .sync-dialog::backdrop{background:#0008}
    .sync-sr-only{position:absolute;width:1px;height:1px;overflow:hidden;clip-path:inset(50%)}
    .header-sync[aria-busy=true] svg{animation:sync-spin 1.5s linear infinite}
    #syncDashboard [hidden]{display:none}
    @keyframes sync-spin{to{transform:rotate(360deg)}}
    @media(max-width:760px){.sync-panel{padding:14px}.sync-summary{grid-template-columns:repeat(2,minmax(0,1fr))}.sync-metric{padding:12px}.sync-table th,.sync-table td{padding:10px 8px}}
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

  function renderHeader(activity) {
    const action = document.getElementById('headerSync');
    if (!action) return;
    const active = (activity.runs || []).find(run => run.state === 'running');
    const wasActive = Boolean(activeRun);
    activeRun = active || null;
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

  function renderRunDetails(element, run) {
    element.replaceChildren();
    if (run.source_results?.length) {
      element.append(node('h4', 'Indexed by source'));
      const sources = table('Source breakdown', ['Source', 'Conversations', 'Messages', 'Changed groups', 'Status'], `sources-${run.run_id || run.finished_at}`);
      for (const result of run.source_results) cells(sources.body, [result.source, number(result.conversations), number(result.messages), number(result.workspaces), result.error ? 'Failed' : result.yielded ? 'Partial' : 'Complete']);
      element.append(sources.wrapper);
    } else {
      element.append(node('p', 'Per-source counts were not recorded for this older run.', 'muted'));
    }
    if (run.phases?.length) {
      const phases = table('Time by phase', ['Phase', 'Elapsed', 'CPU time'], `phases-${run.run_id || run.finished_at}`);
      for (const phase of run.phases) cells(phases.body, [phase.name === 'audit' ? 'Verification' : phase.name === 'projections' ? 'Library view' : phase.name, seconds(phase.seconds), seconds(phase.cpu_seconds)]);
      element.append(phases.wrapper);
    }
    const audit = run.audit;
    if (run.trigger) element.append(node('p', run.trigger === 'manual' ? 'Manual sync · includes analysis refreshes' : 'Automatic indexing · analyses deferred', 'muted'));
    if (audit) element.append(node('p', `Verification: ${audit.outcome || 'Unknown'} · ${number(audit.discovered_units)} discoverable units · ${number(audit.deferred_units)} deferred. ${audit.detail || ''}`));
    element.append(node('p', `CPU: ${percent(run.cpu_percent_one_core)} of one core · RAM at start: ${memory(run.rss_baseline_bytes)} · Allocated: ${memory(run.allocated_bytes)}`, 'muted'));
    if (run.finished_at) element.append(node('p', `Finished ${new Date(run.finished_at).toLocaleString()}`, 'muted'));
  }

  function renderRecentRuns(dashboard) {
    const history = status.history || [];
    const latest = history[0];
    const section = node('section', '', 'sync-panel');
    section.setAttribute('aria-label', 'Recent runs');
    const heading = node('div', '', 'sync-section-head');
    const title = node('div', '');
    title.append(node('h3', 'Recent runs'));
    const subtitle = node('p', latest ? 'Index changes · Last run ' : 'Index changes on this Mac', 'muted');
    if (latest) subtitle.append(time(latest.finished_at), document.createTextNode(` · ${resultLabel(latest)}`));
    title.append(subtitle);
    heading.append(title);
    if (status.busy) heading.append(node('span', status.pause_reason || 'Indexing in progress', 'sync-result'));
    section.append(heading);
    if (!latest) {
      section.append(node('p', 'No runs yet. Use Index changes now to index your selected sources.', 'sync-empty'));
      dashboard.append(section);
      return;
    }
    const summary = node('div', '', 'sync-summary');
    summary.setAttribute('aria-label', 'Last run cost');
    for (const [label, value] of [['Conversations indexed', number(latest.conversations)], ['Elapsed time', seconds(latest.seconds)], ['CPU time', seconds(latest.cpu_seconds)], ['RAM peak', memory(latest.rss_observed_peak_bytes)]]) {
      const metric = node('div', '', 'sync-metric');
      metric.append(node('span', label), node('strong', value));
      summary.append(metric);
    }
    section.append(summary);
    const runs = table('Recent run history', ['When', 'Result', 'Sources', 'Conversations', 'Messages', 'Elapsed', 'CPU time', 'RAM peak']);
    runs.wrapper.classList.add('sync-history');
    for (const run of showAllRuns ? history : history.slice(0, recentRunLimit)) {
      const key = run.run_id || run.finished_at;
      const timestamp = node('div', '');
      timestamp.append(time(run.finished_at));
      const toggle = node('button', openRuns.has(key) ? 'Hide details' : 'Details', 'sync-run-toggle');
      toggle.type = 'button';
      toggle.dataset.syncFocus = `run-${key}`;
      toggle.setAttribute('aria-label', `Details for run ${when(run.finished_at)}`);
      toggle.setAttribute('aria-expanded', String(openRuns.has(key)));
      timestamp.append(toggle);
      const sources = node('div', '', 'sync-source-list');
      for (const name of sourceNames(run)) {
        const result = run.source_results?.find(item => item.source === name);
        const text = result ? `${name} · ${number(result.conversations)} conv.` : name;
        const source = node('span', text);
        if (result) source.title = `${number(result.messages)} messages in indexed conversations${result.error ? ' · Failed' : result.yielded ? ' · Partial' : ''}`;
        sources.append(source);
      }
      if (!sources.childElementCount) sources.append(node('span', 'Not recorded', 'muted'));
      const result = resultLabel(run);
      cells(runs.body, [timestamp, node('span', result, `sync-result${['Failed', 'Stopped', 'Needs attention', 'Check deferred'].includes(result) ? ' attention' : ''}`), sources, number(run.conversations), number(run.messages_in_changed_groups), seconds(run.seconds), seconds(run.cpu_seconds), memory(run.rss_observed_peak_bytes)]);
      const detailsRow = node('tr', '');
      detailsRow.hidden = !openRuns.has(key);
      const detailsCell = node('td', '', 'sync-run-detail');
      detailsCell.colSpan = 8;
      detailsRow.append(detailsCell);
      runs.body.append(detailsRow);
      if (!detailsRow.hidden) renderRunDetails(detailsCell, run);
      toggle.onclick = () => {
        detailsRow.hidden = !detailsRow.hidden;
        if (detailsRow.hidden) openRuns.delete(key); else {
          openRuns.add(key);
          renderRunDetails(detailsCell, run);
        }
        toggle.textContent = detailsRow.hidden ? 'Details' : 'Hide details';
        toggle.setAttribute('aria-expanded', String(!detailsRow.hidden));
      };
    }
    section.append(runs.wrapper);
    if (history.length > recentRunLimit) {
      const more = node('button', showAllRuns ? `Show latest ${recentRunLimit}` : `Show all ${number(history.length)} runs`);
      more.type = 'button';
      more.dataset.syncFocus = 'history-size';
      more.onclick = () => { showAllRuns = !showAllRuns; renderDashboard(); };
      section.append(more);
    }
    section.append(node('p', 'Counts cover whole indexed conversations, not just newly added messages. CPU and RAM reflect the whole Pharos process, including other work.', 'muted'));
    dashboard.append(section);
  }

  function renderDiagnostics(dashboard) {
    const diagnostics = panel('Performance & verification details', 'diagnostics');
    diagnostics.append(node('p', 'Measured cost by workload, across the last 200 runs. Median is the typical run; p95 covers 95% of runs.', 'muted'));
    const costs = table('Measured indexing cost', ['Workload', 'Runs', 'Elapsed median / p95', 'CPU median / p95', 'Last RAM peak', 'Estimated CPU duty']);
    for (const [classification, stats] of Object.entries(status.statistics || {})) {
      const distribution = values => `${seconds(values?.median)} / ${seconds(values?.p95)}`;
      cells(costs.body, [classification.replaceAll('_', ' '), number(stats.samples), distribution(stats.elapsed_seconds), distribution(stats.cpu_seconds), memory(stats.rss_observed_peak_bytes?.last), `${percent(stats.estimated_cpu_duty_percent_one_core)} of one core`]);
    }
    if (costs.body.childElementCount) diagnostics.append(costs.wrapper);
    else diagnostics.append(node('p', 'Performance measurements will appear after the first run.', 'muted'));
    if (status.measurement_limits) diagnostics.append(node('p', status.measurement_limits, 'muted'));
    if (status.coverage?.length) {
      const coverage = table('Verification coverage', ['Source', 'Units', 'Result', 'Last verified']);
      for (const item of status.coverage) cells(coverage.body, [item.source_name, number(item.units), item.outcome, time(item.last_verification)]);
      diagnostics.append(coverage.wrapper, node('p', 'Sampling does not verify the entire library.', 'muted'));
    }
    if (status.deferred?.length) {
      const deferred = table('Pending library-wide work', ['Work', 'Changed', 'Last completed']);
      for (const item of status.deferred) cells(deferred.body, [item.name, time(item.changed_at), time(item.completed_at)]);
      diagnostics.append(node('h3', 'Pending library-wide work'), deferred.wrapper);
    }
    dashboard.append(diagnostics);
  }

  function renderDashboard() {
    const dashboard = document.getElementById('syncDashboard');
    if (!dashboard || !status?.settings || (dashboard.contains(document.activeElement) && document.activeElement.matches('input,select'))) return;
    const focused = dashboard.contains(document.activeElement) ? document.activeElement.dataset.syncFocus : null;
    const scrollPositions = new Map([...dashboard.querySelectorAll('.sync-table-wrap')].map(element => [element.dataset.syncScroll, element.scrollLeft]));
    const operations = document.getElementById('syncOperations');
    const operationFocus = operations?.contains(document.activeElement) ? document.activeElement : null;
    operations?.remove();
    dashboard.replaceChildren();
    const settings = status.settings;
    renderRecentRuns(dashboard);
    if (operations) dashboard.append(operations);
    const preferences = node('section', '', 'sync-panel');
    preferences.append(node('h3', 'Automatic indexing'));
    const controls = node('div', '', 'sync-controls');
    const label = node('label', 'Index changes ');
    const cadence = document.createElement('select');
    cadence.setAttribute('aria-label', 'Automatic indexing schedule');
    for (const [value, text] of [[0, 'Off'], [60, 'Every minute'], [300, 'Every 5 minutes'], [900, 'Every 15 minutes'], [1800, 'Every 30 minutes'], [-1, 'Custom…']]) {
      const option = node('option', text);
      option.value = value;
      cadence.append(option);
    }
    cadence.value = settings.enabled ? ([60, 300, 900, 1800].includes(settings.interval_seconds) ? settings.interval_seconds : -1) : 0;
    const custom = document.createElement('input');
    custom.type = 'number'; custom.min = 1; custom.max = 1440;
    custom.value = settings.interval_seconds / 60;
    custom.setAttribute('aria-label', 'Custom interval in minutes');
    custom.hidden = Number(cadence.value) !== -1;
    const save = async () => {
      const value = Number(cadence.value);
      custom.hidden = value !== -1;
      if (value === -1 && !custom.reportValidity()) return;
      await call('/api/sync/settings', {enabled: value !== 0, interval_seconds: value === -1 ? Number(custom.value) * 60 : value || settings.interval_seconds});
      await refresh();
    };
    cadence.onchange = () => save().catch(error => showError(error.message));
    custom.onchange = cadence.onchange;
    label.append(cadence);
    const check = button('Index changes now', () => call('/api/sync/check', {}));
    check.disabled = Boolean(status.busy);
    controls.append(label, custom, check);
    preferences.append(node('p', 'Automatic indexing updates conversations. Index changes now also refreshes GitHub, identities, Git integration, tool summaries, Human Words, and findings.', 'muted'));
    const schedule = node('p', `${status.pause_reason || 'Ready'} · Next run: `, 'muted');
    schedule.append(settings.enabled ? time(settings.next_at) : document.createTextNode('Off'));
    preferences.append(controls, schedule);
    const sources = node('fieldset', '', 'sync-controls');
    sources.append(node('legend', 'Index changes from these sources on this Mac'));
    const selected = new Set(settings.sources.length ? settings.sources : (status.available_sources || status.sources || []).map(source => source.name));
    for (const source of status.available_sources || status.sources || []) {
      const choice = document.createElement('input');
      choice.type = 'checkbox'; choice.checked = selected.has(source.name);
      choice.setAttribute('aria-label', `Index changes from ${source.name}`);
      choice.onchange = async () => {
        if (choice.checked) selected.add(source.name); else selected.delete(source.name);
        if (!selected.size) { choice.checked = true; selected.add(source.name); showError('Choose at least one source, or turn automatic indexing Off.'); return; }
        try { await call('/api/sync/settings', {sources: [...selected]}); }
        catch (error) { showError(error.message); }
      };
      const sourceLabel = node('label', ''); sourceLabel.append(choice, document.createTextNode(` ${source.name}`)); sources.append(sourceLabel);
    }
    preferences.append(sources, node('p', 'Automatic indexing makes new conversations searchable without capturing them. Manual Index changes also refreshes library-wide analysis. Capture & index also keeps a copy of the source files.', 'muted'));
    dashboard.append(preferences);
    const sourceStatus = node('section', '', 'sync-panel');
    sourceStatus.append(node('h3', 'Source status'));
    const states = table('Source status', ['Source', 'Last checked', 'Last success', 'Last captured', 'Status']);
    for (const source of status.source_states || []) {
      const state = node('div', source.error ? 'Error' : source.pending_preservation ? 'Needs capture' : 'Up to date');
      if (source.pending_preservation) state.append(node('span', 'Indexed, but not captured yet.', 'muted sync-source-note'));
      if (source.error) state.append(node('span', source.error, 'badtext sync-source-note'));
      cells(states.body, [source.source_name, time(source.attempted_at), time(source.succeeded_at), time(source.last_capture_at), state]);
    }
    if (states.body.childElementCount) sourceStatus.append(states.wrapper);
    else sourceStatus.append(node('p', 'Source status will appear after the first run.', 'muted'));
    dashboard.append(sourceStatus);
    const latest = status.history?.[0];
    if (settings.enabled && latest?.seconds > settings.interval_seconds) dashboard.append(node('p', 'Indexing changes takes longer than the selected interval. The next run starts after this one finishes.', 'badtext'));
    renderDiagnostics(dashboard);
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
      const line = node('p', `${source.source_name} `);
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
    for (const element of dashboard.querySelectorAll('.sync-table-wrap')) element.scrollLeft = scrollPositions.get(element.dataset.syncScroll) || 0;
    operationFocus?.focus({preventScroll: true});
    if (focused) [...dashboard.querySelectorAll('[data-sync-focus]')].find(element => element.dataset.syncFocus === focused)?.focus({preventScroll: true});
  }

  async function refresh() {
    if (busy) return;
    busy = true;
    try {
      const activity = await call('/api/activity');
      renderHeader(activity);
      if (document.getElementById('settings')?.classList.contains('active') || !status) {
        status = await call('/api/sync/status');
        renderDashboard();
      }
      // Open mismatches turn the drive chip's dot red; the drive panel lists them.
      const issues = await call('/api/sync/issues');
      const open = issues.issues?.filter(issue => issue.state === 'open') || [];
      if (JSON.stringify(open.map(issue => issue.id)) !== JSON.stringify(openIssues.map(issue => issue.id))) {
        openIssues = open;
        window.pharosLibrary?.renderAlerts?.();
      }
    } catch (error) { if (!status) showError(error.message); }
    finally { busy = false; clearTimeout(timer); timer = setTimeout(refresh, 3000); }
  }
  window.addEventListener('pharos:route', refresh);
  window.pharosSync = {refresh, recovery, isBusy: () => Boolean(activeRun), issues: () => openIssues};
  refresh();
})();
