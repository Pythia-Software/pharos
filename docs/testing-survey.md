# Test suite survey and plan

Survey date: **2026-10-19**. Code reviewed at `bb496be`.
This is a survey and a proposal; nothing in it has been implemented. Timings
were measured on the shared Mac Studio while other agents were building and
testing (load average 25–63 on 28 cores), so absolute seconds are inflated.
Comparisons that ran side by side, and Pharos's own record of agent test
runs, are the reliable numbers. Per-test working notes from the five reviews
behind this survey are in [`testing-survey/`](testing-survey/).

Status: the browser plan (§4.4 and §5, decision 1) is in place. The 23
`tests/*-ui.test.cjs` files this survey cites are deleted; the browser tier is
the structural suite in `web/tests/browser/`, and the library drive test is
the opt-in `web/tests/e2e/library-drive.test.mjs`. See `AGENTS.md`.

## Recommendation

Most of the time goes to **harness cost**, not to the number of tests.
Low-value tests are a real problem in the browser suite, and they are the
reason the browser suite can never pass. In Go, deleting every low-value
test would save about 15 seconds.

Fix the harnesses first, then move logic down a layer and delete what that
makes redundant. In order:

1. **Make "green" possible and the commands obvious.**
   - 16 browser and JS tests fail or hang on master today.
   - `go test ./...` cannot finish inside Go's default 10-minute timeout.
   - The documented test commands point at a Python suite that does not exist.

   Fix or delete the broken tests, add per-test timeouts, and write one
   AGENTS.md that names the test tiers.
2. **Go: give each test a pre-initialized catalog** instead of running the
   full schema initialization for every test. I prototyped this. The same
   723 tests went from **1,520 s to 452 s** (summed test time), and the
   median test from 1.0 s to 0.09 s, with no assertions changed.
3. **Browser: one Chrome per run and one shared harness.** Today each test
   launches its own Chrome (about 100 launches per run). Each file also
   rebuilds its own fake server.
4. **Move logic out of the browser.** About 39 of the 213 browser cases test
   pure functions: transcript parsing, carbon arithmetic, formatting and
   carousel geometry. Node can test these in milliseconds.
5. **Delete duplicates and change-detectors.** This covers:
   - about 55 browser cases, mostly copies of node tests added in the same PR;
   - the 1,667-line `*-review` test family built around 10 modules nothing ships;
   - upstream query-table tests that never run.
6. **Unify the JS layout.** One `npm test` for the fast tier, one browser
   command, and one esbuild step.

Expected end state, estimated:

| Tier | Contents | Target |
|---|---|---|
| fast | node unit tests plus `go test -short` | about 1 minute |
| full | everything else | a few minutes |
| opt-in | the drive end-to-end test, macOS harnesses and performance probes | only when needed |

Today agents either run everything or guess. A full slate cannot pass, and
`home-ui` alone ran for more than 15 minutes in today's measurement.

---

## 1. What we have

| Suite | Files | Lines | Cases | Runner |
|---|---|---|---|---|
| Go `internal/archive` | 117 | 34.4k | 726 `Test*` | `go test ./...` (one package, strictly serial) |
| Go `internal/querytable` | 3 | 0.4k | 16 | `go test ./...` |
| Go `third_party/query-table-go` | 19 | 3.7k | 124 | **never run.** It is a nested module, and the files are byte-identical to upstream v0.8.0. |
| Browser, `tests/*-ui.test.cjs` | 23 | 9.8k | 213 | `node --test` + Playwright from an undeclared per-workspace install |
| JS unit, `tests/*.test.cjs` | 8 | 0.6k | 49 | `node --test` from any cwd |
| JS unit, `web/tests/*.mjs` | 27 | 4.2k | 318 | `node --test` **only from `web/`**; there is no `npm test` |
| Python, `tools/test_*.py` | 3 | 0.3k | 14 | `python3 -m unittest discover -s tools` |
| macOS harnesses, `macos/test/*` | 7 | 1.1k | — | manual scripts |

**Growth.** Over three weeks, Go source grew 1.75×. The tests grew faster:

