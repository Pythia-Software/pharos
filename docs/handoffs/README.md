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

## Analysis chain cost

From the "what refreshes when" survey (2026-10-29): groundwork for running the
analysis chain automatically after scheduled indexes.

| Handoff | Problem |
|---|---|
| [analysis-chain-rewrites.md](analysis-chain-rewrites.md) | Identities rewrites ~3,500 unchanged conversations, so every chain refreshes the whole Search view; writing classification rebuilds everything (≈6 min) on any new message |

## Catalog size

From the storage survey of the production library (2026-10-28). The two
rebuild the same index; read both before starting either.

| Handoff | Problem |
|---|---|
| [message-search-without-text-copy.md](message-search-without-text-copy.md) | `messages_fts` stores a second copy of all message text (25.6 GB of a 94.5 GB catalog); make it contentless without changing results |
| [message-search-without-tool-output.md](message-search-without-tool-output.md) | Tool results are ~84% of the word index; stop indexing them and make the affected searches say so (#274) |

## Capture compression

Phases of [Compressing captured originals at rest](../capture-compression.md),
shipped as #271–#273.

| Handoff | Problem |
|---|---|
| [capture-compression-1-generations.md](capture-compression-1-generations.md) | Superseded SQLite snapshot generations hold ~21.6 GB of originals uncompressed; compress them with an encoding the manifest records |
| [capture-compression-2-cold-files.md](capture-compression-2-cold-files.md) | Cold latest files and superseded versions (~27 GB, mostly Codex/Claude JSONL); readers list and stat through the manifest and decode only what they parse |
| [capture-compression-3-idle-snapshots.md](capture-compression-3-idle-snapshots.md) | Idle latest snapshots (Antigravity-CLI per-conversation databases, TL1 installations), excluding any an index reads every run |

## Upstream Query Table

Pharos uses the published Query Table module and packages without patches.
Query Table 0.9.7 delivered every request in
[`query-table-after-0.9.6.md`](query-table-after-0.9.6.md): chart labels,
colours, cycle grouping and hit targets; fit-to-screen clipping and grouped
Detail; saving host query state and last-successful persistence; minimum
counts, calendar grouping keys and OR-of-AND filters; and the V2, text-index
and metadata performance asks. Pharos took the chart, fitting and metadata
fixes on upgrading; it adopts the new capabilities in later changes.

[`query-table-fit-wrapping-links.md`](query-table-fit-wrapping-links.md) is
open: 0.9.7's fit-to-screen minimums measure a wrapping host link as one
unshrinkable line, so Analyze's fitted tables scroll at desktop widths. It can
be sent directly to the Query Table team.

That handoff superseded the requests in
[`query-table-dashboard-views.md`](query-table-dashboard-views.md),
[`query-table-fit-link-clipping.md`](query-table-fit-link-clipping.md),
[`query-table-fit-screen-controls.md`](query-table-fit-screen-controls.md),
[`query-table-group-detail.md`](query-table-group-detail.md),
[`query-table-saved-host-query-state.md`](query-table-saved-host-query-state.md)
(with [`query-table-host-search-clauses.md`](query-table-host-search-clauses.md)),
[`query-table-query-recovery.md`](query-table-query-recovery.md),
[`query-table-after-0.9.3.md`](query-table-after-0.9.3.md) and
[`usage-query-upstream-handoff.md`](../usage-query-upstream-handoff.md).
Each is marked with the release that resolved it and remains the detailed
record of its request.

[`query-table-go-link-codec.md`](query-table-go-link-codec.md) asked for Go
`EncodeQueryLink` and `DecodeQueryLink`, at parity with `encodeQuery` and
`decodeQueryStrict`, for the [site map](../site-map.md)'s link tools. Query
Table 0.9.6 resolved it.

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
