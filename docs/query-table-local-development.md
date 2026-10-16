# Query Table release builds and local development

Pharos uses published Query Table **0.7.1** npm packages (core, React, UI and
codegen) and the **v0.7.1** Go module. Production builds no longer depend on a
Query Table worktree, source archives or a vendored adapter. See
[the integration guide](query-table-integration.md) for feature coverage,
contracts and execution limits.

## Release builds

```sh
npm --prefix web ci --no-audit --no-fund
npm --prefix web run generate-schemas
npm --prefix web run typecheck
npm --prefix web run check-schemas
(cd web && node --test tests/*.test.mjs)
npm --prefix web run build
npm --prefix web run check-bundle
GOWORK=off go test ./...
GOWORK=off go vet ./...
```

`web/package-lock.json` pins registry archives and external dependency
integrities. `go.mod`/`go.sum` pin the released Go adapter without a replacement.
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

The browser fixtures use Playwright from a gitignored local installation and an
installed Chrome when available:

```sh
npm install --prefix .context/browser-tests playwright --no-audit --no-fund
node --test tests/usage-presets-ui.test.cjs tests/tools-ui.test.cjs tests/home-ui.test.cjs
```

These exercise grouped reloads, route restoration, undo/redo after table pausing,
compact covers, saved display limits and Other remainders. Fixtures stub their
HTTP data and do not read or modify the production library.

The older `library-query-ui.test.cjs` and `mcp-ui.test.cjs` fixtures currently
have four failures that also reproduce on the pre-upgrade branch: a Library
layout expectation, two outdated search/filter interactions, and an incomplete
MCP API stub. Their failures are independent of the 0.7.0 dependency upgrade.
