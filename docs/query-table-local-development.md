# Trying the next Query Table release in Pharos

This branch integrates unreleased Query Table V3 features ahead of the next
published release. All four frontend packages are built from one retained Phoenix
source snapshot, with baseline commit, file hashes and archive hashes recorded
under `web/vendor`. Package manifests still identify as 0.6.0; these are explicit
prerelease snapshots. Clean builds work without a Query Table checkout.

## Worktrees and builds

Miami's changes have merged into Query Table main. Active local development uses
Phoenix. The four npm archives include compact covers, visible row/series limits,
Other remainders, persistent underline-list placement and label-line limits.
The checked-in JS/CSS assets are built from installed archives. The Go vendor
has independent provenance: Phoenix `ad41521` plus the changes recorded in
`third_party/query-table-go/source-manifest.json`.

From Pharos (Phoenix is the link script's default):

```sh
node tools/link-query-table.mjs /Users/gbw/conductor/workspaces/query-table-v1/phoenix
```

The script builds core, React, UI and codegen, installs their external runtime
dependencies without changing the lockfile, and links those four npm packages.
`.context/query-table` points to Phoenix. A gitignored `go.work` overrides the
existing Go replacement with that checkout's `backends/go`. No branch is renamed
and no packages are published. Changes to Query Table belong in Phoenix.

The frontend uses esbuild's `--preserve-symlinks`: linked packages resolve React
through Pharos's dependencies, avoiding multiple bundled React runtimes.

```sh
# Rebuild both libraries and Pharos when TypeScript/CSS changes:
cd web
npm run watch:local

# Full Pharos backend and frontend against the existing isolated dev workflow:
# (refreshes /Volumes/euclid/pharos-dev from production, as before)
cd ..
tools/dev.sh
```

Both `tools/dev.sh` and `tools/dev-ui.sh` use the combined watcher now. `dev-ui`
uses an already-running backend; use `dev.sh` to exercise the new backend APIs.
Restart the backend after editing Go, including Phoenix's Go adapter. Schema edits
still need `npm run generate-schemas` in `web`. Rerunning `npm ci` removes the npm
links; rerun the link script afterward. To restore release dependencies, remove
`go.work`, `go.work.sum`, and `.context/query-table`, then run `npm ci` in `web`.
That restores the checked-in source snapshot packages. To capture linked changes
for shipping, run `python3 tools/vendor-query-table.py /path/to/query-table`; it
packs all four packages from one isolated source snapshot and builds Pharos from
an isolated archive install. Verify `npm ci && npm run check-bundle` in a clean
checkout before shipping. Local linking remains
optional for working on both repositories at once.

The combined dev runner waits for Library, MCP metrics and text-search projections
before opening the UI, reporting pending workspace counts as it prepares. A fresh
production snapshot can need a full projection rebuild when the checkout changes
its derived columns. To restart against the existing isolated snapshot, retain the
same destination and ports and add `--reuse-library`; the default still refreshes
from production. `GET /api/ready` remains a cheap liveness check;
`GET /api/ready?projections=1` also reports projection readiness and pending counts.

The checked-in `third_party/query-table-go` production sources track Phoenix
`ad41521` plus the local changes and file hashes in its own `source-manifest.json`,
including the snapshot quota hook. The retained frontend source archive
is not the provenance of this Go adapter. The vendored
Go module keeps standalone Go builds working with `GOWORK=off`. The earlier Pharos adapter patches have landed
upstream; the old handoff describes their history, not another patch to apply.

## New Pharos host behavior

- `GET/PUT /api/query-snapshots?dataset=pharos_{dataset}&code=...`: immutable
  query/view snapshots in the library's SQLite catalog. Codes are dataset-scoped
  identifiers and require Pharos authentication. Saves and Share, named-query
  updates, Home collection actions and the native bookmark controls promote
  state. Normal edits only write a bounded browser cache. A fresh browser/Mac
  can open a permanent link without that cache.
- `GET /api/query/{dataset}/item?id=...`: complete base fields and host enrichment
  for an item, independent of the table projection/search. Missing items return
  `{ "row": null }`. Detail cards use this loader and save independent property
  layouts/defaults in library preferences. Group projections hide source item
  actions and conversation renderers.
- `POST /api/query-object-references`: bounded batches resolving workspace and
  conversation identities to labels and existing Pharos routes. Only registered
  object types/keys are accepted; missing targets remain unresolved. Schema
  relationships make these available in item properties.
- `POST /api/query/{dataset}/query-v3`: server execution over all ten datasets.
  The negotiated contract separates source WHERE from grouped result HAVING;
  source LIMIT/OFFSET cannot truncate group membership. It supports typed
  multi-key grouping, aggregate summaries, canonical computed source fields,
  representative rows, result sorting/paging, group metrics and timezone-aware
  calendar buckets. Searches retain host scope; only grouped searches reject
  incomplete results. Ordinary formula rows retain search stop metadata.
- `GET /api/query/{dataset}/capabilities` includes `queryCapabilities` with V3
  support, profile and budgets. The host permits 10 million matching source rows,
  512 MiB of narrow typed staging, 10,000 groups, 10,000 shown rows and a
  30-second deadline. V3 leases use SQLite file-backed temporary storage and
  restore the connection’s previous setting afterward. Calendar metrics select
  only their dependencies, check each scratch population and UTF-8 payload
  budget before and after materialization (including bucket keys and ordinals),
  and release each copy before the next metric. Calendar key updates use
  bounded 400-row batches. These are logical payload budgets, not an RSS or
  physical disk-size ceiling. Budget failures return an error, never partial groups.
- The older `/population` endpoint is retired and returns HTTP 410. Grouping
  and calendar metrics run on the server; there is no population-download
  fallback. Local Query Table execution is for data already in memory.

Use the standard **Add group by** control or Usage's group/comparison presets.
Groups support summaries, representative rows, HAVING, sorting and paging.
Metrics reduce the group population after HAVING; shown-row metrics reduce the
selected group page. Refresh, saved-view restoration and Home previews use the
server executor. Source-filter changes keep the source/result stages distinct.
Ordinary row pages and non-calendar metrics retain their optimized V2 path,
including Usage's per-metric hourly ledger semantics. Grouped/calendar Usage
queries operate on the table's daily facts, with hour filters selecting days
through the hourly ledger. Synthetic group identities are never item-enriched.

MCP argument JSON now uses the searchable JSON inspector. Other existing renderers
remain available alongside the library's configurable cell formats.

Snapshots preserve query/view state, not historical data or computed-definition
revisions. They are never overwritten/deleted by named-query updates. The catalog
allows 10,000 snapshots and 64 MiB of token storage across all datasets. Quota
checks and writes share a transaction; an idempotent save still works at the
quota. New saves return HTTP 507 when full. No automatic expiry invalidates old
links. The exported `ErrQuerySnapshotQuota` handling was added and tested in Miami.

Metadata requests retain a separate concurrency slot and a 10-second queue
wait. Their 30-second execution deadline starts after acquiring that slot, so
a preceding statistics request does not consume another request’s execution time.

## Remaining upstream boundaries to assess before the major release

V3 supports grouped rows, timezone-aware calendar metrics and canonical computed
source predicates. Regex source filters use SQLite's supported host expressions;
regex aggregate formulas remain outside the negotiated profile. Exact sample
reducers and calendar expansion retain their explicit adapter budgets.

Query Table currently takes its URL parameter from `itemDetails.queryParameter`.
A standalone query-URL parameter option would help hosts that manage several
mounted tables without item cards. Pharos bridges its router events to the
upstream `popstate` resolver and leaves strict decoding/reference restoration to
the library. Its old manual base64 decoder was incompatible with the new codec.

## Validation

```sh
npm --prefix web run typecheck
npm --prefix web run check-schemas
(cd web && node --test tests/*.test.mjs)
npm --prefix web run build
npm --prefix web run check-bundle
go test ./...
go vet ./...
(cd third_party/query-table-go && GOWORK=off go test ./... && GOWORK=off go vet ./...)
```

Current frontend validation uses a fresh `npm ci` install with no Query Table
symlinks. Its typecheck, schema/bundle comparison and all 124 web unit tests pass.
The vendored Go adapter and host grouping/Usage regressions are checked separately
with `GOWORK=off`.
The browser preset regression verifies grouped reloads against a source-scoped
computed catalogue and asserts that grouping never calls `/population`.

Live checks against the isolated local library on 2026-10-13:

| Dataset | Complete source rows | Groups | COUNT query |
| --- | ---: | ---: | ---: |
| Tool Calls | 977,290 | 140 | 20.4 s |
| Usage | 31,852 | 3 providers | 0.17 s |
| Library | 30,198 | 47 repositories | 0.18 s |
| Writing Messages | 28,078 | 46 repositories | 2.43 s |

A full-history Tool Calls query with COUNT, SUM(duration_ms) and a metric over
its groups also completed in 21.5 s; the metric counted all 977,290 source rows.
Usage's full summary presets and provider/model grouping were checked in the UI.

Host HTTP coverage includes all ten datasets, retired population downloads, item identities,
auth/CSRF, invalid requests, immutable conflicts, quota/idempotency and relationship
batches. Browser smoke checks use isolated synthetic tool calls and test property
cards, grouping, HAVING/summary columns and permanent-link restoration in a new
browser context. Screenshots are in this workspace's `.context` directory.

The review fixes synchronize the vendored Go adapter with Phoenix, and tests
must use `GOWORK=off` to exercise that copy. `go.work` deliberately selects
`.context/query-table/backends/go` during local development.

The previously failing `TestFindingSnoozeUntilWorseStaysWoken` used wall-clock
time for recent finding observations while the rest of the catalog used its
injected clock. All catalog finding reads now use that same clock.


Second review validation (2026-10-14): all 124 web unit tests, frontend build and
typecheck, focused host V3/lease/metadata regressions, and the Phoenix Go core,
SQLite and PostgreSQL suites pass. The vendored adapter’s SQLite and PostgreSQL suites also
pass with `GOWORK=off`. Calendar regressions exercise cleared predicates,
shown-row hidden sorting, pre-copy budgets, batched keys and per-metric cleanup.
The 1,001-row fixture includes unrelated 8-KiB labels and multiple calendar
metrics; this is a deterministic staging check, not a multi-gigabyte RSS load test.

Two focused Home browser regressions pass and were visually inspected: stale
covers remain editable after their metrics disappear, and all twenty configured
work bars retain friendly titles. The restarted dev library passes authenticated
readiness and HTML checks; its live calendar request with a cleared date predicate
returned eight daily buckets over 1,486 usage rows.


Home template editor validation (2026-10-14): initialize every field from the
current synchronous preferences before opening the modal. A regression observes
the first exposed form and saves immediately; it preserves the title,
description, lookback, metric, chart, visible limit, key and Other settings.
The original reload test also retries restored-field checks after opening.


A fresh, link-free archive install passes `npm ci && npm run check-bundle`.
The full native arm64 `macos/build-app.sh` build also completes and passes its
code-signature verification in an isolated source tree; its generated JS/CSS
are byte-for-byte identical to the checked-in assets tested in the browser.
Both the restored-template test and immediate-save regression passed three
consecutive runs. The immediate-save test also waits for the preference POST
and checks the persisted settings. All ten live V3 capability endpoints report
10,000 rows, 10,000 groups and 10,000,000 source records.
