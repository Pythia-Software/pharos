// The header card and its settings editor share this metric registry.
const QUICK_STATS_KEY = 'pharos-quick-stats-v1';
const QUICK_STATS_DEFAULT_WINDOWS = ['1h', '6h', 'today', '24h', '7d', '30d', 'all'];
const QUICK_STATS_DEFAULT_METRICS = ['human_words', 'tokens', 'co2e_g', 'cost_usd'];
const QUICK_STATS_WINDOWS = [['1h', '1h'], ['6h', '6h'], ['today', 'Today'], ['24h', '24h'], ['7d', '7d'], ['30d', '30d'], ['all', 'All time'], ['5m', '5m'], ['15m', '15m'], ['yesterday', 'Yesterday'], ['week', 'This week'], ['month', 'This month']];
function quickStatsMetrics(concurrency = false) {
  const rows = [
    ['Human words', 'human_words', 'Words you typed yourself: not pasted, quoted, templated, or sent by a tool', '', 'writing'],
    ['Tokens', 'tokens', 'All tokens, cache reads and writes included', '', 'tokens'],
    ['CO₂e', 'co2e_g', 'Estimated inference emissions using the settings in Analyze → Carbon Impact', '', 'carbon'],
    ['API cost', 'cost_usd', 'API-equivalent cost at list prices', '$', 'tokens'],
  ];
  rows.push(
    ['Conversations active', 'active_conversations', 'Distinct top-level conversations with recorded activity in this window. Continuations and delegated work belong to their root conversation.', '', 'conversations'],
    ['Messages', 'message_count', 'User and assistant messages, including delegated work. Tool events and system instructions are excluded.', '', 'conversations'],
    ['Your messages', 'user_messages', 'User-role messages in top-level conversations, including pasted text. Delegated agents’ prompts are excluded.', '', 'writing'],
    ['Agent replies', 'assistant_messages', 'Assistant messages, including delegated agents. Tool events and system instructions are excluded.', '', 'conversations'],
    ['Tool calls', 'tool_calls', 'Individual recorded tool invocations, including delegated work. Mirrored transcripts count once.', '', 'tools'],
    ['Estimated agent time', 'agent_time_ms', 'Estimated top-level session time, using the saved turn gap. Parallel sessions count separately; child work belongs to its root. Timing coverage is required for the full window.', '', 'parallelism', 'duration'],
    ['Estimated active time', 'active_time_ms', 'Estimated wall-clock time with at least one top-level session active, using the saved turn gap. Overlapping sessions count once.', '', 'parallelism', 'duration'],
    ['Delegated runs', 'delegated_runs', 'Distinct delegated executions with recorded activity or a start in this window. Linked handles, child transcripts and continuations count once.', '', 'conversations'],
    ['Failed tool calls', 'failed_tool_calls', 'Tool calls marked as errors. A non-zero shell exit can be expected, such as a search with no matches.', '', 'tools'],
    ['Tool failure rate', 'tool_failure_rate', 'Failed tool calls divided by all tool calls in the window. No calls means no rate.', '', 'tools', 'percent'],
    ['Repositories active', 'active_repositories', 'Distinct known repositories with conversation or tool activity. Unknown repositories are excluded.', '', 'conversations'],
    ['Recorded tool time', 'tool_time_ms', 'Sum of recorded durations for calls started in the window. Durations can overlap and include approval waits. Untimed calls contribute no duration.', '', 'tools', 'duration'],
  );
  if (concurrency) rows.push(['Parallelism', 'parallelism', 'Average concurrent workspaces or sessions over the full time window, including idle time, using the saved counting choice, turn gap and repository.', '', 'parallelism']);
  return rows;
}
function normalizeQuickStatsLayout(saved, metrics = quickStatsMetrics()) {
  const normalize = (value, allowed, defaults) => {
    if (!Array.isArray(value)) return defaults;
    const valid = [...new Set(value.filter(id => allowed.includes(id)))];
    return valid.length ? valid : defaults;
  };
  return {
    metrics: normalize(saved?.metrics, metrics.map(row => row[1]), metrics.map(row => row[1]).filter(id => QUICK_STATS_DEFAULT_METRICS.includes(id) || id === 'parallelism')),
    windows: normalize(saved?.windows, QUICK_STATS_WINDOWS.map(row => row[0]), QUICK_STATS_DEFAULT_WINDOWS.slice()),
  };
}
// The detail request is opt-in; ordinary token/cost summaries stay small.
const QUICK_STATS_DETAIL_METRICS = quickStatsMetrics().map(row => row[1]).filter(id => !QUICK_STATS_DEFAULT_METRICS.includes(id) && !['agent_time_ms', 'active_time_ms'].includes(id));
function formatQuickStatsValue(value, format) {
  if (value == null || !Number.isFinite(Number(value))) return '—';
  const number = Number(value);
  if (format === 'percent') return `${Number(number.toFixed(1))}%`;
  if (format === 'duration') {
    if (number > 0 && number < 1000) return `${Math.round(number)}ms`;
    const seconds = Math.round(Math.max(0, number) / 1000);
    if (seconds < 60) return `${seconds}s`;
    const minutes = Math.round(seconds / 60);
    return minutes < 60 ? `${minutes}m` : `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
  }
  return String(number);
}
// A destination is a boundary in the original list, or null for the toolbox.
function editQuickStatsLayout(saved, group, id, destination, metrics = quickStatsMetrics()) {
  const layout = normalizeQuickStatsLayout(saved, metrics);
  const allowed = group === 'metrics' ? metrics.map(row => row[1]) : group === 'windows' ? QUICK_STATS_WINDOWS.map(row => row[0]) : [];
  if (!allowed.includes(id)) return layout;
  const order = layout[group], before = order.indexOf(id);
  if (destination === null && order.length === 1) return layout;
  const next = order.filter(other => other !== id);
  if (destination !== null) {
    const boundary = Math.max(0, Math.min(order.length, destination));
    next.splice(boundary - (before >= 0 && before < boundary ? 1 : 0), 0, id);
  }
  return {...layout, [group]: next};
}

// buildTable is also used by the live header card: preview pieces keep its
// typography, values, links and formatting instead of approximating them.
function initQuickStatsEditor(metrics, initial, changed, buildTable) {
  const root = document.getElementById('quickStatsEditor');
  if (!root) return;
  const element = (tag, className = '', text = '') => {
    const node = document.createElement(tag); node.className = className; node.textContent = text; return node;
  };
  const glyph = name => {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('class', 'app-icon'); svg.setAttribute('viewBox', '0 0 24 24'); svg.setAttribute('aria-hidden', 'true');
    const use = document.createElementNS(svg.namespaceURI, 'use'); use.setAttribute('href', `#ph-icon-${name}`); svg.append(use); return svg;
  };
  let layout = initial, drag = null, dragImage = null, frames = [];
  const status = document.getElementById('quickStatsEditorStatus'), menu = element('div', 'header-tool-menu');
  menu.id = 'quickStatsToolMenu'; menu.setAttribute('popover', 'auto'); menu.setAttribute('aria-label', 'Quick Stats item actions');
  const previewSlot = element('div'), previewNote = element('p', 'muted quick-stats-preview-note', 'Live library values. Arrange rows and columns just as they will appear in your card.');
  const heading = element('h3', 'quick-stats-preview-heading', 'Your Quick Stats'); heading.id = 'quickStatsPreviewTitle';
  previewSlot.setAttribute('role', 'region'); previewSlot.setAttribute('aria-labelledby', heading.id);
  const toolboxes = element('div', 'quick-stats-toolboxes');
  const groups = [
    {id: 'metrics', title: 'Rows', description: 'Drag a metric row into your card, or click its name to add it.', empty: 'All rows are in your card. Drop a row here to hide it.', entries: metrics.map(([label, id, description]) => ({id, label, description}))},
    {id: 'windows', title: 'Columns', description: 'Drag a time-period column into your card, or click its heading to add it.', empty: 'All columns are in your card. Drop a column here to hide it.', entries: QUICK_STATS_WINDOWS.map(([id, label]) => ({id, label}))},
  ].map(group => {
    const section = element('section', 'quick-stats-toolbox-section'), title = element('h4', '', group.title);
    title.id = `quickStats-${group.id}-title`; section.setAttribute('aria-labelledby', title.id);
    const toolbox = element('div', 'quick-stats-toolbox');
    toolbox.setAttribute('role', 'region'); toolbox.setAttribute('aria-label', `Available ${group.title.toLowerCase()}`);
    section.append(title, element('p', 'muted', group.description), toolbox); toolboxes.append(section);
    return {...group, toolbox};
  });
  root.append(menu, heading, previewSlot, previewNote, element('h3', 'quick-stats-preview-heading', 'Toolbox'), toolboxes);
  const definition = (group, id) => groups.find(item => item.id === group).entries.find(item => item.id === id);
  const closeMenu = () => { if (menu.matches(':popover-open')) menu.hidePopover(); };
  function clearDrag() {
    root.querySelectorAll('.drop-target,.drop-before,.drop-after,.dragging,.column-before,.column-after,.column-dragging').forEach(node => node.classList.remove('drop-target', 'drop-before', 'drop-after', 'dragging', 'column-before', 'column-after', 'column-dragging'));
  }
  function finishDrag() { drag = null; dragImage?.remove(); dragImage = null; clearDrag(); }
  function change(group, id, destination = layout[group].length) {
    const next = editQuickStatsLayout(layout, group, id, destination, metrics);
    if (next[group].join() === layout[group].join()) {
      if (destination === null && layout[group].includes(id)) status.textContent = `Keep at least one ${group === 'metrics' ? 'row' : 'column'} in Quick Stats.`;
      return;
    }
    closeMenu(); layout = next; pharosPrefs.set(QUICK_STATS_KEY, layout); changed(layout); render();
    const entry = definition(group, id);
    status.textContent = destination === null ? `${entry.label} moved to the toolbox.` : `${entry.label} is in position ${layout[group].indexOf(id) + 1}.`;
    const focused = root.querySelector(`[data-group="${group}"][data-item="${id}"] button`);
    focused?.focus({preventScroll: true}); focused?.scrollIntoView({block: 'nearest', inline: 'nearest'});
  }
  function actions(group, entry, handle, index) {
    closeMenu();
    const button = (label, action, disabled = false) => {
      const node = element('button', '', label); node.type = 'button'; node.disabled = disabled;
      node.setAttribute('aria-label', `${label} ${entry.label}`); node.onclick = action; return node;
    };
    const [before, after] = group === 'metrics' ? ['up', 'down'] : ['left', 'right'];
    menu.replaceChildren(
      button(`Move ${before}`, () => change(group, entry.id, index - 1), index === 0),
      button(`Move ${after}`, () => change(group, entry.id, index + 2), index === layout[group].length - 1),
      button('Remove', () => change(group, entry.id, null), layout[group].length === 1),
    );
    menu.querySelector('button:not(:disabled)')?.setAttribute('autofocus', '');
    const box = handle.getBoundingClientRect();
    menu.style.left = `${Math.max(8, Math.min(innerWidth - 250, box.left))}px`;
    menu.style.top = `${Math.max(8, Math.min(innerHeight - 55, box.bottom + 6))}px`;
  }
  function control(group, entry, active, index) {
    const button = element('button', active ? `quick-stats-handle quick-stats-${group === 'metrics' ? 'row' : 'column'}-handle` : 'quick-stats-add', active ? '' : entry.label);
    button.type = 'button'; button.setAttribute('aria-label', `${active ? 'Arrange' : 'Add'} ${entry.label}`);
    if (!active) { button.onclick = () => change(group, entry.id); return button; }
    button.append(glyph('grip')); button.setAttribute('popovertarget', menu.id);
    const [before, after] = group === 'metrics' ? ['ArrowUp', 'ArrowDown'] : ['ArrowLeft', 'ArrowRight'];
    button.title = `Drag to move. Click for actions. Alt+${group === 'metrics' ? '↑/↓' : '←/→'} to reorder; Delete to remove.`;
    button.onclick = () => actions(group, entry, button, index);
    button.onkeydown = event => {
      if (event.altKey && [before, after].includes(event.key)) {
        event.preventDefault(); change(group, entry.id, index + (event.key === before ? -1 : 2));
      } else if (['Delete', 'Backspace'].includes(event.key)) { event.preventDefault(); change(group, entry.id, null); }
    };
    return button;
  }
  function columnCells(table, id) { return [...table.querySelectorAll('[data-quick-stats-window]')].filter(cell => cell.dataset.quickStatsWindow === id); }
  function bindPiece(target, table, group, entry, active, index) {
    target.dataset.group = group; target.dataset.item = entry.id; target.draggable = true;
    target.ondragstart = event => {
      event.stopPropagation(); closeMenu(); drag = {group, id: entry.id}; clearDrag();
      event.dataTransfer.effectAllowed = 'move'; event.dataTransfer.setData('text/plain', entry.label);
      // A dragged column is a column, with its heading and actual values. A
      // dragged row is its metric label followed by its actual values.
      dragImage?.remove(); dragImage = element('div', 'usage-summary quick-stats-preview quick-stats-drag-image');
      const ghost = buildTable({...layout, [group]: [entry.id]});
      if (group === 'windows') for (const row of ghost.rows) row.firstElementChild.remove();
      else ghost.tHead.remove();
      dragImage.append(ghost); document.body.append(dragImage);
      event.dataTransfer.setDragImage(dragImage, Math.min(dragImage.offsetWidth / 2, 100), 12);
      if (group === 'windows') columnCells(table, entry.id).forEach(cell => cell.classList.add('column-dragging'));
      else target.classList.add('dragging');
    };
    target.ondragend = finishDrag;
    // Column drops work over its values as well as its heading. Row drops
    // use the full row, preserving the same two axes as the real table.
    const cells = group === 'windows' ? columnCells(table, entry.id) : [target];
    for (const cell of cells) {
      const after = event => {
        const box = cell.getBoundingClientRect();
        return group === 'windows' ? event.clientX > box.left + box.width / 2 : event.clientY > box.top + box.height / 2;
      };
      cell.addEventListener('dragover', event => {
        if (!active || drag?.group !== group) return;
        event.preventDefault(); event.stopPropagation(); clearDrag();
        cells.forEach(node => node.classList.add(group === 'windows' ? after(event) ? 'column-after' : 'column-before' : after(event) ? 'drop-after' : 'drop-before'));
      });
      cell.addEventListener('dragleave', event => { if (!cell.contains(event.relatedTarget)) clearDrag(); });
      cell.addEventListener('drop', event => {
        if (!active || drag?.group !== group) return;
        event.preventDefault(); event.stopPropagation(); change(group, drag.id, index + (after(event) ? 1 : 0)); finishDrag();
      });
    }
  }
  function dropZone(target, group = null) {
    target.ondragover = event => {
      if (!drag || group && drag.group !== group) return;
      event.preventDefault(); event.dataTransfer.dropEffect = 'move'; target.classList.add('drop-target');
    };
    target.ondragleave = event => { if (!target.contains(event.relatedTarget)) target.classList.remove('drop-target'); };
    target.ondrop = event => {
      if (!drag || group && drag.group !== group) return;
      event.preventDefault(); change(drag.group, drag.id, group ? null : layout[drag.group].length); finishDrag();
    };
  }
  function card(selection, label) {
    const frame = element('div', 'usage-summary quick-stats-preview'), scroll = element('div', 'usage-summary-scroll');
    const table = buildTable(selection); table.setAttribute('aria-label', label);
    scroll.append(table); frame.append(scroll); frames.push({table, selection});
    return {frame, table};
  }
  function render() {
    clearDrag(); frames = [];
    const {frame, table} = card(layout, 'Your Quick Stats preview'); frame.id = 'quickStatsPreview';
    for (const [index, id] of layout.metrics.entries()) {
      const row = table.tBodies[0].rows[index], entry = definition('metrics', id);
      row.cells[0].prepend(control('metrics', entry, true, index)); bindPiece(row, table, 'metrics', entry, true, index);
    }
    for (const [index, id] of layout.windows.entries()) {
      const cell = table.tHead.rows[0].cells[index + 1], entry = definition('windows', id);
      cell.prepend(control('windows', entry, true, index)); bindPiece(cell, table, 'windows', entry, true, index);
    }
    dropZone(frame); previewSlot.replaceChildren(frame);
    for (const group of groups) {
      const available = group.entries.filter(entry => !layout[group.id].includes(entry.id));
      group.toolbox.replaceChildren(); dropZone(group.toolbox, group.id);
      if (!available.length) { group.toolbox.append(element('p', 'muted', group.empty)); continue; }
      const selection = {...layout, [group.id]: available.map(entry => entry.id)};
      const {frame, table} = card(selection, `Available Quick Stats ${group.title.toLowerCase()}`);
      // Toolbox rows have no time headings or update controls: each is an
      // actual row ready to be inserted. Toolbox columns keep their heading.
      if (group.id === 'metrics') table.tHead.remove();
      else table.tHead.rows[0].cells[0].replaceChildren();
      for (const [index, entry] of available.entries()) {
        const target = group.id === 'metrics' ? table.tBodies[0].rows[index] : table.tHead.rows[0].cells[index + 1];
        const face = group.id === 'metrics' ? target.cells[0] : target;
        face.replaceChildren(control(group.id, entry, false, index)); bindPiece(target, table, group.id, entry, false, index);
      }
      group.toolbox.append(frame);
    }
  }
  // Refresh only value cells. Handles, active drags and keyboard focus stay
  // in place while the API or carbon/concurrency assumptions update.
  function refresh(error = '') {
    previewNote.textContent = error || ('Live library values. Arrange rows and columns just as they will appear in your card.' + (layout.windows.some(key => ['5m', '15m'].includes(key)) ? ' Token, cost and CO₂e totals are estimated from hourly usage.' : ''));
    for (const {table, selection} of frames) {
      const fresh = buildTable(selection);
      for (const cell of table.querySelectorAll('td[data-quick-stats-metric]')) {
        const source = [...fresh.querySelectorAll('td')].find(other => other.dataset.quickStatsMetric === cell.dataset.quickStatsMetric && other.dataset.quickStatsWindow === cell.dataset.quickStatsWindow);
        cell.replaceChildren(...source.childNodes); cell.title = source.title;
      }
    }
  }
  document.getElementById('quickStatsReset').onclick = () => {
    closeMenu(); layout = normalizeQuickStatsLayout(null, metrics); pharosPrefs.set(QUICK_STATS_KEY, layout); changed(layout); render(); status.textContent = 'Default Quick Stats restored.';
  };
  addEventListener('resize', closeMenu);
  addEventListener('pharos:route', () => { closeMenu(); finishDrag(); });
  document.querySelector('main').addEventListener('scroll', closeMenu);
  document.getElementById('quickStatsExpand').addEventListener('click', closeMenu);
  render();
  return {refresh};
}

function quickStatsSettingsURL() {
  return '/settings/layout?' + new URLSearchParams({disclosures: JSON.stringify({quickStatsSettings: true}), reveal: 'section:quickStatsSettings'});
}
