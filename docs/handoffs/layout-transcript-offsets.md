# Handoff: show long transcript time offsets at phone widths

## Assignment

At phone widths, an event's time offset that is longer than the transcript's
gutter (for example "+1m03s") is cut off past the turn's left edge. Make
offsets readable at 390 and 320 px.

## Evidence

The structural suite reports it on "Long transcript @ 390" and "@ 320", with
the fixture in `web/tests/browser/fixtures.mjs`. The message is "content
escapes details.turn: text "+1m03s" past the left".

## Where to look

`internal/archive/assets/ui.py`:
- `.timeline-time`, and the 38 px gutter column of `.timeline-entry` in the phone-width styles;
- `relativeTime`, which formats the offset;
- `timelineEntry`, which renders it.

If a shorter format is the right fix, move `relativeTime` into
`internal/archive/assets/reader-model.js` and test it in node beside the
reader's other tests in `web/tests/unit/reader/`.

## Done when

The general criteria in [README.md](README.md#layout-and-focus-bugs) apply.
The transcript-offset `KNOWN` entry is gone.
