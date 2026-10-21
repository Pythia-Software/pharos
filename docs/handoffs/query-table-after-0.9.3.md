# Query Table: replace the remaining host reducers after 0.9.3

For the Query Table team. This is a self-contained assignment against the
published **0.9.3** release, dated **2026-10-21**.

## Goal and integration

Make normal Query Table execution fast enough for a Go/SQLite host to remove
its own scalar-metric and grouped-row reducers. Separately, let a modern
transport serve computed-column previews without implementing the legacy row
transport. Implement reusable library behavior and publish it in an aligned
Go/npm release.

The consumer is **Pharos**, a Go service with a React frontend and an embedded
SQLite catalog. It passes authorized SQL sources to Query Table, uses V2 for
scalar metric batches and V3 for grouped result rows, and sometimes supplies
already prepared row maps to `ExecuteV2Rows`. Its frontend uses negotiated V2
rows, V3 grouping and metrics transports.

Pharos's Go module and core/React/UI/codegen npm packages all pin **0.9.3**.
Installed dependencies are unmodified published artifacts. Host code still
contains two general SQL accelerators because they remain faster than ordinary
adapter execution. Replacing them is the performance work requested here.

The reproducible consumer baseline is Pharos commit
[`19ad3109c1b04ebc9f54b69a2bae512506c180be`](https://github.com/Pythia-Software/pharos/tree/19ad3109c1b04ebc9f54b69a2bae512506c180be),
available in [Pharos PR #185](https://github.com/Pythia-Software/pharos/pull/185).
All Pharos code paths and measurements below refer to that commit. Start
library work from current Query Table mainline and account for any changes
newer than 0.9.3.

## What 0.9.3 already provides

- Cross-field V2 scalar fusion, guarded integer reductions and direct reduction
  of eligible stored-column sources, retaining exact fallback behavior.
- Cleanup of adapter-owned temporary objects, including interrupted creation,
  with cleanup failures returned to the caller. Successful caller-owned V2/V3
  transactions may be committed; failed executions must roll back.
- Portable SQLite metric, V3 source-copy and transaction-lifecycle benchmarks.

These are delivered capabilities. The performance requests below concern the
remaining cost after adopting them. The supported release contract is described
in [Query Table's SQLite performance/lifecycle guide](https://github.com/Pythia-Software/query-table/blob/v0.9.3/docs/sqlite-performance-handoff.md).

## Reproduction and measured gap

From the Pharos baseline checkout, use Node 24 (`.nvmrc`) and the Go toolchain
selected by `go.mod`, then run:

```sh
tools/setup.sh
PHAROS_TEST_PROCESSES=1 GOWORK=off go test -tags probe \
  -run '^TestUsagePerformanceSQLHost$' -v -count=1 ./internal/archive
```

The test is in
[`internal/archive/usage_performance_test.go`](https://github.com/Pythia-Software/pharos/blob/19ad3109c1b04ebc9f54b69a2bae512506c180be/internal/archive/usage_performance_test.go).
It creates a temporary synthetic catalog and needs no production data, private
catalog, development drive or retained local artifacts. It calls the real
authenticated HTTP routes, including dependency discovery, SQL source
projection, transaction leases, response serialization and cleanup.

The fixture has **50,000 authored-message facts**, five text day keys and nine
nonnegative integer fields: total `words` plus `typed_words`, `harness_words`,
`automated_words`, `template_words`, `attachment_words`, `quoted_words`,
`resent_words` and `pasted_words`. For source ordinal `n = 0..49999`, category
`i = 0..7` has `(n+i)%7` words; total words is their sum. Day is
`2026-09-01` through `2026-09-05`, selected by `n%5`. Days 1, 3 and 5 belong to
provider `codex`; days 2 and 4 to `claude`. The provider filter matches 30,000
rows. There are no tool cubes. Both execution paths use the same authorized
source, catalog, filters and indexes.

The probe switches the internal test flag `nativeMetrics` between host and
ordinary adapter execution. This is not an HTTP option. It rejects metric and
bucket errors and checks identical complete responses, including totals and
identities. One warmup is discarded, followed by three measured requests per
path. Timings are local warm medians on macOS, Go 1.26.8 and modernc SQLite
1.39.1:

| Full HTTP workload | Host accelerator | Query Table 0.9.3 |
|---|---:|---:|
| V2 COUNT + nine SUMs, ungrouped | 15 ms | 44 ms |
| Same V2 batch grouped by day | 29 ms | 66 ms |
| Same grouped batch, provider = codex | 32 ms | 70 ms |
| V3 grouped COUNT/SUM/AVG, result page and metrics | 33 ms | 299 ms |

The library is much faster than the preceding release, and all these responses
match. The remaining gap is approximately **2–3× for V2 and 9× for V3** on this
fixture. It is not a production or concurrent-dashboard latency guarantee.
The HTTP figures include Pharos's current transaction policy; profile backend
and host costs separately rather than assigning the whole difference to the
library.

To reproduce the request shapes directly in an upstream synthetic fixture:

- Bind a stable text ID, text day, enum/text provider and the nine numeric
  fields to stored SQL columns. Numeric fields use the normal SQLite numeric
  schema binding. Use a host-authorized source SELECT with bound arguments.
- V2: `MetricQuery{Version: 2, Profile: SQLiteExpressionProfile, Limit: 50}`,
  containing a row count and nine plain sums. All metrics use the same
  `GroupBy`: either empty or `["day"]`. The filtered variant adds source WHERE
  `provider = "codex"`. Execute with `SQLiteV2Dataset.ExecuteV2In`.
- V3: a version-3 SQLite-profile request with a fixed `Now`, `Rows: true`,
  grouping key `day`, and summary IDs `messages = COUNT()`,
  `words_total = SUM(words)` and `mean = AVG(words)`. Select the day and those
  three summaries, order day ascending and limit the result to two groups.
  Add `COUNT()` over all matching groups and `SUM(messages)` with
  `Scope: "shownRows"`. The probe also sets source limit 1/offset 100, which
  must not truncate grouping membership. Execute with
  `QueryDatasetV3.ExecuteIn`. Expected source total is 50,000, grouped total
  is five, and the shown groups contain 20,000 messages.

## Priority 1: reduce ordinary V3 grouped execution cost

**Host intervention.** Pharos's
[`nativeGroupedRows`](https://github.com/Pythia-Software/pharos/blob/19ad3109c1b04ebc9f54b69a2bae512506c180be/internal/archive/query_groups_native.go)
first reduces supported SQL sources to one compact row per group for plain
count/sum/average summaries and text/enum keys. It rewrites each summary as MAX
of that row's precomputed value, then gives the compact source to Query Table.
Query Table still produces group identities, result schema, HAVING, ordering,
paging and result metrics. Unsupported numeric domains, formulas, computed
revisions and representatives retain ordinary execution.

**Request.** Profile the ordinary V3 path on the request above and reduce the
typed projection, validation and grouped execution work enough to make the host
pre-reduction unnecessary. A compiler-owned direct reduction for eligible
sources is one possible implementation; an equally effective improvement to
the existing pipeline is suitable. Keep the public request and result semantics.

The existing upstream investigation found copying already typed rows was only
about 4% of CPU in its portable fixture. Treat that separately from evaluating
the typed projection. Removing a copy alone is not an established solution.
Useful starting points are **Query Table** `backends/go/query_v3_execute.go`
and `backends/sqlite-tests/query_v3_performance_test.go`.

**Acceptance.**

- Compare the candidate and current executor on the same authorized source
  without host pre-reduction, cubes or cached result substitution. Report wall
  latency, process CPU, allocations and temporary work, with a breakdown of
  source proofs/projection, grouping, result metrics and cleanup.
- Preserve group identity/schema, source totals, NULL versus empty keys,
  empty populations, HAVING, tie ordering, result paging and both metric scopes.
  Source paging must never truncate group membership.
- Preserve exact numeric results and diagnostics, computed revision checks,
  authorization, population/byte/group/sample budgets and one source snapshot.
  Expensive or volatile source expressions retain their shared evaluation.
- Use conservative fallback for unsupported shapes. Exercise failure,
  cancellation and cleanup as well as successful grouped requests.
- Add portable coverage to existing upstream suites. Pharos's
  `TestQueryV3NativeGroupsMatchGeneralExecution` and the HTTP probe are consumer
  acceptance checks. A result that requires synthetic host rows or request
  rewriting has not replaced the intervention.

## Priority 2: close the residual V2 scalar reduction cost

**Host intervention.** Pharos's
[`nativeMetricsV2`](https://github.com/Pythia-Software/pharos/blob/19ad3109c1b04ebc9f54b69a2bae512506c180be/internal/archive/query_metrics_native.go)
compiles and validates the request through Query Table, then executes a plain
integer count/sum reduction per compatible grouping. It uses exact storage and
safe-integer checks, generation-scoped bound/count caches and narrow source
projection. Shapes it cannot answer exactly use the adapter.

**Request.** Reduce the remaining overhead of normal 0.9.3 scalar execution,
with enough evidence to decide whether this host reducer can be deleted.
Cross-field fusion itself is already implemented. Investigate remaining proof,
planning, staging, savepoint and mapping costs rather than assuming a cause.
Distinguish the host's cached source bounds from library guarantees that must
hold for arbitrary authorized input on every execution.

Useful **Query Table** starting points are `backends/go/sqlite_v2_cross_field.go`,
`sqlite_v2_integer.go`, `sqlite_v2_reuse.go` and
`backends/sqlite-tests/cross_field_benchmark_test.go`.

**Acceptance.**

- Retain cross-field sharing across compatible populations, grouping, scopes
  and metric predicates, with independent errors, final formulas, sorting,
  top-N, minimum counts and response payloads.
- Cover count, count(field), sum, average, extrema and distinct count where
  eligible; use exact fallback for unsupported storage and numeric domains.
  Preserve decimal sums rounded once (including ten 0.1 values summing to 1),
  cancellation between signed values, unsafe integers, numeric-looking TEXT,
  invalid keys, NULLs and lazy expression branches.
- Keep different predicates or shown-row windows as distinct populations.
  Preserve revisions and execution budgets; a bad metric must not fail valid
  siblings. Keep existing reduction-reuse tests passing.
- Report the full adapter cost alongside guarded SQL and bare SQL, explicitly
  identifying guarantees omitted by any lower-bound comparison. Count source
  function evaluations separately from submitted reductions or physical scans.
- Run Pharos's `TestNativeMetricsMatchTheAdapter`,
  `TestNativeMetricsMatchTheAdapterOnPharosDatasets` and the full HTTP probe
  with the host reducer disabled. Aim for host-comparable latency; document any
  remaining cost and the guarantees it buys so the consumer can make an explicit
  performance/simplicity decision. Use benchmarks rather than latency assertions
  in unit tests.

## Independent API request: computed previews on modern transports

**Current library coupling.** In 0.9.3,
[`packages/react/src/useComputedColumns.ts`](https://github.com/Pythia-Software/query-table/blob/v0.9.3/packages/react/src/useComputedColumns.ts)
builds its preview request with `toServerQuery` and unconditionally calls
`transport.fetchRows`, paging at up to 500 rows per request. Meanwhile
[`packages/core/src/adapters.ts`](https://github.com/Pythia-Software/query-table/blob/v0.9.3/packages/core/src/adapters.ts)
requires `fetchRows`, while V2/V3 transport methods are optional. Consequently,
hosts using modern server execution still have to maintain the legacy row route
for computed-column previews.

**Request.** Use the negotiated row transport for computed-column previews:
V2 when available, V3 where appropriate to the supported preview semantics,
and the existing legacy transport for legacy-only hosts. Let capable modern
hosts typecheck and run without implementing `fetchRows`. Preserve evaluation
of unsaved draft expressions and the current bounded preview behavior.

**Acceptance.**

- A host implementing modern rows/grouping and metrics, without legacy
  `fetchRows`, can render its tables and computed previews.
- Preview dependency loading, filters, ordering, paging, canonical computed
  revisions, row totals and diagnostics retain their documented semantics.
- Cancellation and superseded previews cannot publish stale results. Legacy
  transports and complete client datasets retain their existing behavior.
- Include public type coverage, focused React tests and migration documentation.

Pharos separately has a legacy caller in `ShareSelection` in
`web/src/index.tsx`: **Select all** for share/export. The host will migrate that
caller before deleting its legacy endpoint. That application action belongs in
Pharos; preview negotiation and the public transport type belong in Query Table.

## Ownership boundary and delivery

Pharos owns its remaining transaction-policy migration: drop host-created
`pharos_query_rows`, `pharos_query_hours`, `pharos_query_selected_days` and
`pharos_query_shown_hours` tables when present, commit only successful requests,
roll back execution/cleanup failures, and choose stable memory/file connection
settings while retaining disk spill for large populations. The 0.9.3 library
cleanup contract already supports this; another cleanup hook is not requested.
Coordinate measurements after that host change to separate its benefit from
new library improvements.

Pricing, carbon calculation, daily/hourly fact selection, lineage, source
authorization, indexed candidates, cube publication/freshness and data-generation
caches remain host responsibilities. The library work should support general
SQL sources without incorporating Pharos's tables or product rules.

Deliver focused, independently reviewable changes for V3 performance, further
V2 optimization where supported by evidence, and preview transport negotiation.
Put portable correctness cases and benchmarks in the library's existing suites;
use configured PostgreSQL tests for changes to shared compiler/executor code.
Report hardware, Go/driver versions, fixture shape and warmup methodology.
Keep strict value guards, exact arithmetic, error isolation, authorization,
revision/snapshot semantics, budgets and cleanup intact.

Publish normal matching Go/core/React/UI/codegen release versions with migration
notes. Pharos will upgrade those published artifacts, rerun its full Go and
frontend checks and both browser engines, compare the same HTTP workload, then
remove only the host implementations proven redundant. Installed-package,
module-cache or generated-bundle patches are not an integration route.
