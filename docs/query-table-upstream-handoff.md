# Query Table SQLite/Go upstream handoff

## Release and reproducible patch

Pharos integrated Query Table **0.6.0** (Go **v0.6.0**, release commit
`00c16461ad72a0d3d19fe8fc37ca11a9d94d1a0a`). npm and Go registry latest checks
confirmed these versions on October 8, 2026 (America/Denver).

Please review and implement the changes in
[`../patches/query-table/v0.6.0-pharos.patch`](../patches/query-table/v0.6.0-pharos.patch).
The patch applies at the Query Table repository root:

```sh
git checkout v0.6.0
git apply --check /path/to/pharos/patches/query-table/v0.6.0-pharos.patch
git apply /path/to/pharos/patches/query-table/v0.6.0-pharos.patch
(cd backends/go && go test ./... && go vet ./...)
(cd backends/sqlite-tests && go test -race ./... && go vet ./...)
```

The checked-in adapter at `third_party/query-table-go` is the release plus these
patches. Tests needing monorepo fixtures use local copies under `testdata`; those
path adaptations are excluded from the upstream patch. No other upstream
workspace is needed to build Pharos. Remove the replacement only after an
upstream release provides the changes and the host checks pass against it.

## Final verification

Completed on October 8, 2026:

- Full Pharos Go tests and vet passed, along with targeted host race tests.
- The nested patched Go module's tests and vet passed.
- The portable patch applied to a clean release checkout; its Go tests/vet and
  real-SQLite fixture tests with the race detector/vet passed.
- Clean npm installation, schema drift checks, typecheck, build, and embedded
  bundle comparison passed.
- All synthetic and joined tool-ledger stress cases passed through 250,000 rows;
  correctness coverage includes all ten datasets. Browser automation was
  unavailable, so UI verification is limited to frontend checks and authenticated
  HTTP integration tests.
- After inline review, full internal tests/vet, the targeted race suite, schema
  checks and frontend type/build/bundle checks passed again. Four browser-store
  regressions cover save/refresh timing; host regressions cover partial capability
  recovery, incremental repairs, compact Library reads, dependency selection and
  full-versus-selective execution parity.

## Requested implementation

### 1. Reuse identical metric plans inside one execution

`sqlite_v2_dataset.go` memoizes by plan fingerprint plus scope after validating
the full batch and revision envelope. Independent metric IDs can share exactly
the same SQL result. Reused buckets, keys, sidecar pointers, histogram edges/
counts, and box summaries/outliers are cloned so caller mutations do not affect
siblings. Nothing is cached across requests or snapshots.

Twenty repeated cards previously scanned and reduced the population twenty
times. Regression coverage verifies actual scalar probe invocation counts,
independent payload mutation, invalid-batch rejection, and scope separation.

### 2. Share reductions and fuse compatible scalar cards

`metrics.go` exposes the scalar reduction boundary, output expressions/aliases,
and its FROM/GROUP BY relation on `SQLPlan`. `sqlite_v2_reuse.go` compares exact
compiler-produced stage prefixes and their numbered parameter values. It:

- Materializes an identical reduction once when only final group formulas,
  sort, or top-N differ.
- Combines different reductions with identical validated input stages and
  grouping into a single grouped SELECT, then maps the generated aliases back
  into each plan's final expression stages.
- Limits fusion to 128 projected expressions and keeps distributions on their
  existing exact sampling/streaming path.
- Uses uniquely named temporary tables and drops them before returning the
  caller's transaction, including cleanup with a separate bounded context when
  the request context is cancelled.

The original plan fingerprint and every revision, policy, input/error guard,
window, and result budget remain applicable. Unreferenced earlier CTEs preserve
numbered bindings without executing the source. This implementation is tested
with modernc; other drivers should be tested for unused positional arguments
before adopting it unchanged. A backend-native multi-output plan is a possible
cleaner long-term API than exposing these stage metadata fields.

Pharos's `TestQueryV2ReductionReuse` compares independent execution with the
combined batch, including SUM, AVG, MIN, literal group arithmetic, different
IDs, sorting/top-N, shown-row windows, predicates with parameters, actual
population visits, and absence of leaked temporary tables. Portable unit tests
in `sqlite_pharos_performance_test.go` cover the reuse/fusion policies.

### 3. Preserve indexes before bounded row windows

`sqlite_v2.go` keeps raw BINARY ordering for trusted text IDs and canonical
`utc-millis` datetimes, and lets raw filter CTEs flatten. The previous ordering
called value/error functions and materialized the matching population before
LIMIT, preventing an ordinary timestamp index from serving the window.

