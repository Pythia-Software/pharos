# Query Table 0.9.7 in Pharos

Pharos pins core, React, UI and codegen to the published npm **0.9.7** packages,
and the Go adapter to the published **v0.9.7** module, with no replacement or
local copy. The npm lockfile records registry URLs and integrities; `go.sum`
records the released Go module. Clean builds use these pinned sources without
another Query Table checkout.

The [upstream migration guide](https://github.com/Pythia-Software/query-table/blob/v0.9.7/docs/host-integration-next-release.md)
and [release notes](https://github.com/Pythia-Software/query-table/blob/v0.9.7/CHANGELOG.md)
define the release contract. Optional worktree linking is described in
[local development](query-table-local-development.md). The earlier
[next-release handoff](query-table-next-release-handoff.md) is a historical record.

## 0.9.7 changes

0.9.7 delivers every request in
[the after-0.9.6 handoff](handoffs/query-table-after-0.9.6.md). This upgrade
takes the type changes and the fixes that need no Pharos feature; the new
capabilities are adopted in later changes.

- **Where terms.** A term can be an OR of AND branches
  (`{ any: [{ all: [a, b] }, c] }`). Pharos code that walks terms reads every
  branch's predicates (`predicatesOf`) to find fields, describes the term as
  "(a and b) or c", and rewrites it branch by branch or not at all
  (`web/src/where-terms.ts`). Slice and category readers skip it, and the
  message filter editor shows it as one removable condition. In Go, the SQL
  dataset's WHERE, its indexed candidate source, the Machine Time matcher and
  the map executor (`internal/querytable`) evaluate `WhereTerm.Branches()`.
- **Grouping keys** are `GroupKey[]`; Pharos reads a key's column with
  `groupKeyField`, and descriptions name a calendar key's interval.
- **Minimums.** `AggSpec.MinCount` is a `MetricMinCount`. The native V2
  reducer, the Machine Time cubes and the count-only fast path answer no
  clause with an active minimum, so the adapter runs row, distinct and summed
  minimums alike.
- **Capabilities.** Pharos serves the library's own `Capabilities()` for V2
  and V3, so it advertises `minCountMeasures`, `metricMinCountMeasures`,
  `calendarCycles` and `calendarGroupKeys`.
  `TestQueryV3RunsEveryAdvertisedShape` runs each over a SQL dataset and over
  staged map rows. Host fast paths decline date grouping (cycles included),
  calendar keys and minimums, so these shapes always reach the adapter.
  Grouped Home previews pass `metricMinCountMeasures` to
  `metricPopulationError`, as the library's own table does.
- **Fixes taken as they are.** Chart label gutters, whole-key colours for
  multi-key pies, bars and boxes, nearest-point picking on line and scatter
  charts, fit-to-screen minimum widths for links and Detail and JSON buttons,
  and the source-filter metadata flood. The browser suite's `KNOWN` list is
  empty again. Pharos keeps its metadata scheduler
  (`web/src/query-metadata.ts`): the service runs one metadata scan at a time,
  and keeping one request in flight stops waiting reads from holding HTTP/1
  connections the rows need. The dashboard scatter still puts the breakdown
  first, because scatter charts still colour by their first key.

## 0.9.6 changes

The Go query-link codec (`EncodeQueryLink`, `DecodeQueryLink`) lets the
[site map](site-map.md)'s link tools build and read links in Go. Metric
windows, shares and whole intervals, and `--qt-touch-target-scale`, are
available; Pharos keeps its own trend smoothing.

## 0.9.5 changes

Distinct filter values now open in a content-sized combobox menu independently
of the compact filter input. Long names wrap and long lists scroll within the
viewport, including in mobile filter dialogs. Freeform entry, pointer/keyboard
selection and input focus are preserved, and stale suggestions are discarded.
This resolves the [filter value menu handoff](handoffs/query-table-filter-value-menu.md).

## 0.9.4 changes

Ordinary V3 execution now owns eligible direct SQLite grouping and its exact
fallback. Pharos passes the authorized narrow source and original request to
`QueryDatasetV3.ExecuteIn`. The general `nativeGroupedRows` pre-reducer, its
synthetic source rows/schema fields, MAX summary rewrites and source-total
correction have been removed. Group identities, HAVING, ordering, paging,
source budgets and result metrics remain library-owned. Source filters still
select the full population before result paging.

The frontend transport implements modern V2/V3 rows without legacy `fetchRows`.
Computed-column previews negotiate those methods in Query Table. Library
**Select all** prefers negotiated V3 with the execution schema and computed
handshake, preserving canonical computed predicates and sorting even though the
table catalogue describes those fields as derived. V2 is a fallback for
V2-only transports; it cannot execute computed filters under Pharos's policy.
Selection requests identities from offset zero and validates the response's
schema, execution context and computed revisions.
It rejects incomplete or oversized results before replacing the selection and
shows failures in the toolbar. The legacy HTTP row routes remain available for
older clients; current tables, previews and selection no longer need them.

The synthetic HTTP probe at this upgrade measured ordinary V3 at 66 ms versus
45 ms with the former host pre-reducer, with complete response parity. This
trades about 22 ms on that 50,000-row fixture for removing the custom execution
path. V2 still measured 49–77 ms through the adapter versus 16–33 ms through
Pharos's cached reducer, so `nativeMetricsV2` remains enabled. The domain-specific
Machine Time V2 cubes and indexed text complements also remain. These are local
warm medians, not production or concurrent-dashboard guarantees. See
[the release's direct grouping contract](https://github.com/Pythia-Software/query-table/blob/v0.9.4/docs/sqlite-direct-groups.md)
and [preview migration](https://github.com/Pythia-Software/query-table/blob/v0.9.4/docs/computed-columns.md#modern-row-transports).

The existing scratch lease/connection policy is a separate host migration,
tracked in [the schema-reload handoff](handoffs/query-table-schema-reload.md).

## 0.9.3 changes

SQLite V2 batches now fuse compatible scalar reductions across different
fields. Guarded integer reductions can read resolved stored-column sources
directly; unsupported storage and numeric domains retain exact decimal
execution. Metric predicates, scopes, errors, sorting and budgets stay
independent. This supplies QT-14's library capability without changing Pharos's
requests.

The release also qualifies scratch-table cleanup, handles interrupted creation,
and returns V3 cleanup errors before reporting success. Successful caller-owned
V2/V3 transactions may be committed after adapter cleanup; failures must roll
back. Pharos still needs to drop its own staging tables and change its lease
and connection policy before adopting that lifecycle. At that release there was
no direct V3 grouping path.

See the [manual-interventions audit](handoffs/query-table-manual-interventions.md)
for host parity/timing results and the remaining removal decisions, and the
[upstream performance/lifecycle guide](https://github.com/Pythia-Software/query-table/blob/v0.9.3/docs/sqlite-performance-handoff.md)
for the supported contract. Both general host SQL reducers remained enabled in
0.9.3; 0.9.4 removes the V3 pre-reducer as described above.

## 0.9.1 and 0.9.2 changes

- **0.9.1** accepts an explicit host group budget up to 100,000 in SQLite V2
  and V3. Usage opts into it; other datasets keep their budgets. It was the
  last change Pharos patched locally, so the vendored backend, its Go
  replacement and its fork test script are gone.
- **0.9.2** speeds up `ExecuteV2Rows` (typed group keys, decimal sums by
  scale). Usage answers its metrics with it, filtered or not, and selects its
  hourly rows with `CompileRowFilter`; see `usageMetricsInMemory` and
  `selectUsageHours`. On `TestUsagePerformanceFilteredMetrics` (20,000 daily
  and 60,000 hourly synthetic rows, `-tags probe`) the Token summary takes
  about 8 ms unfiltered and 3–11 ms filtered, against about 115–200 ms staged.
  0.9.2's frontend packages also compute `shownRows` metrics from the
  browser page when a transport lacks them; Pharos advertises native
  `shownRows`, so its routes are unchanged.

## 0.9.0 backend changes

The release supplies representative-row reuse, validated staged values and
error sidecars, fused scalar SQLite V3 summaries, and bounded native projection
inlining that preserves expensive input evaluation and V2 reduction reuse.
Pharos no longer carries its implementations of those four optimizations.
The release also adds `ExecuteV2Rows` for metrics over authorized maps, which
replaced Pharos's own in-memory Usage reducer. Pharos's native SQL path for
plain counts and sums over SQL datasets remains host code.

## 0.8.0 feature coverage

| Release feature | Pharos integration |
| --- | --- |
| Group link columns (`op: "link"`), `linkTargets`, `onNavigate` | Every table's `DataTable` registers each dataset as a link target (`web/src/query-link-targets.ts`, built on `queryURL`), so template and query links, including those in saved or shared queries, open the right page. Plain clicks route through `pharosNavigate` without reloading; modified clicks keep native behavior. The Usage **Work** preset links each title to `/work/{workspace_id}`. |
| Query patches (`applyPatch`, `matchPatch`, `removePatch`) | Usage **Group by** and **Compare** presets are patches that own grouping, columns and order. Highlighting matches a preset's grouping while ignoring HAVING, sorting, paging and layout edits. Link destinations can also carry patches. |
| Per-metric `where` and `minCount`, `hiddenGroups` | The Go adapter advertises and executes both on v2 metrics and v3 queries for every dataset. Pharos's host paths honor them: the tool-call count fast path steps aside for filtered metrics, map-backed staging includes metric filter fields, and Usage metrics filtered on `hour` read hourly populations. Home cards check the advertised flags, note hidden groups, and never present a total when groups are hidden. |
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
| Server grouping, HAVING, summaries, MEDIAN and representative rows | Every backend dataset negotiates V3; Usage presets and Home comparison tables use it. Source actions remain available through Records. |
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

Every dataset uses its canonical JSON document in `schemas/`. The map-backed `parallelism` (estimated agent concurrency by local hour) and `findings` (one row per finding, from the Findings view) datasets back the Home cards for those areas; Usage rows also carry Carbon Impact estimates computed with the saved settings. Explicit
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
npm test
npm run build
npm run check-bundle
```

The macOS build also generates schemas before bundling. Go schema tests compare
every generated schema with the runtime SQLite loader.

## HTTP and computation

Legacy `/api/query/{dataset}`, `/distinct`, and `/field-stats` retain the v1
contract and Pharos's optimized sources/rollups. V1 decoders reject v2-only
settings; row bodies reject trailing JSON. Signed datetime operands now share
the upstream duration grammar and one execution clock. The legacy
`/aggregations` route was removed: it answers 410 Gone, telling a page loaded
before an upgrade to reload. Every metric goes through `/metrics` or
`/query-v3`.

`/metrics` answers some requests itself, in the same result shape and with
the adapter's values, and passes everything else to the adapter:

- **Bare counts** of Tool calls come from the rollup (`countOnlyMetricsV2`).
- **Machine Time's cubes** (calls, timed calls and total duration by
  repository, activity, status and operation, and separately by day or month
  and status) come from the cached tool-cube projection
  (`machineTimeMetricsV2`), with a 50,000-group budget per metric, so months
  of calls need not be read one by one. The page never crosses days with
  operations, so a longer period adds few groups: on a library of a million
  calls in October 2026 the largest cube had about 12,000 groups over 90 days
  and 20,000 over all time.
- **Plain integer counts and sums** grouped by text fields (`nativeMetricsV2`)
  share one guarded SQL reduction and generation-scoped bounds/count caches.
  Unsupported shapes and storage retain ordinary adapter execution. Upstream
  cross-field fusion is available, but the measured residual strict-proof cost
  still makes this host cache/reducer useful.
- **Usage metrics** run through Query Table's `ExecuteV2Rows` over authorized
  daily/hourly row maps, retaining exact decimal arithmetic without SQL staging.

These run in a plain read transaction; requests the adapter executes lease
memory-backed temporary storage. Usage's V2 metric group budget is 100,000,
as V3's is; Tool summary keeps 10,000, and other datasets keep 2,000.

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
groups (100,000 for Usage) and 10,000 shown rows, with a 30-second deadline. Incomplete searches,
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
day/week/month equality can use indexed timestamp bounds. The full upstream
predicate still checks candidates. A separate, complete substring index can
answer supported ASCII `contains` predicates of at least three characters
exactly, including the compiler's nonempty guard for negation. Only an exact
term is removed from residual filtering; other text operators and incomplete
indexes retain the ordinary predicate. OR narrowing requires a safe relation
for every branch.

Tool calls also have a narrow, transactionally maintained fact projection for
identity, timing, activity, category, provider, and local calendar. Eligible
counts and integer grouped summaries reduce these facts before staging and
return compact groups to the adapter for identities, HAVING, ordering, paging,
and result metrics. Fresh cube counts and supported text-complement reductions
use the same read snapshot. Unsupported shapes retain general execution.
See [Usage audit repairs](usage-audit-repairs.md) and the
[upstream handoff](usage-query-upstream-handoff.md) for exactness boundaries and
parity tests. Filter suggestions share a small host metadata queue, so they
cannot fill the browser's connections ahead of foreground data requests.

SQL timestamps bound as `utc-millis` must be ingested as fixed-width canonical
UTC milliseconds. Those fields and trusted text IDs keep raw BINARY ordering so
SQLite can use indexes before LIMIT. Selected fields and consumed formula
inputs retain their storage/domain checks. Other datetime bindings normalize
RFC3339 values.

Map sources retain host enrichment/search and stage one temporary table per
request; Usage metrics are the exception, answered from its rows in memory
unless the executor reports a shape unsupported. Library stages only referenced selections, filters, ordering, metric
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
race detector and vet against the module then patched. Integration coverage includes
typed rows and every bound numeric SUM across the dataset schemas, aggregate/formula
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
