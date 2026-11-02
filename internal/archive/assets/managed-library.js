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
// What a library does besides holding history, in the words its card shows.
// MCP always answers for the connected library.
function pharosLibraryRoles(library, state, open) {
  const name = id => (state.libraries || []).find(other => other.id === id)?.name || 'another library';
  const roles = [];
  if (open) roles.push('Agents using Pharos MCP search this library');
  if (state.delivery_source && state.delivery_target) {
    if (library.id === state.delivery_source) roles.push('Captures here and delivers to ' + name(state.delivery_target));
    if (library.id === state.delivery_target) roles.push('Receives history captured in ' + name(state.delivery_source));
  }
  if (library.recovery_point) roles.push('Restored from ' + library.recovery_point + ' · capture starts paused');
  return roles;
}
// How long ago a library was last open, in a card's words.
function pharosLibraryAge(iso, now = Date.now()) {
  const at = Date.parse(iso), minutes = Math.floor((now - at) / 60e3);
  if (!(minutes >= 0)) return '';
  if (minutes < 1) return 'just now';
  if (minutes < 60) return minutes + ' min ago';
  if (minutes < 24 * 60) return Math.floor(minutes / 60) + ' hr ago';
  const days = Math.floor((new Date(now).setHours(0, 0, 0, 0) - new Date(at).setHours(0, 0, 0, 0)) / 864e5);
  if (days <= 1) return 'yesterday';
  if (days < 7) return days + ' days ago';
  const sameYear = new Date(at).getFullYear() === new Date(now).getFullYear();
  return new Date(at).toLocaleDateString('en-US', sameYear ? {month: 'short', day: 'numeric'} : {month: 'short', day: 'numeric', year: 'numeric'});
}
// The line at the foot of a card: when a closed library was last open, and the
// conversations it held, as the app last saw them.
function pharosLibraryFacts(library, open, now = Date.now()) {
  const facts = [];
  if (!open) facts.push(library.last_opened ? 'Last opened ' + pharosLibraryAge(library.last_opened, now) : 'Not opened recently');
  if (Number.isFinite(library.conversations)) facts.push(library.conversations.toLocaleString('en-US') + (library.conversations === 1 ? ' conversation' : ' conversations'));
  return facts.join(' · ');
}
// The broker keeps only its last operation's status, and never clears it. A
// running operation is always shown; any other result for ten minutes, which
// outlasts delivery's five-minute retry, so a pause that persists stays shown.
// Pauses come from delivery and from a library that failed to open at launch;
// with no delivery pair set, neither concerns this page.
function pharosLibraryJob(job, state, now = Date.now()) {
  if (!job) return null;
  if (job.state !== 'running') {
    if (!(now - Date.parse(job.updated_at) < 10 * 60e3)) return null;
    if (job.state === 'paused' && !(state.delivery_source && state.delivery_target)) return null;
  }
  const label = {running: 'Running: ', failed: 'Last library operation failed: ', paused: 'Paused: ', complete: 'Done: '}[job.state] || '';
  return {text: label + job.phase, error: job.state === 'failed' || job.state === 'paused'};
}
async function changePharosLibrary(operation, {flush, navigate, destination = '/settings/libraries'}) {
  window.pharosLibrarySwitching = true;
  try {
    await flush();
    await operation();
    navigate(destination);
    // Navigation has started, but this document may still receive a state poll.
    // Keep the guard until unload; only a failed action may resume this page.
  } catch (error) {
    window.pharosLibrarySwitching = false;
    throw error;
  }
}

