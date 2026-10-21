# Query Table manual interventions: audit and upstream handoff

For the Query Table team. Audited **2026-10-21** against Pharos
`b454ffe` (origin/master, including #174). Paths below are in Pharos unless
explicitly marked **Query Table**. This consolidates the historical interventions
and remaining performance work; it does not reopen requests already delivered.

## 0.9.3 follow-up

Query Table 0.9.3 supplies cross-field V2 scalar fusion and guarded direct
integer reductions (request A / QT-14), plus a tested caller-owned transaction
contract and cleanup-error fixes (the library portion of request C). These are
normal released dependencies; Pharos needs no local library patches.

Pharos now pins the Go module and all four npm packages to **0.9.3**, with
registry/checksum provenance and no replacements or package symlinks. Schema
regeneration produced no changes; the ignored frontend bundles were rebuilt
from the locked packages. The upgrade includes the release's updated virtualizer
and Temporal dependencies.

Request B was investigated upstream. Copying already typed source rows was a
small part of the measured V3 cost; validation and projection cost much more.
The release retains V3 staging and provides no direct V3 grouping API. See
[the upstream response and benchmarks](https://github.com/Pythia-Software/query-table/blob/v0.9.3/docs/sqlite-performance-handoff.md).

The generic SQL host reducers remain enabled. Feature availability and parity
are necessary but do not establish that removing the reducers preserves
interactive performance. `TestUsagePerformanceSQLHost` in
`internal/archive/usage_performance_test.go` compares complete authenticated
HTTP requests, including preparation, dependency discovery, staging leases,
serialization and cleanup, over 50,000 synthetic authored-message facts with
nine integer fields. Both paths use the same source, catalog and indexes;
the fixture uses no tool cubes. It checks identical responses and successful
metric/bucket results, reports timings, and has no timing assertions.

Warm wall-time medians on the same Mac, Go 1.26.8 and Pharos's modernc SQLite
1.39.1, with one discarded warmup and three measured requests per path:

| Complete HTTP request | Library 0.9.2 | Library 0.9.3 | Host reducer on 0.9.3 |
|---|---:|---:|---:|
| V2 count + nine sums, scalar | 353 ms | 44 ms | 15 ms |
| V2 count + nine sums, grouped by day | 1,015 ms | 66 ms | 29 ms |
| V2 grouped, filtered by provider | 726 ms | 70 ms | 32 ms |
| V3 grouped count/sum/average, paging + result metrics | 1,245 ms | 299 ms | 33 ms |

Every compared HTTP response was identical. These are synthetic, local warm
measurements, not a production-catalog or concurrent-dashboard latency claim.
The HTTP routes include Pharos's current rollback/PRAGMA policy. The provider
filter matches 30,000 of the fixture's 50,000 rows. The reproducible command is:

```sh
PHAROS_TEST_PROCESSES=1 go test -tags probe \
  -run '^TestUsagePerformanceSQLHost$' -v -count=1 ./internal/archive
```

**Removal decisions:**

- **V2 SQL reducer:** no missing cross-field API or observed parity blocker
  remains for the measured cases. Removing `nativeMetricsV2` and routing its
  callers to ordinary adapter execution would exchange the custom path for
  roughly 2–3 times its warm latency in these cases. Keep it enabled for now;
  removing it is a measured performance/simplicity decision, or further
  optimization work, rather than waiting for QT-14 to be implemented. Retain
  existing error, budget and revision coverage during removal. Some helpers
  (`nativeGroups`, `nativeTableBounds`, bucket sorting) are also used by V3 or
  domain-specific Machine Time paths, so the entire file cannot simply vanish.
- **V3 SQL pre-reducer:** keep `nativeGroupedRows`. The tested general route
  remains about nine times slower despite the improvement in 0.9.3. Further
  upstream reduction of validation/projection/grouping cost is needed to make
  removal preserve this performance; a direct plan is one possible strategy,
  not a requirement to bypass validation or budgets.

The remaining host scratch-lifecycle work is tracked in
[query-table-schema-reload.md](query-table-schema-reload.md). Successful query
commits require dropping Pharos's own staging tables, propagating cleanup and
commit errors, and selecting a stable connection policy instead of toggling
`temp_store` on every request. Query Table already owns cleanup of its objects;
failed or cancelled executions must still roll back. This is host integration
work, not a request for another upstream cleanup hook.

The computed-preview transport request (QT-15) is unchanged. Pharos also uses
legacy `fetchRows` for Library's **Select all** share/export action, so retiring
the legacy row route requires migrating that host caller as well as upstream
preview negotiation. Domain-specific sources, authorization, cubes, freshness,
pricing, hourly facts and indexed candidates remain appropriate host code.

### Upgrade validation

- `tools/setup.sh`, schema regeneration (no generated changes), and
  `go mod verify` pass. All four installed npm packages match their registry
  tarballs byte for byte, with lockfile integrity verified and no package links.
- `go test -short ./...` and `go test ./...` pass against the rebuilt 0.9.3
  frontend assets and the published Go module.
- `npm --prefix web run check` and `npm --prefix web run test:full` pass,
  including all 629 unit tests and the full card/cover matrix.
- The browser suite passes all 207 tests in Chrome and all 207 in WebKit,
  with no failures, skips or todos.
- The synthetic full-HTTP timing/parity probe above passes on both releases.

The initial WebKit run exposed a Pharos header-preview ResizeObserver loop.
Both failing tests reproduced on an isolated, unmodified 0.9.2 baseline.
`internal/archive/assets/header.js` now applies preview sizing in the next
animation frame and only when widths change; the existing focus and responsive
browser tests pass. This is a host UI fix, with no Query Table source changes.

## Original request (0.9.2 baseline)

Please make ordinary Query Table execution efficient enough that hosts can
delete their own SQL reduction implementations. Start with **cross-field V2
metric fusion (QT-14)**, then measure the opportunity to avoid copying raw
sources for simple V3 grouped queries. Also establish a supported SQLite
temporary-table/transaction lifecycle with hosts. Publish applicable changes
through the normal aligned Go and npm releases, with migration guidance and
portable benchmarks.

Pharos previously modified the library itself, but **no such patches remain
in the audited checkout**. Query Table v0.9.0–v0.9.2 already replaced the recent
fork's optimizations, budget override and Pharos's in-memory Usage reducer.
The outstanding interventions described below live in Pharos host code. Please
adapt the general mechanisms rather than incorporate Pharos's domain logic.

## What was checked in the original 0.9.2 audit

- `go.mod` and `go.sum`: published Go backend **v0.9.2**, no replacement.
  Both ordinary `go env GOWORK` and a filesystem check showed no active
  `go.work` override in this workspace. `GOWORK=off go list -m -json
  github.com/Pythia-Software/query-table/backends/go` selected v0.9.2 without
  `Replace`; `go mod verify` reported all modules verified.
- `web/package.json` and `web/package-lock.json`: core, React, UI and codegen
  are all pinned to registry **0.9.2**, with integrity hashes. Before reinstalling
  anything, all installed files in those four packages were compared byte for
  byte with their registry tarballs, after verifying each tarball against its
  lockfile integrity. There were no differences, extra files or package symlinks.
- Tracked files and build scripts: no remaining `third_party/query-table-go`,
  `patches/query-table`, `web/vendor`, fork test gate, source-packaging script,
  or package-patching install hook. Frontend bundles are ignored build outputs.
  `tools/setup.sh` reinstalls locked packages and rebuilds them normally.
- `tools/link-query-table.mjs` still provides **explicit local development**:
  npm symlinks and a gitignored `go.work` select a requested upstream checkout.
  It does not change release pins; those links were not active during the audit.
  See [the development guide](../query-table-local-development.md).
- Git history, retained handoffs, current host accelerators, and the released
  v0.9.2 Go source. Integrity checks describe this checkout and its dependency
  installation; they do not certify an already installed Pharos application.

## Historical library interventions and their disposition

The source links below are pinned because the relevant directories have been
deleted. Do not copy these old patches onto current Query Table.

### 1. v0.6.0: a vendored Go adapter and a portable patch

[Pharos #100, `b5acd6c`](https://github.com/Pythia-Software/pharos/commit/b5acd6c85d35c7c88317dce973c05fa34c55e505)
added a `go.mod` replacement selecting `third_party/query-table-go` and
[`patches/query-table/v0.6.0-pharos.patch`](https://github.com/Pythia-Software/pharos/blob/b5acd6c85d35c7c88317dce973c05fa34c55e505/patches/query-table/v0.6.0-pharos.patch).
The interventions were:

| Intervention | Reason and reusable library behavior |
|---|---|
| Reuse identical metric plans within one transaction | Repeated cards otherwise executed identical work; clone results so sibling metrics cannot mutate each other. |
| Share reductions and fuse compatible scalar metrics | Reuse identical validated input/grouping stages even when final formulas, sorting or top-N differ; retain independent validation and budgets. |
| Preserve indexed row windows | Avoid materializing/normalizing all matching rows before LIMIT when the host declares canonical IDs/datetimes; keep validation of selected and computed values. |
| Use an integral exact accumulator before rational promotion | Avoid per-row rational allocations on safe integer SUM/AVG while preserving decimal arithmetic and error priority. |
| Export `ResolveRelativeDatetime` | Let host filters use the compiler's grammar and one captured clock. |
| Add `SQLiteComputedColumnStore.SaveIn` and diagnostic handling | Allow a host to validate the resulting computed graph before committing its save. |

The [original handoff and measurements](https://github.com/Pythia-Software/pharos/blob/b5acd6c85d35c7c88317dce973c05fa34c55e505/docs/query-table-upstream-handoff.md)
record 20 identical cards over 100,000 synthetic rows dropping from 7.437 s /
2.015 GB allocated to 0.484 s / 64.2 MB. Those measurements include host tuning;
they do not isolate a single patch. These interventions were superseded by
upstream work and the published **0.7.0** migration in
[Pharos #120, `c5414a5`](https://github.com/Pythia-Software/pharos/commit/c5414a59be798c6e26641811d649b2778410ce06).
Same-input reduction reuse is already delivered; cross-field V2 fusion remains
the separate request below.

### 2. Before 0.7.0: unreleased frontend archives and backend source snapshots

[Pharos #117, `0014760`](https://github.com/Pythia-Software/pharos/commit/0014760e2c8cb21b37d7b39bd8a713962e5d5096)
installed four locally built npm archives from Query Table commit
`43b018279b44b2a91b8ccd8c553744a99093cff7`, although their manifests still said
0.6.0. Later snapshots retained uncommitted upstream edits: the final frontend
manifest recorded base `29549f3d490ca47e1a8b0d86100385632717b65f`,
`upstream_dirty: true`, and source digest
`01d37558a7489aae7d2940d786d421135f3c0596b5c44269814cc6f1e312cf9e`.

These were explicit source builds, not registry 0.6.0. They carried V3,
calendar, router, display and other integration work before publication.
The retained backend manifest separately recorded base
`ad41521cf735c70d073280c832102d27c117da3c` plus local calendar changes:
ignore cleared filters, narrow and release each scratch relation, assign keys
in batches, share UTF-8 size accounting, and check payload/key/ordinal budgets
before and after copying.

Evidence is in the [frontend provenance](https://github.com/Pythia-Software/pharos/blob/95862d49ab30235fd7e5584f89774928cc680e3a/web/vendor/query-table-provenance.json)
and [backend manifest](https://github.com/Pythia-Software/pharos/blob/95862d49ab30235fd7e5584f89774928cc680e3a/third_party/query-table-go/source-manifest.json)
immediately before #120. That migration deleted both snapshots, the portable
patch and `tools/vendor-query-table.py` in favor of published **0.7.0**.
These are historical packaging interventions, not outstanding frontend patches.

### 3. v0.8.0: the recent performance fork

[Pharos #140, `bb496be`](https://github.com/Pythia-Software/pharos/commit/bb496be)
reintroduced a vendored Go replacement, documented in its
[`README.pharos.md`](https://github.com/Pythia-Software/pharos/blob/bb496be/third_party/query-table-go/README.pharos.md).
The frontend remained on the published release. The five changes were:

| Local change | Upstream outcome |
|---|---|
| Materialize representative rows once for identity validation and ROW projection | v0.9.0, upstream #127 |
| Reuse executor-validated staged values and error sidecars | v0.9.0, #128 |
| Fuse compatible scalar SQLite V3 summaries into the grouped reduction | v0.9.0, #129 |
| Inline cheap SQLite projections, retaining expensive evaluation | v0.9.0, #133, with safer compiler handling |
| Accept explicit host `MaxGroups` up to 100,000 instead of 10,000 | v0.9.1; defaults and enforcement retained |

The original inlining heuristic inspected SQL for `qt_`/`json_extract` and
overrode some guards. The complete fork failed upstream `TestV2ReductionReuse`:
39 or 54 source-function evaluations versus a 24-evaluation baseline, despite
matching values. This proves repeated expression evaluation, not additional full
table scans or a uniquely isolated cause. Released upstream implementations
replaced that heuristic and resolved the failure. See the
[performance stack](query-table-performance-stack.md) and
[failure report](query-table-fork-reduction-reuse.md).

[Pharos #173, `71976f8`](https://github.com/Pythia-Software/pharos/commit/71976f80c65af629a1cd704beda0ab625c8260d9)
replaced the four performance patches with v0.9.0 code, leaving only the two
budget-ceiling lines. [Pharos #174, `e1d9c2d`](https://github.com/Pythia-Software/pharos/commit/e1d9c2d1ecc0d0daa58e41e6f09fa5a5687097b3)
upgraded all dependencies to 0.9.2 and deleted the vendor, replacement and fork
gate after the budget change shipped.

### 4. A host in-memory reducer, now deleted

Pharos also implemented `nativeRowMetricsV2`, `nativeRowGroups` and an
`exactSum` decimal accumulator to avoid staging Usage's in-memory maps.
Its initial optimization declined filters, so filtered requests still staged;
its older hourly filter could disagree with the adapter on NULLs.

Query Table v0.9.0 supplied `ExecuteV2Rows`, `CompileRowFilter` and
`SQLiteRowValues` (QT-16); v0.9.2 supplied typed group keys and decimal
accumulation by scale (QT-17). #174 switched Usage to those APIs and deleted
the reducer and `query_metrics_exact.go`. Recorded synthetic probe results:
Token summary 8.5 ms versus the deleted reducer's 12 ms, weekly provider cost
5.4 ms versus 8 ms, hourly provider cost 16.7 ms versus 22 ms.
See [QT-16](query-table-in-memory-metrics.md) and
[QT-17](query-table-in-memory-metrics-speed.md). No new upstream request is
needed for those delivered capabilities.

## Original library work requests (0.9.2 baseline)

### A. Fuse V2 reductions across fields (QT-14)

**Current intervention.** `internal/archive/query_metrics_native.go` contains
`nativeMetricsV2`, `nativeAnswer`, `nativeGroups` and `nativeTableBounds`.
Pharos compiles/validates with Query Table, then executes a native SQLite
reduction per compatible grouping for plain numeric counts/sums. It checks
storage types, safe integer bounds, group/population budgets and revisions;
unsupported queries fall back to `ExecuteV2In`. Generation-scoped bound/count
caches and source projection make repeated requests cheaper.

**Why it exists.** Human Words' message mix asks for a count and nine sums over
nine columns. The migration to the general metrics route originally made ten
passes of about 30 ms each instead of the former roughly 48 ms total. This is
the historical QT-14 observation, not a fresh v0.9.2 timing. In released v0.9.2,
**Query Table** `sqlite_v2_reuse.go` still keys fusion by matching pre-reduction
stages; different field input stages prevent this cross-field sharing. V3 scalar
fusion and the new in-memory executor do not remove the SQL V2 intervention.

**Please implement.** Compile compatible scalar metrics over different fields
into one reduction of the authorized source when their population, scope,
filter and grouping agree. Count, sum, min, max, average and distinct count are
useful generally; Pharos's current SQL bypass needs only counts and sums.
Retain exact decimal semantics, per-metric validation/errors and independent
result payloads. Preserve separate populations for differing per-metric filters
or shown-row windows. Consider eliminating redundant source staging where the
same guarantees can be enforced directly against a table/view.

**Acceptance and removal.**

- Extend upstream SQLite differential/reuse tests with nine integer fields plus
  count, both grouped and ungrouped; add fractional, NULL, invalid storage,
  unsafe/cancelling integer, lazy formula, revision, budget and cancellation cases.
- Instrument source evaluation to demonstrate one shared reduction for compatible
  fields. Report validation/budget passes separately; do not equate a function
  counter with physical table scans. Keep `TestV2ReductionReuse` passing.
- Record before/after latency, allocations and temporary work on a synthetic
  fixture with tens of thousands of rows, comparing independent metrics, the
  combined adapter batch and native SQL. Use measurements rather than timing
  assertions in unit tests.
- In Pharos, run `TestNativeMetricsMatchTheAdapter` and
  `TestNativeMetricsMatchTheAdapterOnPharosDatasets`, then benchmark the SQL
  cases with `nativeMetrics` disabled. Equivalent results and comparable speed
  should allow deletion of the general native SQL reducer. Domain-specific
  cubes and their freshness checks can remain.

This updates [the earlier QT-14 request](../query-table-next-release-handoff.md#qt-14--fuse-metrics-over-different-fields-into-one-pass):
that historical text still describes the now-deleted in-memory reducer and
`query_metrics_exact.go`.

### B. Evaluate direct V3 grouping before raw-source staging

**Current intervention.** `internal/archive/query_groups_native.go` implements
`nativeGroupedRows`. For supported SQL sources, text/enum keys, and plain
count/sum/average summaries, it reduces the source first, emits compact synthetic
rows and rewrites summaries as MAX of each single precomputed value. Query Table
then supplies group identities, result schema, HAVING, ordering, paging and
metrics. Formulas, computed revisions, representatives and unsupported numeric
domains retain general execution. The general reduction also uses the storage
and safe-integer checks described above.

**Please investigate and implement where justified.** Benchmark the ordinary V3
executor on that same simple shape. If raw staging dominates, add a bounded
direct-reduction plan or another supported execution strategy that gives hosts
the same speed without fabricating a dataset and rewriting requests. This is
an opportunity distinct from the already released fusion of summaries *after*
staging. This audit establishes the bypass exists; it does not isolate its
current speed benefit from Pharos's indexes and cubes.

**Acceptance and removal.** Compare optimized and general execution for count,
count(field), sum and average, NULL/empty keys and populations, unsafe values,
HAVING, tie ordering, paging, source totals, all-matching/shown-row metrics,
authorization, revisions and budgets. Preserve expensive-expression reuse and
fall back for shapes the direct strategy cannot execute exactly. Port the
portable cases from Pharos's `TestQueryV3NativeGroupsMatchGeneralExecution` into
upstream coverage. Benchmark with the same authorized SQL source and host cubes
disabled before claiming `nativeGroupedRows` can be deleted.

### C. Support efficient SQLite scratch cleanup with host transactions

**Current intervention.** `beginQueryWithTempStore` in
`internal/archive/query_table_v2.go` leases one connection, selects memory/file
scratch storage, enlarges its temp cache, then rolls back and restores settings.
V3 selects spill-capable storage for large/raw populations. Query Table creates
its own temporary staging/reduction relations inside that transaction.

The existing [schema-reload investigation](query-table-schema-reload.md) records
about 5.8 ms for lease/release plus a subsequent query on a catalog with 578
schema objects, versus 53 µs for an explicitly cleaned-up, committed temporary
transaction. Rollback after temp DDL and changing `temp_store` can both force
schema reparsing. These are prior measurements, not rerun by this audit.

**Please coordinate with the host.** Document the supported lifetime/cleanup of
adapter-created scratch relations when using caller-owned transactions, and
ensure a successful execution leaves no adapter-owned temp objects. Establish
whether a host can drop scratch objects and commit a read-only query transaction
without losing snapshot consistency. If existing APIs suffice, documentation
and a tested example are enough; otherwise publish the required cleanup hook.
Failure/cancellation must still roll back safely. The host retains responsibility
for connection pools, PRAGMAs, choosing memory versus disk and restoring settings.

**Acceptance.** Use a synthetic schema with hundreds of objects to profile
successful and cancelled staged requests on one reused connection. Record schema
reloads, CPU and temporary work; verify no leaked objects or settings, one source
snapshot, unchanged byte/population/group/sample guards and disk spill. Coordinate
host changes through the existing schema-reload handoff; a library patch alone
cannot remove Pharos's unconditional rollback or per-request PRAGMA changes.

## Work that should stay in Pharos

Keep pricing/carbon calculation, daily/hourly fact selection, lineage, mirror
exclusion, source authorization, rollup publication and cube freshness in the
host. The same applies to query-generation caches, trigram candidates, covering
indexes, candidate page selection and domain-specific complement reductions
(`nativeTextComplementGroups`, `nativeMachineTimeGroups`). Query Table can offer
generic planning contracts without knowing Pharos's tables or ledger rules.

Host connection tuning and browser mount/debounce/render changes also remain
host work unless a measured portable library defect is isolated. Do not infer
that every speedup in [the Usage audit](../performance/usage.md) came from
library modifications. The published in-memory executor intentionally falls
back for unsupported requests; staging on that documented fallback is not itself
a new bug. The older computed-preview transport request (QT-15) is separate
from this performance handoff.

## Delivery and validation

Start from the current Query Table mainline and reconcile newer changes with
this v0.9.2 baseline before opening work. Deliver independently reviewable
changes for A, measured work for B, and a shared host/library decision for C.
Use upstream Go and real-SQLite suites; run configured PostgreSQL tests for any
shared compiler/executor change. Protect exact arithmetic, error sidecars, lazy
evaluation, revision/authorization checks, budgets, cancellation and cleanup.

Use small portable fixtures for correctness and standalone benchmarks for cost.
Neither `.context` artifacts nor a private production catalog is required to
reproduce the request shapes. Existing Pharos test names above locate the parity
references; `TestUsagePerformanceFilteredMetrics` in
`internal/archive/usage_performance_test.go` is a synthetic `-tags probe` example
for the already completed in-memory work, not a benchmark of SQL cross-field
fusion. Do not reuse its numbers to claim A or B is complete.

After publication, Pharos will pin all four npm packages and the Go module to
the same release, regenerate schemas, rebuild the ignored bundles, verify parity
and cost, and delete only the host bypasses made redundant. Validate the upgrade
with `go test -short ./...`, `go test ./...`, `npm --prefix web run check` and
`npm --prefix web run test:full`. Completion means released library behavior
replaces those general reductions without local library patches, modified
installed dependencies or generated-bundle edits.
