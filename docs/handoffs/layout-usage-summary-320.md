# Handoff: fit the Usage summary popover at 320 px

## Assignment

At 320 px the header's Usage summary popover is about 366 px wide. It leaves
the viewport and cuts off its All time column. Make it fit narrow windows.

## Evidence

The structural suite reports it on "Usage summary @ 320", opened from
`#usageSummaryToggle`. The message is "a fixed surface leaves the viewport:
section#usageSummary.usage-summary".

## Where to look

`internal/archive/assets/ui.py`: `#usageSummary` and its `.usage-summary`
styles.

## Done when

The general criteria in [README.md](README.md#layout-and-focus-bugs) apply.
The Usage summary `KNOWN` entry is gone, and every column is readable at 320
px, whether by wrapping, stacking or scrolling inside the popover.
