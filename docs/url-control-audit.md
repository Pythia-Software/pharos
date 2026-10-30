> Historical audit: the dedicated TL1 report pages were subsequently retired. TL1 attempts remain available in Analyze Query; the current contract, generated control ledger and site map reflect the surviving surfaces.

# View and query URL audit

Baseline audit: 2026-10-29 at commit `a52d889f`. The findings below describe the original behavior, before implementation. See [the implementation contract and coverage status](url-view-state.md) for the changes and the outstanding upstream dependency gap.

## Accepted scope

The follow-up decisions include all query and presentation state expected to survive refresh: content visibility, expanded content, selected records and views, columns, layout, filters, ordering, pagination, and applied searches. Exclude ordinary hover, keyboard focus, menu openness, scroll position, drafts, and editing. Settings and their read-only diagnostics are opted out. Legacy view-state migration is deferred; the inventory below records those omissions explicitly.

Findings ranking, Carbon assumptions and Parallelism counting assumptions need view-scoped URL overrides, separate from saving defaults. Relative time intent must survive the complete UI, URL, saved-view, query and agent round trip. Attention is an additional explicit URL instruction: highlight multiple registered elements and scroll one target into view and highlight it, without moving keyboard focus. See the [view and attention contract](url-view-state.md) for the proposed cohesive mechanism.

## Scope and method

The target contract is: the effective query and presentation visible to a user can be encoded, restored in another visit, edited bidirectionally, and described/constructed through the agent interface. Restoring a view does not promise to freeze its underlying data.

Reviewed the route inventory, rendered control owners, route readers/writers, preference fallbacks, Query Table integration and installed 0.9.7 public types, generated site map, Go link parser/builder, Ask instructions, and completeness tests. Covered the canonical sections (Home, Cards, Search, Analyze, Findings, TL1, work/reader), still-reachable legacy analysis/tools views, global overlays, and the boundary with Settings. The current generated map contains 40 nodes, 12 datasets, eight dashboard topics, and 119 cards. These counts describe the manifest, not a count of all UI controls.

Ran five focused browser observations using the actual rebuilt page and fixture APIs, plus one direct link-transport observation. The fixtures are synthetic application data. They exercise URL behavior without touching a real library. Evidence: the workspace audit files under `.context/` (not distributed with the repository). The source inventory is broad; this is not an exhaustive browser permutation run or a guarantee that every existing URL implementation is bug-free.

The agent-facing interface described as a CLI in the request is currently the MCP pair `browse_site` and `site_link`, presented as nested CLI help. Ask explicitly must use it and must not construct/edit URLs itself. Its limitations directly limit Ask. See [site_tools.go](../internal/archive/site_tools.go), [ask_agent.go](../internal/archive/ask_agent.go), and [site-map documentation](../docs/site-map.md).

## Main findings

1. There are controls without a URL representation: transcript display filters and conversation search; Cards preview window; TL1 dashboard scope; delegation columns/tint; and legacy chart/table controls.
2. There are substantial URL capabilities absent from agent help/building: full Query Table definitions, advanced search settings, record keyword search, parallelism ribbon options, Carbon assumptions, and legacy view modes.
3. URL restoration itself needs correction: explicit table sizing is overwritten; reader URLs are rewritten to a path the manifest does not recognize; hidden TL1 table state appears on overview URLs; long automatic query URLs can depend on browser-local snapshots.
4. Existing tests establish consistency for declared capabilities. They do not require every view control to be declared or explicitly opted out.

## Control inventory

Status: **present** means a route reader and writer exist in source, not that every combination has been browser-proven. **Partial agent support** means only a subset can be represented by `site_link`.

### Common Query Table surfaces

Applies to Search, the nine Analyze query datasets, Findings Table, and TL1 Runs. See [QuerySurface](../web/src/index.tsx) and the Query Table types in `web/node_modules/@pythia-software/query-table-core/dist/index.d.ts`.

