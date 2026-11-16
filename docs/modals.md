# Modals, drawers and the layer stack

Every surface that sits over the page shares one stack, kept by
`internal/archive/assets/modal.js` (`window.pharosModal`). It is a classic
script loaded early by the page shell (`ui.py`) and the setup window
(`application.html`), and each React bundle imports it through
`web/src/modal.tsx`. A guard keeps one stack per page.

## What the stack does

- **One owner for Escape, Tab and backdrop clicks.** Only the topmost surface
  responds. A dialog opened over another one closes first, then the one below
  it. A dialog opened over a drawer closes before the drawer.
- **Tab stays inside the top modal.** Focus moves between its controls and
  wraps at either end. Pharos moves focus itself rather than leaving it to the
  engine, because WebKit skips buttons unless Full Keyboard Access is on.
- **Focus returns to the opener.** By default the opener is whatever had focus
  when the modal opened. Pass `opener` to name a different control, or
  `restoreFocus` with a function to choose a fallback.
- **One scrim.** Only the top dialog paints `::backdrop`. Dialogs beneath it
  get `data-modal-covered`, so stacked dialogs dim the page once.
- **Backdrop clicks are deliberate.** The press and the release must both land
  outside the dialog's box. A text selection dragged out of the dialog does
  not dismiss it.
- **Shared frame.** Every `dialog:modal` gets the same border, radius, panel
  colour, shadow, maximum size and `--modal-scrim`, through zero-specificity
  rules. A dialog's own class sets only its width, padding and layout.
  Use `.modal-close` (`ModalClose` in React, `pharosModal.closeButton()` in
  classic code) for the header's close button.

Modal dialogs are native `<dialog>`s shown with `showModal()`, so they sit in
the browser's top layer above every z-index. Non-modal layers (the History
and Bookmarks drawers, the feedback composer) join the stack only so that
Escape reaches them in order. They keep their own focus handling.

Dialogs that other code opens with `showModal()` directly, including dialogs
from dependencies, still join the stack: `modal.js` wraps
`HTMLDialogElement.prototype.showModal`. Their order then decides `top()`,
but they keep the browser's own Escape, focus and backdrop behaviour.

## Using it

React: use `Modal` from `web/src/modal.tsx`. A mounted `Modal` is shown, or
you can control it with `open`. Do not call `showModal()`, add backdrop click
handlers or write a focus trap.

```tsx
<Modal className="my-dialog" aria-labelledby={titleId} onClose={close} initialFocus="h2">
  <header><h2 id={titleId}>Title</h2><ModalClose label="Close details" onClick={close} /></header>
  …
</Modal>
```

The props that change behaviour:

- `onDismiss(reason)` runs on Escape or a backdrop click. By default the
  dialog closes.
- `dismissible={false}` turns Escape and the backdrop off while a save is in
  flight.
- `backdropDismiss={false}` stops a backdrop click from closing the dialog.
  Use it for forms whose draft a stray click would lose.
- `onClose` runs after the dialog closes for any reason, but not when the
  component unmounts while the dialog is open.

Classic scripts call `pharosModal.open(dialog, options)`:

```js
pharosModal.open(dialog, {opener: trigger, focus: close, remove: true});
```

The options:

- `opener`
- `focus`: an element or selector
- `dismiss(reason)`, or `false`
- `backdrop: false`
- `restoreFocus`: `false` or a function
- `onClose`
- `remove`: removes the dialog on close

To close the dialog, call `dialog.close()`.

A drawer or panel registers itself while it is open:

```js
const release = pharosModal.layer(drawer, {dismiss: () => closeDrawer()});
// …and calls release() when it closes.
```

Floating UI should be appended to `pharosModal.host(anchor)` (`modalHost` in
React). That is the anchor's open dialog, else the top modal, else `body`.
Find and attention scope themselves to `pharosModal.top()` (`topModal`).

## Layers and z-index

Non-modal layers stack with the tokens defined in `modal.js`:

| Token | Value | Used by |
|---|---|---|
| `--z-panel` | 8000 | fixed panels: Quick Stats, the drive panel |
| `--z-drawer` | 9000 | the History and Bookmarks drawers, attention status |
| `--z-overlay` | 9500 | toasts over the page |
| `--z-find`, `--z-tooltip` | 10000 | Find, chart tooltips |
| `--z-feedback` | 10000 | the feedback toolbar; its parts add small offsets |

Modal dialogs need no z-index, because the top layer is above all of these.

## Not on the shared frame yet

- `ModalSurface` from `@pythia-software/query-table-ui` keeps its own stack,
  focus trap and body scroll lock. Under the Query Table dependency policy it
  is not patched here. `top()` and Tab handling account for it when it is
  open inside a dialog. Sharing one stack needs an upstream change.
- Ask Pharos (`ask-pharos.js`) has its own dialogs and its own modal helper.
  They join the stack through the `showModal` wrapper and use
  `--modal-scrim`, but keep their own frame and focus handling.
- The setup window and onboarding keep their full-window blurred backdrop.
- These surfaces still do their own positioning and Escape handling: the
  status and drive panels, Quick Stats and the phone workspace sheet.