| | 2026-09-28 | 2026-10-19 |
|---|---|---|
| Go test lines | 17.7k | 34.9k (2×) |
| Browser test files / lines | 9 / 2.5k | 23 / 9.8k (4×) |
| JS unit test lines | 0 | 4.8k |

The week of October 12–18 alone added 8.9k JS test lines and took JS `test(` sites
from 196 to 558. The biggest single contributors were:

| PR | Test lines | What it added |
|---|---|---|
| #128 | +2,773 | 12 `*-review` test files |
| #119 | +1,986 | node tests, plus browser tests of the same logic |
| #112 | +1,314 | `delegation-ui` |

## 2. What it costs

### 2.1 Agent time

These figures come from Pharos's own catalog: `tool_calls` for the `pharos`
repository, 2026-10-01 to 10-19.

**Totals**
- 3,406 `go test` / `node --test` invocations across 273 pieces of work.
- **37 agent-hours** of test wall clock: 22 h Go and 17 h node.
- Each piece of work that ran tests averaged 12.6 runs and 8 minutes.
- The worst pieces of work spent 2–4 hours running tests.
- About 31 invocations ran into the 10-minute tool cap.

**Average duration per call**

| Period | Claude `go test` | Codex `go test` | Claude `node --test` |
|---|---|---|---|
| Late September | 22 s | 9 s | 44 s |
| Early October | 45 s | 28 s | 92 s |
| October 16–19 | 79 s | 116 s | 93 s |

**Where the October hours went, by command shape**

| Command shape | Hours | Notes |
|---|---|---|
| targeted `go test -run …` | 10.7 | 32 s average, although the compile after an edit is only about 6–7 s |
| named `*-ui` browser files | 9.4 | |
| full `go test ./...` | 6.6 | 74 s average, including runs answered from Go's test cache |

### 2.2 Measured today

**Go.**
- `go test ./...` **fails**. The archive package hit the default 10-minute
  timeout after 610 of 726 tests, using 174 s of CPU (user+sys) in 607 s of
  wall time.
- Given a 40-minute timeout, the package took **1,522 s of wall time for
  250 s of CPU**. The suite mostly waits, and it runs strictly serially: no
  test calls `t.Parallel`.
- One existing test failed on a flake: `TestMainIntegrationNegativeLookupCache`
  asserts an exact count of git subprocess calls.

**Cost of one catalog.** A benchmark of `testCatalog` measured **0.27–0.76 s**
per catalog, depending on load. About 66% of its CPU time is syscalls: writes
and fsyncs from `Initialize` committing about 225 schema statements, roughly 60
migration checks and 138 trigger re-creations one at a time under
`synchronous=FULL` + `checkpoint_fullfsync`.

Reopening a copy of an already-initialized catalog costs about 0.2 s, and
nearly all of that is `Initialize` re-running. About 500 tests create at least
one catalog: 313 `testCatalog` call sites, plus fixture wrappers and table
subtests.

**Prototype.** The patch is in [`testing-survey/template-catalog-prototype.patch`](testing-survey/template-catalog-prototype.patch). It does two things:

- **Template catalog:** it initializes one host-neutral catalog per process, and each test copies the file and opens it without `Initialize`.
- **Test-only `synchronous=OFF`:** tests already turn `fullfsync` off.

It ran the whole package in **453 s, started side by side with the baseline**:

| | Baseline | Prototype |
|---|---|---|
| Summed test time | 1,520 s | 452 s |
| Median per test | 1.0 s | 0.09 s |

| Test | Baseline | Prototype |
|---|---|---|
| `TestIncrementalWriterSearchParity` | 21.9 s | 0.6 s |
| `TestUsageAttributionHealth` | 17.0 s | 0.03 s |
| `TestHeavyOutputAndOutlierDetectors` | 16.9 s | 1.1 s |

The first version broke two host tests, because the template recorded the
host it was built under. Building the template under a fallback host and
running the cheap `migrateHosts` step on each open fixed both. Those two
tests now pass.

### 2.3 Measured per file (JS)

