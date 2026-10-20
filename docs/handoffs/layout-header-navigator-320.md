# Handoff: fit the header at 320 px with the navigation bar open

## Assignment

At 320 px, with the optional navigation bar open, the header's rows overflow
their clipped box and the header scrolls sideways. That cuts off the Back
button, the brand and the Home tab. Make the header fit at 320 px with the
bar open.

## Evidence

The structural suite reports it on the Bookmarks and History drawers at 320
px, which open the navigation bar (preference `pharos-show-nav-button`). The
message is "a control is cut off: button#navBack.header-icon in header".

## Where to look

`internal/archive/assets/ui.py`: the `header` markup and styles, and the
`.navigator` bar (`#navigatorToggle`, `#navBack`, `#bookmarksToggle`,
`#historyToggle`).

## Done when

The general criteria in [README.md](README.md#layout-and-focus-bugs) apply.
The header `KNOWN` entry is gone, and every header control can still be
reached at 320 px.
