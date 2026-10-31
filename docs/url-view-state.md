# URL-addressable views and attention

A copied Pharos URL describes the applied query and presentation, independently of another browser's saved defaults. It restores a moving view, not frozen result data. Settings values, drafts/editing, hover, ordinary keyboard focus, menus and scroll offsets are excluded. Settings section openness is view state. Legacy Analyze/Tools migration remains explicitly deferred.

Query Table **0.9.8** supplies portable native property inspectors. Search, Filter/Find, raw/tree, collapsed JSONPaths, array buckets, visible-entry limit, selected match and open state travel in `query.itemDetails.jsonInspections`, scoped by schema name, typed record ID and property field. Native Save/Share and durable snapshots use the same definition. Fresh-browser tests cover the controls, Back/Forward and record/layout isolation. Table-cell display inspectors and highlight-only/multiple native targets remain [upstream blockers](query-table-url-state-handoff.md), not opt-outs.

## Shared contract

- `web/src/view-contract.ts` defines host fields, URL names, page scope, types/domains, descriptions, target families, opt-outs and dependency gaps.
- `web/src/view-state.ts` supplies `useViewState`, `useViewEntry`, the classic-script bridge and content-disclosure binding. URL values are the source of truth. Back/Forward and in-app navigation use the same reader. Active pages write effective defaults, including false and empty values, so a copied view overrides another browser's preferences. Route-scoped writes are rejected after navigation, even if a component is still mounted. Viewer-dependent disclosure defaults are captured together in one frame; fixed defaults stay implicit and do not accumulate per rendered row. Opt into viewer-dependent capture with `ViewDisclosure captureInitial` or `useViewEntry(..., active, {captureInitial:true})`. Explicitly changed row disclosures remain addressable after paging.
- Existing native query/chart/host-search codecs remain with their owners. `delegatedViewFields` explicitly identifies those owners. Query Table is consumed through published APIs, without local patches.
- `web/src/site-map.ts` exports the contract to `assets/site-map.json`. `site_view.go` reads this same data for validation/building/parsing; it does not maintain a second field catalog.
- `docs/view-controls.json` is the reviewed ledger of actual JSX, every classic script, the static shell and standalone HTML fragments. It includes controls that are buttons or disclosures, not just inputs. `web/view-controls.mjs` discovers these from source.

A new control must bind to a registered field or have an explicit reviewed composite/action/exclusion classification. New or changed raw controls fail `check-view-controls`; `generate-view-controls` writes unknown entries as **unclassified** and fails until reviewed. It never assigns a new raw control the contract of a nearby control automatically. The ledger is a review gate; it does not prove the correctness of a composite's internals. Behavior tests remain necessary, and dependency gaps remain visible. The ledger also records reviewed Query Table release versions; an upgrade fails the gate until its composite controls are audited explicitly.

## Accordion links

Use the existing `disclosures` query parameter for accordion state: a URL-encoded JSON object mapping stable section identities to booleans. `true` opens a section, `false` closes it, and omitted identities use their normal defaults. Multiple sections can open together. This convention supports copied links, reloads and Back/Forward; opening a settings section never changes its saved settings.

The Layout settings identities are `menuBarPreferences`, `headerSettings` and `quickStatsSettings`. For example, the Quick Stats gear opens:

```text
/settings/layout?disclosures=%7B%22quickStatsSettings%22%3Atrue%7D&reveal=section%3AquickStatsSettings
```

`disclosures` opens the content; `reveal=section:<identity>` scrolls to and highlights the destination without moving keyboard focus. Agents use the same convention with `site_link({page:"settings/layout",view:{disclosures:{quickStatsSettings:true}},reveal:"section:quickStatsSettings"})`. The macOS menu section remains unavailable while its menu-bar setting is disabled.

### Adding an accordion

Use `ViewDisclosure` from `web/src/view-disclosure.tsx` for durable React accordions:

```tsx
<ViewDisclosure viewKey="example:help" summary="How this works">
  <p>Section content.</p>
</ViewDisclosure>
```

