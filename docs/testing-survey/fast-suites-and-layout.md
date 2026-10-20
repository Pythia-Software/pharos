> Working notes behind [the test suite survey](../testing-survey.md), written by one of the five 2026-10-19 reviews at `bb496be`. Line numbers refer to that commit. Classifications are judgement calls; the timings here are indicative, taken from contended runs or the Pharos archive. See the survey for measured numbers.

# Fast JS suites, test layout/runner, misc harnesses: survey

Scope: `web/tests/*.mjs` (27 files), the 8 non-browser `tests/*.cjs`, `tools/` prototypes/tests, `macos/test/*`, test docs, and growth.
Method: I read the files, grep, and git history. I ran no test suites. The only things I ran were two single esbuild bundles to time the loader cost (§3.3) and one `curiosity-data` import to count recipes.

---

## 0. Headline findings

1. **No single command runs the JS tests.** `web/package.json` has 5 one-file `test:*` scripts and no `test` script. `web/tests/*.mjs` only work with **cwd = `web/`**: esbuild entry points `'src/…'` and `readFileSync('../schemas/…')` are cwd-relative. So `node --test` from the repo root discovers all 59 JS test files, and every web test then fails to build. `(cd web && node --test)` is the only working "all web unit tests" command, and it appears only once, in `docs/query-table-local-development.md`. The 8 fast `tests/*.cjs` unit files sit in the same directory as the 23 Playwright `*-ui` files. As a result, `node --test tests/*.test.cjs` (an invocation agents use, e.g. `timeout 900 node --test tests/*.test.cjs`) always pulls in the whole browser slate.
2. **Ten of the twelve `*-review` source modules are dead code, and the tests keep them alive.** The tests import `api-cost`, `context-caching`, `delegation`, `error-pattern`, `human-word`, `model-choice`, `research`, `skills-instructions`, `token-usage` and `tool-flow-review-model.ts`. No production module imports them, and none of their review keys appear in the shipped `internal/archive/assets/query-tables.js`. Only `coverage-review-model` ships, through `draft-card-review.tsx` at `?cards=coverage-review`. `docs/homepage-query-cards.md` confirms that "the temporary category study pages … have been removed". Every review file still has a "feedback exports …" test that pins exact strings such as `"Token usage cards — review 3"` and `'pharos-api-cost-review-v2'`. Each also has a "covers every active definition" test, which forces new recipes to be registered in the dead theses lists.
3. **The 12 review files share one copy-pasted shape, but they are not 12 copies of the same test.** Each has the same ~10-line preamble: an esbuild multi-entry `build()`, the `mod()` data-URL importer, `schemas`, `caps`, `execution`, and the `card()`, `table()` and `metric()` helpers. Each then follows the same skeleton: (a) "all active category cards compile cover+destination at N days", (b) retired-ID list checks, (c) 3–18 hand-written fixture tests of recipe semantics, and (d) a feedback/export test. Parts (a), (b) and (d) collapse into one table-driven catalog test (or get deleted). Part (c) is real BEHAVIOR testing of live recipe semantics and should be kept, but consolidated. By heuristic count, **about 32 of the 138 review test sites, plus 7 in `card-recipes`, exercise only *retired* recipes.** All of `steering-review` falls in this group, because all 6 steering recipes are retired.
4. **A 636 KB hand-synced snapshot.** `query-previews.test.mjs` #1 asserts `docs/homepage-query-cards.json.implemented_recipes` deep-equals `queryRecipes` (132 recipes, about 297 KB compact). No generator exists, so every recipe edit means hand-regenerating a doc file. Go's `TestHomeQueryRecipesV2` also consumes that file. It should be generated with a `--check` mode, the same way `generate-schemas` works.
5. **Per-file bundling is wasteful, but it is not the main wall-time driver.** A one-entry bundle of `src/scatter-axis.ts` is 1.34 MB and takes about 146 ms, because it inlines query-table-core. `curiosity-data.ts` bundles to 1.98 MB in about 208 ms and costs about 119 ms to import as a `data:` URL. The 7-entry api-cost build takes 206 ms. With `splitting: true, packages: "external"` it takes 38 ms and the output shrinks from 2.05 MB to 327 KB. The JS unit files make about 47 esbuild calls in total, which is roughly 12–18 s of CPU per full unit run. Wall time is only about 1–2 s on 28 cores. The wall-time drivers are the browser slate, which agents run by default, and probably `card-parity`'s combinatorics (unmeasured, see §1).
6. **Stale docs send agents to commands that do nothing or error out.** `docs/technical-overview.md` "Development and verification" lists `PYTHONPATH=src python3 -m unittest discover -s tests -v` and `python3 -m compileall -q src`. **`src/` has never existed in this repo**: it is absent from `e8cd2f2` (the first real commit) and from all later history. `tests/` has no `.py` files, so unittest prints "NO TESTS RAN" and exits 5 on Python 3.14; compileall fails with "Can't list 'src'". The same block also lists `./launch.sh --no-open`, which does a full app build (`npm ci` + Go + Swift) and writes `~/Library/Application Support/Pharos/archive.toml`. That is not a test. There is no CLAUDE.md or AGENTS.md, and the user-level `~/.claude/CLAUDE.md` is empty.
7. **Playwright is undeclared.** All 23 browser tests, plus `tools/usage-bench.cjs`, `createRequire('.context/browser-tests/package.json')('playwright')`. That is a gitignored per-workspace install (`npm install --prefix .context/browser-tests playwright`), and every new Conductor workspace has to repeat it. `docs/query-table-local-development.md` (added in #139, today) says several browser fixtures "currently fail identically" at baseline (Library, MCP, navigator, Findings, Sync, card library, pricing catalog, Usage badge, Human Words). Running the full browser slate therefore can never come back green.
8. **Node is v20.19.1, which reached EOL on 2026-04-30.** It has no `--experimental-strip-types` (that arrived in v22.6, unflagged by default from 23.6/22.18) and no `--test-isolation=none` (22.8+). It does have `--test-concurrency`, `--test-shard`, `--import` and `module.register()`. esbuild is 0.27.2. Strip-types would not help even after an upgrade: `web/src` uses extensionless relative imports (`moduleResolution: Bundler`), `.tsx`, JSON imports without attributes (`../../schemas/*.json`, `../../pricing/model_colors.json`) and CSS side-effect imports. A bundling step or a loader hook is required either way.

---

## 1. Per-file table: `web/tests/*.mjs` (27 files, 4,178 lines)

Loader key:
- **ESM-data**: `await build({entryPoints:[…], bundle, write:false, format:'esm'})`, then each output is imported as a `data:text/javascript;base64,…` URL. Entries are cwd-relative, so the file must run from `web/`.
- **CJS-fn**: `buildSync(… format:'cjs')`, then `new Function('module','exports'[, 'require'], text)`.
- **stdin**: `buildSync`/`build` with `stdin: {contents: "export * from './src/…'", resolveDir: process.cwd()}`.

Every bundle inlines its own copy of query-table-core (about 1.3–2 MB per output). Most files *also* `import * as core from '@pythia-software/query-table-core'`, so each process holds two core instances.

Class key:
- **BEHAVIOR**: real logic against fixtures.
- **CD**: change-detector. It pins copy, IDs, review-round display settings or dates.
- **DUP**: duplicates coverage found elsewhere.
- **PARITY**: cross-path or snapshot equivalence.
- **DEAD**: tests unshipped code.

"Sites" counts `test(` call sites; parametrised loops add more tests.

| file | lines | sites | loader (builds) | class | recommendation |
|---|---:|---:|---|---|---|
| api-cost-review | 86 | 10 | ESM-data, 1 build × 7 entries | BEHAVIOR (cost recipe semantics) + CD (id-set size 7, `pharos-api-cost-review-v2`, heading text) + DEAD (theses/feedback) + 2 retired-only tests. It also hides a **global** copy lint over all recipes ("descriptions … without advertising a display count"). | Fold the semantics into `unit/recipes/cost.test.mjs`. Move the global lint to `recipe-catalog.test.mjs`. Delete the feedback, theses and retired-only tests. |
| card-loading | 53 | 4 | ESM-data, 2 entries | BEHAVIOR | Keep. Consider merging with card-presentation (same modules). |
| card-parity | 159 | 3 (+2 param) | stdin CJS-fn barrel (6 modules) | PARITY, combinatorial: 132 recipes × windows {1,7,30,90} × {populated, empty}, so about 1,056 catalog-vs-saved preview comparisons through an in-memory fake server (96 rows per dataset, core.applyQuery/evaluateMetrics); plus 80 active × cover choices × chart kinds × {edit, no-edit}. | Keep; it is the strongest single regression net and subsumes the "retired remain executable" checks. **Likely the slowest unit file (estimated ~5–20 s CPU; please confirm in the benchmark).** If it is, default to windows `[7]` and one chart kind per cover, and run the full matrix under `PHAROS_TEST_FULL=1` / `npm run test:full`. |
| card-presentation | 165 | 11 | ESM-data, 1 | BEHAVIOR | Keep. |
| card-recipes | 408 | 25 | ESM-data via `moduleOf` × 8 (query-links bundled **twice**; every module is a separate bundle with its own core) | Mostly BEHAVIOR. DUP: "all card destinations compile …" overlaps query-previews #1, card-parity and each review file's compile test. CD: the wishlist test asserts `capacity_audit.date === "2026-10-14"` in the docs JSON. About 7 retired-only tests. | Keep the behavior. Drop the date assertion. Move the catalog-wide compile check into `recipe-catalog.test.mjs`. Prune the retired-only tests. |
| computed-column-store | 55 | 3 (+1) | ESM-data, 1 | BEHAVIOR | Keep. |
| concurrency | 310 | 27 | ESM-data, **3 separate builds** | BEHAVIOR | Keep. Switch to the shared build. |
| context-caching-review | 140 | 9 | ESM-data, 5 entries | as api-cost; 3 retired-only | Fold into `unit/recipes/context.test.mjs`; delete DEAD/CD/retired-only. |
| coverage-review | 152 | 14 | ESM-data, 1 | BEHAVIOR of the **live** draft-card loaders (fetch/window stubs, DST). 2 feedback tests (live, but CD-ish exact heading). | Keep. Rename to `draft-cards.test.mjs`. Loosen the heading assertions. |
| delegation-review | 59 | 8 | ESM-data, 5 | as api-cost; 1 retired-only, 5 mixed | Fold into `recipes/delegation.test.mjs`. |
| error-pattern-review | 138 | 10 | ESM-data, 5 | as api-cost; 4 retired-only | Fold into `recipes/errors.test.mjs`. |
| human-study | 222 | 12 (+loops) | ESM-data, 1 | BEHAVIOR | Keep. |
| human-word-review | 117 | 7 | ESM-data, `moduleOf` × 3 separate bundles | DEAD (human-word-review-model) + CD (copy) + some BEHAVIOR | Fold the semantics into `recipes/human.test.mjs`. Delete the rest. |
| machine-activity | 155 | 12 | ESM-data, 1 | BEHAVIOR | Keep. |
| model-choice-review | 383 | 26 | ESM-data, 11 entries | BEHAVIOR (about 14 active-recipe tests). CD: "review 2/3/5" tests pin `cover.maxGroups=5`, `height=300`, `legendColumns=2`, `showHeadline=false`, retired IDs. DUP or misplaced: model colors (`metricSeriesColor`, which belongs with chart-colors), pie geometry (compact-pie-model), shared box axes (compact-box-model), template height persistence (overlaps card-parity's "default cover can be saved"). | Split. Recipe semantics go to `recipes/models.test.mjs`. Generic presentation tests move next to their modules. Delete the review-N tests and the feedback test. |
| query-host-integration | 85 | 5 | stdin ESM-data barrel (storage, bookmarks, query-links + core) | BEHAVIOR (durable links, quota errors) | Keep. |
| query-previews | 480 | 33 | ESM-data, 1 | BEHAVIOR (batching, cancellation, server failures, Library bookmarks). Test #1 is a **PARITY-SNAPSHOT** against the 636 KB `docs/homepage-query-cards.json`. | Keep the behavior. Replace #1 with `npm run check-recipes` (a generator plus `--check`). |
| research-review | 73 | 8 | ESM-data, 5 | as api-cost; 2 retired-only, 4 mixed | Fold into `recipes/research.test.mjs`. |
| scatter-axis | 61 | 5 | ESM-data, 1 (still 1.34 MB with core inlined) | BEHAVIOR | Keep. |
| search-composer | 75 | 7 (+loop) | ESM-data, 1 | BEHAVIOR | Keep. |
| skills-instructions-review | 92 | 9 | ESM-data, 5 | as api-cost; 2 retired-only, 6 mixed | Fold into `recipes/skills.test.mjs`. |
| steering-review | 42 | 5 | ESM-data, 2 | **All 6 steering recipes are retired**, and all 5 tests check detailed semantics of retired cards. | **Delete.** The table-driven catalog test plus card-parity already prove the saved definitions still resolve and execute. |
| text-search-model | 129 | 11 | ESM-data, 1 | BEHAVIOR | Keep. |
| token-usage-review | 140 | 14 | ESM-data, 6 | as api-cost; 5 retired-only. Also contains a box-whisker geometry test that belongs with compact-box. | Fold into `recipes/usage.test.mjs`; move the box test. |
| tool-flow-review | 245 | 18 | ESM-data, 5 | as api-cost; 6 retired-only | Fold into `recipes/tools.test.mjs`. |
| usage-aggregations (.mjs) | 59 | 4 (+1) | ESM-data, 1 | BEHAVIOR; **DUP subject** with `tests/usage-aggregations.test.cjs` (same `applyUsageAggregations`, different cases) | Merge both into one file. |
| usage-queries | 95 | 8 | ESM-data, 1 | BEHAVIOR | Keep. |

**Review-file totals:** 12 files, 1,667 lines (40% of web/tests), 138 test sites. All were added in one PR, #128 (8cae8ad, 2026-10-16, +2,773 test lines). About 29 sites are "covers/compiles", feedback or review-N tests (DEAD, CD or DUP), and about 32 more exercise only retired recipes. Expect roughly 1,667 → ~700–800 lines after consolidation, with no loss of live-behavior coverage.

**The table-driven replacement for the repeated (a)/(b) parts** is one `recipe-catalog.test.mjs`:

```js
for (const recipe of queryRecipes) for (const days of [1, 7, 30, 90]) test(`${recipe.id} @${days}d`, () => {
  const { query } = instantiateRecipe(recipe, days, clock);
  for (const state of [query, cardCoverQuery(query, recipe).query])
    assert.deepEqual(core.toQueryRequestV3(state, schemas[recipe.dataset], caps, execution, { now, rows: false }).diagnostics, []);
  if (!recipe.retired) { assert.ok(recipe.action); assert.doesNotMatch(recipe.title, /[?{}]/); assert.match(instantiateRecipe(recipe, days).description, new RegExp(String(days))); }
  assert.doesNotMatch(recipe.description, /top \d+|plus Other|…/);  // the global copy lint now in api-cost
});
```

This replaces the compile loops in 11 review files, card-recipes' "all destinations compile" and the compile half of query-previews #1. Retired-ID lists and per-category "exactly N cards" counts should simply go: they are pure change-detectors.

## 2. Non-browser `tests/*.cjs` (8 files, 611 lines)

All use **CJS-fn**: `require('../web/node_modules/esbuild').buildSync({entryPoints:[path.join(__dirname,'../web/src/…')]})` and then `new Function`. They are `__dirname`-relative, so they run from any cwd. `chart-selection` and `authored-categories` also `import()` query-table-core's `dist/index.js` by file URL, which gives a second core instance.

| file | lines | sites | loads | class | recommendation |
|---|---:|---:|---|---|---|
| authored-categories | 39 | 2 | `authored.tsx` (+core by URL) | BEHAVIOR (legend → ALL set filter) | Move to `web/tests/unit/charts/`. |
| chart-colors | 118 | 9 | `chart-colors.ts` + `chart-series.ts` (2 builds); reads `pricing/model_colors.json`, `cost_changes.json` | BEHAVIOR plus catalog-consistency invariants (unique colors, darker newer versions, never recycle). Overlaps `model-choice-review`'s "compound model groups keep catalog colors". | Move; absorb the model-choice color test. |
| chart-period | 105 | 6 (+zones loop) | `chart-period.ts` | BEHAVIOR (DST) | Move. |
| chart-rolling | 68 | 7 | `chart-series.ts` | BEHAVIOR | Move. Optionally merge with chart-colors' `foldSeries` tests into `chart-series.test.mjs`. |
| chart-scale | 63 | 5 | `chart-scale.ts` | BEHAVIOR | Move. |
| chart-selection | 94 | 7 | `chart-selection.ts` (+core) | BEHAVIOR (matches the query engine) | Move. |
| usage-aggregations (.cjs) | 49 | 1 (5 cases) | stdin barrel (usage-aggregations + core) | BEHAVIOR; **DUP subject** with `web/tests/usage-aggregations.test.mjs` | Merge into the web/tests file. |
| usage-chart-colors | 75 | 7 | `usage-chart-colors.tsx` | BEHAVIOR (table-cell colors). This is a *different module* from chart-colors.ts, so not a true duplicate. | Move, next to chart-colors. |

Shared cross-language fixtures also live in `tests/`. Go reads `tests/token-usage-fixtures.json` (`internal/archive/token_usage_test.go`) and `tests/transcript-find-fixtures.json` (`transcript_find_test.go`), and `transcript-ui.test.cjs` reads both. If `tests/` is reorganised, move them to a root-level `testdata/` and update the 3 readers.

## 3. Proposed unified JS layout and runner

### 3.1 Layout

```
testdata/                                # cross-language fixtures (Go + JS)
  token-usage-fixtures.json
  transcript-find-fixtures.json
  query-recipes.json                     # GENERATED snapshot (replaces docs/…json implemented_recipes)
web/
  tests/
    build.mjs                            # the ONE esbuild step for unit tests
    run.mjs                              # tiny runner: unit|browser|all, file filters, passes --test-* flags
    unit/                                # node only, no browser, no network; target < 10 s wall
      helpers/recipes.mjs                # schemas, caps, execution, card()/table()/metric(), fixture builders
      recipe-catalog.test.mjs            # table-driven compile/copy lint over all 132 recipes
      recipes/{cost,context,delegation,errors,human,models,research,skills,tools,usage}.test.mjs
      cards/{card-parity,card-presentation,card-loading,card-recipes,draft-cards}.test.mjs
      charts/{chart-colors,chart-series,chart-period,chart-scale,chart-selection,authored-categories,usage-chart-colors,scatter-axis}.test.mjs
      query/{query-previews,query-host-integration,usage-aggregations,usage-queries,computed-column-store}.test.mjs
      search/{search-composer,text-search-model}.test.mjs
      {concurrency,machine-activity,human-study}.test.mjs
    browser/                             # the 23 *-ui tests from tests/
      harness.cjs                        # ONE shared harness: chromium launch/reuse, ui.py HTML extraction,
                                         # route stubs, assertEventually, moduleOf; today 20 files re-extract
                                         # ui.py HTML, 22 launch Chrome (39 launch calls), 23 hard-code the Chrome path
      *.test.cjs
  .test-build/                           # gitignored output of build.mjs (NOT under node_modules:
                                         # macos/build-app.sh runs `npm ci`, which wipes node_modules)
```

### 3.2 package.json scripts

```json
"scripts": {
  "test": "node tests/run.mjs unit",
  "test:unit": "node tests/run.mjs unit",
  "test:browser": "node tests/run.mjs browser",
  "test:all": "node tests/run.mjs unit browser",
  "test:full": "PHAROS_TEST_FULL=1 node tests/run.mjs unit",
  "generate-recipes": "node generate-recipe-snapshot.mjs",
  "check-recipes": "node generate-recipe-snapshot.mjs --check",
  "check": "npm run typecheck && npm run check-schemas && npm run check-recipes && npm run check-bundle && npm test",

  "test:usage-aggregations": "node tests/run.mjs unit usage-aggregations",
  "test:computed-store": "node tests/run.mjs unit computed-column-store",
  "test:previews": "node tests/run.mjs unit query-previews",
  "test:text-search": "node tests/run.mjs unit text-search-model",
  "test:search-composer": "node tests/run.mjs unit search-composer"
},
"devDependencies": { "playwright-core": "<pin>" }
```

The last five scripts are kept as aliases because docs reference them.

Using `playwright-core` means no browser download. The tests already prefer `/Applications/Google Chrome.app`. Declaring it removes the per-workspace `.context/browser-tests` install, and the browser harness should fall back to it or skip with a clear install hint.

Agent-facing commands:
- From the repo root: `npm --prefix web test`, the default fast slate.
- `npm --prefix web test -- card-recipes --test-name-pattern=wishlist` for one file or test.
- `npm --prefix web run test:browser -- home-ui` for one browser file.
- Go stays `go test ./...`. Python is `python3 -m unittest discover -s tools` (see §4).

`run.mjs` should put `--test-*` flags **before** the file list (Node 20 treats trailing args as files). It should default to `--test-reporter=spec`. With non-TTY stdout Node 20 defaults to TAP, which is very verbose for agents; spec ends with a "failing tests" recap. It should exit non-zero on failure.

### 3.3 One shared esbuild step (recommended over per-file buildSync)

```js
// web/tests/build.mjs
import { build } from "esbuild"; import { readdirSync } from "node:fs";
const entries = readdirSync("tests/unit", { recursive: true }).filter(f => /\.test\.m?[jt]sx?$/.test(f)).map(f => `tests/unit/${f}`);
await build({ entryPoints: entries, outdir: ".test-build/unit", outbase: "tests/unit", outExtension: { ".js": ".mjs" },
  bundle: true, splitting: true, format: "esm", platform: "node", target: "node20",
  packages: "external",              // query-table-core/react resolve from web/node_modules once per process,
                                     // and tests and src share ONE core instance
  loader: { ".css": "empty" }, sourcemap: "inline", logLevel: "warning" });
```

Tests import TS directly (`import { queryRecipes } from "../../../src/curiosity-data"`) and import schemas as JSON (`import usage from "../../../../schemas/usage.schema.json"`, inlined at build time). That removes all cwd dependence, the data-URL importer and the duplicate core.

Measured cost (single runs, warm disk, 28-core Mac; only these two bundles were built):

| bundle | output | build time | import time |
|---|---|---|---|
| `scatter-axis.ts` alone | 1.34 MB | 146 ms | — |
| `curiosity-data.ts` alone | 1.98 MB | 208 ms | 119 ms (as a data URL) |
| api-cost 7-entry build, today | 2.05 MB total | 206 ms | — |
| same 7 entries, `splitting` + `packages: "external"` | 327 KB | **38 ms** | — |

Projection: today about 47 build calls (38 in web/tests, 9 in tests/*.cjs) across 35 processes, each also spawning the esbuild service and parsing 1.3–2 MB per output, comes to roughly **12–18 s CPU per full unit run** (≈1–2 s wall at 27-way concurrency). One shared build should take about 0.3–0.6 s in total, plus about 35 × ~60 ms of node startup. Running the browser and Go suites in parallel will reduce contention noticeably. The **big wall-clock wins are structural**: browser tests out of the default slate, ~40% fewer web/tests lines, and possibly a reduced default card-parity matrix.

Node 20 runs each file in its own process; there is no `--test-isolation=none` before 22.8. Fewer, larger files (the recipes consolidation) also cut per-process overhead.

**Alternative, no build directory:** `node --import ./tests/register.mjs --test tests/unit/`, where `module.register()` hooks resolve extensionless/`.tsx` specifiers and transpile with `esbuild.transform` (CSS → empty module, JSON → default export). This works on Node 20.19. Each process then transpiles only what it imports, and single-file runs need no build. The trade-off is that the module graph is unbundled, so it differs slightly from production bundling.

**Upgrading Node** (20 is EOL) is recommended separately. As §0.8 notes, strip-types alone would not replace either option.

### 3.4 Snapshot generator

Add `web/generate-recipe-snapshot.mjs`. It bundles `src/curiosity-data.ts`, writes `testdata/query-recipes.json` (or the `implemented_recipes` key of the docs JSON), and supports `--check`. Point `internal/archive/home_query_recipes_test.go` at it. Delete query-previews #1's deep-equal. A recipe edit then becomes `npm run generate-recipes`, not a hand-sync of a 636 KB doc.

## 4. tools/

| file | status | tests | recommendation |
|---|---|---|---|
| `test_dev.py` (178 lines, about 6 tests) + `dev.py` | Live. `tools/dev.sh` → `dev.py` snapshots a ~65 GB production catalog into a dev copy. | Safety checks (destination validation, isolation, restart reuse). One APFS-clone integration test (`darwin` only, temporary directory, `cp -c`). Build/launch steps are mocked. Cheap (seconds). | **Keep.** It guards a destructive script. Documented in technical-overview as `python3 -m unittest discover -s tools -p 'test_dev.py'`. |
| `test_concurrency_prototype.py` (45 lines) + `concurrency-prototype.py` | Live dev prototype. It feeds `pharos dev-ui --concurrency-data`, which is served at `/api/usage/concurrency-prototype` and consumed by `web/src/concurrency-store.ts`; `devui_test.go` covers the endpoint. | 2–3 pure timing tests. Cheap. Not referenced anywhere. | Keep while the prototype exists; delete together with it. |
| `test_backfill_codex_models.py` (57 lines) + `backfill_codex_models.py` | **Superseded.** It is a one-off catalog migration (Sep 25, updated in #104). `docs/token-accounting.md` now says "Existing catalogs migrate automatically … retained source captures are reparsed", and the Go ingester handles `turn_context` effort. No doc or script references the tool. | 1 test. | **Delete both** after confirming no catalog still needs the script. |
| `query-bench.py` | Manual perf tool against a live service. Documented in `docs/query-performance.md` and `query-table-integration.md`. | none | Keep as a manual benchmark (optionally `tools/bench/`). It is not a test. |
| `usage-bench.cjs` | Added today (#140). Drives the real Usage UI against a dev service with the `.context` Playwright install. Documented in `docs/performance/usage.md`. | none | Keep as a manual benchmark. Switch it to the declared `playwright-core`. |

One command for all Python tests: `python3 -m unittest discover -s tools`. That runs 14 test methods in about 1–2 s, and `tools/` must be on `sys.path`, which `-s tools` provides. Only `test_dev.py` is documented today.

## 5. macos/test/*

| file | what it does | how it runs | cost and requirements | refs | recommendation |
|---|---|---|---|---|---|
| `swift-harness.sh` + `Harness.swift` (321 lines) | DiskArbitration handling and the Eject button (release, stop, eject) with an MCP server running from the drive. It also tests the service release using a fake 503 Python server. | Manual: `macos/test/swift-harness.sh [PORT]` | **Heavy.** It runs `macos/build-app.sh`, which does `npm ci` in web (**wiping web/node_modules mid-run for any concurrent JS tests**), npm build, a Go build and the Swift app. It then compiles the harness with `swiftc`, creates and attaches a 300 MB APFS DMG, and force-detaches it. Several minutes; macOS only; mounts `/Volumes/PharosTest-*`. | docs/configuration.md §Tests | Manual/opt-in only; never part of a default slate. Agents should run it only when touching `LibraryVolume.swift`, `Launch.swift` or eject flows. |
| `eject-release.sh` | Service-side release during sync, a forced detach while syncing, and catalog integrity afterwards. | Manual | `go build`, sparse image, `hdiutil`/`diskutil`, runs the service. About 1–2 min. | docs/configuration.md | Manual/opt-in. |
| `adopt-library.sh` | Adoption of a legacy per-user install (schema 4) into a drive library. | Manual `[LEGACY_REF] [PORT]` | Default `REF=euclid/baseline` **does not exist** in this repo, and the script builds `./cmd/alexandria`, which exists only in pre-rename commits (≤ e8cd2f2). Its default invocation is broken. | none | Archive or delete. It is a one-time migration test for a pre-release layout. Otherwise fix the default ref and document it as manual. |
| `menu-bar.sh` + `MenuBarHarness.swift` | Compiles `MenuBar.swift` with a stub service and checks menu order, visibility, status, reopen and eject actions. | Manual `macos/test/menu-bar.sh [out]` | One `swiftc` compile (AppKit + WebKit, roughly 10–30 s); creates NSApplication; no volumes. | none | Keep as opt-in. Last touched 10-13, but `MenuBar.swift` changed on 10-15 (#124, #127), so it may be stale (unverified). Add it to a `macos/test/run-light.sh` together with UpdateCheck. |
| `UpdateCheckHarness.swift` | Update checker against stubbed GitHub release responses. | Only via the comment header (`swiftc … && /tmp/pharos-update-check`); there is no script. | One `swiftc` compile; no volumes or network. | none | Opt-in. Give it a script, or add it to `run-light.sh`. |

None of these are referenced by any automated runner, which is the correct choice. A short docs section should list them as "manual, macOS-only, run when touching X".

## 6. Test documentation: stale and scattered

There is **no canonical "how to test" document**, and no CLAUDE.md or AGENTS.md. Guidance is spread over 9+ docs:

| where | what it says | problem |
|---|---|---|
| `docs/technical-overview.md` §Development and verification (L344–357) | `go test ./...`, `go vet`, web `npm ci` + tsc + build, `check-bundle`, `./launch.sh --no-open`, **`PYTHONPATH=src python3 -m unittest discover -s tests -v`**, **`python3 -m compileall -q src`** | `src/` never existed in this repo. On Python 3.14 the first command finds 0 tests and exits 5; compileall fails. `launch.sh` is a full app build that touches `~/Library/Application Support/Pharos`. **No JS tests are mentioned.** Its claim that the "Python compatibility suite continues to cover the mothballed reference" is stale. It is closest to canonical (README links to it for development). |
| same file, L413 | `python3 -m unittest discover -s tools -p 'test_dev.py'` | Correct, but covers only one of the 3 Python test files. |
| `docs/query-table-local-development.md` L13–22, L72–90 | The only `(cd web && node --test tests/*.test.mjs)` in the docs; Playwright install into `.context/browser-tests`; 3 named UI tests | Says several browser fixtures fail at baseline. |
| `docs/query-table-integration.md` L66–73, L199–206 | `npm run test:computed-store` (one file) as "the" web test; Go race and bench commands | Under-tests. Agents following it run 1 of 27 web files. |
| `docs/incremental-sync.md` L195–213 | `go test ./...`, gated benches, `node --test tests/incremental-sync-ui.test.cjs tests/combo-button-ui.test.cjs` | Feature-specific; fine. |
| `docs/performance/text-search.md` L121–134 | targeted `go test -run …`, `npm run test:text-search`; notes `TestFindingSnoozeUntilWorseStaysWoken` failing at baseline | Feature-specific. |
| `docs/performance/usage.md` L143–160, `docs/query-performance.md` L110–116 | Playwright install, perf server test, bench tools | Benchmarks, not tests. |
| `docs/configuration.md` L502 | macOS harness scripts | OK; should state "manual". |
| `docs/iconography.md` L37 | `go test ./...` | OK. |
| `docs/transcript-reader-design.md` L126, `docs/token-accounting.md` L32 | Describe UI and fixture tests; token-accounting still mentions a "Python importer" running the shared fixtures | The Python importer no longer exists in the repo, so that line is stale. |

Recommendation: create one root `AGENTS.md`, symlinked as `CLAUDE.md`, or a "Testing" section in `docs/technical-overview.md`. It should state:

1. **Default:** `npm --prefix web test` (unit, under 10 s), `go test ./...`, `python3 -m unittest discover -s tools`.
2. **When touching UI pages** (`ui.py`/`library.js`/…): `npm --prefix web run test:browser -- <file>`, scoped to the relevant files, never the whole slate by default.
3. **Before commit:** `npm --prefix web run check`.
4. **Manual only:** `macos/test/*`, `tools/*bench*`.
5. A list of known-failing or quarantined tests, so a red run means something.

Then delete the stale Python lines and point the other docs at it.

## 7. Growth over time

JS test files at week end (`tests/*-ui.test.cjs` browser, `tests/*.test.cjs` unit, `web/tests/*.mjs`), with `*_test.go` for context:

| week ending | commit | browser-ui files/lines | tests/ unit files/lines | web/tests files/lines | JS `test(` sites | Go `_test.go` files/lines |
|---|---|---|---|---|---:|---|
| 2026-09-27 (W39) | 29991ec | 9 / 2,398 | 0 / 0 | 0 / 0 | 79 | 52 / 14,725 |
| 2026-10-04 (W40) | 8c6036f | 15 / 4,198 | 4 / 344 | 0 / 0 | 133 | 81 / 22,428 |
| 2026-10-11 (W41) | d797511 | 18 / 5,559 | 8 / 611 | 3 / 320 | 196 | 92 / 27,218 |
| 2026-10-18 (W42) | 293d511 | 22 / 9,725 | 8 / 611 | 27 / 4,111 | 558 | 117 / 34,240 |
| HEAD (10-19) | bb496be | 23 / 9,807 | 8 / 611 | 27 / 4,178 | 563 | 120 / 34,905 |

JS test lines added and removed per ISO week (git numstat over `tests/*.cjs` and `web/tests/*.mjs`):

| week | lines added | lines removed | new files |
|---|---:|---:|---:|
| W39 | +2,477 | −79 | 9 |
| W40 | +2,331 | −187 | 10 |
| W41 | +2,084 | −136 | 10 |
| W42 | **+8,923** | −966 | **28** |
| W43 (1 day) | +156 | −7 | 1 |

Largest single contributors:

| PR | commit | date | test lines | what it added |
|---|---|---|---:|---|
| #128 | 8cae8ad | 10-16 | +2,773 | the 12 review files + human-study |
| initial import | e8cd2f2 | — | +2,084 | |
| #119 | 95862d4 | 10-14 | +1,986 | card-recipes, card-presentation, card-loading, usage-queries, search-editor-ui, select-ui |
| #112 | 5c1a416 | 10-13 | +1,314 | delegation-ui |
| #99 | f2a2f65 | 10-08 | +704 | home-ui |

**In W42 alone, JS test sites nearly tripled (196 → 558) and JS test lines went from 6.5k to 14.4k.** That matches the reported jump in agent test time (avg `node --test` 44 s → 93 s). Most of the new *wall time* is likely browser files (home-ui 1,783 lines / 45 tests, delegation-ui 1,389, transcript-ui 1,366), which the other auditors cover. Most of the new *line count* in fast suites is the review-file family above.

## 8. Notes for the browser and Go auditors

- Probable unit/browser overlaps to check: `usage-table-colors-ui` vs `usage-chart-colors`, `usage-presets-ui` vs `usage-queries`, `search-editor-ui` vs `search-composer`/`text-search-model`, `machine-activity-ui` vs `machine-activity`, and `home-ui` card-library flows vs `card-parity`/`card-recipes`.
- `home-ui.test.cjs` also bundles 6 modules with its own `moduleOf` (CJS-fn). The shared browser harness should reuse the unit build output.
- Go `TestHomeQueryRecipesV2` reads the same 636 KB docs JSON; see §3.4.
