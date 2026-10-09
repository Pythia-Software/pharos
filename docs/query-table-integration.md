# Query Table 0.6.0 in Pharos

Pharos pins core, React, UI, and codegen to npm **0.6.0**, and the Go adapter to
**v0.6.0**. These were the registry's latest versions when verified on October
8, 2026 (America/Denver). The SQLite guide supplied for this upgrade and the
release's `docs/backend-sqlite.md` define the adapter contract.

The Go module is temporarily replaced by the self-contained patched copy in
`third_party/query-table-go`. Its license, tests, source provenance, and removal
procedure are included there. The upstream handoff and portable patch are in
[`query-table-upstream-handoff.md`](query-table-upstream-handoff.md) and
[`../patches/query-table/v0.6.0-pharos.patch`](../patches/query-table/v0.6.0-pharos.patch).

## Schemas and build

All ten datasets use the canonical JSON documents in `schemas/`. Explicit
`bindings.sqlite` bind to stable `r` column aliases owned by Pharos. Numeric
formula fields opt into `expressionNumeric`; numeric/datetime grouping is
explicit. Tool-call cost fields remain unbound because pricing requires host
row enrichment and is not an SQL metric input.

`web/generate-schemas.mjs` runs the upstream SQLite Go generator, formats its
output, and copies the same JSON into the embedded frontend assets. Generated
Go files are checked in. After editing a schema:

```sh
cd web
npm ci --no-audit --no-fund
npm run generate-schemas
npm run check-schemas
npm run typecheck
npm run test:computed-store
npm run build
npm run check-bundle
```

The macOS build also generates schemas before bundling. Go schema tests compare
every generated schema with the runtime SQLite loader.

## HTTP and computation

Legacy `/api/query/{dataset}`, `/aggregations`, `/distinct`, and `/field-stats`
retain the v1 contract and Pharos's optimized sources/rollups. V1 decoders reject
v2-only settings; row bodies reject trailing JSON. Signed datetime operands now
share the upstream duration grammar and one execution clock.

V2 adds:

| Endpoint | Purpose |
| --- | --- |
| `GET /api/query/{dataset}/capabilities` | SQLite metric capabilities and current canonical computed execution envelope |
| `POST /api/query/{dataset}/rows-v2` | Typed rows, computed projections and sorts, revision checks |
| `POST /api/query/{dataset}/metrics` | Scalar formulas, paired X/Y, multi-key groups, top-N, MEDIAN, boxes/histograms |
| `GET/PUT /api/query-computed-columns?dataset=pharos_{dataset}` | Canonical computed definitions with optimistic revisions |

These endpoints use Pharos authentication and same-origin checks. The computed
DDL is additive and runs during catalog initialization. A computed save runs in
one transaction, validates the edited definition's transitive graph, and preserves
every previously valid definition. Invalid formulas, missing dependencies,
cycles, and stale edits cannot introduce new broken definitions. If schema drift
invalidates stored formulas, capabilities report per-definition diagnostics and
advertise only valid graphs; the full catalogue remains available for repair,
including one-at-a-time repairs of unrelated failures. V2 resolves canonical
definitions and checks
transitive revisions in the same transaction as execution. No signed plan token
or retained cross-request snapshot is advertised.

The browser loads the canonical store and capability handshake, passes both to
`useQueryTable`, and exposes the expanded metric workbench and computed
catalogue. Saves refresh capabilities before publishing the new local definition
so the next request uses its current execution revision. Library v2 requests
retain keyword match summaries/hits and semantic
search explanations. Shared HTML documents continue using the local executor.

## Sources, limits, and performance

SQL datasets preserve Pharos's trusted joins, pricing boundary, and exclusion
of mirrored tool work. Command-search candidates use the existing trigram index;
day/week/month equality can use coarse indexed timestamp bounds. The full
upstream predicate still checks every candidate. OR narrowing requires a safe
candidate for every branch; negation never narrows incorrectly.

SQL timestamps bound as `utc-millis` must be ingested as fixed-width canonical
UTC milliseconds. Those fields and trusted text IDs keep raw BINARY ordering so
SQLite can use indexes before LIMIT. Selected fields and consumed formula
inputs retain their storage/domain checks. Other datetime bindings normalize
RFC3339 values.

Map sources retain host enrichment/search and stage one temporary table per
request. Library stages only referenced selections, filters, ordering, metric
inputs and transitive computed inputs, plus its identity field. Population reads
retain the compact cache and add long text and costs only when referenced;
unused payloads stay out of staging when an earlier request has widened the
shared cache. Page
enrichment still supplies the complete display row. The executor leases a
connection, selects `temp_store=MEMORY` for the
bounded request, and restores its previous setting before returning it to the
pool. Temporary tables are rolled back; host page enrichment happens after the
connection is released. Large text values and many concurrent requests still
increase memory usage; the row caps are not a byte-level memory guarantee.

Limits fail explicitly rather than returning approximate populations:

- 20 metrics per batch; 10,000 shown rows; 2,000 result groups before top-N.
- 250,000 matching SQL rows, or 250,000 total map-source rows before staging.
- A 30-second request deadline; row/metric bodies capped at 1 MB.
- Adapter sample budgets: exact median and box populations are bounded; see the
  upstream guide and diagnostics for per-group limits.

Identical metric plans reuse results with independent mutable payloads.
Different scalar formulas can share identical reductions; compatible reductions
with identical validated input stages and grouping are combined in one scan.
Reuse stays inside the authorized transaction. Distribution samples/edges keep
the upstream execution path and exact semantics.

Usage table rows remain daily. Metrics referencing `hour`, including through
computed dependencies, use hourly ledger populations. Daily thresholds select
days before hourly time bounds. `shownRows` hourly metrics expand only the
selected daily page into its matching hours; their `processedRows` reports the
expanded hourly population. Hour filters on row requests return the selected
daily rows. Preparation/enrichment works with a one-connection pool.

## Verification

Run the host and nested adapter separately:

```sh
go test ./...
go vet ./...
(cd third_party/query-table-go && go test ./... && go vet ./...)
go test -race ./internal/archive ./internal/querytable \
  -run 'TestGeneratedQuerySchemas|TestQueryV2|TestRelativeDatetimeMapFiltering|TestLibraryQueryTableCombinesKeywordSearchWithFilters'
go test ./internal/archive -run '^$' \
  -bench 'BenchmarkQueryTableV2|BenchmarkPharosToolLedgerV2' \
  -benchtime=1x -count=1 -benchmem
```

The upstream `backends/sqlite-tests` real-SQLite suite was also run with the
race detector and vet against the patched module. Integration coverage includes
typed rows and every bound numeric SUM across all ten schemas, aggregate/formula
matrices, paired values, distribution sidecars/shared histogram edges, computed
graphs/revisions, scope/window/order parity, storage guards, population/group
budgets, cancellation, indexed candidates, auth/CSRF, and temporary cleanup.

Synthetic and joined tool-ledger stress workloads use 10,000, 100,000, and
250,000 rows. They include integer/fractional reductions, ratios, MEDIAN, boxes,
histograms, indexed shown rows, twenty identical metrics, twenty distinct
formulas, and twenty mixed metrics. Measurements and remaining optimization
requests are in the upstream handoff. These are local stress measurements,
not production latency guarantees. Browser UI automation was unavailable in the
session (no enabled browser); frontend checks and authenticated HTTP execution
were verified instead.
