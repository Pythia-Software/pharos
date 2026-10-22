# Query Table: let filter value menus grow beyond their inputs

**Resolved in Query Table 0.9.5**, consumed by Pharos's matching published
core/React/UI/codegen packages and Go module. The release replaces the native
datalist with a content-sized, viewport-bounded combobox menu. Browser coverage
in `web/tests/browser/structure.test.mjs` checks long suggestions and focus at
desktop, 390px and 320px widths.

The original upstream assignment below is dated **2026-10-21** and describes
the affected **0.9.3/0.9.4** implementation.

## Problem and requested behavior

In Pharos's Search page (`/library`), add a **Repository** WHERE filter and
open the empty value input's suggestions. The macOS screenshot supplied with
the report shows a menu about as narrow as the compact input. Repository names
are cut down to prefixes such as `explo`, `explo-` and `explo-v…`, so choices
with similar names cannot be distinguished.

Keep the filter row compact, but let its open menu be substantially wider and
taller. Size the menu for its contents, bounded by the available viewport, with
scrolling for a long list. Full names must be readable; wrap unusually long
values rather than clipping them to the input's width. Preserve typing arbitrary
values as well as selecting suggestions. Apply this to the shared filter value
editor wherever it is used, including mobile filter dialogs.

## Confirmed ownership and cause

The host baseline is Pharos commit
[`b3f53853ff2b78c778835275e592828278e2c5bb`](https://github.com/Pythia-Software/pharos/tree/b3f53853ff2b78c778835275e592828278e2c5bb).
Its `schemas/library.schema.json` defines `repository_name` as a text field
labelled Repository. The host's `web/src/index.tsx` supplies distinct values
through the normal transport, using
`GET /api/query/library/distinct?field=repository_name&q=...&limit=...`.

In the released UI's
[`packages/ui/src/QueryBuilder.tsx`](https://github.com/Pythia-Software/query-table/blob/v0.9.4/packages/ui/src/QueryBuilder.tsx#L2223),
`ValueInput` routes distinct suggestions to `AutocompleteInput`, which renders
an `<input list={listId}>` and a native `<datalist>`. Its input width comes from
`chipFieldWidthForInput(value || "value", field.type, 5, 4)`: an empty text
value gets **10ch**, independent of the suggestion lengths. The same path is
present in the installed 0.9.3 UI package.

The screenshot is consistent with the browser sizing its native datalist popup
from this narrow input. Native popup geometry is browser-controlled; changing
the host's table-column widths does not give the suggestions a separate width.
Resolve this in the shared upstream editor, using the existing accessible
picker/overlay infrastructure where appropriate. Simply widening every closed
input would miss the requested compact-row/large-menu behavior.

## Portable reproduction

No Pharos catalog or private data is needed. In an upstream QueryBuilder
fixture, use a text field named `repository_name`, labelled `Repository`, with
`filter.values.source: "distinct"`. Start with this WHERE term:

```json
{"field":"repository_name","op":"=","value":""}
```

Have the fixture's `api.filterValues("repository_name", query)` return matching
values from this small synthetic list, with `hasMore: false`:

```json
[
  "exploration",
  "exploration-service",
  "exploration-service-v2",
  "gbw-utilities",
  "general-purpose-repository",
  "go-query-table-integration-with-a-long-unbroken-suffix-abcdefghijklmnopqrstuvwxyz"
]
```

Focus the value input and open suggestions using both pointer and keyboard.
Check Safari/WebKit on macOS, where the report originated, and Chromium at a
desktop width and at 390px/320px. Also check a filter near the viewport's right
and bottom edges and inside a scrollable filter dialog.

## Acceptance and coverage

- The menu can extend beyond its trigger so all three `exploration` values
  are distinguishable. Long values remain readable, with no page overflow.
  The list scrolls inside the available viewport and stays attached to its
  input when the page or enclosing dialog scrolls or resizes.
- Keyboard users can open, move through and select suggestions, dismiss with
  Escape, and leave with Tab without trapping focus or accidentally choosing
  a value. Pointer selection commits the complete value and preserves focus
  appropriately. Expose combobox/listbox state and the active option to
  assistive technology if replacing the native datalist.
- Freeform editing, clearing, loading, no matches, failed requests and the
  `hasMore` refinement hint keep working. A late result for an earlier input,
  field or filter context must not replace current suggestions. Preserve the
  existing debounce and stale-response protection.
- Keep query values and transport semantics unchanged. Cover scalar equality,
  array autocomplete and any other operators routed through the shared editor;
  retain existing static-option and set-filter behavior.
- Put filtering, selection and asynchronous state tests in existing upstream
  unit suites. Browser tests should verify readable/unclipped options,
  viewport containment and keyboard/focus behavior, without asserting exact
  pixels, CSS values or DOM structure. Manually verify native macOS Safari
  behavior too; a DOM-only test cannot establish native popup geometry.

## Original release and Pharos acceptance requirements

Publish the fix in an aligned Query Table npm/Go release. Pharos's `AGENTS.md`
requires upstream fixes consumed through published releases and prohibits local
Query Table patches, installed-package edits and generated-bundle workarounds.

Once released, pin all four npm packages and the Go module to that same
version, regenerate schemas, rebuild the ignored frontend and verify the menu
with the fixture above. Run Pharos's `go test ./...`,
`npm --prefix web run check`, `npm --prefix web run test:full`, and the relevant
browser suite in Chromium and WebKit before opening the integration PR.
Do not commit generated JS/CSS bundles.
