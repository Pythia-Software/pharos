# Upstream handoff: portable JSON-inspector view state

Pharos consumes all four npm Query Table packages and the Go backend at **0.9.7**. Local dependency patches, vendoring, module replacements, package-cache edits and generated-bundle patches are prohibited. This is an unresolved dependency requirement, **not an approved opt-out** from URL coverage.

## Reproduction

1. Open an item property containing JSON, or the standalone `JsonValueView`.
2. Enter a JSON search, choose Find rather than Filter, expand a nested path or array bucket, change raw/tree mode, show more entries or open/close a JSON cell inspector.
3. Copy the page URL and open it in a fresh browser.
4. The native item identity/property layout restores; these JSON-inspection choices reset.

The published `JsonCellProps` accepts only `value`, `label` and `display`; `JsonValueView` additionally accepts `compact`. Its internal `JsonValueContent` owns `search`, `raw`, `pageSize`, `collapsed`, `expandedBuckets`, `searchMode`, `matchIndex` and `searchOverrides`. There is no controlled state/change API or serialization path for these values. Pharos cannot restore them through a supported host integration.

## Requested public contract

Expose a versioned JSON inspection state, with a controlled value/change API for `JsonValueView`, `JsonCell` and their use inside native item properties. Include inspector open/closed state and applied literal search, filter/find mode, raw/tree mode, collapsed JSONPaths, expanded array-bucket identities, visible-entry limit and deliberately selected match identity. Do not serialize hover, browser focus, copy feedback, DOM measurements or unsubmitted editor state.

Use existing unambiguous JSONPath identities (including dots/quotes/array indices). Scope state to dataset + stable record ID + property field; changing records must not apply another record's expansion. Include this state in the native query/item property snapshot and the TypeScript and Go link codecs. Missing/deleted paths should produce an unavailable-target result without executing a different query.

Expose semantic attention registration/resolution for a row, property and JSONPath, with readiness/materialization hooks. A reveal must be able to open ancestors and scroll/highlight without moving keyboard focus. Avoid requiring a host to inspect dependency DOM or simulate clicks.

## Acceptance tests

- TypeScript ↔ Go codec round trips, including quoted keys, empty search, explicit false and ordered path sets.
- Controlled UI → query URL → fresh browser restoration for every inspection choice.
- Save/Share/snapshot retains definitions without relying on a browser cache.
- Restore stable record/property identities after row reorder and property-layout changes.
- Delayed rendering, missing paths, navigation cancellation, reduced motion, and no repeated scrolling after a background refresh.
- No keyboard-focus movement caused by an attention reveal.

After publishing, pin core, react, ui, codegen and the Go module to the same release. Wire the new state into Pharos's shared contract, add the upstream-owned composite to the control ledger, and remove `viewDependencyGaps.query_table_json` only when fresh-browser coverage passes.
