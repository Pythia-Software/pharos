# Handoff: make tool rollups and findings incremental

## Assignment

Reduce the cost of updating tool rollups and findings after a small amount of
new agent activity. Aim for at least a 10× improvement over a measured baseline
on a realistically large library, with output parity against the existing full
rebuild. Profile first; treat this as a target to validate, not a promised result.

The user wants these analyses cheap enough to consider running after scheduled
indexing later. **Do not enable them on the scheduled path in this task.** The
current requested policy is that scheduled indexing updates conversations,
search, raw tool evidence, and immediate workspace projections, but leaves
expensive library analyses pending. Manual Index changes refreshes GitHub,
identity links, local Git integration, tool rollups, Human Words, and full
findings, even when the source scan finds no new changes.

The behavior change originates in the `manama` workspace. Start from a branch
that includes it, or integrate its final diff before changing the same paths.
Do not discard the separate authorship input-generation invalidation: messages
containing only user text must invalidate Human Words independently of tools.

## Observed costs and their limits

Read-only inspection of the user's running installation on October 8, 2026:

| Stage | Observation | Evidence quality |
| --- | --- | --- |
| Tool rollups | 29.629 seconds in the previous manual Capture & index | Difference between Git and Tools completion timestamps in `sync_deferred`, not an isolated benchmark |
| Full findings | 71.982 seconds in that run | Difference between authorship and findings completion timestamps |
| Latest full findings | 154.464 seconds | Explicit `meta.findings_took_ms`, built at `2026-10-08T20:15:04.152Z` |
| Library size | Approximately 64 GiB | Catalog file size; not the amount each query reads |

Historical per-stage CPU seconds, memory allocations, and I/O bytes were not
recorded. The 72-second and 154-second findings observations are different runs
and workloads/cache conditions; do not present their difference as a regression.
Establish a reproducible before/after baseline on identical input and cache state.
Illustrative targets are about 3 seconds for a small tool-rollup update and
7–15 seconds for findings if the measured baseline supports those comparisons.

The new manual pipeline records per-stage elapsed and process-wide CPU seconds
in `sync_history.sample_json.phases`, with names `github`, `identities`, `git`,
`tools`, `authorship`, and `findings`. `trigger` distinguishes manual/automatic
runs, and manual sample classes have a `manual_` prefix. CPU counters include
concurrent Pharos work and completed children; they are not hardware cycles or
exclusive attribution. RSS and Go allocations are likewise process-wide. OS
block counts are not byte counts. Use isolated benchmark processes for precise
comparisons, and report measurement limitations.

## Entry points

- `internal/archive/incremental_sync.go`: `automaticLoop`, `runAutomatic`,
  `runManualSync`, `runIncremental`. Both use the incremental writer; only the
  manual path finishes expensive analyses.
- `internal/archive/incremental_recovery.go`: `finishDeferredSyncPhases` runs
  ordered stages and advances completed generations only after success.
- `internal/archive/incremental_metrics.go`: existing resource/phase meter.
- `internal/archive/tool_ledger_store.go`: `replaceToolLedger`,
  `bumpToolLedgerGeneration`, `toolRollupStale`, `ensureToolRollup`,
  `rebuildToolRollup`, `publishToolRollup`.
- `internal/archive/tool_cube.go`: cube grouping/query definitions; locate
  `toolCubeSelect` and `toolRollupFromCube` if the file is renamed.
- `internal/archive/tool_paths.go`, `repository_merge.go`, `ingest.go`:
  repository/path attribution, mirror links, and writes that can invalidate
  more than the conversation currently being ingested.
- `internal/archive/findings.go`: `loadFindingEnv`, `RefreshFindings`,
  `runFindingsPass`; detector implementations live in `findings_*.go`.
- `internal/archive/findings_store.go`, `findings_verify.go`, `findings_drift.go`:
  persisted state, fixed measurement plans, backtesting, and intervention results.
- `docs/tool-analytics.md`, `docs/findings.md`, `docs/incremental-sync.md`:
  current contracts and existing profiling/test instructions.

## Why the current implementation is expensive

