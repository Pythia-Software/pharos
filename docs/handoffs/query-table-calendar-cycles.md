# Query Table: hour-of-day and weekday grouping

## Request

Please add cyclic calendar grouping to `AggregationClause.dateGrouping`, in
`@pythia-software/query-table-core` and the Go backend, and publish it in a
release. Two new intervals are needed:

- `hourOfDay`, with keys `0` to `23`;
- `dayOfWeek`, with ISO keys `1` (Monday) to `7`.

Each record falls in the slot that its timestamp occupies in
`dateGrouping.timeZone`. The request has three parts:

1. **Grouping.** Group by the slot, combinable with other `groupBy` fields,
   in `/metrics` and `/query-v3`, and in `evaluateMetrics` and
   `applyAggregations`.
2. **Slot statistics.** Add an option to summarize per-occurrence totals
   within each slot, instead of only pooling records. The option could be a
   `distribution` over the clause's per-occurrence values, such as
   `cycle: { occurrence: "day" | "hour", includeEmpty: true }`. Without it,
   "a typical Monday" cannot be computed natively, because each Monday's total
   must first be computed and then summarized.
3. **Drill-down.** `inspectBucketQuery` should return a filter for a slot
   bucket: either a native predicate (`HOUR([field]) = 9` in the clause's
   time zone), or an explicit, documented null.

## Why Pharos needs it

Pharos dashboards now offer a **cycle** view (hour of day, day of week) on
Tokens and Cost; see [`docs/dashboard-analysis.md`](../dashboard-analysis.md).
Six Home cards need it: `writing-hours`, `parallel-hours`,
`human-words-weekday`, `human-messages-weekday`, `parallel-active-weekday` and
`parallel-weekday`. Without native support, Pharos does the following:

- it asks for daily or hourly totals (`groupBy: ["day"]` or `["hour"]`, or a
  `dateGrouping` with `interval: "day"`, `includeEmpty: true`);
- it folds them in the browser (`cycleSamples` and `cycleBoxes` in
  `web/src/weekday-cover.ts`), filling idle slots and computing box
  statistics with the published `boxSummary`;
- it disables drill-down on cycle bars, because a slot spans many dates and
  no filter selects it.

Folding in the browser costs the following:

- every day or hour in range must cross the wire; a year of hours is 8,760
  groups;
- the result needs `groupLimit: 10000` and is refused as incomplete when the
  host truncates it;
- "View supporting query" can only show the unfolded totals.

## Acceptance

- `dateGrouping: { field, interval: "hourOfDay", timeZone: "America/New_York" }`
  returns 24 buckets keyed `0` to `23`. With `includeEmpty`, every slot
  appears. Daylight-saving transitions are handled.
- Half-hour-offset zones (for example `Asia/Kolkata`) bucket by local hour.
- With the occurrence option, a box per weekday summarizes each date's total,
  idle dates included as zero for `SUM` and `COUNT`. This matches Pharos's
  `weekdayMetric` on the fixtures in
  `web/tests/unit/cards/weekday-cover.test.mjs`.
- `inspectBucketQuery` for a slot bucket returns a query whose rows are
  exactly that slot's records, or a documented null.
- Go and TypeScript executors agree, including `minCount` and `hiddenGroups`.

## Pharos integration once released

1. Pin all four npm packages and the Go module to the release.
2. Replace the `groupBy: [catalog.cycle[...]]` clause in `analysisQuery`
   (`web/src/dashboard-analysis-model.ts`) with the native interval for
   catalogs whose records carry true timestamps.
3. Keep the ledger-hour path for usage, whose facts are day-grained.
4. Enable `onInspect` for cycle views in `web/src/dashboard-analysis.tsx`.
