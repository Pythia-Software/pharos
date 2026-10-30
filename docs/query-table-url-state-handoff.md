# Upstream handoff: portable JSON-inspector view state

**0.9.8 integration:** Pharos now consumes core, React, UI, codegen and Go at **0.9.8**. Native property inspectors retain the complete version-1 JSON inspection envelope in query URLs, snapshots and agent links. The semantic registry is connected for exact detail/property/JSONPath reveal with navigation cancellation and focus preservation. Fresh-browser tests cover manual choices, Back/Forward, layout changes, record isolation, delayed reveal and missing paths. The original request below remains as context; the remaining requirements are listed at the end. Local dependency patches, vendoring, module replacements, package-cache edits and generated-bundle patches remain prohibited.

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


## Remaining requirements after 0.9.8

### DataTable JSON display cells still have local ownership

Reproduce in Pharos on Analyze → Query → MCP calls:

1. Select `arguments_json` with `display:{kind:"json"}`.
2. Open its cell inspector and enter a literal search or switch to Find/raw.
3. Copy the address into a fresh context.
4. The inspector and search do not reopen; `itemDetails.jsonInspections` was never updated.

`resolveRenderer`/`renderCellDisplay` creates `JsonCell` for column JSON displays without an inspection scope, controlled state/change callback or attention registry. The native property path supplies those props; the DataTable cell path does not. An override in the renderer registry cannot intercept a column display, which takes precedence. Hosts must not rewrite dependency rendering or manipulate the query definition to work around this.

Expose a supported DataTable integration (or wire it internally) using the native detail controller's schema/typed row ID/field and inspection lifecycle. Keep query display overrides and header editing intact. Duplicate field instances, projected IDs, grouped rows, virtualized cells, false/empty state and disabled detail affordances all need an explicit policy. The same scoped state must survive URL, Save/Share and snapshot round trips. Pharos carries a browser TODO reproducer in the existing `view-state.test.mjs`; this is a dependency blocker, not an opt-out.

### Semantic highlight lifecycle

The public `AttentionRegistry` only resolves one scroll-and-highlight request at a time. It exposes no highlight-only/multiple-target set or clear/replace operation. Property providers add an outline class without clearing previous property marks. JSON providers retain one attention path. A row-only native reveal scrolls its detail surface but does not mark the surface.

Please expose a cancellable semantic highlight set (without scrolling), one reveal landing, and clear/replace semantics, with exact record/property/path identities. Repeated target sets and refreshes must neither scroll nor reopen user-collapsed content; marks must persist or be reapplied without scrolling after a viewer remount. A row detail needs the same distinctive mark as a property/path. Clearing or changing URL instructions must remove obsolete marks through a public API rather than dependency DOM inspection. Retain focus preservation and reduced-motion behavior.

Pharos currently advertises `detail:<json>` as reveal-only, rejects it in `highlight`, themes native reveal marks, and hides them when URL attention is cleared. Existing host multi-highlights are unchanged. `viewDependencyGaps.query_table_attention` records the remaining upstream behavior; `query_table_json_cells` records the display-cell gap.
