// Pharos library, drive and captures. GET /api/library/status says what holds
// or writes the library; the header shows it and estimated completion times.
// In the app, the drive panel's Eject goes through the pharosLibrary message
// handler, which releases the library, stops agents' MCP servers running from
// the drive, quits, and ejects the drive. Settings → Sources shows
// captures from other Macs alongside this Mac's sources; Settings →
// Health gains the drive's checks and backups.
(() => {
  'use strict';
  const CSS = `
.pharos-library-controls{min-width:0;font-size:var(--fs-md,13px)}
.combo-button>.pharos-drive{gap:7px;min-width:0;max-width:190px;font-weight:400;line-height:1}
.pharos-drive-name{flex:0 1 auto;min-width:2.5em;font-weight:650;overflow:hidden;text-overflow:ellipsis}
.pharos-dot{flex:none;display:inline-block;width:8px;height:8px;border-radius:50%;background:var(--sea,#315845)}
.pharos-drive .pharos-dot{background:#8cc6a0}
.pharos-dot.busy{background:var(--mast-brass,#d4a857);box-shadow:0 0 0 3px color-mix(in srgb,var(--mast-brass,#d4a857) 30%,transparent)}
.pharos-drive .pharos-dot.busy{animation:pharos-drive-pulse 1.6s ease-in-out infinite}
@keyframes pharos-drive-pulse{50%{opacity:.55;box-shadow:0 0 0 6px color-mix(in srgb,var(--mast-brass,#d4a857) 10%,transparent)}}
@media(prefers-reduced-motion:reduce){.pharos-drive .pharos-dot.busy{animation:none}}
.pharos-drive .pharos-dot.bad{background:#e5675c;box-shadow:0 0 0 3px color-mix(in srgb,#e5675c 35%,transparent);animation:pharos-drive-alert 1.6s ease-in-out infinite}
@keyframes pharos-drive-alert{50%{opacity:.6;box-shadow:0 0 0 6px color-mix(in srgb,#e5675c 12%,transparent)}}
@media(prefers-reduced-motion:reduce){.pharos-drive .pharos-dot.bad{animation:none}}
.pharos-attention{margin-top:12px;padding:10px 12px;border:1px solid var(--bad,#9c3d36);border-radius:8px;background:color-mix(in srgb,var(--bad,#9c3d36) 8%,transparent)}
.pharos-drive-panel .pharos-attention h3{margin-top:0;color:var(--bad,#9c3d36)}
.pharos-attention-item+.pharos-attention-item{margin-top:10px;padding-top:10px;border-top:1px solid var(--line,#ccc)}
.pharos-dot.warn{background:var(--warn,#98601d)}.pharos-dot.bad{background:var(--bad,#9c3d36)}.pharos-dot.idle{background:var(--muted,#756c5f)}
.pharos-drive-panel{position:fixed;z-index:8000;width:min(430px,calc(100vw - 24px));max-height:calc(100vh - 100px);overflow:auto;padding:16px 18px;background:var(--panel,#fff);color:var(--ink,#222);border:1px solid var(--line,#ccc);border-radius:14px;box-shadow:0 18px 50px #0005;font-size:13px}
.pharos-drive-panel h2{margin:0;font:700 20px/1.2 var(--serif,serif)}
.pharos-panel-head{display:flex;justify-content:space-between;align-items:center;gap:12px}
.pharos-panel-head>h2{min-width:0;overflow-wrap:anywhere}
.pharos-panel-head>.pharos-button{flex:none}
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
.pharos-activity-timing{display:flex;flex-wrap:wrap;gap:2px 12px;font-size:12px;line-height:1.5;font-variant-numeric:tabular-nums}
.pharos-progress-caption,.pharos-activity-details{font-size:12px;line-height:1.5}
.pharos-activity-details{overflow-wrap:anywhere}
.pharos-activity-details>summary{display:flex;align-items:flex-start;gap:12px;cursor:pointer;list-style:none}
.pharos-activity-details>summary::-webkit-details-marker{display:none}
.pharos-activity-details>summary .pharos-activity-timing{flex:1;min-width:0}
.pharos-details-label{flex:none;margin-left:auto}
.pharos-details-label::before{content:'▸ ';}
.pharos-activity-details[open] .pharos-details-label::before{content:'▾ ';}
.pharos-activity-details>div:first-of-type{margin-top:4px}
.pharos-activity-head{display:flex;align-items:center;gap:8px;font-weight:650}
.pharos-activity-head .pharos-button{margin-left:auto;font-weight:400}
.pharos-activity .pharos-sub{margin-top:2px}
.pharos-last-index{padding-top:8px;border-top:1px solid var(--line,#ccc)}
.pharos-last-index h3{margin-top:4px}
.pharos-last-index .pharos-sub{margin-top:3px}
.pharos-bar{height:6px;margin:7px 0 4px;overflow:hidden;border-radius:6px;background:var(--line,#ccc)}
.pharos-bar>span{display:block;height:100%;background:var(--accent,#315845);transition:width .25s}
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
.source-toolbar>a{margin-left:auto;font-size:var(--fs-sm)}
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
  const progressBar = (value, label = 'Current stage') => {
    const known = Number.isFinite(value);
    const percent = known ? Math.max(0, Math.min(100, Math.floor(value * 100))) : null;
    const bar = node('div', 'pharos-bar' + (known ? '' : ' indeterminate')), fill = node('span');
    bar.setAttribute('role', 'progressbar');
    bar.setAttribute('aria-label', label);
    if (known) {
      fill.style.width = `${percent}%`;
      bar.setAttribute('aria-valuemin', '0');
      bar.setAttribute('aria-valuemax', '100');
      bar.setAttribute('aria-valuenow', String(percent));
    } else bar.setAttribute('aria-valuetext', 'Work in progress; total not yet known');
    bar.append(fill);
    return bar;
  };
  const duration = seconds => {
    if (seconds < 60) return `${Math.max(5, Math.ceil(seconds / 5) * 5)} sec`;
    if (seconds < 3600) return `${Math.ceil(seconds / 60)} min`;
    return `${(Math.ceil(seconds / 360) / 10).toFixed(1)} hr`;
  };
  const ESTIMATE_TEXT = {partial: 'Total time not known yet', provisional: 'Early forecast…', complete: 'Planned work finished', learning: 'Learning this workload…', unknown: 'Time remaining not known', stalled: 'Waiting for progress…', finishing: 'Finishing this stage…', stopping: 'Stopping safely…'};
  // What an activity's timing line shows: whole seconds since it started
  // (null if it reports no start), and either the range of time left in this
  // stage or the estimate's state. A range needs a positive low and a high no
  // lower than it; any other estimate, and every estimate while the status
  // is unavailable, shows its state, unknown if unrecognised.
  function activityTiming(activity, {now = Date.now(), unavailable = Boolean(statusError), scope = 'operation'} = {}) {
    const started = Date.parse(activity.started_at);
    const elapsed = Number.isFinite(started) ? Math.max(0, Math.floor((now - started) / 1000)) : null;
    const forecast = scope !== 'stage' && activity.forecast;
    const estimate = unavailable ? {state: 'unknown', basis: 'Status unavailable; reconnecting before updating the estimate'} : forecast || activity.estimate;
    let range = null;
    if (['estimating', 'provisional'].includes(estimate?.state) && estimate.low_seconds > 0 && estimate.high_seconds >= estimate.low_seconds) {
      const low = duration(estimate.low_seconds), high = duration(estimate.high_seconds);
      const sameUnit = low.split(' ').at(-1) === high.split(' ').at(-1);
      range = low === high ? high : sameUnit ? `${low.split(' ')[0]}–${high}` : `${low}–${high}`;
    }
    const state = range ? estimate.state : ESTIMATE_TEXT[estimate?.state] ? estimate.state : 'unknown';
    return {elapsed, state, range, scope: forecast ? 'operation' : 'stage', basis: estimate?.basis || 'This stage does not report a measurable total.'};
  }
  const completionEstimate = activity => {
    const timing = node('span', 'pharos-sub pharos-activity-timing');
    const {elapsed, state, range, basis, scope} = activityTiming(activity);
    if (elapsed !== null) timing.append(node('span', '', `${elapsed < 60 ? `${elapsed} sec` : `${Math.floor(elapsed / 60)} min`} elapsed`));
    timing.append(node('span', '', range ? `${scope === 'operation' ? 'Total' : 'Stage'}: ~${range} left${state === 'provisional' ? ' (early)' : ''}` : ESTIMATE_TEXT[state]));
    if (scope === 'operation') {
      const stage = activityTiming(activity, {scope: 'stage'});
      if (stage.range) timing.append(node('span', '', `Stage: ~${stage.range} left`));
    }
    timing.title = basis;
    return timing;
  };
  // A stage's progress is measured by its input size when it reports one,
  // else by its unit count. Work without a total has no known progress
  // (null); an activity without work may report its own fraction.
  const workMeasure = work => work?.size?.total > 0 ? work.size : work;
  const activityProgress = activity => {
    const measure = workMeasure(activity.work);
    return measure?.total > 0 ? measure.done / measure.total : activity.work?.total_known && measure?.total === 0 ? 1 : activity.work ? null : activity.progress;
  };
  // An estimate's measured pace: bytes per second, or other units per minute.
  const estimateRate = (estimate, unit) => estimate.units_per_second > 0 && unit
    ? unit === 'byte' ? `${size(estimate.units_per_second)}/sec`
      : `${(estimate.units_per_second * 60).toLocaleString(undefined, {maximumFractionDigits: 1})} ${unit}s/min`
    : '';
  const workCount = work => {
    if (!work?.unit) return '';
    const done = work.unit === 'byte' ? size(work.done) : Number(work.done || 0).toLocaleString();
    const total = work.unit === 'byte' ? size(work.total) : plural(work.total, work.unit);
    return work.total > 0 ? `${done} of ${total}${work.unit === 'byte' ? ' copied' : ' processed'}`
      : work.unit === 'byte' ? `${done} copied` : `${plural(work.done, work.unit)} processed`;
  };
  // Show the stage once, with its operation/source as context. Only remove
  // exact duplicates of structured fields; retain other reported detail.
  function activityPresentation(activity, {unavailable = Boolean(statusError)} = {}) {
    const normalize = text => String(text || '').trim().toLowerCase();
    const phase = activity.work?.phase;
    const title = phase ? phase.replace(/^./, letter => letter.toUpperCase()) : activity.label;
    const work = activity.work, measured = workMeasure(work)?.total > 0;
    const progress = activityProgress(activity);
    const count = work?.done || work?.total > 0 || work?.total_known ? workCount(work) : '';
    const volume = work?.size?.total > 0 ? (work.size.unit === 'byte' ? `${size(work.size.done)} of ${size(work.size.total)} processed` : `${Number(work.size.done).toLocaleString()} of ${plural(work.size.total, 'message')} processed`) : '';
    const percent = measured ? `${Math.min(100, Math.floor(progress * 100))}% of ${work.size?.total > 0 ? 'input data' : 'stage'}` : '';
    const redundant = new Set([title, count, count.replace(/ processed$/, ''), volume, volume.replace(/ processed$/, '')].map(normalize));
    const context = [activity.label, ...(activity.detail || '').split(' · ')].filter(text => text && !redundant.has(normalize(text)));
    if (work?.source && !context.some(text => normalize(text) === normalize(work.source))) context.push(work.source);
    const unique = values => [...new Map(values.filter(Boolean).map(text => [normalize(text), text])).values()];
    const estimate = activity.estimate;
    const rate = estimate && !unavailable ? estimateRate(estimate, estimate.unit || work?.unit) : '';
    const forecast = !unavailable && activity.forecast;
    const details = unique([
      !unavailable && estimate?.basis && `${forecast ? 'Stage: ' : ''}${estimate.basis}`,
      forecast?.basis,
      forecast?.remaining_steps > 0 && `${forecast.predicted_steps} of ${forecast.remaining_steps} remaining steps estimated`,
      forecast?.unknown_steps?.length && `Learning: ${forecast.unknown_steps.join(', ')}`,
      work?.next && `Next: ${work.next}`,
    ]);
    return {title, context: unique(context).join(' · '), caption: unique([count, volume, percent, rate]).join(' · '), details};
  }
  const expandedActivities = new Set();
  function activityLine(activity) {
    const item = node('div', 'pharos-activity');
    const view = activityPresentation(activity);
    item.append(node('div', 'pharos-activity-head', view.title));
    if (view.context) item.append(node('div', 'pharos-sub', view.context));
    item.append(progressBar(activityProgress(activity), `${activity.label}: ${activity.work?.phase || 'current stage'}`));
    if (view.caption) item.append(node('div', 'pharos-sub pharos-progress-caption', view.caption));
    const timing = completionEstimate(activity);
    if (view.details.length) {
      const details = node('details', 'pharos-sub pharos-activity-details');
      details.dataset.activity = activity.id || `${activity.kind}:${activity.label}:${activity.started_at || ''}`;
      details.open = expandedActivities.has(details.dataset.activity);
      const summary = node('summary');
      summary.append(timing, node('span', 'pharos-details-label', 'details'));
      details.append(summary);
      for (const text of view.details) details.append(node('div', '', text));
      item.append(details);
    } else item.append(timing);
    return item;
  }
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
  const nativeLibrary = () => window.webkit?.messageHandlers?.pharosLibrary;
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
    renderSyncControls();
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
  async function stopWork(paths = '/api/index/cancel') {
    for (const path of [paths].flat()) {
      try {
        const {stopped} = await post(path);
        if (!stopped) toast(path === LIBRARY_REFRESH_STOP ? 'Library view indexing is not running.' : 'No index is running.');
      } catch (error) {
        toast(error.message);
      }
    }
    await refreshStatus();
    if (document.getElementById('sourceGrid')) loadHosts().catch(() => {});
  }
  const stopButton = (stopping, path) => {
    const stop = button(stopping ? 'Stopping…' : 'Stop', '', () => {
      stop.disabled = true;
      stop.textContent = 'Stopping…';
      return stopWork(path);
    });
    stop.disabled = stopping;
    stop.title = path === LIBRARY_REFRESH_STOP
      ? 'Stops indexing the Library view. Workspaces not yet indexed are worked out as the Library shows them, which can be slower, until you resume it or Capture & index.'
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
    renderSyncControls();
    listeners.forEach(listener => listener(status));
    const busy = status && !status.idle;
    if (!document.hidden) statusTimer = setTimeout(refreshStatus, busy || statusError ? 2000 : 10000);
    return status;
  }
  document.addEventListener('visibilitychange', () => { if (!document.hidden) refreshStatus(); });

  let controls = null, chip = null, headerSync = null, headerCaptureIndexRunning = false, panel = null, panelMode = 'drive', ejectState = null;
  let driveSpace = null, driveSpaceRequest = null, driveSpaceError = false;
  // The drive chip and Index changes share one combo button (ui.py's
  // .combo-button) in the configurable library slot. Stop, and the full
  // Capture & index, are in the drive panel.
  function ensureControls() {
    if (controls?.isConnected) return controls;
    const icons = document.getElementById('headerLibrary');
    if (!icons) return null;
    controls = node('div', 'combo-button pharos-library-controls');
    controls.setAttribute('role', 'group');
    controls.setAttribute('aria-label', 'Library');
    icons.append(controls);
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
    headerSync.title = 'Index changes';
    headerSync.setAttribute('aria-label', 'Index changes');
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

  // Index changes is busy while the live drive activity has a capture or
  // index, an incremental run is running (sync.js), this page awaits a
  // capture or index it started, or an upgrade runs. Source snapshots stop
  // refreshing outside Settings, so they are not consulted: a cached run
  // cannot keep the header spinning after the drive panel reports that it
  // finished.
  const headerBusy = ({activities, incremental, local, upgrading}) => Boolean(local)
    || Boolean(activities?.some(activity => ['capture', 'capture-other', 'sync', 'index'].includes(activity.kind)))
    || Boolean(incremental) || Boolean(upgrading);

  const captureIndexDisabled = state => headerBusy(state) || state.sourcesEnabled === false && !state.upgradeNeeded || Boolean(state.releasing);
  // A single Stop reaches each independent stoppable task, even when an
  // index and the Library view refresh run at the same time.
  function stopTargets(activities) {
    const targets = new Map();
    for (const activity of activities.filter(activity => activity.stoppable)) {
      const path = activity.stop_path || '/api/index/cancel';
      const target = targets.get(path);
      targets.set(path, {path, stopping: Boolean(activity.stopping) && (!target || target.stopping)});
    }
    return [...targets.values()];
  }

  function renderHeaderAction() {
    const actions = [ensureHeaderAction(), document.getElementById('headerSyncOnly')];
    const state = {activities: status?.activities, incremental: window.pharosSync?.isBusy(),
      local: headerCaptureIndexRunning || hostsBusy, upgrading: upgradeStatus?.running,
      sourcesEnabled: hostsData?.sources.enabled, upgradeNeeded: upgradeStatus?.needed, releasing: ejectState?.phase === 'ejecting'};
    const busy = headerBusy(state);
    for (const action of actions) if (action) {
      action.disabled = busy;
      action.setAttribute('aria-busy', String(busy));
    }
    // Capture & index is on Settings → Capture & index and in the drive panel.
    for (const update of document.querySelectorAll('#pharosUpdateLibrary, .pharos-panel-update')) {
      update.disabled = captureIndexDisabled(state);
      if (update.classList.contains('pharos-panel-update')) update.hidden = update.disabled;
      update.setAttribute('aria-busy', String(busy));
    }
    const capture = document.getElementById('pharosCaptureHost');
    if (capture) {
      capture.disabled = busy || !hostsData?.sources.enabled;
      capture.textContent = hostsData?.capture.active ? 'Capturing…' : 'Capture this Mac';
    }
    const index = document.getElementById('pharosIndexAll');
    if (index) index.disabled = busy || !(hostsData?.index.hosts || []).some(host => (host.sources || []).some(source => source.needs_index));
  }

  // The running activities that make up a library update, shown together:
  // captures and indexes, a library upgrade (with its Tools ledger step), and
  // the Git lookup and findings update that follow an index completed in the
  // last five minutes. Other work, such as standalone maintenance, stays
  // separate. A capture, else an index, else the first gives the label and
  // progress. Null when nothing is updating. The last index run and upgrade
  // status default to this page's.
  function libraryUpdate(activities, {lastIndex = recentIndex, upgradeState = upgradeStatus, now = Date.now()} = {}) {
    const recent = lastIndex?.state === 'complete' && now - new Date(lastIndex.completed_at).getTime() < 300_000;
    const indexFollowUp = recent && activities.find(activity => activity.kind === 'git'
      && activity.detail?.includes('after the last index')
      && new Date(activity.started_at).getTime() >= new Date(lastIndex.started_at).getTime());
    const updating = activities.filter(activity => ['capture', 'sync', 'index'].includes(activity.kind)
      || activity.label === 'Upgrading the library'
      || (upgradeState?.running && upgradeState.step === 'tools' && activity.label === 'Building the Tools ledger')
      || (indexFollowUp && (activity === indexFollowUp || activity.label === 'Updating findings')));
    if (!updating.length) return null;
    const capturing = updating.find(activity => activity.kind === 'capture');
    const indexing = updating.find(activity => activity.kind === 'sync' || activity.kind === 'index');
    const upgrade = upgradeState?.running;
    const step = upgradeState?.steps?.find(item => item.id === upgradeState.step);
    const primary = capturing || indexing || updating[0];
    const progress = activityProgress(primary);
    // Index changes says which step it is on ("Indexing changes: checking for changes").
    const changesStep = indexing?.automatic && indexing.label?.replace(/^Indexing changes: (.)/, (_, first) => first.toUpperCase());
    const label = primary.work?.phase ? primary.work.phase.replace(/^./, letter => letter.toUpperCase()) : capturing ? 'Capturing sources' : indexing ? changesStep || 'Indexing conversations' : upgrade ? (step?.label || 'Upgrading the library') : 'Finishing capture & index';
    return {updating, indexing, capturing, label, progress};
  }

  function activitySummary(activity) {
    const verb = {sync: 'Indexing', index: 'Indexing', capture: 'Capturing', 'capture-other': 'Capturing', backup: 'Backing up'}[activity.kind] || activity.label;
    return activity.progress != null ? `${verb} · current stage ${Math.floor(activity.progress * 100)}%` : verb;
  }

  function libraryUpdateLine(update) {
    const group = node('div', 'pharos-update');
    for (const activity of update.updating) group.append(activityLine(activity));
    return group;
  }

  // The chip is just the drive's name and a dot: green when nothing is
  // running, so ejecting stops nothing; brass and pulsing while work holds
  // the library, which an eject would stop first. The panel has the details.
  const DRIVE_PULSE_MS = 1600;
  // What needs the user's attention. The chip's dot turns red and pulses;
  // the drive panel says what is wrong and offers what to do about it.
  function openCaptureAndIndex() {
    closePanel();
    history.pushState(null, '', '/settings/sync');
    window.dispatchEvent(new PopStateEvent('popstate'));
  }
  function problems() {
    const found = [];
    const issues = window.pharosSync?.issues?.() || [];
    if (issues.length) {
      const shown = issues.slice(0, 3).map(issue => `${issue.source_name} · ${issue.unit}`);
      if (issues.length > shown.length) shown.push(`and ${issues.length - shown.length} more`);
      found.push({title: issues.length === 1 ? 'Index verification found a mismatch' : `Index verification found ${issues.length} mismatches`,
        detail: shown.join('; '), action: 'Review…', summary: 'index verification mismatch'});
    }
    // A failed index stops counting once another capture or index is running.
    const indexing = status?.activities?.some(activity => ['capture', 'sync', 'index'].includes(activity.kind));
    if (!indexing && indexFailed(recentIndex)) {
      const interrupted = recentIndex.state === 'interrupted';
      found.push({title: interrupted ? 'The last index was interrupted' : 'The last index failed',
        detail: indexErrors(recentIndex).join(' · '), action: 'Open Capture & index', summary: interrupted ? 'index interrupted' : 'index failed'});
    }
    return found;
  }

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
      const alerts = problems();
      // A problem outranks running work: an automatic index every minute
      // would otherwise keep it hidden.
      if (alerts.length || activities.length) {
        dot.classList.add(alerts.length ? 'bad' : 'busy');
        // The dot is rebuilt on every status refresh; entering mid-cycle keeps the pulse from restarting.
        dot.style.animationDelay = `-${Date.now() % DRIVE_PULSE_MS}ms`;
      }
      const update = libraryUpdate(activities);
      state = activities.length ? [update && `${update.label}${update.progress == null ? '' : ` · current stage ${Math.min(100, Math.floor(update.progress * 100))}%`}`,
        ...activities.filter(activity => !update?.updating.includes(activity)).map(activitySummary)].filter(Boolean).join(', ')
        : headerCaptureIndexRunning && upgradeStatus?.needed ? 'Preparing library upgrade'
          : upgradeStatus?.needed ? 'Library upgrade available' : 'Nothing running';
      if (alerts.length) state = `Needs attention (${alerts.map(alert => alert.summary).join(', ')}). ${state}`;
    }
    target.replaceChildren(dot, name);
    target.dataset.state = !status ? 'unavailable' : status.idle ? 'idle' : 'busy';
    target.title = `${name.textContent}: ${state}`;
    target.setAttribute('aria-label', `Library drive: ${target.title}`);
  }

  function placePanel() {
    const anchor = window.headerToolAnchor?.(chip) ?? chip;
    if (!panel || !anchor) return;
    const box = anchor.getBoundingClientRect(), header = document.querySelector('body>header')?.getBoundingClientRect();
    const top = Math.round((header?.bottom ?? box.bottom) + 8);
    panel.style.top = `${top}px`;
    panel.style.maxHeight = `${Math.max(0, window.innerHeight - top - 12)}px`;
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
    if (ejectState?.phase !== 'ejecting') ejectState = null;
    chip?.setAttribute('aria-expanded', 'false');
  }
  document.addEventListener('mousedown', event => {
    if (panel && !panel.contains(event.target) && !chip?.contains(event.target)) closePanel();
  });
  document.addEventListener('keydown', event => {
    if (panel && event.key === 'Escape') { closePanel(); (window.headerToolAnchor?.(chip) ?? chip)?.focus(); }
  });
  window.addEventListener('resize', placePanel);

  function renderPanel() {
    if (!panel) return;
    expandedActivities.clear();
    for (const details of panel.querySelectorAll('details[data-activity][open]')) expandedActivities.add(details.dataset.activity);
    panel.replaceChildren();
    if (!status) {
      panel.append(node('h2', '', panelMode === 'activity' ? 'Running activity' : 'Library'), node('p', 'pharos-error', statusError?.status === 503 ? 'Pharos is stopping; it has released or is releasing the library.' : `Could not read the library's status${statusError ? `: ${statusError.message}` : ''}.`));
      placePanel();
      return;
    }
    if (statusError) panel.append(node('p', 'pharos-error', 'Status unavailable. Showing the last reported counts; reconnecting…'));
    const drive = status.drive || {};
    if (panelMode === 'activity') {
      panel.append(node('h2', '', 'Running activity'));
    } else {
      const head = node('div', 'pharos-panel-head');
      head.append(node('h2', '', drive.ejectable ? drive.name : status.portable ? 'Library' : 'This Mac'));
      if (drive.ejectable && nativeLibrary()) head.append(ejectButton(drive, status.activities || []));
      panel.append(head);
      const where = [{external: 'External drive', 'disk-image': 'Disk image', internal: "This Mac's internal disk"}[drive.location] || 'Drive', status.library_dir].filter(Boolean).join(' · ');
      panel.append(node('div', 'pharos-sub', where));
      const space = node('dl', 'pharos-drive-space');
      const unknown = driveSpaceError || (driveSpace && driveSpace.status !== 'checking') ? 'Unavailable' : 'Checking…';
      space.append(node('dt', '', 'Free space'), node('dd', '', driveSpace?.free_bytes == null ? unknown : size(driveSpace.free_bytes)),
        node('dt', '', 'Pharos size'), node('dd', '', driveSpace?.sizes?.library_bytes == null ? unknown : size(driveSpace.sizes.library_bytes)));
      panel.append(space);
      if (ejectState?.phase === 'confirm') panel.append(ejectConfirm(drive, status.activities || []));
      if (ejectState?.phase === 'error') panel.append(node('p', 'pharos-error', ejectState.message));
    }

    const alerts = panelMode === 'drive' ? problems() : [];
    if (alerts.length) {
      const attention = node('section', 'pharos-attention');
      attention.setAttribute('aria-label', 'Needs attention');
      attention.append(node('h3', '', 'Needs attention'));
      for (const alert of alerts) {
        const item = node('div', 'pharos-attention-item'), row = node('div', 'pharos-running-head'), text = node('div');
        text.append(node('strong', '', alert.title));
        if (alert.detail) text.append(node('div', 'pharos-sub', alert.detail));
        row.append(text, button(alert.action, '', openCaptureAndIndex));
        item.append(row);
        attention.append(item);
      }
      panel.append(attention);
    }

    // One action slot: Stop while work can be stopped, Capture & index only
    // when it can run. Each activity below reports its stage and progress.
    const running = node('div', 'pharos-running-head'), runningText = node('div');
    runningText.append(node('h3', '', 'Running now'));
    running.append(runningText);
    panel.append(running);
    const activities = status.activities || [];
    const update = libraryUpdate(activities);
    if (!activities.length) runningText.append(node('p', 'pharos-sub', headerCaptureIndexRunning && upgradeStatus?.needed
      ? 'Preparing the library upgrade…' : upgradeStatus?.needed ? 'A library upgrade is available. Use Capture & index to run it.' : 'Nothing is running.'));
    const targets = stopTargets(activities);
    if (targets.length) {
      const stop = stopButton(targets.every(target => target.stopping), targets.filter(target => !target.stopping).map(target => target.path));
      stop.title = `Stops ${activities.filter(activity => activity.stoppable).map(activity => activity.label).join(' and ')} safely. Completed work is kept.`;
      running.append(stop);
    }
    if (panelMode === 'drive' && !targets.length) {
      const updateLibrary = button('Capture & index', 'pharos-panel-update', captureAndIndex);
      updateLibrary.title = "Captures this Mac's sources, indexes captures from every Mac, and runs any pending library upgrade. Index changes, in the header, only indexes what changed.";
      running.append(updateLibrary);
      renderHeaderAction();
    }
    if (update) {
      panel.append(libraryUpdateLine(update));
    }
    activities.filter(activity => !update?.updating.includes(activity)).forEach(activity => panel.append(activityLine(activity)));
    if (status.library_refresh?.stopped) {
      const item = node('div', 'pharos-activity pharos-library-refresh-stopped'), head = node('div', 'pharos-activity-head');
      head.append(node('span', 'pharos-dot idle'), node('span', '', 'Library view indexing stopped'), button('Resume', '', resumeLibraryRefresh));
      item.append(head, node('div', 'pharos-sub', `${status.library_refresh.pending ? `${plural(status.library_refresh.pending, 'workspace')} left to index; the Library works them out as it shows them, which can be slower. ` : ''}Capture & index or restarting Pharos also resumes it.`));
      panel.append(item);
    }
    if (upgradeStatus?.needed || upgradeStatus?.running) {
      panel.append(button('Upgrade details', '', () => window.pharosUpgrade?.open()));
    }

    if (recentIndex && !update) {
      const last = node('div', 'pharos-last-index');
      last.append(node('h3', '', 'Last index this session'));
      const failed = indexFailed(recentIndex);
      const label = indexStopped(recentIndex) ? 'Stopped' : recentIndex.state === 'interrupted' ? 'Interrupted' : failed ? 'Failed' : 'Complete';
      last.append(node('div', failed ? 'pharos-error' : '', `${label} · ${ago(recentIndex.completed_at)} · ${recentIndex.completed_sources} of ${recentIndex.total_sources} sources`));
      last.append(node('div', 'pharos-sub', `${plural(recentIndex.workspaces, 'workspace')} indexed · ${plural(recentIndex.conversations, 'conversation')}${indexStopped(recentIndex) ? '; the next index resumes' : ''}`));
      // A failure's errors are under Needs attention.
      const errors = failed ? [] : indexErrors(recentIndex);
      if (errors.length) last.append(node('div', 'pharos-error', errors.join(' · ')));
      panel.append(last);
    }

    placePanel();
  }

  // Pharos runs from the drive, as do the MCP servers agents start from it, so
  // ejecting quits Pharos and stops those servers too.
  const ejectNote = drive => `Quits Pharos and stops agents' Pharos MCP servers on this Mac, then ejects ${drive.name}. Reconnect agents (/mcp in Claude Code) once it is back.`;

  function ejectButton(drive, activities) {
    const ejecting = ejectState?.phase === 'ejecting';
    const action = activities.length ? 'Stop and Eject' : 'Eject';
    const eject_ = button(ejecting ? ejectState.message : `⏏ ${action}`, '', () => {
      if (activities.length) { ejectState = {phase: 'confirm'}; renderPanel(); } else eject(drive);
    });
    eject_.id = 'pharosEjectButton';
    eject_.title = ejectNote(drive);
    eject_.setAttribute('aria-label', `${action} ${drive.name}`);
    eject_.disabled = ejecting || ejectState?.phase === 'confirm';
    return eject_;
  }

  function ejectConfirm(drive, activities) {
    const row = node('div', 'pharos-row');
    const stopping = activities.filter(activity => activity.kind !== 'capture-other').map(activity => activity.label);
    if (stopping.length) row.append(node('p', 'pharos-note', `Ejecting stops ${stopping.join(', ')} at a safe point; ${stopping.length === 1 ? 'it resumes' : 'each resumes'} the next time it runs.`));
    if (activities.some(activity => activity.kind === 'capture-other')) row.append(node('p', 'pharos-note pharos-warn', 'Pharos cannot stop a capture by another process; the drive stays busy until it finishes.'));
    row.append(button(`Stop and Eject ${drive.name}`, 'primary', () => eject(drive)), button('Cancel', '', () => { ejectState = null; renderPanel(); }));
    return row;
  }

  async function eject(drive) {
    const handler = nativeLibrary();
    if (!handler) return;
    ejectState = {phase: 'ejecting', message: 'Releasing…'};
    renderPanel();
    try {
      await handler.postMessage({action: 'eject'});
      // The app now quits, and the drive ejects once it has.
      ejectState = {phase: 'ejecting', message: 'Ejecting…'};
    } catch (error) {
      ejectState = {phase: 'error', message: error?.message || String(error)};
    }
    renderPanel();
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
    const {index} = hostsData;
    const captured = index.hosts || [];
    renderSyncControls();

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

  function renderSyncControls() {
    if (!hostsData) return;
    const {index, capture} = hostsData;
    const updateAction = document.getElementById('syncUpdateAction');
    if (updateAction && !document.getElementById('pharosUpdateLibrary')) {
      const update = button('Capture & index', 'primary', captureAndIndex);
      update.id = 'pharosUpdateLibrary';
      updateAction.append(update);
    }
    const actions = document.getElementById('syncRunActions');
    if (actions && !document.getElementById('pharosCaptureHost')) {
      const captureButton = button('Capture this Mac', '', () => startCapture());
      captureButton.id = 'pharosCaptureHost';
      const indexAll = button('Index captured files', '', () => startIndex({all_hosts: true, only_needed: true}, 'captured files'));
      indexAll.id = 'pharosIndexAll';
      actions.append(captureButton, indexAll);
    }
    const needing = (index.hosts || []).filter(host => (host.sources || []).some(source => source.needs_index));
    const indexAll = document.getElementById('pharosIndexAll');
    if (indexAll) indexAll.title = capture.active ? 'Capture is running; index after it finishes' : index.sync_active ? 'Source indexing is running' : needing.length ? `Index captures from ${needing.map(host => host.label).join(', ')}; unchanged files and sessions are skipped` : 'Everything captured is indexed';
    renderHeaderAction();
    const activity = document.getElementById('syncActivity');
    activity?.replaceChildren();
    const update = libraryUpdate(status?.activities || []);
    let line;
    if (!hostsBusy && update) line = libraryUpdateLine(update);
    else if (headerCaptureIndexRunning) {
      line = node('div', 'pharos-run');
      line.append(node('strong', '', upgradeStatus?.running ? 'Upgrading the library…' : 'Starting capture & index…'), progressBar(null));
    } else line = runLine(capture, index);
    if (line) activity?.append(line);
    if (hostsError) activity?.append(node('p', 'pharos-error', hostsError));
    if (index.error && !index.hosts?.length) activity?.append(node('p', 'pharos-sub', `No captures yet (${index.error}).`));
  }

  // Only a known workload defines a bar; source counts are context, not time.
  const indexProgress = run => { const work = run.work?.size?.total > 0 ? run.work.size : run.work; return work?.total > 0 ? work.done / work.total : null; };
  const indexDetail = run => {
    if (run.work) return [run.work.source, run.work.phase, workCount(run.work), `${run.completed_sources} of ${run.total_sources} sources finished`].filter(Boolean).join(' · ');
    const current = run.current_source || run.phase || 'starting';
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
      const current = captureRun.results?.find(result => result.state === 'running');
      const progress = current?.phase === 'copying files' && current.bytes_planned > 0 ? current.bytes_copied / current.bytes_planned : null;
      box.append(node('strong', '', `Capturing ${capture.host?.label || 'this Mac'}`),
        node('div', 'pharos-sub', `${captureRun.current_source || 'starting'} · ${current?.phase || 'discovering files'} · ${captureRun.completed_sources} of ${captureRun.total_sources} sources · ${size(captureRun.bytes_copied)} copied`),
        progressBar(progress, 'Copying files for the current source'));
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

  // Index changes is the incremental run (Settings → Capture & index's
  // Index changes now): it indexes only what changed in this Mac's sources,
  // without capturing them.
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
    renderSyncControls();
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
        if (indexed?.state !== 'complete') toast(indexed?.error || 'Capture & index stopped; committed work is kept.');
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
      await Promise.all([refreshStatus(), loadHosts().catch(() => {})]);
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

  const syncOnly = document.getElementById('headerSyncOnly');
  if (syncOnly) syncOnly.onclick = syncNow;

  window.pharosLibrary = {closePanel, refresh: refreshStatus, status: () => status, runs, refreshSettings, refreshSyncControls: renderSyncControls, renderAlerts: () => { renderChip(); renderPanel(); }, onStatus: listener => listeners.add(listener), toggleActivityPanel: () => (panel && panelMode === 'activity' ? closePanel() : openPanel('activity')),
    // Pure helpers, for web/tests/unit/page/library.test.mjs.
    libraryUpdate, activityTiming, activityProgress, activityPresentation, estimateRate, workCount, headerBusy, captureIndexDisabled, stopTargets};
  const sheet = node('style');
  sheet.textContent = CSS;
  document.head.append(sheet);
  watchSettings();
  refreshStatus();
})();
