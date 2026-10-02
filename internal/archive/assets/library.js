// Pharos library, drive and captures. GET /api/library/status says what holds
// or writes the library; the header shows it and estimated completion times.
// Settings → Sources shows
// captures from other Macs alongside this Mac's sources; Settings →
// Health gains the drive's checks and backups.
(() => {
  'use strict';
  const CSS = `
.pharos-library-controls{min-width:0;font-size:var(--fs-md,13px)}
.combo-button>.pharos-drive{gap:7px;min-width:0;max-width:190px;font-weight:400;line-height:1}
.pharos-drive-name{flex:0 1 auto;min-width:2.5em;font-weight:650;overflow:hidden;text-overflow:ellipsis}
.pharos-drive-alert{flex:0 1 auto;min-width:0;overflow:hidden;text-overflow:ellipsis;color:#f1d690;font-size:var(--fs-xs,12px)}
.pharos-dot{flex:none;display:inline-block;width:8px;height:8px;border-radius:50%;background:var(--sea,#315845)}
.pharos-drive .pharos-dot{background:#8cc6a0}
.pharos-dot.busy{background:var(--mast-brass,#d4a857);box-shadow:0 0 0 3px color-mix(in srgb,var(--mast-brass,#d4a857) 30%,transparent)}
.pharos-drive .pharos-dot.busy{animation:pharos-drive-pulse 1.6s ease-in-out infinite}
@keyframes pharos-drive-pulse{50%{opacity:.55;box-shadow:0 0 0 6px color-mix(in srgb,var(--mast-brass,#d4a857) 10%,transparent)}}
@media(prefers-reduced-motion:reduce){.pharos-drive .pharos-dot.busy{animation:none}}
.pharos-dot.warn{background:var(--warn,#98601d)}.pharos-dot.bad{background:var(--bad,#9c3d36)}.pharos-dot.idle{background:var(--muted,#756c5f)}
.pharos-drive-panel{position:fixed;z-index:8000;width:min(430px,calc(100vw - 24px));max-height:calc(100vh - 100px);overflow:auto;padding:16px 18px;background:var(--panel,#fff);color:var(--ink,#222);border:1px solid var(--line,#ccc);border-radius:14px;box-shadow:0 18px 50px #0005;font-size:13px}
.pharos-drive-panel h2{margin:0;font:700 20px/1.2 var(--serif,serif)}
.pharos-drive-panel h3{margin:14px 0 6px;font-size:12px;letter-spacing:.06em;text-transform:uppercase;color:var(--muted,#666)}
.pharos-drive-panel p{margin:8px 0}
.pharos-running-head{display:flex;justify-content:space-between;align-items:center;gap:12px}
.pharos-running-head>div{min-width:0}
.pharos-running-head>.pharos-button{flex:none}
.pharos-drive-space{display:grid;grid-template-columns:1fr auto;gap:5px 12px;margin:12px 0 2px;padding:10px 0;border-top:1px solid var(--line,#ccc);border-bottom:1px solid var(--line,#ccc)}
.pharos-drive-space dt{color:var(--muted,#666)}
.pharos-drive-space dd{margin:0;font-variant-numeric:tabular-nums;font-weight:650}
.pharos-sub{color:var(--muted,#666);overflow-wrap:anywhere}
.pharos-activity{padding:8px 0;border-bottom:1px solid var(--line,#ccc)}
.pharos-activity:last-child{border-bottom:0}
.pharos-activity-row{display:flex;justify-content:space-between;align-items:flex-start;gap:12px}
.pharos-activity-row>.pharos-button{flex:none}
.pharos-activity-info{flex:1;min-width:0}
.pharos-activity-timing{flex:none;text-align:right;font-size:12px;line-height:1.5;font-variant-numeric:tabular-nums}
.pharos-activity-head{display:flex;align-items:center;gap:8px;font-weight:650}
.pharos-activity-head .pharos-button{margin-left:auto;font-weight:400}
.pharos-activity .pharos-sub{margin-top:2px}
.pharos-last-index{padding-top:8px;border-top:1px solid var(--line,#ccc)}
.pharos-last-index h3{margin-top:4px}
.pharos-last-index .pharos-sub{margin-top:3px}
.pharos-bar{height:6px;margin:7px 0 4px;overflow:hidden;border-radius:6px;background:var(--line,#ccc)}
.pharos-bar>span{display:block;height:100%;min-width:6px;background:var(--accent,#315845);transition:width .25s}
.pharos-bar.indeterminate>span{width:35%;animation:pharos-slide 1.6s ease-in-out infinite}
@keyframes pharos-slide{from{margin-left:-35%}to{margin-left:100%}}
@media(prefers-reduced-motion:reduce){.pharos-bar.indeterminate>span{animation:none;width:100%;opacity:.5}}
.pharos-row{display:flex;flex-wrap:wrap;align-items:center;gap:8px;margin-top:12px}
.pharos-row .pharos-note{flex:1 1 100%}
.pharos-button{border:1px solid var(--line,#ccc);background:var(--panel,#fff);color:var(--ink,#222)}
.pharos-button.primary{background:var(--accent,#315845);border-color:var(--accent,#315845);color:var(--panel,#fff)}
.pharos-button:disabled{opacity:.55;cursor:default}
.pharos-code{display:flex;align-items:center;gap:8px;margin:6px 0}
.pharos-code code{flex:1;min-width:0;padding:6px 9px;border-radius:7px;background:color-mix(in srgb,var(--bg,#eee) 72%,transparent);font:12px/1.4 ui-monospace,SFMono-Regular,monospace;overflow-wrap:anywhere}
.pharos-code button{flex:none;padding:5px 9px;font-size:12px}
.pharos-error{color:var(--bad,#9c3d36)}.pharos-warn{color:var(--warn,#98601d)}.pharos-ok{color:var(--sea,#315845)}
.pharos-section{margin-top:26px}
.pharos-section-head{display:flex;flex-wrap:wrap;justify-content:space-between;align-items:end;gap:10px 20px;margin-bottom:12px}
.pharos-section-head h3{margin:0 0 4px}
.pharos-section-head p{margin:0;max-width:720px}
.pharos-health-actions{display:flex;align-items:center;flex-wrap:wrap;gap:9px}.pharos-health-attention{display:flex;align-items:center;flex-wrap:wrap;gap:6px}.pharos-health-attention-label{color:var(--warn,#98601d)}.pharos-health-attention .pharos-chip{background:var(--panel,#fff)}.pharos-health-details[hidden]{display:none}
.source-host{display:block;margin:0 0 7px;color:var(--muted,#666);font-size:12px}
.source-card.remote{background:color-mix(in srgb,var(--panel,#fff) 58%,var(--bg,#eee));border-style:dashed;box-shadow:none}
.source-card.remote .source-card-head h3{color:color-mix(in srgb,var(--ink,#222) 78%,var(--muted,#666))}
.pharos-chip{border:1px solid var(--line,#ccc);border-radius:20px;padding:1px 8px;font-size:12px;color:var(--muted,#666)}
.pharos-chip.warn{color:var(--warn,#98601d);border-color:color-mix(in srgb,var(--warn,#98601d) 50%,var(--line,#ccc))}
.pharos-run-head{display:flex;align-items:center;justify-content:space-between;gap:8px}
.pharos-run{margin:0 0 12px;padding:10px 14px;border:1px dashed var(--line,#ccc);border-radius:11px}
.pharos-card-capture{margin-top:9px;font-size:12px}
.pharos-checks{display:grid;gap:0;background:var(--panel,#fff);border:1px solid var(--line,#ccc);border-radius:14px;padding:4px 18px}
.pharos-check{display:grid;grid-template-columns:18px 1fr;gap:2px 8px;padding:12px 0;border-bottom:1px solid var(--line,#ccc)}
.pharos-check:last-child{border-bottom:0}
.pharos-check>.pharos-dot{margin-top:6px}
.pharos-check-title{font-weight:650}
.pharos-check>div{grid-column:2}
.pharos-check>div{font-size:13px}
.pharos-backup{background:var(--panel,#fff);border:1px solid var(--line,#ccc);border-radius:14px;padding:16px 18px;margin-top:14px}
.pharos-backup h3{margin:0 0 8px}
.pharos-backup form{display:flex;flex-wrap:wrap;gap:8px;align-items:center;margin-top:10px}
.pharos-backup label{color:var(--muted,#666)}
.pharos-backup input{flex:1 1 320px;min-width:0;padding:7px 10px;border:1px solid var(--line,#ccc);border-radius:8px;background:var(--bg,#fff);color:var(--ink,#222);font:12px ui-monospace,SFMono-Regular,monospace}
.pharos-backup ul{margin:6px 0;padding-left:18px}`;

  // ---- helpers
  const node = (tag, cls, text) => {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined && text !== null) n.textContent = text;
    return n;
  };
  const button = (text, cls = '', onclick = null) => {
    const b = node('button', 'pharos-button ' + cls, text);
    b.type = 'button';
    if (onclick) b.onclick = onclick;
    return b;
  };
  const call = async (path, options = {}) => {
    const response = await fetch(path, {...options, headers: {'Content-Type': 'application/json'}});
    const body = await response.json().catch(() => ({}));
    if (!response.ok) {
      const error = new Error(body.error || response.statusText);
      error.status = response.status;
      throw error;
    }
    return body;
  };
  const post = (path, body = {}) => call(path, {method: 'POST', body: JSON.stringify(body)});
  const toast = message => { if (typeof window.feedbackToast === 'function') window.feedbackToast(message); };
  const plural = (count, unit) => `${Number(count || 0).toLocaleString()} ${unit}${count === 1 ? '' : 's'}`;
  const size = value => {
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let amount = Math.max(0, Number(value) || 0), power = 0;
    while (amount >= 1000 && power < units.length - 1) { amount /= 1000; power++; }
    return `${amount.toFixed(amount < 10 && power ? 1 : 0)} ${units[power]}`;
  };
  const ago = value => {
    if (!value) return 'never';
    const seconds = Math.max(0, (Date.now() - new Date(value).getTime()) / 1000);
    if (!Number.isFinite(seconds)) return 'unknown';
    if (seconds < 60) return 'just now';
    if (seconds < 3600) return `${Math.floor(seconds / 60)} min ago`;
    if (seconds < 172800) return `${Math.floor(seconds / 3600)} h ago`;
    return `${Math.floor(seconds / 86400)} days ago`;
  };
  const when = value => {
    const span = node('span', '', ago(value));
    if (value) span.title = new Date(value).toLocaleString();
    return span;
  };
  const fullTime = value => {
    const span = when(value);
    if (value) span.append(` · ${new Date(value).toLocaleString()}`);
    return span;
  };
  const progressBar = value => {
    const bar = node('div', 'pharos-bar' + (value == null ? ' indeterminate' : '')), fill = node('span');
    if (value != null) fill.style.width = `${Math.round(value * 100)}%`;
    bar.append(fill);
    return bar;
  };
  const completionEstimate = (activities, currentStep = false) => {
    const timing = node('div', 'pharos-sub pharos-activity-timing');
    const starts = activities.map(activity => new Date(activity.started_at).getTime()).filter(Number.isFinite);
    const elapsed = starts.length ? Math.max(0, Math.floor((Date.now() - Math.min(...starts)) / 60_000)) : null;
    timing.append(node('div', '', elapsed == null ? 'Started unknown' : `Started ${elapsed}m ago`));
    const estimates = activities.map(activity => new Date(activity.estimated_completion_at).getTime());
    if (!estimates.length || estimates.some(value => !Number.isFinite(value))) {
      timing.append(node('div', '', 'Estimated complete unknown'));
      timing.title = 'Completion estimate unavailable — not enough past data.';
      return timing;
    }
    const completion = Math.max(...estimates), remaining = completion - Date.now();
    timing.append(node('div', '', remaining <= 0 ? 'Estimated complete overdue' : `Estimated complete in ${Math.ceil(remaining / 60_000)}m`));
    timing.title = remaining <= 0 ? 'Taking longer than past runs — still running.'
      : `${currentStep ? 'Current step estimate. ' : ''}Based on the median duration of the last 10 successful runs on this Mac.`;
    return timing;
  };
  const copy = async text => {
    try {
      if (typeof window.pharosCopyText === 'function') await window.pharosCopyText(text);
      else await navigator.clipboard.writeText(text);
      toast('Copied');
    } catch { toast('Could not copy'); }
  };
  const codeLine = text => {
    const line = node('div', 'pharos-code');
    line.append(node('code', '', text), button('Copy', '', () => copy(text)));
    return line;
  };
  const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));

  // ---- library status (header chip and panel)
  let status = null, statusError = null, statusTimer = null, statusRequest = null;
  let recentIndex = null, indexStatusRequest = null, upgradeStatus = null;
  const listeners = new Set();
  window.addEventListener('pharos:upgrade-status', event => {
    const runningChanged = Boolean(upgradeStatus?.running) !== Boolean(event.detail.running);
    upgradeStatus = event.detail;
    renderHeaderAction();
    renderChip();
    renderPanel();
    if (runningChanged) refreshStatus();
  });
  const driveName = () => status?.drive?.name || 'the drive';
  // A run the user stopped is not a failure: what it wrote is kept and the next
  // run resumes it. Only the "interrupted:" error of the source it cut short is
  // the stop itself; any other error in the run is still a failure.
  const indexStopped = run => run?.stop_requested === true;
  const isStopError = (run, error) => indexStopped(run) && String(error).startsWith('interrupted:');
  const indexErrors = run => {
    const errors = (run?.results || []).filter(result => result.error && !isStopError(run, result.error)).map(result => `${result.source}: ${result.error}`);
    if (run?.error && !isStopError(run, run.error) && !errors.some(error => error.includes(String(run.error)))) errors.unshift(String(run.error));
    return errors;
  };
  const indexFailed = run => run && ((run.state !== 'complete' && !indexStopped(run)) || indexErrors(run).length > 0);

  // Stops the running index (or source sync) between records, or the Library
  // view refresh; it says when it has.
  const LIBRARY_REFRESH_STOP = '/api/library/refresh/stop';
  async function stopWork(path = '/api/index/cancel') {
    try {
      const {stopped} = await post(path);
      if (!stopped) toast(path === LIBRARY_REFRESH_STOP ? 'The Library view refresh is not running.' : 'No index is running.');
    } catch (error) {
      toast(error.message);
    }
    await refreshStatus();
    if (document.getElementById('sourceGrid')) loadHosts().catch(() => {});
  }
  const stopButton = (stopping, path) => {
    const stop = button(stopping ? 'Stopping…' : 'Stop', '', () => stopWork(path));
    stop.disabled = stopping;
    stop.title = path === LIBRARY_REFRESH_STOP
      ? 'Stops refreshing the Library view. Workspaces not yet refreshed are worked out as the Library shows them, which can be slower, until you resume it or update the library.'
      : 'Stops after the record being indexed. Everything already indexed is kept, and the next index picks up where this one stopped.';
    return stop;
  };
  async function resumeLibraryRefresh() {
    try { await post('/api/library/refresh/resume'); }
    catch (error) { toast(error.message); }
    await refreshStatus();
  }

  function refreshIndexStatus() {
    if (!indexStatusRequest) {
      indexStatusRequest = call('/api/activity').then(value => {
        recentIndex = (value.runs || []).find(run => run.state !== 'running') || null;
      }, () => { recentIndex = null; }).finally(() => { indexStatusRequest = null; });
    }
    return indexStatusRequest;
  }

  async function refreshStatus() {
    clearTimeout(statusTimer);
    if (!statusRequest) {
      statusRequest = call('/api/library/status').then(value => { status = value; statusError = null; }, error => { statusError = error; })
        .finally(() => { statusRequest = null; });
    }
    await Promise.all([statusRequest, refreshIndexStatus()]);
    renderChip();
    renderPanel();
    listeners.forEach(listener => listener(status));
    const busy = status && !status.idle;
    if (!document.hidden) statusTimer = setTimeout(refreshStatus, busy || statusError ? 2000 : 10000);
    return status;
  }
  document.addEventListener('visibilitychange', () => { if (!document.hidden) refreshStatus(); });

  let controls = null, chip = null, headerSync = null, headerCaptureIndexRunning = false, panel = null, panelMode = 'drive';
  let driveSpace = null, driveSpaceRequest = null, driveSpaceError = false;
  // The drive chip and Sync now share one combo button (ui.py's
  // .combo-button) at the start of the header icons. Stopping a sync, and the
  // full Update library, are in the drive panel.
  function ensureControls() {
    if (controls?.isConnected) return controls;
    const icons = document.querySelector('body>header .header-icons');
    if (!icons) return null;
    controls = node('div', 'combo-button pharos-library-controls');
    controls.setAttribute('role', 'group');
    controls.setAttribute('aria-label', 'Library');
    icons.prepend(controls);
    return controls;
  }

  function ensureChip() {
    if (chip?.isConnected) return chip;
    const group = ensureControls();
    if (!group) return null;
    chip = node('button', 'pharos-drive');
    chip.id = 'pharosDrive';
    chip.type = 'button';
    chip.setAttribute('aria-haspopup', 'dialog');
    chip.setAttribute('aria-expanded', 'false');
    chip.onclick = () => (panel && panelMode === 'drive' ? closePanel() : openPanel('drive'));
    group.prepend(chip);
    return chip;
  }

  function ensureHeaderAction() {
    const drive = ensureChip();
    if (!drive) return null;
    if (headerSync?.isConnected) return headerSync;
    headerSync = node('button', 'combo-icon header-sync');
    headerSync.id = 'headerSync';
    headerSync.type = 'button';
    headerSync.title = 'Sync now';
    headerSync.setAttribute('aria-label', 'Sync now');
    const icon = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    icon.setAttribute('class', 'app-icon');
    icon.setAttribute('viewBox', '0 0 24 24');
    icon.setAttribute('aria-hidden', 'true');
    use.setAttribute('href', '#ph-icon-refresh');
    icon.append(use);
    headerSync.append(icon);
    headerSync.onclick = syncNow;
    drive.after(headerSync);
    return headerSync;
  }

  function renderHeaderAction() {
    const action = ensureHeaderAction();
    if (!action) return;
    const active = status?.activities?.some(activity => ['capture', 'capture-other', 'sync', 'index'].includes(activity.kind));
    const incremental = Boolean(window.pharosSync?.isBusy());
    action.disabled = headerCaptureIndexRunning || Boolean(hostsBusy) || Boolean(active) || incremental || Boolean(upgradeStatus?.running);
    action.setAttribute('aria-busy', String(headerCaptureIndexRunning || Boolean(active) || incremental));
  }

  let updateProgressFloor = 0;
  function libraryUpdate(activities) {
    const recent = recentIndex?.state === 'complete' && Date.now() - new Date(recentIndex.completed_at).getTime() < 300_000;
    const indexFollowUp = recent && activities.find(activity => activity.kind === 'git'
      && activity.detail?.includes('after the last index')
      && new Date(activity.started_at).getTime() >= new Date(recentIndex.started_at).getTime());
    const updating = activities.filter(activity => ['capture', 'sync', 'index'].includes(activity.kind)
      || activity.label === 'Upgrading the library'
      || (upgradeStatus?.running && upgradeStatus.step === 'tools' && activity.label === 'Building the Tools ledger')
      || (indexFollowUp && (activity === indexFollowUp || activity.label === 'Updating findings')));
    if (!updating.length) {
      updateProgressFloor = 0;
      return null;
    }
    const capturing = updating.find(activity => activity.kind === 'capture');
    const indexing = updating.find(activity => activity.kind === 'sync' || activity.kind === 'index');
    const upgrade = upgradeStatus?.running;
    const step = upgradeStatus?.steps?.find(item => item.id === upgradeStatus.step);
    let progress = null;
    if (capturing?.progress != null) progress = capturing.progress * 0.15;
    else if (indexing?.progress != null) progress = 0.15 + indexing.progress * (upgradeStatus?.needed || upgrade ? 0.55 : 0.7);
    else if (upgrade && upgradeStatus.overall_progress != null) progress = recent
      ? 0.75 + upgradeStatus.overall_progress * 0.24 : upgradeStatus.overall_progress;
    else if (!indexing && recent) progress = upgradeStatus?.needed ? 0.7 : 0.85;
    if (progress != null) {
      updateProgressFloor = Math.max(updateProgressFloor, progress);
      progress = updateProgressFloor;
    }
    const label = capturing ? 'Capturing sources' : indexing ? 'Indexing conversations' : upgrade ? (step?.label || 'Upgrading the library') : 'Finishing the library update';
    return {updating, indexing, label, progress};
  }

  function activitySummary(activity) {
    const verb = {sync: 'Indexing', index: 'Indexing', capture: 'Capturing', 'capture-other': 'Capturing', backup: 'Backing up'}[activity.kind] || activity.label;
    return activity.progress ? `${verb} ${Math.round(activity.progress * 100)}%` : verb;
  }

  // The chip is just the drive's name and a dot: green when nothing is
  // running, so ejecting stops nothing; brass and pulsing while work holds
  // the library, which an eject would stop first. The panel has the details.
  const DRIVE_PULSE_MS = 1600;
  function renderChip() {
    const target = ensureChip();
    if (!target) return;
    renderHeaderAction();
    const dot = node('span', 'pharos-dot'), name = node('span', 'pharos-drive-name');
    let state;
    if (!status) {
      dot.classList.add(statusError?.status === 503 ? 'warn' : 'idle');
      name.textContent = 'Library';
      state = statusError?.status === 503 ? 'Stopping' : 'Status unavailable';
    } else {
      name.textContent = status.drive?.ejectable ? status.drive.name : status.portable ? 'Library' : 'This Mac';
      const activities = status.activities || [];
      if (activities.length) {
        dot.classList.add('busy');
        // The dot is rebuilt on every status refresh; entering mid-cycle keeps the pulse from restarting.
        dot.style.animationDelay = `-${Date.now() % DRIVE_PULSE_MS}ms`;
      } else if (indexFailed(recentIndex)) dot.classList.add('warn');
      const update = libraryUpdate(activities);
      state = activities.length ? [update && `${update.label}${update.progress == null ? '' : ` ${Math.round(update.progress * 100)}%`}`,
        ...activities.filter(activity => !update?.updating.includes(activity)).map(activitySummary)].filter(Boolean).join(', ')
        : headerCaptureIndexRunning && upgradeStatus?.needed ? 'Preparing library upgrade'
          : upgradeStatus?.needed ? 'Library update available' : indexFailed(recentIndex) ? `Last index ${recentIndex.state === 'interrupted' ? 'interrupted' : 'failed'}` : 'Nothing running';
    }
    target.replaceChildren(dot, name);
    if (status && !(status.activities || []).length && indexFailed(recentIndex)) {
      target.append(node('span', 'pharos-drive-alert', recentIndex.state === 'interrupted' ? 'Index stopped' : 'Index failed'));
    }
    target.dataset.state = !status ? 'unavailable' : status.idle ? 'idle' : 'busy';
    target.title = `${name.textContent}: ${state}`;
    target.setAttribute('aria-label', `Library drive: ${target.title}`);
  }

  function placePanel() {
    const anchor = chip;
    if (!panel || !anchor) return;
    const box = anchor.getBoundingClientRect(), header = document.querySelector('body>header')?.getBoundingClientRect();
    panel.style.top = `${Math.round((header?.bottom ?? box.bottom) + 8)}px`;
    panel.style.left = `${Math.round(Math.max(12, Math.min(box.left, window.innerWidth - panel.offsetWidth - 12)))}px`;
  }

  function refreshDriveSpace() {
    if (driveSpaceRequest) return driveSpaceRequest;
    driveSpaceRequest = call('/api/health/drive').then(value => { driveSpace = value; driveSpaceError = false; renderPanel(); }, () => { driveSpace = null; driveSpaceError = true; renderPanel(); })
      .finally(() => { driveSpaceRequest = null; if (panel && panelMode === 'drive' && driveSpace?.status === 'checking') setTimeout(() => { if (panel && panelMode === 'drive') refreshDriveSpace(); }, 1000); });
    return driveSpaceRequest;
  }

  function openPanel(mode = 'drive') {
    if (!ensureChip()) return;
    if (panel) closePanel();
    panelMode = mode;
    panel = node('section', 'pharos-drive-panel');
    panel.id = 'pharosDrivePanel';
    panel.setAttribute('role', 'dialog');
    panel.setAttribute('aria-label', mode === 'activity' ? 'Running activity' : 'Library drive');
    document.body.append(panel);
    chip?.setAttribute('aria-expanded', 'true');
    renderPanel();
    refreshStatus();
    if (mode === 'drive') refreshDriveSpace();
  }

  function closePanel() {
    panel?.remove();
    panel = null;
    chip?.setAttribute('aria-expanded', 'false');
  }
  document.addEventListener('mousedown', event => {
    if (panel && !panel.contains(event.target) && !chip?.contains(event.target)) closePanel();
  });
  document.addEventListener('keydown', event => {
    if (panel && event.key === 'Escape') { closePanel(); chip?.focus(); }
  });
  window.addEventListener('resize', placePanel);

  function renderPanel() {
    if (!panel) return;
    panel.replaceChildren();
    if (!status) {
      panel.append(node('h2', '', panelMode === 'activity' ? 'Running activity' : 'Library'), node('p', 'pharos-error', statusError?.status === 503 ? 'Pharos is stopping; it has released or is releasing the library.' : `Could not read the library's status${statusError ? `: ${statusError.message}` : ''}.`));
      placePanel();
      return;
    }
    const drive = status.drive || {};
    if (panelMode === 'activity') {
      panel.append(node('h2', '', 'Running activity'));
    } else {
      panel.append(node('h2', '', drive.ejectable ? drive.name : status.portable ? 'Library' : 'This Mac'));
      const where = [{external: 'External drive', 'disk-image': 'Disk image', internal: "This Mac's internal disk"}[drive.location] || 'Drive', status.library_dir].filter(Boolean).join(' · ');
      panel.append(node('div', 'pharos-sub', where));
      const space = node('dl', 'pharos-drive-space');
      const unknown = driveSpaceError || (driveSpace && driveSpace.status !== 'checking') ? 'Unavailable' : 'Checking…';
      space.append(node('dt', '', 'Free space'), node('dd', '', driveSpace?.free_bytes == null ? unknown : size(driveSpace.free_bytes)),
        node('dt', '', 'Pharos size'), node('dd', '', driveSpace?.sizes?.library_bytes == null ? unknown : size(driveSpace.sizes.library_bytes)));
      panel.append(space);
    }

    // Update library sits to the right of Running now (and Nothing is running).
    const running = node('div', 'pharos-running-head'), runningText = node('div');
    runningText.append(node('h3', '', 'Running now'));
    running.append(runningText);
    panel.append(running);
    const activities = status.activities || [];
    const update = libraryUpdate(activities);
    if (!activities.length) runningText.append(node('p', 'pharos-sub', headerCaptureIndexRunning && upgradeStatus?.needed
      ? 'Preparing the library upgrade…' : upgradeStatus?.needed ? 'A library update is available. Use Update library to run it.' : 'Nothing is running.'));
    if (panelMode === 'drive') {
      const updateLibrary = button('Update library', '', captureAndIndex);
      updateLibrary.disabled = headerSync?.disabled;
      updateLibrary.title = "Captures this Mac's sources, indexes captures from every Mac, and runs any pending library upgrade. Sync now in the header only indexes what changed.";
      running.append(updateLibrary);
    }
    if (update) {
      const item = node('div', 'pharos-activity'), row = node('div', 'pharos-activity-row'), info = node('div', 'pharos-activity-info'), head = node('div', 'pharos-activity-head');
      head.append(node('span', '', 'Updating library'));
      if (update.indexing?.stoppable) head.append(stopButton(update.indexing.stopping, update.indexing.stop_path));
      info.append(head, node('div', 'pharos-sub', update.label));
      row.append(info, completionEstimate(update.updating, true));
      item.append(row, progressBar(update.progress));
      panel.append(item);
    }
    activities.filter(activity => !update?.updating.includes(activity)).forEach(activity => {
      const item = node('div', 'pharos-activity'), row = node('div', 'pharos-activity-row'), info = node('div', 'pharos-activity-info'), head = node('div', 'pharos-activity-head');
      head.append(node('span', 'pharos-dot busy'), node('span', '', activity.label));
      // The Library view refresh's Stop sits past its timing, like Resume once stopped.
      const stop = activity.stoppable && stopButton(activity.stopping, activity.stop_path);
      if (stop && activity.stop_path !== LIBRARY_REFRESH_STOP) head.append(stop);
      info.append(head);
      if (activity.detail) info.append(node('div', 'pharos-sub', activity.detail));
      row.append(info, completionEstimate([activity]));
      if (stop && activity.stop_path === LIBRARY_REFRESH_STOP) row.append(stop);
      item.append(row);
      if (activity.kind !== 'capture-other' && (activity.kind !== 'maintenance' || activity.progress != null)) item.append(progressBar(activity.progress));
      panel.append(item);
    });
    if (status.library_refresh?.stopped) {
      const item = node('div', 'pharos-activity pharos-library-refresh-stopped'), head = node('div', 'pharos-activity-head');
      head.append(node('span', 'pharos-dot idle'), node('span', '', 'Library view refresh stopped'), button('Resume', '', resumeLibraryRefresh));
      item.append(head, node('div', 'pharos-sub', `${status.library_refresh.pending ? `${plural(status.library_refresh.pending, 'workspace')} left to refresh; the Library works them out as it shows them, which can be slower. ` : ''}Update library or restarting Pharos also resumes it.`));
      panel.append(item);
    }
    if (upgradeStatus?.needed || upgradeStatus?.running) {
      panel.append(button('Update details', '', () => window.pharosUpgrade?.open()));
    }

    if (recentIndex && !update) {
      const last = node('div', 'pharos-last-index');
      last.append(node('h3', '', 'Last index this session'));
      const failed = indexFailed(recentIndex);
      const label = indexStopped(recentIndex) ? 'Stopped' : recentIndex.state === 'interrupted' ? 'Interrupted' : failed ? 'Failed' : 'Complete';
      last.append(node('div', failed ? 'pharos-error' : '', `${label} · ${ago(recentIndex.completed_at)} · ${recentIndex.completed_sources} of ${recentIndex.total_sources} sources`));
      last.append(node('div', 'pharos-sub', `${plural(recentIndex.workspaces, 'workspace')} updated · ${plural(recentIndex.conversations, 'conversation')}${indexStopped(recentIndex) ? '; the next index resumes' : ''}`));
      const errors = indexErrors(recentIndex);
      if (errors.length) last.append(node('div', 'pharos-error', errors.join(' · ')));
      panel.append(last);
    }

    // The panel is rebuilt on every status refresh; extensions (sync.js) re-attach their sections synchronously so they never flash away.
    window.dispatchEvent(new CustomEvent('pharos:drive-panel'));
    placePanel();
  }

  // ---- runs, shared with onboarding. An index is watched through
  // /api/activity, which lists runs only: /api/index also reads every
  // capture's manifest.
  async function watch(path, runID, onUpdate, every = 700) {
    for (let failures = 0; ;) {
      let body;
      try { body = await call(path); failures = 0; } catch (error) {
        if (error.status === 503) throw new Error('Pharos stopped: the library was released or its drive went away. The next run resumes where this one stopped.');
        if (++failures > 20) throw error;
        await sleep(every * 2);
        continue;
      }
      const run = (body.runs || []).find(item => item.id === runID) || null;
      onUpdate?.(run, body);
      if (!run || run.state !== 'running') return run;
      await sleep(every);
    }
  }
  const runs = {
    async capture(sources, onUpdate) {
      const started = await post('/api/capture', sources ? {sources} : {});
      refreshStatus();
      onUpdate?.(started.run);
      const run = await watch('/api/capture', started.run.id, onUpdate);
      refreshStatus();
      return run;
    },
    async index(body, onUpdate) {
      const started = await post('/api/index', body);
      refreshStatus();
      onUpdate?.(started.run);
      const run = await watch('/api/activity', started.run.id, onUpdate);
      refreshStatus();
      return run;
    },
  };

  // ---- Settings → Sources: this Mac's sources and captures from other Macs
  let hostsData = null, hostsBusy = null, hostsError = null;

  async function loadHosts() {
    const [sources, index, capture] = await Promise.all([call('/api/sources'), call('/api/index'), call('/api/capture')]);
    hostsData = {sources, index, capture};
    renderHosts();
    annotateCards();
    const running = index.active || index.sync_active || capture.active;
    return running;
  }

  function renderHosts() {
    const grid = document.getElementById('sourceGrid');
    if (!grid || !hostsData) return;
    const {sources, index, capture} = hostsData;
    const captured = index.hosts || [];
    const busy = headerCaptureIndexRunning || Boolean(hostsBusy) || index.active || index.sync_active || capture.active;
    const actions = document.getElementById('sourceRunActions');
    const captureButton = button(capture.active ? 'Capturing…' : 'Capture this Mac', '', () => startCapture());
    captureButton.id = 'pharosCaptureHost';
    captureButton.disabled = busy || !sources.enabled;
    const needing = captured.filter(host => host.sources.some(source => source.needs_index));
    const indexAll = button('Index captured files', 'primary', () => startIndex({all_hosts: true, only_needed: true}, 'captured files'));
    indexAll.id = 'pharosIndexAll';
    indexAll.disabled = busy || !needing.length;
    indexAll.title = capture.active ? 'Capture is running; index after it finishes' : index.sync_active ? 'Source indexing is running' : needing.length ? `Index captures from ${needing.map(host => host.label).join(', ')}; unchanged files and sessions are skipped` : 'Everything captured is indexed';
    actions?.replaceChildren(captureButton, indexAll);
    renderHeaderAction();
    const activity = document.getElementById('sourceActivity');
    activity?.replaceChildren();
    const line = runLine(capture, index);
    if (line) activity?.append(line);
    if (hostsError) activity?.append(node('p', 'pharos-error', hostsError));
    if (index.error && !captured.length) activity?.append(node('p', 'pharos-sub', `No captures yet (${index.error}).`));

    grid.querySelectorAll('.source-card.remote').forEach(card => card.remove());
    for (const host of captured.filter(host => !host.current)) {
      for (const source of host.sources || []) {
        const card = node('article', 'source-card remote'), head = node('div', 'source-card-head'), title = node('div');
        card.dataset.host = host.id;
        card.dataset.source = source.name;
        title.append(node('h3', '', source.name), node('span', 'source-host', [host.label || host.id, host.user].filter(Boolean).join(' · ')));
        head.append(title);
        const path = node('div', 'source-path', source.path || 'No path recorded'), facts = node('dl', 'source-facts');
        for (const [label, value] of [['Last indexed', source.indexed_at], ['Last index attempt', source.last_attempt_at]]) {
          const item = node('dd');
          item.append(fullTime(value));
          facts.append(node('dt', '', label), item);
        }
        facts.append(node('dt', '', 'Coverage'), node('dd', '', source.coverage || 'not-indexed'),
          node('dt', '', 'Account'), node('dd', '', source.account || ''));
        const captured = node('div', 'pharos-card-capture');
        captured.append(node('span', 'pharos-sub', 'Captured '), when(source.captured_at));
        if (source.captured_at && !source.capture_finished) captured.append(node('span', 'pharos-warn', ' · interrupted'));
        captured.append(node('span', source.needs_index ? 'pharos-warn' : 'pharos-sub', source.needs_index ? ` · needs index (${source.index_reason || 'new data'})` : ' · indexed'));
        card.append(head, path, facts, captured);
        if (source.error) card.append(node('div', 'sync-note badtext', source.error));
        grid.append(card);
      }
    }
  }

  // Runs report progress by source; a lone source shows as work in progress.
  const runProgress = run => (run.total_sources > 1 ? run.completed_sources / run.total_sources : null);
  // An index counts the conversations it has to parse before it starts, and is
  // measured against them (a source it cannot count is one step); sources are
  // only its fallback.
  const indexProgress = run => (run.phase === 'counting' ? null : run.progress ?? runProgress(run));
  const indexDetail = run => {
    const current = run.current_source || 'starting';
    if (run.phase === 'counting') return `${current} · counting the conversations to index`;
    if (run.total_conversations > 0) return `${current} · ${Number(run.done_conversations).toLocaleString()} of ${plural(run.total_conversations, 'conversation')} · ${run.completed_sources} of ${run.total_sources} sources`;
    return `${current} · ${run.completed_sources} of ${run.total_sources} sources · ${plural(run.conversations, 'conversation')} written`;
  };

  function indexRunSummary(run) {
    const results = run?.results || [];
    const parsed = results.reduce((count, result) => count + (result.parsed || 0), 0);
    const unchanged = results.reduce((count, result) => count + (result.unchanged || 0), 0);
    const current = results.filter(result => result.skipped_unchanged).length;
    if (!run?.conversations && !parsed && !unchanged && (current || !results.length)) return 'Already up to date; no conversations rewritten';
    const parts = [plural(run?.conversations || 0, 'conversation') + ' written'];
    if (parsed) parts.push(`${plural(parsed, 'source part')} parsed`);
    if (unchanged) parts.push(`${unchanged.toLocaleString()} unchanged files/sessions skipped`);
    if (current) parts.push(`${plural(current, 'source')} already current`);
    return parts.join(' · ');
  }

  function runLine(capture, index) {
    const box = node('div', 'pharos-run');
    const captureRun = capture.run, indexRun = index.run;
    if (capture.active && captureRun?.state !== 'running') {
      box.append(node('strong', '', 'A capture by another process is running'),
        node('div', 'pharos-sub', 'Another process on this Mac, such as pharos capture, holds the capture lock. Capture waits until it finishes.'), progressBar(null));
      return box;
    }
    if (capture.active && captureRun) {
      box.append(node('strong', '', `Capturing ${capture.host?.label || 'this Mac'}`),
        node('div', 'pharos-sub', `${captureRun.current_source || 'starting'} · ${captureRun.completed_sources} of ${captureRun.total_sources} sources · ${size(captureRun.bytes_copied)} copied`),
        progressBar(runProgress(captureRun)));
      return box;
    }
    if (indexRun?.state === 'running') {
      const head = node('div', 'pharos-run-head');
      head.append(node('strong', '', indexRun.stop_requested ? 'Stopping the index…' : 'Indexing captures'), stopButton(indexRun.stop_requested));
      box.append(head,
        node('div', 'pharos-sub', indexDetail(indexRun)),
        progressBar(indexProgress(indexRun)));
      return box;
    }
    const recent = [captureRun, indexRun].filter(Boolean).sort((a, b) => String(b.completed_at || '').localeCompare(String(a.completed_at || '')))[0];
    if (!recent || !recent.completed_at || Date.now() - new Date(recent.completed_at).getTime() > 3600_000) return null;
    const failed = recent.kind === 'capture' ? recent.state !== 'complete' : indexFailed(recent);
    if (recent.kind === 'capture') {
      box.append(node('span', failed ? 'pharos-error' : '', failed ? `The last capture ${recent.state === 'failed' ? 'finished with errors' : 'stopped'}: ${(recent.errors || []).map(error => error.error).slice(0, 2).join('; ') || 'see the sources below'}.` : `Captured ${size(recent.bytes_copied)} ${ago(recent.completed_at)}.`));
    } else {
      if (indexStopped(recent)) {
        box.append(node('span', '', `You stopped the last index ${ago(recent.completed_at)}; what it wrote is kept, and the next index resumes it.`));
        const errors = indexErrors(recent);
        if (errors.length) box.append(node('div', 'pharos-error', `It also hit errors: ${errors.join(' · ')}`));
      } else box.append(node('span', failed ? 'pharos-error' : '', failed ? `The last index ${recent.state === 'interrupted' ? 'was interrupted; the next one resumes it' : `failed: ${recent.error || 'see the sources below'}`}.` : `${indexRunSummary(recent)} ${ago(recent.completed_at)}.`));
    }
    return box;
  }

  async function startCapture(quiet = false) {
    hostsBusy = 'capture';
    hostsError = null;
    renderHosts();
    try {
      const run = await runs.capture(null, update => {
        if (!update || !hostsData) return;
        hostsData.capture = {...hostsData.capture, run: update, active: update.state === 'running'};
        renderHosts();
      });
      if (!quiet || run?.state !== 'complete') toast(run?.state === 'complete' ? `Captured ${size(run.bytes_copied)}` : 'The capture finished with errors');
      return run;
    } catch (error) {
      hostsError = error.status === 409 ? 'A capture is already running.' : error.message;
      toast(hostsError);
    } finally {
      hostsBusy = null;
      await loadHosts().catch(() => {});
    }
  }

  async function startIndex(body, label, quiet = false) {
    hostsBusy = 'index';
    hostsError = null;
    renderHosts();
    try {
      const run = await runs.index(body, update => {
        if (!update || !hostsData) return;
        hostsData.index = {...hostsData.index, run: update, active: update.state === 'running'};
        renderHosts();
      });
      if (!quiet || run?.state !== 'complete') toast(run?.state === 'complete' ? indexRunSummary(run)
        : indexStopped(run) ? `Indexing ${label} stopped; what it wrote is kept, and the next index resumes it${indexErrors(run).length ? `. It also hit errors: ${indexErrors(run)[0]}` : ''}`
        : `Indexing ${label} ${run?.state === 'interrupted' ? 'was interrupted' : 'finished with errors'}`);
      window.loadSources?.();
      return run;
    } catch (error) {
      hostsError = error.status === 409 ? 'Source indexing is already running; try again when it finishes.' : error.message;
      toast(hostsError);
    } finally {
      hostsBusy = null;
      await loadHosts().catch(() => {});
    }
  }

  // Sync now runs the incremental sync (Settings → Sync's Check now): it
  // indexes only what changed in this Mac's live sources.
  async function syncNow() {
    if (headerCaptureIndexRunning || hostsBusy) return;
    headerCaptureIndexRunning = true;
    renderHeaderAction();
    try {
      await post('/api/sync/check', {});
    } catch (error) {
      toast(error.message);
    } finally {
      headerCaptureIndexRunning = false;
      await window.pharosSync?.refresh();
      renderHeaderAction();
      await refreshStatus();
    }
  }

  async function captureAndIndex() {
    if (headerCaptureIndexRunning || hostsBusy) return;
    headerCaptureIndexRunning = true;
    renderHeaderAction();
    try {
      await window.pharosUpgrade?.refresh();
      if (upgradeStatus?.running) return;
      const sources = await call('/api/sources');
      let upgradeScheduled = false;
      if (sources.enabled) {
        const started = await post('/api/library/update', {});
        refreshStatus();
        const indexed = await watch('/api/activity', started.run_id);
        if (indexed?.state !== 'complete') toast(indexed?.error || 'Update stopped; committed work is retained.');
        upgradeScheduled = indexed?.state === 'complete';
        if (upgradeScheduled) await window.pharosUpgrade?.start();
      } else if (!upgradeStatus?.needed) {
        toast('No enabled sources on this Mac. Open Settings → Sources to enable one.');
      }
      if (upgradeScheduled) {
        for (let attempt = 0; attempt < 8; attempt++) {
          await window.pharosUpgrade?.refresh();
          if (upgradeStatus?.running || !upgradeStatus?.needed) break;
          await sleep(250);
        }
      }
      else await window.pharosUpgrade?.start();
    } catch (error) {
      toast(error.message);
    } finally {
      headerCaptureIndexRunning = false;
      renderHeaderAction();
      await refreshStatus();
    }
  }

  // Each of this Mac's source cards says when it was captured.
  function annotateCards() {
    const grid = document.getElementById('sourceGrid');
    if (!grid || !hostsData) return;
    const current = (hostsData.index.hosts || []).find(host => host.current);
    for (const card of grid.querySelectorAll('.source-card:not(.remote)')) {
      const name = card.querySelector('h2')?.textContent;
      const source = current?.sources.find(item => item.name === name);
      let line = card.querySelector('.pharos-card-capture');
      if (!line) {
        line = node('div', 'pharos-card-capture');
        card.append(line);
      }
      line.replaceChildren();
      if (!source) {
        line.append(node('span', 'pharos-sub', `Not captured to ${driveName() === 'the drive' ? 'the library' : driveName()} yet`));
        continue;
      }
      line.append(node('span', 'pharos-sub', 'Captured '), when(source.captured_at));
      line.append(node('span', source.needs_index ? 'pharos-warn' : 'pharos-sub', source.needs_index ? ` · needs index (${source.index_reason || 'new data'})` : ' · indexed'));
    }
  }

  // ---- Settings → Health: the drive's checks and backups
  const CHECK_TITLES = {encryption: 'Encryption', spotlight: 'Spotlight', filesystem: 'File system', location: 'Location', free_space: 'Free space', volume_pin: 'Volume pin', backup: 'Backup'};
  const STATUS_DOT = {ok: '', info: 'idle', unknown: 'idle', warning: 'warn', problem: 'bad'};
  let backupDraft = null, backupError = null, backupBusy = false, driveHealthExpanded = true, driveHealthInitialized = false;

  async function loadDriveHealth() {
    const [drive, backup] = await Promise.all([call('/api/health/drive'), call('/api/backup')]);
    renderDriveHealth(drive, backup);
    return drive.status === 'checking' || backup.active;
  }

  function renderDriveHealth(drive, backup) {
    const cards = document.getElementById('healthCards');
    // Leave the form alone while someone is typing a destination.
    if (!cards || document.activeElement?.id === 'pharosBackupDestination') return;
    if (!driveHealthInitialized && drive.status !== 'checking') {
      const attention = (drive.checks || []).filter(check => check.status === 'warning' || check.status === 'problem');
      if (attention.some(check => check.id !== 'backup')) driveHealthExpanded = true;
      else if (attention.some(check => check.id === 'backup')) driveHealthExpanded = false;
      driveHealthInitialized = true;
    }
    let section = document.getElementById('pharosDriveHealth');
    if (!section) {
      section = node('section', 'pharos-section');
      section.id = 'pharosDriveHealth';
      cards.after(section);
    }
    const volume = drive.volume || {};
    const name = volume.name || status?.drive?.name || 'Library drive';
    const head = node('div', 'pharos-section-head'), intro = node('div');
    const title = node('h3', '', drive.scope === 'archive_root' ? `Archive drive · ${name}` : `Library drive · ${name}`);
    intro.append(title);
    const facts = [];
    if (volume.location) facts.push({external: 'external drive', 'disk-image': 'disk image', internal: "this Mac's internal disk"}[volume.location] || volume.location);
    if (volume.filesystem) facts.push(volume.filesystem.toUpperCase());
    if (drive.free_bytes != null) facts.push(`${size(drive.free_bytes)} free`);
    if (drive.sizes?.library_bytes) facts.push(`library ${size(drive.sizes.library_bytes)}`);
    if (drive.checked_at) facts.push(`checked ${ago(drive.checked_at)}`);
    intro.append(node('p', 'muted', facts.join(' · ') || drive.path || ''));
    const controls = node('div', 'pharos-health-actions');
    const attention = node('div', 'pharos-health-attention');
    const attentionChecks = (drive.checks || []).filter(check => check.status === 'warning' || check.status === 'problem');
    if (attentionChecks.length) {
      attention.append(node('span', 'pharos-health-attention-label', 'Needs Attention:'));
      for (const check of attentionChecks) {
        const chip = button(CHECK_TITLES[check.id] || check.id, 'pharos-chip warn', () => {
          driveHealthExpanded = true;
          renderDriveHealth(drive, backup);
          section.querySelector(`[data-check="${check.id}"]`)?.scrollIntoView({block: 'center'});
        });
        attention.append(chip);
      }
      controls.append(attention);
    }
    const toggle = button(driveHealthExpanded ? 'Collapse' : 'Expand', '', () => { driveHealthExpanded = !driveHealthExpanded; renderDriveHealth(drive, backup); });
    toggle.setAttribute('aria-expanded', String(driveHealthExpanded));
    toggle.setAttribute('aria-controls', 'pharosDriveHealthDetails');
    controls.append(toggle);
    head.append(intro, controls);
    section.replaceChildren(head);

    const details = node('div', 'pharos-health-details');
    details.id = 'pharosDriveHealthDetails';
    details.hidden = !driveHealthExpanded;
    const list = node('div', 'pharos-checks');
    list.id = 'pharosDriveChecks';
    if (drive.status === 'checking') list.append(node('p', 'pharos-sub', 'Checking the drive (diskutil, Spotlight status, the library’s size)…'));
    for (const check of drive.checks || []) {
      const item = node('div', 'pharos-check');
      item.dataset.check = check.id;
      item.append(node('span', 'pharos-dot ' + (STATUS_DOT[check.status] ?? 'idle')));
      const titleLine = node('div', 'pharos-check-title', CHECK_TITLES[check.id] || check.id);
      if (check.status !== 'ok') titleLine.append(node('span', check.status === 'problem' ? 'pharos-error' : check.status === 'warning' ? 'pharos-warn' : 'pharos-sub', ` · ${check.status}`));
      item.append(titleLine, node('div', 'pharos-sub', check.summary));
      if (check.recommendation) item.append(node('div', '', check.recommendation));
      if (check.action) item.append(node('div', 'pharos-sub', check.action));
      if (check.command) { const line = codeLine(check.command); item.append(line); }
      list.append(item);
    }
    details.append(list, backupPanel(backup, name));
    section.append(details);
  }

  function backupPanel(backup, driveLabel) {
    const box = node('div', 'pharos-backup');
    box.id = 'pharosBackup';
    box.append(node('h3', '', 'Backups'));
    const last = backup.last_success;
    if (last) {
      const line = node('p', '');
      line.append(node('span', '', 'Last backup '), when(last.finished_at), node('span', '', ` to ${last.destination} · ${size(last.bytes_total)} (${size(last.bytes_copied)} copied)`));
      box.append(line);
    } else {
      box.append(node('p', 'pharos-warn', `No backup yet: ${driveLabel} holds the only copy of the library.`));
    }
    const run = backup.run;
    if (backup.active && run) {
      box.append(node('div', '', `Backing up to ${run.destination} · ${run.phase || 'starting'} · ${plural(run.files_copied, 'file')} copied, ${plural(run.files_unchanged, 'file')} unchanged · ${size(run.bytes_copied)} copied`), progressBar(null));
      box.append(button('Cancel backup', '', async () => { try { await post('/api/backup/cancel'); } catch (error) { toast(error.message); } refreshSettings(); }));
    } else if (run && run.completed_at) {
      const done = run.state === 'complete', partial = run.state === 'partial';
      const copied = `${plural(run.files_total, 'file')} (${size(run.bytes_total)}; ${size(run.bytes_copied)} copied) to ${run.destination} in ${Math.max(1, Math.round(run.duration_seconds))} s`;
      box.append(node('p', done ? 'pharos-ok' : partial ? 'pharos-warn' : 'pharos-error', done
        ? `Backed up ${copied}.`
        : partial
          ? `Backed up ${copied}, without ${(run.missing || []).join(', ') || 'parts of the library'}, which could not be read; the backup keeps its earlier copy of those.`
          : `The backup to ${run.destination} ${run.state === 'cancelled' ? 'was stopped' : 'failed'}; the next one carries on from what was copied.`));
      const problems = node('ul');
      (run.errors || []).slice(0, 5).forEach(error => problems.append(node('li', 'pharos-error', error.path ? `${error.path}: ${error.error}` : error.error)));
      (run.warnings || []).forEach(warning => problems.append(node('li', 'pharos-warn', warning)));
      if (problems.childElementCount) box.append(problems);
    }
    const form = node('form'), label = node('label', '', 'Back up to'), input = node('input'), go = node('button', 'pharos-button primary', 'Back up now');
    label.htmlFor = input.id = 'pharosBackupDestination';
    input.value = backupDraft ?? backup.suggested_destination ?? '';
    input.placeholder = '/Volumes/Other Drive/Pharos Backup';
    input.spellcheck = false;
    input.oninput = () => { backupDraft = input.value; };
    go.type = 'submit';
    go.disabled = backup.active || backupBusy;
    form.onsubmit = async event => {
      event.preventDefault();
      backupBusy = true;
      backupError = null;
      go.disabled = true;
      try {
        await post('/api/backup', {destination: input.value.trim()});
        backupDraft = null;
      } catch (error) {
        backupError = error.message;
      } finally {
        backupBusy = false;
        // Polls quickly again while the backup runs.
        refreshSettings();
      }
    };
    form.append(label, input, go);
    box.append(form);
    if (backupError) {
      const error = node('p', 'pharos-error', backupError);
      error.id = 'pharosBackupError';
      error.setAttribute('role', 'alert');
      box.append(error);
    }
    box.append(node('p', 'pharos-sub', 'Choose a folder on another drive, or on a Mac with FileVault on. The library’s own drive is refused: a backup there would be lost with it. Later backups copy only what changed.'));
    return box;
  }

  // ---- Settings visibility
  let settingsTimer = null, settingsRefresh = null, settingsAgain = false;
  const settingsVisible = () => document.getElementById('settings')?.classList.contains('active') && !document.hidden;
  // One refresh at a time; a request meanwhile runs once more after it.
  async function refreshSettings() {
    clearTimeout(settingsTimer);
    if (!settingsVisible()) return;
    if (settingsRefresh) { settingsAgain = true; return; }
    let busy = false;
    settingsRefresh = (async () => {
      do {
        settingsAgain = false;
        const results = await Promise.allSettled([loadHosts(), loadDriveHealth()]);
        busy = results.some(result => result.status === 'fulfilled' && result.value);
      } while (settingsAgain && settingsVisible());
    })();
    await settingsRefresh;
    settingsRefresh = null;
    clearTimeout(settingsTimer);
    // Captures are listed from their manifests on the drive; poll gently.
    if (settingsVisible()) settingsTimer = setTimeout(refreshSettings, busy ? 2500 : 20000);
  }

  function watchSettings() {
    const settings = document.getElementById('settings');
    if (settings) new MutationObserver(() => { if (settingsVisible()) refreshSettings(); }).observe(settings, {attributes: true, attributeFilter: ['class']});
    // Sources rebuilds its cards; mark each with its capture again.
    const grid = document.getElementById('sourceGrid');
    if (grid) new MutationObserver(annotateCards).observe(grid, {childList: true});
    document.addEventListener('visibilitychange', () => { if (settingsVisible()) refreshSettings(); });
    if (settingsVisible()) refreshSettings();
  }

  window.pharosLibrary = {refresh: refreshStatus, status: () => status, runs, refreshSettings, onStatus: listener => listeners.add(listener), toggleActivityPanel: () => (panel && panelMode === 'activity' ? closePanel() : openPanel('activity'))};
  const sheet = node('style');
  sheet.textContent = CSS;
  document.head.append(sheet);
  watchSettings();
  refreshStatus();
})();
