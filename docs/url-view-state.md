# URL-addressable views and attention

A copied Pharos URL describes the applied query and presentation, independently of another browser's saved defaults. It restores a moving view, not frozen result data. Settings values, drafts/editing, hover, ordinary keyboard focus, menus and scroll offsets are excluded. Settings section openness is view state. Legacy Analyze/Tools migration remains explicitly deferred.

Query Table **0.9.8** supplies portable native property inspectors. Search, Filter/Find, raw/tree, collapsed JSONPaths, array buckets, visible-entry limit, selected match and open state travel in `query.itemDetails.jsonInspections`, scoped by schema name, typed record ID and property field. Native Save/Share and durable snapshots use the same definition. Fresh-browser tests cover the controls, Back/Forward and record/layout isolation. Table-cell display inspectors and highlight-only/multiple native targets remain [upstream blockers](query-table-url-state-handoff.md), not opt-outs.

## Shared contract

- `web/src/view-contract.ts` defines host fields, URL names, page scope, types/domains, descriptions, target families, opt-outs and dependency gaps.
- `web/src/view-state.ts` supplies `useViewState`, `useViewEntry`, the classic-script bridge and content-disclosure binding. URL values are the source of truth. Back/Forward and in-app navigation use the same reader. Active pages write effective defaults, including false and empty values, so a copied view overrides another browser's preferences. Route-scoped writes are rejected after navigation, even if a component is still mounted. Viewer-dependent disclosure defaults are captured together in one frame; fixed defaults stay implicit and do not accumulate per rendered row. Opt into viewer-dependent capture with `ViewDisclosure captureInitial` or `useViewEntry(..., active, {captureInitial:true})`. Explicitly changed row disclosures remain addressable after paging.
- Query Table writes each table edit to `q_<dataset>` with `replaceState`. A table can opt into history steps with `QuerySurface queryHistory` (`web/src/query-history.ts`): each change to filters, sorts, grouping, metrics, columns or paging becomes its own Back/Forward entry, rapid edits within a second coalesce into one, and column widths never count. Restorations from Back/Forward or in-app links are recorded into their entry, never pushed over it. Only the Risky actions table opts in.
- Existing native query/chart/host-search codecs remain with their owners. `delegatedViewFields` explicitly identifies those owners. Query Table is consumed through published APIs, without local patches.
- Background processing tables use native `q_<schema>` query parameters (for example, `q_sync_operations`) and `query_layout` entries keyed by schema name. Filters, sorts, columns, grouping, metrics and paging restore independently for each retained snapshot; the query editor starts expanded.
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
| Capture/index settings | `sync:history`, `sync:sources`, `sync:repairs`, `sync:diagnostics`, `sync:forecast:<run ID>` |
| Background schedule items | `background:<item ID>`; several rows may open together. The Work card's finished requests and export use `background:work:details` |
| Connected cloud schedules | `cloud:schedule:<destination ID>`; IDs survive renaming and reordering |
| Library health and app location | `library:drive-health`, `library:application` |
| Drive-status sections | `overlay=drive` plus `status:cadence:<cadence ID>`, `status:support`, or `library:activity:<activity ID>:<started_at>:details` |
| Carbon breakdowns | Existing `carbon:*` sections plus `carbon:row:<tier>:<category>`; restored evidence loads its content |
| Query ideas, layouts and retained row payloads | `query:<dataset>:ideas`, `query:<dataset>:layout`, and `row:<dataset>:<row ID>:<field>` |
| Workspace and conversation content | `work:<work ID>:archive`, `work:<work ID>:files`; the reader's existing `reader_expanded`, `reader_collapsed` and `reader_content` codecs cover turns, source details, tools, structured payloads and prose |
| Other host content | `machine:methodology`, `authored:message:<message ID>`, `feedback:logs:<submission ID>`, and the existing Findings/Delegation disclosures |
| Sidebar branches and Parallelism conversation list | Existing `nav_expanded` and `analyze_parallelism.showConversations` owners remain authoritative. Top-level navigation groups default open; deeper groups default open along the current page's path. Explicit branch choices override those defaults. |

