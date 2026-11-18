// Pharos library, drive and captures. GET /api/library/status says what holds
// or writes the library. This file owns the header's library controls (the
// drive chip, Index changes and the app update button); status-panel.js
// renders the chip and its panel. Settings → Sources shows captures from other
// Macs alongside this Mac's sources; Settings → Health gains the drive's
// checks; Backups holds backup checks and controls.
(() => {
  'use strict';
  const CSS = `
.pharos-library-controls{min-width:0;font-size:var(--fs-md,13px)}
.combo-button>.pharos-drive{gap:7px;min-width:0;max-width:190px;font-weight:400;line-height:1}
.combo-button>.pharos-app-update{flex:none;color:var(--combo-accent);background:color-mix(in srgb,var(--combo-accent) 14%,var(--combo-bg))}
.pharos-app-update[aria-disabled=true]{cursor:default}
.pharos-app-update[data-phase=available]::after{content:'';position:absolute;top:5px;right:5px;width:5px;height:5px;border-radius:50%;background:currentColor;box-shadow:0 0 0 2px var(--combo-bg)}
.pharos-update-ring{position:absolute;inset:3px;width:calc(100% - 6px);height:calc(100% - 6px);fill:none;stroke:currentColor;stroke-width:1.8;pointer-events:none}
.pharos-update-ring[hidden]{display:none}
.pharos-update-track{opacity:.2}
.pharos-update-arc{stroke-linecap:round}
.combo-button>.pharos-app-update[aria-busy=true] svg.app-icon{width:14px;height:14px;animation:none}
.pharos-app-update.is-indeterminate .pharos-update-ring{animation:header-spin 1.2s linear infinite}
@media(prefers-reduced-motion:reduce){.pharos-app-update.is-indeterminate .pharos-update-ring{animation:none}}
.pharos-library-controls>.combo-icon{flex:none}
.header-overflow-row:has(.pharos-app-update){flex-wrap:wrap}
.header-overflow-row:has(.pharos-app-update)>:last-child{min-width:0;max-width:100%}
.header-overflow-row .pharos-library-controls{max-width:100%}
.header-overflow-row .pharos-drive-name{min-width:0}
.pharos-drive-name{flex:0 1 auto;min-width:2.5em;font-weight:650;overflow:hidden;text-overflow:ellipsis}
.pharos-dot{flex:none;display:inline-block;width:8px;height:8px;border-radius:50%;background:var(--sea,#315845)}
.pharos-drive .pharos-dot{background:#8cc6a0}
.pharos-dot.busy{background:var(--mast-brass,#d4a857);box-shadow:0 0 0 3px color-mix(in srgb,var(--mast-brass,#d4a857) 30%,transparent)}
/* Pulses animate opacity only, with the outer ring on ::after. WebKit runs
   opacity on the compositor; animating box-shadow repaints and re-composites
   the whole page every frame, which made long transcripts sluggish. */
.pharos-drive .pharos-dot:is(.busy,.bad){position:relative}
.pharos-drive .pharos-dot:is(.busy,.bad)::after{content:'';position:absolute;inset:0;border-radius:50%;box-shadow:0 0 0 6px color-mix(in srgb,var(--mast-brass,#d4a857) 10%,transparent);opacity:0;animation:pharos-drive-ring 1.6s ease-in-out infinite}
.pharos-drive .pharos-dot.busy{animation:pharos-drive-pulse 1.6s ease-in-out infinite}
@keyframes pharos-drive-pulse{50%{opacity:.55}}
@keyframes pharos-drive-ring{50%{opacity:1}}
.pharos-drive .pharos-dot.bad{background:#e5675c;box-shadow:0 0 0 3px color-mix(in srgb,#e5675c 35%,transparent);animation:pharos-drive-alert 1.6s ease-in-out infinite}
.pharos-drive .pharos-dot.bad::after{box-shadow:0 0 0 6px color-mix(in srgb,#e5675c 12%,transparent)}
@keyframes pharos-drive-alert{50%{opacity:.6}}
@media(prefers-reduced-motion:reduce){.pharos-drive .pharos-dot:is(.busy,.bad),.pharos-drive .pharos-dot:is(.busy,.bad)::after{animation:none}}
.pharos-attention{margin-top:12px;padding:10px 12px;border:1px solid var(--bad,#9c3d36);border-radius:8px;background:color-mix(in srgb,var(--bad,#9c3d36) 8%,transparent)}
.pharos-drive-panel .pharos-attention h3{margin-top:0;color:var(--bad,#9c3d36)}
.pharos-attention-item+.pharos-attention-item{margin-top:10px;padding-top:10px;border-top:1px solid var(--line,#ccc)}
.pharos-dot.warn{background:var(--warn,#98601d)}.pharos-dot.bad{background:var(--bad,#9c3d36)}.pharos-dot.idle{background:var(--muted,#756c5f)}
.pharos-drive-panel{position:fixed;z-index:var(--z-panel,8000);width:min(430px,calc(100vw - 24px));max-height:calc(100vh - 100px);overflow:auto;padding:16px 18px;background:var(--panel,#fff);color:var(--ink,#222);border:1px solid var(--line,#ccc);border-radius:14px;box-shadow:0 18px 50px #0005;font-size:13px}
.pharos-drive-panel h2{margin:0;font:700 20px/1.2 var(--serif,serif)}
.pharos-panel-head{display:flex;justify-content:space-between;align-items:center;gap:12px}
.pharos-panel-head>h2{min-width:0;overflow-wrap:anywhere}
.pharos-panel-head>.pharos-button{flex:none}
.pharos-drive-panel h3{margin:14px 0 6px;font-size:12px;letter-spacing:.06em;text-transform:uppercase;color:var(--muted,#666)}
.pharos-drive-panel p{margin:8px 0}
.pharos-running-head{display:flex;justify-content:space-between;align-items:center;gap:12px}
.pharos-drive-panel>.pharos-running-head{margin-top:8px}
.pharos-running-head>div{min-width:0}
.pharos-running-head>.pharos-button{flex:none}
.pharos-sub{color:var(--muted,#666);overflow-wrap:anywhere}
.pharos-activity{padding:8px 0;border-bottom:1px solid var(--line,#ccc)}
.pharos-activity:last-child{border-bottom:0}
.pharos-bar{height:6px;margin:7px 0 4px;overflow:hidden;border-radius:6px;background:var(--line,#ccc)}
.pharos-bar>span{display:block;height:100%;background:var(--accent,#315845);transition:width .25s}
.pharos-bar.indeterminate>span{width:35%;animation:pharos-slide 1.6s ease-in-out infinite}
/* transform runs on the compositor; margin-left would lay out every frame. */
@keyframes pharos-slide{from{transform:translateX(-100%)}to{transform:translateX(286%)}}
@media(prefers-reduced-motion:reduce){.pharos-bar>span{transition:none}.pharos-bar.indeterminate>span{animation:none;width:100%;opacity:.5}}
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
.pharos-backup-locations{margin:0 0 10px;border-top:1px solid var(--line,#ccc)}
.pharos-backup-locations .pharos-check{grid-template-columns:18px minmax(0,1fr)}
.pharos-backup-locations .pharos-check-title{overflow-wrap:anywhere}
.pharos-backup-actions{display:flex;flex-wrap:wrap;gap:6px;margin-top:6px}
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
  // Reconcile the small status surfaces in place. Stable activity keys keep
  // progress tracks, disclosure state and focused controls alive across polls.
  // Do not detach unchanged nodes: doing so restarts CSS animations.
  const liveKey = n => n.nodeType === 1 ? n.dataset.key || n.id || `${n.nodeName}:${n.classList[0] || ''}` : n.nodeName;
  function updateLiveChildren(target, source) {
    const desired = [...source.childNodes], remaining = [...target.childNodes];
    const matches = desired.map(next => {
      const index = remaining.findIndex(current => current.nodeType === next.nodeType && current.nodeName === next.nodeName && liveKey(current) === liveKey(next));
      return index < 0 ? null : remaining.splice(index, 1)[0];
    });
    for (const obsolete of remaining) obsolete.remove();
    let cursor = target.firstChild;
    desired.forEach((next, index) => {
      const current = matches[index];
      if (!current) {
        target.insertBefore(next, cursor);
        return;
      }
      if (current !== cursor) {
        // moveBefore preserves browser state. Older Safari needs its CSS
        // animation phase and focused descendant restored after a move.
        if (target.moveBefore) target.moveBefore(current, cursor);
        else {
          const focused = current.contains(document.activeElement) ? document.activeElement : null;
          const animations = (current.getAnimations?.({subtree: true}) || []).filter(animation => animation.animationName).map(animation => ({
            target: animation.effect.target, name: animation.animationName, time: animation.currentTime, paused: animation.playState === 'paused',
          }));
          target.insertBefore(current, cursor);
          for (const saved of animations) {
            const animation = saved.target.getAnimations().find(animation => animation.animationName === saved.name);
            if (animation && saved.time != null) { animation.currentTime = saved.time; if (saved.paused) animation.pause(); }
          }
          if (focused && document.activeElement !== focused) focused.focus({preventScroll: true});
        }
      }
      if (current.nodeType === 1) {
        for (const attr of [...current.attributes]) if (!next.hasAttribute(attr.name)) current.removeAttribute(attr.name);
        for (const attr of next.attributes) if (current.getAttribute(attr.name) !== attr.value) current.setAttribute(attr.name, attr.value);
        current.onclick = next.onclick;
        updateLiveChildren(current, next);
      } else if (current.nodeValue !== next.nodeValue) current.nodeValue = next.nodeValue;
      cursor = current.nextSibling;
    });
  }
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
  const copy = async text => {
    try {
      await window.pharosHost.clipboard.writeText(text);
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
  let upgradeStatus = null;
  const listeners = new Set();
  window.addEventListener('pharos:upgrade-status', event => {
    const runningChanged = Boolean(upgradeStatus?.running) !== Boolean(event.detail.running);
    upgradeStatus = event.detail;
    renderHeaderAction();
    renderChip();
    if (runningChanged) refreshStatus();
  });
  const driveName = () => status?.drive?.name || 'the drive';

  async function refreshStatus() {
    clearTimeout(statusTimer);
    if (!statusRequest) {
      statusRequest = call('/api/library/status').then(value => { status = value; statusError = null; }, error => { statusError = error; })
        .finally(() => { statusRequest = null; });
    }
    await statusRequest;
    renderChip();
    renderHeaderAction();
    listeners.forEach(listener => listener(status));
    const busy = status && !status.idle;
    if (!document.hidden) statusTimer = setTimeout(refreshStatus, busy || statusError ? 2000 : 10000);
    return status;
  }
  document.addEventListener('visibilitychange', () => { if (!document.hidden) refreshStatus(); });

  let controls = null, chip = null, headerSync = null, headerCaptureIndexRunning = false;
  let appUpdate = null, appUpdateButton = null, appUpdateRequest = false;
  // The drive chip and Index changes share one combo button (ui.py's
  // .combo-button) in the configurable library slot.
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
    chip.onclick = () => window.pharosStatusPanel?.toggle();
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

  // Sparkle supplies only verified, installable releases through the native
  // bridge. Source builds and ordinary browser sessions have no update action.
  const nativeUpdatesAvailable = () => window.pharosHost.capabilities.updates.available;
  function appUpdatePresentation(state) {
    if (!state?.version || !['available', 'downloading', 'preparing', 'installing'].includes(state.phase)) return null;
    const busy = state.phase !== 'available';
    const progress = state.phase === 'downloading' && Number.isFinite(state.progress) ? Math.max(0, Math.min(100, Math.floor(state.progress))) : null;
    return {busy, phase: state.phase, progress,
      icon: state.phase === 'preparing' ? 'package' : state.phase === 'installing' ? 'restart' : 'download',
      title: state.phase === 'available' ? `Update Pharos to ${state.version}. Pharos installs it and restarts.`
        : state.phase === 'downloading' ? `Downloading Pharos ${state.version}${progress == null ? '' : ` (${progress}%)`}. Pharos will restart when ready.`
          : state.phase === 'preparing' ? `Verifying and preparing Pharos ${state.version}. Pharos will restart when ready.`
            : `Installing Pharos ${state.version} and restarting…`};
  }
  const updateGlyphs = {
    download: 'M12 3v11m-4-4 4 4 4-4M5 16v4h14v-4',
    package: 'm3 7 9-4 9 4v10l-9 4-9-4ZM3 7l9 4 9-4M12 11v10M7.5 5l9 4',
    restart: 'M20 9a8 8 0 1 0 0 6M20 3v6h-6',
  };
  const updateSVG = (tag, attributes) => {
    const element = document.createElementNS('http://www.w3.org/2000/svg', tag);
    for (const [key, value] of Object.entries(attributes)) element.setAttribute(key, value);
    return element;
  };
  function renderAppUpdate() {
    const view = nativeUpdatesAvailable() && appUpdatePresentation(appUpdate);
    if (!view) { appUpdateButton?.remove(); appUpdateButton = null; return; }
    if (!ensureHeaderAction()) return;
    if (!appUpdateButton) {
      appUpdateButton = node('button', 'combo-icon pharos-app-update');
      appUpdateButton.id = 'pharosAppUpdate';
      appUpdateButton.type = 'button';
      const glyph = updateSVG('svg', {class: 'app-icon', viewBox: '0 0 24 24', 'aria-hidden': 'true'});
      glyph.append(updateSVG('path', {}));
      const ring = updateSVG('svg', {class: 'pharos-update-ring', viewBox: '0 0 32 32', 'aria-hidden': 'true'});
      for (const kind of ['track', 'arc']) ring.append(updateSVG('circle', {
        class: `pharos-update-${kind}`, cx: '16', cy: '16', r: '13', pathLength: '100', transform: 'rotate(-90 16 16)',
      }));
      appUpdateButton.append(glyph, ring);
      appUpdateButton.onclick = async () => {
        if (appUpdateRequest || appUpdatePresentation(appUpdate)?.busy) return;
        appUpdateRequest = true;
        renderAppUpdate();
        try {
          // Native status events own state after this starts: an older reply
          // must not overwrite download progress that arrived in the meantime.
          await window.pharosHost.updates.install();
        } catch (error) { toast(error.message || 'Could not start the Pharos update.'); }
        finally { appUpdateRequest = false; await refreshAppUpdate(); }
      };
      headerSync.before(appUpdateButton);
    }
    appUpdateButton.querySelector('path').setAttribute('d', updateGlyphs[view.icon]);
    appUpdateButton.dataset.phase = view.phase;
    const busy = view.busy || appUpdateRequest;
    appUpdateButton.classList.toggle('is-indeterminate', busy && view.progress == null);
    const ring = appUpdateButton.querySelector('.pharos-update-ring');
    ring.toggleAttribute('hidden', !busy);
    ring.querySelector('.pharos-update-arc').setAttribute('stroke-dasharray', `${view.progress ?? 28} 100`);
    appUpdateButton.title = view.title;
    appUpdateButton.setAttribute('aria-label', view.title);
    appUpdateButton.setAttribute('aria-busy', String(busy));
    // Keep progress focusable and hoverable, while guarding the action above.
    appUpdateButton.setAttribute('aria-disabled', String(busy));
  }
  let appUpdateRevision = 0;
  async function refreshAppUpdate() {
    if (!nativeUpdatesAvailable()) return;
    const revision = appUpdateRevision;
    try {
      const state = await window.pharosHost.updates.status();
      if (revision === appUpdateRevision) { appUpdate = state; renderAppUpdate(); }
    } catch { /* Native update dialogs report update failures. */ }
  }
  window.addEventListener('pharos:app-update', event => {
    appUpdateRevision++;
    appUpdate = event.detail;
    renderAppUpdate();
  });
  document.addEventListener('visibilitychange', () => { if (!document.hidden) refreshAppUpdate(); });

  // status-panel.js owns Index changes' busy state; this makes sure the
  // button exists before it renders.
  function renderHeaderAction() {
    ensureHeaderAction();
    window.pharosStatusPanel?.renderHeader();
  }

  function renderChip() {
    if (!ensureChip()) return;
    renderHeaderAction();
    renderAppUpdate();
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
  let hostsData = null;

  async function loadHosts() {
    const [sources, index, capture, providers] = await Promise.all([call('/api/sources'), call('/api/index'), call('/api/capture'), window.pharosSourceProviders()]);
    hostsData = {sources, index, capture, providers};
    renderHosts();
    annotateCards();
    const running = index.active || index.sync_active || capture.active;
    return running;
  }

  function renderHosts() {
    const grid = document.getElementById('sourceGrid');
    if (!grid || !hostsData) return;
    const {index, providers} = hostsData;
    const captured = index.hosts || [];

    grid.querySelectorAll('.source-card.remote,.source-group-heading.remote').forEach(card => card.remove());
    if (captured.some(host => !host.current && host.sources?.length)) grid.append(node('h3', 'source-group-heading remote', 'Captured from other Macs'));
    for (const host of captured.filter(host => !host.current)) {
      for (const source of host.sources || []) {
        const card = node('article', 'source-card remote'), head = node('div', 'source-card-head'), title = node('div');
        card.dataset.host = host.id;
        card.dataset.source = source.name;
        const titleRow = node('div', 'source-title-row'); titleRow.append(node('h3', '', source.name));
        const info = window.pharosSourceInfoButton(source, providers); if (info) titleRow.append(info);
        title.append(titleRow, node('span', 'source-host', [host.label || host.id, host.user].filter(Boolean).join(' · ')));
        head.append(title);
        const path = node('div', 'source-path', source.path || 'No path recorded'), facts = node('dl', 'source-facts');
        for (const [label, value] of [['Last indexed', source.indexed_at], ['Last index attempt', source.last_attempt_at]]) {
          const item = node('dd');
          item.append(fullTime(value));
          facts.append(node('dt', '', label), item);
        }
        const captured = node('div', 'pharos-card-capture');
        captured.append(node('span', 'pharos-sub', 'Captured '), when(source.captured_at));
        if (source.captured_at && !source.capture_finished) captured.append(node('span', 'pharos-warn', ' · interrupted'));
        captured.append(node('span', source.needs_index ? 'pharos-warn' : 'pharos-sub', source.needs_index ? ` · needs index (${source.index_reason || 'new data'})` : ' · indexed'));
        captured.append(...sourceAccount(source.account));
        card.append(head, path, facts, captured);
        if (source.error) card.append(node('div', 'sync-note badtext', source.error));
        grid.append(card);
      }
    }
  }

  // Index changes is the incremental run (Settings → Background Processing'
  // Update everything now): it indexes only what changed in this Mac's sources,
  // without capturing them.
  async function syncNow() {
    if (headerCaptureIndexRunning) return;
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

  // Each of this Mac's source cards says when it was captured.
  function annotateCards() {
    const grid = document.getElementById('sourceGrid');
    if (!grid || !hostsData) return;
    const current = (hostsData.index.hosts || []).find(host => host.current);
    for (const card of grid.querySelectorAll('.source-card:not(.remote)')) {
      const name = card.dataset.source;
      const source = current?.sources.find(item => item.name === name);
      let line = card.querySelector('.pharos-card-capture');
      if (!line) {
        line = node('div', 'pharos-card-capture');
        card.insertBefore(line, card.querySelector('.source-removal'));
      }
      line.replaceChildren();
      const traits = sourceAccount(card.dataset.account);
      if (!source) {
        line.append(node('span', 'pharos-sub', `Not captured to ${driveName() === 'the drive' ? 'the library' : driveName()} yet`), ...traits);
        continue;
      }
      line.append(node('span', 'pharos-sub', 'Captured '), when(source.captured_at));
      line.append(node('span', source.needs_index ? 'pharos-warn' : 'pharos-sub', source.needs_index ? ` · needs index (${source.index_reason || 'new data'})` : ' · indexed'), ...traits);
    }
  }

  // A source's account follows its capture state on one line. Its coverage is
  // left out: "needs index" already covers an incomplete index.
  function sourceAccount(account) { return account ? [node('span', 'pharos-sub', ` · Account: ${account}`)] : []; }

  // ---- Settings → Health and Backups
  const CHECK_TITLES = {volume: 'Drive', encryption: 'Encryption', spotlight: 'Spotlight', filesystem: 'File system', location: 'Location', free_space: 'Free space', volume_pin: 'Volume pin', backup: 'Backup'};
  const CHECK_STATUS_WORDS = {problem: 'Problem', warning: 'Warning', info: 'Note', unknown: 'Unknown'};
  const STATUS_DOT = {ok: '', info: 'idle', unknown: 'idle', warning: 'warn', problem: 'bad'};
  let backupDraft = null, backupError = null, backupBusy = false;

  async function loadDriveHealth() {
    const [drive, backup] = await Promise.all([call('/api/health/drive'), call('/api/backup')]);
    renderDriveHealth(drive, backup);
    return drive.status === 'checking' || backup.active;
  }

  function renderDriveHealth(drive, backup) {
    window.pharosCloudBackup?.seedLibrary?.(drive.sizes?.library_bytes, drive.cloud_growth);
    const backupPage = document.getElementById('backupSettingsLocal');
    // Leave the form alone while someone is typing a destination.
    if (backupPage && document.activeElement?.id !== 'pharosBackupDestinationPage') {
      const backupChecks = (drive.checks || []).filter(check => check.id === 'backup');
      backupPage.replaceChildren();
      if (backupChecks.length) {
        const list = node('div', 'pharos-checks');
        list.append(...backupChecks.map(driveCheckItem));
        backupPage.append(list);
      }
      backupPage.append(backupPanel(backup, drive.volume?.name || 'the library’s drive'));
    }
    const cards = document.getElementById('healthCards');
    if (!cards) return;
    const driveHealthExpanded = window.pharosView?.read('disclosures', {})['library:drive-health'] ?? true;
    const checks = (drive.checks || []).filter(check => check.id !== 'backup');
    let section = document.getElementById('pharosDriveHealth');
    if (!section) {
      section = node('section', 'pharos-section');
      section.id = 'pharosDriveHealth';
      cards.after(section);
    }
    const volume = drive.volume || {};
    const name = volume.name || status?.drive?.name || 'Library drive';
    const head = node('div', 'pharos-section-head'), intro = node('div');
    const title = node('h3', '', drive.scope === 'archive_root' ? `Data drive · ${name}` : `Library drive · ${name}`);
    intro.append(title);
    const facts = [];
    if (volume.location) facts.push({external: 'external drive', 'disk-image': 'disk image', internal: 'this Mac’s internal disk'}[volume.location] || volume.location);
    if (volume.filesystem) facts.push(volume.filesystem.toUpperCase());
    if (drive.free_bytes != null) facts.push(`${size(drive.free_bytes)} free`);
    if (drive.sizes?.library_bytes) facts.push(`library ${size(drive.sizes.library_bytes)}`);
    if (drive.checked_at) facts.push(`checked ${ago(drive.checked_at)}`);
    intro.append(node('p', 'muted', facts.join(' · ') || drive.path || ''));
    const controls = node('div', 'pharos-health-actions');
    const attention = node('div', 'pharos-health-attention');
    const attentionChecks = checks.filter(check => check.status === 'warning' || check.status === 'problem');
    if (attentionChecks.length) {
      attention.append(node('span', 'pharos-health-attention-label', 'Needs attention:'));
      for (const check of attentionChecks) {
        const chip = button(CHECK_TITLES[check.id] || check.id, 'pharos-chip warn', () => {
          window.pharosView?.write('disclosures', {...window.pharosView.read('disclosures', {}), 'library:drive-health': true});
          section.querySelector(`[data-check="${check.id}"]`)?.scrollIntoView({block: 'center'});
        });
        attention.append(chip);
      }
      controls.append(attention);
    }
    const toggle = button('');
    toggle.dataset.viewKey = 'library:drive-health';
    toggle.dataset.viewDefault = 'true';
    const action = node('span', '', driveHealthExpanded ? 'Collapse' : 'Expand');
    action.setAttribute('data-view-action', '');
    toggle.append(action);
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
    list.append(...checks.map(driveCheckItem));
    details.append(list);
    section.append(details);
  }

  function driveCheckItem(check) {
    const item = node('div', 'pharos-check');
    item.dataset.check = check.id;
    item.append(node('span', 'pharos-dot ' + (STATUS_DOT[check.status] ?? 'idle')));
    const titleLine = node('div', 'pharos-check-title', CHECK_TITLES[check.id] || check.id);
    if (check.status !== 'ok') titleLine.append(node('span', check.status === 'problem' ? 'pharos-error' : check.status === 'warning' ? 'pharos-warn' : 'pharos-sub', ` · ${CHECK_STATUS_WORDS[check.status] || check.status}`));
    item.append(titleLine, node('div', 'pharos-sub', check.summary));
    if (check.recommendation) item.append(node('div', '', check.recommendation));
    if (check.action) item.append(node('div', 'pharos-sub', check.action));
    if (check.command) { const line = codeLine(check.command); item.append(line); }
    return item;
  }

  // Starts a backup or changes the list; the panel redraws from the result.
  async function backupAction(action) {
    backupBusy = true;
    try {
      await action();
    } catch (error) {
      toast(error.message);
    } finally {
      backupBusy = false;
      refreshSettings();
    }
  }

  function backupLocationOutcome(location) {
    const last = location.last_backup, run = location.last_run;
    const partial = Boolean(last?.partial || run?.state === 'partial');
    const missing = [...new Set([...(last?.partial ? last.missing || [] : []), ...(run?.state === 'partial' ? run.missing || [] : [])])];
    return {partial, missing};
  }

  function backupLocationItem(location, backup, latestRun) {
    const item = node('div', 'pharos-check');
    const running = backup.active && backup.run?.destination === location.path;
    const run = location.last_run, failed = !running && run?.state === 'failed';
    const outcome = backupLocationOutcome(location);
    const dot = failed ? 'bad' : outcome.partial ? 'warn' : !location.connected ? 'idle' : '';
    item.append(node('span', 'pharos-dot ' + dot), node('div', 'pharos-check-title', location.path));
    const line = node('div', 'pharos-sub');
    const last = location.last_backup;
    if (last) line.append(node('span', '', 'Last backup '), when(last.finished_at), node('span', '', ` · ${size(last.bytes_total)}`));
    else line.append(node('span', '', 'No finished backup yet'));
    if (running) line.append(node('span', '', ' · Backing up now'));
    else if (!location.connected) line.append(node('span', '', ' · Drive not connected'));
    item.append(line);
    if (outcome.partial) item.append(node('div', 'pharos-warn', `The last finished backup omitted ${outcome.missing.join(', ') || 'parts of the library'}; those sources could not be read. Any earlier copies were kept.`));
    // The latest run's errors are already listed in full below the locations.
    if (failed && run.id !== latestRun?.id) item.append(node('div', 'pharos-error', run.errors?.[0]?.error || 'The last backup here failed.'));
    const actions = node('div', 'pharos-backup-actions');
    const now = button('Back up now', '', () => backupAction(() => post('/api/backup', {destination: location.path})));
    const remove = button('Remove', '', () => backupAction(async () => {
      await post('/api/backup/locations/remove', {path: location.path});
      toast(`Pharos will no longer back up to ${location.path}. The backup already there was not deleted.`);
    }));
    now.disabled = backup.active || backupBusy;
    remove.disabled = running || backupBusy;
    now.setAttribute('aria-label', `Back up now to ${location.path}`);
    remove.setAttribute('aria-label', `Remove backup location ${location.path}`);
    actions.append(now, remove);
    item.append(actions);
    return item;
  }

  function backupPanel(backup, driveLabel) {
    const box = node('div', 'pharos-backup');
    box.id = 'pharosBackupPage';
    box.append(node('h3', '', 'Back up to other drives'));
    const locations = backup.locations || [];
    const last = backup.last_success;
    if (locations.length) {
      const list = node('div', 'pharos-backup-locations');
      list.append(...locations.map(location => backupLocationItem(location, backup, backup.run)));
      box.append(list);
      if (locations.filter(location => location.connected).length > 1) {
        const all = button('Back up all connected', '', () => backupAction(async () => {
          await post('/api/background/run', {item: 'backup'});
          // The pass starts from the queue, a moment after this answer.
          setTimeout(refreshSettings, 1000);
        }));
        all.disabled = backup.active || backupBusy;
        box.append(all);
      }
    } else if (last) {
      const line = node('p', '');
      line.append(node('span', '', 'Last backup '), when(last.finished_at), node('span', '', ` to ${last.destination} · ${size(last.bytes_total)} (${size(last.bytes_copied)} copied)`));
      box.append(line);
    } else {
      box.append(node('p', 'pharos-warn', `No backup yet: ${driveLabel} holds the only copy of the library.`));
    }
    const run = backup.run;
    if (backup.active && run) {
      box.append(node('div', '', `Backing up to ${run.destination} · ${run.phase || 'starting'} · ${plural(run.files_copied, 'file')} copied, ${plural(run.files_unchanged, 'file')} unchanged · ${size(run.bytes_copied)} copied`), progressBar(null, 'Backup progress'));
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
    const form = node('form'), label = node('label', '', locations.length ? 'Add a location' : 'Back up to'), input = node('input'), go = node('button', 'pharos-button primary', locations.length ? 'Add and back up' : 'Back up now');
    label.htmlFor = input.id = 'pharosBackupDestinationPage';
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
      error.id = 'pharosBackupErrorPage';
      error.setAttribute('role', 'alert');
      box.append(error);
    }
    box.append(node('p', 'pharos-sub', 'Choose a folder on another drive, or on a FileVault-protected Mac you trust. The library’s own drive is refused: a backup there would be lost with it. Each location holds its own full backup, and later backups copy only what changed.'));
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

  window.pharosLibrary = {setPanelOpen(open) {
    if (open) window.pharosStatusPanel?.open({fromURL: true, focus: false});
    else window.pharosStatusPanel?.close({fromURL: true});
  }, refresh: refreshStatus,
    // For status-panel.js: the chip and Index changes it renders, the
    // in-place renderer, and whether this page is starting an index.
    ensureChip, updateLiveChildren, localBusy: () => headerCaptureIndexRunning, status: () => status, runs, refreshSettings, refreshSyncControls: renderHeaderAction, onStatus: listener => listeners.add(listener),
    // A pure helper, for web/tests/unit/page/library.test.mjs.
    appUpdatePresentation, backupLocationOutcome};
  window.dispatchEvent(new Event('pharos:drive-view-ready'));
  const sheet = node('style');
  sheet.textContent = CSS;
  document.head.append(sheet);
  watchSettings();
  refreshAppUpdate();
  refreshStatus();
})();
