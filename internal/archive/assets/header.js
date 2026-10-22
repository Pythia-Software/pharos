// Add a header tool here and give its existing DOM node an element ID. The
// registry owns the toolbox, defaults, legacy preferences and allowed layouts.
const HEADER_TOOLS = [
  { id: 'history', family: 'history', element: 'headerHistory', label: 'Back / forward', description: 'Move through pages visited in this window.', on: true },
  { id: 'back', family: 'history', element: 'headerBackOnly', label: 'Back only', description: 'Go back through pages visited in this window.', on: false },
  { id: 'library', family: 'library', element: 'headerLibrary', label: 'Drive & sync status', description: 'Library activity, drive actions, and Index changes.', on: true },
  { id: 'sync', family: 'library', element: 'headerSyncOnly', label: 'Sync only', description: 'Index changes without the drive name or status.', on: false },
  { id: 'search', element: 'searchToggle', label: 'Search', description: 'Search the library. The ⌘K shortcut also works.', on: true },
  { id: 'summary', element: 'usageSummaryToggle', label: 'Analyze summary', description: 'Recent words, tokens, CO₂e, and API cost.', on: true, legacy: 'pharos-show-usage-summary-button' },
  { id: 'annotations', element: 'feedbackToggle', label: 'Agent annotations', description: 'Mark up the interface to share feedback with an agent.', on: false, legacy: 'pharos-show-agent-annotation-button' },
  { id: 'settings', element: 'settingsToggle', label: 'Settings', description: 'Keep app settings within reach.', on: true, required: true },
  { id: 'theme', element: 'themeToggle', label: 'Color theme', description: 'Switch between system, light, and dark themes.', on: true, legacy: 'pharos-show-theme-button' },
  { id: 'navigator', element: 'navigatorToggle', label: 'Navigation bar', description: 'Navigate by URL, save bookmarks, and view page history.', on: false, legacy: 'pharos-show-nav-button' },
];
const HEADER_LAYOUT_KEY = 'pharos-header-tools-v1';

// Collect tools from the left, reserving More's width as soon as one moves.
function headerToolPlacement(tools, available, widths, gap, moreWidth) {
  let first = 0;
  let used = tools.reduce((sum, id) => sum + widths[id], 0) + gap * Math.max(0, tools.length - 1);
  while (first < tools.length && used > available) {
    used -= widths[tools[first]] + (first ? gap : 0);
    if (!first) used += moreWidth;
    first++;
  }
  return { inline: tools.slice(first), overflow: tools.slice(0, first) };
}

// Keep the tabs until even the fully collected desktop toolbar cannot fit.
// The dropdown then gets its own row, freeing the first row for tools again.
// All dimensions come from the rendered controls and computed header styles.
function headerNavigationPlacement(tools, { width, brandWidth, tabsWidth, widths, gap, moreWidth, barGap }) {
  const toolbarGap = tools.length ? barGap : 0;
  const desktopAvailable = width - brandWidth - tabsWidth - barGap - toolbarGap;
  const phone = desktopAvailable < (tools.length ? moreWidth : 0);
  const available = phone ? width - brandWidth - toolbarGap : desktopAvailable;
  return { phone, ...headerToolPlacement(tools, available, widths, gap, moreWidth) };
}

function headerToolAnchor(element) {
  return element?.getClientRects().length ? element : document.getElementById('headerMore');
}

