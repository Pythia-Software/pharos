# Handoff: keep screen-reader-only labels inside `main`

## Assignment

Several pages grow taller than the window because labels meant only for
screen readers escape `main`. Once the document scrolls, `scrollIntoView`
scrolls the whole app, header included. Contain those labels so the document
never scrolls and only `main` does.

## Evidence

The structural suite reports this on:
- TL1 runs, Usage tokens, Human Words, Human Words messages and Usage conversations, at 390 and 320 px;
- Capture & index, at 1440, 390 and 320 px.

The message it matches is "…so it can scroll instead of main, reaching past it: span.qt-sr-only / span.sr-only / caption.sync-sr-only". The labels are `position: absolute` with no positioned ancestor inside `main`, so `main`'s clipping does not apply to them.

## Where to look

- **`.qt-sr-only`.** Query Table's own stylesheet defines it (`web/node_modules/@pythia-software/query-table-ui/dist/theme.css`). Fix it with a Pharos rule, or by giving the table container a containing block. If the right fix is upstream, also add it as a request to `docs/query-table-next-release-handoff.md`.
- **`.sr-only`.** The rule that reaches these pages is probably Query Table's own; the Pharos rule in `web/src/text-search-editor.css` applies only inside the search editor.
- **`.sync-sr-only`.** Defined in `internal/archive/assets/sync.js` (the injected styles, and the table captions it adds).
- **The shell.** `main` and its containers live in `internal/archive/assets/ui.py`.

## Done when

The general criteria in [README.md](README.md#layout-and-focus-bugs) apply. Also:
- the screen-reader `KNOWN` entry is gone from `web/tests/browser/structure.test.mjs`;
- the labels are still exposed to assistive technology; don't use `display: none`.
