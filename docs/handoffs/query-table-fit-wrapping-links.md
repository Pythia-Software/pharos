# Query Table: fitting treats a wrapping host link as an unshrinkable control

For the Query Table team. A self-contained report against **0.9.7** (`main`
at `6849399d`, "prepare 0.9.7"), dated **2026-10-28**. It is a regression
from 0.9.6 introduced by the fit-to-screen control minimums (after-0.9.6
requests 5 and 6).

## Problem

`DataTable`'s `measureControlMinimums` gives each column a floor of
`measureControlWidth(cells)`. For any cell containing a control
(`CONTROL_SELECTOR = "a[href], button, [role=button], input, select, textarea"`),
it clones the **whole cell content** into a probe styled
`width:max-content; white-space:nowrap` and measures it. Only `a.qt-link` and
`.qt-json-preview-text` are capped at 96 px.

A host renderer that wraps its text in an ordinary anchor, such as a
conversation title clamped to two wrapping lines, therefore gets a floor equal
to its longest title on one line. When the floors exceed the viewport,
`fitScreenWidths` returns the floors, and the table scrolls horizontally at
every desktop width. In 0.9.6 the same column fitted and its text wrapped.

## Pharos evidence

Pharos's Analyze tables render titles as
`<span class="usage-row-title"><a href="/work/…">long title</a></span>` with
`-webkit-line-clamp: 2; white-space: normal; overflow-wrap: anywhere`. With
0.9.7, the title column's floor is 672 px (the fixture's long title on one
line), the other data columns sit at the 48 px floor, and the fitted table is
1,255 px wide at 1024, 1280 and 1440 px viewports alike. The browser cases
`Analyze workspace query {usage, writing, writing_messages, tool_calls,
skill_usages, conversation_activity} @ {1024, 1280, 1440}` (16 cases) time out
waiting for the fitted table to stop scrolling. They pass on 0.9.6.

## Request

Count only what a control cannot give up. For example:

- measure the controls themselves, not the cell's whole content, and let an
  anchor whose computed `white-space` allows wrapping (or any link) shorten or
  wrap rather than set a nowrap floor; or
- treat every `a[href]` like `a.qt-link`: a floor of at most 96 px, keeping its
  accessible name; or
- let a renderer or `SelectConfig` mark its content as wrapping, so its column
  keeps the 48 px floor.

## Acceptance

- A column whose host renderer wraps long linked text fits a 1024 px viewport
  with several other columns, and its text wraps as in 0.9.6.
- Buttons, Detail and JSON previews keep the 0.9.7 minimums.
- No pixel-exact assertions; check containment and that the table does not
  scroll horizontally when the floors allow it.
