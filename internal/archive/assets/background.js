// Settings → Background Processing (docs/background-work.md §11): the three
// cadences, each item's cost and state, and the controls that steer them,
// from GET /api/background. The server says every sentence; this file adds
// ages, times and durations. It renders into #backgroundSchedule, which
// sync.js keeps when it redraws the rest of the page.
//
// The model functions are pure, for web/tests/unit/page/background.test.mjs.
(() => {
  'use strict';
  if (window.pharosBackground) return;
  const CSS = `
#backgroundSchedule{display:grid;gap:18px;min-width:0;--bg-indent:22px;container-type:inline-size}
.bg-card{border:1px solid var(--line);border-radius:8px;padding:18px;background:var(--panel);min-width:0}
.bg-card h3{margin:0;font-size:var(--fs-lg)}
.bg-card p{margin:6px 0 0;font-size:var(--fs-sm)}
.bg-mode{display:flex;flex-wrap:wrap;align-items:center;gap:12px 18px}
.bg-mode>div:first-child{flex:1 1 220px;min-width:0}
.bg-seg{display:inline-flex;border:1px solid var(--line);border-radius:6px;overflow:hidden}
.bg-seg button{border:0;border-radius:0;background:transparent;color:var(--ink);font:inherit;font-size:var(--fs-sm);padding:7px 12px;cursor:pointer}
.bg-seg button+button{border-left:1px solid var(--line)}
.bg-seg button[aria-pressed=true]{background:var(--accent);color:var(--bg);font-weight:650}
#backgroundSchedule .bg-button{padding:7px 14px;border:1px solid var(--line);border-radius:6px;background:var(--panel);color:var(--ink);font:inherit;font-size:var(--fs-sm);font-weight:600;cursor:pointer}
#backgroundSchedule .bg-button:hover:not(:disabled),.bg-seg button:hover{border-color:var(--accent)}
#backgroundSchedule button:disabled{opacity:.55;cursor:default}
.bg-everything{flex-basis:100%;margin:0!important;color:var(--muted)}
.bg-cols{display:grid;grid-template-columns:repeat(var(--bg-across,3),minmax(0,1fr));gap:12px;margin-top:16px}
.bg-cols.bg-four{--bg-across:2}
@container (min-width:1240px){.bg-cols.bg-four{--bg-across:4}}
.bg-col{border:1px solid var(--line);border-radius:8px;padding:12px 14px;min-width:0;background:var(--bg)}
.bg-col-head{display:flex;flex-wrap:wrap;align-items:baseline;gap:4px 8px}
.bg-col-head h4{display:flex;align-items:center;gap:8px;margin:0;font-size:var(--fs-md)}
#backgroundSchedule .bg-run{display:inline-flex;align-items:center;justify-content:center;width:20px;height:20px;padding:0;border:1px solid var(--line);border-radius:4px;background:var(--panel);color:var(--ink);cursor:pointer}
#backgroundSchedule .bg-run:hover:not(:disabled){border-color:var(--accent)}
#backgroundSchedule .bg-item-line .bg-run{width:15px;height:15px;border-radius:3px}
.bg-play{display:inline-block;width:7px;height:10px;margin-right:7px;background:currentColor;clip-path:polygon(0 0,100% 50%,0 100%)}
.bg-run .bg-play{margin:0 0 0 1px}
.bg-item-line .bg-run .bg-play{width:5px;height:8px}
.bg-item[draggable=true]{cursor:grab;position:relative}
.bg-grip{position:absolute;left:0;top:50%;transform:translateY(-50%);width:8px;height:11px;background:radial-gradient(circle,var(--muted) 1px,transparent 1.3px) 0 0/4px 4px;opacity:.7}
.bg-item[draggable=true]:hover .bg-grip{opacity:1}
.bg-item.bg-dragging{opacity:.5}
.bg-col.bg-drop{border-color:var(--accent);box-shadow:inset 0 0 0 1px var(--accent)}
.bg-col-head .bg-state{margin-left:auto}
.bg-when{font-size:var(--fs-sm);color:var(--muted);margin:4px 0 8px}
.bg-when select{font:inherit;font-size:var(--fs-sm);border:1px solid var(--line);border-radius:5px;padding:1px 4px;background:var(--panel);color:var(--ink)}
.bg-state{font-size:var(--fs-sm);color:var(--muted);overflow-wrap:anywhere}
.bg-item{border-top:1px solid var(--line);padding:8px 0}
.bg-item:last-child{padding-bottom:0}
.bg-item-line{display:flex;align-items:center;gap:8px}
.bg-item-head{display:flex;align-items:baseline;gap:8px;min-width:0;border:0;background:none;padding:0;color:var(--ink);font:inherit;text-align:left;cursor:pointer}
.bg-item-head:hover .bg-name{color:var(--accent)}
.bg-name{font-weight:650;min-width:0;overflow-wrap:anywhere}
.bg-cost{margin-left:auto;flex:none;font-size:var(--fs-xs);padding:0 7px;border:1px solid var(--line);border-radius:10px;color:var(--muted)}
.bg-cost.light{color:var(--accent)}
.bg-cost.heavy{color:var(--warn);border-color:var(--warn)}
.bg-sub,.bg-item .bg-state{display:block;font-size:var(--fs-sm);color:var(--muted);padding-left:var(--bg-indent)}
.bg-note{display:block;font-size:var(--fs-sm);color:var(--accent);padding-left:var(--bg-indent)}
.bg-detail{margin:8px 0 0 var(--bg-indent);padding:10px 12px;border-radius:6px;background:var(--panel);font-size:var(--fs-sm)}
.bg-detail p{margin:0 0 6px}
.bg-detail .bg-row{display:flex;flex-wrap:wrap;align-items:center;gap:8px;margin-top:8px}
.bg-detail select{font:inherit;font-size:var(--fs-sm);border:1px solid var(--line);border-radius:5px;padding:4px 6px;background:var(--bg);color:var(--ink);max-width:100%}
.bg-detail .bg-row>label{min-width:0;max-width:100%}
#backgroundSchedule .bg-detail .bg-button{max-width:100%;white-space:normal;text-align:left}
.bg-detail .bg-moves{gap:6px}
#backgroundSchedule .bg-moves .bg-button{padding:4px 8px;border-radius:4px;font-size:var(--fs-xs);font-weight:500;line-height:1.3}
.bg-error{color:var(--bad)}
.bg-sources{border:0;margin:6px 0 0;padding:0}
.bg-sources legend{font-weight:600;padding:0}
.bg-sources .bg-check{margin-top:4px}
.bg-dot{display:inline-block;width:8px;height:8px;border-radius:50%;background:var(--accent);flex:none;position:relative;top:-1px;margin-right:6px}
.bg-dot.current{background:#8cc6a0}
.bg-dot.busy{animation:bg-pulse 1.6s ease-in-out infinite}
.bg-dot.behind{background:var(--warn)}
.bg-dot.bad{background:var(--bad)}
.bg-dot.paused{background:transparent;box-shadow:inset 0 0 0 2px var(--muted)}
@keyframes bg-pulse{50%{opacity:.45}}
@media(prefers-reduced-motion:reduce){.bg-dot.busy{animation:none}}
.bg-rule{display:flex;flex-wrap:wrap;gap:4px 16px;margin-top:14px;padding-top:10px;border-top:1px dashed var(--line);font-size:var(--fs-sm);color:var(--muted)}
.bg-busy{display:inline-flex;align-items:center;gap:9px;font-size:var(--fs-sm);cursor:pointer}
.bg-busy input{appearance:none;position:relative;width:34px;height:20px;margin:0;border:1px solid var(--line);border-radius:20px;background:var(--line);cursor:pointer;flex:none}
.bg-busy input::after{content:"";position:absolute;top:2px;left:2px;width:14px;height:14px;border-radius:50%;background:var(--ink);transition:transform .15s ease}
.bg-busy input:checked{background:var(--accent);border-color:var(--accent)}
.bg-busy input:checked::after{background:var(--bg);transform:translateX(14px)}
.bg-busy input:focus-visible{outline:2px solid var(--focus-ring, var(--accent));outline-offset:2px}
.bg-busy-row{margin:8px 0 2px}
.bg-check{display:flex;gap:8px;align-items:flex-start;margin-top:10px;font-size:var(--fs-sm)}
.bg-battery{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:10px;margin-top:12px}
.bg-battery-row{display:grid;gap:4px;font-size:var(--fs-sm);font-weight:600;min-width:0}
.bg-battery-row select{font:inherit;font-weight:400;border:1px solid var(--line);border-radius:5px;padding:5px 6px;background:var(--bg);color:var(--ink);min-width:0}
.bg-check input{margin-top:3px;accent-color:var(--accent)}
.bg-detail .optional-behavior{padding:14px 0 0;border-bottom:0}
.bg-detail .optional-behavior h4{margin:0;font-size:var(--fs-md)}
.bg-detail .optional-behavior p{margin:4px 0 0;overflow-wrap:anywhere}
.bg-labeler-heading{display:flex;flex-wrap:wrap;align-items:center;gap:4px 14px}
.bg-labeler-meta{display:flex;flex-wrap:wrap;align-items:center;gap:4px 12px;font-size:var(--fs-sm);color:var(--muted)}
.bg-threads{display:inline-flex;align-items:center;gap:6px;font-size:var(--fs-sm);color:var(--muted)}
.bg-threads select{font:inherit;font-size:var(--fs-sm);border:1px solid var(--line);border-radius:5px;padding:1px 4px;background:var(--panel);color:var(--ink)}
@media(max-width:900px){.bg-cols,.bg-battery{grid-template-columns:minmax(0,1fr)}}
@container (max-width:560px){.bg-cols{grid-template-columns:minmax(0,1fr)}}`;

  const CADENCE_ICONS = {frequent: '↻', hourly: '◷', daily: '☾'};
  const CADENCE_LABELS = {frequent: 'Frequently', hourly: 'Hourly', daily: 'Daily'};
  const DOTS = {current: 'current', running: 'busy', queued: 'busy', failed: 'bad', missed: 'behind', never: 'behind', paused: 'paused', manual: 'paused', unavailable: 'paused', disabled: 'paused', power: 'paused'};

  // ---- times and durations (the only words this file makes)
  const parse = value => { const time = Date.parse(value); return Number.isFinite(time) ? time : null; };
  function ago(value, now = Date.now()) {
    const time = parse(value);
    if (time == null) return '';
    const seconds = Math.max(0, (now - time) / 1000);
    if (seconds < 60) return 'just now';
    if (seconds < 3600) return `${Math.floor(seconds / 60)} min ago`;
    if (seconds < 86400 * 2) return `${Math.floor(seconds / 3600)} h ago`;
    return `${Math.floor(seconds / 86400)} d ago`;
  }
  const clock = time => new Date(time).toLocaleTimeString([], {hour: 'numeric', minute: '2-digit'});
  // When something next happens: "now", "in 3 min", "at 5:00 PM",
  // "tomorrow at 3:00 AM", "Fri at 3:00 AM".
  function until(value, now = Date.now()) {
    const time = parse(value);
    if (time == null) return '';
    const seconds = (time - now) / 1000;
    if (seconds < 60) return 'now';
    if (seconds < 3600) return `in ${Math.round(seconds / 60)} min`;
    const day = date => new Date(date).toDateString();
    if (day(time) === day(now)) return `at ${clock(time)}`;
    if (day(time) === day(now + 86400000)) return `tomorrow at ${clock(time)}`;
    return `${new Date(time).toLocaleDateString([], {weekday: 'short'})} at ${clock(time)}`;
  }
  function duration(seconds) {
    const value = Math.max(0, Number(seconds) || 0);
    if (value < 60) return `${Math.round(value)} s`;
    if (value < 3600) return `${Math.round(value / 60)} min`;
    return `${(value / 3600).toFixed(value < 36000 ? 1 : 0)} h`;
  }

  // An item's or a cadence's state line: the server's label, then the age
  // or the next time that goes with it.
  function stateText(entry, now = Date.now()) {
    if (!entry) return '';
    const label = entry.state_label || '';
    const next = entry.next_at ? until(entry.next_at, now) : '';
    switch (entry.state) {
      case 'current': {
        const parts = [entry.as_of ? `${label} ${ago(entry.as_of, now)}` : label];
        if (next) parts.push(`next ${next}`);
        return parts.join(' · ');
      }
      case 'failed': return next ? `${label} · retrying ${next}` : label;
      case 'missed': return next ? `${label} · runs ${next}` : label;
      case 'never': return next ? `${label} · runs ${next}` : label;
      case 'paused': case 'manual': return entry.as_of ? `${label} · ${ago(entry.as_of, now)}` : label;
      default: return label;
    }
  }

  // The page's model from GET /api/background.
  function scheduleModel(document, now = Date.now(), pendingCadences = {}) {
    const view = document?.view;
    if (!view) return null;
    const items = (view.items || []).map(item => {
      const model = itemModel(item, now);
      if (!Object.hasOwn(pendingCadences, item.id)) return model;
      const cadence = pendingCadences[item.id];
      return {...model, column: cadence || model.usual, temporary: cadence ? {cadence} : null,
        text: 'Saving schedule…', note: '', moves: []};
    });
    const byId = Object.fromEntries(items.map(item => [item.id, item]));
    // Keep the last server snapshot while saves run, but place each pending
    // move immediately. Keep the server's order and append incoming items.
    const placed = new Map((view.cadences || []).flatMap(row => (row.items || []).map(id => [id, row.id])));
    for (const id of view.unscheduled || []) placed.set(id, 'manual');
    for (const item of items) if (Object.hasOwn(pendingCadences, item.id)) placed.set(item.id, item.column);
    const inColumn = (id, order = []) => [...new Set([...order, ...items.map(item => item.id)])]
      .filter(item => placed.get(item) === id).map(item => byId[item]).filter(Boolean);
    // Every cadence has a column, an empty one too (the view leaves it
    // out), so it stays somewhere to drag an item to.
    const rows = Object.fromEntries((view.cadences || []).map(row => [row.id, row]));
    const columns = Object.keys(CADENCE_ICONS).map(id => rows[id] || {id, label: CADENCE_LABELS[id], items: []}).map(row => ({
      id: row.id, label: row.label, icon: CADENCE_ICONS[row.id] || '', description: document.descriptions?.[row.id] || row.description || '',
      dot: DOTS[row.state] ?? '', text: stateText(row, now), running: row.state === 'running', empty: !inColumn(row.id, row.items).length,
      items: inColumn(row.id, row.items),
    }));
    // Only when you ask is the fourth column, on no automatic schedule.
    const unscheduled = inColumn('manual', view.unscheduled);
    // Only when you ask runs as the manual cadence: not while one of its
    // items runs, nor when none of them can.
    const unscheduledRunnable = unscheduled.length > 0 && !unscheduled.some(item => item.state === 'running') && !unscheduled.every(item => ['unavailable', 'disabled'].includes(item.state));
    const mode = document.settings?.mode || view.mode;
    return {mode, pause: mode === 'paused' ? pauseLength(document.settings?.paused_until || view.paused_until, now) : '',
      settings: document.settings || {}, columns, unscheduled, unscheduledRunnable, everything: view.everything?.text || '',
      rule: document.rule || null, headline: view.headline || null, battery: batteryModel(document), labels: document.labels || null,
      agentEvents: document.agent_events || null};
  }

  // Which pause choice a pause is: one that ends within the hour is the
  // hour's, a longer one the day's.
  function pauseLength(pausedUntil, now = Date.now()) {
    const until = parse(pausedUntil);
    return until != null && until - now > 3600000 ? 'day' : 'hour';
  }

  // On battery: each cadence's choice, and the low-battery rule. Null on a
  // Mac with no battery, where it never applies.
  function batteryModel(document) {
    const power = document?.power, settings = document?.settings || {};
    if (!power?.battery || !settings.battery) return null;
    const words = {same: 'Same as plugged in', slower: `Every ${settings.battery_slower_minutes || 15} min`, wait: 'Wait until plugged in'};
    const charge = Number.isFinite(power.percent) && power.percent >= 0 ? ` (${power.percent}%)` : '';
    return {
      status: `${power.on_battery ? 'This Mac is on battery' : 'This Mac is plugged in'}${charge}${power.low_power_mode ? ', in Low Power Mode' : ''}.`,
      onBattery: Boolean(power.on_battery),
      rows: ['frequent', 'hourly', 'daily'].map(cadence => ({cadence, label: CADENCE_LABELS[cadence], value: settings.battery[cadence] || 'same',
        choices: (settings.battery_choices?.[cadence] || ['same', 'wait']).map(value => ({value, label: words[value] || value}))})),
      lowWaits: settings.battery.low_battery_waits !== false,
      lowText: `Below ${settings.low_battery_percent || 20}% or in Low Power Mode, everything waits until plugged in`,
    };
  }

  function itemModel(item, now = Date.now()) {
    // How long a run typically takes leads the state line, so its cost
    // reads beside its schedule.
    const text = [item.cost?.typical || '', stateText(item, now)].filter(Boolean).join(' · ');
    return {id: item.id, label: item.label, sub: item.sub || '', feeds: item.feeds || '', why: item.why || '', note: item.note || '',
      dot: DOTS[item.state] ?? '', state: item.state, text, error: item.error || '',
      adaptive: Boolean(item.adaptive), column: item.cadence === 'manual' ? 'manual' : item.column || '', usual: item.usual_cadence || '',
      cost: item.cost ? {class: item.cost.class || '', text: item.cost.text || ''} : null,
      choices: item.choices || [], override: item.override || '', cadenceLabel: item.cadence_label || '', fixed: Boolean(item.fixed),
      moves: item.cadence_moves || [], temporary: item.temporary_cadence || null, stop: item.stop?.id ? {id: item.stop.id} : null,
      sources: Array.isArray(item.sources) ? item.sources.map(source => ({name: source.name, selected: Boolean(source.selected)})) : null};
  }

  // What dropping an item on a cadence's column saves as its temporary
  // schedule: that cadence for a week, "" to end the week when it is the
  // item's usual cadence, or null when it cannot move there.
  function dropCadence(item, cadence) {
    if (!item?.id || !['frequent', 'hourly', 'daily', 'manual'].includes(cadence) || cadence === item.column) return null;
    return item.temporary && cadence === item.usual ? '' : cadence;
  }

  // Serialize settings writes so rapid moves reach the server in order.
  // Pending moves remain visible over each response until their own save
  // settles; a failed save removes only that move, revealing the saved state.
  function createSettingsQueue(send, accept, changed) {
    const pending = [];
    let tail = Promise.resolve(), revision = 0;
    return {
      get busy() { return pending.length > 0; },
      get revision() { return revision; },
      get cadences() { return Object.assign({}, ...pending.map(entry => entry.body.temporary_cadences)); },
      save(body) {
        const entry = {body};
        pending.push(entry);
        revision++;
        if (body.temporary_cadences) changed();
        const result = tail.then(() => send(body)).then(accept).finally(() => {
          pending.splice(pending.indexOf(entry), 1);
          revision++;
          changed();
        });
        tail = result.catch(() => {});
        return result;
      },
    };
  }

  function labelingControls(settings) {
    const execution = settings.execution || {device: 'unknown', status: 'Device not yet confirmed', detail: 'The device will be confirmed when labeling starts.'};
    const notice = settings.notice || (settings.enabled && execution.device === 'unavailable' ? 'Processing is unavailable. Check the local model and runtime installation.' : '');
    return {execution, notice, threadChoices: execution.device === 'cpu' ? settings.thread_choices || [] : [],
      threadsCanChange: execution.device === 'cpu' && settings.threads_can_change === true};
  }

  // ---- rendering
  // The Agent Experience switch (web/src/agent-events-flag.ts).
  const AGENT_EVENTS_KEY = 'pharos-agent-events-v1';
  let document_ = null, dragging = null, inFlight = false, fetchedAt = 0, sheet = null, labelsSaving = false, labelsRevision = 0;
  const node = (tag, className = '', text) => {
    const element = document.createElement(tag);
    if (className) element.className = className;
    if (text != null) element.textContent = text;
    return element;
  };
  const host = () => document.getElementById('backgroundSchedule');
  const toast = message => window.pharosLibrary?.toast?.(message) ?? window.feedbackToast?.(message);
  async function call(path, body) {
    const response = await fetch(path, body === undefined ? {} : {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body)});
    const value = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(value.error || `HTTP ${response.status}`);
    return value;
  }
  const button = (text, action, className = 'bg-button') => {
    const element = node('button', className, text);
    element.type = 'button';
    element.onclick = async () => {
      element.disabled = true;
      try { await action(); } catch (error) { toast(error.message); } finally { element.disabled = false; }
    };
    return element;
  };
  const settingsQueue = createSettingsQueue(body => call('/api/background/settings', body), next => {
    document_ = next;
    fetchedAt = Date.now();
  }, () => render());
  const save = body => settingsQueue.save(body);

  function select(options, value, onchange, label) {
    const element = node('select');
    element.setAttribute('aria-label', label);
    for (const [optionValue, text] of options) {
      const option = node('option', '', text);
      option.value = optionValue;
      element.append(option);
    }
    if (![...element.options].some(option => option.value === String(value))) {
      const option = node('option', '', String(value));
      option.value = value;
      element.prepend(option);
    }
    element.value = String(value);
    element.onchange = () => onchange(element.value).catch(error => toast(error.message));
    return element;
  }

  // A drawn play triangle: the ▶ glyph's rounded corners blur at this size.
  function play() {
    const icon = node('span', 'bg-play');
    icon.setAttribute('aria-hidden', 'true');
    return icon;
  }

  // ▶ beside a cadence's name runs all its work now, as the status
  // panel's does; beside an item's, that item.
  function runButton(label, body, disabled) {
    if (typeof body === 'string') body = {cadence: body};
    // Columns show pending moves before the server uses that membership.
    const waitingForMoves = () => Boolean(body.cadence && Object.keys(settingsQueue.cadences).length);
    const run = button('', async () => {
      if (waitingForMoves()) return;
      await call('/api/background/run', body);
      toast(`${label} runs now.`);
      refresh(true);
    }, 'bg-run');
    run.append(play());
    run.dataset.focusKey = 'run:' + (body.item || body.cadence);
    run.disabled = disabled || waitingForMoves();
    run.title = `Run ${label} now`;
    run.setAttribute('aria-label', `Run ${label} now`);
    return run;
  }

  // Dropping a dragged item on a column moves it there for a week, as its
  // Move buttons do.
  function dropTarget(section, cadence) {
    const accepts = () => dropCadence(dragging, cadence) != null;
    section.ondragover = event => {
      if (!accepts()) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = 'move';
      section.classList.add('bg-drop');
    };
    section.ondragleave = event => { if (!section.contains(event.relatedTarget)) section.classList.remove('bg-drop'); };
    section.ondrop = event => {
      section.classList.remove('bg-drop');
      const item = dragging, value = dropCadence(item, cadence);
      if (value == null) return;
      event.preventDefault();
      dragging = null;
      save({temporary_cadences: {[item.id]: value}}).catch(error => toast(error.message));
    };
  }

  function renderColumns(model) {
    const columns = node('div', 'bg-cols bg-four');
    for (const column of model.columns) {
      const section = node('section', 'bg-col');
      section.setAttribute('aria-label', column.label);
      section.dataset.cadence = column.id;
      dropTarget(section, column.id);
      const head = node('div', 'bg-col-head');
      const title = node('h4', '', `${column.icon} ${column.label}`);
      title.append(runButton(column.label, column.id, column.running || column.empty));
      const state = node('span', 'bg-state');
      state.append(node('span', `bg-dot ${column.dot}`), column.text);
      head.append(title, state);
      const when = node('div', 'bg-when');
      if (column.id === 'frequent') {
        const minutes = Math.round((model.settings.interval_seconds || 300) / 60);
        when.append('Every ', select([[2, '2'], [5, '5'], [15, '15'], [30, '30']], minutes, value => save({interval_seconds: Number(value) * 60}), 'Minutes between frequent updates'), ' min, as soon as there’s new work');
      } else if (column.id === 'daily') {
        const hours = ['00:00', '01:00', '02:00', '03:00', '04:00', '05:00', '06:00', '12:00', '18:00', '22:00', '23:00'];
        const label = value => new Date(`2000-01-01T${value}:00`).toLocaleTimeString([], {hour: 'numeric', minute: '2-digit'});
        when.append('Overnight at ', select(hours.map(value => [value, label(value)]), model.settings.daily_at || '03:00', value => save({daily_at: value}), 'Daily update time'), ', or the first quiet moment after');
      } else {
        when.textContent = column.description;
      }
      section.append(head);
      if (column.id === 'hourly' || column.id === 'daily') {
        const hold = node('label', 'bg-busy');
        hold.title = 'Shared by Hourly and Daily';
        const box = node('input');
        box.type = 'checkbox';
        box.setAttribute('role', 'switch');
        box.dataset.focusKey = 'busy:' + column.id;
        box.checked = model.settings.hold_when_busy !== false;
        box.onchange = async () => {
          const checked = box.checked;
          host()?.querySelectorAll('.bg-busy input').forEach(input => { input.checked = checked; });
          try { await save({hold_when_busy: checked}); }
          catch (error) {
            host()?.querySelectorAll('.bg-busy input').forEach(input => { input.checked = !checked; });
            toast(error.message);
          }
        };
        hold.append(box, 'Hold while busy');
        const row = node('div', 'bg-busy-row');
        row.append(hold);
        section.append(row);
      }
      section.append(when);
      for (const item of column.items) section.append(renderItem(item, model));
      columns.append(section);
    }
    // Only when you ask is one more column; four go two by two until
    // they fit on one line.
    columns.append(renderUnscheduled(model.unscheduled, model.unscheduledRunnable, model));
    return columns;
  }

  function renderUnscheduled(items, runnable, model) {
    const section = node('section', 'bg-col bg-unscheduled');
    section.setAttribute('aria-label', 'Only when you ask');
    section.dataset.cadence = 'manual';
    dropTarget(section, 'manual');
    const head = node('div', 'bg-col-head'), title = node('h4', '', 'Only when you ask');
    title.append(runButton('Only when you ask', 'manual', !runnable));
    head.append(title);
    section.append(head, node('div', 'bg-when', 'On no schedule: these run when you choose Run now, or once you give them a schedule.'));
    for (const item of items) section.append(renderItem(item, model));
    return section;
  }

  function renderItem(item, model) {
    const entry = node('div', 'bg-item');
    entry.dataset.item = item.id;
    entry.id = 'background-' + item.id;
    if (item.id && item.state !== 'disabled') {
      entry.draggable = true;
      entry.ondragstart = event => {
        dragging = item;
        event.dataTransfer.effectAllowed = 'move';
        event.dataTransfer.setData('text/plain', item.label);
        entry.classList.add('bg-dragging');
      };
      entry.ondragend = () => {
        dragging = null;
        entry.classList.remove('bg-dragging');
        for (const column of host()?.querySelectorAll('.bg-drop') || []) column.classList.remove('bg-drop');
        render();
      };
    }
    const open = window.pharosView?.read('disclosures', {})['background:' + item.id] === true;
    const head = node('button', 'bg-item-head');
    head.type = 'button';
    head.dataset.viewKey = 'background:' + item.id;
    head.dataset.focusKey = 'head:' + item.id;
    head.dataset.viewDefault = 'false';
    head.setAttribute('aria-controls', 'background-detail-' + item.id);
    head.setAttribute('aria-expanded', String(open));
    head.append(node('span', `bg-dot ${item.dot}`), node('span', 'bg-name', item.label));
    // The name opens the details; ▶ beside it runs the item now.
    const headLine = node('div', 'bg-item-line');
    headLine.append(head, runButton(item.label, {item: item.id}, ['running', 'unavailable', 'disabled'].includes(item.state)));
    if (item.cost?.class) headLine.append(node('span', `bg-cost ${item.cost.class}`, {light: 'Light', moderate: 'Moderate', heavy: 'Heavy'}[item.cost.class]));
    entry.append(headLine, node('span', 'bg-sub', item.sub), node('span', 'bg-state', item.text));
    if (entry.draggable) {
      // A grip under the dot: this item drags to another schedule.
      const grip = node('span', 'bg-grip');
      grip.setAttribute('aria-hidden', 'true');
      entry.append(grip);
    }
    if (item.note) entry.append(node('span', 'bg-note', item.note));
    const detail = node('div', 'bg-detail');
    detail.id = 'background-detail-' + item.id;
    detail.hidden = !open;
    const line = (label, value) => { const p = node('p'); p.append(node('strong', '', `${label}: `), value); return p; };
    detail.append(line('Feeds', item.feeds), line('Usually takes', item.cost?.text || 'not measured yet'));
    if (item.why) detail.append(node('p', 'muted', item.why));
    if (item.error) detail.append(node('p', 'bg-error', item.error));
    if (item.sources?.length) detail.append(sourcePicker(item.sources));
    if (item.id === 'labels' && model.labels) detail.append(renderLabelSettings(model.labels));
    if (item.id === 'agent_events' && model.agentEvents) detail.append(renderAgentEventSettings(model.agentEvents));
    const row = node('div', 'bg-row');
    if (item.choices.length && item.state !== 'disabled') {
      const label = node('label', '', item.temporary ? 'Normally ' : 'Run ');
      label.append(select(item.choices.map(choice => [choice.value, choice.label]), item.override, value => save({overrides: {[item.id]: value}}), `When ${item.label} runs`));
      row.append(label);
    }
    // A running item that can stop offers Stop instead: it ends at its next
    // safe point, keeping what it finished, and its next run resumes it.
    const run = item.stop ? button('Stop', async () => {
      await call('/api/operations/stop', {id: item.stop.id});
      toast(`Stopping ${item.label}. What it finished is kept; its next run resumes it.`);
      refresh(true);
    }) : button('Run now', async () => {
      await call('/api/background/run', {item: item.id});
      toast(`${item.label} runs next.`);
      refresh(true);
    });
    run.disabled = ['unavailable', 'disabled'].includes(item.state);
    row.append(run);
    detail.append(row);
    if (item.temporary?.expires_at) {
      const expiry = new Date(item.temporary.expires_at).toLocaleString([], {month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit'});
      detail.append(line('Temporary schedule', `${item.cadenceLabel} until ${expiry}`));
    }
    if (item.state !== 'disabled' && (item.moves.length || item.temporary)) {
      const moves = node('div', 'bg-row bg-moves');
      for (const move of item.moves) {
        moves.append(button(move.label, () => save({temporary_cadences: {[item.id]: move.cadence}})));
      }
      if (item.temporary) moves.append(button('End temporary schedule', () => save({temporary_cadences: {[item.id]: ''}})));
      detail.append(moves);
    }
    entry.append(detail);
    return entry;
  }

  // The sources scheduled indexing reads: all of them (stored as none
  // chosen, so a new source is read too) or the ones checked; at least one.
  function sourcePicker(sources) {
    const fieldset = node('fieldset', 'bg-sources');
    fieldset.append(node('legend', '', 'Sources read on this schedule'));
    for (const source of sources) {
      const label = node('label', 'bg-check'), box = node('input');
      box.type = 'checkbox';
      box.checked = source.selected;
      box.onchange = async () => {
        const chosen = [...fieldset.querySelectorAll('input')].filter(input => input.checked).map(input => input.value);
        if (!chosen.length) { box.checked = true; toast('Choose at least one source, or choose Only when I ask.'); return; }
        try { await call('/api/sync/settings', {sources: chosen.length === sources.length ? [] : chosen}); await refresh(true); }
        catch (error) { box.checked = !box.checked; toast(error.message); }
      };
      box.value = source.name;
      label.append(box, source.name);
      fieldset.append(label);
    }
    return fieldset;
  }

  function render() {
    const target = host();
    // A drag keeps the items it is over.
    if (!target || !document_ || labelsSaving || dragging) return;
    // A select being used keeps its value until it is changed.
    if (target.contains(document.activeElement) && document.activeElement.matches('select')) return;
    // The focused run button or item keeps focus across the redraw.
    const focusKey = document.activeElement?.dataset?.focusKey;
    const focused = document.activeElement?.closest?.('[data-item]')?.dataset.item;
    const labelsFocused = ['labelsEnabled', 'agentEventsEnabled'].includes(document.activeElement?.id) && document.activeElement.id;
    const model = scheduleModel(document_, Date.now(), settingsQueue.cadences);
    if (!model) return;
    const content = node('div');
    const card = node('section', 'bg-card');
    const mode = node('div', 'bg-mode');
    const intro = node('div');
    intro.append(node('h3', '', 'Task Schedule'),
      node('p', 'muted', {on: 'Everything below runs automatically. Pharos measures every run and moves work between schedules as it gets faster or slower.',
        paused: 'Paused; everything resumes at the end of the pause.', manual: 'Nothing runs until you choose Update everything now.'}[model.mode] || ''));
    const seg = node('div', 'bg-seg');
    seg.setAttribute('role', 'group');
    seg.setAttribute('aria-label', 'Background Processing');
    for (const [body, label, pressed] of [[{mode: 'on'}, 'On', model.mode === 'on'],
      [{mode: 'pause', pause_minutes: 60}, 'Pause for 1 hour', model.pause === 'hour'],
      [{mode: 'pause', pause_minutes: 1440}, 'Pause for 1 day', model.pause === 'day'],
      [{mode: 'manual'}, 'Only when I ask', model.mode === 'manual']]) {
      const choice = button(label, () => save(body), '');
      choice.setAttribute('aria-pressed', String(pressed));
      seg.append(choice);
    }
    const everything = button('Update everything now', async () => {
      await call('/api/sync/check', {});
      toast('Updating everything now: new conversations first, then every analysis.');
    });
    everything.prepend(play());
    mode.append(intro, seg, everything);
    if (model.everything) mode.append(node('p', 'bg-everything', model.everything));
    card.append(mode, renderColumns(model));
    if (model.rule) {
      const rule = node('div', 'bg-rule');
      rule.append(node('strong', '', 'How work is placed:'), ...[`usually under ${duration(model.rule.frequent_seconds)} → Frequently`,
        `under ${duration(model.rule.hourly_seconds)} → Hourly`, 'longer → Daily', 'Choose an item for its cost, or to change or run it; drag one to another schedule for a week.'].map(text => node('span', '', text)));
      card.append(rule);
    }
    content.append(card);
    if (model.battery) {
      const battery = node('section', 'bg-card');
      battery.dataset.card = 'battery';
      battery.append(node('h3', '', 'On battery'), node('p', 'muted', `${model.battery.status} Choose what keeps running when this Mac is unplugged; Update everything now and Run now always run.`));
      const rows = node('div', 'bg-battery');
      for (const row of model.battery.rows) {
        const label = node('label', 'bg-battery-row');
        label.append(node('span', '', row.label), select(row.choices.map(choice => [choice.value, choice.label]), row.value, value => save({battery: {[row.cadence]: value}}), `${row.label} on battery`));
        rows.append(label);
      }
      const low = node('label', 'bg-check');
      const lowBox = node('input');
      lowBox.type = 'checkbox';
      lowBox.checked = model.battery.lowWaits;
      lowBox.onchange = () => save({battery: {low_battery_waits: lowBox.checked}}).catch(error => toast(error.message));
      low.append(lowBox, model.battery.lowText);
      battery.append(rows, low);
      content.append(battery);
    }
    target.replaceChildren(...content.childNodes);
    const keyed = focusKey && target.querySelector(`[data-focus-key="${window.CSS.escape(focusKey)}"]`);
    if (labelsFocused) target.querySelector(`#${labelsFocused}`)?.focus({preventScroll: true});
    else if (keyed) keyed.focus({preventScroll: true});
    else if (focused) target.querySelector(`[data-item="${window.CSS.escape(focused)}"] .bg-item-head`)?.focus({preventScroll: true});
  }

  // Model settings live with the schedule item they configure. Disabled
  // models stay in Only when you ask so their switches remain reachable.
  function renderLabelSettings(labels) {
    return labelSwitch('labelsEnabled', 'messageLabels', 'Message labels',
      'Classify requests, replies, feedback and more. Turning this off keeps its results.', labels,
      enabled => call('/api/labels/settings', {enabled}),
      threads => call('/api/labels/settings', {threads}));
  }

  function renderAgentEventSettings(events) {
    return labelSwitch('agentEventsEnabled', 'agentEvents', 'Agent events (preview)',
      'Find obstacles, discoveries and workarounds in agent replies from the last 90 days. Turning this off keeps its results.', events,
      async enabled => {
        window.pharosPrefs.set(AGENT_EVENTS_KEY, enabled);
        await window.pharosPrefs.flush?.();
        if (enabled) await call('/api/background/run', {item: 'agent_events'}).catch(() => {});
        return call('/api/background');
      },
      threads => call('/api/labels/settings', {agent_events_threads: threads}));
  }

  // Each model keeps its effective CPU control and returns the next document.
  function labelSwitch(id, prefix, text, about, settings, saveSwitch, saveThreads) {
    const controls = labelingControls(settings);
    const row = node('div', 'optional-behavior');
    const copy = node('div');
    const title = node('h4', '', text);
    title.id = `${prefix}Title`;
    const description = node('p', 'muted', about);
    description.id = `${prefix}Description`;
    const status = node('p', 'muted', controls.notice);
    status.hidden = !controls.notice;
    status.id = `${prefix}Status`;
    status.setAttribute('role', 'status');
    const toggle = node('button', 'toggle');
    toggle.id = id;
    toggle.type = 'button';
    toggle.setAttribute('role', 'switch');
    toggle.setAttribute('aria-labelledby', title.id);
    toggle.setAttribute('aria-describedby', `${description.id} ${status.id}`);
    const show = on => { toggle.classList.toggle('on', on); toggle.setAttribute('aria-checked', String(on)); };
    show(settings.enabled);
    toggle.disabled = !settings.can_change;
    toggle.onclick = async () => {
      const focused = document.activeElement === toggle;
      const enabled = toggle.getAttribute('aria-checked') !== 'true';
      show(enabled);
      labelsSaving = true;
      labelsRevision++;
      toggle.disabled = true;
      let saved = false;
      try {
        document_ = await saveSwitch(enabled);
        saved = true;
      } catch (error) {
        show(settings.enabled);
        status.textContent = error.message;
        status.hidden = false;
        toast(error.message);
      } finally {
        labelsSaving = false;
        toggle.disabled = !settings.can_change;
      }
      if (saved) render();
      if (focused && (document.activeElement === toggle || document.activeElement === document.body)) host()?.querySelector(`#${id}`)?.focus({preventScroll: true});
    };
    const heading = node('div', 'bg-labeler-heading');
    const meta = node('div', 'bg-labeler-meta');
    meta.append(node('span', '', controls.execution.status));
    heading.append(title, meta);
    copy.append(heading, description, status);
    if (controls.threadChoices.length) {
      const cores = node('label', 'bg-threads', 'Threads ');
      const choice = select(controls.threadChoices.map(threads => [threads, String(threads)]), settings.threads, async value => {
        document_ = await saveThreads(Number(value));
        render();
      }, `${text} CPU threads`);
      choice.dataset.focusKey = `${prefix}:threads`;
      choice.title = 'Changes apply within two minutes after current batches finish. Battery policy may lower this target.';
      choice.disabled = !controls.threadsCanChange;
      cores.append(choice);
      meta.append(cores);
    }
    row.append(copy, toggle);
    return row;
  }

  async function refresh(force = false) {
    const target = host();
    if (!target || inFlight || labelsSaving || settingsQueue.busy || (!force && (!target.offsetParent || Date.now() - fetchedAt < 4000))) return;
    inFlight = true;
    const revision = labelsRevision;
    const settingsRevision = settingsQueue.revision;
    try {
      const next = await call('/api/background');
      if (revision !== labelsRevision || labelsSaving || settingsRevision !== settingsQueue.revision || settingsQueue.busy) return;
      document_ = next;
      fetchedAt = Date.now();
      render();
    } catch (error) {
      if (!document_) target.replaceChildren(node('p', 'badtext', error.message));
    } finally { inFlight = false; }
  }

  function start() {
    if (!sheet) {
      sheet = node('style');
      sheet.textContent = CSS;
      document.head.append(sheet);
    }
    refresh(true);
  }
  // sync.js polls while Settings is open; follow it.
  window.addEventListener('pharos:sync-status', () => refresh());
  window.addEventListener('pharos:route', () => refresh(true));

  window.pharosBackground = {refresh, render,
    // Pure helpers, for web/tests/unit/page/background.test.mjs.
    scheduleModel, itemModel, batteryModel, labelingControls, createSettingsQueue, stateText, dropCadence, pauseLength, ago, until, duration};
  if (host()) start(); else document.addEventListener('DOMContentLoaded', start, {once: true});
})();
