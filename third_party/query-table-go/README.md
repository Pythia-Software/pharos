# Pharos's Query Table Go adapter

The production sources track Phoenix commit **ad41521cf735c70d073280c832102d27c117da3c**,
which includes the V3 executor, computed-cell errors, default and hidden-field
sorting, calendar empty-series handling, and separate identities for errored
group keys. The HTTP deadline fix returns 504 for an execution timeout.
`source-manifest.json` records the exact production file hashes, baseline and
local calendar fixes. Calendar metrics stage only their own dependencies, check
scratch budgets including date keys, release each copy promptly, and key rows in
bounded batches.
The vendored source itself is the retained snapshot; the older frontend source
archives are historical and do not describe this Go revision.
Existing adapter regressions and local fixtures are retained; snapshot tests
also track upstream. The upstream MIT license is retained in `LICENSE`. The root `go.mod` pins the module
version and replaces it with this directory, so clean checkouts build without
another workspace or unpublished release artifacts.

The upstream sources include transaction-local reuse/fusion of compatible scalar metrics,
preserve indexes for canonical datetime/ID windows, speed up exact integral
reductions, expose relative datetime resolution, and add transactional computed
saves so Pharos can validate the complete graph before committing.

See `../../docs/query-table-local-development.md` for the current integration.
The earlier `../../docs/query-table-upstream-handoff.md` and portable patch are
historical; those adapter fixes have landed upstream. The two fixtures in `testdata`
replace upstream tests' repository-relative fixture paths; this path adaptation
is intentionally omitted from the upstream patch.

Run `GOWORK=off go test ./... && GOWORK=off go vet ./...` in this directory. Root `go test ./...` does
not enter this nested module. After the next release includes these APIs,
pin that release, remove the root replacement and this copy, and rerun schema,
frontend, adapter, integration, and performance checks before removing the patch.