Draft controls are explicitly classified as editing: source/library onboarding; create/open/restore/copy/move library forms; the unapplied cloud setup/download wizard; and editable feedback attachments/Ask activity within the chat editor. Their drafts and action steps are not reconstructed from a disclosure URL. Transient pickers, tooltips, overflow menus and chart popovers retain their explicit transient classification. Query Table owns its published native state; its existing documented dependency gaps remain blockers.

`check-view-controls` now rejects native accordions, summaries, and `aria-expanded` controls classified only as `settings`. JSX expansion attributes and classic `setAttribute("aria-expanded", ...)` sites are included in the inventory. Static HTML `data-view-field` bindings are discovered directly, so settings values cannot accidentally hide a missing view binding in the review ledger. New controls still require individual review.

## Timeline Key

The timeline Key uses the dashboard legend menu: show only a value or category,
add/remove values, show everything except a value or category, or reset that
section. Hidden values stay in the Key with muted, desaturated swatches so they
can be restored. Sender, Receiver and message-mode families apply to all of
their displayed labels, with mixed selection indicated on the parent.

`timeline_visibility` stores independent `modes`, `events`, `tools` and `origins`
selections. Each optional section has `{values: string[], excluded: boolean}`;
an absent section or empty values shows everything in that section. Values use
stable message-label IDs (`?` for uncategorized), event-group keys, tool-category
IDs (`error` for failed calls), or caller-source IDs. The selection filters
ribbon spans, event arcs and markers, counts and inspection targets together.
Original timing, zoom, idle compression and retained records remain unchanged.
Caller-source filtering preserves each run's original portion of the message.

## Agent interface

On work pages, **Hierarchy & usage** uses `delegation_scope=self|all` for both its visible metrics and sorting (`all`, or **Self + children**, is the default). The selection survives returning from a ranking to the hierarchy. Each session's **Timeline** button sets `explorer=timeline`, `delegation_session`, `delegation_start`/`delegation_end` to that session's own evidence span, and `delegation_page` to its hierarchy page. It opens `disclosures["delegation-labels:<session ID>:session"]` and resets deeper event disclosures for that session, showing one event level. Session names continue to open the conversation reader.

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

Home card links open full dashboards directly with `analyze_chart` and `analyze_filters`. Collected-card source queries and cover metadata remain in storage for previews and editing, and are removed from navigation links. There is no expanded-card page. Dashboard Today links may pin an IANA zone with `analyze_timezone`. **Create card** saves that canonical dashboard URL to the Home collection, including chart, filters, Today/timezone, Carbon assumptions and Parallelism counting/gap settings. Card names, descriptions, categories and calls to action, and the creation dialog, are authoring data/drafts; they do not add view parameters. One dashboard URL can hold several charts (Parallelism draws its ribbon above its catalog chart): each chart has its own **Create card** button, the dialog chooses among them, and the card stores its choice as `dashboardTarget` beside the URL rather than in it. Custom card previews resolve the saved URL and run the dashboard’s analysis/trend models again.

A catalog dashboard's `analyze_filters` terms other than its range are its **population**: every view, breakdown and panel counts only those records. A catalog with `population` (Session Shape) shows them as removable chips in a Population control beside Range, edits them as a draft with the query filter editor, and lets a legend value's menu add one: "Count only this value" writes `{field, op: "=", value}` on the breakdown's field, "Leave this value out" `{any: [{field, op: "!=", value}, {field, op: "is_null"}]}`. A legend selection stays in `analyze_chart` and only chooses what is drawn; "Make this the population" moves it into the filters. The chart title names a partial population ("· excluding antigravity-cli"). There is no separate parameter: site_link's `filters` are these terms, as in `{"page": "analyze/session_shape", "filters": [{"field": "agent_kind", "op": "=", "value": "root"}]}`.

