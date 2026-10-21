> Historical prerelease integration record. Pharos now uses the published
> [release packages](query-table-integration.md); source archives, the Go vendor,
> the patch and the snapshot-packaging script described below have been retired.

# Query Table next-release handoff from the Pharos integration

Current integration update (2026-10-13): Pharos negotiates V3 server execution for
grouped tables, calendar metrics and computed source filters on all ten datasets.
QT-01, QT-02 and QT-10 are implemented in the retained dirty source snapshot;
QT-03's canonical computed path is available. The browser population workaround
below describes the earlier integration. See `query-table-local-development.md`
for the current API, budgets and validation.

Prepared October 13, 2026. This is a review draft: each numbered request can be
copied independently into the Miami worktree. No Query Table issues or PRs have
been opened for these requests.

## Integration context and evidence

Pharos integration merged in [PR #117](https://github.com/Pythia-Software/pharos/pull/117),
master commit `0014760e2c8cb21b37d7b39bd8a713962e5d5096`. Clean-checkout npm
installation/typechecking/schema checks/tests/bundle comparison passed with the
checked-in source archives; the local symlinks are optional.

Pharos integrated Query Table commit
`43b018279b44b2a91b8ccd8c553744a99093cff7`, including item properties, configurable
cell/JSON displays, calendar metrics, compressed/snapshot links, and grouped rows.
Query Table worktree: `/Users/gbw/conductor/workspaces/query-table-v1/miami`.
Pharos worktree: `/Users/gbw/conductor/workspaces/pharos-v1/davao`.

Pharos has a SQLite catalog, host search/enrichment, canonical computed-column
storage, and datasets large enough that downloading everything is impractical.
Its production catalog on this machine is roughly 67 GiB. The adapter supports
server rows and metrics; the new grouped/calendar paths currently use a complete
filtered population with explicit 10,000-row and 16-MiB response caps. Those are
Pharos's fallback limits, not Query Table's intrinsic limits. They prevent a page
or partial text-search result from masquerading as a complete dataset.

Evidence is in the current Query Table source/docs and the Pharos changes:

- `internal/archive/query_table_exploration.go`: populations, item reads,
  immutable snapshot storage and quotas.
- `internal/archive/query_table_v2.go`: canonical computed capabilities,
  source dependency selection, SQL execution and shared clocks.
- `web/src/index.tsx`: exploration mode, item details, route integration.
- `web/src/query-links.ts`, `search-composer-state.ts`, `search-dialog.tsx`:
  compressed/reference links and source-stage rolling windows.
- `web/src/storage.ts`, `bookmarks.ts`, `query-previews.ts`: permanent saves,
  updates, synchronous bookmark metadata and grouped/calendar previews.
- `web/tests/unit/query/query-host-integration.test.mjs`, `query-previews.test.mjs`,
  `internal/archive/query_table_exploration_test.go`: behavioral regressions.

Browser checks exercised item cards, COUNT/SUM/MEDIAN/representative group
columns, HAVING, sorting, calendar metrics and a permanent link in a fresh
browser context. Broken links prevented row/metric execution. The frontend has
76 passing tests. Go integration/race and adapter suites passed; the earlier
findings-snooze failure also reproduces on untouched Pharos code and is unrelated.

## Suggested review order

| ID | Priority | Classification | Request |
| --- | --- | --- | --- |
| QT-01 | High | Documented missing feature | Server execution of grouped result rows |
| QT-02 | High | Documented missing feature | Server calendar/timezone metric grouping |
| QT-03 | Medium | Documented restriction | Computed and isolated-regex source grouping |
| QT-04 | Medium | Integration API request | Complete-source execution with remote metadata and a shared clock |
| QT-05 | Medium | Confirmed API coupling | Query parameter independent of item details |
| QT-06 | Medium | Integration API request | Explicit router restoration and lifecycle state |
| QT-07 | Medium | Patch already in Miami + confirmed UX issue | Snapshot quota errors and actionable browser messages |
| QT-08 | Medium | Store API request | Transactional snapshot saves and exported validation |
| QT-09 | Medium | Existing outstanding adapter request | Partial computed-capability descriptions |
| QT-10 | Medium | Performance API request | Source dependency discovery for grouping/local previews |
| QT-11 | Low | Migration/development documentation | A host integration and release migration guide |
| QT-14 | Medium | Performance API request | Fuse metrics over different fields into one pass |
| QT-15 | Low | Confirmed API coupling | Computed-column previews through the negotiated transport |

“High” means high value for Pharos before broad use of the next release; it does
not assert that the release must be blocked. Documented unsupported features
below are not mislabeled as execution bugs. Proposed API names are illustrative.

## QT-01 — Execute row-producing grouping on the backend

**Observed:** `docs/grouping.md` explicitly limits grouped rows to complete
`clientRows` without a transport. Loading a grouped query into a backend table
fails before fetching. Neither Go backend has the grouped-row protocol.

**Pharos consequence:** the new table grouping controls cannot run directly over
large tool-call or message ledgers. Pharos switches to a bounded complete-source
endpoint, so users must narrow their filters before grouping.

**Request:** negotiate grouped-row execution through a versioned transport and
implement it in SQLite and PostgreSQL. Preserve this stage order:

`source WHERE → GROUP BY / summaries / representative → HAVING → result ORDER BY → result OFFSET/LIMIT`.

Return the projected schema and stable group identity. Support typed multi-key
groups, ordinary aggregate formulas including supported exact MEDIAN, and ROW
columns from one shared representative selected by lexicographic ordering and
the documented source-ID tie break. Source paging must never truncate groups.
Metrics should reduce groups after HAVING, with shown-row scope reducing only the
displayed group page. Retain host field allowlists, computed revision envelopes,
snapshots and population/group/sample/timeout budgets.

**Acceptance:** compare complete-client and backend results for null versus empty
keys, multiple keys, mixed key types, tied/null representative sort values,
HAVING, output sorting/paging, COUNT/SUM/AVG/MEDIAN and group metrics. Verify that
changing source LIMIT/OFFSET does not change group membership, and all ROW
projections come from the same source record.

## QT-02 — Execute calendar and timezone metric groups on the backend

**Observed:** `packages/core/src/metrics.ts` returns
`unsupported_date_grouping`; `encode.ts` also rejects sending date-grouped
aggregations through the legacy aggregation path. `metricDates.ts` implements
hour/day/week/month/year, IANA timezones and empty buckets locally.

**Pharos consequence:** otherwise server-capable metric dashboards must switch
to complete client populations for these date settings. Fixed SQL day/week/month
columns do not express a user-selected timezone or configurable empty buckets.

**Request:** extend the metric wire contract and capabilities with calendar
grouping, timezone, include-empty and range settings. Implement adapters with
the same calendar boundaries and null semantics as the local engine, including
additional group dimensions and derived/grouped result fields where supported.
The host should still choose its dataset granularity; a daily fact row must not
be silently turned into hourly usage evidence.

**Acceptance:** local/backend parity around spring-forward and fall-back,
repeated hours, half-hour DST transitions, Monday weeks, month/year boundaries,
explicit custom ranges and null timestamps. Empty buckets need correct COUNT,
SUM and AVG semantics, and filling must honor the group/calendar budgets.

## QT-03 — Extend grouped source support to canonical computed fields and isolated regex

**Observed:** `docs/grouping.md` explicitly rejects source computed-field
predicates, regex source filters and regex aggregate formulas in this path.
`createGroupingModel` builds its catalogue from the supplied source schema;
Pharos's separately stored computed definitions are not automatically a complete
grouping input catalogue.

**Request:** support host-approved canonical computed keys, source filters,
summaries and representative ordering, with transitive definitions and revisions
resolved consistently. Advertise individual capabilities and reject unsupported
operations before execution. Add regex only through the established isolated
worker/watchdog or appropriate backend profile; retain explicit rejection until
that execution path exists. Do not enable unbounded regex evaluation on the UI
thread as a workaround.

**Acceptance:** groups derived from a canonical numeric/text computed column,
transitive dependencies, revision conflicts, missing/invalid definitions,
source computed predicates, and cancellation/timeouts for any enabled regex.
Unsupported forms must retain editable state and explain the limitation.

## QT-04 — Separate complete-source execution from remote metadata services

**Observed:** backend tables disable local grouping, while passing only the
current server page as `clientRows` would be incorrect. Pharos therefore removes
the transport when it enters exploration mode, fetches a complete filtered
population itself, and separately supplies a full-item loader. This also loses
transport-based autocomplete/stats in the local path. The server and local core
currently capture their execution clocks independently.

**Request:** provide an explicit complete-source/local-execution mode that can
retain remote item loading, distinct values, stats and computed metadata. A
source provider should carry completion/coverage, the source query identity,
limits and one evaluation clock (or an equivalent resolved-predicate contract).
This is useful even before the server grouping work lands.

**Acceptance:** never accept a paginated or incomplete source as complete; never
execute a stale population after filters change; cancel superseded requests;
retain server autocomplete when a scoped population has only one value. Test a
relative-time boundary with network delay so server source filtering and local
HAVING/metrics cannot silently disagree about “now.” The clock concern is a
contract risk identified in this integration, not a separately reproduced
boundary failure.

## QT-05 — Give query URL state its own parameter option

**Observed:** `useQueryTable.ts` selects its URL parameter from
`opts.itemDetails.queryParameter`, falling back to `q`. The controller options
have no independent top-level query parameter.

**Pharos consequence:** several mounted tables share routes and use
`q_library`, `q_tool_calls`, etc. Naming the collection parameter currently
requires supplying item-detail configuration even when a host needs no cards.

**Request:** add an independent collection `queryParameter` option. Keep the
existing item-details setting as a backward-compatible fallback, with documented
precedence. Use the resolved parameter consistently for restoration, edit
synchronization, Save/Share, item links, duplicates and navigation.

**Acceptance:** two mounted controllers using distinct parameters must not
restore or overwrite each other's state; naming a parameter must not inject a
Detail column or enable item views. Legacy item-details-only configuration
must continue to work.

## QT-06 — Expose router restoration and readiness explicitly

**Observed:** the controller restores navigation through `popstate`, whereas
Pharos's existing router uses `pushState` and emits `pharos:route`. Pharos bridges
that event by dispatching a synthetic `popstate`. It also tracks active routes
externally to prevent hidden tables from synchronizing into the address bar.

**Request:** expose a cancellable controller method to restore a location, plus
explicit restoration state such as resolving/ready/error. Alternatively support
an injected location source/router adapter. Restore through the same strict
inline/reference resolver used on initial load; do not duplicate decoding or
let hosts distinguish restoration by parsing user-facing errors. A readiness
seam should let hosts defer complete-source fetches as well as row/metric calls.

**Acceptance:** pushState navigation, back/forward, unrelated host parameters,
hash-only changes, hidden/active table transitions, a slow snapshot resolution,
and an intervening query edit. Missing/duplicate/corrupt links must preserve the
incoming URL and block all dependent execution until repaired or reset.

## QT-07 — Keep the quota hook and surface useful snapshot failures

**Already in Miami, uncommitted:** the integration added `ErrQuerySnapshotQuota`
in `backends/go/query_snapshots.go`, maps it to HTTP 507 in
`NewQuerySnapshotsHandler`, and adds `TestQuerySnapshotQuotaHTTP`. Those two files
are the only Query Table changes from this integration. Review/adopt the
existing diff rather than pasting a second implementation. Pharos's copy of the
adapter also includes it. This handoff does not commit or merge that worktree.

**Confirmed remaining UX issue:** `httpQuerySnapshotStore` discards the server
reason and throws only `Query snapshot request failed (507).` A quota, lack of
permission or storage outage is therefore difficult to act on. This is visible
with any fetch stub returning 507.

**Request:** retain a typed HTTP status/error code and show safe actionable
messages, with bounded handling of both JSON and plain-text error bodies. The Go
handler currently uses plain-text `http.Error` for errors; the observed 403
response correctly has `text/plain`, so there is no incorrect-JSON-header bug to
fix. Optional host formatting/custom error mapping would also work. Clarify
that snapshots preserve state, not historical dataset contents, and that a
quota blocks new saves without breaking existing links.

**Acceptance:** quota 507, permission 403, immutable conflict 409, unavailable
reference 404, and infrastructure 500 are distinguishable. Failed saves/updates
must preserve the previous named query/default and retry the same logical
snapshot code. Existing resolve and idempotent save must still work at quota.

## QT-08 — Export snapshot validation and allow host-owned transactions

**Observed:** `SQLQuerySnapshotStore.Save` owns its insert/read through `DB`;
`validateQuerySnapshot` is private. Pharos implements a custom repository to
check library-wide row/byte quotas and insert atomically in its own catalog write
transaction. HTTP validation protects that path, but there is no reusable public
validator for another authorized write path.

**Request:** expose snapshot validation and a `SaveIn` transaction hook (or a
transactional policy API) so hosts can enforce quotas without duplicating the
immutable store implementation. Continue to supply scope/dataset from trusted
host authorization, preserve canonical timestamps, and keep quota/retention
policy host-owned. A schema definition or small migration hook could complement
this, but automatic expiration of permanent links is not requested.

**Acceptance:** concurrent saves cannot exceed a host quota; same-code/same-state
is idempotent even when full; same-code/different-state conflicts; rollback
leaves no snapshot; malformed/compressed-oversized tokens are rejected through
HTTP and direct validated saves. Test SQLite and PostgreSQL transactions.

## QT-09 — Describe healthy computed definitions despite schema drift

**Observed:** Pharos still implements `describeQueryComputed` around the Go
`DescribeComputedIn` API. It compiles definitions individually so one invalid
persisted graph does not hide every healthy computed capability. This remains
an outstanding request from the earlier Pharos adapter handoff.

**Request:** add an official partial-description API returning the valid
execution envelope and per-definition diagnostics from one authorized
transaction. Keep the existing all-or-error method compatible. The metadata
catalogue should remain complete and editable, including broken definitions.
Infrastructure/context failures must still fail the request, not be relabeled
as a bad formula.

**Acceptance:** mix healthy definitions with invalid syntax, deleted fields,
missing dependencies and cycles. Advertise only valid transitive graphs and
current revisions; retain actionable diagnostics; allow incremental repair of
unrelated failures without invalidating healthy definitions.

## QT-10 — Expose grouping source dependencies for narrow population reads

**Observed:** Pharos's complete-source endpoint currently selects every bound
base field. Tool calls have many columns, some of them long text. The ordinary
v2 Library path already uses compiler dependencies to stage only required
inputs, but there is no analogous complete grouping/local-preview source plan
in the backend protocol.

**Request:** expose a dependency plan for grouping/local metrics that includes
keys, summary expressions, representative ordering/ROW projections, source
predicates and canonical transitive computed inputs. Preserve source identity
and fields required for deterministic ordering. Keep result/HAVING field names
separate from source dependencies. Hosts should be able to load only those
inputs and lazy-load complete items through the existing item API.

**Acceptance:** narrow and full reads produce identical groups/metrics;
unreferenced long text stays out of staging; representative-only and computed
inputs are retained; source and result aliases cannot be confused. Budget and
cancellation failures must not fall back to incomplete projections.

## QT-11 — Document the host migration and simultaneous-worktree workflow

**Actual integration fixes in Pharos, not Query Table execution bugs:**

- Pharos's old search composer manually decoded raw base64 JSON. The `2j.`/
  `2d.` codec and `@code` references invalidated that assumption. It now uses
  strict decoding and asynchronous reference resolution.
- Existing recipe/rolling-window helpers edited top-level WHERE. In grouped
  mode that is HAVING, so Pharos now renews bounds in `grouping.source.where`.
- Custom source-record readers/actions needed to hide on group projections;
  QueryBuilder/MetricsPanel needed the projected catalogue. Item property
  loading needed complete rows rather than the current table SELECT.
- Linking packages from another checkout risks duplicate React runtimes;
  Pharos uses esbuild `--preserve-symlinks` and installs external dependencies.
- Running package watchers with `--clean` briefly removed `dist` while dependent
  watchers read it, producing transient module-resolution errors. A clean
  initial build followed by `--watch --no-clean` fixed that observed race.
- Before publication, unchanged 0.6.0 registry pins were insufficient for new
  frontend exports. Pharos now pins checked-in npm archives with commit/hash
  provenance, while local links remain optional.

**Request:** publish a consumer migration/example guide covering those seams,
canonical Save/Share/Bookmark integration, stable storage adapters, named-query
updates/default IDs, item relationships, configurable display versus renderer
precedence, and the server/local capability boundaries. Include a multi-table
host router and a complete-source example alongside the single-table demo.

**Acceptance:** a clean consumer fixture builds from the intended release;
a linked fixture edits both repositories without duplicate React or temporary
missing-package errors; legacy/new inline and permanent links restore; grouped
recipes retain source/HAVING semantics; custom renderers receive the correct
row shape. No host should silently treat `decodeQuery('@code')` as a default
query or use a server page as a complete grouping population.

## QT-12 — Representative datetime ordering should accept the same values as filters

**Observed during the card revamp:** local `createGroupingModel(...).representative()` converts a datetime with `Date.parse(String(raw))`. Epoch-millisecond numbers become invalid dates, so they fall through to the source-ID tie-break. A workspace's older row can supply its representative title even with `last_usage_at DESC NULLS LAST`. Numeric datetime values already work in the local query's source predicates and metric execution.

**Minimal reproduction:** use one workspace with IDs `old` and `new`, different titles, and `last_usage_at` values `clock - 100` and `clock` as finite epoch-millisecond numbers. Group by workspace ID with a ROW(title) summary and representative order `last_usage_at DESC NULLS LAST`. The current local result selects `old`; replacing those two timestamps with RFC3339 strings selects `new`. Pharos's production backend uses RFC3339 timestamps and supplies the correct title; the Pharos representative-title regression test uses that production shape.

**Request:** normalize representative datetime values through the shared datetime reader, or explicitly document/reject unsupported representations. Avoid silently treating a valid instant as null.

**Acceptance:** numeric epochs, Date values and RFC3339 strings representing the same instants choose the same representative (for every representation the public API supports); invalid dates retain explicit null placement and deterministic ID tie-breaking. Add parity coverage with the Go/server path.

## QT-13 — Investigate distribution preview cost at catalog scale

**Status:** reproducible performance report; ownership between the host projection and Query Table execution has not yet been isolated.

**Observed:** the `output-distribution` recipe in Pharos over a 30-day local calendar window times out after 15,024 ms as a standalone preview request, outside the landing-page fanout. The same card also failed during the real-catalog gallery check. The period contains approximately 205,000 tool calls before the recipe's nonnull output-token predicate. Other ranked/grouped covers complete over that period.

**Exact request shape:** POST `/api/query/tool_calls/metrics`, source WHERE `result_tokens IS NOT NULL` plus `started_at >= window_start AND started_at < window_end`; two metrics: a 12-bin histogram of `[result_tokens]` grouped by `result_tokens_source` with groupLimit 4, and Tukey box summaries of `[result_tokens]` grouped by `tool_name, result_tokens_source` with groupLimit 8. Pharos negotiates the SQLite profile and does not fetch a sample population.

**Request:** profile this paired-distribution path and distinguish source materialization cost from reduction/sorting cost. Check whether companion distributions repeat source scans or retain unnecessarily large per-group buffers. Recommend a host-facing execution approach if a synchronous preview cannot meet a reasonable small-card budget.

**Acceptance:** reproduce against a comparable skewed 200,000-row fixture, report where the time/memory goes, and verify cancellation stops expensive work. Any optimization must preserve exact box summaries, measured/estimated source separation and complete coverage; a sampled result must not be presented as an exact distribution.

## QT-14 — Fuse metrics over different fields into one pass

> Updated audit and assignment: [manual interventions, request A](handoffs/query-table-manual-interventions.md#a-fuse-v2-reductions-across-fields-qt-14).
> Cross-field V2 fusion is delivered in Query Table v0.9.3; removal of Pharos's
> SQL fast path still needs host parity and cost validation. The in-memory reducer and
> `query_metrics_exact.go` mentioned below were deleted in Pharos #174 after
> adopting the published in-memory executor.

**Observed:** the SQLite V2 adapter fuses metrics only when they read the same
field, and reduces every other metric in its own pass over the source. When
Pharos moved its pages from its legacy aggregation route to `/metrics`
(Pharos #157), two surfaces got 3–16× slower:

- **Message mix.** Human Words' message mix sends nine sums over nine fields, so it made ten passes of about 30 ms each where one pass had taken about 48 ms in total.
- **Usage Token chart.** The chart's sums and cost totals also staged Usage's rows into a temporary table on every request.

Pharos recovered legacy speed with a host fast path, which the next Query Table release could make unnecessary:

- `internal/archive/query_metrics_native.go` answers plain counts, sums and distinct counts in one native statement, or over rows already in memory.
- `query_metrics_exact.go` reproduces the adapter's exact decimal sum and single rounding.

**Request:** fuse compatible reductions over different fields in one scan when they share a grouping and filter: count, sum, min, max, average and distinct count. Keep the adapter's exact arithmetic, its per-metric errors (unsafe integers, out-of-range values, text in number fields), and its group and population budgets. Where possible, also avoid staging a copy when the source is already a plain table or view.

**Acceptance:**

- N metrics over different fields read the source once.
- Results are identical to reducing each metric separately, including exact decimal sums (for example ten 0.1s summing to exactly 1), NULLs, empty strings, and every error case.
- Pharos's parity tests (`TestNativeMetricsMatchTheAdapter*` in `internal/archive/query_metrics_native_test.go`) pass with the native path switched off at legacy-comparable speed, so Pharos can delete it.

## QT-15 — Computed-column previews through the negotiated transport

**Observed:** Query Table's computed-column preview calls `transport.fetchRows` unconditionally (in the 0.8.0 React dist), and the `Transport` type requires `fetchRows`. Pharos therefore keeps its legacy row route, POST `/api/query/<dataset>`, alive only for that preview. The same applies to any host that has moved its tables to `fetchRowsV2` and `/query-v3`.

**Request:** route computed-column previews through the transport the host negotiated through capabilities, using `fetchRowsV2` or V3 when available. Make `fetchRows` optional for hosts that advertise those.

**Acceptance:**

- A host that implements only `fetchRowsV2`/V3 and metrics can render tables, metric panels and computed-column previews.
- A host without capabilities keeps working on `fetchRows`.
- Previews show the same rows and diagnostics either way.

## Avoid duplicate or unrelated reports

The earlier Pharos requests for metric reuse/fusion, indexed canonical datetime
windows, exact integral reductions, exported relative datetime resolution and
transactional computed saves have landed in the source snapshot. The old
`patches/query-table/v0.6.0-pharos.patch` is historical, not a patch to reapply.
QT-09 is the partial-description request that still needs a public API.

The invalid grouped smoke query that initially reused the source field name
`call_count` as a summary ID was a host/test mistake. The library correctly
rejected that collision; the corrected unique-ID query worked. It is not a bug
report. The 10,000-row fallback cap, Pharos's snapshot quota, field relationships,
search-completion checks and daily/hourly Usage fact semantics remain host-owned.

No historical-results snapshot feature, arbitrary SQL access, automatic expiry
of shared links, public sharing without host authorization, or removal of
execution budgets is being requested.

## Implemented in Phoenix: underline metric lists

Long list labels can now use `display.list.barPosition: "below"` with
`display.kind: "list"`. Labels wrap across the available row, values remain at
the right, and a thin bar spans the row underneath. `"inline"` retains the
existing layout. The metric editor exposes **Bar placement → Under label**.
Core normalization and query links retain this option, without changing metric
computation identity. `showBars`, `showValues`, and group colors remain supported.

Pharos chooses the underline layout for long card labels and representative
work titles, caps cover labels at two lines, and retains full labels in their
titles and grouped destinations. An explicit saved bar placement takes priority.
Core/UI package snapshots include this feature for clean installs. Rendering,
editor application, URL reload, and desktop/phone card layout were verified.

The wrapped label limit is now a native `display.list.maxLabelLines` setting.
Positive integers cap the label when bars are below it; omission allows unlimited
lines in Query Table. **Maximum label lines** in the metric editor accepts a
blank value to remove the limit. Inline bars ignore the setting, full tooltip
text remains available, and query links retain it without changing computation.
Pharos's compact card presentation supplies two lines when no limit is saved,
honors an explicit limit, and no longer hardcodes truncation in host CSS.

## Implemented in Phoenix: compact axes, loading and display limits

Compact chart axis titles now share a footer with ↑ / → direction indicators.
The plot uses a narrower tick-only gutter. Native metric loading has an
accessible spinner with reduced-motion support.

`display.maxGroups` controls visible groups separately from computed
`groupLimit`; `display.showOther` controls exact additive remainders in lists
and pies. Hiding Other on a pie retains the full denominator and leaves the
omitted share empty. Lists never combine averages, rates, or incomplete data
without an exact remainder. These options are available in the metric editor.

Pharos's card-template editor also exposes visible rows / series, Chart key,
and Other remainder. Settings persist through template edits and collection.
Single-series timeline keys are hidden automatically; multiple series keep
their key unless explicitly hidden. Larger list/box selections get taller
covers so the requested rows fit. Card loading has a spinner and animated
placeholder bars, and both timeline renderers share the new axis footer.


## Review resolutions (2026-10-14)

All eleven integration review issues have fixes and regressions:

| Issue | Resolution |
| --- | --- |
| 1 | Limited/incomplete searches return ordinary rows with formulas; only grouping requires a complete search. Both stop conditions retain their search metadata. |
| 2 | Ungrouped V3 returns formula errors in individual cells. The vendor includes Phoenix's fix. |
| 3 | Group partitions, joins and identities include error codes, keeping NULL and failed formula keys separate. Representative rows and summaries use the same partitions. |
| 4 | Ungrouped V3 preserves host defaults and includes unselected sort dependencies in its source plan. |
| 5 | PostgreSQL shown-row metrics retain staged error bindings. Verified on PostgreSQL. |
| 6 | Editing a collected card preserves kind, visible rows, Other and legend when the metric is unchanged. |
| 7 | Usage Work projects workspace_id. Records drill-in is enabled and filters that workspace. |
| 8 | Relative window mode survives URL synchronization, search submission and saved bookmarks. Window reconciliation waits for restoration. |
| 9 | Host name changes invalidate derived query caches; observation timestamps do not. |
| 10 | Home tests use the current search editor and inline/durable query codecs. Routing restores the destination table directly, including mounted inactive tables. Fixtures return valid V2 execution envelopes. |
| 11 | Dev readiness retries HTTP 5xx responses and still rejects 4xx errors. |

The calendar empty-series guard and generic HTTP 500 response were already
implemented upstream and are now vendored. V3 execution deadlines return 504.
Unused Core/UI archives were removed: four active npm archives and two source
archives remain. The Go baseline and hashes are in
`third_party/query-table-go/source-manifest.json`.

The snooze failure was a pre-existing mixed-clock bug: recent observations used
wall-clock time instead of the catalog clock. Catalog reads now use its injected
clock. Both reported archive failures passed ten isolated repetitions, and the
full archive suite passed in 664 seconds. The Library status failure was not
reproduced, so no speculative business-logic change was made.

Validation includes GOWORK=off build/vet, the full archive suite, host regressions
for the vendored executor, Query Table SQLite/PostgreSQL integration suites
against Phoenix and the vendor, and nested adapter tests with PostgreSQL enabled.
Packaged frontend dependencies pass typecheck and all 121 web unit tests. Dev
supervisor tests cover transient 500 and fatal 401 readiness checks.

Home browser validation covered all 32 cases: 30 passed in the full run, and
both remaining failures were corrected and passed in focused reruns. One stale
assertion targeted the former axis element; the current shared footer is checked
instead. Collection and reload checks wait for asynchronous preference writes.
Headline timelines with a key retain enough vertical room for a legible plot.
Desktop/phone layout checks and the Usage Work Records drill-in also pass.
The existing dev library is running with Phoenix links at localhost:55071.


## Second review resolutions (2026-10-14)

The six follow-up issues are addressed:

| Issue | Resolution |
| --- | --- |
| 1 | Replacing a saved query clears obsolete `cover.metricId` and legacy cover references. Home also recovers from stale preferences with no available metrics, allowing title and description edits. Valid covers retain their display settings. |
| 2 | Pharos V3 uses SQLite `temp_store=FILE`, restoring the original connection mode afterward. V2 retains its existing memory policy. |
| 3 | Query-range calendar filling ignores empty date predicates, matching row-filter behavior. |
| 4 | Each calendar scratch table contains only its dependencies, receives population/byte checks before and after copying, and is dropped after that metric. Bucket keys and ordinals count toward the payload budget; indexed pages and up to 400 keys per UPDATE replace per-row writes. |
| 5 | Friendly-label rows are fetched to cover the configured metric/group display limit, including covers with 20–100 rows. |
| 6 | Docs distinguish historical frontend source archives from Phoenix `ad41521` Go sources and the explicit local changes in the Go manifest. |

The calendar changes are implemented in Phoenix and copied to Pharos’s vendor.
`backends/sqlite-tests/calendar_review_test.go` and
`backends/postgres-tests/calendar_review_test.go` contain integration regressions
for cleared date filters, scratch budgets, batching, lifetime and shown-row sorting.
Pharos retains cleared-filter and shown-row tests for standalone vendor verification.

The metadata queue concern is also addressed: its ten-second queue limit is
separate from the thirty-second execution deadline, which begins upon acquisition.
The concurrency cap remains in place.

Resource validation checks SQL execution shape and budgets on real SQLite and
PostgreSQL. It does not claim a measured peak-memory ceiling for a multi-gigabyte
catalog. SQLite file-backed staging can still use bounded page caches, and the
staging budget counts logical payload rather than database/index overhead.

Validation passes against both Phoenix and Pharos’s vendored module on real
SQLite and PostgreSQL, with standalone Go build/vet, all 124 web unit tests,
frontend typecheck/schema/bundle checks, and nine dev-supervisor tests. Two
focused Home browser regressions pass and were visually inspected. The existing
isolated dev library is ready at localhost:55071, using Phoenix; a live cleared
calendar filter query returned eight buckets over 1,486 usage rows.


## Packaging and capability review resolutions (2026-10-14)

All four frontend modules now come from one retained Phoenix snapshot based on
`29549f3d490ca47e1a8b0d86100385632717b65f`, including visible row/series limits
and Other remainders. The package pins, npm integrities, provenance and source
manifest agree. Historical mixed frontend snapshots were replaced by one source
archive. The checked-in assets are rebuilt from an archive install.

`tools/vendor-query-table.py` captures the current Query Table sources into an
isolated build, packs all four modules, and rebuilds Pharos from those archives.
Use it to capture further Phoenix changes before shipping. Local npm links alone
do not update the shipped packages.

The clean-install bundle mismatch was reproduced with the old archives.
`npm ci && npm run check-bundle` now passes without Query Table symlinks, along
with typechecking, schema checks and all 124 web unit tests. Package/archive
integrities and every retained source-file hash were verified.

The capabilities handler now supplies the schema-only source required by V3
validation. Its HTTP regression checks all ten datasets advertise exactly
10,000 rows, 10,000 groups and 10,000,000 source records.

Three focused browser checks pass against installed archives, covering saved
visible limits, native Other rendering, twenty friendly labels, and larger
list/box covers. Editor initialization is awaited before asserting restored
values. All ten live capability endpoints report the same expected limits.
Phoenix development links are restored and their build also matches the shipped
assets with lockfile versions of external dependencies.


## Home editor initialization follow-up (2026-10-14)

A deterministic immediate-save regression reproduced the default-limit flash.
Library preferences are supplied by the blocking script before React mounts;
the template editor opened its modal partway through initializing its React
state. Modal focus could flush earlier fields while display settings were stale.
The edit action now initializes every field together, and the modal opens after
that committed render. An immediate save preserves all saved presentation and
lookback settings. The reload test retries restored-field checks as well.


A fresh, link-free archive install passes `npm ci && npm run check-bundle`.
The full native arm64 `macos/build-app.sh` build also completes and passes its
code-signature verification in an isolated source tree; its generated JS/CSS
are byte-for-byte identical to the checked-in assets tested in the browser.
Both the restored-template test and immediate-save regression passed three
consecutive runs. The immediate-save test also waits for the preference POST
and checks the persisted settings. All ten live V3 capability endpoints report
10,000 rows, 10,000 groups and 10,000,000 source records.
