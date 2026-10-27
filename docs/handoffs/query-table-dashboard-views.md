# Query Table: requests from Pharos dashboard analysis views

For the Query Table team. This is a self-contained assignment against the
published **0.9.5** release, dated **2026-10-27**. Implement the requests below
as reusable library behaviour, and publish them in one aligned Go and npm
release (core, react, ui, codegen and `backends/go`).

## Context

The consumer is **Pharos**, a Go service with a React frontend and an
embedded SQLite catalog. Its Analyze dashboards are moving from fixed
time-series charts to a general analysis model. Each dashboard can now
compare groups, show distributions, plot two measures per entity, and profile
an hour-of-day or weekday cycle. The goal is for every Home card to open a
dashboard that shows the same numbers as the card.

Pharos builds each view as an ordinary Query Table query: a grouping stage
for per-entity units, then an `AggregationClause` with `distribution`,
`expressionY`, `minCount` or `groupBy`. It renders the result with
`@pythia-software/query-table-ui`'s `MetricCard`. Pharos does not patch,
vendor or wrap Query Table. Where a capability is missing, it chooses the
nearest honest behaviour, states the limitation in the UI, and asks here.

All four Pharos npm packages and the Go module pin **0.9.5**, unmodified.

The requests are in priority order. Each one can ship on its own.

---

## 1. Fit chart group labels instead of clipping them

**Problem.** In `query-table-ui` 0.9.5, `MetricChart` reserves a fixed left
margin for group labels: `left = Math.min(78, W * 0.24)`. Box rows and
horizontal bars draw each label right-aligned at `left - 8`, inside an SVG
that clips its content. A label wider than about 70 px loses its *start*:
"root_model_family-0" draws as "odel_family-0". Real labels are often longer
than that, for example repository names or "claude / claude-opus-4".

**Request.** Do one of the following:
- size the margin to the widest label, up to a cap (for example 40% of the
  chart width), and truncate longer labels at their *end* with an ellipsis,
  keeping the full label in the existing tooltip and `aria-label`;
- or wrap labels onto at most two lines, as list displays already do with
  `list.maxLabelLines`.

**Acceptance.**
- At 1440 px and 390 px widths, a box or horizontal-bar chart with
  30-character group labels shows the start of each label, either whole or
  truncated at its end.
- No label text extends outside the chart's SVG.
- Charts with short labels keep today's layout.

**Pharos today.** Its browser layout suite detects SVG text cut off by a
`.qt-metric` chart and lists the Distribution view as a known failure until
this ships.

---

## 2. Colour multi-key groups by their whole key

**Problem.** `MetricChart` colours each mark by the bucket's first key only:
`shade = b => colors.color(clause.groupBy[0], b.keys[0])`. A pie, bar or box
chart grouped by `["provider", "model_family"]` therefore gives every group
from the same provider the same colour. Adjacent pie slices become
indistinguishable, and the legend repeats colours. Pharos needs this
two-field breakdown because one model family can be served by several
providers.

**Request.** Do one of the following:
- colour by the whole key tuple for every kind that draws one mark per group
  (pie, donut, bar, box, histogram);