Analyze query pages accept `options.return_topic` (`analyze_topic`) independently of their dataset so a supporting query returns to the dashboard that opened it.

Analysis source-query links also retain the dashboard chart/population and opened-query baseline. These are documented view fields so returning to a dashboard can reconcile user edits without dropping relative time intent. Specialized time companions (`window_mode/days/field/until_field/dataset/timezone`) remain discoverable and portable.

`<page>/query` also publishes `schema_name`, an inspection template, and a `properties_path` to the paginated property catalogue. Use that schema name (for example `pharos_mcp_calls`) in each inspection scope. `JsonInspectionState`, `JsonMatchIdentity` and related types are generated from the installed core declarations. Returning a native `JsonValueView` directly from a property renderer lets Query Table bind its controlled state and semantic registry; wrapping that element in a host div hides it from the supported integration.

The session reader's `reader_navigator` stores any combination of `timeline`,
`context` and `turns` (default `["timeline","context","turns"]`); `[]` hides the bars. All bars
share the session clock and one visible-window indicator. Scroll progress and
keyboard browsing positions are transient. Clicking a bar materializes, scrolls
to and highlights its source without moving keyboard focus. Entirely untimed
sessions explicitly use message order. Expanding/collapsing content refreshes
cached geometry; scrolling does not remeasure transcript elements.

## Attention

`highlight` is repeatable; `reveal` names one landing target. Reveal means **scroll and distinctive persistent highlighting**, without moving browser/keyboard focus. Ordinary tabbing and scrolling never change these parameters. Clear highlights removes the instructions.

Targets include the page, controls, named sections, card definition IDs, message/tool-call IDs, session IDs, records in the current result page, and literal text. IDs refer to data/semantic identity rather than DOM order. A row target highlights its actual rendered row through public table extension points. It does not open a modal or broaden a query.

