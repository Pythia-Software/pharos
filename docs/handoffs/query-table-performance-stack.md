# Query Table backend performance PR stack

> Completed in Query Table v0.9.0 through upstream PRs #127, #128, #129 and
> #133. Pharos now uses the released implementations. The only remaining
> adapter patch is the separate host group-budget allowance described in
> [README.pharos.md](../../third_party/query-table-go/README.pharos.md).
> The original request below is retained as a historical record.

## Request and scope

Please upstream four Go backend optimizations from Pharos as a stack of four
reviewable PRs: reuse representative rows, reuse validated staged values, fuse
scalar V3 group summaries, and inline cheap SQLite projections. These reduce
repeated evaluation and temporary population copies while preserving Query
Table's existing results, diagnostics and execution budgets.

Use the Pharos implementation as a reference and adapt it to current Query
Table. The SQLite inlining change needs further work before it can merge:
Pharos's complete fork currently fails the upstream reduction-reuse test.

This request covers the first four patches listed in
[`third_party/query-table-go/README.pharos.md`](../../third_party/query-table-go/README.pharos.md).
The fifth patch, which raises the host group-budget ceiling to 100,000, is
excluded. Broader V2 cross-field metric fusion remains the separate
[QT-14 request](../query-table-next-release-handoff.md#qt-14--fuse-metrics-over-different-fields-into-one-pass).
Frontend and wire-protocol changes are outside this stack.

## Source and stack structure

The reference implementation is Pharos commit
[`abb35942af0b91fafe81df845a238df719b44ed5`](https://github.com/Pythia-Software/pharos/tree/abb35942af0b91fafe81df845a238df719b44ed5/third_party/query-table-go),
based on Query Table **v0.8.0**, release commit
`2251cdd2350d7ca43d808c3607bbbe4298896815`. In Pharos, the adapter lives in
`third_party/query-table-go/`; its upstream counterpart is `backends/go/`.

Open PR 1 against Query Table's current mainline, then base each subsequent PR
on the preceding branch. Keep each PR's diff limited to its change and tests;
include predecessor and successor links in the descriptions. Each intermediate
state must pass its applicable tests. Merge in order, updating the remaining
PR bases as predecessors land.

| PR | Suggested title | Upstream files containing the reference changes |
|---|---|---|
| 1 | Reuse representative rows for validation and projection | `query_v3_execute.go` |
| 2 | Reuse validated values and error sidecars after V3 staging | `schema.go`, `sqlite_v2.go`, `query_v3_execute.go` |
| 3 | Fuse scalar SQLite V3 group summaries in one reduction | `metrics.go`, `query_v3_execute.go` |
| 4 | Inline cheap SQLite projections without regressing reduction reuse | `plan.go`, `sqlite_v2.go`, `metrics.go` |

The order keeps the broad compiler change last. PR 1 is independently useful;
PR 2 establishes the staged-value invariant used by PR 3. Split the interleaved
changes in `query_v3_execute.go`, `sqlite_v2.go` and `metrics.go` by behavior.
Do not replace whole files with the fork or bring along the excluded budget
changes in `query_v3.go` and `sqlite_v2_dataset.go`.

## PR 1 Reuse representative rows

`QueryDatasetV3.stageGroups` currently executes the representative plan for
identity validation and again to populate ROW summaries. Materialize its result
once within the authorized transaction, validate that result, and use the same
rows for every ROW projection. The reference change applies to both SQLite and
PostgreSQL.

Retain existing partitioning, ordering, NULL placement, source-ID tie-breaking
and duplicate/missing identity diagnostics. Clean up the temporary relation on
success, validation failure, database failure and cancellation, using the
existing bounded cleanup-context convention.

Acceptance:

- Multiple ROW fields come from the same chosen source row, including tied
  ordering values and NULLs.
- Duplicate or missing identities still fail with the existing diagnostic;
  validation and projection consume the same materialized result.
- Computed group errors and NULL keys remain distinct partitions.
- SQLite and PostgreSQL tests cover results and cleanup. A focused probe or
  benchmark demonstrates that the representative plan is evaluated once for
  these two consumers.

## PR 2 Reuse validated staged values

V3 has already compiled, typed and staged its narrow source, including error
sidecars. Later SQLite compilation currently repeats field validation and
datetime decoding over those executor-created relations.

The reference adds a private `FieldSpec.typedValue` flag, sets it on staged
source fields and group outputs, and lets `planBuilder.sqliteField` reuse the
value/error pair. An equivalent internal representation is welcome. Keep this
an executor invariant, with no public option allowing callers to bypass source
validation.

Acceptance:

- Caller-provided numeric, boolean, text and datetime storage still passes the
  original validation; invalid base storage is rejected as before.
- Valid staged datetimes are consumed in their normalized representation
  without another decode. NULLs and computed error sidecars survive unchanged.
- Group keys, summaries, representative fields, HAVING, sorting and downstream
  metrics preserve their types and diagnostics, including enum fields and
  unselected sort dependencies.
- Numeric-domain checks, aggregate policies, computed revisions and lazy
  expression branches retain their behavior. Demonstrate reduced repeated
  validation/decoding on a staged fixture.

## PR 3 Fuse scalar V3 summaries

The existing grouped path builds the group table with COUNT, then runs separate
summary plans and updates the groups for each summary. Compile compatible
scalar summaries together with COUNT over the same staged population and
grouping keys. Write their values and error sidecars directly into the group
table, avoiding one reduction and group-table update per summary.

The reference extends the internal `compileMetricDialect` helper with additional
expressions and maps their output columns in `stageGroups`. Reuse the existing
expression compiler, aggregate policies and exact aggregate implementations.
Every summary must still receive independent validation before staging; the
extra-expression mechanism must not accidentally accept differing scopes,
filters or grouping settings as a general batch API.

Keep this PR focused on SQLite. Preserve PostgreSQL's existing execution.
Sampled summaries retain their existing path and sample budgets. Pharos disables
fusion for the entire summary set if any summary uses a sampled expression such
as MEDIAN; retaining that conservative behavior is sufficient. If a combined
plan exceeds compiler limits while independent summaries fit, fall back to
independent execution. Cancellation must remain an error.

Acceptance:

- Compare fused output with independent execution for COUNT, COUNT(field),
  distinct count, SUM, AVG, MIN, MAX and scalar formulas over multiple fields.
- Cover fractional sums, cancellation between positive and negative values,
  unsafe integers, numeric-range errors, NULL inputs, empty populations,
  datetime/enum extrema and lazy branches such as
  `IF(TRUE,SUM([n]),SUM([n]/0))`.
- Preserve computed group error identities, representative ROW fields, HAVING,
  result sorting/paging and all-matching versus shown-row metric scopes.
- Exercise a combined plan that exceeds compiler limits, mixed scalar/MEDIAN
  summaries, sample-budget rejection, group-budget rejection and cancellation.
- A focused benchmark or execution counter shows compatible summaries sharing
  the grouped reduction. Tests assert behavior rather than exact SQL text or
  a particular CTE layout.

## PR 4 Inline cheap SQLite projections

Avoid materializing an entire intermediate population just to project a cheap
field guard, numeric guard, CASE expression or error sidecar. Preserve shared
evaluation for datetime decoding and expensive function payloads.

The reference changes `planBuilder.emit`, `sqliteField` and `metricNumber`.
Its generic classification uses SQL substring checks for `qt_` and
`json_extract`, with overrides for field and numeric guards. Prefer explicit
compiler metadata describing which stages can be inlined, and account for
expensive or volatile host expressions underneath apparently cheap aliases.
Adapt the implementation based on measured evaluation cost and temporary work.

### Reduction reuse must remain intact

The full Pharos fork's recorded v0.8.0 suite result on **2026-10-19** was
171 passing tests, 23 skips, and one failure: `TestV2ReductionReuse` in
`backends/sqlite-tests/reuse_test.go`. Unpatched v0.8.0 passes. SUM batched with
AVG/MIN produces 39 or 54 source-probe evaluations against a baseline of 24;
metric values still match.

The probe counts scalar-function invocations, so this establishes repeated
expression evaluation, not necessarily additional full table scans. The failing
fork contains all four patches; isolate the cause rather than assuming it is
already proven to be inlining. Keep the test green throughout PRs 1–3 and
resolve any interaction before PR 4 merges. Do not weaken or skip its reuse
assertions to accommodate the patch.

Acceptance:

- `TestV2ReductionReuse` passes unchanged, including independent sharing for
  shown-row scope, budget errors and temporary-table cleanup.
- Ordinary V2 rows/metrics and V3 grouped requests retain values, per-value
  errors, lazy branches, indexed windows, population checks and cancellation.
- Counter-based coverage guards against repeated expensive input evaluation;
  benchmark evidence shows the intended reduction in temporary work and cost.
  Do not require every cheap deterministic guard to run exactly once.

## Validation and delivery

Put portable regressions in Query Table's existing Go, SQLite and PostgreSQL
suites with small synthetic fixtures. Pharos's useful behavioral references are:

- `internal/archive/query_table_v3_patch_test.go`:
  `TestVendoredPatchFormulaGroupErrorIdentities` and
  `TestVendoredPatchHiddenAndDefaultSort`.
- `internal/archive/usage_query_performance_test.go`:
  `TestUsageFusedSummariesMatchIndependentMetrics`.
- `internal/archive/query_table_v2_test.go`:
  `TestQueryV2ReductionReuse`, `TestQueryV2IndexedWindow` and
  `TestQueryIndexedDatetimeCandidatesPreserveTypedResults`.

Run the applicable checks at each PR tip and the full upstream backend suites
on the final stack, following Query Table's own contribution instructions.
The baseline module commands, from the Query Table root, are:

```sh
(cd backends/go && GOWORK=off go test ./...)
(cd backends/sqlite-tests && GOWORK=off go test ./...)
(cd backends/postgres-tests && GOWORK=off go test ./...)
```

Provision PostgreSQL using upstream's documented test setup for the shared
representative change; skipped database tests do not validate that path. Include
the normal race/vet checks required upstream. Report benchmark fixture size,
query shape, before/after measurements and any relevant memory/temp-storage
tradeoff in the corresponding PR, without adding wall-clock test thresholds.

Pharos's `tools/test-query-table-fork.sh` is a baseline diagnostic: it extracts
upstream tests at v0.8.0 and runs them against Pharos's entire vendored copy.
It does not test a new upstream stack. Pharos's root `go test ./...` also cannot
replace upstream validation because the vendored adapter is a nested module.

Deliver four linked PRs with independently passing intermediate states and a
passing final stack. Preserve source authorization, revision checks, exact
arithmetic, error sidecars, staging/population/group/sample budgets and cleanup.
Publish the changes in the normal Query Table release so Pharos can upgrade,
verify host parity and remove the corresponding local patches. The separate
group-budget patch remains until it is addressed independently.
