// macOS status menu preferences. Optional lines keep the native menu's fixed
// order; this settings page controls visibility only.
(() => {
  'use strict';
  const root = document.getElementById('menuBarPreferences');
  if (!root) return;
  const key = 'pharos-menu-bar';
  const defaults = {enabled: true, indexed: true, words: true, tokens: true, carbon: true,
    cost: false, backup: false, space: false, index: false, eject: false};
  const stored = window.pharosPrefs.get(key, {});
  const settings = Object.fromEntries(Object.entries(defaults).map(([name, fallback]) =>
    [name, typeof stored?.[name] === 'boolean' ? stored[name] : fallback]));
  const information = [
    ['indexed', 'Last successful index', 'Freshness, beneath the current status.'],
    ['words', 'Words typed', 'Estimated human words in archived conversations · last 24 hours.'],
    ['tokens', 'Tokens used', 'Combined token usage · last 24 hours.'],
    ['cost', 'Estimated API cost', 'API-equivalent cost at list prices · last 24 hours.'],
    ['carbon', 'Estimated carbon impact', 'CO₂e using your Carbon Impact settings · last 24 hours.'],
    ['backup', 'Last successful backup', 'When the library was last fully backed up.'],
    ['space', 'Library size and free space', 'Archive size and free space on its drive.'],
  ];
  const actions = [
    ['index', 'Index changes now', 'Start indexing from beside the last-indexed information.'],
    ['eject', 'Eject library', 'Shown above Quit when the library drive is ejectable. Uses Pharos’s safe eject flow.'],
  ];
  const native = () => window.webkit?.messageHandlers?.pharosMenuBar;
  const controls = new Map();
  const notify = () => { try { native()?.postMessage({...settings}); } catch {} };
  function update() {
    controls.forEach((control, name) => {
      control.classList.toggle('on', settings[name]);
      control.setAttribute('aria-checked', String(settings[name]));
    });
    document.getElementById('menuBarOptions').hidden = !settings.enabled;
  }
  function toggle(name, control) {
    controls.set(name, control);
    control.onclick = () => {
      settings[name] = !settings[name];
      window.pharosPrefs.set(key, {...settings});
      update();
      notify();
    };
  }
  function addRows(container, items) {
    for (const [name, label, description] of items) {
      const row = document.createElement('div'); row.className = 'optional-behavior';
      const content = document.createElement('div');
      const title = document.createElement('h3'); title.id = `menuBar-${name}-title`; title.textContent = label;
      const detail = document.createElement('p'); detail.id = `menuBar-${name}-description`; detail.className = 'muted'; detail.textContent = description;
      const control = document.createElement('button'); control.type = 'button'; control.className = 'toggle'; control.id = `menuBar-${name}`;
      control.setAttribute('role', 'switch'); control.setAttribute('aria-labelledby', title.id); control.setAttribute('aria-describedby', detail.id);
      toggle(name, control);
      content.append(title, detail); row.append(content, control); container.append(row);
    }
  }
  toggle('enabled', document.getElementById('showMacMenuBar'));
  addRows(document.getElementById('menuBarInformation'), information);
  addRows(document.getElementById('menuBarActions'), actions);
  update();
  notify();
  window.addEventListener('pharos:preferences-saved', event => {
    if (event.detail?.set?.[key]) notify();
    if (event.detail?.set?.['pharos-carbon-v1']) { try { native()?.postMessage({refreshUsage: true}); } catch {} }
  });
})();
