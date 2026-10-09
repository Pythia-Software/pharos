# Pharos's Query Table Go adapter

This is the production Go module and its tests from Query Table **v0.6.0**
(`00c16461ad72a0d3d19fe8fc37ca11a9d94d1a0a`), with local patches. The
upstream MIT license is retained in `LICENSE`. The root `go.mod` pins the module
version and replaces it with this directory, so clean checkouts build without
another workspace or unpublished release artifacts.

The changes add transaction-local reuse/fusion of compatible scalar metrics,
preserve indexes for canonical datetime/ID windows, speed up exact integral
reductions, expose relative datetime resolution, and add transactional computed
saves so Pharos can validate the complete graph before committing.

See `../../docs/query-table-upstream-handoff.md` and the portable patch at
`../../patches/query-table/v0.6.0-pharos.patch`. The two fixtures in `testdata`
replace upstream tests' repository-relative fixture paths; this path adaptation
is intentionally omitted from the upstream patch.

Run `go test ./... && go vet ./...` in this directory. Root `go test ./...` does
not enter this nested module. After the patches land in an upstream release,
pin that release, remove the root replacement and this copy, and rerun schema,
frontend, adapter, integration, and performance checks before removing the patch.