This relies on the documented host contract: IDs are unique nonempty scalar
text, and `utc-millis` is validated fixed-width canonical UTC storage. Ordinary
selected fields and consumed formula inputs still undergo guards. Arbitrary
RFC3339, alternate extraction, and computed ordering keep normalization/checks.
Please document this trust boundary explicitly and retain EXPLAIN regression
coverage. Pharos checks for its timestamp index and absence of the premature
materialized filter stage.

### 4. Avoid rational allocations for safe integral populations

`sqlite_v2_functions.go` uses an integer exact accumulator for SUM/AVG while the
absolute sum stays safe, then promotes to `big.Rat` before fractional input or
bound overflow. Integral `sqliteRat` inputs use `big.NewRat` directly. AVG keeps
large/cancelling populations valid when their exact final value is safe; SUM
keeps its conservative absolute-sum diagnostic. Error precedence and decimal
promotion are preserved. Tests cover fractional transitions, cancellation,
boundary integers, tiny values, and numeric-range diagnostics.

Fractional reductions still use exact decimal arithmetic. They are inherently
more expensive than integral reductions and must retain their numerical
contract in any further optimization.

### 5. Export relative datetime resolution

`relative_time.go` adds `ResolveRelativeDatetime(value, now)`, reusing the
compiler's signed-duration grammar and bounds. Pharos uses it for legacy map
and optimized SQL paths so they agree with modern requests without mutating
saved operands. OR branches share one captured clock. Portable unit tests and
host filtering tests cover this behavior.

### 6. Allow transactional computed-definition validation

`SQLiteComputedColumnStore.SaveIn` retains the same revision predicates as
`Save` but joins a host-owned transaction. Pharos saves, calls
`DescribeComputedIn` for the complete resulting catalogue, and commits only
when the edited graph validates and every previously valid definition remains
valid. Existing unrelated formulas invalidated by schema drift can be repaired
one at a time. This catches invalid syntax/types, unknown fields, missing
dependencies, cycles, and updates that invalidate healthy definitions.
`NewComputedColumnsHandler`
returns a `PlanDiagnostic` as HTTP 400; revision conflicts remain 409 and
infrastructure failures remain 500.

Please ship this hook or an equivalent transactional validation API. The store
can remain usable for metadata-only applications; validation is an explicit
host choice.

Pharos describes capabilities one definition at a time: compilation diagnostics
are returned per ID, valid graphs contribute their revision envelopes, and the
metadata catalogue stays complete and editable. Database/context failures still
fail the handshake. Please consider an official partial-description API so hosts
can recover from schema drift without a single failure hiding every definition.

## Host optimizations already implemented

These changes live in Pharos, outside the portable adapter patch:

- Register v1/v2 scalar and aggregate descriptors before opening connections;
  use SQLite codegen for all ten canonical schemas.
- Stage each map source once per batch and preserve SQL joins/mirror exclusion.
- Compile Library requests to collect referenced base fields, including raw
  filters, ordering, X/Y, distribution/group inputs and transitive computed
  dependencies. Retain the compact Library cache, adding long text and costs
  only when referenced. Stage only required values even if the shared cache
  contains earlier large-field reads.
  Keep unused schema aliases as NULL projections so compilation stays valid.
  The dependency read releases its transaction before host data reads; execution
  revalidates revisions before using the staged values.
- Refresh browser capabilities after computed saves before the hook publishes
  the new definition locally, keeping revision and execution support current.
- Preserve command trigram candidates and safe OR behavior.
- Lease a connection with `temp_store=MEMORY` for bounded v2 execution, restore
  its previous setting, and release it before page enrichment. Profiling showed
  temporary-file I/O dominated a slow mixed batch (about 70% of sampled CPU was
  in system calls). Enabling memory-backed temporary relations avoided the
  30-second failure. This is a host configuration decision, not a silent
  mutation inside the reusable adapter.
- Compute local-calendar UTC bounds using SQLite itself, including DST, to
  narrow day/week/month equality before the final shared predicate. This avoids
  repeatedly computing expensive local-time date expressions on adjacent days.
- Keep daily/hourly usage semantics, including transitive computed dependencies
  and expansion of only the daily shown-row window into its matching hours.
- Bound requests at 250,000 rows, 2,000 groups, 20 metrics, and 30 seconds.

## Measurements

Apple M3 Ultra, macOS/arm64, Go 1.26.8, modernc SQLite 1.39.1. Each stress sample
uses `-benchtime=1x -count=1 -benchmem`; fixture construction and initial rollup
preparation are outside timed iterations. Other workspace activity affects wall
time, so these are workload measurements rather than stable microbenchmarks.
The final measurements include the host temporary-storage setting as well as
adapter patches; baseline comparisons do not isolate every individual change.
Allocations below are cumulative allocated bytes, not peak resident memory.

At **100,000 synthetic rows**:

