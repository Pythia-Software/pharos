# Analyze workspace: dashboards and queries

## Current direction

Analyze uses the same persistent left sidebar layout as Settings, with two
expandable groups. The global header keeps an ordinary Analyze item. Tools,
Tool summary, and Skill evidence live within this sidebar.

| Area | Purpose | Page shape | Controls |
| --- | --- | --- | --- |
| **Dashboards** | Explore one known analysis | A main interactive graph with compact supporting summaries, simple controls and links to its query and records | Metric, breakdown, time bucket, range, scale, smoothing where applicable; graph zoom and legend selection |
| **Query** | Build or change an analysis, or find records | Dataset → full standard Query Table editor, metrics, and results, with an optional Search and filters panel | All supported filters, groups, summaries, formulas, computed columns, charts, sorting, columns, saved queries, exports, and structured record search |

Dashboard pages are ordered **Human Words**, **Tokens and Cost**, **Skills**,
**Tools**, **Agent Time**, **Parallelism**, **Pharos MCP**, and **Carbon Impact**. Each page is the
analysis itself. The sidebar takes the place of the nested tabs and dropdown
navigation. On a phone, the sidebar is available in an expandable navigation
panel above the content.

Home retains its existing card previews, collections, and discovery catalog.
Those cards are a separate surface; Analyze dashboards do not render a card
gallery or require choosing a saved-question card before reaching a graph.

Query offers these datasets: Token usage; Conversation
activity; Human words by conversation; Human messages; Tool calls; Tool
summary; Skill evidence; Parallel activity. A dashboard's simple controls
compose queries, while Query exposes their underlying datasets. Search is an optional addition on the same Query page; it edits the query’s WHERE (or HAVING for grouped results).
Pharos MCP covers requests served by Pharos's own MCP server; archived calls to
external MCP servers remain part of Tools. Settings → MCP owns the enable
switch, connection configuration, agent prompt, and skill, with links in both
directions.

## Current implementation

