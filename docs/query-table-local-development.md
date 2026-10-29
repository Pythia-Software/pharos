# Query Table release builds and local development

Pharos uses published Query Table **0.9.7** npm packages (core, React, UI and
codegen) and the published **v0.9.7** Go module, unpatched. Production builds
do not depend on a Query Table worktree or source archives. See
[the integration guide](query-table-integration.md) for feature coverage,
contracts and execution limits.

## Release builds

Pharos uses **Node.js 24 LTS**, pinned in `.nvmrc` (`nvm use`), for dependency
installation and frontend checks. `tools/setup.sh` installs the locked packages,
checks schemas and builds the frontend, selecting an installed Node 24 through
nvm when necessary. Conductor runs it for new workspaces. The app/release build
also runs it and requires Node/npm; there is no prebuilt-bundle fallback.

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
integrities. `go.mod`/`go.sum` pin the released Go adapter;
`GOWORK=off go list -m -json github.com/Pythia-Software/query-table/backends/go`
shows the selected version and no `Replace`.
The JS/CSS assets in `internal/archive/assets` are built from these installed
packages and are gitignored. Run setup before a fresh checkout's Go builds or
tests: the embed directives explicitly require both bundles so a missing
frontend fails compilation. After frontend edits, run `npm --prefix web run
build` before running Go tests. `npm --prefix web run check` builds automatically,
as do the browser and e2e test runners. Codegen regenerates the Go schemas from the canonical JSON in
`schemas/`, and copies those same documents into the frontend assets.

## Updating a branch

Start with a clean feature branch, then run:

```sh
tools/update-branch.py
```

The command fetches and merges `origin/master` without committing, reinstalls
the locked npm dependencies, rebuilds the frontend, runs `npm --prefix web run
check` and `GOWORK=off go test -short ./...`, and commits the verified merge.
It never pushes. Review and push the resulting commit separately, and run the
full PR checks in [AGENTS.md](../AGENTS.md) before opening a PR.

During the transition, older branches may still track or modify the bundles.
The command keeps those files deleted from Git and regenerates ignored copies
from the merged source. It handles both content and modify/delete conflicts.
It never automatically resolves source conflicts. Resolve and stage those,
then continue the same merge:

```sh
git add <resolved-source-files>
tools/update-branch.py --continue
```

Stage new helpers too: untracked files are rejected before verification and
again before committing, so checks cannot pass using files missing from the
commit. Gitignored build outputs are allowed.

Failed installation, generation or checks leave the merge uncommitted; fix the
problem and use the same continuation command. If a schema or recipe snapshot
check fails, regenerate it with `npm --prefix web run generate-schemas` or
`npm --prefix web run generate-recipes`, review and stage it, then continue.
`git merge --abort` cancels a pending merge; rerun setup afterwards to refresh
the ignored assets. Setup replaces optional local Query Table links with the
published dependencies; restore links explicitly when returning to upstream work.

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
temporary app build uses the pinned npm packages and published Go module
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
