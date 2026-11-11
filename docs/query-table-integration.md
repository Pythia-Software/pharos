# Query Table 1.0.7 in Pharos

Pharos pins core, React, UI and codegen to the published npm **1.0.7** packages,
and the Go backend to **v1.0.7**. The lockfile and `go.sum` record the released
sources. There are no vendored copies, module replacements, package patches or
build-time source rewrites. Optional upstream worktree linking is described in
[local development](query-table-local-development.md); ordinary builds need no
Query Table checkout.

The [1.0.7 release](https://github.com/Pythia-Software/query-table/releases/tag/v1.0.7),
[wire protocol](https://github.com/Pythia-Software/query-table/blob/v1.0.7/docs/wire-protocol.md)
and [error contract](https://github.com/Pythia-Software/query-table/blob/v1.0.7/docs/errors.md)
define the integration. Query Table 1.0.2 added the run-bar host slot used for
the readiness indicator; 1.0.3 added the host population counter that Pharos's
count cache plugs into (below); 1.0.4 sped up the SQLite executor. 1.0.5 adds
what the dashboards' move onto query-v3 asked for: `PERCENTILE(x, p)`, medians
and box quartiles without a per-group sample cap, percentile box whiskers and
quantile curves, calendar buckets of several intervals (`every`), confidence
intervals for ratio metrics, Other remainders for ratios, the text functions
on the Go servers, currency and compact number formats, and a chart's
colouring key. The service advertises each through `Capabilities()`; the
SQLite functions and aggregates it needs are registered from the complete
`SQLiteV2Functions()` and `SQLiteV2Aggregates()` lists on every connection.
The remaining 1.0.0 follow-ups below still apply.

## SQLite reduction reuse in 1.0.7

Query Table 1.0.7 automatically shares filtered, validated text inputs across
eligible exact `COUNT_DISTINCT` metrics with different grouping lists. Pharos's
Labels trend already sends its `[day,family]` count and independent `[day]`
denominator in one V3 request, so the released executor selects this path without
a host flag or source rewrite. Keep these metrics batched; labels overlap and
the denominator cannot be derived by summing the family counts. Repeated sort,
top-N and minimum-count variants can also reuse their grouped reductions.

Mixed scalar, distribution and calendar metric batches gain stored-source scan
sharing where eligible. Computed fields, scopes and filters retain the upstream
fallback and diagnostic rules. Pharos continues to use its normal source
projection, population counter and temporary-storage lease; no host reducer or
dependency patch is needed.

The [upstream performance report](https://github.com/Pythia-Software/query-table/blob/v1.0.7/docs/sqlite-text-distinct-performance.md)
describes eligibility and correctness coverage. The
[Labels handoff](handoffs/query-table-label-trend-latency.md) retains the original
request and a standalone reproduction for comparing released versions.

## Analyze dataset pages

Query tables in Analyze are organized by the source row described in
`data-taxonomy-model.ts`. `analyzeQueryDatasets` defines the navigation and
agent-facing site map. Navigation opens an explicit unfiltered, ungrouped record
query; page headings identify the dataset throughout query changes.

The Messages dataset reads all retained canonical messages, including user,
assistant, thinking and tool records. Authorship joins are optional and never
multiply or remove messages. Unclassified writing measures remain null. Writing
context fields retain the credited workspace and stored authorship dates separately
from the original message context, so generated writing links preserve their population. The
classified-user source `writing_messages`, workspace writing totals, daily tool
rollups and People label populations remain internal dashboard/storage sources.
They are not separate navigation offerings.

Query ideas are independent editable starting points with their own source
filters, result grouping, metrics and columns. Choosing one clears previous
query and keyword filters; All records restores the dataset's schema defaults.
Grouped drill-ins retain the chosen idea's exact source population. Idea sections
use `ViewDisclosure` so their openness round-trips in the URL. New ideas belong
in `usage-query-ideas-model.ts` and must validate against the source schema and
V3 execution contract.

## V3 execution

`GET /api/query/:dataset/capabilities` supplies `queryCapabilities` from
`QueryDatasetV3.Capabilities()` and the canonical `computedExecution` handshake.
Every current table, chart, card preview, contribution evidence read, event
example, filter suggestion, field statistic and identity lookup uses
`POST /api/query/:dataset/query-v3`.

The native React transport implements only `queryCapabilities` and
`fetchQueryV3`. Native auxiliary requests use the released V3 builders. Host
identity lookups use `fetchRowV3` against the source schema and omit collection
searches; Tool Call inspection separately enriches its bounded payload from the
existing domain detail endpoint. Standalone row readers use the same V3 wire
stage. Preview batches retain metric identities, computed revisions and host
search state, and validate the returned V3 envelope.

The handler uses `DecodeQueryRequestV3`, so the evaluation clock and both query
stages are required. `WriteQueryError` preserves typed status/code information
and hides internal database errors. The client retains codes and HTTP statuses;
resource limits are identified by code instead of matching an English message.
Per-metric failures remain independent. Failed, unavailable or partial evidence
never becomes a successful zero.

Native `fit: "screen"` / `fit: "content"` state replaces `fitToScreen` in new
queries. Upstream continues to normalize old saved links. The view-control
ledger and generated site map use the 1.0.0 declarations; disclosure and JSON
inspection ownership is unchanged. Fresh fitted views clear explicit column
widths, matching the native Fit All to Screen action; saved layouts retain
their sizing. See [URL view state](url-view-state.md).

## Execution ownership and retained host responsibilities

SQL datasets pass an authorized, projected SELECT and its bound arguments to
`QueryDatasetV3.ExecuteIn`. Ungrouped rows and metrics run directly against that
source; grouping and calendar scratch use the library's executor. Default row
budget is 10,000; metric/group budgets are 10,000, or 100,000 for Usage and
message labels. Staged map/group populations are bounded to ten million rows
and 512 MiB. Limits fail explicitly.

Pharos retains authorization, schema bindings, indexed candidate narrowing,
keyword membership, source field discovery, domain row enrichment and temporary
storage leases. Numeric bindings opt into bounded arithmetic. Map-backed
datasets stage only the base fields required by the native source plan. Leases
roll back host staging and restore connection settings, including cancellation;
changing this lifecycle remains separate work.

Every ungrouped request needs its population's exact count. SQL datasets set
the executor's `PopulationCounter` (`query_count_cache.go`): counts are
remembered in memory under Query Table's count key, the process timezone and
the `query_cache_generation` read inside the request's own transaction, so a
reused total always belongs to the snapshot of the page it is returned with.
Generation triggers cover every table these sources read, or tables kept in
step with ones that do (see `queryCacheTables`). Writes to the label and
agent-event tables also advance a count of their own (`queryCacheDomains`), so
sources and derived values that read neither (`queryCoreDatasets`,
`derivedCoreFamilies`) compare a generation without them and keep their counts
and values while those passes commit. A source or value that starts reading
one of those tables must leave its list. Map-backed and Usage-hour
sources are staged per request, and keyword record search gives each request
its own plan, so neither uses the cache. A relative filter's count is reused
only by requests with the same `now`. Tool Calls also answers unfiltered,
grouped-field equality and present/absent counts from `tool_call_cube`, but
only while the cube's ledger generation, dirty partitions, rebuild revision and
timezone all match in that transaction. `GET /api/diagnostics/query-counts`
reports, since the service started, how many counts were reused, answered from
the cube or counted, and how often a new generation cleared the cache.

Usage's daily table and hourly ledger are different populations. Source filters
first choose eligible days and hours. Metrics that depend on `hour`, including
computed expressions and metric-local filters, run against hourly facts;
`shownRows` expands the selected daily page to its hours. Other metrics and
grouping stages use daily facts. Both metric populations execute through V3.
Typed preselection also uses the published planner, preserving SQL NULL and date
semantics rather than a local approximation.

Legacy host row/metadata endpoints remain for older clients and internal domain
APIs. The removed Query Table `/rows-v2` and `/metrics` operations return an
upgrade/reload error; current consumers no longer negotiate or fall back to them.

## Patch and workaround audit

Removed in this migration:

- V2 execution, its generic native count/sum reducers, source-proof cache and
  indexed row-window execution overrides. Count reuse came back through the
  published `PopulationCounter` hook, not an override (see above).
- Machine Time's custom Query Table metric reductions and text-complement reducer.
  Domain projections and their normal catalog APIs remain separate.
- The frontend V2/V3 keyword transport switch and identical-request budget retry.
- Stable API/source-filter proxies that worked around the old nullability effect.
  The released UI now depends on `api.filterValues`, rather than the facade object.
- The obsolete frontend metadata scheduler; V3 distinct scans retain the service's
  metadata queue policy.

Still needed: numeric metric renderers recognize only upstream's built-in unit
renderer keys. Pharos retains its USD/token-to-`byte_size` presentation alias so
those metrics keep their existing formatting. This is a host presentation
workaround, not a modified package. JSON cell inspection/reveal gaps previously
recorded in [the URL-state handoff](query-table-url-state-handoff.md) remain
upstream capabilities to request, not reasons to patch a dependency.

Since 1.0.5, screen fitting lets wrapping host links shrink, and `SourcePlan`
includes a distinct request's target, so dependency discovery plans
distinct-only requests directly.

The released public source planner bounds identity plans used to short-circuit
record keyword verification. Typed filters that leave more than ten million
eligible records now fail explicitly before keyword matching. They must not
silently search a truncated identity set. The remaining presentation and planner
requests are recorded in [the 1.0.0 follow-up](handoffs/query-table-after-1.0.0.md).

Earlier performance comparisons and removal decisions are historical evidence in
[the manual-interventions audit](handoffs/query-table-manual-interventions.md),
not timing claims for 1.0.0. This migration does not run manual benchmarks.