Each file was run alone with `node --test`. Exit codes and pass/fail counts
are from those runs.

| File | Cases | Wall time | Result |
|---|---|---|---|
| `home-ui` | 45 | **>900 s**, killed by my cap | The first 13 tests passed in 95 s, then `:549` hung. In agent runs recorded by Pharos this file took 95–304 s. |
| `library-drive-ui` | 9 | 202 s | 3 fail |
| `library-query-ui` | 4 | 79 s | 3 fail, mostly 30 s timeouts |
| `navigator-ui` | 2 | 66 s | 2 fail, both timeouts |
| `incremental-sync-ui` | 6 | 65 s | 2 fail |
| `transcript-ui` | 70 | 58 s | pass |
| `findings-ui` | 4 | 43 s | 2 fail |
| `usage-table-colors-ui` | 2 | 40 s | 1 fail |
| `search-editor-ui` | 8 | 37 s | pass |
| `machine-activity-ui` | 6 | 36 s | pass |
| `library-link-ui` | 6 | 29 s | pass |
| `carbon-ui` | 3 | 28 s | pass |
| `delegation-ui` | 23 | 24 s | pass |
| `combo-button-ui` | 6 | 23 s | pass |
| `feedback-ui` | 11 | 13 s | pass |
| 8 other browser files | 1 each | 3–9 s each, 48 s total | pass |
| 8 unit files in `tests/` | 49 | 5.5 s total | 1 fail (`chart-colors`) |
| 27 files in `web/tests`, run from `web/` | 318 | 35 s one at a time; **53 s all at once** | pass. `card-parity` alone took 25.6 s; every other file was under 1 s. All 27 fail when run from the repo root. |

Run one at a time, the browser files total **about 1,700 s**, or about 790 s
without `home-ui`. Files with broken tests spend most of their time waiting on
30 s locator timeouts. Running all `web/tests` files at once was slower than
running them one by one: 27 processes each bundle their own copy of
query-table-core.

### 2.4 Broken or hanging on master

These cases fail in today's runs (and in the 10-18 Query Table baseline run
recorded in Pharos), so a full browser run can never come back green:

| File | Failing | Cause |
|---|---|---|
| `home-ui` | `:549` hangs; `:1027` fails | `:549` advances a fake clock 60 s at a time with no per-test timeout, so it ran for more than 13 minutes. `:1027` has a stale assertion that no link contains `-review`, and nothing after line 1031 runs. |
| `library-query-ui` | 3 of 4 | `:224` tests a "why this matched" UI that no longer exists. `:165` waits for a chip label removed in Query Table 0.8.0. `:50` asserts a 0 px layout gap. |
| `library-drive-ui` | 3 of 9 | This is a real end-to-end test: `go build`, an `hdiutil` image, a real service and 600 sessions (202 s). |
| `navigator-ui` | 2 of 2 | The search box label and visible toggle no longer exist. |
| `incremental-sync-ui` | 2 of 6, and `:12` is flaky | `:12` asserts an exact `/api/index` read count. |
| `findings-ui` | 2 of 4 | The nav now starts with a Home tab, and the asserted copy is gone. |
| `usage-table-colors-ui` | `:13` is flaky | A 30 s timeout on the cell menu. |
| `chart-colors` | 1 of 9 | The static pricing catalog does not cover the priced models. |

Most failing locators also burn Playwright's 30 s default timeout, because
only 2 of 23 browser files set a default. Go has one flaky test
(`TestMainIntegrationNegativeLookupCache`, above).

---

## 3. Why it ballooned

**1. Go pays a fixed floor of setup for every test.** The time is not in the
tests themselves:

| Change | Saves |
|---|---|
| Delete every duplicate, superseded and change-detector Go test | about 15 s |
| Fix catalog creation | measured: about 18 min of summed test time |

The remaining Go costs are:

- **Fixtures inserted one row per autocommitted transaction.** Examples:
  - 8,400 inserts in `TestHeavyOutputAndOutlierDetectors`;
  - 10,031 in `TestQueryV3LargeSourceHavingMetricsAndPaging`.