`text:<literal>` is an alias for [page Find](#page-find): on arrival the targets are replaced by `find` (several literals become one escaped OR regex with `find_regex=1`). A revealed literal lands on its first match, opening the disclosures on its path. No token IDs are created. Message reveal materializes its owning turn and opens ancestors. Content expansions are retained as view state.

`reveal=detail:{"rowId":"id","field":"arguments_json","path":"$[\"events\"][41][\"text\"]"}` invokes the published semantic registry. The page selects the dataset; preserve numeric versus string IDs. Field and path are optional. The target loads exact details and materializes a selected property/JSONPath without changing the population, filters, sort or pagination. Missing records/properties/paths report unavailable. Reveal waits for restoration and the initial row execution, so a temporary fallback cannot consume the landing. Navigation aborts pending work; completed landings survive temporary provider withdrawal, so background refresh does not repeat scrolling. This family is reveal-only because the upstream registry has no highlight-only or multi-target API. Native marks use Pharos reveal styling and Clear highlights hides them.

In the Mac app, the page's context menu adds **Copy Page Link** and **Highlight _Card/Message/…_** to WebKit's Back/Forward/Reload, followed by **Open Logs**, **Send Feedback…** and **Annotate…** (`window.pharosPageAction`). Annotating, from there or from Send feedback, also puts the annotation toolbar's button in the header so it can be turned off. Highlight names the innermost element under the pointer whose target reference resolves back to it (`pharosView.contextTarget`), and adds it to the URL without scrolling: as `reveal` when the link has no landing, otherwise as another `highlight`. Choosing it again (**Remove Highlight**) removes it, promoting the first highlight to the landing. On a Markdown link to a local file the service has found (`data-file-path`), WebKit's link items give way to Finder's: **Open** (left out when the service says opening would run the file), **Open With** (the default app, the others that claim the type, and **Other…**), **Show in Finder** and **Copy Path**. A plain click opens the file in its default app through `POST /api/markdown-link`, or shows a script or installer in Finder.

Components register target resolvers with `window.pharosView.register`. Rendering can expose `data-pharos-target`, `data-view-field` or an existing semantic identity attribute. Registration and DOM mutation signals handle delayed content. Navigation cancels stale scheduled landings. A background refresh reapplies highlighting without repeatedly scrolling. Targets unavailable under the current tab/query are reported and never silently widen its data scope.

## Page Find

Cmd/Ctrl+F opens one Find bar on every page, including the conversation reader (`web/src/page-find.ts`; the DOM-free rules are in `page-find-model.ts`). In a browser, a second Cmd/Ctrl+F while the Find input has focus falls through to the browser's own find. The Mac app has no browser find; its **Edit → Find** menu (Find…, Find Next ⌘G, Find Previous ⇧⌘G) calls `window.pharosFind`.

- **URL.** `find` holds the term and is present while Find is open (empty when just opened). `find_case=1` and `find_regex=1` are the Match case and Regular expression toggles; their false defaults stay implicit. Typing updates `find` with `replaceState` after a pause. A typed term shorter than three characters waits for Enter; a restored term searches at any length. Find belongs to its page: navigation drops `find*` with `highlight`/`reveal`, bookmarks and saved card links exclude it, and Back/Forward restore it.
- **Scope.** Find searches the open dialog (a native `<dialog>` or an `aria-modal` dialog, which also hosts the bar), else an open drawer, else the active view. It includes text that is hidden only by a disclosure: a closed `<details>`, or the panel of a collapsed `button[aria-controls]`. Lazy content marks itself `data-pharos-find-pending` and supplies the text it would render (`pharosFindPending`) and a renderer (`pharosFindMaterialize`). The reader does this for unrendered turns, "Show more" event pages, sub-agent runs and tool details, counting only the kinds its Show filter shows. Matches in hidden kinds are reported in the bar with a **Show** action. Content hidden for other reasons (inactive views, `inert`, `aria-hidden`, CSS) is not searched. Find does not fetch data that is not on the page: other result pages or unloaded record details.
- **Attention.** Visible matches use CSS Custom Highlights; the current one is distinct. A closed disclosure holding matches is outlined on its summary with their count. Typing never opens anything: it selects the first visible match from the viewport down. Enter/Next on a collapsed match opens each disclosure on its path through the disclosure's own handlers, so `disclosures` (or the reader's `reader_expanded`/`reader_content`) records the openings and they stay open. Lazy content renders and records `loaded:<message-id>` in `reader_content` so a copied URL shows it, clamped prose expands (`pharosFindExpand`), and the opened summaries stay marked as a trail until the next step or until Find closes. Clicking an outlined disclosure goes to its first match the same way. Focus stays in the Find input throughout. Escape in any Find control closes Find, removes `find*` and returns focus to where it was.
- **Bar.** The input looks like other Pharos inputs, with the Match case (`Aa`) and Regular expression (`.*`) toggles inside it. When matches sit in collapsed content, the status line counts them and **Expand all** opens every such disclosure (recorded like any other opening). The bar sits 24 px below the header; drag its grip, or focus the grip and use the arrow keys (Shift for larger steps), to move it. Home or a double-click on the grip resets it. The position lasts for the page session and is not view state.
- **Performance.** The page walk yields every ~8 ms. Searches triggered by page changes queue behind a running search and only move earlier, so continual updates cannot starve it. Mutations outside the searched content are ignored. Matches are capped at 1,000.

## Attention colours

Highlighter colours mean "Find, a link or a Data Guide explanation is pointing here". `ui.py` defines them in `:root` (light and dark): `--attention-mark` (all matches, count badges), `--attention-current` (the current match, `reveal`, the Find trail), `--attention-ring` (`highlight` outlines, collapsed containers holding matches) and `--attention-ink`. Only `page-find.css`, `view-attention.css` and the reader's deep-link landing (`.authorship-target`) use them. The guide's declarative `.pharos-guide-highlight` shares the linked-highlight style in `view-attention.css`; it marks the illustrated region described by the selected term, independently of keyboard focus and URL attention targets. `web/tests/unit/page/attention-colors.test.mjs` enforces colour ownership and keeps the raw hex values out of every other source.

