// Microphone buttons for text boxes. The Mac app records and transcribes on
// this Mac (host-services `dictation`, macos/Dictation.swift); this script
// types the words at the caret, revising live text as the transcriber does.
// It does not use, or need, the system Dictation shortcut. Outside the Mac
// app, or where this Mac cannot transcribe, the buttons stay hidden.
// dictationSpaced and dictationStep are DOM-free for the unit tests.

// The transcript as it should follow `before`, the character before the
// insertion point: a space separates it from preceding text, except after an
// opening bracket or quote, or before closing punctuation.
function dictationSpaced(before, text) {
  const words = String(text).trim();
  if (!words) return '';
  if (!before || /\s/.test(before) || /[([{“‘]/.test(before) || /^[.,!?;:%)\]}”’…]/.test(words)) return words;
  return ' ' + words;
}

// Applies one transcriber result. `state.pending` is the length of the
// previous non-final text at `state.anchor` (or of a selection the dictation
// replaces); this result replaces it. A final result stays, and the next one
// starts after it. Offsets are in the field's own units.
function dictationStep(state, text, final, before) {
  const insert = dictationSpaced(before, text);
  const edit = {start: state.anchor, end: state.anchor + state.pending, text: insert};
  return {edit, state: final ? {anchor: state.anchor + insert.length, pending: 0} : {anchor: state.anchor, pending: insert.length}};
}

(() => {
  'use strict';
  const labels = {
    idle: 'Dictate', starting: 'Starting dictation. Click to cancel.', preparing: 'Downloading the speech model. Click to cancel.',
    listening: 'Stop dictation', stopping: 'Finishing dictation…',
  };
  let status = null, active = null;
  const support = () => {
    const host = window.pharosHost;
    if (!host?.capabilities?.dictation?.available) return Promise.resolve({available: false});
    return status ??= host.dictation.status().catch(() => ({available: false}));
  };

  // Connects `button` to `field` (a textarea, input, or an editor with the
  // same value/selection API). `charBefore(offset)` reads the field's text in
  // its own offsets; `onError(message)` shows a failure where the field's
  // other errors appear. Returns a function that detaches and stops listening.
  function attach({button, field, onError = () => {}, charBefore = offset => field.value.slice(Math.max(0, offset - 1), offset)}) {
    let phase = 'idle', session = null, state = null, expected = '', applying = false, detached = false, watch = 0;
    const tooltip = button.hasAttribute('data-tooltip') ? 'data-tooltip' : 'title';
    const disabled = () => field.disabled === true || field.readOnly === true || field.getAttribute('aria-disabled') === 'true';
    function render() {
      button.dataset.state = phase;
      button.setAttribute('aria-pressed', String(phase !== 'idle'));
      button.setAttribute('aria-label', labels[phase] || labels.idle);
      button.setAttribute(tooltip, labels[phase] || labels.idle);
      button.disabled = phase === 'stopping' || phase === 'idle' && disabled();
    }
    button.hidden = true;
    render();
    support().then(value => { if (!detached) button.hidden = value.available !== true; });

    function end(error) {
      clearInterval(watch);
      session = null; state = null; phase = 'idle'; render();
      if (active === controller) active = null;
      if (error) onError(error);
    }
    // Typing, clearing or sending the text, closing the field or moving to
    // another control ends dictation; what it already typed stays. The button
    // may hold focus, so a keyboard user can Tab to it and stop.
    function interfered() {
      const focus = document.activeElement;
      return !field.isConnected || !field.getClientRects().length || field.value !== expected || disabled()
        || focus !== field && focus !== button && !field.contains(focus);
    }
    function apply(edit) {
      // Editing commands act on the focused field, so borrow focus from the
      // button for the edit and give it back.
      const returnTo = document.activeElement === button ? button : null;
      applying = true;
      try {
        if (returnTo) field.focus({preventScroll: true});
        field.setSelectionRange(edit.start, edit.end);
        if (edit.text) document.execCommand('insertText', false, edit.text);
        else if (edit.end > edit.start) document.execCommand('delete');
      } finally {
        applying = false;
        returnTo?.focus({preventScroll: true});
      }
      expected = field.value;
    }
    function start() {
      if (session || disabled()) return;
      active?.cancel();
      // Read the caret before focusing: focusing an editor can move it.
      const from = Number(field.selectionStart ?? field.value.length), to = Number(field.selectionEnd ?? from);
      field.focus({preventScroll: true});
      state = {anchor: from, pending: Math.max(0, to - from)};
      expected = field.value;
      phase = 'starting'; render();
      active = controller;
      const current = window.pharosHost.dictation.start({
        onText(text, final) {
          if (session !== current) return;
          if (interfered()) { cancel(); return; }
          const step = dictationStep(state, text, final, charBefore(state.anchor));
          apply(step.edit); state = step.state;
        },
        onState(value) { if (session === current && labels[value]) { phase = value; render(); } },
        onEnd(error) { if (session === current) end(error); },
      });
      session = current;
      // Silence brings no results to check against, so look while it listens.
      watch = setInterval(() => { if (session === current && interfered()) cancel(); }, 500);
      current.started.then(() => { if (session === current && phase === 'starting') { phase = 'listening'; render(); } }, () => {});
    }
    function stop() {
      if (!session) return;
      const current = session;
      phase = 'stopping'; render();
      current.stop().catch(error => { if (session === current) end(error?.message || String(error)); });
      // The last results normally arrive within a second or two.
      setTimeout(() => { if (session === current) { current.cancel().catch(() => {}); end(null); } }, 15000);
    }
    function cancel() {
      if (!session) return;
      session.cancel().catch(() => {});
      end(null);
    }
    const controller = {cancel};
    const onMouseDown = event => event.preventDefault();
    const onClick = () => { if (phase === 'idle') start(); else if (phase === 'listening') stop(); else cancel(); };
    const onInput = () => { if (session && !applying) cancel(); };
    const onKeyDown = event => {
      if (event.key !== 'Escape' || !session) return;
      event.preventDefault(); event.stopPropagation();
      if (phase === 'listening') stop(); else cancel();
    };
    const onFocusOut = event => {
      // The button keeps the field's caret; anything else ends dictation.
      if (session && event.relatedTarget && event.relatedTarget !== button && !field.contains(event.relatedTarget)) cancel();
    };
    // Pressing the button leaves focus and the caret in the field, so words
    // go where the user was typing.
    button.addEventListener('mousedown', onMouseDown);
    button.addEventListener('click', onClick);
    field.addEventListener('input', onInput);
    field.addEventListener('keydown', onKeyDown);
    field.addEventListener('focusout', onFocusOut);
    const observer = new MutationObserver(() => { if (session && disabled()) cancel(); else render(); });
    observer.observe(field, {attributes: true, attributeFilter: ['disabled', 'readonly', 'aria-disabled', 'contenteditable']});
    return () => {
      detached = true; cancel(); observer.disconnect();
      button.removeEventListener('mousedown', onMouseDown);
      button.removeEventListener('click', onClick);
      field.removeEventListener('input', onInput);
      field.removeEventListener('keydown', onKeyDown);
      field.removeEventListener('focusout', onFocusOut);
    };
  }
  window.pharosDictation = {attach};
})();