(() => {
  'use strict';
  const current = window.pharosManagedLibrary;
  const page = document.getElementById('librarySettings');
  if (!current || !page) return;
  const $ = id => document.getElementById('library-settings-' + id);
  const status = $('status');
  const forms = {create: ['new', 'name'], open: ['open', 'existing'], restore: ['restore', 'backup']};
  let state, signature = '', working = false, renaming = '', deliveryOpen = false;
  const nameOf = id => (state?.libraries || []).find(library => library.id === id)?.name || 'that library';
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
  async function action(operation) {
    if (working || !state || state.busy) return;
    const focused = document.activeElement;
    working = true;
    lock();
    try { await operation(); }
    catch (error) { notice(error.message, true); }
    finally {
      working = window.pharosLibrarySwitching === true; lock();
      if (!working && document.activeElement === document.body && focused?.isConnected) focused.focus();
    }
  }
  // Library changes and operations close this library's service; save the
  // page's pending session state first, then leave before its polls notice.
  function changeLibrary(action, body, destination) {
    return changePharosLibrary(() => call(action, body), {
      flush: () => window.pharosSession?.flush(),
      navigate: path => location.assign(path),
      destination,
    });
  }
  function icon(name) {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg'), use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    svg.setAttribute('class', 'app-icon'); svg.setAttribute('viewBox', '0 0 24 24'); svg.setAttribute('aria-hidden', 'true');
    use.setAttribute('href', '#ph-icon-' + name); svg.append(use);
    return svg;
  }
  function renameForm(library) {
    const form = document.createElement('form'); form.className = 'library-settings-rename';
    const input = document.createElement('input'); input.value = library.name; input.maxLength = 160; input.autocomplete = 'off';
    input.setAttribute('aria-label', 'Library name');
    const save = document.createElement('button'); save.type = 'submit'; save.className = 'primary'; save.textContent = 'Save';
    const cancel = document.createElement('button'); cancel.type = 'button'; cancel.textContent = 'Cancel';
    const focusRename = () => [...page.querySelectorAll('[data-rename-id]')].find(button => button.dataset.renameId === library.id)?.focus();
    const stop = () => { renaming = ''; cards(); focusRename(); };
    cancel.onclick = stop;
    input.onkeydown = event => { if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); stop(); } };
    form.onsubmit = event => {
      event.preventDefault();
      const name = input.value.trim();
      if (name === library.name) { stop(); return; }
      // The rebuilt card's button can take focus once the page unlocks.
      void action(async () => {
        await call('rename', {id: library.id, name});
        notice('Renamed to ' + name + '.');
        renaming = '';
        await refresh();
      }).then(() => { if (!renaming) focusRename(); });
    };
    form.append(input, save, cancel);
    return form;
  }
  function libraryCard(library, selected) {
    const card = document.createElement('article');
    card.className = 'library-settings-card library-settings-library' + (selected ? ' library-settings-current' : '');
    const heading = document.createElement('div'); heading.className = 'library-settings-heading';
    const external = (library.config || '').startsWith('/Volumes/');
    const tag = document.createElement('span'); tag.className = 'tag';
    const where = external ? 'External drive' : 'This Mac';
    tag.textContent = selected ? 'Connected · ' + where : !library.available ? 'Not connected · attach its drive' : where;
    if (renaming === library.id) heading.append(renameForm(library));
    else {
      const title = document.createElement('h3'); title.textContent = library.name;
      const rename = document.createElement('button'); rename.type = 'button'; rename.className = 'library-settings-icon';
      rename.title = 'Rename'; rename.setAttribute('aria-label', 'Rename ' + library.name); rename.dataset.renameId = library.id;
      rename.append(icon('edit'));
      rename.onclick = () => { renaming = library.id; cards(); const input = page.querySelector('.library-settings-rename input'); input?.focus(); input?.select(); };
      heading.append(title, rename, tag);
    }
    const path = document.createElement('p'); path.className = 'library-settings-path'; path.textContent = library.config;
    card.append(heading, path);
    if (library.error) {
      const error = document.createElement('p'); error.className = 'error library-settings-path'; error.textContent = library.error; card.append(error);
    }
    for (const role of pharosLibraryRoles(library, state, selected)) {
      const line = document.createElement('p'); line.className = 'library-settings-role'; line.textContent = role; card.append(line);
    }
    // The facts and the card's action share its last line.
    const foot = document.createElement('div'); foot.className = 'library-settings-foot';
    const facts = document.createElement('span'); facts.className = 'muted'; facts.textContent = pharosLibraryFacts(library, selected);
    foot.append(facts);
    if (!selected) {
      const button = document.createElement('button'); button.type = 'button'; button.textContent = 'Switch';
      button.setAttribute('aria-label', 'Switch to ' + library.name);
      button.dataset.libraryID = library.id;
      button.dataset.unavailable = String(!library.available);
      button.onclick = () => action(async () => {
        notice('Opening ' + library.name + '…');
        await changeLibrary('connect', {id: library.id});
      });
      foot.append(button);
    } else if (external) {
      const button = document.createElement('button'); button.type = 'button'; button.textContent = 'Release for eject';
      button.title = 'Closes this library so you can eject its drive in Finder.';
      button.onclick = () => action(async () => {
        notice('Releasing ' + library.name + '…');
        await changeLibrary('disconnect', {id: library.id}, '/app?released=1');
      });
      foot.append(button);
    }
    card.append(foot);
    return card;
  }
  // Rebuilding the cards would discard a name being typed; polls wait for it.
  function cards() {
    const focus = document.activeElement?.dataset.libraryID;
    const libraries = state.libraries || [];
    $('current').replaceChildren(libraryCard(libraries.find(library => library.id === current.id) || current, true));
    const others = libraries.filter(library => library.id !== current.id);
    $('list').replaceChildren(...others.map(library => libraryCard(library, false)));
    if (!others.length) {
      const empty = document.createElement('p'); empty.className = 'muted'; empty.textContent = 'This is your only library.'; $('list').append(empty);
    }
    if (focus) [...$('list').querySelectorAll('button')].find(button => button.dataset.libraryID === focus)?.focus();
    lock();
  }
  // Delivery's switch opens its settings; it turns delivery on only when they are saved.
  function renderDelivery() {
    const delivering = !!(state.delivery_source && state.delivery_target), open = delivering || deliveryOpen;
    $('delivery-toggle').classList.toggle('on', open);
    $('delivery-toggle').setAttribute('aria-checked', String(open));
    $('delivery-form').hidden = !open;
    $('delivery').textContent = delivering ? 'Save changes' : 'Start delivery';
    $('delivery-summary').textContent = delivering ? 'On: captures into ' + nameOf(state.delivery_source) + ' and delivers to ' + nameOf(state.delivery_target) + ' whenever it’s connected.'
      : open ? 'Choose the libraries, then start delivery.' : 'Off. Keep capturing on this Mac while your archive drive is away.';
  }
  function options(id, libraries, preferred) {
    const select = $(id), kept = select.value;
    select.replaceChildren(...libraries.map(library => {
      const option = document.createElement('option'); option.value = library.id;
      option.textContent = library.name + (library.available ? '' : ' · not connected'); return option;
    }));
    select.value = libraries.some(library => library.id === kept) ? kept : preferred || libraries[0]?.id || '';
  }
  function transferMode() {
    return [...page.querySelectorAll('input[name=library-settings-mode]')].find(input => input.checked)?.value || 'copy';
  }
  function render(next) {
    state = next;
    const job = pharosLibraryJob(next.job, next);
    $('job').hidden = !job;
    if (job) {
      $('job').textContent = job.text;
      $('job').classList.toggle('error', job.error);
    }
    const key = JSON.stringify([next.libraries, next.busy, next.delivery_source, next.delivery_target, next.application]);
    if (key !== signature && !renaming) {
      signature = key;
      const libraries = next.libraries || [], others = libraries.filter(library => library.id !== current.id);
      cards();
      $('between-empty').hidden = libraries.length > 1;
      $('transfer-panel').hidden = $('delivery-panel').hidden = libraries.length < 2;
      options('from', libraries, current.id);
      options('to', libraries, others[0]?.id);
      // Capture happens on this Mac, so propose a library that isn't on a drive.
      const local = libraries.find(library => !(library.config || '').startsWith('/Volumes/'));
      options('pending', libraries, next.delivery_source || local?.id);
      options('archive', libraries, next.delivery_target || libraries.find(library => library.id !== $('pending').value)?.id);
      renderDelivery();
      $('application').textContent = next.application ? 'This copy: ' + next.application : '';
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
    for (const [other, [button]] of Object.entries(forms)) {
      $(other + '-form').hidden = other !== kind || !open;
      $(button).setAttribute('aria-expanded', String(other === kind && open));
    }
    notice('');
    const [button, field] = forms[kind];
    if (open) {
      if (kind === 'create' && !$('name').value) {
        $('name').value = pharosLibraryName(state?.libraries || []);
        $('parent').value = state?.local_library_parent || '';
        proposal();
      }
      $(field).focus();
    } else $(button).focus();
  }
  function proposal() {
    try {
      const value = pharosLibraryProposal($('name').value, $('parent').value, $('storage').value);
      $('destination').textContent = value.path || 'Standard Pharos Libraries folder/' + value.name;
    } catch (error) { $('destination').textContent = error.message; }
  }
  for (const [kind, [button]] of Object.entries(forms)) $(button).onclick = () => form(kind, $(kind + '-form').hidden);
  for (const button of page.querySelectorAll('[data-library-cancel]')) button.onclick = () => form(button.dataset.libraryCancel, false);
  for (const input of [$('name'), $('parent')]) input.oninput = proposal;
  $('storage').onchange = () => {
    $('parent').value = $('storage').value === 'mac' ? state?.local_library_parent || '' : '';
    proposal();
  };
  for (const button of page.querySelectorAll('[data-library-pick]')) button.onclick = async () => {
    await action(async () => {
      const result = await call('choose-folder', {});
      const target = button.dataset.libraryPick;
      if (result.path) document.getElementById(target).value = result.path + (target === 'library-settings-recover-path' ? '/Recovered Library' : '');
      proposal();
    });
    // Restore focus after re-enabling the enclosing fieldset, including in Safari.
    if (!button.disabled) button.focus();
  };
  for (const input of page.querySelectorAll('input[name=library-settings-mode]')) input.onchange = () => {
    $('transfer').textContent = transferMode() === 'move' ? 'Move history' : 'Copy history';
  };
  $('open-form').onsubmit = event => {
    event.preventDefault();
    const path = $('existing').value.trim();
    if (!path) { $('existing').focus(); return; }
    void action(async () => {
      notice('Opening library…');
      await changeLibrary('open', {path});
    });
  };
  $('create-form').onsubmit = event => {
    event.preventDefault();
    let payload;
    try { payload = pharosLibraryProposal($('name').value, $('parent').value, $('storage').value); }
    catch (error) { notice(error.message, true); return; }
    void action(async () => {
      notice('Creating ' + payload.name + '…');
      await changeLibrary('create', payload, '/settings/sources');
    });
  };
  // Restores and transfers close every library while they run; the startup
  // window shows their progress and reopens a library here when they finish.
  $('restore-form').onsubmit = event => {
    event.preventDefault();
    const backup = $('backup').value.trim(), path = $('recover-path').value.trim();
    if (!backup) { $('backup').focus(); return; }
    if (!path) { $('recover-path').focus(); return; }
    void action(async () => {
      notice('Starting restoration…');
      await changeLibrary('recover', {backup, path}, '/app?after=operation');
    });
  };
  $('transfer-form').onsubmit = event => {
    event.preventDefault();
    const source = $('from').value, target = $('to').value, move = transferMode() === 'move';
    if (source === target) { notice('Choose two different libraries.', true); $('to').focus(); return; }
    void action(async () => {
      notice((move ? 'Starting move' : 'Starting copy') + '…');
      await changeLibrary('transfer', {source, target, move}, '/app?after=operation');
    });
  };
  $('delivery-form').onsubmit = event => {
    event.preventDefault();
    const source = $('pending').value, target = $('archive').value;
    if (source === target) { notice('Choose two different libraries.', true); $('archive').focus(); return; }
    void action(async () => {
      await call('delivery', {source, target});
      deliveryOpen = false;
      notice('Delivery is on. Pharos captures into ' + nameOf(source) + ' and delivers to ' + nameOf(target) + ' whenever it’s connected.');
      await refresh();
    });
  };
  $('delivery-toggle').onclick = () => {
    if (!(state.delivery_source && state.delivery_target)) {
      deliveryOpen = !deliveryOpen; notice(''); renderDelivery();
      if (deliveryOpen) $('pending').focus();
      return;
    }
    void action(async () => {
      await call('delivery', {source: '', target: ''});
      deliveryOpen = false;
      notice('Delivery is off. Pending history stays where it is.');
      await refresh();
    });
  };
  addEventListener('pharos:application-state', event => render(event.detail));
  addEventListener('pharos:route', () => { if (location.pathname === '/settings/libraries') void refresh(); });
  lock();
  void refresh();
})();
