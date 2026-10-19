# Query Table 0.8.0 in Pharos

Pharos pins core, React, UI and codegen to the published npm **0.8.0** packages,
and the Go adapter to **v0.8.0**. The npm lockfile records registry URLs and
integrities; `go.sum` records the released Go module. The prerelease npm archives,
Go replacement, vendored adapter and historical patch have been removed. Clean
builds use these releases without another Query Table checkout.

The [upstream migration guide](https://github.com/Pythia-Software/query-table/blob/v0.8.0/docs/host-integration-next-release.md)
and [release notes](https://github.com/Pythia-Software/query-table/blob/v0.8.0/CHANGELOG.md)
define the release contract. Optional worktree linking is described in
[local development](query-table-local-development.md). The earlier
[upstream handoff](query-table-upstream-handoff.md) and
[next-release handoff](query-table-next-release-handoff.md) are historical records.

## 0.8.0 feature coverage

| Release feature | Pharos integration |
| --- | --- |
| Group link columns (`op: "link"`), `linkTargets`, `onNavigate` | Every table's `DataTable` registers each dataset as a link target (`web/src/query-link-targets.ts`, built on `queryURL`), so template and query links, including those in saved or shared queries, open the right page. Plain clicks route through `pharosNavigate` without reloading; modified clicks keep native behavior. The Usage **Work** preset links each title to `/work/{workspace_id}`. |
| Query patches (`applyPatch`, `matchPatch`, `removePatch`) | Usage **Group by** and **Compare** presets are patches that own grouping, columns and order. Highlighting matches a preset's grouping while ignoring HAVING, sorting, paging and layout edits. Link destinations can also carry patches. |
| Per-metric `where` and `minCount`, `hiddenGroups` | The Go adapter advertises and executes both on v2 metrics and v3 queries for all ten datasets. Pharos's host paths honor them: the tool-call count fast path steps aside for filtered metrics, map-backed staging includes metric filter fields, and Usage metrics filtered on `hour` read hourly populations. Home cards check the advertised flags, note hidden groups, and never present a total when groups are hidden. |
| Per-metric errors | One failing metric no longer fails its batch. Pharos discovers each metric's inputs separately, so an invalid metric cannot fail its siblings' staging or the Usage hourly split. |
| Metric drilldown (`onInspect`, `inspectBucketQuery`, `groupSourceQuery`) | Clicking a metric bar, list row, value, box cohort or histogram bin filters the table to those records, including the metric's own filter. A grouped row's **Records** action uses `groupSourceQuery` with computed fields, and is disabled when a key cannot be matched exactly. |
| `withGroupingKeys` and `exitGrouping` | The Usage **Rows** chip uses `exitGrouping`; presets enter and re-key grouping through `applyPatch`. |
| Summary column `display`, metric formats from cell displays | The Usage cache rate is a fraction with a percent display. Pharos's render fallbacks for grouped summaries only apply when a summary has no display. Links saved before this change keep their 0–100 rendering. |
| `SelectColumn.display` and `{ kind: "text", lines }` | Available from the standard column editor; Pharos never rewrites `select`, so overrides round-trip through links, saved queries and bookmarks. Schemas keep their existing defaults. |
| Scalar set filters on `enum` fields | Every enum field with static options opts in to `filter.editor: "set"` (ANY / NONE, keeping empty rows). The Go loader ignores the editor hint; predicates are unchanged. |
| `useQueryFilters` | Not needed: Pharos's record search editors edit a draft and apply it on submit. |
| `URL_ENCODE`, clearer formula errors, large-text and calendar fixes | Supplied by the packages. The Go SQLite profile rejects `URL_ENCODE`, like other text functions, so it is browser-only. |

## Earlier release features

| Release feature | Pharos integration |
| --- | --- |
| Server grouping, HAVING, summaries, MEDIAN and representative rows | All ten backend datasets negotiate V3; Usage presets and Home comparison tables use it. Source actions remain available through Records. |
| Calendar intervals, IANA timezones, empty buckets and explicit ranges | Standard metric workbench and Home previews execute on the server, with dependency, population and scratch budgets. Daily Usage facts retain their daily granularity. |
| Canonical computed source predicates, grouping and partial capabilities | Capability discovery uses `DescribeComputedPartialIn` on the V3 dataset; invalid graphs stay editable with per-definition diagnostics. V2 save validation uses the same partial API in a host transaction. Catalogues over 64 definitions are described in bounded batches. |
| Independent collection URL parameters and explicit router lifecycle | Every table supplies top-level `queryParameter`; `pharos:route` calls `restoreLocation`. `restoration.ready` gates host query work and `active` pauses hidden tables. |
| Compressed links, immutable snapshots and actionable HTTP failures | Save, Share, Bookmark and Home collections promote durable links. Snapshots use exported validation and `ResolveIn`/`SaveIn` inside the catalog's serialized quota transaction. The frontend preserves typed quota/access/conflict errors and leaves previous saves intact on failure. |
| Item modals, popovers, expansions, property layouts and relationships | Complete item loaders, library-backed property preferences and authorized reference batches are enabled. Synthetic group identities never load source items. |
| Configurable cell displays and searchable JSON inspectors | Standard table/column/property editors expose the release controls; MCP arguments use JSON display. |
| Compact covers, visible limits, Other remainders, label wrapping and underline lists | Home covers use native `MetricView`/`MetricsCards` display options while destination queries retain their full results. |
| Shared editor panels, menus, column-role themes and fit-to-screen persistence | The published UI components and theme supply these controls. Pharos sets role colors for light/dark appearances. |
| SQLite metric reuse, reduction fusion, indexed windows and exact reductions | The released Go adapter supplies these optimizations; host source planning, connection leases and regression/benchmark fixtures remain in place. |

The release's DuckDB backend, PostgreSQL adapter, standalone public demo and
saved-query prototype are alternative integrations and examples. Pharos uses its
existing SQLite catalog and library-backed saved-query UI; the standard
`QueryBuilder` already provides Save/Update/Default actions. The optional
`savedQueryControls` slot is available for a future custom presentation.

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

Metric batches return per-metric errors: a metric that exceeds a budget or
fails to compile gets its own `error` with empty buckets while its siblings and
the rows still return. Request-level problems (malformed bodies, stale
revisions, cancellation) still fail the request.

V3 adds `POST /api/query/{dataset}/query-v3` for grouped rows and calendar
metrics across every supported dataset. `/capabilities` negotiates its profile
and budgets. Source WHERE runs before summaries; result WHERE is HAVING, followed
by result sorting/paging and metrics. Group rows have stable synthetic identities
and retain enum/boolean/datetime types. Canonical computed predicates and summary
inputs are resolved against the authorized store and revision envelope. The
frontend uses V3 when grouping, calendar buckets or computed predicates require
it; ordinary pages and metrics keep their existing V2 execution path.

V3 allows 10 million matching source rows, 512 MiB of narrow staging, 10,000
groups and 10,000 shown rows, with a 30-second deadline. Incomplete searches,
invalid values, cancelled requests and exceeded budgets return errors rather
than partial groups. The old 10,000-row browser population cap does not apply.
See `query-table-local-development.md` for release builds and optional local links.

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

Home previews use the same v2 capability handshake, canonical computed revisions,
row projection and metric execution. Compatible cards share requests of up to
20 metrics, with three concurrent workers, cancellation and a one-minute result
cache. Library search context and shown-row paging remain part of the population.
Native metric covers retain the full dashboard results; a bookmark's optional
`coverMetricId` changes only its cover, while its URL opens every saved metric.
Rates, distinct counts and distribution medians are not added into totals.

Scalar `MEDIAN` is supported in both the browser core and the Go SQLite profile.
Home comparison recipes use it directly; native box distributions expose exact
quartiles and spread. Arbitrary scalar percentiles such as p95 remain unavailable.

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

V2 limits fail explicitly rather than returning approximate populations:

- 20 metrics per batch; 10,000 shown rows; 2,000 result groups before top-N.
- 250,000 matching rows by default; covered raw tool metrics allow 2 million.
  Indexed SQL row windows allow 10 million matches while validating selected
  values/computed inputs on the bounded page. Map staging retains 250,000.
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

Run the release integration checks:

```sh
GOWORK=off go test ./...
GOWORK=off go vet ./...
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
not production latency guarantees. The earlier upgrade measurements are retained in the historical handoff;
release validation uses the published packages and the checked-in browser fixtures.

The production-data pass and its remaining 500 ms misses are documented in
[query-performance.md](query-performance.md). `tools/query-bench.py` retains
request fixtures, HTTP timings, diagnostics and result digests for repeatable
comparisons. Small V3 summary/map populations use leased memory scratch storage;
raw payload populations retain disk spill and the existing byte budget.
