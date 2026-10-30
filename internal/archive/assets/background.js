// Settings → Background updates (docs/background-work.md §11): the three
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
#backgroundSchedule{display:grid;gap:18px;min-width:0}
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
.bg-cols{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px;margin-top:16px}
.bg-col{border:1px solid var(--line);border-radius:8px;padding:12px 14px;min-width:0;background:var(--bg)}
.bg-col-head{display:flex;flex-wrap:wrap;align-items:baseline;gap:4px 8px}
.bg-col-head h4{margin:0;font-size:var(--fs-md)}
.bg-col-head .bg-state{margin-left:auto}
.bg-when{font-size:var(--fs-sm);color:var(--muted);margin:4px 0 8px}
.bg-when select{font:inherit;font-size:var(--fs-sm);border:1px solid var(--line);border-radius:5px;padding:1px 4px;background:var(--panel);color:var(--ink)}
.bg-state{font-size:var(--fs-sm);color:var(--muted);overflow-wrap:anywhere}
.bg-item{border-top:1px solid var(--line);padding:8px 0}
.bg-item:last-child{padding-bottom:0}
.bg-item-head{display:flex;align-items:baseline;gap:8px;width:100%;border:0;background:none;padding:0;color:var(--ink);font:inherit;text-align:left;cursor:pointer}
.bg-item-head:hover .bg-name{color:var(--accent)}
.bg-name{font-weight:650;min-width:0;overflow-wrap:anywhere}
.bg-cost{margin-left:auto;flex:none;font-size:var(--fs-xs);padding:0 7px;border:1px solid var(--line);border-radius:10px;color:var(--muted)}
.bg-cost.light{color:var(--accent)}
.bg-cost.heavy{color:var(--warn);border-color:var(--warn)}
.bg-sub,.bg-item .bg-state{display:block;font-size:var(--fs-sm);color:var(--muted);padding-left:15px}
.bg-note{display:block;font-size:var(--fs-sm);color:var(--accent);padding-left:15px}
.bg-detail{margin:8px 0 0 15px;padding:10px 12px;border-radius:6px;background:var(--panel);font-size:var(--fs-sm)}
.bg-detail p{margin:0 0 6px}
.bg-detail .bg-row{display:flex;flex-wrap:wrap;align-items:center;gap:8px;margin-top:8px}
.bg-detail select{font:inherit;font-size:var(--fs-sm);border:1px solid var(--line);border-radius:5px;padding:4px 6px;background:var(--bg);color:var(--ink)}
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
.bg-stack{display:flex;height:12px;border-radius:6px;overflow:hidden;background:var(--line);margin:12px 0 6px}
.bg-stack i{display:block;height:100%}
.bg-legend{display:flex;flex-wrap:wrap;gap:4px 16px;font-size:var(--fs-sm);color:var(--muted)}
.bg-legend span::before{content:"";display:inline-block;width:9px;height:9px;border-radius:2px;margin-right:6px;background:var(--swatch)}
.bg-check{display:flex;gap:8px;align-items:flex-start;margin-top:10px;font-size:var(--fs-sm)}
.bg-battery{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:10px;margin-top:12px}
.bg-battery-row{display:grid;gap:4px;font-size:var(--fs-sm);font-weight:600;min-width:0}
.bg-battery-row select{font:inherit;font-weight:400;border:1px solid var(--line);border-radius:5px;padding:5px 6px;background:var(--bg);color:var(--ink);min-width:0}
.bg-check input{margin-top:3px;accent-color:var(--accent)}
@media(max-width:900px){.bg-cols,.bg-battery{grid-template-columns:minmax(0,1fr)}}`;

  const CADENCE_ICONS = {frequent: '↻', hourly: '◷', daily: '☾'};
  const DOTS = {current: 'current', running: 'busy', queued: 'busy', failed: 'bad', missed: 'behind', never: 'behind', paused: 'paused', manual: 'paused', unavailable: 'paused', power: 'paused'};
  const SWATCHES = {frequent: '#8cc6a0', hourly: 'var(--accent)', daily: '#9db1d6'};

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
  function scheduleModel(document, now = Date.now()) {
    const view = document?.view;
    if (!view) return null;
    const byId = Object.fromEntries((view.items || []).map(item => [item.id, item]));
    const columns = (view.cadences || []).map(row => ({
      id: row.id, label: row.label, icon: CADENCE_ICONS[row.id] || '', description: document.descriptions?.[row.id] || row.description || '',
      dot: DOTS[row.state] ?? '', text: stateText(row, now),
      items: (row.items || []).map(id => byId[id]).filter(Boolean).map(item => itemModel(item, now)),
    }));
    return {mode: document.settings?.mode || view.mode, settings: document.settings || {}, columns, everything: view.everything?.text || '',
      rule: document.rule || null, cost: costModel(document.cost), headline: view.headline || null, battery: batteryModel(document)};
  }

  // On battery: each cadence's choice, and the low-battery rule. Null on a
  // Mac with no battery, where it never applies.
  function batteryModel(document) {
    const power = document?.power, settings = document?.settings || {};
    if (!power?.battery || !settings.battery) return null;
    const words = {same: 'Same as plugged in', slower: `Every ${settings.battery_slower_minutes || 15} min`, wait: 'Wait until plugged in'};
    const labels = {frequent: 'Frequently', hourly: 'Hourly', daily: 'Daily'};
    const charge = Number.isFinite(power.percent) && power.percent >= 0 ? ` (${power.percent}%)` : '';
    return {
      status: `${power.on_battery ? 'This Mac is on battery' : 'This Mac is plugged in'}${charge}${power.low_power_mode ? ', in Low Power Mode' : ''}.`,
      onBattery: Boolean(power.on_battery),
      rows: ['frequent', 'hourly', 'daily'].map(cadence => ({cadence, label: labels[cadence], value: settings.battery[cadence] || 'same',
        choices: (settings.battery_choices?.[cadence] || ['same', 'wait']).map(value => ({value, label: words[value] || value}))})),
      lowWaits: settings.battery.low_battery_waits !== false,
      lowText: `Below ${settings.low_battery_percent || 20}% or in Low Power Mode, everything waits until plugged in`,
    };
  }

  function itemModel(item, now = Date.now()) {
    return {id: item.id, label: item.label, sub: item.sub || '', feeds: item.feeds || '', why: item.why || '', note: item.note || '',
      dot: DOTS[item.state] ?? '', state: item.state, text: stateText(item, now), error: item.error || '',
      cost: item.cost ? {class: item.cost.class || '', text: item.cost.text || ''} : null,
      choices: item.choices || [], override: item.override || '', cadenceLabel: item.cadence_label || '', fixed: Boolean(item.fixed),
      sources: Array.isArray(item.sources) ? item.sources.map(source => ({name: source.name, selected: Boolean(source.selected)})) : null};
  }

  // The day's cost: each cadence's share of the total, measured or
  // projected.
  function costModel(cost) {
    if (!cost || !Array.isArray(cost.cadences)) return null;
    const total = cost.cadences.reduce((sum, entry) => sum + (Number(entry.seconds) || 0), 0);
    return {total: duration(total), peakBytes: Number(cost.peak_bytes) || 0,
      segments: cost.cadences.map(entry => ({id: entry.id, label: entry.label, measured: Boolean(entry.measured), text: entry.text || '',
        duration: duration(entry.seconds), share: total > 0 ? (Number(entry.seconds) || 0) / total : 0}))};
  }

  // ---- rendering
  let document_ = null, expanded = null, inFlight = false, fetchedAt = 0, sheet = null;
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
  async function save(body) {
    document_ = await call('/api/background/settings', body);
    render();
  }
  const sizeText = bytes => bytes >= 1e9 ? `${(bytes / 1e9).toFixed(1)} GB` : `${Math.round(bytes / 1e6)} MB`;

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

  function renderColumns(model) {
    const columns = node('div', 'bg-cols');
    for (const column of model.columns) {
      const section = node('section', 'bg-col');
      section.setAttribute('aria-label', column.label);
      const head = node('div', 'bg-col-head');
      const title = node('h4', '', `${column.icon} ${column.label}`);
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
      section.append(head, when);
      for (const item of column.items) section.append(renderItem(item));
      columns.append(section);
    }
    return columns;
  }

  function renderItem(item) {
    const entry = node('div', 'bg-item');
    entry.dataset.item = item.id;
    const open = expanded === item.id;
    const head = node('button', 'bg-item-head');
    head.type = 'button';
    head.setAttribute('aria-expanded', String(open));
    head.append(node('span', `bg-dot ${item.dot}`), node('span', 'bg-name', item.label));
    if (item.cost?.class) head.append(node('span', `bg-cost ${item.cost.class}`, {light: 'Light', moderate: 'Moderate', heavy: 'Heavy'}[item.cost.class]));
    head.onclick = () => { expanded = open ? null : item.id; render(); };
    entry.append(head, node('span', 'bg-sub', item.sub), node('span', 'bg-state', item.text));
    if (item.note) entry.append(node('span', 'bg-note', item.note));
    if (!open) return entry;
    const detail = node('div', 'bg-detail');
    const line = (label, value) => { const p = node('p'); p.append(node('strong', '', `${label}: `), value); return p; };
    detail.append(line('Feeds', item.feeds), line('Usually takes', item.cost?.text || 'not measured yet'));
    if (item.why) detail.append(node('p', 'muted', item.why));
    if (item.error) detail.append(node('p', 'bg-error', item.error));
    if (item.sources?.length) detail.append(sourcePicker(item.sources));
    const row = node('div', 'bg-row');
    if (item.choices.length) {
      const label = node('label', '', 'Run ');
      label.append(select(item.choices.map(choice => [choice.value, choice.label]), item.override, value => save({overrides: {[item.id]: value}}), `When ${item.label} runs`));
      row.append(label);
    }
    row.append(button('Run now', async () => {
      await call('/api/background/run', {item: item.id});
      toast(`${item.label} runs next.`);
      refresh(true);
    }));
    detail.append(row);
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
    if (!target || !document_) return;
    // A select being used keeps its value until it is changed.
    if (target.contains(document.activeElement) && document.activeElement.matches('select')) return;
    const focused = document.activeElement?.closest?.('[data-item]')?.dataset.item;
    const model = scheduleModel(document_);
    if (!model) return;
    const content = node('div');
    const card = node('section', 'bg-card');
    const mode = node('div', 'bg-mode');
    const intro = node('div');
    intro.append(node('h3', '', 'Keep my library current'),
      node('p', 'muted', {on: 'Everything below runs automatically. Pharos measures every run and moves work between schedules as it gets faster or slower.',
        paused: 'Paused; everything resumes at the end of the pause.', manual: 'Nothing runs until you choose Update everything now.'}[model.mode] || ''));
    const seg = node('div', 'bg-seg');
    seg.setAttribute('role', 'group');
    seg.setAttribute('aria-label', 'Background updates');
    for (const [value, label, state] of [['on', 'On', 'on'], ['pause', 'Pause for 1 hour', 'paused'], ['manual', 'Only when I ask', 'manual']]) {
      const choice = button(label, () => save({mode: value}), '');
      choice.setAttribute('aria-pressed', String(model.mode === state));
      seg.append(choice);
    }
    const everything = button('Update everything now', async () => {
      await call('/api/sync/check', {});
      toast('Updating everything now: new conversations first, then every analysis.');
    });
    mode.append(intro, seg, everything);
    if (model.everything) mode.append(node('p', 'bg-everything', model.everything));
    card.append(mode, renderColumns(model));
    if (model.rule) {
      const rule = node('div', 'bg-rule');
      rule.append(node('strong', '', 'How work is placed:'), ...[`usually under ${duration(model.rule.frequent_seconds)} → Frequently`,
        `under ${duration(model.rule.hourly_seconds)} → Hourly`, 'longer → Daily', 'Choose an item for its cost, or to change or run it.'].map(text => node('span', '', text)));
      card.append(rule);
    }
    content.append(card);
    if (model.cost) {
      const cost = node('section', 'bg-card');
      cost.append(node('h3', '', 'What it costs'),
        node('p', 'muted', `Background work on this Mac in the last 24 hours: about ${model.cost.total} of work${model.cost.peakBytes ? `, with memory peaking at ${sizeText(model.cost.peakBytes)}` : ''}. Hourly and Daily are projected from their measured runs until they have run here.`));
      const stack = node('div', 'bg-stack');
      stack.setAttribute('role', 'img');
      stack.setAttribute('aria-label', model.cost.segments.map(segment => `${segment.label} ${segment.duration}`).join(', '));
      const legend = node('div', 'bg-legend');
      for (const segment of model.cost.segments) {
        const bar = node('i');
        bar.style.width = `${(segment.share * 100).toFixed(1)}%`;
        bar.style.background = SWATCHES[segment.id];
        stack.append(bar);
        const key = node('span', '', `${segment.label} ${segment.duration}${segment.text ? ` · ${segment.text}` : ''}`);
        key.style.setProperty('--swatch', SWATCHES[segment.id]);
        legend.append(key);
      }
      cost.append(stack, legend);
      content.append(cost);
    }
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
    const considerate = node('section', 'bg-card');
    const hold = node('label', 'bg-check');
    const box = node('input');
    box.type = 'checkbox';
    box.checked = model.settings.hold_when_busy !== false;
    box.onchange = () => save({hold_when_busy: box.checked}).catch(error => toast(error.message));
    hold.append(box, 'Hold Hourly and Daily work while this Mac is busy (other apps using most of the CPU)');
    considerate.append(node('h3', '', 'Be considerate'), hold,
      node('p', 'muted', 'Background work stops as soon as you eject the library or quit Pharos, and carries on where it left off.'));
    content.append(considerate);
    target.replaceChildren(...content.childNodes);
    if (focused) target.querySelector(`[data-item="${window.CSS.escape(focused)}"] .bg-item-head`)?.focus({preventScroll: true});
  }

  async function refresh(force = false) {
    const target = host();
    if (!target || inFlight || (!force && (!target.offsetParent || Date.now() - fetchedAt < 4000))) return;
    inFlight = true;
    try {
      document_ = await call('/api/background');
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
    scheduleModel, itemModel, costModel, batteryModel, stateText, ago, until, duration};
  if (host()) start(); else document.addEventListener('DOMContentLoaded', start, {once: true});
})();
