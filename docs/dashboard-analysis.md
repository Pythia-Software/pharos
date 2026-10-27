# Dashboard analysis views

Every Home card should open a standard Analyze dashboard that shows the same
information as the card: the same population and filters, time range, group-by,
metric, smoothing and basis. The chart's shape may differ. When no dashboard
can express a card, the dashboard has a gap to fix. The audit behind this plan
classified the 115 active cards by the capability each needs: comparing groups,
a unit of analysis, distributions, scatters, calendar cycles, ratio and reach
measures, and minimum-sample thresholds.

This document describes the engine that supplies those capabilities, how a
card maps onto it, and the phases that adopt it.

## The model: view × unit × catalog

The engine is `web/src/dashboard-analysis-model.ts`, with the trend's query
and series in `web/src/dashboard-trend-model.ts`. Neither has a DOM; both are
tested in node (`web/tests/unit/dashboards/`).

A **catalog** describes one dashboard topic
(`web/src/token-analysis-catalog.ts` for Tokens and Cost):

- **Measures.** Each has a label and format, and a record-level aggregate:
  an `op` with a `field`, an `expression`, or a `ratio` of two parts.
  A ratio such as `["cache_read_input_tokens", "input_tokens"]` means
  `SUM(numerator)/NULLIF(SUM(denominator),0)`. A part may instead count a
  field's non-empty values, `{ op: "count", field }`, so a mean pools too: a
  mean duration per timed call is `["duration_ms", { op: "count", field:
  "duration_ms" }]`. Each also has:
  - an optional per-record `value`, which makes the measure eligible for
    record distributions;
  - an `additive` flag, which decides whether groups may be shown as shares;
  - an optional implied population (`where`), such as fully priced usage.
- **Breakdowns.** Each maps to one or more record fields (`fields`), such as
  `["provider", "model_family"]`, with an optional implied `where`. For the
  trend's series a breakdown may also fix an `order` (providers, agent
  kinds), label known `values` (`root` is "Root agent") and name a missing
  value (`empty`, "No repository").
- **Component breakdowns** (optional, `components`). These split a measure
  into its additive parts, which are fields rather than groups of records:
  Tokens' "Token type" is uncached input, cache reads, cache writes, output
  and unclassified tokens. Each lists the `measures` it splits and a
  `fallback` breakdown for any other measure (cost by token type shows
  providers, as the Usage chart always has). Only the trend draws them.
- **Units.** Each has a grouping `key` and a required `where`, such as
  `root_conversation_id is not empty`. Each unit also lists the breakdowns it
  supports and the field each maps to at unit level. A breakdown is listed only
  if it is constant within one unit: a conversation's model is
  `root_model_family`, not `model_family`, and an agent session has no model
  breakdown because a session can switch models. Records are the implicit
  default unit.
- **Cycle fields** (optional). These are the calendar dimensions a cycle
  folds; for usage, the ledger's `hour` and `day`. A catalog without them has
  no cycle view.
- **Periods.** The calendar fields the trend groups by: `hour`, `day`,
  `week` and `month`. For usage, hours come from the hourly ledger and the
  others are recorded dates. Several hours or days sum hourly or daily
  totals in the browser.