- **Fixtures sized to get past hard-coded product limits**, for example:
  - the 5,000-result search cap;
  - `LIMIT 25000`;
  - the 10,000-snapshot quota;
  - the 16 MB audit budget;
  - the 2,000-message Conductor gate.
- **Leaked background work.** There are 125 `NewServer` calls but only 8
  `stop` calls, so one test's rebuilds and `diskutil` refreshes run into
  later tests.
- **Real host probes.** Tests shell out to `ioreg`, `scutil`, `lsof` and
  `diskutil`.

**2. Browser tests pay for a browser to test logic, then test it twice.**
Every test launches Chrome: about 100 launches per run. Each one loads the
346 KB `ui.py` page and the 1.7 MB bundle.

Classification of the 213 browser cases:

| Class | Cases | Share |
|---|---|---|
| Behavior | 92 | 43% |
| Logic in the browser | 39 | 18% |
| Duplicate | 37 | 17% |
| Change-detector | 25 | 12% |
| Integration-essential | 17 | 8% |
| Huge fixture | 3 | 1% |

About a third of the "behavior" tests also contain change-detector
assertions: exact px or colour values, copy text, and "the removed control is
still absent". Those assertions are why they break.

Most duplicates were added in the same PR as the node test for the same
logic. For example, `home-ui:1081` clicks Collect on all 80 cards, which
`card-parity` already checks for 132 recipes × 4 windows in node.

There are 68 screenshots, and nothing ever compares them; they are leftovers
from design review. Fixed sleeps appear in 9 files. The pixel and CSS
assertions run in Chrome, but the app ships in WKWebView.

**3. Review rounds accreted as new tests instead of edits.**

- PR #128 added 12 `*-review` files (1,667 lines). 10 of the modules they
  import (`web/src/*-review-model.ts`, 210 lines) have **no production
  importer**. Each file pins exact feedback strings for that dead code. About
  32 more tests exercise only *retired* recipes.
- Go files named `*_review_*` and `*_regression_*` are ordinary behaviour
  tests that were left in review-named files.
- Five Go `TestVendoredV3*` tests are line-for-line copies of upstream's
  sqlite-tests.

**4. No tiers, no canonical commands, no achievable "green".**

- `node --test tests/*.test.cjs` pulls in all 23 browser files along with the
  8 fast ones.
- `web/tests` only runs from inside `web/`.
- `go test ./...` times out.
- `docs/technical-overview.md` tells agents to run
  `PYTHONPATH=src python3 -m unittest …` and `compileall src`. `src/` has
  never existed in this repo.
- There is no AGENTS.md or CLAUDE.md.
- Known-failing tests make red the normal state, so agents rerun suites to
  tell new failures from old ones.

**5. Harness fragmentation.**

- In the browser files:
  - 22 copies of the Chrome path;
  - 20 copies of the `ui.py` extraction;
  - 4–5 separate fake query servers, some on the legacy v2 transport that
    production no longer uses;
  - 6 screenshot environment-variable names.
- In Go:
  - 8 HTTP-call helpers;
  - 11+ inline "ingest in a transaction" copies;
  - six ways to write a Conductor database.
- Across the JS unit files, about 47 esbuild calls, each inlining its own copy
  of query-table-core.

---

## 4. Plan

### 4.1 Rules the plan follows

- **A test earns a browser only for wiring, focus/keyboard/a11y, real layout
  invariants, or races.** Values, formatting, parsing, aggregation and state
  machines get node tests.
- **Assert invariants, not incidental details.** Examples: "no horizontal
  overflow at 390 px", "a background caret implies `appearance:none`". Not
  `borderRadius === '7px'`, exact copy, or "the removed button is absent".
- **Every deletion names what still covers the behaviour,** or states that
  nothing should (dead UI, dead code).
- **Data-safety tests stay**, even when slow: adopt, backup, capture,
  capture-index rollback guards, cross-host merges, WAL durability, and the
  incremental-vs-rebuild parity oracles. They get cheaper fixtures, not
  deletion.
