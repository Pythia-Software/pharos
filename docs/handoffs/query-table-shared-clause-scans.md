# Query Table: one scan for a request's clauses

Pharos consumes unmodified Query Table 1.0.5 (Go `backends/go` v1.0.5, npm
`@pythia-software/query-table-*` 1.0.5). No local dependency patches are
permitted; this is a request for upstream work. It builds on
[`query-table-row-pipeline-cost.md`](query-table-row-pipeline-cost.md), which
is about the cost of each clause. This one is about how many times a request
reads its source.

## Problem

A dashboard that reads several summaries of one population sends one metric
request with several clauses. The Go backend evaluates each clause in its own
statement, so the request reads and validates its population once per clause.
On a large SQL source that multiplies a cost of seconds by the number of
clauses.

Pharos's Thinking dashboards (Waiting and Thinking time, `model_requests`)
need these clauses per view, all over the same `where` (the Range, Model and
Effort filters):

| view | clauses | date-grouped | distributions |
|---|---|---|---|
| Time (periods) | 19 | 8 | 2 |
| Histogram, Reasoning, Wait box, Heatmap | 9 | 0 | 2 |
| Calendar | 13 | 0 | 3 |
| Waiting overview | 16 | 0 | 5 |

The clauses differ in `groupBy` (none, the breakdown, the breakdown plus the
wait band) and in a clause `where` (timed requests only). They are counts,
`COUNT_DISTINCT([conversation_id])`, `quantiles` distributions and, for
periods, `PERCENTILE([gap_seconds],p)`. Each page also sends a second request
counting requests by model and by effort over the whole population, for the
Model and Effort choices.

## Evidence

Measured 2026-11-10 on a replica of a production catalog (APFS clone,
modernc SQLite v1.61, warm page cache; the service's own background
maintenance was running at the same time). The source is a stored table
(`model_request_timings`, 1,434,386 rows; 1,229,773 after exclusions). The
30-day range holds 297,071 rows, of which 262,121 are timed.

The same reads written by hand, through the same driver:

| statement | time |
|---|---|
| 30-day count | 0.49 s |
| 30-day count and distinct conversations, grouped by model | 0.72 s |
| every timed gap in 30 days, ordered by model and value (enough for every exact quantile) | 0.87 s |
| all history, count grouped by model | 2.4 s |

What the compiled clauses cost:

| clause | time |
|---|---|
| a 30-day date-grouped scalar clause: budget probe, then the staging insert that repeats it | 2.2–3.9 s + 2.2–3.9 s |
| a 30-day `quantiles` or count-distinct reduction | 1.6–6 s |
| all history, count grouped by model (the choices request; the integer reduction) | 2.3–4.7 s |

Whole views, against the endpoint they replace (`/api/thinking`, which reads
the population once into memory and caches it):

| view | `/api/thinking` | query-v3 |
|---|---|---|
| Time, 6-hour periods, 7 days | 49 ms | 13.9 s |
| Reasoning, 30 days, by model and effort | 184 ms | 15.8 s |
| Histogram, all history, by agent app | 405 ms | over the 30 s limit |
| Time, daily periods, 30 days, by model | 42 s cold, then under 1 s | over the 30 s limit |

Where the query-v3 views finished, their counts, percentiles, wait bands,
periods and cells matched `/api/thinking` exactly. Only the time is a problem.

Pharos also tried a plain stored source (no derived rows for conversations
awaiting refresh, raw label columns), so that more clauses take the
stored-source and integer paths. That saved about a quarter (Time 7 days
10.6 s, Reasoning 14.1 s, Histogram 21.1 s). Date-grouped clauses still
stage separately, and each clause still reads the population on its own.

## Request

Any one of these makes the Thinking views fast enough. The first two keep
SQL execution; the third replaces it for hosts that already hold their rows.

1. **Share one scan across a request's clauses.** Clauses over the same
   source and request `where` should read the population once. They may
   differ in `groupBy`, in a clause `where` and in operation (count,
   count-distinct, sum, `quantiles`, `PERCENTILE`). `directDistributions`
   already shares one native scan across distribution metrics over stored
   fields. Extending it to counts and distinct counts, to different
   groupings of the same rows (grouping sets) and to clause filters would
   cover every clause above.
2. **Stage a date grouping once, and let it take distributions.** Clauses
   with the same `dateGrouping` (field, interval, `every`, `timeZone`) and
   `where` should share one staged calendar source. Date grouping should
   also accept `quantiles`, so the four percentile lines of a period chart
   are one distribution rather than four `PERCENTILE` clauses, each of which
   stages the population again. Budgeting while staging (§4 of the
   row-pipeline handoff) removes the second pass inside each clause.
3. **Or: a V3 executor over host rows.** `ExecuteV2Rows` runs V2 metrics
   over rows a Go host holds in memory. A V3 equivalent covering
   `quantiles`, `PERCENTILE`, `dateGrouping` with `every` and `timeZone`, and
   clause `where`, with the results the SQLite backend returns (the
   TypeScript core's `evaluateMetrics` is the reference), would let Pharos
   answer from the per-generation snapshot `/api/thinking` already keeps.

Semantics to preserve: every clause's result, diagnostics and error
precedence as today; population and byte budgets per request; `minCount`,
`groupLimit` and revision checks; `COLLATE BINARY` group identity; empty
periods omitted with `includeEmpty: false`.

## Reproduce

A SQLite table of about 1.2 million rows: `id TEXT`, `conversation_id TEXT`,
`requested_at TEXT` (RFC 3339, indexed), `model TEXT`, `effort TEXT`,
`gap_seconds REAL` (null for about one row in ten). Send one V3 metric
request with a 30-day `where` on `requested_at` and these clauses:

- `count`, and `COUNT_DISTINCT([conversation_id])`, with no grouping;
- `quantiles` of `[gap_seconds]` at 0.5, 0.75, 0.9 and 0.95, with clause
  `where` `gap_seconds is_not_null`;
- the same three grouped by `model`;
- with `dateGrouping` `{field: requested_at, interval: day, every: 1,
  timeZone: America/Denver}` grouped by `model`: `count`,
  `COUNT_DISTINCT([conversation_id])` and `PERCENTILE([gap_seconds],p)` for
  each of the four probabilities.

Compare the request's time with one hand-written statement that reads the
30-day rows ordered by model and gap.

## Where Pharos stands

The Thinking migration is written and verified against `/api/thinking`, on
branch `gbdubs/dashboards-qv3-7-thinking` (draft PR). It stays unmerged
until a release delivers one of the requests above, because it would turn
sub-second views into timeouts on a large library. When it lands, Pharos
should also give `model_requests` a plain stored source while its stored
timings are current (labels normalized when stored, and the source
reading derived rows only for conversations still marked for refresh), so
clauses can take the stored-source paths.

Done when a published release answers the Time view's 19 clauses over the
30-day population above within about twice the hand-written read (about 2 s
here), and Pharos consumes it by a normal version bump.