| Controls/state | Browser URL today | Agent support | Audit result |
|---|---|---|---|
| Field filters, operators, AND/OR, negation, set filters | `q_<dataset>.where` | Filters supported within manifest/schema limits | Existing shared foundation |
| Visible columns, column order/width, rendering, fit-to-screen | `q_<dataset>.select`, `fitToScreen` | Missing general query support | Include; explicit fit setting currently overwritten on Analyze query open |
| Multi-sort, direction, extraction, null ordering | `q_<dataset>.orderBy` | Missing | Include |
| Limit and page offset | `q_<dataset>.limit/offset` | Missing | Include ordinary table pagination |
| Group keys/intervals, source query, summaries/formulas, representative record, HAVING, group presentation | `q_<dataset>.grouping` and result query | Missing for arbitrary views; fixed card recipes are a special case | Include |
| Metrics, metric charts, grouping, sort/top-N, chart presentation, smoothing/share/distribution options | `q_<dataset>.aggregations` | Missing for arbitrary views | Include |
| Native item detail mode, property layout, active item | `q_<dataset>.itemDetails` is supported by dependency | Missing general query support | Include stable detail targets; verify individual modalities during implementation |
| Record keyword field, rules/syntax, literal/regex, matching options | `record_<dataset>`, mirrored in query `hostState` | Missing; recording `host_params` in manifest does not make it a constructible option | Include |
| Time-window controls | `window_mode/days/field/dataset/until_field/timezone`, query date terms | Partial: general supported windows and Today, not every host context | Preserve today/relative/fixed semantics explicitly |
| Query ideas/presets | Resulting query already travels in `q_*` | Depends on full-query support | Link the resulting view; menu-open state is a separate boundary choice |
| Saved/default selection, computed-column creation, row checks for export, refresh/cancel | Mix of saved library objects and local state | Not general view parameters | Saving/creating/exporting are actions; references to existing definitions may still be needed by a view |

The agent limitation is explicit: `site_link.go`'s `whereOnly` rejects tokens with columns, sorting, grouping or metrics. This also prevents accepting many URLs the UI itself produces, beyond the inability to construct them. See [whereOnly](../internal/archive/site_link.go).

The host unconditionally applies `fitToScreen=true` after restoration on Analyze query surfaces. Browser observation: a link with `fitToScreen=false`, a selected model column, and width 240 re-encoded as `fitToScreen=true`. This is a host restoration policy to change, not an upstream patch.

### Search (`/library`)

| Controls/state | Browser URL today | Agent support | Audit result |
|---|---|---|---|
| Table / Conversations result mode | `view`, query host state | Present | Keep |
| Messages / Files / URLs search target, applied file/URL text, case sensitivity | `kind`, `search`, `case` / `text_builder` | Present for basic forms | Keep |
| Visual rules: words, any words, phrase, prefix, proximity, ordered proximity, exclusion | `text_builder` | Flat rules supported | Keep and extend |
| Nested rule groups, ALL/ANY composition, syntax editor/mode, literal/regex, case/fuzzy/separator matching | Full applied `text_builder` | Missing beyond the restricted builder template | Include |
| Search scope: message role/kind, repository, provider, source, conversation/workspace, dates/duration | Full applied `text_builder.scope/dateWindow` | Only named surface/role presets exposed | Include |
| Result ordering/layout | Query state | Missing general query support | Include |
| Browse turns inside a result, selected turn, expanded message text | Local React state | Missing | Include stable result/turn identity and expanded content |
| Unapplied search editor contents and open search dialog | Local draft | Missing | Boundary: recommend opt out of drafts; applied search must always serialize |

Sources: [host-query-state.ts](../web/src/host-query-state.ts), [text-search-model.ts](../web/src/text-search-model.ts), [text-search-editor.tsx](../web/src/text-search-editor.tsx), [LibraryPage and result previews](../web/src/index.tsx). Go `parseTextBuilder` rejects settings outside its rules/scope template, explicitly including fuzzy/case and other filters.

### Analyze dashboards (`/analyze`)

All eight topics share the catalog controls. Existing URL state: `analyze_mode`, `analyze_topic`, `analyze_data`, `analyze_chart`, `analyze_filters`, `analyze_days`, `analyze_window`.