The component owns the native summary, `disclosures` URL entry and `section:<viewKey>` attention target. A nonempty `viewKey` and summary are required. Repeated content includes its stable entity ID in the key. Use `defaultOpen` only for the fallback when that identity is absent from the URL. Add `captureInitial` if the default depends on a saved preference or viewport. `onOpenChange` runs for deliberate toggles, allowing a saved preference to follow user changes; restoring a URL does not call it. Direct `open` and `onToggle` props are excluded from the public API. `summaryTarget` can add a separate semantic target to the summary without replacing the section target.

Classic HTML opts into the same runtime with `<details data-view-key="example:help">` and a native summary, or a `<button data-view-key="example:help" aria-controls="exampleHelp" aria-expanded="false">` controlling a panel with that ID. Programmatically created classic controls set `element.dataset.viewKey`; custom buttons also set `aria-controls`. Do not add a second click handler or a separate open-state store.

Button disclosures expose their section target on a named section wrapper or the button itself, so it remains available while the panel is closed. The runtime repairs target attributes removed by polling reconciliation on retained nodes without reapplying open state or repeating reveal scrolling.

`check-view-controls` (included in `npm --prefix web run check`) rejects generic durable accordions without a discovered shared binding, even if their ledger entry or `data-view-field` claims `view.disclosures`. React uses the component; classic controls must supply the key and, for buttons, a panel identity. Neither `settings` nor `legacy` can exempt accordion openness. Existing specialized URL codecs and genuine editing/menu exceptions remain individually reviewed in the ledger. Unit tests exercise both accepted and rejected source examples, so weakening the check is visible in test failures.

### Disclosure audit (2026-10-30)

The audit reviewed 178 disclosure/expansion source sites, including 41 previously untracked classic `aria-expanded` writes. It covers native `<details>`, custom expand buttons, React disclosures, and disclosures rebuilt by status polling. Reveal keeps a targeted accordion’s own closed state while opening ancestors that hide the target. Both classic shapes now use the same runtime: native `details[data-view-key]` and `button[data-view-key][aria-controls]`. React content uses `ViewDisclosure`. Each receives a semantic `section:<key>` attention target. Removed nodes cannot write stale toggle events, and same-page URL changes restore already mounted controls. Fixed defaults stay implicit; data-dependent defaults are captured only on the active page. A URL restored into a fresh browser and an absent/empty map restored through history use the same defaults.

| Surface | Portable state |
|---|---|
| Layout settings | `menuBarPreferences`, `headerSettings`, `quickStatsSettings` |
| Capture/index settings | `sync:separate`, `sync:repairs`, `sync:diagnostics`, `sync:forecast:<run ID>` |
| Background schedule items | `background:<item ID>`; several rows may open together |
| Connected cloud schedules | `cloud:schedule:<destination ID>`; IDs survive renaming and reordering |
| Library health and app location | `library:drive-health`, `library:application` |
| Drive-status sections | `overlay=drive` plus `status:cadence:<cadence ID>`, `status:support`, or `library:activity:<activity ID>:<started_at>:details` |
| Carbon breakdowns | Existing `carbon:*` sections plus `carbon:row:<tier>:<category>`; restored evidence loads its content |
| Query ideas, layouts and retained row payloads | `query:<dataset>:ideas`, `query:<dataset>:layout`, and `row:<dataset>:<row ID>:<field>` |
| Workspace and conversation content | `work:<work ID>:archive`, `work:<work ID>:files`; the reader's existing `reader_expanded`, `reader_collapsed` and `reader_content` codecs cover turns, source details, tools, structured payloads and prose |
| Other host content | `machine:methodology`, `authored:message:<message ID>`, `feedback:logs:<submission ID>`, and the existing Findings/Delegation disclosures |
| Sidebar branches and Parallelism conversation list | Existing `nav_expanded` and `analyze_parallelism.showConversations` owners remain authoritative |

Draft controls are explicitly classified as editing: source/library onboarding; create/open/restore/copy/move library forms; the unapplied cloud setup/download wizard; and editable feedback attachments/Ask activity within the chat editor. Their drafts and action steps are not reconstructed from a disclosure URL. Transient pickers, tooltips, overflow menus and chart popovers retain their explicit transient classification. Query Table owns its published native state; its existing documented dependency gaps remain blockers.

`check-view-controls` now rejects native accordions, summaries, and `aria-expanded` controls classified only as `settings`. JSX expansion attributes and classic `setAttribute("aria-expanded", ...)` sites are included in the inventory. Static HTML `data-view-field` bindings are discovered directly, so settings values cannot accidentally hide a missing view binding in the review ledger. New controls still require individual review.

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

