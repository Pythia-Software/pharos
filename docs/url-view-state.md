# URL-addressable views and attention

A copied Pharos URL describes the applied query and presentation, independently of another browser's saved defaults. It restores a moving view, not frozen result data. Settings, drafts/editing, hover, ordinary keyboard focus, menus and scroll offsets are excluded. Legacy Analyze/Tools migration remains explicitly deferred.

**Coverage is incomplete for upstream JSON-inspector controls.** Query Table 0.9.7 does not expose controlled JSON search/mode/path state. This is recorded as a dependency gap, not an opt-out. See [the upstream handoff](query-table-url-state-handoff.md). Do not remove that gap until a published release supplies the API and fresh-browser restoration passes.

## Shared contract

- `web/src/view-contract.ts` defines host fields, URL names, page scope, types/domains, descriptions, target families, opt-outs and dependency gaps.
- `web/src/view-state.ts` supplies `useViewState`, `useViewEntry`, the classic-script bridge and content-disclosure binding. URL values are the source of truth. Back/Forward and in-app navigation use the same reader. Active pages write effective defaults, including false and empty values, so a copied view overrides another browser's preferences. Route-scoped writes are rejected after navigation, even if a component is still mounted. Viewer-dependent disclosure defaults are captured together in one frame; fixed defaults stay implicit and do not accumulate per rendered row. Opt into viewer-dependent capture with `ViewDisclosure captureInitial` or `useViewEntry(..., active, {captureInitial:true})`. Explicitly changed row disclosures remain addressable after paging.
- Existing native query/chart/host-search codecs remain with their owners. `delegatedViewFields` explicitly identifies those owners. Query Table is consumed through published APIs, without local patches.
- `web/src/site-map.ts` exports the contract to `assets/site-map.json`. `site_view.go` reads this same data for validation/building/parsing; it does not maintain a second field catalog.
- `docs/view-controls.json` is the reviewed ledger of actual JSX, every classic script, the static shell and standalone HTML fragments. It includes controls that are buttons or disclosures, not just inputs. `web/view-controls.mjs` discovers these from source.

A new control must bind to a registered field or have an explicit reviewed composite/action/exclusion classification. New or changed raw controls fail `check-view-controls`; `generate-view-controls` writes unknown entries as **unclassified** and fails until reviewed. It never assigns a new raw control the contract of a nearby control automatically. The ledger is a review gate; it does not prove the correctness of a composite's internals. Behavior tests remain necessary, and dependency gaps remain visible. The ledger also records reviewed Query Table release versions; an upgrade fails the gate until its composite controls are audited explicitly.

## Agent interface

Ask Pharos uses the existing MCP CLI-style interface:

- `browse_site({path:"<page>/view"})` lists host view fields, their types, URL parameters and descriptions, plus dependency gaps. `<page>/view/<field>` includes structured templates and domains for complex fields.
- `browse_site({path:"<query-page>/query"})` describes the full native query state and available schema columns. `<page>/query/types/<Type>` provides the installed public type definition and its nested references, generated transitively from `QueryState`.
- `browse_site({path:"<page>/targets"})` lists attention targets and dynamic identity families.
- `site_link` accepts the existing `page`, `card`, `options`, `filters`, `days`, or a full native `query`, plus `view`, `highlight` and `reveal`.
- `site_link({url})` reads the UI's links back into canonical arguments, including full queries, advanced applied searches and library-backed snapshots. Both `/work/{id}?conversation=...` and `/work/{id}/conversation/{conversation}` are accepted.

For example:

```json
{
  "page": "cards",
  "view": {"card_window": "30", "card_filters": true},
  "highlight": ["card:tool-failures"],
  "reveal": "card:tool-failures"
}
```

A native `query` includes its filters/time predicates; give it without `filters`, `days` or `card`. It carries ordered columns/widths, sort, grouping, summaries, metrics, page size/offset, item properties and host search. Host-state parameter mirrors must agree. Advanced Search uses `view.search_state` with a complete `TextSearchDraft`; Analyze record search uses `view.record_keyword`.

Analysis source-query links also retain the dashboard chart/population and opened-query baseline. These are documented view fields so returning to a dashboard can reconcile user edits without dropping relative time intent. Specialized time companions (`window_mode/days/field/until_field/dataset/timezone`) remain discoverable and portable.