| Controls/state | Browser URL today | Agent support | Audit result |
|---|---|---|---|
| Dashboard/query mode, topic, dataset | Route parameters | Present | Keep |
| Trend/Compare/Distribution/Scatter/Cycle where offered | `analyze_chart.view` | Present | Keep |
| Metric/X/Y, count-each unit, breakdown/color, reduction, values/share, scale/X/Y scale | `analyze_chart` | Present | Strong existing coverage |
| Interval/cumulative display, period and number of hours/days, smoothing/window operation, complete periods, norm, minimum samples | `analyze_chart` | Present | Strong existing coverage |
| Legend include/exclude selection, show-more series | `analyze_chart.selection/seriesLimit` | Present | Strong existing coverage |
| Preset/custom date/time range and drag-to-filter | `analyze_filters`, Today marker | Filters and supported windows available | Preserve date-only versus custom-time editor mode if exact control mode is required |
| Pinned chart bucket/key, hovered point, context-menu openness | Local chart state | Missing | Include deliberate pinned selections; exclude hover and menus |

Browser positive control: changing the Tokens dashboard breakdown to provider updated `analyze_chart` and survived reload.

Sources: [dashboard-analysis.tsx](../web/src/dashboard-analysis.tsx), [dashboard-trend.tsx](../web/src/dashboard-trend.tsx), [usage-chart.tsx](../web/src/usage-chart.tsx), [dashboard state owner](../web/src/index.tsx), [site-map chart options](../web/src/site-map.ts).

**Parallelism:** `analyze_parallelism` already carries ribbon view (Threaded / Model activity), count-by, turn gap, repository, range/custom dates, selected workspace/session, zoom, smoothing minutes, and conversation-list visibility. Agent links only supply a window-derived ribbon template, not arbitrary ribbon settings. The prototype modal has `parallelism=1`, but its unscoped controls rely on saved preferences rather than a complete URL payload. See [concurrency.tsx](../web/src/concurrency.tsx), [concurrency-settings.ts](../web/src/concurrency-settings.ts), [concurrency-store.ts](../web/src/concurrency-store.ts).

Parallelism's lower analysis views use saved count/gap assumptions independently of a linked ribbon. The UI even warns about disagreement. A link cannot currently guarantee a coherent whole-page calculation when the recipient's saved assumptions differ. This is a product/data contract boundary, not just missing help text.

**Carbon:** `analyze_carbon` already snapshots electricity grid/custom intensity, energy scenario, PUE scenario, input sensitivity, vehicle, and flight endpoints. Initial URL override does not save preferences; editing a control saves them. The manifest claims assumptions cannot travel in links and omits this parameter. The UI and manifest disagree. See [carbon.js](../internal/archive/assets/carbon.js), [analysis-catalog-hooks.ts](../web/src/analysis-catalog-hooks.ts), [manifest](../web/src/site-map.ts).

### Cards and Home

| Controls/state | Browser URL today | Agent support | Audit result |
|---|---|---|---|
| Card topic, search, data source, kind, sort | `card_topic/search/data/kind/sort` (`card_data` is the data-source key) | Present | Keep |
| Preview window: defaults / Today / 24h / 7 / 30 / 90 days | Local `days` state | Missing | Definite gap: changes visible previews and destination links; browser reload resets 30 days to defaults |
| Filters visible / hidden | Saved card-library preference, with URL-derived initial opening | No independent parameter | Include as presentation state |
| Edit cards mode | Saved card-library preference; legacy `cards=library` also opens editing | No independent canonical parameter | Exclude editing under the agreed scope |
| Card detail analysis window | Existing query/window/chart parameters | Existing card-window support | Keep |
| Editing card title, description, default window, cover/chart type, height, series count, key, Other remainder | Saved card template mutations | Fixed generated recipes only | Exclude Save/Reset mutations; edited template affects generated links and needs reconciliation with agent catalog |
| Home collection contents/order, keep/remove/edit, drag/drop | Saved bookmarks/library objects | Not reconstructible from a page URL alone | Content mutations excluded |
| Suggestions play/pause, previous/next/refresh, random welcome | Local state | Missing | Preserve deliberate presentation choices if expected across refresh; exclude automatic animation progress and generated random content |

