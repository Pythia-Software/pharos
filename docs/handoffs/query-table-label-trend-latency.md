# Query Table SQLite distinct reduction optimization

**Implemented upstream in [Query Table 1.0.7](https://github.com/Pythia-Software/query-table/releases/tag/v1.0.7)
and adopted in Pharos.** Eligible text distinct metrics share filtered, validated
inputs across grouping lists automatically; no host opt-in is needed. The
original request and 1.0.5 measurements below remain as the baseline.

## Pharos integration verification

On November 11, 2026, the attached standalone reproduction was run serially
against the published 1.0.5 and 1.0.7 Go modules on the same Mac, with modernc
1.61.0, the same 972,298 generated observations and three uncached executions
per version. Every execution matched all 360 day/family and 90 day native
buckets. Fixture setup and the native comparisons were outside the executor
timer.

| Version | First execution | Subsequent executions | Subsequent median |
| --- | ---: | --- | ---: |
| 1.0.5 | 6.588 s | 6.587 s, 6.711 s | 6.649 s |
| 1.0.7 | 4.508 s | 4.487 s, 4.498 s | 4.493 s |

The subsequent median fell approximately 32.4%. Native reference timings stayed
close between the runs: about 1.45–1.48 s for day/family and 0.83–0.85 s for day.
These synthetic observations establish the benefit of the released executor on
this fixture; they are not a measurement of the installed Pharos app or a
production latency guarantee.

All four Pharos npm packages and the Go module are pinned to 1.0.7. Schemas and
frontend assets are regenerated from the published packages. The installed
frontend files in core, React, UI and codegen are byte-identical to 1.0.5 except
their package manifests, so the existing native URL-state ownership still
applies. Pharos's `TestMessageLabelFacts` also exercises the batched V3 trend
against the actual label projection, including sibling labels within one family,
overlap across families, coverage observations and a parent-label filter.

## Request for the Query Table team

Please optimize exact text `COUNT(DISTINCT ...)` metrics in the Go SQLite V3
executor, especially a batch over the same filtered source with different
grouping lists: `[day,family]` and `[day]`. Pharos's Labels dashboard takes
approximately 20–23 seconds to compute this request over 972,298 observations.
The captured stack points to the general reduction materialization path in
Query Table v1.0.5. A synthetic reproduction also shows substantial executor
overhead without Pharos's source joins or any private library data.

Investigate repeated source reads, datetime filtering and typed validation,
distinct-key sorting, and temporary reduction materialization. Reuse work where
the source, filters, scope and snapshot agree, while keeping the two distinct
counts independent: a message can belong to several families, so family totals
cannot be summed to obtain the denominator. Choose the optimization after
profiling; the captured stack does not identify every contributor to the cost.

Deliver an upstream implementation with focused correctness tests, a repeatable
benchmark and profiles explaining the improvement, and a published release
Pharos can consume. Scope this work to the shared SQLite executor and its tests.

Investigation date: November 10, 2026, America/Denver.

## Finding

The Labels dashboard's first uncached 90-day load spends its time executing
SQLite reductions, rather than serving HTML, waiting for a query slot, waiting
for the catalog writer, or running the label classifier. The expensive request
is `POST /api/query/message_labels/query-v3` using Query Table **v1.0.5**.

The original request completed at approximately 02:04:50 UTC on November 11
(November 10 in America/Denver). Its service diagnostic recorded:

| Measurement | Original request |
| --- | ---: |
| Total | 19.74 s |
| Database statements | 19.72 s across 30 statements |
| Longest statement | 8.17 s |
| Writer, derived-value and query-admission waits | 0.00 s each |

The five-second contention report captured the request executing
`sqliteReductionCache.crossFieldPlan`, at the general reduction's
`CREATE TEMP TABLE ... AS ...` in `backends/go/sqlite_v2_cross_field.go:137`.
This establishes an expensive reduction stage, not which individual stage
accounts for every second of the request. Background analysis was running, so
CPU and drive contention can still affect elapsed database time.

## Reproduction on the user's library

The URL selects distinct messages, grouped daily by General Label. Its default
presentation is a share of all distinct messages in the interval and a seven-day
rolling window. Consequently it asks for **two distinct-count metrics**, with
six days of history before the visible 90-day range. Labels overlap: summing
the family counts cannot replace the distinct-message denominator.

The actual source contained **972,298 label observations**, not 972,298 distinct
messages. The response contained 938 day/family buckets and 90 denominator
buckets. This library's catalog was approximately 103 GiB. Stored label-query
facts were ready, and their dirty queue was empty when inspected.

| Read | Elapsed | Observation |
| --- | ---: | --- |
| Original page request, service diagnostic | 19.74 s | Database work dominates |
| Same request replayed with cache policy `live` | 22.635 s | Successful, exact response |
| Same clock and metrics, both select lists narrowed to `id`, policy `live` | 26.914 s | Metrics exactly identical; narrowing did not help |
| Original request, policy `cached` | 0.042 s | Existing fresh entry; metrics exactly identical |

These are single local observations, not latency guarantees or machine-cold
benchmarks. They were serialized; production was not restarted, migrated,
reindexed, or modified directly. Requests used the running service and existing
authentication. The browser inspection host was unavailable, so there is no
browser network trace or measurement of browser rendering time.

A read-only native SQLite reference over the stored facts returned identical
bucket keys and counts for both metrics. The straightforward statements took
6.755 s and 2.648 s; a subsequent version using the existing time covering index,
conservative raw timestamp candidates, and facts-first join order took 2.723 s
and 3.010 s. These omit the live-pending branch and Query Table's typed
validation, error semantics and resource checks, and use Python's SQLite rather
than the service's modernc driver. They demonstrate useful optimization room;
they are not a drop-in execution path or a controlled estimate of its speedup.

## Suggested investigation points

The captured stack establishes the general reduction path; it does not establish
that an integer speculation failed.

Relevant Query Table files in `backends/go/`:

- `query_v3_direct.go`: ungrouped population count and metric execution.
- `sqlite_v2_dataset.go`: metric planning, isolation and result reading.
- `sqlite_v2_cross_field.go`: reduction materialization. Its compatibility key
  includes the exact grouping list, so these two groupings are separate groups.
- `sqlite_v2_reuse.go`, `metrics.go`: reuse and general typed reduction plans.
- `query_v3.go`: source dependencies; `rows:false` already drops selected row
  fields, explaining why narrowing the frontend's selection did not help.

Use a published upstream release for any executor change. Pharos must not patch
the module cache, vendor the executor, introduce a special local reducer, or
replace distinct counts with approximate counts. Pin the four npm packages and
Go module together when integrating a release.

## Standalone reproduction

The accompanying [Go reproduction](query-table-label-trend-repro.go.txt) contains
the schema, function registrations, synthetic input, request, native references
and exact bucket comparisons. It imports only Query Table and modernc, opens an
in-memory SQLite database, and requires no Pharos checkout, service, token, saved
library or `.context` files.

Copy the attachment into a disposable directory as `main.go`, then run:

```sh
go mod init example.com/query-table-distinct-repro
go get github.com/Pythia-Software/query-table/backends/go@v1.0.5 modernc.org/sqlite@v1.61.0
go run . -messages 2000 -repeat 1
go run . -repeat 3
```

The first run is a small smoke check. The second uses the measured fixture size
of 486,149 messages and 972,298 label observations. Input generation is outside
the timers. Each repetition executes Query Table and both native statements on
the same immutable database through the same modernc driver, checking source
counts, metric errors, bucket keys and exact values. Each Query Table request
uses a fresh transaction and computes its result without a result cache.

For the comparison after implementing a fix, change only the Query Table
dependency version, keep modernc and the fixture fixed, and run on an otherwise
idle machine. Report the first execution and the median of subsequent
executions, plus allocation/CPU or SQL execution evidence for the changed path.

### Synthetic schema and request

This fixture contains no library content. Run in a disposable SQLite database
with Query Table's normal scalar and aggregate function registrations. It makes
972,298 narrow observations, with two overlapping labels per message:

```sql
CREATE TABLE observations (
  id TEXT PRIMARY KEY, message_id TEXT, day TEXT, family TEXT, occurred_at TEXT
);
WITH RECURSIVE n(i) AS (
  VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<486149
), labels(j) AS (VALUES(0),(1))
INSERT INTO observations
SELECT printf('%064d:%d',i,j), printf('%064d',i),
       date('2026-08-13','+'||(i%90)||' days'),
       'P.family'||((i+j)%12),
       strftime('%Y-%m-%dT%H:%M:%SZ','2026-08-13','+'||(i%90)||' days')
FROM n CROSS JOIN labels;
CREATE INDEX observation_time
  ON observations(occurred_at,day,message_id,family);
```

Bind `id`, `message_id`, `day`, and `family` as text, and `occurred_at` as an
RFC3339 datetime, with `id` as identity. Make text fields groupable. Use
`occurred_at DESC NULLS LAST` as default order and `id ASC` as tiebreak order.
The authorized `SourceSQL` is:

```sql
SELECT id,message_id,day,family,occurred_at FROM observations
```

Run `QueryDatasetV3.Execute` with SQLite, `temp_store=MEMORY`, max rows 10,000,
max groups 100,000, max population 10,000,000, max staging bytes 512 MiB, and the
following request:

```json
{
  "version": 3,
  "profile": "qt-sqlite-v1",
  "now": 1794362816119,
  "rows": false,
  "source": {"select": ["id"], "limit": 100, "offset": 0},
  "result": {
    "select": ["id"], "limit": 100, "offset": 0, "orderBy": [],
    "where": [
      {"field": "occurred_at", "op": "<", "value": "+0s"},
      {"field": "occurred_at", "op": ">=", "value": "2026-08-06T00:00:00-06:00"}
    ]
  },
  "metrics": [
    {"id": "trend", "op": "count_distinct", "field": "message_id", "groupBy": ["day", "family"]},
    {"id": "trend:population", "op": "count_distinct", "field": "message_id", "groupBy": ["day"]}
  ]
}
```

Compare timings and bucket values with native `COUNT(DISTINCT message_id)`
grouped by each key set, using the same modernc driver, snapshot and timestamp
predicates. Keep synthetic and production measurements separate. The production
source additionally joins stored facts/memberships/names and unions live pending
conversations; the synthetic fixture isolates executor overhead.

The attached standalone reproduction passed with both 4,000 and 972,298
observations through v1.0.5 and modernc. The full fixture returned 360 day/family
buckets and 90 denominator buckets; every bucket's keys and count matched the
two native statements. Its verification run took 16.089 s through Query Table
versus 3.149 s and 1.594 s for native SQL. These are single local observations,
not a controlled benchmark or a forecast of production improvement. Use the
repeated before/after procedure above to evaluate a change.

## Acceptance criteria

- Preserve exact distinct counts for overlapping labels within one family and
  across families. The `[day]` denominator counts each message once, independent
  of its number of labels.
- Match existing results for NULL and empty text, empty populations, selective
  source and metric filters, and different grouping lists and metric scopes.
- Preserve invalid-storage diagnostics, metric-local error isolation, computed
  revisions, group budgets, cancellation and temporary-table cleanup. Extend
  existing upstream tests for these contracts.
- Demonstrate a material reduction in uncached execution time on the attached
  fixture with identical bucket keys, values and error behavior. Record the
  before/after version, driver, machine, first execution and repeated timings.
- Explain the reduced work using a profile or execution counters, and check a
  smaller/selective workload so that reuse does not add a large fixed cost.

The native reference measures the available optimization room; it omits Query
Table's complete validation and diagnostic contract. Preserve that contract and
keep counts exact. A Pharos result-cache hit cannot establish an executor
improvement.

## Release and Pharos integration

Publish the fix upstream and return the PR link, release version and benchmark
results. Pharos will consume the published implementation by pinning the four
npm packages and Go module to the same release, generating schemas as needed,
rebuilding the frontend and running its integration checks. Local module-cache
patches, vendoring and special Pharos reducers are outside this integration.

Relevant host code for understanding the source and request:
`web/src/message-labels-catalog.ts`, `web/src/dashboard-trend-model.ts`,
`internal/archive/message_label_facts.go` and
`internal/archive/query_table_v3.go`. The standalone reproduction does not depend
on these files. Pharos loading feedback and planner-statistics experiments are
separate host work.
