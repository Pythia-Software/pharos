// Shared preference contract with menu_bar.go and macos/MenuBar.swift.
const MENU_BAR_KEY = 'pharos-menu-bar';
const MENU_BAR_TOOLS = [
  {id: 'open', label: 'Open Pharos', required: true, group: 'open'},
  {id: 'status', label: 'Pharos status', required: true, group: 'status', description: 'Current work and errors always stay visible.'},
  {id: 'indexed', label: 'Last successful index', on: true, group: 'status', description: 'When this Mac last indexed successfully.'},
  {id: 'index', label: 'Index changes now', group: 'status', description: 'Start indexing from the menu.'},
  {id: 'words', label: 'Words typed', on: true, group: 'usage', description: 'Estimated human words · last 24 hours.'},
  {id: 'tokens', label: 'Tokens used', on: true, group: 'usage', description: 'Combined token usage · last 24 hours.'},
  {id: 'cost', label: 'Estimated API cost', group: 'usage', description: 'API-equivalent cost at list prices · last 24 hours.'},
  {id: 'carbon', label: 'Estimated CO₂e', on: true, group: 'usage', description: 'Carbon impact using your settings · last 24 hours.'},
  {id: 'backup', label: 'Last backup', group: 'library', description: 'When the library was last fully backed up.'},
  {id: 'space', label: 'Library size', group: 'library', description: 'Archive size and free space on its drive.'},
  {id: 'search', label: 'Search conversations…', group: 'navigation', description: 'Find a conversation or search the archive.'},
  {id: 'analyze', label: 'Analyze usage…', group: 'navigation', description: 'Open usage, cost, writing, and carbon analysis.'},
  {id: 'optimize', label: 'Optimize…', group: 'navigation', description: 'Review findings and opportunities to improve agent work.'},
  {id: 'capture', label: 'Capture & index…', group: 'manage', description: 'Open capture and indexing controls, schedules, and recent runs.'},
  {id: 'health', label: 'Library health & backups…', group: 'manage', description: 'Check storage and recent activity, or choose a backup destination.'},
  {id: 'sources', label: 'Manage sources…', group: 'manage', description: 'Choose which conversation files Pharos reads on this Mac.'},
  {id: 'finder', label: 'Show library in Finder', group: 'manage', description: 'Reveal the connected library folder in Finder.'},
  {id: 'settings', label: 'Settings…', required: true, group: 'settings'},
  {id: 'eject', label: 'Eject library…', group: 'quit', description: 'Safely eject the library. Appears when its drive is ejectable.'},
  {id: 'quit', label: 'Quit Pharos', required: true, group: 'quit'},
];
const MENU_BAR_SEPARATOR = {id: 'separator', label: 'Separator', separator: true, group: 'separator',
  description: 'Drag a divider into the menu, or click to add one. Add as many as you need.'};