Sources: [card-library.tsx](../web/src/card-library.tsx), [card-library-model.ts](../web/src/card-library-model.ts), [card-analysis.tsx](../web/src/card-analysis.tsx), [home.tsx](../web/src/home.tsx), [query-conveyor.tsx](../web/src/query-conveyor.tsx).

### TL1 (all five pages)

| Controls/state | Browser URL today | Agent support | Audit result |
|---|---|---|---|
| Overview / Configurations / Follow-ups / Quality / Runs | Path | Present | Keep |
| Project | Saved `tl1-project` preference | Missing | Definite query gap |
| Mac/installation | Local state | Missing | Definite query gap |
| All definitions / Current definitions | Local `scope` | Missing | Definite query gap |
| Latest enqueue / 24h / 7d / 30d / all / custom from-to / chosen enqueue / segment checkbox | Saved `tl1-window` and local state | Missing for dashboard scope | Definite query gap; API params exist but are not browser URL params |
| Follow-up From flavor / Next flavor | Local state | Missing | Definite query gap |
| Runs query | `q_tl1_attempts` | Filters only | Full-query gap shared with other tables |
| Flavor/candidate details and run-detail selection | Local state/dialogs | Missing | Recommend stable detail IDs in URL; prompt copy stays an action |

Browser observation: choosing Current definitions and 7 days changed the API query to `project=example&days=7&scope=current` while the browser URL stayed unchanged. Also the hidden Runs table wrote `q_tl1_attempts` onto `/tl1` Overview; that node declares no options. Dataset activation uses `path.startsWith('/tl1')`. This needs route ownership cleanup alongside serialization.

Source: [tl1.tsx](../web/src/tl1.tsx), [datasetIsVisible](../web/src/index.tsx), [manifest TL1 nodes](../web/src/site-map.ts).

### Findings

| Controls/state | Browser URL today | Agent support | Audit result |
|---|---|---|---|
| List/status, repository, dashboard/table/settings, finding ID, detail tab | `list`, `repo`, `view`, `finding`, `tab` | Present | Keep |
| Rank by API cost / tokens / time / failures | Saves `/api/findings/settings`; temporary local override | Missing | Query/view control exposed as a setting; recommend a URL override separate from saved default |
| Table controls | `q_findings` | Filters only | Full-query gap |
| Cart selection, watch/won/dismiss, checklist completion, handoff choices, detector/threshold configuration | Mutations/local editing | Not view parameters | Exclude actions/settings; review modal identity is a separate boundary choice |

Sources: [findings.tsx](../web/src/findings.tsx), [findings-route.ts](../web/src/findings-route.ts).

### Work, conversation reader, session explorer

| Controls/state | Browser URL today | Agent support | Audit result |
|---|---|---|---|
| Work, selected conversation, message, tool call, timestamp | Work/conversation path plus `conversation/message/call/at` | Older `/work/{id}?conversation=...` form only | Reader rewrites to `/work/{id}/conversation/{id}`; manifest parser requires exact path segment count and does not recognize the resulting path |
| Transcript Show presets, custom event-category checkboxes | Saved `pharos-transcript-filter` | Missing | Definite gap: changes visible content; browser observed Primary only without URL change |
| Find in conversation: applied rules/syntax, match options/scope, message-order selector | Local `ConversationSearch` state | Missing | Definite query gap; Library search URL support does not cover this reader search |
| Session table / parallel lanes, explorer visibility | `explorer`, `sessions` | Present | Keep |
| Hierarchy/ranking, metric, Self/Self+children, sort direction, collapsed nodes, selected session, zoom range | `delegation_*` | Present | Existing base; paging and detailed lane state still differ |
| Visible metric columns, column order, cell tint | Saved delegation preferences | Missing | Definite view gap |
| Explorer next/previous page | Local cursor/page stack | Missing | Boundary: stable page/session anchor needed rather than a potentially expiring cursor |
| Per-session detailed parallel lanes, selected event detail | Local expanded set and mark state | Missing | Include stable expanded sessions/event identity |
| Open/close all turns, individual turns/tool payloads, full message text, tool-use summary, source/archive details | Local DOM disclosure state; turn openness partly remembered in memory | Missing | Include under the agreed disclosure scope; message links already reveal their targets |
| Workspace sidebar show/hide, width, mobile sheet | Local state / saved width | Missing | Include durable visibility/layout choices; exclude mobile navigation menu openness |

