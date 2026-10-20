# Handoffs

Each file here is a self-contained assignment for an agent. It says what to
change, the evidence for the problem, where to start, and when the work is
done.

The handoffs below came out of the test-suite work in
[`docs/testing-survey.md`](../testing-survey.md) (PRs #143–#157). They assume
that work has landed: start from a `master` that includes #157. Follow
[`AGENTS.md`](../../AGENTS.md) for test commands and the rules for writing
tests.

## Product bugs

| Handoff | Problem |
|---|---|
| [machine-time-all-time-cpu.md](machine-time-all-time-cpu.md) | Machine Time's all-time view may take twice the CPU the legacy route did; confirm or fix |
| [query-table-schema-reload.md](query-table-schema-reload.md) | Every staged Query Table request makes its connection re-parse the whole catalog schema twice, adding about 6 ms of CPU |

## Upstream Query Table

[`query-table-host-group-budget.md`](query-table-host-group-budget.md) requests
the final two-line allowance for explicit host budgets up to 100,000 groups in
V2 and V3. Once released, Pharos can remove its vendored backend, Go replacement
and fork test script.

[`query-table-performance-stack.md`](query-table-performance-stack.md) is
completed: Query Table v0.9.0 supplies representative reuse, validated staged
values, scalar V3 summary fusion and SQLite projection inlining. This upgrade
also resolves the [fork reduction-reuse failure](query-table-fork-reduction-reuse.md).
The only remaining adapter patch is the
[100,000-group host-budget allowance](../../third_party/query-table-go/README.pharos.md).

Requests for the Query Table library itself are collected in
[`docs/query-table-next-release-handoff.md`](../query-table-next-release-handoff.md):

- **QT-14:** fuse metrics over different fields in one pass, so Pharos can
  delete its native count and sum path.
- **QT-15:** let computed-column previews use the negotiated transport, so
  Pharos can retire its legacy row route.
- **QT-16** ([query-table-in-memory-metrics.md](query-table-in-memory-metrics.md)),
  completed in v0.9.0: V2 metrics and the row filter over rows in memory.
  Usage now answers its metrics with `ExecuteV2Rows`, filtered or not, and
  filters its hours with `CompileRowFilter`.
- **QT-17** ([query-table-in-memory-metrics-speed.md](query-table-in-memory-metrics-speed.md)):
  remove two hot spots in `ExecuteV2Rows`, per-row JSON group keys and
  `big.Rat` sums of float costs, which leave Usage's unfiltered metrics
  2–3.5× slower than the reducer Pharos deleted.
