// The unified activity panel (docs/background-work.md §7–9, §11), shown
// while the pharos-unified-activity-v1 preference is on. It renders only
// from the status store (status-store.js) and, while on, is the one renderer
// of the header's drive chip, its panel and the header's Index changes
// state; library.js and sync.js leave those alone. Off, it does nothing at
// all and library.js's drive panel runs as before.
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
.pharos-status-eject{display:flex;gap:8px;align-items:baseline;margin-top:4px;font-size:12px;color:var(--muted,#666)}
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
.pharos-status-line{display:block;overflow-wrap:anywhere}
.pharos-status-line+.pharos-status-line{margin-top:8px}
.pharos-status-line>strong{font-weight:650}
.pharos-status-line .pharos-status-time{color:var(--muted,#666);white-space:nowrap}
.pharos-status-step{display:block;font-size:12px}
.pharos-status-bar{height:5px;border-radius:3px;background:color-mix(in srgb,var(--ink,#000) 10%,transparent);margin:5px 0 3px;overflow:hidden}
.pharos-status-bar>i{display:block;height:100%;background:var(--accent,#d8ae5e)}
.pharos-status-load{display:block;font-size:12px;color:var(--muted,#666);margin-top:4px}
.pharos-status-cadences{border-top:1px solid var(--line,#ddd)}
.pharos-status-cadences details{border-bottom:1px solid var(--line,#ddd)}
.pharos-status-cadences summary{list-style:none;display:grid;grid-template-columns:18px 86px minmax(0,1fr);gap:2px 8px;align-items:baseline;padding:8px 2px;cursor:pointer}
.pharos-status-cadences summary::-webkit-details-marker{display:none}
.pharos-status-cadences .pharos-status-icon{color:var(--muted,#666)}
.pharos-status-cadences .pharos-status-name{font-weight:650}
.pharos-status-cadences .pharos-status-state{font-size:13px;min-width:0;overflow-wrap:anywhere}
.pharos-status-cadences .pharos-status-what{grid-column:3;font-size:12px;color:var(--muted,#666);white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.pharos-status-cadences details[open] .pharos-status-what{display:none}
.pharos-status-items{list-style:none;margin:0 0 8px;padding:0 0 0 26px}
.pharos-status-items li{display:flex;gap:8px;align-items:baseline;padding:2px 0;font-size:13px}
.pharos-status-items li>span:last-child{margin-left:auto;text-align:right;color:var(--muted,#666);font-size:12px}
.pharos-status-panel .pharos-dot.small{width:7px;height:7px;flex:none;margin-right:6px}
.pharos-status-panel .pharos-dot.paused{background:transparent;box-shadow:inset 0 0 0 2px var(--muted,#888)}
.pharos-status-foot{display:flex;flex-wrap:wrap;align-items:center;gap:8px 12px;margin-top:12px}
.pharos-status-foot a{margin-left:auto}
.pharos-status-foot small{flex-basis:100%;color:var(--muted,#666);font-size:12px}
.pharos-status-others{margin-top:10px;font-size:12px;color:var(--muted,#666);overflow-wrap:anywhere}
.pharos-status-actions{display:flex;flex-wrap:wrap;gap:8px;justify-content:flex-end}
.pharos-status-panel .pharos-panel-head{flex-wrap:wrap}`;

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

  // ---- numbers (the only words this file makes)
  const size = value => {
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let amount = Math.max(0, Number(value) || 0), power = 0;
    while (amount >= 1000 && power < units.length - 1) { amount /= 1000; power++; }
    return `${amount.toFixed(amount < 10 && power ? 1 : 0)} ${units[power]}`;
  };
  const count = value => Math.round(Number(value) || 0).toLocaleString('en-US');
  const unitName = (unit, amount) => amount === 1 ? unit : /(s|x|ch|sh)$/.test(unit) ? `${unit}es` : `${unit}s`;
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
  const SCHEDULE_DOTS = {current: '', running: 'busy', queued: 'busy', failed: 'bad', missed: 'warn', never: 'warn', paused: 'paused', manual: 'paused', unavailable: 'paused', power: 'paused'};
  const CADENCE_ICONS = {frequent: '↻', hourly: '◷', daily: '☾'};

  // The schedule's part of the panel: its headline, one row per cadence
  // (open when something in it is happening or wrong), and what Update
  // everything now costs. Null when the snapshot has no schedule.
  function scheduleModel(background, now = Date.now()) {
    if (!background || !Array.isArray(background.cadences)) return null;
    const items = Object.fromEntries((background.items || []).map(item => [item.id, item]));
    const rows = background.cadences.map(row => {
      const members = (row.items || []).map(id => items[id]).filter(Boolean);
      return {id: row.id, label: row.label, icon: CADENCE_ICONS[row.id] || '', dot: SCHEDULE_DOTS[row.state] ?? '', text: scheduleText(row, now),
        open: ['running', 'queued', 'failed', 'missed'].includes(row.state), what: members.map(item => item.label).join(', '),
        items: members.map(item => ({id: item.id, label: item.label, dot: SCHEDULE_DOTS[item.state] ?? '', text: scheduleText(item, now)}))};
    });
    const headline = background.headline || {};
    return {headline: {tone: headline.tone || 'good', title: headline.title || '', text: headline.text || '', tech: headline.tech || '',
      action: headline.action?.path ? {label: headline.action.label || '', path: headline.action.path, body: headline.action.body || {}} : null},
      rows, everything: background.everything?.text || ''};
  }

  // A step's measured progress: "812 MB of 2 GB", "42 of 100 conversations".
  function stepAmount(step) {
    if (!step?.unit || !(step.total > 0)) return '';
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
  // estimating, or early and marked so); the secondary part, shown muted, is
  // the step's name and source.
  function operationLine(operation) {
    const primary = [], secondary = [];
    if (operation.state !== 'running' && operation.state_label && operation.state_label !== operation.label) primary.push(operation.state_label);
    const step = operation.step;
    if (step?.count > 0 && step.index > 0) primary.push(`step ${step.index} of ${step.count}`);
    const amount = stepAmount(step);
    if (amount) primary.push(amount);
    const name = step?.name && step.name !== operation.label ? step.name : '';
    if (name) secondary.push(name);
    if (step?.source && step.source !== name) secondary.push(step.source);
    const tooltip = [operation.detail, operation.on_eject].filter(Boolean).join('\n');
    return {id: operation.id, label: operation.label, primary, secondary, time: operation.time?.label || '', tooltip,
      fraction: Number.isFinite(step?.fraction) ? step.fraction : null, state: operation.state, background: operation.priority === 'background'};
  }
  // A line as one string, for tooltips and the other libraries' rows.
  const lineText = line => [[line.label, ...line.primary, ...line.secondary].join(' · '), line.time].filter(Boolean).join(' ');

  // The chip's and the panel's name: the drive's, else Library for a
  // portable library on this Mac's disk, else This Mac.
  const driveName = view => {
    const library = view?.library?.library || {};
    const drive = library.drive || {};
    if (!view?.library) return view?.service?.name || 'Library';
    return drive.ejectable ? drive.name || 'Library' : library.portable ? 'Library' : 'This Mac';
  };
  const LOCATIONS = {external: 'External drive', 'disk-image': 'Disk image', internal: "This Mac's internal disk"};

  // What runs for the selected library: the app's operations that touch it
  // (a transfer, a delivery), then its own.
  const currentOperations = view => [...(view?.appOperations || []), ...(view?.library?.operations || [])].filter(running);

  // The panel's content, from the store's view. options.ejectState is the
  // panel's eject flow; options.verdict overrides the snapshot's verdict
  // (the debounced one, see settleVerdict); options.stopRequested holds the
  // operations Stop was pressed for.
  function panelModel(view, {ejectState = null, verdict = null, unavailable = null, nativeEject = false, stopRequested = new Set(), now = Date.now()} = {}) {
    const library = view?.library || null;
    const operations = currentOperations(view);
    // Now: the foreground work, else the first background work; the
    // schedule's rows say what else is under way.
    const foreground = operations.filter(operation => operation.priority !== 'background');
    const shown = foreground.length ? foreground : operations.slice(0, 1);
    const common = {lines: shown.map(operationLine), attention: (view?.attention || []).map(attentionModel)};
    if (!library) {
      return {available: false, title: driveName(view), ...common, stateLabel: view?.service?.stateLabel || null,
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
      title: driveName(view),
      where: [LOCATIONS[drive.location] || (drive.ejectable ? 'Drive' : ''), library.library?.path].filter(Boolean).join(' · '),
      verdict: {verdict: eject.verdict || 'unsafe', text, dot: VERDICT_DOT[eject.verdict] ?? 'bad', checked: eject.checked || null,
        tooltip: [eject.summary !== text ? eject.summary : '', ...(eject.blockers || []).map(blocker => [blocker.label, blocker.on_eject].filter(Boolean).join(': '))].filter(Boolean).join('\n')},
      // Null without a schedule (an older service).
      schedule: scheduleModel(library.background, now),
      load: loadModel(view.resources ?? library.resources),
      others: (view.others || []).map(other => {
        const first = (other.operations || []).find(running);
        return {id: other.id, name: other.name || 'Library',
          text: first ? lineText(operationLine(first)) : other.stateLabel || other.eject?.headline || '', tooltip: first?.detail || other.eject?.summary || ''};
      }),
      stop: stoppable.length ? {targets: stoppable.map(operation => ({id: operation.id, method: operation.stop.method || 'POST', path: operation.stop.path, body: operation.stop.body || {id: operation.id}})),
        stopping: requested, title: stoppable.map(operation => operation.label).join(', ')} : stopping ? {targets: [], stopping: true, title: ''} : null,
      eject: drive.ejectable && nativeEject ? ejectModel(ejectState, drive) : null,
    };
  }

  // The Load row (docs/background-work.md §10): the server's line ("Pharos
  // is using ~45% of one core · 1.2 GB"; in the app, every Pharos process on
  // this Mac), each process's line in the tooltip. Numbers alone for a
  // service that sends no line.
  function loadModel(resources) {
    if (!resources || typeof resources !== 'object') return null;
    const text = resources.label || [Number.isFinite(resources.cpu_percent) ? `${Math.round(resources.cpu_percent)}% of one core` : '', Number.isFinite(resources.memory_bytes) ? size(resources.memory_bytes) : ''].filter(Boolean).join(' · ');
    if (!text) return null;
    return {text, tooltip: (Array.isArray(resources.processes) ? resources.processes : []).map(process => process.text).filter(Boolean).join('\n')};
  }

  function attentionModel(item) {
    return {id: `${item.library || ''}:${item.id}`, severity: item.severity === 'warning' ? 'warning' : 'error', title: item.title || '',
      detail: item.detail || '', action: item.action?.path ? {label: item.action.label || item.action.path, path: item.action.path} : null};
  }

  // The eject flow: Eject asks for a fresh verdict and acts only on it.
  function ejectModel(state, drive) {
    const phase = state?.phase || 'idle';
    return {phase, name: drive.name || 'the drive', busy: phase === 'checking' || phase === 'ejecting',
      text: state?.eject?.summary || state?.message || '', verdict: state?.eject?.verdict || null,
      blockers: (state?.eject?.blockers || []).map(blocker => [blocker.label, blocker.on_eject].filter(Boolean).join(': '))};
  }

  // What the fresh verdict lets Eject do: go ahead, ask first (work stops at
  // a safe point), or refuse.
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

  // ---- rendering (only while the flag is on)
  const enabled = () => window.pharosStatusStore?.enabled() === true;
  let panel = null, ejectState = null, shown = null, holdTimer = null, sheet = null, subscribed = null;
  const live = (target, content) => window.pharosLibrary?.updateLiveChildren ? window.pharosLibrary.updateLiveChildren(target, content) : target.replaceChildren(...content.childNodes);
  const chip = () => window.pharosLibrary?.ensureChip?.() || document.getElementById('pharosDrive');
  const anchor = () => window.headerToolAnchor?.(chip()) ?? chip();
  const nativeLibrary = () => window.webkit?.messageHandlers?.pharosLibrary;
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
  async function stopAll(targets) {
    for (const target of targets) stopRequested.add(target.id);
    render();
    for (const target of targets) {
      try {
        const response = await fetch(target.path, {method: target.method, headers: {'Content-Type': 'application/json'}, body: JSON.stringify(target.body)});
        if (!response.ok) {
          // Not stopped: let the user try again.
          stopRequested.delete(target.id);
          toast((await response.json().catch(() => ({}))).error || response.statusText);
        }
      } catch (error) { stopRequested.delete(target.id); toast(error.message); }
    }
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
      ejectState = {phase: decision === 'confirm' ? 'confirm' : 'refused', eject: document.eject};
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
      if (ejectDecision(document.eject) === 'refuse') { ejectState = {phase: 'refused', eject: document.eject}; render(); return; }
      return eject();
    } catch (error) {
      ejectState = {phase: 'error', message: error?.message || String(error)};
      render();
    }
  }

  async function eject() {
    const handler = nativeLibrary();
    if (!handler) return;
    ejectState = {phase: 'ejecting', message: 'Releasing…'};
    render();
    try {
      await handler.postMessage({action: 'eject'});
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
    const model = panelModel(state?.view, {ejectState, verdict: currentVerdict(), nativeEject: Boolean(nativeLibrary()), stopRequested,
      unavailable: state?.error ? (state.error.status === 503 ? 'Pharos is stopping; it has released or is releasing the library.' : `Could not read the library's status: ${state.error.message}`) : null});
    const scrollTop = panel.scrollTop;
    const content = node('div');
    const head = node('div', 'pharos-panel-head'), title = node('div'), heading = node('h2', '', model.title);
    title.append(heading);
    if (model.available && model.where) title.append(node('div', 'pharos-sub', model.where));
    head.append(title);
    const actions = node('div', 'pharos-status-actions');
    actions.dataset.key = 'actions';
    if (model.eject) {
      const label = model.eject.phase === 'checking' ? 'Checking…' : model.eject.phase === 'ejecting' ? model.eject.text : '⏏ Eject';
      const ejectButton = button(label, '', checkEject);
      ejectButton.id = 'pharosStatusEject';
      ejectButton.dataset.key = 'eject';
      ejectButton.disabled = model.eject.busy || model.eject.phase === 'confirm';
      ejectButton.setAttribute('aria-label', `Eject ${model.eject.name}`);
      actions.append(ejectButton);
    }
    if (actions.childElementCount) head.append(actions);
    content.append(head);
    if (!model.available) {
      if (model.message) content.append(node('p', 'pharos-error', model.message));
      else if (model.stateLabel) content.append(node('div', 'pharos-sub', model.stateLabel));
      appendNow(content, model);
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
    if (model.eject && ['confirm', 'refused', 'error'].includes(model.eject.phase)) {
      const flow = node('div', 'pharos-row');
      flow.dataset.key = `eject-${model.eject.phase}`;
      flow.append(node('p', 'pharos-note' + (model.eject.phase === 'confirm' ? '' : ' pharos-error'), model.eject.text));
      for (const blocker of model.eject.blockers) flow.append(node('p', 'pharos-note pharos-sub', blocker));
      if (model.eject.phase === 'confirm') flow.append(button(`Stop and Eject ${model.eject.name}`, 'primary', confirmEject));
      else flow.append(button('Check again', '', checkEject));
      flow.append(button('Cancel', '', () => { ejectState = null; render(); }));
      content.append(flow);
    }
    const schedule = model.schedule;
    if (schedule?.headline.title) {
      const headline = node('div', `pharos-status-headline ${schedule.headline.tone}`);
      headline.dataset.key = 'headline';
      headline.append(node('strong', '', schedule.headline.title));
      if (schedule.headline.text) headline.append(node('p', '', schedule.headline.text));
      if (schedule.headline.tech) {
        const details = node('details'), summary = node('summary', '', 'Details for support');
        details.append(summary, schedule.headline.tech);
        headline.append(details);
      }
      if (schedule.headline.action) headline.append(button(schedule.headline.action.label, 'primary', () => post(schedule.headline.action.path, schedule.headline.action.body)));
      content.append(headline);
    }
    appendNow(content, model);
    if (schedule?.rows.length) content.append(cadenceList(schedule.rows));
    appendAttention(content, model.attention);
    const foot = node('div', 'pharos-status-foot');
    foot.dataset.key = 'foot';
    const everything = button('Update everything now', '', () => post('/api/sync/check', {}));
    everything.disabled = indexBusy(state?.view, window.pharosLibrary?.localBusy?.());
    const settings = node('a', '', 'Schedule settings');
    settings.href = '/settings/sync';
    settings.onclick = event => { event.preventDefault(); navigate('/settings/sync'); };
    foot.append(everything, settings);
    if (schedule?.everything) foot.append(node('small', '', schedule.everything));
    content.append(foot);
    if (model.others.length) {
      const others = node('div', 'pharos-status-others');
      others.dataset.key = 'others';
      others.append('Other libraries: ');
      model.others.forEach((other, index) => {
        const entry = node('span');
        entry.dataset.key = `other:${other.id}`;
        entry.append(index ? ', ' : '', node('strong', '', other.name), other.text ? ` (${other.text})` : '');
        if (other.tooltip) entry.title = other.tooltip;
        others.append(entry);
      });
      content.append(others);
    }
    commit(content, scrollTop);
  }

  // A schedule action or Update everything now; the snapshot shows what
  // it changed.
  async function post(path, body) {
    try {
      const response = await fetch(path, {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body || {})});
      if (!response.ok) toast((await response.json().catch(() => ({}))).error || response.statusText);
    } catch (error) { toast(error.message); }
    window.pharosStatusStore?.refresh();
  }

  // Now: each shown operation with its step, measured amount, time range
  // and progress, then the Load line.
  function appendNow(content, model) {
    if (!model.lines.length) return;
    const now = node('div', 'pharos-status-now');
    now.dataset.key = 'now';
    for (const line of model.lines) {
      const item = node('div', 'pharos-status-line');
      item.dataset.key = `line:${line.id}`;
      const primary = node('div');
      primary.dataset.key = 'primary';
      primary.append(node('strong', '', line.label));
      if (line.primary.length) primary.append(` · ${line.primary.join(' · ')}`);
      if (line.time) primary.append(' ', node('span', 'pharos-status-time', line.time));
      item.append(primary);
      if (line.fraction != null) {
        const bar = node('div', 'pharos-status-bar'), fill = node('i');
        fill.style.width = `${Math.round(Math.max(0, Math.min(1, line.fraction)) * 100)}%`;
        bar.append(fill);
        bar.setAttribute('aria-hidden', 'true');
        item.append(bar);
        item.setAttribute('aria-description', `${Math.floor(line.fraction * 100)}% of this step`);
      }
      if (line.secondary.length) item.append(node('span', 'pharos-sub pharos-status-step', line.secondary.join(' · ')));
      if (line.tooltip) item.title = line.tooltip;
      now.append(item);
    }
    if (model.load) {
      const load = node('span', 'pharos-status-load', model.load.text);
      load.dataset.key = 'load';
      if (model.load.tooltip) load.title = model.load.tooltip;
      now.append(load);
    }
    if (model.stop) {
      const stop = button(model.stop.stopping ? 'Stopping…' : 'Stop', '', () => stopAll(model.stop.targets));
      stop.dataset.key = 'stop';
      stop.disabled = Boolean(model.stop.stopping);
      if (model.stop.title) stop.title = model.stop.title;
      now.append(stop);
    }
    content.append(now);
  }

  // One row per cadence, opening to its items. A row the user opened or
  // closed stays so while the panel is open.
  const cadenceOpen = new Map();
  function cadenceList(rows) {
    const list = node('div', 'pharos-status-cadences');
    list.dataset.key = 'cadences';
    for (const row of rows) {
      const details = node('details');
      details.dataset.key = `cadence:${row.id}`;
      details.open = cadenceOpen.has(row.id) ? cadenceOpen.get(row.id) : row.open;
      details.addEventListener('toggle', () => cadenceOpen.set(row.id, details.open));
      const summary = node('summary'), state = node('span', 'pharos-status-state');
      state.append(node('span', 'pharos-dot small' + (row.dot ? ` ${row.dot}` : '')), row.text);
      summary.append(node('span', 'pharos-status-icon', row.icon), node('span', 'pharos-status-name', row.label), state, node('span', 'pharos-status-what', row.what));
      const items = node('ul', 'pharos-status-items');
      for (const item of row.items) {
        const entry = node('li');
        entry.dataset.key = `item:${item.id}`;
        entry.append(node('span', 'pharos-dot small' + (item.dot ? ` ${item.dot}` : '')), node('span', '', item.label), node('span', '', item.text));
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
      const entry = node('div', 'pharos-attention-item'), line = node('div', 'pharos-running-head'), text = node('div');
      entry.dataset.key = `attention:${item.id}`;
      text.append(node('strong', item.severity === 'warning' ? 'pharos-warn' : '', item.title));
      if (item.detail) text.append(node('div', 'pharos-sub', item.detail));
      line.append(text, button(item.action.label, '', () => navigate(item.action.path)));
      entry.append(line);
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

  function open() {
    if (!enabled() || !chip()) return;
    window.pharosLibrary?.closePanel?.();
    if (panel) close();
    panel = node('section', 'pharos-drive-panel pharos-status-panel');
    panel.id = 'pharosStatusPanel';
    panel.tabIndex = -1;
    panel.setAttribute('role', 'dialog');
    panel.setAttribute('aria-label', 'Library drive');
    document.body.append(panel);
    renderChip();
    renderPanel();
    (panel.querySelector('button:not(:disabled), summary, [href]') || panel).focus({preventScroll: true});
  }

  function close({restoreFocus = false} = {}) {
    if (!panel) return;
    panel.remove();
    panel = null;
    cadenceOpen.clear();
    if (ejectState?.phase !== 'ejecting') ejectState = null;
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
    if (!enabled()) return;
    renderChip();
    renderHeader();
    renderPanel();
  }

  function activate() {
    if (enabled()) {
      if (!sheet) {
        sheet = node('style');
        sheet.textContent = CSS;
        document.head.append(sheet);
      }
      subscribed ??= window.pharosStatusStore?.subscribe(render) || null;
      window.pharosStatusStore?.start();
      render();
    } else {
      close();
      subscribed?.();
      subscribed = null;
      shown = null;
      window.pharosStatusStore?.start(); // stops it
      window.pharosLibrary?.renderAlerts?.();
      window.pharosLibrary?.refresh?.();
    }
  }
  window.addEventListener('pharos:unified-activity', activate);

  window.pharosStatusPanel = {
    open, close, toggle, render, renderHeader,
    // Pure helpers, for web/tests/unit/page/status-panel.test.mjs.
    panelModel, chipModel, operationLine, lineText, currentOperations, settleVerdict, ejectDecision, indexBusy, stepAmount, scheduleModel, scheduleText, ago, until, span,
  };
  if (enabled()) activate();
})();
