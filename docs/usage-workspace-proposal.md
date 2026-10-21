# Analyze workspace: dashboards and queries

## Current direction

Analyze uses the same persistent left sidebar layout as Settings, with two
expandable groups. The global header keeps an ordinary Analyze item. Tools,
Tool summary, and Skill evidence live within this sidebar.

| Area | Purpose | Page shape | Controls |
| --- | --- | --- | --- |
| **Dashboards** | Explore one known analysis | One large interactive graph per page, with a small set of controls and links to its query and records | Metric, breakdown, time bucket, range, scale, smoothing where applicable; graph zoom and legend selection |
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
existing writing graph without the conversation table or the breakdown table.
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
More tool analyses keeps the existing specialized presets available.
Parallelism reuses the original Threaded / Model activity ribbon with workspace/session counting,
turn gap, repository, captured date selection, zoom, and conversation details.
It uses only the real captured timing snapshot. Analyze views have no example-data source or selector; older demo links also use library data.
Skills and Agent time retain their focused time series, with the richer Machine
Time explorer still available through Full current analysis. Carbon reuses the
calculator and graph. Graphs and controls sit directly on the page, without a
central card frame.

Chart settings and source filters are encoded in `analyze_chart` and
`analyze_filters`; Parallelism uses `analyze_parallelism` for its ribbon settings.
**Open grouped query** produces native grouped result rows with the graph’s
source filters, dimensions and summaries. Tools includes the selected measure
and its supporting call counts at the same grouping.
**Open records** opens the matching source population; **Search / filter
records** opens the same Query page with its optional panel expanded.
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

- **Open grouped query** creates grouped result rows. Existing full Query links preserve grouped filters and summaries.
- **Search / filter records** carries source predicates and opens Query with search expanded.
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