| Workload | Before relevant fixes | Final |
| --- | ---: | ---: |
| 20 identical cards | 7.437 s / 2.015 GB allocated | 0.484 s / 64.2 MB |
| 20 distinct formulas sharing SUM | 21.445 s / 1.262 GB | 0.645 s / 65.7 MB |
| Indexed shown-row SUM (50 rows) | 363 ms / 57.8 MB | 97.9 ms / 114 KB |
| 20 mixed formulas/distributions | 11.276 s / 1.236 GB, before fusion/temp tuning | 4.942 s / 543 MB |

At **250,000 synthetic rows**, the complete final run passed:

| Workload | Final latency |
| --- | ---: |
| COUNT | 0.488 s |
| SUM / AVG | 1.057 s / 0.908 s |
| MEDIAN | 0.800 s |
| Ratio of two SUMs | 1.215 s |
| Box / histogram | 1.074 s / 1.099 s |
| Fractional SUM | 1.311 s |
| 20 identical / 20 distinct formulas | 1.079 s / 1.277 s |
| Indexed shown rows | 0.284 s / 112 KB |
| 20 mixed formulas/distributions | 11.574 s (previously exceeded 30 s) |

A separate compiled-binary run of the 250,000-row mixed batch measured about
**201 MB maximum resident memory**, with about 191 MB peak footprint. Its
cumulative allocations were 1.356 GB; those numbers describe different things.

Joined tool-ledger workloads also exercise real Pharos sources, mirror
exclusion, grouping, NULL values, trigram search, calendar filters, and timestamp
windows. At 100,000 calls, before tightening calendar candidates, the measured
latencies were: paired formula 0.968 s, histogram 0.619 s, command substring
0.191 s, indexed shown rows 0.114 s, and twenty distinct SUM formulas 0.678 s.
Calendar COUNT took 1.920 s / 810 MB allocated, motivating the exact SQLite
boundary optimization. The tighter bounds reduced the 100,000-call calendar query to **0.562 s /
269 MB**, with DST parity checked against the original predicate.

At 250,000 tool calls, all joined workloads passed: paired formula **1.699 s**,
histogram **1.437 s**, command substring **0.290 s**, indexed shown rows **0.535 s**,
and twenty distinct SUM formulas **1.589 s**. The earlier coarse calendar
candidate took 3.826 s / 2.021 GB allocated; the final tight-candidate run took
**5.317 s / 676 MB** under different concurrent load. Allocation reduction is
consistent at both sizes; the single-sample wall-time improvement was observed
at 100,000 calls, and is not established at 250,000 calls. Every joined workload
completed within its request deadline.

A separate **10,000-row Library staging** fixture carries about 8.7 KB in each
of purpose, outcome and changed-files text. Staging every schema field took
**486 ms / 24.3 MB allocated**; staging only ID, title and activity time took
**18.7 ms / 3.05 MB**. These timings measure temporary-table staging and cleanup,
not source retrieval, page enrichment or end-to-end request latency. Regression
tests also compare full and selective execution for filters, sorting, formulas,
paired values, distributions, computed grouping and transitive dependencies.

Raw logs are in Pharos's gitignored `.context/` directory:
`query-table-baseline-bench.txt`, `query-table-distinct-before.txt`,
`query-table-final-memory-bench.txt`, `query-table-ledger-bench.txt`,
`query-table-day-optimized.txt`, `query-table-peak-memory.txt`, and
`query-table-library-staging-review.txt`. Benchmarks are checked in at
`internal/archive/query_table_v2_test.go` and
`internal/archive/query_table_v2_review_test.go` so the results can be
reproduced without these local logs.

## Follow-up priorities

1. Generalize fusion to compatible overlapping input DAGs, and share guarded
   projections with distribution plans when doing so preserves population,
   shared histogram edges, exact sample caps, and lazy diagnostics. The largest
   twenty-card mixed batch still takes about 12 seconds at the host population
   cap; it is much faster but remains the heaviest tested request.
2. Reduce modernc scalar bridge/projection allocations while preserving all
   storage and arithmetic diagnostics. Fractional exact reduction can use a
   coefficient/exponent accumulator if independently proven equivalent.
3. Add official indexed-window and many-card performance fixtures, document
   temporary-storage configuration, and include real-SQLite driver coverage
   for fused plans, unused binds, cancellation, caller-owned transactions,
   cleanup, and revision envelopes.
4. Explore cube-aware weighted basic reductions and precomputed calendar
   columns at the host boundary. Any rollup shortcut must still detect unsafe
   inputs that cancel; checking only final sums is insufficient.

These follow-ups are performance opportunities. Current failures for populations,
groups, unsupported profiles/scopes, or per-group exact sample limits are
intentional diagnostics and should not become silent approximations.