## Attention

`highlight` is repeatable; `reveal` names one landing target. Reveal means **scroll and distinctive persistent highlighting**, without moving browser/keyboard focus. Ordinary tabbing and scrolling never change these parameters. Clear highlights removes the instructions.

Targets include the page, controls, named sections, card definition IDs, message/tool-call IDs, session IDs, records in the current result page, and literal text. IDs refer to data/semantic identity rather than DOM order. A row target highlights its actual rendered row through public table extension points. It does not open a modal or broaden a query.

`text:<literal>` restores a prefilled Find box. Repeated literal targets are highlighted together; the reader represents multiple literals as an escaped OR search. The conversation reader uses its existing search API to find hidden/unmaterialized messages and reveal a match; other pages use a literal page Find with previous/next navigation. No token IDs are created. Message reveal materializes its owning turn and opens ancestors. Content expansions are retained as view state.

Components register target resolvers with `window.pharosView.register`. Rendering can expose `data-pharos-target`, `data-view-field` or an existing semantic identity attribute. Registration and DOM mutation signals handle delayed content. Navigation cancels stale scheduled landings. A background refresh reapplies highlighting without repeatedly scrolling. Targets unavailable under the current tab/query are reported and never silently widen its data scope.

## Time intent

Native elapsed predicates (`-7d`, `+0s`) remain relative in definitions, URLs, snapshots and agent arguments. Search keeps its duration descriptor; execution resolves it once for a cursor batch and renews it on a new open/refresh. Fixed custom bounds remain fixed.

Today is represented explicitly and re-resolved at execution boundaries. Omitted timezone follows the viewer; a supplied IANA timezone stays pinned. Dashboard-to-query navigation preserves separate lower/upper interval-overlap fields. Long range controls are labeled **180 days** and **365 days**, accurately reflecting their elapsed-day definitions rather than promising calendar months/years.

TL1's latest enqueue remains a selector, days remain relative, and a selected enqueue/segment retains its event timestamp and segmentation choice. Session pagination and conversation Find batches store a page number and rebuild current server cursors when opened; expired cursor blobs are not used as identities.

Carbon and Parallelism assumptions are view-scoped overrides. Request parameters carry them to rows, metrics and item details. Restoring a URL never saves preferences. Deliberate changes to counting assumptions, Carbon settings or Findings ranking also update the corresponding library defaults; other linked overrides are not copied into those defaults. Existing explicit URLs keep their chosen values, while new unlinked views use the saved defaults.

## Portable large definitions

Automatic native synchronization keeps the definition inline until the library's immutable snapshot store confirms Save. It then replaces the query token with `@code`, guarded against a newer query/navigation. A failed/quota-limited save leaves the inline URL usable. Another browser resolves the reference from the same library, without the original browser's cache.

The endpoint and retention/quotas are the existing `/api/query-snapshots` and library snapshot table. Agent link construction and Ask destination validation are read-only; they resolve existing references from that store but leave new definitions inline. Canonicalization retains an already-resolved immutable reference rather than creating another definition. Relative definitions stay relative; snapshots do not freeze data rows.

Bookmark identity and saved bookmark URLs exclude `overlay`, `history_search`, and `history_collapsed`. These remain addressable in copied page URLs, but drawer activity does not unbookmark a page or make its bookmark reopen a drawer.

## Maintaining coverage

1. Add a field to `viewFields`, including its page scope and validation/domain.
2. Bind React state with `useViewState` or repeated content with `useViewEntry` keyed by a stable entity. Classic scripts use `window.pharosView.read/write`. Use `ViewDisclosure` for content; classic content can use `data-view-key`.
3. Bind a semantic attention identity. Parameterized targets should use stable record IDs; arbitrary CSS selectors are never a public URL language.
4. Run `npm --prefix web run generate-view-controls`. Review/classify every unknown entry; never label a view input a setting merely to pass the check.
5. Run `npm --prefix web run generate-site-map`; help/parameter documentation changes travel with the generated manifest.
6. Cover parsing/building/time transitions in node/Go. Use browser tests for real restoration, materialization, scrolling/focus and a fresh context without a local snapshot cache.

`npm --prefix web run check` includes the source-control review gate, manifest freshness, type checks and unit tests. Do not run a frontend bundle build concurrently with Go compilation: Go embeds the generated bundles.
