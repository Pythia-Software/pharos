# Handoffs

Each file here is a self-contained assignment for an agent. It says what to
change, the evidence for the problem, where to start, and when the work is
done. Follow [`AGENTS.md`](../../AGENTS.md) for test commands and the rules for
writing tests.

Only open handoffs live here. When one is resolved, delete it and record what
landed in the doc that describes the feature (or in the pull request); git
history keeps the original request.

## Product bugs

| Handoff | Problem |
|---|---|
| [machine-time-all-time-cpu.md](machine-time-all-time-cpu.md) | Machine Time's all-time view may take twice the CPU the legacy route did; confirm or fix |
| [usage-v3-hour-grouping.md](usage-v3-hour-grouping.md) | `/query-v3` groups usage by `hour` over daily records, where it is null; only `/metrics` reads the hourly ledger |
| [query-table-schema-reload.md](query-table-schema-reload.md) | Every staged Query Table request makes its connection re-parse the whole catalog schema twice, adding about 6 ms of CPU |

## Catalog size

| Handoff | Problem |
|---|---|
| [message-search-without-text-copy.md](message-search-without-text-copy.md) | `messages_fts` stores a second copy of all message text; make it contentless without changing results. Tool output already left the index in #274, so re-measure first |

## Design and follow-up work

| Handoff | Problem |
|---|---|
| [analysis-readiness-contract.md](analysis-readiness-contract.md) | Result-scoped freshness: executed-query receipts and shared CPU/GPU admission are not implemented yet |
| [claude-export-compaction-and-incremental-imports.md](claude-export-compaction-and-incremental-imports.md) | Repeated Claude Chat exports pile up duplicate payloads and are re-read in full on every import |
| [plugins-design-review.md](plugins-design-review.md) | Review the importer and exporter plugin design, its public contracts and implementation plan |
| [plugin-importers.md](plugin-importers.md) | Take over the import side of the plugin design: pull transcripts from SSH hosts, sandboxes and buckets on a cadence. The revised design is [importer plugins](../plugin-imports.md) |

## Upstream Query Table

Pharos consumes published Query Table releases without patches (currently
1.0.9; see [Query Table in Pharos](../query-table-integration.md)). These
requests are open upstream:

| Handoff | Problem |
|---|---|
| [query-table-log-axes.md](query-table-log-axes.md) | Native log axes give the smallest bar zero length and unround ticks, and histograms bin evenly, so long-tailed histograms cannot take a log axis |
| [query-table-significant-digits.md](query-table-significant-digits.md) | Compact numbers format by fraction digits, so token counts read `123.4K` instead of `123K` |
| [query-table-pie-color-by.md](query-table-pie-color-by.md) | Compound pie and donut slices ignore `colorBy` |
| [query-table-row-pipeline-cost.md](query-table-row-pipeline-cost.md) | A V3 metric over a large SQL source copies rows through one validation stage per field: 8.5 s where the hand-written aggregate takes 2.4 s on 1.5 million rows |
| [query-table-after-1.0.0.md](query-table-after-1.0.0.md) | Numeric metrics still take no host renderer (§1, in part) and there is no unpaged identity plan (§2) |
| [query-table-manual-interventions.md](query-table-manual-interventions.md) | Audit of the former local patches; the host SQL reduction and scratch-lifecycle decisions remain, and direct V3 grouping is an upstream performance opportunity |

Earlier numbered requests (QT-14, QT-15) are recorded in
[`docs/query-table-next-release-handoff.md`](../query-table-next-release-handoff.md).