function isMenuBarSeparator(id) { return typeof id === 'string' && /^separator:[1-9][0-9]*$/.test(id); }
function menuBarTool(id) {
  return id === 'separator' || isMenuBarSeparator(id) ? {...MENU_BAR_SEPARATOR, id} : MENU_BAR_TOOLS.find(tool => tool.id === id);
}
function menuBarActivityDetail(activity, now = Date.now()) {
  const parts = [typeof activity.label === 'string' ? activity.label : 'Working'];
  if (Number.isFinite(activity.progress)) parts.push(`stage ${Math.trunc(activity.progress * 100)}%`);
  const completion = Date.parse(activity.estimated_completion_at);
  if (completion > now) parts.push(`about ${Math.max(1, Math.ceil((completion - now) / 60000))} min for this stage`);
  return parts.join(' · ');
}
function normalizeMenuBarSettings(saved = {}) {
  let order = [];
  const ids = Array.isArray(saved?.order) ? saved.order : MENU_BAR_TOOLS
    .filter(tool => tool.required || (typeof saved?.[tool.id] === 'boolean' ? saved[tool.id] : tool.on)).map(tool => tool.id);
  for (const id of ids) if ((isMenuBarSeparator(id) || MENU_BAR_TOOLS.some(tool => tool.id === id)) && !order.includes(id)) order.push(id);
  for (const tool of MENU_BAR_TOOLS) if (tool.required && !order.includes(tool.id)) order.push(tool.id);
  // Turn the old automatic dividers into editable items once. Version 2 can
  // deliberately contain no separators, so removing the last one stays saved.
  if (saved?.layoutVersion !== 2 && !order.some(isMenuBarSeparator)) {
    let previous, separator = 0;
    order = order.flatMap(id => {
      const group = menuBarTool(id).group, before = previous && previous !== group ? [`separator:${++separator}`] : [];
      previous = group;
      return [...before, id];
    });
  }
  return {enabled: typeof saved?.enabled === 'boolean' ? saved.enabled : true, layoutVersion: 2,
    ...Object.fromEntries(MENU_BAR_TOOLS.map(tool => [tool.id, order.includes(tool.id)])), order};
}
// Destination is an insertion boundary in the original list; null removes.
// The toolbox's separator is a factory; every inserted divider has its own ID.
function editMenuBarSettings(saved, id, destination) {
  const settings = normalizeMenuBarSettings(saved);
  if (id === 'separator') {
    if (destination === null) return settings;
    let number = 1;
    while (settings.order.includes(`separator:${number}`)) number++;
    id = `separator:${number}`;
  }
  const tool = menuBarTool(id);
  if (!tool || (destination === null && tool.required)) return settings;
  const before = settings.order.indexOf(id), next = settings.order.filter(other => other !== id);
  if (destination !== null) {
    const boundary = Math.max(0, Math.min(settings.order.length, destination));
    next.splice(boundary - (before >= 0 && before < boundary ? 1 : 0), 0, id);
  }
  return normalizeMenuBarSettings({...settings, order: next});
}

