// One stack for every modal surface on a page. Native <dialog>s opened with
// showModal() and the non-modal layers that Escape dismisses (drawers, fixed
// panels) share it, so whichever surface is on top owns Escape, Tab and
// backdrop clicks, and focus returns to the control that opened it.
//
//   pharosModal.open(dialog, {opener, focus, dismiss, onClose, remove, backdrop})
//   pharosModal.layer(element, {dismiss, opener}) -> release()
//   pharosModal.top()        the topmost modal surface, or null
//   pharosModal.host(anchor) where floating UI for anchor should be appended
//
// React reaches the same stack through web/src/modal.tsx. This file is also
// bundled into each React entry, so the guard below keeps one stack per page.
(() => {
  'use strict';
  // Node unit tests import React modules that bundle this file: no page there.
  if (typeof window === 'undefined' || window.pharosModal) return;
  const dom = typeof HTMLDialogElement === 'function';

  // Pure helpers, exposed as pharosModal.model for unit tests.
  const model = {
    // A click on a dialog's ::backdrop targets the dialog itself, outside its box.
    outside(rect, x, y) { return x < rect.left || x > rect.right || y < rect.top || y > rect.bottom; },
    // Next focus stop for Tab, given the stops in document order. Tab is moved
    // here rather than by the engine: WebKit leaves buttons out of the tab order
    // unless Full Keyboard Access is on, so a trap that only wraps from the first
    // or last control never fires there.
    tabTarget(stops, index, backwards) {
      if (!stops.length) return -1;
      if (index < 0) return backwards ? stops.length - 1 : 0;
      return (index + (backwards ? -1 : 1) + stops.length) % stops.length;
    },
    // Only the topmost modal paints its scrim, so stacked dialogs dim the page once.
    covered(depths) { return depths.map((_, index) => index < depths.length - 1); },
  };

  const FOCUSABLE = 'a[href], area[href], button:not(:disabled), input:not(:disabled):not([type=hidden]), select:not(:disabled), textarea:not(:disabled), summary, iframe, audio[controls], video[controls], [contenteditable]:not([contenteditable=false]), [tabindex]:not([tabindex="-1"])';
  // Entries, oldest first: {element, native, options, opener}.
  const stack = [];
  const isModalDialog = element => dom && element instanceof HTMLDialogElement && element.open && element.matches(':modal');
  const live = entry => entry.element.isConnected && (!entry.native || isModalDialog(entry.element));

  function prune() {
    for (let index = stack.length - 1; index >= 0; index--) if (!live(stack[index])) finish(stack[index]);
  }
  function topEntry() { prune(); return stack.at(-1) ?? null; }

  function paint() {
    const dialogs = stack.filter(entry => entry.native);
    model.covered(dialogs).forEach((covered, index) => dialogs[index].element.toggleAttribute('data-modal-covered', covered));
  }

  function visible(element) {
    if (element.closest('[inert]')) return false;
    if (typeof element.checkVisibility === 'function') return element.checkVisibility({visibilityProperty: true});
    return element.getClientRects().length > 0;
  }
  // Tab cycles within the innermost ARIA modal (a dependency's overlay inside
  // the dialog) when focus is in one, else within the dialog.
  function tabRoot(dialog) {
    const inner = document.activeElement?.closest?.('[role=dialog][aria-modal=true]');
    return inner && inner !== dialog && dialog.contains(inner) ? inner : dialog;
  }
  function stops(root) {
    const all = [...root.querySelectorAll(FOCUSABLE)].filter(visible);
    // A radio group is one stop: its checked radio, or its first when none is.
    return all.filter(element => {
      if (!(element instanceof HTMLInputElement) || element.type !== 'radio' || element.checked || !element.name) return true;
      const group = all.filter(other => other instanceof HTMLInputElement && other.type === 'radio' && other.name === element.name && other.form === element.form);
      return !group.some(radio => radio.checked) && group[0] === element;
    });
  }
  function focusInside(root, target) {
    const element = typeof target === 'string' ? root.querySelector(target) : target;
    if (element instanceof HTMLElement) element.focus({preventScroll: true});
  }

  function restore(entry) {
    const {options, opener} = entry;
    if (options.restoreFocus === false) return;
    // Leave focus alone when the closing action deliberately moved it
    // elsewhere; the engine's own restore (to what had focus at open) is not that.
    const active = document.activeElement;
    if (active && active !== document.body && active !== entry.previous && !entry.element.contains(active)) return;
    const preferred = typeof options.restoreFocus === 'function' ? options.restoreFocus(opener) : opener;
    const target = preferred?.isConnected ? preferred : null;
    target?.focus?.({preventScroll: true});
  }

  function finish(entry, restoreFocus = true) {
    const index = stack.indexOf(entry);
    if (index < 0) return;
    stack.splice(index, 1);
    entry.cleanup?.();
    entry.element.removeAttribute('data-modal-covered');
    paint();
    if (restoreFocus) restore(entry);
    if (entry.options.remove) entry.element.remove();
    entry.options.onClose?.(entry.element.returnValue ?? '');
  }

  function dismiss(entry, reason) {
    const handler = entry.options.dismiss;
    if (handler === false) return;
    if (typeof handler === 'function') handler(reason);
    else if (entry.native) entry.element.close();
  }

  function track(element, native, options) {
    const existing = stack.find(entry => entry.element === element);
    if (existing?.options.unmanaged) finish(existing, false);
    else if (existing) {
      existing.options = {...existing.options, ...options};
      return existing;
    }
    const active = document.activeElement;
    const opener = options.opener === undefined ? (active instanceof HTMLElement && active !== document.body && !element.contains(active) ? active : null) : options.opener;
    const entry = {element, native, options, opener, previous: active};
    stack.push(entry);
    return entry;
  }

  /** Shows dialog as a modal on the shared stack. Calling it for an open modal updates its options. */
  function open(dialog, options = {}) {
    if (!dom || !(dialog instanceof HTMLDialogElement)) throw new TypeError('pharosModal.open needs a <dialog>');
    if (!dialog.isConnected) document.body.append(dialog);
    const known = stack.some(entry => entry.element === dialog && !entry.options.unmanaged);
    const entry = track(dialog, true, options);
    if (!known) {
      let pressedOutside = false;
      const press = event => { pressedOutside = event.target === dialog && model.outside(dialog.getBoundingClientRect(), event.clientX, event.clientY); };
      const click = event => {
        // Both ends of the click must be on the backdrop: a text selection
        // dragged out of the dialog does not dismiss it.
        const outside = pressedOutside && event.target === dialog && model.outside(dialog.getBoundingClientRect(), event.clientX, event.clientY);
        pressedOutside = false;
        if (outside && entry.options.backdrop !== false && topEntry() === entry) dismiss(entry, 'backdrop');
      };
      const cancel = event => {
        if (topEntry() !== entry) return;
        event.preventDefault();
        dismiss(entry, 'escape');
      };
      // The engine may still close a dialog itself (a second Escape without
      // user activation in Chromium, or a form method=dialog): finish then too.
      // The close event is queued: ignore it when the dialog has reopened since.
      const close = () => { if (!dialog.open) finish(entry); };
      dialog.addEventListener('pointerdown', press);
      dialog.addEventListener('click', click);
      dialog.addEventListener('cancel', cancel);
      dialog.addEventListener('close', close);
      entry.cleanup = () => {
        dialog.removeEventListener('pointerdown', press);
        dialog.removeEventListener('click', click);
        dialog.removeEventListener('cancel', cancel);
        dialog.removeEventListener('close', close);
      };
    }
    if (!dialog.open) showModal.call(dialog);
    else if (!dialog.matches(':modal')) { dialog.close(); showModal.call(dialog); }
    // Reopening moves the dialog to the top of the stack, as the top layer does.
    stack.splice(stack.indexOf(entry), 1);
    stack.push(entry);
    paint();
    if (!known && options.focus) focusInside(dialog, options.focus);
    return dialog;
  }

  /** Closes a modal opened with open(); same as dialog.close(). */
  function close(dialog, value) { if (dialog?.open) dialog.close(value); }

  /**
   * Registers a non-modal layer (drawer, panel) so Escape reaches it in
   * stacking order: Escape calls options.dismiss('escape'). The owner keeps
   * its own focus handling and calls the returned release() when it closes.
   */
  function layer(element, options = {}) {
    const entry = track(element, false, options);
    stack.splice(stack.indexOf(entry), 1);
    stack.push(entry);
    return () => finish(entry, false);
  }

  /**
   * The topmost modal surface: the newest open modal dialog, or an ARIA modal
   * (a dependency's overlay) shown inside it or, with no dialog open, the page.
   */
  function top() {
    prune();
    const dialog = [...stack].reverse().find(entry => entry.native)?.element ?? null;
    const aria = [...(dialog ?? document).querySelectorAll('[role=dialog][aria-modal=true]')].filter(element => element !== dialog && visible(element));
    return aria.at(-1) ?? dialog;
  }

  /** Where floating UI anchored at anchor belongs: its open dialog, the top modal, or body. */
  function host(anchor) {
    const own = anchor?.closest?.('dialog[open], [role=dialog][aria-modal=true]');
    return own ?? top() ?? document.body;
  }

  // Every modal dialog joins the stack, including those opened by code that
  // predates this file or lives in a dependency: their order decides top().
  const showModal = dom ? HTMLDialogElement.prototype.showModal : null;
  if (dom) HTMLDialogElement.prototype.showModal = function () {
    showModal.call(this);
    if (!stack.some(entry => entry.element === this)) {
      // The browser keeps its own Escape, focus and backdrop behaviour for these.
      const entry = track(this, true, {unmanaged: true, dismiss: false, restoreFocus: false});
      const close = () => { if (!this.open) finish(entry, false); };
      this.addEventListener('close', close);
      entry.cleanup = () => this.removeEventListener('close', close);
      paint();
    }
  };

  // Listen on window so handlers on document and below (popovers, editors,
  // ModalSurface) see the key first and can claim it with preventDefault.
  if (dom) window.addEventListener('keydown', event => {
    if (event.defaultPrevented || event.isComposing) return;
    const entry = topEntry();
    if (!entry) return;
    if (event.key === 'Escape' && !entry.native) {
      // An open auto popover (a menu above the layer) takes Escape first.
      if (entry.options.dismiss === false || document.querySelector('[popover=auto]:popover-open, [popover=""]:popover-open')) return;
      event.preventDefault();
      dismiss(entry, 'escape');
      return;
    }
    if (event.key !== 'Tab' || !entry.native || entry.options.unmanaged || event.ctrlKey || event.metaKey || event.altKey) return;
    const root = tabRoot(entry.element), list = stops(root), active = document.activeElement;
    event.preventDefault();
    let index = list.indexOf(active);
    // From a non-stop inside (a focused heading), go to its neighbouring stop.
    if (index < 0 && active && root.contains(active) && active !== root) {
      const after = list.findIndex(element => active.compareDocumentPosition(element) & Node.DOCUMENT_POSITION_FOLLOWING);
      index = event.shiftKey ? (after < 0 ? list.length : after) : (after < 0 ? list.length : after) - 1;
      if (index < 0 || index >= list.length) index = -1;
    }
    const next = model.tabTarget(list, index, event.shiftKey);
    if (next >= 0) list[next].focus();
    else root.focus();
  });

  /** The standard close button for a modal's header: closes the dialog it is in. */
  function closeButton(label = 'Close') {
    const button = document.createElement('button'), svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg'), use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    button.type = 'button';
    button.className = 'modal-close';
    button.setAttribute('aria-label', label);
    button.title = label;
    svg.setAttribute('class', 'app-icon');
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('aria-hidden', 'true');
    use.setAttribute('href', '#ph-icon-close');
    svg.append(use);
    button.append(svg);
    button.addEventListener('click', () => button.closest('dialog')?.close());
    return button;
  }

  window.pharosModal = {open, close, layer, top, host, closeButton, model};
  if (!dom) return;
  const style = document.createElement('style');
  style.id = 'pharos-modal-style';
  // Zero-specificity defaults: every dialog's own class still sets its size,
  // padding and layout; the frame, scrim and stacking come from here.
  style.textContent = `
:root{--modal-scrim:color-mix(in srgb,#071310 58%,transparent);--modal-radius:10px;--modal-shadow:0 24px 70px #0006;--modal-gap:16px;
--z-panel:8000;--z-drawer:9000;--z-overlay:9500;--z-find:10000;--z-tooltip:10000;--z-feedback:10000}
:where(dialog:modal){box-sizing:border-box;max-width:calc(100vw - 2*var(--modal-gap));max-height:calc(100dvh - 2*var(--modal-gap));border:1px solid var(--line);border-radius:var(--modal-radius);background:var(--panel);color:var(--ink);box-shadow:var(--modal-shadow);overscroll-behavior:contain}
:where(dialog)::backdrop{background:var(--modal-scrim)}
dialog[data-modal-covered]::backdrop{background:transparent;backdrop-filter:none;-webkit-backdrop-filter:none}
:where(.modal-close){display:grid;place-items:center;flex:none;width:32px;height:32px;padding:0;border:1px solid var(--line);border-radius:50%;background:transparent;color:var(--muted);font:inherit;font-size:20px;line-height:1;cursor:pointer}
:where(.modal-close):hover{color:var(--ink);border-color:var(--accent)}
:where(.modal-close) svg{width:16px;height:16px}
@media(prefers-reduced-motion:no-preference){:where(dialog:modal){animation:pharos-modal-in .14s ease-out}}
@keyframes pharos-modal-in{from{opacity:0;transform:translateY(6px) scale(.99)}}`;
  (document.head ?? document.documentElement).append(style);
})();
