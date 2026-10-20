# Handoff: keep the expanded feedback toolbar on screen at 360 px

## Assignment

With the feedback panel open in a 360 px window, the annotation toolbar runs
6 px off the left edge. It is `min(440px, 100vw - 16px)` wide but positioned
22 px from the right. Keep it entirely on screen at any width.

## Evidence

`web/tests/browser/focus.test.mjs`, "the expanded feedback panel stays inside
a 360px viewport", runs as a `todo` and records the toolbar at
`[-6, 184, 338, 398]` in a 360 × 420 window.

## Where to look

`internal/archive/assets/ui.py`: `.feedback-toolbar.expanded`, with its
width and right offset. The toolbar can also be dragged, and its position is
restored on reload. Clamping to the viewport after a resize already works and
is tested ("the annotation toolbar and composer stay inside the viewport
after a resize"), so keep that behaviour.

## Done when

The general criteria in [README.md](README.md#layout-and-focus-bugs) apply.
The `todo` is removed from that focus test.