(() => {
  'use strict';
  const get = id => document.getElementById(id), root = get('menuBarPreferences');
  if (!root) return;
  const active = get('menuBarActive'), toolbox = get('menuBarToolbox'), menu = get('menuBarToolMenu');
  let settings = normalizeMenuBarSettings(window.pharosPrefs.get(MENU_BAR_KEY, {})), dragId = null;
  let expanded = false;
  let snapshot = null, usage = null, loading = false, lastUsage = 0, previewError = false;
  const native = () => window.webkit?.messageHandlers?.pharosMenuBar;
  const notify = () => { try { native()?.postMessage({...settings}); } catch {} };
  const element = (tag, className = '', text = '') => {
    const node = document.createElement(tag); node.className = className; node.textContent = text; return node;
  };
  const glyph = name => {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('class', 'app-icon'); svg.setAttribute('viewBox', '0 0 24 24'); svg.setAttribute('aria-hidden', 'true');
    const use = document.createElementNS(svg.namespaceURI, 'use'); use.setAttribute('href', `#ph-icon-${name}`); svg.append(use); return svg;
  };
  const definition = menuBarTool;
  const status = () => previewError ? 'Unavailable' : snapshot?.error ? 'Needs attention' :
    !snapshot?.library || snapshot.library.activities?.length ? 'Working' : 'Idle';
  const ago = value => {
    const elapsed = Date.now() - Date.parse(value);
    if (!Number.isFinite(elapsed)) return null;
    const minutes = Math.max(0, Math.floor(elapsed / 60000));
    return minutes < 1 ? 'just now' : minutes < 60 ? `${minutes} min ago` : minutes < 1440 ? `${Math.floor(minutes / 60)} hr ago` : `${Math.floor(minutes / 1440)} d ago`;
  };
  const bytes = value => Number.isFinite(value) ? new Intl.NumberFormat(undefined, {style: 'unit', unit: 'gigabyte', maximumFractionDigits: 1}).format(value / 1e9) : '…';
  function faceContent(id) {
    const drive = snapshot?.library?.drive, storage = snapshot?.storage;
    const metric = (label, value) => [label, value ?? '…'];
    switch (id) {
      case 'status': return [`Pharos · ${status()}`];
      case 'indexed': return [snapshot ? (ago(snapshot.indexed_at) ? `Indexed ${ago(snapshot.indexed_at)}` : 'Not indexed yet') : 'Checking last index…'];
      case 'words': return metric('Words typed', usage?.human_words?.toLocaleString());
      case 'tokens': return metric('Tokens used', usage && new Intl.NumberFormat(undefined, {notation: 'compact', maximumFractionDigits: 1}).format(usage.tokens));
      case 'cost': return metric('Estimated API cost', usage && new Intl.NumberFormat(undefined, {style: 'currency', currency: 'USD'}).format(usage.cost_usd));
      case 'carbon': return metric('Estimated CO₂e', usage && `${new Intl.NumberFormat(undefined, {maximumSignificantDigits: 3}).format(usage.co2e_g >= 1000 ? usage.co2e_g / 1000 : usage.co2e_g)} ${usage.co2e_g >= 1000 ? 'kg' : 'g'}`);
      case 'backup': return metric('Last backup', snapshot && (ago(snapshot.backup_at) ?? 'Never'));
      case 'space': return metric('Library size', bytes(storage?.sizes?.library_bytes));
      case 'eject': return [`Eject ${drive?.name ?? 'library'}…`];
      default: return [definition(id).label];
    }
  }
  function syncFaces() {
    for (const row of active.querySelectorAll('[data-tool]')) {
      const id = row.dataset.tool, face = row.querySelector('.menu-bar-face');
      if (isMenuBarSeparator(id)) continue;
      const [label, value] = faceContent(id);
      face.replaceChildren(element('span', 'menu-bar-label', label));
      if (value != null) face.append(element('span', 'menu-bar-value', value));
      if (id === 'status' && snapshot?.error) face.append(element('small', 'menu-bar-detail', snapshot.error));
      if (id === 'status' && snapshot?.library?.activities?.length) face.append(element('small', 'menu-bar-detail', menuBarActivityDetail(snapshot.library.activities[0])));
      if (id === 'space') face.append(element('small', 'menu-bar-detail', `${bytes(snapshot?.storage?.free_bytes)} free on ${snapshot?.library?.drive?.name ?? 'library drive'}`));
      if (id === 'eject' && snapshot?.library?.drive?.ejectable !== true) face.append(element('small', 'menu-bar-detail', 'When the library drive is ejectable'));
    }
  }
  async function refreshPreview() {
    if (loading || !settings.enabled || !root.getClientRects().length) return;
    loading = true;
    try {
      const response = await fetch('/api/menu-bar/status');
      if (!response.ok) throw new Error('Preview unavailable');
      snapshot = await response.json(); previewError = false;
      if (settings.order.some(id => definition(id).group === 'usage') && Date.now() - lastUsage >= 60000) {
        lastUsage = Date.now();
        try {
          const response = await fetch('/api/menu-bar/usage');
          usage = response.ok ? await response.json() : null;
        } catch { usage = null; }
      }
    } catch { previewError = true; }
    finally { loading = false; syncFaces(); }
  }
  function closeMenu() { if (menu.matches(':popover-open')) menu.hidePopover(); }
  function clearDrag() {
    for (const list of [active, toolbox]) {
      list.classList.remove('drop-target');
      for (const row of list.children) row.classList.remove('dragging', 'drop-before', 'drop-after');
    }
  }
  function focusTool(id) {
    const list = settings.order.includes(id) ? active : toolbox;
    const target = isMenuBarSeparator(id) && list === toolbox ? 'separator' : id;
    list.querySelector(`[data-tool="${target}"] button`)?.focus();
  }
  function commit(next, message, focusId) {
    closeMenu(); settings = next;
    window.pharosPrefs.set(MENU_BAR_KEY, settings); notify(); render();
    get('menuBarEditorStatus').textContent = message;
    if (focusId) focusTool(focusId);
    refreshPreview();
  }
  function change(id, destination = settings.order.length) {
    const next = editMenuBarSettings(settings, id, destination), tool = definition(id);
    if (next.order.join() === settings.order.join()) return;
    const focusId = id === 'separator' ? next.order.find(item => !settings.order.includes(item)) : id;
    commit(next, destination === null ? `${tool.label} removed from the menu.` : `${tool.label} is in position ${next.order.indexOf(focusId) + 1}.`, focusId);
  }
  function prepareMenu(tool, handle, index) {
    closeMenu();
    const make = (label, run, disabled = false) => {
      const button = element('button', '', label); button.type = 'button'; button.disabled = disabled; button.onclick = run; return button;
    };
    menu.replaceChildren(make('Move up', () => change(tool.id, index - 1), index === 0),
      make('Move down', () => change(tool.id, index + 2), index === settings.order.length - 1),
      ...(!tool.required ? [make('Remove', () => change(tool.id, null))] : []));
    menu.querySelector('button:not(:disabled)')?.setAttribute('autofocus', '');
    const rect = handle.getBoundingClientRect();
    menu.style.left = Math.max(8, Math.min(innerWidth - 180, rect.left)) + 'px';
    menu.style.top = Math.max(8, Math.min(innerHeight - 100, rect.bottom + 6)) + 'px';
  }
  function renderDisclosure() {
    if (!settings.enabled) expanded = false;
    root.classList.toggle('is-enabled', settings.enabled);
    const button = get('menuBarExpand');
    button.hidden = !settings.enabled;
    button.setAttribute('aria-expanded', String(expanded));
    button.querySelector('span').textContent = expanded ? 'Collapse' : 'Expand';
    get('menuBarOptions').hidden = !settings.enabled || !expanded;
  }
  function render() {
    get('showMacMenuBar').classList.toggle('on', settings.enabled);
    get('showMacMenuBar').setAttribute('aria-checked', String(settings.enabled));
    renderDisclosure();
    clearDrag(); active.replaceChildren(); toolbox.replaceChildren();
    let group;
    settings.order.forEach((id, index) => {
      const tool = definition(id), row = element('li', 'menu-bar-tool'), face = element('div', 'menu-bar-face');
      row.dataset.tool = id; row.draggable = true;
      if (tool.separator) {
        row.classList.add('menu-bar-separator');
        const line = element('span', 'menu-bar-separator-line');
        line.setAttribute('role', 'separator'); line.setAttribute('aria-label', 'Menu separator'); face.append(line);
      } else {
        if (group !== tool.group && ['usage', 'library'].includes(tool.group)) row.append(element('div', 'menu-bar-heading', tool.group === 'usage' ? 'Last 24 hours' : 'Library'));
        group = tool.group;
      }
      const handle = element('button', 'menu-bar-handle'); handle.type = 'button'; handle.append(glyph('grip'));
      handle.setAttribute('aria-label', `Arrange ${tool.label}${tool.separator ? ' ' + id.split(':')[1] : ''}`);
      handle.title = 'Drag to move. Click for actions. Alt+↑/↓ to reorder; Delete to remove.';
      handle.setAttribute('popovertarget', 'menuBarToolMenu'); handle.onclick = () => prepareMenu(tool, handle, index);
      handle.onkeydown = event => {
        if (event.altKey && ['ArrowUp', 'ArrowDown'].includes(event.key)) {
          event.preventDefault(); change(id, event.key === 'ArrowUp' ? index - 1 : index + 2);
        } else if (['Delete', 'Backspace'].includes(event.key)) {
          event.preventDefault();
          if (tool.required) get('menuBarEditorStatus').textContent = `${tool.label} always stays in the menu.`;
          else change(id, null);
        }
      };
      const entry = element('div', 'menu-bar-entry'); entry.append(face, handle);
      row.append(entry); installDrag(row, tool);
      row.ondragover = event => {
        if (!dragId) return;
        event.preventDefault(); event.stopPropagation(); clearDrag();
        const rect = row.getBoundingClientRect();
        row.classList.add(event.clientY > rect.top + rect.height / 2 ? 'drop-after' : 'drop-before');
      };
      row.ondrop = event => {
        if (!dragId) return;
        event.preventDefault(); event.stopPropagation();
        const rect = row.getBoundingClientRect();
        change(dragId, index + (event.clientY > rect.top + rect.height / 2 ? 1 : 0)); dragId = null; clearDrag();
      };
      active.append(row);
    });
    let availableGroup;
    for (const tool of [...MENU_BAR_TOOLS.filter(tool => !settings.order.includes(tool.id)), MENU_BAR_SEPARATOR]) {
      const row = element('li', 'menu-bar-available'), add = element('button', 'menu-bar-add');
      row.dataset.tool = tool.id; row.draggable = true;
      add.type = 'button'; add.setAttribute('aria-label', `Add ${tool.label}`);
      add.title = tool.description;
      add.setAttribute('aria-description', tool.description);
      add.append(element('span', '', tool.label));
      if (tool.separator) add.append(element('span', 'menu-bar-separator-line'));
      add.append(glyph('plus'));
      if (availableGroup && availableGroup !== tool.group) row.classList.add('menu-bar-section');
      availableGroup = tool.group;
      add.onclick = () => change(tool.id, tool.separator ? settings.order.length - 1 : settings.order.length); row.append(add); installDrag(row, tool); toolbox.append(row);
    }
    syncFaces();
  }
  function installDrag(row, tool) {
    row.ondragstart = event => {
      closeMenu(); dragId = tool.id; event.dataTransfer.effectAllowed = 'move'; event.dataTransfer.setData('text/plain', tool.label); row.classList.add('dragging');
    };
    row.ondragend = () => { dragId = null; clearDrag(); };
  }
  for (const [list, inMenu] of [[active, true], [toolbox, false]]) {
    list.ondragover = event => {
      if (!dragId || (!inMenu && definition(dragId).required)) return;
      event.preventDefault(); event.dataTransfer.dropEffect = 'move'; list.classList.add('drop-target');
    };
    list.ondragleave = event => { if (!list.contains(event.relatedTarget)) list.classList.remove('drop-target'); };
    list.ondrop = event => {
      if (!dragId) return;
      event.preventDefault();
      if (!inMenu && definition(dragId).required) get('menuBarEditorStatus').textContent = `${definition(dragId).label} always stays in the menu.`;
      else change(dragId, inMenu ? settings.order.length : null);
      dragId = null; clearDrag();
    };
  }
  get('showMacMenuBar').onclick = () => commit({...settings, enabled: !settings.enabled}, settings.enabled ? 'Mac menu bar hidden.' : 'Mac menu bar shown.');
  get('menuBarExpand').onclick = () => {
    expanded = !expanded; closeMenu(); renderDisclosure();
    if (expanded) refreshPreview();
  };
  get('menuBarReset').onclick = () => commit({...normalizeMenuBarSettings(), enabled: settings.enabled}, 'Default menu restored.');
  addEventListener('resize', closeMenu);
  document.querySelector('main').addEventListener('scroll', closeMenu);
  window.addEventListener('pharos:preferences-saved', event => {
    if (event.detail?.set?.[MENU_BAR_KEY]) { notify(); refreshPreview(); }
    if (event.detail?.set?.['pharos-carbon-v1']) {
      lastUsage = 0; refreshPreview();
      try { native()?.postMessage({refreshUsage: true}); } catch {}
    }
  });
  new IntersectionObserver(entries => { if (entries.some(entry => entry.isIntersecting)) refreshPreview(); }).observe(root);
  setInterval(refreshPreview, 15000);
  render(); notify();
})();