Home card links open full dashboards directly with `analyze_chart` and `analyze_filters`. Collected-card source queries and cover metadata remain in storage for previews and editing, and are removed from navigation links. There is no expanded-card page. Dashboard Today links may pin an IANA zone with `analyze_timezone`. **Create card** saves that canonical dashboard URL to the Home collection, including chart, filters, Today/timezone, Carbon assumptions and Parallelism counting/gap settings. Card names/descriptions and the creation dialog are authoring data/drafts; they do not add view parameters. Custom card previews resolve the saved URL and run the dashboard’s analysis/trend models again.

Analyze query pages accept `options.return_topic` (`analyze_topic`) independently of their dataset so a supporting query returns to the dashboard that opened it.

Analysis source-query links also retain the dashboard chart/population and opened-query baseline. These are documented view fields so returning to a dashboard can reconcile user edits without dropping relative time intent. Specialized time companions (`window_mode/days/field/until_field/dataset/timezone`) remain discoverable and portable.

`<page>/query` also publishes `schema_name`, an inspection template, and a `properties_path` to the paginated property catalogue. Use that schema name (for example `pharos_mcp_calls`) in each inspection scope. `JsonInspectionState`, `JsonMatchIdentity` and related types are generated from the installed core declarations. Returning a native `JsonValueView` directly from a property renderer lets Query Table bind its controlled state and semantic registry; wrapping that element in a host div hides it from the supported integration.

## Attention

`highlight` is repeatable; `reveal` names one landing target. Reveal means **scroll and distinctive persistent highlighting**, without moving browser/keyboard focus. Ordinary tabbing and scrolling never change these parameters. Clear highlights removes the instructions.

Targets include the page, controls, named sections, card definition IDs, message/tool-call IDs, session IDs, records in the current result page, and literal text. IDs refer to data/semantic identity rather than DOM order. A row target highlights its actual rendered row through public table extension points. It does not open a modal or broaden a query.

`text:<literal>` restores a prefilled Find box. Repeated literal targets are highlighted together; the reader represents multiple literals as an escaped OR search. The conversation reader uses its existing search API to find hidden/unmaterialized messages and reveal a match; other pages use a literal page Find with previous/next navigation. No token IDs are created. Message reveal materializes its owning turn and opens ancestors. Content expansions are retained as view state.

`reveal=detail:{"rowId":"id","field":"arguments_json","path":"$[\"events\"][41][\"text\"]"}` invokes the published semantic registry. The page selects the dataset; preserve numeric versus string IDs. Field and path are optional. The target loads exact details and materializes a selected property/JSONPath without changing the population, filters, sort or pagination. Missing records/properties/paths report unavailable. Reveal waits for restoration and the initial row execution, so a temporary fallback cannot consume the landing. Navigation aborts pending work; completed landings survive temporary provider withdrawal, so background refresh does not repeat scrolling. This family is reveal-only because the upstream registry has no highlight-only or multi-target API. Native marks use Pharos reveal styling and Clear highlights hides them.

Components register target resolvers with `window.pharosView.register`. Rendering can expose `data-pharos-target`, `data-view-field` or an existing semantic identity attribute. Registration and DOM mutation signals handle delayed content. Navigation cancels stale scheduled landings. A background refresh reapplies highlighting without repeatedly scrolling. Targets unavailable under the current tab/query are reported and never silently widen its data scope.

## Time intent

Native elapsed predicates (`-7d`, `+0s`) remain relative in definitions, URLs, snapshots and agent arguments. Search keeps its duration descriptor; execution resolves it once for a cursor batch and renews it on a new open/refresh. Fixed custom bounds remain fixed.

Today is represented explicitly and re-resolved at execution boundaries. Omitted timezone follows the viewer; a supplied IANA timezone stays pinned. Dashboard-to-query navigation preserves separate lower/upper interval-overlap fields. Long range controls are labeled **180 days** and **365 days**, accurately reflecting their elapsed-day definitions rather than promising calendar months/years.

TL1 attempts remain available through the Analyze query dataset; the retired TL1 report pages and their view fields are removed. Session pagination and conversation Find batches store a page number and rebuild current server cursors when opened; expired cursor blobs are not used as identities.

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
