# Production-data query performance — October 15, 2026

**169 of 183 measured query shapes have a median below 500 ms. All 183 complete
successfully. The 500 ms goal remains unmet for 14 shapes**, chiefly full-history
raw tool metrics, large groupbys, calendar metrics, and representative rows.

The isolated development service is at `http://127.0.0.1:8799/`, backed by
`/Volumes/euclid/pharos-dev-delhi`. It was freshly copied with SQLite backup,
including committed WAL contents, from the recently indexed production catalog.
Production was read only. Capture/preserved files use APFS clones and automatic
sync is disabled in the development copy. Other workspaces' dev libraries and
the installed production service were left running.

## Dataset and measurements

- Approximately 72 GiB catalog; 34,658 workspaces.
- 1,486,762 raw tool calls; 993,820 calls after the trusted joins/mirror exclusion.
- 66,505 tool summary rows; 28,492 classified messages; 7,026 skill usages.
- Ten datasets, rows/default projections, paging, numeric ordering/filtering,
  scalar/grouped metrics, formulas, median, HAVING, multiple keys, distributions,
  calendar buckets, representatives, OR/negation, text/regex/array filters, and
  20-metric batches.
- Timings cover HTTP, execution, enrichment, and response bytes. The baseline
  has one serialized observation per shape; final timings use three consecutive
  observations. Neither OS caches nor SSD caches were flushed. A shape's first
  request is **not** a machine-cold measurement. Errors are recorded separately
  and never count as fast successful queries.

Selected results (milliseconds):

| Shape | Before | Final median | Final first request |
| --- | ---: | ---: | ---: |
| Library default page | 396 | 267 | 466 |
| Usage default page | 753 | 287 | 299 |
| Usage count | 636 | 25 | 26 |
| Human Words default page | 10,797 | 8 | 39 |
| Human Words SUM/AVG/MIN/MAX batch | 4,310 | 171 | 374 |
| Human Words groupby | 6,794 | 399 | 376 |
| Tool calls default page | 3,515, rejected by population budget | 10 | 46 |
| Tool calls count | 3,518, rejected by population budget | 6 | 15 |
| Recent tool-call page | 2,821 | 9 | 226 |
| Recent grouped tool-call box distribution | — | 436 | 450 |
| Recent 20-metric tool-call batch | — | 392 | 404 |
| Full-history tool-call groupby | 30,007, deadline | 7,535 | 8,979 |
| Full-history provider/repository tool-call groupby | 30,023, deadline | 12,218 | 12,218 |

Measurements are local observations, not latency guarantees. A first command
search still takes about 2.2 seconds; following requests are about 105–118 ms.

## Changes retained

1. Discover all required source dependencies, including computed inputs and
   hidden/default ordering, before staging. Apply this to every map dataset and
   project SQL datasets too. Remove unused optional joins to unique keys.
2. Batch temporary inserts below 999 parameters. Load hourly usage only for
   requests that consume hour; keep daily filters and shown-row expansion intact.
3. Add chronological and typed numeric paging indexes, plus separate workspace
   and timestamp covering indexes for common tool analytics. Create the analytics
   indexes after legacy column migrations. Keep conversation previews/counts on
   the conversation index despite changing planner statistics.
4. Page indexable SQL row requests before typed selected-value/computed execution.
   Prefer paging indexes explicitly when broad joined plans would sort the full
   ledger, and preserve selective identity lookup plans. Date/ID candidates still
   receive the complete canonical predicate.
5. Cache exact counts by the generation read **inside the execution transaction**.
   The unfiltered tool count can use the cube's indexed counts only when ledger
   generation, dirty partitions, full revision, day and timezone all match in
   that transaction. Stale cubes fall back to authoritative raw rows. Pure COUNT
   metrics retain the normal adapter's validation/revision/result envelope.
6. Keep main connection caches at 32 MiB with a 256 MiB mmap window. Lease/restore
   scratch settings; small summary/map populations use memory scratch, while raw
   payload populations retain disk spill and the existing staging-byte guard.
7. Install bounded planner statistics for query-source tables and new indexes.
   Avoid unrestricted startup `PRAGMA optimize`: the experiment took 121 seconds
   on this catalog. Do not invalidate Library projections for an equivalent
   COUNT index hint.

Indexed row windows allow up to 10 million matching source rows while executing
typed/computed selections on the bounded page. Covered raw tool scalar metrics
allow up to 2 million matching rows. Other V2 populations retain 250,000; V3
retains its 10-million-row/512-MiB staging limits. Exact sample and group budgets,
canonical revision checks, cancellation, and durability settings remain enforced.

## Remaining bottleneck

The slow-shape list and samples are in `.context/performance/complete.json`.
Remaining medians include full tool reductions (~1.75 s), grouped formulas and
median (~2.4 s), full raw groupbys (~7.5–12.2 s), raw calendar grouping (~4 s),
raw representatives (~2.5 s), Tools representatives (~1.6 s), and some multi-key
summary/Human Words groupbys (~0.66–0.72 s).

A native SQLite reference query doing provider COUNT and SUM over the same
993,820 representative calls took 1,088 ms initially and 339–351 ms warm. This is
not the complete Query Table request: it omits typed/runtime diagnostics and the
general grouped-result pipeline. It demonstrates that a narrow single-pass
reduction can meet the target on this data.

The pinned Query Table 0.7.0 V3 executor checks source bytes, copies the source,
checks it again, and then stages count and summaries in additional passes.
Profiles show substantial B-tree construction, sorting, allocation and scratch
I/O. A further improvement needs combined grouped reductions and reuse of a
validated narrow population in that adapter, with exact numerical, error,
representative, calendar and budget semantics preserved. No approximate result,
silently truncated population, or skipped storage guard was introduced here.

## Reproduce and verify

```sh
tools/dev.sh --destination /Volumes/euclid/pharos-dev-delhi --port 8798 --ui-port 8799 --reuse-library
python3 tools/query-bench.py \
  --config /Volumes/euclid/pharos-dev-delhi/library.toml \
  --cases .context/performance/cases.json \
  --output .context/performance/recheck.json --repeat 3
GOWORK=off go test ./...
GOWORK=off go vet ./...
GOWORK=off go test -race ./internal/archive \
  -run 'TestQueryCount|TestQueryIndexedRows|TestQueryIndexedCount|TestQueryV2TempStorageLease|TestQueryV3TempStorageLease'
```

The benchmark creates its request fixture if absent; retain it to compare the
same clock and filters. Opt-in profiles in `performance_probe_test.go` require
`PHAROS_QUERY_PROBE` to point to a dev catalog with a `.pharos-dev` marker, and
open the source read only without initialization.

Validation includes 169 successful paired queries against a read-only server
built from the original commit on the **same** frozen development database:
zero differences (numeric comparisons tolerate 1e-12 relative/1e-9 absolute).
The original's 14 rejected/deadline shapes are covered by regression fixtures
and the successful final API run. Full Go tests, vet, scoped race tests, frontend
typecheck and nine development-snapshot tests pass. Artifacts, request fixtures,
profiles, parity results and detailed samples stay in `.context/performance/`.
