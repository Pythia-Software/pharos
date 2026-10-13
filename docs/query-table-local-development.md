# Trying the next Query Table release in Pharos

This branch integrates the Query Table source at `43b0182` (row-producing
grouping and shared query editors), ahead of the next published release. The
frontend installs four checked-in npm archives under `web/vendor`, with commit
and SHA-256 provenance recorded beside them. Clean builds work without a Query
Table checkout. The package manifests inside those archives still identify as
0.6.0; they contain the unreleased source at the commit above. Replace the archive
pins with the published version after the next release.

## Worktrees and builds

From Pharos:

```sh
node tools/link-query-table.mjs /Users/gbw/conductor/workspaces/query-table-v1/miami
```

The script builds core, React, UI and codegen, installs their external runtime
dependencies without changing the lockfile, and links those four npm packages.
`.context/query-table` points to Miami. A gitignored `go.work` overrides the
existing Go replacement with that checkout's `backends/go`. No branch is renamed
and no packages are published. Changes to Query Table belong in Miami.

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
Restart the backend after editing Go, including Miami's Go adapter. Schema edits
still need `npm run generate-schemas` in `web`. Rerunning `npm ci` removes the npm
links; rerun the link script afterward. To restore release dependencies, remove
`go.work`, `go.work.sum`, and `.context/query-table`, then run `npm ci` in `web`.
That restores the checked-in source snapshot packages. Local linking remains
optional for working on both repositories at once.

The checked-in `third_party/query-table-go` production sources also track this
upstream commit, plus the snapshot quota hook. They keep standalone Go builds
working with `GOWORK=off`. The earlier Pharos adapter patches have landed
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
- `POST /api/query/{dataset}/population`, body `{ "where": [...] }`: a complete
  filtered source for browser execution. It retains Library/message search and
  host enrichment, rejects incomplete text-search populations, and returns no
  rows when the source exceeds 10,000 rows or 16 MiB of serialized response.
  Unknown fields, malformed/trailing bodies and paging parameters are rejected.
  Execution uses the existing SQLite population budgets and 30-second deadline.

In a table, choose **Explore groups and calendar metrics**, then use the standard
Query Table grouping/metric editors. Groups support multiple keys, summary
formulas, representative rows, HAVING, result sorting and paging. Metrics on
those results operate on the group population. Rolling windows edit the source stage without changing HAVING. Source-filter
changes and Refresh reload the complete population with cancellation; mismatched/stale populations
cannot execute. Large datasets continue using server tables and SQL metrics.
Saved grouped/calendar views automatically enter exploration mode on restore;
Home previews use the same bounded source and core execution path.

MCP argument JSON now uses the searchable JSON inspector. Other existing renderers
remain available alongside the library's configurable cell formats.

Snapshots preserve query/view state, not historical data or computed-definition
revisions. They are never overwritten/deleted by named-query updates. The catalog
allows 10,000 snapshots and 64 MiB of token storage across all datasets. Quota
checks and writes share a transaction; an idempotent save still works at the
quota. New saves return HTTP 507 when full. No automatic expiry invalidates old
links. The exported `ErrQuerySnapshotQuota` handling was added and tested in Miami.

## Remaining upstream boundaries to assess before the major release

Row-producing grouping and timezone-aware calendar metric buckets still have no
Go row/metric transport protocol. Pharos provides complete local sources rather
than treating a server page as a population. Large-ledger grouping requires a
future backend protocol; source computed predicates and regex grouping formulas
retain the upstream restrictions. Calendar metrics in exploration operate on
that dataset's actual rows (Usage rows remain daily facts).

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

Host HTTP coverage includes all ten datasets, complete sources, item identities,
auth/CSRF, invalid requests, immutable conflicts, quota/idempotency and relationship
batches. Browser smoke checks use isolated synthetic tool calls and test property
cards, grouping, HAVING/summary columns and permanent-link restoration in a new
browser context. Screenshots are in this workspace's `.context` directory.

The full Go suite has an existing failure in
`TestFindingSnoozeUntilWorseStaysWoken`, also reproduced from an untouched archive
of this branch's starting commit. The targeted integration/race suites and vet pass, and the complete Go suite
passes when that one baseline failure is skipped. It is outside this integration.
