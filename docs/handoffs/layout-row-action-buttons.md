# Handoff: stop cutting off table row action buttons

## Assignment

The trailing action buttons in Query Table rows are wider than their column
and are clipped mid-word ("Detai…"), even at 1440 px. The buttons are
Details, Tool call, Evidence and Messages. Make the buttons fit their column
at every width.

## Evidence

The structural suite reports it on Tools (Tool calls), Skills, MCP and Human
Words messages, at 1440, 390 and 320 px. The message is "a control is cut
off: button.mcp-detail-button … in td.qt-cell.qt-trailing-cell".

## Where to look

- **The trailing column.** `web/src/index.tsx` passes `trailing` and `trailingLabel` to `QuerySurface`. Search for `trailing:`. The per-dataset buttons are rendered by the functions it passes.
- **Width.** The column's width comes from Query Table's trailing-cell layout. Size the column to its content, make the control compact (for example an icon button with an accessible label), or both. Keep the action's accessible name.

## Done when

The general criteria in [README.md](README.md#layout-and-focus-bugs) apply.
The trailing-button `KNOWN` entry is gone.