- **Three tiers:**

  | Tier | Contents |
  |---|---|
  | fast | node unit tests plus `go test -short`; what an agent runs on every change |
  | full | adds the browser harness and slow Go tests; run before a PR |
  | opt-in | end-to-end drive, macOS harnesses, perf probes, the upstream fork suite |

### 4.2 Phase 1: ground truth (one small PR, low risk)

| Change | Why it's OK |
|---|---|
| Delete the browser tests of UI that no longer exists: `library-query-ui:224` (why-chips), `navigator-ui:13` (search box), and `library-query-ui:165` (rewrite as a node test of schema enum labels). | The UI they target is gone from `web/src`, the assets and `ui.py`. Server-side explain is covered by `search_explain_test.go`. |
| Fix the stale assertions: `home-ui:1031`, `findings-ui:100` and `:250`, and `incremental-sync-ui:117` and `:238`. Drop the exact read count in `incremental-sync-ui:12`. Drop the 0 px gap in `library-query-ui:50`. | These assertions are wrong now. The behaviour beneath them, such as card-library filter, sort and templates, currently has **no running coverage** because the test dies first. |
| Add `--test-timeout` (for example 60 s) to node runs and `page.setDefaultTimeout(5000)` to every browser file. | All fixtures are local. A 30 s locator timeout or an unbounded hang only turns a failure into a 10-minute stall. |
| Fix or replace the `home-ui:549` carousel fast-forward hang. Use bounded `runFor` steps, or test the autoplay timer as a pure function. | It is the hang I hit today. |
| Write `AGENTS.md`, symlinked as `CLAUDE.md`, with the tiers and the exact commands. Delete the stale Python/`src` lines from `technical-overview.md` and point the other docs at it. | Agents follow docs. The current ones send them to a suite that does not exist and give them no fast tier. |
| Quarantine anything still flaky behind an explicit `todo` with an issue reference, not a silent skip. | After this, red means a regression. |

### 4.3 Phase 2: Go harness (biggest measured win, no coverage change)

| Change | Why it's OK |
|---|---|
| **Template catalog.** `testCatalog` copies a once-per-process, host-neutral initialized catalog and opens it without `Initialize`, then runs `migrateHosts`. Add `freshCatalog(t)` for the handful of tests whose subject is initialization or migration (fallback host, pre-host migration, FTS rowid migration, effort index, authorship columns, adopt). | Production still runs `Initialize` on every open. This only stops tests re-running it. `OpenCatalogForQuery` already skips `Initialize` when this build initialized the catalog. **Prototype: 1,520 s → 452 s summed, every test passes.** |
| **Test-mode durability.** Tests use `synchronous(OFF)` and `checkpoint_fullfsync(0)`. `durability_test.go` restores production pragmas for its own tests, as it already does for `commitFullFsync`. | Production durability is unchanged. The tests that verify it still run with production pragmas. |
| **Batch fixture inserts in one transaction.** This covers `findingFixture`, `addAuditMessage`, `seedToolCalls` (prepare once), and the Conductor fixtures. | The data is identical; only the commit count changes. |
| **Turn hard-coded limits into package variables** that tests can lower: search cap 5,000, `LIMIT 25000`, snapshot quota 10,000, `automaticAuditBytes`, the Conductor audit gate and `checkpointWaitMS`. This follows the existing `backupStepPages` / `adoptCopyChunk` seams. | The same property is tested with about 10 rows instead of 10,000. The production values are unchanged. |
| **Hermetic `TestMain`.** Set `PHAROS_HOST_ID`; stub `driveTool`; set `GIT_CONFIG_GLOBAL=/dev/null`. Add a `testServer(t, …)` helper that registers `stop` in `t.Cleanup`. | Removes `ioreg`/`scutil`/`diskutil` and leaked background work, a plausible cause of 9–12 s outliers. Tests of the real tools opt in. |
| **Move probes and tools out of the test binary** (`//go:build probe`). This covers performance probes, `usage_performance`, `mcp_latency`, `analysis_replica`, `authorship_eval`/`dump`, `capture_bench`, `text_search_preview` (2-minute sleep), and `usage_attribution_live`. | They already skip by default. One blocks forever in `ListenAndServe`. They are investigation tools, not tests. |
| Consolidate helpers into `harness_test.go`: one `apiCall`, `mcpCall`, `ingest`, `execTx`, `waitFor`, source builders, and `seededCatalog(t, name)` snapshot templates for the expensive fixtures. | No behaviour change. It replaces about 25 near-duplicate helpers and makes the cheap path the default. |
| Mark the slowest integration-essential tests with `testing.Short()` skips, and document `go test -short -vet=off -run X ./internal/archive` as the inner loop. | They still run in the full tier. This gives agents a fast tier. |