Other UI stays away from that look:

- Keyboard focus rings use `--focus-ring` (green: the accent in light mode, sea in dark), never gold.
- Text selection, the delegation hierarchy's frames and the sub-agent/run accents use sea instead of gold.
- Dark-mode `--gold` is a muted brass (`#d4ad66`), not a bright yellow.
- The retired `--lamp`, `--lamp-strong`, `--find-marker` and `--find-current` tokens are gone.

Brand gold remains where it cannot be confused with attention: thin 1 px brand borders, the active header tab, the human-message left rule (`speaker-row.human`), the lighthouse artwork, and hover borders.

## Time intent

Native elapsed predicates (`-7d`, `+0s`) remain relative in definitions, URLs, snapshots and agent arguments. Search keeps its duration descriptor; execution resolves it once for a cursor batch and renews it on a new open/refresh. Fixed custom bounds remain fixed.

Today is represented explicitly and re-resolved at execution boundaries. Omitted timezone follows the viewer; a supplied IANA timezone stays pinned. Dashboard-to-query navigation preserves separate lower/upper interval-overlap fields. Long range controls are labeled **180 days** and **365 days**, accurately reflecting their elapsed-day definitions rather than promising calendar months/years.

TL1 attempts remain available through the Analyze query dataset; the retired TL1 report pages and their view fields are removed. Session pagination and conversation Find batches store a page number and rebuild current server cursors when opened; expired cursor blobs are not used as identities.

Carbon and Parallelism assumptions are view-scoped overrides. Request parameters carry them to rows, metrics and item details. Restoring a URL never saves preferences. Deliberate changes to counting assumptions, Carbon settings or Findings ranking also update the corresponding library defaults; other linked overrides are not copied into those defaults. Existing explicit URLs keep their chosen values, while new unlinked views use the saved defaults.

## Portable large definitions

Automatic native synchronization keeps the definition inline until the library's immutable snapshot store confirms Save. It then replaces the query token with `@code`, guarded against a newer query/navigation. A failed/quota-limited save leaves the inline URL usable. Another browser resolves the reference from the same library, without the original browser's cache.

The endpoint and retention/quotas are the existing `/api/query-snapshots` and library snapshot table. Agent link construction and Ask destination validation are read-only; they resolve existing references from that store but leave new definitions inline. Canonicalization retains an already-resolved immutable reference rather than creating another definition. Relative definitions stay relative; snapshots do not freeze data rows.

Bookmark identity and saved bookmark URLs exclude `overlay`, `history_search`, `history_collapsed` and page Find (`find`, `find_case`, `find_regex`). These remain addressable in copied page URLs, but drawer activity does not unbookmark a page or make its bookmark reopen a drawer.

## Maintaining coverage

1. Add a field to `viewFields`, including its page scope and validation/domain.
2. Bind React state with `useViewState` or repeated content with `useViewEntry` keyed by a stable entity. Classic scripts use `window.pharosView.read/write`. Use `ViewDisclosure` for content; classic content can use `data-view-key`.
3. Bind a semantic attention identity. Parameterized targets should use stable record IDs; arbitrary CSS selectors are never a public URL language.
4. Run `npm --prefix web run generate-view-controls`. Review/classify every unknown entry; never label a view input a setting merely to pass the check.
5. Run `npm --prefix web run generate-site-map`; help/parameter documentation changes travel with the generated manifest.
6. Cover parsing/building/time transitions in node/Go. Use browser tests for real restoration, materialization, scrolling/focus and a fresh context without a local snapshot cache.

`npm --prefix web run check` includes the source-control review gate, manifest freshness, type checks and unit tests. Do not run a frontend bundle build concurrently with Go compilation: Go embeds the generated bundles.