Tools stores individual calls incrementally, but a rollup generation change
rebuilds the entire materialized cube and daily summaries. The rebuild chooses
representatives for mirrored work, groups all calls into a temporary cube,
then replaces the published cube and summaries. Temporary construction reduces
writer contention, but it does not reduce the amount aggregated. Publication
can still hold the catalog writer while copying and replacing global tables.

Findings loads a broad environment spanning up to 91 days, including
conversation/provider/repository context, tool aggregates, token usage, model
requests, and authorship. A full pass runs detectors, persistence/backtests,
candidate deduplication, state updates, and intervention measurements. Repeating
these broad reads for one changed conversation is a likely optimization target.
The old daily gating policy is not an incremental algorithm: manual sync now
requests a full pass even if a findings generation already exists for today.

## Suggested approach

1. Profile representative no-op, one append, one corrected tool result, and
   several active conversations. Separate SQL execution, Go aggregation,
   temporary construction, publication, detector work, and backtesting. Examine
   query plans and rows visited before choosing indexes or caches.
2. Consider durable dirty sets and per-workspace/conversation summaries. Rebuild
   affected cube partitions from authoritative raw calls, then aggregate the
   smaller cube into affected daily dimensions. Partition replacement may be
   simpler and safer than subtracting arbitrary previous contributions.
3. For findings, consider cached per-conversation features and per-day detector
   inputs. Update changed entities and relevant rolling windows; preserve
   complete candidate discovery and the existing statistical/backtest behavior.
   Make feature loading lazy where only some detectors need it. Profile these
   hypotheses rather than assuming they dominate.
4. Record invalidation in the transaction that changes authoritative inputs.
   Preserve new dirtiness arriving while a build runs. Publish data and its
   completed generation atomically; readers must see an entire old or new build.
5. Keep a full reference/recovery path for first builds, rule/schema changes,
   broad invalidations, and repairs. A no-op should do almost no analysis work,
   while small updates should scale with affected entities rather than the
   whole library. Avoid moving expensive work into every ingest transaction.

## Correctness cases to cover

- Calls added, edited, pruned, reordered, or reclassified; delayed tool results
  and model usage that revise a call's earlier contribution.
- New mirror links or a different representative: previously counted work may
  need to be suppressed, restored, or reattributed.
- Repository merges, renames, path attribution changes, provider/model changes,
  price-book revisions, and rule-version changes.
- Day rollover and entries expiring from 28/91-day windows, including timezone
  boundaries and timestamps corrected after ingestion.
- Long-running conversations receiving new activity; do not restrict dirtiness
  solely to a conversation's original start day.
- Findings copied/watched/dismissed/snoozed/won, fixed intervention plans,
  savings, regression checks, and user-maintained state. No reset of user state
  or weaker thresholds just to improve speed.
- Cancellation, restart, competing writers, and another ingest occurring while
  summaries are built. Failed or interrupted work must remain dirty.
- Upgraded existing catalogs and forced full rebuilds. Compare canonical output
  against a separate full rebuild, excluding only documented volatile metadata.

## Validation and deliverables

Use scratch catalogs and controlled replicas. The installed library is
`/Volumes/euclid/Pharos/catalog/catalog.sqlite3`; production and native agent
inputs may be inspected read-only, but do not benchmark writes, force repairs,
alter settings, stop the user's app, or swap its binaries there. Do not copy a
live SQLite main file alone while ignoring its WAL; create a consistent replica
with SQLite's backup mechanism or an existing verified backup. Budget disk
space before materializing a 64 GiB replica.

Reuse/extend `incremental_bench_test.go`, `incremental_writer_test.go`, tool
ledger/cube tests, `incremental_recovery_regression_test.go`, and findings tests.
The existing publication-retry regression checks that a busy writer does not
force the temporary cube to be built again. Add meaningful parity and bounded
work tests for the scenarios above, then run the Go suite and scoped race tests.
Avoid noisy machine-wide timing assertions in ordinary unit tests.

Deliver the implementation, benchmark/profile artifacts, before/after wall and
CPU seconds, allocations/RSS, writer hold time, workload size, and cache state.
Show warm no-op and small-update scaling on a realistically large replica, plus
first/full-build cost. Explain any target not met and the remaining bottleneck.
Keep scheduled analysis disabled, and conclude with measured evidence for
whether a future change could safely enable it and at what cadence.
