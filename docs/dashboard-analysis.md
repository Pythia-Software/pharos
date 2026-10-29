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
Pharos MCP (`mcp-analysis-catalog.ts`), Agent Time and Skills
(`record-analysis-catalogs.ts`), Human Words (`human-analysis-catalog.ts`),
Carbon (`carbon-analysis-catalog.ts`) and Parallelism
(`parallelism-analysis-catalog.ts`). See [The catalogs](#the-catalogs) for
what each offers and why.

- **Measures.** Each has a label and format, and a record-level aggregate:
  an `op` with a `field`, an `expression`, or a `ratio` of two parts.
  A ratio such as `["cache_read_input_tokens", "input_tokens"]` means
  `SUM(numerator)/NULLIF(SUM(denominator),0)`. A part may instead count a
  field's non-empty values, `{ op: "count", field }`, so a mean pools too: a
  mean duration per timed call is `["duration_ms", { op: "count", field:
  "duration_ms" }]`. A part may also be **conditional**: it sums a row
  expression, such as `{ op: "sum", expression: "IF([result_count]=0,1,0)",
  label: "requests with no results" }`, so a share of records meeting a
  condition pools like any ratio. It runs as `SUM(IF(...))` in clauses,
  formulas and grouped rows, and names what it counts (`label`, which
  `catalogProblems` requires). Pooling still sums each part per period,
  window or unit. Each also has:
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
  A component breakdown may be **derived** (`derived`): its parts are
  computed rather than summed. The trend queries the summed `inputs.fields`
  per period, grouped further by `inputs.groupBy`, and `derive(totals,
  metric)` turns one period's totals into that period's part values. Its
  parameters (Carbon's factors and settings) are closed over when the
  catalog is built, so new parameters are a new catalog. `trendSeries`
  applies `derive` to each period before smoothing, accumulating or taking
  shares, so the order of operations is the one summed parts have. A
  derived part names the fields its drill-down shows (`fields`).
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
  field a cycle reads and how its keys name their slots (`CycleKey`):
  - `timestamp`: a datetime field. Query Table computes the cycle itself:
    hour of day or day of week in the viewer's zone (`dateGrouping`
    `hourOfDay` or `dayOfWeek`), over whole local hours or days. Pharos
    MCP, Agent Time, Skills, Human Words and Parallelism read `called_at`,
    `started_at`, `created_at`, `sent_at` and `started_at` this way.
  - `recorded-date`: a date the recording Mac assigned in its own zone, such
    as usage `day`. It folds as a date.
  - `instant`: a UTC instant, such as usage `hour`, placed at the viewer's
    local hour.

  Usage's two keys are folded in the browser (`cycleResult`). One function,
  `cycleFrame` in `weekday-cover.ts`, derives the zone a fold uses, its
  complete range and each key's slot start, for `cycleResult` and for the
  weekday cards' `weekdayMetric` alike. A catalog without cycle keys offers
  the cycle view disabled with its reason; one without a cycle disables that
  cycle.
- **Record views** follow from the catalog. Without units, Scatter is
  unavailable; without units or any measure with a per-record `value`,
  Distribution is too (the Tools rollup: a daily summary is not an
  observation). The View control disables them with the reason
  (`viewUnavailable`), and a link asking for one opens Compare. Where records
  carry values but some measure does not (Pharos MCP's call count), the
  distribution's Metric control disables that measure.
- **Samples** (optional, `samples`). What a records compare's minimum counts
  when one record is not one observation: a summed field or a field's
  distinct values, with the unit the controls name. Tools counts calls
  (`{ sum: "call_count" }`), so "ten calls" sums its daily summaries' calls.
  Tokens and Cost and Carbon count conversations
  (`{ distinct: "conversation_id" }`): a usage record is a slice of one
  session's day.
- **Request** (optional, `request`). How a catalog's records reach the
  service, beside the dashboard's filters (`AnalysisRequest`):
  - `params` are query-string parameters every request carries
    (`runAnalysisQuery`, on `/metrics` and `/query-v3` alike). Carbon sends
    the user's current assumptions as `carbon_settings`, under which the
    service estimates each usage record.
  - `note` says so beneath every view.

  There is no request population: Parallelism's service serves only the
  saved settings' records, so a filter on them would only turn a settings
  change in another window into empty charts.

  A catalog that depends on the user's settings is built with them:
  `analysisCatalogFor(topic, settings)` (`analysis-catalogs.ts`) gives the
  complete catalog of any topic, and `useCarbonCatalog`,
  `useParallelismCatalog` and `useAnalysisCatalog(topic)`
  (`analysis-catalog-hooks.ts`) supply the settings in the page: Carbon's
  current assumptions (`carbon.js`) with the factors the service embeds
  (`carbon/co2_factors.json`), and Parallelism's saved counting. Links are
  read and validated with the shapes (`carbonCatalogShape`,
  `parallelismCatalogShape`, `CatalogDashboard`'s `linkCatalog`), which
  carry no settings, so a link stays valid under any setting and is drawn
  with the settings' catalog.
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
- **The day boundary** (`timestamp`). Days, weeks and months are always
  the recorded calendar fields (`day`, `week`, `month`), as the cards group
  them: the trend runs on `/metrics` (and Agent Time's accelerator), its
  supporting query opens grouped rows, and a card's daily points are the
  dashboard's. A dataset whose records carry a true timestamp also offers
  hours: Over time groups the timestamp by UTC clock hour with
  `dateGrouping`, placed at the viewer's hour, and the hour and weekday
  cycles group it in the viewer's zone, as the weekday and hour cards do.
  Pharos MCP, Agent Time, Skills, Human Words and Parallelism follow this
  rule; usage keeps its recorded `hour` too. The trade-off: a record's
  recorded day is the recording Mac's local date, so after a time-zone
  change a late-evening record can sit on one day in a day trend and on the
  next in the weekday cycle; the trend's note says so. An hourly timestamp
  trend's supporting query opens the hourly totals it draws as grouped rows,
  keyed by a calendar group key (`{ field, interval: "hour", timeZone: "UTC"
  }`, `groupedDashboardQuery`), as a day trend's opens its recorded days.
- **Trend defaults.** The trend's default measure and breakdown.
- **Legacy mapping** (optional, `legacy`). A function that reads a link or
  saved state written in an older vocabulary into the catalog's terms, and
  leaves a current state unchanged. For Tokens, `percent` reads as tokens
  shown as shares and `percent_cost` as cost shown as shares. Agent Time's
  and Skills' numbered metrics (`0`, `1`) read as the measure at that
  position. Human Words' `scope` reads as its measure (`typed` as Typed
  words, alone as one total; `all` as All user words, by Text source), and
  its workspace splits (`channels`, `machines`, `execution_machines`,
  `providers`) as the message's own field. Carbon's `tier` reads as
  `carbon_tier`; `co2` and `energy` are still its measure ids.
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
- **Compare, Distribution and Scatter.** A share keeps every group in its
  query and draws the selected groups as slices and every other group as
  Other (`selectGroups`); a selected group beyond the 1,000 returned groups
  is counted in Other, and the note says how many. Outside shares there is no
  denominator to protect, so the selection filters the executed query: a
  record filter on the breakdown's fields, or for units a group filter on the
  unit's fields (`selectionWhere`). No group or point limit can then crowd a
  selected group out (a scatter draws its 2,000 largest points among the
  selected groups). A combined selection is an OR of each group's
  equalities: an included one is one term, `{ any: [{ all: [...] }, ...] }`,
  with one AND branch per group, and an excluded one is one OR per group
  (each field differs or is missing). Every predicate counts toward Query
  Table's 100 per where list, the dashboard's filters included
  (`selectionWhere`, `predicateCount`). A selection that would exceed them is
  drawn from the groups the query returns instead, and the note says so and
  that one beyond the query's group limit is not drawn.
- **Units.** A selection is of a breakdown group, so it maps through the
  unit's fields: `provider = codex` selects the conversations (or delegated
  runs) whose top-level provider is codex, never a population filter on
  record `provider`. The note says so.
- **Supporting queries** add the selection as the same filter, so they list
  what is drawn (unless it is too large to state, as above). The trend's
  supporting query does too, for single-field and combined breakdowns alike.
- **Drill-downs** from a selected series or bucket open that group's records,
  within the population, with the same term. The term round-trips through
  query links (`encodeQuery`, `decodeQueryStrict`) and the site map's Go link
  codec, whose `site_link` arguments accept `{ all: [...] }` branches.
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
- A card's link never held a legend selection. Where a link stops being a
  card link (`relatedCardDashboardURL`, or `usageWorkspaceURL` dropping the
  `card_*` parameters), an engine dashboard's unversioned chart is marked
  current (`stampCardChart`), so a card saved before the marker opens its
  related dashboard with its filters as the population.
- Either way the result is marked. A migrated link is rewritten when the
  dashboard opens it; one restored by a route change is written whole with
  the dashboard's next change, never by a route event that may belong to
  another page.
- **The way back.** "View supporting query" records the query it opened
  (`analyze_opened`), and "Back to dashboard" returns the dashboard's own
  filters, less what the user removed from the opened query, plus what they
  added. What the link itself added (a selection, a measure's population,
  translated days) never becomes a dashboard filter, so a codex selection
  shown as shares comes back as the same selection. Dates are compared as a
  whole, because the opened query may state the range differently (Tools'
  whole days, a trend's rounded periods): if the user changed any date
  predicate, the dashboard's own range is dropped and the query's dates
  become the range, and the link says "dates from this query". The two are
  never intersected.
- Collected cards compare their dashboard charts as drawn
  (`normalizedDashboardChart`: normalized, defaults filled, keys sorted at
  every depth), so re-collecting a card saved before the marker finds the
  saved one, and charts that differ only in their selection do not match.
- Card destinations (`card-destinations.ts`) write versioned states, so a
  card's own filter stays its population. The legacy Usage page keeps its
  chart in preferences; its legend now writes the chart's selection, and a
  filter in its table is a population.

Human Words' older links wrote its legend selection as a filter on the
split's field (`legacySlices`). Their filters on the works dataset are read
as message filters only where the meaning holds (`readHumanLink`): the time
bounds as `sent_at`, a work id as `work_id`, a combined field's single value
(`providers = codex`, a single-value legend selection) as the message's own
field, and fields both datasets hold with one meaning. The rest are never
rewritten: a work's totals (`typed_words > 100` meant a work's total;
`user_words` messages lack), a combined value (`providers = "claude,codex"`
meant works holding exactly both) or its inequality or `contains`, and a
legend selection of a combined group. The dashboard opens without them and
a notice lists them, with a link to the legacy Human words view, where they
still apply. A versioned link is already in message terms. Every engine
dashboard reads an older link in its
catalog's terms once, when it opens, and rewrites it. Carbon's older filters
came from its filter controls, never a legend, so they stay populations
(`legacySlices: false`); its older charts (`co2`/`energy`, `tier`) read
through its legacy mapping, on the dashboard and on the legacy Carbon page,
where a link's `analyze_chart` wins over the saved chart on entry and on
route changes (`legacyCarbonChart`).
Parallelism's ribbon selection keeps its own link (`analyze_parallelism`)
and its "query view" link (`parallelismSelectionQuery`).

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
- **Supporting query.** It covers what is drawn: the dashboard's filters, the
  legend selection as a filter where it fits, and populations, with whole periods from the
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
- **Cycle.** A timestamp cycle is one clause Query Table computes; usage's
  is folded in the browser.
  - **Timestamps.** The clause groups the timestamp by `hourOfDay` or
    `dayOfWeek` in the viewer's zone, so a record falls in the slot of its
    local hour or weekday, across daylight-saving changes and in
    fractional-offset zones. A box or a mean summarises each slot's
    occurrences (`dateGrouping.occurrence`: one whole local hour or day,
    idle ones included): Query Table evaluates the measure once per
    occurrence, counting an idle one as zero for sums, counts and distinct
    counts and leaving it out for ratios. A total adds up every record in
    the slot's whole hours or days (`partialIntervals: "exclude"`).
    Occurrences the range cuts short, and the one in progress, are left
    out. With a range start, idle occurrences fill the range (`range:
    "query"`); without one, they run from the first observed one.
    `planMetricSeries` turns the clause into a plain calendar metric, which
    `runAnalysisQuery` sends to `/query-v3` (the service advertises
    `calendarCycles`) and combines into slots; servers never receive
    `occurrence`. The chart draws the result as returned, with Query Table's
    slot labels, and `nativeCycleResult` checks it is complete.
  - **Usage.** One clause grouped by the ledger's `hour` or `day`:
    `/metrics` answers `hour` from its hourly ledger rows, and `day` is the
    recorded day. `cycleResult` folds the totals into 24 hours or 7
    weekdays, reading keys through `cycleFrame`. Usage's facts are
    day-grained and its `day` is text, so neither key can be a calendar
    interval: `hourOfDay` over `first_usage_at` would place a whole day's
    usage at its first hour.
    - **Observations.** Every complete slot in the range is one. The same
      rule (`idleIsZero`) decides idle slots for the fold, the note and the
      weekday cards. Sums, counts and distinct counts of an idle slot are
      zero, and so is a sum whose only inputs were missing: an hour of
      unpriced usage adds no cost. A ratio of nothing has no value.
    - **Time zones.** Hour keys are UTC instants, placed at the viewer's
      local hour. Day keys are dates the recording Mac assigned in its own
      time zone, which the dashboard does not know. A date's weekday is the
      same in every zone, so days fold as dates.
      - The weekday query's range starts at the viewer's next local midnight
        (`completeStart`). A preset such as "Last 30 days" opened
        mid-afternoon would otherwise return the first date with only part
        of its records.
      - Dates from the viewer's current day on are left out as incomplete.
      - Idle dates pad out the viewer's range.
      - When the recording Mac's zone differs from the viewer's, a date just
        before the first one can still be returned, holding part of a day.
        It is left out of the fold and reported beneath the chart.
      - The note says which rule applies.
    - **Range.** An hour profile allows 400 days: every hour of them fits the
      query's 10,000-group limit.
    - **Missing keys.** A bucket without a key is an error, never an idle
      slot.
  - **Weekday cards.** The weekday cards (`weekday-cover.ts`,
    `weekdayMetric`) keep folding their saved daily query with the same
    `cycleSamples` and `cycleBoxes`: their previews run through the card
    preview batch, which sends clauses as saved and does not plan
    occurrences, and a collected card keeps its daily query and "Inspect
    daily totals" action. The parity tests hold each card's fold to its
    dashboard's native boxes, weekday by weekday, over the same days.
  - **Range.** An hourly occurrence is one calendar group per hour of the
    range, within `/query-v3`'s 10,000 groups: about 400 days.
  - **Supporting query.** A timestamp cycle's opens the occurrences it
    summarises: grouped rows keyed by local hour or day (a calendar group
    key, `{ field, interval, timeZone }`, which the service advertises as
    `calendarGroupKeys`), over whole hours or days. Hour-of-day and
    day-of-week are not grouping keys. The usage weekday cycle's opens the
    daily totals grouped by `day`; the usage hour cycle's opens records,
    because `/query-v3` cannot yet group usage by hour; see
    [`handoffs/usage-v3-hour-grouping.md`](handoffs/usage-v3-hour-grouping.md).
- **Thresholds.** These use Query Table's native `minCount`, which the service
  advertises for both `/metrics` and `/query-v3`, with its distinct and
  summed measures (`minCountMeasures`, `metricMinCountMeasures`). It removes
  small groups before `groupLimit` and reports `hiddenGroups`, so small groups
  cannot crowd out eligible ones, and the note beneath the chart says how
  many were hidden. At unit level, each grouped row is one unit, so the
  threshold counts units. A records compare counts the catalog's `samples`
  where it has them: `{ min, sum: "call_count" }` for Tools' calls,
  `{ min, distinct: "conversation_id" }` for usage's conversations. The
  supporting query keeps the same groups with a group filter on that column.
  Distributions take row minimums only, as Query Table requires, so a
  distribution counts its plotted records or units. Pharos's Go fast paths
  (the native V2 reducer, Machine Time's accelerator, the count-only path)
  decline any active minimum, so Query Table's adapter answers it.
- **Drill-down.** A records bucket uses `inspectBucketQuery`. A unit bucket
  opens the grouped query restricted to that group (HAVING on its key). A
  timestamp cycle's slot opens the records of its whole hours or days in the
  range: a range from the first to the last, with one OR per gap. It has no
  drill-down without a range start, or past about 48 occurrences (Query
  Table's predicate budget), such as one hour of the day over 90 days. A
  folded usage slot spans many dates and has no drill-down.

### Execution

`web/src/analysis-request.ts` runs the query:

- record metrics go to `/metrics`, falling back to `/query-v3` when `/metrics`
  refuses the budget;
- a clause with per-occurrence statistics or whole intervals (a timestamp
  cycle) is first planned into plain calendar metrics (`planMetricSeries`),
  whose results are combined into its slots;
- grouped queries go to `/query-v3` (`postQueryStateMetrics` in
  `metric-request.ts`), and so do clauses with a `dateGrouping` (a timestamp
  cycle or trend), which `/metrics` does not accept;
- a catalog's request `params` ride on every one of these requests;
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
| Human Words | `writing_messages`: one classified user message | conversation; work | `typed`, `words` | `sent_at` (timestamp) | Over time |
| Carbon | `usage`, estimated under the user's assumptions | conversation, agent session, delegated run | `co2`, `energy`, `tokens` | usage `hour`, `day` | Over time |
| Parallelism | `parallelism`: one local hour of one agent at one level of agents at once, under the saved settings | agent | `agent_hours`, `active_hours`, `parallel_hours` | `started_at` (timestamp) | Over time, below the ribbon |

- **Tools** (`tools-analysis-catalog.ts`). Calls, failed calls, failure
  rate (`error_count` / `call_count`), truncation rate (`truncated_count` /
  `call_count`), recorded tool time, mean timed call duration
  (`total_duration_ms` / `timed_count`), context added and carried, by every
  split the dashboard had plus MCP method. Command, program, command category,
  MCP server and MCP method imply their populations. A rollup row is no
  observation a person cares about, so there are no units or per-record
  values: Distribution and Scatter are disabled with that reason, and a
  minimum sums calls (`samples`) and reports the groups it hides; a bar
  opens the group's daily summaries. Its
  Compare view keeps the legacy-view link, since that page still shows
  failure kinds. The
  rollup's text `day` makes it the one catalog with `executable` bounds.
  - **Whole days.** The rollup records whole local days, so a range snaps to
    whole days: one starting mid-day counts that whole day, and the note
    (`rangeNote`) says so. A card whose window starts mid-day therefore
    counts a little more on the dashboard than on the card; parity holds for
    windows starting at a local midnight, and the test checks the
    difference for one that starts at noon. Phase 3 (record-level Tools on
    `tool_calls`) decides whether exact-window Tools cards move to a
    record-level catalog. Record-level analysis of individual calls
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
  maximum and rolling windows are not drawn. The empty-search rate
  (`empty_rate`) is a conditional ratio: requests with no results over
  requests with a recorded result count, among successful requests (its
  population).
- **Agent Time** (`record-analysis-catalogs.ts`). Recorded tool time (shown
  in hours, as before), tool calls, mean call duration (pooled over calls
  with a duration), failed calls and result tokens, by the old splits plus
  tool, operation, command, command + timing source and tool + result-size
  source. An agent conversation is one recorded transcript (`conversation_id`;
  delegated subagents are their own), split by what is constant within it.
  A tool in a conversation (`[conversation_id, tool_name]`) is the
  observation of the per-conversation failure cards. `AgentTimeSummary`
  follows every view and covers the population: with a legend selection it
  says it counts all activities in range, not only the selection.
  Activities read as Agent Time names them; two that
  share a name (`command:Edit` and `tool:Edit`) also show their value, stay
  separate groups and series keyed by the raw value, and open their own
  records.
- **Skills** (`record-analysis-catalogs.ts`). Evidence records, retained body
  volume (`content_bytes` over its population of known positive sizes, which
  replaces the old `IF` expression and keeps drill-downs exact), distinct
  conversations reached and distinct file-load calls, by the old splits plus
  skill + evidence type. Units are conversations and skill paths in a
  conversation (`[conversation_id, skill_path]`, with the skill as its
  breakdown), the observation of `skill-load-distribution`. Body volume
  counts only evidence with a known positive size; a scatter pairing it with
  another measure counts that measure over the same evidence, and its note
  says so.

- **Human Words** (`human-analysis-catalog.ts`). It reads
  `writing_messages`, one record per classified user message, rather than
  the writing-series endpoint (`POST /api/query/writing/series`); the
  message records hold the same authorship counts, and the parity tests
  check every card against them. Measures: typed words, all user words,
  messages with typed text (typed words over zero), all messages,
  conversations (distinct), the typed share of words (pooled), typed words
  per message with typed text (a conditional ratio), and the shares of
  messages with the dataset's correction cue, positive cue and copied text
  (conditional ratios). Text source is a component breakdown of all user
  words into the page's five groups (typed, likely pasted, copied or
  re-sent, templates and attachments, harness and automation); Typed words
  by Text source is one total. Breakdowns: repository, app, provider, the
  three origin fields and the main category. Units: a conversation, and a
  work (`work_id`: a workspace's conversations with its folded delegated
  work, as Human words by workspace counts them). A message holds one
  channel, machine and provider, so the series endpoint's combined
  `channels`, `machines` and `providers` of a whole work become the
  message's own field, which adds up across groups. The dashboard keeps the
  authorship status (a finished classification reruns its queries), the
  summary, the note on typed versus supplied text, and the message mix
  (`MessageMix`, which shares words: messages carry no per-source
  characters). The `writing` dataset stays a query table and the legacy
  page's chart; no card needs it as a unit source, since `work_id` on
  messages is the same work.
- **Carbon** (`carbon-analysis-catalog.ts`). Usage records, as in Tokens
  and Cost, with their estimated CO₂e (`co2e_g`), facility electricity
  (`energy_wh`) and tokens. The service estimates each record from its
  token counts and model tier with the Carbon page's arithmetic (carbon.go
  `withCarbon`, carbon.js `calculate`), under the assumptions a request
  names (`carbon_settings`) or else the saved ones. The catalog is built
  with the calculator's current settings and sends them with every request,
  so the stored estimates are the user's, not the defaults, and a link's
  assumptions (`analyze_carbon`) are honoured without being saved. A shared
  fixture (`testdata/carbon-estimates.json`) holds the service (a Go test)
  and the calculator (a node test, with the token-type split) to the same
  grams and watt-hours under several settings. Token
  type is a derived component: the trend queries token totals by model tier
  per period and the page's own calculator (`window.pharosCarbon.estimate`)
  splits them into uncached input, cache reads, cache writes and output; the
  parts add up to the stored estimate. Breakdowns: model tier, provider,
  repository, model, provider + model, effort, model + effort, agent kind
  and the origin fields, with Tokens and Cost's units. The page keeps its
  filters, estimate, assumptions and explanations; the chart is the engine's.
- **Parallelism** (`parallelism-analysis-catalog.ts`). The ribbon stays: a
  timeline of overlapping conversations with zoom and selection, whose
  "query view" link is unchanged. Beneath it, the engine's views read the
  `parallelism` records: agent hours (`unit_hours`), active hours
  (`elapsed_hours`: each record's share of the wall time, so groups add up),
  hours with two or more at once, agents at once while active (the pooled
  ratio of `unit_ms` over `elapsed_ms`) and agents (distinct), by agents at
  once, repository, workspace and the origin fields (provider and model
  only under agent counting, the only counting for which the service
  records them), with an agent unit (`unit_id`). The service builds every
  record with the saved counting and gap settings and serves no others, so
  the catalog filters nothing by them. A note names the settings the
  returned records carry (`parallelismSettingsNote`) and warns when they
  differ from what the page believes is saved (another window saved
  others), and under workspace counting that Provider and Model need agent
  counting. A saved setting refreshes the parallelism dataset, so every
  view and the note rerun (results fetched before a refresh read as
  loading, so the note never warns about the user's own change). Links are
  validated with the shape, so a Provider link opened under workspace
  counting draws the default breakdown. "Back to dashboard" keeps `counting`
  and `gap_minutes` out of the dashboard's filters. A link may open the
  ribbon with settings it has not saved; the views then follow the saved
  ones and say so.

Every engine dashboard, Tokens and Cost included, has a Refresh action,
which reruns its queries (Pharos MCP's own requests arrive while it is open).
A refresh of a dashboard's own dataset (`pharos:data-refresh`, such as
parallelism records after a saved setting) reruns them too
(`onDatasetRefresh`). A shared snapshot has none.

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
| `machine-latency-trend` | Tools, trend, mean timed call duration, 7-day pooled, whole periods, norm (`buildCardTrend` over the same rollup; Agent Time's pooled mean over individual calls agrees) |
| `truncated-outputs` | Tools, compare, truncation rate by tool, minimum 10 calls |
| `mcp-methods` | Tools, compare, calls by MCP server + method, which implies the card's method population |
| `mcp-response-size`, `pharos-mcp-durations` | Pharos MCP, distribution of bytes or request time by tool, minimum 3 |
| `pharos-mcp-query-sources` | Pharos MCP, compare, calls by requested source |
| `mcp-call-trend` | Pharos MCP, trend, calls by tool, 7-day rolling mean |
| `machine-time` | Agent Time, trend, tool time by activity, 6 series (the cover's limit); compared by raw activity with the card's daily totals, since the card's preview names series by label |
| `machine-activities` | Agent Time, compare, share, tool time by activity |
| `large-outputs` | Agent Time, compare, share, result tokens by tool + result-size source |
| `duration-distribution` | Agent Time, distribution, time by command + timing source, minimum 3 |
| `output-distribution` | Agent Time, distribution, result tokens by tool + result-size source, minimum 3 |
| `conversation-error-counts` | Agent Time, distribution, tool-in-conversation unit, calls by tool, minimum 3 |
| `tool-calls-per-conversation`, `research-conversation-searches` | Agent Time, distribution, conversation unit, calls by repository, minimum 3 |
| `skill-evidence` | Skills, compare, conversations reached by skill |
| `skill-body-sizes` | Skills, distribution, body by skill + evidence type, minimum 3 |
| `skill-load-distribution` | Skills, distribution, skill-path unit, distinct load calls by skill, minimum 3 |

`web/tests/unit/dashboards/bespoke-dashboard-parity.test.mjs` does the same
for Human Words, Carbon and Parallelism over small ledgers of messages,
parallelism records and usage estimated by `carbon.js` under non-default
assumptions (`tests/unit/helpers/bespoke-ledgers.mjs`); the empty-search
rate is in `record-dashboard-parity.test.mjs`:

| Card | Dashboard state |
|---|---|
| `human-words` | Human Words, trend, typed words a day (the card's preview, day by day) |
| `explaining-repos` | Human Words, compare, share, typed words by repository |
| `human-word-mix` | Human Words, compare, typed share of words by repository |
| `human-repository-words-trend` | Human Words, trend, typed words by repository, 7-day mean |
| `human-messages-per-day`, `human-conversations-per-day` | Human Words, trend, messages with typed text or conversations, 7-day mean, whole periods, norm (`buildCardTrend`) |
| `writing-hours` | Human Words, hour cycle, total typed words |
| `human-words-weekday`, `human-messages-weekday` | Human Words, weekday cycle, typed words or messages with typed text (`weekdayMetric`) |
| `human-words-per-message` | Human Words, compare, typed words per message |
| `human-words-per-conversation` | Human Words, compare, conversation unit, mean typed words |
| `human-repo-conversation-depth` | Human Words, compare, conversation unit, mean typed words by repository |
| `human-message-lengths` | Human Words, distribution, conversation unit, typed words |
| `carbon-trend` | Carbon, trend, CO₂e, 7-day total including today, norm (`buildCardTrend`) |
| `carbon-tiers`, `carbon-providers`, `carbon-effort` | Carbon, compare, share, CO₂e by model tier, provider or effort |
| `carbon-repositories`, `carbon-models` | Carbon, compare, CO₂e by repository or model |
| `parallel-days` | Parallelism, trend, active hours by agents at once |
| `parallel-trend` | Parallelism, trend, agents at once while active, 7-day pooled, whole periods, norm (`buildCardTrend`) |
| `parallel-hours` | Parallelism, hour cycle, total agent hours (the card's `hour_of_day`) |
| `parallel-repositories` | Parallelism, compare, agent hours by repository |
| `parallel-active-weekday`, `parallel-weekday` | Parallelism, weekday cycle, active hours or agents at once while active (`weekdayMetric`) |
| `archive-empty-search-rate` | Pharos MCP, compare, empty-search rate by tool, minimum 10 requests |

A Carbon card reports kilograms and the dashboard grams; the tests scale.
The Carbon tests also check that each period's token-type parts are the
calculator's categories for its tokens by tier, and add up to the stored
estimate.

A share's zero group has no slice: `tool-failures` keeps only failing calls,
a filter the rollup cannot apply, so the dashboard's pie holds tools with no
failures at 0%, which draw nothing.

### Cards that cannot be matched yet

The input to phase 4. Every other card in the parity tables above matches.

| Card | Missing capability |
|---|---|
| `tool-failure-rates` | A second minimum: the card requires one failure as well as ten calls. The dashboard shows ten-call tools without failures at 0%; every tool the card shows has the card's rate. |
| `error-signatures`, `edit-failures`, `read-paths`, `repeated-reads`, `primary-domains`, `primary-urls`, `cross-repo-errors`, `error-reach`, `test-failures` | Record-level Tools fields and filters (phase 3); some of them are Agent Time states once its catalog gains those breakdowns. |
| `model-conversation-reach` | A share of group incidences (phase 4). |
| `human-words-per-work` | A workspace directory: the card joins each message's work to its Library location and combines works sharing a repository and directory. Messages carry no location, so the dashboard opens the mean per work (`work_id`), which is not a directory. |
| `human-specific-feedback`, `human-example-phrases` | Human studies classify typed text in the browser (`human-study.ts`: feedback and detail cues, images, pastes, file references). No dataset field holds those classes; the dataset's own `correction_cue` and `positive_cue` are other rules. They open the correction-cue share and all words by Text source. Matching needs those classes as `writing_messages` fields (a Go change). |
| `human-word-total` | The human study (`volume`) totals elapsed windows ending at the current instant (now − k days), not calendar days. It opens typed words as a total over the card's window with a norm, which counts whole calendar days. |
| `session-human-output` | A scatter over `conversation_activity` (estimated human text tokens against output tokens per conversation). No catalog reads that dataset; it opens all words by provider. |

Caveats on matched cards:
- `writing-hours` is all-time on the card; an hour cycle allows 400 days, so
  an older library must choose a shorter range.
- `parallel-hours` groups the dataset's `hour_of_day`, the service's local
  hour; the cycle groups by the viewer's local hour, which agree on the Mac
  that recorded them. The cycle counts complete hours only, so the hour
  in progress is on the card and not on the dashboard; the test opens both
  mid-hour and asserts exactly that difference.
- `carbon-trend` and `human-word-total` trail over the card's own window:
  their destination's `rollingPeriods` follows it when the card opens and
  when its window is edited (`windowedChart`); `carbon-trend` matches at 7
  and 30 days.
- `human-word-mix` lists eight repositories and the dashboard twelve bars.

`machine-operations` has the breakdown it needs (operation) but no parity
test yet; phase 4 maps and checks it with the rest. The Tools cards above
match for windows starting at a local midnight; see Whole days.

## Limitations

- The usage ledger is day-grained per session and model, so cycles use the
  ledger's hourly rows and recorded days rather than calendar grouping on
  `first_usage_at`. Calendar grouping would place a whole day's usage at its
  first hour. Usage cycles are therefore folded in the browser.
- `/query-v3` groups usage by `hour` over daily records, so the usage hour
  cycle's supporting query opens records, and a `/query-v3` fallback for the
  hour clause fails visibly ([handoff](handoffs/usage-v3-hour-grouping.md)).
- Usage's hours step on UTC hour boundaries and take the local hour of their
  start. In zones offset by a fraction of an hour, each slot starts mid-hour
  locally. Timestamp cycles use whole local hours.
- A timestamp cycle's slot drills down only within a range with a start and
  at most about 48 of its hours or days; a usage slot has no drill-down.
- Distributions take row minimums only, so a records distribution's minimum
  counts records even where its compare counts calls or conversations.
- A day trend files a record under its recorded day, the cycles under its
  day in the viewer's zone; they differ for late records after a time-zone
  change.
- Tools' trend has days, weeks and months only: the rollup has no hours.
- Parallelism records follow the saved counting and gap settings only: the
  service has no per-request override like Carbon's `carbon_settings` (a Go
  change), so a link's unsaved ribbon settings cannot reach the engine's
  views.
- Human Words' message mix shares words, not characters: messages carry
  per-source word counts only.

Query Table 0.9.7 resolved the requests in
[`handoffs/query-table-dashboard-views.md`](handoffs/query-table-dashboard-views.md):
chart labels fit their gutter, and multi-key pies, bars and boxes colour each
whole key. Scatters still colour by their first key, by design, so a scatter
keeps its breakdown first. It also delivered the dashboards' requests in
[`handoffs/query-table-after-0.9.6.md`](handoffs/query-table-after-0.9.6.md)
(3, 10, 11, 12 and 13): native hour-of-day and day-of-week cycles, distinct
and summed minimums, calendar keys in grouping stages, and OR of AND legend
selections, which the sections above describe.

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
   - **2c (done).** Human Words, Carbon and Parallelism are catalogs
     ([The catalogs](#the-catalogs)), with the engine items they needed: a
     request seam (`request`: parameters every request carries),
     conditional ratio parts, one day boundary for timestamped records
     (`timestamp`: recorded days, timestamp hours and cycles, which also
     gives Pharos MCP, Agent Time and Skills hourly trends), and derived
     components, and the settings each catalog needs from reusable hooks. Pharos MCP's empty-search rate is a
     conditional ratio. The dashboard-only parts of `WritingPanel`
     (`dashboard`, `viewOverride`), Carbon's bespoke chart (`CarbonView`,
     its series builder, `initialView`, the history request and the
     custom supporting-query links) and the series request on the Human
     Words dashboard are gone. The legacy Human Words page keeps
     `WritingPanel` over the series endpoint, since its table lists works;
     the legacy Carbon page draws the engine's trend.
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
   - Close or record the cards in [Cards that cannot be matched
     yet](#cards-that-cannot-be-matched-yet). Since 2b, a card filtered on
     its own breakdown's field maps to a population filter, which versioned
     card links already write.
   - Draw any card's state with `useAnalysisCatalog(topic)`, or check all
     cards with `analysisCatalogFor(topic, settings)`, which give Carbon's
     and Parallelism's complete catalogs outside their pages.
   - Remove `CardAnalysis`, the stopgap that re-renders a card's own chart.