### 4.4 Phase 3: one browser harness

A `tests/browser/harness.cjs` (API sketches in the browser appendices):

- **One Chrome per run.** A small runner calls `chromium.launchServer` and the
  tests connect over WebSocket. Each test gets a new isolated context.
  `node --test-concurrency` is capped at 3–4.
- **Shared, cached pieces:** `ui.py` HTML extracted once; assets cached in
  memory; one fake query server shared with the node tests. The fake server
  defaults to the production v3 transport.
- **Default stubs** for every shell endpoint, so no test can have an
  "incomplete stub" again.
- **Checks run in `t.after`:** page errors, and unexpected API calls.
- **Waiting:** a `settled()` helper replaces fixed sleeps, and the 5 s
  default timeout applies everywhere.
- **Screenshots** only when `PHAROS_UI_SCREENSHOTS=dir` is set.
- **`library-drive-ui` moves to `tests/e2e/`**, behind `PHAROS_E2E=1`. Its
  stub-able panels move to harness pages.
- **Playwright becomes a declared `playwright-core` devDependency.** Today
  every new workspace has to hand-install it.

Why it's OK: no assertion changes. It removes about 100 Chrome launches and
the oversubscription that turns a 4.6 s test into a 13.3 s one.
`feedback-ui` already uses one browser per file and is the model.

### 4.5 Phase 4: move logic down to node (keep coverage, drop the browser)

| From | To | Why it's OK |
|---|---|---|
| 21 `transcript-ui` logic tests: token reports, context windows, result pairing, noise trimming, edit deltas, todo summaries, `commandKind` and root prefixes. Includes the two shared-fixture parity tests with Go. | Extract the DOM-free reader model from `ui.py` (about lines 715–937 and 1161–1205) into `assets/reader-model.js`, loaded by the page and by node via `vm`. | Every one of these already runs inside `page.evaluate` or calls only DOM-free functions. The node version asserts the same values. `share.go` inlines the new file, and `share_test` already checks there are no `/assets/` references. |
| `carbon-ui`'s ~20 exact CO₂/Wh/OE figures | A node `vm` test of `window.pharosCarbon.{estimate,energy,mass}` | The JS math has no other coverage; Go tests a separate implementation. One browser test keeps controls, persistence and focus. |
| `incremental-sync` refresh/announce logic and the run-history formatters (`sync.js`); `combo-button` activity and estimate wording (`library.js`) | Extract pure functions and test them as node tables. | Removes polling loops and the racy request counts. One browser check that the panels render remains. |
| `home-ui` belt and carousel geometry (`:573`, `:644`); `usage-presets` `presetMatches`; delegation formatters; navigator PR-link renderers; four home-ui tests that never touch the browser | Pure functions in `web/src`, tested in `web/tests` | Keeps the a11y and focus invariants in browser tests. Drops mid-animation pixel checks that are timing assertions. |

### 4.6 Phase 5: delete duplicates and change-detectors

Each item names what still covers it. Per-test lists are in the appendices.

**Browser, about 55 cases deleted or merged.**

- **home-ui.** Delete `:514`, `:827`, `:1081`, `:1111`, `:1129`, `:1165`, `:1280`, `:1470`, `:1500`, `:1597`, `:1669` and `:1693`. Each is covered by `card-parity`, `card-recipes`, `query-previews`, `card-loading` or a kept browser test. Fold nine layout checks into one **layout sweep** at three widths.
- **Whole files and single tests:**
  - delete both `usage-table-colors-ui` tests (covered by `usage-chart-colors`);
  - delete `combo-button-ui:99` (styles only);
  - delete `library-query-ui:12` (a subset of `feedback-ui:156`);
  - delete `feedback-ui:323` (screenshots only);
  - delete `machine-activity-ui:390` (covered by 3 node tests).
