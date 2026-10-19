# Working on Pharos

Pharos is a Go service (`internal/archive`, one package) with an embedded web
UI. The page shell and its scripts live in `internal/archive/assets/`
(`ui.py` holds the HTML; `library.js`, `sync.js`, `carbon.js` and others sit
beside it). The Query Table frontend is TypeScript in `web/src`, bundled into
the checked-in `internal/archive/assets/query-tables.{js,css}`. See
[docs/technical-overview.md](docs/technical-overview.md) for the architecture.

## Running tests

Run the narrowest tests that cover your change while you work, and the fast
Go tier on every change. Before opening a PR, run `go test ./...`,
`npm --prefix web run check` and `npm --prefix web run test:full`.

| What | Command |
|---|---|
| Go, one test while iterating | `go test -vet=off -run 'TestName' ./internal/archive` (`-vet=off` skips vetting the whole package on every run) |
| Go, fast tier | `go test -short ./...` (skips the slowest tests, which call `slowTest(t)`) |
| Go, everything | `go test ./...` |
| JS unit tests (`web/tests/unit`, node only, seconds) | `npm --prefix web test` from any directory |
| One JS unit file or test | `npm --prefix web test -- card-recipes --test-name-pattern=wishlist` (arguments are file-path filters, plus `--test-*` flags) |
| JS unit tests, full matrix | `npm --prefix web run test:full` (every window, chart kind and edit in `card-parity`/`cover-parity`) |
| One browser file | `npm --prefix web run test:browser -- <page>-ui` (runs `tests/<page>-ui.test.cjs`) |
| JS checks before a PR | `npm --prefix web run check` (typecheck, schemas, recipe snapshot, bundle, unit tests) |
| Python tools | `python3 -m unittest discover -s tools` |
| Frontend bundle | `(cd web && npm run build && npm run check-bundle)` after any `web/src` or `pricing/*.json` change |
| Recipe snapshot | `npm --prefix web run generate-recipes` after changing card recipes; it rewrites `implemented_recipes` in `docs/homepage-query-cards.json`, which Go's `TestHomeQueryRecipesV2` executes |

- **JS unit tests** live in `web/tests/unit/**` and import `web/src` and
  schema JSON directly. `web/tests/run.mjs` bundles them once with esbuild
  into a gitignored `web/.test-build/` and runs them with `node --test`;
  invoke tests through it, since the source files cannot be run by
  `node --test` directly. Cross-language fixtures that Go also reads live in
  `testdata/`.

- **Browser tests:** they need Playwright installed once per workspace:
  `npm install --prefix .context/browser-tests playwright --no-audit --no-fund`.
  Run only the files for the pages you changed. Several browser files fail
  on master; [docs/testing-survey.md](docs/testing-survey.md) lists them and
  the plan to replace them.
- **Manual only:** `macos/test/*` (builds the app and mounts disk images),
  `tools/*bench*`, and `tools/test-query-table-fork.sh` (after changing
  `third_party/query-table-go`). Run these only when you change the code they
  exercise. Go probes, which time or inspect a real catalog, build only with
  `-tags probe`; each file's header says how to run it.

## Writing tests

- **Test logic in node or Go, not in a browser.** Formatting, parsing,
  aggregation, query and URL building, and state transitions all get unit
  tests against the module that owns them.
- **Browser tests check only what a browser can.** Examples:
  - layout invariants, such as no horizontal overflow at 390 px;
  - pages rendering without errors;
  - focus moving into and back out of dialogs.

  The UI changes often, so do not assert on exact CSS values, pixels,
  colours or copy, on DOM structure, or on a removed control staying absent.
- **No fixed sleeps.** Wait for a condition. Take screenshots only behind an
  environment variable, and never assert on them.
- **Do not freeze an implementation.** A test should survive a refactor that
  keeps behaviour. Avoid exact SQL text, `EXPLAIN` output, or a second copy of
  an algorithm kept as an oracle.
- **Extend the existing test when fixing a review finding.** Do not add
  `*-review` or `*_regression` files.
- **Keep fixtures small.** Insert seed rows in one transaction (`seedTx`,
  `execTx`). When a test needs to exceed a product limit, make the limit a
  package variable the test can lower with `override`, rather than inserting
  thousands of rows.
- **Use the Go harness** in `internal/archive/harness_test.go`:
  - `testCatalog` copies a catalog initialized once per process. Use
    `freshCatalog` only when what `Initialize` does to a new catalog is the
    subject.
  - `testServer` instead of `NewServer`, so that no background work outlives
    the test.
  - `slowTest(t)` for a test that takes seconds, so that `-short` skips it.

  `TestMain` keeps tests off this Mac: no `ioreg`, `scutil`, `diskutil` or
  GitHub, no user git configuration, and catalogs without drive flushes.
  Tests of those opt back in, for example with `stubDriveTool` or
  `useDurableCatalog`.
- **Keep data-safety tests**, even when slow. These include adopt, backup,
  capture, durability, and cross-host merges. Make their fixtures cheaper
  instead.