Sources: [reader controls in ui.py](../internal/archive/assets/ui.py), [reader categories/model](../internal/archive/assets/reader-model.js), [conversation-search.tsx](../web/src/conversation-search.tsx), [delegation.tsx](../web/src/delegation.tsx), [delegation-route.ts](../web/src/delegation-route.ts), [workspace-inbox.ts](../web/src/workspace-inbox.ts), [matchRoute](../internal/archive/site_link.go).

There are two different controls called sessions visibility: the explorer's existing `sessions=hidden` and the workspace navigator sidebar's visibility. They need distinct ownership/names if both are made linkable.

### Still-reachable legacy views

These are reachable through explicit old URLs and current “Legacy view” links. The site map deliberately excludes their route vocabulary, so equivalent canonical topics do not imply parity of controls.

Legacy view-state migration is now explicitly excluded from this project. Retain the rows as known gaps, preserve compatibility, and record the exclusion by page/surface in the completeness manifest. An exclusion must not mask a missing control on a canonical page.

| Surface | Existing URL support | Missing view state |
|---|---|---|
| Legacy Analyze tabs | `analyze_view`, `machine_view` | Agent help excludes these modes |
| Machine Activity shared scope | `activity_days/repo/provider/model` plus origin dimensions | Agent help excludes it |
| Legacy token trend | Table filters in `q_usage`; chart stored in `pharos-usage-chart` | Chart measure/split/period/smoothing/display/scale/legend/series settings do not travel with the table |
| Legacy Human Words | `writing`, `messages`, `q_writing/q_writing_messages`, `message_query`, `message_cursor` | Writing chart stored in `pharos-writing-view`; advanced message search absent from agent interface |
| Legacy Agent Time | Source calls/query scope can travel | Hours/share, trend measure, analytics grouping local; `MachineAnalyticsTable` has URL sync off by default |
| Legacy parallelism | `q_machine_parallelism` and shared scope | No general agent construction; prototype modal settings differ |
| Legacy Carbon | Chart reads `analyze_chart` on entry | Chart edits only save preference, so an existing chart URL can become stale |
| `/tools` Calls/Summary/Skills | `tools_view`, relevant `q_*` | Agent uses canonical query pages; custom selected-call dialog is local |

Sources: [legacy components](../web/src/index.tsx), [machine-activity.tsx](../web/src/machine-activity.tsx), [machine-time.tsx](../web/src/machine-time.tsx), [machine-analytics-table.tsx](../web/src/machine-analytics-table.tsx), [legacy routes](../web/src/site-routes.ts).

### Global/Settings boundary

Theme, header/menu arrangement, Ask enable/provider/model/effort, source/sync/backup setup, pricing acknowledgement, detector settings, and library maintenance are settings/actions and excluded.

Some read-only controls happen to live there: Sync history/diagnostic Query Tables explicitly use `syncUrl:false`; selected index-run details are local. Storage breakdown tabs in Settings are local. These are explicitly opted out by the follow-up decision.

Global controls still require classification against the refresh rule: the header usage-summary overlay, sidebar expansion, bookmarks/history drawers, Data Guide entity/example selection and deliberate pinned selections. There is no blanket opt-out for all chrome, all geometry or all disclosures. Editing, hover, ordinary focus, menus and scroll offsets are excluded. Ask chat IDs intentionally stay on the private lookup protocol and must retain that treatment.

## URL transport and restoration contract

