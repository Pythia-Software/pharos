# Handoff: fit Carbon Impact's Low/Central/High switch at 320 px

## Assignment

At 320 px one of Carbon Impact's Low/Central/High segmented switches is wider
than its clipped box, and "High" is cut off. Make the switch fit.

## Evidence

The structural suite reports it on "Carbon Impact @ 320". The message is "a
control is cut off: button "High" in div.carbon-segmented".

## Where to look

`internal/archive/assets/carbon.js`: the `.carbon-segmented` control and the
styles it injects. Its arithmetic has node tests
(`web/tests/unit/page/carbon.test.mjs`); keep them passing.

## Done when

The general criteria in [README.md](README.md#layout-and-focus-bugs) apply.
The Carbon `KNOWN` entry is gone, and all three options can be read and
selected at 320 px.
