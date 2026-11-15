# Query Table: shared metric reads for number, calendar and derived keys

**Resolved in Query Table 1.0.8**: the shared read groups by number fields,
and a host may assert that its source is deterministic
(`SQLiteSharedMetricsSource`), so the read is shared over a source that fails
the stored-source proof (derived rows, computed calendar columns). On the
replica below, the agent depth breakdown takes 2.8–3.1 s (from 17 s), and
views while conversations await refresh 4–6 s (from timeouts). Pharos keeps
the time-of-day cells on `/api/thinking`: on query-v3 they would put the
calendar's whole batch on the materialized read, 4.7 s against about 2 s
([measurements](../thinking-dashboards.md)).

Pharos consumes unmodified Query Table 1.0.7 (Go `backends/go` v1.0.7, npm
`@pythia-software/query-table-*` 1.0.7). No local dependency patches are
permitted; this is a request for upstream work. It follows
[`query-table-shared-clause-scans.md`](query-table-shared-clause-scans.md),
which 1.0.7 resolved.

## Problem

1.0.7 reads a metric batch's population once (`sqliteSharedMetrics`) when
every clause groups by stored text, enum or boolean fields of a source whose
projection passes the stored-source proof. Otherwise each clause reads the
population again, as before. Three of Pharos's reads still fall outside the
shared read:

1. **A number group key.** `rows_metrics.go` refuses a group field that is not
   text, enum or boolean (`grouping %s`). The Thinking breakdown by agent
   depth groups `model_requests` by `agent_depth`, a stored `INTEGER` column,
   so its 9–19 clauses each read the population.
2. **A calendar row and an hour of the day in the viewer's zone.** The Thinking
   time-of-day view counts each group's requests by row period (day, Monday
   week or month) and local hour, with a median wait and distinct
   conversations per cell. A metric takes one calendar key, so these cells
   group by computed columns (`pharos_zoned_time(requested_at, zone)`, or
   SQLite's `localtime`). Computed columns fail the stored-source proof.
3. **A source with derived rows.** While some conversations await a timing
   refresh, `model_requests` reads stored rows for the others `UNION ALL` rows
   derived again for those. That projection also fails the proof, so every
   view reads its population once per clause until the refresh completes.

## Evidence

Measured 2026-11-11 on a replica of a production catalog (APFS clone, modernc
SQLite v1.61, warm page cache), `model_request_timings` with 1,434,386 rows;
a 30-day range holds 297,071.

| request | shared read | time |
|---|---|---|
| Histogram, all history, by agent app (9 clauses) | yes | 2.6 s |
| Histogram, all history, by agent depth (the same 9 clauses) | no (number key) | 17 s |
| Calendar, 30 days, by model, without its two cell clauses | yes | 2.9 s |
| the two cell clauses alone (`quantiles` [.5] and `COUNT_DISTINCT`, grouped by model, `day`, `hour_of_day`) | no (computed keys) | 17.5 s |
| any view while conversations await refresh | no (derived rows) | 16 s to over 30 s |

Pharos answers the time-of-day cells from its own endpoint for now, from the
same rows it already loads to fit workload estimates for those cells, and
leaves the agent depth breakdown on query-v3 at 17 s.

What is measured and what is inferred: the times above are measured. That
the number key is what keeps the agent depth request off the shared read is
read from `rows_metrics.go` (`grouping %s`), not confirmed by a run; the
field's binding is `r."agent_depth"` with `expressionNumeric`, which may meet
another check. The derived-rows request has not been timed as a single read,
so how much (3) saves is unknown; its population is the stored rows of
conversations not awaiting refresh, `UNION ALL` the awaiting ones derived
again.

## Request

Any subset helps; each is independent.

1. **Group the shared read by number fields.** Integer-valued stored columns
   (here 0–5) as group keys, with the identity and ordering the per-clause
   path gives them.
2. **Two calendar keys, or computed keys, in the shared read.** Either let a
   metric's `dateGrouping` carry a cycle (`hourOfDay`) beside an interval
   (`day`, `week`, `month`) in one `timeZone`, read in the shared scan; or let
   the shared read group by a host-declared computed column (a deterministic
   expression over stored columns), evaluating it once per row.
3. **Share the read over a non-stored source.** Read the population of a
   source that fails the stored-source proof once, and run the batch's
   clauses over that one read, rather than reading it once per clause.

Semantics to preserve: as for 1.0.7's shared read, every clause's result,
diagnostics and error precedence as the per-clause path gives them; budgets;
`minCount`, `groupLimit` and revision checks; `COLLATE BINARY` group identity.

## Reproduce

A SQLite table of about 1.2 million rows: `id TEXT`, `conversation_id TEXT`,
`requested_at TEXT` (RFC 3339, indexed), `model TEXT`, `agent_depth INTEGER`
(0–5), `gap_seconds REAL` (null for about one row in ten). Send one V3 metric
request with nine clauses (count, `COUNT_DISTINCT([conversation_id])`,
`quantiles` of `[gap_seconds]`, ungrouped and grouped) grouped by `model`, and
the same request grouped by `agent_depth`; compare their times. For (2), add
`day` and `hour_of_day` fields bound to `date(requested_at,'localtime')` and
`CAST(strftime('%H',requested_at,'localtime') AS INTEGER)` and group a
`quantiles` clause by `model`, `day`, `hour_of_day`.

## Where Pharos stands

The Thinking dashboards read query-v3 on branch
`gbdubs/dashboards-qv3-7-thinking` (PR #645). Done when a published release
lets the agent depth breakdown take the shared read (1); Pharos can then move
the time-of-day cells back to query-v3 (2) and stop slowing down while
timings await refresh (3), each by a normal version bump.
