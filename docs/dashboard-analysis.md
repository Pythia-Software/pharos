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

A **catalog** describes one dashboard topic: Tokens and Cost
(`web/src/token-analysis-catalog.ts`), Tools (`tools-analysis-catalog.ts`),
Pharos MCP (`mcp-analysis-catalog.ts`), and Agent Time and Skills
(`record-analysis-catalogs.ts`). See [The catalogs](#the-catalogs) for what
each offers and why.

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

  A mean is declared as a ratio of a sum and a count, never as `avg`:
  `catalogProblems` rejects an `avg` measure, because a mean of per-period
  means would weight a quiet period like a busy one. A largest or smallest
  value (`op: "max"`, such as Pharos MCP's largest output) is compared but
  never drawn over time (`notTrended`): a window or running value of maxima
  would average or add them. Over time disables it with that reason, and a
  compare of it cannot switch to Over time.
- **Breakdowns.** Each maps to one or more record fields (`fields`), such as
  `["provider", "model_family"]`, with an optional implied `where`. For the
  trend's series a breakdown may also fix an `order` (providers, agent
  kinds), label known `values` (`root` is "Root agent") and name a missing
  value (`empty`, "No repository"). A breakdown with an implied population
  says so in its `note` ("Commands count shell commands only"), which the
  view's note repeats. Values the schema cannot label take the catalog's
  `labels` (Agent Time's activities), in the trend's series and in Compare.
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
  default unit. A key may combine fields (`["conversation_id", "skill_path"]`):
  one observation is then one pair, and a field of the key may be a
  breakdown, since it is constant within the pair. The grouping keys are the
  breakdown's fields followed by the key's other fields, so a card keyed by
  `[tool_name, conversation_id]` reads the same rows.
- **Cycle keys** (optional, `cycle`). For each of `hour` and `weekday`, the
  field a cycle folds and how its keys name their slots (`CycleKey`):
  - `recorded-date`: a date the recording Mac assigned in its own zone, such
    as usage `day`. It folds as a date.
  - `instant`: a UTC instant, such as usage `hour`, placed at the viewer's
    local hour.
  - `timestamp`: a datetime field grouped natively with `dateGrouping`, by
    day in the viewer's zone or by UTC hour (`includeEmpty`, observed
    range). Pharos MCP, Agent Time and Skills fold `called_at`, `started_at`
    and `created_at` this way.

  One function, `cycleFrame` in `weekday-cover.ts`, derives the zone a cycle
  folds in, its complete range and each key's slot start, for `cycleResult`
  and for the weekday cards' `weekdayMetric` alike. A catalog without cycle
  keys offers the cycle view disabled with its reason; one without a cycle
  disables that cycle.
- **Record views** follow from the catalog. Without units, Scatter is
  unavailable; without units or any measure with a per-record `value`,
  Distribution is too (the Tools rollup: a daily summary is not an
  observation). The View control disables them with the reason
  (`viewUnavailable`), and a link asking for one opens Compare. Where records
  carry values but some measure does not (Pharos MCP's call count), the
  distribution's Metric control disables that measure.
- **Samples** (optional, `samples`). What a records minimum counts when one
  record is not one observation: an additive measure. Tools counts calls, so
  "ten calls" is a group filter on summed calls over one grouped row per
  group, not `minCount` on daily summaries.
- **Executable bounds** (optional, `executable`). Translates the time bounds
  into predicates the dataset executes, for every executed and supporting
  query and every drill-down (`dashboardChartURL`'s `translate`). The Tools
  rollup's `day` is text, so bounds become day equalities or patterns
  (`toolDashboardWhere`); drawing keeps the bounds.
- **Periods.** The calendar fields the trend groups by: `hour`, `day`,
  `week` and `month`. For usage, hours come from the hourly ledger and the
  others are recorded dates. Several hours or days sum hourly or daily
  totals in the browser. A dataset may leave a period out (a `day` field is
  required); the Time bucket control then disables it with a reason, and a
  link asking for it draws days.
- **Trend defaults.** The trend's default measure and breakdown.
- **Legacy mapping** (optional, `legacy`). A function that reads a link or
  saved state written in an older vocabulary into the catalog's terms, and
  leaves a current state unchanged. For Tokens, `percent` reads as tokens
  shown as shares and `percent_cost` as cost shown as shares. Agent Time's
  and Skills' numbered metrics (`0`, `1`) read as the measure at that
  position. 2c's older shapes (Human Words' `scope`, Carbon's `co2` and
  `tier`) will map the same way.
- **Legacy selections** (`legacySlices`). Whether a link written before the
  versioned format may hold a legend selection as a filter; see
  [Selections and links](#selections-and-links).

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
| `seriesLimit`, `selection` | series drawn before Other, and the legend selection: the breakdown's groups drawn (or hidden, when `excluded`) |
| `v` | the link format, `2`; see [Selections and links](#selections-and-links) |

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
  chooses which groups are drawn; it never changes the denominator. See
  [Selections and links](#selections-and-links).
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

### Selections and links

`where` (`analyze_filters`) is the population and nothing else. A legend
selection is the state's own `selection`, for every breakdown, single-field
or combined; a single-field selection's values are the field's values (`""`
for a missing one), a combined one's are JSON arrays of them.

- **Shares are of the population.** A filter on the breakdown's own field,
  such as a card filtered to `provider = codex` and broken down by provider,
  is a population: its shares are of codex alone. A selection never enters
  the executed query.
- **The trend** ranks and draws the selected series (never folded into
  Other), divides shares by every series, and keeps every series in the key.
- **Compare, Distribution and Scatter** (`selectGroups`). A share draws the
  selected groups as slices and every other group as Other. Bars, boxes and
  points draw only the selected groups; a scatter matches a point by its
  breakdown keys. A selected group beyond the 1,000 returned groups is
  counted in Other or not drawn, and the note says how many.
- **Units.** A selection is of a breakdown group, so it maps through the
  unit's fields: `provider = codex` selects the conversations (or delegated
  runs) whose top-level provider is codex, never a population filter on
  record `provider`. The note says so.
- **Supporting queries** add a single-field selection as a filter on its
  field (a group filter on the unit's field, for units), so they list what
  is drawn. A combined selection has no single filter and is left out.
- **Drill-downs** from a selected series or bucket open that group's records,
  within the population.
- **Clearing.** Changing the breakdown clears the selection. Views without a
  legend say a selection is drawn and offer "Show every group".
- A test checks that the trend and Compare give the same share under a
  selection, and that a population filter on the breakdown's field is a
  population.

**Links.** `analyze_chart` carries `v: 2` (`analysisVersion`). The dashboard
reads every link through `readAnalysisLink`:

- With the marker, filters stay filters.
- Without it, on a dashboard whose old chart wrote legend selections into the
  filters (`legacySlices`: Tokens and Cost, Agent Time, Skills), a slice-form
  filter on the current breakdown's single field (`provider = codex`, an OR of
  such values, or their exclusion) becomes the selection and leaves `where`.
  Tools and Pharos MCP had no legend, so their old filters were always
  populations and stay so.
- Either way the result is marked, and a migrated link is rewritten in place,
  so it is never read twice.
- Card destinations (`card-destinations.ts`) write versioned states, so a
  card's own filter stays its population. The legacy Usage page keeps its
  chart in preferences; its legend now writes the chart's selection, and a
  filter in its table is a population.

Human Words, Carbon and Parallelism still keep their own charts and
selections until 2c.

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
  the single total series cards need, drawn as columns like any one series.
  The legend selection is the state's own for every breakdown
  ([Selections and links](#selections-and-links)).
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
  never idle periods, as in `buildCardTrend`. No results yet means loading,
  not an error.
- **Supporting query.** It covers what is drawn: the dashboard's filters, a
  single-field legend selection as a filter, and populations, with whole periods from the
  first drawn bucket (for custom multi-day or multi-hour buckets, the first
  whole bucket after the rounded start) to the end of the last whole period.
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
  their denominator). Only the drawn periods rank groups (with whole
  periods, not the partial last one); smoothing and norm history then follow
  the chosen series, so turning the norm on never changes which groups are
  shown. A group that only history holds still adds to drawn windows, shares
  and the norm, so Other lists it, and the series always add up to the
  total without a breakdown. It then smooths or accumulates, then divides a ratio's pooled
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
- **Cycle.** One clause grouped by the catalog's cycle key. For usage that is
  the ledger's `hour` or `day`: `/metrics` answers `hour` from its hourly
  ledger rows, and `day` is the recorded day. A `timestamp` key adds a
  `dateGrouping`: local days in the viewer's zone, or UTC hours.
  `cycleResult` folds the totals into 24 hours or 7 weekdays, reading keys
  through `cycleFrame`.
  - **Observations.** Every complete slot in the range is one. The same rule
    (`idleIsZero`) decides idle slots for the fold, the note and the weekday
    cards. Sums, counts and distinct counts of an idle slot are zero, and so
    is a sum whose only inputs were missing: an hour of unpriced usage adds no
    cost. A ratio of nothing has no value.
  - **Time zones.** Hour keys are UTC instants, placed at the viewer's local
    hour; a timestamp's hours are grouped as UTC hours for the same reason.
    A timestamp's days are local days in the viewer's zone, so every
    complete local day in the range is one observation and the current day
    is left out.
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
  - **Supporting query.** The usage hour cycle's supporting query opens
    records, because `/query-v3` cannot yet group usage by hour; see
    [`handoffs/usage-v3-hour-grouping.md`](handoffs/usage-v3-hour-grouping.md).
    A timestamp cycle's opens records too: a grouping stage cannot group a
    timestamp by calendar date (Query Table request 6).
- **Thresholds.** These use Query Table's native `minCount`, which the service
  advertises for both `/metrics` and `/query-v3`. It removes small groups
  before `groupLimit` and reports `hiddenGroups`, so small groups cannot crowd
  out eligible ones. At unit level, each grouped row is one unit, so the
  threshold counts units. A catalog's `samples` measure (Tools' calls) is a
  group filter over one grouped row per group instead; such a minimum does
  not count the groups it hides (Query Table request 5).
- **Drill-down.** A records bucket uses `inspectBucketQuery`. A unit bucket
  opens the grouped query restricted to that group (HAVING on its key). A cycle
  slot spans many dates and has no drill-down.

### Execution

`web/src/analysis-request.ts` runs the query:

- record metrics go to `/metrics`, falling back to `/query-v3` when `/metrics`
  refuses the budget;
- grouped queries go to `/query-v3` (`postQueryStateMetrics` in
  `metric-request.ts`), and so do clauses with a `dateGrouping` (a timestamp
  cycle), which `/metrics` does not accept;
- a shared snapshot evaluates its own rows. An `hour` filter first selects
  the daily rows holding a matching hour, for grouped queries as well as
  record metrics, as the service's `/query-v3` path does.

`web/src/dashboard-analysis.tsx` renders the control bar, Query Table UI's
`MetricCard`, the supporting-query links and a note stating what one
observation is.

## The catalogs

Each engine dashboard renders `CatalogDashboard` (`index.tsx`): Over time, or
Compare, Distribution, Scatter and Cycle, with the dashboard's own panels
around it. Its default view is the one it always opened on.

| Dashboard | Dataset: one record is | Units | Record values | Cycle | Default |
|---|---|---|---|---|---|
| Tokens and Cost | `usage`: a session's usage of one model and effort on one recorded day | conversation, agent session, delegated run | every additive measure | usage `hour` (instant), `day` (recorded date) | Over time |
| Tools | `tools`: one day's calls of one tool group (a rollup) | none | none | none | Compare |
| Pharos MCP | `mcp_calls`: one request Pharos served | none | `output`, `bytes`, `time` | `called_at` (timestamp) | Compare |
| Agent Time | `tool_calls`: one call | agent conversation; tool in a conversation | `time` (`duration_ms`), `result_tokens` | `started_at` (timestamp) | Over time |
| Skills | `skill_usages`: one piece of skill evidence | conversation; skill path in a conversation | `body` (`content_bytes`) | `created_at` (timestamp) | Over time |

- **Tools** (`tools-analysis-catalog.ts`). Calls, failed calls, failure
  rate (`error_count` / `call_count`), truncation rate (`truncated_count` /
  `call_count`), recorded tool time, mean timed call duration
  (`total_duration_ms` / `timed_count`), context added and carried, by every
  split the dashboard had plus MCP method. Command, program, command category,
  MCP server and MCP method imply their populations. A rollup row is no
  observation a person cares about, so there are no units or per-record
  values: Distribution and Scatter are disabled with that reason, and a
  minimum counts calls (`samples`). The rollup's text `day` makes it the one
  catalog with `executable` bounds. Record-level analysis of individual calls
  (phase 3) would be another catalog over `tool_calls`; this one declares its
  dataset, so the seam is a second catalog, not a change to this one.
  - **Trend ranking.** The old trend drew the six groups with the most calls
    in the range and dropped the rest. Over time ranks by each measure's own
    volume over the drawn periods: an additive amount by itself, a ratio by
    its denominator, so failure and truncation rates still rank by calls and
    mean time by timed calls. It draws `seriesLimit` (15) groups and folds
    the rest into Other, which is drawn for additive measures and ratios, so
    nothing is silently dropped. Time and context rank by themselves rather
    than by calls.
- **Pharos MCP** (`mcp-analysis-catalog.ts`). Calls, failures, failure
  rate, estimated output, mean output (now the pooled ratio of the sum over
  the count of `estimated_output_tokens`), largest output, response bytes,
  truncated calls, request time and mean request duration, by tool, status
  and requested source. A request is an observation, so output, bytes and
  time distribute per request; there is no larger unit in this dataset.
  Largest output is compared only (`notTrended`): its old cumulative running
  maximum and rolling windows are not drawn. The empty-search rate is not
  offered: it needs a ratio part counting only zero-result searches, which
  ratio parts (a sum or count of a field) cannot state; see the unmatched
  cards below.
- **Agent Time** (`record-analysis-catalogs.ts`). Recorded tool time (shown
  in hours, as before), tool calls, mean call duration (pooled over calls
  with a duration), failed calls and result tokens, by the old splits plus
  tool, operation, command, command + timing source and tool + result-size
  source. An agent conversation is one recorded transcript (`conversation_id`;
  delegated subagents are their own), split by what is constant within it.
  A tool in a conversation (`[conversation_id, tool_name]`) is the
  observation of the per-conversation failure cards. `AgentTimeSummary`
  follows every view.
- **Skills** (`record-analysis-catalogs.ts`). Evidence records, retained body
  volume (`content_bytes` over its population of known positive sizes, which
  replaces the old `IF` expression and keeps drill-downs exact), distinct
  conversations reached and distinct file-load calls, by the old splits plus
  skill + evidence type. Units are conversations and skill paths in a
  conversation (`[conversation_id, skill_path]`, with the skill as its
  breakdown), the observation of `skill-load-distribution`.

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

`web/tests/unit/dashboards/record-dashboard-parity.test.mjs` does the same
for the 2b dashboards over a small ledger of tool calls, the rollup derived
from them, Pharos requests and skill evidence
(`tests/unit/helpers/record-ledgers.mjs`). Each card opens its
`card-destinations.ts` state:

| Card | Dashboard state |
|---|---|
| `tool-failures`, `tool-call-share` | Tools, compare, share, failures or calls by tool |
| `tool-failure-rates` | Tools, compare, failure rate by tool, minimum 10 calls (see below) |
| `machine-call-trend` | Tools, trend, calls, 7-day mean, whole periods, norm (`buildCardTrend`) |
| `mcp-response-size`, `pharos-mcp-durations` | Pharos MCP, distribution of bytes or request time by tool, minimum 3 |
| `pharos-mcp-query-sources` | Pharos MCP, compare, calls by requested source |
| `mcp-call-trend` | Pharos MCP, trend, calls by tool, 7-day rolling mean |
| `machine-time` | Agent Time, trend, tool time by activity, 6 series (the cover's limit) |
| `machine-activities` | Agent Time, compare, share, tool time by activity |
| `large-outputs` | Agent Time, compare, share, result tokens by tool + result-size source |
| `duration-distribution` | Agent Time, distribution, time by command + timing source, minimum 3 |
| `output-distribution` | Agent Time, distribution, result tokens by tool + result-size source, minimum 3 |
| `conversation-error-counts` | Agent Time, distribution, tool-in-conversation unit, calls by tool, minimum 3 |
| `tool-calls-per-conversation`, `research-conversation-searches` | Agent Time, distribution, conversation unit, calls by repository, minimum 3 |
| `machine-latency-trend` | Agent Time, trend, mean call duration, 7-day pooled, whole periods, norm (`buildCardTrend` over the rollup) |
| `skill-evidence` | Skills, compare, conversations reached by skill |
| `skill-body-sizes` | Skills, distribution, body by skill + evidence type, minimum 3 |
| `skill-load-distribution` | Skills, distribution, skill-path unit, distinct load calls by skill, minimum 3 |

A share's zero group has no slice: `tool-failures` keeps only failing calls,
a filter the rollup cannot apply, so the dashboard's pie holds tools with no
failures at 0%, which draw nothing.

### Cards that cannot be matched yet

| Card | Missing capability |
|---|---|
| `tool-failure-rates` | A second minimum: the card requires one failure as well as ten calls. The dashboard shows ten-call tools without failures at 0%; every tool the card shows has the card's rate. |
| `archive-empty-search-rate` | A ratio part with its own condition (searches with `result_count = 0` over successful searches with a count). Ratio parts are a sum or count of a field; `SUM(IF(...))` parts would state it. |
| `error-signatures`, `edit-failures`, `read-paths`, `repeated-reads`, `primary-domains`, `primary-urls`, `cross-repo-errors`, `error-reach`, `test-failures` | Record-level Tools fields and filters (phase 3); some of them are Agent Time states once its catalog gains those breakdowns. |
| `model-conversation-reach` | A share of group incidences (phase 4). |
| Human Words, Carbon and Parallelism cards | Their catalogs (2c). |

`truncated-outputs`, `mcp-methods` and `machine-operations` now have the
measure or breakdown they need (truncation rate, MCP method, operation) but
no parity test yet; phase 4 maps and checks them with the rest.

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
  each row is one conversation. A minimum over a summed field (Tools' calls)
  runs as a group filter and cannot report how many groups it hid.
- A grouping stage cannot group a timestamp by calendar date, so a timestamp
  cycle's supporting query opens records.
- Tools' trend has days, weeks and months only: the rollup has no hours.
  Pharos MCP, Agent Time and Skills group by their recorded `day`, `week` and
  `month`; their hourly trends would need the trend's periods to take
  timestamp keys too, which 2c's Human Words needs as well.

The Query Table limitations are requested in
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
   - **2b (done).** Tools, Pharos MCP, Agent Time and Skills are catalogs
     over their Query Table datasets ([The catalogs](#the-catalogs)), with
     every view their data supports. Legend selections are in the view
     state, with versioned links (`v: 2`) and a migration for older ones;
     cycle keys may be recorded dates, instants or timestamps, read by one
     `cycleFrame`; units may have compound keys; a catalog may count its
     minimum in a measure and translate its bounds. The bespoke Tools and
     MCP charts (`toolDashboardQuery`, `smoothedToolTrend`,
     `cumulativeToolTrend`, the MCP equivalents and `mcp-dashboard-model.ts`)
     and `RecordUsageDashboard` (`recordDashboards`) are gone, with the
     metric-trend helpers only they used.
   - **2c inherits** all of that. Human Words (its writing-series endpoint),
     Carbon (derived components, below) and Parallelism (a ribbon timeline)
     each need a catalog, `legacySlices` for their legends, a `legacy`
     mapping for their older shapes (Human Words' `scope`, Carbon's `co2`
     and `tier`), and `timestamp` cycle keys for their weekday cards, whose
     `weekdayMetric` already reads keys through `cycleFrame`. Writing is
     not a Query Table dataset today, so its catalog needs a runner beside
     `runAnalysisQuery`.
   - **What 2b inherited** from 2a: a new catalog declares its
     measures (additive, ratio or distinct), breakdowns with any `order`,
     `values` and `empty` labels, component breakdowns, `periods`, and a
     `legacy` mapping for its old link shapes, then renders `AnalysisTrend`
     (typed to a `UsageDataset`, which every 2b dashboard is). Component
     breakdowns are the exception: today a part is a summed field, so
     Carbon needs the derived components below, a change to
     `AnalysisComponent` and to the component branch of `trendSeries`. What
     2b adds:
     - Cycle key kinds (done in 2b). The trend's `periods` still need
       timestamp keys for datasets without calendar fields (2c).
     - The bespoke trends of `WritingPanel` and `CarbonImpact` remain for
       2c. Carbon's trailing sum is `rollingOp: "sum"`.
     - **Carbon's token types: derived components.** A component part today
       is a summed record field, but usage has only total
       `energy_wh`/`energy_kwh` and `co2e_g`/`co2e_kg`. Carbon computes its
       per-type split in the browser from token counts and the user's
       factors (`carbon-impact.tsx`, `calculator.estimate`). The plan for
       2b is a derived component:
       - `inputs`: the summed fields (and any extra grouping, such as
         `carbon_tier`) the trend queries per period;
       - `derive(totals, params)`: a pure function from one period's input
         totals to that period's part values, where `params` are
         catalog-supplied parameters such as the current carbon factors (the
         catalog is built with them, so a change of factors is a new
         catalog);
       - the series builder applies `derive` to each period's totals
         before smoothing, accumulating or taking shares, so the order of
         operations is unchanged, and Carbon's own calculation is its test
         oracle.
     - **Legend selections in the view state** (done in 2b for every engine
       dashboard; [Selections and links](#selections-and-links)). Phase 4
       consequence: a card filtered on its own breakdown field maps to a
       population filter, which versioned card links already do.
     - Each 2b catalog relies on `validAnalysisState` for the trend's
       period, counts, smoothing, display, scale, `seriesLimit` and
       `selection`, rather than a dashboard-specific validator.
     - Add catalogs for Carbon, Human Words and Parallelism (Agent Time and
       Skills are done).
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