function initHeaderOverflow(readTools) {
  const root = document.querySelector('body>header .header-icons');
  const more = document.getElementById('headerMore'), popup = document.getElementById('headerOverflow'), list = document.getElementById('headerOverflowTools');
  if (!root || !more || !popup || !list) return () => {};
  const bar = root.parentElement, brand = bar.querySelector('.brand'), tabs = bar.querySelector('.tabs'), location = document.getElementById('appLocation');
  const rows = new Map();
  for (const tool of HEADER_TOOLS) {
    const row = document.createElement('div'), label = document.createElement('span');
    row.className = 'header-overflow-row';
    row.hidden = true;
    label.textContent = tool.label;
    row.append(label);list.append(row);rows.set(tool.id, row);
  }
  const close = () => { if (popup.matches(':popover-open')) popup.hidePopover(); };
  function placement() {
    const tools = readTools(), style = getComputedStyle(bar), gap = parseFloat(style.columnGap) || 0;
    const width = bar.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight);
    // Measure inert copies in the inline context, including tools currently
    // inside the closed popover. Never move live controls just to measure them:
    // doing so would blur keyboard focus and reset their popovers.
    const probe = document.createElement('div');
    probe.className = 'header-icons';
    probe.inert = true;
    probe.setAttribute('aria-hidden', 'true');
    probe.style.cssText = 'position:absolute;visibility:hidden;width:max-content;max-width:none;left:0;top:0';
    const ids = [...tools, 'more'];
    for (const id of ids) {
      const source = id === 'more' ? more : document.getElementById(HEADER_TOOLS.find(tool => tool.id === id).element);
      const copy = source.cloneNode(true);
      for (const node of [copy, ...copy.querySelectorAll('[id]')]) node.removeAttribute('id');
      copy.hidden = false;
      probe.append(copy);
    }
    bar.append(probe);
    const widths = Object.fromEntries(ids.map((id, index) => [id, probe.children[index].getBoundingClientRect().width]));
    const controlGap = parseFloat(getComputedStyle(probe).columnGap) || 0;
    probe.remove();
    const measure = source => {
      const copy = source.cloneNode(true);
      for (const node of [copy, ...copy.querySelectorAll('[id]')]) node.removeAttribute('id');
      copy.inert = true;
      copy.setAttribute('aria-hidden', 'true');
      copy.style.cssText = 'position:absolute;visibility:hidden;display:flex;width:max-content;max-width:none;left:0;top:0';
      bar.append(copy);
      const width = copy.getBoundingClientRect().width;
      copy.remove();
      return width;
    };
    return headerNavigationPlacement(tools, {
      width, brandWidth: measure(brand), tabsWidth: measure(tabs), widths,
      gap: controlGap, moreWidth: widths.more, barGap: gap,
    });
  }
  function layout(focused = document.activeElement) {
    const ownsFocus = root.contains(focused) || list.contains(focused);
    const { phone, inline, overflow } = placement();
    const html = document.documentElement;
    if (html.classList.contains('app-header-phone') !== phone) {
      html.classList.toggle('app-header-phone', phone);
      dispatchEvent(new Event('pharos:header-layout'));
    }
    for (const tool of HEADER_TOOLS) {
      const node = document.getElementById(tool.element), row = rows.get(tool.id);
      const overflows = overflow.includes(tool.id);
      if (row.hidden !== !overflows) row.hidden = !overflows;
      if (!node) continue;
      const target = overflows ? row : root;
      if (node.parentElement !== target) target === root ? root.insertBefore(node, more) : target.append(node);
    }
    // Work backwards from each list's end, leaving controls in place when
    // their order is already correct. Reinserting a focused node blurs it.
    let next = null;
    for (const id of [...inline].reverse()) {
      const node = document.getElementById(HEADER_TOOLS.find(tool => tool.id === id).element);
      if (!node) continue;
      if (node.nextElementSibling !== next) root.insertBefore(node, next);
      next = node;
    }
    if (root.firstElementChild !== more) root.insertBefore(more, root.firstElementChild);
    next = null;
    for (const id of [...overflow].reverse()) {
      const row = rows.get(id);
      if (row.nextElementSibling !== next) list.insertBefore(row, next);
      next = row;
    }
    if (more.hidden !== (overflow.length === 0)) more.hidden = overflow.length === 0;
    if (more.hidden) close();
    if (popup.matches(':popover-open')) place();
    if (ownsFocus) {
      // A required move between inline and overflow can also blur a tool.
      // Restore it when visible, otherwise keep focus on a reachable control.
      const target = focused.getClientRects().length ? focused : more.hidden
        ? [...root.querySelectorAll('button:not(:disabled)')].find(node => node.getClientRects().length) : more;
      if (target && document.activeElement !== target) target.focus({ preventScroll: true });
    }
  }
  function place() {
    const bottom = document.querySelector('body>header').getBoundingClientRect().bottom;
    popup.style.top = `${Math.round(bottom + 8)}px`;
    popup.style.maxHeight = `${Math.max(0, innerHeight - bottom - 20)}px`;
  }
  more.setAttribute('popovertarget', 'headerOverflow');
  more.onclick = place;
  document.getElementById('headerOverflowClose').onclick = () => { close(); more.focus(); };
  popup.addEventListener('toggle', () => { more.setAttribute('aria-expanded', String(popup.matches(':popover-open'))); });
  // Close before invoking the existing handler. Dialogs and floating panels
  // can then anchor to More and restore focus there when their tool is hidden.
  list.addEventListener('click', event => { if (event.target.closest('button')) { close(); more.focus({ preventScroll: true }); } }, true);
  addEventListener('resize', () => { const focused = document.activeElement; close(); layout(focused); });
  addEventListener('pharos:route', close);
  let frame = 0;
  const schedule = () => {
    if (!frame) frame = requestAnimationFrame(() => { frame = 0; layout(); });
  };
  // Font loading, drive labels, tab visibility, preferences, and native window
  // padding can change the fit without a window resize.
  const resize = new ResizeObserver(schedule);
  for (const node of [bar, brand, tabs, root, location].filter(Boolean)) resize.observe(node);
  const changes = new MutationObserver(schedule);
  for (const node of [tabs, root, list, location].filter(Boolean)) changes.observe(node, { subtree: true, childList: true, characterData: true, attributes: true });
  document.fonts?.ready.then(schedule);
  return layout;
}

