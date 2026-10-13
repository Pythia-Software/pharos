# Pharos's Query Table Go adapter

The production sources track Query Table commit **43b0182** (the next-release
source integration), with its snapshot quota hook from the Miami worktree.
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
