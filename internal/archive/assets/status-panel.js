// The unified activity panel (docs/background-work.md §7–9), shown while the
// pharos-unified-activity-v1 preference is on. It renders only from the
// status store (status-store.js) and, while on, is the one renderer of the
// header's drive chip, its panel and the header's Index changes state;
// library.js and sync.js leave those alone. Off, it does nothing at all and
// library.js's drive panel runs as before.
//
// The panel answers, in this order: is it safe to eject; is the data
// current (the freshness ledger's summary groups; omitted while the
// snapshot has none); is Pharos busy on this Mac (phase 4; omitted while
// null); what is running, one line per top-level operation; then "Show all"
// for the operation tree and each data domain, the other libraries, and
// what needs attention. Every sentence comes from the server; this file
// formats numbers and durations only. Explanations are in tooltips.
//
// panelModel and chipModel are pure, for web/tests/unit/page/status-panel.test.mjs.
(() => {
  'use strict';
  const CSS = `
.pharos-status-panel .pharos-status-rows{display:grid;grid-template-columns:auto minmax(0,1fr);gap:6px 12px;margin:12px 0 4px;align-items:baseline}
.pharos-status-rows>dt{color:var(--muted,#666);font-size:12px;letter-spacing:.04em;text-transform:uppercase}
.pharos-status-rows>dd{margin:0;min-width:0;overflow-wrap:anywhere}
.pharos-status-verdict{display:flex;gap:8px;align-items:baseline}
.pharos-status-verdict>.pharos-dot{position:relative;top:-1px}
.pharos-status-line{display:block}
.pharos-status-line+.pharos-status-line{margin-top:6px}
.pharos-status-step{display:block;font-size:12px}
.pharos-status-finished{margin-top:4px;font-size:12px}
.pharos-status-line>strong{font-weight:650}
.pharos-status-line .pharos-status-time{color:var(--muted,#666);white-space:nowrap}
.pharos-status-more{display:flex;flex-wrap:wrap;justify-content:space-between;gap:4px 12px;color:var(--muted,#666)}
.pharos-status-tree{margin:8px 0 0;padding-left:16px}
.pharos-status-tree ul{padding-left:16px}
.pharos-status-tree li{margin:3px 0;overflow-wrap:anywhere}
.pharos-status-domains{margin-top:10px;border-top:1px solid var(--line,#ddd);padding-top:8px}
.pharos-status-panel details>summary{cursor:pointer}
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

  // The Data row and its detail (docs/background-work.md §5): the server's
  // summary groups that are shown, each "Label: text (age)", and under Show
  // all one line per domain. The server says every word; this adds ages and
  // counts. Null until the snapshot has a ledger.
  function dataModel(library, now = Date.now()) {
    if (!Array.isArray(library?.freshness)) return null;
    const age = (value, label) => value ? ` (${[label, ago(value, now)].filter(Boolean).join(' ')})` : '';
    const line = (library.freshness_summary || []).filter(group => group.shown).map(group => ({
      group: group.group, state: group.state, label: group.label, text: `${group.text}${age(group.as_of, group.as_of_label)}`, tooltip: group.tooltip || ''}));
    const domains = library.freshness.map(row => {
      const behind = row.behind || {};
      const amount = behind.count > 0 && behind.unit ? `${count(behind.count)} ${unitName(behind.unit, behind.count)}` : '';
      const reason = row.reason?.text ? (row.catching_up_label ? `${row.reason.text} (${row.catching_up_label})` : row.reason.text) : '';
      return {domain: row.domain, group: row.group, state: behind.state, label: row.label,
        text: [row.state_label, reason, amount].filter(Boolean).join(' · ') + (row.as_of ? ` · ${ago(row.as_of, now)}` : ''),
        tooltip: [row.reason?.detail, row.next].filter(Boolean).join('\n')};
    });
    return {line, domains};
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

  function treeNode(operation) {
    const line = operationLine(operation);
    return {id: operation.id, label: operation.label, text: [...line.primary, ...line.secondary].join(' · '), time: line.time, tooltip: line.tooltip,
      children: (operation.children || []).filter(running).map(treeNode),
      hidden: Math.max(0, (operation.children_count || 0) - (operation.children || []).filter(running).length)};
  }

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
  function panelModel(view, {ejectState = null, verdict = null, unavailable = null, nativeEject = false, stopRequested = new Set()} = {}) {
    const library = view?.library || null;
    const operations = currentOperations(view);
    const foreground = operations.filter(operation => operation.priority !== 'background');
    const shown = foreground.length ? foreground : operations.slice(0, 1);
    // Show all reveals what Now does not: the shown runs' running stages,
    // and the other work. Finished runs are counted apart.
    const tree = [...shown.flatMap(operation => treeNode(operation).children), ...operations.filter(operation => !shown.includes(operation)).map(treeNode)];
    const finished = (library?.operations || []).filter(operation => !running(operation)).length;
    const common = {lines: shown.map(operationLine), tree, moreCount: tree.length, finished, attention: (view?.attention || []).map(attentionModel)};
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
      // A null keeps the row out (phase 4 fills load).
      freshness: dataModel(library),
      load: library.resources ?? null,
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
  let panel = null, ejectState = null, showAll = false, shown = null, holdTimer = null, sheet = null, subscribed = null;
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
    const head = node('div', 'pharos-panel-head'), heading = node('h2', '', model.title);
    head.append(heading);
    const actions = node('div', 'pharos-status-actions');
    actions.dataset.key = 'actions';
    if (model.stop) {
      const stop = button(model.stop.stopping ? 'Stopping…' : 'Stop', '', () => stopAll(model.stop.targets));
      stop.dataset.key = 'stop';
      stop.disabled = Boolean(model.stop.stopping);
      if (model.stop.title) stop.title = model.stop.title;
      actions.append(stop);
    }
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
      const rows = node('dl', 'pharos-status-rows');
      appendWork(content, rows, model);
      appendAttention(content, model.attention);
      commit(content, scrollTop);
      return;
    }
    if (model.where) content.append(node('div', 'pharos-sub', model.where));
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
    const rows = node('dl', 'pharos-status-rows');
    const row = (key, label, value) => {
      const term = node('dt', '', label), detail = node('dd');
      term.dataset.key = `${key}-label`;
      detail.dataset.key = key;
      detail.append(...[value].flat());
      rows.append(term, detail);
      return detail;
    };
    const verdict = node('div', 'pharos-status-verdict');
    verdict.append(node('span', 'pharos-dot' + (model.verdict.dot ? ` ${model.verdict.dot}` : '')), node('span', '', model.verdict.text));
    if (model.verdict.tooltip) verdict.title = model.verdict.tooltip;
    row('eject', 'Eject', verdict);
    if (model.freshness?.line.length) row('data', 'Data', model.freshness.line.map((group, index) => {
      const item = node('span', 'pharos-status-data-group');
      item.dataset.key = `data:${group.group}`;
      item.append(index ? ' · ' : '', node('strong', '', `${group.label}:`), ` ${group.text}`);
      if (group.tooltip) item.title = group.tooltip;
      return item;
    }));
    if (model.load) row('load', 'Load', node('span', '', loadText(model.load)));
    appendWork(content, rows, model);
    if (model.others.length) {
      const others = node('dl', 'pharos-status-rows');
      others.dataset.key = 'others';
      model.others.forEach((other, index) => {
        const term = node('dt', '', index ? '' : 'Other'), detail = node('dd');
        detail.dataset.key = `other:${other.id}`;
        detail.append(node('strong', '', other.name));
        if (other.text) detail.append(` — ${other.text}`);
        if (other.tooltip) detail.title = other.tooltip;
        others.append(term, detail);
      });
      content.append(others);
    }
    appendAttention(content, model.attention);
    commit(content, scrollTop);
  }

  // The Now row and Show all.
  function appendWork(content, rows, model) {
    if (model.lines.length) {
      const lines = model.lines.map(line => {
        const item = node('span', 'pharos-status-line');
        item.dataset.key = `line:${line.id}`;
        const primary = node('span');
        primary.dataset.key = 'primary';
        primary.append(node('strong', '', line.label));
        if (line.primary.length) primary.append(` · ${line.primary.join(' · ')}`);
        if (line.time) primary.append(' ', node('span', 'pharos-status-time', line.time));
        item.append(primary);
        if (line.secondary.length) item.append(node('span', 'pharos-sub pharos-status-step', line.secondary.join(' · ')));
        if (line.tooltip) item.title = line.tooltip;
        if (line.fraction != null) item.setAttribute('aria-description', `${Math.floor(line.fraction * 100)}% of this step`);
        return item;
      });
      const term = node('dt', '', 'Now'), detail = node('dd');
      term.dataset.key = 'now-label';
      detail.dataset.key = 'now';
      detail.append(...lines);
      rows.append(term, detail);
    }
    if (rows.childElementCount) content.append(rows);
    const domains = model.freshness?.domains || [];
    if (!model.moreCount && !model.finished && !domains.length) return;
    const details = node('details', 'pharos-status-all');
    details.dataset.key = 'show-all';
    if (showAll) details.open = true;
    details.addEventListener('toggle', () => { showAll = details.open; });
    const summary = node('summary', 'pharos-status-more');
    summary.append(node('span', '', model.moreCount ? `+ ${model.moreCount} more` : ''), node('span', '', 'Show all'));
    details.append(summary);
    if (model.tree.length) details.append(treeList(model.tree));
    if (model.finished) details.append(node('div', 'pharos-sub pharos-status-finished', `${model.finished} finished`));
    if (domains.length) details.append(dataList(domains));
    content.append(details);
  }

  // Show all's per-domain detail.
  function dataList(domains) {
    const list = node('ul', 'pharos-status-tree pharos-status-domains');
    list.dataset.key = 'domains';
    list.setAttribute('aria-label', 'Data');
    for (const domain of domains) {
      const entry = node('li', domain.state === 'current' ? 'pharos-sub' : '');
      entry.dataset.key = `domain:${domain.domain}`;
      entry.append(node('strong', '', domain.label), ` · ${domain.text}`);
      if (domain.tooltip) entry.title = domain.tooltip;
      list.append(entry);
    }
    return list;
  }
  const loadText = value => [Number.isFinite(value?.cpu_percent) ? `${Math.round(value.cpu_percent)}% of one core` : '', Number.isFinite(value?.memory_bytes) ? size(value.memory_bytes) : ''].filter(Boolean).join(' · ');

  function treeList(items) {
    const list = node('ul', 'pharos-status-tree');
    for (const item of items) {
      const entry = node('li');
      entry.dataset.key = `tree:${item.id}`;
      entry.append(node('strong', '', item.label));
      if (item.text) entry.append(` · ${item.text}`);
      if (item.time) entry.append(' ', node('span', 'pharos-status-time', item.time));
      if (item.tooltip) entry.title = item.tooltip;
      if (item.children.length) entry.append(treeList(item.children));
      if (item.hidden) entry.append(node('div', 'pharos-sub', `+ ${item.hidden} finished`));
      list.append(entry);
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
    panelModel, chipModel, operationLine, lineText, treeNode, currentOperations, settleVerdict, ejectDecision, indexBusy, stepAmount, dataModel, ago,
  };
  if (enabled()) activate();
})();
