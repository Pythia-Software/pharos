# Query Table release builds and local development

Pharos uses published Query Table **0.9.0** npm packages (core, React, UI and
codegen) and a local copy of the **v0.9.0** Go module. The only local backend
change permits explicit host group budgets up to 100,000 in V2 and V3; the four
earlier performance patches are now supplied by the release.
Production builds do not depend on a Query Table worktree or source archives; see
[`third_party/query-table-go/README.pharos.md`](../third_party/query-table-go/README.pharos.md)
for the backend patch details. See
[the integration guide](query-table-integration.md) for feature coverage,
contracts and execution limits.

## Release builds

Pharos uses **Node.js 24 LTS**, pinned in `.nvmrc` (`nvm use`), for dependency
installation and frontend checks.

```sh
npm --prefix web ci --no-audit --no-fund
npm --prefix web run generate-schemas
npm --prefix web run typecheck
npm --prefix web run check-schemas
npm --prefix web run check-recipes
npm --prefix web test
npm --prefix web run build
npm --prefix web run check-bundle
GOWORK=off go test ./...
GOWORK=off go vet ./...
```

`web/package-lock.json` pins registry archives and external dependency
integrities. `go.mod`/`go.sum` pin the released Go adapter, and the root replacement
selects the matching copy with the two group-ceiling edits under
`third_party/query-table-go`. `tools/test-query-table-fork.sh` derives its default
upstream test tag from that Go module pin and must pass without a `-skip`
override. Upstream tests requiring an external database may still skip when
that database is not configured.
The JS/CSS assets in `internal/archive/assets` are built from these installed
packages. Codegen regenerates the Go schemas from the canonical JSON in
`schemas/`, and copies those same documents into the frontend assets.

## Optional worktree links

To change both repositories together, explicitly select the Query Table checkout:

```sh
node tools/link-query-table.mjs /path/to/query-table
npm --prefix web run watch:local
```

The link script builds all four packages, installs their external runtime
dependencies without changing the lockfile, and symlinks them into Pharos's
`web/node_modules`. `.context/query-table` points at the selected checkout. A
local gitignored `go.work` selects its `backends/go` instead of the published
module. Changes to Query Table belong in that checkout.

`macos/test/swift-harness.sh` can run while these links are configured. Its
temporary app build uses the pinned npm packages and checked-in Go backend
with `GOWORK=off`; it excludes `go.work`/`go.work.sum` from the copy and leaves
the original workspace files and package links intact.

The combined watcher keeps package output available with `--watch --no-clean`
and rebuilds Pharos. esbuild uses `--preserve-symlinks` so linked packages resolve
React through Pharos's dependencies. Schema edits still require
`npm --prefix web run generate-schemas`. Restart the backend after changing Go.

Both `tools/dev.sh` and `tools/dev-ui.sh` use the combined watcher. `dev.sh`
refreshes an isolated development library from production by default. Retain the
same destination/ports and use `--reuse-library` to reuse that isolated snapshot.
`GET /api/ready?projections=1` reports projection readiness and pending counts.

To return to published dependencies:

```sh
rm -f go.work go.work.sum .context/query-table
npm --prefix web ci --no-audit --no-fund
npm --prefix web run generate-schemas
npm --prefix web run build
npm --prefix web run check-bundle
```

Local links do not change release pins or shipped assets automatically. Before
shipping, restore published dependencies and run the release checks above. For
future upgrades, pin all four frontend packages and the Go module together, read
the upstream release/migration guide, regenerate schemas and rebuild/test.

## Browser validation

The browser suite uses the declared `playwright-core` with the installed Chrome
(or `PHAROS_BROWSER=webkit` with Playwright's WebKit):

```sh
npm --prefix web run test:browser
```

It loads every page at desktop and phone widths against stubbed data, served
through the production Query Table transport (`web/tests/support/query-server.mjs`
over `@pythia-software/query-table-core`), and checks that pages render
without errors and keep their layout: no horizontal overflow, the header in
place, and covers and cards holding their content. It does not read or modify
the production library. Query and card behaviour is covered by the unit tests
in `web/tests/unit`.
