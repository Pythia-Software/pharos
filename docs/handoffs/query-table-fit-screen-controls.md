# Query Table: keep native controls usable when fitting to screen

Pharos consumes published Query Table **0.9.5**, aligned across core, React, UI,
codegen and the Go module. Its dependency policy disallows local library fixes.

## Reproduction

1. Open a query with `fitToScreen: true`, at least six data columns and native
   Detail configured with both `modal` and `expand` modes.
2. Use a phone viewport, such as 390 or 320 pixels wide.
3. The screen-fit algorithm reaches its uniform 48-pixel column floor. After
   cell padding, the Detail column cannot contain both native buttons. The
   second button is partially clipped by the cell. JSON preview buttons can
   also exceed the available inner width; Pharos's fixture exercises the invalid
   JSON preview, which has a roughly 50-pixel button in a 48-pixel column.

This reproduces in Pharos's nine Analyze query tables using the supported
`fitToScreen` preference, with default data columns and native Detail. Pharos's
own drilldown renderers use concise labels and wrap inside narrow cells; the
remaining clipping is in Query Table's native controls.

From the Pharos checkout, use Node 24 and run:

```sh
tools/setup.sh
npm --prefix web run test:browser -- structure --test-name-pattern='Analyze workspace query'
```

The existing layout inspector reports controls mostly clipped by a nonscrolling
ancestor. Cases at 320/390 pixels are explicitly marked todo for this upstream
issue; desktop cases continue to require that screen-fit tables fit their
container, with no clipped actions. The known-issue matcher covers only native
Detail/JSON controls in these phone query cases, not host links or other pages.

## Requested upstream behavior

Keep every enabled native action reachable under screen fitting. Account for
native interactive content and cell padding when determining minimum usable
column widths, or provide a native narrow-column presentation that preserves
all actions. If minimum usable widths exceed the viewport, retain horizontal
scrolling rather than clipping a control inside its own cell. Consider a public
per-field minimum-width contract for host-rendered interactive content too.

Keep `fitToScreen` URL persistence, manual width overrides, ordinary column
resizing and fit-to-content behavior. Avoid host DOM measurement, simulated menu
clicks or application CSS overrides of native control internals.

Cover two or more Detail modes, valid and invalid JSON previews, empty/loading
states and custom cell padding. Assert geometry and action reachability in
browser tests rather than exact pixel widths. Preserve keyboard access and
modal/inline focus return. After publishing an aligned release, Pharos can
upgrade all five pins, regenerate schemas, rebuild its ignored frontend and
remove the scoped known-issue entry.
