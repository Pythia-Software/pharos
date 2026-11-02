# Query Table: fitted columns clip computed link controls

**Resolved in Query Table 0.9.7**, consumed by Pharos's matching published
core/React/UI/codegen packages and Go module. Fitted group-link and template
links shorten to an ellipsis no narrower than 96 px, keeping their accessible
name and href, and the table scrolls when the minimums exceed the screen. The
`Query ideas` cases in `web/tests/browser/structure.test.mjs` pass again.

The original report below describes the **0.9.5** implementation.

Observed with the published Query Table **0.9.5** core, React, UI and codegen
packages and Go module. Pharos enables the supported `fitToScreen: true`
preference on suggested Query views and computed query-link destinations.
The preference survives URL encoding and restoration, but native group-link
controls can extend past their fitted cells and be clipped.

## Reproduction without personal data

Use a native grouped DataTable fixture with eight or more visible columns:
a text grouping key, numeric SUM/COUNT summaries, and an `op: "link"` summary
at the end. Seed a few records with a shared group key and positive numeric
values. Include the link in the query's selected result columns and set
`fitToScreen: true` on that grouped query. For example, the final summary is:

```json
{
  "id": "records",
  "op": "link",
  "label": "Drill In",
  "link": {
    "label": "Matching records",
    "to": { "kind": "query", "target": "fixture" }
  }
}
```

Register a normal `linkTargets.fixture` route and retain the source filters
and key predicates through `groupLinkQuery`. These query semantics work; the
failure concerns the rendered anchor. Include a second template link labelled
`Open conversation` to exercise direct-source links as well.

Render at desktop, 1024px, 390px and 320px viewport widths. Some desktop tables
also reproduce when enough columns are selected. Wait for fitting and queries
to settle; do not use fixed sleeps. Inspect the computed link anchors' bounds
against their containing `td.qt-cell`, which clips horizontal overflow.

In Pharos, an existing fixture-based reproduction is:

```sh
# Node 24, installed locked dependencies; no real catalog is used.
npm --prefix web run test:browser -- --test-name-pattern='Query ideas'
```

The routes cover the first suggestion and an Explore suggestion for each of
nine datasets at five viewport widths. With fitting enabled, 48 of the 92
browser checks fail; 44 pass. A representative failure is:

```text
a control is cut off where nothing scrolls:
a.qt-link.qt-group-link "Matching records" in td.qt-cell "Matching records"
(36 of 96px shown)
```

`Open conversation` and `Open work` anchors reproduce the same problem.
The same sweep passed before automatic fitting was enabled.

## Ownership and requested behavior

The published UI's `createGroupLinkRenderer` renders the link as a native
inline `a.qt-link.qt-group-link`. Its anchor can be wider than the space left
inside a fitted cell. This occurs with ordinary native computed link summaries,
without a custom link renderer or modified dependency code.

Fix this in upstream Query Table's fitting/link rendering behavior. A fitted
link must keep a usable anchor and understandable label within the cell. For
example, upstream may account for interactive labels when choosing minimum
widths, or provide bounded wrapping/ellipsis with a full accessible label.
If minimum usable widths exceed the viewport, keep the table reachable through
its supported scrolling behavior. Avoid merely hiding the overflowing control.

Preserve source/key filters, URL state, manual resize behavior, modified clicks,
normal href navigation, keyboard activation and focus. Exercise both query and
template links, null/unavailable destinations, short labels and long labels,
and tables reopened from an encoded `fitToScreen: true` query.

Add upstream browser coverage for anchor containment and keyboard/pointer
usability, without asserting exact pixel sizes. Publish a release and consume
matching core/React/UI/codegen npm packages and the Go module in Pharos.
Pharos has no local CSS or renderer workaround for this issue, and its browser
checks remain strict.
