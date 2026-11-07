// The unified activity panel (docs/background-work.md §7–9, §11). It renders
// only from the status store (status-store.js) and is the one renderer of
// the header's drive chip, its panel and the header's Index changes state;
// library.js and sync.js leave those alone.
//
// The panel answers, in this order: what is happening and what it means for
// the pages (the schedule's headline); what runs now, with its progress and
// load; how each cadence (Frequently, Hourly, Daily) is doing, each opening
// to its items; what needs attention; and whether ejecting is safe, beside
// Eject. Every sentence comes from the server; this file adds ages, times
// and numbers.
//
// panelModel and chipModel are pure, for web/tests/unit/page/status-panel.test.mjs.
(() => {
  'use strict';
  const CSS = `
.pharos-status-panel{width:min(440px,calc(100vw - 24px))}
.pharos-status-eject{display:flex;gap:8px;align-items:baseline;margin:4px 0 12px;font-size:12px;color:var(--muted,#666)}
.pharos-status-headline{margin:12px 0 10px;padding:10px 12px;border-radius:8px;background:color-mix(in srgb,var(--ink,#000) 5%,transparent);border-left:3px solid #8cc6a0}
.pharos-status-headline.busy{border-left-color:var(--accent,#d8ae5e)}
.pharos-status-headline.behind{border-left-color:var(--warn,#f0955a)}
.pharos-status-headline.bad{border-left-color:var(--bad,#ef8c84)}
.pharos-status-headline.paused{border-left-color:var(--muted,#888)}
.pharos-status-headline strong{display:block;font-weight:650}
.pharos-status-headline p{margin:2px 0 0;font-size:13px;color:var(--muted,#666)}
.pharos-status-headline details{margin-top:6px;font-size:12px;color:var(--muted,#666);overflow-wrap:anywhere}
.pharos-status-headline details>summary{cursor:pointer}
.pharos-status-headline .pharos-button{margin-top:8px}
.pharos-status-now{margin:0 0 10px}
.pharos-status-heading{display:flex;align-items:baseline;margin:14px 0 6px;font-size:11px;font-weight:600;letter-spacing:.06em;text-transform:uppercase;color:var(--muted,#666)}
.pharos-status-heading a{margin-left:auto;font-size:12px;font-weight:400;letter-spacing:normal;text-transform:none}
.pharos-status-panel .pharos-attention-item>.pharos-sub{margin-top:4px;overflow-wrap:anywhere}
.pharos-status-line{display:block;overflow-wrap:anywhere}
.pharos-status-line+.pharos-status-line{margin-top:8px}
.pharos-status-line>strong{font-weight:650}
.pharos-status-line .pharos-status-time{color:var(--muted,#666);white-space:nowrap}
.pharos-status-step{display:block;font-size:12px}
.pharos-status-current{display:flex;align-items:baseline;justify-content:space-between;gap:8px}
.pharos-status-current strong{min-width:0;font-weight:650;overflow-wrap:anywhere}
.pharos-status-cadence{font-size:11px;color:var(--muted,#666);white-space:nowrap}
.pharos-status-position{margin:5px 0 3px}
.pharos-status-position-label{font-size:11px;color:var(--muted,#666)}
.pharos-status-position-track{display:grid;grid-template-columns:repeat(var(--step-count),minmax(0,1fr));gap:3px;margin-top:4px}
.pharos-status-position-track>i{height:5px;border-radius:3px;background:color-mix(in srgb,var(--ink,#000) 10%,transparent)}
.pharos-status-position-track>i.done{background:var(--accent,#d8ae5e)}
.pharos-status-position-track>i.active{background:color-mix(in srgb,var(--accent,#d8ae5e) 35%,transparent);box-shadow:inset 0 0 0 1px var(--accent,#d8ae5e)}
.pharos-status-bar{height:5px;border-radius:3px;background:color-mix(in srgb,var(--ink,#000) 10%,transparent);margin:5px 0 3px;overflow:hidden}
.pharos-status-bar>i{display:block;height:100%;background:var(--accent,#d8ae5e)}
.pharos-status-bar.indeterminate>i{width:30%;animation:pharos-status-progress 1.8s ease-in-out infinite}
.pharos-status-bar.waiting>i{animation:none;opacity:.5}
@keyframes pharos-status-progress{from{transform:translateX(-100%)}to{transform:translateX(340%)}}
@media(prefers-reduced-motion:reduce){.pharos-status-bar.indeterminate>i{animation:none;opacity:.5}}
.pharos-status-load{display:grid;grid-auto-flow:column;grid-auto-columns:minmax(0,1fr);margin-top:10px;padding-top:8px;border-top:1px solid var(--line,#ddd);font-size:11px;color:var(--muted,#666)}
.pharos-status-load>span{display:flex;flex-direction:column;align-items:center;gap:1px;min-width:0;padding:0 2px;white-space:nowrap}
.pharos-status-load>span:first-child{padding-left:0}
.pharos-status-load>span:last-child{padding-right:0}
.pharos-status-load>span+span{border-left:1px solid var(--line,#ddd)}
.pharos-status-load>span>span{display:inline-flex;align-items:center;gap:4px}
.pharos-status-load .app-icon{width:13px;height:13px;flex:none}
.pharos-status-load b{font-size:13px;font-weight:600;color:var(--ink,#000);font-variant-numeric:tabular-nums}
.pharos-status-history{margin-top:10px;padding-top:8px;border-top:1px solid var(--line,#ddd);font-size:11px;color:var(--muted,#666);outline-offset:2px}
.pharos-status-history-caption{display:flex;gap:6px;justify-content:space-between;margin-bottom:4px;min-width:0}
.pharos-status-history-caption>span{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.pharos-status-history-row{display:grid;grid-template-columns:32px minmax(0,1fr) 60px;gap:6px;align-items:center;height:20px}
.pharos-status-history-row>b{font-weight:600;color:var(--ink,#000);text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.pharos-status-history svg{display:block;width:100%;height:18px;overflow:visible}
.pharos-status-history .line{fill:none;stroke:var(--accent,#d8ae5e);stroke-width:1.5;stroke-linecap:round;stroke-linejoin:round;vector-effect:non-scaling-stroke}
.pharos-status-history .base{stroke:var(--line,#ddd);stroke-width:1;vector-effect:non-scaling-stroke}
.pharos-status-history .marker{stroke:color-mix(in srgb,var(--ink,#000) 22%,transparent);stroke-width:1;stroke-dasharray:2 2;vector-effect:non-scaling-stroke}
.pharos-status-history .marker.end{stroke-dasharray:1 3}
.pharos-status-history .cursor{stroke:var(--ink,#000);stroke-width:1;vector-effect:non-scaling-stroke}
.pharos-status-history .cursor.pinned{stroke-width:2}
.pharos-status-history-lane{position:relative;height:12px}
.pharos-status-history-lane>i{position:absolute;top:0;font-style:normal;font-size:10px;line-height:12px}
.pharos-status-history-readout{min-height:14px;margin-top:2px;overflow-wrap:anywhere}
.pharos-status-history-readout.hint{opacity:.75}
.pharos-status-sr{position:absolute;width:1px;height:1px;overflow:hidden;clip-path:inset(50%);white-space:nowrap}
@media(max-width:480px){.pharos-status-load{font-size:10px}.pharos-status-load b{font-size:11px}.pharos-status-load .app-icon{display:none}.pharos-status-load>span{padding:0 1px}.pharos-status-load:has(>span:nth-child(6)){font-size:9px}.pharos-status-load:has(>span:nth-child(6)) b{font-size:9px}}
/* Opacity only, with the ring on ::after: see .pharos-drive .pharos-dot.busy. */
.pharos-status-panel .pharos-dot.pulse{position:relative;animation:pharos-status-pulse 1.6s ease-in-out infinite}
.pharos-status-panel .pharos-dot.pulse::after{content:'';position:absolute;inset:0;border-radius:50%;box-shadow:0 0 0 5px color-mix(in srgb,var(--mast-brass,#d4a857) 10%,transparent);opacity:0;animation:pharos-status-ring 1.6s ease-in-out infinite}
@keyframes pharos-status-pulse{50%{opacity:.55}}
@keyframes pharos-status-ring{50%{opacity:1}}
@media(prefers-reduced-motion:reduce){.pharos-status-panel .pharos-dot.pulse,.pharos-status-panel .pharos-dot.pulse::after{animation:none}}
.pharos-status-cadences{border-top:1px solid var(--line,#ddd)}
.pharos-status-cadences details{border-bottom:1px solid var(--line,#ddd)}
.pharos-status-cadences summary{list-style:none;display:grid;grid-template-columns:18px minmax(0,auto) minmax(0,1fr);gap:2px 8px;align-items:baseline;padding:8px 0;cursor:pointer}
.pharos-status-cadences .pharos-status-run{display:inline-flex;align-items:center;justify-content:center;padding:0;min-height:0;width:18px;height:18px;font-size:9px;line-height:1;border-radius:4px;align-self:center}
.pharos-status-cadences summary::-webkit-details-marker{display:none}
.pharos-status-cadences .pharos-status-icon{color:var(--muted,#666)}
.pharos-status-cadences .pharos-status-name{font-weight:650}
.pharos-status-cadences .pharos-status-name{display:flex;align-items:baseline;gap:8px;white-space:nowrap}
.pharos-status-cadences .pharos-status-state{font-size:12px;min-width:0;text-align:right;color:var(--muted,#666);overflow-wrap:anywhere}
.pharos-status-cadences .pharos-status-what{grid-column:2/-1;font-size:12px;color:var(--muted,#666);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.pharos-status-cadences details[open] .pharos-status-what{display:none}
.pharos-status-items{list-style:none;margin:0 0 8px;padding:0 0 0 26px}
.pharos-status-items li{display:flex;gap:8px;align-items:baseline;padding:2px 0;font-size:13px}
.pharos-status-items li>span:last-child{margin-left:auto;text-align:right;color:var(--muted,#666);font-size:12px}
.pharos-status-items li>span:nth-child(2){flex:0 1 auto;min-width:0;overflow-wrap:anywhere}
.pharos-status-items li>span:last-child{flex:0 0 auto;max-width:65%}
.pharos-status-panel .pharos-dot.small{width:7px;height:7px;flex:none;margin-right:6px}
.pharos-status-panel .pharos-dot.paused{background:transparent;box-shadow:inset 0 0 0 2px var(--muted,#888)}
.pharos-status-others{margin-top:10px;font-size:12px;color:var(--muted,#666);overflow-wrap:anywhere}
.pharos-status-others>div+div{margin-top:2px}
.pharos-panel-head .pharos-sub a{color:inherit}
.pharos-status-actions{display:flex;gap:6px;flex:none}
.pharos-status-panel .pharos-panel-head{align-items:flex-start}
.pharos-status-panel .pharos-panel-head>div:first-child{min-width:0}
.pharos-status-icon-button{display:inline-flex;align-items:center;justify-content:center;width:30px;height:30px;padding:0}
.pharos-status-icon-button .app-icon{width:16px;height:16px}
.pharos-status-icon-button[aria-busy=true] .app-icon{animation:pharos-status-pulse 1.6s ease-in-out infinite}
@media(prefers-reduced-motion:reduce){.pharos-status-icon-button[aria-busy=true] .app-icon{animation:none}}`;

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
  const svgIcon = name => {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg'), use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    svg.setAttribute('class', 'app-icon');
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('aria-hidden', 'true');
    use.setAttribute('href', `#ph-icon-${name}`);
    svg.append(use);
    return svg;
  };
  // An icon-only button: its name is its accessible name and its tooltip.
  const iconButton = (icon, label, onclick, title = label) => {
    const b = button('', 'pharos-status-icon-button', onclick);
    b.append(svgIcon(icon));
    b.setAttribute('aria-label', label);
    b.title = title;
    return b;
  };

  // ---- numbers (the only words this file makes)
  const size = value => {
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let amount = Math.max(0, Number(value) || 0), power = 0;
    while (amount >= 1000 && power < units.length - 1) { amount /= 1000; power++; }
    return `${amount.toFixed(amount < 10 && power ? 1 : 0)} ${units[power]}`;
  };
  const count = value => Math.round(Number(value) || 0).toLocaleString('en-US');
  const unitName = (unit, amount) => amount === 1 ? unit : /[^aeiou]y$/.test(unit) ? `${unit.slice(0, -1)}ies` : /(s|x|ch|sh)$/.test(unit) ? `${unit}es` : `${unit}s`;
  // How long ago a time was: "just now", "4 min ago", "3 h ago", "2 d ago".
  function ago(value, now = Date.now()) {
    const seconds = Math.max(0, (now - Date.parse(value)) / 1000);
    if (!Number.isFinite(seconds)) return '';
    if (seconds < 60) return 'just now';
    if (seconds < 3600) return `${Math.floor(seconds / 60)} min ago`;
    if (seconds < 86400 * 2) return `${Math.floor(seconds / 3600)} h ago`;
    return `${Math.floor(seconds / 86400)} d ago`;
  }
  // An age without "ago", for "behind 3 h".
  const span = (value, now = Date.now()) => {
    const text = ago(value, now);
    return text === 'just now' ? '<1 min' : text.replace(/ ago$/, '');
  };

  // When something next happens: "now", "in 3 min", "at 5:00 PM",
  // "tomorrow at 3:00 AM".
  function until(value, now = Date.now()) {
    const time = Date.parse(value);
    if (!Number.isFinite(time)) return '';
    const seconds = (time - now) / 1000;
    if (seconds < 60) return 'now';
    if (seconds < 3600) return `in ${Math.round(seconds / 60)} min`;
    const clock = new Date(time).toLocaleTimeString([], {hour: 'numeric', minute: '2-digit'});
    const day = date => new Date(date).toDateString();
    if (day(time) === day(now)) return `at ${clock}`;
    return day(time) === day(now + 86400000) ? `tomorrow at ${clock}` : `${new Date(time).toLocaleDateString([], {weekday: 'short'})} at ${clock}`;
  }

  // A cadence's or an item's state: the server's label, then its age or
  // its next time (docs/background-work.md §11).
  function scheduleText(entry, now = Date.now()) {
    if (!entry) return '';
    const label = entry.state_label || '';
    const next = entry.next_at ? until(entry.next_at, now) : '';
    switch (entry.state) {
      case 'current': return [entry.as_of ? `${label} ${ago(entry.as_of, now)}` : label, next ? `next ${next}` : ''].filter(Boolean).join(' · ');
      case 'failed': return next ? `${label} · retrying ${next}` : label;
      case 'missed': case 'never': return next ? `${label} · runs ${next}` : label;
      case 'paused': case 'manual': return entry.as_of ? `${label} · ${ago(entry.as_of, now)}` : label;
      default: return label;
    }
  }
  // Running pulses, as the header chip's dot does; queued (Up next) is
  // steady.
  const SCHEDULE_DOTS = {current: '', running: 'busy pulse', queued: 'busy', failed: 'bad', missed: 'warn', never: 'warn', paused: 'paused', manual: 'paused', unavailable: 'paused', power: 'paused'};
  const CADENCE_ICONS = {frequent: '↻', hourly: '◷', daily: '☾', manual: '⚐'};
  const ROUTINE_TONES = new Set(['good', 'busy']);

  // The schedule's part of the panel: its headline, one row per cadence
  // (open when something in it is happening or wrong). Null when the
  // snapshot has no schedule.
  function scheduleModel(background, now = Date.now()) {
    if (!background || !Array.isArray(background.cadences)) return null;
    const items = Object.fromEntries((background.items || []).map(item => [item.id, item]));
    const rows = background.cadences.map(row => {
      const members = (row.items || []).map(id => items[id]).filter(Boolean);
      return {id: row.id, label: row.label, icon: CADENCE_ICONS[row.id] || '', dot: SCHEDULE_DOTS[row.state] ?? '', text: scheduleText(row, now),
        open: ['running', 'queued', 'failed', 'missed'].includes(row.state), what: members.map(item => item.label).join(', '),
        run: {cadence: row.id, disabled: row.state === 'running'},
        items: members.map(item => scheduleItem(item, scheduleText(item, now)))};
    });
    // Ad hoc: the items set to only when you ask (Back up, unless chosen),
    // on no schedule, as one more row, run as the manual cadence.
    const adhoc = (background.unscheduled || []).map(id => items[id]).filter(item => item && item.state !== 'disabled');
    if (adhoc.length) {
      const running = adhoc.some(item => item.state === 'running'), failed = adhoc.some(item => item.state === 'failed');
      rows.push({id: 'manual', label: 'Ad hoc', icon: CADENCE_ICONS.manual, dot: running ? SCHEDULE_DOTS.running : failed ? 'bad' : 'paused',
        text: running ? 'Running now' : 'When you ask', open: running || failed, what: adhoc.map(item => item.label).join(', '),
        run: {cadence: 'manual', disabled: running || adhoc.every(item => item.state === 'unavailable')},
        items: adhoc.map(item => scheduleItem(item, ['running', 'failed', 'unavailable'].includes(item.state) ? scheduleText(item, now)
          : item.as_of ? `Last run ${ago(item.as_of, now)}` : 'Never run'))});
    }
    // The headline only when something is out of the ordinary (failed,
    // behind, paused, waiting for power): Up to date and the running item
    // are already in the rows below, and what a running item feeds is its
    // tooltip.
    const headline = background.headline || {};
    const routine = !headline.title || ROUTINE_TONES.has(headline.tone || 'good');
    return {headline: routine ? null : {tone: headline.tone, title: headline.title, text: headline.text || '', tech: headline.tech || '',
      action: headline.action?.path ? {label: headline.action.label || '', path: headline.action.path, body: headline.action.body || {}} : null},
      rows};
  }
  const scheduleItem = (item, text) => ({id: item.id, label: item.label, dot: SCHEDULE_DOTS[item.state] ?? '', text,
    tooltip: item.state === 'running' && item.feeds ? `${item.feeds} update when this finishes.` : ''});

  // A step's measured progress: "812 MB of 2 GB", "42 of 100 conversations".
  function stepAmount(step) {
    if (!step?.unit) return '';
    if (!(typeof step.total === 'number' && step.total >= 0)) return step.unit === 'byte' ? `${size(step.done)} processed`
      : `${count(step.done)} ${unitName(step.unit, step.done)} processed`;
    return step.unit === 'byte' ? `${size(step.done)} of ${size(step.total)}`
      : `${count(step.done)} of ${count(step.total)} ${unitName(step.unit, step.total)}`;
  }

  const TERMINAL = new Set(['succeeded', 'failed', 'cancelled', 'interrupted', 'skipped']);
  const running = operation => operation && !TERMINAL.has(operation.state);
  const RANK = {safe: 0, 'safe-in': 1, busy: 2, unsafe: 3};
  const VERDICT_DOT = {safe: '', 'safe-in': 'busy', busy: 'warn', unsafe: 'bad'};
  // Work that the header's Index changes waits for, as headerBusy in
  // library.js does for activities: captures, indexes and the upgrade.
  const INDEX_KINDS = /^(index\.|capture\.|ingest\.|library\.(update|rebuild|upgrade)$|host\.add$)/;

  // One line per top-level operation. The primary part is label · the state
  // word when not running (and not just the label again) · step N of M · the
  // measured amount · a time range when the server gives one (only while
  // estimating, or early and marked so). A background pass instead leads
  // with its current task and shows its position among the planned tasks.
  // service is false for the app's own operations, whose records are not
  // in the library service's ledger.
  function operationLine(operation, now = Date.now(), {service = true} = {}) {
    const primary = [], secondary = [];
    const pass = operation.kind === 'background.update';
    // A pass may be inside a chain stage, and authorship has its own child
    // rebuild. Its current work and measured amount belong on the Now row.
    let current = operation;
    const path = [current];
    for (let child; (child = current.children?.find(running));) {
      current = child;
      path.push(current);
    }
    // Measurements belong to the active child. A stop or wait anywhere on
    // its path still governs the operation, even when that child is running.
    const state = path.find(entry => entry.state === 'stopping')
      || path.find(entry => entry.state === 'blocked')
      || path.find(entry => entry.state === 'queued') || current;
    if (state.state !== 'running' && state.state_label && state.state_label !== state.label) primary.push(state.state_label);
    const step = current.step;
    const planned = operation.step;
    const position = pass && Number.isInteger(planned?.index) && Number.isInteger(planned?.count) && planned.index > 0 && planned.index <= planned.count
      ? {index: planned.index, count: planned.count} : null;
    const positionStep = planned?.count > 0 ? planned : step;
    if (!pass && positionStep?.count > 0 && positionStep.index > 0) primary.push(`step ${positionStep.index} of ${positionStep.count}`);
    const amount = stepAmount(step);
    if (amount) primary.push(amount);
    const origin = operationOrigin(operation);
    if (origin) secondary.push(origin);
    if (!pass && current !== operation) secondary.push(current.label);
    const name = step?.name && step.name !== operation.label && step.name !== current.label ? step.name : '';
    if (name && (!pass || current !== operation)) secondary.push(name);
    if (step?.source && step.source !== name) secondary.push(step.source);
    if (step?.detail) secondary.push(step.detail);
    if (state.state !== 'running' && state.detail) secondary.push(state.detail);
    if (current.started_at) secondary.push(`${span(current.started_at, now)} elapsed`);
    const fraction = Number.isFinite(step?.fraction) ? step.fraction : null;
    const progressLabel = amount || name || current.verb || current.state_label || 'Working…';
    if (!step && !(state.state !== 'running' && state.detail)) secondary.push(current.verb || 'Preparing…');
    const tooltip = [...new Set(path.map(entry => entry.detail).filter(Boolean)), operation.on_eject].filter(Boolean).join('\n');
    const measured = current.resources ? current : operation;
    const resources = taskResourceModel(measured.resources);
    return {id: operation.id, label: pass ? (current !== operation ? current.label : name || operation.label) : operation.label, cadence: pass ? CADENCE_NAMES[operation.variant] || '' : '',
      position, primary, secondary, time: state.state === 'running' ? operation.time?.label || '' : '', tooltip,
      fraction, progressLabel, currentLabel: current.label, waiting: state.state !== 'running',
      state: state.state, background: operation.priority === 'background', resources,
      export: resources && service && measured.kind ? {path: resourceExportPath(measured.kind), file: `pharos-${measured.kind}-resources.json`} : null};
  }
  // The schedule cadence is shown beside a background pass's current task;
  // other work says who started it.
  const CADENCE_NAMES = {frequent: 'Frequently', hourly: 'Hourly', daily: 'Daily'};
  function operationOrigin(operation) {
    if (operation.kind === 'background.update') return '';
    return {user: 'Started by you', schedule: 'Scheduled'}[operation.trigger] || '';
  }

  // A line as one string, for tooltips and the other libraries' rows.
  const lineText = line => [[line.label, ...(line.cadence ? [line.cadence] : []), ...line.primary, ...line.secondary].join(' · '), line.time].filter(Boolean).join(' ');

  // The chip's and the panel's name: the drive's, else Library for a
  // portable library on this Mac's disk, else This Mac.
  const driveName = view => {
    const library = view?.library?.library || {};
    const drive = library.drive || {};
    if (!view?.library) return view?.service?.name || 'Library';
    return drive.ejectable ? drive.name || 'Library' : library.portable ? 'Library' : 'This Mac';
  };
  const PANEL_TITLE = 'Background Processing';
  const baseName = path => String(path || '').replace(/\/+$/, '').split('/').pop() || '';
  const LOCATIONS = {external: 'External drive', 'disk-image': 'Disk image', internal: "This Mac's internal disk"};

  // What runs for the selected library: the app's operations that touch it
  // (a transfer, a delivery), then its own.
  const currentOperations = view => [...(view?.appOperations || []), ...(view?.library?.operations || [])].filter(running);
  // Task kinds' names, from the operations in a snapshot and their stages.
  function operationLabels(view, into = {}) {
    const visit = operation => {
      if (!operation || typeof operation !== 'object') return;
      if (operation.kind && operation.label) into[operation.kind] = operation.label;
      for (const child of operation.children || []) visit(child);
    };
    for (const operation of [...(view?.appOperations || []), ...(view?.library?.operations || [])]) visit(operation);
    return into;
  }
  const operationStartTime = operation => {
    const time = Date.parse(operation.started_at);
    return Number.isFinite(time) ? time : Infinity;
  };

  // The panel's content, from the store's view. options.ejectState is the
  // panel's eject flow; options.verdict overrides the snapshot's verdict
  // (the debounced one, see settleVerdict); options.stopRequested holds the
  // operations Stop was pressed for.
  // options.labels names task kinds for the history's markers (see
  // operationLabels).
  function panelModel(view, {ejectState = null, verdict = null, unavailable = null, nativeEject = false, stopRequested = new Set(), history = null, labels = operationLabels(view), now = Date.now()} = {}) {
    const library = view?.library || null;
    const operations = currentOperations(view);
    const appIds = new Set((view?.appOperations || []).map(operation => operation.id));
    // Oldest first, so new work joins the end and child stages do not move
    // their parent's row. IDs break ties consistently across snapshots;
    // operations without a known start follow those with one.
    const shown = operations.slice().sort((a, b) => operationStartTime(a) - operationStartTime(b) || a.id.localeCompare(b.id));
    const lines = shown.map(operation => operationLine(operation, now, {service: Boolean(library) && !appIds.has(operation.id)}));
    const common = {lines, attention: (view?.attention || []).map(attentionModel)};
    if (!library) {
      const load = loadModel(view?.resources ?? view?.service?.resources, view?.lastLibrarySizes?.storage, view?.lastLibrarySizes?.wal, Boolean(view?.lastLibrarySizes));
      return {available: false, title: PANEL_TITLE, library: view?.service?.name ? {name: view.service.name, drive: '', path: ''} : null, ...common, stateLabel: view?.service?.stateLabel || null,
        load, history: load ? historyModel(history, {labels}) : null, profile: null,
        message: operations.length ? null : unavailable || (view?.service?.error ? `${view.service.error}` : null)};
    }
    const drive = library.library?.drive || {};
    const eject = verdict || library.eject || {};
    const stoppable = operations.filter(operation => operation.stoppable && operation.stop?.path);
    const requested = stoppable.length > 0 && stoppable.every(operation => stopRequested.has(operation.id));
    const stopping = operations.some(operation => operation.state === 'stopping');
    const text = eject.headline || eject.summary || '';
    return {
      available: true, ...common,
      title: PANEL_TITLE,
      // "Library Pharos on euclid": the library (the app's name for it, else
      // its folder) and the drive it is on; the path in the tooltip.
      library: {name: view.service?.name || baseName(library.library?.path) || 'Library', drive: drive.ejectable ? drive.name || 'its drive' : 'this Mac',
        of: view.service?.count > 1 ? `${view.service.position}/${view.service.count}` : '',
        path: [LOCATIONS[drive.location] || (drive.ejectable ? 'Drive' : ''), library.library?.path].filter(Boolean).join(' · ')},
      verdict: {verdict: eject.verdict || 'unsafe', text, dot: VERDICT_DOT[eject.verdict] ?? 'bad', checked: eject.checked || null,
        tooltip: [eject.summary !== text ? eject.summary : '', ...(eject.blockers || []).map(blocker => [blocker.label, blocker.on_eject].filter(Boolean).join(': '))].filter(Boolean).join('\n')},
      // Null without a schedule (an older service).
      schedule: scheduleModel(library.background, now),
      load: loadModel(view.resources ?? library.resources, library.library?.storage_bytes, library.library?.wal_bytes),
      // The history strip sits above the Load row, in the same scope.
      history: historyModel(history, {labels}),
      // A deliberate, short CPU profile of the service process, offered
      // only beside a service that keeps task records.
      profile: lines.some(line => line.export) ? {path: PROFILE_PATH, file: 'pharos-service-cpu-5s.pprof'} : null,
      // Other libraries are only counted, in the library line ("1/3"); an
      // app operation touching no library is listed by its own label.
      others: (view.others || []).filter(other => String(other.id).startsWith('app:')).map(other => {
        const first = (other.operations || []).find(running);
        return {id: other.id, name: other.name || 'Library',
          text: first ? lineText(operationLine(first)) : other.stateLabel || other.eject?.headline || '', tooltip: first?.detail || other.eject?.summary || ''};
      }),
      stop: stoppable.length ? {targets: stoppable.map(operation => ({id: operation.id, method: operation.stop.method || 'POST', path: operation.stop.path, body: operation.stop.body || {id: operation.id}})),
        stopping: requested, title: stoppable.map(operation => operation.label).join(', ')} : stopping ? {targets: [], stopping: true, title: ''} : null,
      eject: drive.ejectable && nativeEject ? ejectModel(ejectState, drive, eject) : null,
    };
  }

  // The Load row (docs/background-work.md §10): CPU, GPU, memory and disk in
  // one row (in the app, every Pharos process on this Mac). CPU is % of one core,
  // as Activity Monitor shows it; I/O is what the processes read and write
  // per second, and Storage what the library keeps on its drive (from the
  // last drive check). A figure not measured yet is a dash, and I/O stays
  // out for a service that does not measure it. GPU uses counters from
  // the same Pharos process tree; WAL is shown only above 1 GB.
  function loadModel(resources, storage = null, wal = null, sizesStale = false) {
    if (!resources || typeof resources !== 'object') return null;
    const known = Number.isFinite(resources.cpu_percent) || Number.isFinite(resources.memory_bytes) || resources.label;
    if (!known) return null;
    const metrics = [
      {id: 'cpu', label: 'CPU', value: Number.isFinite(resources.cpu_percent) ? `${Math.round(resources.cpu_percent)}%` : '—'},
    ];
    const gpu = resources.gpu;
    if (gpu) metrics.push({id: 'gpu', icon: 'cpu', label: 'GPU', value: Number.isFinite(gpu.utilization_percent) ? `${Math.round(gpu.utilization_percent)}%` : '—'});
    metrics.push({id: 'memory', label: 'RAM', value: Number.isFinite(resources.memory_bytes) ? size(resources.memory_bytes) : '—'});
    if ('disk_bytes_per_second' in resources) metrics.push({id: 'io', label: 'I/O', value: Number.isFinite(resources.disk_bytes_per_second) ? `${size(resources.disk_bytes_per_second)}/s` : '—'});
    metrics.push({id: 'disk', label: 'Storage', value: Number.isFinite(storage) ? `${sizesStale ? 'last ' : ''}${size(storage)}` : '—'});
    if (Number.isFinite(wal) && wal > 1e9) metrics.push({id: 'wal', icon: 'disk', label: 'WAL', value: `${sizesStale ? 'last ' : ''}${size(wal)}`});
    for (const metric of metrics) {
      const partial = metric.id === 'cpu' ? resources.cpu_partial : metric.id === 'gpu' ? gpu?.partial : metric.id === 'io' ? resources.disk_partial : false;
      if (partial && metric.value !== '—') {
        metric.value = '≥' + metric.value;
        metric.warning = 'Some processes or clients are still waiting for another sample; this reading may undercount usage.';
      }
    }
    return {metrics};
  }

  // ---- resource history (docs/work-resource-accounting.md)
  // Four separate series on one time axis, each in its own unit: CPU as % of
  // one core (it can exceed 100%), GPU as the driver's GPU-time share, RAM
  // as physical footprint and I/O as bytes per second. A missing reading, a
  // segment boundary (start, restart, gap) or a sample interval longer than
  // HISTORY_BREAK_MS breaks the line; nothing is interpolated or drawn as
  // zero. GPU samples keep their own times.
  const HISTORY_SERIES = [
    {id: 'cpu', label: 'CPU', source: 'cpu', unit: '% of one core'},
    {id: 'gpu', label: 'GPU', source: 'gpu', unit: 'driver GPU-time share'},
    {id: 'memory', label: 'RAM', source: 'memory', unit: 'physical footprint'},
    {id: 'io', label: 'I/O', source: 'disk', unit: 'read and written per second'},
  ];
  const HISTORY_BREAK_MS = 25000;
  const HISTORY_WIDTH = 300, HISTORY_HEIGHT = 18;
  const round1 = value => Math.round(value * 10) / 10;
  const historyText = (id, value) => !Number.isFinite(value) ? '—' : id === 'cpu' || id === 'gpu' ? `${Math.round(value)}%` : id === 'memory' ? size(value) : `${size(value)}/s`;
  function historyColumns(history, id) {
    const samples = history.samples || {};
    if (id === 'gpu') return {t: history.gpu?.t || [], value: history.gpu?.value || [], partial: history.gpu?.partial || []};
    if (id === 'memory') return {t: samples.t || [], value: samples.memory || [], partial: []};
    if (id === 'io') return {t: samples.t || [], value: samples.disk || [], partial: samples.disk_partial || []};
    return {t: samples.t || [], value: samples.cpu || [], partial: samples.cpu_partial || []};
  }
  const historyMinutes = history => Math.round((Number(history.window_seconds) || 900) / 60);

  // historyModel turns a history document into the strip's geometry and
  // text. Null when there is no history (classic execution, an older
  // service), so the panel draws nothing.
  function historyModel(history, {width = HISTORY_WIDTH, height = HISTORY_HEIGHT, labels = {}} = {}) {
    if (!history?.available || !history.samples?.t?.length) return null;
    const end = Number(history.now) || history.samples.t.at(-1);
    const span = (Number(history.window_seconds) || 900) * 1000;
    const start = end - span;
    const x = time => round1(Math.max(0, Math.min(1, (time - start) / span)) * width);
    const breaks = (history.segments || []).map(segment => segment.t);
    const minutes = historyMinutes(history);
    const series = [];
    for (const spec of HISTORY_SERIES) {
      if (history.series?.[spec.source]?.available === false || (spec.id === 'gpu' && !history.gpu)) continue;
      const columns = historyColumns(history, spec.id);
      const points = columns.t.map((time, index) => ({t: time, value: Number.isFinite(columns.value[index]) ? columns.value[index] : null, partial: Boolean(columns.partial[index])}))
        .filter(point => point.t >= start && point.t <= end);
      const known = points.filter(point => point.value !== null);
      const peak = known.length ? Math.max(...known.map(point => point.value)) : null;
      // CPU's scale starts at one core so idle work does not fill the row.
      const top = spec.id === 'gpu' ? 100 : Math.max(spec.id === 'cpu' ? 100 : spec.id === 'io' ? 1e6 : 1, peak ?? 0);
      const y = value => round1(height - 1 - Math.max(0, Math.min(1, value / top)) * (height - 2));
      const runs = [];
      let run = null, previous = null;
      for (const point of points) {
        if (point.value === null) { run = null; previous = point; continue; }
        const broken = !run || !previous || point.t - previous.t > HISTORY_BREAK_MS || breaks.some(at => at > previous.t && at <= point.t);
        if (broken) runs.push(run = []);
        run.push([x(point.t), y(point.value)]);
        previous = point;
      }
      // A lone sample is a short dash, so it is not lost.
      const paths = runs.map(line => line.length === 1
        ? `M${round1(line[0][0] - 1)} ${line[0][1]}L${round1(line[0][0] + 1)} ${line[0][1]}`
        : `M${line.map(([px, py]) => `${px} ${py}`).join('L')}`);
      const latest = points.at(-1);
      const current = latest ? historyText(spec.id, latest.value) : '—';
      const partial = points.some(point => point.partial);
      const gaps = runs.length > 1 || points.some(point => point.value === null);
      const summary = `${spec.label} (${spec.unit}): now ${current}${peak !== null ? `, peak ${historyText(spec.id, peak)}` : ''} over the last ${minutes} min`
        + `${gaps ? ', with gaps' : ''}${partial ? ', some samples are lower bounds' : ''}.`;
      series.push({id: spec.id, label: spec.label, unit: spec.unit, current: latest?.partial && latest.value !== null ? `≥${current}` : current,
        peak: peak !== null ? historyText(spec.id, peak) : null, paths, gaps, partial, summary, basis: history.series?.[spec.source]?.basis || ''});
    }
    const coverage = (history.coverage || []).at(-1) || null;
    const covered = coverage?.covered?.length ?? null;
    const omitted = coverage?.omitted || [];
    const markers = (history.markers || []).filter(marker => marker.t >= start && marker.t <= end)
      .map(marker => ({x: x(marker.t), t: marker.t, event: marker.event === 'end' ? 'end' : 'start', kind: marker.kind, id: marker.id, observed: Boolean(marker.observed),
        text: markerText(history, marker, labels)}));
    const starts = markers.filter(marker => marker.event === 'start').length, ends = markers.length - starts;
    const scope = history.scope || '';
    const caption = [`Last ${minutes} min`, scope, covered !== null ? `${covered} ${unitName('process', covered)}` : ''].filter(Boolean).join(' · ');
    return {scope, caption, omitted, note: omitted.join('\n'), start, end, width, height, series, markers, labels,
      summary: [caption + '.', ...series.map(entry => entry.summary), markers.length ? `Task markers: ${starts} ${unitName('start', starts)}, ${ends} ${unitName('end', ends)}.` : '',
        omitted.length ? `Not measured: ${omitted.join('; ')}.` : ''].filter(Boolean).join(' ')};
  }

  // A marker in words. A start is the task's own start time. An end the
  // collector observed is the first sample that no longer saw the task, so
  // it ended after the sample before it; an end its owner recorded is exact.
  const historyClock = time => new Date(time).toLocaleTimeString([], {hour: 'numeric', minute: '2-digit', second: '2-digit'});
  const kindLabel = (marker, labels) => marker.label || labels[marker.kind] || marker.kind || 'a task';
  function markerText(history, marker, labels = {}) {
    const label = kindLabel(marker, labels);
    if (marker.event !== 'end') return `${label} ${marker.observed ? 'start seen' : 'started'} ${historyClock(marker.t)}`;
    if (!marker.observed) return `${label} ended ${historyClock(marker.t)}`;
    const before = (history.samples?.t || []).filter(time => time < marker.t).at(-1);
    return before !== undefined && marker.t - before <= HISTORY_BREAK_MS
      ? `${label} ended between ${historyClock(before)} and ${historyClock(marker.t)}` : `${label} end seen ${historyClock(marker.t)}`;
  }

  // historyInspect is what the strip shows at a time: each series' nearest
  // sample (within HISTORY_BREAK_MS), the coverage then, and the tasks that
  // were running. Running at the same time is overlap, not attribution.
  // Markers near that time are named too.
  function historyInspect(history, time, {labels = {}} = {}) {
    if (!history?.available || !Number.isFinite(time)) return null;
    const values = [];
    let at = null;
    for (const spec of HISTORY_SERIES) {
      if (history.series?.[spec.source]?.available === false || (spec.id === 'gpu' && !history.gpu)) continue;
      const columns = historyColumns(history, spec.id);
      let best = -1;
      for (let index = 0; index < columns.t.length; index++) {
        if (best < 0 || Math.abs(columns.t[index] - time) < Math.abs(columns.t[best] - time)) best = index;
      }
      const near = best >= 0 && Math.abs(columns.t[best] - time) <= HISTORY_BREAK_MS;
      const value = near && Number.isFinite(columns.value[best]) ? columns.value[best] : null;
      if (near && spec.id === 'cpu') at = columns.t[best];
      values.push({id: spec.id, label: spec.label, value, text: `${near && columns.partial[best] && value !== null ? '≥' : ''}${historyText(spec.id, value)}`});
    }
    at ??= time;
    const coverage = (history.coverage || []).filter(entry => entry.t <= at).at(-1) || null;
    const running = new Map();
    const markers = [...(history.markers || [])].sort((a, b) => a.t - b.t);
    for (const marker of markers) {
      if (marker.t > at) break;
      if (marker.event === 'start') running.set(marker.id, marker);
      else running.delete(marker.id);
    }
    const tasks = [...new Set([...running.values()].map(marker => marker.kind))];
    const names = [...new Set([...running.values()].map(marker => kindLabel(marker, labels)))];
    // Markers within a step of the inspected time are named, start or end.
    const reach = Math.max(2000, (Number(history.window_seconds) || 900) * 10);
    const marks = markers.filter(marker => Math.abs(marker.t - at) <= reach).map(marker => markerText(history, marker, labels));
    const text = [historyClock(at), ...values.map(entry => `${entry.label} ${entry.text}`), names.length ? `running then: ${names.join(', ')}` : '', ...marks].filter(Boolean).join(' · ');
    return {t: at, values, tasks, marks, covered: coverage?.covered?.length ?? null, omitted: coverage?.omitted || [], text};
  }

  // ---- a task's resources (opView "resources", work_resources.go)
  // The same accounting record the API and exports carry. CPU, memory, I/O
  // and GPU show their basis in the text itself (tooltips are out of reach
  // of keyboard and touch): a number with its basis word and "partial" for
  // a lower bound, otherwise the basis alone (shared, not measured). The
  // grant is an allowance, shown apart from use, and links to the Background
  // CPU allowance in Settings → Background Processing.
  const BASIS_WORDS = {shared: 'shared', unavailable: 'not measured', allocated: 'allocated', measured: 'measured', counted: 'counted', sampled: 'sampled', estimated: 'estimated'};
  const WAIT_WORDS = {queue: 'in the queue', dependency: 'for earlier work', capacity: 'for a CPU slot', writer: 'for the library writer', power: 'for power', lock: 'for a lock'};
  const waitWords = kind => WAIT_WORDS[kind] || `for ${String(kind).replace(/[_.]+/g, ' ')}`;
  const ALLOWANCE_PATH = '/settings/sync';
  function duration(seconds) {
    if (!Number.isFinite(seconds)) return '—';
    if (seconds < 1) return '<1 s';
    if (seconds < 60) return `${Math.round(seconds)} s`;
    if (seconds < 3600) return `${Math.round(seconds / 60)} min`;
    const hours = Math.floor(seconds / 3600), rest = Math.round((seconds % 3600) / 60);
    return rest ? `${hours} h ${rest} min` : `${hours} h`;
  }
  const cpuSeconds = value => `${value.toFixed(1)} s`;
  // One figure as words: "2.5 s measured", "12 MB counted, partial",
  // "shared". The server's coverage sentence says when it is a lower bound.
  function figure(quantity, format) {
    if (!quantity || typeof quantity !== 'object') return null;
    const word = BASIS_WORDS[quantity.basis] || 'not measured', note = quantity.coverage || '';
    if (!Number.isFinite(quantity.value)) return {known: false, basis: word, text: word, note};
    const basis = /lower bound|partial/i.test(note) ? `${word}, partial` : word;
    return {known: true, basis, text: `${format(quantity.value)} ${basis}`, note};
  }
  function taskResourceModel(resources) {
    if (!resources || typeof resources !== 'object' || !resources.version) return null;
    const items = [];
    const add = (id, text, note = '', link = '') => { if (text) items.push({id, text, note, link}); };
    const grant = resources.allocation?.current;
    if (grant && (grant.cpu_slots || grant.gpu)) {
      add('allocation', `${[grant.cpu_slots ? `${grant.cpu_slots} CPU ${unitName('slot', grant.cpu_slots)}` : '', grant.gpu ? 'GPU' : ''].filter(Boolean).join(' and ')} allowed, not usage`,
        'A share of the Background CPU allowance (Settings → Background Processing). A grant, not CPU used.', ALLOWANCE_PATH);
    }
    const active = resources.time?.active;
    if (Number.isFinite(active?.value)) add('active', `active ${duration(active.value)}`, active.coverage || '');
    const waits = Object.entries(resources.waits || {}).filter(([, quantity]) => Number.isFinite(quantity?.value) && quantity.value > 0).sort((a, b) => b[1].value - a[1].value);
    if (waits.length) add('waits', `waited ${duration(waits.reduce((sum, [, quantity]) => sum + quantity.value, 0))}: ${waits.map(([kind, quantity]) => `${duration(quantity.value)} ${waitWords(kind)}`).join(', ')}`);
    const helpers = figure(resources.cpu?.helpers, cpuSeconds);
    if (helpers) add('cpu', `helper-process CPU ${helpers.text}`, helpers.note);
    // The service process's CPU and memory are shared by every task in it.
    const service = figure(resources.cpu?.go_process, cpuSeconds), serviceMemory = figure(resources.memory?.process, size);
    if (service && serviceMemory && !service.known && !serviceMemory.known && service.text === serviceMemory.text) {
      add('service', `service CPU and memory ${service.text}`, [service.note, serviceMemory.note].filter(Boolean).join(' '));
    } else {
      if (service) add('service', `service CPU ${service.text}`, service.note);
      if (serviceMemory) add('service-memory', `service memory ${serviceMemory.text}`, serviceMemory.note);
    }
    const gpu = figure(resources.gpu?.time, duration);
    if (gpu && (gpu.known || grant?.gpu)) add('gpu', `GPU time ${gpu.text}`, gpu.note);
    const buffers = resources.memory?.owned_buffers_peak_bytes;
    if (buffers?.value > 0) add('memory', `working buffers peak ${figure(buffers, size).text}`, buffers.coverage || '');
    const read = resources.io?.logical_read_bytes, written = resources.io?.logical_written_bytes;
    if ((Number(read?.value) || 0) + (Number(written?.value) || 0) > 0 ||
        Number.isFinite(read?.value) !== Number.isFinite(written?.value)) {
      const basis = figure(read?.value > 0 ? read : written, size);
      const readText = Number.isFinite(read?.value) ? size(read.value) : 'not measured';
      const writtenText = Number.isFinite(written?.value) ? size(written.value) : 'not measured';
      add('io', `read ${readText}, wrote ${writtenText} (${basis.basis})`, basis.note);
    }
    if (!items.length) return null;
    return {items, text: items.map(item => item.text).join(' · '),
      tooltip: items.filter(item => item.note).map(item => `${item.text}: ${item.note}`).join('\n')};
  }
  // The finished-task records the service keeps for a kind (bounded, no
  // transcript content), and a short CPU profile of the whole service.
  const resourceExportPath = kind => `/api/operations/resources?kind=${encodeURIComponent(kind)}`;
  const PROFILE_PATH = '/api/diagnostics/profile?seconds=5';

  function attentionModel(item) {
    return {id: `${item.library || ''}:${item.id}`, severity: item.severity === 'warning' ? 'warning' : 'error', title: item.title || '',
      detail: item.detail || '', action: item.action?.path ? {label: item.action.label || item.action.path, path: item.action.path} : null};
  }

  // The eject flow: Eject asks for a fresh verdict and acts only on it.
  // shown is the verdict on screen, which names what the button will do.
  function ejectModel(state, drive, shown = null) {
    const phase = state?.phase || 'idle';
    return {phase, name: drive.name || 'the drive', busy: phase === 'checking' || phase === 'ejecting', label: ejectLabel(phase, shown, state?.message),
      text: state?.eject?.summary || state?.message || '', verdict: state?.eject?.verdict || null,
      // Waiting lists only what keeps it waiting; the release stops the rest.
      blockers: (state?.eject?.blockers || []).filter(blocker => phase !== 'waiting' || ejectDecision({verdict: blocker.verdict}) === 'refuse')
        .map(blocker => [blocker.label, blocker.on_eject].filter(Boolean).join(': '))};
  }

  // The Eject button's words: what pressing it does under the shown
  // verdict. Not safe yet, it waits (stopping what it can) and ejects once
  // a fresh verdict allows it.
  function ejectLabel(phase, shown, message = '') {
    if (phase === 'checking') return 'Checking…';
    if (phase === 'ejecting') return message || 'Ejecting…';
    if (phase === 'waiting') return 'Waiting to eject…';
    const decision = ejectDecision(shown);
    return decision === 'eject' ? '⏏ Eject' : decision === 'confirm' ? '⏏ Stop and Eject' : '⏏ Eject when safe';
  }

  // What the fresh verdict lets Eject do: go ahead, ask first (work stops at
  // a safe point), or refuse (Eject when safe waits).
  const ejectDecision = eject => eject?.verdict === 'safe' ? 'eject' : eject?.verdict === 'safe-in' ? 'confirm' : 'refuse';

  // The header chip: the drive's name and a dot. Red (or amber for a
  // warning) when something needs attention, brass while work runs, green
  // when idle; grey before the first snapshot.
  function chipModel(view, {error = null} = {}) {
    const operations = currentOperations(view);
    const attention = view?.attention || [];
    const alert = attention.some(item => item.severity !== 'warning') ? 'bad' : attention.length ? 'warn' : '';
    if (!view?.library && !operations.length) {
      // No snapshot of the selected library, but the broker may say why
      // ("… is not open"): its attention still sets the dot and tooltip.
      const name = view?.service?.name || 'Library';
      const status = attention[0]?.title || view?.service?.error || '';
      return {name, dot: alert || (error?.status === 503 ? 'warn' : 'idle'), state: 'unavailable', title: status ? `${name}: ${status}` : ''};
    }
    const name = driveName(view);
    const first = operations.find(operation => operation.priority !== 'background') || operations[0];
    const dot = alert || (operations.length ? 'busy' : '');
    const status = attention[0]?.title || (first ? lineText(operationLine(first)) : view.library?.eject?.headline || view.library?.eject?.summary || '');
    return {name, dot, state: operations.length ? 'busy' : 'idle', title: status ? `${name}: ${status}` : name};
  }

  // Index changes is busy while a capture or index runs (or this page
  // started one, local).
  const indexBusy = (view, local = false) => Boolean(local) || (view?.library?.operations || []).some(operation => running(operation) && INDEX_KINDS.test(operation.kind || ''));

  // The verdict shown: a less safe one at once; a safer one only once it has
  // held for hold ms, so a verdict that flickers never shows "safe" early.
  function settleVerdict(previous, next, at, hold = 2000) {
    if (!next) return previous;
    if (!previous?.eject || (RANK[next.verdict] ?? 3) >= (RANK[previous.eject.verdict] ?? 3)) return {eject: next, at, pending: null};
    const pending = previous.pending && previous.pending.verdict === next.verdict ? previous.pending : {verdict: next.verdict, since: at};
    if (at - pending.since >= hold) return {eject: next, at, pending: null};
    return {eject: previous.eject, at: previous.at, pending, next};
  }

  // ---- rendering
  let panel = null, ejectState = null, shown = null, holdTimer = null;
  let displayedHistory = null, displayedHistoryDocument = null;
  // Task kinds' names seen while the page is open, so a finished task's
  // markers keep its name.
  const knownLabels = {};
  const live = (target, content) => window.pharosLibrary?.updateLiveChildren ? window.pharosLibrary.updateLiveChildren(target, content) : target.replaceChildren(...content.childNodes);
  const chip = () => window.pharosLibrary?.ensureChip?.() || document.getElementById('pharosDrive');
  const anchor = () => window.headerToolAnchor?.(chip()) ?? chip();
  const nativeEjectAvailable = () => window.pharosHost.capabilities.eject.available;
  const toast = message => { if (typeof window.feedbackToast === 'function') window.feedbackToast(message); };
  const storeState = () => window.pharosStatusStore?.current() || null;

  function currentVerdict() {
    const eject = storeState()?.view?.library?.eject;
    const settled = settleVerdict(shown, eject, Date.now());
    shown = settled;
    clearTimeout(holdTimer);
    if (settled?.pending) holdTimer = setTimeout(render, 2100);
    return settled?.eject || eject || null;
  }

  function renderChip() {
    const target = chip();
    if (!target) return;
    const state = storeState();
    const model = chipModel(state?.view, {error: state?.error});
    const content = node('div'), dot = node('span', 'pharos-dot' + (model.dot ? ` ${model.dot}` : '')), name = node('span', 'pharos-drive-name', model.name);
    content.append(dot, name);
    live(target, content);
    target.dataset.state = model.state;
    target.title = model.title;
    target.setAttribute('aria-label', `Library drive: ${model.title}`);
    target.setAttribute('aria-expanded', String(Boolean(panel)));
  }

  function renderHeader() {
    const busy = indexBusy(storeState()?.view, window.pharosLibrary?.localBusy?.());
    for (const action of [document.getElementById('headerSync'), document.getElementById('headerSyncOnly')]) if (action) {
      action.disabled = busy;
      action.setAttribute('aria-busy', String(busy));
      action.title = 'Index changes';
      action.setAttribute('aria-label', 'Index changes');
    }
  }

  function navigate(path) {
    close();
    history.pushState(null, '', path);
    window.dispatchEvent(new PopStateEvent('popstate'));
  }

  // The operations Stop was pressed for, so a snapshot that arrives before
  // their state changes does not enable it again.
  const stopRequested = new Set();
  async function sendStops(targets, requested, send = fetch, report = toast) {
    for (const target of targets) {
      try {
        const response = await send(target.path, {method: target.method, headers: {'Content-Type': 'application/json'}, body: JSON.stringify(target.body)});
        if (!response.ok) {
          // Not stopped: let the user try again.
          requested.delete(target.id);
          report((await response.json().catch(() => ({}))).error || response.statusText);
        }
      } catch (error) { requested.delete(target.id); report(error.message); }
    }
  }
  async function stopAll(targets) {
    for (const target of targets) stopRequested.add(target.id);
    render();
    await sendStops(targets, stopRequested);
    render();
    window.pharosStatusStore?.refresh();
  }

  async function checkEject() {
    ejectState = {phase: 'checking'};
    render();
    try {
      const document = await window.pharosStatusStore.fresh();
      const decision = ejectDecision(document.eject);
      if (decision === 'eject') return eject();
      if (decision === 'refuse') return waitToEject(document.eject);
      ejectState = {phase: 'confirm', eject: document.eject};
    } catch (error) {
      ejectState = {phase: 'error', message: error?.message || String(error)};
    }
    render();
  }

  // The confirmation acts on a verdict checked again at this moment.
  async function confirmEject() {
    ejectState = {phase: 'checking'};
    render();
    try {
      const document = await window.pharosStatusStore.fresh();
      if (ejectDecision(document.eject) === 'refuse') return waitToEject(document.eject);
      return eject();
    } catch (error) {
      ejectState = {phase: 'error', message: error?.message || String(error)};
      render();
    }
  }

  // Eject when safe: stop what Pharos can now, then check again each time
  // the snapshot changes (and every few seconds while the polled verdict
  // looks safe but a fresh one does not), and eject on the first fresh
  // verdict that allows it. A hidden page gets no snapshots, so while the
  // polled verdict still refuses, a fresh check also runs once the
  // snapshots have been quiet for waitFallback ms. The release stops whatever is left at a safe
  // point. It keeps waiting with the panel closed; Cancel ends it.
  let waitCheck = null, waitTimer = null, waitCheckedAt = 0;
  const waitFallback = 5000;
  function waitToEject(verdict) {
    ejectState = {phase: 'waiting', eject: verdict};
    const targets = panelModel(storeState()?.view).stop?.targets || [];
    if (targets.length) stopAll(targets.filter(target => !stopRequested.has(target.id)));
    render();
  }

  function cancelEject() {
    clearTimeout(waitTimer);
    ejectState = null;
    render();
  }

  function checkWaiting(quiet = false) {
    if (ejectState?.phase !== 'waiting' || waitCheck) return;
    clearTimeout(waitTimer);
    // A fresh check probes every capture and index lock: run it once the
    // polled verdict allows ejecting, or once snapshots have been quiet
    // (each one rearms this), and at most every 2 s.
    if (!quiet && ejectDecision(storeState()?.view?.library?.eject) === 'refuse') {
      waitTimer = setTimeout(() => checkWaiting(true), waitFallback);
      return;
    }
    const wait = 2000 - (Date.now() - waitCheckedAt);
    if (wait > 0) { waitTimer = setTimeout(() => checkWaiting(quiet), wait); return; }
    waitCheckedAt = Date.now();
    waitCheck = (async () => {
      try {
        const document = await window.pharosStatusStore.fresh();
        if (ejectState?.phase !== 'waiting') return;
        if (ejectDecision(document.eject) !== 'refuse') { waitCheck = null; return eject(); }
        ejectState = {phase: 'waiting', eject: document.eject};
        render();
      } catch {
        // Keep waiting; the next snapshot or the retry checks again.
      } finally { waitCheck = null; }
      if (ejectState?.phase === 'waiting') waitTimer = setTimeout(checkWaiting, 3000);
    })();
  }

  async function eject() {
    ejectState = {phase: 'ejecting', message: 'Releasing…'};
    render();
    try {
      await window.pharosHost.lifecycle.requestEject();
      ejectState = {phase: 'ejecting', message: 'Ejecting…'};
    } catch (error) {
      ejectState = {phase: 'error', message: error?.message || String(error)};
    }
    render();
  }

  function renderPanel() {
    if (!panel) return;
    const state = storeState();
    for (const id of [...stopRequested]) if (!currentOperations(state?.view).some(operation => operation.id === id && operation.state !== 'stopping')) stopRequested.delete(id);
    const model = panelModel(state?.view, {ejectState, verdict: currentVerdict(), nativeEject: Boolean(nativeEjectAvailable()), stopRequested, history: state?.history,
      labels: operationLabels(state?.view, knownLabels),
      unavailable: state?.error ? (state.error.status === 503 ? 'Pharos is stopping; it has released or is releasing the library.' : `Could not read the library's status: ${state.error.message}`) : null});
    displayedHistory = model.history;
    displayedHistoryDocument = state?.history || null;
    // A pinned time that has left the window is let go.
    if (inspect && (!model.history || inspect.at < model.history.start)) inspect = null;
    const scrollTop = panel.scrollTop;
    const content = node('div');
    const head = node('div', 'pharos-panel-head'), title = node('div'), heading = node('h2', '', model.title);
    title.append(heading);
    if (model.library) title.append(libraryLine(model.library));
    head.append(title);
    const actions = node('div', 'pharos-status-actions');
    actions.dataset.key = 'actions';
    // Stop sits beside Eject: both act on everything running.
    if (model.stop) {
      const label = model.stop.stopping ? 'Stopping…' : 'Stop';
      const stop = iconButton('stop', label, () => stopAll(model.stop.targets), model.stop.title ? `${label}: ${model.stop.title}` : label);
      stop.dataset.key = 'stop';
      stop.disabled = Boolean(model.stop.stopping);
      stop.setAttribute('aria-busy', String(Boolean(model.stop.stopping)));
      actions.append(stop);
    }
    if (model.eject) {
      const words = model.eject.label.replace(/^⏏ /, '');
      const ejectButton = iconButton('eject', `${words} ${model.eject.name}`, checkEject, words);
      ejectButton.setAttribute('aria-busy', String(model.eject.busy || model.eject.phase === 'waiting'));
      ejectButton.id = 'pharosStatusEject';
      ejectButton.dataset.key = 'eject';
      ejectButton.disabled = model.eject.busy || model.eject.phase === 'confirm' || model.eject.phase === 'waiting';
      actions.append(ejectButton);
    }
    if (actions.childElementCount) head.append(actions);
    content.append(head);
    if (!model.available) {
      if (model.message) content.append(node('p', 'pharos-error', model.message));
      else if (model.stateLabel) content.append(node('div', 'pharos-sub', model.stateLabel));
      appendNow(content, model);
      if (model.history) content.append(historyStrip(model.history, state?.history));
      if (model.load) content.append(loadRow(model.load));
      appendAttention(content, model.attention);
      commit(content, scrollTop);
      return;
    }
    // Whether ejecting is safe, under the drive's name.
    const verdict = node('div', 'pharos-status-eject');
    verdict.dataset.key = 'eject-verdict';
    verdict.append(node('span', 'pharos-dot small' + (model.verdict.dot ? ` ${model.verdict.dot}` : '')), node('span', '', model.verdict.text));
    if (model.verdict.tooltip) verdict.title = model.verdict.tooltip;
    content.append(verdict);
    if (model.eject && ['confirm', 'waiting', 'error'].includes(model.eject.phase)) {
      const flow = node('div', 'pharos-row');
      flow.dataset.key = `eject-${model.eject.phase}`;
      if (model.eject.phase === 'waiting') flow.append(node('p', 'pharos-note', `Pharos ejects ${model.eject.name} as soon as it is safe, stopping what it can now. Waiting for:`));
      else flow.append(node('p', 'pharos-note' + (model.eject.phase === 'confirm' ? '' : ' pharos-error'), model.eject.text));
      for (const blocker of model.eject.blockers) flow.append(node('p', 'pharos-note pharos-sub', blocker));
      if (model.eject.phase === 'confirm') flow.append(button(`Stop and Eject ${model.eject.name}`, 'primary', confirmEject));
      else if (model.eject.phase === 'error') flow.append(button('Check again', '', checkEject));
      flow.append(button('Cancel', '', cancelEject));
      content.append(flow);
    }
    const schedule = model.schedule;
    if (schedule?.headline) {
      const headline = node('div', `pharos-status-headline ${schedule.headline.tone}`);
      headline.dataset.key = 'headline';
      headline.append(node('strong', '', schedule.headline.title));
      if (schedule.headline.text) headline.append(node('p', '', schedule.headline.text));
      if (schedule.headline.tech) {
        const details = node('details'), summary = node('summary', '', 'Details for support');
        details.dataset.viewKey = 'status:support';
        details.dataset.viewDefault = 'false';
        details.open = window.pharosView?.read('disclosures', {})['status:support'] === true;
        details.append(summary, schedule.headline.tech);
        headline.append(details);
      }
      if (schedule.headline.action) headline.append(button(schedule.headline.action.label, 'primary', () => post(schedule.headline.action.path, schedule.headline.action.body)));
      content.append(headline);
    }
    appendNow(content, model);
    if (model.load && model.history) content.append(historyStrip(model.history, state?.history));
    if (model.load) content.append(loadRow(model.load));
    // Settings sits on the Schedule heading's line, at the right.
    const settings = node('a', '', 'Settings');
    settings.href = '/settings/sync';
    settings.onclick = event => { event.preventDefault(); navigate('/settings/sync'); };
    const scheduleHeading = sectionHeading('Schedule', 'schedule-heading');
    scheduleHeading.append(settings);
    content.append(scheduleHeading);
    if (schedule?.rows.length) content.append(cadenceList(schedule.rows));
    appendAttention(content, model.attention);
    if (model.others.length) {
      const others = node('div', 'pharos-status-others');
      others.dataset.key = 'others';
      for (const other of model.others) {
        const entry = node('div');
        entry.dataset.key = `other:${other.id}`;
        entry.append(node('strong', '', other.name));
        if (other.text) entry.append(` · ${other.text}`);
        if (other.tooltip) entry.title = other.tooltip;
        others.append(entry);
      }
      content.append(others);
    }
    commit(content, scrollTop);
  }

  // A schedule action; the snapshot shows what it changed.
  async function post(path, body) {
    try {
      const response = await fetch(path, {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body || {})});
      if (!response.ok) toast((await response.json().catch(() => ({}))).error || response.statusText);
    } catch (error) { toast(error.message); }
    window.pharosStatusStore?.refresh();
  }

  // The history strip: one row per series, with its own scale, the task
  // markers across every row and on their own lane, and an inspection
  // readout. Pointing inspects; a click or Enter pins the time and another
  // unpins it; the arrow keys step through samples; Escape clears. The
  // inspected time is transient, never in the URL. Only keyboard inspection
  // is announced, so moving the pointer does not chatter.
  const SVG = 'http://www.w3.org/2000/svg';
  const svgNode = (tag, attributes) => {
    const element = document.createElementNS(SVG, tag);
    for (const [name, value] of Object.entries(attributes)) element.setAttribute(name, String(value));
    return element;
  };
  const HISTORY_HINT = 'Point, or use the arrow keys, to inspect · click or Enter pins a time';
  // inspect is {at, pinned, keyboard}; announcement is the last keyboard
  // reading, kept until the next one so a redraw does not repeat it.
  let inspect = null, announcement = '';
  function historyStrip(model, history) {
    const strip = node('div', 'pharos-status-history');
    strip.dataset.key = 'history';
    strip.dataset.history = '';
    strip.tabIndex = 0;
    strip.setAttribute('role', 'group');
    strip.setAttribute('aria-label', `Resource history: ${model.summary} Arrow keys step through samples, Enter pins a time, Escape clears.`);
    const caption = node('div', 'pharos-status-history-caption');
    caption.dataset.key = 'caption';
    caption.append(node('span', '', model.caption));
    if (model.omitted.length) {
      const omitted = node('span', '', `${model.omitted.length} not measured`);
      omitted.title = model.note;
      caption.append(omitted);
    }
    strip.append(caption);
    const inspected = inspect ? historyInspect(history, inspect.at, {labels: model.labels}) : null;
    if (inspected && inspect.keyboard) announcement = `${inspect.pinned ? 'Pinned · ' : ''}${inspected.text}`;
    const cursor = inspected ? Math.max(0, Math.min(model.width, ((inspected.t - model.start) / (model.end - model.start)) * model.width)) : null;
    for (const series of model.series) {
      const row = node('div', 'pharos-status-history-row');
      row.dataset.key = `history:${series.id}`;
      row.title = series.summary;
      const graph = svgNode('svg', {viewBox: `0 0 ${model.width} ${model.height}`, preserveAspectRatio: 'none', role: 'img', 'aria-label': series.summary, focusable: 'false'});
      graph.append(svgNode('line', {class: 'base', x1: 0, x2: model.width, y1: model.height - 0.5, y2: model.height - 0.5}));
      for (const marker of model.markers) graph.append(svgNode('line', {class: `marker ${marker.event}`, x1: marker.x, x2: marker.x, y1: 0, y2: model.height}));
      for (const d of series.paths) graph.append(svgNode('path', {class: 'line', d}));
      if (cursor !== null) graph.append(svgNode('line', {class: `cursor${inspect.pinned ? ' pinned' : ''}`, x1: cursor, x2: cursor, y1: 0, y2: model.height}));
      row.append(node('span', '', series.label), graph, node('b', '', series.current));
      strip.append(row);
    }
    // Starts and ends, told apart by shape, each named in its tooltip. The
    // readout and the strip's summary carry the same for assistive tech.
    if (model.markers.length) {
      const row = node('div', 'pharos-status-history-row'), lane = node('div', 'pharos-status-history-lane');
      row.dataset.key = 'history:tasks';
      row.setAttribute('aria-hidden', 'true');
      for (const marker of model.markers) {
        const mark = node('i', marker.event, marker.event === 'end' ? '◂' : '▸');
        mark.dataset.key = `${marker.event}:${marker.id}:${marker.t}`;
        // Shifted by its own position, so a mark at either edge stays inside.
        const at = round1((marker.x / model.width) * 100);
        mark.style.left = `${at}%`;
        mark.style.transform = `translateX(-${at}%)`;
        mark.title = marker.text;
        lane.append(mark);
      }
      row.append(node('span', '', 'Tasks'), lane, node('b', ''));
      strip.append(row);
    }
    const readout = node('div', 'pharos-status-history-readout' + (inspected ? '' : ' hint'), inspected ? `${inspect.pinned ? 'Pinned · ' : ''}${inspected.text}` : HISTORY_HINT);
    readout.dataset.key = 'readout';
    const status = node('span', 'pharos-status-sr', announcement);
    status.dataset.key = 'announce';
    status.setAttribute('role', 'status');
    status.setAttribute('aria-atomic', 'true');
    strip.append(readout, status);
    return strip;
  }
  // Pointer and keys reach the strip through the document, so a re-rendered
  // panel keeps them (live updates copy attributes, not listeners).
  let inspectFrame = 0;
  const setInspect = next => {
    if (inspect === next || (inspect && next && ['at', 'pinned', 'keyboard'].every(key => inspect[key] === next[key]))) return;
    if (!next && inspect?.keyboard) announcement = 'Inspection cleared';
    inspect = next;
    if (inspectFrame) return;
    const request = window.requestAnimationFrame || (callback => setTimeout(callback, 16));
    inspectFrame = request(() => {
      inspectFrame = 0;
      const strip = panel?.querySelector('[data-history]');
      if (strip && displayedHistory) {
        // Hover changes the cursor/readout only. Reuse plotted geometry
        // and the small live renderer without rebuilding the task panel.
        live(strip, historyStrip(displayedHistory, displayedHistoryDocument));
      }
    });
  };
  const historyElement = target => panel && target?.closest ? target.closest('[data-history]') : null;
  const historyDocument = () => storeState()?.history || null;
  // The time under the pointer, or null off the plotted area.
  function pointerTime(strip, event) {
    const model = displayedHistory;
    const graph = strip.querySelector('svg');
    const box = graph?.getBoundingClientRect();
    if (!model || !box?.width) return null;
    const fraction = Math.max(0, Math.min(1, (event.clientX - box.left) / box.width));
    return Math.round(model.start + fraction * (model.end - model.start));
  }
  document.addEventListener('pointermove', event => {
    if (inspect?.pinned) return;
    const strip = historyElement(event.target);
    const time = strip ? pointerTime(strip, event) : null;
    if (time === null) { if (inspect && !inspect.keyboard && !strip) setInspect(null); return; }
    setInspect({at: time, pinned: false, keyboard: false});
  });
  document.addEventListener('click', event => {
    const strip = historyElement(event.target);
    if (!strip) return;
    const time = pointerTime(strip, event);
    if (inspect?.pinned) setInspect(time === null ? null : {at: time, pinned: false, keyboard: false});
    else if (time !== null) setInspect({at: time, pinned: true, keyboard: false});
  });
  document.addEventListener('focusout', event => { if (historyElement(event.target) && inspect && !inspect.pinned) setInspect(null); });
  // Captured, so Escape clears the inspection before it can close the panel.
  document.addEventListener('keydown', event => {
    const strip = historyElement(document.activeElement);
    if (!strip || event.target !== strip) return;
    if (event.key === 'Escape') {
      if (!inspect) return;
      event.preventDefault();
      event.stopPropagation();
      setInspect(null);
      return;
    }
    const times = historyDocument()?.samples?.t || [];
    if (!times.length || !['ArrowLeft', 'ArrowRight', 'Home', 'End', 'Enter', ' '].includes(event.key)) return;
    event.preventDefault();
    // Step from the inspected sample (or from now) to its neighbour.
    let index = -1;
    if (inspect) for (let i = 0; i < times.length; i++) if (index < 0 || Math.abs(times[i] - inspect.at) < Math.abs(times[index] - inspect.at)) index = i;
    if (index < 0 || event.key === 'End') index = times.length - 1;
    else if (event.key === 'ArrowLeft') index = Math.max(0, index - 1);
    else if (event.key === 'ArrowRight') index = Math.min(times.length - 1, index + 1);
    if (event.key === 'Home') index = 0;
    const pinned = event.key === 'Enter' || event.key === ' ' ? !(inspect?.pinned) : Boolean(inspect?.pinned);
    setInspect({at: times[index], pinned, keyboard: true});
  }, true);

  // Now: each shown operation with its step, measured amount, time range
  // and progress, then the Load line.
  function loadRow(load) {
    const row = node('div', 'pharos-status-load');
    row.dataset.key = 'load';
    for (const metric of load.metrics) {
      const cell = node('span');
      cell.dataset.key = `load:${metric.id}`;
      if (metric.warning) cell.title = metric.warning;
      const label = node('span');
      label.append(svgIcon(metric.icon || metric.id), metric.label);
      cell.append(label, node('b', '', metric.value));
      row.append(cell);
    }
    return row;
  }

  const sectionHeading = (text, key) => {
    const element = node('div', 'pharos-status-heading', text);
    element.dataset.key = key;
    return element;
  };

  function appendNow(content, model) {
    if (!model.lines.length) return;
    const now = node('div', 'pharos-status-now');
    now.dataset.key = 'now';
    for (const line of model.lines) {
      const item = node('div', 'pharos-status-line');
      item.dataset.key = `line:${line.id}`;
      const primary = node('div');
      primary.dataset.key = 'primary';
      if (line.cadence) {
        const current = node('span', 'pharos-status-current');
        current.append(node('strong', '', line.label), node('span', 'pharos-status-cadence', line.cadence));
        primary.append(current);
      } else primary.append(node('strong', '', line.label));
      if (line.primary.length) primary.append(` · ${line.primary.join(' · ')}`);
      if (line.time) primary.append(' ', node('span', 'pharos-status-time', line.time));
      item.append(primary);
      if (line.position) {
        const progress = node('div', 'pharos-status-position');
        progress.dataset.key = 'position';
        progress.append(node('div', 'pharos-status-position-label', `Task ${line.position.index} of ${line.position.count}`));
        const track = node('div', 'pharos-status-position-track');
        track.setAttribute('aria-hidden', 'true');
        track.style.setProperty('--step-count', line.position.count);
        for (let index = 1; index <= line.position.count; index++) track.append(node('i', index < line.position.index ? 'done' : index === line.position.index ? 'active' : ''));
        progress.append(track);
        item.append(progress);
      }
      const bar = node('div', `pharos-status-bar${line.fraction == null ? ' indeterminate' : ''}${line.waiting ? ' waiting' : ''}`), fill = node('i');
      bar.dataset.key = 'progress';
      bar.setAttribute('role', 'progressbar');
      bar.setAttribute('aria-label', `${line.currentLabel} progress`);
      bar.setAttribute('aria-valuetext', line.progressLabel);
      if (line.fraction != null) {
        const percent = Math.round(Math.max(0, Math.min(1, line.fraction)) * 100);
        fill.style.width = `${percent}%`;
        bar.setAttribute('aria-valuemin', '0');
        bar.setAttribute('aria-valuemax', '100');
        bar.setAttribute('aria-valuenow', String(percent));
      }
      bar.append(fill);
      item.append(bar);
      if (line.secondary.length) item.append(node('span', 'pharos-sub pharos-status-step', line.secondary.join(' · ')));
      if (line.resources) item.append(resourceLine(line));
      if (line.tooltip) item.title = line.tooltip;
      now.append(item);
    }
    if (model.profile) {
      const profile = node('span', 'pharos-sub pharos-status-step');
      profile.dataset.key = 'profile';
      profile.append(download(model.profile, 'Record a 5 s CPU profile', 'Runs only when you click; the download arrives after 5 seconds.'),
        ' of the whole service process. Samples are labeled with the task that ran them; it is not any one task\'s own CPU.');
      now.append(profile);
    }
    content.append(sectionHeading('Running now', 'now-heading'), now);
  }

  const download = (target, text, title = '') => {
    const link = node('a', '', text);
    link.href = target.path;
    link.download = target.file;
    if (title) link.title = title;
    return link;
  };
  // A task's resources: each figure with its basis in words; the grant
  // links to the allowance it comes from; then the kind's finished records.
  function resourceLine(line) {
    const element = node('span', 'pharos-sub pharos-status-step');
    element.dataset.key = 'resources';
    line.resources.items.forEach((item, index) => {
      if (index) element.append(' · ');
      if (!item.link) { element.append(item.text); return; }
      const link = node('a', '', item.text);
      link.href = item.link;
      link.title = item.note;
      link.onclick = event => { event.preventDefault(); navigate(item.link); };
      element.append(link);
    });
    if (line.export) element.append(' · ', download(line.export, 'finished records (JSON)', 'The recent finished tasks of this kind, as the service keeps them: figures with their bases, no conversation content.'));
    if (line.resources.tooltip) element.title = line.resources.tooltip;
    return element;
  }

  // One row per cadence, opening to its items. A row the user opened or
  // closed stays so while the panel is open.
  // "Library Pharos on euclid", the name linking to Settings → Libraries.
  function libraryLine(library) {
    const line = node('div', 'pharos-sub'), link = node('a', '', library.name);
    link.href = '/settings/libraries';
    link.onclick = event => { event.preventDefault(); navigate('/settings/libraries'); };
    if (library.of) link.textContent += ` (${library.of})`;
    line.append('Library ', link, ` on ${library.drive}`);
    if (library.path) line.title = library.path;
    return line;
  }

  function cadenceList(rows) {
    const list = node('div', 'pharos-status-cadences');
    list.dataset.key = 'cadences';
    for (const row of rows) {
      const details = node('details');
      details.dataset.key = `cadence:${row.id}`;
      details.dataset.viewKey = 'status:' + details.dataset.key;
      details.dataset.viewDefault = String(Boolean(row.open));
      details.setAttribute('data-view-capture-initial', '');
      details.open = window.pharosView?.read('disclosures', {})[details.dataset.viewKey] ?? row.open;
      // Icon, the cadence's dot and name, then its state at the line's end,
      // aligned with its items' dots and states.
      const summary = node('summary'), name = node('span', 'pharos-status-name');
      name.append(node('span', 'pharos-dot small' + (row.dot ? ` ${row.dot}` : '')), row.label);
      if (row.run) {
        // Run this cadence's items now; inside the summary, so it must not
        // also open or close the row.
        const run = button('▶', 'pharos-status-run', event => {
          event.preventDefault();
          event.stopPropagation();
          post('/api/background/run', {cadence: row.run.cadence});
          toast(`${row.label} runs now.`);
        });
        run.dataset.key = 'run';
        run.disabled = row.run.disabled;
        run.title = `Run ${row.label} now`;
        run.setAttribute('aria-label', `Run ${row.label} now`);
        name.append(run);
      }
      summary.append(node('span', 'pharos-status-icon', row.icon), name, node('span', 'pharos-status-state', row.text), node('span', 'pharos-status-what', row.what));
      const items = node('ul', 'pharos-status-items');
      for (const item of row.items) {
        const entry = node('li');
        entry.dataset.key = `item:${item.id}`;
        entry.append(node('span', 'pharos-dot small' + (item.dot ? ` ${item.dot}` : '')), node('span', '', item.label), node('span', '', item.text));
        if (item.tooltip) entry.title = item.tooltip;
        items.append(entry);
      }
      details.append(summary, items);
      list.append(details);
    }
    return list;
  }

  function appendAttention(content, items) {
    const actionable = items.filter(item => item.action);
    if (!actionable.length) return;
    const section = node('section', 'pharos-attention');
    section.dataset.key = 'attention';
    section.setAttribute('aria-label', 'Needs attention');
    for (const item of actionable) {
      // The title and its action on one line; the detail below, full width.
      const entry = node('div', 'pharos-attention-item'), line = node('div', 'pharos-running-head');
      entry.dataset.key = `attention:${item.id}`;
      line.append(node('strong', item.severity === 'warning' ? 'pharos-warn' : '', item.title), button(item.action.label, '', () => navigate(item.action.path)));
      entry.append(line);
      if (item.detail) entry.append(node('div', 'pharos-sub', item.detail));
      section.append(entry);
    }
    content.append(section);
  }

  function commit(content, scrollTop) {
    live(panel, content);
    place();
    panel.scrollTop = scrollTop;
  }

  function place() {
    const target = anchor();
    if (!panel || !target) return;
    const box = target.getBoundingClientRect(), header = document.querySelector('body>header')?.getBoundingClientRect();
    const top = Math.round((header?.bottom ?? box.bottom) + 8);
    panel.style.top = `${top}px`;
    panel.style.maxHeight = `${Math.max(0, window.innerHeight - top - 12)}px`;
    panel.style.left = `${Math.round(Math.max(12, Math.min(box.left, window.innerWidth - panel.offsetWidth - 12)))}px`;
  }

  function open({fromURL = false, focus = true} = {}) {
    if (!chip() || panel) return;
    window.pharosLibrary?.closePanel?.({fromURL: true});
    if (!fromURL) window.pharosView?.write('overlay', 'drive');
    panel = node('section', 'pharos-drive-panel pharos-status-panel');
    panel.id = 'pharosStatusPanel';
    panel.tabIndex = -1;
    panel.setAttribute('role', 'dialog');
    panel.setAttribute('aria-label', 'Library drive');
    document.body.append(panel);
    inspect = null;
    announcement = '';
    window.pharosStatusStore?.openHistory?.();
    renderChip();
    renderPanel();
    if (focus) (panel.querySelector('button:not(:disabled), summary, [href]') || panel).focus({preventScroll: true});
  }

  function close({restoreFocus = false, fromURL = false} = {}) {
    if (!panel) return;
    if (!fromURL && window.pharosView?.read('overlay', 'none') === 'drive') window.pharosView.write('overlay', 'none');
    panel.remove();
    panel = null;
    displayedHistory = displayedHistoryDocument = null;
    inspect = null;
    announcement = '';
    window.pharosStatusStore?.closeHistory?.();
    if (ejectState?.phase !== 'ejecting' && ejectState?.phase !== 'waiting') ejectState = null;
    chip()?.setAttribute('aria-expanded', 'false');
    if (restoreFocus) anchor()?.focus();
  }

  function toggle() { if (panel) close(); else open(); }

  // Focus stays in the open panel; Escape closes it and returns focus to
  // the chip (or More, when the chip is in the overflow).
  document.addEventListener('keydown', event => {
    if (!panel) return;
    if (event.key === 'Escape') { event.preventDefault(); close({restoreFocus: true}); return; }
    if (event.key !== 'Tab') return;
    const focusable = [...panel.querySelectorAll('button:not(:disabled), summary, [href], [tabindex]:not([tabindex="-1"])')].filter(element => element.getClientRects().length);
    if (!focusable.length) { event.preventDefault(); panel.focus(); return; }
    const first = focusable[0], last = focusable.at(-1);
    if (!panel.contains(document.activeElement)) { event.preventDefault(); first.focus(); }
    else if (event.shiftKey && (document.activeElement === first || document.activeElement === panel)) { event.preventDefault(); last.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
  });
  document.addEventListener('mousedown', event => {
    if (panel && !panel.contains(event.target) && !chip()?.contains(event.target)) close();
  });
  window.addEventListener('resize', place);

  function render() {
    renderChip();
    renderHeader();
    renderPanel();
    checkWaiting();
  }


  window.pharosStatusPanel = {
    open, close, toggle, render, renderHeader,
    // Pure helpers, for web/tests/unit/page/status-panel.test.mjs.
    panelModel, chipModel, ejectLabel, operationLine, lineText, currentOperations, settleVerdict, ejectDecision, indexBusy, sendStops, stepAmount, scheduleModel, scheduleText, ago, until, span,
    historyModel, historyInspect, taskResourceModel, loadModel, operationLabels,
  };
  const sheet = node('style');
  sheet.textContent = CSS;
  document.head.append(sheet);
  window.pharosStatusStore?.subscribe(render);
  window.pharosStatusStore?.start();
  render();
  window.dispatchEvent(new Event('pharos:drive-view-ready'));
})();