- or add `MetricDisplay.colorBy`: `"first"` (today's behaviour), `"all"`
  (the whole tuple), or a field name.

Scatter can keep colouring by its first key. There, the first key is the
series and the remaining keys identify the point.

**Acceptance.**
- A pie over `["provider", "model_family"]` with buckets `claude/opus`,
  `claude/sonnet` and `codex/gpt-5` uses three distinct colours.
- A given tuple keeps its colour across renders and between charts in the same
  `MetricTheme`.
- Single-key charts are unchanged.

---

## 3. Hour-of-day and day-of-week grouping

**Problem.** Dashboards need cycle profiles: a typical hour of the day, a
typical weekday. `MetricDateGrouping.interval` offers only calendar intervals
(`hour`, `day`, `week`, `month`, `year`). Pharos therefore:
- requests every day or hour in range (a year of hours is 8,760 groups, so
  it needs `groupLimit: 10000`, and refuses a truncated result as
  incomplete);
- folds the buckets into 24 or 7 slots in the browser, filling idle slots and
  computing box statistics with the published `boxSummary`;
- disables drill-down on cycle marks, because a slot spans many dates and no
  filter selects it.

**Request.** Add two cyclic intervals to `AggregationClause.dateGrouping`, in
core and the Go backend:
- `hourOfDay`, with keys `0` to `23`;
- `dayOfWeek`, with ISO keys `1` (Monday) to `7`.

A record falls in the slot its timestamp occupies in `dateGrouping.timeZone`.

1. **Grouping.** Group by the slot, combined with any other `groupBy`
   fields, in `/metrics` (V2), `/query-v3`, `evaluateMetrics` and
   `applyAggregations`.
2. **Per-occurrence statistics.** Add an option to summarise each slot over
   its per-occurrence totals, not only by pooling records. "A typical Monday"
   needs each Monday's total first, then a box or mean over those totals.
   - A possible shape:
     `dateGrouping: { field, interval: "dayOfWeek", timeZone, occurrence: { interval: "day", includeEmpty: true } }`
     together with `distribution: { kind: "box" }`.
   - Idle occurrences count as zero for `sum`, `count` and `count_distinct`,
     and have no value for ratio expressions.
   - Only complete occurrences inside the query's time bounds count: a
     partial first or last day or hour is excluded.
3. **Drill-down.** `inspectBucketQuery` returns a query that selects exactly
   one slot's records. That can be a native predicate, for example an
   `HOUR_OF_DAY([field], zone) = 9` formula or filter. Or it returns an
   explicitly documented null.

**Acceptance.**
- `{ interval: "hourOfDay", timeZone: "America/New_York", includeEmpty: true }`
  returns 24 buckets keyed `0` to `23`, with daylight-saving transitions
  handled.
- A zone with a fractional-hour offset (`Asia/Kolkata`) buckets by local
  hour.
- With the occurrence option, a weekday box summarises each complete date's
  total, idle dates included as zero for additive operations. The partial
  boundary dates of a relative range (for example "last 30 days" opened at
  14:00) are excluded.
- `minCount` and `hiddenGroups` behave as for other groupings.
- The Go and TypeScript executors return identical buckets for the same
  rows.

**Pharos integration once released.** For datasets whose records carry true
timestamps, replace the browser fold with the native interval and enable
drill-down on cycle marks. Usage is the exception: its facts are day-grained,
so it keeps reading its own hourly ledger.

---

## 4. A minimum count over distinct values (lower priority)

**Problem.** `AggregationClause.minCount` counts the rows in a group. An
analysis such as "API cost by model, hiding models used in fewer than five
conversations" needs a minimum on *distinct conversations*. Pharos can
express it today only by grouping records into one row per conversation
first. That works, but it forces the per-conversation view whenever a
records-level threshold would do.

**Request.** Add `minCountDistinct: { field: string; min: number }`, or let
`minCount` take `{ min, distinct: field }`. Executed natively, it removes
groups before `groupLimit` and reports them in `hiddenGroups`, exactly as
`minCount` does.

**Acceptance.**
- A metric grouped by `model_family` with
  `minCountDistinct: { field: "conversation_id", min: 5 }` hides a model with
  40 records in 3 conversations, and keeps a model with 6 records in 6
  conversations.
- `hiddenGroups` reports the hidden groups.
- `/metrics`, `/query-v3` and the in-process evaluators agree.
- `metricPopulationError` reports when a host lacks support, as it does for
  `minCount` today.

---

## Not requested here

`/query-v3` grouping usage records by `hour` is a Pharos host issue:
Pharos's usage source holds day-grained rows plus a separate hourly ledger.
It is tracked in Pharos, not here.