Analyze, Settings, and TL1 share `NestedPageLayout`: the same sidebar tree,
mobile menu, full breadcrumb trail, page heading, local link handling, and
router subscription. Settings opens directly into its selected page without
an overarching heading or introduction. Its panel metadata supplies both the
menu and routes. See [Nested page layouts](technical-overview.md#nested-page-layouts)
for the reusable component contract.

Open `/analyze`. The sidebar layout is the default Analyze page; it is no longer an
opt-in prototype. Earlier `analyze_workspace=1` links remain valid. It opens Tokens and Cost
directly, defaulting to API Cost with a Model breakdown. Breakdown ordering
is unchanged; the agent-kind choice is labeled Root Agent/Subagent.
Metric choices are API Cost, Token Count,
% API Cost, and % Token Count. This page reuses the original large Usage chart with metric,
breakdown, bucket, range, scale, and smoothing controls. Its interactive legend
and drag selection remain available. The Human words dashboard reuses the
existing writing graph and the compact **Where user-turn text came from**
breakdown, without the conversation query table. The breakdown shares the
graph's selected-date totals request and conversation filters. Its five source
groups show words, characters, and character-based shares, regardless of the
graph's typed/all-input metric or text-source legend selection. Smoothing
history does not enter these totals. The breakdown scrolls locally on narrow
screens.
Tools is a new dashboard for understanding workload and overhead. It defaults
to call volume by tool category, a view supported even when timing is missing.
Metric and Breakdown controls cover calls, failed calls, weighted failure rate,
recorded tool time, mean duration of timed calls, and context added/carried;
breakdowns include tools, shell programs/commands, MCP servers, repositories,
models, and agent kind. Compare groups shows a ranked chart; Over time shows
separate lines for the six busiest groups by call volume, with a
day/week/month control. Its grouped and record links preserve that exact
selected population. Rates are not stacked or averaged
across daily summaries. Grouped queries include the relevant numerators and
call-count denominators. Bars and points open matching daily summary records;
Legacy view keeps the existing specialized presets available.
Parallelism reuses the original Threaded / Model activity ribbon with workspace/session counting,
turn gap, repository, captured date selection, zoom, and conversation details.
It uses only the real captured timing snapshot. Analyze views have no example-data source or selector; older demo links also use library data.
Skills and Agent time retain their focused time series, with the richer Machine
Time explorer still available through Legacy view. Carbon reuses the
calculator and graph. Graphs and controls sit directly on the page, without a
central card frame.

Chart settings and source filters are encoded in `analyze_chart` and
`analyze_filters`; Parallelism uses `analyze_parallelism` for its ribbon settings.
Each dashboard offers four compact query previews: the current measure and
breakdown, ranked source records, a comparison across related dimensions, and
a focused relationship or exception query. Titles describe the concrete result,
such as **Highest-cost token records**, **Conversation cost & cache use**, or
**Repeated file-load attempts per conversation**. All retain the dashboard's
source predicates; focused queries add their own explicit source or group filters.
A muted sentence beneath each query name describes its filters, grouping,
measures and sort order, omitting empty clauses. Bold grammatical cues such as
**where**, **grouped by** and **sorted by** help scan the explanation without
competing with the query name.
Relative dates keep signed offsets such as `-30 days`; fixed dates stay fixed.
All four open Query with search and filters expanded.
**Legacy view** opens the older specialized analysis and labels any filter reset.
`analyze_search=1` persists this panel, and old prototype `analyze_mode=search`
links resolve to Query with search open. Back to dashboard retains graph state.

The UI calls out grain differences: Human words links to grouped conversation
totals for the matching population, while its graph restricts writing to the
selected dates. Parallelism links to hourly threaded occupancy records touching
the ribbon selection; model activity remains a timing estimate in the ribbon.
Carbon’s grouped rows combine token-type components into total estimates under
the current saved assumptions. Multi-hour/day buckets, percentages, smoothing,
and folded legend series remain chart presentation; grouped rows expose the
underlying measures. These are explicit grain differences to resolve before
claiming full chart-to-row parity.

Pharos MCP uses the same single-graph shape: choose a measure (calls, failed
calls or rate, total/mean/largest estimated output, bytes, truncation, or
total/mean duration), breakdown (Pharos tool, status, requested archive source),
range, and comparison or trend. Trends show the six busiest groups across the
selected range. Grouped queries and record links preserve those filters and
groups; call details load by identity regardless of the table's selected columns.
The Query page retains the existing By tool, By day, Failures, and Token cost
analyses. Both views describe the 5,000-call retention limit and link to
`/settings/mcp`. Older `/mcp` URLs resolve to the MCP dashboard or to its query
when `q_mcp_calls` is present, preserving query and rolling-window state.

Existing pages, URLs, saved queries, and Home's dashboard catalog continue to
work. Specialized older analyses remain reachable from the dashboards while
their controls migrate. The main header uses Analyze; MCP setup lives in the
Settings sidebar. There are no database schema changes, Query Table patches,
or dependency changes. Development runs against the
`tools/dev.sh` snapshot at `/Volumes/euclid/pharos-dev-riyadh-v18`; source sync
is disabled by the dev script. No personal data or credentials belong in the
checked-in proposal or assets.

### Dashboard date and smoothing controls

Every dashboard supports a numeric trailing-average window. Time-bucket charts
offer an Off/On selector, showing the window input only when On. Off serializes
as zero; On accepts any safe integer of at least 2 intervals. Earlier links and
saved windows of 1 upgrade to 2, keeping the chart and URL aligned. Toggling
back on restores the prior window within the
current view, or starts at 7. There is no preset-only limit. The window
follows the selected interval, including custom hour/day buckets. Empty
intervals count as zero. Chart requests include the preceding N−1 intervals,
so the first visible point has its full N-interval average, independent of
the display range. History is aligned to local calendar/custom bucket boundaries
(including DST), averaged, then removed from the plot. Totals and linked queries
retain the visible dates and other filters. Human Words and Carbon keep separate
visible-range totals requests. All-time views use available retained history at
its true beginning. Sparse history anchors and sliding sums count idle intervals
without allocating a bucket for every requested interval, even for huge windows.

Home's Provider cost share, Model token share over time, and Model cost share
over time cards use the same history-aware averaging, including collected and
customized covers. Their preview loader fetches exact visible-range totals and
the preceding N−1 calendar days separately, averages amounts before calculating
shares, and hides earlier dates from the rendered columns. Saved queries and
destination links keep the selected range. Both reductions are cached; incomplete
history produces an unavailable preview instead of a misleading partial average.

Tokens and Cost also offers Display: Per interval (stacked bars) or Cumulative
(one line per breakdown value), serialized in `analyze_chart.display`. Cumulative
balances sum original amounts from the selected range start; percentage
metrics show each accumulated amount's share of the accumulated total.
Smoothing is disabled in cumulative display to preserve exact balances, and
the interval window is retained for switching back. Legends, tooltips, scale
selection, range dragging, and query links remain available. Activating a
single cumulative point selects the full range contributing to it. Earlier
amounts remain in balances when the plot reaches its 5,000-period limit.
Grouped queries continue to expose raw interval measurements. Custom hour/day
counts sit beside the interval selector on the same line, including on phones.

Tools and Pharos MCP enable smoothing in Over time, averaging each group's
interval values independently; comparison charts keep their full-range
measures. Missing intervals are counted as zero before averaging. Native chart point
inspection and grouped queries retain original records and measurements;
the dashboard labels this distinction when smoothing is enabled.

Parallelism accepts a window in minutes and overlays a trailing-average
occupancy line. The average weights elapsed time, including idle periods.
It includes captured activity before the displayed range or zoom, and uses a
partial window only at the beginning of timing coverage.
Ribbons, hit testing, summary metrics, and linked records retain actual
activity. This preserves the individual conversations behind each ribbon.
Its window is serialized in `analyze_parallelism.smoothingMinutes`; the other
dashboards use `analyze_chart.rollingPeriods`.

Ranges offer increasing preset durations, Custom dates, and All time last.
All dashboards default to 30 days when a range is not specified. Parallelism
ends presets at its latest captured day and clips them to snapshot coverage.
Native date pickers support start/end or one-sided ranges; Through includes
the selected date. Hourly charts offer date and time inputs with an exclusive
end. Dragging a chart uses the same source filters, and the date picker follows
the selected bounds. Both bounds survive reload and query drilldown. Tools
converts its text day dimension to supported filters, using compact patterns
for wide or one-sided ranges without the former two-year restriction.

## Capability inventory and final destinations

| Existing capability | Final home | Preserve during migration |
| --- | --- | --- |
| Token totals and time series; cost/percent modes; token, model, effort, provider, repository, and agent splits | Tokens & cost reports | Series selection, exclusions, chart zoom, date bucketing, rolling windows, scale, cost assumptions, and pricing refresh |
| Usage group presets (including Work, model/effort, repository) and comparison presets | Saved reports, each with an Open in Query link | Source filters, grouping, HAVING, representative choices, summaries, formula/display settings, price coverage, and drill to source records |
| Machine Time | Agent time reports | Shared scope controls; wall time vs summed duration distinctions; timeline navigation/zoom/history; analytics; work and call drilldown; raw calls in Query |
| Parallelism | Parallelism & delegation reports | Live and example modes, time selection, counting/gap assumptions, scopes, chart navigation, raw hour-by-agent records, and delegated-work interpretation |
| Human Words charts, composition, breakdowns, rebuild status, conversation totals | Human words reports | Authorship provenance, classification categories, chart parameters, generation/rebuild status, and totals vs per-message distinctions |
| Human message table/text and message search | Human messages Query, with optional search | Field filters, message text view, text-search semantics, pagination/cursors, and links to the source conversation |
| Whole-conversation input/output | Human words reports; Conversation activity Query | Whole-conversation grain, delegated work, source time placement, exclusions, and distinct human/usage accounting |
| Carbon estimate and sensitivity/scenario controls | Carbon impact report plus an assumptions panel | Grid/PUE/input sensitivities, scenario, factors, published sources, token scope, and persistent scenario preferences |
| Tool Calls and call presets | Tools reports; Tool calls Query | Result joins; parsed commands/program/category; status, duration and context measures; invocation/result/URL details; source conversations |
| Tool Summary and its aggregation presets | Tools reports; Tool summary Query | Row-to-call drilldown, source identities, grouping grain, context accounting, and mirror deduplication |
| Skills | Skills reports; Skill evidence Query | Evidence type/status, body sizes, paths, parent call details, message evidence, and the distinction between a load and following a skill |
| Ledger/authorship rebuilds and errors | Small data-status notices on relevant pages | Freshness, missing evidence, build progress, failure messages, retry controls, and refresh of the affected records/reports |
| Saved queries, bookmarks, history, shared HTML exports, selection export | Query; reports can be bookmarked too | Complete query/window state, dataset identity, share behavior, old links, and portable exports without live-service dependencies |
| Conversation text / phrases / Boolean and proximity search, search surfaces and snippets | Existing Library search, linked from Query’s optional search | Keep full-text semantics distinct from predicates over retained record fields; never imply `contains` searches uncaptured transcript output |

## Navigation and URL contract

For the current layout, `/analyze` plus `analyze_mode`,
`analyze_topic`, `analyze_data`, and `analyze_days` encodes the page.
`analyze_chart` carries the graph controls; `analyze_filters` carries the source
predicates in the native query codec.
The existing `q_<dataset>` codec carries the actual query; `window_*` retains
rolling-window metadata. The route chooses the active dataset, and switching
datasets must remove stale query/window state. New query links for all nine
Analyze datasets, including Home cards and saved queries, use the Analyze
query route. Header navigation, history titles, summaries, Settings links,
the macOS menu bar, and shared exports use the Analyze page name. `/usage`
and its page parameters have been replaced without compatibility aliases.

The specialized full analyses remain reachable under
`/analyze?analyze_view=…`, `machine_view`, `writing`, and
`/tools?tools_view=…`, retaining their query, scope, chart, and search state.
Explicit URL state must outrank saved preferences. Dataset names such as
`usage`, query codecs such as `q_usage`, and usage data API endpoints keep
their domain names.

Every cross-page action should say what it carries:

- **Grouped results** creates grouped result rows. Existing full Query links preserve grouped filters and summaries.
- **Source records** carries source predicates and opens Query with search expanded.
  Group-level HAVING conditions are not record predicates. Each grouped row's
  **Records** action uses native group-source query helpers to find the source
  population for that group, retaining source filters.
- **View call / evidence / conversation** carries exact IDs and the reader
  anchor. Source results should keep a return link to their analysis.
- A report-only scenario (Carbon assumptions, parallelism counting settings,
  chart split) must be serialized explicitly if it affects the meaning of
  a link. Do not silently discard unsupported predicates on a dashboard;
  offer Query when a custom query exceeds that report's controls.

## Rough implementation sequence

1. **Agree on navigation and page boundaries using the current layout.** Decide
   whether Dashboards and Query are clear, whether Agent time is preferable to
   Machine Time, whether Tool summary deserves its own dataset label, and
   which finite controls each large graph should offer. Confirm the Carbon/time/parallelism
   assumptions stay within their dedicated guided reports.
2. **Introduce the routing/state boundary.** Centralize page/dataset identity,
   canonical links and compatibility mappings. Make all navigation, browser
   back/forward, bookmarks/history, and existing query links use it. Keep
   shared exports working. Build round-trip fixtures for existing URLs before
   removing tabs.
3. **Extract the Query surface and optional search panel.** Separate query-controller/state
   ownership from presentation. Query exposes standard Query Table controls;
   optional search edits the same filters and sort, retaining grouping and evidence links. Keep all native Query Table capabilities and use
   upstream published releases for anything missing in the library.
4. **Move guided reports one topic at a time.** Reuse existing metric/query definitions
   and interactive chart components. Extract the chart/analytics portions from
   Machine Time, Parallelism, Human Words, and Carbon; replace embedded full
   tables/editors with explicit links. Carry all current controls and data
   status/error notices to their relevant destination. Avoid fetching hidden
   pages or all report previews at once.
5. **Retire old navigation after parity.** Check the inventory above against
   live data; migrate the Tools item and nested tabs; maintain old URL aliases.
   Add browser coverage for menus, focused reports, dialogs, responsive
   layout, and source-to-report navigation. Unit-test query transformations
   and route round trips. Before a PR run `go test ./...`,
   `npm --prefix web run check`, and `npm --prefix web run test:full`.

This branch implements the new Analyze page. Continue iterating on its names,
controls, and chart-to-query parity, retaining specialized analyses until their
capabilities have moved into the new layout.

### Cumulative display across dashboards

Human Words, Skills, Agent Time and Carbon Impact now share the Tokens and Cost
Display control: Per interval or Cumulative. Cumulative lines accumulate original
amounts from the selected range start, retaining an opening balance if the plot
is capped at 5,000 intervals. Hidden smoothing history never contributes to that
balance. Smoothing is temporarily disabled in cumulative mode; returning to Per
interval restores the saved smoothing window.

Tools and Pharos MCP offer Per interval and Cumulative alongside Compare groups in Display. Counts, duration totals, bytes,
and token totals accumulate independently for each breakdown. Failure rates and
mean durations/output use cumulative numerators and denominators, including the
recorded-value count for MCP means; they never sum daily percentages or averages.
Largest MCP output tracks the running maximum. Missing denominators remain blank.
A cumulative point opens all matching records in the selected range through its
interval, preserving the selected breakdown and other source filters. Grouped
queries retain the original interval measures and supporting totals. Display is
encoded in analyze_chart and survives reloads and query/dashboard transitions.
Parallelism retains its occupancy view.

### Dashboard control layout

Dashboard controls use four columns by default on desktop, shrinking to fewer
columns on narrow screens. Shared controls follow Metric, Breakdown, Display,
Range, Scale, Time Bucket, Smoothing; dashboards omit controls they do not use.
Custom hour/day inputs stay beside the consistently named Time Bucket selector.
Tools and Pharos MCP put Compare groups, Per interval and Cumulative into one
Display control, retaining the existing view/display URL settings. Carbon keeps
Repository and Provider in its filter section and places Range with the chart
controls. Parallelism retains its context-specific controls in a four-column grid.

## Restored dashboard summaries and Query ideas

Human Words now includes selected-period totals for typed/dictated words,
messages with writing, words per written message, and contributing works,
plus classification freshness and build/error status. Works are the existing
Library conversation groups, not a count of individual agent threads. The
text-source breakdown remains below the graph. Token and Cost restores total
tokens, API price equivalent, weighted cache-hit rate, output tokens and
agent sessions. Agent Time restores total recorded tool time, call volume,
mean time per timed run and timing coverage. Zero-duration calls count as
measured; missing durations do not enter the mean. All summaries follow the
same source filters and selected period as their graph, excluding smoothing
history. Unpriced token usage is retained without inventing a zero price.

All nine Analyze Query datasets, including their legacy table surfaces, have
one **Query ideas** disclosure above their editor and results. It starts open
and stores an independent boolean preference per dataset through the Library
preferences API. The initial render does not save a preference. Collapsing
ideas leaves the active query and its results intact; the native summary
supports keyboard toggles and retains focus. Search and filters remains a
separate optional panel. Query ideas itself contains only its disclosure
header and compact chip rows, with explanations kept in chip tooltips.

Three independent read-only agent reviews informed the curated suggestions:
Human Words/activity, tokens/cost/parallelism, and tools/time/skills/MCP.
The 82 starting points include:

| Dataset | Examples |
| --- | --- |
| Token usage | Repository/model/effort/session summaries, weighted cache efficiency/churn, price changes/coverage, top-level conversations, delegation by root model, agent sessions and costly conversations |
| Human words by conversation | Writing by repository, source app and provider set; deepest/hands-on works, pasted/copied context; pricing coverage |
| Human messages | Message-date weekly writing, text sources, app/provider comparisons, conversation writing, longest and most-pasted messages |
| Conversation activity | Human input and output by repository, root model/effort, event kind, conversation, top-level threads and recent individual events |
| Tool calls | Command-family timing, weighted failure rates and recurring signatures, individual slow calls, context-heavy results, sites and repeated operations |
| Tool summary | Weighted failure/timing comparisons, shell programs, external MCP methods, interruptions, context/cost, root agents vs subagents, longest individual runs and context-heavy commands |
| Skill evidence | Observations by skill, evidence type, repository, status, conversation, latest evidence and large retained bodies |
| Pharos MCP | Tool requests, daily activity, failures, response volume/truncation and requested archive sources, slow requests and large responses |
| Parallel activity | Additive unit-hours by repository, week, concurrency band and conversation, busy units and checkout directories, retaining counting/gap assumptions |

Each suggestion is an editable native grouped query, with native summary
metrics and a compact result chart where useful. Applying one preserves the
source query and its filters, resets result-stage HAVING/paging/metrics, and
selects its new grouping and result layout. Leaving the native grouping
stage restores the original record query. Rates are computed from their numerators and
denominators across matching records, rather than averaging stored ratios.
Small samples remain visible as counts; no quality/productivity score is
inferred from writing volume or input/output ratios.

Every suggestion ends with computed native `op: "link"` columns. **Drill In**
opens matching records in the same dataset, retaining source filters and exact
group-key predicates (including null keys). Every suggested query view and
computed query-link destination sets `fitToScreen: true`, so columns refit to
the viewer’s table viewport after loading. Since Query Table 0.9.7, fitted
link controls shorten to an ellipsis inside their cells and keep their full
accessible name ([handoff](handoffs/query-table-fit-link-clipping.md)).
Work/conversation suggestions also
link directly to their source. Message writing, command-family timing and
skill evidence demonstrate coherent representative-row message links: the
latest message, the call with the longest recorded duration when available,
or latest observed evidence. Native links encode identifiers and leave unresolved identities unavailable. Helper
IDs remain available to the link without crowding the visible columns.
Template links open an individual item; their paired Matching records link
retains the filtered population. Normal modified-click behavior supports new
tabs.

Cross-dataset group drill-downs are deliberately avoided where field names or
filter populations differ. After opening source rows, existing detail links
lead to their exact call, message, evidence, conversation or work. Pharos MCP
links never invent caller identity. Parallel activity does not sum repeated
elapsed occupancy, and distinct counts are not described as additive.

The integration uses only the published Query Table release. It introduces
no local dependency patches, schema changes or generated bundle commits.
