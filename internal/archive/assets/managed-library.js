// Keep validation and the save-before-switch boundary usable without the DOM.
function pharosLibraryName(libraries) {
  const names = new Set(libraries.map(library => library.name));
  let name = 'My Library', number = 2;
  while (names.has(name)) name = 'My Library ' + number++;
  return name;
}
function pharosLibraryProposal(name, parent, storage) {
  name = name.trim(); parent = parent.trim();
  if (!name || name === '.' || name === '..' || /[\\/\0]/.test(name) || new TextEncoder().encode(name).length > 160)
    throw Error('Enter a library name without slashes; shorten it if needed.');
  if (!['mac', 'ssd'].includes(storage)) throw Error('Choose where to store your library.');
  if (storage === 'ssd' && !parent) throw Error('Choose a parent folder on the external drive.');
  if (parent && !parent.startsWith('/') && !parent.startsWith('~/')) throw Error('Choose an absolute folder path, or one starting with ~/.');
  return {name, path: parent ? parent.replace(/\/+$/, '') + '/' + name : '', location: storage};
}
async function switchPharosLibrary(id, {flush, connect, navigate}) {
  await flush();
  await connect(id);
  navigate('/settings/libraries');
}

(() => {
  'use strict';
  const current = window.pharosManagedLibrary;
  const page = document.getElementById('librarySettings');
  if (!current || !page) return;
  const $ = id => document.getElementById('library-settings-' + id);
  const status = $('status');
  let state, signature = '', working = false;
  function notice(message, error = false) {
    status.textContent = message;
    status.classList.toggle('error', error);
  }
  async function call(action, body) {
    const response = await fetch('/api/app/' + action, body === undefined ? {} : {
      method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body),
    });
    const result = await response.json();
    if (!response.ok) throw Error(result.error || 'Could not complete this library action. Try again.');
    return result;
  }
  function lock() {
    const disabled = working || !state || state.busy;
    for (const button of page.querySelectorAll('button')) button.disabled = disabled || button.dataset.unavailable === 'true';
    for (const fieldset of page.querySelectorAll('fieldset')) fieldset.disabled = disabled;
  }
  async function action(operation, switching = false) {
    if (working || !state || state.busy) return;
    const focused = document.activeElement;
    working = true;
    window.pharosLibrarySwitching = switching;
    lock();
    try { await operation(); }
    catch (error) { notice(error.message, true); }
    finally {
      working = false; window.pharosLibrarySwitching = false; lock();
      if (document.activeElement === document.body && focused?.isConnected) focused.focus();
    }
  }
  function libraryCard(library, selected) {
    const card = document.createElement('article');
    card.className = 'library-settings-card' + (selected ? ' library-settings-current' : '');
    const heading = document.createElement('div'); heading.className = 'library-settings-heading';
    const title = document.createElement('h3'); title.textContent = library.name;
    const tag = document.createElement('span'); tag.className = 'tag';
    tag.textContent = selected ? 'Current library' : library.available ? 'Available' : 'Unavailable · connect its drive';
    heading.append(title, tag);
    const path = document.createElement('p'); path.className = 'library-settings-path'; path.textContent = library.config;
    card.append(heading, path);
    if (library.error) {
      const error = document.createElement('p'); error.className = 'error library-settings-path'; error.textContent = library.error; card.append(error);
    }
    if (library.id === state.mcp) {
      const mcp = document.createElement('p'); mcp.className = 'muted'; mcp.textContent = 'Assigned to MCP' + (selected ? '' : ' · switch here to make it available to agents'); card.append(mcp);
    }
    if (!selected) {
      const row = document.createElement('div'); row.className = 'library-settings-actions';
      const button = document.createElement('button'); button.type = 'button'; button.textContent = 'Switch to library';
      button.setAttribute('aria-label', 'Switch to ' + library.name);
      button.dataset.libraryID = library.id;
      button.dataset.unavailable = String(!library.available);
      button.onclick = () => action(async () => {
        notice('Opening ' + library.name + '…');
        await switchPharosLibrary(library.id, {
          flush: () => window.pharosSession?.flush(),
          connect: id => call('connect', {id}),
          navigate: path => location.assign(path),
        });
      }, true);
      row.append(button); card.append(row);
    }
    return card;
  }
  function render(next) {
    state = next;
    const key = JSON.stringify([next.libraries, next.mcp, next.busy]);
    if (key !== signature) {
      signature = key;
      const focus = document.activeElement?.dataset.libraryID;
      const libraries = next.libraries || [];
      $('current').replaceChildren(libraryCard(libraries.find(library => library.id === current.id) || current, true));
      const others = libraries.filter(library => library.id !== current.id);
      $('list').replaceChildren(...others.map(library => libraryCard(library, false)));
      if (!others.length) {
        const empty = document.createElement('p'); empty.className = 'muted'; empty.textContent = 'Open or create another library to switch between collections.'; $('list').append(empty);
      }
      if (focus) [...$('list').querySelectorAll('button')].find(button => button.dataset.libraryID === focus)?.focus();
      const selected = $('mcp').value;
      $('mcp').replaceChildren(...libraries.map(library => {
        const option = document.createElement('option'); option.value = library.id; option.textContent = library.name; return option;
      }));
      $('mcp').value = libraries.some(library => library.id === selected) ? selected : next.mcp;
    }
    lock();
    if (next.busy && !working) notice('A library operation is running. You can change libraries when it finishes.');
    else if (status.textContent === 'A library operation is running. You can change libraries when it finishes.') notice('');
  }
  async function refresh() {
    try { render(await call('state')); }
    catch (error) { notice(error.message, true); }
  }
  function form(kind, open) {
    $('open-form').hidden = kind !== 'open' || !open;
    $('create-form').hidden = kind !== 'create' || !open;
    $('open').setAttribute('aria-expanded', String(kind === 'open' && open));
    $('new').setAttribute('aria-expanded', String(kind === 'create' && open));
    notice('');
    if (open) {
      if (kind === 'create' && !$('name').value) {
        $('name').value = pharosLibraryName(state?.libraries || []);
        $('parent').value = state?.local_library_parent || '';
        proposal();
      }
      $(kind === 'open' ? 'existing' : 'name').focus();
    } else $(kind === 'open' ? 'open' : 'new').focus();
  }
  function proposal() {
    try {
      const value = pharosLibraryProposal($('name').value, $('parent').value, $('storage').value);
      $('destination').textContent = value.path || 'Standard Pharos Libraries folder/' + value.name;
    } catch (error) { $('destination').textContent = error.message; }
  }
  $('open').onclick = () => form('open', $('open-form').hidden);
  $('new').onclick = () => form('create', $('create-form').hidden);
  for (const button of page.querySelectorAll('[data-library-cancel]')) button.onclick = () => form(button.dataset.libraryCancel, false);
  for (const input of [$('name'), $('parent')]) input.oninput = proposal;
  $('storage').onchange = () => {
    $('parent').value = $('storage').value === 'mac' ? state?.local_library_parent || '' : '';
    proposal();
  };
  for (const button of page.querySelectorAll('[data-library-pick]')) button.onclick = async () => {
    await action(async () => {
      const result = await call('choose-folder', {});
      if (result.path) document.getElementById(button.dataset.libraryPick).value = result.path;
      proposal();
    });
    // Restore focus after re-enabling the enclosing fieldset, including in Safari.
    if (!button.disabled) button.focus();
  };
  $('open-form').onsubmit = event => {
    event.preventDefault();
    const path = $('existing').value.trim();
    if (!path) { $('existing').focus(); return; }
    void action(async () => {
      notice('Opening library…');
      await window.pharosSession?.flush();
      await call('open', {path});
      location.assign('/settings/libraries');
    }, true);
  };
  $('create-form').onsubmit = event => {
    event.preventDefault();
    let payload;
    try { payload = pharosLibraryProposal($('name').value, $('parent').value, $('storage').value); }
    catch (error) { notice(error.message, true); return; }
    void action(async () => {
      notice('Creating ' + payload.name + '…');
      await window.pharosSession?.flush();
      await call('create', payload);
      location.assign('/settings/sources');
    }, true);
  };
  $('mcp-form').onsubmit = event => {
    event.preventDefault();
    void action(async () => {
      await call('mcp-library', {id: $('mcp').value});
      notice('MCP library assignment saved.');
      await refresh();
    });
  };
  addEventListener('pharos:application-state', event => render(event.detail));
  addEventListener('pharos:route', () => { if (location.pathname === '/settings/libraries') void refresh(); });
  lock();
  void refresh();
})();