**Long links are a functional gap.** Query Table automatic synchronization uses local inline-or-cache links, with a default whole-URL budget of 2000 bytes. Long queries become `q_*= @code` references into a bounded browser-local cache. Explicit Share/Bookmark can promote to a server snapshot. A plain copied address can therefore fail in a different browser or after cache eviction. Direct observation with a valid 70-term synthetic query produced `kind:local` and an `@...` parameter. `site_link` rejects all such references today. See the dependency's public `createQueryLinkService` contract and [storage.ts](../web/src/storage.ts). We need to decide between self-contained URLs and automatic durable references; the literal “always copy the URL” goal cannot rely on browser-local references.

**Preference precedence:** incoming explicit state must win without requiring the next reader to share saved defaults. Omitted parameters may still use saved defaults for a bare navigation, but copied effective views must carry enough state to override them. Carbon, Parallelism, Findings rank, Cards layouts, legacy charts and saved query defaults make this especially relevant.

**Time:** Today, latest enqueue, trailing windows and all-time are moving views. Fixed custom bounds are fixed views. Timezone affects Today and chart calendar grouping. Recommend preserving the selected semantics, with a separate fixed-bounds option if useful. URLs restore a view specification, not a historical dataset snapshot.

Two follow-up model observations confirm time intent loss. Opening a Tokens dashboard with `analyze_days=7` on October 29 materializes an October 23 lower bound. After writing those returned filters into the URL as the dashboard does, reopening on November 1 retains October 23. Search can preserve the label `7d` but keep the resolved October 22 timestamp when restored on November 1; its existing renewal helper refreshes Today only. See the workspace time observations under `.context/` and the workspace observation source. These are direct calls to the current page models, not browser or server integration tests.

**Navigation:** exercise cold open, reload, browser back/forward, same-route in-app navigation and route exit/reentry against conflicting saved preferences. URL writing alone is insufficient. Current update channels include `popstate`, `pharos:route`, `pharos:view`, and `pharos:uri-changed`; several components subscribe to subsets.

## Decisions and remaining transport choice

The accepted scope above resolves disclosure, overrides, drafts/editing, relative time, diagnostics and legacy coverage. The remaining transport question is how to retain oversized state. The current browser-only short reference has been reproduced. The proposed replacement is inline state where it fits and an immutable view definition in the Pharos library for large state, so another browser with access to that library can resolve it. The stored definition must retain relative time expressions; it does not freeze query results. The new attention contract keeps highlight/focus targets discoverable through the same registry and agent interface.

## What the current completeness checks miss

`check-site-map` catches stale generated output. Site-map tests check served routes, declared reader params, chart domains/rules, recipe templates and generated round trips; Go tests check the corresponding parser/builder. These are useful and should remain.

They do not enumerate all control owners or demand a URL contract for each semantic control. For example, the reader test checks each declared parameter has a reader/writer, not that every reader control appears in the declaration. The route test accepts the work prefix without checking the conversation subroute the reader actually emits. TL1's declared empty dashboard options pass without testing dashboard scope controls.

For the later implementation, extend this system with one versioned view-state contract consumed by state readers/writers and agent help/building. Each semantic control declares a field, a delegated composite contract, or a reasoned opt-out. Cover buttons, segmented modes, disclosures and drag interactions as well as `<input>` elements. Add a source/registration completeness check, unit round trips and override/history tests, cross-language full-state fixtures, and a small browser integration inventory for real rendering/restoration. Avoid brittle assertions about exact DOM shape/CSS/copy. New raw controls outside the registered primitives need a check that fails until they are classified.

Query Table internals must remain upstream-owned. Use published codecs and supported host state; if upstream lacks a needed contract, produce the required handoff and consume a published release. Do not patch installed packages or generated bundles.

## Validation and handoff

Frontend rebuilt using Node 24. The final audit script completed six observations (five browser, one direct transport), with no harness errors. Confirmed gaps are intentionally observations of existing behavior, not passing product-correctness assertions. No Go/source change was made, so Go/full web suites were not needed for this audit. No commits, branch changes, library writes, or PRs.

The initial harness run had two audit-script selector errors (Cards class and compact explorer tint); those were corrected, and the final run is in the linked log. Tint is source-audited, not claimed as browser-tested.