- **Trend defaults.** The trend's default measure and breakdown.
- **Legacy mapping** (optional, `legacy`). A function that reads a link or
  saved state written in an older vocabulary into the catalog's terms, and
  leaves a current state unchanged. For Tokens, `percent` reads as tokens
  shown as shares and `percent_cost` as cost shown as shares. 2b's older
  shapes (Human Words' `scope`, Skills' and Agent Time's numeric metrics,
  Carbon's `co2` and `tier`) map the same way.

The **state** is one flat object stored in `analyze_chart` beside the trend's
fields:

| Field | Meaning |
|---|---|
| `view` | `trend` (default), `compare`, `distribution`, `scatter`, `cycle` |
| `unit` | `records` or a catalog unit ("Count each") |
| `metric`, `metricY` | measures; `metricY` is the scatter's Y |
| `split` | a breakdown or `none` |
| `reduce` | how per-unit values combine in compare: `total`, `mean`, `median`, `max`, `count`; in a cycle: `median` (box), `mean`, `total` |
| `compareAs` | `bars` or `share` |
| `minSamples` | hide groups with fewer observations |
| `scale`, `scaleY` | `linear` or `logarithmic` |
| `cycle` | `hour` or `weekday` |
| `period`, `hours`, `days` | the trend's time bucket, and how many hours or days a custom bucket holds |
| `rollingPeriods`, `rollingOp` | the trend's trailing window in buckets, and whether each point is its `mean` (default) or `sum` |
| `display` | `interval` or `cumulative` |
| `share` | each trend series as a share of its bucket's total |
| `completePeriods` | whole periods only |
| `norm`, `normPeriods` | a reference line at the mean of the `normPeriods` (30) points before the latest |
| `seriesLimit`, `selection` | series drawn before Other, and a legend selection the chart keeps itself |

A link without `view` is the trend, so existing bookmarks and
`card-destinations.ts` presets keep working; an old `percent` or
`percent_cost` metric is read as its measure with `share`. A unit test
enumerates every Tokens card destination and Usage chart preset and checks
the state and query each opens. `validAnalysisState` checks each field.
`canonicalState` reads a state through the catalog's legacy mapping.

**Saved as asked, drawn as normalized.** A control's change is applied to
the canonical state as the user asked for it (`changeState`) and saved that
way; only drawing and querying normalize. A setting the current measure
cannot use (shares of a ratio, say) is drawn without it, explained beneath
the chart (`trendAdjustments`), and returns when the user picks a measure
that allows it. A view switch maps the fields views share; the trend's own
settings survive a visit to another view. `normalizeAnalysis` and
`switchView` move a combination the view cannot use to the nearest one it
can:

- a trend's shares become a compare share display, and back;
- a breakdown that is not constant within the unit becomes `none`;
- a scatter needs an entity unit and two different measures;
- a record distribution needs a per-record value, so a ratio moves to the
  first unit;
- a unit Total is offered only for additive measures and ratios (see below),
  so distinct counts fall back to the mean;
- a cycle profiles records, with no breakdown.

### Rules every catalog inherits

- **Reserved ids.** `records` (the implicit unit) and `none` (no breakdown)
  are reserved. `catalogProblems` reports a unit or breakdown with either id,
  and its test runs over every catalog. It also reports a component
  breakdown that shares a breakdown's id, splits a measure that is not
  additive, or falls back to an unknown breakdown.
- **Implied filters and units.** A breakdown with an implied `where` is never
  offered for units. The filter would remove records from inside each unit,
  changing every unit's own value rather than only choosing which units
  appear. `catalogProblems` rejects such a mapping. A measure's implied
  population, by contrast, does apply to the units: it defines what the
  measure counts, as with fully priced cost.
- **Combining units.**
  - Total adds up an additive measure.
  - For a ratio, Total pools the parts. It sums the numerator and the
    denominator over the group's units and divides, so it equals the ratio
    over the group's records.
  - A distinct count has no honest unit total, because one entity can sit in
    several units, so Total is disabled.
  - Mean weights every unit equally, and the note says so for non-additive
    measures.
- **Combining cycle slots.** A cycle's Total adds up every day or hour on a
  bar, so it is offered only for additive measures. A ratio of 60% on two
  Mondays is not 120%, and a conversation active on two Mondays is not two
  conversations. Other measures default to the distribution. Their Mean is
  labelled "Unweighted mean": each day or hour counts once, not by tokens.
- **Thresholds count plotted observations.**
  - A record distribution leaves out records without the measure's value
    before grouping.
  - A unit distribution, and a unit compare using total, mean, median or
    largest, drop units without a value as a group filter.
  - A count of units counts every unit.
  - `minCount` then counts what is drawn.
  - Share displays take no threshold: a share's denominator is every group.
    Groups beyond `groupLimit` still count in the denominator through Query
    Table's exact Other.
- **Shares need groups.** With no breakdown there is nothing to divide, so a
  share becomes bars.
- **A share is of every group the filters define.** A legend selection only
  hides series; it never changes the denominator. In the trend, a selection
  on a single-field breakdown is a filter on that field, which the trend's
  query leaves out. In Compare, the same filter is left out of a records
  share's query, so the pie shows every group and says that the selection
  does not change the shares; bars keep the filter. The two views therefore
  agree on the same filters, and a test checks it.
- **No legacy link.** The analysis views do not link to the legacy page.
  That page has no analysis views, so it would show the grouped query under
  its default chart.
- **Groups drawn.** Bars and boxes show 12 groups and pies 8. The trend's
  `seriesLimit` belongs to the trend alone.
- **Leaving for the trend.** The trend draws every measure and population
  over records. A unit compare maps only when it adds up records (Total). A
  count, mean, median or largest of units is another quantity, so
  `trendUnavailable` disables Over time for it, the View control shows the
  reason, and `switchView` leaves the state unchanged. A breakdown carries
  across; a component breakdown becomes `none` in the other views.

### The trend

Over time is a view like the others (`dashboard-trend-model.ts`). Every
point combines a measure, a window and a display:

| | Additive (tokens, cost, input, output) | Ratio (cache-read share, output share) | Distinct count (conversations, sessions, runs) |
|---|---|---|---|
| Each bucket | the sum | the pooled ratio, Σ numerator ÷ Σ denominator | distinct values in the bucket |
| Rolling mean | mean of the window's sums, idle buckets as zero | the window's pooled ratio | the window's counts added and divided by its length, as `human-conversations-per-day` does |
| Rolling sum | the window's total, as `carbon-trend` needs | not offered: a sum of ratios means nothing | the window's counts added: conversation-days, labelled so |
| Cumulative | running totals from the range start | the running pooled ratio | not offered: a running distinct count cannot be built from bucket counts |
| Share | each series ÷ the bucket's total of all series, Other included, after smoothing | not offered | not offered |
| Stacking | stacked columns, with a total | lines when there are several series, no total | lines when there are several series, no total; Other is not drawn |

Any other non-additive measure is a level per bucket: rolling means only,
no sum, cumulative or share.

- **Breakdowns.** Any catalog breakdown, a component breakdown, or `none`:
  the single total series cards need, drawn as columns like any one series. A single-field breakdown's legend
  selection filters the dashboard, and the chart's query leaves that filter
  out so the alternatives stay in the key. A multi-field breakdown, a
  component breakdown and `none` keep the selection in the state.
- **Populations.** A measure's or breakdown's implied `where` joins the
  trend's query and its drill-downs, never the user's filters.
- **Whole periods** (`completePeriods`). The range starts at the next local
  period boundary (`completeStart`, the function the weekday cycle uses for
  days), a custom multi-day or multi-hour bucket starting before it is
  history, and buckets ending after the range's end or now are left out.
  For days this is `cardTrendDays`' `completeDays`.
- **Norm.** The mean of the `normPeriods` (default 30) points before the
  latest drawn point, whatever the range: the stacked total of the drawn
  series for additive measures, or the one series otherwise. The query
  reaches back for those points and their smoothing windows, and every one
  of them counts, idle ones included. It is `buildCardTrend`'s baseline, the
  mean of the prior 30 endpoints, even when "Last 30 days" opened
  mid-afternoon draws only 29 whole days. When fewer earlier points have a
  value (all time, or a ratio with idle periods), the chart says how many it
  averaged. It is not offered for shares, cumulative displays, or
  non-additive measures with several series. The legend and tooltip show it,
  and the note says what it averages.
- **Complete results only.** A clause with partial coverage, a truncated
  group count, hidden groups or a bucket error is an error beneath the chart,
  never idle periods, as in `buildCardTrend`.
- **Drill-downs** open the records behind a point: the dashboard's filters,
  the measure's and breakdown's populations, the series and the point's
  window, and for a distinct count only records holding a value
  (`TrendQuery.records`).
- **Coercion.** `normalizeAnalysis` moves the trend to the nearest state it
  can draw (to draw, not to save): a share of a ratio or of no breakdown becomes values; a
  cumulative distinct count becomes per interval; a rolling sum of a ratio
  becomes pooled; a distinct count over several hours or days moves to one;
  a component breakdown with a measure it does not split becomes its
  fallback. `trendAdjustments` says each change beneath the chart, and each
  control disables the option with the same reason (`trendLimits`).
- **Query.** `trendQuery` builds one clause per series kind grouped by
  `[period field, ...breakdown fields]`, a ratio's parts as two clauses (a sum or a count each), or one
  sum per component, over the filters widened by the smoothing window
  (`smoothingWhere`). It runs through `runAnalysisQuery`, so hourly usage
  reads the hourly ledger, as before.
- **Series.** `trendSeries` lays out the calendar grid, folds groups into the
  top `seriesLimit` plus Other (`foldSeries`, ranking a ratio's groups by
  their denominator), smooths or accumulates, then divides a ratio's pooled
  parts or takes shares, drops history and partial periods, and computes
  the norm over the drawn series.
- **Rendering.** `StackedColumns` keeps its interactions: legend selection,
  pinned keys, date dragging, series drill-downs, expanding the series
  limit, and range presets. Given `prepared` buckets it only draws; it no
  longer smooths, accumulates or takes shares of the trend's data, so pooled
  ratios and rolling sums are never averaged again. New props draw lines
  without totals (`lines`, `additive`), name the window (`rolling`) and draw
  the norm (`reference`). The other dashboards still pass interval amounts
  and let it transform them.
- **Controls.** `AnalysisControls` is the one control bar for every view;
  the trend adds Show as (values or shares), Display, Time bucket,
  Smoothing with its Window (average or total), Periods (whole periods
  only) and Norm.

### Queries

`analysisQuery(catalog, state, where)` returns the executed `QueryState`, its
chart clause, and the supporting query that "View supporting query" opens.

- **Records.** A compare view is one clause grouped by the breakdown. A
  distribution is a `distribution` clause over the measure's per-record
  value: a box per group, or a histogram without a breakdown.
- **Units.** A grouping stage keyed by `[unit-level breakdown, unit key]`
  holds one `SummaryColumn` per measure. A group-stage clause then reduces,
  distributes or pairs those rows. This is the shape card recipes use: see
  `model-choice-recipes.ts`, `api-cost-recipes.ts` and `cardCoverQuery`. A
  scatter puts the breakdown first because Query Table colors points by the
  first key. The supporting query is the executed grouped query itself.
- **Cycle.** One clause grouped by the ledger's `hour` or `day`. `/metrics`
  answers `hour` from its hourly ledger rows; `day` is the recorded day.
  `cycleResult` folds the totals into 24 hours or 7 weekdays.
  - **Observations.** Every complete slot in the range is one. The same rule
    (`idleIsZero`) decides idle slots for the fold, the note and the weekday
    cards. Sums, counts and distinct counts of an idle slot are zero, and so
    is a sum whose only inputs were missing: an hour of unpriced usage adds no
    cost. A ratio of nothing has no value.
  - **Time zones.** Hour keys are UTC instants, placed at the viewer's local
    hour.
    - Day keys are dates the recording Mac assigned in its own time zone,
      which the dashboard does not know. A date's weekday is the same in
      every zone, so days fold as dates. The weekday cards follow the same
      rule: their keys are dates in their query's zone.
    - Only complete dates count, by the rule `weekdayMetric` uses for the
      cards; a test runs both folds on the same daily totals.
      - The weekday query's range starts at the viewer's next local midnight
        (`completeStart`). A preset such as "Last 30 days" opened mid-afternoon
        would otherwise return the first date with only part of its records.
      - Dates from the viewer's current day on are left out as incomplete.
      - Idle dates pad out the viewer's range.
    - When the recording Mac's zone differs from the viewer's, a date just
      before the first one can still be returned, holding part of a day. It
      is left out of the fold and reported beneath the chart.
    - The note says which rule applies.
  - **Range.** An hour profile allows 400 days: every hour of them fits the
    query's 10,000-group limit.
  - **Missing keys.** A bucket without a key is an error, never an idle slot.
  - **The fold's code** (`cycleSamples`, `cycleBoxes`) lives beside the
    weekday cover in `weekday-cover.ts`, which now uses it too.
  - **Supporting query.** The hour cycle's supporting query opens records,
    because `/query-v3` cannot yet group usage by hour; see
    [`handoffs/usage-v3-hour-grouping.md`](handoffs/usage-v3-hour-grouping.md).
- **Thresholds.** These use Query Table's native `minCount`, which the service
  advertises for both `/metrics` and `/query-v3`. It removes small groups
  before `groupLimit` and reports `hiddenGroups`, so small groups cannot crowd
  out eligible ones. At unit level, each grouped row is one unit, so the
  threshold counts units.
- **Drill-down.** A records bucket uses `inspectBucketQuery`. A unit bucket
  opens the grouped query restricted to that group (HAVING on its key). A cycle
  slot spans many dates and has no drill-down.

### Execution

`web/src/analysis-request.ts` runs the query:

- record metrics go to `/metrics`, falling back to `/query-v3` when `/metrics`
  refuses the budget;
- grouped queries go to `/query-v3` (`postQueryStateMetrics` in
  `metric-request.ts`);
- a shared snapshot evaluates its own rows. An `hour` filter first selects
  the daily rows holding a matching hour, for grouped queries as well as
  record metrics, as the service's `/query-v3` path does.

`web/src/dashboard-analysis.tsx` renders the control bar, Query Table UI's
`MetricCard`, the supporting-query links and a note stating what one
observation is.

## Mapping a card to a dashboard state

Read the card's recipe as follows:

1. **Population.** The card's `where` becomes the dashboard's
   `analyze_filters`, as `cardDestination` already writes it. A measure with
   an implied population, such as `priced_cost`, can carry it instead.
2. **Unit.** If the recipe has a grouping stage keyed by an entity (for
   example `root_conversation_id` or `delegated_session_id`), that entity is
   the unit, and the other keys are its breakdown. Otherwise the unit is
   records.
3. **Measure.** This is the summary column or clause the cover plots.
4. **View.**
   - Pie or donut covers are `compare` with `compareAs: "share"`.
   - Bar and list covers are `compare`.
   - A `distribution` clause is `distribution`.
   - An `expressionY` clause is `scatter`.
   - Weekday or hour covers are `cycle`.
   - Daily columns and trailing-window sparklines are `trend`: the cover's
     `timeline` gives `rollingPeriods`, `share` and `display`, and a
     `CardTrend` gives `rollingOp` (a `perDay` window is the mean, a plain
     sum the total), `completePeriods` (`completeDays`) and `norm`.
5. **Threshold.** The cover's `minSamples` becomes `minSamples`.

These exemplars are checked in
`web/tests/unit/dashboards/dashboard-parity.test.mjs`. Each runs the card's
query and the dashboard's query over the same in-memory ledger with Query
Table's in-process evaluation, and the numbers must match:

| Card | Dashboard state |
|---|---|
| `model-cost-share` | compare, share, cost by provider + model, priced filters |
| `cache-efficiency` | compare, `cache_read_share` by provider + model |
| `model-session-costs` | distribution, conversation unit, cost by provider + model, `minSamples: 5` |
| `session-token-cost` | scatter, conversation unit, tokens × cost, colored by provider + model |
| `token-volume-trend` | trend, tokens by provider, 7-day rolling mean |
| `token-volume-log` | trend, tokens by repository, log scale |
| `cumulative-api-cost` | trend, cost by provider, cumulative, priced filters (and fully priced cost without them) |
| `model-token-share-trend` | trend, tokens shown as shares by model, 7-day: smoothed before the share |

The trend cards compare the card's own preview, history request and
timeline transforms with the dashboard's series day by day. Two more trend
checks have no usage card: cache-read share by provider + model over 7 days
against an independent pooled calculation, on a fixture whose daily ratios
differ, and a `buildCardTrend` card (daily tokens, 7-day mean over complete
days, and the same card for one provider selected in the legend) against
the trend opened on "Last 30 days" at 14:00 with whole periods and a norm,
point by point and norm against baseline.

The fixture serves one model family from two providers, and the tests
compare full group keys. Its input includes cache writes, so a wrong
cache-share denominator fails.

## Limitations

- The usage ledger is day-grained per session and model, so cycles use the
  ledger's hourly rows and recorded days rather than calendar grouping on
  `first_usage_at`. Calendar grouping would place a whole day's usage at its
  first hour.
- `/query-v3` groups usage by `hour` over daily records, so the hour cycle's
  supporting query opens records, and a `/query-v3` fallback for the hour
  clause fails visibly ([handoff](handoffs/usage-v3-hour-grouping.md)).
- Hours step on UTC hour boundaries and take the local hour of their start.
  In zones offset by a fraction of an hour, each slot starts mid-hour locally.
- Query Table has no hour-of-day or weekday grouping, so cycle slots are
  folded in the browser and cannot be drilled into.
- Query Table colors chart marks by the first group key only, so a share of
  "Provider + model" draws same-provider slices in one color.
- Query Table's box and horizontal-bar charts clip long group labels at a
  fixed margin.
- `minCount` counts rows. A records compare view therefore cannot require
  "five distinct conversations per model"; use the conversation unit, where
  each row is one conversation.

The four Query Table limitations are requested in
[`handoffs/query-table-dashboard-views.md`](handoffs/query-table-dashboard-views.md).

## Phases

1. **Engine (done).** Build the engine and adopt it in **Tokens and Cost**,
   adding compare, distribution, scatter and cycle views beside the existing
   trend.
2. **Trend options and more dashboards.**
   - **2a (done).** Over time is catalog-driven: every measure and
     population, any breakdown, component breakdowns and a single total series,
     rolling sums as well as means, whole periods only, and a norm. Tokens
     and Cost and the legacy Usage page draw it; the bespoke Token chart is
     gone.
   - **2b inherits** the trend unchanged: a new catalog declares its
     measures (additive, ratio or distinct), breakdowns with any `order`,
     `values` and `empty` labels, component breakdowns, `periods`, and a
     `legacy` mapping for its old link shapes, then renders `AnalysisTrend`
     (typed to a `UsageDataset`, which every 2b dashboard is). What 2b adds:
     - Let each cycle entry declare how its keys work. A recorded date (usage
       `day`) and an instant (usage `hour`) work today. A `dateGrouping` over
       timestamps in the viewer's zone is needed for Human Words and
       Parallelism, whose weekday cards use it. One function should then
       derive the zone, the complete-day range and the key times for both
       `cycleResult` and `weekdayMetric`, instead of each doing it. The
       trend's `periods` need the same for datasets without calendar fields.
     - Move the Tools and MCP views (compare and trend) onto the engine,
       and the bespoke trends of `RecordUsageDashboard`, `WritingPanel` and
       `CarbonImpact`. Carbon's trailing sum is `rollingOp: "sum"`.
     - **Open question: Carbon's token types.** A component part is a summed
       record field, but usage has only total `energy_wh`/`energy_kwh` and
       `co2e_g`/`co2e_kg`; the per-type split is computed in the browser from
       the user's carbon factors (`carbon-impact.tsx`). Carbon's type split
       therefore needs a component part that is an expression (or a computed
       field) over the token fields and the factors, or per-type fields in
       the ledger.
     - Add catalogs for Carbon, Human Words, Agent Time, Skills and
       Parallelism.
3. **Record-level Tools analysis.** Add `tool_calls` fields such as
   `error_signature`, `repo_path`, `host` and `url`, plus the card filters the
   `tools` rollup drops.
4. **Card routing.**
   - Map every card to a dashboard state.
   - Add a share of group incidences. `model-conversation-reach` divides
     each provider/model's distinct conversations by the sum of those group
     counts, so a conversation using two models counts twice. The engine can
     show the counts, but disallows shares of distinct counts. This card needs
     an explicitly named "share of pairings" display with that denominator.
   - Add an all-cards parity test.
   - Remove `CardAnalysis`, the stopgap that re-renders a card's own chart.