function defaultHeaderTools(read = (_key, fallback) => fallback) {
  return HEADER_TOOLS.filter(tool => tool.required || (tool.legacy ? read(tool.legacy, tool.on) === true : tool.on)).map(tool => tool.id);
}

function normalizeHeaderTools(value, read) {
  if (!Array.isArray(value)) return defaultHeaderTools(read);
  const tools = [], families = new Set();
  for (const id of value) {
    const tool = HEADER_TOOLS.find(tool => tool.id === id);
    if (!tool || tools.includes(id) || (tool.family && families.has(tool.family))) continue;
    tools.push(id);
    if (tool.family) families.add(tool.family);
  }
  for (const tool of HEADER_TOOLS) if (tool.required && !tools.includes(tool.id)) tools.push(tool.id);
  return tools;
}

// A destination is an insertion boundary in the original list. A null
// destination returns a tool to the toolbox; required tools cannot be removed.
function editHeaderTools(value, id, destination) {
  const tools = normalizeHeaderTools(value), tool = HEADER_TOOLS.find(tool => tool.id === id);
  if (!tool || (destination === null && tool.required)) return tools;
  const removed = new Set(tools.filter(other => other === id || (destination !== null && tool.family && HEADER_TOOLS.find(tool => tool.id === other).family === tool.family)));
  const next = tools.filter(other => !removed.has(other));
  if (destination !== null) {
    const boundary = Math.max(0, Math.min(tools.length, destination));
    const precedingRemoved = tools.slice(0, boundary).filter(other => removed.has(other)).length;
    next.splice(boundary - precedingRemoved, 0, id);
  }
  return next;
}

// Clicking an alternative replaces its current variant in place; a drag can
// instead choose any insertion boundary in the header.
function headerToolAddPosition(value, id) {
  const tools = normalizeHeaderTools(value), family = HEADER_TOOLS.find(tool => tool.id === id)?.family;
  const existing = family ? tools.findIndex(other => HEADER_TOOLS.find(tool => tool.id === other).family === family) : -1;
  return existing >= 0 ? existing : tools.length;
}

