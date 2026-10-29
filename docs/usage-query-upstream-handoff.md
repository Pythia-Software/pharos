# Usage query optimization handoff

**Resolved upstream.** Query Table 0.9.3 fuses metrics across fields, 0.9.4
reduces grouped V3 sources directly, and 0.9.7 accepts host predicate
relations, proves V2 sources once per data generation, and picks the nearest
point on line charts. Pharos gives Query Table its substring indexes as
predicate relations and source generations to its adapter executions; its host
reducer and broad-exclusion complement remain. See
[source proofs](query-table-integration.md#source-proofs) and
[substring relations](query-table-integration.md#substring-relations) for the
measurements and what stays in the host.

Pharos consumes the published Query Table **0.9.2** release. This work changes
no Query Table package, module, installed dependency, or generated library code.
The frontend bundle is rebuilt from the host's normal TypeScript source.

The following reusable opportunities would let Pharos retire host accelerators.
They are proposals for upstream work, not prerequisites for this branch.

## Reduce before staging grouped sources

A V3 request grouping a large SQL source by a few text dimensions, with integer
`count`, `sum`, or `avg` summaries, currently stages the source before reducing
it. A million source rows can produce only tens of result groups. This makes a
dashboard-to-grouped-query transition much slower than its chart metric.

Reproduce with a SQLite source containing `id`, `day`, `provider`, and nullable
integer `duration_ms`. Request groups by `day,provider`, summaries `COUNT()`,
`SUM(duration_ms)`, and `AVG(duration_ms)`, then apply a count HAVING predicate,
sort, offset, and a shown-rows result metric. Scale the source beyond the normal
row-window budget while keeping a small number of groups.

An upstream reducer could push eligible groups below staging, while preserving
the adapter's group identities, result schema, HAVING, stable ordering, paging,
revision checks, diagnostics, budgets, and metrics. It must retain null versus
zero sums and the count of nonnull values used for means, and decline unsafe
integer domains, unsupported formulas, and representative rows.

Pharos's integration is `internal/archive/query_groups_native.go`. It reduces
eligible SQL sources, then passes one compact row per group back to the adapter.
Synthetic `MAX` summaries carry those already reduced values through the normal
result executor. `TestQueryV3NativeGroupsMatchGeneralExecution` compares complete
responses against the general executor, including filters, nulls, paging, HAVING,
and result metrics. This provides a small behavioral matrix for upstream work.

## Fuse metrics reading different fields

A dashboard often requests several sums and counts over the same population
and grouping. Reducing them in one SQLite pass avoids repeatedly evaluating the
same typed WHERE and rereading the source. The existing Pharos implementation,
`query_metrics_native.go`, describes the exactness restrictions and compares
responses with the ordinary adapter in `query_metrics_native_test.go`.

## Accept a host-provided exact predicate relation

Pharos has a transactionally maintained substring index. For supported ASCII
needles of at least three characters, it stores the compiler's Go-normalized
text in a case-sensitive trigram index. Its membership relation is exact; the
same text predicate need not run again against wide source payloads.

A reusable seam should distinguish exact relations from candidate relations.
Only a complete, exact relation may replace its predicate. Candidates must keep
the final typed check. OR pushdown is safe only when every branch has a safe
relation. Negation must preserve the compiler's null/empty guard, and every
relation must use the execution's data snapshot. Short/non-ASCII needles,
unsupported operators, and incomplete backfills must retain ordinary execution.

Pharos's implementation and parity cases are in `query_text_index.go` and
`tool_search_test.go`. The host also answers supported broad exclusions by
subtracting positive matches from a fresh cube's nonempty-command population.
That optimization depends on Pharos's own derived facts and belongs in the host;
an upstream predicate seam would make the integration less specialized.

## Evidence and integration boundary

Grouped source filter controls can issue many repeated metadata reads while
the view initializes. In Chrome over HTTP/1, those requests can occupy the
origin's connection limit and delay a foreground V3 query. The audit observed
a roughly 20-second browser transition despite much faster native execution.
The host now shares in-flight metadata reads, retains a stable result for five
seconds, and permits one metadata request on the network at a time. Rows and
metrics are not cached by this mechanism. Subscriber cancellation does not
cancel a metadata read shared by another control. An upstream improvement is
to stabilize source filter API/callback identities and fetch suggestions only
for controls that need them. `web/src/query-metadata.ts` and its integration
tests document the host scheduling contract.

[Usage audit repairs](usage-audit-repairs.md) records the fixes and verification;
[the current audit](usage-audit-current.md) contains the selected timing results.
The original [audit](usage-audit.md) remains available for comparison. The audit
scripts generate cases without embedding catalog records or private query
values. Their runtime evidence stays in the gitignored `.context/usage-audit/`.

## Resolve overlapping line-chart hit targets

The dashboard control verification also observed overlapping native line-chart
hit circles. A small positive point can be covered by a later-rendered zero
point in another series at the same date. This is particularly noticeable when
a host pads missing time intervals to calculate moving averages. Keyboard
focus and Enter still activate the intended point; pointer selection can
activate its neighbour instead. No installed package or bundle is patched.

Reproduce with a native `MetricCard`, a line clause grouped by `day,tool`, and
two series sharing three ISO dates. Give one series values near zero and the
other a large peak plus zeroes. Use a nonzero row count on real intervals and
zero on padded intervals. At the low-valued date, the transparent 12px hit
circle of the later series can cover the visible point in the earlier series.

Upstream should select the nearest visible point (with deterministic handling
of exact ties or a choice of coincident series), while retaining keyboard
navigation, tooltips, and exact bucket identity for `onInspect`. A host option
to mark padded points as non-inspectable would also help distinguish measured
intervals from presentation-only samples. Validate pointer and keyboard
activation for dense, crossing, zero, negative, and missing-value series.
