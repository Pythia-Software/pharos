(() => {
  if (document.getElementById('pharosShareData')) return;
  let status = null;
  let busy = false;
  let timer = null;
  let activeRun = null;
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
  const when = value => value ? new Date(value).toLocaleString() : 'Not yet';
  const seconds = value => `${Number(value || 0).toFixed(3)} s`;
  const memory = value => `${(Number(value || 0) / 1048576).toFixed(1)} MiB`;
  const style = node('style', `#syncDashboard{display:grid;gap:14px}.sync-controls,.sync-actions{display:flex;align-items:center;gap:10px;flex-wrap:wrap}.sync-costs{border-collapse:collapse;width:100%;font-size:var(--fs-sm)}.sync-costs td,.sync-costs th{text-align:left;padding:7px;border-bottom:1px solid var(--line)}.sync-warning{border:1px solid var(--accent);padding:12px;border-radius:6px}.sync-menu{display:grid;gap:8px;margin-top:15px;border-top:1px solid var(--line);padding-top:12px}.sync-dialog{max-width:620px;background:var(--panel);color:var(--ink);border:1px solid var(--line);border-radius:10px;padding:24px}.sync-dialog::backdrop{background:#0008}.sync-phase-list{font-size:var(--fs-sm)}.header-sync[aria-busy=true] svg{animation:sync-spin 1.5s linear infinite}@keyframes sync-spin{to{transform:rotate(360deg)}}`);
  style.textContent += '#syncDashboard input[hidden]{display:none}';
  document.head.append(style);

  function showError(message) {
    const dashboard = document.getElementById('syncDashboard');
    if (dashboard) dashboard.prepend(node('p', message, 'badtext'));
  }

  async function recovery(mode, sources, resume) {
    if (resume) { await call('/api/sync/recovery', {resume}); return; }
    const dialog = node('dialog', '', 'sync-dialog');
    dialog.append(node('h2', mode === 'full' ? 'Full recapture & re-index' : 'Rebuild indexes from retained messages'));
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
      ? 'Freshly copy available source evidence on this Mac, then reparse selected captures from every Mac and rebuild supported indexes. This can take a long time. It is stoppable and resumable. Historical evidence and catalog-only user records are preserved. Other Macs are re-indexed from retained captures, not recaptured remotely.'
      : 'Rebuild search, conversation documents, sessions, usage and tool ledgers from retained catalog messages. No original source is required. This cannot recreate missing transcripts or source-only metadata. Your findings, preferences, identity links and retained history are preserved.'));
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

  function renderMenu() {
    const panel = document.querySelector('.pharos-drive-panel');
    if (!panel || panel.querySelector('.sync-menu')) return;
    const menu = node('section', '', 'sync-menu');
    menu.setAttribute('aria-label', 'Sync and repair');
    menu.append(button('Sync settings & measured cost', () => {
      history.pushState(null, '', '/settings/sync');
      window.dispatchEvent(new PopStateEvent('popstate'));
      document.getElementById('syncSettings')?.scrollIntoView();
    }), button('Full recapture & re-index…', () => recovery('full')), button('Rebuild indexes from retained messages…', () => recovery('retained')));
    panel.append(menu);
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
      action.title = automatic ? `Automatic sync: ${active.phase === 'verifying' ? 'verifying a conversation' : active.phase === 'checking' ? 'checking for changes' : `syncing ${active.conversations} conversations`}` : `${active.kind}: ${active.phase}`;
      action.setAttribute('aria-label', action.title);
    } else {
      action.setAttribute('aria-busy', 'false');
      action.title = 'Sync now';
      action.setAttribute('aria-label', 'Sync now');
      if (wasActive) window.pharosLibrary?.refresh?.();
    }
  }

  function renderDashboard() {
    const dashboard = document.getElementById('syncDashboard');
    if (!dashboard || !status?.settings || (dashboard.contains(document.activeElement) && document.activeElement.matches('input,select'))) return;
    dashboard.replaceChildren();
    const settings = status.settings;
    const controls = node('div', '', 'sync-controls');
    const label = node('label', 'Automatic refresh ');
    const cadence = document.createElement('select');
    cadence.setAttribute('aria-label', 'Automatic refresh cadence');
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
    const check = button('Check now', () => call('/api/sync/check', {}));
    check.disabled = Boolean(status.busy);
    controls.append(label, custom, check);
    dashboard.append(controls, node('p', `${status.pause_reason || 'Ready'} · Next check: ${settings.enabled ? when(settings.next_at) : 'Off'}`, 'muted'));
    dashboard.append(node('p', 'Live refresh makes retained messages searchable without capturing raw evidence. Freshness is the selected interval plus processing. Update library preserves source evidence and reconciles library-wide analysis.', 'muted'));
    const sources = node('fieldset', '', 'sync-controls');
    sources.append(node('legend', 'Sources checked on this Mac'));
    const selected = new Set(settings.sources.length ? settings.sources : (status.available_sources || status.sources || []).map(source => source.name));
    for (const source of status.available_sources || status.sources || []) {
      const choice = document.createElement('input');
      choice.type = 'checkbox'; choice.checked = selected.has(source.name);
      choice.setAttribute('aria-label', `Automatically refresh ${source.name}`);
      choice.onchange = async () => {
        if (choice.checked) selected.add(source.name); else selected.delete(source.name);
        if (!selected.size) { choice.checked = true; selected.add(source.name); showError('Choose at least one source, or turn automatic refresh Off.'); return; }
        try { await call('/api/sync/settings', {sources: [...selected]}); }
        catch (error) { showError(error.message); }
      };
      const sourceLabel = node('label', ''); sourceLabel.append(choice, document.createTextNode(` ${source.name}`)); sources.append(sourceLabel);
    }
    dashboard.append(sources);
    const actions = node('div', '', 'sync-actions');
    actions.append(button('Full recapture & re-index…', () => recovery('full')), button('Rebuild indexes from retained messages…', () => recovery('retained')), button('Verify all retained inputs', () => call('/api/sync/verify', {all: true})));
    dashboard.append(actions);
    for (const job of status.recovery_jobs || []) {
      const line = node('div', `${job.mode} repair: ${job.state} · last progress ${when(job.updated_at)}${job.error ? ` · ${job.error}` : ''}`);
      const resume = button('Resume repair', () => recovery(job.mode, undefined, job.id));
      resume.disabled = Boolean(status.busy);
      line.append(resume); dashboard.append(line);
    }
    for (const source of status.source_states || []) {
      const line = node('div', `${source.source_name}: last check ${when(source.attempted_at)}; success ${when(source.succeeded_at)}; captured ${when(source.last_capture_at)}`);
      if (source.pending_preservation) line.append(node('strong', ' · Indexed after last capture / pending preservation', 'badtext'));
      if (source.error) line.append(node('span', ` · ${source.error}`, 'badtext'));
      line.append(button('Repair this source…', () => recovery('full', [source.source_name])));
      dashboard.append(line);
    }
    const table = node('table', '', 'sync-costs');
    const head = node('tr', '');
    for (const text of ['Workload', 'Samples', 'Elapsed last / median / p95', 'CPU last / median / p95', 'Observed RAM peak', 'Estimated CPU duty']) head.append(node('th', text));
    table.append(head);
    for (const [classification, stats] of Object.entries(status.statistics || {})) {
      const row = node('tr', '');
      const distribution = values => [values.last, values.median, values.p95].map(seconds).join(' / ');
      for (const text of [classification.replaceAll('_', ' '), stats.samples, distribution(stats.elapsed_seconds), distribution(stats.cpu_seconds), memory(stats.rss_observed_peak_bytes.last), `${stats.estimated_cpu_duty_percent_one_core.toFixed(2)}% of one core`]) row.append(node('td', text));
      table.append(row);
    }
    dashboard.append(node('h3', 'Measured refresh cost (last 200 runs)'), table, node('p', status.measurement_limits, 'muted'));
    const latest = status.history?.[0];
    if (latest) {
      dashboard.append(node('p', `Last run: ${seconds(latest.seconds)} including audit; CPU ${seconds(latest.cpu_seconds)} (${latest.cpu_percent_one_core.toFixed(1)}% of one core); RAM baseline ${memory(latest.rss_baseline_bytes)}, observed peak ${memory(latest.rss_observed_peak_bytes)}; allocated ${memory(latest.allocated_bytes)}`));
      const phases = node('ul', '', 'sync-phase-list');
      for (const phase of latest.phases || []) phases.append(node('li', `${phase.name}: ${seconds(phase.seconds)} elapsed, ${seconds(phase.cpu_seconds)} CPU`));
      dashboard.append(phases, node('p', `Verification: ${latest.audit.outcome} · ${latest.audit.discovered_units} discoverable units; ${latest.audit.deferred_units} deferred this run. ${latest.audit.detail || ''}`));
      if (settings.enabled && latest.seconds > settings.interval_seconds) dashboard.append(node('p', 'Refresh takes longer than the selected interval. Missed checks coalesce; the next interval begins after this run finishes.', 'badtext'));
    }
    for (const coverage of status.coverage || []) dashboard.append(node('p', `${coverage.source_name}: ${coverage.units} units ${coverage.outcome}; last verification ${when(coverage.last_verification)}. Sampling does not verify the entire library.`, 'muted'));
    for (const item of status.deferred || []) dashboard.append(node('p', `${item.name}: pending library-wide work since ${when(item.changed_at)}; last completed ${when(item.completed_at)}`, 'muted'));
    for (const issue of status.issues || []) {
      const warning = node('div', '', 'sync-warning');
      warning.append(node('strong', 'Automatic sync verification found a mismatch.'), node('p', `${issue.source_name} · ${issue.unit}`), button('View details / copy diagnostics', async () => {
        const data = await call('/api/sync/issues');
        const details = data.issues.find(item => item.id === issue.id);
        const dialog = node('dialog', '', 'sync-dialog');
        dialog.append(node('h2', 'Sync integrity issue'), node('pre', JSON.stringify(details, null, 2)), button('Copy diagnostics', () => navigator.clipboard.writeText(JSON.stringify(details, null, 2))), button('Close', () => dialog.close()));
        dialog.addEventListener('close', () => dialog.remove()); document.body.append(dialog); dialog.showModal();
      }), button('Full recapture & re-index…', () => recovery('full', [issue.source_name])));
      dashboard.append(warning);
    }
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
      const issues = await call('/api/sync/issues');
      let warning = document.getElementById('syncIntegrityWarning');
      const open = issues.issues?.filter(issue => issue.state === 'open') || [];
      if (open.length && !warning) {
        warning = button('Sync verification mismatch', () => { history.pushState(null, '', '/settings/sync'); window.dispatchEvent(new PopStateEvent('popstate')); });
        warning.id = 'syncIntegrityWarning'; warning.className = 'badtext'; document.getElementById('headerSync')?.closest('.combo-button')?.after(warning);
      }
      if (!open.length) warning?.remove();
      renderMenu();
    } catch (error) { if (!status) showError(error.message); }
    finally { busy = false; clearTimeout(timer); timer = setTimeout(refresh, 3000); }
  }
  document.addEventListener('click', () => queueMicrotask(renderMenu));
  window.addEventListener('pharos:drive-panel', renderMenu);
  window.addEventListener('pharos:route', refresh);
  window.pharosSync = {refresh, recovery, isBusy: () => Boolean(activeRun)};
  refresh();
})();