function initHeaderHistory(changed) {
  const key = 'pharos-nav-max', push = history.pushState.bind(history), replace = history.replaceState.bind(history);
  const position = () => Number.isInteger(history.state?.pharosIndex) && history.state.pharosIndex >= 0 ? history.state.pharosIndex : 0;
  let max = position();
  try { max = Math.max(max, Number(sessionStorage.getItem(key)) || 0); } catch {}
  const saveMax = value => { max = value; try { sessionStorage.setItem(key, String(value)); } catch {} };
  // Mark the initial entry too, so reloading it retains the forward branch.
  if (!Number.isInteger(history.state?.pharosIndex)) {
    replace({ ...(history.state || {}), pharosIndex: 0 }, '', location.href);
    saveMax(0);
  }
  function sync() {
    document.getElementById('navBack').disabled = position() <= 0;
    const backOnly = document.getElementById('headerBackOnly');
    if (backOnly) backOnly.disabled = position() <= 0;
    document.getElementById('navForward').disabled = position() >= max;
  }
  history.pushState = (state, title, url) => {
    const index = position() + 1;
    push({ ...(state || {}), pharosIndex: index }, title, url);
    saveMax(index);
    sync();
    changed();
  };
  history.replaceState = (state, title, url) => {
    replace({ ...(state || {}), pharosIndex: position() }, title, url);
    sync();
    changed();
  };
  addEventListener('popstate', () => { sync(); changed(); });
  sync();
  return sync;
}