- **transcript-ui.** Delete 4 pixel and DOM change-detectors and 3 copies of feedback-ui tests.
- **library-link-ui.** Keep 2 of its 6 matrix cases.
- **usage-ui.** Cut the 450-line monolith to a wiring test of about 60 lines. The rest re-checks `chart-*`.
- **Kept tests:** strip exact px, colour, copy and "absent control" assertions.

**JS unit.**

- Replace the repeated compile/coverage parts of the 12 `*-review` files with one table-driven `recipe-catalog.test.mjs` over all 132 recipes. Fold each file's live recipe semantics into about 10 per-category files.
- Delete `steering-review`: every steering recipe is retired.
- Delete the review-N, feedback-export and retired-only tests.
- Delete the 10 dead `*-review-model.ts` modules.
- Merge the two `usage-aggregations` files.
- Replace the hand-synced 636 KB `docs/homepage-query-cards.json` deep-equal with a generator plus `--check`, like `generate-schemas`.

**Go, about 30 tests deleted, merged or rewritten.**

- **Delete the dead path and its tests.** `activityDuration` has no product caller since #127. Its 3 tests go with it.
- **Delete duplicates:**
  - `TestCatalogPoolEnforcesForeignKeys`, a subset of the durability pragma test;
  - `TestEmbeddedUIIsServedWithoutPython`;
  - `TestReopenedCatalogHasNoLegacyQueryStats`;
  - `TestQueryPopulationDownloadsDisabled`;
  - `TestDumpAuthorship`.
- **Rewrite EXPLAIN tests that check SQL written inside the test.** Point them at the product's queries, or delete them.
- **Replace the upstream copies with a gate.** Remove the 19 `third_party` test files and the 5 `TestVendoredV3*` and 2 V2 upstream copies. In their place, add `tools/test-query-table-fork.sh`, which runs upstream's `backends/sqlite-tests` at v0.8.0 against the patched fork. That is the real guard for the local patches; today nothing runs them. Delete the `TestVendoredV3*` copies only after the gate exists, because they are the only in-repo tests of the patched grouped-summary path.
- **Merge into tables:**
  - the three per-dataset HTTP smokes;
  - the hourly-usage tests across 4 engines;
  - repository-merge planner scenarios;
  - the two real-git capture tests;
  - the two force-recapture tests;
  - the 14 MCP freshness rows on one catalog;
  - the `sql_table` ↔ `internal/querytable` twins, run from one shared case table.
- **Dissolve the `*_review_*` / `*_regression_*` Go files** into their subsystem files.

### 4.7 Phase 6: one JS layout and runner

```
web/tests/unit/**      node only; one esbuild step (splitting, packages external)
web/tests/browser/**   the *-ui files on the shared harness
web/tests/e2e/**       library-drive (PHAROS_E2E=1)
testdata/              cross-language fixtures (token-usage, transcript-find, recipe snapshot)
```

- `npm --prefix web test` runs the unit tier.
- `test:browser` runs the browser tier; `test:all` runs both.
- `check` runs typecheck, schemas, recipes, bundle and unit.
- The 8 `tests/*.cjs` unit files move under `unit/`.
- `card-parity` takes 25.6 s, 72% of the unit tier. It compares about 1,056
  catalog and saved previews: 132 recipes × 4 windows × populated/empty. The
  fast tier should run one window; `test:full` and the full tier keep the
  whole matrix, so the matrix still runs before every PR.

Why it's OK: the moves are mechanical. One build replaces about 47 per-file
builds, each of which inlines its own query-table-core. It also gives
agents one command that cannot accidentally start 23 browser files.

### 4.8 Deferred: splitting `internal/archive`

