# Handoff: fit the Human Words size-profile cover's rows

## Assignment

The Human Words size-profile card cover's six rows are taller than the cover
and run about 4 px past its bottom. Make the rows fit inside the cover.

## Evidence

The structural suite reports it on the Dashboard Catalog
(`/home?cards=library`) at 1440, 390 and 320 px. The message matches
"content escapes span.human-study-cover.is-profile: span.human-study-rows
past the bottom".

## Where to look

`web/src/human-study.css`: `.human-study-rows` is a flex column with a
`9px` gap and `space-evenly`. The cover is in `web/src/human-study-cover.tsx`.
Rebuild the bundle after the change.

## Done when

The general criteria in [README.md](README.md#layout-and-focus-bugs) apply.
The Human Words cover `KNOWN` entry is gone.
