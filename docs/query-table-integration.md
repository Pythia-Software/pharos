# Query Table 1.0.1 in Pharos

Pharos pins core, React, UI and codegen to the published npm **1.0.1** packages,
and the Go backend to **v1.0.1**. The lockfile and `go.sum` record the released
sources. There are no vendored copies, module replacements, package patches or
build-time source rewrites. Optional upstream worktree linking is described in
[local development](query-table-local-development.md); ordinary builds need no
Query Table checkout.

The [1.0.1 release](https://github.com/Pythia-Software/query-table/releases/tag/v1.0.1),
[wire protocol](https://github.com/Pythia-Software/query-table/blob/v1.0.1/docs/wire-protocol.md)
and [error contract](https://github.com/Pythia-Software/query-table/blob/v1.0.1/docs/errors.md)
define the integration. 1.0.1 only speeds up SQLite execution; it has no API
or client changes, so the 1.0.0 declarations and follow-ups below still apply.

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

- V2 execution, its generic native count/sum reducers, count caches, source-proof
  cache and indexed row-window execution overrides.
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
workaround, not a modified package. Screen fitting still floors wrapping host
links at their one-line width; the existing Usage browser TODO records this
upstream layout limitation. JSON cell inspection/reveal gaps previously
recorded in [the URL-state handoff](query-table-url-state-handoff.md) remain
upstream capabilities to request, not reasons to patch a dependency.

Dependency discovery for distinct-only requests currently plans an equivalent
row projection because upstream omits the distinct target from `SourcePlan`.
The original distinct request still executes through V3 unchanged.

The released public source planner bounds identity plans used to short-circuit
record keyword verification. Typed filters that leave more than ten million
eligible records now fail explicitly before keyword matching. They must not
silently search a truncated identity set. The remaining presentation and planner
requests are recorded in [the 1.0.0 follow-up](handoffs/query-table-after-1.0.0.md).

Earlier performance comparisons and removal decisions are historical evidence in
[the manual-interventions audit](handoffs/query-table-manual-interventions.md),
not timing claims for 1.0.0. This migration does not run manual benchmarks.