Splitting the package would let `go test` run the pieces in parallel, which
in-package `t.Parallel` cannot do safely while about 30 files mutate package
globals. The obvious first leaf is the pure parsers and record types, about
16k lines that never touch the catalog. About 175 tests finished in 10 ms or
less in the prototype run, which suggests they need no catalog.

But a split means moving methods off `*Catalog` (80 files define them) and
breaking `Initialize`'s calls into findings and TL1. Once Phase 2 lands, the
median test is under 0.1 s and the compile after an edit is about 7 s, so I
would measure again before taking this on.

### 4.9 Guardrails against regrowth

These go in AGENTS.md:

- Put logic tests in node or Go unit tests. A browser test must say which
  wiring, a11y or layout invariant it guards.
- Write new browser tests on the harness, with no fixed sleeps, no
  unconditional screenshots, and no exact CSS, pixel or copy asserts.
- Fix a review finding by editing or extending the existing test. Do not
  add a `*-review` or `*_regression` file.
- The runners print the 10 slowest tests. A file that grows past a budget
  (for example 20 s) is the cue to look at it.

---

## 5. Decisions (2026-10-19)

1. **Browser tests check only what a browser can.** The UI changes often and
   gets a lot of UX feedback, so browser tests are limited to structural
   checks that nothing else can make. Examples:
   - no horizontal overflow at 390 px;
   - a page renders without errors;
   - focus moves into a dialog and back out.

   Chrome versus WebKit does not matter. The harness runs Chromium by
   default and can run WebKit.

   This goes further than §4.6. Interaction flows such as collection editing,
   card editors and route restoration are deleted from the browser suite.
   Their logic gets node tests when it can be extracted. The browser suite
   becomes a structural sweep over every page.
2. **Remove the legacy `/aggregations` query path.** Metrics cover its uses.
   The UI callers move to metrics or Query Table v3, the server routes go,
   and the parity tests that existed for the legacy path go with them.
   Done in PR 7.
3. **Land the work as a stack of GitHub PRs**, each one passing:

   | # | PR | Contents |
   |---|---|---|
   | 1 | Survey and test guidance | This survey, `AGENTS.md`, the stale-doc fix, and the four model colours whose absence failed `chart-colors` |
   | 2 | Go test harness | Template catalog, test-mode durability, hermetic `TestMain`, batched fixtures, adjustable limits |
   | 3 | Go test cleanup | Duplicates, dead code, probes behind a build tag, the upstream fork gate, merges |
   | 4 | JS test layout | One layout and runner, one build, consolidated recipe tests, dead review modules removed |
   | 5 | Page logic in node | Reader model out of `ui.py`; carbon, sync, library and delegation logic tested in node |
   | 6 | Structural browser suite | Shared harness and one structural sweep; everything else deleted; drive E2E opt-in |
   | 7 | Remove legacy aggregations | Done: UI callers on `/metrics`, with host fast paths for Machine Time and plain counts and sums; routes, engines and legacy parity tests removed; the route answers 410 |

## 6. Method and caveats

- **Inventory and classification.** Five parallel reviews read every test:
  - two covering the browser files;
  - two covering the Go files;
  - one covering the fast suites, layout and docs.

  Each classified every case as behavior, logic in the browser, duplicate,
  change-detector, perf, integration-essential or superseded, and cited the
  test that preserves the coverage.
- **Claims checked directly:**
  - the dead review modules (`grep` for importers);
  - the byte-identical `third_party` tests (`cmp` against the module cache);
  - `activityDuration` having no caller;
  - the failing and hanging browser tests, by running them.
- **Timings.** Go tests were run with `-json` and timed per test. The A/B
  compared two test binaries started at the same moment. JS files were each
  run alone. Telemetry is from the Pharos catalog, read-only.
- **Load.** The machine was shared throughout, with load average 25–63 on
  28 cores. The baseline Go run continued alone for 17 minutes after the
  prototype finished, so its total is somewhat inflated relative to the
  prototype. The per-test medians (1.0 s versus 0.09 s) are less sensitive
  to this.
- **Browser projections are estimates.** No browser change was prototyped.
