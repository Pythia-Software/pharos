# Working on Pharos

Pharos is a Go service (`internal/archive`, one package) with an embedded web
UI. The page shell and its scripts live in `internal/archive/assets/`
(`ui.py` holds the HTML and its inline script; `reader-model.js` holds the
conversation reader's DOM-free model; `library.js`, `sync.js`, `carbon.js` and
others sit beside it). The Query Table frontend is TypeScript in `web/src`,
bundled into the gitignored `internal/archive/assets/query-tables.{js,css}`. See
[docs/technical-overview.md](docs/technical-overview.md) for the architecture.

## Publishing releases

Use the [quick release workflow](docs/releases-and-updates.md#quick-release-default)
for routine publishing requests. Fetch `origin/master` and tags, temporarily
check out that exact commit, run `macos/release.sh X.Y.Z`, then restore the
workspace branch and return the download link. Use the requested version;
default to Developer ID signing and notarization; use `--adhoc` only when requested.

A release-only request does not require merging master into the workspace
branch or running test suites. The testing requirements below apply to source
changes and PRs. Do not add Go, JavaScript, browser, native or end-to-end test
runs, unrelated failure investigations, or custom release-note edits unless
requested. Keep the release script's built-in build and artifact checks. If
the release command fails, address that specific failure and resume publishing.

Release-only tasks must never run release-machine setup or create/import signing
keys. Missing deployment configuration or signing keys is a blocker to report;
signing and publication belong on the separately configured deployment Mac.

## Query Table dependency policy

Do not add, extend, or reintroduce local Query Table patches. Required fixes
and features belong in the upstream Query Table repository and must be
consumed through a published release. Write a self-contained upstream handoff
when a needed change is unavailable. Do not vendor or replace the Go module, or
patch the module cache, installed npm packages or generated bundles, to work
around upstream behavior. Normal package upgrades, schema generation and
frontend bundle rebuilds are part of the integration: pin all four npm
packages and the Go module to the same release.

## Running tests

The JS tooling needs Node 24 LTS, pinned in `.nvmrc` (`nvm use`) and in
`engines` in `web/package.json`; `web/tests/run.mjs` refuses an older Node.

Run `tools/setup.sh` on a fresh checkout before Go builds or tests. It installs
the locked npm packages and builds the ignored frontend; Conductor runs it as
workspace setup. Go explicitly embeds both bundles, so a missing bundle fails
compilation. Run `npm --prefix web run build` again after changing frontend
sources or dependencies. `npm --prefix web run check` and browser/e2e test runs
build the frontend themselves. Never commit generated JS/CSS bundles.

To merge master, use `tools/update-branch.py`: it fetches `origin/master`,
merges without committing, rebuilds the frontend from locked dependencies, runs
the web checks and Go fast tier, then commits the merge. It never pushes.
It handles legacy bundle conflicts by keeping the bundles untracked. For source
conflicts, resolve and stage them, then run `tools/update-branch.py --continue`.
Failed checks leave the merge pending for the same continuation command.

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
| Browser structure suite (every page at 1440, 390 and 320 px; under 90 s) | `npm --prefix web run test:browser` (`PHAROS_BROWSER=webkit` for WebKit; `-- --test-name-pattern=Findings` for one page) |
| Library drive end to end (macOS, slow) | `npm --prefix web run test:e2e` (real build, disk image and service) |
| JS checks before a PR | `npm --prefix web run check` (typecheck, schemas, recipe snapshot, bundle, unit tests) |
| Python tools | `python3 -m unittest discover -s tools` |
| Frontend bundle | `npm --prefix web run build` after any `web/src` or `pricing/*.json` change; outputs are gitignored |
| Recipe snapshot | `npm --prefix web run generate-recipes` after changing card recipes; it rewrites `implemented_recipes` in `docs/homepage-query-cards.json`, which Go's `TestHomeQueryRecipesV2` executes |

- **JS unit tests** live in `web/tests/unit/**` and import `web/src` and
  schema JSON directly. `web/tests/run.mjs` bundles them once with esbuild
  into a gitignored `web/.test-build/` and runs them with `node --test`;
  invoke tests through it, since the source files cannot be run by
  `node --test` directly. Cross-language fixtures that Go also reads live in
  `testdata/`. Tests of the page's classic scripts (`reader-model.js`,
  `carbon.js`, `sync.js`, `library.js`) load them with
  `helpers/classic-script.mjs`, so keep their logic in functions those
  scripts expose rather than in DOM code.

- **Browser tests** live in `web/tests/browser/` and use the declared
  `playwright-core` with the installed Google Chrome (or `CHROME_PATH`);
  `PHAROS_BROWSER=webkit` uses Playwright's WebKit, installed once with
  `npx playwright-core install webkit` in `web/`. Without a browser they
  skip and say what to install. `structure.test.mjs` loads every page
  against stubbed data (`fixtures.mjs`) and checks only what a browser can:
  no errors or unstubbed API calls, no horizontal overflow, the header stays
  put, and cards hold their content. A new page is one line in its `ROUTES`;
  a new endpoint needs a default stub in `fixtures.mjs`. `focus.test.mjs`
  checks dialogs' focus. Known layout bugs are listed in `KNOWN` and run as
  todo. `PHAROS_UI_SCREENSHOTS=dir` saves screenshots; nothing asserts on them.
- **Manual only:** on macOS with Xcode Command Line Tools,
  `macos/test/run-light.sh` checks `MenuBar.swift` and `UpdateChecker.swift`
  (AppKit needs a GUI session; no images or service).
  `macos/test/swift-harness.sh [PORT]` checks `LibraryVolume.swift`,
  `Launch.swift` and eject flows (Go, npm, Python 3, rsync; builds in a
  temporary copy, mounts a 300 MB image; default port 18768).
  `macos/test/eject-release.sh [PORT]` checks service shutdown and drive
  recovery (Go, Python 3, curl, sqlite3; mounts a 2 GB sparse image; default
  port 18767). See [Manual macOS tests](docs/configuration.md#running-from-an-external-drive)
  for details; `macos/test/menu-bar.sh` runs just the menu checks.
  Also manual: `tools/*bench*`. Run these only when you change the code they
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
- **Go tests run in several processes, one test at a time in each.**
  `TestMain` splits the tests `-run` selects across up to eight processes
  (`processes_test.go`), so:
  - a test may change package variables, `time.Local` or the environment,
    provided it restores them when it ends (`override`, `t.Setenv`);
  - a test must not depend on another having run, in its process or any
    other, and must keep its files under `t.TempDir()`;
  - do not call `t.Parallel()`. On macOS every SQLite allocation in a
    process takes one lock in `modernc.org/libc`, so catalog tests in
    parallel in one process run no faster than one at a time.

  `PHAROS_TEST_PROCESSES=1` runs everything in one process, for example
  under a debugger. Profiles, coverage, benchmarks, fewer than 16 tests and
  `-run` patterns with a `/` already run in one process.
- **Keep data-safety tests**, even when slow. These include adopt, backup,
  capture, durability, and cross-host merges. Make their fixtures cheaper
  instead.
