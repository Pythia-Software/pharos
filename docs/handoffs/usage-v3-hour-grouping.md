# Usage: group and measure by `hour` through /query-v3

## Problem

The usage dataset has a `hour` dimension, but only `/metrics` honours it.
`/query-v3` silently groups `hour` over daily usage records, where it is null.
This is a Pharos host bug in `internal/archive`, not a Query Table bug.

- **Daily rows have no hour.** Daily usage rows are built without `hour`:
  `usageRowsFromLedger` deletes it when it is not building hourly rows
  (`internal/archive/usage_table.go`, the `if !hourly { delete(row, "hour") }`
  branch).
- **`/metrics` handles it.** It sends metrics whose dependencies include
  `hour` to the hourly ledger: `usageHourMetrics`, `hourlyUsageForAggregation`
  and `usageMetricsInMemory` in `internal/archive/query_table_v2.go`.
- **`/query-v3` does not.** It reads the hourly ledger only to select daily
  records by a `WHERE hour` filter (`internal/archive/query_table_v3.go`, the
  `if dataset == "usage" && slices.Contains(whereFields(where), "hour")`
  branch). Neither a grouping key `hour` nor a metric grouped by `hour` reads
  hourly rows.

### Effects

- **"View supporting query" for an hour-of-day cycle cannot be grouped by
  hour.** The Tokens and Cost cycle view
  (`web/src/dashboard-analysis-model.ts`, `analysisQuery`) would get one group
  with a null hour. It therefore opens the records instead and says so in its
  note.
- **A metric grouped by `hour` that falls back to `/query-v3` comes back as
  one null-keyed bucket.** This fallback happens when `/metrics` refuses it
  for its group budget (`runAnalysisQuery` in `web/src/analysis-request.ts`).
  The cycle fold now rejects a null key with an error ("Some totals have no
  hour"), rather than drawing zeros. The Tokens trend never falls back.

## Change

In the `/query-v3` handler for usage, apply the split `/metrics` uses:

- When the grouping keys or any metric depend on `hour`, use the hourly rows
  that `hourlyUsageForAggregation` selects as the source.
- Otherwise keep the daily rows.

A grouping stage keyed by `hour` must then group hourly rows. Summaries are
exact for the additive token and cost fields. Distinct counts stay correct,
because an hourly row keeps its session, conversation and model keys.

## Done when

- A Go test seeds usage over several hours. It shows that `/query-v3` with
  `grouping.keys = ["hour"]`, and with a metric `groupBy: ["hour"]`, returns
  the same per-hour totals as `/metrics` `groupBy: ["hour"]`.
- In `analysisQuery`, the hour cycle's `supporting` query becomes the grouped
  `hour` query again, as for the weekday cycle. Its unit test in
  `web/tests/unit/dashboards/dashboard-analysis.test.mjs`
  ("a cycle query reads the ledger's hourly or daily totals") is updated to
  match. The note in `analysisNote` drops "cannot yet open as a grouped
  query".