function initHeaderControls(close = {}) {
  const root = document.querySelector('body>header .header-icons');
  const active = document.getElementById('headerActive'), toolbox = document.getElementById('headerToolbox');
  if (!root || !active || !toolbox) return;
  let tools = normalizeHeaderTools(pharosPrefs.get(HEADER_LAYOUT_KEY, null), (key, fallback) => pharosPrefs.get(key, fallback));
  const layoutOverflow = initHeaderOverflow(() => tools);
  let dragId = null;
  const get = id => document.getElementById(id);
  const definition = id => HEADER_TOOLS.find(tool => tool.id === id);
  const element = (tag, className, text) => {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text) node.textContent = text;
    return node;
  };
  const glyph = name => {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('aria-hidden', 'true');
    use.setAttribute('href', '#ph-icon-' + name);
    svg.append(use);
    return svg;
  };
  // Use the same rendered controls and styles as the live header, without
  // duplicating identities, route bindings, focus targets or live actions.
  function appearance(source) {
    const copy = source.cloneNode(true);
    for (const node of [copy, ...copy.querySelectorAll('*')]) {
      for (const attr of [...node.attributes]) {
        if (attr.name === 'id' || attr.name === 'data-view' || attr.name.startsWith('on') || attr.name.startsWith('aria-')) node.removeAttribute(attr.name);
      }
      if (node.matches('button,input,a,[tabindex]')) node.tabIndex = -1;
    }
    copy.hidden = false;
    copy.inert = true;
    copy.setAttribute('aria-hidden', 'true');
    return copy;
  }
  const brand = get('headerPreviewBrand');
  const brandCopy = appearance(document.querySelector('body>header .brand'));
  brandCopy.classList.remove('sweeping', 'loading');
  brand.append(brandCopy);
  // Keep the real header's proportions and wrapping, fitting the complete
  // bar into the settings column rather than stretching its individual gaps.
  const liveBar = document.querySelector('body>header .bar'), previewBar = document.querySelector('.header-preview-bar');
  const preview = previewBar.parentElement;
  let previewWidth = 0, barWidth = 0, fitFrame = 0;
  function fitPreview() {
    if (!preview.clientWidth || !liveBar.clientWidth) return;
    if (previewWidth === preview.clientWidth && barWidth === liveBar.clientWidth) return;
    previewWidth = preview.clientWidth;
    barWidth = liveBar.clientWidth;
    previewBar.style.width = barWidth + 'px';
    previewBar.style.zoom = String(Math.min(1, previewWidth / barWidth));
  }
  // Zoom changes the observed preview's height. Apply it in the next frame
  // and only when widths change, so WebKit can finish delivering resize events.
  const previewResize = new ResizeObserver(() => {
    if (!fitFrame) fitFrame = requestAnimationFrame(() => { fitFrame = 0; fitPreview(); });
  });
  previewResize.observe(preview);
  previewResize.observe(liveBar);
  fitPreview();
  const tabs = get('headerPreviewTabs'), liveTabs = document.querySelector('body>header .tabs');
  function syncTabs() {
    tabs.replaceChildren(...[...liveTabs.querySelectorAll('button:not([hidden])')].map(appearance));
  }
  syncTabs();

  function apply() {
    for (const tool of HEADER_TOOLS) {
      const node = get(tool.element), visible = tools.includes(tool.id);
      if (!node) continue;
      if (!visible) close[tool.id]?.();
      node.hidden = !visible;
    }
    for (const id of tools) root.append(get(definition(id).element));
    layoutOverflow();
    dispatchEvent(new Event('resize'));
  }
  function clearDrag(keepSource = false) {
    for (const list of [active, toolbox]) {
      list.classList.remove('drop-target');
      list.querySelectorAll('.header-tool').forEach(row => {
        row.classList.remove('drop-before', 'drop-after');
        row.classList.toggle('dragging', keepSource && row.dataset.tool === dragId);
      });
    }
  }
  const menu = get('headerToolMenu');
  function closeMenu() { if (menu.matches(':popover-open')) menu.hidePopover(); }
  function focusTool(id) {
    const row = [...active.children, ...toolbox.children].find(row => row.dataset.tool === id);
    row?.querySelector(tools.includes(id) ? '.header-drag-handle' : '.header-tool-face')?.focus();
  }
  function commit(next, message, focusId) {
    closeMenu();
    tools = next;
    pharosPrefs.set(HEADER_LAYOUT_KEY, tools);
    apply();
    render();
    get('headerEditorStatus').textContent = message;
    if (focusId) focusTool(focusId);
  }
  function change(id, destination = headerToolAddPosition(tools, id)) {
    const tool = definition(id), next = editHeaderTools(tools, id, destination);
    if (next.join() === tools.join()) return;
    commit(next, destination === null ? `${tool.label} moved to the toolbox.` : `${tool.label} is in position ${next.indexOf(id) + 1}.`, id);
  }
  function button(tool, action, label, icon, run, disabled = false) {
    const node = element('button');
    node.type = 'button';
    node.dataset.action = action;
    node.title = label;
    node.setAttribute('aria-label', `${label} ${tool.label}`);
    node.append(glyph(icon));
    if (action === 'remove') node.append('Remove');
    node.disabled = disabled;
    node.onclick = run;
    return node;
  }
  function prepareMenu(tool, handle, inHeader, index) {
    if (menu.dataset.tool !== tool.id) closeMenu();
    menu.dataset.tool = tool.id;
    const actions = inHeader ? [
      button(tool, 'up', 'Move left', 'arrow-left', () => change(tool.id, index - 1), index === 0),
      button(tool, 'down', 'Move right', 'arrow-right', () => change(tool.id, index + 2), index === tools.length - 1),
      ...(!tool.required ? [button(tool, 'remove', 'Remove', 'close', () => change(tool.id, null))] : []),
    ] : [button(tool, 'add', 'Add', 'plus', () => change(tool.id))];
    if (!inHeader) actions[0].append('Add');
    menu.replaceChildren(...actions);
    actions.find(action => !action.disabled)?.setAttribute('autofocus', '');
    const rect = handle.getBoundingClientRect();
    menu.style.left = Math.max(8, Math.min(innerWidth - 210, rect.left)) + 'px';
    menu.style.top = Math.min(innerHeight - 55, rect.bottom + 6) + 'px';
  }
  function syncAppearances() {
    // Library status and the theme can change after the editor first renders.
    // Refresh only their inert faces, so focus and drags survive.
    for (const row of [...active.children, ...toolbox.children]) {
      const tool = definition(row.dataset.tool);
      if (!tool) continue;
      const source = get(tool.element), face = row.querySelector('.header-tool-face');
      if (source) face.replaceChildren(appearance(source));
    }
  }
  function render() {
    clearDrag();
    active.replaceChildren();
    toolbox.replaceChildren();
    const ordered = tools.map(definition), available = HEADER_TOOLS.filter(tool => !tools.includes(tool.id));
    for (const [list, entries, inHeader] of [[active, ordered, true], [toolbox, available, false]]) {
      entries.forEach((tool, index) => {
        const row = element('li', 'header-tool'), face = element('div', 'header-tool-face');
        row.dataset.tool = tool.id;
        row.draggable = true;
        face.title = inHeader ? tool.label : `${tool.label}: ${tool.description}`;
        if (!inHeader) {
          face.tabIndex = 0;
          face.setAttribute('role', 'button');
          face.setAttribute('aria-label', `Add ${tool.label}`);
          face.onclick = () => change(tool.id);
          face.onkeydown = event => {
            if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); face.click(); }
          };
        }
        const handle = element('button', 'header-drag-handle');
        handle.type = 'button';
        handle.draggable = true;
        handle.append(glyph('grip'));
        handle.setAttribute('aria-label', `Arrange ${tool.label}`);
        handle.title = tool.required ? 'Drag to move. Settings always stays in the header.' : 'Drag to move. Click for actions. Alt+←/→ to reorder; Delete to remove.';
        handle.setAttribute('popovertarget', 'headerToolMenu');
        handle.onclick = () => prepareMenu(tool, handle, inHeader, index);
        handle.onkeydown = event => {
          if (inHeader && event.altKey && ['ArrowLeft', 'ArrowRight'].includes(event.key)) {
            event.preventDefault();
            change(tool.id, event.key === 'ArrowLeft' ? index - 1 : index + 2);
          } else if (inHeader && ['Delete', 'Backspace'].includes(event.key)) {
            event.preventDefault();
            if (tool.required) get('headerEditorStatus').textContent = 'Settings always stays in the header.';
            else change(tool.id, null);
          }
        };
        row.append(face, handle);
        if (!inHeader) row.append(element('span', 'header-tool-caption', tool.label));
        row.ondragstart = event => {
          closeMenu();
          dragId = tool.id;
          event.dataTransfer.effectAllowed = 'move';
          event.dataTransfer.setData('text/plain', tool.label);
          event.dataTransfer.setDragImage(face, face.offsetWidth / 2, face.offsetHeight / 2);
          row.classList.add('dragging');
        };
        row.ondragend = () => { dragId = null; clearDrag(); };
        row.ondragover = event => {
          if (!dragId || !inHeader) return;
          event.preventDefault();
          clearDrag(true);
          const after = event.clientX > row.getBoundingClientRect().left + row.getBoundingClientRect().width / 2;
          row.classList.add(after ? 'drop-after' : 'drop-before');
        };
        row.ondragleave = () => row.classList.remove('drop-before', 'drop-after');
        row.ondrop = event => {
          if (!dragId || !inHeader) return;
          event.preventDefault();
          event.stopPropagation();
          const after = event.clientX > row.getBoundingClientRect().left + row.getBoundingClientRect().width / 2;
          change(dragId, index + (after ? 1 : 0));
          dragId = null;
        };
        list.append(row);
      });
      if (!entries.length) list.append(element('li', 'muted', 'All buttons are in your header. Drop one here to remove it.'));
    }
    syncAppearances();
  }
  for (const [list, inHeader] of [[active, true], [toolbox, false]]) {
    list.ondragover = event => {
      if (!dragId) return;
      if (!inHeader && definition(dragId).required) {
        event.dataTransfer.dropEffect = 'none';
        list.classList.remove('drop-target');
        return;
      }
      event.preventDefault();
      event.dataTransfer.dropEffect = 'move';
      list.classList.add('drop-target');
    };
    list.ondragleave = event => { if (!list.contains(event.relatedTarget)) list.classList.remove('drop-target'); };
    list.ondrop = event => {
      if (!dragId) return;
      event.preventDefault();
      if (!inHeader && definition(dragId).required) get('headerEditorStatus').textContent = 'Settings always stays in the header.';
      else change(dragId, inHeader ? tools.length : null);
      dragId = null;
      clearDrag();
    };
  }
  addEventListener('resize', closeMenu);
  document.querySelector('main').addEventListener('scroll', closeMenu);
  get('headerReset').onclick = () => commit(defaultHeaderTools(), 'Default header restored.');
  apply();
  render();
  new MutationObserver(syncAppearances).observe(root, { subtree: true, childList: true, attributes: true });
  const overflow = get('headerOverflowTools');
  if (overflow) new MutationObserver(syncAppearances).observe(overflow, { subtree: true, childList: true, attributes: true });
  new MutationObserver(syncTabs).observe(liveTabs, { subtree: true, childList: true, attributes: true });
}
