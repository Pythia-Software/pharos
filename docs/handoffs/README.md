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
| [usage-v3-hour-grouping.md](usage-v3-hour-grouping.md) | `/query-v3` groups usage by `hour` over daily records, where it is null; only `/metrics` reads the hourly ledger |
| [query-table-schema-reload.md](query-table-schema-reload.md) | Every staged Query Table request makes its connection re-parse the whole catalog schema twice, adding about 6 ms of CPU |

## Upstream Query Table

Pharos uses the published Query Table module and packages without patches.
The current standalone assignment is
[`query-table-after-0.9.3.md`](query-table-after-0.9.3.md): close the remaining
V3/V2 reduction cost gaps and let computed previews use modern transports.
It includes the 0.9.3 baseline, complete reproduction context and acceptance
criteria, and can be sent directly to the Query Table team.

[`query-table-dashboard-views.md`](query-table-dashboard-views.md) is a
standalone assignment against 0.9.5 from the dashboard analysis views. It
asks for:
- chart group labels that fit instead of clipping their start;
- marks coloured by their whole group key;
- hour-of-day and weekday grouping with per-occurrence statistics and
  drill-down;
- a minimum count over distinct values.

It can be sent directly to the Query Table team.

[`query-table-go-link-codec.md`](query-table-go-link-codec.md) is a standalone
assignment against 0.9.5. It asks for Go `EncodeQueryLink` and
`DecodeQueryLink`, at parity with `encodeQuery` and `decodeQueryStrict`, so
Pharos can build and read query links in Go for the
[site map](../site-map.md)'s link tools. It covers the parity contract, the
pitfalls a reviewed reference port found, and how to prove parity. It can be
sent directly to the Query Table team.

The [manual-interventions audit and upstream handoff](query-table-manual-interventions.md)
records the previous patches and source snapshots, the 0.9.3 response, and the
remaining host SQL reduction and scratch-lifecycle decisions. Cross-field V2
fusion is delivered and host parity passes; deleting the faster host reducer
remains a performance decision. Direct V3 grouping remains an upstream
performance opportunity.

These requests are completed:

- [`query-table-performance-stack.md`](query-table-performance-stack.md), in
  v0.9.0: representative reuse, validated staged values, scalar V3 summary
  fusion and SQLite projection inlining. It also resolved the
  [fork reduction-reuse failure](query-table-fork-reduction-reuse.md).
- [`query-table-host-group-budget.md`](query-table-host-group-budget.md), in
  v0.9.1: explicit host budgets up to 100,000 groups. Pharos then removed its
  vendored backend, Go replacement and fork test script.

Earlier numbered requests for the Query Table library are recorded in
[`docs/query-table-next-release-handoff.md`](../query-table-next-release-handoff.md):

- **QT-14:** cross-field V2 scalar fusion is delivered in v0.9.3. Pharos's
  native count and sum path remains pending the host performance decision.
- **QT-15:** let computed-column previews use the negotiated transport, so
  Pharos can retire its legacy row route.
- **QT-16** ([query-table-in-memory-metrics.md](query-table-in-memory-metrics.md)),
  completed in v0.9.0: V2 metrics and the row filter over rows in memory.
  Usage now answers its metrics with `ExecuteV2Rows`, filtered or not, and
  filters its hours with `CompileRowFilter`.
- **QT-17** ([query-table-in-memory-metrics-speed.md](query-table-in-memory-metrics-speed.md)),
  completed in v0.9.2: `ExecuteV2Rows` without per-row JSON group keys or
  `big.Rat` sums of float costs. Usage's unfiltered metrics are now faster
  than the reducer Pharos deleted.
