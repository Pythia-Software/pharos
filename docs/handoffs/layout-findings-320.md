# Handoff: fit Findings at 320 px

## Assignment

Two overflows on Findings at 320 px:

- **The "In prompt" chip.** A finding's "In prompt for …" chip and its Remove from prompt button stay on one line and run past the card and `main`.
- **The detail chart.** The finding detail chart keeps a 320 px minimum width and runs past its panel and `main`.

Both are on the same page, so fix them together.

## Evidence

- **"Findings @ 320".** The message matches ": span.findings-in-cart".
- **"Finding detail @ 320".** The message matches an `svg "0%…"` that spans or runs past the right edge.

## Where to look

- `web/src/findings.css`: `.findings-in-cart` is `inline-flex` with no wrapping, and `.findings-in-cart-label` sets `white-space: nowrap`.
- `web/src/findings.tsx`: the weekly chart's `ResizeObserver` clamps its width with `Math.max(320, …)`. Search for `Math.max(320`. Let it follow its container below 320 px, and check its axis labels still read.
- Rebuild the bundle after the change: `(cd web && npm run build && npm run check-bundle)`.

## Done when

The general criteria in [README.md](README.md#layout-and-focus-bugs) apply.
Both Findings `KNOWN` entries are gone.
