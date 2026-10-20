# Handoff: trap focus in the Findings prompt dialog on WebKit

## Assignment

In WebKit, Tab can leave the Findings prompt review dialog and move to the
page behind the modal. WebKit is what the Mac app renders in (WKWebView). Make
focus stay in the dialog until it closes, on WebKit and Chromium.

## Evidence

`web/tests/browser/focus.test.mjs`, "the findings prompt review takes focus,
keeps it and returns it on Escape", runs as a `todo` when
`PHAROS_BROWSER=webkit`. It passes on Chromium.

The cause is that in WebKit Tab skips buttons by default. They are only
focusable by Tab when macOS Full Keyboard Access is on. The dialog's trap
(`Dialog` in `web/src/findings.tsx`) wraps focus only from its first or last
button, so on WebKit the wrap never fires.

The other dialogs the suite checks keep focus on WebKit: quick search, the
card template editor and the delegation phone sheet.

## Where to look

- **The dialog.** `web/src/findings.tsx`: the `Dialog` component, around the findings cart, and its keydown handling.
- **Options.** Use a native `<dialog>` opened with `showModal()`, which makes the rest of the page inert, as the card template editor does. Or make the page behind the dialog `inert` while it is open, and handle Tab without relying on which elements the engine puts in the tab order.

## Done when

- The `todo` is removed, and the focus suite passes with `npm --prefix web run test:browser` and with `PHAROS_BROWSER=webkit` (WebKit installed once with `npx playwright-core install webkit` in `web/`).
- Escape still closes the dialog and returns focus to the button that opened it.
- The bundle is rebuilt.
